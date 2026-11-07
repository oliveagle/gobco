package impact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oliveagle/gobco/gomut/internal/cover"
	"github.com/oliveagle/gobco/gomut/internal/engine"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

// IncrementalEngine extends the base engine with impact analysis.
type IncrementalEngine struct {
	*engine.Engine
	dir         string
	baselineRef string
	version     string
	domain      *Domain
	callGraph   *CallGraph
	lineTests   *cover.LineToTests
}

// NewIncremental creates a new incremental engine.
func NewIncremental(opts engine.Options, baselineRef string) *IncrementalEngine {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &IncrementalEngine{
		Engine:      engine.New(opts),
		dir:         opts.Dir,
		baselineRef: baselineRef,
		version:     version,
	}
}

// RunIncremental performs incremental mutation testing based on code changes.
func (ie *IncrementalEngine) RunIncremental(ctx context.Context) (*report.Report, error) {
	changes, err := ParseGitDiff(ie.dir, ie.baselineRef)
	if err != nil {
		return nil, fmt.Errorf("parsing git diff: %w", err)
	}

	if len(changes) == 0 {
		fmt.Fprintf(os.Stderr, "No Go file changes detected in diff\n")
		rep := report.New("gomut", ie.version, map[string][]*mutant.Mutant{}, map[string][]string{}, nil)
		rep.Mode = "incremental-no-diff"
		return rep, nil
	}

	fmt.Fprintf(os.Stderr, "Found %d changed Go files\n", len(changes))

	pkgs, err := ie.discoverPackages()
	if err != nil {
		return nil, fmt.Errorf("discovering packages: %w", err)
	}

	cg, err := BuildCallGraph(ie.dir, pkgs)
	if err != nil {
		return nil, fmt.Errorf("building call graph: %w", err)
	}

	ie.callGraph = cg
	fmt.Fprintf(os.Stderr, "Built call graph with %d functions, %d interfaces\n",
		len(cg.Funcs), len(cg.Interfaces))

	domain := ComputeDomain(cg, changes)
	ie.domain = domain
	fmt.Fprintf(os.Stderr, "Impact domain: %s\n", domain.String())

	return ie.runFiltered(ctx, changes)
}

// runFiltered performs filtered mutation testing based on the impact domain.
func (ie *IncrementalEngine) runFiltered(ctx context.Context, changes []Change) (*report.Report, error) {
	mutantFilter := func(m *mutant.Mutant) bool {
		return isInImpactDomainFromMutant(m, ie.domain, ie.callGraph)
	}

	testFilter := func(m *mutant.Mutant, tests []string) []string {
		if ie.domain == nil || len(tests) == 0 {
			return tests
		}
		if ie.lineTests != nil {
			return ie.filterTestsByDomain(m, tests)
		}
		return tests
	}

	return ie.Engine.RunFiltered(ctx, mutantFilter, testFilter)
}

// filterTestsByDomain returns only tests that cover lines in the impact domain.
func (ie *IncrementalEngine) filterTestsByDomain(m *mutant.Mutant, tests []string) []string {
	if ie.domain == nil || ie.lineTests == nil {
		return tests
	}

	affectedLines := ie.domain.AffectedLines(m.RelFile)
	if len(affectedLines) == 0 {
		return tests
	}

	testSet := make(map[string]bool)
	for _, line := range affectedLines {
		coveringTests := ie.lineTests.TestNamesAt(m.RelFile, line)
		for _, t := range coveringTests {
			testSet[t] = true
		}
	}

	var filtered []string
	for _, t := range tests {
		if testSet[t] {
			filtered = append(filtered, t)
		}
	}

	if len(filtered) == 0 {
		return tests
	}
	return filtered
}

// isInImpactDomainFromMutant checks if a mutant is within the impact domain.
func isInImpactDomainFromMutant(m *mutant.Mutant, domain *Domain, cg *CallGraph) bool {
	if domain == nil {
		return true
	}

	// ChangedLines is keyed by the absolute path supplied by the call graph,
	// while mutants carry a repository-relative path. Resolve the mutant path
	// against the graph before comparing, otherwise every direct-line match is
	// missed and incremental mode silently filters out all generated mutants.
	absFile := resolveAbsPath(cg, m.RelFile)
	if lines, ok := domain.ChangedLines[absFile]; ok {
		if lines[m.Line] {
			return true
		}
	}

	node := mutantNodeAtLine(cg, m)
	if node == nil {
		return false
	}

	return domain.All[FuncKey(node.Package, node.Name)]
}

