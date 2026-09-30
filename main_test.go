package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

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

func TestAsk(t *testing.T) {
	setup(t)
	patch := writeFile(t, filepath.Join(t.TempDir(), "change.patch"), "print(\"hello\")\n")

	if out := wantCode(t, 0, "list"); !strings.Contains(out, "public-release [project, needs --file]\n") {
		t.Errorf("list: %s", out)
	}

	var req jev.Request
	out := wantCode(t, 0, "ask", "--dry-run", "public-release", "--file", patch)
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatal(err)
	}
	if req.State["files"].(map[string]any)[patch] != "print(\"hello\")\n" || req.Questions["english_only"] == nil {
		t.Errorf("dry run: %s", out)
	}
	out = wantCode(t, 0, "ask", "--dry-run", "example")
	if err := json.Unmarshal([]byte(out), &req); err != nil || len(req.State["files"].(map[string]any)) == 0 {
		t.Errorf("default state not sent: %v", err)
	}

	wantCode(t, 2, "ask", "public-release")
	wantCode(t, 2, "ask", "../x", "--file", patch)
	wantCode(t, 2, "ask", "public-release", "--file", patch, "--threshold", "2")
	wantCode(t, 2, "ask", "no-such-check", "--file", patch)

	out = wantCode(t, 1, "ask", "public-release", "--file", patch, "--threshold", "0.5")
	project, _ := os.Getwd()
	for _, line := range []string{"FAIL  0.3  english_only\n", "ok    0.9  no_personal_info\n", "saved: " + filepath.Join(project, ".jev-check", "output") + "/"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
	wantCode(t, 0, "ask", "public-release", "--file", patch, "--threshold", "0.2")

	writeSettings(t, ".", "TYPESAFE_API_KEY=wrong\nJEV_CHECK_ENDPOINT="+fakeEndpoint+"\n")
	wantCode(t, 2, "ask", "public-release", "--file", patch)
}

func TestSecrets(t *testing.T) {
	dir := t.TempDir()
	// Fake keys are built at run time, so this file holds none.
	r := strings.Repeat
	leaks := [][2]string{
		{"aws-access-key", "aws = AKIA" + r("Q", 16)},
		{"env-secret", "export DB_PASSWORD=" + r("p4", 8)},
		{"private-key", "-----BEGIN OPENSSH PRIV" + "ATE KEY-----"},
		{"github-token", "token: ghp_" + r("a1", 18)},
		{"gitlab-token", "glpat-" + r("x1", 10)},
		{"slack-token", "xoxb-" + r("12", 6)},
		{"stripe-key", "stripe = sk_live_" + r("a1", 12)},
		{"google-api-key", "AIza" + r("B", 35)},
		{"sk-api-key", "sk-ant-api03-" + r("c2", 12)},
		{"jwt", "eyJ" + r("h", 12) + ".eyJ" + r("p", 12) + "." + r("s", 12)},
		{"url-credentials", "postgres://app:s3cr3t" + "@db:5432"},
		{"bearer-literal", "Authorization: Bearer " + r("t9", 12)},
		{"quoted-secret", `api_key = "` + r("k7", 8) + `"`},
	}
	patch := "+++ b/x\n"
	for _, leak := range leaks {
		patch += "+" + leak[1] + "\n"
	}
	code, out := runCmd(t, "secrets", writeFile(t, filepath.Join(dir, "leak.patch"), patch))
	if code != 1 {
		t.Errorf("exit %d on keys, want 1", code)
	}
	for _, leak := range leaks {
		if !strings.Contains(out, "looks like "+leak[0]+"\n") {
			t.Errorf("%s not reported", leak[0])
		}
		if strings.Contains(out, leak[1]) {
			t.Errorf("printed the secret for %s", leak[0])
		}
	}

	clean := []string{`TOKEN=$(cat file)`, `api_key = os.environ["KEY"]`, `KEY=`, `API_KEY=your-api-key-here`,
		`password = "changeme-please"`, `postgres://user:password@localhost`, `Authorization: Bearer $TOKEN`,
		`SKILL_md = "a3b9c1d7e5f2a3b9c1d7e5f2a3b9c1d7"`}
	wantCode(t, 0, "secrets", writeFile(t, filepath.Join(dir, "clean.patch"), "+"+strings.Join(clean, "\n+")+"\n"))

	sources, _ := filepath.Glob("*.go")
	internal, _ := filepath.Glob("internal/*/*.go")
	sources = append(sources, internal...)
	if len(internal) == 0 {
		t.Fatal("found no internal/*/*.go to scan")
	}
	for _, source := range sources {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		self := "+" + strings.ReplaceAll(string(content), "\n", "\n+")
		if reports := secretscan.Scan(source, self); reports != nil {
			t.Errorf("false positive on %s: %v", source, reports)
		}
	}
	wantCode(t, 2, "secrets", filepath.Join(dir, "missing.patch"))
}

