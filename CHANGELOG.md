# Changelog

All notable changes are documented here, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-06

### Added
- `check`: resolves each `-X` flag against the source through `go list -deps`, for a chosen GOOS, GOARCH and build tags.
- `binary`: reads `-ldflags` from build info and the stamped values from the symbol table of ELF, Mach-O and PE binaries.
- Errors for wrong package paths, wrong names, consts, the full path of package `main`, vars in left-out or test files, local vars and struct fields. Errors for package patterns that don't match the main package and stray arguments. Warnings for empty or unexpanded values, duplicate targets, and vars whose initializer may run at program start and replace the value.
- `--want`, `--format json`, `--fail-on`, and exit 2 for any input that can't be checked.
- `checking-version-stamps` skill and plugin manifest.
