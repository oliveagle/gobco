package mutate

import (
	"fmt"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"go/ast"
	"go/token"
	"go/constant"
	"go/types"
	"strconv"
	"strings"
)

// ReturnVals replaces return values with type-appropriate zero values:
// bool -> true/false, numeric -> 0, string -> "", nilable -> nil,
// named struct -> T{} / &T{} (pointer).
//
// Port of pitest's ReturnsMutatorGroup (Primitive/Null/Boolean/Empty returns),
// adapted to Go's type system.
type ReturnVals struct{}

func (ReturnVals) Name() string     { return "ReturnVals" }
func (ReturnVals) NeedsTypes() bool { return true }

func (ReturnVals) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	if tc == nil {
		return nil
	}
	var sites []mutant.Site
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		// Skip functions never called anywhere in the package: their
		// return-value mutants would only ever be NO_COVERAGE, and the
		// ADR-0001 fixture expects them to be absent entirely (T-12).
		if tc.Used != nil && !tc.Used[fd.Name.Name] {
			continue
		}
		tf, ok := tc.Info.Defs[fd.Name].(*types.Func)
		if !ok {
			continue
		}
		sites = append(sites, mutateReturnBody(src, fset, file, tc, tf.Type().(*types.Signature), fd.Body)...)
	}
	return sites
}

func mutateReturnBody(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx, sig *types.Signature, body *ast.BlockStmt) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			ls, ok := tc.Info.TypeOf(v).(*types.Signature)
			if !ok {
				return true
			}
			sites = append(sites, mutateReturnBody(src, fset, file, tc, ls, v.Body)...)
			return false // handled recursively
		case *ast.ReturnStmt:
			sites = append(sites, mutateReturn(src, fset, file, tc, sig, v)...)
		}
		return true
	})
	return sites
}

func mutateReturn(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx, sig *types.Signature, ret *ast.ReturnStmt) []mutant.Site {
	if len(ret.Results) == 0 {
		return nil // bare return with named results: v2 (todo T-12)
	}
	var sites []mutant.Site
	for i, res := range ret.Results {
		if i >= sig.Results().Len() {
			break
		}
		t := sig.Results().At(i).Type()
		reps := returnReplacements(t, tc, file)
		start := fset.Position(res.Pos()).Offset
		end := fset.Position(res.End()).Offset
		orig := strings.TrimSpace(src[start:end])
		// Compute the original's constant value so we can skip replacements
		// that resolve to the same compile-time value. Catches the iota-bug
		// pattern `ModeText` ↔ `0` where the named constant and the integer
		// literal are byte-identical at compile time.
		origConst := astConstValue(res, tc)
		for _, rep := range reps {
			if rep == orig {
				continue // no-op mutant
			}
			if origConst != nil {
				if repConst := literalConstValue(rep); repConst != nil && sameConst(*origConst, *repConst) {
					continue // equivalent mutation: skip
				}
			}
			sites = append(sites, mutant.Site{
				Line: fset.Position(res.Pos()).Line,
				Desc: fmt.Sprintf("return value replaced with %s (%s)", rep, t),
				Patch: mutant.Patch{
					Start:   start,
					End:     end,
					Replace: rep,
				},
			})
		}
	}
	return sites
}

// astConstValue extracts the constant value from an AST expression.
// Supports BasicLit (numeric/string literals), Ident (named constants
// resolved via tc.Info), and unary +/- wrapping any of these. Returns nil
// for expressions whose constant value cannot be determined statically.
func astConstValue(expr ast.Expr, tc *mutant.TypeCtx) *string {
	if expr == nil || tc == nil || tc.Info == nil {
		return nil
	}
	switch v := expr.(type) {
	case *ast.BasicLit:
		return literalConstValue(v.Value)
	case *ast.Ident:
		obj := tc.Info.ObjectOf(v)
		if con, ok := obj.(*types.Const); ok {
			s := constToString(con.Val())
			if s != "" {
				return &s
			}
		}
	case *ast.ParenExpr:
		return astConstValue(v.X, tc)
	case *ast.UnaryExpr:
		// Support leading +/- on numeric literals and named consts.
		inner := astConstValue(v.X, tc)
		if inner == nil {
			return nil
		}
		if v.Op == token.SUB {
			if strings.HasPrefix(*inner, "-") {
				*inner = strings.TrimPrefix(*inner, "-")
			} else {
				neg := "-" + *inner
				inner = &neg
			}
			return inner
		}
	}
	return nil
}

