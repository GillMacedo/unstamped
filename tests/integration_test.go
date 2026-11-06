// Package tests runs the unstamped command against real go list output and
// real binaries built by the go toolchain on the test machine.
package tests

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	toolPath  string
	buildErr  error
)

// tool builds the unstamped command once per test run.
func tool(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "unstamped-tool")
		if err != nil {
			buildErr = err
			return
		}
		toolPath = filepath.Join(dir, "unstamped")
		if runtime.GOOS == "windows" {
			toolPath += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", toolPath, "../plugin/scripts")
		cmd.Env = goEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildError{string(out)}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return toolPath
}

type buildError struct{ out string }

func (e *buildError) Error() string { return "building unstamped: " + e.out }

// goEnv keeps tests offline and independent of the caller's GOOS and GOARCH.
func goEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "GOOS" || k == "GOARCH" || k == "GOFLAGS" || k == "CGO_ENABLED" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "CGO_ENABLED=0")
	return append(env, extra...)
}

// writeModule writes files into a new module example.com/fx.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/fx\n\ngo 1.22\n"
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// otherOS is a GOOS that isn't the one running the tests.
func otherOS() string {
	if runtime.GOOS == "windows" {
		return "linux"
	}
	return "windows"
}

func fixture(t *testing.T) string {
	return writeModule(t, map[string]string{
		"cmd/app/main.go": `package main

import (
	"fmt"

	"example.com/fx/internal/buildinfo"
	"example.com/fx/internal/ver.v2"
)

var (
	version = "dev"
	commit  string
	date    = mkDate()
	count   = 3
	info    struct{ Version string }
)

type label = string

var tag label = "none"

const channel = "stable"

func mkDate() string { return "unknown" }

func main() {
	local := "x"
	fmt.Println(version, commit, date, count, info, tag, channel, local, buildinfo.Version, ver.Name)
}
`,
		"cmd/app/platform_" + runtime.GOOS + ".go": "package main\n\nvar platformName = \"here\"\n\nfunc init() { _ = platformName }\n",
		"cmd/app/only_" + otherOS() + ".go":        "package main\n\nvar onlyThere = \"x\"\n\nfunc init() { _ = onlyThere }\n",
		"cmd/app/main_test.go":                     "package main\n\nvar testOnly = \"x\"\n",
		"internal/buildinfo/buildinfo.go":          "package buildinfo\n\nvar Version = \"dev\"\n",
		"internal/ver.v2/ver.go":                   "package ver\n\nvar Name = \"dev\"\n",
		"internal/unused/unused.go":                "package unused\n\nvar Version = \"dev\"\n",
		"lib/lib.go":                               "package lib\n\nvar V = \"x\"\n",
	})
}

type result struct {
	code    int
	out     string
	reports []report
}

type report struct {
	Mode    string
	Problem string `json:"inputProblem"`
	Stamps  []struct {
		Target, Value, Status, Severity, Problem, Hint, Where string
	}
}

func run(t *testing.T, dir string, args ...string) result {
	t.Helper()
	cmd := exec.Command(tool(t), args...)
	cmd.Dir = dir
	cmd.Env = goEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	r := result{code: code, out: stdout.String() + stderr.String()}
	for _, a := range args {
		if a == "json" {
			if err := json.Unmarshal(stdout.Bytes(), &r.reports); err != nil {
				t.Fatalf("bad JSON: %v\n%s", err, stdout.String())
			}
		}
	}
	return r
}

func stampOf(t *testing.T, r result, target string) (status, severity, problem, hint string) {
	t.Helper()
	for _, rep := range r.reports {
		for _, s := range rep.Stamps {
			if s.Target == target {
				return s.Status, s.Severity, s.Problem, s.Hint
			}
		}
	}
	t.Fatalf("no stamp %s in output:\n%s", target, r.out)
	return
}

