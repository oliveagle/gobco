package mutate

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"

	"github.com/oliveagle/gobco/gomut/internal/mutant"
)

// SiteInfo binds one mutation patch to the runtime case id used by the
// injected switch (GOMUT_MUTANT=<CaseID> activates the patch).
//
// The injector consumes a file's SiteInfo slice sorted by Patch.Start so
// the relative offsets stay stable while applying each patch to the
// function body copy.
type SiteInfo struct {
	Patch  mutant.Patch
	CaseID string
}

// GenerateInjected returns a copy of src in which every top-level function
// that contains at least one site has its body wrapped in a switch keyed
// on gomutPick(); each case carries the original body with that patch
// applied, and the default case is the unchanged original body. Functions
// without any site are left byte-identical.
//
// The result, compiled together with a package-level helper that reads
// GOMUT_MUTANT from the environment, gives runtime-injection mutation
// testing: a single `go test -c` produces a binary that runs every mutant
// by setting the env var, eliminating the per-mutant `go test -overlay`
// compile that dominates cold-run wall time.
func GenerateInjected(src string, fset *token.FileSet, file *ast.File, sites []SiteInfo) (string, error) {
	if len(sites) == 0 {
		return src, nil
	}
	sorted := append([]SiteInfo(nil), sites...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Patch.Start < sorted[j].Patch.Start })

	type funcHit struct {
		decl  *ast.FuncDecl
		hit   []SiteInfo // sites inside this function, in Patch.Start order
	}
	hits := map[*ast.FuncDecl][]SiteInfo{}

	for _, s := range sorted {
		fd := enclosingFuncDecl(file, fset, s.Patch.Start)
		if fd == nil {
			// site is at package/var level; only valid for whole-file
			// decls like const var init — operators don't emit those,
			// so this is defensive.
			return "", fmt.Errorf("site at offset %d has no enclosing FuncDecl", s.Patch.Start)
		}
		hits[fd] = append(hits[fd], s)
	}

	// Collect the replacements (offset ranges in src to substitute) and
	// apply them in DESCENDING order so earlier offsets stay valid.
	type repl struct {
		start, end int
		text       string
	}
	var repls []repl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		hs := hits[fd]
		if len(hs) == 0 {
			continue
		}
		newText, err := wrapFuncBody(src, fset, fd, hs)
		if err != nil {
			return "", err
		}
		start := fset.Position(fd.Pos()).Offset
		end := fset.Position(fd.End()).Offset
		repls = append(repls, repl{start: start, end: end, text: newText})
	}
	sort.Slice(repls, func(i, j int) bool { return repls[i].start > repls[j].start })

	out := src
	for _, r := range repls {
		if r.start < 0 || r.end > len(out) || r.start > r.end {
			return "", fmt.Errorf("replacement range [%d,%d) out of bounds", r.start, r.end)
		}
		out = out[:r.start] + r.text + out[r.end:]
	}
	return out, nil
}

// enclosingFuncDecl returns the FuncDecl whose byte range contains off.
// Returns nil for package-level decls (var/const/type). FuncLits are
// ignored on purpose — they live inside their enclosing FuncDecl's body
// and are covered by wrapping that FuncDecl.
func enclosingFuncDecl(file *ast.File, fset *token.FileSet, off int) *ast.FuncDecl {
	var found *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		s := fset.Position(fd.Pos()).Offset
		e := fset.Position(fd.End()).Offset
		if off >= s && off < e {
			found = fd
		}
	}
	return found
}

// wrapFuncBody rewrites one function declaration: keep the signature,
// replace its `{ body }` with `{ switch gomutPick() { case "<id>": body+patch
// ... default: body } }`.
func wrapFuncBody(src string, fset *token.FileSet, fd *ast.FuncDecl, sites []SiteInfo) (string, error) {
	lb := fset.Position(fd.Body.Lbrace).Offset
	rb := fset.Position(fd.Body.Rbrace).Offset
	sig := src[fset.Position(fd.Pos()).Offset:lb]
	body := src[lb+1 : rb]

	// Build case bodies: each is `body` with the site's patch applied.
	// Sites are sorted by Patch.Start (caller's contract).
	cases := make([]string, 0, len(sites)+1)
	for _, s := range sites {
		relStart := s.Patch.Start - (lb + 1)
		relEnd := s.Patch.End - (lb + 1)
		if relStart < 0 || relEnd > len(body) || relStart > relEnd {
			return "", fmt.Errorf("site offset outside function body")
		}
		caseBody := body[:relStart] + s.Patch.Replace + body[relEnd:]
		cases = append(cases, fmt.Sprintf("\tcase %q:\n%s", s.CaseID, indent(caseBody, "\t\t")))
	}

	switchText := "switch gomutPick() {\n"
	switchText += joinLines(cases)
	switchText += fmt.Sprintf("\tdefault:\n%s\n", indent(body, "\t\t"))
	switchText += "\t}"

	// Guarantee the closing brace of the function is on its own line so
	// a body that ends with a // comment without a trailing newline
	// cannot swallow the brace.
	return sig + " {\n" + switchText + "\n}\n", nil
}

// indent prefixes every line of s with prefix. Empty lines stay empty.
func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	out := ""
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		line := s[i:j]
		if line != "" {
			out += prefix + line
		}
		if j < len(s) {
			out += "\n"
			j++
		}
		i = j
	}
	return out
}

func joinLines(parts []string) string {
	out := ""
	for _, p := range parts {
		out += p + "\n"
	}
	return out
}

// PickSource returns the helper source that gomut will inject into the
// package: a single function reading GOMUT_MUTANT from the environment.
// pkgName must be the package being mutated (the helper lives in it).
// It must be added to the package (via overlay) before the injected
// production files reference it.
func PickSource(pkgName string) string {
	return fmt.Sprintf(`// auto-generated by gomut; do not edit.
package %s

import "os"

func gomutPick() string { return os.Getenv("GOMUT_MUTANT") }
`, pkgName)
}
