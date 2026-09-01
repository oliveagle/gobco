// Package report renders mutation testing results.
//
// It owns the result data model (report.Report) and two renderers:
// human-readable text (aligned with gobco's output style) and JSON.
// HTML output is a v2 concern (todo T-11).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/oliveagle/gomut/internal/mutant"
)

// Mutant is one mutant and its outcome, in report form.
type Mutant struct {
	Operator string   `json:"operator"`
	Package  string   `json:"package"`
	File     string   `json:"file"` // import-path keyed (cover profile form)
	RelFile  string   `json:"relFile,omitempty"`
	Line     int      `json:"line"`
	Desc     string   `json:"description"`
	Status   string   `json:"status"`
	Killers  []string `json:"killers,omitempty"`
	Output   string   `json:"output,omitempty"`
}

// Pkg is the result for one package under test.
type Pkg struct {
	ImportPath string   `json:"importPath"`
	Operators  []string `json:"operators"`
	Mutants    []Mutant `json:"mutants"`
	// Errors are package-level problems (baseline failure, type-check
	// degradation notes) that do not attach to a single mutant.
	Errors []string `json:"errors,omitempty"`
}

// OpScore is the per-operator score table row.
type OpScore struct {
	Operator string  `json:"operator"`
	Total    int     `json:"total"`
	Detected int     `json:"detected"`
	Score    float64 `json:"score"` // percent
}

// Score summarizes the whole run (ADR-0001 D5).
type Score struct {
	Total        int `json:"total"`
	Detected     int `json:"detected"`
	NoCoverage   int `json:"noCoverage"`
	Killed       int `json:"killed"`
	Survived     int `json:"survived"`
	TimedOut     int `json:"timedOut"`
	CompileError int `json:"compileError"`
	RunError     int `json:"runError"`
	// Main is detected/(total-NO_COVERAGE) in percent: the coverage-
	// normalized mutation score.
	Main float64 `json:"main"`
	// Raw is detected/total in percent: the pitest raw ratio.
	Raw float64 `json:"raw"`
}

// Report is the full result of a mutation testing run.
type Report struct {
	Tool      string    `json:"tool"`
	Version   string    `json:"version"`
	Generated time.Time `json:"generated"`
	Packages  []Pkg     `json:"packages"`
	Score     Score     `json:"score"`
	ByOp      []OpScore `json:"byOperator"`
}

// New builds a Report from per-package mutant lists.
//
// pkgs maps a package import path to its mutants (already executed), and
// pkgErrors carries package-level notes. opNames is the operator set in
// report order.
func New(tool, version string, pkgs map[string][]*mutant.Mutant, pkgErrors map[string][]string, opNames []string) *Report {
	r := &Report{Tool: tool, Version: version, Generated: time.Now()}
	paths := make([]string, 0, len(pkgs))
	for p := range pkgs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		muts := pkgs[p]
		pr := Pkg{ImportPath: p, Operators: opNames}
		for _, m := range muts {
			pr.Mutants = append(pr.Mutants, toMutant(m))
		}
		pr.Errors = append(pr.Errors, pkgErrors[p]...)
		r.Packages = append(r.Packages, pr)
	}
	r.Score = computeScore(r.Packages)
	r.ByOp = computeByOp(r.Packages)
	return r
}

func toMutant(m *mutant.Mutant) Mutant {
	k := make([]string, len(m.Killers))
	copy(k, m.Killers)
	return Mutant{
		Operator: m.Operator,
		Package:  m.Package,
		File:     m.File,
		RelFile:  m.RelFile,
		Line:     m.Line,
		Desc:     m.Desc,
		Status:   string(m.Status),
		Killers:  k,
		Output:   m.Output,
	}
}

