import ast
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from types import ModuleType, SimpleNamespace

import pytest
import requests

from hcu.osworld.assets import config_asset_sources, task_asset_sources
from hcu.osworld.preflight import validate_official_asset_runtime
from hcu.osworld.prepare_assets import download_asset, prepare_task_assets


ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def source_module(monkeypatch):
    # Load the actual overlay without importing the optional VM dependency tree.
    package = ModuleType("desktop_env")
    package.__path__ = [str(ROOT / "osworld-overlay/desktop_env")]
    monkeypatch.setitem(sys.modules, "desktop_env", package)
    spec = importlib.util.spec_from_file_location(
        "desktop_env.file_source", ROOT / "osworld-overlay/desktop_env/file_source.py"
    )
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, module)
    spec.loader.exec_module(module)
    monkeypatch.delenv("OSWORLD_FILE_BASE_URL", raising=False)
    monkeypatch.delenv("OSWORLD_V1_ASSET_ROOT", raising=False)
    monkeypatch.setattr(requests, "get", lambda *a, **kw: pytest.fail("unexpected network retrieval"))
    return module


def task(source=None, task_id="selected"):
    return {
        "id": task_id,
        "config": [] if source is None else [
            {"type": "download", "parameters": {"files": [{"url": source, "path": "/tmp/file"}]}}
        ],
    }


def manifest(root, *, sources=None, tasks=None):
    root.mkdir(parents=True, exist_ok=True)
    (root / "setup-assets.json").write_text(json.dumps({
        "version": 1, "sources": sources or {}, "tasks": tasks or {},
    }))


def python_task(tmp_path, monkeypatch, code):
    path = tmp_path / "selected_task.py"
    path.write_text(code)
    spec = importlib.util.spec_from_file_location("selected_test_task", path)
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, module)
    spec.loader.exec_module(module)
    return module.Task()


def test_v1_without_assets_needs_no_v2_snapshot(source_module, monkeypatch):
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", "https://unused.invalid/v2")
    report = validate_official_asset_runtime("v1", task())
    assert report["assets"] == []
    assert report["asset_root"] is None


def test_v1_checks_actual_files_not_a_v2_directory(source_module, tmp_path, monkeypatch):
    root = tmp_path / "v1"
    file = root / "calc/uuid/input file.xlsx"
    file.parent.mkdir(parents=True)
    file.write_bytes(b"workbook")
    monkeypatch.setenv("OSWORLD_V1_ASSET_ROOT", str(root))
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", "https://unused.invalid/v2")
    url = (
        "https://huggingface.co/datasets/xlangai/ubuntu_osworld_file_cache/"
        "resolve/main/calc/uuid/input%20file.xlsx?download=true"
    )
    report = validate_official_asset_runtime("v1", task(url))
    assert report["assets"][0]["local_path"] == str(file)
    assert report["assets"][0]["size_bytes"] == 8
    file.unlink()
    with pytest.raises(FileNotFoundError, match="network fallback is disabled"):
        validate_official_asset_runtime("v1", task(url))


def test_other_benchmark_manifest_does_not_affect_selected_sources(source_module, tmp_path, monkeypatch):
    v1, v2 = tmp_path / "v1", tmp_path / "v2"
    manifest(v1, sources={"https://assets.test/file": "file"})
    (v1 / "file").write_text("selected")
    v2.mkdir()
    (v2 / "setup-assets.json").write_text("invalid unrelated manifest")
    monkeypatch.setenv("OSWORLD_V1_ASSET_ROOT", str(v1))
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", str(v2))
    report = validate_official_asset_runtime("v1", task("https://assets.test/file"))
    assert report["assets"][0]["local_path"] == str(v1 / "file")


def test_v2_only_validates_selected_task_files(source_module, tmp_path, monkeypatch):
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", str(tmp_path))
    path = tmp_path / "task_077/input.txt"
    path.parent.mkdir()
    path.write_text("input")
    selected = python_task(tmp_path, monkeypatch, '''
from desktop_env.file_source import asset as source
TASK_ID = "077"
INPUT = source(f"task_{TASK_ID}/input.txt")
class Task(dict):
    def __init__(self):
        super().__init__(id="077", config=[])
    def setup(self, controller):
        controller.download([{"url": INPUT, "path": "/tmp/input"}])
''')
    report = validate_official_asset_runtime("v2", selected)
    assert len(report["assets"]) == 1
    assert not (tmp_path / "task_001").exists()
    path.unlink()
    with pytest.raises(FileNotFoundError):
        validate_official_asset_runtime("v2", selected)


