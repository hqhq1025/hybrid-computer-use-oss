#!/usr/bin/env python3
"""Split computer-use's single Go `package main` into packages.

Run through scripts/maint/split_packages.sh from the repository root on a
clean tree that already has the a11y-to-ax rename. Steps (each one commits
when --commit is given, so a reviewer can read them one by one):

  prepare-backend   platform backend chosen through a core hook, not a
                    function every platform file defines
  prepare-version   server version lives in contract; main copies the
                    -X main.version value into it before anything runs
  prepare-moves     move declarations whose package differs from their
                    file's package into files of the right package
  prepare-tests     move tests that need a higher package into their own
                    files and copy shared test helpers for them
  apply             pkgsplit: move files, export names, qualify references
  post              blank-import the provider, fix relative test paths
                    and build scripts

Every prepare step keeps one `package main` that builds for linux, darwin
and windows. The script checks its own assumptions (each text it edits must
appear exactly once) and stops at the first mismatch, so re-running it after
other branches merge either reproduces the split or names what changed.
"""
from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip())
GO = REPO / "computer-use"
MAINT = REPO / "scripts" / "maint"
TOOL = Path(os.environ.get("PKGSPLIT_BIN", "/tmp/hcu-maint-pkgsplit"))
ENV = dict(os.environ, PATH=f"{Path.home()}/.local/bin:{os.environ['PATH']}")
MODULE = "github.com/hqhq1025/hybrid-computer-use-oss/computer-use"


def run(cmd, cwd=GO, quiet=False):
    result = subprocess.run(cmd, cwd=cwd, env=ENV, text=True, capture_output=True)
    if result.returncode != 0:
        sys.exit(f"command failed: {' '.join(map(str, cmd))}\n{result.stdout}{result.stderr}")
    if not quiet and result.stdout.strip():
        print(result.stdout.rstrip())
    return result.stdout


def pkgsplit(*args, quiet=False):
    return run([str(TOOL), "-root", str(GO), *args], quiet=quiet)


def edit(path: str, old: str, new: str, count: int = 1):
    file = GO / path
    text = file.read_text()
    found = text.count(old)
    if found != count:
        sys.exit(f"{path}: expected {count} occurrence(s) of {old!r}, found {found}")
    file.write_text(text.replace(old, new))


def build_all():
    for goos in ("linux", "darwin", "windows"):
        run(["env", f"GOOS={goos}", "go", "vet", "./..."], quiet=True)
    run(["go", "vet", "-tags", "darwin_sidecar_test windows_sidecar_test", "./..."], quiet=True)


def commit(message: str, args):
    run(["gofmt", "-w", "."], quiet=True)
    build_all()
    if args.commit:
        run(["git", "add", "-A", "computer-use", "Makefile", ".github"], cwd=REPO, quiet=True)
        run(["git", "commit", "-q", "-m", message], cwd=REPO, quiet=True)
        print("committed:", message.splitlines()[0])


# --------------------------------------------------------------------------
# prepare-backend