// computeScore implements ADR-0001 D5:
//
//	main = detected / (total - NO_COVERAGE); raw = detected / total.
//
// detected = KILLED + TIMED_OUT. A zero denominator scores 100 (there is
// nothing that could be detected), mirroring pitest.
func computeScore(pkgs []Pkg) Score {
	var s Score
	count := func(status string, det bool) {
		s.Total++
		switch status {
		case "NO_COVERAGE":
			s.NoCoverage++
			return
		case "KILLED":
			s.Killed++
		case "TIMED_OUT":
			s.TimedOut++
		case "COMPILE_ERROR":
			s.CompileError++
		case "RUN_ERROR":
			s.RunError++
		case "SURVIVED":
			s.Survived++
		}
		if det {
			s.Detected++
		}
	}
	for _, p := range pkgs {
		for _, m := range p.Mutants {
			var det bool
			switch m.Status {
			case "KILLED", "TIMED_OUT":
				det = true
			}
			count(m.Status, det)
		}
	}
	s.Main = pct(s.Detected, s.Total-s.NoCoverage)
	s.Raw = pct(s.Detected, s.Total)
	return s
}

// computeByOp groups the score per operator (same formulas as D5).
func computeByOp(pkgs []Pkg) []OpScore {
	type acc struct{ total, detected, nocov int }
	byOp := map[string]*acc{}
	order := []string{}
	for _, p := range pkgs {
		for _, m := range p.Mutants {
			a := byOp[m.Operator]
			if a == nil {
				a = &acc{}
				byOp[m.Operator] = a
				order = append(order, m.Operator)
			}
			a.total++
			switch m.Status {
			case "NO_COVERAGE":
				a.nocov++
			case "KILLED", "TIMED_OUT":
				a.detected++
			}
		}
	}
	out := make([]OpScore, 0, len(order))
	for _, name := range order {
		a := byOp[name]
		out = append(out, OpScore{
			Operator: name,
			Total:    a.total,
			Detected: a.detected,
			Score:    pct(a.detected, a.total-a.nocov),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Operator < out[j].Operator })
	return out
}

func pct(detected, total int) float64 {
	if total <= 0 {
		return 100
	}
	return float64(detected) / float64(total) * 100
}

// WriteJSON writes the report as pretty-printed JSON.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText renders a human-readable summary in gobco's style:
// an overall line, a per-operator table, per-package mutant details
// (survivors, non-pass outcomes and the full list with detail), and a
// per-file summary.
func WriteText(w io.Writer, r *Report, showAll bool) {
	fmt.Fprintf(w, "\nMutation testing: %.1f%% mutation score (%d/%d detected, %d no coverage)\n",
		r.Score.Main, r.Score.Detected, r.Score.Total-r.Score.NoCoverage, r.Score.NoCoverage)
	fmt.Fprintf(w, "  (raw: %d/%d = %.1f%%)\n\n", r.Score.Detected, r.Score.Total, r.Score.Raw)

	fmt.Fprint(w, "Per-operator:\n")
	fmt.Fprintf(w, "  %-22s %6s %6s %10s\n", "operator", "total", "detected", "score")
	for _, o := range r.ByOp {
		fmt.Fprintf(w, "  %-22s %6d %6d %9.1f%%\n", o.Operator, o.Total, o.Detected, o.Score)
	}

	for _, p := range r.Packages {
		fmt.Fprintf(w, "\n%s (%d mutants)\n", p.ImportPath, len(p.Mutants))
		for _, e := range p.Errors {
			fmt.Fprintf(w, "  !! %s\n", e)
		}
		for _, m := range p.Mutants {
			if !showAll && interesting(m) == "" {
				continue
			}
			fmt.Fprintf(w, "  %-12s %-18s %s:%d  %s", m.Status, m.Operator, m.RelFile, m.Line, m.Desc)
			if len(m.Killers) > 0 {
				fmt.Fprintf(w, "  [killed by %s]", strings.Join(m.Killers, ", "))
			}
			fmt.Fprintln(w)
			if m.Output != "" {
				for _, line := range strings.Split(strings.TrimRight(m.Output, "\n"), "\n") {
					fmt.Fprintf(w, "      | %s\n", line)
				}
			}
		}
		if showAll {
			continue
		}
		if n := countInteresting(p.Mutants); n == 0 {
			fmt.Fprintln(w, "  (all mutants killed)")
		}
	}
}

// interesting reports why a mutant deserves attention in a non-detailed
// output: an empty string means "killed, nothing to show".
func interesting(m Mutant) string {
	switch m.Status {
	case "KILLED":
		return ""
	default:
		return m.Status
	}
}

func countInteresting(ms []Mutant) int {
	n := 0
	for _, m := range ms {
		if interesting(m) != "" {
			n++
		}
	}
	return n
}