def test_directory_and_dynamic_v2_assets_require_inventory(source_module, tmp_path, monkeypatch):
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", str(tmp_path))
    directory = tmp_path / "task_005"
    directory.mkdir()
    first = directory / "a.txt"
    first.write_text("a")
    selected = python_task(tmp_path, monkeypatch, '''
from desktop_env.file_source import asset
BASE = asset("task_005")
class Task(dict):
    def __init__(self):
        super().__init__(id="005", config=[])
    def setup(self, controller, name):
        controller.download([{"url": asset(name), "path": "/tmp/input"}])
''')
    with pytest.raises(RuntimeError, match="dynamic setup assets"):
        validate_official_asset_runtime("v2", selected)
    manifest(tmp_path, tasks={"v2:005": ["task_005/a.txt"]})
    report = validate_official_asset_runtime("v2", selected)
    assert report["manifest_task_entry"]
    assert source_module.require_setup_source(str(first)) == str(first)
    other = directory / "unlisted.txt"
    other.write_text("not admitted")
    with pytest.raises(ValueError, match="Undeclared"):
        source_module.require_setup_source(str(other))
    first.unlink()
    with pytest.raises(FileNotFoundError):
        validate_official_asset_runtime("v2", selected)


def test_plain_v2_directory_is_not_completeness(source_module, tmp_path, monkeypatch):
    monkeypatch.setenv("OSWORLD_FILE_BASE_URL", str(tmp_path))
    (tmp_path / "task_005").mkdir()
    selected = python_task(tmp_path, monkeypatch, '''
from desktop_env.file_source import asset
BASE = asset("task_005")
class Task(dict):
    def __init__(self):
        super().__init__(id="005", config=[])
''')
    with pytest.raises(RuntimeError, match="directory existence is not completeness"):
        validate_official_asset_runtime("v2", selected)


def test_web_navigation_urls_are_not_setup_assets(source_module):
    config = [
        {"type": "chrome_open_tabs", "parameters": {"urls": ["https://example.com"]}},
        {"type": "execute", "parameters": {"command": ["curl", "http://website/api/health"]}},
        {"type": "update_browse_history", "parameters": {"history": [{"url": "https://example.com"}]}},
        {"type": "googledrive", "parameters": {
            "operation": ["delete", "upload"],
            "args": [{"query": "x"}, {"url": "https://assets.test/file", "path": ["file"]}],
        }},
    ]
    assert config_asset_sources(config) == [source_module.HISTORY_DB_URL, "https://assets.test/file"]


@pytest.mark.parametrize("source", ["../escape", "/outside", "https://remote.test/base"])
def test_invalid_local_root_is_rejected(source_module, monkeypatch, source):
    monkeypatch.setenv("OSWORLD_V1_ASSET_ROOT", source)
    if source == "/outside":
        # Absolute missing roots are harmless for a task needing no assets.
        assert validate_official_asset_runtime("v1", task())["assets"] == []
    else:
        with pytest.raises(ValueError, match="local directory"):
            validate_official_asset_runtime("v1", task())


def test_manifest_cannot_escape_root(source_module, tmp_path, monkeypatch):
    monkeypatch.setenv("OSWORLD_V1_ASSET_ROOT", str(tmp_path))
    manifest(tmp_path, sources={"https://assets.test/file": "../outside"})
    with pytest.raises(ValueError, match="escapes"):
        validate_official_asset_runtime("v1", task("https://assets.test/file"))
    manifest(tmp_path, tasks={"v1:selected": ["../outside"]})
    with pytest.raises(ValueError, match="escapes"):
        validate_official_asset_runtime("v1", task())


