// Command pkgsplit moves the files of the single computer-use `package main`
// into several packages without changing behaviour.
//
// It type-checks the package (with its tests) once per build configuration
// (linux with the sidecar test tags, darwin, windows), so files behind build
// constraints are covered. For a file-to-package map it then:
//
//   - finds every reference that crosses a package boundary and reports the
//     package dependency graph, its cycles, and the names behind each edge;
//   - exports each unexported name used from another package (also unexported
//     methods an interface in another package requires), using Go initialism
//     style (axFoo -> AXFoo, guiFoo -> GUIFoo) and refusing collisions;
//   - qualifies cross-package references (pkg.Name) and adds the imports;
//   - adds `json:"-"` to struct fields it exports that had no json tag, so
//     encoding/json output stays identical (unexported fields were skipped);
//   - rewrites the package clause and writes each file into its package dir.
//
// Test-only declarations used by tests in another package cannot be fixed by
// exporting; they are reported, and `apply` refuses to run while any remain.
//
// Modes:
//
//	pkgsplit -root computer-use -map package_map.txt -mode analyze
//	pkgsplit -root computer-use -map package_map.txt -mode apply
//	pkgsplit -root computer-use -mode extract -from main.go -to cli_entry.go -decls runCLI,helpText
//
// extract moves top-level declarations between files of the same package
// (used before the split when one file holds declarations for two packages).
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	goimports "golang.org/x/tools/imports"
)

type buildConfig struct {
	name string
	goos string
	tags string
}

var configs = []buildConfig{
	{"linux+sidecar-tags", "linux", "darwin_sidecar_test windows_sidecar_test"},
	{"darwin", "darwin", ""},
	{"windows", "windows", ""},
}

// target describes one destination package.
type target struct {
	dir  string // relative to root; "." is the module root
	name string // package name
}

