package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const onlyQuestion = `{"questions":{"only":{"type":"noul","instructions":"Is it fine?"}}}`

func dryRunQuestions(t *testing.T, args ...string) []string {
	t.Helper()
	var req request
	if err := json.Unmarshal([]byte(wantCode(t, 0, append([]string{"ask", "--dry-run"}, args...)...)), &req); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for id := range req.Questions {
		ids = append(ids, id)
	}
	return ids
}

func TestProjectChecks(t *testing.T) {
	requests := setup(t)
	project, _ := os.Getwd()
	file := writeFile(t, filepath.Join(project, "x.txt"), "hello\n")

	// A project-local check shadows the bundled one and does not inherit its state.
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/public-release.json"), onlyQuestion)
	writeFile(t, filepath.Join(project, ".jev-check/input/questions/example.json"), onlyQuestion)
	out := wantCode(t, 0, "list")
	if strings.Count(out, "public-release [") != 1 || !strings.Contains(out, "public-release [project, needs --file]\n") {
		t.Errorf("list:\n%s", out)
	}
	if ids := dryRunQuestions(t, "public-release", "--file", file); len(ids) != 1 || ids[0] != "only" {
		t.Errorf("local check not used: %v", ids)
	}
	wantCode(t, 2, "ask", "--dry-run", "example")
	state := writeFile(t, filepath.Join(project, "state.json"), `{"files":{"a":"b"}}`)
	dryRunQuestions(t, "example", state)

	// A bundled check keeps its embedded default state.
	os.Remove(filepath.Join(project, ".jev-check/input/questions/example.json"))
	if out := wantCode(t, 0, "list"); !strings.Contains(out, "example [bundled, has default state]\n") {
		t.Errorf("list:\n%s", out)
	}
	dryRunQuestions(t, "example")

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
		writeFile(t, configPath(repo), `{"exclude":[".jev-check/input/"],"checks":[{"check":"mine","threshold":0.5}]}`)
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
	if !strings.HasPrefix(saved, filepath.Join(project, ".jev-check", "output")+"/") || !fileExists(saved) {
		t.Errorf("saved path %q", saved)
	}

	// An explicit questions path elsewhere still uses the working directory's key and output.
	elsewhere := writeFile(t, filepath.Join(t.TempDir(), "q.json"), onlyQuestion)
	out = wantCode(t, 0, "ask", elsewhere, "--file", file)
	if !strings.Contains(out, "saved: "+filepath.Join(project, ".jev-check", "output")+"/") {
		t.Errorf("ask with explicit path:\n%s", out)
	}
}

