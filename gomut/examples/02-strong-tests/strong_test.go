package strong

import "testing"

func TestBetween(t *testing.T) {
	cases := []struct {
		v, lo, hi int
		want     bool
	}{
		{5, 0, 10, true},
		{-1, 0, 10, false}, // below lo
		{11, 0, 10, false}, // above hi
		{5, 5, 10, true},   // lo == v
		{5, 0, 5, true},    // v == hi
	}
	for _, c := range cases {
		if got := Between(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("Between(%d,%d,%d) = %v, want %v", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

func TestAbs(t *testing.T) {
	cases := []struct{ in, want int }{
		{3, 3}, {-3, 3}, {0, 0},
	}
	for _, c := range cases {
		if got := Abs(c.in); got != c.want {
			t.Fatalf("Abs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSum(t *testing.T) {
	for _, n := range []int{0, 1, 5, 10} {
		want := n * (n - 1) / 2 // 0 + 1 + ... + (n - 1)
		if got := Sum(n); got != want {
			t.Fatalf("Sum(%d) = %d, want %d", n, got, want)
		}
	}
}
