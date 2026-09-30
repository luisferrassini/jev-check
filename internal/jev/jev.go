package jev

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

type Request struct {
	Model     string                     `json:"model"`
	Questions map[string]json.RawMessage `json:"questions"`
	State     map[string]any             `json:"state"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
}

// Answer is a noul (the probability that the answer is yes), a choice, or a score.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Score         any                `json:"score"`
}

// NoulIDs returns the sorted ids of the yes/no (noul) questions.
func NoulIDs(questions map[string]json.RawMessage) []string {
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(questions)) {
		var q struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(questions[id], &q) == nil && q.Type == "noul" {
			ids = append(ids, id)
		}
	}
	return ids
}

// ValidateAnswers rejects incomplete or invalid answers before they can pass a check.
// Saved outputs without a request can still be checked for valid answer values.
func ValidateAnswers(answers map[string]Answer, questions map[string]json.RawMessage) error {
	if len(answers) == 0 {
		return errors.New("response needs a non-empty answers object")
	}
	for id, raw := range questions {
		var question struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &question); err != nil || question.Type == "" {
			return fmt.Errorf("question %s needs a type", id)
		}
		a, ok := answers[id]
		if !ok || a.Type != question.Type {
			return fmt.Errorf("question %s needs an answer of type %s", id, question.Type)
		}
	}
	for id, a := range answers {
		if questions != nil && questions[id] == nil {
			return fmt.Errorf("unexpected answer %s", id)
		}
		switch a.Type {
		case "noul":
			if a.Noul == nil || !(*a.Noul >= 0 && *a.Noul <= 1) {
				return fmt.Errorf("answer %s needs a noul number from 0 to 1", id)
			}
		case "choice":
			probability, ok := a.Probabilities[a.Choice]
			if !ok || !(probability >= 0 && probability <= 1) {
				return fmt.Errorf("answer %s needs a choice with a probability from 0 to 1", id)
			}
		case "score":
			if a.Score == nil {
				return fmt.Errorf("answer %s needs a score", id)
			}
		default:
			return fmt.Errorf("answer %s has unknown type %q", id, a.Type)
		}
	}
	return nil
}

// CallJev sends a request to cfg's endpoint and saves it, with the response, under project/.jev-check/output/.
// It returns the response and the absolute saved path.
func CallJev(project, name string, cfg Settings, req Request) (Response, string, error) {
	var res Response
	// Every path to the API passes here, so nothing that looks like a secret is sent.
	if err := secretscan.ScanRequest(req); err != nil {
		return res, "", err
	}
	if cfg.Key == "" {
		return res, "", cfg.MissingKey()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return res, "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return res, "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Key)
	httpReq.Header.Set("Content-Type", "application/json")
	// A redirect is returned as a 3xx error, so the key never follows it to another host.
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	httpRes, err := client.Do(httpReq)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	defer httpRes.Body.Close()
	raw, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	if httpRes.StatusCode/100 != 2 {
		return res, "", fmt.Errorf("API call failed: %s: %s", httpRes.Status, bytes.TrimSpace(raw))
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Answers == nil {
		return res, "", fmt.Errorf("unexpected API response: %s", bytes.TrimSpace(raw))
	}

	if err := ValidateAnswers(res.Answers, req.Questions); err != nil {
		return res, "", fmt.Errorf("unexpected API response: %w", err)
	}

	dir := filepath.Join(project, workspace.JevDir, "output")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, "", err
	}
	// CreateTemp adds a random suffix, so two runs in the same second never overwrite each other.
	f, err := os.CreateTemp(dir, time.Now().Format("2006-01-02_15-04-05")+"-"+name+"-*.json")
	if err != nil {
		return res, "", err
	}
	defer f.Close()
	err = fsutil.WriteJSON(f, struct {
		Request  Request         `json:"request"`
		Response json.RawMessage `json:"response"`
	}{req, raw})
	if err == nil {
		err = f.Close()
	}
	return res, f.Name(), err
}

// CacheVersion names the cache key and entry format under output/cache/v2/.
// Entries in other folders, such as the unversioned ones in output/cache/, are never read.
const CacheVersion = 2

// CacheLifetime bounds reuse, because a model name such as jev-latest can change behind it.
// It is a policy, not proof that the model stayed the same.
const CacheLifetime = 24 * time.Hour

// CacheEntry is one file in output/cache/v2/.
type CacheEntry struct {
	Version   int      `json:"version"`
	CreatedAt string   `json:"created_at"`
	Response  Response `json:"response"`
}

// Fresh reports whether an entry created at created can be reused at now.
// A future time counts as unknown age, so it is not reused.
func Fresh(created, now time.Time) bool {
	age := now.Sub(created)
	return age >= 0 && age < CacheLifetime
}

// CachedJev returns the answers from project/.jev-check/output/cache/v2/ when the same request went to the
// same endpoint less than cacheLifetime ago, else it calls Jev and caches the answers.
// The key is the whole request, so any change to the model, questions, or state misses.
// Thresholds are not in the request: the gate judges cached answers again on every run.
// noCache skips the lookup but still saves the new answers.
func CachedJev(project, name string, cfg Settings, req Request, noCache bool, stderr io.Writer) (Response, bool, error) {
	// Scan before the cache, so an old answer never hides a secret.
	if err := secretscan.ScanRequest(req); err != nil {
		return Response{}, false, err
	}
	// json.Marshal sorts map keys, so the same request always gives the same key.
	key, err := json.Marshal(struct {
		Version  int     `json:"version"`
		Endpoint string  `json:"endpoint"`
		Request  Request `json:"request"`
	}{CacheVersion, cfg.Endpoint, req})
	if err != nil {
		return Response{}, false, err
	}
	sum := sha256.Sum256(key)
	path := filepath.Join(project, workspace.JevDir, "output", "cache", "v2", hex.EncodeToString(sum[:])+".json")
	var entry CacheEntry
	if !noCache && fsutil.ReadJSON(path, &entry) == nil && entry.Version == CacheVersion {
		created, err := time.Parse(time.RFC3339, entry.CreatedAt)
		if err == nil && Fresh(created, time.Now()) && ValidateAnswers(entry.Response.Answers, req.Questions) == nil {
			return entry.Response, true, nil
		}
	}
	res, _, err := CallJev(project, name, cfg, req)
	if err != nil {
		return res, false, err
	}
	// A failed cache write only costs an API call next time, so it warns and keeps the answer.
	entry = CacheEntry{CacheVersion, time.Now().UTC().Format(time.RFC3339), res}
	if err := WriteCache(path, entry); err != nil {
		// gate and eval both call here, so the warning names neither.
		fmt.Fprintf(stderr, "jev-check: cache not saved: %v\n", err)
	}
	return res, false, nil
}

// WriteCache writes entry to a temporary file and renames it to path,
// so a reader never sees half an entry.
func WriteCache(path string, entry CacheEntry) error {
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

// SettingsFile holds jev-check's key, endpoint, and model, relative to the project.
// Nothing else is read for them: not the environment, not the project's own .env.
const SettingsFile = workspace.JevDir + "/.env"

const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"
)

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
		Endpoint: cmp.Or(values["JEV_CHECK_ENDPOINT"], DefaultEndpoint),
		Model:    cmp.Or(model, values["JEV_CHECK_MODEL"], DefaultModel),
	}
	if err := CheckEndpoint(s.Endpoint); err != nil {
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
	_, err := gitcmd.Git(project, "rev-parse", "--is-inside-work-tree")
	if err != nil && !strings.Contains(err.Error(), "not a git repository") {
		return nil, path, fmt.Errorf("checking whether %s is tracked by Git: %w", path, err)
	}
	if err == nil {
		out, err := gitcmd.Git(project, "ls-files", "-z", "--", SettingsFile)
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

// CheckEndpoint refuses an endpoint the key must not be sent to. Its message never holds the URL,
// so user info in it is never printed.
func CheckEndpoint(raw string) error {
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
