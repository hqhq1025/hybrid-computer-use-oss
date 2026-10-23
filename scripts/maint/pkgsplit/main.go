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
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"
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
