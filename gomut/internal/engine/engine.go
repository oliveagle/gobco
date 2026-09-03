// Package engine orchestrates a mutation testing run (ADR-0001):
//
//  1. discover target packages with "go list"
//  2. per package:
//     a. baseline run: unmutated, all tests, full coverprofile (D3)
//     b. per-test coverprofiles -> line-to-tests map (D3)
//     c. type-check (degrade to syntactic operators on failure, D4)
//     d. generate single-site mutants with the active operators (D2)
//     e. select the covering tests per mutant (D3); uncovered -> NO_COVERAGE
//     f. execute each mutant in an isolated "go test" subprocess (D6)
//     g. cache results keyed by sources+tests+operators+go version (D7)
//  3. aggregate into a report.Report (D5)
//
// The engine depends on the other internal packages one way only:
// engine -> {mutate, cover, exec, report} -> mutant.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oliveagle/gobco/gomut/internal/cover"
	gexec "github.com/oliveagle/gobco/gomut/internal/exec"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/mutate"
	"github.com/oliveagle/gobco/gomut/internal/report"
)

// Options configures a mutation testing run (ADR-0001 D9, D6).
type Options struct {
	// Dir is the working directory for package discovery.
	// Empty means the current directory.
	Dir string
	// Patterns are the package patterns to test (default ["."]).
	Patterns []string
	// Operators is the active operator set (default: mutate.All()).
	Operators []mutant.Operator
	// Version is recorded in the report.
	Version string
	// Workers is the number of parallel test subprocesses
	// (default: CPU count, capped at 8).
	Workers int
	// Timeout budgets one mutant's test run (default 30s).
	Timeout time.Duration
	// BaselineTimeout budgets the full unmutated baseline run
	// (default 10m).
	BaselineTimeout time.Duration
	// AllTests runs the whole test suite per mutant instead of the
	// coverage-selected subset.
	AllTests bool
	// NoCache disables the result cache.
	NoCache bool
	// GoBin overrides the go binary (default: "go" on PATH).
	GoBin string
	// Logf receives progress lines (may be nil).
	Logf func(format string, args ...interface{})
}

// Engine runs a mutation testing session.
type Engine struct {
	opts      Options
	goVersion string
	goVerOnce bool
}