func TestCheckHealthyStaysQuiet(t *testing.T) {
	dir := fixture(t)
	r := run(t, dir, "check", "--ldflags",
		"-s -w -X main.version=1.2.3 -X 'main.commit=abc 123' -X main.tag=v1 "+
			"-X example.com/fx/internal/buildinfo.Version=1.2.3 -X example.com/fx/internal/ver.v2.Name=n -X main.platformName=p",
		"--want", "main.version,main.commit", "./cmd/app")
	if r.code != 0 || strings.Contains(r.out, "ERROR") || strings.Contains(r.out, "WARN") {
		t.Fatalf("healthy flags: exit %d\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "6 of 6 -X flags name a string var") {
		t.Fatalf("missing summary:\n%s", r.out)
	}
}

func TestCheckFindsSilentMistakes(t *testing.T) {
	dir := fixture(t)
	cases := []struct {
		flag, target, problem, hint string
	}{
		{"-X main.Version=1", "main.Version", "no top-level var Version", "did you mean main.version"},
		{"-X main.channel=beta", "main.channel", "is a const", ""},
		{"-X example.com/fx/cmd/app.version=1", "example.com/fx/cmd/app.version", "is the main package", "use -X main.version"},
		{"-X main.local=1", "main.local", "no top-level var local", "local variable in func main"},
		{"-X main.onlyThere=1", "main.onlyThere", "no top-level var onlyThere", "leave out"},
		{"-X main.testOnly=1", "main.testOnly", "no top-level var testOnly", "test file"},
		{"-X main.info.Version=1", "main.info.Version", "not linked into", "struct field"},
		{"-X example.com/fx/internal/buildinfo/.Version=1", "example.com/fx/internal/buildinfo/.Version", "not linked into", "did you mean example.com/fx/internal/buildinfo.Version"},
		{"-X example.com/fx/internal/unused.Version=1", "example.com/fx/internal/unused.Version", "not linked into", "string var Version"},
		{"-X example.com/fx/lib.V=1", "example.com/fx/lib.V", "not linked into", "doesn't import it"},
		{"-X example.com/fx/internal/info.Version=1", "example.com/fx/internal/info.Version", "not linked into", "string var Version: example.com/fx/internal/buildinfo.Version"},
		{"-X main.count=1", "main.count", "isn't a string", ""},
		{"-X main.version", "main.version", "has no '='", ""},
	}
	for _, c := range cases {
		r := run(t, dir, "check", "--format", "json", "--goos", runtime.GOOS, "--ldflags", c.flag, "./cmd/app")
		if r.code != 1 {
			t.Errorf("%s: exit %d, want 1\n%s", c.flag, r.code, r.out)
			continue
		}
		status, sev, problem, hint := stampOf(t, r, c.target)
		if status == "set" || sev != "error" || !strings.Contains(problem, c.problem) || !strings.Contains(hint, c.hint) {
			t.Errorf("%s: got %s %s %q %q", c.flag, status, sev, problem, hint)
		}
	}
}

func TestCheckHonorsGOOS(t *testing.T) {
	dir := fixture(t)
	other := otherOS()
	r := run(t, dir, "check", "--goos", other, "--goarch", "amd64", "--ldflags", "-X main.onlyThere=1", "./cmd/app")
	if r.code != 0 {
		t.Fatalf("var in only_%s.go should be found for GOOS=%s: exit %d\n%s", other, other, r.code, r.out)
	}
	r = run(t, dir, "check", "--goos", other, "--goarch", "amd64", "--ldflags", "-X main.platformName=1", "./cmd/app")
	if r.code != 1 || !strings.Contains(r.out, "leave out") {
		t.Fatalf("platform_%s.go isn't built for GOOS=%s: exit %d\n%s", runtime.GOOS, other, r.code, r.out)
	}
}

func TestCheckValueWarningsAndFailOn(t *testing.T) {
	dir := fixture(t)
	for _, v := range []string{"", "{{.Version}}", "$(VERSION)"} {
		args := []string{"check", "--ldflags", "-X 'main.version=" + v + "'", "./cmd/app"}
		if r := run(t, dir, args...); r.code != 1 || !strings.Contains(r.out, "WARN") {
			t.Errorf("value %q: exit %d\n%s", v, r.code, r.out)
		}
		if r := run(t, dir, append([]string{"check", "--fail-on", "error"}, args[1:]...)...); r.code != 0 {
			t.Errorf("value %q with --fail-on error: exit %d", v, r.code)
		}
	}
	r := run(t, dir, "check", "--ldflags", "-X main.version=1 -X main.version=2", "./cmd/app")
	if r.code != 1 || !strings.Contains(r.out, "more than once") {
		t.Errorf("duplicate: exit %d\n%s", r.code, r.out)
	}
	r = run(t, dir, "check", "--fail-on", "none", "--ldflags", "-X main.Version=1", "./cmd/app")
	if r.code != 0 || !strings.Contains(r.out, "ERROR") {
		t.Errorf("--fail-on none should still print the error and exit 0: exit %d\n%s", r.code, r.out)
	}
	r = run(t, dir, "check", "--want", "main.commit", "--ldflags", "-X main.version=1", "./cmd/app")
	if r.code != 1 || !strings.Contains(r.out, "required by --want") {
		t.Errorf("--want: exit %d\n%s", r.code, r.out)
	}
}

// Inputs that can't be checked must exit 2 and never print a clean summary,
// even with --fail-on none.
func TestCheckUntrustedInputs(t *testing.T) {
	failed := "go: cannot find main module, but found .git/config in /src\n\tto create a module there, run:\n\tgo mod init\n"
	ldflagCases := map[string]string{
		"empty":        "",
		"garbage":      "\x01\x02 zz qq -",
		"cut off":      "-X 'main.version=1.2",
		"failed build": failed,
		"no -X":        "-s -w",
	}
	dir := fixture(t)
	for name, l := range ldflagCases {
		r := run(t, dir, "check", "--fail-on", "none", "--ldflags", l, "./cmd/app")
		assertUntrusted(t, "ldflags "+name, r)
	}

	fileCases := map[string]string{
		"empty":        "",
		"garbage":      "\x00\x01\x02\xff not go",
		"cut off":      "package buildinfo\n\nvar Version = \"dev\"\n\nfunc F() {\n\tif true {\n",
		"failed build": failed,
	}
	for name, body := range fileCases {
		dir := fixture(t)
		if err := os.WriteFile(filepath.Join(dir, "internal/buildinfo/buildinfo.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		r := run(t, dir, "check", "--fail-on", "none", "--ldflags", "-X example.com/fx/internal/buildinfo.Version=1", "./cmd/app")
		assertUntrusted(t, "package file "+name, r)
	}

	for name, pkg := range map[string]string{"library": "./lib", "missing": "./nope", "many": "./..."} {
		r := run(t, dir, "check", "--fail-on", "none", "--ldflags", "-X main.version=1", pkg)
		assertUntrusted(t, "package "+name, r)
	}
}

func assertUntrusted(t *testing.T, name string, r result) {
	t.Helper()
	if r.code != 2 {
		t.Errorf("%s: exit %d, want 2\n%s", name, r.code, r.out)
	}
	if strings.Contains(r.out, " of ") && strings.Contains(r.out, "stamps are in the binary") ||
		strings.Contains(r.out, "name a string var") || strings.Contains(r.out, "  ok ") {
		t.Errorf("%s: printed a clean result for input it can't trust\n%s", name, r.out)
	}
}

type target struct{ goos, goarch string }

var targets = []target{
	{"linux", "amd64"}, {"linux", "386"}, {"linux", "arm64"},
	{"darwin", "arm64"}, {"darwin", "amd64"},
	{"windows", "amd64"}, {"windows", "386"},
}

func build(t *testing.T, dir string, tg target, out string, args ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), out)
	cmd := exec.Command("go", append(append([]string{"build", "-o", bin}, args...), "./cmd/app")...)
	cmd.Dir = dir
	cmd.Env = goEnv("GOOS="+tg.goos, "GOARCH="+tg.goarch)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s/%s: %v\n%s", tg.goos, tg.goarch, err, b)
	}
	return bin
}

