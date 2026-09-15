package impact

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *hunkHeader
		wantNil bool
	}{
		{
			name:  "standard hunk",
			input: "@@ -10,5 +20,5 @@",
			want:  &hunkHeader{oldLine: 10, oldCount: 5, newLine: 20, newCount: 5},
		},
		{
			name:  "single line change",
			input: "@@ -1 +1 @@",
			want:  &hunkHeader{oldLine: 1, oldCount: 1, newLine: 1, newCount: 1},
		},
		{
			name:  "no count",
			input: "@@ -10 +20 @@",
			want:  &hunkHeader{oldLine: 10, oldCount: 1, newLine: 20, newCount: 1},
		},
		{
			name:    "invalid header",
			input:   "not a header",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHunkHeader(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.oldLine != tt.want.oldLine || got.oldCount != tt.want.oldCount ||
				got.newLine != tt.want.newLine || got.newCount != tt.want.newCount {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestIsGitRepo(t *testing.T) {
	// Test with current directory (should be a git repo)
	if !IsGitRepo(".") {
		t.Error("Expected current directory to be a git repo")
	}

	// Test with a non-git directory
	dir := t.TempDir()
	if IsGitRepo(dir) {
		t.Error("Expected temp directory to not be a git repo")
	}
}

func TestGetCurrentCommit(t *testing.T) {
	// This test requires a git repo with commits
	// Skip if not in a git repo
	if !IsGitRepo(".") {
		t.Skip("Not in a git repo")
	}

	commit, err := GetCurrentCommit(".")
	if err != nil {
		t.Fatalf("Failed to get current commit: %v", err)
	}

	if commit == "" {
		t.Error("Expected non-empty commit hash")
	}

	// Commit hashes are typically 40 hex characters
	if len(commit) != 40 {
		t.Errorf("Expected 40 character commit hash, got %d", len(commit))
	}
}

func TestParseGitDiff(t *testing.T) {
	// This test requires a git repo with changes
	// Skip if not in a git repo
	if !IsGitRepo(".") {
		t.Skip("Not in a git repo")
	}

	// Create a temporary directory with a Go file
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create a Go file
	src := `package main

func main() {
	fmt.Println("hello")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	// Add and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Modify the file
	newSrc := `package main

func main() {
	fmt.Println("world")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(newSrc), 0644); err != nil {
		t.Fatal(err)
	}

	// Parse diff
	changes, err := ParseGitDiff(dir, "")
	if err != nil {
		t.Fatalf("Failed to parse git diff: %v", err)
	}

	if len(changes) == 0 {
		t.Error("Expected at least one change")
	}

	// Check that main.go is in the changes
	found := false
	for _, c := range changes {
		if c.File == "main.go" {
			found = true
			if len(c.AddedLines) == 0 {
				t.Error("Expected added lines in main.go")
			}
			break
		}
	}
	if !found {
		t.Error("Expected main.go to be in changes")
	}
}