func main() {
	root := flag.String("root", "computer-use", "Go module directory")
	mapFile := flag.String("map", "", "file-to-package map")
	mode := flag.String("mode", "analyze", "analyze | apply | extract")
	from := flag.String("from", "", "extract: source file")
	to := flag.String("to", "", "extract: destination file")
	decls := flag.String("decls", "", "extract: comma-separated top-level names")
	verbose := flag.Bool("v", false, "analyze: list every cross-package name")
	keepTag := flag.Bool("keep-build-tag", true, "extract: copy the source file's //go:build line to a new destination")
	objName := flag.String("name", "", "rename-object: package-level name to rename")
	newObj := flag.String("to-name", "", "rename-object: new name")
	files := flag.String("files", "", "rename-object, copy: comma-separated files to edit")
	prefix := flag.String("prefix", "", "copy: prefix for copied helper names")
	allowEmbedded := flag.String("allow-embedded", "", "apply: embedded fields known never to be JSON-encoded")
	flag.Parse()

	var err error
	switch *mode {
	case "extract":
		err = extract(*root, *from, *to, strings.Split(*decls, ","), *keepTag)
	case "rename-object":
		err = renameObject(*root, *objName, *newObj, *files)
	case "copy":
		err = copyHelpers(*root, *decls, *to, *prefix, *files)
	case "analyze", "apply":
		err = split(*root, *mapFile, *mode == "apply", *verbose, splitList(*allowEmbedded))
	case "decls":
		err = declGraph(*root)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pkgsplit:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// package map

type packageMap struct {
	targets map[string]target // package key -> target
	exact   map[string]string // file -> package key
	globs   []globRule
}

type globRule struct{ pattern, key string }

// readMap reads lines of the form
//
//	package <key> <dir> <name>
//	<file-or-glob> <key>
//
// Exact file names win over globs; the first matching glob wins. A test file
// without its own rule follows its source file (foo_test.go -> foo.go).
func readMap(file string) (*packageMap, error) {
	m := &packageMap{targets: map[string]target{}, exact: map[string]string{}}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if i := strings.Index(text, "#"); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		switch {
		case len(fields) == 0:
		case fields[0] == "package" && len(fields) == 4:
			m.targets[fields[1]] = target{dir: fields[2], name: fields[3]}
		case len(fields) == 2:
			if _, ok := m.targets[fields[1]]; !ok {
				return nil, fmt.Errorf("%s:%d: unknown package %q", file, line, fields[1])
			}
			if strings.ContainsAny(fields[0], "*?[") {
				m.globs = append(m.globs, globRule{fields[0], fields[1]})
			} else {
				if _, dup := m.exact[fields[0]]; dup {
					return nil, fmt.Errorf("%s:%d: %s mapped twice", file, line, fields[0])
				}
				m.exact[fields[0]] = fields[1]
			}
		default:
			return nil, fmt.Errorf("%s:%d: cannot parse %q", file, line, scanner.Text())
		}
	}
	return m, scanner.Err()
}

func (m *packageMap) lookup(file string) (string, bool) {
	if key, ok := m.exact[file]; ok {
		return key, true
	}
	if strings.HasSuffix(file, "_test.go") {
		if key, ok := m.exact[strings.TrimSuffix(file, "_test.go")+".go"]; ok {
			return key, true
		}
	}
	for _, rule := range m.globs {
		if ok, _ := path.Match(rule.pattern, file); ok {
			return rule.key, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// analysis

type objKey struct {
	file   string
	offset int
	name   string
}

type objInfo struct {
	key       objKey
	obj       types.Object
	kind      string // "pkg", "method", "field"
	declPkg   string
	testOnly  bool
	newName   string
	embedType *types.TypeName // embedded field: its type name
}

type edge struct{ from, to string }

type useSite struct {
	file     string
	offset   int
	ident    string
	obj      objKey
	qualify  string // package key to qualify with, "" for none
	filePkg  string
	scopeHit bool // the qualifier name is shadowed here
}

type analysis struct {
	m          *packageMap
	root       string
	modulePath string
	objects    map[objKey]*objInfo
	needExport map[objKey]bool
	edges      map[edge]map[string]bool
	testEdges  map[edge]map[string]bool
	sites      map[string]map[int]*useSite // file -> offset -> site
	fileKey    map[string]string
	shadowed   map[string]map[string]bool // file -> qualifier -> shadowed
	testShared map[string]map[string]bool // test-only decl -> using packages
	fieldTags  map[objKey]*fieldTag
	special    []string
	implements []string
	configsFor map[string][]string // file -> configs that compiled it
	edgeFiles  map[edge]map[string]map[string]bool
	// allowEmbedded names embedded fields the caller has checked are never
	// JSON-encoded, so exporting them is safe.
	allowEmbedded map[string]bool
	docRenames    map[string]string
}

type fieldTag struct {
	file      string
	insertAt  int // offset after the field type, when there is no tag
	tagStart  int // offset of the tag literal, when there is one
	tagValue  string
	names     []string
	embedded  bool
	embedKind string
}

func split(root, mapFile string, apply, verbose bool, allowEmbedded []string) error {
	if mapFile == "" {
		return fmt.Errorf("-map is required")
	}
	m, err := readMap(mapFile)
	if err != nil {
		return err
	}
	a := &analysis{
		m: m, root: root,
		objects:    map[objKey]*objInfo{},
		needExport: map[objKey]bool{},
		edges:      map[edge]map[string]bool{},
		testEdges:  map[edge]map[string]bool{},
		sites:      map[string]map[int]*useSite{},
		fileKey:    map[string]string{},
		shadowed:   map[string]map[string]bool{},
		testShared: map[string]map[string]bool{},
		fieldTags:  map[objKey]*fieldTag{},
		configsFor: map[string][]string{},
		edgeFiles:  map[edge]map[string]map[string]bool{},
	}
	modData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(modData), "\n") {
		if strings.HasPrefix(line, "module ") {
			a.modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	var unmapped []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		key, ok := m.lookup(name)
		if !ok {
			unmapped = append(unmapped, name)
			continue
		}
		a.fileKey[name] = key
	}
	if len(unmapped) > 0 {
		return fmt.Errorf("files without a package rule (add them to %s):\n  %s",
			mapFile, strings.Join(unmapped, "\n  "))
	}
	for _, cfg := range configs {
		if err := a.load(cfg); err != nil {
			return fmt.Errorf("%s: %w", cfg.name, err)
		}
	}
	a.assignNames()
	a.allowEmbedded = map[string]bool{}
	for _, name := range allowEmbedded {
		a.allowEmbedded[name] = true
	}
	problems := a.report(verbose)
	if !apply {
		return nil
	}
	if len(problems) > 0 {
		return fmt.Errorf("refusing to apply:\n  %s", strings.Join(problems, "\n  "))
	}
	return a.apply()
}

func (a *analysis) load(cfg buildConfig) error {
	absRoot, err := filepath.Abs(a.root)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "GOOS="+cfg.goos, "CGO_ENABLED=0", "GOFLAGS=-mod=vendor")
	var flags []string
	if cfg.tags != "" {
		flags = append(flags, "-tags="+cfg.tags)
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps,
		Dir: absRoot, Env: env, BuildFlags: flags, Tests: true,
	}, ".")
	if err != nil {
		return err
	}
	var pkg *packages.Package
	for _, p := range pkgs {
		if strings.Contains(p.ID, ".test]") {
			pkg = p
		}
	}
	if pkg == nil {
		return fmt.Errorf("no test variant of the package was loaded")
	}
	if len(pkg.Errors) > 0 {
		return fmt.Errorf("type errors: %v", pkg.Errors)
	}
	a.analyzePackage(cfg, pkg)
	return nil
}

func (a *analysis) keyOf(fset *token.FileSet, obj types.Object) objKey {
	pos := fset.Position(obj.Pos())
	return objKey{file: filepath.Base(pos.Filename), offset: pos.Offset, name: obj.Name()}
}

func (a *analysis) qualifierName(key string) string {
	return a.m.targets[key].name
}

func (a *analysis) analyzePackage(cfg buildConfig, pkg *packages.Package) {
	fset := pkg.Fset
	scope := pkg.Types.Scope()
	files := map[*ast.File]string{}
	for _, file := range pkg.Syntax {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		files[file] = name
		a.configsFor[name] = append(a.configsFor[name], cfg.name)
	}

	classify := func(obj types.Object) (string, bool) {
		if obj == nil || obj.Pkg() != pkg.Types {
			return "", false
		}
		switch o := obj.(type) {
		case *types.Func:
			if o.Type().(*types.Signature).Recv() != nil {
				return "method", true
			}
		case *types.Var:
			if o.IsField() {
				return "field", true
			}
		}
		if obj.Parent() == scope {
			return "pkg", true
		}
		return "", false
	}
	record := func(obj types.Object, kind string) *objInfo {
		key := a.keyOf(fset, obj)
		info, ok := a.objects[key]
		if !ok {
			info = &objInfo{key: key, obj: obj, kind: kind,
				declPkg: a.fileKey[key.file], testOnly: strings.HasSuffix(key.file, "_test.go")}
			if v, isVar := obj.(*types.Var); isVar && v.Embedded() {
				t := v.Type()
				if p, isPtr := t.(*types.Pointer); isPtr {
					t = p.Elem()
				}
				if named, isNamed := t.(*types.Named); isNamed {
					info.embedType = named.Obj()
				}
			}
			a.objects[key] = info
		}
		return info
	}

	// Struct field declarations: remember where a json tag would go.
	for file, name := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				tag := &fieldTag{file: name, insertAt: fset.Position(field.Type.End()).Offset, embedded: len(field.Names) == 0}
				if field.Tag != nil {
					tag.tagStart = fset.Position(field.Tag.Pos()).Offset
					tag.tagValue = field.Tag.Value
				}
				idents := field.Names
				if tag.embedded {
					t := field.Type
					if star, isStar := t.(*ast.StarExpr); isStar {
						t = star.X
						tag.embedKind = "pointer"
					}
					switch x := t.(type) {
					case *ast.Ident:
						idents = []*ast.Ident{x}
					case *ast.SelectorExpr:
						idents = []*ast.Ident{x.Sel}
					}
				}
				for _, id := range idents {
					tag.names = append(tag.names, id.Name)
				}
				for _, id := range idents {
					if obj := pkg.TypesInfo.Defs[id]; obj != nil {
						a.fieldTags[a.keyOf(fset, obj)] = tag
					}
				}
			}
			return true
		})
	}

	for file, name := range files {
		filePkg := a.fileKey[name]
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			offset := fset.Position(id.Pos()).Offset
			if obj := pkg.TypesInfo.Defs[id]; obj != nil {
				if kind, ok := classify(obj); ok {
					info := record(obj, kind)
					a.addSite(name, offset, id.Name, info.key, "", filePkg)
				}
			}
			obj := pkg.TypesInfo.Uses[id]
			kind, ok := classify(obj)
			if !ok {
				return true
			}
			info := record(obj, kind)
			qualify := ""
			if info.declPkg != filePkg {
				e := edge{filePkg, info.declPkg}
				names := a.edges
				if strings.HasSuffix(name, "_test.go") {
					names = a.testEdges
				}
				if names[e] == nil {
					names[e] = map[string]bool{}
				}
				names[e][obj.Name()] = true
				if a.edgeFiles[e] == nil {
					a.edgeFiles[e] = map[string]map[string]bool{}
				}
				if a.edgeFiles[e][name] == nil {
					a.edgeFiles[e][name] = map[string]bool{}
				}
				a.edgeFiles[e][name][obj.Name()+"("+info.key.file+")"] = true
				if info.testOnly {
					if a.testShared[info.key.name+" ("+info.key.file+")"] == nil {
						a.testShared[info.key.name+" ("+info.key.file+")"] = map[string]bool{}
					}
					a.testShared[info.key.name+" ("+info.key.file+")"][filePkg+" via "+name] = true
				}
				if !obj.Exported() {
					a.needExport[info.key] = true
				}
				if kind == "pkg" {
					qualify = info.declPkg
					q := a.qualifierName(qualify)
					if inner := scope.Innermost(id.Pos()); inner != nil {
						if _, hit := inner.LookupParent(q, id.Pos()); hit != nil {
							if a.shadowed[name] == nil {
								a.shadowed[name] = map[string]bool{}
							}
							a.shadowed[name][q] = true
						}
					}
				}
			}
			a.addSite(name, offset, id.Name, info.key, qualify, filePkg)
			return true
		})
	}

	// Interfaces whose unexported methods are implemented in another package.
	var named []*types.TypeName
	for _, n := range scope.Names() {
		if tn, ok := scope.Lookup(n).(*types.TypeName); ok && !tn.IsAlias() {
			named = append(named, tn)
		}
	}
	for _, iface := range named {
		it, ok := iface.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		ifacePkg := a.fileKey[a.keyOf(fset, iface).file]
		for _, impl := range named {
			if impl == iface || types.IsInterface(impl.Type()) {
				continue
			}
			implPkg := a.fileKey[a.keyOf(fset, impl).file]
			if implPkg == ifacePkg {
				continue
			}
			if !types.Implements(impl.Type(), it) && !types.Implements(types.NewPointer(impl.Type()), it) {
				continue
			}
			for i := 0; i < it.NumMethods(); i++ {
				method := it.Method(i)
				if method.Exported() {
					continue
				}
				a.implements = append(a.implements, fmt.Sprintf("%s.%s implemented by %s", iface.Name(), method.Name(), impl.Name()))
				a.needExport[record(method, "method").key] = true
				// The concrete method must be renamed too.
				sel, _, _ := types.LookupFieldOrMethod(impl.Type(), true, pkg.Types, method.Name())
				if sel != nil {
					a.needExport[record(sel, "method").key] = true
				}
			}
		}
	}
}