def prepare_backend(args):
    """Core asks a hook for the default backend; each platform file sets it.

    Before: every platform file defined defaultRuntimeBackend() and
    shutdownPlatformRuntime(), and core called them. Core cannot import the
    provider package, so core now owns two variables whose defaults are the
    unsupported backend and a no-op, and each platform file installs its
    implementation in init(). A binary that links a platform backend behaves
    as before; core's own tests, which link none, run against the
    unsupported backend.
    """
    platform_files = "runtime_backend_linux.go,runtime_backend_darwin.go,runtime_backend_windows.go"
    pkgsplit("-mode", "rename-object", "-name", "defaultRuntimeBackend,shutdownPlatformRuntime",
             "-to-name", "platformRuntimeBackend,platformShutdown", "-files", platform_files)
    unsupported = GO / "runtime_backend_unsupported.go"
    text = unsupported.read_text()
    for old in (
        "//go:build !linux && !darwin && !windows\n\n",
        "\nfunc defaultRuntimeBackend() PlatformBackend {\n\treturn unsupportedRuntimeBackend{}\n}\n",
        "\nfunc shutdownPlatformRuntime() {}\n",
    ):
        if text.count(old) != 1:
            sys.exit(f"runtime_backend_unsupported.go: cannot find {old!r}")
        text = text.replace(old, "")
    unsupported.write_text(text)
    (GO / "platform_default.go").write_text('''package main

// defaultRuntimeBackend returns the backend of a service created without
// one, and shutdownPlatformRuntime stops what that backend started. A
// platform backend file installs both in init(). Where no platform backend
// is linked (an unsupported GOOS, or tests of packages below the provider),
// a service gets the unsupported backend.
var (
	defaultRuntimeBackend   = func() PlatformBackend { return unsupportedRuntimeBackend{} }
	shutdownPlatformRuntime = func() {}
)
''')
    for path in platform_files.split(","):
        file = GO / path
        file.write_text(file.read_text().rstrip("\n") + '''

func init() {
	defaultRuntimeBackend = platformRuntimeBackend
	shutdownPlatformRuntime = platformShutdown
}
''')
    commit("refactor(maint): select the platform backend through a core hook\n\n"
           "Generated by scripts/maint/split_packages.sh prepare-backend.", args)


# --------------------------------------------------------------------------
# prepare-version


def prepare_version(args):
    """The server version moves to contract; main still owns main.version.

    Release builds set -X main.version=...; main() copies it into
    serverVersion before it runs any command, so every reader sees the same
    value as before and the build scripts keep their -X flag.
    """
    pkgsplit("-mode", "rename-object", "-name", "version", "-to-name", "serverVersion")
    edit("main.go", 'var serverVersion = "dev"\n', 'var version = "dev"\n')
    edit("main.go", "func main() {\n", "func main() {\n\tserverVersion = version\n")
    edit("contract_describe.go", "\n// storedRequest ", '''
// serverVersion is the version the server reports. main sets it from the
// release build's -X main.version value before any command runs.
var serverVersion = "dev"

// storedRequest ''')
    commit("refactor(maint): keep the server version in contract\n\n"
           "Generated by scripts/maint/split_packages.sh prepare-version.", args)


# --------------------------------------------------------------------------
# prepare-moves

# Declarations whose package is decided here, not by their file. Each entry:
# (source file, destination file, declarations). The destination file's
# package comes from package_map.txt.
FIXED_MOVES = [
    # main.go keeps main() and the version; the command line is transport.
    ("main.go", "cli_main.go", ["runCLI", "runCallCommand", "callSpec", "readArguments",
                                "readCallSequence", "readJSONSource", "helpText", "runtimeNotes"]),
    # Calls on the service are core; the CLI calls them.
    ("main.go", "service_entry.go", ["service.callForCommandLine", "service.callForText"]),
    # The session carries the negotiated MCP version; core owns the type.
    ("mcp_transport.go", "mcp_session_state.go", ["mcpTransportState", "mcpProtocolLegacy+"]),
    # request_id replay is service state; contract only describes it.
    ("contract_describe.go", "request_replay.go", ["storedRequest", "desktopShared.replayOnce",
                                                   "desktopShared.pruneRequestResults", "errRequestIDReused"]),
    # Rejected-argument results are core results; contract only validates.
    ("schema_validator.go", "argument_errors.go", ["argumentErrorResult"]),
    ("tool_argument_rules.go", "argument_errors.go", ["withArgumentRule"]),
    # go:embed cannot reach a parent directory, so the embedded Linux
    # provider files stay at the module root in a one-file package.
    ("linux_runtime_bundle.go", "runtime_bundle_fs.go", ["linuxRuntimeFS"]),
]
NO_TAG = {"runtime_bundle_fs.go"}
# Destination for declarations the layering pulls into contract.
PULLED_TO = {"contract": "published_values.go"}


def extract(source, dest, names):
    args = ["-mode", "extract", "-from", source, "-to", dest, "-decls", ",".join(names)]
    if dest in NO_TAG:
        args.append("-keep-build-tag=false")
    pkgsplit(*args)


