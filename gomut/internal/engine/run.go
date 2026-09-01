package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"os"
	"path/filepath"
	"sync"

	"github.com/oliveagle/gomut/internal/cover"
	gexec "github.com/oliveagle/gomut/internal/exec"
	"github.com/oliveagle/gomut/internal/mutant"
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
func (e *Engine) execute(ctx context.Context, p listPkg, muts []*mutant.Mutant, selected map[*mutant.Mutant][]string, files []srcFile) {
	// D7 cache: hit -> reuse, else run and store.
	if !e.opts.NoCache {
		if e.cacheLoad(p, muts, files) {
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

	if !e.opts.NoCache {
		e.cacheStore(p, muts, files)
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

// cachedMutant is the persisted per-mutant result.
type cachedMutant struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Killers []string `json:"killers,omitempty"`
	Output  string   `json:"output,omitempty"`
}

// cacheKey hashes sources, tests, operators and go version.
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
	for _, name := range p.TestGoFiles {
		abs := filepath.Join(p.Dir, name)
		data, err := os.ReadFile(abs)
		write(name)
		if err == nil {
			write(string(data))
		}
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

func (e *Engine) cacheLoad(p listPkg, muts []*mutant.Mutant, files []srcFile) bool {
	path := e.cachePath(p, files)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cached []cachedMutant
	if err := json.Unmarshal(data, &cached); err != nil || len(cached) != len(muts) {
		return false
	}
	byID := make(map[string]cachedMutant, len(cached))
	for _, c := range cached {
		byID[c.ID] = c
	}
	for _, m := range muts {
		c, ok := byID[m.ID()]
		if !ok {
			return false
		}
		m.Status = mutant.Status(c.Status)
		m.Killers = c.Killers
		m.Output = c.Output
	}
	e.logf("  cache hit: %d mutants from %s", len(muts), path)
	return true
}

func (e *Engine) cacheStore(p listPkg, muts []*mutant.Mutant, files []srcFile) {
	var cached []cachedMutant
	for _, m := range muts {
		if m.Status == "" {
			continue
		}
		cached = append(cached, cachedMutant{
			ID:      m.ID(),
			Status:  string(m.Status),
			Killers: m.Killers,
			Output:  tailBytes([]byte(m.Output), 4096),
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
