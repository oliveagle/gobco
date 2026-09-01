package mutate

import "testing"

func TestReturnValsBool(t *testing.T) {
	src, fset, f, tc := typed(t, "func f(x int) bool { return x > 0 }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Patch.Replace] = true
	}
	if !seen["true"] || !seen["false"] {
		t.Errorf("replaces = %v, want true and false", got)
	}
}

func TestReturnValsNumericString(t *testing.T) {
	src, fset, f, tc := typed(t, "func f() (int, string) { return 1, \"x\" }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	if got[0].Patch.Replace != "0" || got[1].Patch.Replace != `""` {
		t.Errorf("replaces = %q %q", got[0].Patch.Replace, got[1].Patch.Replace)
	}
}

func TestReturnValsNilables(t *testing.T) {
	src, fset, f, tc := typed(t, "func f() (map[int]int, chan int, func()) { m := map[int]int{}; c := make(chan int); g := func() {}; return m, c, g }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	if len(got) != 3 {
		t.Fatalf("got %d sites, want 3: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Patch.Replace != "nil" {
			t.Errorf("replace = %q, want nil", s.Patch.Replace)
		}
	}
}

func TestReturnValsPointerToStruct(t *testing.T) {
	src, fset, f, tc := typed(t, "type T struct{ A int }\n\nfunc f() *T { return &T{A: 1} }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Patch.Replace] = true
	}
	if !seen["nil"] || !seen["&T{}"] {
		t.Errorf("replaces = %v, want nil and &T{}", seen)
	}
}

func TestReturnValsStruct(t *testing.T) {
	src, fset, f, tc := typed(t, "type P struct{ A int }\n\nfunc f() P { return P{1} }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	if len(got) != 1 || got[0].Patch.Replace != "P{}" {
		t.Errorf("got %+v", got)
	}
}

func TestReturnValsInFuncLit(t *testing.T) {
	src, fset, f, tc := typed(t, "func f() func() int { return func() int { return 7 } }\n")
	got := (ReturnVals{}).Mutate(src, fset, f, tc)
	// The outer return (a func value -> nil) and the inner return (7 -> 0)
	// are two independent single-site mutants.
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, x := range got {
		seen[x.Patch.Replace] = true
	}
	if !seen["nil"] || !seen["0"] {
		t.Errorf("replaces = %v, want nil and 0", seen)
	}
}

func TestReturnValsSkipsWhenUnreferencable(t *testing.T) {
	// Anonymous struct result: no name to construct, no site.
	src, fset, f, tc := typed(t, "func f() struct{ A int } { return struct{ A int }{1} }\n")
	if got := (ReturnVals{}).Mutate(src, fset, f, tc); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestReturnValsNoTypeCtx(t *testing.T) {
	src, fset, f, _ := typed(t, "func f() bool { return true }\n")
	if got := (ReturnVals{}).Mutate(src, fset, f, nil); len(got) != 0 {
		t.Errorf("got %+v, want none without types", got)
	}
}
