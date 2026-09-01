package mutate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// typed parses src (adding a package clause if missing) and returns the
// full source, parsed file, and TypeCtx. src must not import external
// packages.
func typed(t *testing.T, src string) (string, *token.FileSet, *ast.File, *mutant.TypeCtx) {
	t.Helper()
	full := withPackage(src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", full, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Sizes: types.SizesFor("gc", "amd64")}
	pkg, err := conf.Check("fixture", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("type check: %v", err)
	}
	return full, fset, f, &mutant.TypeCtx{Pkg: pkg, Info: info, Fset: fset}
}

func TestMathNumeric(t *testing.T) {
	src, fset, f, tc := typed(t, "func f(a, b, c, d, e, f2 int) int { return a + b - c * d / e % f2 }\n")
	got := (Math{}).Mutate(src, fset, f, tc)
	if len(got) != 5 {
		t.Fatalf("got %d sites, want 5: %+v", len(got), got)
	}
	wants := []string{"+", "-", "/", "*", "/"}
	for i, w := range wants {
		if got[i].Patch.Replace != w {
			t.Errorf("site %d = %q, want %q", i, got[i].Patch.Replace, w)
		}
	}
}

func TestMathStringConcatUntouched(t *testing.T) {
	src, fset, f, tc := typed(t, "func f(a, b string) string { return a + b }\n")
	if got := (Math{}).Mutate(src, fset, f, tc); len(got) != 0 {
		t.Errorf("string +: got %+v, want none", got)
	}
}

func TestMathShiftIntegerOnly(t *testing.T) {
	src, fset, f, tc := typed(t, "func f(x int) int { return x << 1 }\n")
	got := (Math{}).Mutate(src, fset, f, tc)
	if len(got) != 1 || got[0].Patch.Replace != ">>" {
		t.Errorf("shift: got %+v", got)
	}
}

func TestMathCompoundAssign(t *testing.T) {
	src, fset, f, tc := typed(t, "func f() { var a int; a += 1; a *= 2 }\n")
	got := (Math{}).Mutate(src, fset, f, tc)
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	if got[0].Patch.Replace != "-=" || got[1].Patch.Replace != "/=" {
		t.Errorf("replaces = %q %q", got[0].Patch.Replace, got[1].Patch.Replace)
	}
	// String compound assign untouched.
	src2, fset2, f2, tc2 := typed(t, "func g() { var s string; s += \"x\" }\n")
	if got := (Math{}).Mutate(src2, fset2, f2, tc2); len(got) != 0 {
		t.Errorf("string +=: got %+v, want none", got)
	}
}

func TestIncrements(t *testing.T) {
	_, got := sites(t, Increments{}, "func f(n int) int { i := 0; for i < n { i++ }; i--; return i }\n")
	if len(got) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(got), got)
	}
	if got[0].Patch.Replace != "--" || got[1].Patch.Replace != "++" {
		t.Errorf("replaces = %q %q", got[0].Patch.Replace, got[1].Patch.Replace)
	}
}

func TestConstant(t *testing.T) {
	_, got := sites(t, Constant{}, "func f() (int, int, int) { a := 5; b := 0x10; c := 3; return a, b, c }\n")
	if len(got) != 3 {
		t.Fatalf("got %d sites, want 3: %+v", len(got), got)
	}
	wants := []string{"6", "17", "4"}
	for i, w := range wants {
		if got[i].Patch.Replace != w {
			t.Errorf("site %d = %q, want %q", i, got[i].Patch.Replace, w)
		}
	}
}

func TestConstantFloat(t *testing.T) {
	_, got := sites(t, Constant{}, "func f() float64 { return 1.5 }\n")
	if len(got) != 1 || got[0].Patch.Replace != "2.5" {
		t.Errorf("got %+v", got)
	}
}

func TestConstantOverflowGuarded(t *testing.T) {
	// With types available, an int8 literal at its max must not overflow.
	src, fset, f, tc := typed(t, "func f() int8 { return 127 }\n")
	if got := (Constant{}).Mutate(src, fset, f, tc); len(got) != 0 {
		t.Errorf("int8 max: got %+v, want none (overflow guarded)", got)
	}
}

func TestTypeAwareDegradesWithoutTypes(t *testing.T) {
	src := "package p\n\nfunc f(a, b int) int { return a + b }\n"
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := (Math{}).Mutate(src, fset, astFile, nil); len(got) != 0 {
		t.Errorf("Math without types: got %+v, want none", got)
	}
}
