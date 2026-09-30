package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/patchset"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/verdict"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

const evalUsage = `Usage: jev-check eval <check> [DIR] [--no-cache] [--model ID]   (default DIR: .)
Tests a check's thresholds in DIR/.jev-check/config.json against
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
	cfg, err := jev.LoadSettings(dir, model)
	if err != nil {
		return 0, err
	}
	p, err := workspace.LoadProject(dir)
	if err != nil {
		return 0, err
	}
	i := slices.IndexFunc(p.Checks, func(c workspace.GateCheck) bool { return c.Check == name })
	if i < 0 {
		return 0, fmt.Errorf("%s has no check %s", workspace.ConfigPath(dir), name)
	}
	c := p.Checks[i]
	questions, err := catalog.ValidateChecks(dir, []workspace.GateCheck{c})
	if err != nil {
		return 0, err
	}
	// Only yes/no questions have thresholds, so each needs its own fail fixtures.
	blocking := jev.NoulIDs(questions[0])
	if len(blocking) == 0 {
		return 0, fmt.Errorf("check %s has no yes/no (noul) question, so there is no threshold to evaluate", name)
	}
	stylePaths, styles, err := workspace.LoadStyles(dir, []workspace.GateCheck{c})
	if err != nil {
		return 0, err
	}
	state, err := workspace.ProjectState(dir, p)
	if err != nil {
		return 0, err
	}
	fixtures := filepath.Join(dir, workspace.JevDir, "fixtures", name)
	jobs, err := patchset.FindFixtures(fixtures, blocking)
	if err != nil {
		return 0, err
	}

	// Read, parse, and scan every fixture before the first cache lookup or API call,
	// so a bad fixture late in the list never lets earlier ones through.
	for i, job := range jobs {
		patch, err := os.ReadFile(filepath.Join(fixtures, job.Rel))
		if err != nil {
			return 0, errors.New(patchset.Printable(err.Error()))
		}
		// The patch is sent under its target path, as the gate sends it, so the fixture folder never reaches Jev.
		file, err := patchset.PatchPath(string(patch))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", patchset.Printable(filepath.Join(fixtures, job.Rel)), err)
		}
		jobs[i].Req = jev.Request{Model: cfg.Model, Questions: questions[0], State: workspace.FileState(state, file, string(patch), stylePaths[0], styles)}
	}
	// A secret in the shared document is reported once, not once per fixture.
	var blocked []string
	holder := "a fixture"
	if err := secretscan.StyleSecrets(styles); err != nil {
		blocked = append(blocked, err.Error())
		holder = "the coding_style document"
	} else {
		for _, job := range jobs {
			if err := secretscan.ScanRequest(job.Req); err != nil {
				blocked = append(blocked, fmt.Sprintf("== %s\n%v", secretscan.SafeLabel(job.Rel, "a fixture"), err))
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
		res, _, err := jev.CachedJev(dir, name, cfg, job.Req, noCache, stderr)
		if err != nil {
			return 0, err
		}
		if job.Question != "" {
			a := *res.Answers[job.Question].Noul
			negatives[job.Question]++
			highestFail[job.Question] = max(highestFail[job.Question], a)
			if a >= c.Limit(job.Question) {
				fmt.Fprintf(stdout, "MISS  %s  %s  %s passes it\n", verdict.FormatFloat(a), job.Question, patchset.Printable(job.Rel))
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
			if a < c.Limit(q) {
				fmt.Fprintf(stdout, "MISS  %s  %s  %s fails it\n", verdict.FormatFloat(a), q, patchset.Printable(job.Rel))
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
		fmt.Fprintf(stdout, "%-11s  %-12s  %-9s  %s\n", verdict.FormatFloat(lowestPass[q]), verdict.FormatFloat(highestFail[q]), verdict.FormatFloat(c.Limit(q)), q)
	}
	fmt.Fprintf(stdout, "eval: %d misses in %d fixtures\n", misses, len(jobs))
	return min(misses, 1), nil
}
