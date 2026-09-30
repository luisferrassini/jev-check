package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

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
func TestAskPreflight(t *testing.T) {
	requests := setup(t)
	dir := t.TempDir()
	clean := writeFile(t, filepath.Join(dir, "clean.txt"), "hello\n")

	for name, c := range map[string]struct{ leak, state string }{
		"nested array":        {ghToken, `{"a":{"b":["x","token ` + ghToken + `"]}}`},
		"dynamic key":         {ghToken, `{"a":{"` + ghToken + `":"x"}}`},
		"unicode escape":      {awsKey, `{"a":"\u0041` + awsKey[1:] + `"}`},
		"escaped newlines":    {"PRIVATE", `{"a":"line one\n-----BEGIN RSA PRIV` + `ATE KEY-----\nabc"}`},
		"assignment only":     {password, `{"db":{"password":"` + password + `"}}`},
		"assignment a number": {"123456789012", `{"db":{"password":123456789012}}`},
		"assignment in array": {password, `{"db":{"password":["x","` + password + `"]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			state := writeFile(t, filepath.Join(dir, "state.json"), c.state)
			wantBlocked(t, requests, c.leak, "ask", "public-release", state, "--dry-run")
			wantBlocked(t, requests, c.leak, "ask", "public-release", state)
		})
	}
	placeholder := writeFile(t, filepath.Join(dir, "ok.json"), `{"db":{"password":"changeme-please"}}`)
	wantCode(t, 0, "ask", "public-release", placeholder, "--dry-run")

	leaky := writeFile(t, filepath.Join(dir, "leak.txt"), "key = "+awsKey+"\n")
	out := wantBlocked(t, requests, awsKey, "ask", "public-release", "--file", leaky)
	if !strings.Contains(out, leaky+" line 1 looks like aws-access-key") {
		t.Errorf("report does not name the file and line:\n%s", out)
	}
	wantBlocked(t, requests, awsKey, "ask", "public-release", "--file", leaky, "--dry-run")
	named := writeFile(t, filepath.Join(dir, ghToken+".txt"), "hello\n")
	wantBlocked(t, requests, ghToken, "ask", "public-release", "--file", named)
	wantBlocked(t, requests, ghToken, "ask", "public-release", "--file", clean, "--model", ghToken)

	for _, q := range []string{
		`{"questions":{"q":{"type":"noul","instructions":"Is ` + ghToken + ` fine?"}}}`,
		`{"questions":{"` + ghToken + `":{"type":"noul"}}}`,
		`{"questions":{"q\nSECRET  forged":{"type":"noul","criteria":{"true":["` + awsKey + `"]}}}}`,
	} {
		out := wantBlocked(t, requests, ghToken, "ask", writeFile(t, filepath.Join(dir, "q.json"), q), "--file", clean)
		if strings.Contains(out, awsKey) || strings.Contains(out, "forged") {
			t.Errorf("question id echoed:\n%s", out)
		}
	}

	// Nothing blocked is saved or cached, and the credential is never needed.
	writeSettings(t, ".", "")
	wantBlocked(t, requests, awsKey, "ask", "public-release", "--file", leaky)
	if files := outputFiles(t, "."); files != nil || fsutil.FileExists(filepath.Join(".jev-check", "output")) {
		t.Errorf("a blocked request created .jev-check/output/: %v", files)
	}
}
func TestProjectChecks(t *testing.T) {
	requests := setup(t)
	project, _ := os.Getwd()
	file := writeFile(t, filepath.Join(project, "x.txt"), "hello\n")

	// The project's copy is what runs, edited or not, and its state is the project's state file.
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/public-release.json"), onlyQuestion)
	out := wantCode(t, 0, "list")
	if strings.Count(out, "public-release [") != 1 || !strings.Contains(out, "public-release [project, needs --file]\n") {
		t.Errorf("list:\n%s", out)
	}
	if ids := dryRunQuestions(t, "public-release", "--file", file); len(ids) != 1 || ids[0] != "only" {
		t.Errorf("local check not used: %v", ids)
	}
	if !strings.Contains(out, "example [project, has default state]\n") {
		t.Errorf("list:\n%s", out)
	}
	dryRunQuestions(t, "example")
	os.Remove(filepath.Join(project, ".jev-check/input/states/example.json"))
	wantCode(t, 2, "ask", "--dry-run", "example")
	state := writeFile(t, filepath.Join(project, "state.json"), `{"files":{"a":"b"}}`)
	dryRunQuestions(t, "example", state)

	// A missing check is never read from the bundle: the error names the file and the add command.
	os.Remove(filepath.Join(project, ".jev-check/input/questions/example.json"))
	if out := wantCode(t, 0, "list"); !strings.Contains(out, "example [bundled, not added: jev-check add example]\n") {
		t.Errorf("list:\n%s", out)
	}
	if err := wantErr(t, "jev-check add example", "ask", "--dry-run", "example"); !strings.Contains(err, filepath.Join(project, ".jev-check/input/questions/example.json")) {
		t.Errorf("missing check error: %s", err)
	}

	// add copies the questions and the state, keeps existing files, and checks every name first.
	writeFile(t, filepath.Join(project, ".jev-check/input/states/example.json"), `{"files":{"mine":"x"}}`)
	if out := wantCode(t, 0, "add", "example"); !strings.Contains(out, "created "+filepath.Join(project, ".jev-check/input/questions/example.json")) ||
		!strings.Contains(out, "kept    "+filepath.Join(project, ".jev-check/input/states/example.json")) {
		t.Errorf("add:\n%s", out)
	}
	if data, _ := os.ReadFile(filepath.Join(project, ".jev-check/input/states/example.json")); string(data) != `{"files":{"mine":"x"}}` {
		t.Errorf("add replaced a state: %s", data)
	}
	want, _ := os.ReadFile(filepath.Join(sourceDir, catalog.BundleDir, "input/questions/example.json"))
	if got, _ := os.ReadFile(filepath.Join(project, ".jev-check/input/questions/example.json")); string(got) != string(want) {
		t.Error("add did not copy the bundled questions")
	}
	os.Remove(filepath.Join(project, ".jev-check/input/questions/example.json"))
	wantCode(t, 2, "add", "example", "no-such-check")
	wantCode(t, 2, "add", "../x")
	wantCode(t, 2, "add")
	if fsutil.FileExists(filepath.Join(project, ".jev-check/input/questions/example.json")) {
		t.Error("a failed add wrote a file")
	}
	elsewhere := t.TempDir()
	wantCode(t, 0, "add", "--dir", elsewhere, "example")
	if !fsutil.FileExists(filepath.Join(elsewhere, ".jev-check/input/questions/example.json")) {
		t.Error("add --dir did not write into DIR")
	}
	wantCode(t, 2, "add", "--dir", filepath.Join(elsewhere, "missing"), "example")
	wantCode(t, 0, "add", "--help")

	// A broken local check is an error, never a fallback to the bundle.
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/public-release.json"), "{")
	wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	wantCode(t, 2, "list")
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/public-release.json"), `{"questions":{}}`)
	wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	wantCode(t, 2, "list")
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(project, ".jev-check/input/questions/public-release.json"), onlyQuestion)
		os.Chmod(filepath.Join(project, ".jev-check/input/questions/public-release.json"), 0)
		wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	}
	os.Remove(filepath.Join(project, ".jev-check/input/questions/public-release.json"))
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/bad name.json"), onlyQuestion)
	wantCode(t, 2, "list")
	os.Remove(filepath.Join(project, ".jev-check/input/questions/bad name.json"))

	// list DIR reads another project; list takes at most one DIR.
	other := t.TempDir()
	writeFile(t, filepath.Join(other, ".jev-check/input/questions/mine.json"), onlyQuestion)
	if out := wantCode(t, 0, "list", other); !strings.Contains(out, "mine [project, needs --file]\n") {
		t.Errorf("list DIR:\n%s", out)
	}
	if out := wantCode(t, 0, "list"); strings.Contains(out, "mine") {
		t.Errorf("list picked up another project:\n%s", out)
	}
	wantCode(t, 2, "list", project, other)
	wantCode(t, 2, "list", filepath.Join(other, "missing"))
	wantCode(t, 0, "list", "--help")

	// gate DIR uses DIR's own definition of a check.
	for _, q := range []string{"first", "second"} {
		repo := t.TempDir()
		gitInit(t, repo)
		writeFile(t, filepath.Join(repo, ".jev-check/input/questions/mine.json"), `{"questions":{"`+q+`":{"type":"noul"}}}`)
		writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/"],"checks":[{"check":"mine","threshold":0.5}]}`)
		writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
		gitRun(t, repo, "add", "a.txt")
		*requests = nil
		wantCode(t, 0, "gate", repo)
		if len(*requests) != 1 || (*requests)[0].Questions[q] == nil {
			t.Errorf("gate %s sent %v", q, *requests)
		}
	}
}

