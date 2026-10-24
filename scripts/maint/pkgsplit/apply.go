package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"golang.org/x/tools/go/ast/astutil"
	goimports "golang.org/x/tools/imports"
)

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