func (a *analysis) addSite(file string, offset int, ident string, key objKey, qualify, filePkg string) {
	if a.sites[file] == nil {
		a.sites[file] = map[int]*useSite{}
	}
	if existing, ok := a.sites[file][offset]; ok {
		// Embedded fields appear as both a field definition and a type
		// use at the same identifier; the type use decides the edit.
		if existing.qualify == "" && qualify != "" {
			existing.qualify = qualify
			existing.obj = key
		} else if existing.obj != key && a.objects[key] != nil && a.objects[key].kind == "pkg" {
			existing.obj = key
		}
		return
	}
	a.sites[file][offset] = &useSite{file: file, offset: offset, ident: ident, obj: key, qualify: qualify, filePkg: filePkg}
}

var initialisms = []string{"ax", "gui", "mcp", "cli", "id", "json", "url", "http", "ui", "rpc", "sdk", "os", "pid", "xml"}

var specialMethods = map[string]bool{
	"String": true, "Error": true, "Format": true, "GoString": true,
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true,
	"UnmarshalText": true, "Unwrap": true, "Is": true, "As": true,
	"Read": true, "Write": true, "Close": true, "Len": true, "Less": true, "Swap": true,
}

func exportedName(name string) string {
	for _, prefix := range initialisms {
		if strings.HasPrefix(name, prefix) {
			rest := name[len(prefix):]
			r, _ := utf8.DecodeRuneInString(rest)
			if rest == "" || unicode.IsUpper(r) || unicode.IsDigit(r) {
				return strings.ToUpper(prefix) + rest
			}
		}
	}
	r, size := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(r)) + name[size:]
}