const goodFlags = "-X main.version=1.2.3 -X 'main.commit=abc 123' -X example.com/fx/internal/ver.v2.Name=two -X main.tag=v1"

func TestBinaryFormats(t *testing.T) {
	dir := fixture(t)
	for _, tg := range targets {
		bin := build(t, dir, tg, "good", "-ldflags", goodFlags)
		r := run(t, dir, "binary", "--format", "json", "--want", "main.version", bin)
		if r.code != 0 {
			t.Errorf("%s/%s healthy: exit %d\n%s", tg.goos, tg.goarch, r.code, r.out)
			continue
		}
		want := map[string]string{"main.version": "1.2.3", "main.commit": "abc 123", "example.com/fx/internal/ver.v2.Name": "two", "main.tag": "v1"}
		for _, s := range r.reports[0].Stamps {
			if want[s.Target] != s.Value || s.Status != "set" {
				t.Errorf("%s/%s: %s = %q (%s)", tg.goos, tg.goarch, s.Target, s.Value, s.Status)
			}
		}

		bad := build(t, dir, tg, "bad", "-ldflags", "-X main.Version=1 -X main.channel=beta -X main.version=")
		r = run(t, dir, "binary", "--format", "json", bad)
		if r.code != 1 {
			t.Errorf("%s/%s broken: exit %d\n%s", tg.goos, tg.goarch, r.code, r.out)
			continue
		}
		if st, sev, _, hint := stampOf(t, r, "main.Version"); st != "not-set" || sev != "error" || !strings.Contains(hint, "main.version") {
			t.Errorf("%s/%s main.Version: %s %s %q", tg.goos, tg.goarch, st, sev, hint)
		}
		if st, sev, _, _ := stampOf(t, r, "main.channel"); st != "not-set" || sev != "error" {
			t.Errorf("%s/%s main.channel: %s %s", tg.goos, tg.goarch, st, sev)
		}
		if st, sev, _, _ := stampOf(t, r, "main.version"); st != "set" || sev != "warn" {
			t.Errorf("%s/%s empty main.version: %s %s", tg.goos, tg.goarch, st, sev)
		}
	}
}

