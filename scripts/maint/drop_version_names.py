#!/usr/bin/env python3
"""Remove history-only version markers and retired concept names from names.

AXSnapshotV1, ProviderV2Server, provider_wire_v2.go, wire_v2.py,
testdata/provider-v2/ and the private wire value "computer-use-provider-v2"
say that a type or protocol once had a predecessor. No predecessor exists in
the tree, so the marker carries no meaning.

Usage (repository root, clean tree; normally through drop_version_names.sh):
  drop_version_names.py measure   # planned moves, token renames, collisions
  drop_version_names.py map       # "old<TAB>new" for every path and token
  drop_version_names.py apply     # git mv and whole-token text rewrites

Rules match names, not a file list, so a file or identifier that another
branch adds under an old name is renamed too. Inputs are sorted, so one input
tree always gives one output tree, and a second run reports nothing to do.

  1. Paths: EXPLICIT_MOVES (retired concept names), then on each component
     the PHRASES and token rules, then a "_v<N>" infix before "_" or "." in
     a basename under computer-use/.
  2. Text, in every tracked text file outside EXCLUDED: PHRASES (the wire
     value, the fixture directory, prose), then old basenames of moved
     files, then the token rules on every [A-Za-z_][A-Za-z0-9_]* word.
     The token map is global, so a Go identifier, the comments and test
     strings that quote it, and its uses in Swift, C#, YAML and Markdown
     change together. The build and vet in drop_version_names.sh catch
     anything this misses.

Kept on purpose: the integer handshake (schema_version and protocol_version
2, ordered-input contract_version 1); external names (OSWorld-V2,
jsonschema/v6, PerMonitorV2, examples_v2); opaque value prefixes
(layout-v1-, layout-v2-); MCP protocolVersion; VERSION; CHANGELOG.md and
analysis/ history; the replay inputs of the earlier tools in scripts/maint/.

Before writing anything, apply refuses to run when a move target exists or
a new name already occurs in the same language (EXISTING_OK lists the
reviewed exceptions with the reason).
"""
import io
import os
import re
import subprocess
import sys
import tokenize

EXCLUDED = re.compile(
    r"^(computer-use/vendor/|analysis/|CHANGELOG\.md$|scripts/maint/)"
)
BINARY = re.compile(r"\.(gz|png|jpe?g|ico|icns|zip|pdf)$")

# Retired concepts. The public `until` argument was removed; these files
# serve wait steps now.
EXPLICIT_MOVES = {
    "computer-use/internal/core/ax_until_scope.go": "computer-use/internal/core/ax_wait_scope.go",
    "computer-use/internal/core/ax_until_scope_test.go": "computer-use/internal/core/ax_wait_scope_test.go",
    "computer-use/internal/core/ax_postcondition.go": "computer-use/internal/core/ax_wait_assertion.go",
    "computer-use/internal/core/ax_postcondition_test.go": "computer-use/internal/core/ax_wait_assertion_test.go",
    # Its tests check find paging and public error hints, not until.
    "computer-use/internal/core/ax_until_test.go": "computer-use/internal/core/ax_public_hint_test.go",
}

PHRASES = (
    # Go <-> provider wire value. All four sides ship from one commit.
    (re.compile(r"computer-use-provider-v2"), "computer-use-provider"),
    (re.compile(r"provider-v2"), "provider-wire"),
    (re.compile(r'(?<=""")Provider V2 '), "Provider "),
    (re.compile(r"Provider V2 "), "provider "),
    (re.compile(r"mismatched V2 "), "mismatched "),
    (re.compile(r"\(private protocol v2\)"), "(private protocol)"),
    (re.compile(r"wire v2 "), "wire "),
)

