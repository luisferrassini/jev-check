package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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

const initUsage = `Usage: jev-check init [DIR]   (default: .)
Creates DIR/project-context.json with the starting defaults. DIR must be in a
git working tree. An existing file is never replaced.
`

// initConfig is the starting project-context.json. Its defaults are this repository's, not a policy for every project.
const initConfig = `{
  "purpose": "",
  "rules": [],
  "folders": {},
  "exclude": [".env", "output/", "fixtures/"],
  "checks": [{ "check": "public-release", "threshold": 0.5, "skip": ["LICENSE"] }]
}
`

func initCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, initUsage)
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	dir, err := filepath.Abs(cmp.Or(append(args, ".")...))
	if err != nil {
		return 0, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", dir)
	}
	if out, err := git(dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return 0, fmt.Errorf("%s is not in a git working tree", dir)
	}
	path := filepath.Join(dir, "project-context.json")
	// O_EXCL refuses any existing entry, a symlink included, even when two inits race.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return 0, fmt.Errorf("%s already exists; inspect it instead of running init", path)
	} else if err != nil {
		return 0, err
	}
	_, err = f.WriteString(initConfig)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return 0, fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintf(stdout, `created %s
Next:
  1. Review it: purpose, rules, exclude, and the public-release check at threshold 0.5.
  2. Set TYPESAFE_API_KEY in the environment or in %s.
  3. Add .env and output/ to the project's ignore rules.
  4. Stage the work you want checked: git add -- <path>
  5. Run: jev-check gate %s
`, path, filepath.Join(dir, ".env"), dir)
	return 0, nil
}
