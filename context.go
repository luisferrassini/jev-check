package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

func stateCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, `Usage: jev-check state [DIR]   reads DIR/.jev-check/config.json (default: .)
Prints the project fields and tree that go with every request. When checks
name a coding_style document, coding_styles maps each path to its contents
once. A request holds only its own check's document, as state.coding_style.
`)
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	p, err := workspace.LoadProject(dir)
	if err != nil {
		return 0, err
	}
	_, styles, err := workspace.LoadStyles(dir, p.Checks)
	if err != nil {
		return 0, err
	}
	if err := secretscan.StyleSecrets(styles); err != nil {
		return 0, err
	}
	state, err := workspace.ProjectState(dir, p)
	if err != nil {
		return 0, err
	}
	if len(styles) > 0 {
		state["coding_styles"] = styles
	}
	return 0, fsutil.WriteJSON(stdout, state)
}

const initUsage = `Usage: jev-check init [DIR]   (default: .)
Sets up DIR/.jev-check/, creating each of these files that is missing:
  config.json            the config, with the starting defaults
  .gitignore             keeps .env and output/ out of Git
  README.md              what each file in .jev-check/ is for
  input/questions/, input/states/
                         the checks available: every bundled check when
                         input/questions/ is new, else the ones in "checks"
DIR must be in a git working tree. The gate runs only the checks listed in
"checks" in config.json, so add an entry there to turn one on.
A file that already exists is kept, never replaced, so running init again
restores only what is missing and does not bring back a deleted check.
`

// initConfig is the starting config.json. Its defaults are this repository's, not a policy for every project.
const initConfig = `{
  "purpose": "",
  "rules": [],
  "folders": {},
  "exclude": [".jev-check/"],
  "checks": [{ "check": "public-release", "threshold": 0.5, "skip": ["LICENSE"] }]
}
`

func initCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, initUsage)
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	dir, err := filepath.Abs(cmp.Or(append(args, ".")...))
	if err != nil {
		return 0, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", dir)
	}
	if out, err := gitcmd.Git(dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return 0, fmt.Errorf("%s is not in a git working tree", dir)
	}
	// Mkdir, not MkdirAll: a file or symlink named .jev-check is an error, not a folder to follow.
	mkErr := os.Mkdir(filepath.Join(dir, workspace.JevDir), 0o755)
	if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
		return 0, mkErr
	}
	if info, err := os.Lstat(filepath.Join(dir, workspace.JevDir)); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", filepath.Join(dir, workspace.JevDir))
	}
	path := workspace.ConfigPath(dir)
	var p workspace.Project
	switch err := fsutil.CreateFile(path, initConfig); {
	case errors.Is(err, fs.ErrExist):
		fmt.Fprintf(stdout, "kept    %s\n", path)
		if err := fsutil.ReadJSON(path, &p); err != nil {
			return 0, err
		}
	case err != nil:
		if mkErr == nil {
			os.Remove(filepath.Join(dir, workspace.JevDir)) // only this run made it; Remove keeps a non-empty folder
		}
		return 0, err
	default:
		fmt.Fprintf(stdout, "created %s\n", path)
		json.Unmarshal([]byte(initConfig), &p)
	}
	ignore := filepath.Join(dir, workspace.JevDir, ".gitignore")
	if err := fsutil.InstallFile(ignore, ".env\noutput/\n", stdout); err != nil {
		return 0, err
	}
	readme, _ := bundled.ReadFile(bundleDir + "/README.md")
	if err := fsutil.InstallFile(filepath.Join(dir, workspace.JevDir, "README.md"), string(readme), stdout); err != nil {
		return 0, err
	}
	// A new input/questions/ gets every bundled check, the ones available to list in "checks".
	// Later runs restore only the listed ones, so a check the user deleted stays deleted.
	names := bundledNames()
	if _, err := os.Stat(inputDir(dir, "questions")); err == nil {
		names = nil
		for _, c := range p.Checks {
			if isBundled(c.Check) && !slices.Contains(names, c.Check) {
				names = append(names, c.Check)
			}
		}
	}
	if err := addChecks(dir, names, stdout); err != nil {
		return 0, err
	}
	fmt.Fprintf(stdout, `Next:
  1. Review %s: purpose, rules, exclude, and "checks".
     The gate runs only the checks listed in "checks". %s
     holds every check available: edit, delete, or add files there.
  2. Put TYPESAFE_API_KEY=<key> in %s. No other place is read.
     %s keeps it and output/ out of Git.
  3. Check the setup: jev-check doctor %s
  4. Stage the work you want checked: git add -- <path>
  5. Run: jev-check gate %s
`, path, inputDir(dir, "questions")+string(filepath.Separator),
		filepath.Join(dir, jev.SettingsFile), ignore, fsutil.ShellQuote(dir), fsutil.ShellQuote(dir))
	return 0, nil
}
