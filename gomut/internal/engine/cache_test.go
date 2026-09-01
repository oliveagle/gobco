package engine

import (
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
	e.cacheStore(p, muts, files)
	// Wipe the in-memory statuses; cacheLoad must repopulate each correctly.
	muts[0].Status = ""
	muts[1].Status = ""
	if !e.cacheLoad(p, muts, files) {
		t.Fatalf("cache load: miss (expected hit)")
	}
	if got, want := muts[0].Status, mutant.Killed; got != want {
		t.Errorf("false-return mutant status = %q, want %q", got, want)
	}
	if got, want := muts[1].Status, mutant.Survived; got != want {
		t.Errorf("true-return mutant status = %q, want %q", got, want)
	}
}
