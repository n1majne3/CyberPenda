//go:build ignore

// verify-go-moves reports whether package directories hold exactly the Go
// declarations they held at a git base commit. Declarations may move between
// files of the same package. Imports, file names, positions, comments, and
// formatting are ignored. Additions, removals, content changes, build tag
// changes, compiler directive changes, package name changes, and relocated
// init functions or side-effectful package variables are failures.
//
// Usage:
//
//	go run ./scripts/verify-go-moves.go -base 71e7052 ./internal/store
//	go run ./scripts/verify-go-moves.go --self-test
//
// Exit codes are 0 for pass, 1 for verification failure, and 2 for a git,
// read, or parse error. This tool proves declaration moves only. It does not
// infer equivalence for rewritten declarations. File-name build constraints,
// legacy build tags, and per-file import alias bindings need separate review.
// Run package tests in addition to this syntactic check.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type decl struct {
	key   string
	hash  string
	tag   string
	order bool
}

// side holds one tree state: the declarations per package name, the compiler
// directives per package name, the file count per package name, and the
// cross-file layout of order-sensitive declarations per package name.
type side struct {
	groups  map[string][]decl
	dirs    map[string]map[string]int
	files   map[string]int
	order   map[string]map[string]string
	imports map[string]map[string]bool
}

func newSide() *side {
	return &side{
		groups:  map[string][]decl{},
		dirs:    map[string]map[string]int{},
		files:   map[string]int{},
		order:   map[string]map[string]string{},
		imports: map[string]map[string]bool{},
	}
}

func printNode(fset *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, n); err != nil {
		fatal("print node: %v", err)
	}
	return b.String()
}

func hashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func hasSideEffect(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			found = true
			return false
		case *ast.UnaryExpr:
			if v.Op == token.ARROW {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isDirective(text string) bool {
	if strings.HasPrefix(text, "//line ") || strings.HasPrefix(text, "/*line ") {
		return true
	}
	return strings.HasPrefix(text, "//go:") && !strings.HasPrefix(text, "//go:build")
}

func buildTag(fset *token.FileSet, f *ast.File) string {
	for _, g := range f.Comments {
		if g.Pos() > f.Package {
			break
		}
		for _, cm := range g.List {
			t := strings.TrimSpace(cm.Text)
			if strings.HasPrefix(t, "//go:build") {
				return strings.TrimSpace(strings.TrimPrefix(t, "//go:build"))
			}
		}
	}
	return ""
}

// adjacentDirectives maps each non-import declaration index to the directive
// comment group that ends on the line directly above it. Directive groups
// next to an import block stay file level, because import blocks are ignored.
func adjacentDirectives(fset *token.FileSet, f *ast.File) map[int]string {
	out := map[int]string{}
	for i, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		line := fset.Position(d.Pos()).Line
		for _, g := range f.Comments {
			if fset.Position(g.End()).Line != line-1 {
				continue
			}
			var lines []string
			for _, cm := range g.List {
				if isDirective(strings.TrimSpace(cm.Text)) {
					lines = append(lines, strings.TrimSpace(cm.Text))
				}
			}
			if len(lines) > 0 {
				out[i] = strings.Join(lines, "\n")
			}
		}
	}
	return out
}

func declRecords(fset *token.FileSet, d ast.Decl, tag, dir string) []decl {
	prefix := ""
	if dir != "" {
		prefix = dir + "\n"
	}
	rec := func(key string, n ast.Node, order bool) decl {
		return decl{key: key, hash: hashOf(prefix + printNode(fset, n)), tag: tag, order: order}
	}
	switch n := d.(type) {
	case *ast.FuncDecl:
		key := "func " + n.Name.Name
		if n.Recv != nil && len(n.Recv.List) > 0 {
			key = "func (" + printNode(fset, n.Recv.List[0].Type) + ")." + n.Name.Name
		}
		isInit := n.Name.Name == "init" && n.Recv == nil
		return []decl{rec(key, n, isInit)}
	case *ast.GenDecl:
		switch n.Tok {
		case token.TYPE:
			var out []decl
			for _, spec := range n.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					out = append(out, rec("type "+ts.Name.Name, ts, false))
				}
			}
			return out
		case token.CONST, token.VAR:
			var names []string
			effect := false
			for _, spec := range n.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, nm := range vs.Names {
					names = append(names, nm.Name)
				}
				for _, v := range vs.Values {
					if hasSideEffect(v) {
						effect = true
					}
				}
			}
			if len(names) == 0 {
				return nil
			}
			// Grouped and ungrouped forms get different keys, so a change
			// of grouping or of spec order within a block is a failure.
			key := n.Tok.String() + " " + strings.Join(names, ",")
			if n.Lparen.IsValid() {
				key = n.Tok.String() + "block (" + strings.Join(names, ",") + ")"
			}
			return []decl{rec(key, n, n.Tok == token.VAR && effect)}
		}
	}
	return nil
}

