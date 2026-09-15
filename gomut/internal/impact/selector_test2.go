package impact

import (
	"testing"

	"github.com/oliveagle/gobco/gomut/internal/cover"
)

func TestNewSelector(t *testing.T) {
	domain := &Domain{
		Changed: map[string]bool{"pkg:Func": true},
		All:     map[string]bool{"pkg:Func": true},
	}

	selector := NewSelector(nil, domain)
	if selector == nil {
		t.Error("Expected non-nil selector")
	}
	if selector.domain != domain {
		t.Error("Expected domain to be set")
	}
}

func TestNewSelectorWithGraph(t *testing.T) {
	domain := &Domain{
		Changed: map[string]bool{"pkg:Func": true},
		All:     map[string]bool{"pkg:Func": true},
	}

	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	selector := NewSelectorWithGraph(nil, domain, cg)
	if selector == nil {
		t.Error("Expected non-nil selector")
	}
	if selector.domain != domain {
		t.Error("Expected domain to be set")
	}
	if selector.callGraph != cg {
		t.Error("Expected callGraph to be set")
	}
}

func TestSelectTestsForDomainNilGraph(t *testing.T) {
	domain := &Domain{
		Changed: map[string]bool{"pkg:Func": true},
		All:     map[string]bool{"pkg:Func": true},
	}

	selector := NewSelector(nil, domain)
	tests := selector.SelectTestsForDomain()

	if tests != nil {
		t.Error("Expected nil tests when callGraph is nil")
	}
}

func TestGetTestCoverageNil(t *testing.T) {
	result := GetTestCoverage(nil, nil, nil)
	if result != nil {
		t.Error("Expected nil result")
	}
}

func TestGetImpactSummaryNil(t *testing.T) {
	result := GetImpactSummary(nil, nil, nil)
	if result != nil {
		t.Error("Expected nil result")
	}
}

func TestGetImpactSummaryWithTestCoverage(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	a := &FuncNode{Name: "A", Package: "pkg", File: "file.go", StartLine: 1, EndLine: 10}
	cg.Funcs[FuncKey("pkg", "A")] = a

	domain := &Domain{
		Changed:      map[string]bool{"pkg:A": true},
		ChangedLines: map[string]map[int]bool{"file.go": {5: true}},
	}

	// Create a mock LineToTests (we can't easily create one without the cover package)
	// For now, just test with nil
	summary := GetImpactSummary(domain, cg, nil)

	if summary == nil {
		t.Fatal("Expected non-nil summary")
	}

	if summary.AffectedTests != 0 {
		t.Errorf("Expected 0 affected tests, got %d", summary.AffectedTests)
	}
}

func TestGetImpactSummaryWithTestCoverageWithData(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	a := &FuncNode{Name: "A", Package: "pkg", File: "file.go", StartLine: 1, EndLine: 10}
	cg.Funcs[FuncKey("pkg", "A")] = a

	domain := &Domain{
		Changed:      map[string]bool{"pkg:A": true},
		ChangedLines: map[string]map[int]bool{"file.go": {5: true}},
	}

	// Create a mock LineToTests
	lt := &cover.LineToTests{}

	summary := GetImpactSummary(domain, cg, lt)

	if summary == nil {
		t.Fatal("Expected non-nil summary")
	}

	if summary.AffectedTests != 0 {
		t.Errorf("Expected 0 affected tests, got %d", summary.AffectedTests)
	}
}

func TestGetImpactSummaryWithCallerFuncs(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	a := &FuncNode{Name: "A", Package: "pkg", File: "file.go", StartLine: 1, EndLine: 10}
	b := &FuncNode{Name: "B", Package: "pkg", File: "file.go", StartLine: 20, EndLine: 30}
	cg.Funcs[FuncKey("pkg", "A")] = a
	cg.Funcs[FuncKey("pkg", "B")] = b

	domain := &Domain{
		Changed:      map[string]bool{"pkg:A": true},
		Callees:      map[string]bool{"pkg:B": true},
		Callers:     map[string]bool{},
		All:          map[string]bool{"pkg:A": true, "pkg:B": true},
		ChangedLines: map[string]map[int]bool{"file.go": {5: true}},
	}

	summary := GetImpactSummary(domain, cg, nil)

	if summary == nil {
		t.Fatal("Expected non-nil summary")
	}

	if summary.TotalFunctions != 2 {
		t.Errorf("Expected 2 total functions, got %d", summary.TotalFunctions)
	}

	if len(summary.ChangedFuncs) != 1 {
		t.Errorf("Expected 1 changed function, got %d", len(summary.ChangedFuncs))
	}

	if len(summary.CalleeFuncs) != 1 {
		t.Errorf("Expected 1 forward function, got %d", len(summary.CalleeFuncs))
	}

	if len(summary.CallerFuncs) != 0 {
		t.Errorf("Expected 0 backward functions, got %d", len(summary.CallerFuncs))
	}
}
