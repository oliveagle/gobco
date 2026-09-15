// Package impact implements impact analysis for incremental mutation testing.
//
// It builds a call graph from Go AST analysis, computes the impact domain
// of code changes (forward and backward propagation), and selects the minimal
// set of tests that exercise the changed code paths.
//
// The analysis workflow:
//  1. Parse git diff to identify changed files and line ranges
//  2. Build call graph from AST (who calls whom) - including cross-package
//  3. Compute impact domain: changed code + all transitive callers/callees
//  4. Select tests that cover any line in the impact domain
//  5. Only run mutants in the impact domain
package impact

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Change represents a code change from git diff.
type Change struct {
	// File is the relative path to the changed file.
	File string
	// AddedLines are the 1-based line numbers that were added/modified.
	AddedLines []int
	// RemovedLines are the 1-based line numbers that were removed.
	RemovedLines []int
}

// FuncNode represents a function in the call graph.
type FuncNode struct {
	// Name is the function name (e.g. "Foo" or "(*Bar).Baz").
	Name string
	// Package is the package import path.
	Package string
	// File is the source file path.
	File string
	// StartLine and EndLine are the 1-based line numbers.
	StartLine, EndLine int
	// Callees are the functions called by this function.
	Callees []*FuncNode
	// Callers are the functions that call this function.
	Callers []*FuncNode
	// IsExported indicates if the function is exported (capitalized).
	IsExported bool
	// ReceiverType is the receiver type name for methods (empty for functions).
	ReceiverType string
	// IsMethod indicates if this is a method (has a receiver).
	IsMethod bool
}

// InterfaceInfo tracks interface definitions and their implementations.
type InterfaceInfo struct {
	// Name is the interface type name.
	Name string
	// Package is the package import path.
	Package string
	// Methods is the set of method names in the interface.
	Methods map[string]bool
	// Implementations maps concrete type names to their packages.
	Implementations map[string]string
}

// CallGraph represents the dependency graph of functions.
type CallGraph struct {
	// Funcs maps function key to FuncNode.
	Funcs map[string]*FuncNode
	// ByFile maps file path to function nodes in that file.
	ByFile map[string][]*FuncNode
	// ByPackage maps package import path to function keys.
	ByPackage map[string][]string
	// Interfaces tracks interface definitions and implementations.
	Interfaces map[string]*InterfaceInfo
	// InterfaceImpls maps interface method calls to possible implementations.
	InterfaceImpls map[string][]string
	// pendingCalls temporarily stores call relationships before linking.
	pendingCalls []callInfo
	// modulePath is the Go module path for this project.
	modulePath string
	// pkgDirs maps package import paths to their directories.
	pkgDirs map[string]string
	// pkgImports maps package directories to their import paths.
	pkgImports map[string]string
}

// callInfo temporarily stores call relationships before linking.
type callInfo struct {
	callerKey  string
	calleeKeys []string
}

// FuncKey creates a unique key for a function.
func FuncKey(pkg, name string) string {
	return pkg + ":" + name
}

// BuildCallGraph constructs a call graph from Go source files.
// It discovers packages using go list and resolves cross-package calls.
func BuildCallGraph(dir string, packages []string) (*CallGraph, error) {
	cg := &CallGraph{
		Funcs:          make(map[string]*FuncNode),
		ByFile:         make(map[string][]*FuncNode),
		ByPackage:      make(map[string][]string),
		Interfaces:     make(map[string]*InterfaceInfo),
		InterfaceImpls: make(map[string][]string),
		pkgDirs:        make(map[string]string),
		pkgImports:     make(map[string]string),
	}

	// Discover module path
	if err := cg.discoverModule(dir); err != nil {
		// Fallback: use dir as module path
		cg.modulePath = filepath.Base(dir)
	}

	// Discover all packages in the project
	if err := cg.discoverPackages(dir); err != nil {
		// Fallback: use provided packages
		for _, pkg := range packages {
			pkgDir := filepath.Join(dir, pkg)
			cg.pkgDirs[pkg] = pkgDir
			cg.pkgImports[pkgDir] = pkg
		}
	}

	// Parse all packages to build function declarations and interface info
	for pkgPath, pkgDir := range cg.pkgDirs {
		if err := cg.parsePackage(pkgDir, pkgPath); err != nil {
			continue // skip packages that can't be parsed
		}
	}

	// Parse test files to detect cross-package calls
	for pkgPath, pkgDir := range cg.pkgDirs {
		if err := cg.parseTestFiles(pkgDir, pkgPath); err != nil {
			continue
		}
	}

	// Link callees and callers
	cg.linkCalls()

	// Resolve interface implementations
	cg.resolveInterfaceImplementations()

	return cg, nil
}

