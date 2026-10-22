#!/usr/bin/env python3
"""Compute a cycle-free declaration layering by pulling dependencies down.

Usage: pulldown.py DECLS_TXT MAP_TXT ORDER [--pin FILE:NAME=PKG ...] [--decls FILE]

--decls FILE lists "file:name pkg" overrides for single declarations.

ORDER is a comma list of package keys from lowest to highest, e.g.
contract,provider,core,transport,cmd. Each declaration starts in the package
of its file (MAP_TXT). While a non-test declaration depends on a declaration
in a higher layer, the dependency moves down to the dependant's layer.
Pinned declarations never move; a pin that must move is reported as a
conflict. Prints the declarations that moved, grouped by source file.
"""
import collections
import sys

sys.path.insert(0, __import__("os").path.dirname(__file__))
from layering import read_map  # noqa: E402


def main():
    decls, mapfile, order = sys.argv[1], sys.argv[2], sys.argv[3].split(",")
    pins = {}
    args = sys.argv[4:]
    for i, a in enumerate(args):
        if a == "--pin":
            key, pkg = args[i + 1].rsplit("=", 1)
            pins[key] = pkg
    rank = {p: i for i, p in enumerate(order)}
    lookup = read_map(mapfile)
    units, deps = {}, collections.defaultdict(set)
    for raw in open(decls):
        parts = raw.split()
        if parts[0] == "unit":
            units[f"{parts[1]}:{parts[2]}"] = int(parts[3])
        elif parts[0] == "edge" and not parts[1].endswith("_test.go"):
            deps[f"{parts[1]}:{parts[2]}"].add(f"{parts[3]}:{parts[4]}")
    cuts = {args[i + 1] for i, a in enumerate(args) if a == "--cut"}
    for u in deps:
        deps[u] = {d for d in deps[u] if d.split(":")[1] not in cuts}
    overrides = {}
    if "--decls" in args:
        for raw in open(args[args.index("--decls") + 1]):
            parts = raw.split("#", 1)[0].split()
            if parts:
                overrides[parts[0]] = parts[1]
    def home_of(u):
        return overrides.get(u) or lookup(u.split(":")[0])
    layer = {u: home_of(u) for u in units}
    why = {}
    layer.update({k: v for k, v in pins.items() if k in layer})
    conflicts = set()
    # A method lives in its receiver type's package: link "T.m" with "T".
    types_by_name = collections.defaultdict(list)
    for u in units:
        name = u.split(":")[1].rstrip("+")
        if "." not in name:
            types_by_name[name].append(u)
    free = {args[i + 1] for i, a in enumerate(args) if a == "--free"}
    for u in units:
        name = u.split(":")[1]
        if name in free or name.split(".")[0] + ".*" in free:
            continue
        if "." in name and not u.split(":")[0].endswith("_test.go"):
            for t in types_by_name.get(name.split(".")[0], []):
                if not t.split(":")[0].endswith("_test.go"):
                    deps[u].add(t)
                    deps[t].add(u)
    changed = True
    while changed:
        changed = False
        for u in list(units):
            if u.split(":")[0].endswith("_test.go"):
                continue
            for d in deps[u]:
                if rank[layer[d]] > rank[layer[u]]:
                    if d in pins:
                        conflicts.add(f"{u} ({layer[u]}) needs pinned {d} ({layer[d]})")
                        continue
                    layer[d] = layer[u]
                    why[d] = u
                    changed = True
    moved = collections.defaultdict(list)
    for u in sorted(units):
        f = u.split(":")[0]
        if f.endswith("_test.go"):
            continue
        home = home_of(u)
        if layer[u] != home:
            moved[(f, home, layer[u])].append((u.split(":")[1] + "<" + why.get(u, "?").split(":")[1], units[u]))
    total = 0
    for (f, home, to), items in sorted(moved.items()):
        lines = sum(n for _, n in items)
        total += lines
        print(f"{f} {home}->{to} {len(items)} decls {lines} lines: " + " ".join(n for n, _ in items))
    print(f"total moved lines: {total}")
    if "--dump" in args:
        with open(args[args.index("--dump") + 1], "w") as out:
            for u in sorted(units):
                if not u.split(":")[0].endswith("_test.go"):
                    out.write(f"{u} {layer[u]}\n")
    for c in sorted(conflicts):
        print("CONFLICT", c)


if __name__ == "__main__":
    main()
