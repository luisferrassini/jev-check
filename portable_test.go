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
	want, _ := os.ReadFile(filepath.Join(sourceDir, bundleDir, "input/questions/example.json"))
	if got, _ := os.ReadFile(filepath.Join(project, ".jev-check/input/questions/example.json")); string(got) != string(want) {
		t.Error("add did not copy the bundled questions")
	}
	os.Remove(filepath.Join(project, ".jev-check/input/questions/example.json"))
	wantCode(t, 2, "add", "example", "no-such-check")
	wantCode(t, 2, "add", "../x")
	wantCode(t, 2, "add")
	if fileExists(filepath.Join(project, ".jev-check/input/questions/example.json")) {
		t.Error("a failed add wrote a file")
	}
	elsewhere := t.TempDir()
	wantCode(t, 0, "add", "--dir", elsewhere, "example")
	if !fileExists(filepath.Join(elsewhere, ".jev-check/input/questions/example.json")) {
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

	// init copies the README and every bundled check, with its default state, as the checks available.
	checks := filepath.Join(sub, ".jev-check", "input", "questions")
	want, _ := os.ReadFile(filepath.Join(sourceDir, bundleDir, "input/questions/public-release.json"))
	if got, _ := os.ReadFile(filepath.Join(checks, "public-release.json")); string(got) != string(want) {
		t.Error("init did not copy public-release")
	}
	if entries, _ := os.ReadDir(checks); len(entries) != len(bundledNames()) {
		t.Errorf("init copied %v, want every bundled check %v", entries, bundledNames())
	}
	if !fileExists(filepath.Join(sub, ".jev-check", "input", "states", "example.json")) {
		t.Error("init did not copy the example state")
	}
	if list := wantCode(t, 0, "list", sub); !strings.Contains(list, "public-release [project, gate 0.5, needs --file]") ||
		!strings.Contains(list, "no-leftovers [project, not in checks, needs --file]") {
		t.Errorf("list after init does not show which checks the gate runs:\n%s", list)
	}
	if !fileExists(filepath.Join(sub, ".jev-check", "README.md")) {
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
	if fileExists(filepath.Join(checks, "no-leftovers.json")) {
		t.Error("init brought back a deleted check the config does not name")
	}
	writeFile(t, config, `{"checks":[{"check":"no-leftovers","threshold":0.5},{"check":"mine","threshold":0.5}]}`)
	wantCode(t, 0, "init", sub)
	if !fileExists(filepath.Join(checks, "no-leftovers.json")) || fileExists(filepath.Join(checks, "mine.json")) {
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
	wantStatus := "?? .jev-check/.gitignore\n?? .jev-check/README.md\n"
	for _, kind := range []string{"questions", "states"} {
		files, _ := fs.Glob(bundled, bundleDir+"/input/"+kind+"/*.json")
		for _, f := range files {
			wantStatus += "?? .jev-check/input/" + kind + "/" + path.Base(f) + "\n"
		}
	}
	if wantStatus += "?? .jev-check/project-context.json\n"; string(status) != wantStatus {
		t.Errorf("git status after init:\n%s\nwant:\n%s", status, wantStatus)
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
	if err := readJSON(configPath(race), &p); ok == 0 || err != nil || len(p.Checks) != 1 {
		t.Errorf("%d inits succeeded, config error %v", ok, err)
	}
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
	if code, out := runBin(fresh, "jev-check", "ask", "example"); code != 2 || !strings.Contains(out, "set TYPESAFE_API_KEY in "+filepath.Join(fresh, settingsFile)) {
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
	if code, out := initFull(); code != 2 || !strings.Contains(out, "writing ") || fileExists(filepath.Join(repo, ".jev-check")) {
		t.Errorf("init with a full disk: exit %d, .jev-check left: %v\n%s", code, fileExists(filepath.Join(repo, ".jev-check")), out)
	}
	writeSettings(t, repo, fakeSettings)
	if code, out := initFull(); code != 2 || !strings.Contains(out, "writing ") || fileExists(configPath(repo)) || !fileExists(filepath.Join(repo, settingsFile)) {
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
