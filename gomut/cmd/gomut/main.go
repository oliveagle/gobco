// Command gomut runs mutation testing on Go packages.
//
// Usage:
//
//	gomut [flags] [packages...]
//
// It injects single-site mutations (see -mutators) into the production
// code of each package via go test -overlay, runs the covering tests
// per mutant in isolated subprocesses, and reports a mutation score
// (detected / (total - NO_COVERAGE)).
//
// Exit codes: 0 = ok; 1 = usage/environment error; 2 = mutation score
// below -threshold.
//
// The command-line contract lives in the cli package so that embedders
// (e.g. devine-utils-ci) stay compatible with this binary.
package main

import (
	"os"

	"github.com/oliveagle/gobco/gomut/cli"
)

func main() {
	os.Exit(cli.Run(cli.Options{}, os.Stdout, os.Stderr, os.Args[1:]))
}
