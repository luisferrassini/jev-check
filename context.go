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

// project is a config.json.
type project struct {
	about
	Exclude []string    `json:"exclude"`
	Checks  []gateCheck `json:"checks"`
}

func stateCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, `Usage: jev-check state [DIR]   reads DIR/.jev-check/config.json (default: .)
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

// configPath is dir's config.json.
func configPath(dir string) string { return filepath.Join(dir, jevDir, "config.json") }

// loadProject reads dir's config. A config under an old name or at the project root
// is never read: it is an error with the move steps, so its checks are not silently replaced.
func loadProject(dir string) (project, error) {
	var p project
	err := readJSON(configPath(dir), &p)
	if !errors.Is(err, fs.ErrNotExist) {
		return p, err
	}
	if old := filepath.Join(dir, jevDir, "project-context.json"); fileExists(old) {
		return p, fmt.Errorf(`%s is no longer read; jev-check reads %s. Rename it, in %s:
  git mv .jev-check/project-context.json .jev-check/config.json`, old, configPath(dir), dir)
	}
	if old := filepath.Join(dir, "project-context.json"); fileExists(old) {
		return p, fmt.Errorf(`%s is no longer read; jev-check reads %s. Move the jev-check files, in %s:
  mkdir -p .jev-check
  git mv project-context.json .jev-check/config.json
  git mv input .jev-check/      # only jev-check checks, if any
  git mv fixtures .jev-check/   # if any
Old output/ can be deleted`, old, configPath(dir), dir)
	}
	return p, err
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

// git runs git in dir and returns its output. An error includes git's message,
// untranslated so callers can match it.
func git(dir string, args ...string) (string, error) {
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

// excludes turns git pathspecs into exclude pathspecs.
func excludes(patterns []string) []string {
	out := make([]string, len(patterns))
	for i, p := range patterns {
		out[i] = ":(exclude)" + p
	}
	return out
}

const initUsage = `Usage: jev-check init [DIR]   (default: .)
Sets up DIR/.jev-check/, creating each of these files that is missing:
  config.json            the config, with the starting defaults
  .gitignore             keeps .env and output/ out of Git
  README.md              what each file in .jev-check/ is for
  input/questions/, input/states/
                         the checks available: every bundled check when
                         input/questions/ is new, else the ones in "checks"
DIR must be in a git working tree. The gate runs only the checks listed in
"checks" in config.json, so add an entry there to turn one on.
A file that already exists is kept, never replaced, so running init again
restores only what is missing and does not bring back a deleted check.
`

// initConfig is the starting config.json. Its defaults are this repository's, not a policy for every project.
const initConfig = `{
  "purpose": "",
  "rules": [],
  "folders": {},
  "exclude": [".jev-check/"],
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
	// Mkdir, not MkdirAll: a file or symlink named .jev-check is an error, not a folder to follow.
	mkErr := os.Mkdir(filepath.Join(dir, jevDir), 0o755)
	if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
		return 0, mkErr
	}
	if info, err := os.Lstat(filepath.Join(dir, jevDir)); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", filepath.Join(dir, jevDir))
	}
	path := configPath(dir)
	var p project
	switch err := createFile(path, initConfig); {
	case errors.Is(err, fs.ErrExist):
		fmt.Fprintf(stdout, "kept    %s\n", path)
		if err := readJSON(path, &p); err != nil {
			return 0, err
		}
	case err != nil:
		if mkErr == nil {
			os.Remove(filepath.Join(dir, jevDir)) // only this run made it; Remove keeps a non-empty folder
		}
		return 0, err
	default:
		fmt.Fprintf(stdout, "created %s\n", path)
		json.Unmarshal([]byte(initConfig), &p)
	}
	ignore := filepath.Join(dir, jevDir, ".gitignore")
	if err := installFile(ignore, ".env\noutput/\n", stdout); err != nil {
		return 0, err
	}
	readme, _ := bundled.ReadFile(bundleDir + "/README.md")
	if err := installFile(filepath.Join(dir, jevDir, "README.md"), string(readme), stdout); err != nil {
		return 0, err
	}
	// A new input/questions/ gets every bundled check, the ones available to list in "checks".
	// Later runs restore only the listed ones, so a check the user deleted stays deleted.
	names := bundledNames()
	if _, err := os.Stat(inputDir(dir, "questions")); err == nil {
		names = nil
		for _, c := range p.Checks {
			if isBundled(c.Check) && !slices.Contains(names, c.Check) {
				names = append(names, c.Check)
			}
		}
	}
	if err := addChecks(dir, names, stdout); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, `Next:
  1. Review %s: purpose, rules, exclude, and "checks".
     The gate runs only the checks listed in "checks". %s
     holds every check available: edit, delete, or add files there.
  2. Put TYPESAFE_API_KEY=<key> in %s. No other place is read.
     %s keeps it and output/ out of Git.
  3. Check the setup: jev-check doctor %s
  4. Stage the work you want checked: git add -- <path>
  5. Run: jev-check gate %s
`, path, inputDir(dir, "questions")+string(filepath.Separator),
		filepath.Join(dir, settingsFile), ignore, shellQuote(dir), shellQuote(dir))
	return 0, nil
}

// shellQuote returns s ready to paste into a POSIX shell, quoted only when needed.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:@%,=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// createFile writes a new file. O_EXCL refuses any existing entry, a symlink
// included, even when two inits race. A failed write removes the file.
func createFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
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
			// The path may hold a secret, so neither it nor an OS error that repeats it is printed.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
			}
			return nil, nil, fmt.Errorf("check %s: coding_style %s: %w", c.Check, safeLabel(string(c.CodingStyle), "path (not shown)"), err)
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
	if strings.HasSuffix(filepath.FromSlash(rel), sep) {
		return "", errors.New("must name a file, without a trailing " + sep)
	}
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
