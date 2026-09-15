// Command archunit checks architecture rules for Go projects.
//
// Usage:
//
//	archunit [flags] [packages...]
//
// It parses Go packages, builds a dependency graph, and verifies
// that the codebase follows user-defined architectural rules.
//
// Exit codes: 0 = all rules passed; 1 = violations found or error.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/oliveagle/gobco/gomut/internal/archunit"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("archunit", flag.ContinueOnError)
	rulesFile := fs.String("rules", "", "path to architecture rules file (default: auto-detect)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	verbose := fs.Bool("v", false, "print detailed check results")

	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: archunit [flags] [packages...]")
		fmt.Fprintln(fs.Output(), "\nChecks architecture rules for Go projects.")
		fmt.Fprintln(fs.Output(), "\nflags:")
		fs.PrintDefaults()
		fmt.Fprintln(fs.Output(), "\nexamples:")
		fmt.Fprintln(fs.Output(), "  archunit ./...")
		fmt.Fprintln(fs.Output(), "  archunit -rules=archunit.yaml ./pkg/...")
		fmt.Fprintln(fs.Output(), "  archunit -v ./...")
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fs.Usage()
			return 0
		}
		return 1
	}

	if *showVersion {
		fmt.Fprintf(os.Stdout, "archunit %s\n", version)
		return 0
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	// Try to load rules from file, fall back to defaults
	rules := loadRules(*rulesFile)
	if rules == nil {
		fmt.Fprintln(os.Stderr, "archunit: no rules file found, using built-in defaults")
		rules = defaultRules()
	}

	// Run checks
	violations, err := rules.Check(".", patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archunit: %v\n", err)
		return 1
	}

	if len(violations) == 0 {
		fmt.Fprintln(os.Stderr, "archunit: all checks passed ✓")
		return 0
	}

	// Print violations
	fmt.Fprintf(os.Stderr, "archunit: %d violation(s) found:\n\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n", v)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "\nRule set: %s\n", rules.Name())
	}

	return 1
}

// loadRules tries to load rules from a YAML file.
func loadRules(path string) *archunit.RuleSet {
	if path == "" {
		// Auto-detect common file names
		for _, name := range []string{"archunit.yaml", "archunit.yml", ".archunit.yaml"} {
			if _, err := os.Stat(name); err == nil {
				path = name
				break
			}
		}
	}

	if path == "" {
		return nil
	}

	// TODO: implement YAML parsing
	fmt.Fprintf(os.Stderr, "archunit: YAML rules file not yet implemented\n")
	return nil
}

// defaultRules returns the default architecture rules.
func defaultRules() *archunit.RuleSet {
	return archunit.NewRules("default").
		NoCycles().
		Build()
}