// assignNames exports what must be exported. Methods are renamed by name
// across all types, so interface satisfaction inside a package is kept.
func (a *analysis) assignNames() {
	methodNames := map[string]bool{}
	for key := range a.needExport {
		if info := a.objects[key]; info.kind == "method" {
			methodNames[info.key.name] = true
		}
	}
	for _, info := range a.objects {
		switch {
		case info.kind == "method" && methodNames[info.key.name]:
			info.newName = exportedName(info.key.name)
		case a.needExport[info.key]:
			info.newName = exportedName(info.key.name)
		}
	}
	// Embedded fields take the new name of their type.
	for _, info := range a.objects {
		if info.embedType == nil {
			continue
		}
		for _, other := range a.objects {
			if other.obj == info.embedType || (other.kind == "pkg" && other.key.name == info.embedType.Name() &&
				other.obj.Pos() == info.embedType.Pos()) {
				if other.newName != "" {
					info.newName = other.newName
				}
			}
		}
	}
}

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

var docCommentStart = regexp.MustCompile(`(?m)^[ \t]*// ([A-Za-z_][A-Za-z0-9_]*) `)

func (a *analysis) apply() error {
	a.docRenames = map[string]string{}
	for _, info := range a.objects {
		if info.kind == "pkg" && info.newName != "" && info.newName != info.key.name {
			a.docRenames[info.key.name] = info.newName
		}
	}
	for _, file := range sortedKeys(a.fileKey) {
		key := a.fileKey[file]
		src := filepath.Join(a.root, file)
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if len(a.configsFor[file]) == 0 {
			return fmt.Errorf("%s was not compiled by any build configuration; move it aside before apply and port it by hand", file)
		}
		type edit struct {
			start, end int
			text       string
		}
		var edits []edit
		imports := map[string]string{} // package key -> qualifier used
		for _, site := range a.sites[file] {
			info := a.objects[site.obj]
			name := site.ident
			if info != nil && info.newName != "" && site.ident == info.key.name {
				name = info.newName
			}
			if site.qualify != "" {
				q := a.qualifierName(site.qualify)
				if a.shadowed[file][q] {
					q += "pkg"
				}
				imports[site.qualify] = q
				name = q + "." + name
			}
			if name != site.ident {
				edits = append(edits, edit{site.offset, site.offset + len(site.ident), name})
			}
		}
		// json:"-" for fields this file declares and the split exports.
		for objKey, tag := range a.fieldTags {
			info := a.objects[objKey]
			if tag.file != file || info == nil || info.kind != "field" || tag.embedded ||
				info.newName == "" || info.newName == objKey.name || tagLookup(tag.tagValue, "json") != "" {
				continue
			}
			if tag.tagValue == "" {
				edits = append(edits, edit{tag.insertAt, tag.insertAt, " `json:\"-\"`"})
			} else {
				value, _ := strconv.Unquote(tag.tagValue)
				edits = append(edits, edit{tag.tagStart, tag.tagStart + len(tag.tagValue), "`" + value + ` json:"-"` + "`"})
			}
		}
		// Doc comments start with the declared name (Go convention); keep
		// that first word in step with an exported name. Other comment text
		// is prose and stays as written.
		for _, m := range docCommentStart.FindAllSubmatchIndex(data, -1) {
			word := string(data[m[2]:m[3]])
			if newName, ok := a.docRenames[word]; ok {
				edits = append(edits, edit{m[2], m[3], newName})
			}
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		var out bytes.Buffer
		last := 0
		for i, e := range edits {
			if i > 0 && e.start < edits[i-1].end {
				return fmt.Errorf("%s: overlapping edits at %d", file, e.start)
			}
			if i > 0 && e.start == edits[i-1].start && e.end == edits[i-1].end {
				continue
			}
			out.Write(data[last:e.start])
			out.WriteString(e.text)
			last = e.end
		}
		out.Write(data[last:])

		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, out.Bytes(), parser.ParseComments)
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		parsed.Name.Name = a.m.targets[key].name
		for _, dep := range sortedKeys(imports) {
			q := imports[dep]
			alias := ""
			if q != path.Base(a.importPath(dep)) {
				alias = q
			}
			astutil.AddNamedImport(fset, parsed, alias, a.importPath(dep))
		}
		var formatted bytes.Buffer
		if err := format.Node(&formatted, fset, parsed); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		// Group imports the way goimports -local does: standard library,
		// third-party modules, then this module's own packages.
		goimports.LocalPrefix = a.modulePath
		grouped, err := goimports.Process(file, formatted.Bytes(), &goimports.Options{
			FormatOnly: true, Comments: true, TabIndent: true, TabWidth: 8,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		dstDir := filepath.Join(a.root, a.m.targets[key].dir)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		dst := filepath.Join(dstDir, file)
		if err := os.WriteFile(dst, grouped, 0o644); err != nil {
			return err
		}
		if filepath.Clean(dst) != filepath.Clean(src) {
			if err := os.Remove(src); err != nil {
				return err
			}
		}
	}
	fmt.Printf("moved %d files into %d packages\n", len(a.fileKey), len(a.m.targets))
	return nil
}

// ---------------------------------------------------------------------------
// decls: top-level declaration dependency graph
//
// Prints "unit <file> <name> <lines>" for every top-level declaration (a
// function, a method as Recv.name, or a whole type/var/const GenDecl) and
// "edge <from-file> <from> <to-file> <to>" for every reference between units,
// over all build configurations. Used to find which declarations must move
// together to break a package cycle.

func declGraph(root string) error {
	units := map[string]string{}
	edges := map[string]bool{}
	for _, cfg := range configs {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		env := append(os.Environ(), "GOOS="+cfg.goos, "CGO_ENABLED=0", "GOFLAGS=-mod=vendor")
		var flags []string
		if cfg.tags != "" {
			flags = append(flags, "-tags="+cfg.tags)
		}
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
			Dir: absRoot, Env: env, BuildFlags: flags, Tests: true,
		}, ".")
		if err != nil {
			return err
		}
		var pkg *packages.Package
		for _, p := range pkgs {
			if strings.Contains(p.ID, ".test]") {
				pkg = p
			}
		}
		if pkg == nil {
			return fmt.Errorf("%s: no test package", cfg.name)
		}
		type span struct {
			file       string
			start, end token.Pos
			name       string
		}
		var spans []span
		for _, file := range pkg.Syntax {
			fname := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
			for _, decl := range file.Decls {
				name := declName(decl)
				if gd, ok := decl.(*ast.GenDecl); ok {
					if gd.Tok == token.IMPORT {
						continue
					}
					if name == "" {
						switch s := gd.Specs[0].(type) {
						case *ast.TypeSpec:
							name = s.Name.Name + "+"
						case *ast.ValueSpec:
							name = s.Names[0].Name + "+"
						}
					}
				}
				spans = append(spans, span{fname, decl.Pos(), decl.End(), name})
				start := pkg.Fset.Position(decl.Pos()).Line
				end := pkg.Fset.Position(decl.End()).Line
				units[fname+" "+name] = fmt.Sprint(end - start + 1)
			}
		}
		owner := func(pos token.Pos) *span {
			for i := range spans {
				if spans[i].start <= pos && pos < spans[i].end {
					return &spans[i]
				}
			}
			return nil
		}
		for id, obj := range pkg.TypesInfo.Uses {
			if obj == nil || obj.Pkg() != pkg.Types {
				continue
			}
			from := owner(id.Pos())
			to := owner(obj.Pos())
			if from == nil || to == nil || from == to {
				continue
			}
			edges[from.file+" "+from.name+" "+to.file+" "+to.name] = true
		}
	}
	for _, key := range sortedKeys(units) {
		fmt.Printf("unit %s %s\n", key, units[key])
	}
	for _, key := range sortedKeys(edges) {
		fmt.Printf("edge %s\n", key)
	}
	return nil
}

