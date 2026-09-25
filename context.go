package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// about is what Jev reads about a project, next to its file tree.
type about struct {
	Purpose string            `json:"purpose,omitempty"`
	Rules   []string          `json:"rules,omitempty"`
	Folders map[string]string `json:"folders,omitempty"`
}

// project is a project-context.json.
type project struct {
	about
	Exclude []string    `json:"exclude"`
	Checks  []gateCheck `json:"checks"`
}

func contextCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(stdout, "Usage: jev-check context [DIR]   reads DIR/project-context.json (default: .)")
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	p, err := loadProject(dir)
	if err != nil {
		return 0, err
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	return 0, writeJSON(stdout, state)
}

func loadProject(dir string) (project, error) {
	var p project
	return p, readJSON(filepath.Join(dir, "project-context.json"), &p)
}

// projectState is the state Jev sees: the project's about fields and its file tree.
// The tree is listed on every run, so it never goes stale.
func projectState(dir string, p project) (map[string]any, error) {
	// This sends the whole tree. Cap it if a large project starts to dilute answers.
	out, err := git(dir, append([]string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."}, excludes(p.Exclude)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": p.about, "tree": splitNUL(out)}, nil
}

// git runs git in dir and returns its output. An error includes git's message.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %v: %s", args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// excludes turns git pathspecs into exclude pathspecs.
func excludes(patterns []string) []string {
	out := make([]string, len(patterns))
	for i, p := range patterns {
		out[i] = ":(exclude)" + p
	}
	return out
}
