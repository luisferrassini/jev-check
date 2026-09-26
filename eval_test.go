package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitPatch returns the patch git makes for staging content at path in a fresh repository.
func gitPatch(t *testing.T, path, content string) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, path), content)
	gitRun(t, repo, "add", "--", path)
	out, err := git(repo, "diff", "--cached", "--", path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const twoQuestions = `{"questions":{
  "q1":{"type":"noul","instructions":"One?"},
  "q2":{"type":"noul","instructions":"Two?"},
  "info":{"type":"choice","instructions":"Which?","choices":["x","y"]}}}`

// evalProject makes a git project with the check "two" and returns its fixture folder.
func evalProject(t *testing.T, config string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "input/questions/two.json"), twoQuestions)
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["fixtures/","input/"],"checks":[`+config+`]}`)
	return repo, filepath.Join(repo, "fixtures", "two")
}

func TestEvalCoverage(t *testing.T) {
	requests := setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	jevAnswers = `{"answers":{"q1":{"type":"noul","noul":0.9},"q2":{"type":"noul","noul":0.9},"info":{"type":"choice","choice":"x","probabilities":{"x":1}}}}`
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)

	// Missing sets are named together, before any request.
	code, _ := jev(t, "eval", "two", repo)
	var stderr strings.Builder
	run([]string{"eval", "two", repo}, &strings.Builder{}, &stderr)
	for _, want := range []string{"pass/", "fail/q1/", "fail/q2/"} {
		if code != 2 || !strings.Contains(stderr.String(), want) {
			t.Errorf("exit %d, missing %q in %s", code, want, stderr.String())
		}
	}
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "b.go", "package b\n"))
	stderr.Reset()
	if code := run([]string{"eval", "two", repo}, &strings.Builder{}, &stderr); code != 2 || !strings.Contains(stderr.String(), "fail/q2/") || strings.Contains(stderr.String(), "fail/q1/") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "c.go", "package c\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "notes.md"), "sidecar notes are ignored\n")

	// Fail folders must name a yes/no question, even when empty.
	for _, bad := range []string{"info", "unknown"} {
		os.MkdirAll(filepath.Join(fixtures, "fail", bad), 0o755)
		wantCode(t, 2, "eval", "two", repo)
		os.Remove(filepath.Join(fixtures, "fail", bad))
	}
	os.MkdirAll(filepath.Join(fixtures, "pass", "dir.patch"), 0o755)
	wantCode(t, 2, "eval", "two", repo)
	os.Remove(filepath.Join(fixtures, "pass", "dir.patch"))
	if len(*requests) != 0 || fileExists(filepath.Join(repo, "output")) {
		t.Fatalf("%d requests before the suite was complete", len(*requests))
	}

	// Equality passes: a pass fixture at the threshold passes, a fail fixture at it misses.
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["fixtures/","input/"],"checks":[{"check":"two","threshold":0.9}]}`)
	out := wantCode(t, 1, "eval", "two", repo)
	for _, want := range []string{
		"positive  negative  question\n1         1         q1\n1         1         q2\n",
		"MISS  0.9  q1  fail/q1/b.patch passes it\n", "MISS  0.9  q2  fail/q2/c.patch passes it\n",
		"eval: 2 misses in 3 fixtures\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pass/a.patch fails") {
		t.Errorf("a pass fixture at the threshold failed:\n%s", out)
	}

	// Only the named question counts on a fail fixture; per_question overrides apply.
	jevAnswers = ""
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["fixtures/","input/"],"checks":[{"check":"two","threshold":0.5,"per_question":{"q2":0.95}}]}`)
	os.RemoveAll(filepath.Join(fixtures, "fail"))
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "bad-q1.go", "package b\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "bad-q2.go", "package c\n"))
	out = wantCode(t, 1, "eval", "two", repo, "--no-cache")
	if !strings.Contains(out, "MISS  0.9  q2  pass/a.patch fails it\n") || !strings.HasSuffix(out, "eval: 1 misses in 3 fixtures\n") {
		t.Errorf("eval output:\n%s", out)
	}
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["fixtures/","input/"],"checks":[{"check":"two","threshold":0.5}]}`)
	wantCode(t, 0, "eval", "two", repo)

	// A malformed last fixture stops the run even when the others are cached.
	before := len(*requests)
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "z.patch"), "+++ b/z.go\n+package z\n")
	wantCode(t, 2, "eval", "two", repo)
	if len(*requests) != before {
		t.Error("a malformed fixture let requests through")
	}
	os.Remove(filepath.Join(fixtures, "fail", "q2", "z.patch"))

	// A check with no yes/no question cannot be evaluated.
	writeFile(t, filepath.Join(repo, "input/questions/two.json"), `{"questions":{"info":{"type":"choice"}}}`)
	wantCode(t, 2, "eval", "two", repo)
}

