package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
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

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// optInChecks are the bundled opt-in checks whose corpora TestOptInCorpora covers.
// codingStyle names the repository document the check needs, if any, and minPass
// is the size of its pass set.
var optInChecks = []struct {
	name, codingStyle string
	minPass           int
}{
	{name: "no-leftovers", minPass: 10},
	{name: "test-quality", minPass: 10},
	{name: "secret-handling", minPass: 10},
	{name: "help-text-honesty", minPass: 10},
	{name: "coding-style", codingStyle: "CODING_STYLE.md", minPass: 5},
}

// readmeConfig returns the JSON block that follows the canonical-config marker in README.md.
func readmeConfig(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sourceDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "<!-- canonical-config"
	parts := strings.Split(string(data), marker)
	if len(parts) != 2 {
		t.Fatalf("README.md has %d %s markers, want 1", len(parts)-1, marker)
	}
	_, rest, _ := strings.Cut(parts[1], "-->")
	block, ok := strings.CutPrefix(strings.TrimLeft(rest, "\n"), "```json\n")
	if !ok {
		t.Fatalf("no ```json block right after %s", marker)
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatal("unclosed README config block")
	}
	return block
}

// styleRepo makes a git repo with a staged a.go and the given checks.
func styleRepo(t *testing.T, checks string) string {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/other.json"), `{"questions":{"other":{"type":"noul"}}}`)
	styleConfig(t, repo, checks)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	gitRun(t, repo, "add", "a.go")
	return repo
}

func styleConfig(t *testing.T, repo, checks string) {
	t.Helper()
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/",".jev-check/fixtures/","CODING_STYLE.md"],"checks":[`+checks+`]}`)
}

// sentStyles returns each request's coding_style, by check.
func sentStyles(requests []jev.Request) map[string]any {
	got := map[string]any{}
	for _, req := range requests {
		check := "public-release"
		if req.Questions["other"] != nil {
			check = "other"
		}
		got[check] = req.State["coding_style"]
	}
	return got
}

const withStyle = `{"check":"public-release","threshold":0.2,"coding_style":"CODING_STYLE.md"}`

const otherCheck = `{"check":"other","threshold":0.2}`

