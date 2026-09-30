package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luisferrassini/jev-check/internal/secretscan"
)

func TestSecrets(t *testing.T) {
	dir := t.TempDir()
	// Fake keys are built at run time, so this file holds none.
	r := strings.Repeat
	leaks := [][2]string{
		{"aws-access-key", "aws = AKIA" + r("Q", 16)},
		{"env-secret", "export DB_PASSWORD=" + r("p4", 8)},
		{"private-key", "-----BEGIN OPENSSH PRIV" + "ATE KEY-----"},
		{"github-token", "token: ghp_" + r("a1", 18)},
		{"gitlab-token", "glpat-" + r("x1", 10)},
		{"slack-token", "xoxb-" + r("12", 6)},
		{"stripe-key", "stripe = sk_live_" + r("a1", 12)},
		{"google-api-key", "AIza" + r("B", 35)},
		{"sk-api-key", "sk-ant-api03-" + r("c2", 12)},
		{"jwt", "eyJ" + r("h", 12) + ".eyJ" + r("p", 12) + "." + r("s", 12)},
		{"url-credentials", "postgres://app:s3cr3t" + "@db:5432"},
		{"bearer-literal", "Authorization: Bearer " + r("t9", 12)},
		{"quoted-secret", `api_key = "` + r("k7", 8) + `"`},
	}
	patch := "+++ b/x\n"
	for _, leak := range leaks {
		patch += "+" + leak[1] + "\n"
	}
	code, out := runCmd(t, "secrets", writeFile(t, filepath.Join(dir, "leak.patch"), patch))
	if code != 1 {
		t.Errorf("exit %d on keys, want 1", code)
	}
	for _, leak := range leaks {
		if !strings.Contains(out, "looks like "+leak[0]+"\n") {
			t.Errorf("%s not reported", leak[0])
		}
		if strings.Contains(out, leak[1]) {
			t.Errorf("printed the secret for %s", leak[0])
		}
	}

	clean := []string{`TOKEN=$(cat file)`, `api_key = os.environ["KEY"]`, `KEY=`, `API_KEY=your-api-key-here`,
		`password = "changeme-please"`, `postgres://user:password@localhost`, `Authorization: Bearer $TOKEN`,
		`SKILL_md = "a3b9c1d7e5f2a3b9c1d7e5f2a3b9c1d7"`}
	wantCode(t, 0, "secrets", writeFile(t, filepath.Join(dir, "clean.patch"), "+"+strings.Join(clean, "\n+")+"\n"))

	sources, _ := filepath.Glob("*.go")
	internal, _ := filepath.Glob("internal/*/*.go")
	sources = append(sources, internal...)
	if len(internal) == 0 {
		t.Fatal("found no internal/*/*.go to scan")
	}
	for _, source := range sources {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		self := "+" + strings.ReplaceAll(string(content), "\n", "\n+")
		if reports := secretscan.Scan(source, self); reports != nil {
			t.Errorf("false positive on %s: %v", source, reports)
		}
	}
	wantCode(t, 2, "secrets", filepath.Join(dir, "missing.patch"))
}
func TestSecretsSafeLabel(t *testing.T) {
	dir := t.TempDir()
	patch := writeFile(t, filepath.Join(dir, "x\nSECRET  forged.patch"), "+"+awsKey+"\n")
	code, out := runCmd(t, "secrets", patch)
	if code != 1 || strings.Count(out, "\n") != 1 || strings.Contains(out, "forged") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}
