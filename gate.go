package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const gateUsage = `Usage: jev-check gate [DIR] [--no-cache]   (default: .)
Sends one patch per staged file to each check in DIR/project-context.json.
Answers are cached in DIR/output/cache/ by model, questions, project, and patch,
so an unchanged file is not sent twice. --no-cache always calls the API.
Exit 0 pass, 1 fail, 2 usage or API error.
`

// gateCheck is one entry of "checks" in project-context.json.
type gateCheck struct {
	Check       string             `json:"check"`
	Threshold   *float64           `json:"threshold"`
	PerQuestion map[string]float64 `json:"per_question"`
	Skip        []string           `json:"skip"`
}

func gateCmd(args []string, stdout, stderr io.Writer) (int, error) {
	dir, noCache := "", false
	for _, arg := range args {
		switch {
		case isHelp(arg):
			fmt.Fprint(stdout, gateUsage)
			return 0, nil
		case arg == "--no-cache":
			noCache = true
		case strings.HasPrefix(arg, "-"):
			return 0, fmt.Errorf("unknown option %s (see --help)", arg)
		case dir != "":
			return 0, errors.New("expected at most one DIR")
		default:
			dir = arg
		}
	}
	dir, err := filepath.Abs(cmp.Or(dir, "."))
	if err != nil {
		return 0, err
	}
	p, err := loadProject(dir)
	if err != nil {
		return 0, err
	}
	questions, err := validateChecks(dir, p.Checks)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", filepath.Join(dir, "project-context.json"), err)
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	files, err := stagedFiles(dir, p.Exclude)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "nothing staged")
		return 0, nil
	}

	// status keeps the worst result: 1 when a check fails, 2 on an error.
	status := 0
	patches := map[string]string{}
	for _, file := range files {
		patch, err := git(dir, "diff", "--cached", "--relative", "--", ":(literal)"+file)
		if err != nil {
			return 0, err
		}
		// A patch that looks like it holds a secret is never sent.
		if reports := scanSecrets(file+".patch", patch); reports != nil {
			for _, report := range reports {
				fmt.Fprintln(stdout, report)
			}
			status = 1
			continue
		}
		patches[file] = patch
	}

	for i, c := range p.Checks {
		checkFiles, err := stagedFiles(dir, append(slices.Clone(p.Exclude), c.Skip...))
		if err != nil {
			return 0, err
		}
		for _, file := range checkFiles {
			patch, ok := patches[file]
			if !ok {
				continue
			}
			// Each patch is named after its file, so the state key tells Jev which file it reads.
			fileState := maps.Clone(state)
			fileState["files"] = map[string]string{file + ".patch": patch}
			res, cached, err := cachedJev(dir, c.Check, request{Model: defaultModel, Questions: questions[i], State: fileState}, noCache, stderr)
			if err != nil {
				fmt.Fprintf(stdout, "== %s %s\n", c.Check, file)
				fmt.Fprintf(stderr, "jev-check gate: %v\n", err)
				status = 2
				continue
			}
			fmt.Fprintf(stdout, "== %s %s%s\n", c.Check, file, map[bool]string{true: " (cached)"}[cached])
			if printVerdicts(stdout, res.Answers, *c.Threshold, c.PerQuestion) {
				status = max(status, 1)
			}
		}
	}

	fmt.Fprintln(stdout, "gate: "+[]string{"PASS", "FAIL", "ERROR"}[status])
	return status, nil
}

// cachedJev returns the answers from project/output/cache/ when the same model, questions,
// project, and patch were asked before, else it calls Jev and caches the answers.
// The tree is left out of the key, so adding a file does not miss the cache for the others.
// Thresholds are not in the key either: the gate judges cached answers again on every run.
func cachedJev(project, name string, req request, noCache bool, stderr io.Writer) (response, bool, error) {
	keyState := maps.Clone(req.State)
	delete(keyState, "tree")
	key, err := json.Marshal(request{Model: req.Model, Questions: req.Questions, State: keyState})
	if err != nil {
		return response{}, false, err
	}
	sum := sha256.Sum256(key)
	path := filepath.Join(project, "output", "cache", hex.EncodeToString(sum[:])+".json")
	var res response
	if !noCache && readJSON(path, &res) == nil && validateAnswers(res.Answers, req.Questions) == nil {
		return res, true, nil
	}
	res, _, err = callJev(project, name, req)
	if err != nil {
		return res, false, err
	}
	// A failed cache write only costs an API call next time, so it warns and keeps the answer.
	data, err := json.Marshal(res)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "jev-check gate: cache not saved: %v\n", err)
	}
	return res, false, nil
}

// validateChecks checks the gate config before any API call and returns each check's questions.
func validateChecks(project string, checks []gateCheck) ([]map[string]json.RawMessage, error) {
	if len(checks) == 0 {
		return nil, errors.New(`needs a "checks" list, each with "check" and "threshold"`)
	}
	var all []map[string]json.RawMessage
	for _, c := range checks {
		if !checkName.MatchString(c.Check) {
			return nil, fmt.Errorf("check %q must be the name of a check (run jev-check list)", c.Check)
		}
		if c.Threshold == nil || *c.Threshold < 0 || *c.Threshold > 1 {
			return nil, fmt.Errorf("check %s needs a threshold from 0 to 1", c.Check)
		}
		_, questions, _, err := loadCheck(project, c.Check)
		if err != nil {
			return nil, err
		}
		for id, t := range c.PerQuestion {
			if _, ok := questions[id]; !ok {
				return nil, fmt.Errorf("check %s has no question %s", c.Check, id)
			}
			if t < 0 || t > 1 {
				return nil, fmt.Errorf("check %s: threshold for %s must be from 0 to 1", c.Check, id)
			}
		}
		all = append(all, questions)
	}
	return all, nil
}

// stagedFiles lists the staged files in dir, minus the exclude pathspecs.
// A git error stops the gate, so a failure never looks like "nothing staged".
func stagedFiles(dir string, exclude []string) ([]string, error) {
	out, err := git(dir, append([]string{"diff", "--cached", "--relative", "--name-only", "-z", "--", "."}, excludes(exclude)...)...)
	return splitNUL(out), err
}
