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
	"time"
)

const gateUsage = `Usage: jev-check gate [DIR] [--no-cache] [--model ID]   (default: .)
Sends one patch per staged file to each check in
DIR/.jev-check/project-context.json. Answers are cached for 24 hours in
DIR/.jev-check/output/cache/v2/, keyed by the endpoint and the whole request:
model, questions, project fields, tree, patch, and coding_style. Any change misses; a threshold change does not. --no-cache
always calls the API and saves the new answers.
The 24 hours are a policy, not a guarantee: a moving model alias such as
jev-latest can change within them.
The key, endpoint, and model come from DIR/.jev-check/.env; --model ID
overrides its model (see jev-check doctor).
A check's optional "coding_style" names a document in DIR; its working-tree
contents go to that check as state.coding_style, even when exclude or skip
lists it. Every request is scanned for secrets first. A secret in the project fields or
tree stops the gate; one in a check's questions skips that check; one in a
patch skips that file.
Exit 0 pass, 1 fail or a secret found, 2 usage or API error.
`

// gateCheck is one entry of "checks" in project-context.json.
type gateCheck struct {
	Check       string             `json:"check"`
	Threshold   *float64           `json:"threshold"`
	PerQuestion map[string]float64 `json:"per_question"`
	Skip        []string           `json:"skip"`
	// CodingStyle is the raw coding_style value, so null can be told apart from a missing field.
	CodingStyle json.RawMessage `json:"coding_style"`
}

func gateCmd(args []string, stdout, stderr io.Writer) (int, error) {
	dir, model, noCache := "", "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case isHelp(arg):
			fmt.Fprint(stdout, gateUsage)
			return 0, nil
		case arg == "--no-cache":
			noCache = true
		case arg == "--model":
			m, err := modelFlag(args, i)
			if err != nil {
				return 0, err
			}
			model = m
			i++
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
	cfg, err := loadSettings(dir, model)
	if err != nil {
		return 0, err
	}
	p, err := loadProject(dir)
	if err != nil {
		return 0, err
	}
	questions, err := validateChecks(dir, p.Checks)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", configPath(dir), err)
	}
	stylePaths, styles, err := loadStyles(dir, p.Checks)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", configPath(dir), err)
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	// Shared content goes with every request, so a secret there stops the gate before any request.
	// A coding_style document counts as shared, even when one check uses it.
	err = styleSecrets(styles)
	if err == nil {
		err = scanRequest(request{Model: cfg.model, State: state})
	}
	if err != nil {
		fmt.Fprintf(stdout, "%v\ngate: FAIL\n", err)
		return 1, nil
	}
	// status keeps the worst result: 1 when a check fails or a secret is found, 2 on an error.
	status := 0
	// A check whose questions look like they hold a secret is skipped; the others still run.
	blocked := map[int]bool{}
	for i, c := range p.Checks {
		if err := scanRequest(request{Questions: questions[i]}); err != nil {
			fmt.Fprintf(stdout, "== %s questions\n%v\n", c.Check, err)
			blocked[i], status = true, 1
		}
	}
	files, err := stagedFiles(dir, p.Exclude)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 && status == 0 {
		fmt.Fprintln(stdout, "nothing staged")
		return 0, nil
	}
	// label names a file in output, unless its name could leak a secret or forge a line.
	label := func(file string) string {
		return safeLabel(file, fmt.Sprintf("staged file %d", slices.Index(files, file)+1))
	}

	patches := map[string]string{}
	for _, file := range files {
		patch, err := git(dir, "diff", "--cached", "--relative", "--", ":(literal)"+file)
		if err != nil {
			return 0, err
		}
		// A patch that looks like it holds a secret is never sent.
		if reports := secretReports(label(file)+".patch", patch, true); reports != nil {
			for _, report := range reports {
				fmt.Fprintln(stdout, report)
			}
			status = 1
			continue
		}
		patches[file] = patch
	}

	for i, c := range p.Checks {
		if blocked[i] {
			continue
		}
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
			addStyle(fileState, stylePaths[i], styles)
			res, cached, err := cachedJev(dir, c.Check, cfg, request{Model: cfg.model, Questions: questions[i], State: fileState}, noCache, stderr)
			var found secretsFound
			switch {
			case errors.As(err, &found):
				fmt.Fprintf(stdout, "== %s %s\n%v\n", c.Check, label(file), found)
				status = max(status, 1)
				continue
			case err != nil:
				fmt.Fprintf(stdout, "== %s %s\n", c.Check, label(file))
				fmt.Fprintf(stderr, "jev-check gate: %v\n", err)
				status = 2
				continue
			}
			fmt.Fprintf(stdout, "== %s %s%s\n", c.Check, label(file), map[bool]string{true: " (cached)"}[cached])
			if printVerdicts(stdout, res.Answers, *c.Threshold, c.PerQuestion) {
				status = max(status, 1)
			}
		}
	}

	fmt.Fprintln(stdout, "gate: "+[]string{"PASS", "FAIL", "ERROR"}[status])
	return status, nil
}

