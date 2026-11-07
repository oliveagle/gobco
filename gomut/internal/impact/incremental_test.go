package impact

import (
	"testing"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

func TestFilterMutants(t *testing.T) {
	domain := &Domain{
		Changed:      map[string]bool{"pkg:Func": true},
		Callees:      map[string]bool{"pkg:Helper": true},
		Callers:      map[string]bool{"pkg:Caller": true},
		All:          map[string]bool{"pkg:Func": true, "pkg:Helper": true, "pkg:Caller": true},
		ChangedLines: map[string]map[int]bool{"file.go": {10: true, 20: true}},
	}

	mutations := []*MutantInfo{
		{Package: "pkg", File: "file.go", Line: 10, Function: "Func"},  // In domain
		{Package: "pkg", File: "file.go", Line: 15, Function: "Func"},  // In domain (function)
		{Package: "pkg", File: "file.go", Line: 30, Function: "Other"}, // Not in domain
	}

	filtered := FilterMutants(mutations, domain)

	if len(filtered) != 2 {
		t.Errorf("Expected 2 filtered mutants, got %d", len(filtered))
	}
}

func TestFilterMutantsNilDomain(t *testing.T) {
	mutations := []*MutantInfo{
		{Package: "pkg", File: "file.go", Line: 10, Function: "Func"},
	}

	filtered := FilterMutants(mutations, nil)

	if len(filtered) != 1 {
		t.Errorf("Expected 1 filtered mutant, got %d", len(filtered))
	}
}

func TestIsInImpactDomain(t *testing.T) {
	domain := &Domain{
		Changed:      map[string]bool{"pkg:Func": true},
		Callees:      map[string]bool{"pkg:Helper": true},
		Callers:      map[string]bool{"pkg:Caller": true},
		All:          map[string]bool{"pkg:Func": true, "pkg:Helper": true, "pkg:Caller": true},
		ChangedLines: map[string]map[int]bool{"file.go": {10: true, 20: true}},
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
			if got := isInImpactDomain(&tt.m, domain); got != tt.want {
				t.Errorf("isInImpactDomain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsInImpactDomainFromMutantMapsCallerLines(t *testing.T) {
	absFile := "/tmp/project/math.go"
	cg := &CallGraph{
		ByFile: map[string][]*FuncNode{
			absFile: {
				{Name: "Add", Package: "example.com/math", File: absFile, StartLine: 1, EndLine: 3},
				{Name: "Twice", Package: "example.com/math", File: absFile, StartLine: 5, EndLine: 7},
				{Name: "Mul", Package: "example.com/math", File: absFile, StartLine: 9, EndLine: 11},
			},
		},
	}
	domain := &Domain{
		Changed: map[string]bool{"example.com/math:Add": true},
		Callers: map[string]bool{"example.com/math:Twice": true},
		All: map[string]bool{
			"example.com/math:Add":   true,
			"example.com/math:Twice": true,
		},
		ChangedLines: map[string]map[int]bool{
			absFile: {2: true},
		},
	}

	tests := []struct {
		name string
		line int
		want bool
	}{
		{"changed function", 2, true},
		{"caller function", 6, true},
		{"unrelated function", 10, false},
		{"outside any function", 100, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mutant.Mutant{RelFile: "math.go", Line: tt.line}
			if got := isInImpactDomainFromMutant(m, domain, cg); got != tt.want {
				t.Fatalf("isInImpactDomainFromMutant(line %d) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main_test.go", true},
		{"main.go", false},
		{"test.go", false},
		{"_test.go", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isTestFile(tt.path); got != tt.want {
				t.Errorf("isTestFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestAffectedFiles(t *testing.T) {
	domain := &Domain{
		ChangedLines: map[string]map[int]bool{
			"file1.go": {10: true, 20: true},
			"file2.go": {30: true},
		},
	}

	files := domain.AffectedFiles()

	if len(files) != 2 {
		t.Errorf("Expected 2 affected files, got %d", len(files))
	}

	// Check that files are sorted
	if len(files) >= 2 && files[0] > files[1] {
		t.Error("Expected sorted files")
	}
}

func TestAffectedFilesNil(t *testing.T) {
	domain := &Domain{
		ChangedLines: map[string]map[int]bool{},
	}

	files := domain.AffectedFiles()

	if len(files) != 0 {
		t.Errorf("Expected 0 affected files, got %d", len(files))
	}
}
