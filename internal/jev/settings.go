package jev

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// SettingsFile holds jev-check's key, endpoint, and model, relative to the project.
// Nothing else is read for them: not the environment, not the project's own .env.
const SettingsFile = workspace.Dir + "/.env"

// Settings are the resolved values one command uses for every request.
// key is empty when the file has none; only a call to the API needs it.
type Settings struct {
	path, Key, Endpoint, Model string
}

// LoadSettings reads project's settings file and resolves the endpoint and model.
// model is the --model value, empty when the flag was not given.
func LoadSettings(project, model string) (Settings, error) {
	values, path, err := ReadSettings(project)
	if err != nil {
		return Settings{}, err
	}
	s := Settings{
		path:     path,
		Key:      values["TYPESAFE_API_KEY"],
		Endpoint: cmp.Or(values["JEV_CHECK_ENDPOINT"], defaultEndpoint),
		Model:    cmp.Or(model, values["JEV_CHECK_MODEL"], defaultModel),
	}
	if err := checkEndpoint(s.Endpoint); err != nil {
		return s, fmt.Errorf("JEV_CHECK_ENDPOINT in %s %w", path, err)
	}
	return s, nil
}

// ReadSettings returns the first nonempty NAME=value of each name in project's settings file,
// trimmed, and the file's path. A missing file has no values. A file tracked by Git is refused,
// so a cloned repository cannot choose where the key goes.
func ReadSettings(project string) (map[string]string, string, error) {
	path := filepath.Join(project, SettingsFile)
	// Outside a Git repository, as ask allows, there is nothing to be tracked in. Any other
	// failure, such as a repository Git refuses for its owner, could hide a tracked file.
	_, err := gitcmd.Run(project, "rev-parse", "--is-inside-work-tree")
	if err != nil && !strings.Contains(err.Error(), "not a git repository") {
		return nil, path, fmt.Errorf("checking whether %s is tracked by Git: %w", path, err)
	}
	if err == nil {
		out, err := gitcmd.Run(project, "ls-files", "-z", "--", SettingsFile)
		if err != nil {
			return nil, path, err
		}
		if out != "" {
			return nil, path, fmt.Errorf("%s is tracked by Git; run git rm --cached -- %s and add .env to .jev-check/.gitignore", path, path)
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
func (s Settings) MissingKey() error {
	note := ""
	if os.Getenv("TYPESAFE_API_KEY") != "" {
		note = " (TYPESAFE_API_KEY in the environment is ignored)"
	}
	return fmt.Errorf("set TYPESAFE_API_KEY in %s%s", s.path, note)
}

const (
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	defaultModel    = "jev-latest"
)