// ---------------------------------------------------------------------------
// extract

func extract(root, from, to string, names []string, keepTag bool) error {
	if from == "" || to == "" || len(names) == 0 {
		return fmt.Errorf("extract needs -from, -to and -decls")
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.TrimSpace(n)] = true
	}
	fset := token.NewFileSet()
	srcPath := filepath.Join(root, from)
	file, err := parser.ParseFile(fset, srcPath, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	var moved []ast.Decl
	found := map[string]bool{}
	for _, decl := range file.Decls {
		name := declName(decl)
		if want[name] {
			moved = append(moved, decl)
			found[name] = true
		}
	}
	for n := range want {
		if !found[n] {
			return fmt.Errorf("%s: declaration %s not found", from, n)
		}
	}
	dstPath := filepath.Join(root, to)
	var body bytes.Buffer
	existing, readErr := os.ReadFile(dstPath)
	if readErr == nil {
		body.Write(existing)
	} else {
		if keepTag {
			body.WriteString(buildConstraint(data))
		}
		fmt.Fprintf(&body, "package %s\n", file.Name.Name)
	}
	var cut [][2]int
	for _, decl := range moved {
		start := declStart(fset, decl)
		end := fset.Position(decl.End()).Offset
		body.WriteString("\n")
		body.Write(data[start:end])
		body.WriteString("\n")
		cut = append(cut, [2]int{start, end})
	}
	// Give the destination every import of the source; unused ones are
	// dropped below.
	dstSet := token.NewFileSet()
	dstFile, err := parser.ParseFile(dstSet, dstPath, body.Bytes(), parser.ParseComments)
	if err != nil {
		return fmt.Errorf("%s: %w", to, err)
	}
	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		alias := ""
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		astutil.AddNamedImport(dstSet, dstFile, alias, importPath)
	}
	var merged bytes.Buffer
	if err := format.Node(&merged, dstSet, dstFile); err != nil {
		return err
	}
	var rest bytes.Buffer
	last := 0
	for _, c := range cut {
		rest.Write(data[last:c[0]])
		last = c[1]
	}
	rest.Write(data[last:])
	for _, out := range []struct {
		path string
		data []byte
	}{{dstPath, merged.Bytes()}, {srcPath, rest.Bytes()}} {
		cleaned, err := dropUnusedImports(out.path, out.data)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out.path, cleaned, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("moved %d declarations from %s to %s\n", len(moved), from, to)
	return nil
}

