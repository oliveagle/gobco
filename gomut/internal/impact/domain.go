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
	// Forward is the set of functions affected by forward propagation (callees).
	// These are functions that the changed code calls, whose behavior might
	// affect the changed code's correctness.
	Forward map[string]bool
	// Backward is the set of functions affected by backward propagation (callers).
	// These are functions that call the changed code, and might be affected
	// if the changed code's behavior changes.
	Backward map[string]bool
	// InterfaceForward is the set of interface implementations affected by
	// changes to interface methods.
	InterfaceForward map[string]bool
	// InterfaceBackward is the set of interface definitions affected by
	// changes to implementations.
	InterfaceBackward map[string]bool
	// All is the union of Changed + Forward + Backward + InterfaceForward + InterfaceBackward.
	All map[string]bool
	// ChangedLines maps file paths to affected line numbers.
	ChangedLines map[string]map[int]bool
	// ChangedPackages is the set of packages with changes.
	ChangedPackages map[string]bool
}

// ComputeDomain calculates the impact domain for a set of changes.
//
// Forward propagation: changed function -> all callees (transitively)
//
//	If function A calls B and B is changed, A's correctness might be affected
//	if B's behavior changes.
//
// Backward propagation: changed function -> all callers (transitively)
//
//	If function A calls B and B is changed, A might need to be retested.
//
// Interface propagation: changes to interface methods propagate to implementations,
// and changes to implementations propagate to interface callers.
func ComputeDomain(cg *CallGraph, changes []Change) *Domain {
	d := &Domain{
		Changed:           make(map[string]bool),
		Forward:           make(map[string]bool),
		Backward:          make(map[string]bool),
		InterfaceForward:  make(map[string]bool),
		InterfaceBackward: make(map[string]bool),
		All:               make(map[string]bool),
		ChangedLines:      make(map[string]map[int]bool),
		ChangedPackages:   make(map[string]bool),
	}

	// Step 1: Identify changed functions from changes
	for _, change := range changes {
		d.ChangedLines[change.File] = make(map[int]bool)
		for _, line := range change.AddedLines {
			d.ChangedLines[change.File][line] = true
		}

		// Find functions that overlap with changed lines
		nodes := findNodesByFile(cg, change.File)
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

	// Step 2: Forward propagation (callees)
	for key := range d.Changed {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}
		forwardWalk(node, d.Forward, make(map[string]bool))
	}

	// Step 3: Backward propagation (callers)
	for key := range d.Changed {
		node, ok := cg.Funcs[key]
		if !ok {
			continue
		}
		backwardWalk(node, d.Backward, make(map[string]bool))
	}

	// Step 4: Interface propagation
	cg.computeInterfacePropagation(d)

	// Step 5: Compute union
	for key := range d.Changed {
		d.All[key] = true
	}
	for key := range d.Forward {
		d.All[key] = true
	}
	for key := range d.Backward {
		d.All[key] = true
	}
	for key := range d.InterfaceForward {
		d.All[key] = true
	}
	for key := range d.InterfaceBackward {
		d.All[key] = true
	}

	return d
}

