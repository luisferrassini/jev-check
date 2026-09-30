package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const evalUsage = `Usage: jev-check eval <check> [DIR] [--no-cache] [--model ID]   (default DIR: .)
Tests a check's thresholds in DIR/.jev-check/project-context.json against
DIR/.jev-check/fixtures/<check>/: every patch in pass/ must pass every yes/no
question, and every patch in fail/<question>/ must fail that question. pass/ and fail/<question>/ for every
yes/no question each need at least one patch. A probability at or above the
threshold passes.

Each fixture is one single-file patch from git, such as
  git diff --cached --relative -- <file>
It is sent under the path in its headers: the new path, or the old path of a
deleted file. Binary, mode-only, and multi-file patches are refused.

Every fixture is read, parsed, and scanned for secrets before any is sent.
Answers share the gate's cache: 24 hours, keyed by the endpoint and the whole
request. A moving model alias can change within that time. --no-cache always
calls the API and saves the new answers. The key, endpoint, and model come from
DIR/.jev-check/.env; --model ID overrides its model (see jev-check doctor).
Exit 0 no misses, 1 a miss or a secret found, 2 usage, fixture, or API error.
`

func evalCmd(args []string, stdout, stderr io.Writer) (int, error) {
	var positional []string
	model, noCache := "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case isHelp(arg):
			fmt.Fprint(stdout, evalUsage)
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
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return 0, errors.New("expected a check and an optional DIR (see --help)")
	}
	name := positional[0]
	dir, err := filepath.Abs(cmp.Or(append(positional[1:], ".")...))
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
	i := slices.IndexFunc(p.Checks, func(c gateCheck) bool { return c.Check == name })
	if i < 0 {
		return 0, fmt.Errorf("%s has no check %s", configPath(dir), name)
	}
	c := p.Checks[i]
	questions, err := validateChecks(dir, []gateCheck{c})
	if err != nil {
		return 0, err
	}
	// Only yes/no questions have thresholds, so each needs its own fail fixtures.
	blocking := noulIDs(questions[0])
	if len(blocking) == 0 {
		return 0, fmt.Errorf("check %s has no yes/no (noul) question, so there is no threshold to evaluate", name)
	}
	stylePaths, styles, err := loadStyles(dir, []gateCheck{c})
	if err != nil {
		return 0, err
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	fixtures := filepath.Join(dir, jevDir, "fixtures", name)
	jobs, err := findFixtures(fixtures, blocking)
	if err != nil {
		return 0, err
	}

	// Read, parse, and scan every fixture before the first cache lookup or API call,
	// so a bad fixture late in the list never lets earlier ones through.
	for i, job := range jobs {
		patch, err := os.ReadFile(filepath.Join(fixtures, job.rel))
		if err != nil {
			return 0, errors.New(printable(err.Error()))
		}
		// The patch is sent under its target path, as the gate sends it, so the fixture folder never reaches Jev.
		file, err := patchPath(string(patch))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", printable(filepath.Join(fixtures, job.rel)), err)
		}
		jobs[i].req = request{Model: cfg.model, Questions: questions[0], State: fileState(state, file, string(patch), stylePaths[0], styles)}
	}
	// A secret in the shared document is reported once, not once per fixture.
	var blocked []string
	holder := "a fixture"
	if err := styleSecrets(styles); err != nil {
		blocked = append(blocked, err.Error())
		holder = "the coding_style document"
	} else {
		for _, job := range jobs {
			if err := scanRequest(job.req); err != nil {
				blocked = append(blocked, fmt.Sprintf("== %s\n%v", safeLabel(job.rel, "a fixture"), err))
			}
		}
	}
	if blocked != nil {
		fmt.Fprintf(stdout, "%s\neval: BLOCKED, %s looks like it holds a secret; nothing was sent\n", strings.Join(blocked, "\n"), holder)
		return 1, nil
	}
	// A probability at or above its threshold passes, as in the gate.
	misses, positives, negatives := 0, 0, map[string]int{}
	lowestPass, highestFail := map[string]float64{}, map[string]float64{}
	for _, job := range jobs {
		res, _, err := cachedJev(dir, name, cfg, job.req, noCache, stderr)
		if err != nil {
			return 0, err
		}
		if job.question != "" {
			a := *res.Answers[job.question].Noul
			negatives[job.question]++
			highestFail[job.question] = max(highestFail[job.question], a)
			if a >= c.limit(job.question) {
				fmt.Fprintf(stdout, "MISS  %s  %s  %s passes it\n", formatFloat(a), job.question, printable(job.rel))
				misses++
			}
			continue
		}
		positives++
		for _, q := range blocking {
			a := *res.Answers[q].Noul
			if low, ok := lowestPass[q]; !ok || a < low {
				lowestPass[q] = a
			}
			if a < c.limit(q) {
				fmt.Fprintf(stdout, "MISS  %s  %s  %s fails it\n", formatFloat(a), q, printable(job.rel))
				misses++
			}
		}
	}

	fmt.Fprintln(stdout, "positive  negative  question")
	for _, q := range blocking {
		fmt.Fprintf(stdout, "%-8d  %-8d  %s\n", positives, negatives[q], q)
	}
	// A question separates its fixtures when its highest fail is below its lowest pass.
	fmt.Fprintln(stdout, "lowest-pass  highest-fail  threshold  question")
	for _, q := range blocking {
		fmt.Fprintf(stdout, "%-11s  %-12s  %-9s  %s\n", formatFloat(lowestPass[q]), formatFloat(highestFail[q]), formatFloat(c.limit(q)), q)
	}
	fmt.Fprintf(stdout, "eval: %d misses in %d fixtures\n", misses, len(jobs))
	return min(misses, 1), nil
}

