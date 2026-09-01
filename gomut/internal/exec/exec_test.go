package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oliveagle/gomut/internal/mutant"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		timedOut bool
		want     mutant.Status
		wantFail []string
	}{
		{"survived", "ok\tpkg\t0.100s\n", false, mutant.Survived, nil},
		{"survived-cached", "ok\tpkg\t(cached)\n", false, mutant.Survived, nil},
		{"killed", "--- FAIL: TestA (0.00s)\n\ta_test.go:5: oops\nFAIL\tpkg\t0.05s\n", false, mutant.Killed, []string{"TestA"}},
		{"killed-pkgonly", "FAIL\tpkg\t0.05s\n", false, mutant.Killed, nil},
		{"killed-panic", "panic: runtime error: index out of range\nFAIL\tpkg\t0.02s\n", false, mutant.Killed, nil},
		{"build-failed", "# pkg [pkg.test]\n./a.go:3:1: expected declaration, found '}\nFAIL\tpkg [build failed]\n", false, mutant.CompileError, nil},
		{"no-test-files", "?   \tpkg\t[no test files]\n", false, mutant.RunError, nil},
		{"env-error", "go: no required module provides package foo; to add it:\n\tgo get foo\n", false, mutant.RunError, nil},
		{"no-tests-to-run", "ok\tpkg\t0.001s\n  (no tests to run)\n", false, mutant.NoCoverage, nil},
		{"timed-out", "--- FAIL: whatever\n", true, mutant.TimedOut, []string{"whatever"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.output, tc.timedOut)
			if got != tc.want {
				t.Errorf("classify = %s, want %s", got, tc.want)
			}
			if fail := extractFailures(tc.output); strings.Join(fail, ",") != strings.Join(tc.wantFail, ",") {
				t.Errorf("failures = %v, want %v", fail, tc.wantFail)
			}
		})
	}
}

func TestBuildTestArgs(t *testing.T) {
	tr := TestRun{
		Pkg:          "pkg",
		Tests:        []string{"TestA", "TestB"},
		FailFast:     true,
		CoverProfile: "/tmp/c.out",
		OverlayPath:  "/tmp/o.json",
	}
	got := strings.Join(buildTestArgs(tr), " ")
	want := "test -run=^(TestA|TestB)$ -failfast -covermode=count -coverprofile=/tmp/c.out -overlay=/tmp/o.json pkg"
	if got != want {
		t.Errorf("args = %s, want %s", got, want)
	}
}

// newFixture writes a tiny go module into a temp dir.
func newFixture(t *testing.T, src, testSrc string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":    "module fixture\n\ngo 1.16\n",
		"a.go":      src,
		"a_test.go": testSrc,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunRealPassAndFail(t *testing.T) {
	dir := newFixture(t,
		"package fixture\n\nfunc Add(a, b int) int { return a + b }\n",
		"package fixture\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n")
	ctx := context.Background()

	r := Run(ctx, TestRun{Dir: dir, Pkg: ".", Timeout: 60 * time.Second})
	if r.Status != mutant.Survived {
		t.Fatalf("status = %s (%s), want SURVIVED", r.Status, r.Output)
	}

	dir2 := newFixture(t,
		"package fixture\n\nfunc Add(a, b int) int { return a + b }\n",
		"package fixture\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n")
	r = Run(ctx, TestRun{Dir: dir2, Pkg: ".", Timeout: 60 * time.Second})
	if r.Status != mutant.Killed {
		t.Fatalf("status = %s (%s), want KILLED", r.Status, r.Output)
	}
	if len(r.Failures) != 1 || r.Failures[0] != "TestFail" {
		t.Errorf("failures = %v, want [TestFail]", r.Failures)
	}
}

func TestRunRealTimeout(t *testing.T) {
	dir := newFixture(t,
		"package fixture\n\nfunc Spin() {}\n",
		"package fixture\n\nimport \"testing\"\n\nfunc TestSpin(t *testing.T) { for {} }\n")
	start := time.Now()
	r := Run(context.Background(), TestRun{Dir: dir, Pkg: ".", Timeout: 3 * time.Second})
	elapsed := time.Since(start)
	if r.Status != mutant.TimedOut {
		t.Fatalf("status = %s (%s), want TIMED_OUT", r.Status, r.Output)
	}
	if elapsed > 15*time.Second {
		t.Errorf("run took %v; process group kill not effective", elapsed)
	}
}
