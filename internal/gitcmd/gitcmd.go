// Package gitcmd runs git and lists staged files.
package gitcmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run runs git in dir and returns its output. An error includes git's message,
// untranslated so callers can match it.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %v: %s", args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Excludes turns git pathspecs into exclude pathspecs.
func Excludes(patterns []string) []string {
	out := make([]string, len(patterns))
	for i, p := range patterns {
		out[i] = ":(exclude)" + p
	}
	return out
}

// StagedFiles lists the staged files in dir, minus the exclude pathspecs.
// A git error stops the gate, so a failure never looks like "nothing staged".
func StagedFiles(dir string, exclude []string) ([]string, error) {
	out, err := Run(dir, append([]string{"diff", "--cached", "--relative", "--name-only", "-z", "--", "."}, Excludes(exclude)...)...)
	return SplitNUL(out), err
}

// SplitNUL splits git's -z output into paths.
func SplitNUL(out string) []string {
	if out == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}
