package impact

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
)

// ParseGitDiff parses the output of "git diff" and returns the changes.
func ParseGitDiff(dir string, ref string) ([]Change, error) {
	args := []string{"diff", "--name-only", "--diff-filter=ACMR"}
	if ref != "" {
		args = append(args, ref)
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var changes []Change
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		file := strings.TrimSpace(scanner.Text())
		if file == "" || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}

		change, err := parseFileDiff(dir, file, ref)
		if err != nil {
			continue
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}

	return changes, nil
}

// ParseGitDiffIndex parses unstaged changes (working directory vs index).
func ParseGitDiffIndex(dir string) ([]Change, error) {
	return parseDiffWithArgs(dir, "diff", "--name-only", "--diff-filter=ACMR")
}

// ParseGitDiffStaged parses staged changes (index vs HEAD).
func ParseGitDiffStaged(dir string) ([]Change, error) {
	return parseDiffWithArgs(dir, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
}

// parseDiffWithArgs parses git diff with custom arguments.
func parseDiffWithArgs(dir string, args ...string) ([]Change, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var changes []Change
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		file := strings.TrimSpace(scanner.Text())
		if file == "" || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}

		// For index/staged, we need to get the actual diff
		change, err := parseFileDiffWithArgs(dir, file, args[:len(args)-2]...)
		if err != nil {
			continue
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}

	return changes, nil
}

// parseFileDiff parses the diff for a single file.
func parseFileDiff(dir, file, ref string) (*Change, error) {
	args := []string{"diff", "-U0"}
	if ref != "" {
		args = append(args, ref, "--", file)
	} else {
		args = append(args, "--", file)
	}
	return parseFileDiffWithArgs(dir, file, args...)
}

// parseFileDiffWithArgs parses file diff with custom arguments.
func parseFileDiffWithArgs(dir, file string, baseArgs ...string) (*Change, error) {
	args := append(baseArgs, "--", file)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	change := &Change{File: file}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))

	for scanner.Scan() {
		line := scanner.Text()

		// Parse unified diff hunk header: @@ -old,count +new,count @@
		if strings.HasPrefix(line, "@@") {
			header := parseHunkHeader(line)
			if header != nil {
				// Add lines in this hunk as changed
				for i := 0; i < header.newCount; i++ {
					change.AddedLines = append(change.AddedLines, header.newLine+i)
				}
				// Add removed lines
				for i := 0; i < header.oldCount; i++ {
					change.RemovedLines = append(change.RemovedLines, header.oldLine+i)
				}
			}
		}
	}

	if len(change.AddedLines) == 0 && len(change.RemovedLines) == 0 {
		return nil, nil
	}

	return change, nil
}

// hunkHeader represents a parsed hunk header.
type hunkHeader struct {
	oldLine  int
	oldCount int
	newLine  int
	newCount int
}

// parseHunkHeader parses a unified diff hunk header.
func parseHunkHeader(line string) *hunkHeader {
	// Format: @@ -oldLine,oldCount +newLine,newCount @@
	if !strings.HasPrefix(line, "@@ ") {
		return nil
	}

	parts := strings.Fields(line)
	if len(parts) < 4 {
		return nil
	}

	h := &hunkHeader{}

	// Parse -oldLine,oldCount
	oldPart := strings.TrimPrefix(parts[1], "-")
	if idx := strings.Index(oldPart, ","); idx >= 0 {
		h.oldLine, _ = strconv.Atoi(oldPart[:idx])
		h.oldCount, _ = strconv.Atoi(oldPart[idx+1:])
	} else {
		h.oldLine, _ = strconv.Atoi(oldPart)
		h.oldCount = 1
	}

	// Parse +newLine,newCount
	newPart := strings.TrimPrefix(parts[2], "+")
	if idx := strings.Index(newPart, ","); idx >= 0 {
		h.newLine, _ = strconv.Atoi(newPart[:idx])
		h.newCount, _ = strconv.Atoi(newPart[idx+1:])
	} else {
		h.newLine, _ = strconv.Atoi(newPart)
		h.newCount = 1
	}

	return h
}

// IsGitRepo checks if the given directory is inside a git repository.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	err := cmd.Run()
	return err == nil
}

// GetCurrentCommit returns the current HEAD commit hash.
func GetCurrentCommit(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