// buildConstraint returns the //go:build line (and its blank line) that
// precedes the package clause, or "".
func buildConstraint(data []byte) string {
	var out strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "package ") {
			break
		}
		if strings.HasPrefix(line, "//go:build ") || strings.HasPrefix(line, "// +build ") {
			out.WriteString(line + "\n")
		}
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	return out.String()
}

func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			return recvName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		if d.Tok == token.IMPORT || len(d.Specs) == 0 {
			return ""
		}
		suffix := ""
		if len(d.Specs) > 1 {
			suffix = "+"
		}
		switch s := d.Specs[0].(type) {
		case *ast.TypeSpec:
			return s.Name.Name + suffix
		case *ast.ValueSpec:
			if len(s.Names) > 1 && suffix == "" {
				suffix = "+"
			}
			return s.Names[0].Name + suffix
		}
	}
	return ""
}

func recvName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	}
	return ""
}

func declStart(fset *token.FileSet, decl ast.Decl) int {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Doc != nil {
			return fset.Position(d.Doc.Pos()).Offset
		}
	case *ast.GenDecl:
		if d.Doc != nil {
			return fset.Position(d.Doc.Pos()).Offset
		}
	}
	return fset.Position(decl.Pos()).Offset
}

func dropUnusedImports(name string, data []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, data, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	for _, spec := range append([]*ast.ImportSpec{}, file.Imports...) {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		alias := ""
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias == "_" || alias == "." {
			continue
		}
		name := alias
		if name == "" {
			name = importedName(importPath)
		}
		if !usesName(file, name) {
			astutil.DeleteNamedImport(fset, file, alias, importPath)
		}
	}
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// importedName is the default package name of an import path: the last
// element, skipping a /vN major-version suffix (".../jsonschema/v6" is
// jsonschema). Every non-standard import in this module follows that rule.
func importedName(importPath string) string {
	parts := strings.Split(importPath, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = parts[len(parts)-2]
	}
	return last
}

// usesName reports whether the file selects anything from package name.
func usesName(file *ast.File, name string) bool {
	used := false
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && id.Obj == nil {
				used = true
			}
		}
		return !used
	})
	return used
}

// ---------------------------------------------------------------------------

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func uniqueSorted(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// shared loading for the single-package edit modes

type loadedConfig struct {
	cfg buildConfig
	pkg *packages.Package
}

func loadAll(root string) ([]loadedConfig, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var out []loadedConfig
	for _, cfg := range configs {
		env := append(os.Environ(), "GOOS="+cfg.goos, "CGO_ENABLED=0", "GOFLAGS=-mod=vendor")
		var flags []string
		if cfg.tags != "" {
			flags = append(flags, "-tags="+cfg.tags)
		}
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
			Dir: absRoot, Env: env, BuildFlags: flags, Tests: true,
		}, ".")
		if err != nil {
			return nil, err
		}
		var pkg *packages.Package
		for _, p := range pkgs {
			if strings.Contains(p.ID, ".test]") {
				pkg = p
			}
		}
		if pkg == nil {
			return nil, fmt.Errorf("%s: no test package", cfg.name)
		}
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("%s: type errors: %v", cfg.name, pkg.Errors)
		}
		out = append(out, loadedConfig{cfg, pkg})
	}
	return out, nil
}

type posKey struct {
	file   string
	offset int
}

func posOf(fset *token.FileSet, pos token.Pos) posKey {
	p := fset.Position(pos)
	return posKey{filepath.Base(p.Filename), p.Offset}
}

type textEdit struct {
	start, end int
	text       string
}

