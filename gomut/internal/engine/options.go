package engine

import (
	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// FilterFunc is a predicate that returns true if a mutant should be included.
type FilterFunc func(m *mutant.Mutant) bool

// TestFilterFunc is a predicate that returns true if a test should be included
// for a given mutant.
type TestFilterFunc func(m *mutant.Mutant, tests []string) []string

// ProcessPackageOpts extends Options with filtering support for incremental mode.
type ProcessPackageOpts struct {
	Options
	// MutantFilter, if non-nil, filters which mutants to generate/keep.
	MutantFilter FilterFunc
	// TestFilter, if non-nil, filters which tests to run for each mutant.
	TestFilter TestFilterFunc
}
