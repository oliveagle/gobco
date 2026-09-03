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
	"strings"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/engine"
	"github.com/oliveagle/gobco/gomut/internal/mutate"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

const version = "0.2.5"

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

	e := engine.New(engine.Options{
		Patterns:  fs.Args(),
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

func writeReport(dir, name string, write func(w *os.File) error) error {
	path := filepath.Join(dir, name)
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	return write(w)
}
