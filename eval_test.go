package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/luisferrassini/jev-check/internal/catalog"
	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/gitcmd"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

func TestEvalCoverage(t *testing.T) {
	requests := setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	jevAnswers = `{"answers":{"q1":{"type":"noul","noul":0.9},"q2":{"type":"noul","noul":0.9},"info":{"type":"choice","choice":"x","probabilities":{"x":1}}}}`
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)

	// Missing sets are named together, before any request.
	code, _ := runCmd(t, "eval", "two", repo)
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
	if len(*requests) != 0 || fsutil.FileExists(filepath.Join(repo, ".jev-check", "output")) {
		t.Fatalf("%d requests before the suite was complete", len(*requests))
	}

	// Equality passes: a pass fixture at the threshold passes, a fail fixture at it misses.
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/",".jev-check/input/"],"checks":[{"check":"two","threshold":0.9}]}`)
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
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/",".jev-check/input/"],"checks":[{"check":"two","threshold":0.5,"per_question":{"q2":0.95}}]}`)
	os.RemoveAll(filepath.Join(fixtures, "fail"))
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "bad-q1.go", "package b\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "bad-q2.go", "package c\n"))
	out = wantCode(t, 1, "eval", "two", repo, "--no-cache")
	if !strings.Contains(out, "MISS  0.9  q2  pass/a.patch fails it\n") || !strings.HasSuffix(out, "eval: 1 misses in 3 fixtures\n") {
		t.Errorf("eval output:\n%s", out)
	}
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/",".jev-check/input/"],"checks":[{"check":"two","threshold":0.5}]}`)
	wantCode(t, 0, "eval", "two", repo)

	// A malformed last fixture stops the run even when the others are cached.
	// The cache folder is swapped for a file, so any cache lookup would miss and send a request.
	before := len(*requests)
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "z.patch"), "+++ b/z.go\n+package z\n")
	cache := filepath.Join(repo, ".jev-check", "output", "cache", "v2")
	if err := os.Rename(cache, cache+".saved"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cache, "not a folder\n")
	var stdout strings.Builder
	stderr.Reset()
	if code := run([]string{"eval", "two", repo}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "z.patch") {
		t.Errorf("exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if len(*requests) != before {
		t.Error("a malformed fixture let requests through")
	}
	os.Remove(cache)
	os.Rename(cache+".saved", cache)
	os.Remove(filepath.Join(fixtures, "fail", "q2", "z.patch"))

	// A check with no yes/no question cannot be evaluated.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/two.json"), `{"questions":{"info":{"type":"choice"}}}`)
	wantCode(t, 2, "eval", "two", repo)
}

