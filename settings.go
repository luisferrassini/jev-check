package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// modelFlag reads the value of --model, which must not be empty.
func modelFlag(args []string, i int) (string, error) {
	if i+1 == len(args) || args[i+1] == "" {
		return "", errors.New("--model needs a model ID")
	}
	return args[i+1], nil
}

const doctorUsage = `Usage: jev-check doctor [DIR]   (default: .)
Prints the settings jev-check would use for DIR and any setup problem, without
calling the API: the settings file DIR/.jev-check/.env, the endpoint, the model,
whether the API key is set (never its value), DIR/.jev-check/config.json,
no old DIR/project-context.json or DIR/.jev-check/project-context.json, and whether DIR is a git working tree with a
writable DIR/.jev-check/output/ folder.
Exit 0 when every line is ok, 1 when the model looks like a secret, 2 on a problem.
`

func doctorCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, doctorUsage)
		return 0, nil
	}
	if len(args) > 1 {
		return 0, errors.New("expected at most one DIR")
	}
	dir, err := filepath.Abs(cmp.Or(append(args, ".")...))
	if err != nil {
		return 0, err
	}
	status := 0
	line := func(item, detail string, err error) {
		if err != nil {
			fmt.Fprintf(stdout, "FAIL  %s  %v\n", item, err)
			status = 2
			return
		}
		fmt.Fprintf(stdout, "ok    %s  %s\n", item, detail)
	}

	values, path, err := jev.ReadSettings(dir)
	missing := ""
	if !fsutil.FileExists(path) {
		missing = " missing"
	}
	line("settings file", path+missing, err)
	if err == nil {
		source := func(name string) string {
			if values[name] != "" {
				return path
			}
			return "default"
		}
		s, err := jev.LoadSettings(dir, "")
		if err := secretscan.ScanRequest(jev.Request{Model: s.Model}); err != nil {
			return 0, err
		}
		line("endpoint", s.Endpoint+" ("+source("JEV_CHECK_ENDPOINT")+")", err)
		line("model", s.Model+" ("+source("JEV_CHECK_MODEL")+")", nil)
		if s.Key == "" {
			line("API key", "", fmt.Errorf("missing: %w", s.MissingKey()))
		} else {
			line("API key", "set", nil)
		}
		if os.Getenv("TYPESAFE_API_KEY") != "" {
			fmt.Fprintln(stdout, "info  API key  TYPESAFE_API_KEY in the environment is ignored")
		}
	} else {
		for _, item := range []string{"endpoint", "model", "API key"} {
			line(item, "", errors.New("not read: the settings file failed"))
		}
	}

	config := workspace.ConfigPath(dir)
	p, err := workspace.Load(dir)
	if err == nil {
		_, err = catalog.Validate(dir, p.Checks)
	}
	if err == nil {
		_, _, err = workspace.LoadStyles(dir, p.Checks)
	}
	if err != nil && !strings.Contains(err.Error(), config) {
		err = fmt.Errorf("%s: %w", config, err)
	}
	line("project", config, err)
	// With no new config, workspace.Load already reported the old one.
	for _, old := range []string{filepath.Join(dir, "project-context.json"), filepath.Join(dir, workspace.Dir, "project-context.json")} {
		if fsutil.FileExists(old) && fsutil.FileExists(config) {
			line("old layout", "", fmt.Errorf("%s is ignored; delete it or move it over %s", old, config))
		}
	}

	out := filepath.Join(dir, workspace.Dir, "output")
	if !fsutil.FileExists(out) {
		out = filepath.Join(dir, workspace.Dir)
	}
	_, err = gitcmd.Run(dir, "rev-parse", "--is-inside-work-tree")
	if err == nil {
		var probe *os.File
		if probe, err = os.CreateTemp(out, ".doctor-probe-*"); err == nil {
			probe.Close()
			err = os.Remove(probe.Name())
		}
	}
	line("git and output", out+" is writable", err)

	fmt.Fprintf(stdout, "doctor: %s; the key and the service were not tested\n", map[int]string{0: "ok", 2: "FAIL"}[status])
	return status, nil
}
