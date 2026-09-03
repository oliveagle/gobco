// Package cover implements coverage-based test selection for mutation testing.
//
// It parses Go coverage profiles produced by "go test -coverprofile", both for
// the baseline (all tests) and per-test profiles, then builds mappings from
// source lines to the tests that cover them. This is the coverage-based approach
// validated by pitest as the most effective test-selection strategy.
package cover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Statement is one source block reported by a coverage profile.
//
// A statement spans the line range [Start, End]. Covered reports whether the
// block was executed at least once.
type Statement struct {
	// File is the package path with the file name, e.g.
	// "github.com/oliveagle/gobco/gomut/a.go". It matches the file key used elsewhere.
	File string
	// Start and End are the inclusive line numbers the statement occupies.
	Start, End int
	// Covered reports whether the statement was executed at least once.
	Covered bool
}

// Profile is a parsed "go test -coverprofile" output.
type Profile struct {
	Statements []Statement
}

// ParseProfile reads a single Go coverage profile from data.
//
// It accepts the modern two-position form
// ("file:startline.startcol,endline.endcol N M") as well as the older
// single-position form ("file:line.startcol N M"). The "mode:" header and
// blank lines are ignored.
func ParseProfile(data []byte) (*Profile, error) {
	p := &Profile{}
	lineNum := 0
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// Format: file:startcol,endcol <nstmts> <ncovered>
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("malformed coverage profile at line %d: expected at least 3 fields, got %d", lineNum, len(fields))
		}
		file, startLine, endLine, err := parsePositionField(fields[0])
		if err != nil {
			return nil, fmt.Errorf("malformed coverage profile at line %d: %w", lineNum, err)
		}
		nCovered, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("malformed coverage profile at line %d: invalid coverage count %q: %w", lineNum, fields[2], err)
		}
		p.Statements = append(p.Statements, Statement{
			File:    file,
			Start:   startLine,
			End:     endLine,
			Covered: nCovered > 0,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading coverage profile: %w", err)
	}
	return p, nil
}

func parsePositionField(s string) (file string, startLine, endLine int, err error) {
	// Split into start and end positions on the comma that separates
	// them. The single-position (older) form has no comma. Filenames do
	// not contain commas on Unix.
	startStr := s
	var endStr string
	if comma := strings.LastIndexByte(s, ','); comma >= 0 {
		startStr = s[:comma]
		endStr = s[comma+1:]
	}

	// startStr is "file:line.startcol" (or "file:line").
	colon := strings.LastIndexByte(startStr, ':')
	if colon < 0 {
		err = errors.New("missing ':' separating file from line")
		return
	}
	file = startStr[:colon]

	startLine, err = parseLine(startStr[colon+1:])
	if err != nil {
		err = fmt.Errorf("invalid start position %q: %w", startStr[colon+1:], err)
		return
	}

	endLine = startLine
	if endStr != "" {
		endLine, err = parseLine(endStr)
		if err != nil {
			err = fmt.Errorf("invalid end position %q: %w", endStr, err)
			return
		}
	}
	if endLine < startLine {
		endLine = startLine
	}
	return
}

// parseLine extracts the line number from a "line" or "line.startcol"
// position field.
func parseLine(s string) (int, error) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return strconv.Atoi(s)
}

// CoveredLines expands the profile into a set of covered line numbers per file.
//
// A statement covers its whole inclusive line range when its count is > 0.
func (p *Profile) CoveredLines() map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, st := range p.Statements {
		if !st.Covered {
			continue
		}
		if out[st.File] == nil {
			out[st.File] = map[int]bool{}
		}
		for line := st.Start; line <= st.End; line++ {
			out[st.File][line] = true
		}
	}
	return out
}

// LineToTests maps a source line to the tests that cover it.
//
// It is built by the engine from per-test profiles. When a mutant's line is not
// covered by any test, the mutant has no viable tests and is classified
// NO_COVERAGE.
type LineToTests struct {
	byFile map[string]map[int]map[string]bool
	order  []string // test names, first-added first
	seen   map[string]bool
}

// Add records that testName covers the covered statements in prof.
func (l *LineToTests) Add(prof *Profile, testName string) {
	if l.byFile == nil {
		l.byFile = map[string]map[int]map[string]bool{}
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	// Record first-added order so TestNamesAt emits a deterministic list.
	if !l.seen[testName] {
		l.seen[testName] = true
		l.order = append(l.order, testName)
	}
	for _, st := range prof.Statements {
		if !st.Covered {
			continue
		}
		for line := st.Start; line <= st.End; line++ {
			if l.byFile[st.File] == nil {
				l.byFile[st.File] = map[int]map[string]bool{}
			}
			if l.byFile[st.File][line] == nil {
				l.byFile[st.File][line] = map[string]bool{}
			}
			l.byFile[st.File][line][testName] = true
		}
	}
}

// TestNamesAt reports the names of tests whose profiles cover the given line.
func (l *LineToTests) TestNamesAt(file string, line int) []string {
	m := l.byFile[file][line]
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	// Emit in first-added order so the result is deterministic
	// (Go map iteration order is otherwise randomized).
	for _, name := range l.order {
		if m[name] {
			out = append(out, name)
		}
	}
	return out
}

// FileLines is a map of package file to the set of covered line numbers.
type FileLines map[string]map[int]bool

// Contains reports whether line is covered in file.
func (f FileLines) Contains(file string, line int) bool {
	return f[file][line]
}

// ---- caching support ---------------------------------------------------

// lineTestsJSON is the serialized form of LineToTests. byFile is
// flattened to per-line sorted test-name lists so JSON stays compact and
// deterministic; UnmarshalJSON reconstructs the set-of-names form.
type lineTestsJSON struct {
	ByFile map[string]map[int][]string `json:"byFile"`
	Order  []string                    `json:"order"`
}

// MarshalJSON serializes LineToTests for the perTestCoverage cache. The
// result is deterministic (per-file/per-line test lists are sorted), so a
// cached file is byte-identical across runs with identical inputs.
func (l *LineToTests) MarshalJSON() ([]byte, error) {
	out := lineTestsJSON{
		ByFile: map[string]map[int][]string{},
		Order:  l.order,
	}
	for file, lines := range l.byFile {
		lm := map[int][]string{}
		for line, tests := range lines {
			names := make([]string, 0, len(tests))
			// Emit in first-added order for determinism.
			for _, name := range l.order {
				if tests[name] {
					names = append(names, name)
				}
			}
			lm[line] = names
		}
		out.ByFile[file] = lm
	}
	return json.Marshal(out)
}

// UnmarshalJSON restores a LineToTests previously written by MarshalJSON.
func (l *LineToTests) UnmarshalJSON(b []byte) error {
	var in lineTestsJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	l.byFile = map[string]map[int]map[string]bool{}
	l.order = in.Order
	l.seen = map[string]bool{}
	for _, name := range in.Order {
		l.seen[name] = true
	}
	for file, lines := range in.ByFile {
		fm := map[int]map[string]bool{}
		for line, names := range lines {
			sm := map[string]bool{}
			for _, name := range names {
				sm[name] = true
			}
			fm[line] = sm
		}
		l.byFile[file] = fm
	}
	return nil
}
