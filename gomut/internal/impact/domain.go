package impact

import (
	"path/filepath"
	"sort"
	"strings"
)

// Domain represents the impact domain of a code change.
type Domain struct {
	// Changed is the set of directly changed function keys.
	Changed map[string]bool
	// Callers is the set of functions that call changed code (transitively).
	// If a changed function's behavior changes, callers need retesting.
	// This is the PRIMARY propagation direction for mutation testing.
	Callers map[string]bool
	// Callees is the set of functions called by changed code (transitively).
	// These are informational — they didn't change, so they don't need retesting.
	// Included for visibility into what the changed code depends on.
	Callees map[string]bool
	// InterfaceForward is the set of interface implementations affected by
	// changes to interface methods.
	InterfaceForward map[string]bool
	// InterfaceBackward is the set of interface definitions affected by
	// changes to implementations.
	InterfaceBackward map[string]bool
	// All is the set of functions that need retesting:
	// Changed + Callers + InterfaceForward + InterfaceBackward.
	All map[string]bool
	// ChangedLines maps absolute file paths to affected line numbers.
	ChangedLines map[string]map[int]bool
	// ChangedPackages is the set of packages with changes.
	ChangedPackages map[string]bool
}

// ComputeDomain calculates the impact domain for a set of changes.
//
// For mutation testing, only functions whose behavior might have changed
// (or whose callers depend on changed behavior) need retesting.
//
// Propagation:
//   - Changed -> Callers (backward): if B changes, A (which calls B) needs retesting.
//   - Changed -> Callees (informational): what the changed code depends on.
//   - Interface methods <-> implementations: bidirectional.
func ComputeDomain(cg *CallGraph, changes []Change) *Domain {
	d := &Domain{
		Changed:           make(map[string]bool),
		Callers:           make(map[string]bool),
		Callees:           make(map[string]bool),
		InterfaceForward:  make(map[string]bool),
		InterfaceBackward: make(map[string]bool),
		All:               make(map[string]bool),
		ChangedLines:      make(map[string]map[int]bool),
		ChangedPackages:   make(map[string]bool),
	}

	// Step 1: Identify changed functions from changes
	for _, change := range changes {
		absFile := resolveAbsPath(cg, change.File)
		d.ChangedLines[absFile] = make(map[int]bool)
		for _, line := range change.AddedLines {
			d.ChangedLines[absFile][line] = true
		}

		nodes := findNodesByFile(cg, absFile)
		for _, node := range nodes {
			if overlaps(node, change) {
				key := FuncKey(node.Package, node.Name)
				d.Changed[key] = true
				d.ChangedPackages[node.Package] = true
			}
		}
	}

	// Step 1.5: Check if changes affect interface definitions
	for _, change := range changes {
		cg.checkInterfaceChanges(d, change)
	}

	// Step 2: Backward propagation — callers of changed functions.
	// These are the functions that NEED RETESTING.
	for key := range d.Changed {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}
		walkCallers(node, d.Callers, make(map[string]bool))
	}

	// Step 3: Forward propagation — callees of changed functions.
	// Informational only; these don't need retesting.
	for key := range d.Changed {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}
		walkCallees(node, d.Callees, make(map[string]bool))
	}

	// Step 4: Interface propagation
	cg.computeInterfacePropagation(d)

	// Step 5: Compute "All" = functions that need retesting
	for key := range d.Changed {
		d.All[key] = true
	}
	for key := range d.Callers {
		d.All[key] = true
	}
	for key := range d.InterfaceForward {
		d.All[key] = true
	}
	for key := range d.InterfaceBackward {
		d.All[key] = true
	}
	// Note: Callees are NOT in All — they don't need retesting.

	return d
}

