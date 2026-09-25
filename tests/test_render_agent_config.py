import json
import os
from pathlib import Path
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from hcu.osworld import agents
from hcu.osworld.guest import GUEST_CODEX_HOME


ROOT = Path(__file__).resolve().parents[1]


def render(tmp_path, agent, *, extra=(), env=None, python=sys.executable):
    base = tmp_path / "base instructions.md"
    skill = tmp_path / "selected skill.md"
    base.write_text("BASE_CONTEXT_MARKER\n", encoding="utf-8")
    skill.write_text('SELECTED_SKILL_MARKER\nUse "AX".\n', encoding="utf-8")
    output = tmp_path / "profile" / (
        "config.toml" if agent == "codex" else "mcp.json"
    )
    environment = dict(os.environ, PYTHONPATH=str(ROOT))
    environment.update(env or {})
    result = subprocess.run(
        [python, "-m", "hcu.render_agent_config", "--agent", agent,
         "--gateway", "http://127.0.0.1:1", "--mcp", python,
         "--base-instructions", base.name, "--skill", skill.name,
         "--output", str(output), *extra],
        cwd=tmp_path, env=environment, capture_output=True, text=True,
        timeout=15,
    )
    assert result.returncode == 0, result.stderr
    return output, base, skill


def read_toml(path):
    tomllib = pytest.importorskip("tomllib")
    return tomllib.loads(path.read_text(encoding="utf-8"))


@pytest.mark.parametrize("home_source", ["default", "environment", "argument"])
def test_codex_render_preserves_context_and_uses_local_home(tmp_path, home_source):
    local_home = tmp_path / "local user"
    env_home = tmp_path / "env codex"
    cli_home = tmp_path / "cli codex"
    project_context = tmp_path / "AGENTS.md"
    project_context.write_text("PROJECT_CONTEXT_MARKER", encoding="utf-8")
    env = {"HOME": str(local_home), "USERPROFILE": str(local_home),
           "CODEX_HOME": ""}
    expected_home = local_home / ".codex"
    extra = []
    if home_source != "default":
        env["CODEX_HOME"] = str(env_home)
        expected_home = env_home
    if home_source == "argument":
        extra = ["--codex-home", str(cli_home)]
        expected_home = cli_home
    expected_home.mkdir(parents=True)
    user_context = expected_home / "AGENTS.md"
    user_context.write_text("USER_CONTEXT_MARKER", encoding="utf-8")
    output, base, skill = render(tmp_path, "codex", extra=extra, env=env)
    config = read_toml(output)
    assert config["developer_instructions"] == skill.read_text(encoding="utf-8")
    assert Path(config["model_instructions_file"]).read_text() == base.read_text()
    assert Path(config["model_instructions_file"]).is_absolute()
    assert Path(config["mcp_servers"]["computer_use"]["command"]).is_absolute()
    assert config["skills"]["config"][0]["path"] == (
        f"{expected_home}/skills/.system/{agents.CODEX_BUNDLED_SYSTEM_SKILLS[0]}/SKILL.md"
    )
    assert project_context.read_text() == "PROJECT_CONTEXT_MARKER"
    assert user_context.read_text() == "USER_CONTEXT_MARKER"
    assert base.read_text() == "BASE_CONTEXT_MARKER\n"


def test_guest_builders_keep_original_injection_and_interpreter():
    tomllib = pytest.importorskip("tomllib")
    config = tomllib.loads(agents.codex_config(
        model="gpt-5.5", effort="xhigh", base_url="http://gateway/v1",
        codex_home=GUEST_CODEX_HOME, base_instructions="/guest/base.md",
        mcp_command="/guest/computer-use",
    ))
    assert "developer_instructions" not in config
    assert config["model_instructions_file"] == "/guest/base.md"
    server = json.loads(agents.claude_mcp_config())["mcpServers"]["computer_use"]
    assert server["command"] == "/usr/bin/python3"


@pytest.mark.parametrize("agent", ["codex", "claude-code"])
def test_standalone_expands_user_paths(tmp_path, agent):
    filename = "config.toml" if agent == "codex" else "mcp.json"
    render(
        tmp_path, agent,
        env={"HOME": str(tmp_path), "USERPROFILE": str(tmp_path)},
        extra=["--output", f"~/expanded/{filename}",
               "--base-instructions", "~/base instructions.md",
               "--skill", "~/selected skill.md", "--mcp", "~/computer-use"],
    )
    output = tmp_path / "expanded" / filename
    if agent == "codex":
        config = read_toml(output)
        assert config["model_instructions_file"] == str(tmp_path / "base instructions.md")
        server = config["mcp_servers"]["computer_use"]
        assert server["command"] == str(tmp_path / "computer-use")
    else:
        server = json.loads(output.read_text())["mcpServers"]["computer_use"]
        assert server["args"][1] == str(tmp_path / "computer-use")
        assert "SELECTED_SKILL_MARKER" in output.with_suffix(".system-prompt.md").read_text()


