# unstamped: check that Go -ldflags -X version stamps really land in the binary

[![CI](https://github.com/GillMacedo/unstamped/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/unstamped/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI for Go developers and release engineers. It checks that every `go build -ldflags "-X importpath.name=value"` flag in your Makefile, GoReleaser config or CI really sets a variable, so `app --version` doesn't ship as `dev` or `0.0.0`.**

The Go linker ignores an `-X` flag whose target doesn't exist, without an error or a warning. A wrong package path, a typo in the variable name, a const instead of a var, or the full import path of package `main` all build fine and stamp nothing. You find out when someone asks which version is running in production. `unstamped` catches this in two places: against the source before you build, and in the binary after.

## What you can ask Claude

- "Our release binary says version dev. Why isn't the ldflags version working?"
- "Check the ldflags in our .goreleaser.yaml before I tag a release."
- "Does go build -X main.version actually set anything in this project?"
- "Which version, commit and date are stamped in dist/app?"

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/unstamped
    claude plugin install unstamped@unstamped-marketplace

Or run it directly with Go 1.22 or newer. It has no dependencies:

    go run github.com/GillMacedo/unstamped/plugin/scripts@latest check --ldflags "-X main.version=1.2.3" ./cmd/app

## Example

`examples/stampdemo` is a made-up program with a `Version`, a `Commit`, a `BuildDate` const and a `builtBy` var in `main`. The demo build stamps it with four flags, three of them wrong in common ways:

    -X example.com/stampdemo/internal/buildinfo.Version=1.4.0
    -X example.com/stampdemo/internal/buildinfo.commit=3f2a9c1
    -X example.com/stampdemo/internal/buildinfo.BuildDate=2026-11-06
    -X example.com/stampdemo/cmd/stampdemo.builtBy=ci

`go build` accepts all four. Checking them against the source, before building:

    $ make demo-check
    ./cmd/stampdemo (linux/amd64, example.com/stampdemo/cmd/stampdemo)
      ok    example.com/stampdemo/internal/buildinfo.Version = "1.4.0"  (buildinfo.go:7)
      ERROR example.com/stampdemo/internal/buildinfo.commit = "3f2a9c1"
            package example.com/stampdemo/internal/buildinfo has no top-level var commit, so this sets nothing
            names are case sensitive: did you mean example.com/stampdemo/internal/buildinfo.Commit (buildinfo.go:8)?
      ERROR example.com/stampdemo/internal/buildinfo.BuildDate = "2026-11-06"  (buildinfo.go:12)
            is a const; -X only sets vars, so this sets nothing
      ERROR example.com/stampdemo/cmd/stampdemo.builtBy = "ci"
            example.com/stampdemo/cmd/stampdemo is the main package, and the linker names main packages "main", so this sets nothing
            use -X main.builtBy=...
      3 errors, 0 warnings.

Checking the binary that `go build` made with the same flags:

    $ make demo-binary
    bin/stampdemo (linux/amd64, example.com/stampdemo/cmd/stampdemo)
      note: -ldflags from build info: -X example.com/stampdemo/internal/buildinfo.Version=1.4.0 -X example.com/stampdemo/internal/buildinfo.commit=3f2a9c1 -X example.com/stampdemo/internal/buildinfo.BuildDate=2026-11-06 -X example.com/stampdemo/cmd/stampdemo.builtBy=ci
      ok    example.com/stampdemo/internal/buildinfo.Version = "1.4.0"
      ERROR example.com/stampdemo/internal/buildinfo.commit = "3f2a9c1"
            no var example.com/stampdemo/internal/buildinfo.commit in this binary: the path or name is wrong, it's a const, or nothing uses it and the linker dropped it
            vars with that name in the binary: example.com/stampdemo/internal/buildinfo.Commit
      ERROR example.com/stampdemo/internal/buildinfo.BuildDate = "2026-11-06"
            no var example.com/stampdemo/internal/buildinfo.BuildDate in this binary: the path or name is wrong, it's a const, or nothing uses it and the linker dropped it
      ERROR example.com/stampdemo/cmd/stampdemo.builtBy = "ci"
            no var example.com/stampdemo/cmd/stampdemo.builtBy in this binary: the path or name is wrong, it's a const, or nothing uses it and the linker dropped it
            this is the main package; use -X main.builtBy=...
      3 errors, 0 warnings.

With the three mistakes fixed, it stays quiet and exits 0:

    $ make demo-fixed
    ./cmd/stampdemo (linux/amd64, example.com/stampdemo/cmd/stampdemo)
      ok    example.com/stampdemo/internal/buildinfo.Version = "1.4.0"  (buildinfo.go:7)
      ok    example.com/stampdemo/internal/buildinfo.Commit = "3f2a9c1"  (buildinfo.go:8)
      ok    main.builtBy = "ci"  (main.go:10)
      3 of 3 -X flags name a string var that this build links.

## Usage

    unstamped check  [--ldflags FLAGS] [--dir DIR] [--goos OS] [--goarch ARCH] [--tags TAGS] [--want T,...] PACKAGE
    unstamped binary [--want T,...] FILE...

- **`check`** reads the main package and everything it links through `go list -deps`, for the GOOS, GOARCH and build tags you give, then parses those files to find each `-X` target. Pass the `-ldflags` string exactly as `go build` gets it, with template variables filled in. It never builds or runs the code.
- **`binary`** reads a built Go binary (ELF, Mach-O or PE). It takes `-ldflags` from the embedded build info and reads each stamped value from the symbol table, where the linker stores it as `name.str`. Give several files to check every platform of a release.
- **`--want main.version,main.commit`** makes a target required. Use it with `-trimpath` builds, whose build info leaves out `-ldflags`.
- **`--format json`** for scripts and agents. **`--fail-on`** sets the lowest severity that exits 1: `warn` by default, or `error` or `none`.

Exit 0 means no findings. Exit 1 means findings. Exit 2 means bad usage or an input it couldn't check: an empty or cut-off file, something that isn't a Go binary, a stripped binary, a Go file that won't parse, or `-ldflags` with no `-X` flags. Exit 2 ignores `--fail-on`, so a check that read nothing can't pass.

## What it catches

| Mistake | `check` | `binary` |
|---|---|---|
| Package path wrong, or not linked for this GOOS, GOARCH and tags | Error, with packages that have a var of that name | Error, with same-name vars in the binary |
| Var name wrong, including case (`main.Version` for `main.version`) | Error, with the right name | Error, with same-name vars |
| Target is a const | Error | Error (it isn't in the binary) |
| Full import path of package `main` (`example.com/app/cmd/app.version`) | Error: use `main.version` | Error: use `main.version` |
| Var only in a file left out by build constraints, or in a test file | Error, naming the file | Error |
| Local var inside a function, or a struct field (`main.info.Version`) | Error | Error |
| Trailing slash or wrong case in the package path | Error, with the right path | Error |
| Empty value, or an unexpanded `{{.Version}}`, `$(VAR)` or `${VAR}` | Warning | Warning |
| Same target set twice | Warning | Warning |
| A package pattern (`./cmd/other=-X ...`) that doesn't match the main package | Error | Error (the var isn't set) |
| A var initialized by code that runs at program start, such as `fmt.Sprint(...)` or `"v" + other`, which replaces the `-X` value | Warning | Not detected |
| A stray argument from broken quoting (go build then fails) | Error | Error |
| No `-X` flags at all | Exit 2 | Error |

A var with a non-string type makes `go build` fail, so that one isn't silent. `check` flags it anyway when the type is written out or the value is a non-string literal.

## How it compares

- **`go version -m app`** prints the `-ldflags` a binary was built with, but not whether each flag set anything. With `-trimpath` it leaves `-ldflags` out.
- **`go tool nm app | grep version`** is the manual check. `unstamped binary` does the same lookup for every flag and reads the values.
- **[GoReleaser](https://goreleaser.com)** builds with your `ldflags` and passes them through. I found no option in it that checks `-X` targets.
- **The Go toolchain** won't warn. In [golang/go#18246](https://github.com/golang/go/issues/18246), the Go team explains that they tried a warning for a missing `-X` variable and removed it, because people pass the same `-X` flags to programs that don't all have the variable. That's a reasonable workflow, so the check belongs in a separate step for the programs you release.
- **[imjasonh/version](https://github.com/imjasonh/version)** (Apache-2.0) argues for reading `runtime/debug.ReadBuildInfo` instead of using `-X`. That avoids the problem for module versions and VCS data. If you stamp with `-X`, this tool checks it.

## Limits

- **`binary` needs a symbol table.** Release builds with `-s` have none, so it exits 2 and says so. Use `check` on the source, or check a build without `-s` from the same flags.
- **`check` sees what `go list` sees.** It uses your environment for anything you don't pass, including `CGO_ENABLED`, `GOFLAGS` and the module cache. If modules aren't downloaded, `go list` may try to download them.
- **It checks the link, not everything your code does.** The linker writes the `-X` value into the var's static data. If the var's initializer runs at program start instead, it replaces that value ([golang/go#24890](https://github.com/golang/go/issues/24890), still open). With Go 1.27.3, `var v = fmt.Sprint("dev")` and `var v = "dev" + other` lose the `-X` value, while string literals, consts, `"a" + "b"`, plain copies and some small function calls keep it. `check` warns for any initializer that isn't a literal, a const or a plain copy, so some warnings will be for vars that are in fact set. `binary` can't see this, and neither mode sees an `init` function that assigns the var later.
- **A var nothing uses** is dropped by the linker. `check` calls it fine, because the flag names a real var. `binary` reports it as missing, because there is nothing in the binary to read.
- **Template values aren't filled in for you.** Replace `{{.Version}}` and shell variables with sample values first. The skill tells Claude to do this.
- Buildmode `plugin`, where `main` is renamed by `-pluginpath`, isn't handled.

## FAQ

**Why does go build -ldflags -X not set my variable?** Usually the path doesn't match the package's import path exactly, the name's case is wrong, it's a const, or it's in package `main` and you wrote the full path. The linker doesn't warn about any of these. Run `unstamped check` to see which.

**Why doesn't Go warn about a wrong -X flag?** The Go team tried it and removed it, because one set of flags is often shared by programs that don't all have the variable (golang/go#18246).

**Why does my Go binary print version dev?** The `-X` flag that should replace `dev` didn't match the var. Run `unstamped binary` on the binary, or `unstamped check` with your release `-ldflags`.

**How do I check the version stamped in a Go binary without running it?** `unstamped binary app` reads the values from the symbol table.

**Does it work with GoReleaser?** Yes, for the `ldflags` you copy from `.goreleaser.yaml` with template values filled in. It doesn't read the YAML itself.

**Is it an MCP server?** No. It's a skill with one standard-library Go file, so `go run` is the whole install.

## Tested on

- Unit tests for `-ldflags` splitting, quoting, package patterns, malformed flags and path escaping.
- An independent review that tried to make it report a false pass. It found four problems, all fixed with tests: a non-matching package pattern passed `check`, runtime initializers weren't flagged, a binary with its value bytes zeroed passed `binary`, and an ELF with a section marked compressed crashed it. `binary` now also exits 2 when a value it reads doesn't match the build info's `-ldflags`. A test builds a native program to confirm that runtime initializers really lose the value.
- Integration tests in `tests/` that call `go list` on a fixture module and build real binaries for linux/amd64, linux/386, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 and windows/386, then check every mistake in the table and a healthy build that must stay quiet. Each input was also tried empty, as garbage, cut off, and as failed `go build` output: the `-ldflags` string, a Go file in the stamped package, and the binary (cut at five points per platform, and stripped). All of them exit 2 and none prints a clean result, with or without `--fail-on none`.
- `ustclug/rsync-proxy`, whose `v0.1.4` release shipped with default version values because `.goreleaser.yml` pointed at a package that doesn't exist (fixed in its PR 33). On the commit before the fix, `check` reports all three flags with the right package as a hint. On the fix commit it stays quiet. I built both with the same flags, without `-s`, and `binary` gives the same results, with and without `-trimpath`. The real `v0.1.4` release binary is stripped, so `binary` exits 2 on it.
- `dolph/find-replace`, whose issue 26 reports seven `-X` flags in `build.sh` for vars that don't exist. `check` and `binary` report all seven.
- `junegunn/fzf` and `prometheus/node_exporter` as healthy projects: their release flags pass `check` (fzf for linux/amd64 and windows/amd64, node_exporter for linux/amd64 and darwin/arm64), and `binary` reads the right values from local builds. node_exporter stamps a dependency's package, `github.com/prometheus/common/version`.
- Binaries built by Go 1.22.0 and Go 1.27.3, and PIE builds for linux/arm64 and windows/amd64, plus an externally linked cgo build on linux/arm64.

Not tested: binaries from gccgo or TinyGo, buildmode `plugin` or `c-shared`, and use in a fresh Claude session.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/unstamped/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

### How pull requests work

1. **Open a PR.** Fork the repo, work on a branch, and keep it to one change. Write a title that says what changes for users, because it becomes the changelog line: `feat:` goes under Added and `fix:` under Fixed.
2. **Automatic check.** Code changes need tests, and `make test` must pass. Changes over 30 lines need a linked open issue (`Closes #<number>`), and a PR can have at most 800 changed lines. If something is missing, a comment tells you what to fix.
3. **Review.** Claude reviews each PR automatically. Code PRs also get a security review and are tested in a sandbox. The review then merges the PR or asks for changes, and a small problem may become a follow-up issue instead. A PR with no new commit 14 days after a change request is closed, and you can reopen it. PRs that change CI, agent settings, npm or Python dependencies, vendored code or build files can't be merged this way, and a comment says so. The same goes for anything the security review doesn't clear.
4. **Release.** A merge that changes what users install becomes a new version within about two hours, with a GitHub Release and, if the project has one, its npm package. Docs and CI changes wait for the next release.

## AI disclosure

This project was largely generated with Claude Code and reviewed by its maintainer.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `unstamped` saved you some prompts, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