// resolveAbsPath tries to resolve a relative change path to an absolute path
// matching a key in the ByFile map.
func resolveAbsPath(cg *CallGraph, file string) string {
	if cg == nil {
		return file
	}
	// Exact match first
	if _, ok := cg.ByFile[file]; ok {
		return file
	}
	// Try matching the complete repository-relative suffix first. The
	// basename-only fallback must stay separate: combining it with || inside
	// this loop lets an unrelated path sharing the basename win based on Go's
	// randomized map iteration order.
	for absPath := range cg.ByFile {
		if strings.HasSuffix(absPath, "/"+file) {
			return absPath
		}
	}
	// Try matching just the base name (less precise, last resort)
	base := filepath.Base(file)
	for absPath := range cg.ByFile {
		if filepath.Base(absPath) == base {
			return absPath
		}
	}
	return file
}

// checkInterfaceChanges checks if changes affect interface definitions.
func (cg *CallGraph) checkInterfaceChanges(d *Domain, change Change) {
	absFile := resolveAbsPath(cg, change.File)
	for _, iface := range cg.Interfaces {
		ifaceKey := FuncKey(iface.Package, iface.Name)
		if node, ok := cg.Funcs[ifaceKey]; ok {
			if node.File == absFile {
				for _, line := range change.AddedLines {
					if line >= node.StartLine && line <= node.EndLine {
						d.Changed[ifaceKey] = true
						d.ChangedPackages[iface.Package] = true
						break
					}
				}
			}
		}
	}
}

// computeInterfacePropagation handles interface-related impact propagation.
func (cg *CallGraph) computeInterfacePropagation(d *Domain) {
	for key := range d.Changed {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		pkg := parts[0]
		name := parts[1]

		// Check if this is an interface method definition
		// Interface methods look like "InterfaceName.MethodName"
		if idx := strings.Index(name, "."); idx > 0 {
			ifaceName := name[:idx]
			methodName := name[idx+1:]

			ifaceKey := FuncKey(pkg, ifaceName)
			if iface, ok := cg.Interfaces[ifaceKey]; ok {
				if iface.Methods[methodName] {
					// Propagate to all implementations
					for implType, implPkg := range iface.Implementations {
						implKey := FuncKey(implPkg, "("+implType+")."+methodName)
						if _, exists := cg.Funcs[implKey]; exists {
							d.InterfaceForward[implKey] = true
						}
					}
				}
			}
		}

		// Check if this is an implementation of an interface method
		if node, ok := cg.Funcs[key]; ok && node.IsMethod {
			for _, iface := range cg.Interfaces {
				// Check if this receiver type implements this interface
				if implPkg, ok := iface.Implementations[node.ReceiverType]; ok {
					if implPkg != node.Package {
						continue
					}
				}

				for methodName := range iface.Methods {
					methodPart := name
					if idx := strings.LastIndex(name, "."); idx > 0 {
						methodPart = name[idx+1:]
					}
					if methodPart == methodName {
						ifaceMethodKey := FuncKey(iface.Package, iface.Name+"."+methodName)
						if _, exists := cg.Funcs[ifaceMethodKey]; exists {
							d.InterfaceBackward[ifaceMethodKey] = true
						}
					}
				}
			}
		}
	}
}

// findNodesByFile finds function nodes by file path.
func findNodesByFile(cg *CallGraph, file string) []*FuncNode {
	// Exact match
	if nodes, ok := cg.ByFile[file]; ok {
		return nodes
	}
	// Try suffix match against absolute paths
	for filePath, nodes := range cg.ByFile {
		if strings.HasSuffix(filePath, "/"+file) || strings.HasSuffix(filePath, "/"+filepath.Base(file)) {
			return nodes
		}
	}
	// Base name match (less precise)
	baseName := filepath.Base(file)
	for filePath, nodes := range cg.ByFile {
		if filepath.Base(filePath) == baseName {
			return nodes
		}
	}
	return nil
}

// overlaps checks if a function node overlaps with a change.
func overlaps(node *FuncNode, change Change) bool {
	absFile := resolveAbsPath(nil, change.File)
	if node.File != absFile && filepath.Base(node.File) != filepath.Base(change.File) {
		return false
	}
	for _, line := range change.AddedLines {
		if line >= node.StartLine && line <= node.EndLine {
			return true
		}
	}
	return false
}

