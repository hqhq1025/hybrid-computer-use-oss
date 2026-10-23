package main

import (
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

func (a *analysis) report(verbose bool) []string {
	var problems []string
	fmt.Println("== files per package")
	perPkg := map[string][]string{}
	for file, key := range a.fileKey {
		perPkg[key] = append(perPkg[key], file)
	}
	for _, key := range sortedKeys(perPkg) {
		files := perPkg[key]
		sort.Strings(files)
		tests := 0
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				tests++
			}
		}
		fmt.Printf("%-10s %3d files (%d tests)\n", key, len(files), tests)
	}

	fmt.Println("\n== package dependencies from non-test files (names behind each edge)")
	graph := map[string]map[string]bool{}
	printEdges := func(edges map[edge]map[string]bool, label string) {
		var keys []edge
		for e := range edges {
			keys = append(keys, e)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].from != keys[j].from {
				return keys[i].from < keys[j].from
			}
			return keys[i].to < keys[j].to
		})
		for _, e := range keys {
			names := sortedKeys(edges[e])
			shown := names
			if !verbose && len(shown) > 12 {
				shown = append(append([]string{}, shown[:12]...), fmt.Sprintf("... (%d more)", len(names)-12))
			}
			fmt.Printf("%s %s -> %s: %d names: %s\n", label, e.from, e.to, len(names), strings.Join(shown, " "))
			if graph[e.from] == nil {
				graph[e.from] = map[string]bool{}
			}
			graph[e.from][e.to] = true
		}
	}
	printEdges(a.edges, "dep ")
	fmt.Println("\n== extra dependencies from test files")
	testOnly := map[edge]map[string]bool{}
	for e, names := range a.testEdges {
		if a.edges[e] == nil {
			testOnly[e] = names
		}
	}
	printEdges(testOnly, "test")

	if verbose {
		fmt.Println("\n== per-file detail (using file: name(declaring file))")
		var keys []edge
		for e := range a.edgeFiles {
			keys = append(keys, e)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].from+keys[i].to < keys[j].from+keys[j].to })
		for _, e := range keys {
			for _, file := range sortedKeys(a.edgeFiles[e]) {
				fmt.Printf("%s -> %s  %s: %s\n", e.from, e.to, file, strings.Join(sortedKeys(a.edgeFiles[e][file]), " "))
			}
		}
	}

	fmt.Println("\n== import cycles")
	cycles := findCycles(graph)
	if len(cycles) == 0 {
		fmt.Println("none")
	}
	for _, cycle := range cycles {
		msg := "cycle: " + strings.Join(cycle, " -> ")
		fmt.Println(msg)
		problems = append(problems, msg)
	}

	fmt.Println("\n== test-only declarations used by tests in another package")
	if len(a.testShared) == 0 {
		fmt.Println("none")
	}
	for _, name := range sortedKeys(a.testShared) {
		msg := fmt.Sprintf("%s used from %s", name, strings.Join(sortedKeys(a.testShared[name]), ", "))
		fmt.Println(msg)
		problems = append(problems, "shared test helper: "+msg)
	}

	fmt.Println("\n== exports")
	perKind := map[string]int{}
	byName := map[string]map[string][]*objInfo{} // package -> new name -> objects
	var renamed []string
	for _, info := range a.objects {
		if info.newName == "" || info.newName == info.key.name {
			continue
		}
		perKind[info.kind]++
		renamed = append(renamed, fmt.Sprintf("%s %s %s -> %s (%s)", info.declPkg, info.kind, info.key.name, info.newName, info.key.file))
		if info.kind == "method" && specialMethods[info.newName] {
			problems = append(problems, fmt.Sprintf("method %s would become %s, which changes fmt/io/json behaviour", info.key.name, info.newName))
		}
	}
	fmt.Printf("package-level %d, methods %d, fields %d\n", perKind["pkg"], perKind["method"], perKind["field"])
	if verbose {
		sort.Strings(renamed)
		for _, line := range renamed {
			fmt.Println("  " + line)
		}
	}
	// Package-level collisions per destination package.
	for _, info := range a.objects {
		if info.kind != "pkg" {
			continue
		}
		name := info.key.name
		if info.newName != "" {
			name = info.newName
		}
		if byName[info.declPkg] == nil {
			byName[info.declPkg] = map[string][]*objInfo{}
		}
		byName[info.declPkg][name] = appendUnique(byName[info.declPkg][name], info)
	}
	for pkg, names := range byName {
		for name, infos := range names {
			if len(infos) > 1 && !sameDeclaredName(infos) {
				problems = append(problems, fmt.Sprintf("collision in %s: %s", pkg, name))
			}
		}
	}
	// Method and field collisions per type.
	problems = append(problems, a.memberCollisions()...)
	// Struct tags for exported fields.
	for key, info := range a.objects {
		if info.kind != "field" || info.newName == "" || info.newName == key.name {
			continue
		}
		tag := a.fieldTags[key]
		if tag == nil {
			problems = append(problems, fmt.Sprintf("no struct field found for %s (%s)", key.name, key.file))
			continue
		}
		if tag.embedded {
			if info.embedType != nil {
				if _, isStruct := info.embedType.Type().Underlying().(*types.Struct); isStruct && tag.embedKind != "pointer" {
					continue // promoted fields encode the same either way
				}
			}
			if !a.allowEmbedded[key.name] {
				problems = append(problems, fmt.Sprintf("embedded field %s (%s) becomes exported and is not a struct value; check its JSON encoding, then pass -allow-embedded", key.name, key.file))
			}
			continue
		}
		if json := tagLookup(tag.tagValue, "json"); json != "" && json != "-" {
			problems = append(problems, fmt.Sprintf("field %s (%s) has json tag %q and would start being encoded once exported", key.name, key.file, json))
		}
		for _, other := range tag.names {
			if token.IsExported(other) && tagLookup(tag.tagValue, "json") == "" {
				problems = append(problems, fmt.Sprintf("field group %v in %s mixes exported names; cannot add json:\"-\"", tag.names, key.file))
			}
		}
	}

	fmt.Println("\n== interface methods implemented across packages")
	if len(a.implements) == 0 {
		fmt.Println("none")
	}
	for _, line := range uniqueSorted(a.implements) {
		fmt.Println(line)
	}

	fmt.Println("\n== files where a package qualifier is shadowed (an import alias is used)")
	if len(a.shadowed) == 0 {
		fmt.Println("none")
	}
	for _, file := range sortedKeys(a.shadowed) {
		fmt.Printf("%s: %s\n", file, strings.Join(sortedKeys(a.shadowed[file]), " "))
	}
	fmt.Println("\n== problems")
	if len(problems) == 0 {
		fmt.Println("none")
	}
	for _, p := range problems {
		fmt.Println(p)
	}
	return problems
}