def layering_moves():
    """Declarations the layering must move, as {(file, decl): package}."""
    decls = GO / ".pkgsplit-decls.txt"
    decls.write_text(pkgsplit("-mode", "decls", quiet=True))
    out = run([sys.executable, str(MAINT / "split_layering.py"), str(decls),
               str(MAINT / "package_map.txt"), "bundle,contract,core,provider,transport,cmd",
               "--cut", "defaultRuntimeBackend", "--cut", "shutdownPlatformRuntime"],
              quiet=True)
    decls.unlink()
    moves = {}
    for line in out.splitlines():
        match = re.match(r"(\S+) (\w+)->(\w+) \d+ decls \d+ lines: (.*)", line)
        if match:
            for item in match.group(4).split():
                moves[(match.group(1), item.split("<")[0])] = match.group(3)
    return moves, out


def prepare_moves(args):
    for source, dest, names in FIXED_MOVES:
        extract(source, dest, names)
    moves, report = layering_moves()
    unknown = {k: v for k, v in moves.items() if v not in PULLED_TO}
    if unknown:
        sys.exit("the layering needs moves this script does not place; decide a "
                 f"destination in FIXED_MOVES:\n{report}")
    by_source = {}
    for (source, name), package in sorted(moves.items()):
        by_source.setdefault((source, PULLED_TO[package]), []).append(name)
    for (source, dest), names in sorted(by_source.items()):
        extract(source, dest, names)
    remaining, report = layering_moves()
    if remaining:
        sys.exit(f"layering still needs moves after prepare-moves:\n{report}")
    commit("refactor(maint): move declarations to the file of their target package\n\n"
           "Generated by scripts/maint/split_packages.sh prepare-moves. Pure\n"
           "moves inside package main; no declaration changes.", args)


# --------------------------------------------------------------------------
# prepare-tests

# Test declarations that need a higher package than their file.
TEST_MOVES = [
    ("ax_find_native_delivery_test.go", "find_delivery_transport_test.go", [
        "TestFindDeliveryActualTransportProtocols", "TestFindDeliveryBudgetPagesFreezeAndReplay",
        "TestFindDeliveryLiteralTruncationMarkerThroughTransport",
        "TestFindDeliveryLongEscapedIdentityAndMatchedTail", "TestFindDeliveryUnaddressableAndTinyBudgets",
        "TestFindDeliveryWireEscapingIsSeparateFromTextBudget", "findDeliveryWire"]),
    ("ax_find_native_exposure_test.go", "find_delivery_transport_test.go", ["TestFindExposureTransport"]),
    ("ax_safe_trim_test.go", "find_delivery_transport_test.go", ["TestFindTextExcerptDeliveredWithinBudget"]),
    ("runtime_backend_test.go", "runtime_timeout_linux_test.go",
     ["TestRuntimeTimeoutDiagnosticsSeparateProcessWindowAndBridgeState"]),
    ("tool_arguments_test.go", "cli_arguments_test.go", ["TestElementIndexAcceptsStringAndJSONNumber"]),
]

# Shared test helpers copied for tests in a higher package:
# (prefix, copy file, user files, seed helpers).
HELPER_COPIES = [
    ("transport", "transport_helpers_test.go", None, None),
    ("provider", "provider_helpers_test.go", None, None),
]


def prepare_tests(args):
    for source, dest, names in TEST_MOVES:
        extract(source, dest, names)
    for prefix, copy_file, users, seeds in HELPER_COPIES:
        users = [u for u in (users or package_test_files(prefix)) if (GO / u).exists()]
        pkgsplit("-mode", "copy", "-to", copy_file, "-prefix", prefix, "-files", ",".join(users))
    # RefRegistry.Acquire is a test-only method on a core type, so tests in
    # another package cannot call it. It only forwards to AcquireTyped with
    # AXLocatorSemantic; the copied helpers call AcquireTyped directly.
    copy = GO / "transport_helpers_test.go"
    if copy.exists():
        text = copy.read_text()
        if ".refRegistry.Acquire(" in text:
            copy.write_text(text.replace(".refRegistry.Acquire(", ".refRegistry.AcquireTyped(AXLocatorSemantic, "))
    commit("test(maint): give tests of higher packages their own files and helpers\n\n"
           "Generated by scripts/maint/split_packages.sh prepare-tests. Tests move\n"
           "unchanged; shared helpers are copied under prefixed names.", args)


