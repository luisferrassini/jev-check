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
	writeFile(t, filepath.Join(project, "input/questions/public-release.json"), onlyQuestion)
	writeFile(t, filepath.Join(project, "input/questions/example.json"), onlyQuestion)
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
	os.Remove(filepath.Join(project, "input/questions/example.json"))
	if out := wantCode(t, 0, "list"); !strings.Contains(out, "example [bundled, has default state]\n") {
		t.Errorf("list:\n%s", out)
	}
	dryRunQuestions(t, "example")

	// A broken local check is an error, never a fallback to the bundle.
	writeFile(t, filepath.Join(project, "input/questions/public-release.json"), "{")
	wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	wantCode(t, 2, "list")
	writeFile(t, filepath.Join(project, "input/questions/public-release.json"), `{"questions":{}}`)
	wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	wantCode(t, 2, "list")
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(project, "input/questions/public-release.json"), onlyQuestion)
		os.Chmod(filepath.Join(project, "input/questions/public-release.json"), 0)
		wantCode(t, 2, "ask", "--dry-run", "public-release", "--file", file)
	}
	os.Remove(filepath.Join(project, "input/questions/public-release.json"))
	writeFile(t, filepath.Join(project, "input/questions/bad name.json"), onlyQuestion)
	wantCode(t, 2, "list")
	os.Remove(filepath.Join(project, "input/questions/bad name.json"))

	// list DIR reads another project; list takes at most one DIR.
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "input/questions/mine.json"), onlyQuestion)
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
		gitRun(t, repo, "init", "-q")
		writeFile(t, filepath.Join(repo, "input/questions/mine.json"), `{"questions":{"`+q+`":{"type":"noul"}}}`)
		writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["input/"],"checks":[{"check":"mine","threshold":0.5}]}`)
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

	// The environment wins over the project's .env.
	writeFile(t, filepath.Join(project, ".env"), "TYPESAFE_API_KEY=wrong\n")
	wantCode(t, 0, "ask", "public-release", "--file", file)
	t.Setenv("TYPESAFE_API_KEY", "")
	wantCode(t, 2, "ask", "public-release", "--file", file)
	writeFile(t, filepath.Join(project, ".env"), "TYPESAFE_API_KEY=test\n")
	out := wantCode(t, 0, "ask", "public-release", "--file", file)
	saved := strings.TrimSpace(out[strings.Index(out, "saved: ")+len("saved: "):])
	if !strings.HasPrefix(saved, filepath.Join(project, "output")+"/") || !fileExists(saved) {
		t.Errorf("saved path %q", saved)
	}

	// An explicit questions path elsewhere still uses the working directory's key and output.
	elsewhere := writeFile(t, filepath.Join(t.TempDir(), "q.json"), onlyQuestion)
	out = wantCode(t, 0, "ask", elsewhere, "--file", file)
	if !strings.Contains(out, "saved: "+filepath.Join(project, "output")+"/") {
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
	if fileExists(filepath.Join(notGit, "project-context.json")) {
		t.Error("init wrote into a non-git folder")
	}

	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	sub := filepath.Join(repo, "sub dir")
	os.Mkdir(sub, 0o755)
	out := wantCode(t, 0, "init", sub)
	config := filepath.Join(sub, "project-context.json")
	if !strings.Contains(out, config) {
		t.Errorf("init output:\n%s", out)
	}
	var p project
	if err := readJSON(config, &p); err != nil || len(p.Checks) != 1 || p.Checks[0].Check != "public-release" ||
		*p.Checks[0].Threshold != 0.5 || strings.Join(p.Exclude, ",") != ".env,output/,fixtures/" || p.Checks[0].Skip[0] != "LICENSE" {
		t.Errorf("init config %+v, %v", p, err)
	}
	wantCode(t, 0, "context", sub)
	if out := wantCode(t, 0, "gate", sub); out != "nothing staged\n" {
		t.Errorf("gate after init: %s", out)
	}
	if fileExists(filepath.Join(sub, "output")) {
		t.Error("init, context, or an empty gate created output/")
	}

	before, _ := os.ReadFile(config)
	wantCode(t, 2, "init", sub)
	if after, _ := os.ReadFile(config); string(after) != string(before) {
		t.Error("second init changed the config")
	}
	link := t.TempDir()
	gitRun(t, link, "init", "-q")
	os.Symlink("elsewhere.json", filepath.Join(link, "project-context.json"))
	wantCode(t, 2, "init", link)

	// Concurrent runs create one complete config.
	race := t.TempDir()
	gitRun(t, race, "init", "-q")
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
	if err := readJSON(filepath.Join(race, "project-context.json"), &p); ok != 1 || err != nil {
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
	t.Setenv("TYPESAFE_API_KEY", "")
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