// cacheVersion names the cache key and entry format under output/cache/v2/.
// Entries in other folders, such as the unversioned ones in output/cache/, are never read.
const cacheVersion = 2

// cacheLifetime bounds reuse, because a model name such as jev-latest can change behind it.
// It is a policy, not proof that the model stayed the same.
const cacheLifetime = 24 * time.Hour

// cacheEntry is one file in output/cache/v2/.
type cacheEntry struct {
	Version   int      `json:"version"`
	CreatedAt string   `json:"created_at"`
	Response  response `json:"response"`
}

// fresh reports whether an entry created at created can be reused at now.
// A future time counts as unknown age, so it is not reused.
func fresh(created, now time.Time) bool {
	age := now.Sub(created)
	return age >= 0 && age < cacheLifetime
}

// cachedJev returns the answers from project/.jev-check/output/cache/v2/ when the same request went to the
// same endpoint less than cacheLifetime ago, else it calls Jev and caches the answers.
// The key is the whole request, so any change to the model, questions, or state misses.
// Thresholds are not in the request: the gate judges cached answers again on every run.
// noCache skips the lookup but still saves the new answers.
func cachedJev(project, name string, cfg settings, req request, noCache bool, stderr io.Writer) (response, bool, error) {
	// Scan before the cache, so an old answer never hides a secret.
	if err := scanRequest(req); err != nil {
		return response{}, false, err
	}
	// json.Marshal sorts map keys, so the same request always gives the same key.
	key, err := json.Marshal(struct {
		Version  int     `json:"version"`
		Endpoint string  `json:"endpoint"`
		Request  request `json:"request"`
	}{cacheVersion, cfg.endpoint, req})
	if err != nil {
		return response{}, false, err
	}
	sum := sha256.Sum256(key)
	path := filepath.Join(project, jevDir, "output", "cache", "v2", hex.EncodeToString(sum[:])+".json")
	var entry cacheEntry
	if !noCache && readJSON(path, &entry) == nil && entry.Version == cacheVersion {
		created, err := time.Parse(time.RFC3339, entry.CreatedAt)
		if err == nil && fresh(created, time.Now()) && validateAnswers(entry.Response.Answers, req.Questions) == nil {
			return entry.Response, true, nil
		}
	}
	res, _, err := callJev(project, name, cfg, req)
	if err != nil {
		return res, false, err
	}
	// A failed cache write only costs an API call next time, so it warns and keeps the answer.
	entry = cacheEntry{cacheVersion, time.Now().UTC().Format(time.RFC3339), res}
	if err := writeCache(path, entry); err != nil {
		// gate and eval both call here, so the warning names neither.
		fmt.Fprintf(stderr, "jev-check: cache not saved: %v\n", err)
	}
	return res, false, nil
}

// writeCache writes entry to a temporary file and renames it to path,
// so a reader never sees half an entry.
func writeCache(path string, entry cacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
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
		loaded, err := findCheck(project, c.Check)
		if err != nil {
			return nil, err
		}
		for id, t := range c.PerQuestion {
			if _, ok := loaded.questions[id]; !ok {
				return nil, fmt.Errorf("check %s has no question %s", c.Check, id)
			}
			if t < 0 || t > 1 {
				return nil, fmt.Errorf("check %s: threshold for %s must be from 0 to 1", c.Check, id)
			}
		}
		all = append(all, loaded.questions)
	}
	return all, nil
}

// stagedFiles lists the staged files in dir, minus the exclude pathspecs.
// A git error stops the gate, so a failure never looks like "nothing staged".
func stagedFiles(dir string, exclude []string) ([]string, error) {
	out, err := git(dir, append([]string{"diff", "--cached", "--relative", "--name-only", "-z", "--", "."}, excludes(exclude)...)...)
	return splitNUL(out), err
}