def test_local_file_uri_and_percent_encoded_name(source_module, tmp_path):
    path = tmp_path / "a b.txt"
    path.write_text("content")
    assert source_module.require_setup_source(path.as_uri()) == str(path)
    assert source_module.require_setup_source(str(path).replace(" ", "%20")) == str(path)
    with pytest.raises(ValueError, match="must be local"):
        source_module.resolve_local_source("file://remote/path")


def controller_method(name, source_module, **extra):
    tree = ast.parse((ROOT / "osworld-overlay/desktop_env/controllers/setup.py").read_text())
    controller = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "SetupController")
    method = next(node for node in controller.body if isinstance(node, ast.FunctionDef) and node.name == name)
    code = ast.Module(body=[ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0), method], type_ignores=[])
    ast.fix_missing_locations(code)
    namespace = {
        "require_setup_source": source_module.require_setup_source,
        "HISTORY_DB_URL": source_module.HISTORY_DB_URL,
        **extra,
    }
    exec(compile(code, str(ROOT / "osworld-overlay/desktop_env/controllers/setup.py"), "exec"), namespace)
    return namespace[name]


def test_setup_download_validates_whole_batch_before_upload(source_module, tmp_path):
    file = tmp_path / "input"
    file.write_bytes(b"new bytes")
    uploaded = []
    controller = SimpleNamespace(_upload_file_setup=uploaded.extend)
    download = controller_method("_download_setup", source_module)
    files = [{"url": str(file), "path": "/tmp/output"}, {"url": "https://missing/file", "path": "/tmp/missing"}]
    with pytest.raises(FileNotFoundError):
        download(controller, files)
    assert uploaded == []
    download(controller, files[:1])
    assert uploaded == [{"local_path": str(file), "path": "/tmp/output"}]


def test_history_cache_never_bypasses_missing_source(source_module, tmp_path):
    import os

    (tmp_path / "history_new.sqlite").write_bytes(b"stale cached body")
    history = controller_method("_update_browse_history_setup", source_module, os=os)
    with pytest.raises(FileNotFoundError):
        history(SimpleNamespace(cache_dir=str(tmp_path)), history=[])


def test_cloud_upload_sources_checked_before_auth_or_mutation(source_module):
    authenticate = lambda *args, **kwargs: pytest.fail("authentication before asset preflight")
    upload = controller_method("_googledrive_setup", source_module, GoogleAuth=authenticate)
    with pytest.raises(FileNotFoundError):
        upload(SimpleNamespace(), operation=["delete", "upload"], args=[
            {"query": "existing files"}, {"url": "https://missing/file", "path": ["file"]},
        ])


def test_prepare_requires_explicit_download(source_module, tmp_path, monkeypatch):
    selected = task("https://assets.test/a")
    with pytest.raises(FileNotFoundError, match="--download"):
        prepare_task_assets(selected, "v1", tmp_path, download=False)
    assert not (tmp_path / "setup-assets.json").exists()
    calls = []

    def fetch(source, destination):
        calls.append(source)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(b"prepared")

    monkeypatch.setattr("hcu.osworld.prepare_assets.download_asset", fetch)
    report = prepare_task_assets(selected, "v1", tmp_path, download=True)
    assert calls == ["https://assets.test/a"]
    assert report["assets"][0]["size_bytes"] == 8
    report = prepare_task_assets(selected, "v1", tmp_path, download=False)
    assert calls == ["https://assets.test/a"]
    assert report["task_id"] == "selected"
    second = task("https://assets.test/b", task_id="second")
    prepare_task_assets(second, "v1", tmp_path, download=True)
    inventory = json.loads((tmp_path / "setup-assets.json").read_text())
    assert set(inventory["tasks"]) == {"v1:selected", "v1:second"}
    assert len(set(inventory["sources"].values())) == 2


