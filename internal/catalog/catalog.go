package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// A check name is a plain word, so it cannot reach files outside input/.
var CheckName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Check is a loaded check. path names the question file in errors, raw holds its bytes,
// and state is its default state, nil when there is none.
type Check struct {
	Name, path string
	Raw, State []byte
	Questions  map[string]json.RawMessage
}

// FindCheck reads a check by path, or by name from project/.jev-check/input/questions/<name>.json
// with its optional default state from input/states/. It never reads the bundle: a project runs
// only the checks it holds.
func FindCheck(project, arg string) (Check, error) {
	c := Check{Name: strings.TrimSuffix(filepath.Base(arg), ".json"), path: arg}
	var err error
	switch {
	case strings.HasSuffix(arg, ".json"):
		c.Raw, err = os.ReadFile(arg)
	case !CheckName.MatchString(arg):
		return c, errors.New("check names use only letters, digits, - and _")
	default:
		c.path = InputPath(project, "questions", arg)
		if c.Raw, err = os.ReadFile(c.path); err == nil {
			c.State, err = os.ReadFile(InputPath(project, "states", arg))
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		}
	}
	if errors.Is(err, fs.ErrNotExist) && c.path != arg {
		return c, MissingCheck(project, c.Name)
	} else if errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("no check file %s", arg)
	} else if err != nil {
		return c, err
	}
	c.Questions, err = ParseQuestions(c.path, c.Raw)
	return c, err
}

func ParseQuestions(path string, data []byte) (map[string]json.RawMessage, error) {
	var check struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(data, &check); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	if len(check.Questions) == 0 {
		return nil, fmt.Errorf(`%s needs a non-empty "questions" object`, path)
	}
	return check.Questions, nil
}

// BundleDir is where this repository keeps the published checks. It is tracked,
// unlike the repository's own .jev-check/, so a clone can build the bundle.
const BundleDir = ".jev-check-example"

// Bundled is embedded as an fs.FS. go:embed only reads the package's own folder,
// and moving the folder would change paths that the docs and checks name.
var Bundled fs.FS

func BundledPath(kind, name string) string {
	return BundleDir + "/input/" + kind + "/" + name + ".json"
}

// InputDir returns dir/.jev-check/input/<kind>, the folder that holds a project's checks or states.
func InputDir(dir, kind string) string {
	return filepath.Join(dir, workspace.JevDir, "input", kind)
}

// InputPath returns dir/.jev-check/input/<kind>/<name>.json.
func InputPath(dir, kind, name string) string {
	return filepath.Join(InputDir(dir, kind), name+".json")
}

// BundledNames returns the names of the bundled checks, sorted.
func BundledNames() []string {
	files, _ := fs.Glob(Bundled, BundleDir+"/input/questions/*.json")
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(path.Base(f), ".json")
	}
	slices.Sort(names)
	return names
}

func IsBundled(name string) bool { return slices.Contains(BundledNames(), name) }

// MissingCheck explains a named check that project/.jev-check/input/ does not hold.
func MissingCheck(project, name string) error {
	file := InputPath(project, "questions", name)
	if !IsBundled(name) {
		return fmt.Errorf("no check %q: %s does not exist (run jev-check list)", name, file)
	}
	add := "jev-check add " + name
	if cwd, _ := filepath.Abs("."); cwd != project {
		add = "jev-check add --dir " + fsutil.ShellQuote(project) + " " + name
	}
	return fmt.Errorf("no check %q: %s does not exist; copy the bundled one with: %s", name, file, add)
}

// AddChecks copies the named bundled checks into dir/.jev-check/input/, keeping any file already there.
// It checks every name before it writes anything.
func AddChecks(dir string, names []string, stdout io.Writer) error {
	for _, name := range names {
		if !CheckName.MatchString(name) {
			return fmt.Errorf("%q: check names use only letters, digits, - and _", name)
		}
		if !IsBundled(name) {
			return fmt.Errorf("no bundled check %q (run jev-check list)", name)
		}
	}
	for _, name := range names {
		for _, kind := range []string{"questions", "states"} {
			data, err := fs.ReadFile(Bundled, BundledPath(kind, name))
			if errors.Is(err, fs.ErrNotExist) && kind == "states" {
				continue
			} else if err != nil {
				return err
			}
			if err := fsutil.InstallFile(InputPath(dir, kind, name), string(data), stdout); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateChecks checks the gate config before any API call and returns each check's questions.
func ValidateChecks(project string, checks []workspace.GateCheck) ([]map[string]json.RawMessage, error) {
	if len(checks) == 0 {
		return nil, errors.New(`needs a "checks" list, each with "check" and "threshold"`)
	}
	var all []map[string]json.RawMessage
	for _, c := range checks {
		if !CheckName.MatchString(c.Check) {
			return nil, fmt.Errorf("check %q must be the name of a check (run jev-check list)", c.Check)
		}
		if c.Threshold == nil || *c.Threshold < 0 || *c.Threshold > 1 {
			return nil, fmt.Errorf("check %s needs a threshold from 0 to 1", c.Check)
		}
		loaded, err := FindCheck(project, c.Check)
		if err != nil {
			return nil, err
		}
		for id, t := range c.PerQuestion {
			if _, ok := loaded.Questions[id]; !ok {
				return nil, fmt.Errorf("check %s has no question %s", c.Check, id)
			}
			if t < 0 || t > 1 {
				return nil, fmt.Errorf("check %s: threshold for %s must be from 0 to 1", c.Check, id)
			}
		}
		all = append(all, loaded.Questions)
	}
	return all, nil
}
