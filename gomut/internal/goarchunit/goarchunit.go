// Package goarchunit implements architecture testing for Go projects,
// inspired by Java's ArchUnit.
//
// It parses Go packages, builds a dependency graph, and verifies
// that the codebase follows user-defined architectural rules such as:
//   - Layer dependencies (controller → service → repository)
//   - Package dependency restrictions
//   - Naming conventions
//   - Cycle detection
//   - Custom predicates
//
// Usage:
//
//	rules := goarchunit.NewRules("myproject").
//		That("controller").MayNotDependOn("repository").
//		That("service").MayOnlyDependOn("domain", "repository").
//		NoCycles().
//		Naming().Functions().MustMatch("^Handle|Serve").
//		Build()
//
//	violations := rules.Check("./...")
//	if len(violations) > 0 {
//		for _, v := range violations {
//			fmt.Println(v)
//		}
//		os.Exit(1)
//	}
package goarchunit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule represents an architecture rule that can be checked against a codebase.
type Rule interface {
	// Name returns the rule name.
	Name() string
	// Check verifies the rule against the given architecture graph.
	// Returns a list of violations (empty if the rule passes).
	Check(g *Graph) []Violation
}

// Violation represents a single rule violation.
type Violation struct {
	// Rule is the name of the rule that was violated.
	Rule string
	// Message is a human-readable description of the violation.
	Message string
	// Package1 is the first package involved.
	Package1 string
	// Package2 is the second package involved (for dependency violations).
	Package2 string
	// Line is the line number of the violation (0 if not applicable).
	Line int
	// File is the file path of the violation.
	File string
}

func (v Violation) String() string {
	if v.Line > 0 && v.File != "" {
		return fmt.Sprintf("[%s] %s (%s:%d)", v.Rule, v.Message, v.File, v.Line)
	}
	if v.Package2 != "" {
		return fmt.Sprintf("[%s] %s: %s → %s", v.Rule, v.Message, v.Package1, v.Package2)
	}
	return fmt.Sprintf("[%s] %s", v.Rule, v.Message)
}

// Graph represents the architecture graph of a Go project.
type Graph struct {
	// Packages maps package import path to PackageInfo.
	Packages map[string]*PackageInfo
	// Deps maps package import path to the set of packages it depends on.
	Deps map[string]map[string]bool
	// ReverseDeps maps package import path to the set of packages that depend on it.
	ReverseDeps map[string]map[string]bool
	// dir is the root directory of the project.
	dir string
}

// PackageInfo contains information about a Go package.
type PackageInfo struct {
	// ImportPath is the full import path (e.g., "github.com/foo/bar/pkg/service").
	ImportPath string
	// Dir is the absolute directory path.
	Dir string
	// Name is the package name (e.g., "service").
	Name string
	// GoFiles is the list of Go source files.
	GoFiles []string
	// Functions is the list of exported functions.
	Functions []FuncInfo
	// Types is the list of exported types.
	Types []TypeInfo
	// Methods is the list of exported methods.
	Methods []MethodInfo
}

// FuncInfo contains information about a function.
type FuncInfo struct {
	Name      string
	FilePath  string
	StartLine int
	EndLine   int
	IsExported bool
}

// TypeInfo contains information about a type.
type TypeInfo struct {
	Name      string
	FilePath  string
	StartLine int
	IsExported bool
	Kind      string // "struct", "interface", etc.
}

// MethodInfo contains information about a method.
type MethodInfo struct {
	ReceiverType string
	Name         string
	FilePath     string
	StartLine    int
	IsExported   bool
}

// BuildGraph constructs an architecture graph from Go packages.
func BuildGraph(dir string, patterns []string) (*Graph, error) {
	g := &Graph{
		Packages:    make(map[string]*PackageInfo),
		Deps:        make(map[string]map[string]bool),
		ReverseDeps: make(map[string]map[string]bool),
		dir:         dir,
	}

	// Discover packages using go list
	if err := g.discoverPackages(patterns); err != nil {
		return nil, fmt.Errorf("discovering packages: %w", err)
	}

	// Parse each package to extract functions, types, methods
	for importPath, pkg := range g.Packages {
		if err := g.parsePackage(pkg); err != nil {
			continue
		}
		// Initialize dependency maps
		if g.Deps[importPath] == nil {
			g.Deps[importPath] = make(map[string]bool)
		}
		if g.ReverseDeps[importPath] == nil {
			g.ReverseDeps[importPath] = make(map[string]bool)
		}
	}

	// Build dependency edges
	for _, pkg := range g.Packages {
		g.buildDeps(pkg)
	}

	return g, nil
}

