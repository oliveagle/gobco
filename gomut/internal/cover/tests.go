package cover

import (
	"go/ast"
	"sort"
	"strings"
)

// TestFuncs lists the names of top-level test functions declared in the
// given (test) files, in sorted order.
//
// Only functions the "go test" runner will execute are listed: name
// starts with "Test", exactly one parameter, no receiver. TestMain is
// excluded (it is the runner entry point, not a test).
func TestFuncs(files []*ast.File) []string {
	var names []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			name := fd.Name.Name
			if name == "TestMain" || !strings.HasPrefix(name, "Test") {
				continue
			}
			if len(fd.Type.Params.List) != 1 {
				continue
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
