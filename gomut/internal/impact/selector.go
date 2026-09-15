package impact

import (
	"fmt"

	"github.com/oliveagle/gobco/gomut/internal/cover"
)

// Selector selects tests based on impact analysis.
type Selector struct {
	// lineToTests maps source lines to the tests that cover them.
	lineToTests *cover.LineToTests
	// domain is the computed impact domain.
	domain *Domain
	// callGraph is the call graph for function lookups.
	callGraph *CallGraph
}

// NewSelector creates a new test selector.
func NewSelector(lt *cover.LineToTests, domain *Domain) *Selector {
	return &Selector{
		lineToTests: lt,
		domain:      domain,
	}
}

// NewSelectorWithGraph creates a new test selector with call graph.
func NewSelectorWithGraph(lt *cover.LineToTests, domain *Domain, cg *CallGraph) *Selector {
	return &Selector{
		lineToTests: lt,
		domain:      domain,
		callGraph:   cg,
	}
}

// SelectTests returns the minimal set of tests that cover the impact domain.
//
// For each function in the domain, it finds all tests that cover any line
// within that function's body. This ensures we run tests that exercise the
// changed code paths.
func (s *Selector) SelectTests(cg *CallGraph) []string {
	testSet := make(map[string]bool)

	// For each file with changes, find tests covering affected lines
	for file, lines := range s.domain.ChangedLines {
		for line := range lines {
			tests := s.lineToTests.TestNamesAt(file, line)
			for _, test := range tests {
				testSet[test] = true
			}
		}
	}

	// Also find tests for all functions in the impact domain
	for key := range s.domain.All {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}
		// Find tests covering any line in this function
		for line := node.StartLine; line <= node.EndLine; line++ {
			tests := s.lineToTests.TestNamesAt(node.File, line)
			for _, test := range tests {
				testSet[test] = true
			}
		}
	}

	// Convert to sorted slice
	tests := make([]string, 0, len(testSet))
	for test := range testSet {
		tests = append(tests, test)
	}
	sortStrings(tests)
	return tests
}

// SelectTestsForDomain returns tests that cover the impact domain.
func (s *Selector) SelectTestsForDomain() []string {
	if s.callGraph == nil {
		return nil
	}
	return s.SelectTests(s.callGraph)
}

// SelectMutants filters mutants to only those in the impact domain.
func (s *Selector) SelectMutants(mutations []MutantInfo) []MutantInfo {
	var selected []MutantInfo
	for _, m := range mutations {
		if s.isInDomain(m) {
			selected = append(selected, m)
		}
	}
	return selected
}

// isInDomain checks if a mutant is within the impact domain.
func (s *Selector) isInDomain(m MutantInfo) bool {
	// Check if the mutant's line is in the changed lines
	if lines, ok := s.domain.ChangedLines[m.File]; ok {
		if lines[m.Line] {
			return true
		}
	}

	// Check if the mutant's function is in the domain
	key := FuncKey(m.Package, m.Function)
	return s.domain.All[key]
}

// MutantInfo contains information about a mutant for selection.
type MutantInfo struct {
	Package  string
	File     string
	Line     int
	Function string
}

// sortStrings sorts a slice of strings in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestCoverageInfo contains coverage information for a test.
type TestCoverageInfo struct {
	TestName    string
	CoveredLines map[string][]int // file -> line numbers
}

// GetTestCoverage returns coverage information for tests in the impact domain.
func GetTestCoverage(lt *cover.LineToTests, domain *Domain, cg *CallGraph) []TestCoverageInfo {
	if lt == nil || domain == nil {
		return nil
	}

	var result []TestCoverageInfo

	// For each function in the impact domain, find tests that cover it
	for key := range domain.All {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}

		// Find tests covering any line in this function
		testLines := make(map[string][]int)
		for line := node.StartLine; line <= node.EndLine; line++ {
			tests := lt.TestNamesAt(node.File, line)
			for _, test := range tests {
				testLines[test] = append(testLines[test], line)
			}
		}

		// Create coverage info for each test
		for test, lines := range testLines {
			result = append(result, TestCoverageInfo{
				TestName:     test,
				CoveredLines: map[string][]int{node.File: lines},
			})
		}
	}

	return result
}

// ImpactSummary contains a summary of the impact analysis.
type ImpactSummary struct {
	ChangedFiles   []string
	ChangedFuncs   []string
	ForwardFuncs   []string
	BackwardFuncs  []string
	TotalFunctions int
	AffectedTests  int
}

// GetImpactSummary returns a summary of the impact analysis.
func GetImpactSummary(domain *Domain, cg *CallGraph, lt *cover.LineToTests) *ImpactSummary {
	if domain == nil {
		return nil
	}

	summary := &ImpactSummary{
		TotalFunctions: len(domain.All),
	}

	// Get changed files
	summary.ChangedFiles = domain.AffectedFiles()

	// Get function names
	for key := range domain.Changed {
		if node, ok := cg.Funcs[key]; ok {
			summary.ChangedFuncs = append(summary.ChangedFuncs, 
				fmt.Sprintf("%s:%d-%d", node.File, node.StartLine, node.EndLine))
		}
	}

	for key := range domain.Forward {
		if !domain.Changed[key] {
			if node, ok := cg.Funcs[key]; ok {
				summary.ForwardFuncs = append(summary.ForwardFuncs,
					fmt.Sprintf("%s:%d-%d", node.File, node.StartLine, node.EndLine))
			}
		}
	}

	for key := range domain.Backward {
		if !domain.Changed[key] {
			if node, ok := cg.Funcs[key]; ok {
				summary.BackwardFuncs = append(summary.BackwardFuncs,
					fmt.Sprintf("%s:%d-%d", node.File, node.StartLine, node.EndLine))
			}
		}
	}

	// Count affected tests
	if lt != nil {
		testSet := make(map[string]bool)
		for file, lines := range domain.ChangedLines {
			for line := range lines {
				tests := lt.TestNamesAt(file, line)
				for _, test := range tests {
					testSet[test] = true
				}
			}
		}
		summary.AffectedTests = len(testSet)
	}

	return summary
}
