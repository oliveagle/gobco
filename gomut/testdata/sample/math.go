// Package sample is a tiny fixture for gomut's end-to-end test.
//
// Each function is designed so that specific mutations are killed,
// survive, time out or lack coverage (see the assertions in
// internal/engine/engine_test.go).
package sample

// IsPositive reports whether v is strictly positive.
// The boundary mutant "v > 0" -> "v >= 0" is killed by the v == 0 case.
func IsPositive(v int) bool {
	return v > 0
}

// Abs returns the absolute value of a.
// The boundary mutant "a < 0" -> "a <= 0" survives (no test for a == 0);
// the negated condition is killed.
func Abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// Count returns n, computed with an obvious loop.
// The increment mutant "i++" -> "i--" loops forever (TIMED_OUT); the
// boundary mutant "i < n" -> "i <= n" is killed by Count(5).
func Count(n int) int {
	c := 0
	for i := 0; i < n; i++ {
		c++
	}
	return c
}

// Unused is intentionally never called by any test: all its mutants are
// NO_COVERAGE.
func Unused(x int) int {
	return x * 2
}
