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
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/engine"
	"github.com/oliveagle/gobco/gomut/internal/impact"
	"github.com/oliveagle/gobco/gomut/internal/mutate"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

const version = "0.5.0-agentark.4"

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:]))
}

func run(stdout, stderr *os.File, args []string) int {
	fs := flag.NewFlagSet("gomut", flag.ContinueOnError)
	var (
		workers     = fs.Int("p", 0, "number of parallel test subprocesses (default: CPU count, capped at 8)")
		timeout     = fs.Duration("timeout", 30*time.Second, "per-mutant test run budget")
		mutators    = fs.String("mutators", "", `operator set: "default"|"all"|"none" or a comma list of names, "-" prefixed names remove from the default set (default: all built-ins)`)
		allTests    = fs.Bool("all-tests", false, "run the whole test suite per mutant instead of the coverage-selected subset")
		threshold   = fs.Int("threshold", 0, "exit with code 2 if the mutation score (percent) is below this value")
		reportDir   = fs.String("report", ".gomut-report", "report output directory (empty disables file output)")
		formats     = fs.String("format", "text", "comma separated report formats to write: text,json (html is a v2 feature)")
		noCache     = fs.Bool("no-cache", false, "disable the per-package result cache")
		coverTest   = fs.Bool("cover-test", false, "also mutate _test.go files (not implemented in v1, todo T-12)")
		verbose     = fs.Bool("v", false, "print per-mutant progress")
		showVersion = fs.Bool("version", false, "print the version and exit")
		help        = fs.Bool("help", false, "print this usage")

		// Incremental testing flags
		incremental = fs.Bool("incremental", false, "run incremental mutation testing based on git diff")
		baselineRef = fs.String("baseline", "HEAD", "git ref for baseline comparison in incremental mode")
		showImpact  = fs.Bool("impact", false, "show impact analysis without running mutations")

		// Architecture testing flags
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: gomut [flags] [packages...]")
		fmt.Fprintln(fs.Output(), "\nRuns mutation testing (A/B experiment: mutated code vs. the package's own tests).")
		fmt.Fprintln(fs.Output(), "\nflags:")
		fs.PrintDefaults()
		fmt.Fprintln(fs.Output(), "\nexamples:")
		fmt.Fprintln(fs.Output(), "  gomut ./...")
		fmt.Fprintln(fs.Output(), "  gomut -mutators=Math,Constant -threshold=80 .")
		fmt.Fprintln(fs.Output(), "  gomut -all-tests -v ./pkg")
		fmt.Fprintln(fs.Output(), "  gomut -incremental -baseline=main ./...")
		fmt.Fprintln(fs.Output(), "  gomut -impact -baseline=HEAD~1 ./...")
	}
	if err := fs.Parse(args); err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintf(stderr, "gomut: %v\n", err)
		}
		if err == flag.ErrHelp {
			fs.Usage()
			return 0
		}
		return 1
	}
	if *help {
		fs.Usage()
		return 0
	}
	if *showVersion {
		fmt.Fprintf(stdout, "gomut %s\n", version)
		return 0
	}
	if *coverTest {
		fmt.Fprintln(stderr, "gomut: -cover-test is not implemented in v1 (todo T-12: mutating test code)")
		return 1
	}
	ops, err := mutate.Select(*mutators)
	if err != nil {
		fmt.Fprintf(stderr, "gomut: %v\n", err)
		return 1
	}
	for _, f := range strings.Split(*formats, ",") {
		switch strings.TrimSpace(f) {
		case "text", "json":
		case "":
		case "html":
			fmt.Fprintln(stderr, "gomut: html reports are a v2 feature (todo T-11)")
			return 1
		default:
			fmt.Fprintf(stderr, "gomut: unknown format %q (supported: text,json)", f)
			return 1
		}
	}

	dir := "."
	if len(fs.Args()) > 0 {
		// Use the first argument as directory if it's a path
		if info, err := os.Stat(fs.Args()[0]); err == nil && info.IsDir() {
			dir = fs.Args()[0]
		}
	}

	// Handle impact analysis mode
	if *showImpact {
		return runImpactAnalysis(dir, *baselineRef, fs.Args(), stderr)
	}


	// Handle incremental mode
	if *incremental {
		return runIncremental(stdout, stderr, dir, *baselineRef, fs.Args(), ops, *workers, *timeout, *allTests, *noCache, *threshold, *reportDir, *formats, *verbose)
	}

	// Standard mutation testing mode
	e := engine.New(engine.Options{
		Dir:      dir,
		Patterns: fs.Args(),
		Operators: ops,
		Workers:   *workers,
		Timeout:   *timeout,
		AllTests:  *allTests,
		NoCache:   *noCache,
		Version:   version,
		Logf: func(format string, a ...interface{}) {
			if *verbose {
				fmt.Fprintf(stderr, format+"\n", a...)
			}
		},
	})

	start := time.Now()
	rep, err := e.Run(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "gomut: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "gomut: %d mutants in %s\n", rep.Score.Total, time.Since(start).Round(time.Millisecond))

	// File output.
	if *reportDir != "" {
		if err := os.MkdirAll(*reportDir, 0o755); err != nil {
			fmt.Fprintf(stderr, "gomut: %v\n", err)
			return 1
		}
		for _, f := range strings.Split(*formats, ",") {
			switch strings.TrimSpace(f) {
			case "text":
				if err := writeReport(*reportDir, "report.txt", func(w *os.File) error {
					report.WriteText(w, rep, true)
					return nil
				}); err != nil {
					fmt.Fprintf(stderr, "gomut: %v\n", err)
					return 1
				}
			case "json":
				if err := writeReport(*reportDir, "report.json", func(w *os.File) error {
					return report.WriteJSON(w, rep)
				}); err != nil {
					fmt.Fprintf(stderr, "gomut: %v\n", err)
					return 1
				}
			}
		}
	}

	// Console summary.
	fmt.Fprintln(stdout)
	report.WriteText(stdout, rep, *verbose)

	if *threshold > 0 && rep.Score.Main < float64(*threshold) {
		fmt.Fprintf(stderr, "gomut: mutation score %.1f%% is below threshold %d%%\n", rep.Score.Main, *threshold)
		return 2
	}
	return 0
}

