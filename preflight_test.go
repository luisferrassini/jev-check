package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Fake keys are built at run time, so this file holds none.
var (
	ghToken  = "ghp_" + strings.Repeat("a1", 18)
	awsKey   = "AKIA" + strings.Repeat("Q", 16)
	password = "abc123" + "def456"
)

// wantBlocked runs a command that must stop at the secret scan: exit 1, no request, no leaked value.
func wantBlocked(t *testing.T, requests *[]request, leak string, args ...string) string {
	t.Helper()
	before := len(*requests)
	var stdout, stderr strings.Builder
	code := run(args, &stdout, &stderr)
	out := stdout.String()
	if code != 1 || !strings.Contains(out, "SECRET  ") {
		t.Errorf("jev-check %s: exit %d, want 1 with a SECRET line\n%s%s", strings.Join(args, " "), code, out, stderr.String())
	}
	if strings.Contains(out+stderr.String(), leak) {
		t.Errorf("jev-check %s printed the secret:\n%s", strings.Join(args, " "), out)
	}
	if len(*requests) != before {
		t.Errorf("jev-check %s sent %d requests", strings.Join(args, " "), len(*requests)-before)
	}
	return out
}

// outputFiles lists the files under project/.jev-check/output/, cache entries included,
// so a test can tell that a blocked run wrote nothing.
func outputFiles(t *testing.T, project string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(project, ".jev-check", "output"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
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
	if files := outputFiles(t, "."); files != nil || fileExists(filepath.Join(".jev-check", "output")) {
		t.Errorf("a blocked request created .jev-check/output/: %v", files)
	}
}

func TestScanRequestDedup(t *testing.T) {
	// The value alone and its assignment both find github-token under the same label.
	var found secretsFound
	if !errors.As(scanRequest(request{State: map[string]any{"token": ghToken}}), &found) {
		t.Fatal("token not found")
	}
	want := []string{"SECRET  request.state.token looks like github-token", "SECRET  request.state.token looks like quoted-secret"}
	if !slices.Equal(found, want) {
		t.Errorf("reports %q, want %q", found, want)
	}
}

func TestSecretsSafeLabel(t *testing.T) {
	dir := t.TempDir()
	patch := writeFile(t, filepath.Join(dir, "x\nSECRET  forged.patch"), "+"+awsKey+"\n")
	code, out := jev(t, "secrets", patch)
	if code != 1 || strings.Count(out, "\n") != 1 || strings.Contains(out, "forged") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestGatePreflight(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	config := func(extra string) {
		writeFile(t, configPath(repo), `{`+extra+` "exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2}]}`)
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
	writeFile(t, configPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2,"coding_style":".jev-check/input/`+ghToken+`.md"}]}`)
	if out := wantBlocked(t, requests, ghToken, "gate", repo); !strings.Contains(out, "SECRET  coding_style document path looks like github-token\n") {
		t.Errorf("gate output:\n%s", out)
	}
	os.Remove(style)

	// A blocked question set fails the gate even with nothing staged.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/bad.json"), `{"questions":{"q":{"type":"noul","instructions":"`+ghToken+`"}}}`)
	writeFile(t, configPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"bad","threshold":0.2}]}`)
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
	writeFile(t, configPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"bad","threshold":0.2},{"check":"public-release","threshold":0.2}]}`)
	*requests = nil
	code, out := jev(t, "gate", repo, "--no-cache")
	if code != 1 || len(*requests) != 1 || strings.Contains(out, ghToken) || !strings.Contains(out, "== public-release a.txt") {
		t.Errorf("exit %d, %d requests:\n%s", code, len(*requests), out)
	}

	// A blocked patch and an API failure elsewhere: the error wins.
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/down.json"), `{"questions":{"api_down":{"type":"noul"}}}`)
	writeFile(t, configPath(repo), `{"exclude":[".jev-check/input/"], "checks":[{"check":"public-release","threshold":0.2},{"check":"down","threshold":0.2}]}`)
	writeFile(t, filepath.Join(repo, "b.txt"), "aws = "+awsKey+"\n")
	gitRun(t, repo, "add", "b.txt")
	if code, out := jev(t, "gate", repo, "--no-cache"); code != 2 || strings.Contains(out, awsKey) || !strings.HasSuffix(out, "gate: ERROR\n") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestEvalPreflight(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, configPath(repo), `{"exclude":[".jev-check/fixtures/"],"checks":[{"check":"public-release","threshold":0.2}]}`)
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
