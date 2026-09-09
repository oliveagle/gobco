// Package commaok is a fixture for the INVALID-mutant classification:
// in the comma-ok idiom below, v and ok are each used only in the
// return, so every ReturnVals rewrite of "return v, ok" orphans one
// of the two declared values, which Go rejects at compile time.
package commaok

// Lookup returns the value for k and whether it was present.
func Lookup(m map[string]int, k string) (int, bool) {
	v, ok := m[k]
	return v, ok
}

// Double is a plain function whose mutations are all killable.
func Double(a int) int {
	return a * 2
}
