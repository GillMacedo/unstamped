# unstamped

Claude Code skill and Go CLI that checks Go `-ldflags -X` flags really set a variable, in source or in a built binary.

## Architecture
`plugin/scripts/unstamped.go` holds all the logic in one standard-library file, so `go run` needs no setup. `parseLdflags` splits the flags the way cmd/go and cmd/link do. `runCheck` resolves each target through `go list -deps` and parses the compiled files. `runBinary` reads build info and the `name.str` symbols the linker writes for each applied `-X`. `plugin/skills/checking-version-stamps/SKILL.md` tells Claude how to use it. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet, unit tests and the integration tests in `tests/`
- `make lint`: plugin and marketplace manifest validation
- `make demo`: the three README examples on `examples/stampdemo`

## Conventions
- Standard library only. Never build or run the code being checked; `go list` is the only command it runs.
- An input it can't read fully (cut-off binary, stripped binary, Go file that won't parse, `-ldflags` with no `-X`) exits 2 and prints no clean summary. Never skip it silently.
- Every check needs a real `go build` showing the linker ignores the flag, a test that flags it, and a healthy case that stays quiet.
- Tests must not assume the host GOOS or GOARCH. Pick targets explicitly, or derive the "other" one from `runtime`.

## Do and don't
- Do keep README examples as real output from `make demo-check`, `make demo-binary` and `make demo-fixed`.
- Don't claim a binary is correctly stamped when the symbol table is missing.

## Definition of done
`make test`, `make lint` and `make demo` pass, and `ROADMAP.md`, `README.md` and docs are updated when relevant.
