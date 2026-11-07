// Package cli tests the gomut command-line contract (todo T-7).
package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// runCLI invokes Run with its stdout/stderr routed through pipes, returning
// the exit code plus the content written to each stream. Closing the write
// ends only after Run returns unblocks the readers.
func runCLI(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stderr: %v", err)
	}
	code = Run(Options{}, stdoutW, stderrW, args)
	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutB, _ := io.ReadAll(stdoutR)
	stderrB, _ := io.ReadAll(stderrR)
	stdout = string(stdoutB)
	stderr = string(stderrB)
	_ = stdoutR.Close()
	_ = stderrR.Close()
	return
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-version"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	want := "gomut " + Version
	if got := strings.TrimSpace(stdout); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestVersionFlagWithLabel(t *testing.T) {
	stdoutR, stdoutW, _ := os.Pipe()
	stderrR, stderrW, _ := os.Pipe()
	code := Run(Options{VersionLabel: "embedded in devine-utils-ci"}, stdoutW, stderrW, []string{"-version"})
	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutB, _ := io.ReadAll(stdoutR)
	stderrB, _ := io.ReadAll(stderrR)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, string(stderrB))
	}
	want := "gomut " + Version + " (embedded in devine-utils-ci)"
	if got := strings.TrimSpace(string(stdoutB)); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestHelpFlagExitsZero(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-help"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "usage: gomut") {
		t.Errorf("stderr = %q, want it to contain %q", stderr, "usage: gomut")
	}
}

// TestIncrementalFlagsAreDefined guards the flags that embedders depend on:
// incremental mode must be reachable from the CLI, not only from the
// upstream source tree.
func TestIncrementalFlagsAreDefined(t *testing.T) {
	// The run happens in an empty temp dir so that a flag which parses
	// successfully fails fast (no Go module) instead of mutating a package.
	for _, flagName := range []string{"-incremental", "-baseline", "-impact"} {
		t.Run(flagName, func(t *testing.T) {
			stdoutR, stdoutW, _ := os.Pipe()
			stderrR, stderrW, _ := os.Pipe()
			Run(Options{Dir: t.TempDir()}, stdoutW, stderrW, []string{flagName, "-bogus-suffix"})
			_ = stdoutR
			_ = stdoutW.Close()
			_ = stderrW.Close()
			stderrB, _ := io.ReadAll(stderrR)
			stderr := string(stderrB)
			if strings.Contains(stderr, "flag provided but not defined: "+flagName) {
				t.Fatalf("stderr = %q, want %s to be a defined flag", stderr, flagName)
			}
		})
	}
}

func TestUsageListsIncrementalFlags(t *testing.T) {
	code, _, stderr := runCLI(t, []string{"-help"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, flagName := range []string{"-incremental", "-baseline", "-impact"} {
		if !strings.Contains(stderr, flagName) {
			t.Errorf("usage does not mention %s", flagName)
		}
	}
}

func TestUnknownFormatExitsOne(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-format=bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := `gomut: unknown format "bogus" (supported: text,json)`
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestUnknownMutatorExitsOne(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-mutators=bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := `gomut: unknown operator "bogus"`
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestCoverTestNotImplemented(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-cover-test"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "not implemented in v1") {
		t.Errorf("stderr = %q, want it to contain %q", stderr, "not implemented in v1")
	}
}

func TestInvalidFlagExitsOne(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := `gomut: flag provided but not defined: -bogus`
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}