func appendUnique(list []*objInfo, info *objInfo) []*objInfo {
	for _, existing := range list {
		if existing == info {
			return list
		}
	}
	return append(list, info)
}

// sameDeclaredName reports whether all objects are the same name declared
// once per build configuration (for example a function with a linux and a
// darwin implementation in two files).
func sameDeclaredName(infos []*objInfo) bool {
	for _, info := range infos[1:] {
		if info.key.name != infos[0].key.name {
			return false
		}
	}
	return true
}

func (a *analysis) memberCollisions() []string {
	var problems []string
	seen := map[string]bool{}
	for _, info := range a.objects {
		if info.kind == "pkg" {
			continue
		}
		var recv types.Type
		switch o := info.obj.(type) {
		case *types.Func:
			recv = o.Type().(*types.Signature).Recv().Type()
		default:
			continue
		}
		if p, ok := recv.(*types.Pointer); ok {
			recv = p.Elem()
		}
		named, ok := recv.(*types.Named)
		if !ok {
			continue
		}
		names := map[string]string{}
		for i := 0; i < named.NumMethods(); i++ {
			m := named.Method(i)
			n := m.Name()
			if other := a.objects[a.keyOfObj(m)]; other != nil && other.newName != "" {
				n = other.newName
			}
			if prev, dup := names[n]; dup && prev != m.Name() {
				msg := fmt.Sprintf("method collision on %s: %s and %s -> %s", named.Obj().Name(), prev, m.Name(), n)
				if !seen[msg] {
					problems = append(problems, msg)
					seen[msg] = true
				}
			}
			names[n] = m.Name()
		}
		if st, ok := named.Underlying().(*types.Struct); ok {
			for i := 0; i < st.NumFields(); i++ {
				f := st.Field(i)
				n := f.Name()
				if other := a.objects[a.keyOfObj(f)]; other != nil && other.newName != "" {
					n = other.newName
				}
				if prev, dup := names[n]; dup && prev != f.Name() {
					msg := fmt.Sprintf("field/method collision on %s: %s and %s -> %s", named.Obj().Name(), prev, f.Name(), n)
					if !seen[msg] {
						problems = append(problems, msg)
						seen[msg] = true
					}
				}
				names[n] = f.Name()
			}
		}
	}
	return problems
}

func (a *analysis) keyOfObj(obj types.Object) objKey {
	for key, info := range a.objects {
		if info.obj == obj {
			return key
		}
	}
	return objKey{}
}

func tagLookup(literal, key string) string {
	if literal == "" {
		return ""
	}
	value, err := strconv.Unquote(literal)
	if err != nil {
		return ""
	}
	return reflect.StructTag(value).Get(key)
}

func findCycles(graph map[string]map[string]bool) [][]string {
	// Tarjan's strongly connected components; report each SCC with >1 node
	// (and self loops, which cannot occur here).
	index := 0
	indices := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	var nodes []string
	for n := range graph {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	var strong func(v string)
	strong = func(v string) {
		indices[v] = index
		low[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range sortedKeys(graph[v]) {
			if _, seen := indices[w]; !seen {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], indices[w])
			}
		}
		if low[v] == indices[v] {
			var scc []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			if len(scc) > 1 {
				sort.Strings(scc)
				out = append(out, scc)
			}
		}
	}
	for _, n := range nodes {
		if _, seen := indices[n]; !seen {
			strong(n)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// apply

func (a *analysis) importPath(key string) string {
	t := a.m.targets[key]
	if t.dir == "." {
		return a.modulePath
	}
	return a.modulePath + "/" + t.dir
}