// discoverModule finds the Go module path from go.mod.
func (cg *CallGraph) discoverModule(dir string) error {
	cmd := exec.Command("go", "list", "-m", "-json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	var mod struct {
		Path string `json:"Path"`
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return err
	}

	cg.modulePath = mod.Path
	return nil
}

// discoverPackages uses go list to find all packages in the project.
func (cg *CallGraph) discoverPackages(dir string) error {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	// Parse concatenated JSON objects
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct {
			ImportPath string   `json:"ImportPath"`
			Dir        string   `json:"Dir"`
			GoFiles    []string `json:"GoFiles"`
		}
		if err := dec.Decode(&pkg); err != nil {
			break
		}

		if len(pkg.GoFiles) == 0 {
			continue // skip packages with no Go files
		}

		cg.pkgDirs[pkg.ImportPath] = pkg.Dir
		cg.pkgImports[pkg.Dir] = pkg.ImportPath
	}

	return nil
}

// parsePackage parses all Go files in a package and extracts function declarations.
func (cg *CallGraph) parsePackage(pkgDir, pkgPath string) error {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		filePath := filepath.Join(pkgDir, entry.Name())
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			continue
		}

		cg.parseFile(fset, f, filePath, pkgPath)
	}

	return nil
}

// parseTestFiles parses test files to detect cross-package calls.
func (cg *CallGraph) parseTestFiles(pkgDir, pkgPath string) error {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		filePath := filepath.Join(pkgDir, entry.Name())
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			continue
		}

		// Parse imports to map package aliases to import paths
		importMap := cg.buildImportMap(f)

		// Parse function declarations in test files
		cg.parseTestFileFuncs(fset, f, filePath, pkgPath, importMap)
	}

	return nil
}

// buildImportMap creates a mapping from package aliases to import paths.
func (cg *CallGraph) buildImportMap(f *ast.File) map[string]string {
	importMap := make(map[string]string)

	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "\"")
		var alias string
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			// Use last element of import path as default alias
			parts := strings.Split(path, "/")
			alias = parts[len(parts)-1]
		}
		importMap[alias] = path
	}

	return importMap
}

// parseTestFileFuncs extracts function calls from test files.
func (cg *CallGraph) parseTestFileFuncs(fset *token.FileSet, f *ast.File, filePath, pkgPath string, importMap map[string]string) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		callerName := fn.Name.Name
		callerKey := FuncKey(pkgPath, callerName)

		// Find all calls in the function body
		var calls []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			callExpr, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			calleeName := resolveCallNameWithImports(callExpr.Fun, pkgPath, importMap)
			if calleeName != "" {
				calls = append(calls, calleeName)
			}
			return true
		})

		// Store calls for linking
		cg.storeCalls(callerKey, calls)
	}
}

