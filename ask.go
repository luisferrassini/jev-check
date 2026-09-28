package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const askUsage = `Usage:
  jev-check ask <check-name> [state.json] [--file PATH]... [options]
  jev-check ask <questions.json> [state.json] [--file PATH]... [options]

A check name reads .jev-check/input/questions/<name>.json and, if it exists,
.jev-check/input/states/<name>.json, from the current directory. Copy a bundled
check there first with jev-check add <name>. A first argument ending in .json
is a path instead. The API key, endpoint, and model come from ./.jev-check/.env (see
jev-check doctor). Answers go to ./.jev-check/output/.
Each --file PATH adds that file to the state as files[PATH] = <content>.

Options:
  --threshold N  exit 1 when any yes/no (noul) answer is below N (0 to 1)
  --model ID     model to use (default: JEV_CHECK_MODEL, else jev-latest)
  --dry-run      print the request and exit, without calling the API

Before anything is printed or sent, the whole request is scanned for secrets.
A finding prints SECRET lines instead, and nothing is sent or saved.

Exit codes: 0 ok, 1 below threshold or a secret found, 2 usage or API error.
`

// A check name is a plain word, so it cannot reach files outside input/.
var checkName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type request struct {
	Model     string                     `json:"model"`
	Questions map[string]json.RawMessage `json:"questions"`
	State     map[string]any             `json:"state"`
}

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

// answer is a noul (the probability that the answer is yes), a choice, or a score.
type answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Score         any                `json:"score"`
}

const listUsage = `Usage: jev-check list [DIR]   (default: .)
Lists the checks in DIR/.jev-check/input/questions/, which ask, gate, and eval
run, then the bundled checks DIR does not have yet. jev-check add <name> copies
a bundled check into DIR/.jev-check/input/.
`

func listCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, listUsage)
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	project := cmp.Or(append(args, ".")...)
	if info, err := os.Stat(project); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", project)
	}
	entries, err := os.ReadDir(filepath.Join(project, jevDir, "input", "questions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	var have []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if !checkName.MatchString(name) {
			return 0, fmt.Errorf("%s: check names use only letters, digits, - and _", filepath.Join(project, jevDir, "input", "questions", e.Name()))
		}
		c, err := findCheck(project, name)
		if err != nil {
			return 0, err
		}
		if _, err := parseQuestions(c.path, c.data); err != nil {
			return 0, err
		}
		state := "needs --file"
		if c.state != nil {
			state = "has default state"
		}
		printCheck(stdout, name, "project, "+state, c.data)
		have = append(have, name)
	}
	for _, name := range bundledNames() {
		if !slices.Contains(have, name) {
			data, _ := bundled.ReadFile(bundledPath("questions", name))
			printCheck(stdout, name, "bundled, not added: jev-check add "+name, data)
		}
	}
	return 0, nil
}

func printCheck(w io.Writer, name, tag string, data []byte) {
	var check struct{ Title, Description string }
	json.Unmarshal(data, &check)
	fmt.Fprintf(w, "%s [%s]\n  %s\n  %s\n\n", name, tag,
		cmp.Or(check.Title, "(no title)"), cmp.Or(check.Description, "(no description)"))
}

