package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// settingsFile holds jev-check's key, endpoint, and model, relative to the project.
// Nothing else is read for them: not the environment, not the project's own .env.
const settingsFile = ".jev-check/.env"

const (
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	defaultModel    = "jev-latest"
)

// settings are the resolved values one command uses for every request.
// key is empty when the file has none; only a call to the API needs it.
type settings struct {
	path, key, endpoint, model string
}

// loadSettings reads project's settings file and resolves the endpoint and model.
// model is the --model value, empty when the flag was not given.
func loadSettings(project, model string) (settings, error) {
	values, path, err := readSettings(project)
	if err != nil {
		return settings{}, err
	}
	s := settings{
		path:     path,
		key:      values["TYPESAFE_API_KEY"],
		endpoint: cmp.Or(values["JEV_CHECK_ENDPOINT"], defaultEndpoint),
		model:    cmp.Or(model, values["JEV_CHECK_MODEL"], defaultModel),
	}
	if err := checkEndpoint(s.endpoint); err != nil {
		return s, fmt.Errorf("JEV_CHECK_ENDPOINT in %s %w", path, err)
	}
	return s, nil
}

// readSettings returns the first nonempty NAME=value of each name in project's settings file,
// trimmed, and the file's path. A missing file has no values. A file tracked by Git is refused,
// so a cloned repository cannot choose where the key goes.
func readSettings(project string) (map[string]string, string, error) {
	path := filepath.Join(project, settingsFile)
	// Outside a Git work tree, as ask allows, there is nothing to be tracked in.
	if _, err := git(project, "rev-parse", "--is-inside-work-tree"); err == nil {
		out, err := git(project, "ls-files", "-z", "--", settingsFile)
		if err != nil {
			return nil, path, err
		}
		if out != "" {
			return nil, path, fmt.Errorf("%s is tracked by Git; run git rm --cached -- %s and add .jev-check/.env to .gitignore", path, path)
		}
	}
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, path, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, _ := strings.Cut(line, "=")
		if value = strings.TrimSpace(value); value != "" && values[name] == "" {
			values[name] = value
		}
	}
	return values, path, nil
}

// checkEndpoint refuses an endpoint the key must not be sent to. Its message never holds the URL,
// so user info in it is never printed.
func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	loopback := strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
	switch {
	case err != nil || !u.IsAbs() || host == "":
		return errors.New("must be an absolute URL with a host")
	case u.User != nil:
		return errors.New("must not hold a user name or password")
	case strings.Contains(raw, "#"):
		return errors.New("must not have a #fragment")
	case u.Scheme == "https", u.Scheme == "http" && loopback:
		return nil
	}
	return errors.New("must use https, or http only for localhost, 127.0.0.0/8, or ::1")
}

// missingKey says where the key goes, and that the environment variable older versions read is ignored.
func (s settings) missingKey() error {
	note := ""
	if os.Getenv("TYPESAFE_API_KEY") != "" {
		note = " (TYPESAFE_API_KEY in the environment is ignored)"
	}
	return fmt.Errorf("set TYPESAFE_API_KEY in %s%s", s.path, note)
}

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
whether the API key is set (never its value), DIR/project-context.json, and
whether DIR is a git working tree with a writable output folder.
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

	values, path, err := readSettings(dir)
	missing := ""
	if !fileExists(path) {
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
		s, err := loadSettings(dir, "")
		if err := scanRequest(request{Model: s.model}); err != nil {
			return 0, err
		}
		line("endpoint", s.endpoint+" ("+source("JEV_CHECK_ENDPOINT")+")", err)
		line("model", s.model+" ("+source("JEV_CHECK_MODEL")+")", nil)
		if s.key == "" {
			line("API key", "", fmt.Errorf("missing: %w", s.missingKey()))
		} else {
			line("API key", "set", nil)
		}
		if os.Getenv("TYPESAFE_API_KEY") != "" {
			fmt.Fprintln(stdout, "info  API key  TYPESAFE_API_KEY in the environment is ignored")
		}
	}

	config := filepath.Join(dir, "project-context.json")
	p, err := loadProject(dir)
	if err == nil {
		_, err = validateChecks(dir, p.Checks)
	}
	if err == nil {
		_, _, err = loadStyles(dir, p.Checks)
	}
	if err != nil && !strings.Contains(err.Error(), config) {
		err = fmt.Errorf("%s: %w", config, err)
	}
	line("project", config, err)

	out := filepath.Join(dir, "output")
	if !fileExists(out) {
		out = dir
	}
	_, err = git(dir, "rev-parse", "--is-inside-work-tree")
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
