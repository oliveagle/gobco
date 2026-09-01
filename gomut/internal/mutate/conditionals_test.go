package mutate

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/oliveagle/gomut/internal/mutant"
)

// sites parses src (adding a package clause if missing) and returns
// the full source plus the sites produced by op (no types).
func sites(t *testing.T, op mutant.Operator, src string) (string, []mutant.Site) {
	t.Helper()
	full := withPackage(src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", full, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return full, op.Mutate(full, fset, f, nil)
}

// withPackage ensures src is a parseable Go file.
func withPackage(src string) string {
	if len(src) >= 7 && src[:7] == "package" {
		return src
	}
	return "package p\n\n" + src
}

// applyPatch applies a single patch to src.
func applyPatch(src string, p mutant.Patch) string {
	return src[:p.Start] + p.Replace + src[p.End:]
}

func TestConditionalsBoundary(t *testing.T) {
	src, got := sites(t, ConditionalsBoundary{}, "func f(a, b int) bool { return a > b && a >= b && a < b && a <= b && a == b && a != b }\n")
	if len(got) != 6 {
		t.Fatalf("got %d sites, want 6: %+v", len(got), got)
	}
	wants := []string{">=", ">", "<=", "<", "!=", "=="}
	for i, w := range wants {
		if got[i].Patch.Replace != w {
			t.Errorf("site %d replace = %q, want %q", i, got[i].Patch.Replace, w)
		}
		if out := applyPatch(src, got[i].Patch); out == src {
			t.Errorf("site %d did not change source", i)
		}
	}
	// == is also mutated for strings (comparability preserved).
	_, got2 := sites(t, ConditionalsBoundary{}, "func g(s string) bool { return s == \"x\" }\n")
	if len(got2) != 1 || got2[0].Patch.Replace != "!=" {
		t.Errorf("string ==: got %+v", got2)
	}
	// Non-comparison binary ops untouched.
	_, g := sites(t, ConditionalsBoundary{}, "func h(a, b int) int { return a + b * c }\n")
	if len(g) != 0 {
		t.Errorf("arithmetic: got %+v, want none", g)
	}
}

func TestNegateConditionals(t *testing.T) {
	src, got := sites(t, NegateConditionals{}, "func f(c bool, n int) { if c { _ = n }; for c { _ = n } }\n")
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	if out := applyPatch(src, got[0].Patch); !strings.Contains(out, "if !(c)") {
		t.Errorf("if patch: %q", out)
	}
	if !contains(got[1].Desc, "negated condition c") {
		t.Errorf("for site desc = %q", got[1].Desc)
	}
	// Already-negated condition: skipped (InvertNegs owns it).
	_, g := sites(t, NegateConditionals{}, "func g(c bool) { if !c {}\n}\n")
	if len(g) != 0 {
		t.Errorf("negated condition: got %+v, want none", g)
	}
	// Condition with a compound expression: covered.
	_, got3 := sites(t, NegateConditionals{}, "func h(x int) int { if x > 0 { return 1 }; return 0 }\n")
	if len(got3) != 1 || got3[0].Patch.Replace != "!(x > 0)" {
		t.Errorf("x>0: got %+v", got3)
	}
}

func TestInvertNegs(t *testing.T) {
	src, got := sites(t, InvertNegs{}, "func f(a, b bool) bool { return !a || b }\n")
	if len(got) != 1 {
		t.Fatalf("got %d sites, want 1: %+v", len(got), got)
	}
	if out := applyPatch(src, got[0].Patch); !strings.Contains(out, "return a || b") {
		t.Errorf("patch result: %q", out)
	}
	// No negation: nothing.
	_, g := sites(t, InvertNegs{}, "func g(a bool) bool { return a }\n")
	if len(g) != 0 {
		t.Errorf("got %+v, want none", g)
	}
}

func TestBooleanSwap(t *testing.T) {
	src, got := sites(t, BooleanSwap{}, "func f(a, b bool) bool { return a && b }\n")
	if len(got) != 1 || got[0].Patch.Replace != "||" {
		t.Fatalf("&&: got %+v", got)
	}
	if out := applyPatch(src, got[0].Patch); !strings.Contains(out, "return a || b") {
		t.Errorf("patch result: %q", out)
	}
	_, got2 := sites(t, BooleanSwap{}, "func g(a, b bool) bool { return a || b }\n")
	if len(got2) != 1 || got2[0].Patch.Replace != "&&" {
		t.Errorf("||: got %+v", got2)
	}
	// Nested: both connectives mutated once each.
	_, g := sites(t, BooleanSwap{}, "func h(a, b, c bool) bool { return a && b || c }\n")
	if len(g) != 2 {
		t.Errorf("nested: got %d, want 2", len(g))
	}
}

func TestSelect(t *testing.T) {
	if ops, err := Select(""); err != nil || len(ops) != 8 {
		t.Errorf("Select(\"\") = %d ops, err %v", len(ops), err)
	}
	if ops, err := Select("none"); err != nil || ops != nil {
		t.Errorf("Select(none) = %v, err %v", ops, err)
	}
	ops, err := Select("Math,Constant")
	if err != nil || len(ops) != 2 || ops[0].Name() != "Math" || ops[1].Name() != "Constant" {
		t.Errorf("Select(Math,Constant) = %v, err %v", Names(ops), err)
	}
	ops, err = Select("-Math,-Constant")
	if err != nil || len(ops) != 6 {
		t.Errorf("Select(-Math,-Constant) = %v, err %v", Names(ops), err)
	}
	if _, err := Select("Bogus"); err == nil {
		t.Error("Select(Bogus): expected error")
	}
	if _, err := Select("Math,-Constant"); err == nil {
		t.Error("Select(Math,-Constant): expected mix error")
	}
}

func TestAllIsIndependentCopy(t *testing.T) {
	a := All()
	a = a[:1]
	if len(a) != 1 {
		t.Fatalf("precondition: len(a) = %d, want 1", len(a))
	}
	b := All()
	if len(b) != 8 {
		t.Errorf("All() corrupted by previous slice: %d", len(b))
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