// walkCallers traverses callers transitively (backward walk).
func walkCallers(node *FuncNode, visited map[string]bool, seen map[string]bool) {
	key := FuncKey(node.Package, node.Name)
	if seen[key] {
		return
	}
	seen[key] = true
	visited[key] = true
	for _, caller := range node.Callers {
		walkCallers(caller, visited, seen)
	}
}

// walkCallees traverses callees transitively (forward walk).
func walkCallees(node *FuncNode, visited map[string]bool, seen map[string]bool) {
	key := FuncKey(node.Package, node.Name)
	if seen[key] {
		return
	}
	seen[key] = true
	visited[key] = true
	for _, callee := range node.Callees {
		walkCallees(callee, visited, seen)
	}
}

// AffectedLines returns all line numbers in the impact domain for a given file.
func (d *Domain) AffectedLines(file string) []int {
	lines := make(map[int]bool)
	if fileLines, ok := d.ChangedLines[file]; ok {
		for line := range fileLines {
			lines[line] = true
		}
	}
	result := make([]int, 0, len(lines))
	for line := range lines {
		result = append(result, line)
	}
	sort.Ints(result)
	return result
}

// AffectedLinesWithGraph returns all line numbers in the impact domain
// for a given file, using the call graph for complete coverage.
func (d *Domain) AffectedLinesWithGraph(file string, cg *CallGraph) []int {
	lines := make(map[int]bool)
	if fileLines, ok := d.ChangedLines[file]; ok {
		for line := range fileLines {
			lines[line] = true
		}
	}
	if cg != nil {
		for key := range d.All {
			if node, ok := cg.Funcs[key]; ok {
				if node.File == file || filepath.Base(node.File) == filepath.Base(file) {
					for line := node.StartLine; line <= node.EndLine; line++ {
						lines[line] = true
					}
				}
			}
		}
	}
	result := make([]int, 0, len(lines))
	for line := range lines {
		result = append(result, line)
	}
	sort.Ints(result)
	return result
}

// AffectedFiles returns all files affected by the changes.
func (d *Domain) AffectedFiles() []string {
	files := make(map[string]bool)
	for file := range d.ChangedLines {
		files[file] = true
	}
	for key := range d.All {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			_ = parts
		}
	}
	result := make([]string, 0, len(files))
	for file := range files {
		result = append(result, file)
	}
	sort.Strings(result)
	return result
}

// AffectedFilesWithGraph returns all files affected by the changes.
func (d *Domain) AffectedFilesWithGraph(cg *CallGraph) []string {
	files := make(map[string]bool)
	for file := range d.ChangedLines {
		files[file] = true
	}
	if cg != nil {
		for key := range d.All {
			if node, ok := cg.Funcs[key]; ok {
				files[node.File] = true
			}
		}
	}
	result := make([]string, 0, len(files))
	for file := range files {
		result = append(result, file)
	}
	sort.Strings(result)
	return result
}

// AffectedPackages returns all packages affected by the changes.
func (d *Domain) AffectedPackages() []string {
	packages := make(map[string]bool)
	for key := range d.All {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			packages[parts[0]] = true
		}
	}
	result := make([]string, 0, len(packages))
	for pkg := range packages {
		result = append(result, pkg)
	}
	sort.Strings(result)
	return result
}

// String returns a human-readable summary of the domain.
func (d *Domain) String() string {
	return "changed=" + itoa(len(d.Changed)) +
		" callers=" + itoa(len(d.Callers)) +
		" callees=" + itoa(len(d.Callees)) +
		" interface_forward=" + itoa(len(d.InterfaceForward)) +
		" interface_backward=" + itoa(len(d.InterfaceBackward)) +
		" need_retest=" + itoa(len(d.All))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
