package mutate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// TypeCheck builds type information for a package's production files.
//
// It asks "go list -export -deps" for the compiled export-data files of
// the package and all of its dependencies, then type-checks the parsed
// files with go/types and the gc importer. This is the single place in
// gomut that talks to the Go type system (ADR-0001, constraint 5).
//
// On any failure it returns (nil, err); callers are expected to degrade
// to syntactic-only operators and report the error as a warning rather
// than aborting the run (ADR-0001 D4).
func TypeCheck(dir, importPath string, files []*ast.File, fset *token.FileSet) (*mutant.TypeCtx, error) {
	exports, err := exportFiles(dir, importPath)
	if err != nil {
		return nil, fmt.Errorf("go list -export: %w", err)
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	conf := types.Config{
		Importer: imp,
		Sizes:    types.SizesFor("gc", "amd64"),
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	pkg, err := conf.Check(importPath, fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type check: %v", firstErrLine(err))
	}
	return &mutant.TypeCtx{Pkg: pkg, Info: info, Fset: fset}, nil
}

// exportFiles returns the compiled export-data file for the package and
// every dependency, keyed by import path.
func exportFiles(dir, importPath string) (map[string]string, error) {
	out, err := runGo(dir, "list", "-export", "-deps", "-json", importPath)
	if err != nil {
		return nil, err
	}
	exports := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var e struct {
			ImportPath string
			Export     string
			Error      *struct{ Err string }
		}
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("parsing go list output: %w", err)
		}
		if e.Error != nil && e.Error.Err != "" {
			return nil, fmt.Errorf("building %s: %s", e.ImportPath, e.Error.Err)
		}
		if e.Export != "" {
			exports[e.ImportPath] = e.Export
		}
	}
	return exports, nil
}

func runGo(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) > 512 {
			msg = msg[len(msg)-512:]
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	return out, nil
}

func firstErrLine(err error) string {
	s := err.Error()
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		return string(s[:i])
	}
	return s
}
