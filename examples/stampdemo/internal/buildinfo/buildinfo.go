// Package buildinfo holds the values a release build stamps with -ldflags -X.
// It is a made-up example for unstamped.
package buildinfo

// Version and Commit are set at build time.
var (
	Version = "dev"
	Commit  = "none"
)

// BuildDate is a const, so -X can't change it.
const BuildDate = "unknown"
