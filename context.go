package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
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
		fmt.Fprint(stdout, `Usage: jev-check context [DIR]   reads DIR/project-context.json (default: .)
Prints the project fields and tree that go with every request. When checks
name a coding_style document, coding_styles maps each path to its contents
once. A request holds only its own check's document, as state.coding_style.
`)
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
	_, styles, err := loadStyles(dir, p.Checks)
	if err != nil {
		return 0, err
	}
	if err := styleSecrets(styles); err != nil {
		return 0, err
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	if len(styles) > 0 {
		state["coding_styles"] = styles
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
  2. Put TYPESAFE_API_KEY=<key> in %s. No other place is read.
  3. Add .jev-check/.env and output/ to the project's ignore rules.
  4. Check the setup: jev-check doctor %s
  5. Stage the work you want checked: git add -- <path>
  6. Run: jev-check gate %s
`, path, filepath.Join(dir, settingsFile), dir, dir)
	return 0, nil
}

// maxStyleBytes caps a coding_style document, so a large file cannot flood a request.
const maxStyleBytes = 65536

// loadStyles reads each check's coding_style document from dir's working tree, once per path.
// It returns each check's normalized path, empty for none, and the contents by path.
func loadStyles(dir string, checks []gateCheck) ([]string, map[string]string, error) {
	paths := make([]string, len(checks))
	var docs map[string]string
	for i, c := range checks {
		if c.CodingStyle == nil {
			continue
		}
		path, err := stylePath(dir, c.CodingStyle)
		if docs == nil {
			docs = map[string]string{}
		}
		if _, ok := docs[path]; err == nil && !ok {
			docs[path], err = readStyle(filepath.Join(dir, filepath.FromSlash(path)))
		}
		if err != nil {
			return nil, nil, fmt.Errorf("check %s: coding_style %s: %w", c.Check, printable(string(c.CodingStyle)), err)
		}
		paths[i] = path
	}
	return paths, docs, nil
}

// stylePath checks that raw names a regular file inside dir, reached without
// symlinks or .., and returns it with forward slashes.
func stylePath(dir string, raw json.RawMessage) (string, error) {
	var rel string
	if json.Unmarshal(raw, &rel) != nil || strings.TrimSpace(rel) == "" {
		return "", errors.New("must be a non-empty path string")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("must be relative to the project folder")
	}
	sep := string(filepath.Separator)
	if slices.Contains(strings.Split(filepath.FromSlash(rel), sep), "..") {
		return "", errors.New("must not contain ..")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	path := dir
	var info fs.FileInfo
	for _, part := range strings.Split(clean, sep) {
		path = filepath.Join(path, part)
		var err error
		if info, err = os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("no such file in the working tree")
		} else if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("must not go through a symlink")
		}
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	return filepath.ToSlash(clean), nil
}

// readStyle reads a document of at most maxStyleBytes of UTF-8 text, exactly as it is.
func readStyle(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStyleBytes+1))
	switch {
	case err != nil:
		return "", err
	case len(data) > maxStyleBytes:
		return "", fmt.Errorf("is larger than %d bytes", maxStyleBytes)
	case !utf8.Valid(data):
		return "", errors.New("is not valid UTF-8")
	case bytes.IndexByte(data, 0) >= 0:
		return "", errors.New("holds a NUL byte")
	case strings.TrimSpace(string(data)) == "":
		return "", errors.New("is empty")
	}
	return string(data), nil
}

// addStyle puts a check's document into its request state, when it has one.
func addStyle(state map[string]any, path string, docs map[string]string) {
	if path != "" {
		state["coding_style"] = map[string]string{"path": path, "content": docs[path]}
	}
}