def test_codex_config_quotes_native_paths_and_skill_text():
    tomllib = pytest.importorskip("tomllib")
    home = 'C:\\Users\\Local User\\.codex'
    skill = 'Use "AX".\nLiteral backslash: \\ and non-BMP: \U0001f642'
    config = tomllib.loads(agents.codex_config(
        model="gpt-5.5", effort="xhigh", base_url="http://gateway/v1",
        codex_home=home, base_instructions="base.md", mcp_command="computer-use",
        developer_instructions=skill,
    ))
    assert config["developer_instructions"] == skill
    assert config["skills"]["config"][0]["path"].startswith(home + "/")


def test_missing_selected_skill_fails_before_overwriting_output(tmp_path):
    output, _, _ = render(tmp_path, "codex")
    previous = output.read_bytes()
    result = subprocess.run(
        [sys.executable, "-m", "hcu.render_agent_config", "--agent", "codex",
         "--gateway", "http://127.0.0.1:1", "--mcp", sys.executable,
         "--skill", str(tmp_path / "missing.md"), "--output", str(output)],
        cwd=ROOT, capture_output=True, text=True, timeout=15,
    )
    assert result.returncode != 0
    assert output.read_bytes() == previous


def test_claude_rendered_adapter_runs_with_rendering_interpreter(tmp_path):
    # A venv distinguishes the selected interpreter from /usr/bin/python3.
    import venv

    venv.EnvBuilder(with_pip=False, system_site_packages=True).create(tmp_path / "venv")
    python = str(tmp_path / "venv" / (
        "Scripts/python.exe" if os.name == "nt" else "bin/python"
    ))
    output, base, skill = render(
        tmp_path, "claude-code", python=python,
        extra=["--channels", "a11y,keyboard"],
    )
    server = json.loads(output.read_text())["mcpServers"]["computer_use"]
    assert server["command"] == python
    prompt = output.with_suffix(".system-prompt.md").read_text()
    assert base.read_text() in prompt
    assert skill.read_text() in prompt
    (tmp_path / "mcp").write_text(
        'import json, os, sys\n'
        'request = json.loads(sys.stdin.readline())\n'
        'assert request["method"] == "tools/list"\n'
        'assert os.environ["COMPUTER_USE_CHANNELS"] == "a11y,keyboard"\n'
        'schema = {"oneOf": [{"type": "object", "properties": '
        '{"mode": {"const": "observe"}}, "required": ["mode"]}]}\n'
        'print(json.dumps({"jsonrpc": "2.0", "id": request["id"], '
        '"result": {"tools": [{"name": "AX", "inputSchema": schema}]}}))\n',
        encoding="utf-8",
    )
    result = subprocess.run(
        [server["command"], *server["args"]], cwd=tmp_path,
        env=dict(os.environ, **server["env"]),
        input='{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n',
        capture_output=True, text=True, timeout=15,
    )
    assert result.returncode == 0, result.stderr
    schema = json.loads(result.stdout)["result"]["tools"][0]["inputSchema"]
    assert "oneOf" not in schema
    assert schema["properties"]["mode"] == {"const": "observe"}


def test_codex_cli_consumes_rendered_skill_and_existing_context(tmp_path):
    binary = Path(os.environ.get("CODEX_TEST_BINARY", ROOT / "artifacts/codex"))
    if not binary.is_file():
        pytest.skip("Codex artifact absent; set CODEX_TEST_BINARY for consumer test")
    home = tmp_path / "profile"
    home.mkdir()
    user_context = home / "AGENTS.md"
    user_context.write_text("USER_CONTEXT_MARKER", encoding="utf-8")
    project_context = tmp_path / "AGENTS.md"
    project_context.write_text("PROJECT_CONTEXT_MARKER", encoding="utf-8")
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            requests.append(json.loads(body))
            # Stop after inspecting the request, without model calls or tools.
            response = b'{"error":{"message":"offline consumer test","type":"invalid_request_error"}}'
            self.send_response(400)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, *args):
            pass

    with HTTPServer(("127.0.0.1", 0), Handler) as endpoint:
        thread = threading.Thread(target=endpoint.serve_forever, daemon=True)
        thread.start()
        try:
            output, base, skill = render(
                tmp_path, "codex", env={"CODEX_HOME": str(home)},
                extra=["--gateway", f"http://127.0.0.1:{endpoint.server_port}"],
            )
            result = subprocess.run(
                [str(binary), "exec", "--skip-git-repo-check", "--ephemeral",
                 "--ignore-rules", "-c", "mcp_servers.computer_use.enabled=false",
                 "--json", "OFFLINE_USER_REQUEST"],
                cwd=tmp_path, env=dict(os.environ, CODEX_HOME=str(output.parent)),
                capture_output=True, text=True, timeout=45,
            )
        finally:
            endpoint.shutdown()
            thread.join(timeout=5)
    assert requests, result.stderr
    request = requests[0]
    assert base.read_text().strip() in request["instructions"]
    messages = request["input"]
    developer_messages = json.dumps([
        message for message in messages if message.get("role") == "developer"
    ])
    assert "SELECTED_SKILL_MARKER" in developer_messages
    assert "USER_CONTEXT_MARKER" in json.dumps(messages)
    assert "PROJECT_CONTEXT_MARKER" in json.dumps(messages)
    assert user_context.read_text() == "USER_CONTEXT_MARKER"
    assert project_context.read_text() == "PROJECT_CONTEXT_MARKER"
