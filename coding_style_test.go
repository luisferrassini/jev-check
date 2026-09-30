package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luisferrassini/jev-check/internal/workspace"
)

// styleRepo makes a git repo with a staged a.go and the given checks.
func styleRepo(t *testing.T, checks string) string {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	writeFile(t, filepath.Join(repo, ".jev-check/input/questions/other.json"), `{"questions":{"other":{"type":"noul"}}}`)
	styleConfig(t, repo, checks)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	gitRun(t, repo, "add", "a.go")
	return repo
}

func styleConfig(t *testing.T, repo, checks string) {
	t.Helper()
	writeFile(t, workspace.ConfigPath(repo), `{"exclude":[".jev-check/input/",".jev-check/fixtures/","CODING_STYLE.md"],"checks":[`+checks+`]}`)
}

// sentStyles returns each request's coding_style, by check.
func sentStyles(requests []request) map[string]any {
	got := map[string]any{}
	for _, req := range requests {
		check := "public-release"
		if req.Questions["other"] != nil {
			check = "other"
		}
		got[check] = req.State["coding_style"]
	}
	return got
}

const withStyle = `{"check":"public-release","threshold":0.2,"coding_style":"CODING_STYLE.md"}`
const otherCheck = `{"check":"other","threshold":0.2}`

