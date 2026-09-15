package impact

import (
	"strings"
	"testing"
)

func TestAffectedLines(t *testing.T) {
	domain := &Domain{
		ChangedLines: map[string]map[int]bool{
			"file.go": {10: true, 20: true, 30: true},
		},
	}

	lines := domain.AffectedLines("file.go")

	if len(lines) != 3 {
		t.Errorf("Expected 3 affected lines, got %d", len(lines))
	}

	// Check that lines are sorted
	if len(lines) >= 2 && lines[0] > lines[1] {
		t.Error("Expected sorted lines")
	}
}

func TestAffectedLinesEmpty(t *testing.T) {
	domain := &Domain{
		ChangedLines: map[string]map[int]bool{},
	}

	lines := domain.AffectedLines("file.go")

	if len(lines) != 0 {
		t.Errorf("Expected 0 affected lines, got %d", len(lines))
	}
}

func TestString(t *testing.T) {
	domain := &Domain{
		Changed:  map[string]bool{"A": true, "B": true},
		Forward:  map[string]bool{"C": true},
		Backward: map[string]bool{"D": true},
		All:      map[string]bool{"A": true, "B": true, "C": true, "D": true},
	}

	s := domain.String()

	if !strings.Contains(s, "changed=2") {
		t.Error("Expected string to contain 'changed=2'")
	}
	if !strings.Contains(s, "forward=1") {
		t.Error("Expected string to contain 'forward=1'")
	}
	if !strings.Contains(s, "backward=1") {
		t.Error("Expected string to contain 'backward=1'")
	}
	if !strings.Contains(s, "total=4") {
		t.Error("Expected string to contain 'total=4'")
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{123, "123"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := itoa(tt.input); got != tt.want {
				t.Errorf("itoa(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindNodesByFile(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	a := &FuncNode{Name: "A", Package: "pkg", File: "/path/to/file.go"}
	b := &FuncNode{Name: "B", Package: "pkg", File: "/other/path/file.go"}

	cg.ByFile["/path/to/file.go"] = []*FuncNode{a}
	cg.ByFile["/other/path/file.go"] = []*FuncNode{b}

	// Test direct match
	nodes := findNodesByFile(cg, "/path/to/file.go")
	if len(nodes) != 1 {
		t.Errorf("Expected 1 node, got %d", len(nodes))
	}

	// Test base name match - both files have same base name
	nodes = findNodesByFile(cg, "file.go")
	// Note: findNodesByFile returns first match by base name, so this returns 1 node
	if len(nodes) != 1 {
		t.Errorf("Expected 1 node (first match by base name), got %d", len(nodes))
	}

	// Test suffix match
	nodes = findNodesByFile(cg, "to/file.go")
	if len(nodes) != 1 {
		t.Errorf("Expected 1 node, got %d", len(nodes))
	}
}

func TestOverlaps(t *testing.T) {
	node := &FuncNode{
		Name:      "Func",
		Package:   "pkg",
		File:      "file.go",
		StartLine: 10,
		EndLine:   20,
	}

	tests := []struct {
		name   string
		change Change
		want   bool
	}{
		{
			name:   "line in range",
			change: Change{File: "file.go", AddedLines: []int{15}},
			want:   true,
		},
		{
			name:   "line at start",
			change: Change{File: "file.go", AddedLines: []int{10}},
			want:   true,
		},
		{
			name:   "line at end",
			change: Change{File: "file.go", AddedLines: []int{20}},
			want:   true,
		},
		{
			name:   "line outside range",
			change: Change{File: "file.go", AddedLines: []int{25}},
			want:   false,
		},
		{
			name:   "different file",
			change: Change{File: "other.go", AddedLines: []int{15}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overlaps(node, tt.change); got != tt.want {
				t.Errorf("overlaps() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestForwardWalk(t *testing.T) {
	c := &FuncNode{Name: "C", Package: "pkg"}
	b := &FuncNode{Name: "B", Package: "pkg", Callees: []*FuncNode{c}}
	a := &FuncNode{Name: "A", Package: "pkg", Callees: []*FuncNode{b}}

	visited := make(map[string]bool)
	seen := make(map[string]bool)

	forwardWalk(a, visited, seen)

	if !visited[FuncKey("pkg", "A")] {
		t.Error("Expected A to be visited")
	}
	if !visited[FuncKey("pkg", "B")] {
		t.Error("Expected B to be visited")
	}
	if !visited[FuncKey("pkg", "C")] {
		t.Error("Expected C to be visited")
	}
}

func TestBackwardWalk(t *testing.T) {
	c := &FuncNode{Name: "C", Package: "pkg"}
	b := &FuncNode{Name: "B", Package: "pkg", Callers: []*FuncNode{c}}
	a := &FuncNode{Name: "A", Package: "pkg", Callers: []*FuncNode{b}}
	c.Callers = []*FuncNode{b}
	b.Callers = []*FuncNode{a}

	visited := make(map[string]bool)
	seen := make(map[string]bool)

	backwardWalk(c, visited, seen)

	if !visited[FuncKey("pkg", "C")] {
		t.Error("Expected C to be visited")
	}
	if !visited[FuncKey("pkg", "B")] {
		t.Error("Expected B to be visited")
	}
	if !visited[FuncKey("pkg", "A")] {
		t.Error("Expected A to be visited")
	}
}

func TestComputeDomainComplex(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	// Create a more complex call graph: A -> B -> C, D -> B, E -> C
	a := &FuncNode{Name: "A", Package: "pkg", File: "file.go", StartLine: 1, EndLine: 10}
	b := &FuncNode{Name: "B", Package: "pkg", File: "file.go", StartLine: 20, EndLine: 30}
	c := &FuncNode{Name: "C", Package: "pkg", File: "file.go", StartLine: 40, EndLine: 50}
	d := &FuncNode{Name: "D", Package: "pkg", File: "file.go", StartLine: 60, EndLine: 70}
	e := &FuncNode{Name: "E", Package: "pkg", File: "file.go", StartLine: 80, EndLine: 90}

	cg.Funcs[FuncKey("pkg", "A")] = a
	cg.Funcs[FuncKey("pkg", "B")] = b
	cg.Funcs[FuncKey("pkg", "C")] = c
	cg.Funcs[FuncKey("pkg", "D")] = d
	cg.Funcs[FuncKey("pkg", "E")] = e

	cg.ByFile["file.go"] = []*FuncNode{a, b, c, d, e}

	// Link: A -> B -> C, D -> B, E -> C
	a.Callees = []*FuncNode{b}
	b.Callees = []*FuncNode{c}
	b.Callers = []*FuncNode{a, d}
	c.Callers = []*FuncNode{b, e}

	changes := []Change{
		{File: "file.go", AddedLines: []int{25}}, // Change in B
	}

	domain := ComputeDomain(cg, changes)

	// B should be in changed
	if !domain.Changed[FuncKey("pkg", "B")] {
		t.Error("B should be in changed")
	}

	// C should be in forward (B calls C)
	if !domain.Forward[FuncKey("pkg", "C")] {
		t.Error("C should be in forward (B calls C)")
	}

	// A and D should be in backward (A and D call B)
	if !domain.Backward[FuncKey("pkg", "A")] {
		t.Error("A should be in backward (A calls B)")
	}
	if !domain.Backward[FuncKey("pkg", "D")] {
		t.Error("D should be in backward (D calls B)")
	}

	// E should not be in any set
	if domain.All[FuncKey("pkg", "E")] {
		t.Error("E should not be in the impact domain")
	}
}

func TestComputeDomainNoChanges(t *testing.T) {
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	changes := []Change{}

	domain := ComputeDomain(cg, changes)

	if len(domain.Changed) != 0 {
		t.Errorf("Expected 0 changed functions, got %d", len(domain.Changed))
	}
	if len(domain.Forward) != 0 {
		t.Errorf("Expected 0 forward functions, got %d", len(domain.Forward))
	}
	if len(domain.Backward) != 0 {
		t.Errorf("Expected 0 backward functions, got %d", len(domain.Backward))
	}
}

func TestBackwardWalkCycle(t *testing.T) {
	a := &FuncNode{Name: "A", Package: "pkg"}
	b := &FuncNode{Name: "B", Package: "pkg", Callers: []*FuncNode{a}}
	a.Callers = []*FuncNode{b} // Cycle: A -> B -> A

	visited := make(map[string]bool)
	seen := make(map[string]bool)

	backwardWalk(a, visited, seen)

	if !visited[FuncKey("pkg", "A")] {
		t.Error("Expected A to be visited")
	}
	if !visited[FuncKey("pkg", "B")] {
		t.Error("Expected B to be visited")
	}
}

func TestForwardWalkCycle(t *testing.T) {
	a := &FuncNode{Name: "A", Package: "pkg"}
	b := &FuncNode{Name: "B", Package: "pkg", Callees: []*FuncNode{a}}
	a.Callees = []*FuncNode{b} // Cycle: A -> B -> A

	visited := make(map[string]bool)
	seen := make(map[string]bool)

	forwardWalk(a, visited, seen)

	if !visited[FuncKey("pkg", "A")] {
		t.Error("Expected A to be visited")
	}
	if !visited[FuncKey("pkg", "B")] {
		t.Error("Expected B to be visited")
	}
}
