package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

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
