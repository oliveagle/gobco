package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/cover"
	gexec "github.com/oliveagle/gobco/gomut/internal/exec"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// srcFile is a parsed production file of the package under test.
type srcFile struct {
	abs     string
	name    string
	rel     string
	content string
	ast     *ast.File
}

// execute runs the pending mutants (status == "") in a pool of parallel
// workers, one isolated "go test" subprocess per mutant (ADR-0001 D6),
// and maintains the result cache (D7).
func (e *Engine) execute(ctx context.Context, p listPkg, muts []*mutant.Mutant, selected map[*mutant.Mutant][]string, files []srcFile, testHashes map[string]string) {
	t0 := time.Now()
	// D7 cache: hit -> reuse, else run and store. The cache is incremental:
	// a mutant is reused only if its coverage-selected tests are unchanged
	// AND the source of exactly those tests is unchanged (see cacheLoad).
	if !e.opts.NoCache {
		if e.cacheLoad(p, muts, selected, testHashes, files) {
			return
		}
	}

	var pending []*mutant.Mutant
	for _, m := range muts {
		if m.Status == "" {
			if _, ok := selected[m]; !ok && !e.opts.AllTests {
				m.Status = mutant.NoCoverage // defensive: no selection recorded
				continue
			}
			pending = append(pending, m)
		}
	}

	workers := e.opts.Workers
	if workers > len(pending) {
		workers = len(pending)
	}
	if workers < 1 && len(pending) > 0 {
		workers = 1
	}
	e.logf("execute: %d mutants pending across %d workers", len(pending), workers)
	jobs := make(chan *mutant.Mutant, len(pending))
	for _, m := range pending {
		jobs <- m
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wd, err := os.MkdirTemp("", "gomut-worker-")
		if err != nil {
			e.logf("worker %d: temp dir: %v", i, err)
			continue
		}
		wg.Add(1)
		go func(i int, wd string) {
			defer wg.Done()
			defer os.RemoveAll(wd)
			for m := range jobs {
				e.runMutant(ctx, wd, m, selected[m])
			}
		}(i, wd)
	}
	wg.Wait()
	e.logf("execute: %d mutants in %s", len(pending), time.Since(t0).Round(time.Millisecond))

	if !e.opts.NoCache {
		e.cacheStore(p, muts, selected, testHashes, files)
	}
}

// runMutant materializes one mutant (mutated file + overlay JSON) and
// runs its selected tests in an isolated subprocess.
func (e *Engine) runMutant(ctx context.Context, wd string, m *mutant.Mutant, tests []string) {
	mutPath := filepath.Join(wd, m.FileName)
	if err := os.WriteFile(mutPath, []byte(m.Content), 0o644); err != nil {
		e.mark(m, mutant.RunError, nil, "writing mutated file: "+err.Error())
		return
	}
	ovPath := filepath.Join(wd, "overlay.json")
	ov := gexec.Overlay{filepath.Join(m.Dir, m.FileName): mutPath}
	if err := ov.WriteFile(ovPath); err != nil {
		e.mark(m, mutant.RunError, nil, "writing overlay: "+err.Error())
		return
	}
	r := gexec.Run(ctx, gexec.TestRun{
		Dir:         m.Dir,
		Pkg:         m.Package,
		Tests:       tests,
		OverlayPath: ovPath,
		FailFast:    true,
		Timeout:     e.opts.Timeout,
		GoBin:       e.goBin(),
	})
	e.mark(m, r.Status, r.Failures, r.Output)
}

func (e *Engine) mark(m *mutant.Mutant, st mutant.Status, killers []string, output string) {
	m.Status = st
	m.Killers = killers
	m.Output = output
	e.logf("  %-12s %-18s %s:%d  %s", st, m.Operator, m.RelFile, m.Line, m.Desc)
}

// ---- D7 cache ----------------------------------------------------------

// cachedMutant is the persisted per-mutant result, keyed by the full mutant
// identity (operator|file|line|desc) so distinct mutants that share
// operator+file+line (e.g. two ReturnVals on one bool return) keep distinct
// statuses.
type cachedMutant struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Killers []string `json:"killers,omitempty"`
	Output  string   `json:"output,omitempty"`
	// Tests are the coverage-selected tests that ran against this mutant;
	// TestsHash fingerprints the source of exactly those tests. On a later
	// run, if the mutant's selected set is unchanged AND the selected tests'
	// source is unchanged, the stored result is still valid — even if some
	// OTHER test in the package changed. This is what makes the cache
	// incremental: editing one test only invalidates the mutants covered by
	// it, not the whole package.
	Tests     []string `json:"tests,omitempty"`
	TestsHash string   `json:"testsHash,omitempty"`
}

// mutantKey uniquely identifies a single-site mutant.
func mutantKey(m *mutant.Mutant) string {
	return m.Operator + "|" + m.File + "|" + strconv.Itoa(m.Line) + "|" + m.Desc
}

