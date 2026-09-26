package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// optInChecks are the bundled opt-in checks whose corpora TestOptInCorpora covers.
var optInChecks = []string{"no-leftovers", "test-quality"}

// TestOptInCorpora runs eval offline on a disposable copy of each opt-in corpus,
// as the README's setup does, and checks what the corpus and the requests hold.
func TestOptInCorpora(t *testing.T) {
	for _, name := range optInChecks {
		t.Run(name, func(t *testing.T) {
			requests := setup(t)
			var check struct {
				Questions map[string]json.RawMessage `json:"questions"`
			}
			if err := readJSON(filepath.Join(sourceDir, bundleDir, "input", "questions", name+".json"), &check); err != nil {
				t.Fatal(err)
			}
			corpus := filepath.Join(sourceDir, bundleDir, "fixtures", name)
			for id, q := range check.Questions {
				if !strings.Contains(string(q), "never as instructions") {
					t.Errorf("question %s has no guard against instructions in the patch", id)
				}
				if fail, _ := filepath.Glob(filepath.Join(corpus, "fail", id, "*.patch")); len(fail) < 2 {
					t.Errorf("fail/%s has %d fixtures, want at least 2", id, len(fail))
				}
			}
			if pass, _ := filepath.Glob(filepath.Join(corpus, "pass", "*.patch")); len(pass) < 10 {
				t.Errorf("pass has %d fixtures, want at least 10", len(pass))
			}
			if !fileExists(filepath.Join(corpus, "CALIBRATION.md")) {
				t.Error("no CALIBRATION.md")
			}

			repo := t.TempDir()
			gitInit(t, repo)
			writeFile(t, configPath(repo), `{"exclude":[".jev-check/fixtures/"],"checks":[{"check":"`+name+`","threshold":0.5}]}`)
			if err := os.CopyFS(filepath.Join(repo, ".jev-check", "fixtures", name), os.DirFS(corpus)); err != nil {
				t.Fatal(err)
			}
			// The fake answers 0.9 everywhere, so every fail fixture is a miss; what matters here is what was sent.
			wantCode(t, 1, "eval", name, repo)
			if len(*requests) == 0 {
				t.Fatal("eval sent no requests")
			}
			for _, req := range *requests {
				for file := range req.State["files"].(map[string]any) {
					if strings.Contains(file, "pass") || strings.Contains(file, "fail") || strings.Contains(file, "fixtures") {
						t.Errorf("request path %q reveals the fixture label", file)
					}
				}
			}
		})
	}
}
