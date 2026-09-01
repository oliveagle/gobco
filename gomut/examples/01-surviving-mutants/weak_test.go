package weak

import "testing"

// TestBetween exercises only an in-range value. Because nothing probes an
// out-of-range value or the lo==v / v==hi boundaries, several mutants here
// survive (see ../README.md).
func TestBetween(t *testing.T) {
	if !Between(5, 0, 10) {
		t.Fatal("want true for an in-range value")
	}
}

// TestAbs exercises only a positive input, leaving the negative branch
// uncovered.
func TestAbs(t *testing.T) {
	if Abs(3) != 3 {
		t.Fatal("want 3 for a positive input")
	}
}

// TestSum exercises only n==0, so the loop body is never reached.
func TestSum(t *testing.T) {
	if Sum(0) != 0 {
		t.Fatal("want 0 for n==0")
	}
}