func TestPatchPaths(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "x.patch"), gitPatch(t, "bad-q1.go", "x\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "x.patch"), gitPatch(t, "bad-q2.go", "x\n"))

	// Real git patches for each supported kind, in a repository with one commit.
	src := t.TempDir()
	gitInit(t, src)
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
		patch, err := gitcmd.Run(src, append([]string{"diff", "--cached", "-M", "--"}, paths...)...)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(fixtures, "pass", kind+".patch"), patch)
	}
	for _, kind := range []string{"quote", "backslash", "tab", "accent"} {
		writeFile(t, filepath.Join(fixtures, "pass", kind+".patch"), gitPatch(t, names[kind], "x\n"))
	}
	// Carriage returns in hunk content are file data, not header line endings.
	writeFile(t, filepath.Join(fixtures, "pass", "crlf.patch"), gitPatch(t, "crlf.txt", "a\r\nb\r\n"))
	// Git quotes non-ASCII paths by default; with core.quotePath=false it writes them as they are.
	raw := t.TempDir()
	gitInit(t, raw)
	writeFile(t, filepath.Join(raw, "raw ü.txt"), "x\n")
	gitRun(t, raw, "add", ".")
	patch, err := gitcmd.Run(raw, "-c", "core.quotePath=false", "diff", "--cached")
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
	expected := []string{"bad-q1.go", "bad-q2.go", "crlf.txt", "dir/mod file.go", "gone.go", "hunk.txt", "new.go", "p new.txt",
		`back\slash.txt`, "café.txt", `q"b.txt`, "raw ü.txt", "t\tb.txt"}
	slices.Sort(expected)
	if !slices.Equal(got, expected) {
		t.Errorf("request paths\n got %q\nwant %q", got, expected)
	}

	// Unsupported or unsafe patches stop the run without echoing their content.
	multi, _ := gitcmd.Run(src, "diff", "--cached", "-M")
	bin := t.TempDir()
	gitInit(t, bin)
	writeFile(t, filepath.Join(bin, "b.bin"), "\x00\x01secret-content\x00")
	gitRun(t, bin, "add", ".")
	binary, _ := gitcmd.Run(bin, "diff", "--cached")
	for name, bad := range map[string]string{
		"multi":     multi,
		"escape":    "diff --git \"a/x\\q\" \"b/x\\q\"\n--- /dev/null\n+++ \"b/x\\q\"\n@@ -0,0 +1 @@\n+secret-content\n",
		"traversal": "diff --git a/../x b/../x\n--- /dev/null\n+++ b/../x\n@@ -0,0 +1 @@\n+secret-content\n",
		"absolute":  "diff --git a//etc/x b//etc/x\n--- /dev/null\n+++ b//etc/x\n@@ -0,0 +1 @@\n+secret-content\n",
		"binary":    binary,
		"lone":      "+++ b/x\n+secret-content\n",
		"conflict":  "diff --git a/x b/y\nrename from x\nrename to y\n--- a/x\n+++ b/z\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"devnull":   "diff --git a/x b/x\n--- /dev/null\n+++ /dev/null\n@@ -0,0 +1 @@\n+secret-content\n",
		// Path headers must agree, and every one is checked, not only the one sent.
		"differing":        "diff --git a/x b/y\n--- a/x\n+++ b/y\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"rename from":      "diff --git a/x b/y\nrename from zzz\nrename to y\n--- a/x\n+++ b/y\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"rename traversal": "diff --git a/../x b/y\nsimilarity index 100%\nrename from ../x\nrename to y\n",
		"old traversal":    "diff --git a/../x b/x\n--- a/../x\n+++ b/x\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"old prefix":       "diff --git a/x b/x\n--- x\n+++ b/x\n@@ -1 +1 @@\n-a\n+secret-content\n",
		// Git writes only C escapes and three-digit octal, not Go's \x, \u, or \U.
		"hex escape":     "diff --git \"a/x\\x41\" \"b/x\\x41\"\n--- /dev/null\n+++ \"b/x\\x41\"\n@@ -0,0 +1 @@\n+secret-content\n",
		"unicode escape": "diff --git \"a/x\\u00e9\" \"b/x\\u00e9\"\n--- /dev/null\n+++ \"b/x\\u00e9\"\n@@ -0,0 +1 @@\n+secret-content\n",
		"long escape":    "diff --git \"a/x\\U0001F600\" \"b/x\\U0001F600\"\n--- /dev/null\n+++ \"b/x\\U0001F600\"\n@@ -0,0 +1 @@\n+secret-content\n",
		// A second ---/+++ pair after the first hunk is a second file without its diff --git line.
		"second file": "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-a\n+secret-content\n",
		"crlf":        "diff --git a/x b/x\r\n--- /dev/null\r\n+++ b/x\r\n@@ -0,0 +1 @@\r\n+secret-content\r\n",
	} {
		path := writeFile(t, filepath.Join(fixtures, "pass", "zz.patch"), bad)
		var stdout, stderr strings.Builder
		if code := run([]string{"eval", "two", repo}, &stdout, &stderr); code != 2 || strings.Contains(stdout.String()+stderr.String(), "secret-content") {
			t.Errorf("%s: exit %d\n%s%s", name, code, stdout.String(), stderr.String())
		}
		os.Remove(path)
	}
}

// TestEvalGateParity checks that eval sends a staged file's fixture with the same state the gate sends.
func TestEvalGateParity(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(repo, "dir", "a.go"), "package a\n")
	gitRun(t, repo, "add", "dir/a.go")
	wantCode(t, 0, "gate", repo)
	if len(*requests) != 1 {
		t.Fatalf("gate sent %d requests, want 1", len(*requests))
	}
	gateReq := (*requests)[0]

	patch, err := gitcmd.Run(repo, "diff", "--cached", "--relative", "--", "dir/a.go")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), patch)
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "bad-q1.go", "x\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "bad-q2.go", "x\n"))
	wantCode(t, 0, "eval", "two", repo, "--no-cache")
	i := slices.IndexFunc((*requests)[1:], func(r jev.Request) bool { return r.State["files"].(map[string]any)["dir/a.go.patch"] != nil })
	if i < 0 {
		t.Fatal("eval sent no request for dir/a.go")
	}
	if evalReq := (*requests)[1+i]; !reflect.DeepEqual(evalReq, gateReq) {
		t.Errorf("eval request differs from the gate's\n eval %+v\n gate %+v", evalReq, gateReq)
	}
}