// applyEdits rewrites files in root; edits are keyed by file name.
func applyEdits(root string, edits map[string]map[int]textEdit) error {
	for file, byOffset := range edits {
		path := filepath.Join(root, file)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		offsets := make([]int, 0, len(byOffset))
		for o := range byOffset {
			offsets = append(offsets, o)
		}
		sort.Ints(offsets)
		var out bytes.Buffer
		last := 0
		for _, o := range offsets {
			e := byOffset[o]
			if e.start < last {
				return fmt.Errorf("%s: overlapping edits at %d", file, e.start)
			}
			out.Write(data[last:e.start])
			out.WriteString(e.text)
			last = e.end
		}
		out.Write(data[last:])
		formatted, err := format.Source(out.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// rename-object: type-aware rename of one package-level name

func renameObject(root, nameList, newList, fileList string) error {
	names, newNames := splitList(nameList), splitList(newList)
	if len(names) == 0 || len(names) != len(newNames) {
		return fmt.Errorf("rename-object needs -name and -to-name lists of equal length")
	}
	only := map[string]bool{}
	for _, f := range strings.Split(fileList, ",") {
		if f = strings.TrimSpace(f); f != "" {
			only[f] = true
		}
	}
	loaded, err := loadAll(root)
	if err != nil {
		return err
	}
	edits := map[string]map[int]textEdit{}
	found := map[string]bool{}
	for _, l := range loaded {
		scope := l.pkg.Types.Scope()
		targets := map[types.Object]string{}
		for i, name := range names {
			if scope.Lookup(newNames[i]) != nil {
				return fmt.Errorf("%s: %s already exists", l.cfg.name, newNames[i])
			}
			if target := scope.Lookup(name); target != nil {
				targets[target] = newNames[i]
				found[name] = true
			}
		}
		visit := func(id *ast.Ident, obj types.Object) {
			newName, ok := targets[obj]
			if !ok {
				return
			}
			k := posOf(l.pkg.Fset, id.Pos())
			if len(only) > 0 && !only[k.file] {
				return
			}
			if edits[k.file] == nil {
				edits[k.file] = map[int]textEdit{}
			}
			edits[k.file][k.offset] = textEdit{k.offset, k.offset + len(id.Name), newName}
		}
		for id, obj := range l.pkg.TypesInfo.Defs {
			visit(id, obj)
		}
		for id, obj := range l.pkg.TypesInfo.Uses {
			visit(id, obj)
		}
	}
	for _, name := range names {
		if !found[name] {
			return fmt.Errorf("package-level %s not found", name)
		}
	}
	n := 0
	for _, e := range edits {
		n += len(e)
	}
	fmt.Printf("renamed %s -> %s: %d occurrences in %d files\n", nameList, newList, n, len(edits))
	return applyEdits(root, edits)
}

// ---------------------------------------------------------------------------
// copy: duplicate test helpers under new names for tests that move to a
// higher package
//
// The seeds and every test-only package-level declaration they reach (and
// the methods of copied types) are copied into -to with each copied name
// prefixed by -prefix (recordingPlatformBackend -> transportRecording...).
// References in the -users files are pointed at the copies. The originals
// stay for the tests that remain in the lower package.

func copyHelpers(root, seedList, to, prefix, userList string) error {
	seeds := splitList(seedList)
	users := map[string]bool{}
	for _, u := range splitList(userList) {
		users[u] = true
	}
	if to == "" || prefix == "" || len(users) == 0 {
		return fmt.Errorf("copy needs -to, -prefix and -files")
	}
	loaded, err := loadAll(root)
	if err != nil {
		return err
	}
	// Decls are identified by the position of their first token.
	type declInfo struct {
		file  string
		start int
		end   int
		decl  ast.Decl
		names []string
	}
	var newFileText bytes.Buffer
	var importSpecs []*ast.ImportSpec
	edits := map[string]map[int]textEdit{}
	copiedNames := map[string]string{}
	var constraint string
	for li, l := range loaded {
		fset := l.pkg.Fset
		scope := l.pkg.Types.Scope()
		fileOf := map[*ast.File]string{}
		declsByObj := map[types.Object]*declInfo{}
		methodsByType := map[string][]*declInfo{}
		var all []*declInfo
		srcData := map[string][]byte{}
		for _, f := range l.pkg.Syntax {
			name := filepath.Base(fset.Position(f.Pos()).Filename)
			fileOf[f] = name
			if !strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(fset.Position(f.Pos()).Filename)
			if err != nil {
				return err
			}
			srcData[name] = data
			for _, d := range f.Decls {
				info := &declInfo{file: name, start: declStart(fset, d), end: fset.Position(d.End()).Offset, decl: d}
				switch x := d.(type) {
				case *ast.FuncDecl:
					if x.Recv != nil {
						t := recvName(x.Recv.List[0].Type)
						methodsByType[t] = append(methodsByType[t], info)
						all = append(all, info)
						continue
					}
					declsByObj[l.pkg.TypesInfo.Defs[x.Name]] = info
					info.names = []string{x.Name.Name}
				case *ast.GenDecl:
					for _, spec := range x.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							declsByObj[l.pkg.TypesInfo.Defs[s.Name]] = info
							info.names = append(info.names, s.Name.Name)
						case *ast.ValueSpec:
							for _, n := range s.Names {
								if n.Name != "_" {
									declsByObj[l.pkg.TypesInfo.Defs[n]] = info
									info.names = append(info.names, n.Name)
								}
							}
						}
					}
				}
				all = append(all, info)
			}
		}
		// Closure over test-only package-level declarations.
		chosen := map[*declInfo]bool{}
		var queue []*declInfo
		add := func(info *declInfo) {
			if info != nil && !chosen[info] && !users[info.file] {
				chosen[info] = true
				queue = append(queue, info)
			}
		}
		// Seeds: test-only declarations outside the user files that the
		// user files reference directly. -decls, when given, must be a
		// subset; it documents the expected helpers.
		for id, obj := range l.pkg.TypesInfo.Uses {
			if obj == nil || obj.Parent() != scope {
				continue
			}
			if users[posOf(fset, id.Pos()).file] {
				add(declsByObj[obj])
			}
		}
		if len(queue) == 0 {
			continue
		}
		for _, seed := range seeds {
			if obj := scope.Lookup(seed); obj != nil && declsByObj[obj] != nil && !chosen[declsByObj[obj]] && !users[declsByObj[obj].file] {
				return fmt.Errorf("%s is listed but no user file references it", seed)
			}
		}
		for len(queue) > 0 {
			info := queue[0]
			queue = queue[1:]
			for _, n := range info.names {
				for _, m := range methodsByType[n] {
					add(m)
				}
			}
			ast.Inspect(info.decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					if obj := l.pkg.TypesInfo.Uses[id]; obj != nil && obj.Parent() == scope {
						add(declsByObj[obj])
					}
				}
				return true
			})
		}
		renamed := map[types.Object]string{}
		for obj, info := range declsByObj {
			if chosen[info] {
				newName := prefix + exportedName(obj.Name())
				if scope.Lookup(newName) != nil {
					return fmt.Errorf("%s already exists", newName)
				}
				renamed[obj] = newName
				copiedNames[obj.Name()] = newName
			}
		}
		// An embedded field takes its type's name, so renaming a copied
		// type renames the fields that embed it (in copies and in users).
		for id, obj := range l.pkg.TypesInfo.Defs {
			v, ok := obj.(*types.Var)
			if !ok || !v.Embedded() {
				continue
			}
			t := v.Type()
			if p, isPtr := t.(*types.Pointer); isPtr {
				t = p.Elem()
			}
			named, isNamed := t.(*types.Named)
			if !isNamed {
				continue
			}
			if newName, ok := renamed[named.Obj()]; ok {
				k := posOf(fset, id.Pos())
				if strings.HasSuffix(k.file, "_test.go") {
					renamed[v] = newName
				}
			}
		}
		// User files point at the copies.
		idents := map[*ast.Ident]types.Object{}
		for id, obj := range l.pkg.TypesInfo.Uses {
			idents[id] = obj
		}
		for id, obj := range l.pkg.TypesInfo.Defs {
			if obj != nil {
				idents[id] = obj
			}
		}
		for id, obj := range idents {
			newName, ok := renamed[obj]
			if !ok {
				continue
			}
			k := posOf(fset, id.Pos())
			if !users[k.file] {
				continue
			}
			if edits[k.file] == nil {
				edits[k.file] = map[int]textEdit{}
			}
			edits[k.file][k.offset] = textEdit{k.offset, k.offset + len(id.Name), newName}
		}
		if li > 0 && newFileText.Len() > 0 {
			continue // the copy text is built once, from the first config that has the seeds
		}
		var chosenList []*declInfo
		for _, info := range all {
			if chosen[info] {
				chosenList = append(chosenList, info)
			}
		}
		for _, info := range chosenList {
			data := srcData[info.file]
			if c := buildConstraint(data); c != "" {
				if constraint != "" && constraint != c {
					return fmt.Errorf("copied declarations have different build constraints")
				}
				constraint = c
			}
			type localEdit struct {
				oldLen  int
				newName string
			}
			local := map[int]localEdit{}
			ast.Inspect(info.decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				obj := l.pkg.TypesInfo.Defs[id]
				if obj == nil {
					obj = l.pkg.TypesInfo.Uses[id]
				}
				if newName, ok := renamed[obj]; ok {
					local[fset.Position(id.Pos()).Offset] = localEdit{len(id.Name), newName}
				}
				return true
			})
			offsets := make([]int, 0, len(local))
			for o := range local {
				offsets = append(offsets, o)
			}
			sort.Ints(offsets)
			last := info.start
			newFileText.WriteString("\n")
			for _, o := range offsets {
				newFileText.Write(data[last:o])
				newFileText.WriteString(local[o].newName)
				last = o + local[o].oldLen
			}
			newFileText.Write(data[last:info.end])
			newFileText.WriteString("\n")
		}
		for _, f := range l.pkg.Syntax {
			if strings.HasSuffix(fileOf[f], "_test.go") {
				importSpecs = append(importSpecs, f.Imports...)
			}
		}
	}
	if newFileText.Len() == 0 {
		fmt.Println("no shared test helpers to copy")
		return nil
	}
	var head bytes.Buffer
	head.WriteString(constraint)
	head.WriteString("package main\n\n// Copies of lower-package test helpers for tests in this package.\n// Generated by scripts/maint/split_packages.sh.\n")
	head.Write(newFileText.Bytes())
	dst := filepath.Join(root, to)
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, dst, head.Bytes(), parser.ParseComments)
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", to, err, head.String())
	}
	seenImport := map[string]bool{}
	for _, spec := range importSpecs {
		p, _ := strconv.Unquote(spec.Path.Value)
		alias := ""
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if seenImport[alias+" "+p] {
			continue
		}
		seenImport[alias+" "+p] = true
		astutil.AddNamedImport(fset, parsed, alias, p)
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, parsed); err != nil {
		return err
	}
	cleaned, err := dropUnusedImports(dst, formatted.Bytes())
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, cleaned, 0o644); err != nil {
		return err
	}
	for _, old := range sortedKeys(copiedNames) {
		fmt.Printf("copied %s %s\n", old, copiedNames[old])
	}
	fmt.Printf("copied %d names into %s; pointed %d user files at them\n", len(copiedNames), to, len(edits))
	return applyEdits(root, edits)
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
