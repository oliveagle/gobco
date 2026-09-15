package impact

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseGitDiffIndex(t *testing.T) {
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

	// Modify the file (unstaged)
	newSrc := `package main

func main() {
	fmt.Println("world")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(newSrc), 0644); err != nil {
		t.Fatal(err)
	}

	// Parse unstaged diff
	changes, err := ParseGitDiffIndex(dir)
	if err != nil {
		t.Fatalf("Failed to parse git diff index: %v", err)
	}

	if len(changes) == 0 {
		t.Error("Expected at least one change")
	}
}

func TestParseGitDiffStaged(t *testing.T) {
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

	// Stage the changes
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Parse staged diff
	changes, err := ParseGitDiffStaged(dir)
	if err != nil {
		t.Fatalf("Failed to parse git diff staged: %v", err)
	}

	if len(changes) == 0 {
		t.Error("Expected at least one change")
	}
}
