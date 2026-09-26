package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestSettingsFile(t *testing.T) {
	requests := setup(t)
	project, _ := os.Getwd()
	wantCode(t, 0, "ask", "example")
	if len(*requests) != 1 || (*requests)[0].Model != "jev-latest" {
		t.Fatalf("requests %+v", *requests)
	}

	// Changing the file sends the next request elsewhere.
	other, hits := countingServer(t)
	writeSettings(t, project, "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT="+other+"\n")
	wantCode(t, 2, "ask", "example")
	if *hits != 1 || len(*requests) != 1 {
		t.Errorf("other server got %d, fake server %d", hits, len(*requests))
	}

	// The environment and the project's .env are never read.
	settings := filepath.Join(project, ".jev-check", ".env")
	writeSettings(t, project, "JEV_CHECK_ENDPOINT="+fakeEndpoint+"\n")
	writeFile(t, filepath.Join(project, ".env"), "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT=https://example.com\n")
	wantErr(t, settings, "ask", "example")
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("JEV_CHECK_ENDPOINT", "https://example.com")
	if stderr := wantErr(t, settings, "ask", "example"); !strings.Contains(stderr, "environment is ignored") {
		t.Errorf("no note about the environment: %s", stderr)
	}
	os.Remove(settings)
	wantErr(t, settings, "ask", "example")
	if os.Geteuid() != 0 {
		writeSettings(t, project, fakeSettings)
		os.Chmod(settings, 0)
		wantErr(t, settings, "ask", "--dry-run", "example")
	}
	if len(*requests) != 1 {
		t.Errorf("sent %d requests without a key", len(*requests)-1)
	}
}

func TestTrackedSettings(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a", "x\n"))
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitRun(t, repo, "add", "a.txt")
	wantCode(t, 0, "gate", repo)
	gitRun(t, repo, "add", "-f", ".jev-check/.env")
	*requests = nil
	wantErr(t, "tracked", "gate", repo, "--no-cache")
	wantErr(t, "tracked", "eval", "two", repo)
	if out := wantCode(t, 2, "doctor", repo); !strings.Contains(out, "tracked") {
		t.Errorf("doctor:\n%s", out)
	}
	t.Chdir(repo)
	wantErr(t, "tracked", "ask", "--dry-run", "example")
	wantErr(t, "tracked", "ask", "example")
	if len(*requests) != 0 {
		t.Errorf("sent %d requests with a tracked settings file", len(*requests))
	}
}

func TestModelSetting(t *testing.T) {
	requests := setup(t)
	project, _ := os.Getwd()
	dryModel := func(args ...string) string {
		t.Helper()
		var req request
		json.Unmarshal([]byte(wantCode(t, 0, append([]string{"ask", "--dry-run", "example"}, args...)...)), &req)
		return req.Model
	}
	writeSettings(t, project, fakeSettings+"JEV_CHECK_MODEL=from-file\n")
	if got := dryModel(); got != "from-file" {
		t.Errorf("dry run model %q", got)
	}
	if got := dryModel("--model", "from-flag"); got != "from-flag" {
		t.Errorf("dry run model %q", got)
	}
	wantErr(t, "--model", "ask", "--dry-run", "example", "--model", "")

	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a", "x\n"))
	for _, q := range []string{"q1", "q2"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q, "x\n"))
	}
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitRun(t, repo, "add", "a.txt")
	for _, c := range []struct{ file, flag, want string }{
		{"", "", "jev-latest"},
		{"from-file", "", "from-file"},
		{"from-file", "from-flag", "from-flag"},
	} {
		settings := fakeSettings
		if c.file != "" {
			settings += "JEV_CHECK_MODEL=" + c.file + "\n"
		}
		writeSettings(t, repo, settings)
		var flag []string
		if c.flag != "" {
			flag = []string{"--model", c.flag}
		}
		for _, args := range [][]string{append([]string{"gate", repo}, flag...), append([]string{"eval", "two", repo}, flag...)} {
			*requests = nil
			wantCode(t, 0, args...)
			if len(*requests) == 0 || slices.ContainsFunc(*requests, func(r request) bool { return r.Model != c.want }) {
				t.Errorf("jev-check %v sent %+v, want model %s", args, *requests, c.want)
			}
		}
	}
	wantErr(t, "--model", "gate", repo, "--model", "")
	wantErr(t, "--model", "eval", "two", repo, "--model", "")
	// The model goes with the shared state, so the gate's first scan covers it.
	wantBlocked(t, requests, ghToken, "gate", repo, "--model", ghToken)
	wantBlocked(t, requests, ghToken, "eval", "two", repo, "--model", ghToken)
}