func TestJudge(t *testing.T) {
	out := writeFile(t, filepath.Join(t.TempDir(), "out.json"), `{"response":`+fakeAnswers+`}`)
	wantCode(t, 0, "judge", out, "0.5", "b=0.2")
	got := wantCode(t, 1, "judge", out, "0.5")
	if !strings.Contains(got, "FAIL  0.3  b\n") || !strings.Contains(got, "info  x 0.8  c\n") {
		t.Errorf("judge output:\n%s", got)
	}
	wantCode(t, 2, "judge", out, "0.5", "zzz=0.1")
	wantCode(t, 2, "judge", out, "0.5", "b=x")
	wantCode(t, 2, "judge", out, "1.5")
	for _, response := range []string{`{"answers":{}}`, `{"answers":{"a":{"type":"noul"}}}`} {
		writeFile(t, out, `{"response":`+response+`}`)
		wantCode(t, 2, "judge", out, "0")
	}
	writeFile(t, out, `{"request":{"questions":{"missing":{"type":"noul"}}},"response":`+fakeAnswers+`}`)
	wantCode(t, 2, "judge", out, "0")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGate(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	wantCode(t, 2, "gate", repo) // not a git repository, so the gate must not pass

	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, "app.py"), "print(\"hello\")\n")
	writeFile(t, filepath.Join(repo, "README.md"), "# doc\n")
	writeFile(t, filepath.Join(repo, ".jev-check", "output", "log"), "x\n")
	writeFile(t, workspace.ConfigPath(repo), `{ "purpose": "Test project.", "exclude": [".jev-check/output/"],
  "checks": [{ "check": "public-release", "threshold": 0.5, "per_question": { "no_personal_info": 0.2 }, "skip": ["*.md"] }] }`)

	var state struct {
		Project map[string]any `json:"project"`
		Tree    []string       `json:"tree"`
	}
	if err := json.Unmarshal([]byte(wantCode(t, 0, "state", repo)), &state); err != nil {
		t.Fatal(err)
	}
	slices.Sort(state.Tree)
	if fmt.Sprint(state.Project) != "map[purpose:Test project.]" || !slices.Equal(state.Tree, []string{".jev-check/config.json", "README.md", "app.py"}) {
		t.Errorf("context: %+v", state)
	}

	if out := wantCode(t, 0, "gate", repo); out != "nothing staged\n" {
		t.Errorf("empty gate: %s", out)
	}

	// The fake answers english_only=0.3, so the gate passes only with a threshold at or below that.
	gitRun(t, repo, "add", "app.py", "README.md")
	out := wantCode(t, 1, "gate", repo)
	if !strings.Contains(out, "== public-release app.py\n") || strings.Contains(out, "README") || !strings.HasSuffix(out, "gate: FAIL\n") {
		t.Errorf("gate output:\n%s", out)
	}
	if len(*requests) != 1 {
		t.Fatalf("sent %d requests, want 1", len(*requests))
	}
	sent := (*requests)[0].State
	if files := sent["files"].(map[string]any); len(files) != 1 || files["app.py.patch"] == nil {
		t.Errorf("gate sent files %v", slices.Collect(maps.Keys(files)))
	}
	if sent["project"].(map[string]any)["purpose"] != "Test project." {
		t.Errorf("gate sent project %v", sent["project"])
	}

	// An unchanged request comes from the cache. --no-cache sends it again.
	if out := wantCode(t, 1, "gate", repo); !strings.Contains(out, "== public-release app.py (cached)\n") || len(*requests) != 1 {
		t.Errorf("cache missed, %d requests:\n%s", len(*requests), out)
	}
	wantCode(t, 1, "gate", repo, "--no-cache")
	if len(*requests) != 2 {
		t.Errorf("--no-cache sent %d requests, want 2", len(*requests))
	}
	wantCode(t, 2, "gate", repo, "--bogus")
	if fsutil.FileExists(filepath.Join(repo, "output")) || !fsutil.FileExists(filepath.Join(repo, ".jev-check", "output", "cache", "v2")) {
		t.Error("gate wrote outside .jev-check/output/")
	}

	writeFile(t, workspace.ConfigPath(repo), `{ "checks": [{ "check": "public-release", "threshold": 0.2 }] }`)
	gitRun(t, repo, "add", ".jev-check/config.json")
	if out := wantCode(t, 0, "gate", repo); !strings.HasSuffix(out, "gate: PASS\n") {
		t.Errorf("gate output:\n%s", out)
	}

	for _, bad := range []string{
		`{ "checks": [{ "check": "public-release", "threshold": 0.2, "per_question": { "typo": 0.5 } }] }`,
		`{ "checks": [{ "check": "public-release" }] }`,
		`{ "checks": [] }`,
	} {
		writeFile(t, workspace.ConfigPath(repo), bad)
		wantCode(t, 2, "gate", repo)
	}
	writeFile(t, workspace.ConfigPath(repo), `{ "checks": [{ "check": "public-release", "threshold": 0.2 }] }`)

	// One unsafe patch is withheld; the clean staged README.md still reaches the API.
	*requests = nil
	writeFile(t, filepath.Join(repo, "app.py"), "aws = \"AKIA"+strings.Repeat("Q", 16)+"\"\n")
	gitRun(t, repo, "add", "app.py")
	out = wantCode(t, 1, "gate", repo, "--no-cache")
	if !strings.Contains(out, "SECRET  app.py.patch line") || !strings.Contains(out, "== public-release README.md\n") || !strings.HasSuffix(out, "gate: FAIL\n") {
		t.Errorf("gate did not report the key:\n%s", out)
	}
	sentClean := false
	for _, req := range *requests {
		files := req.State["files"].(map[string]any)
		if files["app.py.patch"] != nil {
			t.Error("gate sent a patch with a key")
		}
		sentClean = sentClean || files["README.md.patch"] != nil
	}
	if !sentClean {
		t.Error("gate did not send the clean patch")
	}
}