// New fills in defaults and returns an Engine.
func New(opts Options) *Engine {
	if opts.Dir == "" {
		opts.Dir = "."
	}
	if len(opts.Patterns) == 0 {
		opts.Patterns = []string{"."}
	}
	if opts.Operators == nil {
		opts.Operators = mutate.All()
	}
	if opts.Workers <= 0 {
		workers := runtime.NumCPU()
		if workers > 8 {
			workers = 8
		}
		if workers < 1 {
			workers = 1
		}
		opts.Workers = workers
	} else if opts.Workers > 8 {
		opts.Workers = 8
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.BaselineTimeout <= 0 {
		opts.BaselineTimeout = 10 * time.Minute
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Engine{opts: opts}
}

// Run executes the whole mutation testing session and returns the
// aggregated report.
func (e *Engine) Run(ctx context.Context) (*report.Report, error) {
	pkgs, err := e.discover(ctx)
	if err != nil {
		return nil, err
	}
	mutantsByPkg := map[string][]*mutant.Mutant{}
	errorsByPkg := map[string][]string{}
	for _, p := range pkgs {
		muts, errs := e.processPackage(ctx, p)
		mutantsByPkg[p.ImportPath] = muts
		errorsByPkg[p.ImportPath] = errs
	}
	return report.New("gomut", e.opts.Version, mutantsByPkg, errorsByPkg, mutate.Names(e.opts.Operators)), nil
}

// listPkg is the subset of "go list -json" fields the engine uses.
type listPkg struct {
	ImportPath  string   `json:"ImportPath"`
	Dir         string   `json:"Dir"`
	Name        string   `json:"Name"`
	GoFiles     []string `json:"GoFiles"`
	CgoFiles    []string `json:"CgoFiles"`
	TestGoFiles []string `json:"TestGoFiles"`
}

func (e *Engine) logf(format string, args ...interface{}) {
	if e.opts.Logf != nil {
		e.opts.Logf(format, args...)
	}
}

// discover resolves the package patterns in the working directory.
func (e *Engine) discover(ctx context.Context) ([]listPkg, error) {
	var out, errb bytes.Buffer
	cmd := e.goCmd(ctx, append([]string{"list", "-json"}, e.opts.Patterns...)...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, tailBytes(errb.Bytes(), 256))
	}
	pkgs, err := parseListPkgOutput(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parsing go list output: %w", err)
	}
	var kept []listPkg
	for _, p := range pkgs {
		if len(p.CgoFiles) > 0 {
			e.logf("skipping %s: cgo packages are not supported in v1", p.ImportPath)
			continue
		}
		if len(p.GoFiles) == 0 {
			continue // test-only package: nothing to mutate
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no Go packages with production code to mutate (patterns: %s)", strings.Join(e.opts.Patterns, " "))
	}
	return kept, nil
}

// processPackage runs the full pipeline for one package and returns the
// executed mutants plus package-level notes.
func (e *Engine) processPackage(ctx context.Context, p listPkg) ([]*mutant.Mutant, []string) {
	var errs []string

	// Parse production files.
	fset := token.NewFileSet()
	var files []srcFile
	for _, name := range p.GoFiles {
		abs := filepath.Join(p.Dir, name)
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, []string{fmt.Sprintf("reading %s: %v", abs, err)}
		}
		f, err := parser.ParseFile(fset, abs, data, 0)
		if err != nil {
			return nil, []string{fmt.Sprintf("parsing %s: %v", abs, err)}
		}
		files = append(files, srcFile{
			abs:     abs,
			name:    name,
			rel:     e.relPath(p.Dir, abs),
			content: string(data),
			ast:     f,
		})
	}

	// Baseline: unmutated, all tests, full coverprofile.
	wd, err := os.MkdirTemp("", "gomut-pkg-")
	if err != nil {
		return nil, []string{fmt.Sprintf("temp dir: %v", err)}
	}
	defer os.RemoveAll(wd)
	baseOut := filepath.Join(wd, "baseline.out")
	e.logf("baseline: %s", p.ImportPath)
	base := gexec.Run(ctx, gexec.TestRun{
		Dir:          p.Dir,
		Pkg:          p.ImportPath,
		FailFast:     false,
		CoverProfile: baseOut,
		Timeout:      e.opts.BaselineTimeout,
		GoBin:        e.goBin(),
	})
	baselineOK := base.Status == mutant.Survived
	if base.Status == mutant.RunError {
		return nil, []string{fmt.Sprintf("package has no runnable tests: %s", firstLine(base.Output))}
	}
	if !baselineOK {
		errs = append(errs, fmt.Sprintf("baseline (unmutated) tests %s: %s", base.Status, firstLine(base.Output)))
	}

	// Per-test coverage (D3) when doing coverage-based selection.
	var lineTests *cover.LineToTests
	var testHashes map[string]string
	if !e.opts.AllTests && baselineOK {
		lineTests, testHashes = e.perTestCoverage(ctx, p, wd, files)
	}

	// Type-check for type-aware operators (degrade on failure, D4).
	var tc *mutant.TypeCtx
	asts := make([]*ast.File, len(files))
	for i, sf := range files {
		asts[i] = sf.ast
	}
	tc, err = mutate.TypeCheck(p.Dir, p.ImportPath, asts, fset)
	if err != nil {
		tc = nil
		errs = append(errs, fmt.Sprintf("type-check unavailable, type-aware operators skipped: %s", firstLine(err.Error())))
	} else {
		// Record which package-level functions are called from anywhere
		// (production or test code), so the ReturnVals operator can skip
		// never-called functions (T-12).
		tc.Used = usedFunctions(p, asts)
	}

	// Generate single-site mutants (D2 phase 2: one patch per mutant).
	var muts []*mutant.Mutant
	for _, sf := range files {
		for _, op := range e.opts.Operators {
			if op.NeedsTypes() && tc == nil {
				continue
			}
			for _, site := range op.Mutate(sf.content, fset, sf.ast, tc) {
				content, err := applyPatch(sf.content, site.Patch)
				if err != nil {
					continue // defensive: malformed site, skip
				}
				muts = append(muts, &mutant.Mutant{
					Operator: op.Name(),
					Package:  p.ImportPath,
					File:     p.ImportPath + "/" + sf.name,
					RelFile:  sf.rel,
					Line:     site.Line,
					Desc:     site.Desc,
					Content:  content,
					Dir:      p.Dir,
					FileName: sf.name,
				})
			}
		}
	}
	sort.Slice(muts, func(i, j int) bool {
		if muts[i].File != muts[j].File {
			return muts[i].File < muts[j].File
		}
		return muts[i].Line < muts[j].Line
	})

	// Selection (D3): uncovered lines -> NO_COVERAGE, the rest run only
	// the tests that cover their line.
	selected := make(map[*mutant.Mutant][]string, len(muts))
	if baselineOK {
		if lineTests == nil {
			// AllTests: no selection, every mutant runs everything.
			for _, m := range muts {
				selected[m] = nil
			}
		} else {
			baseLines := coverProfileLines(baseOut)
			for _, m := range muts {
				if !baseLines.Contains(m.File, m.Line) {
					m.Status = mutant.NoCoverage
					continue
				}
				tests := lineTests.TestNamesAt(m.File, m.Line)
				if len(tests) == 0 {
					m.Status = mutant.NoCoverage
					continue
				}
				sort.Strings(tests)
				selected[m] = tests
			}
		}
	} else {
		// Baseline broken: results would be untrustworthy; mark every
		// mutant RUN_ERROR with the explanation.
		for _, m := range muts {
			m.Status = mutant.RunError
			m.Output = "baseline (unmutated) tests " + string(base.Status) + "; mutant not executed"
		}
	}

	// Execute pending mutants in parallel isolated subprocesses (D6),
	// with the D7 cache.
	e.execute(ctx, p, muts, selected, files, testHashes)

	return muts, errs
}

// perTestCoverage runs the suite once per test function and records the
// line-to-tests map. It also fingerprints each test function's source so the
// D7 cache can decide per-mutant validity incrementally (see cacheLoad).
func (e *Engine) perTestCoverage(ctx context.Context, p listPkg, wd string, files []srcFile) (*cover.LineToTests, map[string]string) {
	fset := token.NewFileSet()
	var testASTs []*ast.File
	testHashes := map[string]string{}
	for _, name := range p.TestGoFiles {
		data, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), data, 0)
		if err != nil {
			continue
		}
		testASTs = append(testASTs, f)
		// Fingerprint every runnable test function's source span: hash the
		// raw bytes [Pos, End) for functions matching cover.TestFuncs' rules.
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			name := fd.Name.Name
			if name == "TestMain" || !strings.HasPrefix(name, "Test") {
				continue
			}
			if len(fd.Type.Params.List) != 1 {
				continue
			}
			pos := fset.Position(fd.Pos()).Offset
			end := fset.Position(fd.End()).Offset
			if pos < 0 || end > len(data) || end <= pos {
				continue
			}
			h := sha256.Sum256(data[pos:end])
			testHashes[name] = hex.EncodeToString(h[:])[:16]
		}
	}
	names := cover.TestFuncs(testASTs)
	lt := &cover.LineToTests{}
	if len(names) == 0 {
		return lt, testHashes
	}

	// perTestCoverage is a pure function of (production sources, test
	// sources, go version): every per-test profile is determined by the
	// test bodies and the line numbers of the code under test. Caching it
	// skips the O(N) per-test "go test" subprocesses — the dominant
	// warm-run cost — whenever nothing relevant changed. The key includes
	// ALL production files (line numbers shift with edits) and ALL test
	// files (test bodies decide which lines each test covers), so a stale
	// cache can never select a wrong test subset.
	if !e.opts.NoCache {
		if cached := e.coverageLoad(p, files); cached != nil {
			e.logf("perTestCoverage: %d tests from cache", len(names))
			return cached, testHashes
		}
	}
	t0 := time.Now()
	defer func() {
		e.logf("perTestCoverage: %d tests in %s", len(names), time.Since(t0).Round(time.Millisecond))
	}()

	// Parallelize the per-test runs across the worker pool. Each run is an
	// independent "go test -run=^TestX$ -coverprofile" subprocess; the Go
	// build cache makes all but the first cheap, so wall time here is
	// dominated by process scheduling, not compilation. Running them in
	// parallel on a quiet host cuts this phase from O(N) to O(N/workers)
	// wall time (N = number of test functions).
	workers := e.opts.Workers
	if workers > len(names) {
		workers = len(names)
	}
	if workers < 1 {
		workers = 1
	}

	var mu sync.Mutex // guards lt.Add (LineToTests is not concurrency-safe)
	jobs := make(chan int, len(names))
	for i := range names {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				name := names[i]
				out := filepath.Join(wd, fmt.Sprintf("test-%03d.out", i))
				r := gexec.Run(ctx, gexec.TestRun{
					Dir:          p.Dir,
					Pkg:          p.ImportPath,
					Tests:        []string{name},
					CoverProfile: out,
					Timeout:      e.opts.Timeout,
					GoBin:        e.goBin(),
				})
				if r.Status == mutant.TimedOut {
					e.logf("  test %s timed out in isolation; its coverage is incomplete", name)
					continue
				}
				if r.Status == mutant.Killed {
					e.logf("  test %s fails on its own; its coverage may be partial", name)
				}
				data, err := os.ReadFile(out)
				if err != nil {
					continue
				}
				prof, err := cover.ParseProfile(data)
				if err != nil {
					continue
				}
				mu.Lock()
				lt.Add(prof, name)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if !e.opts.NoCache {
		e.coverageStore(p, files, lt)
	}
	return lt, testHashes
}

