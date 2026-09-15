package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/oliveagle/gobco/gomut/internal/cover"
	gexec "github.com/oliveagle/gobco/gomut/internal/exec"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"github.com/oliveagle/gobco/gomut/internal/mutate"
	"github.com/oliveagle/gobco/gomut/internal/report"

	"go/ast"
	"go/parser"
	"go/token"
)

// RunFiltered runs mutation testing with optional mutant and test filters.
// If mutantFilter is non-nil, only mutants for which it returns true are kept.
// If testFilter is non-nil, it is called for each surviving mutant to choose
// which subset of the coverage-selected tests to actually run.
func (e *Engine) RunFiltered(ctx context.Context, mutantFilter FilterFunc, testFilter TestFilterFunc) (*report.Report, error) {
	pkgs, err := e.discover(ctx)
	if err != nil {
		return nil, err
	}
	mutantsByPkg := map[string][]*mutant.Mutant{}
	errorsByPkg := map[string][]string{}
	for _, p := range pkgs {
		muts, errs := e.processPackageFiltered(ctx, p, mutantFilter, testFilter)
		mutantsByPkg[p.ImportPath] = muts
		errorsByPkg[p.ImportPath] = errs
	}
	return report.New("gomut", e.opts.Version, mutantsByPkg, errorsByPkg, mutate.Names(e.opts.Operators)), nil
}

// processPackageFiltered is like processPackage but applies mutant and test filters.
func (e *Engine) processPackageFiltered(ctx context.Context, p listPkg, mutantFilter FilterFunc, testFilter TestFilterFunc) ([]*mutant.Mutant, []string) {
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
			fset:    fset,
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

	// Per-test coverage when doing coverage-based selection.
	var lineTests *cover.LineToTests
	var testHashes map[string]string
	if !e.opts.AllTests && baselineOK {
		lineTests, testHashes = e.perTestCoverage(ctx, p, wd, files)
	}

	// Type-check for type-aware operators.
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
		tc.Used = usedFunctions(p, asts)
	}

	// Generate single-site mutants.
	var muts []*mutant.Mutant
	for _, sf := range files {
		for _, op := range e.opts.Operators {
			if op.NeedsTypes() && tc == nil {
				continue
			}
			for _, site := range op.Mutate(sf.content, fset, sf.ast, tc) {
				content, err := applyPatch(sf.content, site.Patch)
				if err != nil {
					continue
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
					Patch:    site.Patch,
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
	for i := range muts {
		muts[i].CaseID = strconv.Itoa(i)
	}

	// Apply mutant filter if provided.
	if mutantFilter != nil {
		filtered := make([]*mutant.Mutant, 0, len(muts))
		for _, m := range muts {
			if mutantFilter(m) {
				filtered = append(filtered, m)
			}
		}
		e.logf("mutant filter: %d/%d mutants kept", len(filtered), len(muts))
		muts = filtered
	}

	// Selection: uncovered lines -> NO_COVERAGE, the rest run only
	// the tests that cover their line.
	selected := make(map[*mutant.Mutant][]string, len(muts))
	if baselineOK {
		if lineTests == nil {
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
		for _, m := range muts {
			m.Status = mutant.RunError
			m.Output = "baseline (unmutated) tests " + string(base.Status) + "; mutant not executed"
		}
	}

	// Apply test filter if provided.
	if testFilter != nil {
		for m, tests := range selected {
			if tests != nil {
				selected[m] = testFilter(m, tests)
			}
		}
	}

	// Execute pending mutants.
	e.execute(ctx, p, muts, selected, files, testHashes)

	return muts, errs
}
