// Command unstamped checks that go build -ldflags "-X importpath.name=value"
// flags really set a variable. The linker ignores an -X flag whose target
// doesn't exist, so a wrong path or name ships a binary with no version stamp.
//
// unstamped check [flags] PACKAGE checks -X flags against source.
// unstamped binary [flags] FILE... reads the stamps from built binaries.
//
// Exit codes: 0 nothing found, 1 findings at or above --fail-on,
// 2 bad usage or an input that can't be checked.
package main

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const version = "0.1.0"

const usage = `unstamped checks that go build -ldflags "-X importpath.name=value" really sets something.

Usage:
  unstamped check  [--ldflags FLAGS] [--dir DIR] [--goos OS] [--goarch ARCH] [--tags TAGS] [--want T,...] PACKAGE
  unstamped binary [--want T,...] FILE...

check reads source through go list, for the GOOS, GOARCH and tags given.
binary reads the build info and symbol table of built Go binaries.

Common flags:
  --format text|json   output format (default text)
  --fail-on warn|error|none
                       lowest severity that exits 1 (default warn)
  --want T,...         targets that must be stamped, like main.version

Exit codes: 0 no findings, 1 findings, 2 bad usage or an input that can't be checked.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version", "version":
		fmt.Fprintln(stdout, "unstamped", version)
		return 0
	case "check", "binary":
	default:
		fmt.Fprintf(stderr, "unstamped: unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	fs := flag.NewFlagSet("unstamped "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "text or json")
	failOn := fs.String("fail-on", "warn", "warn, error or none")
	want := fs.String("want", "", "comma-separated targets that must be stamped")
	ldflags := fs.String("ldflags", "", "the -ldflags string, as passed to go build")
	dir := fs.String("dir", ".", "directory to run go list in")
	goos := fs.String("goos", "", "GOOS to check for (default: go env GOOS)")
	goarch := fs.String("goarch", "", "GOARCH to check for (default: go env GOARCH)")
	tags := fs.String("tags", "", "build tags, as passed to go build -tags")
	ldflagsSet := false
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "ldflags" {
			ldflagsSet = true
		}
	})
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "unstamped: --format must be text or json, not %q\n", *format)
		return 2
	}
	if *failOn != "warn" && *failOn != "error" && *failOn != "none" {
		fmt.Fprintf(stderr, "unstamped: --fail-on must be warn, error or none, not %q\n", *failOn)
		return 2
	}
	var wants []string
	for _, w := range strings.Split(*want, ",") {
		if w = strings.TrimSpace(w); w != "" {
			wants = append(wants, w)
		}
	}

	var reports []*Report
	switch args[0] {
	case "check":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "unstamped check: give exactly one main package, like ./cmd/app")
			return 2
		}
		if !ldflagsSet {
			fmt.Fprintln(stderr, "unstamped check: --ldflags is required; pass the same string you give go build -ldflags")
			return 2
		}
		reports = append(reports, runCheck(checkOptions{
			Dir: *dir, Pkg: fs.Arg(0), Ldflags: *ldflags, GOOS: *goos, GOARCH: *goarch, Tags: *tags, Want: wants,
		}))
	case "binary":
		if fs.NArg() == 0 {
			fmt.Fprintln(stderr, "unstamped binary: give one or more Go binaries")
			return 2
		}
		if ldflagsSet || *goos != "" || *goarch != "" || *tags != "" {
			fmt.Fprintln(stderr, "unstamped binary: --ldflags, --goos, --goarch and --tags are read from the binary; use them with check")
			return 2
		}
		for _, p := range fs.Args() {
			reports = append(reports, runBinary(p, binaryOptions{Want: wants}))
		}
	}

	if *format == "json" {
		if err := writeJSON(stdout, reports); err != nil {
			fmt.Fprintln(stderr, "unstamped:", err)
			return 2
		}
	} else {
		writeText(stdout, reports)
	}

	code := 0
	for _, r := range reports {
		if r.Problem != "" {
			return 2
		}
		errs, warns := r.counts()
		if (*failOn == "warn" && errs+warns > 0) || (*failOn == "error" && errs > 0) {
			code = 1
		}
	}
	return code
}

// Flag is one -X flag from an -ldflags string.
type Flag struct {
	Raw    string // the argument as the linker sees it, "importpath.name=value"
	Target string // "importpath.name"
	Pkg    string // "importpath"
	Name   string // "name"
	Value  string
	Bad    string // why the linker would reject it, if it would
}

// splitQuoted splits an -ldflags string the way cmd/go does: fields are
// separated by spaces, and a field may be wrapped in single or double quotes.
// There are no escapes.
func splitQuoted(s string) ([]string, error) {
	var out []string
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	for {
		for len(s) > 0 && isSpace(s[0]) {
			s = s[1:]
		}
		if s == "" {
			return out, nil
		}
		if s[0] == '"' || s[0] == '\'' {
			q := s[0]
			s = s[1:]
			i := strings.IndexByte(s, q)
			if i < 0 {
				return nil, fmt.Errorf("unterminated %c quote in -ldflags", q)
			}
			out = append(out, s[:i])
			s = s[i+1:]
			continue
		}
		i := 0
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		out = append(out, s[:i])
		s = s[i:]
	}
}

// linkerValueFlags are cmd/link flags that take a separate value. Their value
// is skipped, so "-extldflags -X" can't be read as an -X flag.
var linkerValueFlags = map[string]bool{
	"B": true, "E": true, "H": true, "I": true, "L": true, "R": true, "T": true,
	"buildid": true, "buildmode": true, "capturehostobjs": true, "cpuprofile": true,
	"extar": true, "extld": true, "extldflags": true, "installsuffix": true,
	"libgcc": true, "linkmode": true, "memprofile": true, "memprofilerate": true,
	"o": true, "pluginpath": true, "r": true, "tmpdir": true, "importcfg": true,
	"benchmark": true, "benchmarkprofile": true, "k": true, "strictdups": true,
	"fipso": true, "randlayout": true, "D": true, "debugtextsize": true,
	"debugtramp": true, "funcalign": true, "macos": true, "macsdk": true,
}

// parseLdflags returns the -X flags in an -ldflags string.
func parseLdflags(s string) ([]Flag, error) {
	p, err := parseLdflagsFull(s)
	return p.flags, err
}

type parsedLdflags struct {
	flags   []Flag
	pattern string   // package pattern before the flags, as in all=-X...
	stray   []string // arguments that aren't flags
}

func parseLdflagsFull(s string) (parsedLdflags, error) {
	var p parsedLdflags
	p.pattern, s = stripPattern(s)
	args, err := splitQuoted(s)
	if err != nil {
		return p, err
	}
	var flags []Flag
	defer func() { p.flags = flags }()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			p.stray = append(p.stray, a)
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		val, hasEq := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, hasEq = name[:j], name[j+1:], true
		}
		if name != "X" {
			if linkerValueFlags[name] && !hasEq {
				i++
			}
			continue
		}
		if !hasEq {
			if i+1 >= len(args) {
				flags = append(flags, Flag{Raw: "", Bad: "-X at the end of -ldflags has no importpath.name=value after it"})
				continue
			}
			i++
			val = args[i]
		}
		flags = append(flags, parseX(val))
	}
	p.flags = flags
	return p, nil
}

// stripPattern removes a package pattern prefix, as in -ldflags=all=-X..., or
// -ldflags=./cmd/app=-X..., which cmd/go accepts before the flags.
func stripPattern(s string) (pattern, rest string) {
	t := strings.TrimLeft(s, " \t\n\r")
	if t == "" || t[0] == '-' || t[0] == '"' || t[0] == '\'' {
		return "", s
	}
	end := strings.IndexAny(t, " \t\n\r")
	if end < 0 {
		end = len(t)
	}
	if eq := strings.Index(t[:end], "=-"); eq > 0 {
		return t[:eq], t[eq+1:]
	}
	return "", s
}

// strayStamps reports arguments in -ldflags that aren't flags. cmd/link stops
// reading flags at the first one, so go build fails.
func strayStamps(stray []string) []Stamp {
	var out []Stamp
	for _, a := range stray {
		out = append(out, Stamp{Target: "-ldflags", Status: "not-set", Severity: sevError, noValue: true,
			Problem: fmt.Sprintf("has a stray argument %s that isn't a flag; the linker stops reading flags there and go build fails. Check the quoting", quoteShort(a))})
	}
	return out
}

// parseX splits "importpath.name=value" the way cmd/link does: the target ends
// at the first '=', and the name starts after the last '.' before it.
func parseX(arg string) Flag {
	f := Flag{Raw: arg}
	eq := strings.IndexByte(arg, '=')
	if eq < 0 {
		f.Target = arg
		f.Bad = "has no '=', so go build fails with \"-X flag requires argument of the form importpath.name=value\""
		return f
	}
	f.Target, f.Value = arg[:eq], arg[eq+1:]
	dot := strings.LastIndexByte(f.Target, '.')
	if dot <= 0 || dot == len(f.Target)-1 {
		f.Bad = "has no importpath.name before '=', so the linker can't match it to a variable"
		return f
	}
	f.Pkg, f.Name = f.Target[:dot], f.Target[dot+1:]
	return f
}

// pathToPrefix escapes an import path the way the linker names symbols:
// control characters, space, '%', '"' and DEL everywhere, and '.' after the
// last slash, become %xx.
func pathToPrefix(s string) string {
	slash := strings.LastIndexByte(s, '/')
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || (c == '.' && i > slash) || c == '%' || c == '"' || c >= 0x7F {
			fmt.Fprintf(&b, "%%%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// symbolName is the linker symbol an -X flag writes to.
func symbolName(f Flag) string {
	return pathToPrefix(f.Pkg) + "." + f.Name
}

// valueWarning says why a stamped value looks wrong, or returns "".
func valueWarning(v string) string {
	switch {
	case v == "":
		return "the value is empty, which usually means a CI or Makefile variable wasn't set"
	case strings.Contains(v, "{{"):
		return "the value holds an unexpanded template, " + quoteShort(v)
	case strings.Contains(v, "$(") || strings.Contains(v, "${") || strings.HasPrefix(v, "$"):
		return "the value holds an unexpanded shell or make variable, " + quoteShort(v)
	}
	return ""
}

func quoteShort(v string) string {
	if len(v) > 60 {
		v = v[:57] + "..."
	}
	return fmt.Sprintf("%q", v)
}

// listedPkg is the part of `go list -json` output this tool uses.
type listedPkg struct {
	ImportPath     string
	Name           string
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
	TestGoFiles    []string
	XTestGoFiles   []string
	Standard       bool
	Error          *struct{ Err string }
}

type checkOptions struct {
	Dir     string
	Pkg     string
	Ldflags string
	GOOS    string
	GOARCH  string
	Tags    string
	Want    []string
}

func goEnv(o checkOptions) []string {
	env := os.Environ()
	if o.GOOS != "" {
		env = append(env, "GOOS="+o.GOOS)
	}
	if o.GOARCH != "" {
		env = append(env, "GOARCH="+o.GOARCH)
	}
	return env
}

func runGo(o checkOptions, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = o.Dir
	cmd.Env = goEnv(o)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("go %s failed: %s", strings.Join(args[:1], " "), msg)
	}
	return out, nil
}

func decodePkgs(data []byte) ([]listedPkg, error) {
	var pkgs []listedPkg
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var p listedPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

const listFields = "-json=ImportPath,Name,Dir,GoFiles,CgoFiles,IgnoredGoFiles,TestGoFiles,XTestGoFiles,Standard,Error"

// runCheck resolves every -X flag against the packages that the main package
// links for the chosen GOOS, GOARCH and tags.
func runCheck(o checkOptions) (r *Report) {
	r = &Report{Mode: "check", Input: o.Pkg}
	defer func() {
		if e := recover(); e != nil {
			r.Stamps = nil
			r.Problem = fmt.Sprintf("checking crashed (%v), so the result can't be trusted", e)
		}
	}()
	fail := func(format string, a ...any) *Report {
		r.Problem = fmt.Sprintf(format, a...)
		return r
	}
	parsed, err := parseLdflagsFull(o.Ldflags)
	if err != nil {
		return fail("%v", err)
	}
	flags := parsed.flags
	if len(flags) == 0 {
		return fail("found no -X flags in -ldflags %s, so there is nothing to check", quoteShort(o.Ldflags))
	}

	envOut, err := runGo(o, "env", "GOOS", "GOARCH")
	if err != nil {
		return fail("%v", err)
	}
	if f := strings.Fields(string(envOut)); len(f) == 2 {
		r.GOOS, r.GOARCH = f[0], f[1]
	}

	tagArgs := []string{}
	if o.Tags != "" {
		tagArgs = []string{"-tags", o.Tags}
	}
	rootOut, err := runGo(o, append(append([]string{"list"}, tagArgs...), "-json=ImportPath,Name,Error", "--", o.Pkg)...)
	if err != nil {
		return fail("%v", err)
	}
	roots, err := decodePkgs(rootOut)
	if err != nil {
		return fail("%v", err)
	}
	if len(roots) != 1 {
		return fail("%s matches %d packages; give one main package", o.Pkg, len(roots))
	}
	root := roots[0]
	if root.Error != nil {
		return fail("%s: %s", o.Pkg, root.Error.Err)
	}
	if root.Name != "main" {
		return fail("%s is package %s, not a main package; -X only applies when linking a program", root.ImportPath, root.Name)
	}
	r.Main = root.ImportPath

	depsOut, err := runGo(o, append(append([]string{"list", "-deps"}, tagArgs...), listFields, "--", o.Pkg)...)
	if err != nil {
		return fail("%v", err)
	}
	deps, err := decodePkgs(depsOut)
	if err != nil {
		return fail("%v", err)
	}
	byPath := map[string]*listedPkg{}
	for i := range deps {
		p := &deps[i]
		if p.Error != nil {
			return fail("package %s: %s", p.ImportPath, p.Error.Err)
		}
		byPath[p.ImportPath] = p
	}
	rootPkg := byPath[root.ImportPath]
	if rootPkg == nil {
		return fail("go list -deps didn't include %s", root.ImportPath)
	}
	r.Stamps = append(r.Stamps, strayStamps(parsed.stray)...)
	if pat := parsed.pattern; pat != "" && pat != "all" {
		out, err := runGo(o, append(append([]string{"list"}, tagArgs...), "-json=ImportPath", "--", pat)...)
		if err != nil {
			return fail("checking the -ldflags pattern %q: %v", pat, err)
		}
		matched, err := decodePkgs(out)
		if err != nil {
			return fail("%v", err)
		}
		hit := false
		for _, m := range matched {
			if m.ImportPath == root.ImportPath {
				hit = true
			}
		}
		if !hit {
			for _, f := range flags {
				r.Stamps = append(r.Stamps, Stamp{Target: f.Target, Value: f.Value, Status: "not-set", Severity: sevError,
					Problem: fmt.Sprintf("the -ldflags pattern %q doesn't match %s, so go build doesn't pass these flags to its link", pat, root.ImportPath),
					Hint:    "drop the pattern, or use all= or the main package's path"})
			}
			addWants(r, o.Want)
			return r
		}
	}

	decls := map[string]*pkgDecls{}
	load := func(p *listedPkg) (*pkgDecls, error) {
		if d, ok := decls[p.ImportPath]; ok {
			return d, nil
		}
		d, err := readDecls(p)
		if err != nil {
			return nil, err
		}
		decls[p.ImportPath] = d
		return d, nil
	}

	for _, f := range flags {
		s := Stamp{Target: f.Target, Value: f.Value, Status: "not-set", Severity: sevError}
		if f.Bad != "" {
			s.Problem = f.Bad
			s.noValue = !strings.Contains(f.Raw, "=")
			if f.Target == "" {
				s.Target = "-X"
			}
			r.Stamps = append(r.Stamps, s)
			continue
		}
		var pkg *listedPkg
		if f.Pkg == "main" {
			pkg = rootPkg
		} else if f.Pkg == root.ImportPath {
			s.Problem = fmt.Sprintf("%s is the main package, and the linker names main packages \"main\", so this sets nothing", f.Pkg)
			s.Hint = "use -X main." + f.Name + "=..."
			r.Stamps = append(r.Stamps, s)
			continue
		} else {
			pkg = byPath[f.Pkg]
		}
		if pkg == nil {
			s.Problem, s.Hint = missingPackage(o, f, root.ImportPath, deps, load)
			r.Stamps = append(r.Stamps, s)
			continue
		}
		d, err := load(pkg)
		if err != nil {
			return fail("%v", err)
		}
		where := d.vars[f.Name]
		switch {
		case where != nil && where.problem != "":
			s.Problem = where.problem
			s.Where = where.pos
		case where != nil:
			s.Status, s.Severity, s.Where = "set", "", where.pos
			if w := valueWarning(f.Value); w != "" {
				s.Severity, s.Problem = sevWarn, w
			} else if where.warn != "" {
				s.Severity, s.Problem = sevWarn, where.warn
			}
		default:
			s.Problem, s.Hint = missingVar(f, pkg, d)
		}
		r.Stamps = append(r.Stamps, s)
	}
	addDuplicates(r)
	addWants(r, o.Want)
	return r
}

// addWants adds an error for each wanted target that no -X flag sets.
func addWants(r *Report, want []string) {
	for _, w := range want {
		found := false
		for _, s := range r.Stamps {
			if s.Target == w {
				found = true
			}
		}
		if !found {
			r.Stamps = append(r.Stamps, Stamp{Target: w, Status: "not-set", Severity: sevError,
				Problem: "required by --want, but no -X flag sets it", noValue: true})
		}
	}
}

type declInfo struct {
	pos     string
	problem string // non-empty when -X can't set it
	warn    string // non-empty when package init may overwrite the -X value
}

type pkgDecls struct {
	vars    map[string]*declInfo // top-level vars and consts in compiled files
	other   map[string]string    // name -> where it's declared, outside compiled files or not top-level
	aliases map[string]bool      // type names declared as an alias of string
	consts  map[string]bool      // top-level const names in compiled files
	names   []string             // top-level var names, for suggestions
}

// readDecls parses the files that go list says this build compiles, and the
// files it leaves out, and records every declaration a -X flag could mean.
// A file that doesn't parse is an error: skipping it could hide the var.
func readDecls(p *listedPkg) (*pkgDecls, error) {
	d := &pkgDecls{vars: map[string]*declInfo{}, other: map[string]string{}, aliases: map[string]bool{}, consts: map[string]bool{}}
	fset := token.NewFileSet()
	type parsed struct {
		file     *ast.File
		base     string
		name     string
		compiled bool
	}
	var files []parsed
	add := func(names []string, compiled bool, why string) error {
		for _, n := range names {
			path := filepath.Join(p.Dir, n)
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				if !compiled {
					continue // left-out files may be for other toolchains or tools
				}
				return fmt.Errorf("can't parse %s, so its vars can't be checked: %v", path, err)
			}
			files = append(files, parsed{f, n, n + why, compiled})
		}
		return nil
	}
	if err := add(p.GoFiles, true, ""); err != nil {
		return nil, err
	}
	if err := add(p.CgoFiles, true, ""); err != nil {
		return nil, err
	}
	if len(p.GoFiles)+len(p.CgoFiles) == 0 {
		return nil, fmt.Errorf("package %s has no Go files in this build", p.ImportPath)
	}
	_ = add(p.IgnoredGoFiles, false, ", which this GOOS, GOARCH and tags leave out")
	_ = add(p.TestGoFiles, false, ", a test file, which go build leaves out")
	_ = add(p.XTestGoFiles, false, ", a test file, which go build leaves out")

	for _, pf := range files {
		if !pf.compiled {
			continue
		}
		for _, decl := range pf.file.Decls {
			if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, sp := range g.Specs {
					for _, id := range sp.(*ast.ValueSpec).Names {
						d.consts[id.Name] = true
					}
				}
			}
			if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.TYPE {
				for _, sp := range g.Specs {
					ts := sp.(*ast.TypeSpec)
					if id, ok := ts.Type.(*ast.Ident); ok && id.Name == "string" && ts.Assign.IsValid() {
						d.aliases[ts.Name.Name] = true
					}
				}
			}
		}
	}
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				if decl.Tok != token.VAR && decl.Tok != token.CONST {
					continue
				}
				for _, sp := range decl.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, id := range vs.Names {
						pos := fmt.Sprintf("%s:%d", pf.base, fset.Position(id.Pos()).Line)
						if !pf.compiled {
							if _, seen := d.other[id.Name]; !seen {
								d.other[id.Name] = fmt.Sprintf("%s in %s", kindOf(decl.Tok), pf.name)
							}
							continue
						}
						info := &declInfo{pos: pos}
						if decl.Tok == token.CONST {
							info.problem = "is a const; -X only sets vars, so this sets nothing"
						} else {
							info.problem = typeProblem(vs, i, d.aliases)
							if info.problem == "" {
								info.warn = initWarning(vs, i, d.consts)
							}
							d.names = append(d.names, id.Name)
						}
						d.vars[id.Name] = info
					}
				}
			case *ast.FuncDecl:
				if !pf.compiled || decl.Body == nil {
					continue
				}
				fn := decl.Name.Name
				ast.Inspect(decl.Body, func(n ast.Node) bool {
					var ids []*ast.Ident
					switch n := n.(type) {
					case *ast.DeclStmt:
						if g, ok := n.Decl.(*ast.GenDecl); ok && g.Tok == token.VAR {
							for _, sp := range g.Specs {
								ids = append(ids, sp.(*ast.ValueSpec).Names...)
							}
						}
					case *ast.AssignStmt:
						if n.Tok == token.DEFINE {
							for _, e := range n.Lhs {
								if id, ok := e.(*ast.Ident); ok {
									ids = append(ids, id)
								}
							}
						}
					}
					for _, id := range ids {
						if _, seen := d.other[id.Name]; !seen {
							d.other[id.Name] = fmt.Sprintf("local variable in func %s (%s:%d), and -X only sets top-level vars", fn, pf.base, fset.Position(id.Pos()).Line)
						}
					}
					return true
				})
			}
		}
	}
	sort.Strings(d.names)
	return d, nil
}

func kindOf(t token.Token) string {
	if t == token.CONST {
		return "const"
	}
	return "var"
}

// typeProblem says why the linker would refuse to set this var, from syntax
// alone. It returns "" when the var is a string or the type can't be told.
func typeProblem(vs *ast.ValueSpec, i int, aliases map[string]bool) string {
	if vs.Type != nil {
		if id, ok := vs.Type.(*ast.Ident); ok && (id.Name == "string" || aliases[id.Name]) {
			return ""
		}
		if _, ok := vs.Type.(*ast.SelectorExpr); ok {
			return "" // may be an alias of string from another package; go build reports it if not
		}
		return fmt.Sprintf("has type %s, and -X only sets vars of type string, so go build fails", exprString(vs.Type))
	}
	if i < len(vs.Values) {
		if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind != token.STRING {
			return fmt.Sprintf("is initialized to %s, which isn't a string, so go build fails", lit.Value)
		}
	}
	return ""
}

// initWarning says when a var's initializer may run at program start. The
// linker writes the -X value into the var's static data, and init code that
// runs later replaces it (golang/go#24890). Literals, consts and plain copies
// are initialized statically and are safe.
func initWarning(vs *ast.ValueSpec, i int, consts map[string]bool) string {
	if len(vs.Values) == 0 {
		return ""
	}
	if len(vs.Values) != len(vs.Names) {
		return "is set from a multi-value call; package init may run after the linker and replace the -X value"
	}
	var static func(e ast.Expr, top bool) bool
	static = func(e ast.Expr, top bool) bool {
		switch e := e.(type) {
		case *ast.BasicLit:
			return e.Kind == token.STRING
		case *ast.Ident:
			return top || consts[e.Name]
		case *ast.ParenExpr:
			return static(e.X, top)
		case *ast.BinaryExpr:
			return e.Op == token.ADD && static(e.X, false) && static(e.Y, false)
		}
		return false
	}
	if static(vs.Values[i], true) {
		return ""
	}
	return "is initialized by an expression the compiler may run at program start, which can replace the -X value. Check the built program, or initialize it to a string literal"
}

func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.ArrayType:
		return "[]" + exprString(e.Elt)
	}
	return "a non-string type"
}

// missingVar explains a -X flag whose package is linked but has no such var.
func missingVar(f Flag, p *listedPkg, d *pkgDecls) (problem, hint string) {
	label := f.Pkg
	if f.Pkg == "main" {
		label = "main (" + p.ImportPath + ")"
	}
	problem = fmt.Sprintf("package %s has no top-level var %s, so this sets nothing", label, f.Name)
	if where, ok := d.other[f.Name]; ok {
		return problem, "it is a " + where
	}
	for _, n := range d.names {
		if strings.EqualFold(n, f.Name) {
			return problem, fmt.Sprintf("names are case sensitive: did you mean %s.%s (%s)?", f.Pkg, n, d.vars[n].pos)
		}
	}
	if len(d.names) > 0 {
		hint = "top-level vars there: " + strings.Join(limit(d.names, 6), ", ")
	} else {
		hint = "it has no top-level vars"
	}
	return problem, hint
}

// missingPackage explains a -X flag whose package isn't linked into the build.
func missingPackage(o checkOptions, f Flag, mainPath string, deps []listedPkg, load func(*listedPkg) (*pkgDecls, error)) (problem, hint string) {
	problem = fmt.Sprintf("package %s is not linked into %s for this GOOS, GOARCH and tags, so this sets nothing", f.Pkg, mainPath)
	trimmed := strings.Trim(f.Pkg, "/ ")
	var hints []string
	for i := range deps {
		p := &deps[i]
		if p.ImportPath == f.Pkg {
			continue
		}
		if p.ImportPath == trimmed || strings.EqualFold(p.ImportPath, f.Pkg) {
			hints = append(hints, fmt.Sprintf("did you mean %s.%s?", p.ImportPath, f.Name))
		}
	}
	// "main.build.Version" style: a var with a field.
	if dot := strings.LastIndexByte(f.Pkg, '.'); dot > strings.LastIndexByte(f.Pkg, '/') && dot > 0 {
		base := f.Pkg[:dot]
		if base == "main" || base == mainPath {
			hints = append(hints, "-X can't set a struct field or a nested name, only a top-level string var")
		}
	}
	if len(hints) == 0 {
		var withName []string
		for i := range deps {
			p := &deps[i]
			if p.Standard {
				continue
			}
			d, err := load(p)
			if err != nil {
				continue
			}
			if info, ok := d.vars[f.Name]; ok && info.problem == "" {
				name := p.ImportPath
				if p.ImportPath == mainPath {
					name = "main"
				}
				withName = append(withName, name+"."+f.Name)
			}
		}
		if len(withName) > 0 {
			hints = append(hints, "packages in this build with a string var "+f.Name+": "+strings.Join(limit(withName, 4), ", "))
		}
	}
	if len(hints) == 0 {
		args := []string{"list", "-e", "-json=ImportPath,Name,Error", "--", f.Pkg}
		if o.Tags != "" {
			args = append([]string{"list", "-tags", o.Tags}, args[1:]...)
		}
		if out, err := runGo(o, args...); err == nil {
			if ps, err := decodePkgs(out); err == nil && len(ps) == 1 && ps[0].Error == nil {
				hints = append(hints, "the package exists, but "+mainPath+" doesn't import it in this build")
			}
		}
	}
	return problem, strings.Join(hints, "; ")
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}
	return s
}

type sym struct {
	addr uint64
	size uint64 // 0 when the format doesn't record it
	data bool   // in a data or bss section
}

// image is a loaded Go binary: its symbols, and a way to read memory by address.
type image struct {
	syms    map[string]sym
	read    func(addr, n uint64, needFile bool) ([]byte, error)
	ptrSize int
	order   binary.ByteOrder
}

var errStripped = errors.New("the binary has no symbol table (built with -ldflags=-s, or stripped later)")

type region struct {
	addr, size uint64
	r          io.ReaderAt // nil for zero-filled memory
	fileSize   uint64      // bytes backed by the file, from the start of the region
}

func readRegions(regions []region) func(addr, n uint64, needFile bool) ([]byte, error) {
	return func(addr, n uint64, needFile bool) ([]byte, error) {
		if n > 1<<24 {
			return nil, fmt.Errorf("value at %#x claims %d bytes, which isn't plausible", addr, n)
		}
		for _, rg := range regions {
			if addr < rg.addr || addr-rg.addr >= rg.size || addr-rg.addr+n > rg.size {
				continue
			}
			off := addr - rg.addr
			buf := make([]byte, n)
			end := off + n
			if end > rg.fileSize {
				end = rg.fileSize
			}
			if needFile && (rg.r == nil || end < off+n) {
				return nil, fmt.Errorf("%d bytes at %#x aren't stored in the file, but a stamp's value must be", n, addr)
			}
			if rg.r == nil {
				return buf, nil
			}
			if off < end {
				if _, err := rg.r.ReadAt(buf[:end-off], int64(off)); err != nil {
					return nil, fmt.Errorf("reading %d bytes at %#x: %v", n, addr, err)
				}
			}
			return buf, nil
		}
		return nil, fmt.Errorf("address %#x is outside every section", addr)
	}
}

func openImage(path string) (*image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// The caller keeps the file open for reads; it is small-lived (one run).
	if img, err := openELF(f); err != errNotFormat {
		return img, err
	}
	if img, err := openMachO(f); err != errNotFormat {
		return img, err
	}
	if img, err := openPE(f); err != errNotFormat {
		return img, err
	}
	f.Close()
	return nil, errors.New("not an ELF, Mach-O or PE executable")
}

var errNotFormat = errors.New("not this format")

// checkLength fails when the file is shorter than its headers say, which
// means it was cut off. Some formats parse fine with the tail missing.
func checkLength(f *os.File, need uint64) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if uint64(st.Size()) < need {
		return fmt.Errorf("the file is %d bytes, but its headers describe %d, so it looks cut off", st.Size(), need)
	}
	return nil
}

func magic(f io.ReaderAt) []byte {
	b := make([]byte, 4)
	n, _ := f.ReadAt(b, 0)
	return b[:n]
}

func openELF(f *os.File) (*image, error) {
	if string(magic(f)) != "\x7fELF" {
		return nil, errNotFormat
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return nil, fmt.Errorf("reading ELF: %v", err)
	}
	var need uint64
	for _, s := range ef.Sections {
		if s.Type != elf.SHT_NOBITS && s.Offset+s.FileSize > need {
			need = s.Offset + s.FileSize
		}
	}
	for _, p := range ef.Progs {
		if p.Off+p.Filesz > need {
			need = p.Off + p.Filesz
		}
	}
	if err := checkLength(f, need); err != nil {
		return nil, err
	}
	img := &image{syms: map[string]sym{}, order: ef.ByteOrder, ptrSize: 8}
	if ef.Class == elf.ELFCLASS32 {
		img.ptrSize = 4
	}
	var regions []region
	dataSect := map[int]bool{}
	for i, s := range ef.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Addr == 0 {
			continue
		}
		if s.Flags&elf.SHF_COMPRESSED != 0 {
			return nil, fmt.Errorf("reading ELF: loaded section %s is marked compressed, which a working Go binary never has", s.Name)
		}
		rg := region{addr: s.Addr, size: s.Size}
		if s.Type != elf.SHT_NOBITS {
			rg.r, rg.fileSize = s, s.Size
		}
		regions = append(regions, rg)
		if s.Flags&elf.SHF_WRITE != 0 {
			dataSect[i] = true
		}
	}
	img.read = readRegions(regions)
	syms, err := ef.Symbols()
	if errors.Is(err, elf.ErrNoSymbols) {
		return nil, errStripped
	}
	if err != nil {
		return nil, fmt.Errorf("reading ELF symbols: %v", err)
	}
	for _, s := range syms {
		img.syms[s.Name] = sym{addr: s.Value, size: s.Size, data: dataSect[int(s.Section)]}
	}
	return img, nil
}

func openMachO(f *os.File) (*image, error) {
	m := magic(f)
	if len(m) < 4 {
		return nil, errNotFormat
	}
	le, be := binary.LittleEndian.Uint32(m), binary.BigEndian.Uint32(m)
	ok := false
	for _, x := range []uint32{macho.Magic32, macho.Magic64} {
		if le == x || be == x {
			ok = true
		}
	}
	if !ok {
		return nil, errNotFormat
	}
	mf, err := macho.NewFile(f)
	if err != nil {
		return nil, fmt.Errorf("reading Mach-O: %v", err)
	}
	var need uint64
	for _, l := range mf.Loads {
		if sg, ok := l.(*macho.Segment); ok && sg.Offset+sg.Filesz > need {
			need = sg.Offset + sg.Filesz
		}
	}
	if err := checkLength(f, need); err != nil {
		return nil, err
	}
	img := &image{syms: map[string]sym{}, order: mf.ByteOrder, ptrSize: 8}
	if mf.Magic == macho.Magic32 {
		img.ptrSize = 4
	}
	var regions []region
	dataSect := map[int]bool{}
	for i, s := range mf.Sections {
		rg := region{addr: s.Addr, size: s.Size}
		if s.Flags&0xff != 0x1 && s.Offset != 0 { // 0x1 is S_ZEROFILL
			rg.r, rg.fileSize = s, s.Size
		}
		regions = append(regions, rg)
		if s.Seg == "__DATA" || s.Seg == "__DATA_CONST" {
			dataSect[i+1] = true
		}
	}
	img.read = readRegions(regions)
	if mf.Symtab == nil || len(mf.Symtab.Syms) == 0 {
		return nil, errStripped
	}
	for _, s := range mf.Symtab.Syms {
		img.syms[strings.TrimPrefix(s.Name, "_")] = sym{addr: s.Value, data: dataSect[int(s.Sect)]}
	}
	return img, nil
}

func openPE(f *os.File) (*image, error) {
	if m := magic(f); len(m) < 2 || string(m[:2]) != "MZ" {
		return nil, errNotFormat
	}
	pf, err := pe.NewFile(f)
	if err != nil {
		return nil, fmt.Errorf("reading PE: %v", err)
	}
	var need uint64
	for _, s := range pf.Sections {
		if end := uint64(s.Offset) + uint64(s.Size); s.Size > 0 && end > need {
			need = end
		}
	}
	if pf.PointerToSymbolTable != 0 {
		// 18-byte COFF symbols, then the string table, whose first 4 bytes hold its length.
		if end := uint64(pf.PointerToSymbolTable) + 18*uint64(pf.NumberOfSymbols) + uint64(len(pf.StringTable)); end > need {
			need = end
		}
	}
	if err := checkLength(f, need); err != nil {
		return nil, err
	}
	img := &image{syms: map[string]sym{}, order: binary.LittleEndian, ptrSize: 8}
	var base uint64
	switch h := pf.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		base, img.ptrSize = uint64(h.ImageBase), 4
	case *pe.OptionalHeader64:
		base = h.ImageBase
	default:
		return nil, errors.New("reading PE: no optional header")
	}
	var regions []region
	for _, s := range pf.Sections {
		size := uint64(s.VirtualSize)
		if size == 0 {
			size = uint64(s.Size)
		}
		rg := region{addr: base + uint64(s.VirtualAddress), size: size, r: s, fileSize: uint64(s.Size)}
		if rg.fileSize > size {
			rg.fileSize = size
		}
		regions = append(regions, rg)
	}
	img.read = readRegions(regions)
	if len(pf.Symbols) == 0 {
		return nil, errStripped
	}
	for _, s := range pf.Symbols {
		n := int(s.SectionNumber)
		if n < 1 || n > len(pf.Sections) {
			continue
		}
		sec := pf.Sections[n-1]
		img.syms[s.Name] = sym{
			addr: base + uint64(sec.VirtualAddress) + uint64(s.Value),
			data: sec.Characteristics&pe.IMAGE_SCN_MEM_WRITE != 0,
		}
	}
	return img, nil
}

// stampValue returns the value -X wrote to symbol name, and whether -X wrote one.
// The linker stores the bytes in a symbol named name+".str" and points the
// var's string header at it.
func (img *image) stampValue(name string) (string, bool, error) {
	v, ok := img.syms[name]
	str, ok2 := img.syms[name+".str"]
	if !ok || !ok2 {
		return "", false, nil
	}
	hdr, err := img.read(v.addr, uint64(2*img.ptrSize), false)
	if err != nil {
		return "", true, err
	}
	var n uint64
	if img.ptrSize == 8 {
		n = img.order.Uint64(hdr[8:])
	} else {
		n = uint64(img.order.Uint32(hdr[4:]))
	}
	// ELF records the size, which includes a trailing NUL the linker adds.
	if str.size != 0 && n != str.size && n+1 != str.size {
		return "", true, fmt.Errorf("%s has length %d but its data is %d bytes", name, n, str.size)
	}
	b, err := img.read(str.addr, n, true)
	if err != nil {
		return "", true, err
	}
	// A value comes from a command-line argument, which can't hold a NUL.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", true, fmt.Errorf("the value of %s holds NUL bytes, so the file looks damaged", name)
	}
	return string(b), true, nil
}

// stamps lists every symbol that -X wrote, by the var's symbol name.
func (img *image) stamps() []string {
	var out []string
	for name, s := range img.syms {
		// The linker sets runtime.buildVersion and similar the same way.
		if strings.HasPrefix(name, "runtime.") {
			continue
		}
		if base, ok := strings.CutSuffix(name, ".str"); ok {
			if v, ok := img.syms[base]; ok && v.data && !s.data {
				out = append(out, base)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unescapeSymbol turns a linker symbol name back into "importpath.name".
func unescapeSymbol(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var c byte
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02x", &c); err == nil {
				b.WriteByte(c)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type binaryOptions struct {
	Want []string
}

func runBinary(path string, o binaryOptions) (r *Report) {
	r = &Report{Mode: "binary", Input: path}
	defer func() {
		if e := recover(); e != nil {
			r.Stamps = nil
			r.Problem = fmt.Sprintf("reading the file crashed (%v), so it can't be trusted", e)
		}
	}()
	fail := func(format string, a ...any) *Report {
		r.Problem = fmt.Sprintf(format, a...)
		return r
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fail("can't read Go build info: %v. Is this a complete Go binary?", err)
	}
	r.Main = info.Path
	var ldflags string
	hasLdflags, trimpath := false, false
	for _, s := range info.Settings {
		switch s.Key {
		case "GOOS":
			r.GOOS = s.Value
		case "GOARCH":
			r.GOARCH = s.Value
		case "-ldflags":
			ldflags, hasLdflags = s.Value, true
		case "-trimpath":
			trimpath = s.Value == "true"
		}
	}
	var flags []Flag
	var stray []string
	if hasLdflags {
		parsed, err := parseLdflagsFull(ldflags)
		if err != nil {
			return fail("can't parse the -ldflags in its build info: %v", err)
		}
		flags, stray = parsed.flags, parsed.stray
	}
	lastValue := map[string]string{}
	for _, f := range flags {
		lastValue[f.Target] = f.Value
	}

	img, err := openImage(path)
	if err != nil {
		if err == errStripped {
			return fail("%v, so stamps can't be read from it. Run unstamped check on the source with the same -ldflags, or check a build without -s", err)
		}
		return fail("%v", err)
	}
	if _, ok := img.syms["runtime.main"]; !ok {
		return fail("the symbol table has no runtime.main, so it can't be trusted")
	}

	found := img.stamps()
	foundSet := map[string]bool{}
	for _, s := range found {
		foundSet[s] = true
	}

	if hasLdflags {
		r.Notes = append(r.Notes, "-ldflags from build info: "+ldflags)
		if len(flags) == 0 {
			r.Stamps = append(r.Stamps, Stamp{Target: "-X", Status: "not-set", Severity: sevError, noValue: true,
				Problem: "this binary was built with -ldflags but no -X flags, so nothing is stamped"})
		}
		for _, f := range flags {
			s := Stamp{Target: f.Target, Value: f.Value}
			if f.Bad != "" {
				s.Status, s.Severity, s.Problem = "not-set", sevError, f.Bad
				s.noValue = !strings.Contains(f.Raw, "=")
				r.Stamps = append(r.Stamps, s)
				continue
			}
			name := symbolName(f)
			val, ok, err := img.stampValue(name)
			if err != nil {
				return fail("can't read the value of %s: %v", f.Target, err)
			}
			if !ok {
				s.Status, s.Severity = "not-set", sevError
				s.Problem, s.Hint = missingInBinary(img, f, info.Path)
			} else {
				if val != lastValue[f.Target] {
					return fail("%s holds %s, but the build info says -ldflags set it to %s, so the file looks modified or damaged", f.Target, quoteShort(val), quoteShort(lastValue[f.Target]))
				}
				s.Status, s.Value = "set", val
				delete(foundSet, name)
				if w := valueWarning(val); w != "" {
					s.Severity, s.Problem = sevWarn, w
				}
			}
			r.Stamps = append(r.Stamps, s)
		}
		addDuplicates(r)
		r.Stamps = append(r.Stamps, strayStamps(stray)...)
	} else {
		if trimpath {
			r.Notes = append(r.Notes, "built with -trimpath, which leaves -ldflags out of build info; listing the stamps found in the symbol table")
		} else {
			r.Notes = append(r.Notes, "build info has no -ldflags; listing the stamps found in the symbol table")
		}
		for _, name := range found {
			val, _, err := img.stampValue(name)
			if err != nil {
				return fail("can't read the value of %s: %v", name, err)
			}
			s := Stamp{Target: unescapeSymbol(name), Value: val, Status: "set"}
			if w := valueWarning(val); w != "" {
				s.Severity, s.Problem = sevWarn, w
			}
			r.Stamps = append(r.Stamps, s)
		}
		if len(found) == 0 {
			r.Stamps = append(r.Stamps, Stamp{Target: "-X", Status: "not-set", Severity: sevError, noValue: true,
				Problem: "no -X stamps in this binary"})
		} else if len(o.Want) == 0 {
			// Without the -ldflags, a flag that set nothing leaves no trace,
			// so a list of found stamps alone can't be called complete.
			r.Stamps = append(r.Stamps, Stamp{Target: "-ldflags", Status: "unknown", Severity: sevWarn, noValue: true,
				Problem: "unknown, so an -X flag that set nothing can't be seen here",
				Hint:    "pass --want with the targets you expect, like --want main.version,main.commit"})
		}
	}

	for _, w := range o.Want {
		have := false
		for _, s := range r.Stamps {
			if s.Target == w && s.Status == "set" {
				have = true
			}
		}
		if !have {
			already := false
			for _, s := range r.Stamps {
				if s.Target == w {
					already = true
				}
			}
			if !already {
				r.Stamps = append(r.Stamps, Stamp{Target: w, Status: "not-set", Severity: sevError, noValue: true,
					Problem: "required by --want, but the binary has no -X value for it"})
			}
		}
	}
	return r
}

// missingInBinary explains an -X flag with no value in the binary.
func missingInBinary(img *image, f Flag, mainPath string) (problem, hint string) {
	name := symbolName(f)
	if v, ok := img.syms[name]; ok && v.data {
		return "the var is in the binary, but -X didn't set it", ""
	}
	problem = "no var " + f.Target + " in this binary: the path or name is wrong, it's a const, or nothing uses it and the linker dropped it"
	if f.Pkg == mainPath {
		return problem, "this is the main package; use -X main." + f.Name + "=..."
	}
	var near []string
	for s, v := range img.syms {
		if !v.data {
			continue
		}
		dot := strings.LastIndexByte(s, '.')
		if dot < 0 || strings.HasPrefix(s, "runtime.") || strings.HasPrefix(s, "go:") || strings.HasPrefix(s, "type:") {
			continue
		}
		if strings.EqualFold(s[dot+1:], f.Name) || strings.EqualFold(s, name) {
			near = append(near, unescapeSymbol(s))
		}
	}
	sort.Strings(near)
	if len(near) > 0 {
		hint = "vars with that name in the binary: " + strings.Join(limit(near, 4), ", ")
	}
	return problem, hint
}

const (
	sevError = "error"
	sevWarn  = "warn"
)

// Stamp is one -X target and what happened to it.
type Stamp struct {
	Target   string `json:"target"`
	Value    string `json:"value"`
	Status   string `json:"status"` // "set", "not-set", "unknown"
	Severity string `json:"severity,omitempty"`
	Problem  string `json:"problem,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Where    string `json:"where,omitempty"`
	noValue  bool   // the flag had no "=value" part
}