// goBin returns the go binary to use.
func (e *Engine) goBin() string {
	if e.opts.GoBin != "" {
		return e.opts.GoBin
	}
	return "go"
}

// goCmd builds a go command in the working directory.
func (e *Engine) goCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.goBin(), args...)
	cmd.Dir = e.opts.Dir
	return cmd
}

// goVersionString returns the active go toolchain version (cached).
func (e *Engine) goVersionString() string {
	if e.goVerOnce {
		return e.goVersion
	}
	e.goVerOnce = true
	e.goVersion = "unknown"
	if out, err := exec.Command(e.goBin(), "version").Output(); err == nil {
		// "go version go1.25.5 linux/amd64" -> "go1.25.5 linux/amd64"
		if parts := strings.Fields(string(out)); len(parts) >= 3 {
			e.goVersion = parts[2] + " " + parts[3]
		}
	}
	return e.goVersion
}

// relPath renders abs relative to the module root containing dir
// (the module root is found by walking up to the nearest go.mod).
func (e *Engine) relPath(dir, abs string) string {
	root := dir
	for d := dir; d != "." && d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			root = d
			break
		}
	}
	if rel, err := filepath.Rel(root, abs); err == nil {
		return rel
	}
	return abs
}

func applyPatch(content string, p mutant.Patch) (string, error) {
	if p.Start < 0 || p.End > len(content) || p.Start > p.End {
		return "", fmt.Errorf("patch out of range: [%d,%d) of %d bytes", p.Start, p.End, len(content))
	}
	return content[:p.Start] + p.Replace + content[p.End:], nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func tailBytes(b []byte, n int) string {
	if len(b) <= n {
		return strings.TrimSpace(string(b))
	}
	return "…" + strings.TrimSpace(string(b[len(b)-n:]))
}

// parseListPkgOutput decodes "go list -json" output. Depending on the Go
// version and the number of matched packages, the output is either a
// JSON array (older toolchains) or a stream of concatenated JSON
// objects (newer toolchains, including single matches).
func parseListPkgOutput(data []byte) ([]listPkg, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var pkgs []listPkg
		if err := json.Unmarshal(trimmed, &pkgs); err != nil {
			return nil, err
		}
		return pkgs, nil
	}
	var pkgs []listPkg
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var p listPkg
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// usedFunctions returns the set of package-level function names that are
// called from anywhere in the package (production or test code). The
// type-aware ReturnVals operator uses it to skip never-called functions,
// whose return-value mutants would only ever be NO_COVERAGE.
//
// production lists the already-parsed production files; the test files are
// parsed here from disk so that calls into production code are detected too.
func usedFunctions(p listPkg, production []*ast.File) map[string]bool {
	declared := map[string]bool{}
	for _, f := range production {
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				declared[fd.Name.Name] = true
			}
		}
	}

	var files []*ast.File
	files = append(files, production...)
	for _, name := range p.TestGoFiles {
		data, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(p.Dir, name), data, 0)
		if err != nil {
			continue
		}
		files = append(files, f)
	}

	used := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := ce.Fun.(type) {
			case *ast.Ident:
				if declared[fun.Name] {
					used[fun.Name] = true
				}
			case *ast.SelectorExpr:
				if declared[fun.Sel.Name] {
					used[fun.Sel.Name] = true
				}
			}
			return true
		})
	}
	return used
}
