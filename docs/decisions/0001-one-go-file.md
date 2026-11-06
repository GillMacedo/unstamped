# 0001: One standard-library Go file

**Decision:** the tool is a single Go file, `plugin/scripts/unstamped.go`, using only the standard library.

**Why:** people who stamp Go binaries already have Go, so `go run` is the whole install, with no npm package or release binaries. A single file also runs with `go run path/to/unstamped.go` from inside any other module, which is how the skill calls it from the plugin folder, where there is no go.mod.

**Alternatives:** `golang.org/x/tools/go/packages` with full type checking would find string types through any alias, but adds a dependency and needs the whole project to type-check. An MCP server would give an agent the same two commands with more setup and nothing a script can't do.