func TestEndpointSetting(t *testing.T) {
	requests := setup(t)
	project, _ := os.Getwd()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"checks":[{"check":"public-release","threshold":0.5}]}`)
	for _, bad := range []string{
		"not a url", "/v1/systemone", "api.typesafe.ai/v1", "https:///v1", "ftp://127.0.0.1/",
		"http://api.typesafe.ai/v1", "http://10.0.0.1/", "http://localhost.example.com/",
		"https://user:hunter2" + "@api.typesafe.ai/", "https://hunter2@api.typesafe.ai/", "https://api.typesafe.ai/v1#x",
	} {
		for _, dir := range []string{project, repo} {
			writeSettings(t, dir, "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT="+bad+"\n")
		}
		for _, args := range [][]string{{"ask", "--dry-run", "example"}, {"ask", "example"}, {"gate", repo}} {
			if stderr := wantErr(t, "JEV_CHECK_ENDPOINT", args...); strings.Contains(stderr, "hunter2") {
				t.Errorf("printed user info: %s", stderr)
			}
		}
		if out := wantCode(t, 2, "doctor", repo); strings.Contains(out, "hunter2") || !strings.Contains(out, "JEV_CHECK_ENDPOINT") {
			t.Errorf("doctor:\n%s", out)
		}
	}
	for _, good := range []string{"https://api.typesafe.ai/v1/systemone", "http://localhost:1/x", "http://LOCALHOST:1/", "http://127.0.0.2:1/", "http://[::1]:1/"} {
		writeSettings(t, project, "JEV_CHECK_ENDPOINT="+good+"\n")
		wantCode(t, 0, "ask", "--dry-run", "example")
	}
	if len(*requests) != 0 {
		t.Errorf("sent %d requests", len(*requests))
	}

	// A redirect is an error, and its target never sees the key.
	target, hits := countingServer(t)
	redirect := httptest.NewServer(http.RedirectHandler(target, http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)
	writeSettings(t, project, "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT="+redirect.URL+"\n")
	wantErr(t, "307", "ask", "example")
	if *hits != 0 {
		t.Error("the redirect was followed")
	}
}

func TestDoctor(t *testing.T) {
	requests := setup(t)
	const key = "k3y-value-never-shown"
	repo := t.TempDir()
	gitInit(t, repo)
	settings := filepath.Join(repo, ".jev-check", ".env")
	good := "TYPESAFE_API_KEY=" + key + "\nJEV_CHECK_ENDPOINT=" + fakeEndpoint + "\n"
	writeSettings(t, repo, good)
	config := `{"checks":[{"check":"public-release","threshold":0.5}]}`
	writeFile(t, filepath.Join(repo, "project-context.json"), config)

	out := wantCode(t, 0, "doctor", repo)
	for _, want := range []string{
		"ok    settings file  " + settings + "\n",
		"ok    endpoint  " + fakeEndpoint + " (" + settings + ")\n",
		"ok    model  jev-latest (default)\n",
		"ok    API key  set\n",
		"ok    project  " + filepath.Join(repo, "project-context.json") + "\n",
		"ok    git and output  " + repo + " is writable\n",
		"doctor: ok; the key and the service were not tested\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	os.Mkdir(filepath.Join(repo, "output"), 0o755)
	if out := wantCode(t, 0, "doctor", repo); !strings.Contains(out, filepath.Join(repo, "output")+" is writable") {
		t.Errorf("doctor:\n%s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(repo, "output")); len(entries) != 0 {
		t.Errorf("doctor left %v", entries)
	}
	writeSettings(t, repo, good+"JEV_CHECK_MODEL=pinned\n")
	if out := wantCode(t, 0, "doctor", repo); !strings.Contains(out, "ok    model  pinned ("+settings+")\n") {
		t.Errorf("doctor:\n%s", out)
	}

	// Each broken item fails the doctor and names its problem.
	for name, c := range map[string]struct {
		settings, config, want string
	}{
		"no key":          {"JEV_CHECK_ENDPOINT=" + fakeEndpoint + "\n", config, "FAIL  API key  missing"},
		"first line wins": {good + "JEV_CHECK_ENDPOINT=x\n", config, ""},
		"bad endpoint":    {"JEV_CHECK_ENDPOINT=http://example.com\nTYPESAFE_API_KEY=x\n", config, "FAIL  endpoint  JEV_CHECK_ENDPOINT"},
		"project":         {good, `{"checks":[]}`, "FAIL  project  "},
		"no project":      {good, "", "FAIL  project  "},
	} {
		t.Run(name, func(t *testing.T) {
			writeSettings(t, repo, c.settings)
			os.Remove(filepath.Join(repo, "project-context.json"))
			if c.config != "" {
				writeFile(t, filepath.Join(repo, "project-context.json"), c.config)
			}
			want := 2
			if c.want == "" {
				want = 0 // a later line for a name never overrides the first
			}
			if out := wantCode(t, want, "doctor", repo); !strings.Contains(out, c.want) || !strings.Contains(out, "not tested") {
				t.Errorf("doctor:\n%s", out)
			}
		})
	}
	writeSettings(t, repo, good)
	writeFile(t, filepath.Join(repo, "project-context.json"), config)

	os.Remove(settings)
	if out := wantCode(t, 2, "doctor", repo); !strings.Contains(out, "ok    settings file  "+settings+" missing") {
		t.Errorf("doctor:\n%s", out)
	}
	notGit := t.TempDir()
	writeSettings(t, notGit, good)
	if out := wantCode(t, 2, "doctor", notGit); !strings.Contains(out, "FAIL  git and output  ") {
		t.Errorf("doctor:\n%s", out)
	}
	writeSettings(t, repo, good)
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(repo, "output"), 0o555)
		if out := wantCode(t, 2, "doctor", repo); !strings.Contains(out, "FAIL  git and output  ") {
			t.Errorf("doctor:\n%s", out)
		}
		os.Chmod(filepath.Join(repo, "output"), 0o755)
		os.Chmod(settings, 0)
		if out := wantCode(t, 2, "doctor", repo); !strings.Contains(out, "FAIL  settings file  ") {
			t.Errorf("doctor:\n%s", out)
		}
		os.Chmod(settings, 0o644)
	}

	t.Setenv("TYPESAFE_API_KEY", key)
	if out := wantCode(t, 0, "doctor", repo); !strings.Contains(out, "environment") {
		t.Errorf("no note about the environment:\n%s", out)
	}
	writeSettings(t, repo, good+"JEV_CHECK_MODEL="+ghToken+"\n")
	wantBlocked(t, requests, ghToken, "doctor", repo)

	// The key never appears, and the doctor never calls the API.
	var all strings.Builder
	for _, args := range [][]string{{"doctor", repo}, {"doctor", "--help"}} {
		_, out := jev(t, args...)
		all.WriteString(out)
	}
	if strings.Contains(all.String(), key) {
		t.Errorf("doctor printed the key:\n%s", all.String())
	}
	wantCode(t, 2, "doctor", repo, repo)
	if len(*requests) != 0 {
		t.Errorf("doctor sent %d requests", len(*requests))
	}
}
