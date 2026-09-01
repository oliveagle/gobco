// Package examples is used by gomut's end-to-end demonstration.
//
// Both example modules share this exact source so a reader can compare the
// mutation report produced against weak tests vs. strong tests. The full
// walkthrough is in ../README.md.
package strong

// Between reports whether lo <= v <= hi.
//
// The && here is a classic mutation site: flipping it to || lets a value
// outside [lo, hi] slip through if nothing tests the out-of-range case.
func Between(v, lo, hi int) bool {
	return lo <= v && v <= hi
}

// Abs returns the absolute value of x.
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Sum returns 0 + 1 + ... + (n - 1) for a non-negative n.
func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