func askCmd(args []string, stdout, _ io.Writer) (int, error) {
	model, threshold, dryRun := "", -1.0, false
	var files, positional []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "-h", "--help":
			fmt.Fprint(stdout, askUsage)
			return 0, nil
		case "--dry-run":
			dryRun = true
		case "--model":
			m, err := modelFlag(args, i)
			if err != nil {
				return 0, err
			}
			model = m
			i++
		case "--file", "--threshold":
			if i+1 == len(args) {
				return 0, fmt.Errorf("%s needs a value", arg)
			}
			i++
			switch arg {
			case "--file":
				files = append(files, args[i])
			case "--threshold":
				t, err := parseThreshold(args[i])
				if err != nil {
					return 0, err
				}
				threshold = t
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return 0, fmt.Errorf("unknown option %s (see --help)", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return 0, errors.New("expected a check and an optional state file (see --help)")
	}

	// ask runs in the working directory: its .jev-check/input/, .jev-check/.env, and .jev-check/output/.
	project, err := filepath.Abs(".")
	if err != nil {
		return 0, err
	}
	cfg, err := loadSettings(project, model)
	if err != nil {
		return 0, err
	}
	name, questions, defaultState, err := loadCheck(project, positional[0])
	if err != nil {
		return 0, err
	}
	state := map[string]any{}
	switch {
	case len(positional) == 2:
		err = readJSON(positional[1], &state)
	case defaultState != nil:
		if err = json.Unmarshal(defaultState, &state); err != nil {
			err = fmt.Errorf("invalid JSON in the default state of %s: %w", name, err)
		}
	case len(files) == 0:
		err = fmt.Errorf("check %q has no default state; pass --file PATH", name)
	}
	if err != nil {
		return 0, err
	}
	if state == nil {
		return 0, errors.New("state must be an object, not null")
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		if state["files"] == nil {
			state["files"] = map[string]any{}
		}
		stateFiles, ok := state["files"].(map[string]any)
		if !ok {
			return 0, errors.New(`the state's "files" must be an object`)
		}
		stateFiles[path] = string(content)
	}

	req := request{Model: cfg.model, Questions: questions, State: state}
	if err := scanRequest(req); err != nil {
		return 0, err
	}
	if dryRun {
		return 0, writeJSON(stdout, req)
	}
	res, saved, err := callJev(project, name, cfg, req)
	if err != nil {
		return 0, err
	}
	failed := printVerdicts(stdout, res.Answers, threshold, nil)
	fmt.Fprintf(stdout, "model: %s  saved: %s\n", res.Model, saved)
	if failed {
		return 1, nil
	}
	return 0, nil
}

// foundCheck is a named check's question file and its optional default state.
// path names the question file in errors.
type foundCheck struct {
	path        string
	data, state []byte
}

// findCheck reads project/.jev-check/input/questions/<name>.json and its optional default state
// from input/states/. It never reads the bundle: a project runs only the checks it holds.
func findCheck(project, name string) (foundCheck, error) {
	read := func(kind string) ([]byte, error) {
		return os.ReadFile(filepath.Join(project, jevDir, "input", kind, name+".json"))
	}
	c := foundCheck{path: filepath.Join(project, jevDir, "input", "questions", name+".json")}
	data, err := read("questions")
	if err != nil {
		return c, err
	}
	c.data = data
	c.state, err = read("states")
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
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

// loadCheck reads a check by name from project/.jev-check/input/, or by path.
// It returns the check's name, its questions, and its default state, which is nil when there is none.
func loadCheck(project, arg string) (string, map[string]json.RawMessage, []byte, error) {
	name, path := strings.TrimSuffix(filepath.Base(arg), ".json"), arg
	var data, state []byte
	var err error
	switch {
	case strings.HasSuffix(arg, ".json"):
		data, err = os.ReadFile(arg)
	case !checkName.MatchString(arg):
		return "", nil, nil, errors.New("check names use only letters, digits, - and _")
	default:
		var c foundCheck
		c, err = findCheck(project, arg)
		path, data, state = c.path, c.data, c.state
	}
	if errors.Is(err, fs.ErrNotExist) && path != arg {
		return "", nil, nil, missingCheck(project, name)
	} else if errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil, fmt.Errorf("no check file %s", arg)
	} else if err != nil {
		return "", nil, nil, err
	}
	questions, err := parseQuestions(path, data)
	return name, questions, state, err
}

// validateAnswers rejects incomplete or invalid answers before they can pass a check.
// Saved outputs without a request can still be checked for valid answer values.
func validateAnswers(answers map[string]answer, questions map[string]json.RawMessage) error {
	if len(answers) == 0 {
		return errors.New("response needs a non-empty answers object")
	}
	for id, raw := range questions {
		var question struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &question); err != nil || question.Type == "" {
			return fmt.Errorf("question %s needs a type", id)
		}
		a, ok := answers[id]
		if !ok || a.Type != question.Type {
			return fmt.Errorf("question %s needs an answer of type %s", id, question.Type)
		}
	}
	for id, a := range answers {
		if questions != nil && questions[id] == nil {
			return fmt.Errorf("unexpected answer %s", id)
		}
		switch a.Type {
		case "noul":
			if a.Noul == nil || !(*a.Noul >= 0 && *a.Noul <= 1) {
				return fmt.Errorf("answer %s needs a noul number from 0 to 1", id)
			}
		case "choice":
			probability, ok := a.Probabilities[a.Choice]
			if !ok || !(probability >= 0 && probability <= 1) {
				return fmt.Errorf("answer %s needs a choice with a probability from 0 to 1", id)
			}
		case "score":
			if a.Score == nil {
				return fmt.Errorf("answer %s needs a score", id)
			}
		default:
			return fmt.Errorf("answer %s has unknown type %q", id, a.Type)
		}
	}
	return nil
}

// callJev sends a request to cfg's endpoint and saves it, with the response, under project/.jev-check/output/.
// It returns the response and the absolute saved path.
func callJev(project, name string, cfg settings, req request) (response, string, error) {
	var res response
	// Every path to the API passes here, so nothing that looks like a secret is sent.
	if err := scanRequest(req); err != nil {
		return res, "", err
	}
	if cfg.key == "" {
		return res, "", cfg.missingKey()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return res, "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, cfg.endpoint, bytes.NewReader(body))
	if err != nil {
		return res, "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cfg.key)
	httpReq.Header.Set("Content-Type", "application/json")
	// A redirect is returned as a 3xx error, so the key never follows it to another host.
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	httpRes, err := client.Do(httpReq)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	defer httpRes.Body.Close()
	raw, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	if httpRes.StatusCode/100 != 2 {
		return res, "", fmt.Errorf("API call failed: %s: %s", httpRes.Status, bytes.TrimSpace(raw))
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Answers == nil {
		return res, "", fmt.Errorf("unexpected API response: %s", bytes.TrimSpace(raw))
	}

	if err := validateAnswers(res.Answers, req.Questions); err != nil {
		return res, "", fmt.Errorf("unexpected API response: %w", err)
	}

	dir := filepath.Join(project, jevDir, "output")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, "", err
	}
	// CreateTemp adds a random suffix, so two runs in the same second never overwrite each other.
	f, err := os.CreateTemp(dir, time.Now().Format("2006-01-02_15-04-05")+"-"+name+"-*.json")
	if err != nil {
		return res, "", err
	}
	defer f.Close()
	err = writeJSON(f, struct {
		Request  request         `json:"request"`
		Response json.RawMessage `json:"response"`
	}{req, raw})
	if err == nil {
		err = f.Close()
	}
	return res, f.Name(), err
}
