package engine

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

// TestInvalidMutantClassification exercises the INVALID status end to
// end: the commaok fixture's "Lookup" is the classic comma-ok idiom
// "v, ok := m[k]; return v, ok" where each value is used only in the
// return. Every ReturnVals rewrite of that return therefore orphans
// one of the declared values ("return 0, ok" orphans v; "return v,
// true"/"return v, false" orphan ok), which Go rejects at compile
// time. With a known-good baseline the engine must classify all of
// them as INVALID (not COMPILE_ERROR), exclude them from the score
// denominator, and still kill every executable mutant of the fixture.
func TestInvalidMutantClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	dir := filepath.Join("..", "..", "testdata", "commaok")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		Dir:      abs,
		Patterns: []string{"."},
		Workers:  1,
		Timeout:  10 * time.Second,
		NoCache:  true,
	})
	rep, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	pkg := findPkg(t, rep, "github.com/oliveagle/gobco/gomut/testdata/commaok")

	// The three ReturnVals rewrites of the comma-ok return are all
	// non-compiling, hence INVALID — never COMPILE_ERROR, since the
	// baseline build is known-good and the fault is the patch's.
	invalids := make([]report.Mutant, 0, 3)
	for _, m := range pkg.Mutants {
		if m.Status == string(mutant.Invalid) {
			invalids = append(invalids, m)
		}
		if m.Status == string(mutant.CompileError) {
			t.Errorf("mutant %s@%s:%d = COMPILE_ERROR; a known-good baseline means the patch is at fault (want INVALID)",
				m.Operator, m.File, m.Line)
		}
	}
	if len(invalids) != 3 {
		t.Fatalf("expected exactly 3 INVALID mutants, got %d; statuses: %v",
			len(invalids), statusList(pkg.Mutants))
	}
	for _, m := range invalids {
		if m.Operator != "ReturnVals" || m.Line != 10 {
			t.Errorf("invalid mutant = %s @%s:%d, want a ReturnVals rewrite of the comma-ok return (line 10)",
				m.Operator, m.File, m.Line)
		}
	}

	// Every executable mutant (the Double function) must be killed by
	// the fixture's tests.
	killed := 0
	for _, m := range pkg.Mutants {
		switch m.Status {
		case string(mutant.Killed):
			killed++
		case string(mutant.Survived), string(mutant.TimedOut):
			t.Errorf("mutant %s @%s:%d = %s; the fixture tests kill every executable mutant",
				m.Operator, m.File, m.Line, m.Status)
		}
	}
	if killed != 3 {
		t.Errorf("killed = %d, want 3 (all Double mutants); statuses: %v", killed, statusList(pkg.Mutants))
	}

	// INVALID is excluded from the score denominator: 3 detected over
	// 6 total - 3 invalid = 100.
	if rep.Score.Invalid != 3 {
		t.Errorf("report score Invalid = %d, want 3", rep.Score.Invalid)
	}
	if rep.Score.Total != 6 || rep.Score.Detected != 3 {
		t.Errorf("report score = %+v", rep.Score)
	}
}

// statusList renders each mutant as "operator@file:line:status" for
// diagnostics.
func statusList(ms []report.Mutant) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Operator+"@"+m.File+":"+strconv.Itoa(m.Line)+":"+m.Status)
	}
	return out
}
