// Package exec is the boundary between gomut and the Go toolchain
// (ADR-0001, constraint 5).
//
// It runs "go test" subprocesses with -overlay based mutation injection,
// enforces per-run wall-clock budgets by killing the whole process group
// (setpgid + SIGKILL to the group), and classifies the outcome into
// mutant statuses. Each run is a fresh process in its own group, which
// gives the state isolation pitest gets from JVM minions.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// maxOutput caps how much run output is retained (the tail is kept).
const maxOutput = 256 * 1024

// Overlay maps each original file path to the replacement file's
// path. It is written to disk wrapped in an overlayDocument so the
// "go test -overlay" flag can consume it (the top-level "Replace"
// field holds the actual map).
type Overlay map[string]string

// overlayDocument is the JSON document accepted by "go test -overlay".
// The map of original-to-replacement paths must live under "Replace".
type overlayDocument struct {
	Replace map[string]string `json:"Replace,omitempty"`
}

// WriteFile writes the overlay as a JSON file at path.
func (o Overlay) WriteFile(path string) error {
	data, err := json.Marshal(overlayDocument{Replace: map[string]string(o)})
	if err != nil {
		return fmt.Errorf("encoding overlay: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// TestRun is one isolated "go test" invocation.
type TestRun struct {
	// Dir is the working directory of the go command (the package dir).
	Dir string
	// Pkg is the package argument passed to "go test".
	Pkg string
	// Tests limits the run to the named top-level tests (empty = all).
	Tests []string
	// OverlayPath is a written overlay file; empty = unmutated run.
	OverlayPath string
	// CoverProfile adds -covermode=count -coverprofile=<path>.
	CoverProfile string
	// FailFast stops the test binary after the first failing test.
	FailFast bool
	// Timeout is the wall-clock budget; the whole process group is
	// killed when it elapses. Zero = no budget.
	Timeout time.Duration
	// GoBin overrides the go binary (default: "go" on PATH).
	GoBin string
}

// Result is the classified outcome of one test run.
type Result struct {
	// Status: Survived, Killed, TimedOut, CompileError, RunError or
	// NoCoverage (see mutant.Status).
	Status mutant.Status
	// Output is the (tail of the) combined test output.
	Output string
	// Failures lists the test names that failed, if any.
	Failures []string
	// Duration of the whole run.
	Duration time.Duration
}

var (
	reBuildFailed = regexp.MustCompile(`(?m)^FAIL\s+\S+\s+\[build failed\]\s*$`)
	rePkgFail     = regexp.MustCompile(`(?m)^FAIL\t\S+\s+\S*`)
	reNoTests     = regexp.MustCompile(`no tests to run`)
	reNoTestFiles = regexp.MustCompile(`no test files`)
	reTestFail    = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
	rePanic       = regexp.MustCompile(`panic:`)
	reEnvError    = regexp.MustCompile(`(no required module provides|cannot find package|no such file or directory|go: no such|unknown revision)`)
)

// Run executes one test run and classifies the outcome.
func Run(parent context.Context, tr TestRun) Result {
	ctx := parent
	var cancel context.CancelFunc = func() {}
	if tr.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, tr.Timeout)
	}
	defer cancel()

	goBin := tr.GoBin
	if goBin == "" {
		goBin = "go"
	}
	start := time.Now()

	cmd := exec.Command(goBin, buildTestArgs(tr)...)
	cmd.Dir = tr.Dir
	setProcessGroup(cmd)

	stdout := newCapWriter(maxOutput)
	stderr := newCapWriter(maxOutput)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return Result{
			Status:   mutant.RunError,
			Output:   err.Error(),
			Duration: time.Since(start),
		}
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var timedOut bool
	select {
	case <-waitDone:
	case <-ctx.Done():
		timedOut = true
		killProcessGroup(cmd)
		<-waitDone
	}

	output := combine(stdout.String(), stderr.String())
	return Result{
		Status:   classify(output, timedOut),
		Output:   tail(output, 32*1024),
		Failures: extractFailures(output),
		Duration: time.Since(start),
	}
}

func buildTestArgs(tr TestRun) []string {
	args := []string{"test", "-count=1"}
	if len(tr.Tests) > 0 {
		// Test names are identifiers, safe to embed unescaped.
		args = append(args, "-run=^("+strings.Join(tr.Tests, "|")+")$")
	}
	if tr.FailFast {
		args = append(args, "-failfast")
	}
	if tr.CoverProfile != "" {
		args = append(args, "-covermode=count", "-coverprofile="+tr.CoverProfile)
	}
	if tr.OverlayPath != "" {
		args = append(args, "-overlay="+tr.OverlayPath)
	}
	args = append(args, tr.Pkg)
	return args
}

// classify maps combined "go test" output (and whether our own timeout
// fired) to a mutant status.
func classify(output string, timedOut bool) mutant.Status {
	if timedOut {
		return mutant.TimedOut
	}
	if reNoTestFiles.MatchString(output) {
		return mutant.RunError
	}
	if reBuildFailed.MatchString(output) {
		return mutant.CompileError
	}
	if reEnvError.MatchString(output) {
		return mutant.RunError
	}
	if reNoTests.MatchString(output) {
		return mutant.NoCoverage
	}
	if extractFailures(output) != nil || rePkgFail.MatchString(output) || rePanic.MatchString(output) {
		return mutant.Killed
	}
	return mutant.Survived
}

func extractFailures(output string) []string {
	matches := reTestFail.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

func combine(out, errOut string) string {
	s := strings.TrimRight(out, "\n") + "\n" + strings.TrimLeft(errOut, "\n")
	return strings.TrimSpace(s)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// capWriter is an io.Writer that keeps at most ~2n bytes of input,
// preferring the tail (the interesting part of a failing run).
type capWriter struct {
	buf     bytes.Buffer
	dropped bool
	n       int
}

func newCapWriter(n int) *capWriter { return &capWriter{n: n} }

func (w *capWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if w.buf.Len() > 2*w.n {
		b := w.buf.Bytes()
		w.buf.Reset()
		w.buf.Write(b[len(b)-w.n:])
		w.dropped = true
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	s := w.buf.String()
	if w.dropped {
		s = "…" + s
	}
	return s
}