# The integers stay: they are the handshake values, not names.
EXACT_TOKENS = {
    "ProviderProtocolV2": "ProviderProtocolVersion",
    "ProviderOrderedInputContractV1": "ProviderOrderedInputContractVersion",
    "test_observe_request_uses_v2_envelope": "test_observe_request_uses_wire_envelope",
}
TOKEN_SUBSTRINGS = (
    ("AXSnapshotV1", "AXSnapshot"),
    ("ProviderV2", "Provider"),
    ("providerV2", "provider"),
    ("WireV2", "Wire"),
    ("wire_v2", "wire"),
)
# Python test names of HCU's own tests; tests/ names OSWorld-V2 on purpose.
PYTHON_TEST_VERSION = re.compile(r"_v\d+_")
BASENAME_VERSION = re.compile(r"_v\d+(?=[._])")
TOKEN = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")

EXISTING_OK = {
    # AppSnapshot.AXSnapshot is a field of type *AXSnapshot; Go keeps field
    # and type names apart.
    (".go", "AXSnapshot"),
    # In Go, wire_v2 occurs only inside Python module paths in strings; the
    # Go word "wire" is unrelated prose and locals.
    (".go", "wire"),
}
CODE_SUFFIXES = (".go", ".py", ".swift")


def git(*args):
    return subprocess.run(
        ("git",) + args, check=True, capture_output=True, text=True
    ).stdout


def tracked():
    return sorted(p for p in git("ls-files", "-z").split("\0") if p)


def read(path):
    with open(path, "rb") as handle:
        data = handle.read()
    if b"\0" in data:
        return None
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return None


def rename_token(word, path):
    if word in EXACT_TOKENS:
        return EXACT_TOKENS[word]
    if (
        path.startswith("computer-use/")
        and path.endswith(".py")
        and word.startswith("test_")
    ):
        word = PYTHON_TEST_VERSION.sub("_", word)
    for old, new in TOKEN_SUBSTRINGS:
        word = word.replace(old, new)
    return word


def new_path(path):
    if path in EXPLICIT_MOVES:
        return EXPLICIT_MOVES[path]
    if not path.startswith("computer-use/") or EXCLUDED.search(path):
        return path
    parts = path.split("/")
    for index, part in enumerate(parts):
        for pattern, replacement in PHRASES:
            part = pattern.sub(replacement, part)
        part = TOKEN.sub(lambda m: rename_token(m.group(0), ""), part)
        if index == len(parts) - 1:
            part = BASENAME_VERSION.sub("", part)
        parts[index] = part
    return "/".join(parts)


def basename_rules(moves):
    pairs = {
        os.path.basename(old): os.path.basename(new)
        for old, new in moves
        if os.path.basename(old) != os.path.basename(new)
    }
    # Longest first: ax_snapshot_v1_build.go before ax_snapshot_v1.go.
    ordered = sorted(pairs.items(), key=lambda item: (-len(item[0]), item[0]))
    return [
        (re.compile(r"(?<![\w.-])" + re.escape(old) + r"(?!\w)"), new)
        for old, new in ordered
    ]


def rewrite(text, path, rules, counts=None):
    for pattern, replacement in PHRASES:
        text, n = pattern.subn(replacement, text)
        if counts is not None and n:
            counts[("phrase", pattern.pattern, replacement)] = counts.get(("phrase", pattern.pattern, replacement), 0) + n
    for pattern, replacement in rules:
        text, n = pattern.subn(replacement, text)
        if counts is not None and n:
            counts[("file", pattern.pattern, replacement)] = counts.get(("file", pattern.pattern, replacement), 0) + n

    def token(match):
        old = match.group(0)
        new = rename_token(old, path)
        if counts is not None and new != old:
            counts[("token", old, new)] = counts.get(("token", old, new), 0) + 1
        return new

    return TOKEN.sub(token, text)


def python_names(text):
    names = set()
    try:
        for tok in tokenize.generate_tokens(io.StringIO(text).readline):
            if tok.type == tokenize.NAME:
                names.add(tok.string)
    except (tokenize.TokenError, SyntaxError):
        pass
    return names


