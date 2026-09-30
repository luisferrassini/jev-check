package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
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

// JevDir holds every file jev-check reads or writes in a project, relative to the project.
const JevDir = ".jev-check"
