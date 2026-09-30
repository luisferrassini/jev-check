package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

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

// TestInstalledBinary proves what run cannot: a moved binary can still copy its bundled checks,
// reads no settings and writes nothing beside itself, and a symlink to it changes no project path.
func TestInstalledBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "jev-check"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	requests := setup(t)
	writeFile(t, filepath.Join(bin, ".env"), fakeSettings)
	writeSettings(t, bin, fakeSettings)
	os.Chmod(bin, 0o555)
	t.Cleanup(func() { os.Chmod(bin, 0o755) })
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runBin := func(dir, name string, args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if cmd.ProcessState == nil {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		return cmd.ProcessState.ExitCode(), string(out)
	}
	fresh := t.TempDir()
	if code, out := runBin(fresh, "jev-check", "ask", "example", "--dry-run"); code != 2 || !strings.Contains(out, "jev-check add example") {
		t.Errorf("ask ran a check the project does not have: exit %d\n%s", code, out)
	}
	for _, args := range [][]string{{"--help"}, {"list"}, {"add", "example"}, {"ask", "example", "--dry-run"}} {
		if code, out := runBin(fresh, "jev-check", args...); code != 0 {
			t.Errorf("jev-check %v: exit %d\n%s", args, code, out)
		}
	}
	if code, out := runBin(fresh, "jev-check", "ask", "example"); code != 2 || !strings.Contains(out, "set TYPESAFE_API_KEY in "+filepath.Join(fresh, jev.SettingsFile)) {
		t.Errorf("settings beside the binary were read: exit %d\n%s", code, out)
	}
	if len(*requests) != 0 {
		t.Errorf("sent %d requests without project settings", len(*requests))
	}

	// Through PATH or a symlink, a real request uses the project's settings and output.
	project := t.TempDir()
	writeSettings(t, project, fakeSettings)
	addBundled(t, project)
	links := t.TempDir()
	os.Symlink(filepath.Join(bin, "jev-check"), filepath.Join(links, "jev"))
	for _, name := range []string{"jev-check", filepath.Join(links, "jev")} {
		code, out := runBin(project, name, "ask", "example")
		if code != 0 || !strings.Contains(out, "saved: "+filepath.Join(project, ".jev-check", "output")+"/") {
			t.Errorf("%s ask example: exit %d\n%s", name, code, out)
		}
	}
	if len(*requests) != 2 {
		t.Errorf("sent %d requests, want 2", len(*requests))
	}
	for dir, want := range map[string]int{bin: 3, fresh: 1, links: 1} {
		if entries, _ := os.ReadDir(dir); len(entries) != want {
			t.Errorf("%s holds %v", dir, entries)
		}
	}

	// A failed write leaves no partial config, and removes only a folder this run made.
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	initFull := func() (int, string) {
		t.Helper()
		return runBin(repo, "sh", "-c", `ulimit -f 0 && exec jev-check init .`)
	}
	if code, out := initFull(); code != 2 || !strings.Contains(out, "writing ") || fsutil.FileExists(filepath.Join(repo, ".jev-check")) {
		t.Errorf("init with a full disk: exit %d, .jev-check left: %v\n%s", code, fsutil.FileExists(filepath.Join(repo, ".jev-check")), out)
	}
	writeSettings(t, repo, fakeSettings)
	if code, out := initFull(); code != 2 || !strings.Contains(out, "writing ") || fsutil.FileExists(workspace.ConfigPath(repo)) || !fsutil.FileExists(filepath.Join(repo, jev.SettingsFile)) {
		t.Errorf("init with a full disk: exit %d\n%s", code, out)
	}
}

// TestOldLayout proves the root files from before .jev-check/ are never read.
func TestOldLayout(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	old := writeFile(t, filepath.Join(repo, "project-context.json"), `{"checks":[{"check":"public-release","threshold":0.5}]}`)
	writeFile(t, filepath.Join(repo, "fixtures/public-release/pass/x.patch"), gitPatch(t, "x", "hello\n"))
	writeFile(t, filepath.Join(repo, "x"), "hello\n")
	gitRun(t, repo, "add", "x")
	for _, args := range [][]string{{"gate", repo}, {"eval", "public-release", repo}, {"state", repo}} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), old) ||
			!strings.Contains(stderr.String(), workspace.ConfigPath(repo)) || !strings.Contains(stderr.String(), "git mv project-context.json .jev-check/config.json") {
			t.Errorf("%v: exit %d\n%s", args, code, stderr.String())
		}
	}
	if len(*requests) != 0 || fsutil.FileExists(filepath.Join(repo, "output")) || fsutil.FileExists(filepath.Join(repo, ".jev-check", "output")) {
		t.Errorf("old layout sent %d requests or wrote output", len(*requests))
	}

	// The old name inside .jev-check/ is never read either, and wins over a root config.
	renamed := writeFile(t, filepath.Join(repo, ".jev-check", "project-context.json"), `{"checks":[{"check":"public-release","threshold":0.5}]}`)
	var stdout, stderr strings.Builder
	if code := run([]string{"gate", repo}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), renamed) ||
		!strings.Contains(stderr.String(), "git mv .jev-check/project-context.json .jev-check/config.json") || len(*requests) != 0 {
		t.Errorf("gate: exit %d\n%s", code, stderr.String())
	}
	os.Remove(renamed)

	// A root input/ is ignored, so the check in .jev-check/input/ is used.
	writeFile(t, "input/questions/public-release.json", onlyQuestion)
	if ids := dryRunQuestions(t, "public-release", "--file", old); len(ids) == 1 && ids[0] == "only" {
		t.Error("ask read the root input/")
	}
	os.Remove(filepath.Join(".jev-check", "input", "questions", "public-release.json"))
	wantErr(t, "jev-check add public-release", "ask", "--dry-run", "public-release", "--file", old)
	if out := wantCode(t, 0, "list"); !strings.Contains(out, "public-release [bundled, not added") {
		t.Errorf("list:\n%s", out)
	}
}