// parseFile extracts function declarations and call expressions from a file.
func (cg *CallGraph) parseFile(fset *token.FileSet, f *ast.File, filePath, pkgPath string) {
	// First pass: collect all function declarations
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		name := fn.Name.Name
		isExported := fn.Name.IsExported()
		var recvType string
		isMethod := false

		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType = recvTypeName(fn.Recv.List[0].Type)
			if recvType != "" {
				name = "(" + recvType + ")." + name
				isMethod = true
			}
		}

		key := FuncKey(pkgPath, name)
		startLine := fset.Position(fn.Pos()).Line
		endLine := fset.Position(fn.End()).Line

		node := &FuncNode{
			Name:         name,
			Package:      pkgPath,
			File:         filePath,
			StartLine:    startLine,
			EndLine:      endLine,
			IsExported:   isExported,
			ReceiverType: recvType,
			IsMethod:     isMethod,
		}

		cg.Funcs[key] = node
		cg.ByFile[filePath] = append(cg.ByFile[filePath], node)
		cg.ByPackage[pkgPath] = append(cg.ByPackage[pkgPath], key)
	}

	// Parse imports for cross-package call resolution
	importMap := cg.buildImportMap(f)

	// Second pass: collect call expressions
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		callerName := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType := recvTypeName(fn.Recv.List[0].Type)
			if recvType != "" {
				callerName = "(" + recvType + ")." + callerName
			}
		}
		callerKey := FuncKey(pkgPath, callerName)

		// Find all calls in the function body
		var calls []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			callExpr, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			calleeName := resolveCallNameWithImports(callExpr.Fun, pkgPath, importMap)
			if calleeName != "" {
				calls = append(calls, calleeName)
			}
			return true
		})

		// Store calls for linking
		cg.storeCalls(callerKey, calls)
	}

	// Track interface definitions
	for _, decl := range f.Decls {
		ts, ok := decl.(*ast.GenDecl)
		if !ok || ts.Tok != token.TYPE {
			continue
		}

		for _, spec := range ts.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}

			// Track interface definition
			ifaceInfo := &InterfaceInfo{
				Name:            typeSpec.Name.Name,
				Package:         pkgPath,
				Methods:         make(map[string]bool),
				Implementations: make(map[string]string),
			}

			for _, method := range iface.Methods.List {
				// For interface methods, we need to get the method name from the field names
				for _, name := range method.Names {
					ifaceInfo.Methods[name.Name] = true
					
					// Create a function node for the interface method
					methodKey := FuncKey(pkgPath, typeSpec.Name.Name+"."+name.Name)
					startLine := fset.Position(method.Pos()).Line
					endLine := fset.Position(method.End()).Line
					methodNode := &FuncNode{
						Name:      typeSpec.Name.Name + "." + name.Name,
						Package:   pkgPath,
						File:      filePath,
						StartLine: startLine,
						EndLine:   endLine,
						IsExported: name.IsExported(),
					}
					cg.Funcs[methodKey] = methodNode
					cg.ByFile[filePath] = append(cg.ByFile[filePath], methodNode)
					cg.ByPackage[pkgPath] = append(cg.ByPackage[pkgPath], methodKey)
				}
			}

			cg.Interfaces[FuncKey(pkgPath, typeSpec.Name.Name)] = ifaceInfo
		}
	}
}

// storeCalls temporarily stores call relationships.
func (cg *CallGraph) storeCalls(callerKey string, calleeKeys []string) {
	cg.pendingCalls = append(cg.pendingCalls, callInfo{callerKey, calleeKeys})
}

// linkCalls connects callers and callees in the call graph.
func (cg *CallGraph) linkCalls() {
	for _, info := range cg.pendingCalls {
		caller, ok := cg.Funcs[info.callerKey]
		if !ok {
			continue
		}

		for _, calleeKey := range info.calleeKeys {
			callee, ok := cg.Funcs[calleeKey]
			if !ok {
				// Check if it's an interface method call
				if cg.tryLinkInterfaceMethod(caller, calleeKey) {
					continue
				}
				continue
			}

			// Add to callees (avoid duplicates)
			if !containsFunc(caller.Callees, callee) {
				caller.Callees = append(caller.Callees, callee)
			}

			// Add to callers (avoid duplicates)
			if !containsFunc(callee.Callers, caller) {
				callee.Callers = append(callee.Callers, caller)
			}
		}
	}
	cg.pendingCalls = nil
}

// tryLinkInterfaceMethod attempts to link an interface method call to implementations.
func (cg *CallGraph) tryLinkInterfaceMethod(caller *FuncNode, calleeKey string) bool {
	// Parse the callee key to extract method name
	parts := strings.SplitN(calleeKey, ":", 2)
	if len(parts) != 2 {
		return false
	}

	// Check if the method name contains a type assertion pattern
	// e.g., "io:Read" or "pkg:Interface.Method"
	methodName := parts[1]

	// Look for interfaces that have this method
	for _, iface := range cg.Interfaces {
		if !iface.Methods[methodName] {
			continue
		}

		// Find implementations of this interface
		for implType, implPkg := range iface.Implementations {
			implKey := FuncKey(implPkg, "("+implType+")."+methodName)
			if impl, ok := cg.Funcs[implKey]; ok {
				if !containsFunc(caller.Callees, impl) {
					caller.Callees = append(caller.Callees, impl)
				}
				if !containsFunc(impl.Callers, caller) {
					impl.Callers = append(impl.Callers, caller)
				}
				return true
			}
		}
	}

	return false
}