// gitPatch returns the patch git makes for staging content at path in a fresh repository.
func gitPatch(t *testing.T, path, content string) string {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, path), content)
	gitRun(t, repo, "add", "--", path)
	out, err := gitcmd.Run(repo, "diff", "--cached", "--", path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const twoQuestions = `{"questions":{
  "q1":{"type":"noul","instructions":"One?"},
  "q2":{"type":"noul","instructions":"Two?"},
  "info":{"type":"choice","instructions":"Which?","choices":["x","y"]}}}`

// evalProject makes a git project with the check "two" and returns its fixture folder.
func evalProject(t *testing.T, config string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/two.json"), twoQuestions)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/",".jev-check/input/"],"checks":[`+config+`]}`)
	return repo, filepath.Join(repo, ".jev-check", "fixtures", "two")
}

const fakeAnswers = `{"model":"jev-test","answers":{
  "a":{"type":"noul","noul":0.9},
  "b":{"type":"noul","noul":0.3},
  "c":{"type":"choice","choice":"x","probabilities":{"x":0.8,"y":0.2}}}}`

// sourceDir is the package folder, saved before any test moves into a temp project.
var sourceDir, _ = os.Getwd()

// jevAnswers is what the fake server answers. A test may swap it and restore it.
var jevAnswers string

// fakeEndpoint is the fake server's URL, and fakeSettings points a project at it with the test key.
var fakeEndpoint, fakeSettings string

// setup moves into an empty temp project that holds every bundled check,
// and points it at a fake server that answers each requested question,
// unless jevAnswers overrides it. It returns the requests the server got.
// gitInit points other projects at the same server.
func setup(t *testing.T) *[]jev.Request {
	t.Helper()
	t.Chdir(t.TempDir())
	var got []jev.Request
	fakeEndpoint = fakeServer(t, &got)
	fakeSettings = "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT=" + fakeEndpoint + "\n"
	writeSettings(t, ".", fakeSettings)
	addBundled(t, ".")
	return &got
}

// addBundled copies every bundled check into dir/.jev-check/input/, as jev-check add does.
func addBundled(t *testing.T, dir string) {
	t.Helper()
	if err := catalog.Add(dir, catalog.BundledNames(), io.Discard); err != nil {
		t.Fatal(err)
	}
}

// fakeServer starts a fake Jev server that appends each request it answers to got, and returns its URL.
func fakeServer(t *testing.T, got *[]jev.Request) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		*got = append(*got, req)
		if req.Questions["api_down"] != nil {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		if jevAnswers != "" {
			fmt.Fprint(w, jevAnswers)
			return
		}
		answers := map[string]any{}
		for id, raw := range req.Questions {
			if strings.Contains(string(raw), `"choice"`) {
				answers[id] = map[string]any{"type": "choice", "choice": "x", "probabilities": map[string]float64{"x": 1}}
				continue
			}
			value := 0.9
			if id == "english_only" {
				value = 0.3
			}
			files, _ := req.State["files"].(map[string]any)
			if id == "english_only" && files["b.go.patch"] != nil {
				value = 0.1
			}
			// A fixture whose target path starts with bad-<id> fails that question.
			for file := range files {
				if strings.HasPrefix(file, "bad-"+id) {
					value = 0.1
				}
			}
			answers[id] = map[string]any{"type": "noul", "noul": value}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// writeSettings writes dir/.jev-check/.env.
func writeSettings(t *testing.T, dir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".jev-check", ".env"), content)
}

// gitInit makes dir a git repository with the fake server's settings. The settings
// file, output/, and the copied checks stay out of git and out of the tree it sends.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), ".jev-check/.env\n.jev-check/output/\n.jev-check/input/\n")
	writeSettings(t, dir, fakeSettings)
	addBundled(t, dir)
}

// runCmd runs one command and returns its exit code and stdout.
func runCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := run(args, &stdout, &stderr)
	if stderr.Len() > 0 {
		t.Logf("jev-check %s: stderr: %s", strings.Join(args, " "), stderr.String())
	}
	return code, stdout.String()
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantCode(t *testing.T, want int, args ...string) string {
	t.Helper()
	code, out := runCmd(t, args...)
	if code != want {
		t.Fatalf("jev-check %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, out)
	}
	return out
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

var maintainabilityIDs = []string{"no_mixed_output_channels", "no_redundant_forwarding"}

func maintainabilityAnswers(forwarding, output string) string {
	return `{"answers":{"no_redundant_forwarding":` + forwarding + `,"no_mixed_output_channels":` + output + `}}`
}

const onlyQuestion = `{"questions":{"only":{"type":"noul","instructions":"Is it fine?"}}}`

func dryRunQuestions(t *testing.T, args ...string) []string {
	t.Helper()
	var req jev.Request
	if err := json.Unmarshal([]byte(wantCode(t, 0, append([]string{"ask", "--dry-run"}, args...)...)), &req); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for id := range req.Questions {
		ids = append(ids, id)
	}
	return ids
}

// wantBlocked runs a command that must stop at the secret scan: exit 1, no request, no leaked value.
func wantBlocked(t *testing.T, requests *[]jev.Request, leak string, args ...string) string {
	t.Helper()
	before := len(*requests)
	var stdout, stderr strings.Builder
	code := run(args, &stdout, &stderr)
	out := stdout.String()
	if code != 1 || !strings.Contains(out, "SECRET  ") {
		t.Errorf("jev-check %s: exit %d, want 1 with a SECRET line\n%s%s", strings.Join(args, " "), code, out, stderr.String())
	}
	if strings.Contains(out+stderr.String(), leak) {
		t.Errorf("jev-check %s printed the secret:\n%s", strings.Join(args, " "), out)
	}
	if len(*requests) != before {
		t.Errorf("jev-check %s sent %d requests", strings.Join(args, " "), len(*requests)-before)
	}
	return out
}

// outputFiles lists the files under project/.jev-check/output/, cache entries included,
// so a test can tell that a blocked run wrote nothing.
func outputFiles(t *testing.T, project string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(project, ".jev-check", "output"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
}

// wantErr runs a command that must exit 2 with want in its error, and returns the error output.
func wantErr(t *testing.T, want string, args ...string) string {
	t.Helper()
	var stdout, stderr strings.Builder
	if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), want) {
		t.Errorf("jev-check %s: exit %d, want 2 with %q\n%s%s", strings.Join(args, " "), code, want, stdout.String(), stderr.String())
	}
	return stderr.String()
}

// countingServer answers every request with a server error and returns its URL and request count.
func countingServer(t *testing.T) (string, *int) {
	t.Helper()
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server.URL, &hits
}

// Fake keys are built at run time, so this file holds none.
var (
	ghToken  = "ghp_" + strings.Repeat("a1", 18)
	awsKey   = "AKIA" + strings.Repeat("Q", 16)
	password = "abc123" + "def456"
)
