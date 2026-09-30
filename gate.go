package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/verdict"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

const gateUsage = `Usage: jev-check gate [DIR] [--no-cache] [--model ID]   (default: .)
Sends one patch per staged file to each check in
DIR/.jev-check/config.json. Answers are cached for 24 hours in
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
	cfg, err := jev.LoadSettings(dir, model)
	if err != nil {
		return 0, err
	}
	p, err := workspace.LoadProject(dir)
	if err != nil {
		return 0, err
	}
	questions, err := catalog.ValidateChecks(dir, p.Checks)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", workspace.ConfigPath(dir), err)
	}
	stylePaths, styles, err := workspace.LoadStyles(dir, p.Checks)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", workspace.ConfigPath(dir), err)
	}
	state, err := workspace.ProjectState(dir, p)
	if err != nil {
		return 0, err
	}
	// Shared content goes with every request, so a secret there stops the gate before any request.
	// A coding_style document counts as shared, even when one check uses it.
	err = secretscan.StyleSecrets(styles)
	if err == nil {
		err = secretscan.ScanRequest(jev.Request{Model: cfg.Model, State: state})
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
		if err := secretscan.ScanRequest(jev.Request{Questions: questions[i]}); err != nil {
			fmt.Fprintf(stdout, "== %s questions\n%v\n", c.Check, err)
			blocked[i], status = true, 1
		}
	}
	files, err := gitcmd.StagedFiles(dir, p.Exclude)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 && status == 0 {
		fmt.Fprintln(stdout, "nothing staged")
		return 0, nil
	}
	// label names a file in output, unless its name could leak a secret or forge a line.
	label := func(file string) string {
		return secretscan.SafeLabel(file, fmt.Sprintf("staged file %d", slices.Index(files, file)+1))
	}

	patches := map[string]string{}
	for _, file := range files {
		patch, err := gitcmd.Git(dir, "diff", "--cached", "--relative", "--", ":(literal)"+file)
		if err != nil {
			return 0, err
		}
		// A patch that looks like it holds a secret is never sent.
		if reports := secretscan.SecretReports(label(file)+".patch", patch, true); reports != nil {
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
		checkFiles, err := gitcmd.StagedFiles(dir, append(slices.Clone(p.Exclude), c.Skip...))
		if err != nil {
			return 0, err
		}
		for _, file := range checkFiles {
			patch, ok := patches[file]
			if !ok {
				continue
			}
			req := jev.Request{Model: cfg.Model, Questions: questions[i], State: workspace.FileState(state, file, patch, stylePaths[i], styles)}
			res, cached, err := jev.CachedJev(dir, c.Check, cfg, req, noCache, stderr)
			var found secretscan.SecretsFound
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
			if verdict.PrintVerdicts(stdout, res.Answers, *c.Threshold, c.PerQuestion) {
				status = max(status, 1)
			}
		}
	}

	fmt.Fprintln(stdout, "gate: "+[]string{"PASS", "FAIL", "ERROR"}[status])
	return status, nil
}