// fixture is one patch to evaluate. question is empty for a pass fixture.
type fixture struct {
	rel, question string
	req           request
}

// findFixtures lists pass/*.patch and fail/<question>/*.patch, sorted, pass first.
// Every fail folder must name a blocking question, and the pass set and every
// blocking question's fail set must be non-empty. Missing sets are named together.
func findFixtures(fixtures string, blocking []string) ([]fixture, error) {
	var jobs []fixture
	add := func(rel, question string) error {
		entries, err := os.ReadDir(filepath.Join(fixtures, rel))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errors.New(printable(err.Error()))
		}
		for _, e := range entries {
			name := filepath.Join(rel, e.Name())
			if !strings.HasSuffix(e.Name(), ".patch") {
				continue
			}
			if !e.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", printable(filepath.Join(fixtures, name)))
			}
			jobs = append(jobs, fixture{rel: name, question: question})
		}
		return nil
	}
	if err := add("pass", ""); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(fixtures, "fail"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New(printable(err.Error()))
	}
	for _, e := range entries {
		path := printable(filepath.Join(fixtures, "fail", e.Name()))
		switch {
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".patch"):
			return nil, fmt.Errorf("%s: a fail fixture goes in fail/<question>/", path)
		case !e.IsDir():
		case !slices.Contains(blocking, e.Name()):
			return nil, fmt.Errorf("%s: fail folders must name a yes/no question of the check (%s)", path, strings.Join(blocking, ", "))
		default:
			if err := add(filepath.Join("fail", e.Name()), e.Name()); err != nil {
				return nil, err
			}
		}
	}
	var missing []string
	if !slices.ContainsFunc(jobs, func(f fixture) bool { return f.question == "" }) {
		missing = append(missing, "pass/")
	}
	for _, q := range blocking {
		if !slices.ContainsFunc(jobs, func(f fixture) bool { return f.question == q }) {
			missing = append(missing, "fail/"+q+"/")
		}
	}
	if missing != nil {
		return nil, fmt.Errorf("%s needs patches in %s", printable(fixtures), strings.Join(missing, ", "))
	}
	return jobs, nil
}