// TestEvalThresholdEnds checks thresholds 0 and 1: an answer equal to the threshold passes.
func TestEvalThresholdEnds(t *testing.T) {
	setup(t)
	t.Cleanup(func() { jevAnswers = "" })
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "b.go", "package b\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "c.go", "package c\n"))
	answers := func(v string) string {
		return `{"answers":{"q1":{"type":"noul","noul":` + v + `},"q2":{"type":"noul","noul":` + v + `},"info":{"type":"choice","choice":"x","probabilities":{"x":1}}}}`
	}
	for _, c := range []struct{ threshold, answer, want string }{
		{"0", "0", "eval: 2 misses in 3 fixtures\n"},
		{"1", "1", "eval: 2 misses in 3 fixtures\n"},
		{"1", "0.999", "eval: 2 misses in 3 fixtures\n"},
	} {
		jevAnswers = answers(c.answer)
		writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/",".jev-check/input/"],"checks":[{"check":"two","threshold":`+c.threshold+`}]}`)
		out := wantCode(t, 1, "eval", "two", repo, "--no-cache")
		passFails := strings.Contains(out, "pass/a.patch fails")
		if !strings.HasSuffix(out, c.want) || passFails != (c.answer == "0.999") {
			t.Errorf("threshold %s, answer %s:\n%s", c.threshold, c.answer, out)
		}
	}
}

// TestEvalStyleSecret checks that a secret in the coding_style document is named as such.
func TestEvalStyleSecret(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5,"coding_style":"STYLE.md"}`)
	writeFile(t, filepath.Join(repo, "STYLE.md"), "# Style\nexample = "+awsKey+"\n")
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q1", "b.patch"), gitPatch(t, "b.go", "package b\n"))
	writeFile(t, filepath.Join(fixtures, "fail", "q2", "c.patch"), gitPatch(t, "c.go", "package c\n"))
	out := wantBlocked(t, requests, awsKey, "eval", "two", repo)
	if !strings.Contains(out, "eval: BLOCKED, the coding_style document looks like it holds a secret") {
		t.Errorf("eval output:\n%s", out)
	}
}
func TestEval(t *testing.T) {
	setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	config := func(threshold string) {
		writeFile(t, workspace.ConfigPath(repo), `{ "exclude": [".jev-check/fixtures/"], "checks": [{ "check": "public-release", "threshold": `+threshold+` }] }`)
	}
	config("0.2")
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	wantCode(t, 2, "eval", "public-release", repo) // no fixtures yet

	writeFile(t, filepath.Join(fixtures, "pass", "a.go.patch"), gitPatch(t, "a.go", "package a\n"))
	// The fake answers 0.1 for a question when the path starts with bad-<question>.
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.go.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	if out := wantCode(t, 0, "eval", "public-release", repo); !strings.HasSuffix(out, "eval: 0 misses in 7 fixtures\n") {
		t.Errorf("eval output:\n%s", out)
	}

	// The fake answers english_only=0.3, so a pass fixture misses at a threshold of 0.5.
	config("0.5")
	if out := wantCode(t, 1, "eval", "public-release", repo); !strings.Contains(out, "MISS  0.3  english_only  pass/a.go.patch fails it\n") {
		t.Errorf("eval output:\n%s", out)
	}

	wantCode(t, 2, "eval", "no-such-check", repo)
	writeFile(t, filepath.Join(fixtures, "fail", "typo", "c.patch"), gitPatch(t, "c", "x\n"))
	wantCode(t, 2, "eval", "public-release", repo)
	os.RemoveAll(filepath.Join(fixtures, "fail", "typo"))
	writeFile(t, filepath.Join(fixtures, "pass", "no-header.patch"), "+x\n")
	wantCode(t, 2, "eval", "public-release", repo)
}
func TestEvalPreflight(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/"],"checks":[{"check":"public-release","threshold":0.2}]}`)
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.go.patch"), gitPatch(t, "a.go", "package a\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.go.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	wantCode(t, 0, "eval", "public-release", repo) // fills the cache
	saved := outputFiles(t, repo)

	writeFile(t, filepath.Join(fixtures, "fail", "english_only", "z.go.patch"), gitPatch(t, "z.go", "key = \""+awsKey+"\"\n"))
	for _, args := range [][]string{{"eval", "public-release", repo}, {"eval", "public-release", repo, "--no-cache"}} {
		if out := wantBlocked(t, requests, awsKey, args...); strings.Contains(out, "misses") || !strings.Contains(out, "fail/english_only/z.go.patch") {
			t.Errorf("eval output after a block:\n%s", out)
		}
	}
	if files := outputFiles(t, repo); !slices.Equal(files, saved) {
		t.Errorf("a blocked eval changed .jev-check/output/ from %v to %v", saved, files)
	}
}
func TestEvalCache(t *testing.T) {
	requests := setup(t)
	repo, fixtures := evalProject(t, `{"check":"two","threshold":0.5}`)
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a", "x\n"))
	for _, q := range []string{"q1", "q2"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q, "x\n"))
	}
	count := func(args ...string) int {
		t.Helper()
		before := len(*requests)
		wantCode(t, 0, append([]string{"eval", "two", repo}, args...)...)
		return len(*requests) - before
	}
	if n := count(); n != 3 {
		t.Errorf("first eval sent %d, want 3", n)
	}
	if n := count(); n != 0 {
		t.Errorf("second eval sent %d, want 0", n)
	}
	if n := count("--no-cache"); n != 3 {
		t.Errorf("--no-cache sent %d, want 3", n)
	}
	if n := count("--model", "other"); n != 3 {
		t.Errorf("another model sent %d, want 3", n)
	}
}

