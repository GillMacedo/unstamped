package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestSplitQuoted(t *testing.T) {
	cases := map[string][]string{
		``:                                   nil,
		`-s -w`:                              {"-s", "-w"},
		`-X 'main.date=Fri Nov  6' -X a.b=c`: {"-X", "main.date=Fri Nov  6", "-X", "a.b=c"},
		`  -X "main.v=1 2"  `:                {"-X", "main.v=1 2"},
		"-X\tmain.v=1\n-s":                   {"-X", "main.v=1", "-s"},
	}
	for in, want := range cases {
		got, err := splitQuoted(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{`-X 'main.v=1`, `-X "main.v=1`} {
		if _, err := splitQuoted(bad); err == nil {
			t.Errorf("%q: want an unterminated quote error", bad)
		}
	}
}

func TestParseLdflags(t *testing.T) {
	flags, err := parseLdflags(`-s -w -X main.version=1.0 --X=main.commit=abc -extldflags -X -X 'example.com/a/b.c.v2.Name=x=y'`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range flags {
		got = append(got, f.Pkg+"|"+f.Name+"|"+f.Value)
	}
	want := []string{"main|version|1.0", "main|commit|abc", "example.com/a/b.c.v2|Name|x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseLdflagsPattern(t *testing.T) {
	for _, in := range []string{"all=-X main.version=1", "./cmd/app=-s -X main.version=1"} {
		flags, err := parseLdflags(in)
		if err != nil || len(flags) != 1 || flags[0].Target != "main.version" {
			t.Errorf("%q: got %+v, %v", in, flags, err)
		}
	}
}

func TestParseLdflagsMalformed(t *testing.T) {
	for _, in := range []string{"-X main.version", "-X", "-X =1", "-X main.=1", "-X version=1"} {
		flags, err := parseLdflags(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(flags) != 1 || flags[0].Bad == "" {
			t.Errorf("%q: want one malformed flag, got %+v", in, flags)
		}
	}
}

func TestPathToPrefix(t *testing.T) {
	cases := map[string]string{
		"main":                     "main",
		"gopkg.in/yaml.v3":         "gopkg.in/yaml%2ev3",
		"example.com/a.b/c":        "example.com/a.b/c",
		"example.com/with space/x": "example.com/with%20space/x",
	}
	for in, want := range cases {
		if got := pathToPrefix(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
		if back := unescapeSymbol(pathToPrefix(in) + ".V"); back != in+".V" {
			t.Errorf("%q: round trip gave %q", in, back)
		}
	}
}

func TestValueWarning(t *testing.T) {
	for _, v := range []string{"", "{{.Version}}", "$(VERSION)", "${GIT_SHA}", "$VERSION"} {
		if valueWarning(v) == "" {
			t.Errorf("%q: want a warning", v)
		}
	}
	for _, v := range []string{"1.2.3", "v0.1.0-3-gabc", "2026-11-06T00:00:00Z", "a$b"} {
		if w := valueWarning(v); w != "" {
			t.Errorf("%q: unexpected warning %q", v, w)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"nope"},
		{"check"},
		{"check", "./x"},
		{"check", "--ldflags", "-X main.v=1"},
		{"check", "--ldflags", "-X main.v=1", "--format", "xml", "./x"},
		{"check", "--ldflags", "-X main.v=1", "--fail-on", "high", "./x"},
		{"binary"},
		{"binary", "--ldflags", "-X main.v=1", "f"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	var out bytes.Buffer
	if code := run([]string{"--version"}, &out, &out); code != 0 || !strings.Contains(out.String(), version) {
		t.Errorf("--version: %d %q", code, out.String())
	}
}
