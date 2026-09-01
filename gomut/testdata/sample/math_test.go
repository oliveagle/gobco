package sample

import "testing"

func TestIsPositive(t *testing.T) {
	cases := []struct {
		in   int
		want bool
	}{
		{1, true},
		{0, false},
		{-1, false},
	}
	for _, c := range cases {
		if got := IsPositive(c.in); got != c.want {
			t.Errorf("IsPositive(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAbs(t *testing.T) {
	if got := Abs(3); got != 3 {
		t.Errorf("Abs(3) = %d, want 3", got)
	}
	if got := Abs(-3); got != 3 {
		t.Errorf("Abs(-3) = %d, want 3", got)
	}
}

func TestCount(t *testing.T) {
	if got := Count(0); got != 0 {
		t.Errorf("Count(0) = %d, want 0", got)
	}
	if got := Count(5); got != 5 {
		t.Errorf("Count(5) = %d, want 5", got)
	}
}