// mutantNodeAtLine maps a generated mutant back to the call-graph function
// that contains its source line. Mutants do not carry a function name, so
// this mapping is required before domain membership can be checked.
//
// The previous implementation matched package and file-name substrings from
// the domain keys. That cannot find a caller's mutants (a caller is in the
// domain by function, but often has no changed line), and it can accidentally
// include unrelated functions that happen to share a file name.
func mutantNodeAtLine(cg *CallGraph, m *mutant.Mutant) *FuncNode {
	if cg == nil {
		return nil
	}

	absFile := resolveAbsPath(cg, m.RelFile)
	nodes := cg.ByFile[absFile]

	// Prefer the narrowest containing node if metadata or generated fixtures
	// ever overlap. This keeps interface declarations from shadowing a
	// concrete method at the same source location.
	var found *FuncNode
	for _, node := range nodes {
		if m.Line < node.StartLine || m.Line > node.EndLine {
			continue
		}
		if found == nil || node.EndLine-node.StartLine < found.EndLine-found.StartLine {
			found = node
		}
	}
	return found
}

// GetDomain returns the computed impact domain.
func (ie *IncrementalEngine) GetDomain() *Domain {
	return ie.domain
}

// GetCallGraph returns the built call graph.
func (ie *IncrementalEngine) GetCallGraph() *CallGraph {
	return ie.callGraph
}

// FilterMutants filters mutants to only those in the impact domain.
func FilterMutants(mutations []*MutantInfo, domain *Domain) []*MutantInfo {
	if domain == nil {
		return mutations
	}
	var filtered []*MutantInfo
	for _, m := range mutations {
		if isInImpactDomain(m, domain) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// isInImpactDomain checks if a mutant is within the impact domain.
func isInImpactDomain(m *MutantInfo, domain *Domain) bool {
	absFile := resolveAbsPath(nil, m.File)
	if lines, ok := domain.ChangedLines[absFile]; ok {
		if lines[m.Line] {
			return true
		}
	}
	key := FuncKey(m.Package, m.Function)
	return domain.All[key]
}

// FilterTestsForDomain filters tests to only those covering the impact domain.
func FilterTestsForDomain(lt *cover.LineToTests, domain *Domain, allTests []string) []string {
	if domain == nil || lt == nil {
		return allTests
	}
	testSet := make(map[string]bool)
	for file, lines := range domain.ChangedLines {
		for line := range lines {
			tests := lt.TestNamesAt(file, line)
			for _, test := range tests {
				testSet[test] = true
			}
		}
	}
	tests := make([]string, 0, len(testSet))
	for test := range testSet {
		tests = append(tests, test)
	}
	sort.Strings(tests)
	return tests
}

// discoverPackages finds all Go packages in the directory.
func (ie *IncrementalEngine) discoverPackages() ([]string, error) {
	var packages []string
	err := filepath.Walk(ie.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !info.IsDir() && filepath.Ext(path) == ".go" && !isTestFile(path) {
			dir := filepath.Dir(path)
			rel, err := filepath.Rel(ie.dir, dir)
			if err == nil && rel != "." && rel != "" {
				packages = append(packages, rel)
			}
		}
		return nil
	})
	seen := make(map[string]bool)
	var unique []string
	for _, pkg := range packages {
		if !seen[pkg] {
			seen[pkg] = true
			unique = append(unique, pkg)
		}
	}
	return unique, err
}

// isTestFile checks if a file is a test file.
func isTestFile(path string) bool {
	name := filepath.Base(path)
	return strings.HasSuffix(name, "_test.go")
}

// FilterTests selects only tests that cover the impact domain.
func FilterTests(lt *cover.LineToTests, domain *Domain, allTests []string) []string {
	if domain == nil || lt == nil {
		return allTests
	}
	testSet := make(map[string]bool)
	for file, lines := range domain.ChangedLines {
		for line := range lines {
			tests := lt.TestNamesAt(file, line)
			for _, test := range tests {
				testSet[test] = true
			}
		}
	}
	tests := make([]string, 0, len(testSet))
	for test := range testSet {
		tests = append(tests, test)
	}
	sort.Strings(tests)
	return tests
}