def package_test_files(package):
    out = run([sys.executable, str(MAINT / "split_packages.py"), "map-files", package], cwd=REPO, quiet=True)
    return [f for f in out.split() if f.endswith("_test.go")]


def file_packages():
    sys.path.insert(0, str(MAINT))
    from layering import read_map  # noqa: PLC0415
    lookup = read_map(str(MAINT / "package_map.txt"))
    return {p.name: lookup(p.name) for p in GO.glob("*.go")}


# --------------------------------------------------------------------------
# apply and post


def apply(args):
    # service embeds *desktopState and *sessionState. Exporting the types
    # renames the embedded fields; no code JSON-encodes or reflects over a
    # service, so the rename changes no output.
    pkgsplit("-map", str(MAINT / "package_map.txt"), "-mode", "apply",
             "-allow-embedded", "desktopState,sessionState")
    post()
    commit("refactor(maint): split package main into contract, core, provider, transport and cmd\n\n"
           "Generated by scripts/maint/split_packages.sh apply.", args)


# Relative paths in tests, rewritten for tests three levels deeper
# (computer-use/internal/<pkg>/ instead of computer-use/).
PATH_REWRITES = [
    (r'"\.\./', '"../../../'),
    (r'filepath\.Join\("\.\.", ', 'filepath.Join("..", "..", "..", '),
    (r'"testdata/', '"../../testdata/'),
    (r'filepath\.Join\("testdata", ', 'filepath.Join("..", "..", "testdata", '),
    (r'(ReadFile|Open)\("native/', r'\1("../../native/'),
    (r'filepath\.Join\("native", ', 'filepath.Join("..", "..", "native", '),
]


# Source-contract tests compare fragments of the Go source. After the split
# some fragments name a qualified identifier; each rewrite must match a
# fragment in the test and the new fragment must exist in the source.
SOURCE_FRAGMENTS = [
    ("internal/core/tool_result_test.go", "`if name != publicToolGUI`", "`if name != contract.PublicToolGUI`"),
]