func TestInit(t *testing.T) {
	setup(t)
	notGit := t.TempDir()
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	for _, dir := range []string{filepath.Join(notGit, "missing"), writeFile(t, filepath.Join(notGit, "file"), "x"), notGit, bare} {
		wantCode(t, 2, "init", dir)
	}
	if fileExists(configPath(notGit)) {
		t.Error("init wrote into a non-git folder")
	}

	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "sub dir")
	os.Mkdir(sub, 0o755)
	out := wantCode(t, 0, "init", sub)
	config := configPath(sub)
	if !strings.Contains(out, config) || !strings.Contains(out, "jev-check gate '"+sub+"'") {
		t.Errorf("init output, want the config path and a quoted gate command:\n%s", out)
	}
	var p project
	if err := readJSON(config, &p); err != nil || len(p.Checks) != 1 || p.Checks[0].Check != "public-release" ||
		*p.Checks[0].Threshold != 0.5 || strings.Join(p.Exclude, ",") != ".jev-check/" || p.Checks[0].Skip[0] != "LICENSE" {
		t.Errorf("init config %+v, %v", p, err)
	}
	ignore := filepath.Join(sub, ".jev-check", ".gitignore")
	if data, _ := os.ReadFile(ignore); string(data) != ".env\noutput/\n" {
		t.Errorf("init .gitignore %q", data)
	}
	if context := wantCode(t, 0, "context", sub); strings.Contains(context, ".jev-check") {
		t.Errorf("context lists .jev-check/:\n%s", context)
	}
	if out := wantCode(t, 0, "gate", sub); out != "nothing staged\n" {
		t.Errorf("gate after init: %s", out)
	}
	if fileExists(filepath.Join(sub, ".jev-check", "output")) {
		t.Error("init, context, or an empty gate created output/")
	}

	before, _ := os.ReadFile(config)
	writeFile(t, ignore, "mine\n")
	wantCode(t, 2, "init", sub)
	if after, _ := os.ReadFile(config); string(after) != string(before) {
		t.Error("second init changed the config")
	}
	if after, _ := os.ReadFile(ignore); string(after) != "mine\n" {
		t.Error("second init changed the .gitignore")
	}

	// The created .gitignore alone keeps the settings and output out of Git.
	fresh := t.TempDir()
	gitRun(t, fresh, "init", "-q")
	wantCode(t, 0, "init", fresh)
	writeSettings(t, fresh, fakeSettings)
	writeFile(t, filepath.Join(fresh, ".jev-check", "output", "a.json"), "{}")
	if out, _ := exec.Command("git", "-C", fresh, "status", "--porcelain", "-uall").Output(); string(out) != "?? .jev-check/.gitignore\n?? .jev-check/project-context.json\n" {
		t.Errorf("git status after init:\n%s", out)
	}
	notDir := t.TempDir()
	gitInit(t, notDir)
	os.RemoveAll(filepath.Join(notDir, ".jev-check"))
	writeFile(t, filepath.Join(notDir, ".jev-check"), "x")
	wantCode(t, 2, "init", notDir)
	link := t.TempDir()
	gitInit(t, link)
	os.Symlink("elsewhere.json", configPath(link))
	wantCode(t, 2, "init", link)

	// Concurrent runs create one complete config.
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
	if err := readJSON(configPath(race), &p); ok != 1 || err != nil {
		t.Errorf("%d inits succeeded, config error %v", ok, err)
	}
}

// TestInstalledBinary proves what run cannot: a moved binary still has its bundled checks
// and writes nothing beside itself.
func TestInstalledBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "jev-check"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	os.Chmod(bin, 0o555)
	t.Cleanup(func() { os.Chmod(bin, 0o755) })
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fresh := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"list"}, {"ask", "example", "--dry-run"}} {
		cmd := exec.Command("jev-check", args...)
		cmd.Dir = fresh
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("jev-check %v: %v\n%s", args, err, out)
		}
	}
	for _, dir := range []string{bin, fresh} {
		if entries, _ := os.ReadDir(dir); len(entries) != map[string]int{bin: 1, fresh: 0}[dir] {
			t.Errorf("%s holds %v", dir, entries)
		}
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
	for _, args := range [][]string{{"gate", repo}, {"eval", "public-release", repo}, {"context", repo}} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), old) ||
			!strings.Contains(stderr.String(), configPath(repo)) || !strings.Contains(stderr.String(), "git mv project-context.json .jev-check/") {
			t.Errorf("%v: exit %d\n%s", args, code, stderr.String())
		}
	}
	if len(*requests) != 0 || fileExists(filepath.Join(repo, "output")) || fileExists(filepath.Join(repo, ".jev-check", "output")) {
		t.Errorf("old layout sent %d requests or wrote output", len(*requests))
	}

	// A root input/ override is ignored, so the bundled check is used.
	writeFile(t, "input/questions/public-release.json", onlyQuestion)
	if ids := dryRunQuestions(t, "public-release", "--file", old); len(ids) == 1 && ids[0] == "only" {
		t.Error("ask read the root input/")
	}
	if out := wantCode(t, 0, "list"); !strings.Contains(out, "public-release [bundled") {
		t.Errorf("list:\n%s", out)
	}
}
