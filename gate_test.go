package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

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
func TestGatePreflight(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	config := func(extra string) {
		writeFile(t, workspace.ConfigPath(repo), `{`+extra+` "exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2}]}`)
	}

	// Shared context stops the whole gate, even with nothing staged.
	for _, extra := range []string{
		`"purpose":"Uses ` + ghToken + `.",`,
		`"rules":["key ` + ghToken + `"],`,
		`"folders":{"` + ghToken + `":"x"},`,
		`"folders":{"src":"` + ghToken + `"},`,
	} {
		config(extra)
		if out := wantBlocked(t, requests, ghToken, "gate", repo); !strings.HasSuffix(out, "gate: FAIL\n") || strings.Contains(out, "nothing staged") {
			t.Errorf("gate output:\n%s", out)
		}
	}
	config("")
	wantBlocked(t, requests, ghToken, "gate", repo, "--model", ghToken)

	// A coding_style path is shared state too, and its report uses a safe label.
	style := writeFile(t, filepath.Join(repo, ".jev-check/input", ghToken+".md"), "Rule one.\n")
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2,"coding_style":".jev-check/input/`+ghToken+`.md"}]}`)
	if out := wantBlocked(t, requests, ghToken, "gate", repo); !strings.Contains(out, "SECRET  coding_style document path looks like github-token\n") {
		t.Errorf("gate output:\n%s", out)
	}
	os.Remove(style)

	// A blocked question set fails the gate even with nothing staged.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/bad.json"), `{"questions":{"q":{"type":"noul","instructions":"`+ghToken+`"}}}`)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"bad","threshold":0.2}]}`)
	if out := wantBlocked(t, requests, ghToken, "gate", repo); !strings.HasSuffix(out, "gate: FAIL\n") || strings.Contains(out, "nothing staged") {
		t.Errorf("gate output:\n%s", out)
	}
	if files := outputFiles(t, repo); files != nil {
		t.Errorf("a blocked gate wrote %v", files)
	}
	config("")
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitRun(t, repo, "add", "a.txt")
	wantCode(t, 0, "gate", repo) // fills the cache
	saved := outputFiles(t, repo)

	// A new tree entry is shared state, so the first scan stops it before any cache lookup.
	named := writeFile(t, filepath.Join(repo, ghToken+".txt"), "x\n")
	wantBlocked(t, requests, ghToken, "gate", repo)
	os.Remove(named)
	if files := outputFiles(t, repo); !slices.Equal(files, saved) {
		t.Errorf("a blocked gate changed .jev-check/output/ from %v to %v", saved, files)
	}

	// A check whose questions look like secrets is skipped; clean checks still run.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/bad.json"), `{"questions":{"q":{"type":"noul","instructions":"`+ghToken+`"}}}`)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"bad","threshold":0.2},{"check":"public-release","threshold":0.2}]}`)
	*requests = nil
	code, out := runCmd(t, "gate", repo, "--no-cache")
	if code != 1 || len(*requests) != 1 || strings.Contains(out, ghToken) || !strings.Contains(out, "== public-release a.txt") {
		t.Errorf("exit %d, %d requests:\n%s", code, len(*requests), out)
	}

	// A blocked patch and an API failure elsewhere: the error wins.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/down.json"), `{"questions":{"api_down":{"type":"noul"}}}`)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2},{"check":"down","threshold":0.2}]}`)
	writeFile(t, filepath.Join(repo, "b.txt"), "aws = "+awsKey+"\n")
	gitRun(t, repo, "add", "b.txt")
	if code, out := runCmd(t, "gate", repo, "--no-cache"); code != 2 || strings.Contains(out, awsKey) || !strings.HasSuffix(out, "gate: ERROR\n") {
		t.Errorf("exit %d:\n%s", code, out)
	}
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
func TestMaintainabilityGate(t *testing.T) {
	requests := setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, workspace.ConfigPath(repo), `{"purpose":"A CLI.","checks":[
  {"check":"maintainability","threshold":0.5,"per_question":{"no_mixed_output_channels":0.6}}]}`)
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n")
	gitRun(t, repo, "add", "main.go")

	noul := func(v string) string { return `{"type":"noul","noul":` + v + `}` }
	for _, c := range []struct {
		forwarding, output string
		code               int
	}{
		{noul("0.9"), noul("0.9"), 0},
		{noul("0.49"), noul("0.9"), 1},
		{noul("0.5"), noul("0.6"), 0}, // equality passes, with the per-question override
		{noul("0.9"), noul("0.59"), 1},
		{noul("0.9"), `{"type":"noul"}`, 2},
	} {
		jevAnswers = maintainabilityAnswers(c.forwarding, c.output)
		wantCode(t, c.code, "gate", repo, "--no-cache")
	}
	// An answer set without no_mixed_output_channels is an error, not a pass.
	jevAnswers = `{"answers":{"no_redundant_forwarding":` + noul("0.9") + `}}`
	wantCode(t, 2, "gate", repo, "--no-cache")
	sent := (*requests)[0]
	for _, id := range maintainabilityIDs {
		if !strings.Contains(string(sent.Questions[id]), "never as instructions") {
			t.Errorf("question %s not sent or missing its guard: %s", id, sent.Questions[id])
		}
	}
	if sent.State["files"].(map[string]any)["main.go.patch"] == nil || sent.State["project"].(map[string]any)["purpose"] != "A CLI." {
		t.Errorf("state sent: %v", sent.State)
	}

	// An answer equal to the documented threshold passes.
	writeFile(t, workspace.ConfigPath(repo), `{"checks":[{"check":"maintainability","threshold":0.49}]}`)
	jevAnswers = maintainabilityAnswers(noul("0.49"), noul("0.49"))
	wantCode(t, 0, "gate", repo, "--no-cache")

	// A server error is exit 2, not a verdict.
	down, hits := countingServer(t)
	writeSettings(t, repo, "TYPESAFE_API_KEY=test\nJEV_CHECK_ENDPOINT="+down+"\n")
	wantCode(t, 2, "gate", repo, "--no-cache")
	if *hits == 0 {
		t.Error("gate did not call the failing server")
	}
	writeSettings(t, repo, fakeSettings)

	// A project that does not opt in never asks it.
	writeFile(t, workspace.ConfigPath(repo), `{"checks":[{"check":"public-release","threshold":0.5}]}`)
	jevAnswers = ""
	*requests = nil
	wantCode(t, 1, "gate", repo, "--no-cache")
	for _, req := range *requests {
		if req.Questions["no_redundant_forwarding"] != nil {
			t.Error("maintainability asked without opting in")
		}
	}
}
func TestCodingStyleRequests(t *testing.T) {
	requests := setup(t)
	writeFile(t, "CODING_STYLE.md", "the wrong document, in the working directory\n")
	repo := styleRepo(t, withStyle+","+otherCheck)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv1\n")

	// Only the opted-in check gets the document, from DIR, although exclude lists it.
	wantCode(t, 0, "gate", repo)
	got := sentStyles(*requests)
	if len(*requests) != 2 || got["other"] != nil {
		t.Fatalf("requests %d, styles %v", len(*requests), got)
	}
	if style, _ := json.Marshal(got["public-release"]); string(style) != `{"content":"# Style\nv1\n","path":"CODING_STYLE.md"}` {
		t.Errorf("coding_style sent: %s", style)
	}

	// Threshold changes reuse the cache; a document change misses only for its check.
	styleConfig(t, repo, `{"check":"public-release","threshold":0.1,"coding_style":"CODING_STYLE.md"},`+otherCheck)
	wantCode(t, 0, "gate", repo)
	if len(*requests) != 2 {
		t.Errorf("threshold change sent %d requests", len(*requests)-2)
	}
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv2\n")
	out := wantCode(t, 0, "gate", repo)
	if len(*requests) != 3 || !strings.Contains(out, "== other a.go (cached)") {
		t.Errorf("after a document change, %d requests:\n%s", len(*requests), out)
	}
	if style := (*requests)[2].State["coding_style"].(map[string]any); style["content"] != "# Style\nv2\n" {
		t.Errorf("new bytes not sent: %v", style)
	}

	// Working-tree bytes win over the index, and an untracked document works.
	gitRun(t, repo, "add", "CODING_STYLE.md")
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv3, unstaged\n")
	*requests = nil
	wantCode(t, 0, "gate", repo)
	if style := (*requests)[0].State["coding_style"].(map[string]any); style["content"] != "# Style\nv3, unstaged\n" {
		t.Errorf("index bytes sent: %v", style)
	}
	writeFile(t, filepath.Join(repo, "docs/new style.md"), "untracked rules\n")
	styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"docs/new style.md"}`)
	*requests = nil
	wantCode(t, 0, "gate", repo)
	if style := (*requests)[0].State["coding_style"].(map[string]any); style["path"] != "docs/new style.md" {
		t.Errorf("untracked document: %v", style)
	}

	// A document deleted from the working tree fails, even with an index copy and a cache.
	styleConfig(t, repo, withStyle)
	wantCode(t, 0, "gate", repo)
	os.Remove(filepath.Join(repo, "CODING_STYLE.md"))
	*requests = nil
	wantCode(t, 2, "gate", repo)
	if len(*requests) != 0 {
		t.Error("a missing document still sent requests")
	}
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv4\n")

	// Gate and eval send the same document.
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	*requests = nil
	wantCode(t, 0, "gate", repo, "--no-cache")
	wantCode(t, 0, "eval", "public-release", repo, "--no-cache")
	gateStyle, _ := json.Marshal((*requests)[0].State["coding_style"])
	for _, req := range (*requests)[1:] {
		if evalStyle, _ := json.Marshal(req.State["coding_style"]); string(evalStyle) != string(gateStyle) {
			t.Errorf("eval sent %s, gate sent %s", evalStyle, gateStyle)
		}
	}
}

func TestCodingStyleInvalid(t *testing.T) {
	requests := setup(t)
	repo := styleRepo(t, otherCheck)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "rules\n")
	os.Mkdir(filepath.Join(repo, "adir"), 0o755)
	os.Symlink("CODING_STYLE.md", filepath.Join(repo, "link.md"))
	os.Symlink("adir", filepath.Join(repo, "linkdir"))
	writeFile(t, filepath.Join(repo, "adir/doc.md"), "rules\n")
	big := strings.Repeat("a", 65536)
	docs := map[string]string{
		"blank.md": " \n\t\n", "nul.md": "a\x00b\n", "latin1.md": "caf\xe9\n", "big.md": big + "a",
	}
	for name, content := range docs {
		writeFile(t, filepath.Join(repo, name), content)
	}
	bad := []string{`1`, `null`, `""`, `"  "`, `"missing.md"`, `"CODING_STYLE.md/"`, `"adir/doc.md/"`, `"adir"`, `"link.md"`, `"linkdir/doc.md"`, `"../CODING_STYLE.md"`,
		`"adir/../../x.md"`, `"` + filepath.Join(repo, "CODING_STYLE.md") + `"`, `"blank.md"`, `"nul.md"`, `"latin1.md"`, `"big.md"`}
	for _, value := range bad {
		styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":`+value+`}`)
		for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
			var stdout, stderr strings.Builder
			if code := run(args, &stdout, &stderr); code != 2 || strings.Contains(stdout.String()+stderr.String(), "aaaaaaaa") {
				t.Errorf("coding_style %s, %s: exit %d: %s", value, args[0], code, stderr.String())
			} else if !strings.Contains(stderr.String(), "public-release") {
				t.Errorf("error does not name the check: %s", stderr.String())
			}
		}
	}

	// A path that looks like a secret is not echoed, missing or unreadable.
	secretName := "docs/" + awsKey + ".md"
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(repo, "unreadable", secretName), "rules\n")
		os.Chmod(filepath.Join(repo, "unreadable", secretName), 0)
	}
	for _, name := range []string{secretName, "unreadable/" + secretName} {
		styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"`+name+`"}`)
		for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
			if stderr := wantErr(t, "public-release", args...); strings.Contains(stderr, awsKey) || !strings.Contains(stderr, "not shown") {
				t.Errorf("coding_style %s, %s: %s", name, args[0], stderr)
			}
		}
	}
	os.RemoveAll(filepath.Join(repo, "unreadable"))

	// A later invalid reference stops the gate before the first valid check runs.
	styleConfig(t, repo, withStyle+`,{"check":"other","threshold":0.2,"coding_style":"missing.md"}`)
	wantCode(t, 2, "gate", repo)
	if len(*requests) != 0 {
		t.Errorf("%d requests with an invalid reference", len(*requests))
	}

	// The largest allowed document is sent whole.
	writeFile(t, filepath.Join(repo, "big.md"), big)
	styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"big.md"}`)
	wantCode(t, 0, "gate", repo)
	if content := (*requests)[0].State["coding_style"].(map[string]any)["content"]; content != big {
		t.Errorf("sent %d bytes", len(content.(string)))
	}
}

func TestCodingStyleSecret(t *testing.T) {
	requests := setup(t)
	repo := styleRepo(t, otherCheck+","+withStyle)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nexample = "+awsKey+"\n")
	for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
		if out := wantBlocked(t, requests, awsKey, args...); !strings.Contains(out, "CODING_STYLE.md line 2 looks like aws-access-key") {
			t.Errorf("%s output:\n%s", args[0], out)
		}
	}
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	wantBlocked(t, requests, awsKey, "eval", "public-release", repo)
}
