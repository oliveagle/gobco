package impact

import (
	"fmt"
	"testing"
)

func TestDebugDomain(t *testing.T) {
	// Create a test call graph manually
	cg := &CallGraph{
		Funcs:  make(map[string]*FuncNode),
		ByFile: make(map[string][]*FuncNode),
	}

	// Create functions with call relationships
	a := &FuncNode{Name: "A", Package: "pkg", File: "math.go", StartLine: 1, EndLine: 10}
	b := &FuncNode{Name: "B", Package: "pkg", File: "math.go", StartLine: 20, EndLine: 30}
	c := &FuncNode{Name: "C", Package: "pkg", File: "math.go", StartLine: 40, EndLine: 50}

	cg.Funcs[FuncKey("pkg", "A")] = a
	cg.Funcs[FuncKey("pkg", "B")] = b
	cg.Funcs[FuncKey("pkg", "C")] = c
	cg.ByFile["math.go"] = []*FuncNode{a, b, c}

	// A calls B, B calls C
	a.Callees = []*FuncNode{b}
	b.Callees = []*FuncNode{c}
	b.Callers = []*FuncNode{a}
	c.Callers = []*FuncNode{b}

	changes := []Change{
		{File: "math.go", AddedLines: []int{25}}, // Change in B
	}

	domain := ComputeDomain(cg, changes)
	fmt.Printf("Changed: %v\n", domain.Changed)
	fmt.Printf("Callees: %v\n", domain.Callees)
	fmt.Printf("Callers: %v\n", domain.Callers)
	fmt.Printf("All: %v\n", domain.All)

	// Verify
	if len(domain.Changed) != 1 {
		t.Errorf("Expected 1 changed function, got %d", len(domain.Changed))
	}
	if len(domain.Callees) != 2 { // B and C
		t.Errorf("Expected 2 callee functions (B,C), got %d", len(domain.Callees))
	}
	if len(domain.Callers) != 2 { // B and A
		t.Errorf("Expected 2 caller functions (B,A), got %d", len(domain.Callers))
	}
}
