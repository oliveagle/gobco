// Package cli implements the gomut command-line contract.
//
// It is the single source of truth for gomut's flags, modes and exit codes.
// The standalone binary (cmd/gomut) and every embedder (for example
// devine-utils-ci's `gomut` subcommand) call Run, so an embedded copy can
// never drift from the upstream tool.
//
// Exit codes: 0 = ok; 1 = usage/environment error; 2 = mutation score below
// the requested threshold.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/engine"
	"github.com/oliveagle/gobco/gomut/internal/impact"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/mutate"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

// Version is the gomut engine version.
const Version = "0.7.2"

// Options configures one CLI run.
type Options struct {
	// Dir is the working directory for package discovery and git operations.
	// Empty means "." — except that a first positional argument naming an
	// existing directory wins, which is the standalone CLI behaviour.
	Dir string
	// Program is the program name shown in the usage banner. Empty means
	// "gomut"; embedders pass their own name, e.g. "devine-utils-ci gomut".
	Program string
	// VersionLabel is appended to the -version output, e.g.
	// "embedded in devine-utils-ci".
	VersionLabel string
}

// Run executes the gomut CLI and returns gomut's documented exit code.
func Run(o Options, stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("gomut", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		workers     = fs.Int("p", 0, "number of parallel test subprocesses (default: CPU count, capped at 8)")
		timeout     = fs.Duration("timeout", 30*time.Second, "per-mutant test run budget")
		mutators    = fs.String("mutators", "", `operator set: "default"|"all"|"none" or a comma list of names, "-" prefixed names remove from the default set (default: all built-ins)`)
		allTests    = fs.Bool("all-tests", false, "run the whole test suite per mutant instead of the coverage-selected subset")
		threshold   = fs.Int("threshold", 0, "exit with code 2 if the mutation score (percent) is below this value")
		reportDir   = fs.String("report", ".gomut-report", "report output directory (empty disables file output)")
		formats     = fs.String("format", "text", "comma separated report formats to write: text,json (html is a v2 feature)")
		noCache     = fs.Bool("no-cache", false, "disable the per-package result cache")
		coverTest   = fs.Bool("cover-test", false, "also mutate _test.go files (not implemented in v1)")
		verbose     = fs.Bool("v", false, "print per-mutant progress")
		showVersion = fs.Bool("version", false, "print the version and exit")
		help        = fs.Bool("help", false, "print this usage")

		// Incremental testing flags
		incremental = fs.Bool("incremental", false, "run incremental mutation testing based on git diff")
		baselineRef = fs.String("baseline", "HEAD", "git ref for baseline comparison in incremental mode")
		showImpact  = fs.Bool("impact", false, "show impact analysis without running mutations")
	)

	program := o.Program
	if program == "" {
		program = "gomut"
	}
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: "+program+" [flags] [packages...]")
		fmt.Fprintln(fs.Output(), "\nRuns mutation testing (A/B experiment: mutated code vs. the package's own tests).")
		fmt.Fprintln(fs.Output(), "\nflags:")
		fs.PrintDefaults()
		fmt.Fprintln(fs.Output(), "\nexamples:")
		fmt.Fprintln(fs.Output(), "  "+program+" ./...")
		fmt.Fprintln(fs.Output(), "  "+program+" -mutators=Math,Constant -threshold=80 .")
		fmt.Fprintln(fs.Output(), "  "+program+" -all-tests -v ./pkg")
		fmt.Fprintln(fs.Output(), "  "+program+" -incremental -baseline=main ./...")
		fmt.Fprintln(fs.Output(), "  "+program+" -impact -baseline=HEAD~1 ./...")
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			// The flag package already printed the usage banner.
			return 0
		}
		fmt.Fprintf(stderr, "gomut: %v\n", err)
		return 1
	}
	if *help {
		fs.Usage()
		return 0
	}
	if *showVersion {
		if o.VersionLabel != "" {
			fmt.Fprintf(stdout, "gomut %s (%s)\n", Version, o.VersionLabel)
		} else {
			fmt.Fprintf(stdout, "gomut %s\n", Version)
		}
		return 0
	}
	if *coverTest {
		fmt.Fprintln(stderr, "gomut: -cover-test is not implemented in v1")
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
			fmt.Fprintln(stderr, "gomut: html reports are a v2 feature")
			return 1
		default:
			fmt.Fprintf(stderr, "gomut: unknown format %q (supported: text,json)\n", f)
			return 1
		}
	}

	dir, patterns := resolveWorkDir(o.Dir, fs.Args())

	// Impact analysis mode: report the domain, run no mutants.
	if *showImpact {
		return runImpactAnalysis(dir, *baselineRef, patterns, stderr)
	}

	// Incremental mode: mutate only what the baseline diff touches.
	if *incremental {
		return runIncremental(stdout, stderr, dir, *baselineRef, patterns, ops, *workers, *timeout, *allTests, *noCache, *threshold, *reportDir, *formats, *verbose)
	}

	// Standard mutation testing mode.
	e := engine.New(engine.Options{
		Dir:       dir,
		Patterns:  patterns,
		Operators: ops,
		Workers:   *workers,
		Timeout:   *timeout,
		AllTests:  *allTests,
		NoCache:   *noCache,
		Version:   Version,
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

	if rc := writeReports(dir, *reportDir, *formats, rep, stderr); rc != 0 {
		return rc
	}
	fmt.Fprintln(stdout)
	report.WriteText(stdout, rep, *verbose)

	if *threshold > 0 && rep.Score.Main < float64(*threshold) {
		fmt.Fprintf(stderr, "gomut: mutation score %.1f%% is below threshold %d%%\n", rep.Score.Main, *threshold)
		return 2
	}
	return 0
}

// resolveWorkDir decides the working directory and the package patterns.
//
// An explicit dir (embedders) always wins. Otherwise the first positional
// argument is used when it names an existing directory, which is what the
// standalone CLI has always done.
//
// When that positional directory becomes the working directory it is also
// replaced by "." in the pattern list. The engine resolves patterns relative
// to the working directory, so keeping the directory in the list would make
// the engine look for "<dir>/<dir>" — `gomut sub` used to fail with
// "package sub is not in std" while `gomut ./sub` failed with
// "stat sub/sub: directory not found".
func resolveWorkDir(dir string, patterns []string) (string, []string) {
	if dir != "" {
		return dir, patterns
	}
	dir = "."
	if len(patterns) > 0 {
		if info, err := os.Stat(patterns[0]); err == nil && info.IsDir() {
			dir = patterns[0]
			patterns = append([]string{"."}, patterns[1:]...)
		}
	}
	return dir, patterns
}

// runImpactAnalysis shows impact analysis without running mutations.
func runImpactAnalysis(dir, baselineRef string, patterns []string, stderr io.Writer) int {
	if !impact.IsGitRepo(dir) {
		fmt.Fprintln(stderr, "gomut: not a git repository")
		return 1
	}

	// Get changes
	changes, err := impact.ParseGitDiff(dir, baselineRef)
	if err != nil {
		fmt.Fprintf(stderr, "gomut: error parsing git diff: %v\n", err)
		return 1
	}

	if len(changes) == 0 {
		fmt.Fprintln(stderr, "No Go file changes detected")
		return 0
	}

	fmt.Fprintln(stderr, "Changed files:")
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

	fmt.Fprintln(stderr, "\nImpact Analysis:")
	fmt.Fprintf(stderr, "  Directly changed: %d functions\n", len(domain.Changed))
	fmt.Fprintf(stderr, "  Callees (dependencies): %d functions\n", len(domain.Callees))
	fmt.Fprintf(stderr, "  Callers (need retest): %d functions\n", len(domain.Callers))
	fmt.Fprintf(stderr, "  Total impact domain: %d functions\n", len(domain.All))

	// Show affected functions with details
	if len(domain.Changed) > 0 {
		fmt.Fprintln(stderr, "\nChanged functions:")
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

	// Show callees (functions called by changed code - informational only).
	if len(domain.Callees) > 0 {
		fmt.Fprintln(stderr, "\nCallees (functions called by changed code - informational only):")
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

	// Show callers (functions that call changed code - need retest).
	if len(domain.Callers) > 0 {
		fmt.Fprintln(stderr, "\nCallers (functions that call changed code - need retest):")
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
func runIncremental(stdout, stderr io.Writer, dir, baselineRef string, patterns []string, ops []mutant.Operator, workers int, timeout time.Duration, allTests, noCache bool, threshold int, reportDir, formats string, verbose bool) int {
	if !impact.IsGitRepo(dir) {
		fmt.Fprintln(stderr, "gomut: not a git repository")
		return 1
	}

	e := impact.NewIncremental(engine.Options{
		Dir:       dir,
		Patterns:  patterns,
		Operators: ops,
		Workers:   workers,
		Timeout:   timeout,
		AllTests:  allTests,
		NoCache:   noCache,
		Version:   Version,
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

	if rc := writeReports(dir, reportDir, formats, rep, stderr); rc != 0 {
		return rc
	}

	fmt.Fprintln(stdout)
	report.WriteText(stdout, rep, verbose)

	if threshold > 0 && rep.Score.Main < float64(threshold) {
		fmt.Fprintf(stderr, "gomut: mutation score %.1f%% is below threshold %d%%\n", rep.Score.Main, threshold)
		return 2
	}
	return 0
}

// writeReports writes the requested report formats under reportDir. The
// directory is resolved relative to the working directory, so an embedder
// that runs against another project root still writes inside that root.
func writeReports(workDir, reportDir, formats string, rep *report.Report, stderr io.Writer) int {
	if reportDir == "" {
		return 0
	}
	if !filepath.IsAbs(reportDir) {
		reportDir = filepath.Join(workDir, reportDir)
	}
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "gomut: %v\n", err)
		return 1
	}
	for _, format := range strings.Split(formats, ",") {
		switch strings.TrimSpace(format) {
		case "text":
			if err := writeReportFile(filepath.Join(reportDir, "report.txt"), func(w io.Writer) error {
				report.WriteText(w, rep, true)
				return nil
			}); err != nil {
				fmt.Fprintf(stderr, "gomut: %v\n", err)
				return 1
			}
		case "json":
			if err := writeReportFile(filepath.Join(reportDir, "report.json"), func(w io.Writer) error {
				return report.WriteJSON(w, rep)
			}); err != nil {
				fmt.Fprintf(stderr, "gomut: %v\n", err)
				return 1
			}
		}
	}
	return 0
}

func writeReportFile(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return write(f)
}
