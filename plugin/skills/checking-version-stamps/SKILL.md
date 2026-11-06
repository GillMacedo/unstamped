---
name: checking-version-stamps
description: Checks that go build -ldflags "-X importpath.name=value" flags really set the version, commit and build date variables of a Go program, in source through go list or in a built binary. The Go linker ignores an -X flag whose package path or variable name is wrong, or that targets a const, so releases ship showing "dev" or "0.0.0". Use when a Go binary's --version shows a default value, when reviewing Makefile, GoReleaser, Dockerfile or CI ldflags, or before tagging a release.
---

# Checking Go version stamps

The bundled tool needs Go. It runs `go list` and reads files, and never builds or runs the project.

## 1. Find the -ldflags

Look where the project builds releases: `.goreleaser.yml` or `.goreleaser.yaml` (`builds[].ldflags`), `Makefile`, `Dockerfile`, `build.sh`, `.promu.yml` and `.github/workflows/*.yml`. Note the main package each build uses (`main:` or `dir:` in GoReleaser, the last argument of `go build`), and its `goos`, `goarch`, `tags` and `CGO_ENABLED`.

Replace template and shell variables with plain sample values before checking, such as `{{.Version}}` with `1.2.3` and `$(GIT_COMMIT)` with `abc1234`. Keep everything else exactly as written. If you leave one in, the tool warns about it.

## 2. Check against source

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/unstamped.go" check --dir MODULE_DIR --ldflags 'FLAGS' --goos OS --goarch ARCH ./cmd/app

Run it once per GOOS and GOARCH the release builds, because a var in a `_linux.go` file doesn't exist on darwin. Set `CGO_ENABLED` the same way the release does. Add `--want main.version` for a target that must be set. Add `--format json` if you want to parse the result.

## 3. Check a built binary, when there is one

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/unstamped.go" binary --want main.version,main.commit dist/app

Build without `-s` for this, because a stripped binary has no symbol table and the tool will say it can't check it. With `-trimpath`, build info leaves out `-ldflags`, so always pass `--want` with the targets you expect.

## Exit codes

0 means every -X flag names a string var that the build links (check), or every stamp has a value in the binary (binary). 1 means findings. 2 means bad usage or an input the tool couldn't check. Treat 2 as unknown, never as a pass.

## Report

1. For each ERROR, say which file sets the flag (Makefile line, GoReleaser key) and the smallest fix, using the tool's hint when it names the right target.
2. Say which builds you checked (GOOS, GOARCH, tags) and which you didn't.
3. Don't edit build files unless the user asks. Don't run the project's binaries to check versions unless the user asks.
