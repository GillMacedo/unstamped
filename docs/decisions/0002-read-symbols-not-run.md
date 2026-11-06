# 0002: Read stamps from the symbol table, never run the binary

**Decision:** `binary` reads each value from the `<var>.str` symbol the linker writes, and exits 2 when the binary has no symbol table.

**Why:** running a release binary to read `--version` executes untrusted code, needs the right OS and CPU, and depends on each program's flags. The symbol table is the same on every platform and says directly whether the linker applied each flag.

**Cost:** stripped binaries (`-s`) can't be checked. Searching the file for the expected value would often find it inside the build info's own copy of `-ldflags`, so it can't prove anything. The tool says it can't check and points to `check` on the source.