func TestCodingStyleRequests(t *testing.T) {
	requests := setup(t)
	writeFile(t, "CODING_STYLE.md", "the wrong document, in the working directory\n")
	repo := styleRepo(t, withStyle+","+otherCheck)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv1\n")

	// Only the opted-in check gets the document, from DIR, although exclude lists it.
	wantCode(t, 0, "gate", repo)
	got := sentStyles(*requests)
	if len(*requests) != 2 || got["other"] != nil {
		t.Fatalf("requests %d, styles %v", len(*requests), got)
	}
	if style, _ := json.Marshal(got["public-release"]); string(style) != `{"content":"# Style\nv1\n","path":"CODING_STYLE.md"}` {
		t.Errorf("coding_style sent: %s", style)
	}

	// Threshold changes reuse the cache; a document change misses only for its check.
	styleConfig(t, repo, `{"check":"public-release","threshold":0.1,"coding_style":"CODING_STYLE.md"},`+otherCheck)
	wantCode(t, 0, "gate", repo)
	if len(*requests) != 2 {
		t.Errorf("threshold change sent %d requests", len(*requests)-2)
	}
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv2\n")
	out := wantCode(t, 0, "gate", repo)
	if len(*requests) != 3 || !strings.Contains(out, "== other a.go (cached)") {
		t.Errorf("after a document change, %d requests:\n%s", len(*requests), out)
	}
	if style := (*requests)[2].State["coding_style"].(map[string]any); style["content"] != "# Style\nv2\n" {
		t.Errorf("new bytes not sent: %v", style)
	}

	// Working-tree bytes win over the index, and an untracked document works.
	gitRun(t, repo, "add", "CODING_STYLE.md")
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv3, unstaged\n")
	*requests = nil
	wantCode(t, 0, "gate", repo)
	if style := (*requests)[0].State["coding_style"].(map[string]any); style["content"] != "# Style\nv3, unstaged\n" {
		t.Errorf("index bytes sent: %v", style)
	}
	writeFile(t, filepath.Join(repo, "docs/new style.md"), "untracked rules\n")
	styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"docs/new style.md"}`)
	*requests = nil
	wantCode(t, 0, "gate", repo)
	if style := (*requests)[0].State["coding_style"].(map[string]any); style["path"] != "docs/new style.md" {
		t.Errorf("untracked document: %v", style)
	}

	// A document deleted from the working tree fails, even with an index copy and a cache.
	styleConfig(t, repo, withStyle)
	wantCode(t, 0, "gate", repo)
	os.Remove(filepath.Join(repo, "CODING_STYLE.md"))
	*requests = nil
	wantCode(t, 2, "gate", repo)
	if len(*requests) != 0 {
		t.Error("a missing document still sent requests")
	}
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nv4\n")

	// Gate and eval send the same document.
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	*requests = nil
	wantCode(t, 0, "gate", repo, "--no-cache")
	wantCode(t, 0, "eval", "public-release", repo, "--no-cache")
	gateStyle, _ := json.Marshal((*requests)[0].State["coding_style"])
	for _, req := range (*requests)[1:] {
		if evalStyle, _ := json.Marshal(req.State["coding_style"]); string(evalStyle) != string(gateStyle) {
			t.Errorf("eval sent %s, gate sent %s", evalStyle, gateStyle)
		}
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

func TestCodingStyleInvalid(t *testing.T) {
	requests := setup(t)
	repo := styleRepo(t, otherCheck)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "rules\n")
	os.Mkdir(filepath.Join(repo, "adir"), 0o755)
	os.Symlink("CODING_STYLE.md", filepath.Join(repo, "link.md"))
	os.Symlink("adir", filepath.Join(repo, "linkdir"))
	writeFile(t, filepath.Join(repo, "adir/doc.md"), "rules\n")
	big := strings.Repeat("a", 65536)
	docs := map[string]string{
		"blank.md": " \n\t\n", "nul.md": "a\x00b\n", "latin1.md": "caf\xe9\n", "big.md": big + "a",
	}
	for name, content := range docs {
		writeFile(t, filepath.Join(repo, name), content)
	}
	bad := []string{`1`, `null`, `""`, `"  "`, `"missing.md"`, `"CODING_STYLE.md/"`, `"adir/doc.md/"`, `"adir"`, `"link.md"`, `"linkdir/doc.md"`, `"../CODING_STYLE.md"`,
		`"adir/../../x.md"`, `"` + filepath.Join(repo, "CODING_STYLE.md") + `"`, `"blank.md"`, `"nul.md"`, `"latin1.md"`, `"big.md"`}
	for _, value := range bad {
		styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":`+value+`}`)
		for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
			var stdout, stderr strings.Builder
			if code := run(args, &stdout, &stderr); code != 2 || strings.Contains(stdout.String()+stderr.String(), "aaaaaaaa") {
				t.Errorf("coding_style %s, %s: exit %d: %s", value, args[0], code, stderr.String())
			} else if !strings.Contains(stderr.String(), "public-release") {
				t.Errorf("error does not name the check: %s", stderr.String())
			}
		}
	}

	// A path that looks like a secret is not echoed, missing or unreadable.
	secretName := "docs/" + awsKey + ".md"
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(repo, "unreadable", secretName), "rules\n")
		os.Chmod(filepath.Join(repo, "unreadable", secretName), 0)
	}
	for _, name := range []string{secretName, "unreadable/" + secretName} {
		styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"`+name+`"}`)
		for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
			if stderr := wantErr(t, "public-release", args...); strings.Contains(stderr, awsKey) || !strings.Contains(stderr, "not shown") {
				t.Errorf("coding_style %s, %s: %s", name, args[0], stderr)
			}
		}
	}
	os.RemoveAll(filepath.Join(repo, "unreadable"))

	// A later invalid reference stops the gate before the first valid check runs.
	styleConfig(t, repo, withStyle+`,{"check":"other","threshold":0.2,"coding_style":"missing.md"}`)
	wantCode(t, 2, "gate", repo)
	if len(*requests) != 0 {
		t.Errorf("%d requests with an invalid reference", len(*requests))
	}

	// The largest allowed document is sent whole.
	writeFile(t, filepath.Join(repo, "big.md"), big)
	styleConfig(t, repo, `{"check":"public-release","threshold":0.2,"coding_style":"big.md"}`)
	wantCode(t, 0, "gate", repo)
	if content := (*requests)[0].State["coding_style"].(map[string]any)["content"]; content != big {
		t.Errorf("sent %d bytes", len(content.(string)))
	}
}

func TestCodingStyleSecret(t *testing.T) {
	requests := setup(t)
	repo := styleRepo(t, otherCheck+","+withStyle)
	writeFile(t, filepath.Join(repo, "CODING_STYLE.md"), "# Style\nexample = "+awsKey+"\n")
	for _, args := range [][]string{{"gate", repo}, {"state", repo}} {
		if out := wantBlocked(t, requests, awsKey, args...); !strings.Contains(out, "CODING_STYLE.md line 2 looks like aws-access-key") {
			t.Errorf("%s output:\n%s", args[0], out)
		}
	}
	fixtures := filepath.Join(repo, ".jev-check", "fixtures", "public-release")
	writeFile(t, filepath.Join(fixtures, "pass", "a.patch"), gitPatch(t, "a.go", "package a\n"))
	for _, q := range []string{"english_only", "no_personal_info", "no_outside_paths", "no_private_links", "no_third_party_content", "belongs_in_project"} {
		writeFile(t, filepath.Join(fixtures, "fail", q, "b.patch"), gitPatch(t, "bad-"+q+".go", "package b\n"))
	}
	wantBlocked(t, requests, awsKey, "eval", "public-release", repo)
}