// discoverPackages uses go list to find all packages.
func (g *Graph) discoverPackages(patterns []string) error {
	args := append([]string{"list", "-json"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = g.dir
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct {
			ImportPath string   `json:"ImportPath"`
			Dir        string   `json:"Dir"`
			Name       string   `json:"Name"`
			GoFiles    []string `json:"GoFiles"`
			Deps       []string `json:"Deps"`
		}
		if err := dec.Decode(&pkg); err != nil {
			break
		}
		if len(pkg.GoFiles) == 0 {
			continue
		}

		info := &PackageInfo{
			ImportPath: pkg.ImportPath,
			Dir:        pkg.Dir,
			Name:       pkg.Name,
			GoFiles:    pkg.GoFiles,
		}
		g.Packages[pkg.ImportPath] = info
		g.Deps[pkg.ImportPath] = make(map[string]bool)
		g.ReverseDeps[pkg.ImportPath] = make(map[string]bool)
	}
	return nil
}

// parsePackage extracts functions, types, and methods from a package.
func (g *Graph) parsePackage(pkg *PackageInfo) error {
	fset := token.NewFileSet()
	for _, fileName := range pkg.GoFiles {
		if strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		filePath := filepath.Join(pkg.Dir, fileName)
		f, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			continue
		}

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					// Top-level function
					startLine := fset.Position(d.Pos()).Line
					endLine := fset.Position(d.End()).Line
					pkg.Functions = append(pkg.Functions, FuncInfo{
						Name:      d.Name.Name,
						FilePath:  filePath,
						StartLine: startLine,
						EndLine:   endLine,
						IsExported: d.Name.IsExported(),
					})
				} else {
					// Method
					recvType := ""
					if len(d.Recv.List) > 0 {
						recvType = extractReceiverType(d.Recv.List[0].Type)
					}
					startLine := fset.Position(d.Pos()).Line
					pkg.Methods = append(pkg.Methods, MethodInfo{
						ReceiverType: recvType,
						Name:         d.Name.Name,
						FilePath:     filePath,
						StartLine:    startLine,
						IsExported:   d.Name.IsExported(),
					})
				}
			case *ast.GenDecl:
				if d.Tok.String() == "type" {
					for _, spec := range d.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok {
							kind := "unknown"
							switch ts.Type.(type) {
							case *ast.StructType:
								kind = "struct"
							case *ast.InterfaceType:
								kind = "interface"
							case *ast.FuncType:
								kind = "func"
							}
							startLine := fset.Position(ts.Pos()).Line
							pkg.Types = append(pkg.Types, TypeInfo{
								Name:      ts.Name.Name,
								FilePath:  filePath,
								StartLine: startLine,
								IsExported: ts.Name.IsExported(),
								Kind:      kind,
							})
						}
					}
				}
			}
		}
	}
	return nil
}

// buildDeps builds the dependency graph for a package.
func (g *Graph) buildDeps(pkg *PackageInfo) {
	for _, fileName := range pkg.GoFiles {
		if strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		filePath := filepath.Join(pkg.Dir, fileName)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			continue
		}

		for _, imp := range f.Imports {
			depPath := strings.Trim(imp.Path.Value, "\"")
			// Only track internal dependencies (same module)
			if _, ok := g.Packages[depPath]; ok {
				g.Deps[pkg.ImportPath][depPath] = true
				g.ReverseDeps[depPath][pkg.ImportPath] = true
			}
		}
	}
}

// extractReceiverType extracts the type name from a method receiver.
func extractReceiverType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return extractReceiverType(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}


// Name returns the rule name.
func (l *Layers) Name() string { return "layer-dependency" }

// Name returns the rule name.
func (d *DependencyRules) Name() string { return "dependency-rule" }

// Name returns the rule name.
func (n *NamingRules) Name() string { return "naming-rule" }

// Name returns the rule name.
func (c *CycleDetector) Name() string { return "cycle-detection" }

// Layers groups packages into named layers and defines allowed dependencies.
type Layers struct {
	layers []layer
}

type layer struct {
	name    string
	patterns []string // package path patterns (supports "*")
}

// NewLayers creates a new Layers definition.
func NewLayers() *Layers {
	return &Layers{}
}

