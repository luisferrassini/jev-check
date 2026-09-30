package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/luisferrassini/jev-check/internal/secretscan"
)

// maxStyleBytes caps a coding_style document, so a large file cannot flood a request.
const maxStyleBytes = 65536

// LoadStyles reads each check's coding_style document from dir's working tree, once per path.
// It returns each check's normalized path, empty for none, and the contents by path.
func LoadStyles(dir string, checks []GateCheck) ([]string, map[string]string, error) {
	paths := make([]string, len(checks))
	var docs map[string]string
	for i, c := range checks {
		if c.CodingStyle == nil {
			continue
		}
		path, err := stylePath(dir, c.CodingStyle)
		if docs == nil {
			docs = map[string]string{}
		}
		if _, ok := docs[path]; err == nil && !ok {
			docs[path], err = readStyle(filepath.Join(dir, filepath.FromSlash(path)))
		}
		if err != nil {
			// The path may hold a secret, so neither it nor an OS error that repeats it is printed.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
			}
			return nil, nil, fmt.Errorf("check %s: coding_style %s: %w", c.Check, secretscan.SafeLabel(string(c.CodingStyle), "path (not shown)"), err)
		}
		paths[i] = path
	}
	return paths, docs, nil
}

// stylePath checks that raw names a regular file inside dir, reached without
// symlinks or .., and returns it with forward slashes.
func stylePath(dir string, raw json.RawMessage) (string, error) {
	var rel string
	if json.Unmarshal(raw, &rel) != nil || strings.TrimSpace(rel) == "" {
		return "", errors.New("must be a non-empty path string")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("must be relative to the project folder")
	}
	sep := string(filepath.Separator)
	if strings.HasSuffix(filepath.FromSlash(rel), sep) {
		return "", errors.New("must name a file, without a trailing " + sep)
	}
	if slices.Contains(strings.Split(filepath.FromSlash(rel), sep), "..") {
		return "", errors.New("must not contain ..")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	path := dir
	var info fs.FileInfo
	for _, part := range strings.Split(clean, sep) {
		path = filepath.Join(path, part)
		var err error
		if info, err = os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("no such file in the working tree")
		} else if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("must not go through a symlink")
		}
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	return filepath.ToSlash(clean), nil
}

// readStyle reads a document of at most maxStyleBytes of UTF-8 text, exactly as it is.
func readStyle(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStyleBytes+1))
	switch {
	case err != nil:
		return "", err
	case len(data) > maxStyleBytes:
		return "", fmt.Errorf("is larger than %d bytes", maxStyleBytes)
	case !utf8.Valid(data):
		return "", errors.New("is not valid UTF-8")
	case bytes.IndexByte(data, 0) >= 0:
		return "", errors.New("holds a NUL byte")
	case strings.TrimSpace(string(data)) == "":
		return "", errors.New("is empty")
	}
	return string(data), nil
}

// addStyle puts a check's document into its request state, when it has one.
func addStyle(state map[string]any, path string, docs map[string]string) {
	if path != "" {
		state["coding_style"] = map[string]string{"path": path, "content": docs[path]}
	}
}

// FileState is a copy of state with one file's patch and the check's coding_style document.
// Each patch is named after its file, so the state key tells Jev which file it reads.
func FileState(state map[string]any, file, patch, stylePath string, styles map[string]string) map[string]any {
	out := maps.Clone(state)
	out["files"] = map[string]string{file + ".patch": patch}
	addStyle(out, stylePath, styles)
	return out
}
