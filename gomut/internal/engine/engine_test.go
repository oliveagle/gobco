package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oliveagle/gomut/internal/mutant"
	"github.com/oliveagle/gomut/internal/report"
)

// TestEndToEnd runs the full pipeline against the tiny sample module
// and asserts the designed outcomes of specific mutants:
//
//	IsPositive: boundary "v > 0" -> "v >= 0" is KILLED (v == 0 case),
//	            both ReturnVals mutants are KILLED.
//	Abs:        boundary "a < 0" -> "a <= 0" SURVIVES (no a == 0 test),
//	            negated condition is KILLED.
//	Count:      "i++" -> "i--" loops forever: TIMED_OUT.
//	Unused:     every mutant is NO_COVERAGE.
//
// Skipped with -short (it shells out to the Go toolchain).
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	dir := filepath.Join("..", "..", "testdata", "sample")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Glob(filepath.Join(abs, "go.mod")); err != nil || len(abs) == 0 {
		t.Fatalf("sample module not found at %s", abs)
	}
	e := New(Options{
		Dir:      abs,
		Patterns: []string{"."},
		Workers:  2,
		Timeout:  6 * time.Second,
		NoCache:  true,
		Logf:     t.Logf,
	})
	rep, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var pkg *report.Pkg
	for i := range rep.Packages {
		if rep.Packages[i].ImportPath == "github.com/oliveagle/gomut/testdata/sample" {
			pkg = &rep.Packages[i]
		}
	}
	if pkg == nil {
		t.Fatalf("sample package missing from report: %+v", rep.Packages)
	}

	find := func(op, fileSuffix, descSub string) []report.Mutant {
		var out []report.Mutant
		for _, m := range pkg.Mutants {
			if m.Operator == op && strings.HasSuffix(m.File, fileSuffix) && strings.Contains(m.Desc, descSub) {
				out = append(out, m)
			}
		}
		return out
	}
	check := func(got []report.Mutant, want mutant.Status, label string) {
		t.Helper()
		if len(got) == 0 {
			t.Errorf("%s: no matching mutant found", label)
			return
		}
		for _, m := range got {
			if m.Status != string(want) {
				t.Errorf("%s: %s@%s:%d status = %s, want %s (%s)", label, m.Operator, m.RelFile, m.Line, m.Status, want, m.Desc)
			}
		}
	}

	// KILLED: boundary at the IsPositive return.
	isPositive := find("ConditionalsBoundary", "math.go", `replaced "v > 0" with "v >= 0"`)
	check(isPositive, mutant.Killed, "IsPositive boundary")

	// KILLED: both bool return replacements in IsPositive.
	rets := find("ReturnVals", "math.go", "")
	if len(rets) < 2 {
		t.Errorf("ReturnVals mutants = %d, want >= 2", len(rets))
	}
	// Every ReturnVals mutant in this fixture is killed.
	for _, m := range rets {
		if m.Status != string(mutant.Killed) {
			t.Errorf("ReturnVals %s@%d status = %s, want KILLED (%s)", m.RelFile, m.Line, m.Status, m.Desc)
		}
	}

	// SURVIVED: boundary "a < 0" -> "a <= 0" in Abs (no test for a == 0).
	absB := find("ConditionalsBoundary", "math.go", `replaced "a < 0" with "a <= 0"`)
	check(absB, mutant.Survived, "Abs boundary")

	// KILLED: boundary "i < n" -> "i <= n" in Count.
	countB := find("ConditionalsBoundary", "math.go", `replaced "i < n" with "i <= n"`)
	check(countB, mutant.Killed, "Count boundary")

	// KILLED: negated condition in Abs.
	neg := find("NegateConditionals", "math.go", "a < 0")
	// (The negated *for* condition in Count also hangs: TIMED_OUT,
	// covered by the i-- case above; both are "detected".)
	check(neg, mutant.Killed, "Abs negated condition")

	// TIMED_OUT: i++ -> i-- in Count.
	inc := find("Increments", "math.go", "i++")
	check(inc, mutant.TimedOut, "Count i++ -> i--")

	// NO_COVERAGE: everything in Unused.
	nc := find("Math", "math.go", "x * 2")
	if len(nc) != 1 {
		t.Errorf("Unused math mutant = %d, want 1", len(nc))
	} else if nc[0].Status != string(mutant.NoCoverage) {
		t.Errorf("Unused mutant status = %s, want NO_COVERAGE", nc[0].Status)
	}
	ncConst := find("Constant", "math.go", "constant 2 replaced with 3")
	if len(ncConst) != 1 || ncConst[0].Status != string(mutant.NoCoverage) {
		t.Errorf("Unused constant mutant: %+v", ncConst)
	}

	// Score sanity (D5): main = detected/(total-NO_COVERAGE).
	s := rep.Score
	if s.Total != len(pkg.Mutants) {
		t.Errorf("score total = %d, mutants = %d", s.Total, len(pkg.Mutants))
	}
	if s.Detected != s.Killed+s.TimedOut {
		t.Errorf("detected = %d, killed+timedout = %d", s.Detected, s.Killed+s.TimedOut)
	}
	want := pct(s.Detected, s.Total-s.NoCoverage)
	if s.Main != want {
		t.Errorf("main score = %.1f, want %.1f", s.Main, want)
	}
	if s.Main <= 50 || s.Main > 100 {
		t.Errorf("main score %.1f%% outside sane range (50, 100]", s.Main)
	}
	t.Logf("mutation score: main=%.1f%% raw=%.1f%% (killed=%d survived=%d timedout=%d nocov=%d compile=%d runerr=%d)",
		s.Main, s.Raw, s.Killed, s.Survived, s.TimedOut, s.NoCoverage, s.CompileError, s.RunError)
}

func pct(detected, total int) float64 {
	if total <= 0 {
		return 100
	}
	return float64(detected) / float64(total) * 100
}
