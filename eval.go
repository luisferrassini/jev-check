package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const evalUsage = `Usage: jev-check eval <check> [DIR] [--no-cache]   (default DIR: .)
Tests a check's thresholds in DIR/project-context.json against DIR/fixtures/<check>/:
every patch in pass/ must pass every question, and every patch in fail/<question>/
must fail that question. Answers share the gate's cache.
Exit 0 no misses, 1 a miss, 2 usage or API error.
`

func evalCmd(args []string, stdout, stderr io.Writer) (int, error) {
	var positional []string
	noCache := false
	for _, arg := range args {
		switch {
		case isHelp(arg):
			fmt.Fprint(stdout, evalUsage)
			return 0, nil
		case arg == "--no-cache":
			noCache = true
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
	p, err := loadProject(dir)
	if err != nil {
		return 0, err
	}
	i := slices.IndexFunc(p.Checks, func(c gateCheck) bool { return c.Check == name })
	if i < 0 {
		return 0, fmt.Errorf("%s has no check %s", filepath.Join(dir, "project-context.json"), name)
	}
	c := p.Checks[i]
	questions, err := validateChecks([]gateCheck{c})
	if err != nil {
		return 0, err
	}
	state, err := projectState(dir, p)
	if err != nil {
		return 0, err
	}
	fixtures := filepath.Join(dir, "fixtures", name)
	pass, _ := filepath.Glob(filepath.Join(fixtures, "pass", "*.patch"))
	fail, _ := filepath.Glob(filepath.Join(fixtures, "fail", "*", "*.patch"))
	if len(pass) == 0 || len(fail) == 0 {
		return 0, fmt.Errorf("%s needs patches in pass/ and in fail/<question>/", fixtures)
	}
	for _, path := range fail {
		if q := filepath.Base(filepath.Dir(path)); questions[0][q] == nil {
			return 0, fmt.Errorf("%s: check %s has no question %s", path, name, q)
		}
	}

	ask := func(path string) (response, error) {
		patch, err := os.ReadFile(path)
		if err != nil {
			return response{}, err
		}
		// The patch is sent under its own file name, as the gate sends it, so the fixture folder never reaches Jev.
		file, ok := patchFile(string(patch))
		if !ok {
			return response{}, fmt.Errorf("%s has no +++ b/<path> line", path)
		}
		fileState := maps.Clone(state)
		fileState["files"] = map[string]string{file + ".patch": string(patch)}
		res, _, err := cachedJev(name, request{Model: defaultModel, Questions: questions[0], State: fileState}, noCache, stderr)
		return res, err
	}
	limit := func(q string) float64 {
		if t, ok := c.PerQuestion[q]; ok {
			return t
		}
		return *c.Threshold
	}

	misses := 0
	lowestPass, highestFail := map[string]float64{}, map[string]float64{}
	for _, path := range pass {
		res, err := ask(path)
		if err != nil {
			return 0, err
		}
		for q, a := range res.Answers {
			if a.Type != "noul" {
				continue
			}
			if low, ok := lowestPass[q]; !ok || *a.Noul < low {
				lowestPass[q] = *a.Noul
			}
			if *a.Noul < limit(q) {
				fmt.Fprintf(stdout, "MISS  %s  %s  pass/%s fails it\n", formatFloat(*a.Noul), q, filepath.Base(path))
				misses++
			}
		}
	}
	for _, path := range fail {
		res, err := ask(path)
		if err != nil {
			return 0, err
		}
		q := filepath.Base(filepath.Dir(path))
		a := res.Answers[q]
		if a.Type != "noul" {
			return 0, fmt.Errorf("%s: fail fixtures require a noul question", path)
		}
		highestFail[q] = max(highestFail[q], *a.Noul)
		if *a.Noul >= limit(q) {
			fmt.Fprintf(stdout, "MISS  %s  %s  fail/%s/%s passes it\n", formatFloat(*a.Noul), q, q, filepath.Base(path))
			misses++
		}
	}

	// A question separates its fixtures when its highest fail is below its lowest pass.
	fmt.Fprintln(stdout, "lowest-pass  highest-fail  threshold  question")
	for _, q := range slices.Sorted(maps.Keys(lowestPass)) {
		fail := "-"
		if h, ok := highestFail[q]; ok {
			fail = formatFloat(h)
		}
		fmt.Fprintf(stdout, "%-11s  %-12s  %-9s  %s\n", formatFloat(lowestPass[q]), fail, formatFloat(limit(q)), q)
	}
	fmt.Fprintf(stdout, "eval: %d misses in %d fixtures\n", misses, len(pass)+len(fail))
	return min(misses, 1), nil
}

// patchFile returns the path in a patch's +++ b/<path> line.
func patchFile(patch string) (string, bool) {
	for _, line := range strings.Split(patch, "\n") {
		if file, ok := strings.CutPrefix(line, "+++ b/"); ok {
			return file, true
		}
	}
	return "", false
}
