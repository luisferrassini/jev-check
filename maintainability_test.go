package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

var maintainabilityIDs = []string{"no_mixed_output_channels", "no_redundant_forwarding"}

func maintainabilityAnswers(forwarding, output string) string {
	return `{"answers":{"no_redundant_forwarding":` + forwarding + `,"no_mixed_output_channels":` + output + `}}`
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

// TestMaintainabilityCorpus runs eval on a disposable copy of the canonical corpus,
// as the README's setup does.
func TestMaintainabilityCorpus(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/"],"checks":[{"check":"maintainability","threshold":0.5}]}`)
	corpus := filepath.Join(sourceDir, catalog.BundleDir, "fixtures", "maintainability")
	if err := os.CopyFS(filepath.Join(repo, ".jev-check", "fixtures", "maintainability"), os.DirFS(corpus)); err != nil {
		t.Fatal(err)
	}
	pass, _ := filepath.Glob(filepath.Join(corpus, "pass", "*.patch"))
	for _, id := range maintainabilityIDs {
		if fail, _ := filepath.Glob(filepath.Join(corpus, "fail", id, "*.patch")); len(fail) < 2 {
			t.Errorf("fail/%s has %d fixtures, want at least 2", id, len(fail))
		}
	}
	if len(pass) < 14 {
		t.Errorf("pass has %d fixtures, want the 14 listed cases", len(pass))
	}

	// The fake answers 0.9 everywhere, so every fail fixture is a miss; what matters here is what was sent.
	wantCode(t, 1, "eval", "maintainability", repo)
	for _, req := range *requests {
		for file := range req.State["files"].(map[string]any) {
			if strings.Contains(file, "pass") || strings.Contains(file, "fail") || strings.Contains(file, "fixtures") {
				t.Errorf("request path %q reveals the fixture label", file)
			}
		}
	}
}
