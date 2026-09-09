// Package mutant defines the data model for mutation testing: mutants,
// result statuses, and mutation operators.
package mutant

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
)

// Status is the final classification of a mutant.
type Status string

const (
	// Killed: at least one selected test failed.
	Killed Status = "KILLED"
	// Survived: all selected tests passed.
	Survived Status = "SURVIVED"
	// NoCoverage: no test executes the mutated line.
	NoCoverage Status = "NO_COVERAGE"
	// TimedOut: test execution exceeded the time budget (likely infinite loop).
	TimedOut Status = "TIMED_OUT"
	// CompileError: the test run failed to build for reasons outside the
	// mutant's patched file (transient toolchain/environment problem); the
	// mutant is retried on a later run.
	CompileError Status = "COMPILE_ERROR"
	// RunError: the test runner itself failed (environment problem).
	RunError Status = "RUN_ERROR"
	// Invalid: the mutation itself does not compile — e.g. a ReturnVals
	// rewrite of one value of a comma-ok return ("v, ok := ...; return v,
	// ok") that leaves the other value unused, which Go rejects. The
	// baseline build is known-good, so the patch is the cause. Invalid
	// mutants are excluded from the score denominator (like NO_COVERAGE):
	// they cannot be killed, but the fault is the generator's, not the
	// test suite's.
	Invalid Status = "INVALID"
)

// Detected reports whether the status counts toward the score numerator.
func (s Status) Detected() bool { return s == Killed || s == TimedOut }

// Patch is a single text replacement on the original file content.
type Patch struct {
	Start   int
	End     int
	Replace string
}

// Site is a candidate mutation at one source location (before repair).
type Site struct {
	Line, Col int
	Desc      string
	Patch     Patch
}

// Mutant is a fully generated single-site mutant.
type Mutant struct {
	Operator string // operator name
	Package  string // package import path
	File     string // "import/path/file.go" (matches cover profile keys)
	RelFile  string // module-relative path, for display
	Line     int
	Desc     string
	Content  string // full mutated file content
	Dir      string // absolute package directory
	FileName string // base file name

	// Status is the outcome of executing this mutant (zero value until
	// the engine runs it).
	Status Status
	// Killers are the test names whose failure killed this mutant.
	Killers []string
	// Output is the tail of the test run output, kept for the report
	// on non-pass outcomes (COMPILE_ERROR, RUN_ERROR, TIMED_OUT).
	Output string
}

// ID returns a stable human-readable identifier.
func (m *Mutant) ID() string {
	return m.Operator + " @" + m.File + ":" + strconv.Itoa(m.Line)
}

// TypeCtx carries type information for type-aware operators.
// It is nil when type-checking was unavailable (operators degrade to
// pure-syntactic mode or are skipped).
type TypeCtx struct {
	Pkg  *types.Package
	Info *types.Info
	Fset *token.FileSet
	// Used, when non-nil, is the set of package-level function names that
	// are called from somewhere in the package (production or test code).
	// The type-aware ReturnVals operator skips functions absent from this
	// set (they would only ever produce NO_COVERAGE mutants).
	Used map[string]bool
}

// Operator generates single-site mutants from one source file.
type Operator interface {
	// Name returns the operator's public name (used in -mutators).
	Name() string
	// NeedsTypes reports whether the operator requires type information.
	NeedsTypes() bool
	// Mutate returns zero or more candidate mutations for the file.
	Mutate(src string, fset *token.FileSet, file *ast.File, tc *TypeCtx) []Site
}