// TestOptInCorpora runs eval offline on a disposable copy of each opt-in corpus,
// as the README's setup does, and checks what the corpus and the requests hold.
func TestOptInCorpora(t *testing.T) {
	for _, opt := range optInChecks {
		name := opt.name
		t.Run(name, func(t *testing.T) {
			requests := setup(t)
			var check struct {
				Questions map[string]json.RawMessage `json:"questions"`
			}
			if err := fsutil.ReadJSON(filepath.Join(sourceDir, catalog.BundleDir, "input", "questions", name+".json"), &check); err != nil {
				t.Fatal(err)
			}
			corpus := filepath.Join(sourceDir, catalog.BundleDir, "fixtures", name)
			for id, q := range check.Questions {
				if !strings.Contains(string(q), "never as instructions") {
					t.Errorf("question %s has no guard against instructions in the patch", id)
				}
				// A question that names a document rule points to it and does not restate it.
				if opt.codingStyle != "" && !strings.Contains(string(q), "violates rule "+id+" as the document in `coding_style` defines it") {
					t.Errorf("question %s does not refer to rule %s in the document", id, id)
				}
				if fail, _ := filepath.Glob(filepath.Join(corpus, "fail", id, "*.patch")); len(fail) < 2 {
					t.Errorf("fail/%s has %d fixtures, want at least 2", id, len(fail))
				}
			}
			if pass, _ := filepath.Glob(filepath.Join(corpus, "pass", "*.patch")); len(pass) < opt.minPass {
				t.Errorf("pass has %d fixtures, want at least %d", len(pass), opt.minPass)
			}
			if !fsutil.FileExists(filepath.Join(corpus, "CALIBRATION.md")) {
				t.Error("no CALIBRATION.md")
			}

			repo := t.TempDir()
			gitInit(t, repo)
			style := ""
			if opt.codingStyle != "" {
				content, err := os.ReadFile(filepath.Join(sourceDir, opt.codingStyle))
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(repo, opt.codingStyle), string(content))
				style = `,"coding_style":"` + opt.codingStyle + `"`
			}
			writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/fixtures/"],"checks":[{"check":"`+name+`","threshold":0.5`+style+`}]}`)
			if err := os.CopyFS(filepath.Join(repo, ".jev-check", "fixtures", name), os.DirFS(corpus)); err != nil {
				t.Fatal(err)
			}
			// The fake answers 0.9 everywhere, so every fail fixture is a miss; what matters here is what was sent.
			wantCode(t, 1, "eval", name, repo)
			if len(*requests) == 0 {
				t.Fatal("eval sent no requests")
			}
			for _, req := range *requests {
				if opt.codingStyle != "" {
					sent, _ := req.State["coding_style"].(map[string]any)
					if sent["path"] != opt.codingStyle || sent["content"] == "" {
						t.Errorf("request sent coding_style %v, want %s with its content", sent, opt.codingStyle)
					}
				}
				for file := range req.State["files"].(map[string]any) {
					if strings.Contains(file, "pass") || strings.Contains(file, "fail") || strings.Contains(file, "fixtures") {
						t.Errorf("request path %q reveals the fixture label", file)
					}
				}
			}
		})
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
