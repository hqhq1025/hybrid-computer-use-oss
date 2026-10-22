#!/usr/bin/env bash
# Check the file layout rules in AGENTS.md ("Where new files go").
#
# Usage (any directory inside the repository): scripts/maint/check_layout.sh
#
# Fails on:
#   1. a version marker ("_v2", "-v1", "V2" after a lowercase letter or digit)
#      in a tracked file or directory name, or in a Go type, func, const or
#      var name, outside vendor/ and the external-name allowlist below;
#   2. a Go import against the package direction
#      runtimebundle <- contract <- core <- provider <- transport <- cmd
#      (any GOOS, tests included);
#   3. a Linux provider module missing from linuxProviderPaths
#      (internal/provider/linux_runtime_bundle.go): go:embed takes the whole
#      directory, but only listed files reach the runtime bundle.
# Warns, without failing, for non-test source files over 800 lines and test
# files over 1,200 lines.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"
export PATH="$HOME/.local/bin:$PATH"

graph=$(
	cd computer-use
	for goos in linux darwin windows; do
		GOOS=$goos go list -tags 'darwin_sidecar_test windows_sidecar_test' \
			-f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./...
	done
)

GRAPH="$graph" python3 - <<'EOF'
import os
import re
import subprocess
import sys

MODULE = "github.com/hqhq1025/hybrid-computer-use-oss/computer-use"
RANK = {
    MODULE: 0,  # runtimebundle: the embedded Linux provider
    MODULE + "/internal/contract": 1,
    MODULE + "/internal/core": 2,
    MODULE + "/internal/provider": 3,
    MODULE + "/internal/transport": 4,
    MODULE + "/cmd/computer-use": 5,
}
# Version markers that name something outside this repository.
NAME_ALLOW = re.compile(
    r"osworld[-_]v\d"            # the OSWorld-V1/V2 benchmarks
    r"|/jsonschema/v\d"          # a Go module major version
    r"|^computer-use/vendor/"
    , re.IGNORECASE)
PATH_MARKER = re.compile(r"[_-][vV]\d|[a-z\d]V\d")
IDENT_MARKER = re.compile(r"(?:_v\d|[a-z\d]V\d)")
SOURCE = re.compile(r"\.(go|py|swift|cs|sh|ps1)$")
TEST = re.compile(
    r"(_test\.go$|(^|/)test_[^/]*\.py$|_test\.py$|(^|/)runtime_tests/|(^|/)tests/"
    r"|(^|/)Tests/|\.Tests/)")

files = sorted(p for p in subprocess.run(
    ["git", "ls-files", "-z"], check=True, capture_output=True, text=True
).stdout.split("\0") if p)
errors, warnings = [], []

# 1. Names.
for path in files:
    if NAME_ALLOW.search(path):
        continue
    for part in path.split("/"):
        if PATH_MARKER.search(part):
            errors.append("version marker in path: " + path)
            break

decl = re.compile(r"^(?:func(?:\s*\([^)]*\))?|type|const|var)\s+([A-Za-z_]\w*)")
group_start = re.compile(r"^(type|const|var) \($")
group_item = re.compile(r"^\t([A-Za-z_]\w*)\b")
for path in files:
    if not path.endswith(".go") or path.startswith("computer-use/vendor/"):
        continue
    with open(path, encoding="utf-8") as handle:
        lines = handle.read().splitlines()
    in_group = False
    for number, line in enumerate(lines, 1):
        names = []
        if in_group:
            if line == ")":
                in_group = False
                continue
            match = group_item.match(line)
            if match:
                names.append(match.group(1))
        elif group_start.match(line):
            in_group = True
        else:
            match = decl.match(line)
            if match:
                names.append(match.group(1))
        for name in names:
            if IDENT_MARKER.search(name):
                errors.append("version marker in Go name: {}:{}: {}".format(path, number, name))

# 2. Package direction.
for line in sorted(set(os.environ["GRAPH"].splitlines())):
    fields = line.split()
    if not fields or fields[0] not in RANK:
        continue
    for imported in sorted(set(fields[1:])):
        if imported in RANK and RANK[imported] >= RANK[fields[0]] and imported != fields[0]:
            errors.append("import against the package direction: {} -> {}".format(
                fields[0][len(MODULE):] or "runtimebundle", imported[len(MODULE):] or "runtimebundle"))

# 3. Linux provider registration.
provider_dir = "computer-use/native/linux/provider/"
modules = sorted(
    os.path.basename(p)[:-3] for p in files
    if p.startswith(provider_dir) and p.endswith(".py") and "/" not in p[len(provider_dir):]
    and not p.endswith("__init__.py"))
with open("computer-use/internal/provider/linux_runtime_bundle.go", encoding="utf-8") as handle:
    bundle = set(re.findall(r'"native/linux/provider/(\w+)\.py"', handle.read()))
for module in modules:
    if module not in bundle:
        errors.append("Linux provider module not in linuxProviderPaths: " + module)

# Size warnings.
for path in files:
    if not SOURCE.search(path) or path.startswith("computer-use/vendor/"):
        continue
    with open(path, "rb") as handle:
        count = handle.read().count(b"\n")
    limit = 1200 if TEST.search(path) else 800
    if count > limit:
        warnings.append("{} has {} lines (limit {})".format(path, count, limit))

for warning in warnings:
    print("warning: " + warning)
for error in errors:
    print("error: " + error)
if errors:
    sys.exit("layout check failed: {} error(s)".format(len(errors)))
print("layout ok ({} warning(s))".format(len(warnings)))
EOF
