package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/oliveagle/gomut/internal/mutant"
)

// buildSample returns a fixed set of mutants with a spread of statuses plus
// their per-operator groupings.
func buildSample() (map[string][]*mutant.Mutant, map[string][]string, []string) {
	pkgs := map[string][]*mutant.Mutant{
		"example.com/pkg": {
			{
				Operator: "Math", File: "example.com/pkg/a.go", RelFile: "a.go",
				Line: 5, Desc: "replaced \"*\" with \"/\"",
				Status: mutant.Killed, Killers: []string{"TestA"},
			},
			{
				Operator: "Math", File: "example.com/pkg/a.go", RelFile: "a.go",
				Line: 6, Desc: "replaced \"+\" with \"-\"",
				Status: mutant.Survived,
			},
			{
				Operator: "Increments", File: "example.com/pkg/a.go", RelFile: "a.go",
				Line: 7, Desc: "replaced \"i++\" with \"i--\"",
				Status: mutant.TimedOut,
			},
			{
				Operator: "Constant", File: "example.com/pkg/a.go", RelFile: "a.go",
				Line: 8, Desc: "constant \"1\" replaced with \"2\"",
				Status: mutant.NoCoverage,
			},
		},
	}
	pkgErrors := map[string][]string{
		"example.com/pkg": {"type-check degraded to syntactic operators"},
	}
	opNames := []string{"Math", "Increments", "Constant"}
	return pkgs, pkgErrors, opNames
}

// TestScoreReportText builds a report with the sample mutants and checks the
// ADR-0001 D5 scoring, the JSON renderer, and the text renderer.
func TestScoreReportText(t *testing.T) {
	pkgs, pkgErrors, opNames := buildSample()

	const eps = 1e-9
	r := New("gomut", "0.1.0", pkgs, pkgErrors, opNames)

	// Overall score (ADR-0001 D5): main = detected/(total-NO_COVERAGE);
	// raw = detected/total.
	if r.Score.Total != 4 || r.Score.Detected != 2 || r.Score.NoCoverage != 1 ||
		r.Score.Killed != 1 || r.Score.Survived != 1 || r.Score.TimedOut != 1 {
		t.Errorf("score counts = %+v", r.Score)
	}
	if d := math.Abs(r.Score.Main - 66.66666666666667); d > eps {
		t.Errorf("main score = %v, want ~66.67", r.Score.Main)
	}
	if d := math.Abs(r.Score.Raw - 50); d > eps {
		t.Errorf("raw score = %v, want 50", r.Score.Raw)
	}

	// ByOperator grouping, sorted alphabetically by operator.
	wantByOp := []struct {
		op    string
		total int
		det   int
		score float64
	}{
		{"Constant", 1, 0, 100},
		{"Increments", 1, 1, 100},
		{"Math", 2, 1, 50},
	}
	if len(r.ByOp) != len(wantByOp) {
		t.Fatalf("ByOp len = %d, want %d (%+v)", len(r.ByOp), len(wantByOp), r.ByOp)
	}
	for i, w := range wantByOp {
		o := r.ByOp[i]
		if o.Operator != w.op || o.Total != w.total || o.Detected != w.det {
			t.Errorf("ByOp[%d] = %+v, want %+v", i, o, w)
		}
		if d := math.Abs(o.Score - w.score); d > eps {
			t.Errorf("ByOp[%d] score = %v, want %v", i, o.Score, w.score)
		}
	}

	// JSON round-trips to valid JSON.
	var jb bytes.Buffer
	if err := WriteJSON(&jb, r); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(jb.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON parse: %v\n%s", err, jb.String())
	}
	if decoded["tool"] != "gomut" {
		t.Errorf("json tool = %v, want gomut", decoded["tool"])
	}

	// Text renderer in compact mode hides killed mutants.
	var tb bytes.Buffer
	WriteText(&tb, r, false)
	out := tb.String()
	for _, want := range []string{
		"Mutation testing: 66.7% mutation score (2/3 detected, 1 no coverage)",
		"(raw: 2/4 = 50.0%)",
		"Per-operator:",
		"example.com/pkg (4 mutants)",
		"!! type-check degraded to syntactic operators",
		"SURVIVED",
		"TIMED_OUT",
		"NO_COVERAGE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "KILLED") {
		t.Errorf("compact view should hide killed mutants:\n%s", out)
	}

	// Full view shows every mutant.
	var tb2 bytes.Buffer
	WriteText(&tb2, r, true)
	if !strings.Contains(tb2.String(), "KILLED") {
		t.Errorf("full view should show killed mutants")
	}
}

// TestZeroDenominatorScoresHundred checks the pitest mirroring rule: a
// zero detected-over-no-cov denominator scores 100.
func TestZeroDenominatorScoresHundred(t *testing.T) {
	pkgs := map[string][]*mutant.Mutant{
		"example.com/x": {
			{Operator: "Math", File: "x.go", RelFile: "x.go", Line: 1, Desc: "x", Status: mutant.NoCoverage},
		},
	}
	r := New("gomut", "0.1.0", pkgs, nil, []string{"Math"})
	if r.Score.Main != 100 {
		t.Errorf("main = %v, want 100 (zero denominator)", r.Score.Main)
	}
	if r.Score.Total != 1 || r.Score.NoCoverage != 1 || r.Score.Detected != 0 {
		t.Errorf("score = %+v", r.Score)
	}
	if r.Score.Raw != 0 {
		t.Errorf("raw = %v, want 0", r.Score.Raw)
	}
}