// literalConstValue parses a Go literal string into a normalized constant
// representation. Returns nil for literals we don't recognize.
func literalConstValue(s string) *string {
	t := strings.TrimSpace(s)
	// Strip underscores (Go allows them in numeric literals).
	stripped := strings.ReplaceAll(t, "_", "")
	// Integer?
	if _, err := strconv.ParseInt(stripped, 0, 64); err == nil {
		out := stripped
		return &out
	}
	// Float?
	if _, err := strconv.ParseFloat(stripped, 64); err == nil {
		out := stripped
		return &out
	}
	return nil
}

// sameConst reports whether two constant-value strings denote the same Go
// constant value. Comparison is performed numerically (after parsing both as
// float64) so that "0" and "0.0" compare equal, "1" and "1.0" compare equal,
// but "0" and "1" do not. Strings are compared verbatim.
func sameConst(a, b string) bool {
	// Strip underscores (Go allows them in numeric literals).
	na := strings.ReplaceAll(a, "_", "")
	nb := strings.ReplaceAll(b, "_", "")
	if na == nb {
		return true
	}
	fa, errA := strconv.ParseFloat(na, 64)
	fb, errB := strconv.ParseFloat(nb, 64)
	if errA == nil && errB == nil {
		return fa == fb
	}
	return false
}

// constToString converts a types.Constant to its textual representation.
// Returns "" for values we don't know how to render.
func constToString(v constant.Value) string {
	if v == nil {
		return ""
	}
	// Check Kind() first; Int64Val/Float64Val panic on wrong types.
	switch v.Kind() {
	case constant.Int:
		if i, ok := constant.Int64Val(v); ok {
			return strconv.FormatInt(i, 10)
		}
	case constant.Float:
		if f, ok := constant.Float64Val(v); ok {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	case constant.String:
		return constant.StringVal(v)
	case constant.Bool:
		return strconv.FormatBool(constant.BoolVal(v))
	}
	return ""
}


// returnReplacements lists zero-value replacements for a result type.
func returnReplacements(t types.Type, tc *mutant.TypeCtx, file *ast.File) []string {
	u := t.Underlying()
	switch v := u.(type) {
	case *types.Basic:
		switch v.Kind() {
		case types.Bool:
			return []string{"true", "false"}
		case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Uintptr, types.Float32, types.Float64,
			types.Complex64, types.Complex128,
			types.UntypedInt, types.UntypedFloat, types.UntypedRune:
			return []string{"0"}
		case types.String, types.UntypedString:
			return []string{`""`}
		case types.UntypedBool:
			return []string{"true", "false"}
		}
		return nil
	case *types.Pointer:
		res := []string{"nil"}
		if nt, ok := v.Elem().(*types.Named); ok && nt.TypeArgs() == nil {
			if _, ok := nt.Underlying().(*types.Struct); ok {
				if name := qualifiedName(nt, tc, file); name != "" {
					res = append(res, "&"+name+"{}")
				}
			}
		}
		return res
	case *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return []string{"nil"}
	case *types.Struct:
		if nt, ok := t.(*types.Named); ok && nt.TypeArgs() == nil {
			if name := qualifiedName(nt, tc, file); name != "" {
				return []string{name + "{}"}
			}
		}
		return nil // anonymous struct: no name to construct
	case *types.Array:
		return nil // v1: skip arrays
	}
	return nil
}

// qualifiedName renders a named type as it can be written in this file
// (e.g. "T" or "time.Time"), or "" if it cannot be referenced.
func qualifiedName(nt *types.Named, tc *mutant.TypeCtx, file *ast.File) string {
	obj := nt.Obj()
	name := obj.Name()
	pkg := obj.Pkg()
	if pkg == nil {
		return ""
	}
	if pkg == tc.Pkg {
		return name
	}
	if !ast.IsExported(name) {
		return ""
	}
	local, ok := localImportName(pkg.Path(), tc, file)
	if !ok {
		return ""
	}
	return local + "." + name
}

func localImportName(path string, tc *mutant.TypeCtx, file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if p != path {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return "", false
			}
			return imp.Name.Name, true
		}
		for _, ip := range tc.Pkg.Imports() {
			if ip.Path() == path {
				return ip.Name(), true
			}
		}
		return "", false
	}
	return "", false
}
