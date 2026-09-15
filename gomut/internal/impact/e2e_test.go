package impact

import (
	
	"path/filepath"
	"testing"
)

func TestGitDiffIntegration(t *testing.T) {
	// Use the actual gobco repo
	dir := "/Users/oliveagle/repos/github.com/oliveagle/gobco"

	if !IsGitRepo(dir) {
		t.Skip("Not a git repo")
	}

	// Parse git diff against HEAD
	changes, err := ParseGitDiff(dir, "HEAD")
	if err != nil {
		t.Fatalf("ParseGitDiff: %v", err)
	}

	t.Logf("Changes: %d files", len(changes))
	for _, c := range changes {
		t.Logf("  %s: +%d -%d lines", c.File, len(c.AddedLines), len(c.RemovedLines))
	}
}

func TestImpactAnalysisEndToEnd(t *testing.T) {
	dir := "/Users/oliveagle/repos/github.com/oliveagle/gobco"

	if !IsGitRepo(dir) {
		t.Skip("Not a git repo")
	}

	// Build call graph for gomut subdirectory
	gomutDir := filepath.Join(dir, "gomut")
	cg, err := BuildCallGraph(gomutDir, []string{"cmd/gomut", "internal/impact", "internal/engine", "internal/mutate", "internal/cover", "internal/exec", "internal/report", "internal/mutant"})
	if err != nil {
		t.Fatalf("BuildCallGraph: %v", err)
	}

	t.Logf("Call graph: %d functions, %d interfaces", len(cg.Funcs), len(cg.Interfaces))

	// Show some functions
	count := 0
	for k := range cg.Funcs {
		if count < 5 {
			t.Logf("  func: %s", k)
			count++
		}
	}

	// Compute impact of changes to domain.go
	domainFile := filepath.Join("internal", "impact", "domain.go")
	changes := []Change{
		{File: domainFile, AddedLines: []int{1, 2, 3}},
	}

	domain := ComputeDomain(cg, changes)
	t.Logf("Impact domain: %s", domain.String())

	// Show affected functions
	if len(domain.Changed) > 0 {
		t.Log("Changed functions:")
		for k := range domain.Changed {
			t.Logf("  %s", k)
		}
	}
	if len(domain.Forward) > 0 {
		t.Log("Forward (callees):")
		for k := range domain.Forward {
			if !domain.Changed[k] {
				t.Logf("  %s", k)
			}
		}
	}
	if len(domain.Backward) > 0 {
		t.Log("Backward (callers):")
		for k := range domain.Backward {
			if !domain.Changed[k] {
				t.Logf("  %s", k)
			}
		}
	}
}
