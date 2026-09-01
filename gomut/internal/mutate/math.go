package mutate

import (
	"fmt"
	"github.com/oliveagle/gomut/internal/mutant"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// Math mutates arithmetic operators:
// + <-> -, * <-> /, % -> /, << <-> >>.
// Both operands must be numeric (type-aware: string "+" is concatenation
// and is left alone). Compound assignments (+=, -=, ...) are included.
type Math struct{}

func (Math) Name() string     { return "Math" }
func (Math) NeedsTypes() bool { return true }

var mathSwaps = map[token.Token]string{
	token.ADD: "-",
	token.SUB: "+",
	token.MUL: "/",
	token.QUO: "*",
	token.REM: "/",
	token.SHL: ">>",
	token.SHR: "<<",

	// Compound assignments: mutate x += y into x -= y (2-char replace).
	token.ADD_ASSIGN: "-=",
	token.SUB_ASSIGN: "+=",
	token.MUL_ASSIGN: "/=",
	token.QUO_ASSIGN: "*=",
	token.REM_ASSIGN: "/=",
	token.SHL_ASSIGN: ">>=",
	token.SHR_ASSIGN: "<<=",
}

func (Math) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	if tc == nil {
		return nil
	}
	var sites []mutant.Site
	addBin := func(be *ast.BinaryExpr) {
		replace, ok := mathSwaps[be.Op]
		if !ok {
			return
		}
		if !isNumeric(tc.Info.TypeOf(be.X)) || !isNumeric(tc.Info.TypeOf(be.Y)) {
			return
		}
		if (be.Op == token.SHL || be.Op == token.SHR) && !isInteger(tc.Info.TypeOf(be.X)) {
			return
		}
		pos := fset.Position(be.OpPos)
		expr := src[fset.Position(be.Pos()).Offset:fset.Position(be.End()).Offset]
		// Build the full replacement expression (e.g. "x * 2" -> "x / 2")
		// for the description by swapping the operator inside expr.
		opStr := be.Op.String()
		opOff := pos.Offset - fset.Position(be.Pos()).Offset
		replExpr := expr[:opOff] + replace + expr[opOff+len(opStr):]
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Desc: fmt.Sprintf("replaced %q with %q", expr, replExpr),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(be.Op.String()),
				Replace: replace,
			},
		})
	}
	addAssign := func(ass *ast.AssignStmt) {
		if _, ok := mathSwaps[ass.Tok]; !ok {
			return
		}
		if len(ass.Lhs) != 1 || len(ass.Rhs) != 1 {
			return
		}
		l := tc.Info.TypeOf(ass.Lhs[0])
		r := tc.Info.TypeOf(ass.Rhs[0])
		if l == nil || r == nil || !isNumeric(l) || !isNumeric(r) {
			return
		}
		if (ass.Tok == token.SHL_ASSIGN || ass.Tok == token.SHR_ASSIGN) && !isInteger(l) {
			return
		}
		pos := fset.Position(ass.TokPos)
		orig := src[pos.Offset : pos.Offset+len(ass.Tok.String())]
		replace := mathSwaps[ass.Tok]
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Desc: fmt.Sprintf("replaced %q with %q", orig, replace),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(ass.Tok.String()),
				Replace: replace,
			},
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BinaryExpr:
			addBin(v)
		case *ast.AssignStmt:
			addAssign(v)
		}
		return true
	})
	return sites
}

func isNumeric(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return false
	}
	switch b.Kind() {
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
		types.Uintptr, types.Float32, types.Float64,
		types.Complex64, types.Complex128,
		types.UntypedInt, types.UntypedFloat:
		return true
	}
	return false
}

func isInteger(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return false
	}
	switch b.Kind() {
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
		types.Uintptr, types.UntypedInt:
		return true
	}
	return false
}

// Increments mutates i++ / i-- (and their short forms in for statements).
type Increments struct{}

func (Increments) Name() string     { return "Increments" }
func (Increments) NeedsTypes() bool { return false }

var incSwaps = map[token.Token]string{
	token.INC: "--",
	token.DEC: "++",
}

func (Increments) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.IncDecStmt)
		if !ok {
			return true
		}
		replace, ok := incSwaps[st.Tok]
		if !ok {
			return true
		}
		pos := fset.Position(st.TokPos)
		orig := src[pos.Offset : pos.Offset+len(st.Tok.String())]
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Desc: fmt.Sprintf("replaced %s%s with %s%s", st.X, orig, st.X, replace),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(st.Tok.String()),
				Replace: replace,
			},
		})
		return true
	})
	return sites
}

// Constant replaces numeric literals with nearby values (n -> n+1).
type Constant struct{}

func (Constant) Name() string     { return "Constant" }
func (Constant) NeedsTypes() bool { return false }

func (Constant) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(file, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		var newVal string
		switch bl.Kind {
		case token.INT:
			v, err := parseLitInt(bl.Value)
			if err != nil {
				return true
			}
			if tc != nil {
				if t := tc.Info.TypeOf(bl); t != nil && !fitsInt(t, v+1) {
					return true
				}
			}
			newVal = strconv.FormatInt(v+1, 10)
		case token.FLOAT:
			v, err := strconv.ParseFloat(strings.ReplaceAll(bl.Value, "_", ""), 64)
			if err != nil {
				return true
			}
			newVal = strconv.FormatFloat(v+1, 'f', -1, 64)
		default:
			return true
		}
		pos := fset.Position(bl.Pos())
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Desc: fmt.Sprintf("constant %s replaced with %s", bl.Value, newVal),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(bl.Value),
				Replace: newVal,
			},
		})
		return true
	})
	return sites
}

func parseLitInt(s string) (int64, error) {
	t := strings.ReplaceAll(s, "_", "")
	base := 10
	switch {
	case strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X"):
		base, t = 16, t[2:]
	case strings.HasPrefix(t, "0b") || strings.HasPrefix(t, "0B"):
		base, t = 2, t[2:]
	case strings.HasPrefix(t, "0o") || strings.HasPrefix(t, "0O"):
		base, t = 8, t[2:]
	case strings.HasPrefix(t, "0") && len(t) > 1:
		base, t = 8, t[1:] // legacy octal
	}
	return strconv.ParseInt(t, base, 64)
}

func fitsInt(t types.Type, v int64) bool {
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return true
	}
	switch b.Kind() {
	case types.Int8:
		return v >= -128 && v < 128
	case types.Uint8:
		return v >= 0 && v < 256
	case types.Int16:
		return v >= -32768 && v < 32768
	case types.Uint16:
		return v >= 0 && v < 65536
	case types.Int32:
		return v >= -2147483648 && v < 2147483648
	case types.Uint32:
		return v >= 0 && v < 4294967296
	case types.Int64:
		return v >= -9223372036854775808 && v < 9223372036854775807
	case types.Uint64:
		return v >= 0
	}
	return true
}
