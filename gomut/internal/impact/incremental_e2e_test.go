package impact

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/engine"
)

// TestIncrementalRunsCallerMutants guards the "changed function + callers"
// contract with the real engine. Add is changed, Twice calls Add and must be
// retained, while Mul is unrelated and must be filtered out. All three live
// in the same file so a file-name fallback cannot pass this test.
func TestIncrementalRunsCallerMutants(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}

	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/mutatefixture\n\ngo 1.25\n",
		"math.go": `package mutatefixture

func Add(a, b int) int {
	return a + b
}

func Twice(a int) int {
	return Add(a, a)
}

func Mul(a, b int) int {
	return a * b
}
`,
		"math_test.go": `package mutatefixture

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", got)
	}
}

func TestTwice(t *testing.T) {
	if got := Twice(2); got != 4 {
		t.Fatalf("Twice(2) = %d, want 4", got)
	}
}

func TestMul(t *testing.T) {
	if got := Mul(2, 3); got != 6 {
		t.Fatalf("Mul(2, 3) = %d, want 6", got)
	}
}
`,
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	runIncrementalGit(t, root, "init", "-q")
	runIncrementalGit(t, root, "add", ".")
	runIncrementalGit(t, root, "commit", "-q", "-m", "init")

	changed := strings.Replace(files["math.go"], "return a + b\n", "return a + b + 0\n", 1)
	if changed == files["math.go"] {
		t.Fatalf("failed to build the changed Add fixture")
	}
	if err := os.WriteFile(filepath.Join(root, "math.go"), []byte(changed), 0o644); err != nil {
		t.Fatalf("rewrite math.go: %v", err)
	}

	e := NewIncremental(engine.Options{
		Dir:             root,
		Patterns:        []string{"."},
		Workers:         2,
		Timeout:         6 * time.Second,
		BaselineTimeout: 30 * time.Second,
		NoCache:         true,
		Logf:            t.Logf,
	}, "HEAD")
	rep, err := e.RunIncremental(context.Background())
	if err != nil {
		t.Fatalf("RunIncremental: %v", err)
	}

	if rep.Mode != "coverage-selected" {
		t.Fatalf("report mode = %q, want coverage-selected", rep.Mode)
	}

	gotLines := make(map[int]bool)
	var got []string
	for _, pkg := range rep.Packages {
		for _, m := range pkg.Mutants {
			got = append(got, m.RelFile+":"+strconv.Itoa(m.Line)+" "+m.Desc+" ("+m.Status+")")
			gotLines[m.Line] = true
		}
	}

	// The report keeps the module-relative source line, which is stable in
	// this fixture: Add returns on line 4, Twice on line 8, Mul on line 12.
	if !gotLines[4] {
		t.Errorf("Add mutant at line 4 was not selected")
	}
	if !gotLines[8] {
		t.Errorf("caller Twice mutant at line 8 was not selected")
	}
	if gotLines[12] {
		t.Errorf("unrelated Mul mutant at line 12 was selected")
	}
}

// TestIncrementalNoDiffDoesNotRunEverything pins the safe no-op behavior:
// an explicit incremental request with no Go diff must not silently become a
// full mutation run.
func TestIncrementalNoDiffDoesNotRunEverything(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/nodiff\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	runIncrementalGit(t, root, "init", "-q")
	runIncrementalGit(t, root, "add", ".")
	runIncrementalGit(t, root, "commit", "-q", "-m", "init")

	e := NewIncremental(engine.Options{Dir: root, Patterns: []string{"."}}, "HEAD")
	rep, err := e.RunIncremental(context.Background())
	if err != nil {
		t.Fatalf("RunIncremental: %v", err)
	}
	if rep.Mode != "incremental-no-diff" {
		t.Fatalf("report mode = %q, want incremental-no-diff", rep.Mode)
	}
	if rep.Score.Total != 0 {
		t.Fatalf("mutant total = %d, want 0", rep.Score.Total)
	}
}

func runIncrementalGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
