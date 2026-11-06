# Architecture

    check:  parseLdflags --> go list (root) --> go list -deps --> readDecls per target package --> stamps
    binary: debug/buildinfo --> parseLdflags --> openImage (ELF, Mach-O, PE) --> stampValue per target --> stamps
    both:   addDuplicates, wants --> text or JSON --> exit code

- **Flags are split like cmd/go and cmd/link.** Fields split on spaces, with single or double quotes and no escapes. A leading package pattern (`all=`) is dropped. The target ends at the first `=`, and the name starts after the last `.` before it.
- **`check` trusts `go list` for the build.** It honors GOOS, GOARCH, tags and the rest of the environment, so build constraints are decided by the go command, not by this tool. It parses `GoFiles` and `CgoFiles` to find declarations, and `IgnoredGoFiles` and test files only to explain a miss.
- **`binary` relies on how the linker applies `-X`.** For each applied flag, cmd/link writes the value into a read-only symbol named `<var>.str` and points the var's string header at it. The value's length comes from that header, and its bytes from the `.str` symbol. Symbol names use the linker's escaping, so `gopkg.in/yaml.v3` becomes `gopkg.in/yaml%2ev3`.
- **Every read is checked.** A file shorter than its headers describe, an address outside every section, or a length that doesn't match the symbol size makes the whole input untrusted, which exits 2.

Key choices are in `decisions/`.