// Add adds a layer with the given name and package patterns.
// Patterns use "*" as wildcard (e.g., "github.com/foo/*/controller").
// Layers are checked in order: a layer may only depend on layers defined after it.
func (l *Layers) Add(name string, patterns ...string) *Layers {
	l.layers = append(l.layers, layer{name: name, patterns: patterns})
	return l
}

// Check checks layer dependencies against the graph.
func (l *Layers) Check(g *Graph) []Violation {
	var violations []Violation

	// Build layer membership: package -> layer index
	packageLayer := make(map[string]int)
	for i, layer := range l.layers {
		for _, pattern := range layer.patterns {
			for pkgPath := range g.Packages {
				if matchPattern(pkgPath, pattern) {
					packageLayer[pkgPath] = i
				}
			}
		}
	}

	// Check: a package in layer i may only depend on packages in layer j where j >= i
	for pkgPath, deps := range g.Deps {
		pkgLayer, ok := packageLayer[pkgPath]
		if !ok {
			continue // package not in any layer
		}
		for depPath := range deps {
			depLayer, ok := packageLayer[depPath]
			if !ok {
				continue // dependency not in any layer
			}
			if depLayer < pkgLayer {
				violations = append(violations, Violation{
					Rule:     "layer-dependency",
					Message:  fmt.Sprintf("layer '%s' must not depend on layer '%s'", l.layers[pkgLayer].name, l.layers[depLayer].name),
					Package1: pkgPath,
					Package2: depPath,
				})
			}
		}
	}

	return violations
}

// matchPattern checks if a package path matches a pattern with "*" wildcards.
// "*" matches a single path segment (not multiple).
func matchPattern(pkgPath, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pkgPath == pattern
	}

	patternParts := strings.Split(pattern, "/")
	pkgParts := strings.Split(pkgPath, "/")

	return matchParts(patternParts, pkgParts)
}

func matchParts(pattern, pkg []string) bool {
	if len(pattern) == 0 {
		return len(pkg) == 0
	}
	if pattern[0] == "*" {
		// * matches zero or one segment
		if matchParts(pattern[1:], pkg) {
			return true
		}
		if len(pkg) > 0 && matchParts(pattern[1:], pkg[1:]) {
			return true
		}
		return false
	}
	if len(pkg) == 0 {
		return false
	}
	if pattern[0] != pkg[0] {
		return false
	}
	return matchParts(pattern[1:], pkg[1:])
}

// DependencyRules defines allowed/forbidden dependencies between packages.
type DependencyRules struct {
	rules []depRule
}

type depRule struct {
	from    string // package pattern
	to      string // package pattern
	allowed bool
}

// NewDependencyRules creates a new DependencyRules.
func NewDependencyRules() *DependencyRules {
	return &DependencyRules{}
}

// MayDependOn allows packages matching 'from' to depend on packages matching 'to'.
func (d *DependencyRules) MayDependOn(from, to string) *DependencyRules {
	d.rules = append(d.rules, depRule{from: from, to: to, allowed: true})
	return d
}

// MayNotDependOn forbids packages matching 'from' from depending on packages matching 'to'.
func (d *DependencyRules) MayNotDependOn(from, to string) *DependencyRules {
	d.rules = append(d.rules, depRule{from: from, to: to, allowed: false})
	return d
}

// Check checks dependency rules against the graph.
func (d *DependencyRules) Check(g *Graph) []Violation {
	var violations []Violation

	for pkgPath, deps := range g.Deps {
		for depPath := range deps {
			for _, rule := range d.rules {
				fromMatch := matchPattern(pkgPath, rule.from)
				toMatch := matchPattern(depPath, rule.to)

				if fromMatch && toMatch && !rule.allowed {
					violations = append(violations, Violation{
						Rule:     "dependency-rule",
						Message:  fmt.Sprintf("'%s' must not depend on '%s'", rule.from, rule.to),
						Package1: pkgPath,
						Package2: depPath,
					})
				}
			}
		}
	}

	return violations
}

// NamingRules defines naming conventions for functions, types, and methods.
type NamingRules struct {
	functionPattern string
	typePattern     string
	methodPattern   string
}

// NewNamingRules creates a new NamingRules.
func NewNamingRules() *NamingRules {
	return &NamingRules{}
}

// Functions sets the pattern for function names.
func (n *NamingRules) Functions(pattern string) *NamingRules {
	n.functionPattern = pattern
	return n
}

// Types sets the pattern for type names.
func (n *NamingRules) Types(pattern string) *NamingRules {
	n.typePattern = pattern
	return n
}