func TestEval(t *testing.T) {
	setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	config := func(threshold string) {
		writeFile(t, workspace.ConfigPath(repo), `{ "exclude": [".jev-check/fixtures/"], "checks": [{ "check": "public-release", "threshold": `+threshold+` }] }`)
	}
	config("0.2")
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	wantCode(t, 2, "eval", "public-release", repo) // no fixtures yet

	writeFile(t, filepath.Join(fixtures, "pass", "a.go.patch"), gitPatch(t, "a.go", "package a\n"))
	// The fake answers 0.1 for a question when the path starts with bad-<question>.
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.go.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	if out := wantCode(t, 0, "eval", "public-release", repo); !strings.HasSuffix(out, "eval: 0 misses in 7 fixtures\n") {
		t.Errorf("eval output:\n%s", out)
	}

	// The fake answers english_only=0.3, so a pass fixture misses at a threshold of 0.5.
	config("0.5")
	if out := wantCode(t, 1, "eval", "public-release", repo); !strings.Contains(out, "MISS  0.3  english_only  pass/a.go.patch fails it\n") {
		t.Errorf("eval output:\n%s", out)
	}

	wantCode(t, 2, "eval", "no-such-check", repo)
	writeFile(t, filepath.Join(fixtures, "fail", "typo", "c.patch"), gitPatch(t, "c", "x\n"))
	wantCode(t, 2, "eval", "public-release", repo)
	os.RemoveAll(filepath.Join(fixtures, "fail", "typo"))
	writeFile(t, filepath.Join(fixtures, "pass", "no-header.patch"), "+x\n")
	wantCode(t, 2, "eval", "public-release", repo)
}

