// Command rename_a11y renames internal Go identifiers that still use the
// old "a11y" spelling to the "ax"/"AX" spelling used by the public interface.
//
// It works on the token stream of every non-vendor .go file under -root:
//
//   - IDENT tokens containing "a11y"/"A11y"/"A11Y" are renamed. A leading
//     "a11y" becomes "ax"; any other occurrence becomes "AX" (Go initialism
//     style, matching existing names such as decodeAXRequest).
//   - COMMENT tokens: only whole-word occurrences of identifiers in the
//     rename map are rewritten. Prose such as "the a11y channel" names the
//     public channel value and is kept.
//   - STRING/CHAR tokens: never rewritten in non-test files, because they can
//     be wire names, error text or model-visible text. In _test.go files a
//     whole-word occurrence of a rename-map identifier is rewritten, because
//     such strings are source-contract fragments (see mainGoSource tests).
//     Every literal rewrite is printed so it can be audited.
//
// Before writing anything it refuses to run when a target name already
// exists anywhere in the scanned code (any scope), which rules out both
// redeclaration and silent capture by an existing local of the same name.
//
// Usage:
//
//	go run main.go -root computer-use -mode measure
//	go run main.go -root computer-use -mode map
//	go run main.go -root computer-use -mode apply
package main

import (
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var oldSpelling = regexp.MustCompile(`(?i)a11y`)

type edit struct {
	start, end int
	text       string
}

type sourceFile struct {
	path string
	data []byte
}

func main() {
	root := flag.String("root", "computer-use", "directory to scan")
	mode := flag.String("mode", "measure", "measure | apply | map")
	flag.Parse()

	files, err := loadFiles(*root)
	if err != nil {
		fail(err)
	}
	renames, allIdents := collect(files)
	switch *mode {
	case "map":
		for _, old := range sortedKeys(renames) {
			fmt.Printf("%s\t%s\n", old, renames[old])
		}
	case "measure":
		measure(files, renames)
	case "apply":
		if err := checkCollisions(renames, allIdents); err != nil {
			fail(err)
		}
		if err := apply(files, renames); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown mode %q", *mode))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "rename_a11y:", err)
	os.Exit(1)
}

func loadFiles(root string) ([]sourceFile, error) {
	var files []sourceFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{path: path, data: data})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, err
}

type tok struct {
	kind   token.Token
	offset int
	text   string
}

func tokens(file sourceFile) []tok {
	fset := token.NewFileSet()
	handle := fset.AddFile(file.path, -1, len(file.data))
	var s scanner.Scanner
	s.Init(handle, file.data, func(pos token.Position, msg string) {
		fail(fmt.Errorf("%s: %s", pos, msg))
	}, scanner.ScanComments)
	var out []tok
	for {
		pos, kind, lit := s.Scan()
		if kind == token.EOF {
			return out
		}
		offset := handle.Offset(pos)
		text := lit
		if kind == token.COMMENT || kind == token.STRING || kind == token.CHAR || kind == token.IDENT {
			// lit can differ from the source for raw strings with \r; use
			// the source bytes so offsets and rewrites stay exact.
			text = string(file.data[offset : offset+len(lit)])
		}
		out = append(out, tok{kind: kind, offset: offset, text: text})
	}
}

// overrides names targets that the mechanical rule cannot use because the
// result already names something else. a11yRequest is a service method; its
// mechanical target axRequest is the decoded-request type.
var overrides = map[string]string{
	"a11yRequest": "handleAXRequest",
}

func newName(old string) string {
	if target, ok := overrides[old]; ok {
		return target
	}
	if strings.HasPrefix(old, "a11y") {
		old = "ax" + old[len("a11y"):]
	}
	return oldSpelling.ReplaceAllString(old, "AX")
}

func collect(files []sourceFile) (map[string]string, map[string]bool) {
	renames := map[string]string{}
	all := map[string]bool{}
	for _, file := range files {
		for _, t := range tokens(file) {
			if t.kind != token.IDENT {
				continue
			}
			all[t.text] = true
			if oldSpelling.MatchString(t.text) {
				renames[t.text] = newName(t.text)
			}
		}
	}
	return renames, all
}

func checkCollisions(renames map[string]string, all map[string]bool) error {
	targets := map[string]string{}
	var problems []string
	for _, old := range sortedKeys(renames) {
		target := renames[old]
		if all[target] {
			problems = append(problems, fmt.Sprintf("%s -> %s: target already exists", old, target))
		}
		if previous, ok := targets[target]; ok {
			problems = append(problems, fmt.Sprintf("%s and %s both map to %s", previous, old, target))
		}
		targets[target] = old
	}
	if len(problems) > 0 {
		return fmt.Errorf("name collisions:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func wordPattern(renames map[string]string) *regexp.Regexp {
	keys := sortedKeys(renames)
	// Longest first so a prefix never wins over a longer identifier.
	sort.SliceStable(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = regexp.QuoteMeta(key)
	}
	return regexp.MustCompile(`\b(` + strings.Join(quoted, "|") + `)\b`)
}

func apply(files []sourceFile, renames map[string]string) error {
	if len(renames) == 0 {
		fmt.Println("nothing to rename")
		return nil
	}
	words := wordPattern(renames)
	replaceWords := func(s string) string {
		return words.ReplaceAllStringFunc(s, func(m string) string { return renames[m] })
	}
	changedFiles, identEdits, commentEdits, literalEdits := 0, 0, 0, 0
	for _, file := range files {
		isTest := strings.HasSuffix(file.path, "_test.go")
		var edits []edit
		for _, t := range tokens(file) {
			var replacement string
			switch t.kind {
			case token.IDENT:
				if target, ok := renames[t.text]; ok {
					replacement = target
					identEdits++
				}
			case token.COMMENT:
				if rewritten := replaceWords(t.text); rewritten != t.text {
					replacement = rewritten
					commentEdits++
				}
			case token.STRING, token.CHAR:
				if !isTest {
					continue
				}
				if rewritten := replaceWords(t.text); rewritten != t.text {
					replacement = rewritten
					literalEdits++
					fmt.Printf("literal %s: %s -> %s\n", file.path, t.text, rewritten)
				}
			}
			if replacement != "" {
				edits = append(edits, edit{t.offset, t.offset + len(t.text), replacement})
			}
		}
		if len(edits) == 0 {
			continue
		}
		var out strings.Builder
		last := 0
		for _, e := range edits {
			out.Write(file.data[last:e.start])
			out.WriteString(e.text)
			last = e.end
		}
		out.Write(file.data[last:])
		info, err := os.Stat(file.path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(file.path, []byte(out.String()), info.Mode().Perm()); err != nil {
			return err
		}
		changedFiles++
	}
	fmt.Printf("renamed %d identifiers (%d occurrences), %d comments, %d test literals in %d files\n",
		len(renames), identEdits, commentEdits, literalEdits, changedFiles)
	return nil
}

// measure prints per-file counts of the old spelling by token kind, then
// every string literal that contains it. Literals are the occurrences that
// can be on the wire or visible to the model; they are never renamed in
// non-test files.
func measure(files []sourceFile, renames map[string]string) {
	type counts struct{ ident, literal, comment, renamedWordsInComment int }
	total := counts{}
	fmt.Println("file\tidentifier\tstring_literal\tcomment")
	var literalLines []string
	for _, file := range files {
		c := counts{}
		for _, t := range tokens(file) {
			n := len(oldSpelling.FindAllStringIndex(t.text, -1))
			if n == 0 {
				continue
			}
			switch t.kind {
			case token.IDENT:
				c.ident += n
			case token.STRING, token.CHAR:
				c.literal += n
				line := 1 + strings.Count(string(file.data[:t.offset]), "\n")
				literalLines = append(literalLines, fmt.Sprintf("%s:%d\t%s", file.path, line, truncate(t.text, 140)))
			case token.COMMENT:
				c.comment += n
			}
		}
		if c.ident+c.literal+c.comment == 0 {
			continue
		}
		fmt.Printf("%s\t%d\t%d\t%d\n", file.path, c.ident, c.literal, c.comment)
		total.ident += c.ident
		total.literal += c.literal
		total.comment += c.comment
	}
	fmt.Printf("TOTAL\t%d\t%d\t%d\n", total.ident, total.literal, total.comment)
	fmt.Printf("distinct identifiers to rename: %d\n", len(renames))
	fmt.Println("\nstring literals containing the old spelling (kept in non-test files):")
	for _, line := range literalLines {
		fmt.Println(line)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
