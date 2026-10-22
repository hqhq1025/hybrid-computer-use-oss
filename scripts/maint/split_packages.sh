#!/usr/bin/env bash
# Split computer-use's single Go `package main` into the packages listed in
# scripts/maint/package_map.txt:
#
#   bundle (computer-use/, embedded Linux provider files)
#     < contract (internal/contract)  < core (internal/core)
#     < provider (internal/provider)  < transport (internal/transport)
#     < cmd/computer-use (main)
#
# Usage (repository root, clean tree, after rename_a11y_to_ax.sh apply):
#   scripts/maint/split_packages.sh analyze        # layering report, no edits
#   scripts/maint/split_packages.sh STEP [--commit]
#   scripts/maint/split_packages.sh all --commit   # every step, one commit each
#
# STEP is prepare-backend, prepare-version, prepare-moves, prepare-tests or
# apply. Each prepare step keeps package main building for linux, darwin and
# windows. The steps stop at the first assumption that no longer holds
# (an edited text that is missing or ambiguous, a layering move with no
# destination, a name collision), so after other branches merge the script
# either reproduces the split or names what to update.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"
export PATH="$HOME/.local/bin:$PATH"
export PKGSPLIT_BIN=${PKGSPLIT_BIN:-/tmp/hcu-maint-pkgsplit}
(cd scripts/maint/pkgsplit && go build -o "$PKGSPLIT_BIN" .)

step=${1:-analyze}
shift || true
case "$step" in
analyze)
	decls=$(mktemp)
	"$PKGSPLIT_BIN" -root computer-use -mode decls >"$decls"
	echo "== declarations the layering must move (file from->to: decl<first user)"
	python3 scripts/maint/split_layering.py "$decls" scripts/maint/package_map.txt \
		bundle,contract,core,provider,transport,cmd \
		--cut defaultRuntimeBackend --cut shutdownPlatformRuntime
	rm -f "$decls"
	;;
*)
	if [ -n "$(git status --porcelain -- computer-use)" ]; then
		echo "computer-use has uncommitted changes; commit or stash them first" >&2
		exit 1
	fi
	python3 scripts/maint/split_packages.py "$step" "$@"
	;;
esac