func TestBinaryWithoutLdflagsInBuildInfo(t *testing.T) {
	dir := fixture(t)
	tg := target{"linux", "amd64"}
	trim := build(t, dir, tg, "trim", "-trimpath", "-ldflags", goodFlags)
	if r := run(t, dir, "binary", trim); r.code != 1 || !strings.Contains(r.out, "--want") {
		t.Errorf("-trimpath without --want must not pass: exit %d\n%s", r.code, r.out)
	}
	if r := run(t, dir, "binary", "--want", "main.version,main.commit", trim); r.code != 0 {
		t.Errorf("-trimpath with satisfied --want: exit %d\n%s", r.code, r.out)
	}
	if r := run(t, dir, "binary", "--want", "main.date", trim); r.code != 1 || !strings.Contains(r.out, "required by --want") {
		t.Errorf("-trimpath with missing --want: exit %d\n%s", r.code, r.out)
	}
	plain := build(t, dir, tg, "plain")
	if r := run(t, dir, "binary", plain); r.code != 1 || !strings.Contains(r.out, "no -X stamps") {
		t.Errorf("no -ldflags: exit %d\n%s", r.code, r.out)
	}
	noX := build(t, dir, tg, "nox", "-ldflags", "-w")
	if r := run(t, dir, "binary", noX); r.code != 1 || !strings.Contains(r.out, "no -X flags") {
		t.Errorf("-ldflags without -X: exit %d\n%s", r.code, r.out)
	}
}

func TestBinaryUntrustedInputs(t *testing.T) {
	dir := fixture(t)
	tmp := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	failed := []byte("# example.com/fx/cmd/app\ncmd/app/main.go:3:2: undefined: nope\n")
	cases := map[string]string{
		"empty":        write("empty", nil),
		"garbage":      write("garbage", bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 4096)),
		"failed build": write("failed", failed),
		"fake ELF":     write("fakeelf", append([]byte("\x7fELF"), failed...)),
		"fake PE":      write("fakepe", append([]byte("MZ"), failed...)),
		"missing":      filepath.Join(tmp, "does-not-exist"),
	}
	for _, tg := range targets {
		good := build(t, dir, tg, "good", "-ldflags", goodFlags)
		b, err := os.ReadFile(good)
		if err != nil {
			t.Fatal(err)
		}
		for _, cut := range []int{len(b) / 10, len(b) / 2, len(b) * 9 / 10, len(b) - 4096, len(b) - 1} {
			name := tg.goos + "-" + tg.goarch + "-cut"
			cases[name+itoa(cut)] = write(name+itoa(cut), b[:cut])
		}
		stripped := build(t, dir, tg, "stripped", "-ldflags", "-s -w "+goodFlags)
		cases[tg.goos+"-"+tg.goarch+" stripped"] = stripped
	}
	for name, p := range cases {
		r := run(t, dir, "binary", "--fail-on", "none", p)
		assertUntrusted(t, name, r)
	}
	// One bad file among good ones still fails the whole run.
	good := build(t, dir, target{"linux", "amd64"}, "good", "-ldflags", goodFlags)
	r := run(t, dir, "binary", "--fail-on", "none", good, cases["empty"])
	if r.code != 2 {
		t.Errorf("good plus empty: exit %d, want 2\n%s", r.code, r.out)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestCheckPatternAndStrayArgs(t *testing.T) {
	dir := fixture(t)
	for _, l := range []string{"all=-X main.version=1", "./cmd/app=-X main.version=1", "example.com/fx/cmd/...=-X main.version=1"} {
		if r := run(t, dir, "check", "--ldflags", l, "./cmd/app"); r.code != 0 {
			t.Errorf("%q matches ./cmd/app: exit %d\n%s", l, r.code, r.out)
		}
	}
	for _, l := range []string{"./lib=-X main.version=1", "std=-X main.version=1"} {
		r := run(t, dir, "check", "--ldflags", l, "./cmd/app")
		if r.code != 1 || !strings.Contains(r.out, "doesn't match") || strings.Contains(r.out, "  ok ") {
			t.Errorf("%q doesn't match ./cmd/app: exit %d\n%s", l, r.code, r.out)
		}
	}
	r := run(t, dir, "check", "--ldflags", "--X main.commit='a b'", "./cmd/app")
	if r.code != 1 || !strings.Contains(r.out, "stray argument") {
		t.Errorf("stray argument: exit %d\n%s", r.code, r.out)
	}
}

// Vars whose initializer runs at program start lose the -X value. The test
// runs a native build of the fixture to prove it, then checks the warning.
func TestCheckWarnsOnRuntimeInit(t *testing.T) {
	dir := writeModule(t, map[string]string{"cmd/app/main.go": `package main

import "fmt"

var h = "x"

var (
	lit    = "dev"
	concat = "de" + "v"
	viaFmt = fmt.Sprint("dev")
	withH  = "dev" + h
)

func main() { fmt.Println(lit, concat, viaFmt, withH) }
`})
	flags := "-X main.lit=SET -X main.concat=SET -X main.viaFmt=SET -X main.withH=SET"
	bin := build(t, dir, target{runtime.GOOS, runtime.GOARCH}, "app", "-ldflags", flags)
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(out)); len(got) != 4 || got[0] != "SET" || got[1] != "SET" || got[2] == "SET" || got[3] == "SET" {
		t.Fatalf("toolchain behaves differently than this test assumes: %q", out)
	}
	r := run(t, dir, "check", "--format", "json", "--ldflags", flags, "./cmd/app")
	if r.code != 1 {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	for target, want := range map[string]string{"main.lit": "", "main.concat": "", "main.viaFmt": "warn", "main.withH": "warn"} {
		if _, sev, _, _ := stampOf(t, r, target); sev != want {
			t.Errorf("%s: severity %q, want %q", target, sev, want)
		}
	}
}

// elfSection returns the file offset and header offset of a section.
func elfSection(t *testing.T, path, name string) (off, size, hdr uint64) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shoff := binary.LittleEndian.Uint64(raw[0x28:])
	shentsize := uint64(binary.LittleEndian.Uint16(raw[0x3a:]))
	for i, s := range f.Sections {
		if s.Name == name {
			return s.Offset, s.Size, shoff + uint64(i)*shentsize
		}
	}
	t.Fatalf("no section %s", name)
	return
}

