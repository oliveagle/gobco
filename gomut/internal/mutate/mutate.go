// Package mutate implements gomut's built-in mutation operators.
//
// Each operator is a stateless visitor over one parsed source file. It
// reports zero or more candidate mutation sites (mutant.Site), each a
// minimal text patch; the engine applies exactly one site at a time to
// materialize one mutant per site (no junk mutations, every mutant is
// explainable by a diff).
//
// Type-aware operators (Math, Constant, ReturnVals) consult the type
// information in mutant.TypeCtx and degrade to syntactic-only behaviour
// (or skip) when it is unavailable, per ADR-0001 D4.
package mutate

import (
	"fmt"
	"strings"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// defaultOps is the built-in operator set, in report order (ADR-0001 D4).
var defaultOps []mutant.Operator = []mutant.Operator{
	ConditionalsBoundary{},
	NegateConditionals{},
	InvertNegs{},
	BooleanSwap{},
	Math{},
	Increments{},
	Constant{},
	ReturnVals{},
}

// All returns a copy of the full built-in operator set (the default).
func All() []mutant.Operator {
	out := make([]mutant.Operator, len(defaultOps))
	copy(out, defaultOps)
	return out
}

// Select parses a -mutators spec:
//
//	"" / "default" / "all" -> the full default set
//	"none"                 -> no operators
//	"Math,Constant"        -> an explicit subset (names must be known)
//	"-Math,-Constant"      -> the default set minus the named operators
//
// Bare names and "-" names cannot be mixed in one spec. Unknown names are
// errors.
func Select(spec string) ([]mutant.Operator, error) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "", "default", "all":
		return All(), nil
	case "none":
		return nil, nil
	}
	byName := map[string]mutant.Operator{}
	for _, op := range defaultOps {
		byName[op.Name()] = op
	}
	var parts []string
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return All(), nil
	}
	allRemoved := true
	for _, p := range parts {
		if !strings.HasPrefix(p, "-") {
			allRemoved = false
			break
		}
	}
	if allRemoved {
		selected := All()
		for _, p := range parts {
			name := strings.TrimSpace(p[1:])
			op, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("unknown operator %q", name)
			}
			selected = removeOp(selected, op)
		}
		return selected, nil
	}
	var selected []mutant.Operator
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			return nil, fmt.Errorf("cannot mix bare and '-' operator names in %q", spec)
		}
		op, ok := byName[p]
		if !ok {
			return nil, fmt.Errorf("unknown operator %q", p)
		}
		selected = append(selected, op)
	}
	return selected, nil
}

func removeOp(selected []mutant.Operator, drop mutant.Operator) []mutant.Operator {
	kept := selected[:0]
	for _, op := range selected {
		if op.Name() != drop.Name() {
			kept = append(kept, op)
		}
	}
	return kept
}

// Names returns the names of the given operators.
func Names(ops []mutant.Operator) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.Name())
	}
	return out
}