def test_prepare_includes_evaluator_postconfig_and_binds_downloads(
    source_module, tmp_path, monkeypatch,
):
    selected = task("https://assets.test/input")
    postconfig = [
        {"type": "download", "parameters": {"files": [
            {"url": "https://assets.test/check.py", "path": "/tmp/check.py"},
        ]}},
        {"type": "update_browse_history", "parameters": {"history": []}},
    ]
    selected["evaluator"] = {"postconfig": postconfig}
    expected = [
        "https://assets.test/input", "https://assets.test/check.py",
        source_module.HISTORY_DB_URL,
    ]
    calls = []

    def fetch(source, destination):
        calls.append(source)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(b"prepared")

    monkeypatch.setattr("hcu.osworld.prepare_assets.download_asset", fetch)
    prepare_task_assets(selected, "v1", tmp_path, download=True)
    assert calls == expected
    assert set(json.loads((tmp_path / "setup-assets.json").read_text())["tasks"]["v1:selected"]) == set(expected)
    report = validate_official_asset_runtime("v1", selected)
    assert {item["source"] for item in report["assets"]} == set(expected)
    uploaded = []
    download = controller_method("_download_setup", source_module)
    download(SimpleNamespace(_upload_file_setup=uploaded.extend), postconfig[0]["parameters"]["files"])
    assert Path(uploaded[0]["local_path"]).read_bytes() == b"prepared"
    assert uploaded[0]["path"] == "/tmp/check.py"
    with pytest.raises(ValueError, match="Undeclared"):
        source_module.require_setup_source("https://assets.test/unrelated")
    Path(uploaded[0]["local_path"]).unlink()
    with pytest.raises(FileNotFoundError):
        validate_official_asset_runtime("v1", selected)


@pytest.mark.parametrize(("source_name", "local_name"), [
    ("input file.xlsx", "input file.xlsx"),
    ("input%20file.xlsx", "input file.xlsx"),
    ("input%20file.xlsx", "input%20file.xlsx"),
])
def test_prepare_checkout_relative_files_keep_their_origin(
    source_module, tmp_path, monkeypatch, source_name, local_name,
):
    checkout, root = tmp_path / "checkout", tmp_path / "assets"
    file = checkout / "evaluation_examples" / local_name
    file.parent.mkdir(parents=True)
    file.write_bytes(b"checkout data")
    if "%20" in local_name:
        file.with_name(local_name.replace("%20", " ")).write_bytes(b"wrong decoded file")
    # A same-named file under the asset root must never replace the task input.
    decoy = root / "evaluation_examples" / local_name
    decoy.parent.mkdir(parents=True)
    decoy.write_bytes(b"wrong root")
    source = f"evaluation_examples/{source_name}"
    selected = task(source)
    selected["config"].append({"type": "upload_file", "parameters": {"files": [
        {"local_path": source, "path": "/tmp/upload.xlsx"},
    ]}})
    monkeypatch.chdir(checkout)
    for _ in range(2):
        report = prepare_task_assets(selected, "v1", root, download=False)
        assert {Path(item["local_path"]) for item in report["assets"]} == {file}
        assert source_module.require_setup_source(source) == str(file)
    stored = json.loads((root / "setup-assets.json").read_text())["tasks"]["v1:selected"]
    assert stored == [str(file)]
    # Runtime bindings must keep the admitted file even if the caller changes cwd.
    monkeypatch.chdir(root)
    assert Path(source_module.require_setup_source(source)).read_bytes() == b"checkout data"
    monkeypatch.chdir(checkout)
    if "%20" in local_name:
        file.with_name(local_name.replace("%20", " ")).unlink()
    file.unlink()
    with pytest.raises(FileNotFoundError):
        validate_official_asset_runtime("v1", selected)


def test_explicit_manifest_relative_files_remain_asset_root_relative(
    source_module, tmp_path, monkeypatch,
):
    checkout, root = tmp_path / "checkout", tmp_path / "assets"
    checkout.mkdir()
    root.mkdir()
    (checkout / "input").write_bytes(b"wrong origin")
    (root / "input").write_bytes(b"asset data")
    monkeypatch.chdir(checkout)
    report = prepare_task_assets(
        task(), "v1", root, download=False, sources=["input"],
    )
    assert report["assets"][0]["local_path"] == str(root / "input")
    assert source_module.require_setup_source(str(root / "input")) == str(root / "input")
    with pytest.raises(ValueError, match="Undeclared"):
        source_module.require_setup_source("input")


