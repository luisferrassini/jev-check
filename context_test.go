package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

func TestInit(t *testing.T) {
	setup(t)
	notGit := t.TempDir()
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	for _, dir := range []string{filepath.Join(notGit, "missing"), writeFile(t, filepath.Join(notGit, "file"), "x"), notGit, bare} {
		wantCode(t, 2, "init", dir)
	}
	if fsutil.FileExists(workspace.ConfigPath(notGit)) {
		t.Error("init wrote into a non-git folder")
	}

	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "sub dir")
	os.Mkdir(sub, 0o755)
	out := wantCode(t, 0, "init", sub)
	config := workspace.ConfigPath(sub)
	if !strings.Contains(out, config) || !strings.Contains(out, "jev-check gate '"+sub+"'") {
		t.Errorf("init output, want the config path and a quoted gate command:\n%s", out)
	}
	var p workspace.Project
	if err := fsutil.ReadJSON(config, &p); err != nil || len(p.Checks) != 1 || p.Checks[0].Check != "public-release" ||
		*p.Checks[0].Threshold != 0.5 || strings.Join(p.Exclude, ",") != ".jev-check/" || p.Checks[0].Skip[0] != "LICENSE" {
		t.Errorf("init config %+v, %v", p, err)
	}
	ignore := filepath.Join(sub, ".jev-check", ".gitignore")
	if data, _ := os.ReadFile(ignore); string(data) != ".env\noutput/\n" {
		t.Errorf("init .gitignore %q", data)
	}
	if state := wantCode(t, 0, "state", sub); strings.Contains(state, ".jev-check") {
		t.Errorf("state lists .jev-check/:\n%s", state)
	}
	var stdout, stderr strings.Builder
	if code := run([]string{"context", sub}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "run jev-check state") {
		t.Errorf("context: exit %d\n%s", code, stderr.String())
	}
	if out := wantCode(t, 0, "gate", sub); out != "nothing staged\n" {
		t.Errorf("gate after init: %s", out)
	}
	if fsutil.FileExists(filepath.Join(sub, ".jev-check", "output")) {
		t.Error("init, context, or an empty gate created output/")
	}

	// init copies the README and every bundled check, with its default state, as the checks available.
	checks := filepath.Join(sub, ".jev-check", "input", "questions")
	want, _ := os.ReadFile(filepath.Join(sourceDir, catalog.BundleDir, "input/questions/public-release.json"))
	if got, _ := os.ReadFile(filepath.Join(checks, "public-release.json")); string(got) != string(want) {
		t.Error("init did not copy public-release")
	}
	if entries, _ := os.ReadDir(checks); len(entries) != len(catalog.BundledNames()) {
		t.Errorf("init copied %v, want every bundled check %v", entries, catalog.BundledNames())
	}
	if !fsutil.FileExists(filepath.Join(sub, ".jev-check", "input", "states", "example.json")) {
		t.Error("init did not copy the example state")
	}
	if list := wantCode(t, 0, "list", sub); !strings.Contains(list, "public-release [project, gate 0.5, needs --file]") ||
		!strings.Contains(list, "no-leftovers [project, not in checks, needs --file]") {
		t.Errorf("list after init does not show which checks the gate runs:\n%s", list)
	}
	if !fsutil.FileExists(filepath.Join(sub, ".jev-check", "README.md")) {
		t.Error("init wrote no README.md")
	}

	// A second init keeps every file and restores only the missing ones, but a deleted check only when the config names it.
	before, _ := os.ReadFile(config)
	writeFile(t, ignore, "mine\n")
	os.Remove(filepath.Join(sub, ".jev-check", "README.md"))
	writeFile(t, filepath.Join(checks, "public-release.json"), onlyQuestion)
	out = wantCode(t, 0, "init", sub)
	if !strings.Contains(out, "kept    "+config) || !strings.Contains(out, "created "+filepath.Join(sub, ".jev-check", "README.md")) {
		t.Errorf("second init:\n%s", out)
	}
	if after, _ := os.ReadFile(config); string(after) != string(before) {
		t.Error("second init changed the config")
	}
	if after, _ := os.ReadFile(ignore); string(after) != "mine\n" {
		t.Error("second init changed the .gitignore")
	}
	if after, _ := os.ReadFile(filepath.Join(checks, "public-release.json")); string(after) != onlyQuestion {
		t.Error("second init replaced an edited check")
	}
	os.Remove(filepath.Join(checks, "no-leftovers.json"))
	wantCode(t, 0, "init", sub)
	if fsutil.FileExists(filepath.Join(checks, "no-leftovers.json")) {
		t.Error("init brought back a deleted check the config does not name")
	}
	writeFile(t, config, `{"checks":[{"check":"no-leftovers","threshold":0.5},{"check":"mine","threshold":0.5}]}`)
	wantCode(t, 0, "init", sub)
	if !fsutil.FileExists(filepath.Join(checks, "no-leftovers.json")) || fsutil.FileExists(filepath.Join(checks, "mine.json")) {
		t.Error("init did not restore exactly the bundled checks the config names")
	}
	writeFile(t, config, "{")
	wantCode(t, 2, "init", sub)
	writeFile(t, config, string(before))

	// The created .gitignore alone keeps the settings and output out of Git.
	fresh := t.TempDir()
	gitRun(t, fresh, "init", "-q")
	wantCode(t, 0, "init", fresh)
	writeSettings(t, fresh, fakeSettings)
	writeFile(t, filepath.Join(fresh, ".jev-check", "output", "a.json"), "{}")
	status, _ := exec.Command("git", "-C", fresh, "status", "--porcelain", "-uall").Output()
	wantStatus := "?? .jev-check/.gitignore\n?? .jev-check/README.md\n?? .jev-check/config.json\n"
	for _, kind := range []string{"questions", "states"} {
		files, _ := fs.Glob(catalog.Bundled, catalog.BundleDir+"/input/"+kind+"/*.json")
		for _, f := range files {
			wantStatus += "?? .jev-check/input/" + kind + "/" + path.Base(f) + "\n"
		}
	}
	if string(status) != wantStatus {
		t.Errorf("git status after init:\n%s\nwant:\n%s", status, wantStatus)
	}
	notDir := t.TempDir()
	gitInit(t, notDir)
	os.RemoveAll(filepath.Join(notDir, ".jev-check"))
	writeFile(t, filepath.Join(notDir, ".jev-check"), "x")
	wantCode(t, 2, "init", notDir)
	link := t.TempDir()
	gitInit(t, link)
	os.Symlink("elsewhere.json", workspace.ConfigPath(link))
	wantCode(t, 2, "init", link)

	// Concurrent runs create one complete config. A run that finds it half written may fail, never corrupt it.
	race := t.TempDir()
	gitInit(t, race)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			var o, e strings.Builder
			codes <- run([]string{"init", race}, &o, &e)
		})
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 0 {
			ok++
		}
	}
	if err := fsutil.ReadJSON(workspace.ConfigPath(race), &p); ok == 0 || err != nil || len(p.Checks) != 1 {
		t.Errorf("%d inits succeeded, config error %v", ok, err)
	}
}
func TestCodingStyleContext(t *testing.T) {
	requests := setup(t)
	repo := styleRepo(t, otherCheck)
	var state map[string]any
	json.Unmarshal([]byte(wantCode(t, 0, "state", repo)), &state)
	if _, ok := state["coding_styles"]; ok {
		t.Error("coding_styles without a reference")
	}
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "rules\n")
	styleConfig(t, repo, withStyle+`,{"check":"other","threshold":0.2,"coding_style":"./CODING_STYLE.md"}`)
	json.Unmarshal([]byte(wantCode(t, 0, "state", repo)), &state)
	if styles, _ := json.Marshal(state["coding_styles"]); string(styles) != `{"CODING_STYLE.md":"rules\n"}` {
		t.Errorf("coding_styles: %s", styles)
	}
	if len(*requests) != 0 {
		t.Error("context sent a request")
	}
}

// TestShippedConfigs runs the gate with no staged files on this repository's
// config.json and on the README's canonical example. Both must validate.
func TestShippedConfigs(t *testing.T) {
	requests := setup(t)
	real, err := os.ReadFile(filepath.Join(sourceDir, catalog.BundleDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]string{"config.json": string(real), "README": readmeConfig(t)} {
		repo := t.TempDir()
		gitInit(t, repo)
		writeFile(t, workspace.ConfigPath(repo), config)
		if out := wantCode(t, 0, "gate", repo); out != "nothing staged\n" {
			t.Errorf("%s: gate: %s", name, out)
		}
		var state struct {
			Project map[string]any `json:"project"`
		}
		if err := json.Unmarshal([]byte(wantCode(t, 0, "state", repo)), &state); err != nil ||
			state.Project["purpose"] == nil || state.Project["rules"] == nil || state.Project["folders"] == nil {
			t.Errorf("%s: context project %v, %v", name, state.Project, err)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("gate sent %d requests", len(*requests))
	}
}
