# Examples

`stampdemo/` is a made-up program with its own module, `example.com/stampdemo`. It has a `Version` and a `Commit` var, a `BuildDate` const and a `builtBy` var in package `main`.

- `make demo-check` checks four `-X` flags against its source. Three are wrong on purpose: a lowercase `commit`, the `BuildDate` const, and the full import path of package `main`.
- `make demo-binary` builds it for linux/amd64 with the same flags and checks the binary.
- `make demo-fixed` checks the corrected flags, which must pass.
