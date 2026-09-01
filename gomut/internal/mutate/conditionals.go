package mutate

import (
	"fmt"
	"github.com/oliveagle/gobco/gomut/internal/mutant"
	"go/ast"
	"go/token"
)

// ConditionalsBoundary mutates comparison operators at their boundary:
// > <-> >=, < <-> <=, == <-> !=.
type ConditionalsBoundary struct{}

func (ConditionalsBoundary) Name() string     { return "ConditionalsBoundary" }
func (ConditionalsBoundary) NeedsTypes() bool { return false }

var boundarySwaps = map[token.Token]string{
	token.GEQ: ">",
	token.GTR: ">=",
	token.LSS: "<=",
	token.LEQ: "<",
	token.EQL: "!=",
	token.NEQ: "==",
}

func (ConditionalsBoundary) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(file, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		replace, ok := boundarySwaps[be.Op]
		if !ok {
			return true
		}
		pos := fset.Position(be.OpPos)
		expr := src[fset.Position(be.Pos()).Offset:fset.Position(be.End()).Offset]
		// Build the full replacement expression (e.g. "v > 0" -> "v >= 0")
		// for the description, by swapping the operator inside the expr.
		opStr := be.Op.String()
		opOff := pos.Offset - fset.Position(be.Pos()).Offset
		replExpr := expr[:opOff] + replace + expr[opOff+len(opStr):]
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Col:  pos.Column,
			Desc: fmt.Sprintf("replaced %q with %q", expr, replExpr),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(be.Op.String()),
				Replace: replace,
			},
		})
		return true
	})
	return sites
}

// NegateConditionals negates the condition of if and for statements:
// "if c" -> "if !(c)". Already-negated conditions are skipped (InvertNegs
// covers "!c" -> "c").
type NegateConditionals struct{}

func (NegateConditionals) Name() string     { return "NegateConditionals" }
func (NegateConditionals) NeedsTypes() bool { return false }

func (NegateConditionals) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	add := func(cond ast.Expr) {
		if cond == nil {
			return
		}
		if u, ok := cond.(*ast.UnaryExpr); ok && u.Op == token.NOT {
			return
		}
		start := fset.Position(cond.Pos()).Offset
		end := fset.Position(cond.End()).Offset
		orig := src[start:end]
		sites = append(sites, mutant.Site{
			Line: fset.Position(cond.Pos()).Line,
			Desc: fmt.Sprintf("negated condition %s", orig),
			Patch: mutant.Patch{
				Start:   start,
				End:     end,
				Replace: "!(" + orig + ")",
			},
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IfStmt:
			add(s.Cond)
		case *ast.ForStmt:
			add(s.Cond)
		}
		return true
	})
	return sites
}

// InvertNegs removes negations: "!x" -> "x".
type InvertNegs struct{}

func (InvertNegs) Name() string     { return "InvertNegs" }
func (InvertNegs) NeedsTypes() bool { return false }

func (InvertNegs) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(file, func(n ast.Node) bool {
		u, ok := n.(*ast.UnaryExpr)
		if !ok || u.Op != token.NOT {
			return true
		}
		pos := fset.Position(u.OpPos)
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Col:  pos.Column,
			Desc: "removed negation",
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + 1,
				Replace: "",
			},
		})
		return true
	})
	return sites
}

// BooleanSwap swaps boolean connectives: "&&" <-> "||".
// (Go extension; pitest achieves the equivalent through jump-instruction
// mutations at the bytecode level.)
type BooleanSwap struct{}

func (BooleanSwap) Name() string     { return "BooleanSwap" }
func (BooleanSwap) NeedsTypes() bool { return false }

var boolSwaps = map[token.Token]string{
	token.LAND: "||",
	token.LOR:  "&&",
}

func (BooleanSwap) Mutate(src string, fset *token.FileSet, file *ast.File, tc *mutant.TypeCtx) []mutant.Site {
	var sites []mutant.Site
	ast.Inspect(file, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		replace, ok := boolSwaps[be.Op]
		if !ok {
			return true
		}
		pos := fset.Position(be.OpPos)
		orig := src[pos.Offset : pos.Offset+len(be.Op.String())]
		sites = append(sites, mutant.Site{
			Line: pos.Line,
			Desc: fmt.Sprintf("replaced %q with %q", orig, replace),
			Patch: mutant.Patch{
				Start:   pos.Offset,
				End:     pos.Offset + len(be.Op.String()),
				Replace: replace,
			},
		})
		return true
	})
	return sites
}
