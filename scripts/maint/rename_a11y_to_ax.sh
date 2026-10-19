#!/usr/bin/env bash
# Rename internal Go identifiers from the old "a11y" spelling to "ax"/"AX".
#
# Usage (from the repository root, on a clean tree):
#   scripts/maint/rename_a11y_to_ax.sh measure   # counts per file and kind, kept literals
#   scripts/maint/rename_a11y_to_ax.sh map       # old -> new identifier table
#   scripts/maint/rename_a11y_to_ax.sh apply     # rewrite, gofmt, build, vet
#
# Scope: identifiers in computer-use/*.go (tests included), comment words that
# are one of those identifiers, and string literals in _test.go files that
# quote one of those identifiers (source-contract tests). It never edits
# non-test string literals, so public tool names, the "a11y" channel value,
# the "a11y_batch_result" structured-content key, "[a11y]" note prefixes,
# "a11y_responsive" and AT-SPI object paths stay byte-identical. Python, docs
# and file names are out of scope: no Go file name contains "a11y", and the
# Python occurrences are model-visible note channels, environment variables
# or OSWorld API names.
#
# The tool refuses to run when a new name already exists anywhere in the
# package, so it is safe to re-run after other branches merge: either it
# renames cleanly or it lists the collisions to resolve in its override table
# (scripts/maint/rename_a11y/main.go).
set -euo pipefail

mode=${1:-measure}
repo=$(git rev-parse --show-toplevel)
cd "$repo"
export PATH="$HOME/.local/bin:$PATH"
tool=scripts/maint/rename_a11y/main.go

case "$mode" in
measure | map)
	go run "$tool" -root computer-use -mode "$mode"
	;;
apply)
	if [ -n "$(git status --porcelain -- computer-use)" ]; then
		echo "computer-use has uncommitted changes; commit or stash them first" >&2
		exit 1
	fi
	go run "$tool" -root computer-use -mode apply
	# Renamed constants change column alignment; gofmt restores it.
	changed=$(git diff --name-only -- 'computer-use/*.go')
	if [ -n "$changed" ]; then
		# shellcheck disable=SC2086
		gofmt -w $changed
	fi
	(cd computer-use && go build ./... && go vet ./...)
	left=$(grep -rnE 'a11y|A11y|A11Y' --include='*.go' computer-use |
		grep -v '^computer-use/vendor/' || true)
	echo "remaining occurrences (string literals and channel prose, kept on purpose):"
	echo "$left" | grep -c . || true
	;;
*)
	echo "usage: $0 measure|map|apply" >&2
	exit 2
	;;
esac