def test_prepare_resumes_completed_files_after_later_failure(source_module, tmp_path, monkeypatch):
    selected = task("https://assets.test/first")
    selected["config"][0]["parameters"]["files"].append({
        "url": "https://assets.test/second", "path": "/tmp/second",
    })
    calls = []
    fail = True

    def fetch(source, destination):
        calls.append(source)
        if fail and source.endswith("second"):
            raise requests.ConnectionError("transfer interrupted")
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(b"complete")

    monkeypatch.setattr("hcu.osworld.prepare_assets.download_asset", fetch)
    with pytest.raises(requests.ConnectionError):
        prepare_task_assets(selected, "v1", tmp_path, download=True)
    fail = False
    prepare_task_assets(selected, "v1", tmp_path, download=True)
    assert calls == [
        "https://assets.test/first", "https://assets.test/second", "https://assets.test/second",
    ]


@pytest.mark.parametrize("failure", ["length", "stream", "status", None])
def test_download_publishes_only_complete_body(tmp_path, monkeypatch, failure):
    class Response:
        headers = {"Content-Length": "100" if failure == "length" else "6"}

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

        def raise_for_status(self):
            if failure == "status":
                raise requests.HTTPError("503")

        def iter_content(self, chunk_size):
            yield b"abc"
            if failure == "stream":
                raise requests.ConnectionError("interrupted")
            yield b"def"

    calls = []

    def get(url, **kwargs):
        calls.append(kwargs)
        return Response()

    monkeypatch.setattr(requests, "get", get)
    destination = tmp_path / "asset"
    destination.write_bytes(b"previous")
    if failure:
        with pytest.raises((ValueError, requests.RequestException)):
            download_asset("https://assets.test/file", destination)
        assert destination.read_bytes() == b"previous"
    else:
        download_asset("https://assets.test/file", destination)
        assert destination.read_bytes() == b"abcdef"
    assert calls == [{"stream": True, "timeout": (15, 120)}]
    assert list(tmp_path.iterdir()) == [destination]


@pytest.mark.parametrize("failure", ["missing-asset", "bad-inventory"])
def test_prepare_cli_v1_from_a_clean_checkout(tmp_path, failure):
    checkout = tmp_path / "upstream"
    package = checkout / "desktop_env"
    package.mkdir(parents=True)
    (package / "__init__.py").write_text("")
    (package / "task_base.py").write_text("class BaseTask(dict): pass\n")
    shutil.copyfile(ROOT / "osworld-overlay/desktop_env/file_source.py", package / "file_source.py")
    # A colliding V2 class must never be imported when preparing a V1 task.
    classes = checkout / "evaluation_examples/task_class"
    classes.mkdir(parents=True)
    (classes / "task_selected.py").write_text('raise RuntimeError("wrong benchmark task")\n')
    examples = checkout / "evaluation_examples/examples/calc"
    examples.mkdir(parents=True)
    file = tmp_path / "local-asset"
    file.write_text("asset")
    (examples / "selected.json").write_text(json.dumps(task(str(file))))
    env = dict(os.environ, OSWORLD_V2_ROOT=str(checkout), PYTHONPATH=str(ROOT))
    env.pop("OSWORLD_FILE_BASE_URL", None)
    command = [
        sys.executable, "-m", "hcu.osworld.prepare_assets",
        "--benchmark-version", "v1", "--task-id", "selected",
        "--asset-root", str(tmp_path / "assets"),
    ]
    result = subprocess.run(
        command, env=env, cwd=tmp_path, text=True, capture_output=True, timeout=30,
    )
    assert result.returncode == 0, result.stderr
    report = json.loads(result.stdout)
    assert report["benchmark_version"] == "v1"
    assert report["assets"][0]["local_path"] == str(file)
    if failure == "missing-asset":
        file.unlink()
        expected = str(file)
    else:
        inventory = tmp_path / "invalid.json"
        inventory.write_text("{")
        command.extend(["--sources-file", str(inventory)])
        expected = "Expecting property name"
    failed = subprocess.run(
        command, env=env, cwd=tmp_path, text=True, capture_output=True, timeout=30,
    )
    assert failed.returncode == 1
    assert expected in failed.stderr
    assert "error:" in failed.stderr
    assert "Traceback" not in failed.stderr
    assert failed.stdout == ""


