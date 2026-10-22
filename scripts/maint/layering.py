#!/usr/bin/env python3
"""Evaluate a package layering against the declaration graph from
`pkgsplit -mode decls`.

Usage: layering.py DECLS_TXT MAP_TXT [--moves MOVES_TXT] [--show PKG->PKG]

MAP_TXT uses the pkgsplit map format. MOVES_TXT lines "file name dest-file"
pretend a declaration already lives in another file (planned extraction).
Prints package edges from non-test declarations with their causing
declaration pairs, and the cycles.
"""
import collections
import fnmatch
import sys


def read_map(path):
    targets, exact, globs = {}, {}, []
    for raw in open(path):
        line = raw.split("#", 1)[0].split()
        if not line:
            continue
        if line[0] == "package":
            targets[line[1]] = line[2]
        elif any(c in line[0] for c in "*?["):
            globs.append((line[0], line[1]))
        else:
            exact[line[0]] = line[1]

    def lookup(name):
        if name in exact:
            return exact[name]
        if name.endswith("_test.go") and name[:-8] + ".go" in exact:
            return exact[name[:-8] + ".go"]
        for pattern, key in globs:
            if fnmatch.fnmatch(name, pattern):
                return key
        raise KeyError(name)

    return lookup


def main():
    args = sys.argv[1:]
    decls, mapfile = args[0], args[1]
    moves, show = {}, set()
    if "--moves" in args:
        for raw in open(args[args.index("--moves") + 1]):
            parts = raw.split("#", 1)[0].split()
            if parts:
                moves[(parts[0], parts[1])] = parts[2]
    for i, a in enumerate(args):
        if a == "--show":
            show.add(args[i + 1])
    lookup = read_map(mapfile)

    def pkg_of(file, name):
        return lookup(moves.get((file, name), file))

    edges = collections.defaultdict(set)
    for raw in open(decls):
        parts = raw.split()
        if parts[0] != "edge":
            continue
        ff, fn, tf, tn = parts[1:]
        if ff.endswith("_test.go"):
            continue
        a, b = pkg_of(ff, fn), pkg_of(tf, tn)
        if a != b:
            edges[(a, b)].add(f"{ff}:{fn} -> {tf}:{tn}")
    graph = collections.defaultdict(set)
    for (a, b), pairs in sorted(edges.items()):
        graph[a].add(b)
        print(f"{a} -> {b}: {len(pairs)} references")
        if f"{a}->{b}" in show:
            for p in sorted(pairs):
                print("    " + p)
    # cycles via SCC
    index, low, stack, on, out = {}, {}, [], set(), []
    counter = [0]

    def strong(v):
        index[v] = low[v] = counter[0]
        counter[0] += 1
        stack.append(v)
        on.add(v)
        for w in sorted(graph[v]):
            if w not in index:
                strong(w)
                low[v] = min(low[v], low[w])
            elif w in on:
                low[v] = min(low[v], index[w])
        if low[v] == index[v]:
            scc = []
            while True:
                w = stack.pop()
                on.discard(w)
                scc.append(w)
                if w == v:
                    break
            if len(scc) > 1:
                out.append(sorted(scc))

    for v in sorted(list(graph)):
        if v not in index:
            strong(v)
    print("cycles:", out or "none")


if __name__ == "__main__":
    main()