// loadSide parses each file twice. The parse without comments yields clean
// nodes for hashing, because attached doc comments would enter the printed
// form. The parse with comments yields build tags and directives.
func loadSide(files [][2]string) *side {
	s := newSide()
	for _, f := range files {
		name, src := f[0], f[1]
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			fatal("parse %s: %v", name, err)
		}
		mfset := token.NewFileSet()
		mfile, err := parser.ParseFile(mfset, name, src, parser.ParseComments)
		if err != nil {
			fatal("parse %s: %v", name, err)
		}
		pkg := file.Name.Name
		s.files[pkg]++
		if s.dirs[pkg] == nil {
			s.dirs[pkg] = map[string]int{}
			s.order[pkg] = map[string]string{}
			s.imports[pkg] = map[string]bool{}
		}
		for _, d := range file.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				for _, spec := range gd.Specs {
					s.imports[pkg][printNode(fset, spec)] = true
				}
			}
		}
		tag := buildTag(mfset, mfile)
		for _, g := range mfile.Comments {
			for _, cm := range g.List {
				t := strings.TrimSpace(cm.Text)
				if isDirective(t) {
					s.dirs[pkg][t]++
				}
			}
		}
		adjacent := adjacentDirectives(mfset, mfile)
		base := filepath.Base(name)
		for i, d := range file.Decls {
			for _, r := range declRecords(fset, d, tag, adjacent[i]) {
				s.groups[pkg] = append(s.groups[pkg], r)
				if r.order {
					s.order[pkg][base] += r.key + " " + r.hash[:10] + "; "
				}
			}
		}
	}
	return s
}