// patchPath returns the project path a single-file git patch changes: the new path,
// or the old one for a deletion. It reads only the headers before the first hunk,
// so a hunk line that looks like a header never counts.
func patchPath(patch string) (string, error) {
	lines := strings.Split(patch, "\n")
	if n := len(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return !strings.HasPrefix(l, "diff --git ") })); n != 1 {
		return "", fmt.Errorf("needs exactly one diff --git file section, found %d; make one fixture per file", n)
	}
	h := map[string]string{}
	var hunks []string
	started := false
	for i, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			started = true
		}
		if !started {
			continue
		}
		if strings.HasPrefix(l, "@@") {
			hunks = lines[i:]
			break
		}
		if strings.HasSuffix(l, "\r") {
			return "", errors.New("has a header line that ends in a carriage return; save the patch with LF line endings")
		}
		if strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch" {
			return "", errors.New("binary patches are not supported")
		}
		for _, key := range []string{"--- ", "+++ ", "rename from ", "rename to "} {
			if v, ok := strings.CutPrefix(l, key); ok {
				if _, dup := h[key]; dup {
					return "", fmt.Errorf("has two %q lines", strings.TrimSpace(key))
				}
				h[key] = v
			}
		}
	}
	if err := checkHunks(hunks); err != nil {
		return "", err
	}
	_, hasMinus := h["--- "]
	_, hasPlus := h["+++ "]
	_, hasFrom := h["rename from "]
	_, hasTo := h["rename to "]
	switch {
	case hasFrom != hasTo:
		return "", errors.New("needs both rename from and rename to")
	case hasMinus != hasPlus:
		return "", errors.New("needs both a --- and a +++ line")
	case !hasPlus && !hasTo:
		return "", errors.New("has no ---/+++ or rename lines; binary, mode-only, and other formats are not supported")
	}
	// Every path header is decoded and checked, even the ones that are not sent.
	paths := map[string]string{}
	for _, key := range []string{"--- ", "+++ ", "rename from ", "rename to "} {
		v, ok := h[key]
		prefix := map[string]string{"--- ": "a/", "+++ ": "b/"}[key]
		if !ok || v == "/dev/null" && prefix != "" {
			continue
		}
		path, err := decodePath(v, prefix)
		if err != nil {
			return "", err
		}
		if path == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") || !utf8.ValidString(path) ||
			slices.Contains(strings.Split(path, "/"), "..") {
			return "", fmt.Errorf("has an unsafe or unsupported path %q", path)
		}
		paths[key] = path
	}
	oldPath, newPath := paths["--- "], paths["+++ "]
	switch {
	case hasPlus && oldPath == "" && newPath == "":
		return "", errors.New("has /dev/null on both sides")
	case hasTo && hasPlus && (paths["rename from "] != oldPath || paths["rename to "] != newPath):
		return "", errors.New("its rename lines and its ---/+++ lines disagree")
	case !hasTo && oldPath != "" && newPath != "" && oldPath != newPath:
		return "", errors.New("its --- and +++ lines name different paths without rename lines")
	}
	return cmp.Or(newPath, oldPath, paths["rename to "]), nil
}

// checkHunks follows each hunk's line counts, so a --- or +++ line after a hunk ends
// is found as a second file section without its own diff --git line.
func checkHunks(lines []string) error {
	oldLeft, newLeft := 0, 0
	for _, l := range lines {
		switch {
		case oldLeft > 0 || newLeft > 0:
			switch {
			case strings.HasPrefix(l, "-"):
				oldLeft--
			case strings.HasPrefix(l, "+"):
				newLeft--
			case strings.HasPrefix(l, `\`):
			default:
				oldLeft--
				newLeft--
			}
			if oldLeft < 0 || newLeft < 0 {
				return errors.New("has a hunk longer than its @@ header says")
			}
		case strings.HasPrefix(l, "@@"):
			f := strings.Fields(l)
			if len(f) < 4 || f[0] != "@@" || f[3] != "@@" {
				return errors.New("has an invalid @@ hunk header")
			}
			var err error
			if oldLeft, err = hunkCount(f[1], "-"); err == nil {
				newLeft, err = hunkCount(f[2], "+")
			}
			if err != nil {
				return err
			}
		case strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ "):
			return errors.New("has a second file section without a diff --git line; make one fixture per file")
		}
	}
	return nil
}

// hunkCount returns the line count of one @@ range, such as -3,4 or +5 (one line).
func hunkCount(field, sign string) (int, error) {
	field, ok := strings.CutPrefix(field, sign)
	start, count, hasCount := strings.Cut(field, ",")
	if !hasCount {
		count = "1"
	}
	_, startErr := strconv.Atoi(start)
	n, err := strconv.Atoi(count)
	if !ok || startErr != nil || err != nil || n < 0 {
		return 0, errors.New("has an invalid @@ hunk header")
	}
	return n, nil
}

// decodePath decodes one path field of a git header and strips its a/ or b/ prefix.
// Git ends an unquoted path that holds a space with a tab, and quotes any path with
// a tab, quote, backslash, control, or (by default) non-ASCII character.
func decodePath(field, prefix string) (string, error) {
	field = strings.TrimSuffix(field, "\t")
	if strings.HasPrefix(field, `"`) {
		// Git writes only C escapes and three-digit octal bytes, fewer than strconv.Unquote accepts.
		for i := 0; i < len(field); i++ {
			if field[i] != '\\' {
				continue
			}
			i++
			switch {
			case i < len(field) && strings.IndexByte(`abtnvfr"\`, field[i]) >= 0:
			case i+3 <= len(field) && strings.Trim(field[i:i+3], "01234567") == "":
				i += 2
			default:
				return "", errors.New("has an invalid quoted path")
			}
		}
		unquoted, err := strconv.Unquote(field)
		if err != nil {
			return "", errors.New("has an invalid quoted path")
		}
		field = unquoted
	}
	path, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return "", fmt.Errorf("has a path without the %s prefix", prefix)
	}
	return path, nil
}

// printable quotes s when it holds a control character, so a file name cannot forge an output line.
func printable(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return strconv.Quote(s)
	}
	return s
}
