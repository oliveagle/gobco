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

// TestReturnValsSkipsEquivalentNamedConst is a regression test for the
// "iota bug": when a function returns a named constant whose underlying
// value equals a literal the mutator would generate (e.g. `ModeText Mode
// = iota` resolves to 0, and the mutator would replace `return ModeText`
// with `return 0`), both versions compile to byte-identical machine code.
// The mutator must SKIP such equivalent replacements — otherwise it
// produces "mutations" that are pure noise.
//
// Specifically: ONLY the equivalent mutation is the one where the
// replacement literal has the SAME value as the named const. For
// `ModeText` (value 0), the only equivalent mutation is `return 0`.
// `return 1` for ModeText is NOT equivalent (it changes the value) —
// we keep that mutation.
// Similarly for ModeJSON (value 1) and ModeTOON (value 2): the only
// equivalent mutations are `return 1` and `return 2` respectively.
func TestReturnValsSkipsEquivalentNamedConst(t *testing.T) {
	src := `package p

type Mode int

const (
	ModeText Mode = iota
	ModeJSON
	ModeTOON
)

func ParseMode(s string) Mode {
	switch s {
	case "json":
		return ModeJSON
	case "toon":
		return ModeTOON
	default:
		return ModeText
	}
}
`
	srcBytes, fset, f, tc := typed(t, src)
	got := (ReturnVals{}).Mutate(srcBytes, fset, f, tc)
	// Build a map: line → mutated replacement for fast lookup.
	byLine := map[int]string{}
	for _, site := range got {
		byLine[site.Line] = site.Patch.Replace
	}
	// Equivalence expectations per line (1-indexed, hardcoded from src):
	//   line 14: `return ModeJSON` (value 1) → mutation must NOT be `1`
	//   line 16: `return ModeTOON` (value 2) → mutation must NOT be `2`
	//   line 18: `return ModeText` (value 0) → mutation must NOT be `0`
	checks := []struct {
		line      int
		orig      string // original identifier in source
		forbidMut string // replacement literal that would be EQUIVALENT (= bug)
	}{
		{14, "ModeJSON", "1"},
		{16, "ModeTOON", "2"},
		{18, "ModeText", "0"},
	}
	for _, c := range checks {
		gotRep, ok := byLine[c.line]
		if !ok {
			// Mutator may skip a line entirely (NO_COVERAGE / unreferenced).
			// That's also acceptable.
			continue
		}
		if gotRep == c.forbidMut {
			t.Errorf("equivalent mutation survived on line %d: "+
				"`return %s` (= %s) replaced with `return %s` — "+
				"compile-time identical, the mutator must skip it",
				c.line, c.orig, c.forbidMut, gotRep)
		}
	}
}