// runImpactAnalysis shows impact analysis without running mutations.
func runImpactAnalysis(dir, baselineRef string, patterns []string, stderr *os.File) int {
	if !impact.IsGitRepo(dir) {
		fmt.Fprintf(stderr, "gomut: not a git repository\n")
		return 1
	}

	// Get changes
	changes, err := impact.ParseGitDiff(dir, baselineRef)
	if err != nil {
		fmt.Fprintf(stderr, "gomut: error parsing git diff: %v\n", err)
		return 1
	}

	if len(changes) == 0 {
		fmt.Fprintf(stderr, "No Go file changes detected\n")
		return 0
	}

	fmt.Fprintf(stderr, "Changed files:\n")
	for _, c := range changes {
		fmt.Fprintf(stderr, "  %s (+%d lines, -%d lines)\n", c.File, len(c.AddedLines), len(c.RemovedLines))
	}

	// Build call graph
	cg, err := impact.BuildCallGraph(dir, patterns)
	if err != nil {
		fmt.Fprintf(stderr, "gomut: error building call graph: %v\n", err)
		return 1
	}

	// Compute domain
	domain := impact.ComputeDomain(cg, changes)

	fmt.Fprintf(stderr, "\nImpact Analysis:\n")
	fmt.Fprintf(stderr, "  Directly changed: %d functions\n", len(domain.Changed))
	fmt.Fprintf(stderr, "  Callees (dependencies): %d functions\n", len(domain.Callees))
	fmt.Fprintf(stderr, "  Callers (need retest): %d functions\n", len(domain.Callers))
	fmt.Fprintf(stderr, "  Total impact domain: %d functions\n", len(domain.All))

	// Show affected functions with details
	if len(domain.Changed) > 0 {
		fmt.Fprintf(stderr, "\nChanged functions:\n")
		keys := make([]string, 0, len(domain.Changed))
		for key := range domain.Changed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if node, ok := cg.Funcs[key]; ok {
				fmt.Fprintf(stderr, "  %s:%d-%d\n", node.File, node.StartLine, node.EndLine)
			}
		}
	}

	if len(domain.Callees) > 0 {
		fmt.Fprintf(stderr, "\nCallees (functions called by changed code - informational only):\n")
		keys := make([]string, 0, len(domain.Callees))
		for key := range domain.Callees {
			if !domain.Changed[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if node, ok := cg.Funcs[key]; ok {
				fmt.Fprintf(stderr, "  %s:%d-%d\n", node.File, node.StartLine, node.EndLine)
			}
		}
	}

	if len(domain.Callers) > 0 {
		fmt.Fprintf(stderr, "\nCallers (functions that call changed code - need retest):\n")
		keys := make([]string, 0, len(domain.Callers))
		for key := range domain.Callers {
			if !domain.Changed[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if node, ok := cg.Funcs[key]; ok {
				fmt.Fprintf(stderr, "  %s:%d-%d\n", node.File, node.StartLine, node.EndLine)
			}
		}
	}

	return 0
}

// runIncremental runs incremental mutation testing.
func runIncremental(stdout, stderr *os.File, dir, baselineRef string, patterns []string, ops []mutant.Operator, workers int, timeout time.Duration, allTests, noCache bool, threshold int, reportDir, formats string, verbose bool) int {
	if !impact.IsGitRepo(dir) {
		fmt.Fprintf(stderr, "gomut: not a git repository\n")
		return 1
	}

	e := impact.NewIncremental(engine.Options{
		Dir:      dir,
		Patterns: patterns,
		Operators: ops,
		Workers:   workers,
		Timeout:   timeout,
		AllTests:  allTests,
		NoCache:   noCache,
		Version:   version,
		Logf: func(format string, a ...interface{}) {
			if verbose {
				fmt.Fprintf(stderr, format+"\n", a...)
			}
		},
	}, baselineRef)

	start := time.Now()
	rep, err := e.RunIncremental(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "gomut: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "gomut: %d mutants in %s (incremental)\n", rep.Score.Total, time.Since(start).Round(time.Millisecond))

	// File output.
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			fmt.Fprintf(stderr, "gomut: %v\n", err)
			return 1
		}
		for _, f := range strings.Split(formats, ",") {
			switch strings.TrimSpace(f) {
			case "text":
				if err := writeReport(reportDir, "report.txt", func(w *os.File) error {
					report.WriteText(w, rep, true)
					return nil
				}); err != nil {
					fmt.Fprintf(stderr, "gomut: %v\n", err)
					return 1
				}
			case "json":
				if err := writeReport(reportDir, "report.json", func(w *os.File) error {
					return report.WriteJSON(w, rep)
				}); err != nil {
					fmt.Fprintf(stderr, "gomut: %v\n", err)
					return 1
				}
			}
		}
	}

	// Console summary.
	fmt.Fprintln(stdout)
	report.WriteText(stdout, rep, verbose)

	if threshold > 0 && rep.Score.Main < float64(threshold) {
		fmt.Fprintf(stderr, "gomut: mutation score %.1f%% is below threshold %d%%\n", rep.Score.Main, threshold)
		return 2
	}
	return 0
}

func writeReport(dir, name string, write func(w *os.File) error) error {
	path := filepath.Join(dir, name)
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	return write(w)
}