def post():
    for test in (GO / "internal").glob("*/*_test.go"):
        text = original = test.read_text()
        for pattern, replacement in PATH_REWRITES:
            if "providerV2FixtureFS" in text and "testdata" in pattern:
                continue  # the fixture FS is rooted at computer-use/
            text = re.sub(pattern, replacement, text)
        if "//go:embed testdata/provider-v2/*.json" in text:
            # go:embed cannot reach ../../testdata; the shared fixture corpus
            # stays where the Swift and C# tests read it.
            text = text.replace("//go:embed testdata/provider-v2/*.json\nvar providerV2FixtureFS embed.FS",
                                'var providerV2FixtureFS = os.DirFS("../..").(fs.ReadFileFS)')
            text = text.replace('\t"embed"\n', '\t"io/fs"\n')
            if '\t"os"\n' not in text:
                text = text.replace('\t"io/fs"\n', '\t"io/fs"\n\t"os"\n')
        if "func mainGoSource(" in text:
            # The source-contract helper read every non-test file of package
            # main; it now reads every non-test file of the module.
            old = 'entries, err := os.ReadDir(".")'
            if text.count(old) != 1:
                sys.exit(f"{test}: mainGoSource no longer reads the package directory")
            text = re.sub(
                r"func mainGoSource\(t \*testing\.T\) string \{.*?\n\}\n",
                lambda _: MAIN_GO_SOURCE, text, count=1, flags=re.S)
            for needed in ('"io/fs"', '"path/filepath"', '"slices"'):
                if needed not in text:
                    text = text.replace("import (\n", f"import (\n\t{needed}\n", 1)
        if text != original:
            test.write_text(text)
    for path, old, new in SOURCE_FRAGMENTS:
        replace_in(GO / path, old, new)
    stub = GO / "internal" / "provider" / "windows_parent_death_teststub.go"
    # The stub (built only with windows_sidecar_test off Windows) used
    # nopWriteCloser from a Linux _test.go file, so the package compiled only
    # together with its tests. Importers of the provider package build it
    # without tests, so the stub gets its own closer.
    replace_in(stub, "\treturn nopWriteCloser{}, nil\n", "\treturn testStubCloser{}, nil\n")
    text = stub.read_text()
    if "type testStubCloser" not in text:
        stub.write_text(text.rstrip("\n") + '''

// testStubCloser stands in for the Windows job object handle.
type testStubCloser struct{}

func (testStubCloser) Close() error { return nil }
''')
    # The fuzz corpus must sit beside the fuzz test.
    fuzz = GO / "testdata" / "fuzz"
    if fuzz.exists():
        target = locate_test("func FuzzAXArgumentsMatchSchema").parent / "testdata"
        target.mkdir(exist_ok=True)
        run(["git", "mv", str(fuzz), str(target / "fuzz")], cwd=REPO, quiet=True)
    main = GO / "cmd" / "computer-use" / "main.go"
    text = main.read_text()
    provider = f'"{MODULE}/internal/provider"'
    if provider not in text:
        # A separate import group keeps the comment next to the blank import
        # when gofmt sorts the other imports.
        block = re.search(r"^import \((.*?)^\)\n", text, flags=re.S | re.M)
        if not block:
            sys.exit(f"{main}: no import block")
        text = (text[:block.end(1)] + "\n\t// The provider installs the platform backend in init().\n"
                f"\t_ {provider}\n" + text[block.end(1):])
        main.write_text(text)
    replace_in(REPO / "Makefile", "-o ../artifacts/computer-use .", "-o ../artifacts/computer-use ./cmd/computer-use")
    replace_in(GO / "native" / "macos" / "build.sh", '  -o "${output_dir}/computer-use" \\\n  .\n',
               '  -o "${output_dir}/computer-use" \\\n  ./cmd/computer-use\n')
    replace_in(GO / "native" / "windows" / "build.ps1",
               '-o (Join-Path $OutputDirectory "computer-use.exe") `\n            .\n',
               '-o (Join-Path $OutputDirectory "computer-use.exe") `\n            ./cmd/computer-use\n')


MAIN_GO_SOURCE = r'''func mainGoSource(t *testing.T) string {
	t.Helper()
	// The module root is two directories up (computer-use/internal/<pkg>).
	root := filepath.Join("..", "..")
	names := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "testdata", "native":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read Go source directory: %v", err)
	}
	slices.Sort(names)
	var source strings.Builder
	for _, name := range names {
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		source.Write(data)
		source.WriteByte('\n')
	}
	return source.String()
}
'''


def locate_test(marker):
    for test in GO.rglob("*_test.go"):
        if "vendor" not in test.parts and marker in test.read_text():
            return test
    sys.exit(f"cannot find {marker}")


def replace_in(path: Path, old: str, new: str):
    text = path.read_text()
    if new in text:
        return
    if text.count(old) != 1:
        sys.exit(f"{path}: expected one {old!r}")
    path.write_text(text.replace(old, new))


STEPS = {
    "prepare-backend": prepare_backend,
    "prepare-version": prepare_version,
    "prepare-moves": prepare_moves,
    "prepare-tests": prepare_tests,
    "apply": apply,
}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("step", choices=[*STEPS, "all", "map-files"])
    parser.add_argument("rest", nargs="*")
    parser.add_argument("--commit", action="store_true")
    args = parser.parse_args()
    if args.step == "map-files":
        for name, package in sorted(file_packages().items()):
            if package == args.rest[0]:
                print(name)
        return
    steps = list(STEPS) if args.step == "all" else [args.step]
    for step in steps:
        print(f"== {step}")
        STEPS[step](args)


if __name__ == "__main__":
    main()