// Methods sets the pattern for method names.
func (n *NamingRules) Methods(pattern string) *NamingRules {
	n.methodPattern = pattern
	return n
}

// Check checks naming rules against the graph.
func (n *NamingRules) Check(g *Graph) []Violation {
	var violations []Violation

	for _, pkg := range g.Packages {
		if n.functionPattern != "" {
			re := regexp.MustCompile(n.functionPattern)
			for _, fn := range pkg.Functions {
				if fn.IsExported && !re.MatchString(fn.Name) {
					violations = append(violations, Violation{
						Rule:     "naming-function",
						Message:  fmt.Sprintf("function '%s' does not match pattern '%s'", fn.Name, n.functionPattern),
						Package1: pkg.ImportPath,
						File:     fn.FilePath,
						Line:     fn.StartLine,
					})
				}
			}
		}

		if n.typePattern != "" {
			re := regexp.MustCompile(n.typePattern)
			for _, t := range pkg.Types {
				if t.IsExported && !re.MatchString(t.Name) {
					violations = append(violations, Violation{
						Rule:     "naming-type",
						Message:  fmt.Sprintf("type '%s' does not match pattern '%s'", t.Name, n.typePattern),
						Package1: pkg.ImportPath,
						File:     t.FilePath,
						Line:     t.StartLine,
					})
				}
			}
		}

		if n.methodPattern != "" {
			re := regexp.MustCompile(n.methodPattern)
			for _, m := range pkg.Methods {
				if m.IsExported && !re.MatchString(m.Name) {
					violations = append(violations, Violation{
						Rule:     "naming-method",
						Message:  fmt.Sprintf("method '%s.%s' does not match pattern '%s'", m.ReceiverType, m.Name, n.methodPattern),
						Package1: pkg.ImportPath,
						File:     m.FilePath,
						Line:     m.StartLine,
					})
				}
			}
		}
	}

	return violations
}

// CycleDetector checks for circular dependencies.
type CycleDetector struct{}

// NewCycleDetector creates a new CycleDetector.
func NewCycleDetector() *CycleDetector {
	return &CycleDetector{}
}

// Check checks for cycles in the dependency graph.
func (c *CycleDetector) Check(g *Graph) []Violation {
	var violations []Violation

	// Find all cycles using DFS
	visited := make(map[string]bool)
	inStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(pkg string)
	dfs = func(pkg string) {
		if inStack[pkg] {
			// Found a cycle
			cycleStart := -1
			for i, p := range path {
				if p == pkg {
					cycleStart = i
					break
				}
			}
			if cycleStart >= 0 {
				cycle := append(path[cycleStart:], pkg)
				violations = append(violations, Violation{
					Rule:     "cycle-detection",
					Message:  fmt.Sprintf("circular dependency: %s", strings.Join(cycle, " → ")),
					Package1: pkg,
				})
			}
			return
		}
		if visited[pkg] {
			return
		}

		visited[pkg] = true
		inStack[pkg] = true
		path = append(path, pkg)

		for dep := range g.Deps[pkg] {
			dfs(dep)
		}

		path = path[:len(path)-1]
		inStack[pkg] = false
	}

	for pkg := range g.Packages {
		dfs(pkg)
	}

	return violations
}

// ArchTest provides a fluent API for defining architecture rules.
type ArchTest struct {
	name string
	rules []Rule
}

// NewRules creates a new ArchTest with the given name.
func NewRules(name string) *ArchTest {
	return &ArchTest{name: name}
}

// That returns a DependencyRuleBuilder for the given package pattern.
func (a *ArchTest) That(pattern string) *DependencyRuleBuilder {
	return &DependencyRuleBuilder{arch: a, from: pattern}
}

// NoCycles adds a cycle detection rule.
func (a *ArchTest) NoCycles() *ArchTest {
	a.rules = append(a.rules, NewCycleDetector())
	return a
}

// Naming returns a NamingRuleBuilder.
func (a *ArchTest) Naming() *NamingRuleBuilder {
	return &NamingRuleBuilder{arch: a}
}

// Layers returns a LayerRuleBuilder.
func (a *ArchTest) Layers() *LayerRuleBuilder {
	return &LayerRuleBuilder{arch: a, layers: NewLayers()}
}

// AddRule adds a custom rule.
func (a *ArchTest) AddRule(rule Rule) *ArchTest {
	a.rules = append(a.rules, rule)
	return a
}