func TestBinaryDamagedValues(t *testing.T) {
	dir := fixture(t)
	good := build(t, dir, target{"linux", "amd64"}, "good", "-ldflags", goodFlags)
	trim := build(t, dir, target{"linux", "amd64"}, "trim", "-trimpath", "-ldflags", goodFlags)
	for _, src := range []string{good, trim} {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		f, err := elf.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		syms, _ := f.Symbols()
		var addr, size uint64
		for _, s := range syms {
			if s.Name == "main.version.str" {
				addr, size = s.Value, s.Size
			}
		}
		var fileOff uint64
		for _, s := range f.Sections {
			if s.Type != elf.SHT_NOBITS && addr >= s.Addr && addr < s.Addr+s.Size {
				fileOff = s.Offset + addr - s.Addr
			}
		}
		f.Close()
		if addr == 0 || fileOff == 0 {
			t.Fatal("main.version.str not found")
		}
		tmp := t.TempDir()
		zeroed := append([]byte(nil), raw...)
		for i := uint64(0); i+1 < size; i++ {
			zeroed[fileOff+i] = 0
		}
		changed := append([]byte(nil), raw...)
		copy(changed[fileOff:], "9.9.9")
		cases := map[string][]byte{"zeroed": zeroed}
		if src == good {
			cases["changed"] = changed
		}
		_, _, hdr := elfSection(t, src, ".rodata")
		nobits := append([]byte(nil), raw...)
		binary.LittleEndian.PutUint32(nobits[hdr+4:], uint32(elf.SHT_NOBITS))
		cases["rodata nobits"] = nobits
		_, _, dhdr := elfSection(t, src, ".data")
		compressed := append([]byte(nil), raw...)
		flags := binary.LittleEndian.Uint64(compressed[dhdr+8:])
		binary.LittleEndian.PutUint64(compressed[dhdr+8:], flags|uint64(elf.SHF_COMPRESSED))
		cases["data compressed"] = compressed
		for name, b := range cases {
			p := filepath.Join(tmp, strings.ReplaceAll(name, " ", "-"))
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"binary", "--fail-on", "none"}
			if src == trim {
				args = append(args, "--want", "main.version")
			}
			r := run(t, dir, append(args, p)...)
			assertUntrusted(t, filepath.Base(src)+" "+name, r)
			if strings.Contains(r.out, "panic") {
				t.Errorf("%s: panicked\n%s", name, r.out)
			}
		}
	}
}
