package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

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
