package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// cacheRepo makes a git project with one staged file and the public-release check,
// and returns it with the path of its only cache entry after a first gate run.
func cacheRepo(t *testing.T, requests *[]jev.Request) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, workspace.ConfigPath(repo), `{"purpose":"One.","exclude":[".jev-check/input/","*.md"],"checks":[{"check":"public-release","threshold":0.2}]}`)
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitRun(t, repo, "add", "a.txt")
	*requests = nil
	wantCode(t, 0, "gate", repo)
	return repo, onlyEntry(t, repo)
}

func onlyEntry(t *testing.T, repo string) string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(repo, ".jev-check", "output", "cache", "v2", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries %v, %v", entries, err)
	}
	return entries[0]
}

// gateCalls runs the gate and returns the number of requests it sent and whether it used the cache.
func gateCalls(t *testing.T, requests *[]jev.Request, want int, args ...string) (int, bool) {
	t.Helper()
	before := len(*requests)
	out := wantCode(t, want, append([]string{"gate"}, args...)...)
	return len(*requests) - before, strings.Contains(out, "(cached)")
}

// entry reads a cache envelope as raw fields.
func entry(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var e map[string]json.RawMessage
	if err := fsutil.ReadJSON(path, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// stamp rewrites a cache envelope's created_at.
func stamp(t *testing.T, path string, created time.Time) {
	t.Helper()
	e := entry(t, path)
	e["created_at"], _ = json.Marshal(created.UTC().Format(time.RFC3339))
	data, _ := json.Marshal(e)
	writeFile(t, path, string(data))
}

func TestCacheIdentity(t *testing.T) {
	requests := setup(t)
	repo, path := cacheRepo(t, requests)
	if e := entry(t, path); string(e["version"]) != "2" || e["created_at"] == nil || e["response"] == nil {
		t.Errorf("envelope %v", e)
	}
	if n, cached := gateCalls(t, requests, 0, repo); n != 0 || !cached {
		t.Errorf("repeat sent %d requests, cached %v", n, cached)
	}

	// Each change to the request misses, and the next run with it hits.
	changes := map[string]func(){
		"tree": func() { writeFile(t, filepath.Join(repo, "new.txt"), "x\n") },
		"project": func() {
			writeFile(t, workspace.ConfigPath(repo), `{"purpose":"Two.","exclude":[".jev-check/input/","*.md"],"checks":[{"check":"public-release","threshold":0.2}]}`)
		},
		"patch": func() {
			writeFile(t, filepath.Join(repo, "a.txt"), "b\n")
			gitRun(t, repo, "add", "a.txt")
		},
		"questions": func() {
			writeFile(t, filepath.Join(repo, ".jev-check/input/questions/public-release.json"), onlyQuestion)
		},
		"coding style": func() {
			writeFile(t, filepath.Join(repo, "STYLE.md"), "Rule one.\n")
			writeFile(t, workspace.ConfigPath(repo), `{"purpose":"Two.","exclude":[".jev-check/input/","*.md"],"checks":[{"check":"public-release","threshold":0.2,"coding_style":"STYLE.md"}]}`)
		},
		"coding style content": func() { writeFile(t, filepath.Join(repo, "STYLE.md"), "Rule two.\n") },
		"coding style path": func() {
			writeFile(t, filepath.Join(repo, "OTHER.md"), "Rule two.\n")
			writeFile(t, workspace.ConfigPath(repo), `{"purpose":"Two.","exclude":[".jev-check/input/","*.md"],"checks":[{"check":"public-release","threshold":0.2,"coding_style":"OTHER.md"}]}`)
		},
	}
	for _, name := range []string{"tree", "project", "patch", "questions", "coding style", "coding style content", "coding style path"} {
		changes[name]()
		if n, cached := gateCalls(t, requests, 0, repo); n != 1 || cached {
			t.Errorf("%s: sent %d requests, cached %v", name, n, cached)
		}
		if n, _ := gateCalls(t, requests, 0, repo); n != 0 {
			t.Errorf("%s: repeat sent %d requests", name, n)
		}
	}

	// The model and the endpoint each separate answers.
	if n, cached := gateCalls(t, requests, 0, repo, "--model", "other"); n != 1 || cached || (*requests)[len(*requests)-1].Model != "other" {
		t.Errorf("model change sent %d requests, cached %v", n, cached)
	}
	var second []jev.Request
	writeSettings(t, repo, "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT="+fakeServer(t, &second)+"\n")
	if n, cached := gateCalls(t, &second, 0, repo); n != 1 || cached {
		t.Errorf("endpoint change sent %d requests, cached %v", n, cached)
	}
	writeSettings(t, repo, fakeSettings)

	// A threshold change judges the cached answers again, without a call.
	writeFile(t, workspace.ConfigPath(repo), `{"purpose":"Two.","exclude":[".jev-check/input/","*.md"],"checks":[{"check":"public-release","threshold":0.99,"coding_style":"OTHER.md"}]}`)
	if n, cached := gateCalls(t, requests, 1, repo); n != 0 || !cached {
		t.Errorf("threshold change sent %d requests, cached %v", n, cached)
	}

	// A state field no command sends yet is still part of the key.
	before := len(*requests)
	for _, v := range []string{"one", "two", "two"} {
		req := jev.Request{Model: "m", Questions: map[string]json.RawMessage{"q": json.RawMessage(`{"type":"noul"}`)}, State: map[string]any{"future": v}}
		settings, _ := jev.LoadSettings(repo, "")
		if _, _, err := jev.AskCached(repo, "x", settings, req, false, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(*requests)-before != 2 {
		t.Errorf("a new state field sent %d requests, want 2", len(*requests)-before)
	}
}

func TestCacheTreeSent(t *testing.T) {
	requests := setup(t)
	repo, _ := cacheRepo(t, requests)
	writeFile(t, filepath.Join(repo, "new.txt"), "x\n")
	gateCalls(t, requests, 0, repo)
	if tree := (*requests)[len(*requests)-1].State["tree"]; !strings.Contains(strings.Join(anyStrings(tree), ","), "new.txt") {
		t.Errorf("tree sent %v", tree)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestCacheLifetime(t *testing.T) {
	requests := setup(t)
	repo, path := cacheRepo(t, requests)
	valid, _ := os.ReadFile(path)
	created := entry(t, path)["created_at"]

	// A hit keeps created_at.
	gateCalls(t, requests, 0, repo)
	if got := entry(t, path)["created_at"]; string(got) != string(created) {
		t.Errorf("a hit changed created_at from %s to %s", created, got)
	}
	stamp(t, path, time.Now().Add(-23*time.Hour))
	if n, cached := gateCalls(t, requests, 0, repo); n != 0 || !cached {
		t.Errorf("23 hours old: sent %d requests", n)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for name, content := range map[string]string{
		"expired":         "",
		"future":          "",
		"malformed":       "{",
		"version 1":       `{"version":1,"created_at":"` + now + `","response":` + fakeAnswers + `}`,
		"version string":  `{"version":"2","created_at":"` + now + `","response":` + fakeAnswers + `}`,
		"no version":      `{"created_at":"` + now + `","response":` + fakeAnswers + `}`,
		"no timestamp":    `{"version":2,"response":` + fakeAnswers + `}`,
		"bad timestamp":   `{"version":2,"created_at":"yesterday","response":` + fakeAnswers + `}`,
		"invalid answers": `{"version":2,"created_at":"` + now + `","response":{"answers":{}}}`,
		"legacy":          fakeAnswers,
	} {
		t.Run(name, func(t *testing.T) {
			writeFile(t, path, string(valid))
			switch name {
			case "expired":
				stamp(t, path, time.Now().Add(-25*time.Hour))
				// Touching the file does not make it young again.
				os.Chtimes(path, time.Now(), time.Now())
			case "future":
				stamp(t, path, time.Now().Add(time.Hour))
			default:
				writeFile(t, path, content)
			}
			if n, cached := gateCalls(t, requests, 0, repo); n != 1 || cached {
				t.Errorf("sent %d requests, cached %v", n, cached)
			}
			if e := entry(t, path); string(e["version"]) != "2" {
				t.Errorf("not rewritten: %v", e)
			}
		})
	}

	// A legacy unwrapped answer at the old place for the same digest is never read, changed, or removed.
	writeFile(t, path, string(valid))
	answers := string(entry(t, path)["response"])
	legacy := writeFile(t, filepath.Join(filepath.Dir(filepath.Dir(path)), filepath.Base(path)), answers)
	os.Remove(path)
	if n, cached := gateCalls(t, requests, 0, repo); n != 1 || cached {
		t.Errorf("legacy entry: sent %d requests, cached %v", n, cached)
	}
	if e := entry(t, path); string(e["version"]) != "2" {
		t.Errorf("no v2 envelope after a legacy entry: %v", e)
	}
	if data, _ := os.ReadFile(legacy); string(data) != answers {
		t.Errorf("legacy entry changed: %s", data)
	}

	// --no-cache calls and refreshes the entry, and the next run reuses it.
	stamp(t, path, time.Now().Add(-time.Hour))
	old := entry(t, path)["created_at"]
	if n, cached := gateCalls(t, requests, 0, repo, "--no-cache"); n != 1 || cached {
		t.Errorf("--no-cache sent %d requests", n)
	}
	if got := entry(t, path)["created_at"]; string(got) == string(old) {
		t.Error("--no-cache did not refresh the entry")
	}
	if n, cached := gateCalls(t, requests, 0, repo); n != 0 || !cached {
		t.Errorf("after --no-cache sent %d requests", n)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("cache holds %v", entries)
	}
}

func TestCacheFailures(t *testing.T) {
	requests := setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	repo, path := cacheRepo(t, requests)

	// A failed refresh is an error, never the old answer, and leaves the entry alone.
	stamp(t, path, time.Now().Add(-25*time.Hour))
	expired, _ := os.ReadFile(path)
	for _, answers := range []string{`{"answers":{}}`, "not json"} {
		jevAnswers = answers
		if n, cached := gateCalls(t, requests, 2, repo); n != 1 || cached {
			t.Errorf("sent %d requests, cached %v", n, cached)
		}
		wantCode(t, 2, "gate", repo, "--no-cache")
		if data, _ := os.ReadFile(path); string(data) != string(expired) {
			t.Error("a failed refresh replaced the entry")
		}
	}
	jevAnswers = ""

	// A cache that cannot be written keeps the live verdict, warns, and calls again next time.
	os.RemoveAll(filepath.Join(repo, ".jev-check", "output", "cache"))
	writeFile(t, filepath.Join(repo, ".jev-check", "output", "cache", "v2"), "not a folder")
	for range 2 {
		before := len(*requests)
		var stdout, stderr strings.Builder
		code := run([]string{"gate", repo}, &stdout, &stderr)
		if code != 0 || len(*requests) != before+1 || !strings.Contains(stderr.String(), "jev-check: cache not saved") {
			t.Errorf("exit %d, %d requests, stderr %s", code, len(*requests)-before, stderr.String())
		}
	}
}

func TestEvalCache(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a", "x\n"))
	for _, q := range []string{"q1", "q2"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q, "x\n"))
	}
	count := func(args ...string) int {
		t.Helper()
		before := len(*requests)
		wantCode(t, 0, append([]string{"eval", "two", repo}, args...)...)
		return len(*requests) - before
	}
	if n := count(); n != 3 {
		t.Errorf("first eval sent %d, want 3", n)
	}
	if n := count(); n != 0 {
		t.Errorf("second eval sent %d, want 0", n)
	}
	if n := count("--no-cache"); n != 3 {
		t.Errorf("--no-cache sent %d, want 3", n)
	}
	if n := count("--model", "other"); n != 3 {
		t.Errorf("another model sent %d, want 3", n)
	}
}