func TestPatchPaths(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "x.patch"), gitPatch(t, "bad-q1.go", "x\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "x.patch"), gitPatch(t, "bad-q2.go", "x\n"))

	// Real git patches for each supported kind, in a repository with one commit.
	src := t.TempDir()
	gitRun(t, src, "init", "-q")
	commit := func() { gitRun(t, src, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-qm", "x") }
	names := map[string]string{
		"modified": "dir/mod file.go", "deleted": "gone.go", "renamed": "old.go", "pure": "p old.txt",
		"quote": `q"b.txt`, "backslash": `back\slash.txt`, "tab": "t\tb.txt", "accent": "café.txt",
	}
	body := "one\ntwo\nthree\nfour\nfive\nsix\n"
	for _, name := range names {
		writeFile(t, filepath.Join(src, name), body)
	}
	writeFile(t, filepath.Join(src, "hunk.txt"), "keep\n-- a/evil\n")
	gitRun(t, src, "add", ".")
	commit()
	writeFile(t, filepath.Join(src, names["modified"]), body+"seven\n")
	os.Remove(filepath.Join(src, names["deleted"]))
	gitRun(t, src, "mv", names["renamed"], "new.go")
	writeFile(t, filepath.Join(src, "new.go"), body+"seven\n")
	gitRun(t, src, "mv", names["pure"], "p new.txt")
	writeFile(t, filepath.Join(src, "hunk.txt"), "keep\n++ b/evil\n")
	gitRun(t, src, "add", "-A")
	want := map[string][]string{
		"modified": {names["modified"]}, "deleted": {names["deleted"]}, "renamed": {names["renamed"], "new.go"},
		"pure": {names["pure"], "p new.txt"}, "hunk": {"hunk.txt"},
	}
	for kind, paths := range want {
		patch, err := git(src, append([]string{"diff", "--cached", "-M", "--"}, paths...)...)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(fixtures, "pass", kind+".patch"), patch)
	}
	for _, kind := range []string{"quote", "backslash", "tab", "accent"} {
		writeFile(t, filepath.Join(fixtures, "pass", kind+".patch"), gitPatch(t, names[kind], "x\n"))
	}
	// Git quotes non-ASCII paths by default; with core.quotePath=false it writes them as they are.
	raw := t.TempDir()
	gitRun(t, raw, "init", "-q")
	writeFile(t, filepath.Join(raw, "raw ü.txt"), "x\n")
	gitRun(t, raw, "add", ".")
	patch, err := git(raw, "-c", "core.quotePath=false", "diff", "--cached")
	if err != nil || strings.Contains(patch, `"`) {
		t.Fatalf("unquoted patch: %v\n%s", err, patch)
	}
	writeFile(t, filepath.Join(fixtures, "pass", "raw.patch"), patch)
	wantCode(t, 0, "eval", "two", repo)

	var got []string
	for _, req := range *requests {
		for file := range req.State["files"].(map[string]any) {
			got = append(got, strings.TrimSuffix(file, ".patch"))
		}
	}
	slices.Sort(got)
	expected := []string{"bad-q1.go", "bad-q2.go", "dir/mod file.go", "gone.go", "hunk.txt", "new.go", "p new.txt",
		`back\slash.txt`, "café.txt", `q"b.txt`, "raw ü.txt", "t\tb.txt"}
	slices.Sort(expected)
	if !slices.Equal(got, expected) {
		t.Errorf("request paths\n got %q\nwant %q", got, expected)
	}

	// Unsupported or unsafe patches stop the run without echoing their content.
	multi, _ := git(src, "diff", "--cached", "-M")
	bin := t.TempDir()
	gitRun(t, bin, "init", "-q")
	writeFile(t, filepath.Join(bin, "b.bin"), "\x00\x01secret-content\x00")
	gitRun(t, bin, "add", ".")
	binary, _ := git(bin, "diff", "--cached")
	for name, bad := range map[string]string{
		"multi":     multi,
		"escape":    "diff --git \"a/x\\q\" \"b/x\\q\"\n--- /dev/null\n+++ \"b/x\\q\"\n@@ -0,0 +1 @@\n+secret-content\n",
		"traversal": "diff --git a/../x b/../x\n--- /dev/null\n+++ b/../x\n@@ -0,0 +1 @@\n+secret-content\n",
		"absolute":  "diff --git a//etc/x b//etc/x\n--- /dev/null\n+++ b//etc/x\n@@ -0,0 +1 @@\n+secret-content\n",
		"binary":    binary,
		"lone":      "+++ b/x\n+secret-content\n",
		"conflict":  "diff --git a/x b/y\nrename from x\nrename to y\n--- a/x\n+++ b/z\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"devnull":   "diff --git a/x b/x\n--- /dev/null\n+++ /dev/null\n@@ -0,0 +1 @@\n+secret-content\n",
	} {
		path := writeFile(t, filepath.Join(fixtures, "pass", "zz.patch"), bad)
		var stdout, stderr strings.Builder
		if code := run([]string{"eval", "two", repo}, &stdout, &stderr); code != 2 || strings.Contains(stdout.String()+stderr.String(), "secret-content") {
			t.Errorf("%s: exit %d\n%s%s", name, code, stdout.String(), stderr.String())
		}
		os.Remove(path)
	}
}