func TestInvalidAnswers(t *testing.T) {
	requests := setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, workspace.ConfigPath(repo), `{"checks":[{"check":"public-release","threshold":0}]}`)
	file := writeFile(t, filepath.Join(repo, "x.go"), "package x\n")
	gitRun(t, repo, "add", "x.go")
	writeFile(t, filepath.Join(repo, ".jev-check/fixtures/public-release/pass/x.patch"), gitPatch(t, "x", "hello\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(repo, ".jev-check/fixtures/public-release/fail", q, "x.patch"), gitPatch(t, "x", "hello\n"))
	}
	c, err := catalog.Find(repo, "public-release")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `null`, `{"type":"choice","choice":"yes"}`, `{"type":"noul"}`, `{"type":"noul","noul":null}`, `{"type":"noul","noul":1.1}`, `{"type":"noul","noul":-0.1}`} {
		t.Run(bad, func(t *testing.T) {
			answers := map[string]json.RawMessage{}
			for id := range c.Questions {
				answers[id] = json.RawMessage(`{"type":"noul","noul":0.9}`)
			}
			answers["english_only"] = json.RawMessage(bad)
			raw, err := json.Marshal(map[string]any{"answers": answers})
			if err != nil {
				t.Fatal(err)
			}
			jevAnswers = string(raw)
			wantCode(t, 2, "ask", "public-release", "--file", file, "--threshold", "0")
			wantCode(t, 2, "gate", repo, "--no-cache")
			wantCode(t, 2, "eval", "public-release", repo, "--no-cache")
		})
	}
	for _, raw := range []string{`{"answers":{}}`, `{"answers":{"english_only":{"type":"noul","noul":1}}}`} {
		jevAnswers = raw
		wantCode(t, 2, "gate", repo, "--no-cache")
		wantCode(t, 2, "eval", "public-release", repo, "--no-cache")
	}
	jevAnswers = ""
	wantCode(t, 0, "gate", repo)
	caches, err := filepath.Glob(filepath.Join(repo, ".jev-check/output/cache/v2/*.json"))
	if err != nil || len(caches) != 1 {
		t.Fatalf("cache files: %v, %v", caches, err)
	}
	for _, raw := range []string{`{"answers":{}}`, `{"answers":{"english_only":{"type":"noul","noul":1}}}`} {
		writeFile(t, caches[0], `{"version":2,"created_at":"`+time.Now().UTC().Format(time.RFC3339)+`","response":`+raw+`}`)
		before := len(*requests)
		jevAnswers = raw
		wantCode(t, 2, "gate", repo)
		if len(*requests) != before+1 {
			t.Fatal("invalid cache did not trigger a new API call")
		}
	}
	// Zero is a valid probability, unlike an omitted or null noul value.
	answers := map[string]json.RawMessage{}
	for id := range c.Questions {
		answers[id] = json.RawMessage(`{"type":"noul","noul":0}`)
	}
	raw, err := json.Marshal(map[string]any{"answers": answers})
	if err != nil {
		t.Fatal(err)
	}
	jevAnswers = string(raw)
	wantCode(t, 0, "gate", repo, "--no-cache")
}

func TestGateBlocksRemovedAndContextSecrets(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			requests := setup(t)
			repo := t.TempDir()
			gitInit(t, repo)
			writeFile(t, workspace.ConfigPath(repo), `{"checks":[{"check":"public-release","threshold":0.5}]}`)
			secret := "DB_PASSWORD=" + strings.Repeat("p4", 8) + "\n"
			file := writeFile(t, filepath.Join(repo, "config.txt"), secret+"old\n")
			gitRun(t, repo, "add", ".")
			gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial")
			content := "new\n"
			if keep {
				content = secret + content
			}
			writeFile(t, file, content)
			gitRun(t, repo, "add", "config.txt")
			out := wantCode(t, 1, "gate", repo)
			if !strings.Contains(out, "SECRET  config.txt.patch line") || strings.Contains(out, strings.TrimSpace(secret)) {
				t.Fatalf("unexpected report: %s", out)
			}
			if len(*requests) != 0 {
				t.Fatal("patch containing a secret reached the API")
			}
		})
	}
}

func TestAskRejectsNullState(t *testing.T) {
	requests := setup(t)
	state := writeFile(t, filepath.Join(t.TempDir(), "state.json"), "null")
	file := writeFile(t, filepath.Join(t.TempDir(), "x"), "hello")
	for _, args := range [][]string{{"ask", "example", state}, {"ask", "example", state, "--file", file, "--dry-run"}} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "state must be an object") {
			t.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
	}
	if len(*requests) != 0 {
		t.Fatal("invalid state reached the API")
	}
}
