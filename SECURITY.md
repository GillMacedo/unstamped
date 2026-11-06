# Security

Report vulnerabilities through [GitHub's private vulnerability reporting](https://github.com/GillMacedo/unstamped/security/advisories/new), not in a public issue.

`unstamped` reads source through `go list` and reads binaries as data. It never builds or runs the code it checks. Please report privately if a crafted source tree or binary could make it do anything else. A missed stamp or a wrong finding is a normal bug.