def plan():
    files = tracked()
    moves = [(p, new_path(p)) for p in files if new_path(p) != p]
    rules = basename_rules(moves)
    texts, edits, counts, renamed = {}, {}, {}, {}
    for path in files:
        if EXCLUDED.search(path) or BINARY.search(path):
            continue
        text = read(path)
        if text is None:
            continue
        texts[path] = text
        local = {}
        updated = rewrite(text, path, rules, local)
        for key, value in local.items():
            counts[key] = counts.get(key, 0) + value
            if key[0] == "token":
                renamed.setdefault(os.path.splitext(path)[1], set()).add(key[1:])
        if updated != text:
            edits[path] = updated
    return files, moves, rules, texts, edits, counts, renamed


def problems_for(files, moves, texts, renamed):
    problems = []
    existing = set(files)
    seen = {}
    for old, new in moves:
        if new in existing:
            problems.append("move target exists: {} -> {}".format(old, new))
        if new in seen:
            problems.append("{} and {} both move to {}".format(seen[new], old, new))
        seen[new] = old
    for suffix in CODE_SUFFIXES:
        pairs = renamed.get(suffix, set())
        olds = {old for old, _ in pairs}
        news = {new for _, new in pairs}
        present = set()
        for path, text in texts.items():
            if not path.endswith(suffix):
                continue
            if suffix == ".py":
                present |= python_names(text) & news
            else:
                present |= {n for n in news if re.search(r"\b" + re.escape(n) + r"\b", text)}
        for name in sorted(present - olds):
            if (suffix, name) in EXISTING_OK:
                continue
            sources = ", ".join(sorted(o for o, n in pairs if n == name))
            problems.append("{}: {} -> {}: {} already exists".format(suffix, sources, name, name))
    return problems


def measure():
    files, moves, rules, texts, edits, counts, renamed = plan()
    print("== file moves ({})".format(len(moves)))
    for old, new in moves:
        print("{}\t{}".format(old, new))
    print("\n== token renames (occurrences)")
    for key in sorted(k for k in counts if k[0] == "token"):
        print("{}\t{}\t{}".format(key[1], key[2], counts[key]))
    print("\n== phrase and file-name rewrites (occurrences)")
    for key in sorted(k for k in counts if k[0] != "token"):
        print("{}\t{}\t{}\t{}".format(*key, counts[key]))
    print("\n== files whose text changes: {}".format(len(edits)))
    problems = problems_for(files, moves, texts, renamed)
    print("\n== problems: {}".format(len(problems)))
    for problem in problems:
        print("  " + problem)
    return 1 if problems else 0


def mapping():
    files, moves, rules, texts, edits, counts, renamed = plan()
    for old, new in moves:
        print("{}\t{}".format(old, new))
    for key in sorted(k for k in counts if k[0] == "token"):
        print("{}\t{}".format(key[1], key[2]))
    return 0


def apply():
    if git("status", "--porcelain"):
        sys.exit("the tree has uncommitted changes; commit or stash them first")
    files, moves, rules, texts, edits, counts, renamed = plan()
    problems = problems_for(files, moves, texts, renamed)
    if problems:
        sys.exit("refusing to rename:\n  " + "\n  ".join(problems))
    if not moves and not edits:
        print("nothing to rename")
        return 0
    for old, new in moves:
        os.makedirs(os.path.dirname(new), exist_ok=True)
        git("mv", old, new)
    destination = dict(moves)
    for path in sorted(edits):
        target = destination.get(path, path)
        with open(target, "w", encoding="utf-8", newline="") as handle:
            handle.write(edits[path])
    print(
        "moved {} files, rewrote {} files, renamed {} distinct tokens".format(
            len(moves), len(edits), len({k for k in counts if k[0] == "token"})
        )
    )
    return 0


def main():
    os.chdir(git("rev-parse", "--show-toplevel").strip())
    mode = sys.argv[1] if len(sys.argv) > 1 else "measure"
    actions = {"measure": measure, "map": mapping, "apply": apply}
    if mode not in actions:
        sys.exit("usage: drop_version_names.py measure|map|apply")
    return actions[mode]()


if __name__ == "__main__":
    sys.exit(main())