// selectedTestsHash fingerprints the source of the tests selected for a
// mutant: sha256 of the concatenated per-test source hashes in the given
// (already sorted) order. If none of the selected tests changed, the hash is
// unchanged; if any did, it changes — and only then is the mutant re-run.
func selectedTestsHash(selected []string, testHashes map[string]string) string {
	if len(selected) == 0 {
		return ""
	}
	h := sha256.New()
	for _, t := range selected {
		th, ok := testHashes[t]
		if !ok {
			continue
		}
		h.Write([]byte(t))
		h.Write([]byte{0})
		h.Write([]byte(th))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// mutantTestsHash returns the fingerprint to compare for a single mutant:
// the source of its coverage-selected tests, or (in AllTests mode, where
// every mutant runs the whole suite) the whole suite's source so that any
// test edit invalidates every mutant.
func (e *Engine) mutantTestsHash(tests []string, testHashes map[string]string) string {
	if e.opts.AllTests {
		if len(testHashes) == 0 {
			return ""
		}
		names := make([]string, 0, len(testHashes))
		for n := range testHashes {
			names = append(names, n)
		}
		sort.Strings(names)
		return selectedTestsHash(names, testHashes)
	}
	return selectedTestsHash(tests, testHashes)
}

// cacheKey hashes sources (production files), operators and go version.
// Test files are deliberately NOT in this key: per-mutant validity is decided
// per mutant via mutantTestsHash, so a change to one test does not invalidate
// the whole package cache. (A changed production file changes every mutant,
// so it must still force a fresh package key.)
func (e *Engine) cacheKey(p listPkg, files []srcFile) string {
	h := sha256.New()
	write := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, f := range files {
		write(f.name)
		write(f.content)
	}
	for _, op := range e.opts.Operators {
		write(op.Name())
	}
	write(e.goVersionString())
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (e *Engine) cachePath(p listPkg, files []srcFile) string {
	return filepath.Join(p.Dir, ".gomut-cache", e.cacheKey(p, files)+".json")
}

// cacheLoad reads the package cache and reuses every mutant whose
// coverage-selected tests are unchanged (same names, same source hash).
// It returns true when no in-scope mutant remains pending (full hit); a
// partial hit leaves the survivors pending for re-execution.
func (e *Engine) cacheLoad(p listPkg, muts []*mutant.Mutant, selected map[*mutant.Mutant][]string, testHashes map[string]string, files []srcFile) bool {
	path := e.cachePath(p, files)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cached []cachedMutant
	if err := json.Unmarshal(data, &cached); err != nil {
		return false
	}
	byKey := make(map[string]cachedMutant, len(cached))
	for _, c := range cached {
		byKey[c.ID] = c
	}
	reused := 0
	for _, m := range muts {
		if m.Status != "" {
			continue
		}
		c, ok := byKey[mutantKey(m)]
		if !ok {
			continue
		}
		// Incremental validity: the selected test set must match AND the
		// source of exactly those tests must be unchanged.
		cur := e.mutantTestsHash(selected[m], testHashes)
		if c.TestsHash != cur || !sameStringSet(c.Tests, selected[m]) {
			continue
		}
		m.Status = mutant.Status(c.Status)
		m.Killers = c.Killers
		m.Output = c.Output
		reused++
	}
	// Full hit only when every mutant has a status.
	for _, m := range muts {
		if m.Status == "" {
			if reused > 0 {
				e.logf("  cache hit: %d/%d mutants reused from %s (partial)", reused, len(muts), path)
			}
			return false
		}
	}
	if reused > 0 {
		e.logf("  cache hit: %d mutants from %s", len(muts), path)
	}
	return reused > 0
}

func (e *Engine) cacheStore(p listPkg, muts []*mutant.Mutant, selected map[*mutant.Mutant][]string, testHashes map[string]string, files []srcFile) {
	var cached []cachedMutant
	for _, m := range muts {
		if m.Status == "" {
			continue
		}
		cached = append(cached, cachedMutant{
			ID:        mutantKey(m),
			Status:    string(m.Status),
			Killers:   m.Killers,
			Output:    tailBytes([]byte(m.Output), 4096),
			Tests:     selected[m],
			TestsHash: e.mutantTestsHash(selected[m], testHashes),
		})
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return
	}
	path := e.cachePath(p, files)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.logf("  cache store: %v", err)
	}
}

// sameStringSet reports whether two slices hold the same strings (order
// matters not; callers sort selected test lists deterministically).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]int, len(a))
	for _, s := range a {
		ma[s]++
	}
	for _, s := range b {
		if ma[s] == 0 {
			return false
		}
		ma[s]--
	}
	return true
}

// coverProfileLines parses a coverprofile file into line sets.
func coverProfileLines(path string) cover.FileLines {
	fl := cover.FileLines{}
	data, err := os.ReadFile(path)
	if err != nil {
		return fl
	}
	if prof, err := cover.ParseProfile(data); err == nil {
		for f, lines := range prof.CoveredLines() {
			fl[f] = lines
		}
	}
	return fl
}
