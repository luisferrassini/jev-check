// Package catalog finds a project's checks and copies bundled checks into a project.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

// Find reads a check by path, or by name from project/.jev-check/input/questions/<name>.json
// with its optional default state from input/states/. It never reads the bundle: a project runs
// only the checks it holds.
func Find(project, arg string) (Check, error) {
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
		return c, missingCheck(project, c.Name)
	} else if errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("no check file %s", arg)
	} else if err != nil {
		return c, err
	}
	c.Questions, err = parseQuestions(c.path, c.Raw)
	return c, err
}

func parseQuestions(path string, data []byte) (map[string]json.RawMessage, error) {
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

// Validate checks the gate config before any API call and returns each check's questions.
func Validate(project string, checks []workspace.GateCheck) ([]map[string]json.RawMessage, error) {
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
		loaded, err := Find(project, c.Check)
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
