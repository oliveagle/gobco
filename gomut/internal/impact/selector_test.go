package impact

import (
	"testing"
)

func TestSelectMutants(t *testing.T) {
	// Create a test domain
	domain := &Domain{
		Changed:      map[string]bool{"pkg:Func": true},
		Callees:      map[string]bool{"pkg:Helper": true},
		Callers:     map[string]bool{"pkg:Caller": true},
		All:          map[string]bool{"pkg:Func": true, "pkg:Helper": true, "pkg:Caller": true},
		ChangedLines: map[string]map[int]bool{"file.go": {10: true, 20: true}},
	}

	// Create a selector
	selector := &Selector{
		domain: domain,
	}

	// Test mutants
	mutations := []MutantInfo{
		{Package: "pkg", File: "file.go", Line: 10, Function: "Func"},   // In domain
		{Package: "pkg", File: "file.go", Line: 15, Function: "Func"},   // In domain (function)
		{Package: "pkg", File: "file.go", Line: 30, Function: "Other"}, // Not in domain
	}

	selected := selector.SelectMutants(mutations)

	if len(selected) != 2 {
		t.Errorf("Expected 2 selected mutants, got %d", len(selected))
	}
}

func TestIsInDomain(t *testing.T) {
	domain := &Domain{
		Changed:      map[string]bool{"pkg:Func": true},
		Callees:      map[string]bool{"pkg:Helper": true},
		Callers:     map[string]bool{"pkg:Caller": true},
		All:          map[string]bool{"pkg:Func": true, "pkg:Helper": true, "pkg:Caller": true},
		ChangedLines: map[string]map[int]bool{"file.go": {10: true, 20: true}},
	}

	selector := &Selector{
		domain: domain,
	}

	tests := []struct {
		name string
		m    MutantInfo
		want bool
	}{
		{
			name: "in changed lines",
			m:    MutantInfo{Package: "pkg", File: "file.go", Line: 10, Function: "Func"},
			want: true,
		},
		{
			name: "in domain function",
			m:    MutantInfo{Package: "pkg", File: "file.go", Line: 30, Function: "Func"},
			want: true,
		},
		{
			name: "not in domain",
			m:    MutantInfo{Package: "pkg", File: "file.go", Line: 30, Function: "Other"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selector.isInDomain(tt.m); got != tt.want {
				t.Errorf("isInDomain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetImpactSummary(t *testing.T) {
	// Create a test call graph
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	// Create functions
	a := &FuncNode{Name: "A", Package: "pkg", File: "file.go", StartLine: 1, EndLine: 10}
	b := &FuncNode{Name: "B", Package: "pkg", File: "file.go", StartLine: 20, EndLine: 30}

	cg.Funcs[FuncKey("pkg", "A")] = a
	cg.Funcs[FuncKey("pkg", "B")] = b

	// Create a domain
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
}

func TestSortStrings(t *testing.T) {
	s := []string{"c", "a", "b"}
	sortStrings(s)

	expected := []string{"a", "b", "c"}
	for i, v := range s {
		if v != expected[i] {
			t.Errorf("Expected %q at index %d, got %q", expected[i], i, v)
		}
	}
}
