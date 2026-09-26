package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedChecksDryRun asks every shipped check with --dry-run, the way a user would,
// and requires a valid request with no call to the API.
func TestShippedChecksDryRun(t *testing.T) {
	requests := setup(t)
	names, err := filepath.Glob(filepath.Join(sourceDir, "input/questions/*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no shipped checks: %v", err)
	}
	input := writeFile(t, filepath.Join(t.TempDir(), "input.txt"), "hello\n")
	list := wantCode(t, 0, "list")
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		if !strings.Contains(list, name+" [bundled") {
			t.Errorf("list does not show %s:\n%s", name, list)
		}
		args := []string{"ask", "--dry-run", name}
		if !fileExists(filepath.Join(sourceDir, "input/states", name+".json")) {
			args = append(args, "--file", input)
		}
		var req request
		if err := json.Unmarshal([]byte(wantCode(t, 0, args...)), &req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if req.Model == "" || len(req.Questions) == 0 || len(req.State) == 0 {
			t.Errorf("%s: request %+v", name, req)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("dry run sent %d requests", len(*requests))
	}
}

// TestShippedConfigs runs the gate with no staged files on this repository's
// project-context.json and on the README's canonical example. Both must validate.
func TestShippedConfigs(t *testing.T) {
	requests := setup(t)
	real, err := os.ReadFile(filepath.Join(sourceDir, "project-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]string{"project-context.json": string(real), "README": readmeConfig(t)} {
		repo := t.TempDir()
		gitRun(t, repo, "init", "-q")
		writeFile(t, filepath.Join(repo, "project-context.json"), config)
		if out := wantCode(t, 0, "gate", repo); out != "nothing staged\n" {
			t.Errorf("%s: gate: %s", name, out)
		}
		var state struct {
			Project map[string]any `json:"project"`
		}
		if err := json.Unmarshal([]byte(wantCode(t, 0, "context", repo)), &state); err != nil || state.Project["purpose"] == nil {
			t.Errorf("%s: context project %v, %v", name, state.Project, err)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("gate sent %d requests", len(*requests))
	}
}

// readmeConfig returns the JSON block that follows the canonical-config marker in README.md.
func readmeConfig(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sourceDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "<!-- canonical-config"
	parts := strings.Split(string(data), marker)
	if len(parts) != 2 {
		t.Fatalf("README.md has %d %s markers, want 1", len(parts)-1, marker)
	}
	_, rest, _ := strings.Cut(parts[1], "-->")
	block, ok := strings.CutPrefix(strings.TrimLeft(rest, "\n"), "```json\n")
	if !ok {
		t.Fatalf("no ```json block right after %s", marker)
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatal("unclosed README config block")
	}
	return block
}
