package cover

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestParseProfile(t *testing.T) {
	data := `mode: count
github.com/x/y/a.go:2.1,4.2 1 1
github.com/x/y/a.go:6.1,8.2 1 0
github.com/x/y/b.go:10.5,12.3 3 2
`
	p, err := ParseProfile([]byte(data))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if len(p.Statements) != 3 {
		t.Fatalf("statements = %d, want 3", len(p.Statements))
	}
	if p.Statements[0].File != "github.com/x/y/a.go" || p.Statements[0].Start != 2 || p.Statements[0].End != 4 || !p.Statements[0].Covered {
		t.Errorf("stmt0 = %+v", p.Statements[0])
	}
	if p.Statements[1].Covered {
		t.Errorf("stmt1 should be uncovered")
	}
	lines := p.CoveredLines()
	if !lines["github.com/x/y/a.go"][2] || !lines["github.com/x/y/a.go"][3] || !lines["github.com/x/y/a.go"][4] {
		t.Errorf("covered lines of a.go = %v", lines["github.com/x/y/a.go"])
	}
	if lines["github.com/x/y/a.go"][6] || lines["github.com/x/y/a.go"][7] || lines["github.com/x/y/a.go"][8] {
		t.Errorf("uncovered range of a.go leaked: %v", lines["github.com/x/y/a.go"])
	}
}

func TestParseProfileOldFormat(t *testing.T) {
	// Older Go emits a single "line.startcol" position per line.
	data := `mode: set
github.com/x/y/a.go:5.3 1 1
`
	p, err := ParseProfile([]byte(data))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if len(p.Statements) != 1 {
		t.Fatalf("statements = %d, want 1", len(p.Statements))
	}
	st := p.Statements[0]
	if st.File != "github.com/x/y/a.go" || st.Start != 5 || st.End != 5 {
		t.Errorf("stmt = %+v", st)
	}
}

func TestParseProfileMalformed(t *testing.T) {
	for _, data := range []string{
		"mode: count\nnolineshere\n",
		"mode: count\nfile 1 1\n",
		"mode: count\nfile:1,2 1 x\n",
	} {
		if _, err := ParseProfile([]byte(data)); err == nil {
			t.Errorf("ParseProfile(%q): expected error", data)
		}
	}
}

func TestLineToTests(t *testing.T) {
	l := &LineToTests{}
	l.Add(mustProfile(t, "mode: count\npkg/a.go:2.1,3.1 1 1\n"), "TestA")
	l.Add(mustProfile(t, "mode: count\npkg/a.go:2.1,4.1 1 1\npkg/b.go:1.1,2.1 1 1\n"), "TestB")

	got := l.TestNamesAt("pkg/a.go", 2)
	if len(got) != 2 || got[0] != "TestA" || got[1] != "TestB" {
		t.Errorf("TestNamesAt(a.go,2) = %v", got)
	}
	got = l.TestNamesAt("pkg/a.go", 4)
	if len(got) != 1 || got[0] != "TestB" {
		t.Errorf("TestNamesAt(a.go,4) = %v", got)
	}
	if g := l.TestNamesAt("pkg/c.go", 1); len(g) != 0 {
		t.Errorf("TestNamesAt(c.go,1) = %v, want empty", g)
	}
}

func mustProfile(t *testing.T, s string) *Profile {
	t.Helper()
	p, err := ParseProfile([]byte(s))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	return p
}

func TestTestFuncs(t *testing.T) {
	src := `package p

import "testing"

func TestAlpha(t *testing.T) {}

func TestMain(m *testing.M) { m.Run() }

func helper(t *testing.T) {}

func (r *rec) TestBeta(t *testing.T) {}

func TestGamma(t *testing.T, extra int) {}

func BenchmarkNope(b *testing.B) {}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p_test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := TestFuncs([]*ast.File{f})
	want := []string{"TestAlpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("TestFuncs = %v, want %v", got, want)
	}
}
