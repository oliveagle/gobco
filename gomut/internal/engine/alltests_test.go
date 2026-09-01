package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oliveagle/gomut/internal/mutant"
	"github.com/oliveagle/gomut/internal/report"
)

// TestAllTestsMode exercises the -all-tests engine mode (an untested path:
// TestEndToEnd, TestEngineCache and the unit tests all use coverage-based
// selection). The whole-test-suite mode must still generate exactly the same
// set of mutants, and — because every test runs for every mutant — it must
// never classify any mutant as NO_COVERAGE. The coverage-based mode, by
// contrast, excludes uncovered code from the score.
//
// Skipped with -short (it shells out to the Go toolchain).
func TestAllTestsMode(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	dir := filepath.Join("..", "..", "testdata", "sample")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	opt := func(allTests bool) Options {
		return Options{
			Dir:      abs,
			Patterns: []string{"."},
			Workers:  2,
			Timeout:  6 * time.Second,
			NoCache:  true,
			AllTests: allTests,
			// Logf intentionally nil: this test is not log-aware.
		}
	}

	covered, err := New(opt(false)).Run(context.Background())
	if err != nil {
		t.Fatalf("covered-selection run: %v", err)
	}
	all, err := New(opt(true)).Run(context.Background())
	if err != nil {
		t.Fatalf("all-tests run: %v", err)
	}

	if covered.Score.Total != all.Score.Total {
		t.Errorf("mutant total differs between modes: covered=%d all-tests=%d",
			covered.Score.Total, all.Score.Total)
	}
	if covered.Score.NoCoverage == 0 {
		t.Errorf("covered-selection mode should have NO_COVERAGE mutants (got 0)")
	}
	if all.Score.NoCoverage != 0 {
		t.Errorf("all-tests mode should never leave mutants NO_COVERAGE (got %d)", all.Score.NoCoverage)
	}
}

// mutateScore computes the ADR-0001 D5 mutation score for one package's
// mutants: detected / (total - no_coverage), where detected = killed + timed.
func mutateScore(ms []report.Mutant) float64 {
	var total, detected, nocov int
	for _, m := range ms {
		total++
		switch m.Status {
		case string(mutant.Killed), string(mutant.TimedOut):
			detected++
		case string(mutant.NoCoverage):
			nocov++
		}
	}
	if total-nocov > 0 {
		return float64(detected) / float64(total-nocov) * 100
	}
	return 100
}

// findPkg returns the package whose import path matches want in rep, or fails
// the test.
func findPkg(t *testing.T, rep *report.Report, want string) report.Pkg {
	t.Helper()
	for _, p := range rep.Packages {
		if p.ImportPath == want {
			return p
		}
	}
	t.Fatalf("package %q not found in report", want)
	return report.Pkg{}
}

// TestExamplesEffectiveness is a living end-to-end demonstration: it runs the
// engine against the two example modules in gomut/examples and asserts the
// weak-test module leaves mutants alive while the strong-test module kills
// almost everything. It doubles as documentation - see examples/README.md -
// and as a regression guard on the whole mutation pipeline.
//
// Skipped with -short (it shells out to the Go toolchain).
func TestExamplesEffectiveness(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	base := filepath.Join("..", "..", "examples")

	// Weak tests: mutation testing must surface the gap `go test` hides.
	weak, err := New(Options{
		Dir:      filepath.Join(base, "01-surviving-mutants"),
		Patterns: []string{"."},
		Workers:  2,
		Timeout:  10 * time.Second,
		NoCache:  true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("weak example run: %v", err)
	}
	weakPkg := findPkg(t, weak, "github.com/oliveagle/gomut/examples/01-surviving-mutants")
	if weakPkg.Mutants == nil {
		t.Fatal("no mutants generated for the weak example")
	}
	if got := mutateScore(weakPkg.Mutants); got >= 70 {
		t.Errorf("weak tests should score low (got %.1f%%)", got)
	}
	var survived, nocov int
	for _, m := range weakPkg.Mutants {
		switch m.Status {
		case string(mutant.Survived):
			survived++
		case string(mutant.NoCoverage):
			nocov++
		}
	}
	if survived < 5 {
		t.Errorf("weak tests should leave many survivors (got %d)", survived)
	}
	if nocov == 0 {
		t.Errorf("weak tests should leave no-coverage mutants (got 0)")
	}

	// Strong tests: almost every mutant is killed.
	strong, err := New(Options{
		Dir:      filepath.Join(base, "02-strong-tests"),
		Patterns: []string{"."},
		Workers:  2,
		Timeout:  10 * time.Second,
		NoCache:  true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("strong example run: %v", err)
	}
	strongPkg := findPkg(t, strong, "github.com/oliveagle/gomut/examples/02-strong-tests")
	if strongPkg.Mutants == nil {
		t.Fatal("no mutants generated for the strong example")
	}
	if got := mutateScore(strongPkg.Mutants); got < 75 {
		t.Errorf("strong tests should score high (got %.1f%%)", got)
	}
	for _, m := range strongPkg.Mutants {
		if m.Status == string(mutant.NoCoverage) {
			t.Errorf("strong tests should cover all code, mutant at %s:%d is NO_COVERAGE", m.RelFile, m.Line)
		}
	}
}
