package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestJudge(t *testing.T) {
	out := writeFile(t, filepath.Join(t.TempDir(), "out.json"), `{"response":`+fakeAnswers+`}`)
	wantCode(t, 0, "judge", out, "0.5", "b=0.2")
	got := wantCode(t, 1, "judge", out, "0.5")
	if !strings.Contains(got, "FAIL  0.3  b\n") || !strings.Contains(got, "info  x 0.8  c\n") {
		t.Errorf("judge output:\n%s", got)
	}
	wantCode(t, 2, "judge", out, "0.5", "zzz=0.1")
	wantCode(t, 2, "judge", out, "0.5", "b=x")
	wantCode(t, 2, "judge", out, "1.5")
	for _, response := range []string{`{"answers":{}}`, `{"answers":{"a":{"type":"noul"}}}`} {
		writeFile(t, out, `{"response":`+response+`}`)
		wantCode(t, 2, "judge", out, "0")
	}
	writeFile(t, out, `{"request":{"questions":{"missing":{"type":"noul"}}},"response":`+fakeAnswers+`}`)
	wantCode(t, 2, "judge", out, "0")
}