// resolveInterfaceImplementations links interfaces to their implementations.
func (cg *CallGraph) resolveInterfaceImplementations() {
	// Build a map of method sets for concrete types
	methodSets := make(map[string]map[string]string) // typeKey -> methodName -> funcKey

	for key, node := range cg.Funcs {
		if node.IsMethod && node.ReceiverType != "" {
			typeKey := FuncKey(node.Package, node.ReceiverType)
			if methodSets[typeKey] == nil {
				methodSets[typeKey] = make(map[string]string)
			}
			// Extract method name from "(Type).Method" format
			methodName := node.Name
			if idx := strings.LastIndex(methodName, "."); idx >= 0 {
				methodName = methodName[idx+1:]
			}
			methodSets[typeKey][methodName] = key
		}
	}

	// For each interface, find concrete types that implement all its methods
	for _, iface := range cg.Interfaces {
		for typeKey, methods := range methodSets {
			typeParts := strings.SplitN(typeKey, ":", 2)
			if len(typeParts) != 2 {
				continue
			}
			typePkg := typeParts[0]
			typeName := typeParts[1]

			// Check if this type implements all interface methods
			implementsAll := true
			for methodName := range iface.Methods {
				if _, ok := methods[methodName]; !ok {
					implementsAll = false
					break
				}
			}

			if implementsAll {
				iface.Implementations[typeName] = typePkg

				// Also update the interface implementations map
				for methodName := range iface.Methods {
					if funcKey, ok := methods[methodName]; ok {
						ifaceMethodKey := FuncKey(iface.Package, iface.Name+"."+methodName)
						cg.InterfaceImpls[ifaceMethodKey] = append(cg.InterfaceImpls[ifaceMethodKey], funcKey)
					}
				}
			}
		}
	}
}

// containsFunc checks if a FuncNode slice contains a specific node.
func containsFunc(nodes []*FuncNode, target *FuncNode) bool {
	for _, n := range nodes {
		if n == target {
			return true
		}
	}
	return false
}

// recvTypeName extracts the type name from a function receiver.
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// resolveCallNameWithImports resolves a call expression to a function key,
// using import information for cross-package calls.
func resolveCallNameWithImports(fun ast.Expr, currentPkg string, importMap map[string]string) string {
	switch t := fun.(type) {
	case *ast.Ident:
		// Direct function call in same package
		return FuncKey(currentPkg, t.Name)

	case *ast.SelectorExpr:
		// Method call or package-qualified call
		if ident, ok := t.X.(*ast.Ident); ok {
			// Check if it's a package qualifier
			if importPath, ok := importMap[ident.Name]; ok {
				// Package-qualified call: pkg.Func()
				return FuncKey(importPath, t.Sel.Name)
			}

			// Method call on a variable - try to infer type
			// For now, assume it's in the same package
			return FuncKey(currentPkg, t.Sel.Name)
		}

		// Handle chained selectors like a.b.Func()
		if selExpr, ok := t.X.(*ast.SelectorExpr); ok {
			if ident, ok := selExpr.X.(*ast.Ident); ok {
				if importPath, ok := importMap[ident.Name]; ok {
					return FuncKey(importPath, t.Sel.Name)
				}
			}
		}
	}
	return ""
}

// resolveCallName resolves a call expression to a function key (legacy interface).
func resolveCallName(fun ast.Expr, currentPkg string) string {
	return resolveCallNameWithImports(fun, currentPkg, nil)
}

// isPkgName checks if a name looks like a package name (not a type).
func isPkgName(name string) bool {
	// Heuristic: package names are lowercase
	return len(name) > 0 && name[0] >= 'a' && name[0] <= 'z'
}