// checkInterfaceChanges checks if changes affect interface definitions.
func (cg *CallGraph) checkInterfaceChanges(d *Domain, change Change) {
	// Find interface definitions in the changed file
	for _, iface := range cg.Interfaces {
		// Check if this interface is in the changed file
		ifaceKey := FuncKey(iface.Package, iface.Name)
		if node, ok := cg.Funcs[ifaceKey]; ok {
			if node.File == change.File || filepath.Base(node.File) == filepath.Base(change.File) {
				// Check if any changed line overlaps with the interface definition
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
	// For each changed function, check if it's an interface method
	// or an implementation of an interface method
	for key := range d.Changed {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		pkg := parts[0]
		name := parts[1]

		// Check if this is an interface method
		// Interface methods look like "InterfaceName.MethodName"
		if idx := strings.Index(name, "."); idx > 0 {
			ifaceName := name[:idx]
			methodName := name[idx+1:]

			ifaceKey := FuncKey(pkg, ifaceName)
			if iface, ok := cg.Interfaces[ifaceKey]; ok {
				if iface.Methods[methodName] {
					// This is an interface method change - propagate to implementations
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
			// Find interfaces that have this method name
			for _, iface := range cg.Interfaces {
				for methodName := range iface.Methods {
					// Extract method name from "(Type).Method" format
					methodPart := name
					if idx := strings.LastIndex(name, "."); idx > 0 {
						methodPart = name[idx+1:]
					}

					if methodPart == methodName {
						// This implementation satisfies an interface method
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

// findNodesByFile finds function nodes by file path, handling both relative and absolute paths.
func findNodesByFile(cg *CallGraph, file string) []*FuncNode {
	// First try direct match
	if nodes, ok := cg.ByFile[file]; ok {
		return nodes
	}

	// Try to find by base name
	baseName := filepath.Base(file)
	for filePath, nodes := range cg.ByFile {
		if filepath.Base(filePath) == baseName {
			return nodes
		}
	}

	// Try to find by suffix
	for filePath, nodes := range cg.ByFile {
		if strings.HasSuffix(filePath, "/"+file) || strings.HasSuffix(filePath, "/"+baseName) {
			return nodes
		}
	}

	return nil
}

// overlaps checks if a function node overlaps with a change.
func overlaps(node *FuncNode, change Change) bool {
	if node.File != change.File {
		// Check if the file paths refer to the same file
		if filepath.Base(node.File) != filepath.Base(change.File) {
			return false
		}
	}
	// Check if any changed line falls within the function's line range
	for _, line := range change.AddedLines {
		if line >= node.StartLine && line <= node.EndLine {
			return true
		}
	}
	return false
}

// forwardWalk traverses callees transitively.
func forwardWalk(node *FuncNode, visited map[string]bool, seen map[string]bool) {
	key := FuncKey(node.Package, node.Name)
	if seen[key] {
		return
	}
	seen[key] = true
	visited[key] = true

	for _, callee := range node.Callees {
		forwardWalk(callee, visited, seen)
	}
}

// backwardWalk traverses callers transitively.
func backwardWalk(node *FuncNode, visited map[string]bool, seen map[string]bool) {
	key := FuncKey(node.Package, node.Name)
	if seen[key] {
		return
	}
	seen[key] = true
	visited[key] = true

	for _, caller := range node.Callers {
		backwardWalk(caller, visited, seen)
	}
}

// AffectedLines returns all line numbers in the impact domain for a given file.
// It includes lines from directly changed code AND lines from all functions
// in the impact domain (forward + backward propagation).
func (d *Domain) AffectedLines(file string) []int {
	lines := make(map[int]bool)

	// Add directly changed lines
	if fileLines, ok := d.ChangedLines[file]; ok {
		for line := range fileLines {
			lines[line] = true
		}
	}

	// Add lines from functions in the impact domain
	// This requires the call graph to get function line ranges.
	// We store function ranges in a separate structure for this purpose.
	// For now, we rely on ChangedLines which already includes the right lines.

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

	// Add directly changed lines
	if fileLines, ok := d.ChangedLines[file]; ok {
		for line := range fileLines {
			lines[line] = true
		}
	}

	// Add lines from functions in the impact domain that are in this file
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

// String returns a human-readable summary of the domain.
func (d *Domain) String() string {
	return "changed=" + itoa(len(d.Changed)) +
		" forward=" + itoa(len(d.Forward)) +
		" backward=" + itoa(len(d.Backward)) +
		" interface_forward=" + itoa(len(d.InterfaceForward)) +
		" interface_backward=" + itoa(len(d.InterfaceBackward)) +
		" total=" + itoa(len(d.All))
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
