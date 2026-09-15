package impact

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestComputeDomain(t *testing.T) {
	// Create a test call graph manually
	cg := &CallGraph{
		Funcs:          make(map[string]*FuncNode),
		ByFile:         make(map[string][]*FuncNode),
		ByPackage:      make(map[string][]string),
		Interfaces:     make(map[string]*InterfaceInfo),
		InterfaceImpls: make(map[string][]string),
		pkgDirs:        make(map[string]string),
		pkgImports:     make(map[string]string),
	}

	// Create functions: A -> B -> C
	a := &FuncNode{Name: "A", Package: "pkg", File: "b.go", StartLine: 1, EndLine: 10}
	b := &FuncNode{Name: "B", Package: "pkg", File: "b.go", StartLine: 20, EndLine: 30}
	c := &FuncNode{Name: "C", Package: "pkg", File: "b.go", StartLine: 40, EndLine: 50}

	cg.Funcs[FuncKey("pkg", "A")] = a
	cg.Funcs[FuncKey("pkg", "B")] = b
	cg.Funcs[FuncKey("pkg", "C")] = c

	// Add to ByFile
	cg.ByFile["b.go"] = []*FuncNode{a, b, c}

	// Manually link: A calls B, B calls C
	a.Callees = []*FuncNode{b}
	b.Callees = []*FuncNode{c}
	b.Callers = []*FuncNode{a}
	c.Callers = []*FuncNode{b}

	// Test: change B
	changes := []Change{
		{File: "b.go", AddedLines: []int{25}},
	}

	domain := ComputeDomain(cg, changes)

	t.Logf("Changed: %v", domain.Changed)
	t.Logf("Callees: %v", domain.Callees)
	t.Logf("Callers: %v", domain.Callers)

	// B should be in changed
	if !domain.Changed[FuncKey("pkg", "B")] {
		t.Error("B should be in changed")
	}

	// C should be in forward (B calls C)
	if !domain.Callees[FuncKey("pkg", "C")] {
		t.Error("C should be in forward (B calls C)")
	}

	// A should be in backward (A calls B)
	if !domain.Callers[FuncKey("pkg", "A")] {
		t.Error("A should be in backward (A calls B)")
	}
}

func TestFuncKey(t *testing.T) {
	key := FuncKey("pkg", "Func")
	if key != "pkg:Func" {
		t.Errorf("Expected 'pkg:Func', got '%s'", key)
	}
}

func TestRecvTypeName(t *testing.T) {
	if recvTypeName(nil) != "" {
		t.Error("Expected empty string for nil input")
	}
}