def test_prepare_cli_keeps_unexpected_programming_errors_visible(monkeypatch):
    import hcu.osworld.prepare_assets as preparation

    def unexpected(*args):
        raise RuntimeError("unexpected internal failure")

    monkeypatch.setattr(preparation, "_prepare", unexpected)
    with pytest.raises(RuntimeError, match="unexpected internal failure"):
        preparation.main([
            "--benchmark-version", "v1", "--task-id", "selected", "--asset-root", "/unused",
        ])


def test_pinned_public_v1_configs_have_parseable_asset_dependencies(source_module):
    checkout = Path(os.environ.get("OSWORLD_V2_ROOT", ROOT / "external/OSWorld-V2"))
    examples = list((checkout / "evaluation_examples/examples").glob("*/*.json"))
    if not examples:
        pytest.skip("pinned upstream V1 examples are not installed")
    for path in examples:
        selected = json.loads(path.read_text())
        sources, unresolved = task_asset_sources(selected, "v1")
        assert all(isinstance(source, str) and source for source in sources), path
        assert unresolved == [], path


@pytest.mark.parametrize("version", ["v1", "v2"])
@pytest.mark.parametrize("failures", [False, True])
def test_prepare_config_cli_preserves_order_domains_and_reports_failures(tmp_path, version, failures):
    checkout = tmp_path / "upstream"
    package = checkout / "desktop_env"
    package.mkdir(parents=True)
    (package / "__init__.py").write_text("")
    (package / "task_base.py").write_text("class BaseTask(dict): pass\n")
    shutil.copyfile(ROOT / "osworld-overlay/desktop_env/file_source.py", package / "file_source.py")
    local = tmp_path / "local-asset"
    local.write_text("asset")
    for task_id, domain in (("last", "calc"), ("first", "chrome"), ("missing-asset", "calc")):
        directory = checkout / "evaluation_examples" / (
            f"examples/{domain}" if version == "v1" else f"examples_v2/{domain}"
        )
        directory.mkdir(parents=True, exist_ok=True)
        selected = task(
            "https://assets.invalid/not-prepared" if task_id == "missing-asset" else str(local),
            task_id=task_id,
        )
        (directory / f"{task_id}.json").write_text(json.dumps(selected))
    entries = ["last"]
    if failures:
        entries.extend(["missing-asset", "missing-task"])
    entries.append({"id": "first", "task_domain": "chrome"})
    config = tmp_path / "batch.json"
    config.write_text(json.dumps({
        "benchmark_version": version, "task_domain": "calc", "tasks": entries,
        # Asset preparation must not require unrelated gateway/image variables.
        "guest_image": "${UNSET_TEST_IMAGE}", "model": "unused",
    }))
    root = tmp_path / "assets"
    env = dict(os.environ, OSWORLD_V2_ROOT=str(checkout), PYTHONPATH=str(ROOT))
    env.pop("OSWORLD_FILE_BASE_URL", None)
    result = subprocess.run([
        sys.executable, "-m", "hcu.osworld.prepare_assets",
        "--config", str(config), "--asset-root", str(root),
    ], env=env, cwd=tmp_path, text=True, capture_output=True, timeout=30)
    assert result.returncode == (1 if failures else 0), result.stderr
    report = json.loads(result.stdout)
    expected = ["last", "missing-asset", "missing-task", "first"] if failures else ["last", "first"]
    assert report["task_count"] == len(entries)
    assert report["benchmark_version"] == version
    assert [entry["task_id"] for entry in report["tasks"]] == expected
    assert [entry["task_domain"] for entry in report["tasks"]] == ["calc"] * (len(entries) - 1) + ["chrome"]
    assert report["failed_task_ids"] == (["missing-asset", "missing-task"] if failures else [])
    assert report["tasks"][-1]["status"] == "prepared"
    inventory = json.loads((root / "setup-assets.json").read_text())
    assert set(inventory["tasks"]) == {f"{version}:last", f"{version}:first"}