// Report is the result for one package or one binary.
type Report struct {
	Mode    string   `json:"mode"` // "check" or "binary"
	Input   string   `json:"input"`
	Main    string   `json:"main,omitempty"`
	GOOS    string   `json:"goos,omitempty"`
	GOARCH  string   `json:"goarch,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Stamps  []Stamp  `json:"stamps"`
	Problem string   `json:"inputProblem,omitempty"` // set when the input can't be trusted
}

func (r *Report) counts() (errs, warns int) {
	for _, s := range r.Stamps {
		switch s.Severity {
		case sevError:
			errs++
		case sevWarn:
			warns++
		}
	}
	return
}

// addDuplicates warns about targets set more than once.
func addDuplicates(r *Report) {
	seen := map[string]int{}
	for _, s := range r.Stamps {
		seen[s.Target]++
	}
	for i := range r.Stamps {
		s := &r.Stamps[i]
		if seen[s.Target] > 1 && s.Severity == "" {
			s.Severity = sevWarn
			s.Problem = "set more than once in -ldflags; the linker keeps only one value"
		}
	}
}

func writeText(w io.Writer, reports []*Report) {
	for i, r := range reports {
		if i > 0 {
			fmt.Fprintln(w)
		}
		head := r.Input
		var meta []string
		if r.GOOS != "" {
			meta = append(meta, r.GOOS+"/"+r.GOARCH)
		}
		if r.Main != "" && r.Main != r.Input {
			meta = append(meta, r.Main)
		}
		if len(meta) > 0 {
			head += " (" + strings.Join(meta, ", ") + ")"
		}
		fmt.Fprintln(w, head)
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		if r.Problem != "" {
			fmt.Fprintf(w, "  CAN'T CHECK  %s\n", r.Problem)
			continue
		}
		for _, s := range r.Stamps {
			label := "ok"
			switch {
			case s.Severity == sevError:
				label = "ERROR"
			case s.Severity == sevWarn:
				label = "WARN"
			case s.Status == "unknown":
				label = "?"
			}
			line := fmt.Sprintf("  %-5s %s", label, s.Target)
			if !s.noValue {
				line += fmt.Sprintf(" = %q", s.Value)
			}
			if s.Where != "" {
				line += "  (" + s.Where + ")"
			}
			fmt.Fprintln(w, line)
			if s.Problem != "" {
				fmt.Fprintf(w, "        %s\n", s.Problem)
			}
			if s.Hint != "" {
				fmt.Fprintf(w, "        %s\n", s.Hint)
			}
		}
		errs, warns := r.counts()
		switch {
		case errs == 0 && warns == 0:
			if r.Mode == "binary" {
				fmt.Fprintf(w, "  %d of %d stamps are in the binary with the values shown.\n", len(r.Stamps), len(r.Stamps))
			} else {
				fmt.Fprintf(w, "  %d of %d -X flags name a string var that this build links.\n", len(r.Stamps), len(r.Stamps))
			}
		default:
			fmt.Fprintf(w, "  %s, %s.\n", plural(errs, "error"), plural(warns, "warning"))
		}
	}
}

func writeJSON(w io.Writer, reports []*Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reports)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
