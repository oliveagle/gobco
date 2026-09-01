// Package main tests cmd/gomut's command-line contract (todo T-7).
package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// runCLI invokes run with its stdout/stderr routed through pipes, returning
// the exit code plus the content written to each stream. Closing the write
// ends only after run returns unblocks the readers.
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
	code = run(stdoutW, stderrW, args)
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

// captureGlobalStderr swaps os.Stderr for the duration of fn so that the
// usage text the flag package writes to the process-wide os.Stderr (on
// -help) can be asserted. It restores os.Stderr and returns the captured
// content.
func captureGlobalStderr(t *testing.T, fn func()) (msg string) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = orig
		_ = w.Close()
		b, _ := io.ReadAll(r)
		msg = string(b)
		_ = r.Close()
	}()
	fn()
	return
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"-version"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "gomut 0.1.0" {
		t.Errorf("stdout = %q, want %q", got, "gomut 0.1.0")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr = %q, want empty", stderr)
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
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr param = %q, want empty (usage goes to global os.Stderr)", stderr)
	}
	// -help prints fs.Usage() to the global os.Stderr (fs.Output is not the
	// run stderr parameter).
	msg := captureGlobalStderr(t, func() {
		run(os.Stdout, os.Stderr, []string{"-help"})
	})
	if !strings.Contains(msg, "usage: gomut") {
		t.Errorf("global stderr = %q, want it to contain %q", msg, "usage: gomut")
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
	want := "gomut: flag provided but not defined: -bogus"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}