func TestIsPkgName(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"fmt", true},
		{"os", true},
		{"strings", true},
		{"MyStruct", false},
		{"MyFunc", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isPkgName(tt.input); got != tt.want {
				t.Errorf("isPkgName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsFunc(t *testing.T) {
	a := &FuncNode{Name: "A"}
	b := &FuncNode{Name: "B"}
	nodes := []*FuncNode{a, b}

	if !containsFunc(nodes, a) {
		t.Error("Expected to find A in nodes")
	}
	if !containsFunc(nodes, b) {
		t.Error("Expected to find B in nodes")
	}
	if containsFunc(nodes, &FuncNode{Name: "C"}) {
		t.Error("Expected not to find C in nodes")
	}
}

func TestBuildCallGraph(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "mypkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	src := `package mypkg

func A() {
	B()
}

func B() {
	C()
}

func C() {
	// no calls
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	cg, err := BuildCallGraph(dir, []string{"mypkg"})
	if err != nil {
		t.Fatal(err)
	}

	if len(cg.Funcs) < 3 {
		t.Errorf("Expected at least 3 functions, got %d", len(cg.Funcs))
	}

	aKey := FuncKey("mypkg", "A")
	if a, ok := cg.Funcs[aKey]; ok {
		if len(a.Callees) == 0 {
			t.Error("Expected A to have callees")
		}
	}
}

func TestBuildCallGraphWithInterfaces(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "mypkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	src := `package mypkg

type Greeter interface {
	Greet() string
}

type EnglishGreeter struct{}

func (g EnglishGreeter) Greet() string {
	return "Hello"
}

type FrenchGreeter struct{}

func (g FrenchGreeter) Greet() string {
	return "Bonjour"
}

func UseGreeter(g Greeter) string {
	return g.Greet()
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "greeter.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	cg, err := BuildCallGraph(dir, []string{"mypkg"})
	if err != nil {
		t.Fatal(err)
	}

	if len(cg.Interfaces) == 0 {
		t.Error("Expected to find at least one interface")
	}

	greeterKey := FuncKey("mypkg", "Greeter")
	if iface, ok := cg.Interfaces[greeterKey]; ok {
		if len(iface.Implementations) == 0 {
			t.Error("Expected to find implementations of Greeter interface")
		}
	}
}

func TestBuildImportMap(t *testing.T) {
	src := `package mypkg

import (
	"fmt"
	alias "example.com/other"
)

func UseImports() {
	fmt.Println("hello")
	alias.DoSomething()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	cg := &CallGraph{
		Funcs:      make(map[string]*FuncNode),
		ByFile:     make(map[string][]*FuncNode),
		ByPackage:  make(map[string][]string),
		Interfaces: make(map[string]*InterfaceInfo),
		pkgDirs:    make(map[string]string),
		pkgImports: make(map[string]string),
	}

	importMap := cg.buildImportMap(f)

	if importMap["fmt"] != "fmt" {
		t.Errorf("Expected 'fmt' -> 'fmt', got %q", importMap["fmt"])
	}
	if importMap["alias"] != "example.com/other" {
		t.Errorf("Expected 'alias' -> 'example.com/other', got %q", importMap["alias"])
	}
}

func TestInterfacePropagation(t *testing.T) {
	cg := &CallGraph{
		Funcs:          make(map[string]*FuncNode),
		ByFile:         make(map[string][]*FuncNode),
		ByPackage:      make(map[string][]string),
		Interfaces:     make(map[string]*InterfaceInfo),
		InterfaceImpls: make(map[string][]string),
		pkgDirs:        make(map[string]string),
		pkgImports:     make(map[string]string),
	}

	ifaceMethod := &FuncNode{
		Name:    "Greeter.Greet",
		Package: "mypkg",
		File:    "greeter.go",
		StartLine: 5,
		EndLine: 5,
	}
	cg.Funcs[FuncKey("mypkg", "Greeter.Greet")] = ifaceMethod
	cg.ByFile["greeter.go"] = append(cg.ByFile["greeter.go"], ifaceMethod)
	cg.ByPackage["mypkg"] = append(cg.ByPackage["mypkg"], FuncKey("mypkg", "Greeter.Greet"))

	engImpl := &FuncNode{
		Name:         "(EnglishGreeter).Greet",
		Package:      "mypkg",
		File:         "greeter.go",
		IsMethod:     true,
		ReceiverType: "EnglishGreeter",
		StartLine: 10,
		EndLine: 12,
	}
	freImpl := &FuncNode{
		Name:         "(FrenchGreeter).Greet",
		Package:      "mypkg",
		File:         "greeter.go",
		IsMethod:     true,
		ReceiverType: "FrenchGreeter",
		StartLine: 15,
		EndLine: 17,
	}
	cg.Funcs[FuncKey("mypkg", "(EnglishGreeter).Greet")] = engImpl
	cg.Funcs[FuncKey("mypkg", "(FrenchGreeter).Greet")] = freImpl
	cg.ByFile["greeter.go"] = append(cg.ByFile["greeter.go"], engImpl, freImpl)
	cg.ByPackage["mypkg"] = append(cg.ByPackage["mypkg"], 
		FuncKey("mypkg", "(EnglishGreeter).Greet"),
		FuncKey("mypkg", "(FrenchGreeter).Greet"))

	iface := &InterfaceInfo{
		Name:    "Greeter",
		Package: "mypkg",
		Methods: map[string]bool{"Greet": true},
		Implementations: map[string]string{
			"EnglishGreeter": "mypkg",
			"FrenchGreeter":  "mypkg",
		},
	}
	cg.Interfaces[FuncKey("mypkg", "Greeter")] = iface

	changes := []Change{
		{File: "greeter.go", AddedLines: []int{5}},
	}

	domain := ComputeDomain(cg, changes)

	if !domain.All[FuncKey("mypkg", "Greeter.Greet")] {
		t.Error("Interface method should be in impact domain")
	}

	if !domain.InterfaceForward[FuncKey("mypkg", "(EnglishGreeter).Greet")] {
		t.Error("EnglishGreeter.Greet should be in InterfaceForward")
	}
	if !domain.InterfaceForward[FuncKey("mypkg", "(FrenchGreeter).Greet")] {
		t.Error("FrenchGreeter.Greet should be in InterfaceForward")
	}
}

func TestAffectedPackages(t *testing.T) {
	domain := &Domain{
		Changed: map[string]bool{
			"pkg1:Func1": true,
			"pkg2:Func2": true,
		},
		Callees: map[string]bool{
			"pkg3:Func3": true,
		},
		All: map[string]bool{
			"pkg1:Func1": true,
			"pkg2:Func2": true,
			"pkg3:Func3": true,
		},
		ChangedLines:    make(map[string]map[int]bool),
		ChangedPackages: make(map[string]bool),
	}

	packages := domain.AffectedPackages()

	if len(packages) != 3 {
		t.Errorf("Expected 3 affected packages, got %d", len(packages))
	}

	if len(packages) >= 2 && packages[0] > packages[1] {
		t.Error("Expected sorted packages")
	}
}

func TestDomainStringWithInterface(t *testing.T) {
	domain := &Domain{
		Changed:           map[string]bool{"A": true},
		Callees:           map[string]bool{"B": true},
		Callers:          map[string]bool{"C": true},
		InterfaceForward:  map[string]bool{"D": true},
		InterfaceBackward: map[string]bool{"E": true},
		All:               map[string]bool{"A": true, "B": true, "C": true, "D": true, "E": true},
	}

	s := domain.String()

	if len(s) == 0 {
		t.Error("Expected non-empty string")
	}
}
