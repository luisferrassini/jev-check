package main

import (
	"os"
	"path/filepath"
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
	t.Setenv("TYPESAFE_API_KEY", "")
	wantBlocked(t, requests, awsKey, "ask", "public-release", "--file", leaky)
	if fileExists("output") {
		t.Error("a blocked request created output/")
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
	gitRun(t, repo, "init", "-q")
	config := func(extra string) {
		writeFile(t, filepath.Join(repo, "project-context.json"), `{`+extra+` "exclude":["input/"], "checks":[{"check":"public-release","threshold":0.2}]}`)
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
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitRun(t, repo, "add", "a.txt")
	wantCode(t, 0, "gate", repo) // fills the cache

	// A new tree entry does not change the cache key, so only the shared scan stops it.
	named := writeFile(t, filepath.Join(repo, ghToken+".txt"), "x\n")
	wantBlocked(t, requests, ghToken, "gate", repo)
	os.Remove(named)

	// A check whose questions look like secrets is skipped; clean checks still run.
	writeFile(t, filepath.Join(repo, "input/questions/bad.json"), `{"questions":{"q":{"type":"noul","instructions":"`+ghToken+`"}}}`)
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["input/"], "checks":[{"check":"bad","threshold":0.2},{"check":"public-release","threshold":0.2}]}`)
	*requests = nil
	code, out := jev(t, "gate", repo, "--no-cache")
	if code != 1 || len(*requests) != 1 || strings.Contains(out, ghToken) || !strings.Contains(out, "== public-release a.txt") {
		t.Errorf("exit %d, %d requests:\n%s", code, len(*requests), out)
	}

	// A blocked patch and an API failure elsewhere: the error wins.
	writeFile(t, filepath.Join(repo, "input/questions/down.json"), `{"questions":{"api_down":{"type":"noul"}}}`)
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["input/"], "checks":[{"check":"public-release","threshold":0.2},{"check":"down","threshold":0.2}]}`)
	writeFile(t, filepath.Join(repo, "b.txt"), "aws = "+awsKey+"\n")
	gitRun(t, repo, "add", "b.txt")
	if code, out := jev(t, "gate", repo, "--no-cache"); code != 2 || strings.Contains(out, awsKey) || !strings.HasSuffix(out, "gate: ERROR\n") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestEvalPreflight(t *testing.T) {
	requests := setup(t)
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "project-context.json"), `{"exclude":["fixtures/"],"checks":[{"check":"public-release","threshold":0.2}]}`)
	fixtures := filepath.Join(repo, "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.go.patch"), "+++ b/a.go\n+package a\n")
	writeFile(t, filepath.Join(fixtures, "fail", "english_only", "b.go.patch"), "+++ b/b.go\n+package b\n")
	wantCode(t, 0, "eval", "public-release", repo) // fills the cache

	writeFile(t, filepath.Join(fixtures, "fail", "english_only", "z.go.patch"), "+++ b/z.go\n+key = \""+awsKey+"\"\n")
	for _, args := range [][]string{{"eval", "public-release", repo}, {"eval", "public-release", repo, "--no-cache"}} {
		if out := wantBlocked(t, requests, awsKey, args...); strings.Contains(out, "misses") || !strings.Contains(out, "fail/english_only/z.go.patch") {
			t.Errorf("eval output after a block:\n%s", out)
		}
	}
}
