package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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
