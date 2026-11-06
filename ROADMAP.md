# Roadmap

## Next
- Read `ldflags` straight from `.goreleaser.yaml`, with sample values for its template fields
- A GitHub Action example that runs `check` for every target in a release matrix
- Handle buildmode `plugin`, where `main` is renamed by `-pluginpath`

## Later
- For stripped binaries, report which `-X` values appear in the file at all, labeled as a weaker check
- Warn when a stamped var is never read by the code