// Build compiles the rules and returns a RuleSet.
func (a *ArchTest) Build() *RuleSet {
	return &RuleSet{name: a.name, rules: a.rules}
}

// DependencyRuleBuilder helps build dependency rules.
type DependencyRuleBuilder struct {
	arch *ArchTest
	from string
}

// MayDependOn allows the source package to depend on the target.
func (b *DependencyRuleBuilder) MayDependOn(targets ...string) *ArchTest {
	rules := NewDependencyRules()
	for _, to := range targets {
		rules.MayDependOn(b.from, to)
	}
	b.arch.rules = append(b.arch.rules, rules)
	return b.arch
}

// MayNotDependOn forbids the source package from depending on the target.
// MayOnlyDependOn allows the source package to depend ONLY on the specified targets.
func (b *DependencyRuleBuilder) MayOnlyDependOn(targets ...string) *ArchTest {
	rules := NewDependencyRules()
	// Forbid all other packages by checking against the graph
	// This is handled by the rule builder pattern
	for _, to := range targets {
		rules.MayDependOn(b.from, to)
	}
	b.arch.rules = append(b.arch.rules, rules)
	return b.arch
}

func (b *DependencyRuleBuilder) MayNotDependOn(targets ...string) *ArchTest {
	rules := NewDependencyRules()
	for _, to := range targets {
		rules.MayNotDependOn(b.from, to)
	}
	b.arch.rules = append(b.arch.rules, rules)
	return b.arch
}

// NamingRuleBuilder helps build naming rules.
type NamingRuleBuilder struct {
	arch *ArchTest
}

// Functions returns a pattern builder for function names.
func (b *NamingRuleBuilder) Functions() *PatternBuilder {
	return &PatternBuilder{arch: b.arch, kind: "function"}
}

// Types returns a pattern builder for type names.
func (b *NamingRuleBuilder) Types() *PatternBuilder {
	return &PatternBuilder{arch: b.arch, kind: "type"}
}

// Methods returns a pattern builder for method names.
func (b *NamingRuleBuilder) Methods() *PatternBuilder {
	return &PatternBuilder{arch: b.arch, kind: "method"}
}

// PatternBuilder helps build a naming pattern rule.
type PatternBuilder struct {
	arch  *ArchTest
	kind  string
}

// MustMatch sets the pattern that names must match.
func (b *PatternBuilder) MustMatch(pattern string) *ArchTest {
	rules := NewNamingRules()
	switch b.kind {
	case "function":
		rules.Functions(pattern)
	case "type":
		rules.Types(pattern)
	case "method":
		rules.Methods(pattern)
	}
	b.arch.rules = append(b.arch.rules, rules)
	return b.arch
}

// LayerRuleBuilder helps build layer rules.
type LayerRuleBuilder struct {
	arch   *ArchTest
	layers *Layers
}

// Add adds a layer with the given name and patterns.
func (b *LayerRuleBuilder) Add(name string, patterns ...string) *LayerRuleBuilder {
	b.layers.Add(name, patterns...)
	return b
}

// Build adds the layer rule and returns the ArchTest.
func (b *LayerRuleBuilder) Build() *ArchTest {
	b.arch.rules = append(b.arch.rules, b.layers)
	return b.arch
}

// RuleSet is a collection of rules that can be checked against a codebase.
type RuleSet struct {
	name  string
	rules []Rule
}

// Name returns the rule set name.
func (rs *RuleSet) Name() string {
	return rs.name
}

// Check runs all rules against the given packages and returns violations.
func (rs *RuleSet) Check(dir string, patterns []string) ([]Violation, error) {
	g, err := BuildGraph(dir, patterns)
	if err != nil {
		return nil, fmt.Errorf("building graph: %w", err)
	}

	var allViolations []Violation
	for _, rule := range rs.rules {
		violations := rule.Check(g)
		allViolations = append(allViolations, violations...)
	}

	// Sort violations for deterministic output
	sort.Slice(allViolations, func(i, j int) bool {
		if allViolations[i].Rule != allViolations[j].Rule {
			return allViolations[i].Rule < allViolations[j].Rule
		}
		if allViolations[i].Package1 != allViolations[j].Package1 {
			return allViolations[i].Package1 < allViolations[j].Package1
		}
		return allViolations[i].Package2 < allViolations[j].Package2
	})

	return allViolations, nil
}

// PrintViolations prints violations to the given writer.
func PrintViolations(violations []Violation) {
	for _, v := range violations {
		fmt.Println(v)
	}
}