func TestProjectKeyAndOutput(t *testing.T) {
	setup(t)
	project, _ := os.Getwd()
	file := writeFile(t, filepath.Join(project, "x.txt"), "hello\n")

	out := wantCode(t, 0, "ask", "public-release", "--file", file)
	saved := strings.TrimSpace(out[strings.Index(out, "saved: ")+len("saved: "):])
	if !strings.HasPrefix(saved, filepath.Join(project, ".jev-check", "output")+"/") || !fsutil.FileExists(saved) {
		t.Errorf("saved path %q", saved)
	}

	// An explicit questions path elsewhere still uses the working directory's key and output.
	elsewhere := writeFile(t, filepath.Join(t.TempDir(), "q.json"), onlyQuestion)
	out = wantCode(t, 0, "ask", elsewhere, "--file", file)
	if !strings.Contains(out, "saved: "+filepath.Join(project, ".jev-check", "output")+"/") {
		t.Errorf("ask with explicit path:\n%s", out)
	}
}

// TestShippedChecksDryRun asks every shipped check with --dry-run, the way a user would,
// and requires a valid request with no call to the API.
func TestShippedChecksDryRun(t *testing.T) {
	requests := setup(t)
	names, err := filepath.Glob(filepath.Join(sourceDir, catalog.BundleDir, "input/questions/*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no shipped checks: %v", err)
	}
	input := writeFile(t, filepath.Join(t.TempDir(), "input.txt"), "hello\n")
	list := wantCode(t, 0, "list")
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		if !strings.Contains(list, name+" [project") {
			t.Errorf("list does not show %s:\n%s", name, list)
		}
		args := []string{"ask", "--dry-run", name}
		if !fsutil.FileExists(filepath.Join(sourceDir, catalog.BundleDir, "input/states", name+".json")) {
			args = append(args, "--file", input)
		}
		var req jev.Request
		if err := json.Unmarshal([]byte(wantCode(t, 0, args...)), &req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if req.Model == "" || len(req.Questions) == 0 || len(req.State) == 0 {
			t.Errorf("%s: request %+v", name, req)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("dry run sent %d requests", len(*requests))
	}
}
