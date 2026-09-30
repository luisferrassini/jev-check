package secretscan

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestScanRequestDedup(t *testing.T) {
	// The value alone and its assignment both find github-token under the same label.
	token := "ghp_" + strings.Repeat("a1", 18)
	var found Found
	if !errors.As(ScanRequest(map[string]any{"state": map[string]any{"token": token}}), &found) {
		t.Fatal("token not found")
	}
	want := []string{"SECRET  request.state.token looks like github-token", "SECRET  request.state.token looks like quoted-secret"}
	if !slices.Equal(found, want) {
		t.Errorf("reports %q, want %q", found, want)
	}
}