def test_prepare_config_holds_one_lock_and_propagates_explicit_download(tmp_path, monkeypatch, capsys):
    import fcntl
    import hcu.osworld.prepare_assets as preparation

    config = tmp_path / "batch.json"
    config.write_text(json.dumps({"benchmark_version": "v1", "tasks": ["b", "a"]}))
    root = tmp_path / "assets"
    loader = ModuleType("hcu.osworld.task_loader")
    loader.load_task_config = lambda *args, **kwargs: task(task_id=kwargs["task_id"])
    monkeypatch.setitem(sys.modules, loader.__name__, loader)
    monkeypatch.setattr(preparation, "OSWORLD_ROOT", tmp_path)
    monkeypatch.chdir(tmp_path)
    calls = []

    def prepare(selected, version, asset_root, *, download, sources):
        with (root / ".setup-assets.lock").open("a") as contender:
            with pytest.raises(BlockingIOError):
                fcntl.flock(contender, fcntl.LOCK_EX | fcntl.LOCK_NB)
        calls.append((selected["id"], version, asset_root, download, sources))
        return {"task_id": selected["id"]}

    monkeypatch.setattr(preparation, "prepare_task_assets", prepare)
    assert preparation.main(["--config", str(config), "--asset-root", str(root), "--download"]) == 0
    assert calls == [("b", "v1", root, True, None), ("a", "v1", root, True, None)]
    assert json.loads(capsys.readouterr().out)["failed_task_ids"] == []
    with (root / ".setup-assets.lock").open("a") as contender:
        fcntl.flock(contender, fcntl.LOCK_EX | fcntl.LOCK_NB)


def test_prepare_config_cli_reuses_existing_v2_dynamic_inventory(tmp_path):
    checkout = tmp_path / "upstream"
    package = checkout / "desktop_env"
    package.mkdir(parents=True)
    (package / "__init__.py").write_text("")
    (package / "task_base.py").write_text("class BaseTask(dict): pass\n")
    shutil.copyfile(ROOT / "osworld-overlay/desktop_env/file_source.py", package / "file_source.py")
    classes = checkout / "evaluation_examples/task_class"
    classes.mkdir(parents=True)
    (classes / "task_dynamic.py").write_text('''
from desktop_env.file_source import asset
BASE = asset("task_dynamic")
class Task(dict):
    def __init__(self):
        super().__init__(id="dynamic", config=[])
    def setup(self, controller, name):
        raise AssertionError("resource preparation must not execute setup")
        controller.download([{"url": asset(name), "path": "/tmp/input"}])
''')
    root = tmp_path / "assets"
    (root / "task_dynamic").mkdir(parents=True)
    (root / "task_dynamic/input").write_text("prepared input")
    manifest(root, tasks={"v2:dynamic": ["task_dynamic/input"]})
    config = tmp_path / "batch.json"
    config.write_text(json.dumps({"benchmark_version": "v2", "tasks": ["dynamic"]}))
    result = subprocess.run([
        sys.executable, "-m", "hcu.osworld.prepare_assets",
        "--config", str(config), "--asset-root", str(root),
    ], env=dict(os.environ, OSWORLD_V2_ROOT=str(checkout), PYTHONPATH=str(ROOT)),
        cwd=tmp_path, text=True, capture_output=True, timeout=30)
    assert result.returncode == 0, result.stderr
    report = json.loads(result.stdout)["tasks"][0]
    assert report["manifest_task_entry"] is True
    assert report["dynamic_declarations"]
    assert report["assets"][0]["local_path"] == str(root / "task_dynamic/input")


@pytest.mark.parametrize("extra", [
    ["--task-id", "task"],
    ["--sources-file", "sources.json"],
    ["--benchmark-version", "v1"],
    ["--task-domain", "chrome"],
])
def test_prepare_config_rejects_conflicting_single_task_options(tmp_path, extra):
    result = subprocess.run([
        sys.executable, "-m", "hcu.osworld.prepare_assets",
        "--config", "not-read.json", "--asset-root", str(tmp_path / "assets"), *extra,
    ], cwd=ROOT, text=True, capture_output=True, timeout=30)
    assert result.returncode == 2
    assert "--config" in result.stderr
    assert not (tmp_path / "assets").exists()
