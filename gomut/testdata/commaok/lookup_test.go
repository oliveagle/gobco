package commaok

import "testing"

func TestLookup(t *testing.T) {
	m := map[string]int{"x": 1, "y": 2}
	if v, ok := Lookup(m, "x"); !ok || v != 1 {
		t.Fatalf("Lookup(x) = %d, %v; want 1, true", v, ok)
	}
	if v, ok := Lookup(m, "nope"); ok || v != 0 {
		t.Fatalf("Lookup(nope) = %d, %v; want 0, false", v, ok)
	}
}

func TestDouble(t *testing.T) {
	if got := Double(3); got != 6 {
		t.Fatalf("Double(3) = %d; want 6", got)
	}
}
