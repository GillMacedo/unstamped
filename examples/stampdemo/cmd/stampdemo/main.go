// Command stampdemo prints its build stamps. It is a made-up example for unstamped.
package main

import (
	"fmt"

	"example.com/stampdemo/internal/buildinfo"
)

var builtBy = "local"

func main() {
	fmt.Printf("stampdemo %s (%s), built %s by %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate, builtBy)
}
