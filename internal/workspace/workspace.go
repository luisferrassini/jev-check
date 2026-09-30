package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/secretscan"
)

// About is what Jev reads about a project, next to its file tree.
type About struct {
	Purpose string            `json:"purpose,omitempty"`
	Rules   []string          `json:"rules,omitempty"`
	Folders map[string]string `json:"folders,omitempty"`
}

// Project is a config.json.
type Project struct {
	About
	Exclude []string    `json:"exclude"`
	Checks  []GateCheck `json:"checks"`
}

// ConfigPath is dir's config.json.
func ConfigPath(dir string) string { return filepath.Join(dir, JevDir, "config.json") }

// LoadProject reads dir's config. A config under an old name or at the project root
// is never read: it is an error with the move steps, so its checks are not silently replaced.
func LoadProject(dir string) (Project, error) {
	var p Project
	err := fsutil.ReadJSON(ConfigPath(dir), &p)
	if !errors.Is(err, fs.ErrNotExist) {
		return p, err
	}
	if old := filepath.Join(dir, JevDir, "project-context.json"); fsutil.FileExists(old) {
		return p, fmt.Errorf(`%s is no longer read; jev-check reads %s. Rename it, in %s:
  git mv .jev-check/project-context.json .jev-check/config.json`, old, ConfigPath(dir), dir)
	}
	if old := filepath.Join(dir, "project-context.json"); fsutil.FileExists(old) {
		return p, fmt.Errorf(`%s is no longer read; jev-check reads %s. Move the jev-check files, in %s:
  mkdir -p .jev-check
  git mv project-context.json .jev-check/config.json
  git mv input .jev-check/      # only jev-check checks, if any
  git mv fixtures .jev-check/   # if any
Old output/ can be deleted`, old, ConfigPath(dir), dir)
	}
	return p, err
}

// ProjectState is the state Jev sees: the project's about fields and its file tree.
// The tree is listed on every run, so it never goes stale.
func ProjectState(dir string, p Project) (map[string]any, error) {
	// This sends the whole tree. Cap it if a large project starts to dilute answers.
	out, err := gitcmd.Git(dir, append([]string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."}, gitcmd.Excludes(p.Exclude)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": p.About, "tree": gitcmd.SplitNUL(out)}, nil
}

// MaxStyleBytes caps a coding_style document, so a large file cannot flood a request.
const MaxStyleBytes = 65536

// LoadStyles reads each check's coding_style document from dir's working tree, once per path.
// It returns each check's normalized path, empty for none, and the contents by path.
func LoadStyles(dir string, checks []GateCheck) ([]string, map[string]string, error) {
	paths := make([]string, len(checks))
	var docs map[string]string
	for i, c := range checks {
		if c.CodingStyle == nil {
			continue
		}
		path, err := StylePath(dir, c.CodingStyle)
		if docs == nil {
			docs = map[string]string{}
		}
		if _, ok := docs[path]; err == nil && !ok {
			docs[path], err = ReadStyle(filepath.Join(dir, filepath.FromSlash(path)))
		}
		if err != nil {
			// The path may hold a secret, so neither it nor an OS error that repeats it is printed.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
			}
			return nil, nil, fmt.Errorf("check %s: coding_style %s: %w", c.Check, secretscan.SafeLabel(string(c.CodingStyle), "path (not shown)"), err)
		}
		paths[i] = path
	}
	return paths, docs, nil
}

// StylePath checks that raw names a regular file inside dir, reached without
// symlinks or .., and returns it with forward slashes.
func StylePath(dir string, raw json.RawMessage) (string, error) {
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

// ReadStyle reads a document of at most maxStyleBytes of UTF-8 text, exactly as it is.
func ReadStyle(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxStyleBytes+1))
	switch {
	case err != nil:
		return "", err
	case len(data) > MaxStyleBytes:
		return "", fmt.Errorf("is larger than %d bytes", MaxStyleBytes)
	case !utf8.Valid(data):
		return "", errors.New("is not valid UTF-8")
	case bytes.IndexByte(data, 0) >= 0:
		return "", errors.New("holds a NUL byte")
	case strings.TrimSpace(string(data)) == "":
		return "", errors.New("is empty")
	}
	return string(data), nil
}

// AddStyle puts a check's document into its request state, when it has one.
func AddStyle(state map[string]any, path string, docs map[string]string) {
	if path != "" {
		state["coding_style"] = map[string]string{"path": path, "content": docs[path]}
	}
}

// GateCheck is one entry of "checks" in config.json.
type GateCheck struct {
	Check       string             `json:"check"`
	Threshold   *float64           `json:"threshold"`
	PerQuestion map[string]float64 `json:"per_question"`
	Skip        []string           `json:"skip"`
	// CodingStyle is the raw coding_style value, so null can be told apart from a missing field.
	CodingStyle json.RawMessage `json:"coding_style"`
}

// limit is the threshold for question q: its per_question value, else the check's threshold.
func (c GateCheck) Limit(q string) float64 {
	if t, ok := c.PerQuestion[q]; ok {
		return t
	}
	return *c.Threshold
}

// FileState is a copy of state with one file's patch and the check's coding_style document.
// Each patch is named after its file, so the state key tells Jev which file it reads.
func FileState(state map[string]any, file, patch, stylePath string, styles map[string]string) map[string]any {
	out := maps.Clone(state)
	out["files"] = map[string]string{file + ".patch": patch}
	AddStyle(out, stylePath, styles)
	return out
}

// JevDir holds every file jev-check reads or writes in a project, relative to the project.
const JevDir = ".jev-check"