func unionIntKeys(a, b map[string]int) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unionStrKeys(a, b map[string]string) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unionBoolKeys(a, b map[string]bool) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedGroups(m map[string][]decl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func triple(r decl) string {
	return r.key + "\x1f" + r.hash + "\x1f" + r.tag
}

func compareDecls(bs, cs []decl) []string {
	bt, ct := map[string]int{}, map[string]int{}
	for _, r := range bs {
		bt[triple(r)]++
	}
	for _, r := range cs {
		ct[triple(r)]++
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range unionIntKeys(bt, ct) {
		if bt[t] == ct[t] {
			continue
		}
		key := strings.SplitN(t, "\x1f", 2)[0]
		if !seen[key] {
			out = append(out, fmt.Sprintf("changed: %s (content, grouping, build tag, or count differs; base %d, current %d)", key, bt[t], ct[t]))
			seen[key] = true
		}
	}
	return out
}

func compareDirectives(b, c map[string]int) []string {
	var out []string
	for _, t := range unionIntKeys(b, c) {
		if b[t] != c[t] {
			out = append(out, fmt.Sprintf("compiler directive count changed: %s (base %d, current %d)", t, b[t], c[t]))
		}
	}
	return out
}

// compareOrder fails when an init function or a side-effectful package var
// lands in a different file, or changes order within a file. Go runs these
// in file order across a package, so a relocation is not provable as safe.
func compareOrder(b, c map[string]string) []string {
	var out []string
	for _, f := range unionStrKeys(b, c) {
		switch {
		case b[f] == "":
			out = append(out, "order-sensitive init or side-effectful var moved into "+f)
		case c[f] == "":
			out = append(out, "order-sensitive init or side-effectful var moved out of "+f)
		case b[f] != c[f]:
			out = append(out, "order-sensitive init or side-effectful var order changed in "+f)
		}
	}
	return out
}

// compareImports checks the package-level import union. A pure move
// redistributes imports across files but never adds or drops one.
func compareImports(b, c map[string]bool) []string {
	var out []string
	for _, k := range unionBoolKeys(b, c) {
		switch {
		case c[k] && !b[k]:
			out = append(out, "import added: "+k)
		case b[k] && !c[k]:
			out = append(out, "import removed: "+k)
		}
	}
	return out
}

func compare(b, c *side) ([]string, int) {
	var problems []string
	count := 0
	bg, cg := sortedGroups(b.groups), sortedGroups(c.groups)
	for _, g := range bg {
		if _, ok := c.groups[g]; !ok {
			problems = append(problems, "package group removed: "+g)
			continue
		}
		count += len(c.groups[g])
	}
	for _, g := range cg {
		if _, ok := b.groups[g]; !ok {
			problems = append(problems, "package group added: "+g)
		}
	}
	for _, g := range bg {
		cs, ok := c.groups[g]
		if !ok {
			continue
		}
		problems = append(problems, compareDecls(b.groups[g], cs)...)
		problems = append(problems, compareDirectives(b.dirs[g], c.dirs[g])...)
		problems = append(problems, compareOrder(b.order[g], c.order[g])...)
		problems = append(problems, compareImports(b.imports[g], c.imports[g])...)
	}
	return problems, count
}

func gitRun(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		fatal("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String()
}

func baseFiles(root, base, dir string) [][2]string {
	// The path expression <base>:<dir> is the tree of that directory, so
	// ls-tree lists the blobs inside it. An empty dir selects the root tree.
	prefix := ""
	if dir != "" {
		dir += "/"
		prefix = dir
	}
	listing := gitRun(root, "ls-tree", "--name-only", base+":"+dir)
	var names []string
	for _, line := range strings.Split(listing, "\n") {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, ".go") {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	var out [][2]string
	for _, n := range names {
		out = append(out, [2]string{n, gitRun(root, "cat-file", "blob", base+":"+prefix+n)})
	}
	return out
}

func diskFiles(dir string) [][2]string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatal("read dir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out [][2]string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			fatal("read %s: %v", n, err)
		}
		out = append(out, [2]string{n, string(b)})
	}
	return out
}

func fileCount(s *side) int {
	n := 0
	for _, v := range s.files {
		n += v
	}
	return n
}

// selfTest runs the comparison over in-memory snippets. It proves that file
// names, positions, comments, formatting, and per-file import distribution
// are ignored, while a changed function body, a changed raw SQL literal, and
// a changed import union are failures.
func selfTest() bool {
	ok := true
	base := [][2]string{{"a.go", "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar schemaSQL = `CREATE TABLE t (id INTEGER PRIMARY KEY);`\n\nfunc Greet(n string) string {\n\treturn fmt.Sprintf(\"hi %s\", os.Getenv(\"USER\")+n)\n}\n"}}
	moved := [][2]string{
		{"p1.go", "package p\n\nvar schemaSQL = `CREATE TABLE t (id INTEGER PRIMARY KEY);`\n"},
		{"p2.go", "package p\n\n// moved below, blank lines and spaces added\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc Greet(n string) string {\n    return fmt.Sprintf(\"hi %s\", os.Getenv(\"USER\")+n)\n}\n"},
	}
	bodyChanged := [][2]string{{"a.go", "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar schemaSQL = `CREATE TABLE t (id INTEGER PRIMARY KEY);`\n\nfunc Greet(n string) string {\n\treturn fmt.Sprintf(\"hello %s\", os.Getenv(\"USER\")+n)\n}\n"}}
	sqlChanged := [][2]string{{"a.go", "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar schemaSQL = `CREATE TABLE t (id INTEGER);`\n\nfunc Greet(n string) string {\n\treturn fmt.Sprintf(\"hi %s\", os.Getenv(\"USER\")+n)\n}\n"}}
	importAdded := [][2]string{{"a.go", "package p\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"os\"\n)\n\nvar schemaSQL = `CREATE TABLE t (id INTEGER PRIMARY KEY);`\n\nfunc Greet(n string) string {\n\treturn fmt.Sprintf(\"hi %s\", os.Getenv(\"USER\")+n)\n}\n"}}
	run := func(name string, b, c [][2]string, want string) {
		problems, _ := compare(loadSide(b), loadSide(c))
		if want == "" && len(problems) == 0 {
			fmt.Printf("PASS  self-test %s\n", name)
			return
		}
		for _, p := range problems {
			if want != "" && strings.Contains(p, want) {
				fmt.Printf("PASS  self-test %s\n", name)
				return
			}
		}
		ok = false
		fmt.Printf("FAIL  self-test %s: want %q, got %v\n", name, want, problems)
	}
	run("move-ignores-position-imports-format", base, moved, "")
	run("changed-body-fails", base, bodyChanged, "changed: func Greet")
	run("changed-sql-literal-fails", base, sqlChanged, "changed: var schemaSQL")
	run("import-union-change-fails", base, importAdded, "import added")
	run("duplicate-declaration-fails", base, append(moved, [2]string{"duplicate.go", "package p\nvar schemaSQL = `CREATE TABLE t (id INTEGER PRIMARY KEY);`\n"}), "changed: var schemaSQL")
	initializer := "package p\nvar values = append([]int{1}, 2)\n"
	run("builtin-initializer-move-fails", [][2]string{{"a.go", initializer}}, [][2]string{{"b.go", initializer}}, "order-sensitive")
	initialization := "package p\nfunc init() {}\n"
	run("init-move-fails", [][2]string{{"a.go", initialization}}, [][2]string{{"b.go", initialization}}, "order-sensitive")
	return ok
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "verify-go-moves: "+format+"\n", args...)
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./scripts/verify-go-moves.go -base <commit> <package-dir> [package-dir...]")
}

func main() {
	base := flag.String("base", "", "git commit-ish with the reference declarations (required)")
	self := flag.Bool("self-test", false, "run in-memory checks of this verifier and exit")
	flag.Usage = usage
	flag.Parse()
	if *self {
		if !selfTest() {
			os.Exit(1)
		}
		return
	}
	dirs := flag.Args()
	if *base == "" || len(dirs) == 0 {
		usage()
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fatal("%v", err)
	}
	gitRun(root, "rev-parse", "--is-inside-work-tree")
	failed := false
	for _, dir := range dirs {
		rel := filepath.ToSlash(filepath.Clean(dir))
		if rel == "." {
			rel = ""
		}
		b := loadSide(baseFiles(root, *base, rel))
		c := loadSide(diskFiles(dir))
		if len(b.groups) == 0 && len(c.groups) == 0 {
			fatal("%s: no Go files in base commit or worktree", dir)
		}
		problems, count := compare(b, c)
		if len(problems) == 0 {
			sens := 0
			for _, rs := range c.groups {
				for _, r := range rs {
					if r.order {
						sens++
					}
				}
			}
			note := ""
			if sens > 0 {
				note = fmt.Sprintf(", %d order-sensitive init/var (file layout unchanged)", sens)
			}
			fmt.Printf("PASS  %s [%s]: %d declarations, files %d -> %d%s\n",
				dir, strings.Join(sortedGroups(c.groups), ","), count, fileCount(b), fileCount(c), note)
			continue
		}
		failed = true
		fmt.Printf("FAIL  %s\n", dir)
		for i, p := range problems {
			if i == 12 {
				fmt.Printf("      ... and %d more problems\n", len(problems)-i)
				break
			}
			fmt.Printf("      %s\n", p)
		}
	}
	if failed {
		os.Exit(1)
	}
}
