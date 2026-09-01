package engine

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/oliveagle/gomut/internal/mutate"
)

func TestDebugSites(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	srcPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "sample", "math.go")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, op := range mutate.All() {
		sites := op.Mutate(src, fset, f, nil)
		fmt.Printf("== %s: %d sites\n", op.Name(), len(sites))
		for _, s := range sites {
			fmt.Printf("   line %d: %s  patch[%d:%d]=%q\n", s.Line, s.Desc, s.Patch.Start, s.Patch.End, s.Patch.Replace)
		}
	}
}
