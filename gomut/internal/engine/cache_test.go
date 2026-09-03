package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// logCapture is a concurrency-safe collector of engine log lines. It is used
// to assert cache behaviour across engine runs.
type logCapture struct {
	mu  sync.Mutex
	msg []string
}

func (c *logCapture) logf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msg = append(c.msg, fmt.Sprintf(format, args...))
}

func (c *logCapture) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msg {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// TestEngineCache verifies the ADR-0001 D7 result cache: the first run on a
// package stores its mutant results, and a subsequent identical run reads
// them back and skips re-execution (it reports a cache hit).
func TestEngineCache(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	dir := filepath.Join("..", "..", "testdata", "sample")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Glob(filepath.Join(abs, "go.mod")); err != nil || len(abs) == 0 {
		t.Fatalf("sample module not found at %s", abs)
	}

	// Clean any stale cache so the first run truly starts empty.
	if err := os.RemoveAll(filepath.Join(abs, ".gomut-cache")); err != nil {
		t.Fatal(err)
	}

	cfg := Options{
		Dir:      abs,
		Patterns: []string{"."},
		Workers:  2,
		Timeout:  6 * time.Second,
	}

	run := func(log *logCapture) {
		cfg.Logf = func(format string, args ...interface{}) {
			log.logf(format, args...)
		}
		e := New(cfg)
		if _, err := e.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	// First run: no cache yet, so it must execute and store.
	first := &logCapture{}
	run(first)
	if first.contains("cache hit") {
		t.Fatalf("first run unexpectedly reported a cache hit")
	}
	// The cache directory must have been materialised on disk after storing.
	entries, err := filepath.Glob(filepath.Join(abs, ".gomut-cache", "*"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("cache dir not created after first run: %v", err)
	}

	// Second run: identical inputs must read the cache and skip execution.
	second := &logCapture{}
	run(second)
	if !second.contains("cache hit") {
		t.Fatalf("second run did not report a cache hit; logs=%v", second.msg)
	}
}

// TestCacheLoadPreservesDistinctMutants guards the ADR-0001 D7 cache load
// against a class of bug in which several single-site mutants share
// operator+file+line (hence a shared Mutant.ID): a cache keyed on ID collapses
// them into one stale status. The index-based load assigns cached[i] to
// muts[i], so distinct mutants keep distinct statuses.
func TestCacheLoadPreservesDistinctMutants(t *testing.T) {
	dir := t.TempDir()
	files := []srcFile{{name: "foo.go", content: "package foo\n"}}
	p := listPkg{ImportPath: "example.com/foo", Dir: dir}
	// Two ReturnVals mutants on the same bool return: same ID, but different
	// correct outcomes — one is KILLED by a test, the other SURVIVES.
	muts := []*mutant.Mutant{
		{Operator: "ReturnVals", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 13, Desc: "return value replaced with false (bool)", Status: mutant.Killed},
		{Operator: "ReturnVals", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 13, Desc: "return value replaced with true (bool)", Status: mutant.Survived},
	}
	e := New(Options{})
	e.cacheStore(p, muts, nil, nil, files)
	// Wipe the in-memory statuses; cacheLoad must repopulate each correctly.
	muts[0].Status = ""
	muts[1].Status = ""
	if !e.cacheLoad(p, muts, nil, nil, files) {
		t.Fatalf("cache load: miss (expected hit)")
	}
	if got, want := muts[0].Status, mutant.Killed; got != want {
		t.Errorf("false-return mutant status = %q, want %q", got, want)
	}
	if got, want := muts[1].Status, mutant.Survived; got != want {
		t.Errorf("true-return mutant status = %q, want %q", got, want)
	}
}

// TestEngineCacheIncremental verifies that editing one test function only
// invalidates the mutants covered by that test, not the whole package
// (ADR-0001 D7 incrementality). The setup copies the sample module into a
// temp directory so the fixture is not dirtied across runs.
func TestEngineCacheIncremental(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	src := filepath.Join("..", "..", "testdata", "sample")
	absSrc, err := filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	// Copy the module (go.mod, production code, test code) into a temp dir
	// so we can mutate the test file freely.
	tmp := t.TempDir()
	for _, name := range []string{"go.mod", "math.go", "math_test.go"} {
		in, err := os.ReadFile(filepath.Join(absSrc, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, name), in, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run := func(log *logCapture) {
		e := New(Options{
			Dir:      tmp,
			Patterns: []string{"."},
			Workers:  2,
			Timeout:  6 * time.Second,
			Logf:     log.logf,
		})
		if _, err := e.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	// First run: cold cache, must execute everything.
	first := &logCapture{}
	run(first)
	if first.contains("cache hit") {
		t.Fatalf("first run unexpectedly reported a cache hit")
	}

	// Mutate only TestIsPositive (the test that covers math.go:11). The
	// inserted comment changes its source hash; the IsPositive mutants'
	// selected-tests fingerprint changes → those mutants must be re-run.
	// TestAbs and TestCount are untouched, so their mutants must be reused.
	mtPath := filepath.Join(tmp, "math_test.go")
	mt, err := os.ReadFile(mtPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := bytes.Replace(mt, []byte("for _, c := range cases {"), []byte("// inc-cache-fixture: altered TestIsPositive\n\tfor _, c := range cases {"), 1)
	if !bytes.Contains(patched, []byte("inc-cache-fixture")) {
		t.Fatalf("test patch did not apply")
	}
	if err := os.WriteFile(mtPath, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	second := &logCapture{}
	run(second)
	if !second.contains("cache hit") {
		t.Fatalf("second run did not report a cache hit (logs=%v)", second.msg)
	}
	if !second.contains("(partial)") {
		t.Fatalf("second run should report a partial cache hit (logs=%v)", second.msg)
	}
	// TestIsPositive covers math.go:11 → IsPositive mutants must be re-run,
	// so their KILLED lines appear in the second run's output.
	if !second.contains("math.go:11") {
		t.Errorf("math.go:11 mutants should be re-run after editing TestIsPositive; logs=%v", second.msg)
	}
	// TestAbs is untouched → math.go:18 mutants must be reused, so their
	// lines must NOT appear in the second run's output.
	if second.contains("math.go:18") {
		t.Errorf("math.go:18 mutants should be reused (TestAbs unchanged); logs=%v", second.msg)
	}
	// TestCount is untouched → math.go:29 mutants must be reused too.
	if second.contains("math.go:29") {
		t.Errorf("math.go:29 mutants should be reused (TestCount unchanged); logs=%v", second.msg)
	}
}

// TestCacheStoreSkipsTransientBuildStatuses pins the robustness fix that
// keeps COMPILE_ERROR and RUN_ERROR out of the persistent cache: they can be
// caused by a transient Go build-cache eviction under host load (e.g. a
// cleaned /tmp) rather than by the mutation itself, so caching them would
// poison the (incremental) cache with a degraded score that then survives
// unrelated test edits.
func TestCacheStoreSkipsTransientBuildStatuses(t *testing.T) {
	dir := t.TempDir()
	files := []srcFile{{name: "foo.go", content: "package foo\n"}}
	p := listPkg{ImportPath: "example.com/foo", Dir: dir}
	muts := []*mutant.Mutant{
		{Operator: "NegateConditionals", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 10, Desc: "negated condition", Status: mutant.Killed},
		{Operator: "ReturnVals", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 11, Desc: "return value replaced with nil (error)", Status: mutant.CompileError},
		{Operator: "Constant", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 12, Desc: "constant 0 replaced with 1", Status: mutant.RunError},
		{Operator: "Math", Package: "example.com/foo", File: "example.com/foo/foo.go", Line: 13, Desc: "replaced + with -", Status: mutant.Survived},
	}
	e := New(Options{})
	e.cacheStore(p, muts, nil, nil, files)

	// Reload via cacheLoad: only KILLED and SURVIVED should be reused; the
	// COMPILE_ERROR and RUN_ERROR mutants must remain pending.
	muts[0].Status = ""
	muts[1].Status = ""
	muts[2].Status = ""
	muts[3].Status = ""
	if e.cacheLoad(p, muts, nil, nil, files) {
		t.Fatalf("cacheLoad reported a full hit; the transient-status mutants must stay pending")
	}
	if got, want := muts[0].Status, mutant.Killed; got != want {
		t.Errorf("killed mutant status = %q, want %q (must be reused)", got, want)
	}
	if got, want := muts[3].Status, mutant.Survived; got != want {
		t.Errorf("survived mutant status = %q, want %q (must be reused)", got, want)
	}
	if muts[1].Status != "" {
		t.Errorf("COMPILE_ERROR mutant status = %q, want empty (must not be cached)", muts[1].Status)
	}
	if muts[2].Status != "" {
		t.Errorf("RUN_ERROR mutant status = %q, want empty (must not be cached)", muts[2].Status)
	}
}

// TestCoverageCache verifies the perTestCoverage cache: the O(N) per-test
// "go test -coverprofile" subprocess phase is a pure function of
// (production sources, test sources, go version), so an unchanged package
// must reuse the cached LineToTests on the next run instead of re-running
// every test in isolation. Editing a test must invalidate it.
func TestCoverageCache(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: skip with -short")
	}
	src := filepath.Join("..", "..", "testdata", "sample")
	absSrc, err := filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	for _, name := range []string{"go.mod", "math.go", "math_test.go"} {
		in, err := os.ReadFile(filepath.Join(absSrc, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, name), in, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run := func(log *logCapture) {
		e := New(Options{
			Dir:      tmp,
			Patterns: []string{"."},
			Workers:  2,
			Timeout:  6 * time.Second,
			Logf:     log.logf,
		})
		if _, err := e.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	// First run: no coverage cache, must run per-test subprocesses.
	first := &logCapture{}
	run(first)
	if first.contains("tests from cache") {
		t.Fatalf("first run unexpectedly reused the coverage cache; logs=%v", first.msg)
	}
	if !first.contains("perTestCoverage:") {
		t.Fatalf("first run missing perTestCoverage log; logs=%v", first.msg)
	}
	// The coverage cache file must exist on disk after the first run.
	entries, err := filepath.Glob(filepath.Join(tmp, ".gomut-cache", "coverage-*.json"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("coverage cache not written after first run: %v (%v)", entries, err)
	}

	// Second run: identical inputs must load the coverage cache and skip
	// the per-test subprocess phase entirely.
	second := &logCapture{}
	run(second)
	if !second.contains("tests from cache") {
		t.Fatalf("second run did not reuse the coverage cache; logs=%v", second.msg)
	}
	if second.contains("perTestCoverage: ") && second.contains("in ") {
		// The "N tests in <duration>" line only appears when subprocesses ran.
		for _, m := range second.msg {
			if strings.Contains(m, "perTestCoverage:") && strings.Contains(m, " in ") {
				t.Fatalf("second run re-ran per-test subprocesses: %s", m)
			}
		}
	}

	// Third run: edit the test file → the coverage cache key changes → the
	// per-test subprocesses must run again (no reuse).
	mtPath := filepath.Join(tmp, "math_test.go")
	mt, err := os.ReadFile(mtPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := bytes.Replace(mt, []byte("func TestAbs(t *testing.T) {"), []byte("// cov-cache-fixture: altered TestAbs\nfunc TestAbs(t *testing.T) {"), 1)
	if !bytes.Contains(patched, []byte("cov-cache-fixture")) {
		t.Fatalf("test patch did not apply")
	}
	if err := os.WriteFile(mtPath, patched, 0o644); err != nil {
		t.Fatal(err)
	}
	third := &logCapture{}
	run(third)
	if third.contains("tests from cache") {
		t.Fatalf("third run reused the coverage cache after a test edit; logs=%v", third.msg)
	}
}
