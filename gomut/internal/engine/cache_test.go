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
