// Package patchset reads the pass and fail fixture patches that eval runs.
package patchset

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/luisferrassini/jev-check/internal/jev"
)

// Fixture is one patch to evaluate. question is empty for a pass fixture.
type Fixture struct {
	Rel, Question string
	Req           jev.Request
}

// Find lists pass/*.patch and fail/<question>/*.patch, sorted, pass first.
// Every fail folder must name a blocking question, and the pass set and every
// blocking question's fail set must be non-empty. Missing sets are named together.
func Find(fixtures string, blocking []string) ([]Fixture, error) {
	var jobs []Fixture
	add := func(rel, question string) error {
		entries, err := os.ReadDir(filepath.Join(fixtures, rel))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errors.New(Printable(err.Error()))
		}
		for _, e := range entries {
			name := filepath.Join(rel, e.Name())
			if !strings.HasSuffix(e.Name(), ".patch") {
				continue
			}
			if !e.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", Printable(filepath.Join(fixtures, name)))
			}
			jobs = append(jobs, Fixture{Rel: name, Question: question})
		}
		return nil
	}
	if err := add("pass", ""); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(fixtures, "fail"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New(Printable(err.Error()))
	}
	for _, e := range entries {
		path := Printable(filepath.Join(fixtures, "fail", e.Name()))
		switch {
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".patch"):
			return nil, fmt.Errorf("%s: a fail fixture goes in fail/<question>/", path)
		case !e.IsDir():
		case !slices.Contains(blocking, e.Name()):
			return nil, fmt.Errorf("%s: fail folders must name a yes/no question of the check (%s)", path, strings.Join(blocking, ", "))
		default:
			if err := add(filepath.Join("fail", e.Name()), e.Name()); err != nil {
				return nil, err
			}
		}
	}
	var missing []string
	if !slices.ContainsFunc(jobs, func(f Fixture) bool { return f.Question == "" }) {
		missing = append(missing, "pass/")
	}
	for _, q := range blocking {
		if !slices.ContainsFunc(jobs, func(f Fixture) bool { return f.Question == q }) {
			missing = append(missing, "fail/"+q+"/")
		}
	}
	if missing != nil {
		return nil, fmt.Errorf("%s needs patches in %s", Printable(fixtures), strings.Join(missing, ", "))
	}
	return jobs, nil
}

// PatchPath returns the project path a single-file git patch changes: the new path,
// or the old one for a deletion. It reads only the headers before the first hunk,
// so a hunk line that looks like a header never counts.
func PatchPath(patch string) (string, error) {
	lines := strings.Split(patch, "\n")
	if n := len(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return !strings.HasPrefix(l, "diff --git ") })); n != 1 {
		return "", fmt.Errorf("needs exactly one diff --git file section, found %d; make one fixture per file", n)
	}
	h := map[string]string{}
	var hunks []string
	started := false
	for i, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			started = true
		}
		if !started {
			continue
		}
		if strings.HasPrefix(l, "@@") {
			hunks = lines[i:]
			break
		}
		if strings.HasSuffix(l, "\r") {
			return "", errors.New("has a header line that ends in a carriage return; save the patch with LF line endings")
		}
		if strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch" {
			return "", errors.New("binary patches are not supported")
		}
		for _, key := range []string{"--- ", "+++ ", "rename from ", "rename to "} {
			if v, ok := strings.CutPrefix(l, key); ok {
				if _, dup := h[key]; dup {
					return "", fmt.Errorf("has two %q lines", strings.TrimSpace(key))
				}
				h[key] = v
			}
		}
	}
	if err := checkHunks(hunks); err != nil {
		return "", err
	}
	_, hasMinus := h["--- "]
	_, hasPlus := h["+++ "]
	_, hasFrom := h["rename from "]
	_, hasTo := h["rename to "]
	switch {
	case hasFrom != hasTo:
		return "", errors.New("needs both rename from and rename to")
	case hasMinus != hasPlus:
		return "", errors.New("needs both a --- and a +++ line")
	case !hasPlus && !hasTo:
		return "", errors.New("has no ---/+++ or rename lines; binary, mode-only, and other formats are not supported")
	}
	// Every path header is decoded and checked, even the ones that are not sent.
	paths := map[string]string{}
	for _, key := range []string{"--- ", "+++ ", "rename from ", "rename to "} {
		v, ok := h[key]
		prefix := map[string]string{"--- ": "a/", "+++ ": "b/"}[key]
		if !ok || v == "/dev/null" && prefix != "" {
			continue
		}
		path, err := decodePath(v, prefix)
		if err != nil {
			return "", err
		}
		if path == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") || !utf8.ValidString(path) ||
			slices.Contains(strings.Split(path, "/"), "..") {
			return "", fmt.Errorf("has an unsafe or unsupported path %q", path)
		}
		paths[key] = path
	}
	oldPath, newPath := paths["--- "], paths["+++ "]
	switch {
	case hasPlus && oldPath == "" && newPath == "":
		return "", errors.New("has /dev/null on both sides")
	case hasTo && hasPlus && (paths["rename from "] != oldPath || paths["rename to "] != newPath):
		return "", errors.New("its rename lines and its ---/+++ lines disagree")
	case !hasTo && oldPath != "" && newPath != "" && oldPath != newPath:
		return "", errors.New("its --- and +++ lines name different paths without rename lines")
	}
	return cmp.Or(newPath, oldPath, paths["rename to "]), nil
}

// checkHunks follows each hunk's line counts, so a --- or +++ line after a hunk ends
// is found as a second file section without its own diff --git line.
func checkHunks(lines []string) error {
	oldLeft, newLeft := 0, 0
	for _, l := range lines {
		switch {
		case oldLeft > 0 || newLeft > 0:
			switch {
			case strings.HasPrefix(l, "-"):
				oldLeft--
			case strings.HasPrefix(l, "+"):
				newLeft--
			case strings.HasPrefix(l, `\`):
			default:
				oldLeft--
				newLeft--
			}
			if oldLeft < 0 || newLeft < 0 {
				return errors.New("has a hunk longer than its @@ header says")
			}
		case strings.HasPrefix(l, "@@"):
			f := strings.Fields(l)
			if len(f) < 4 || f[0] != "@@" || f[3] != "@@" {
				return errors.New("has an invalid @@ hunk header")
			}
			var err error
			if oldLeft, err = hunkCount(f[1], "-"); err == nil {
				newLeft, err = hunkCount(f[2], "+")
			}
			if err != nil {
				return err
			}
		case strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ "):
			return errors.New("has a second file section without a diff --git line; make one fixture per file")
		}
	}
	return nil
}

// hunkCount returns the line count of one @@ range, such as -3,4 or +5 (one line).
func hunkCount(field, sign string) (int, error) {
	field, ok := strings.CutPrefix(field, sign)
	start, count, hasCount := strings.Cut(field, ",")
	if !hasCount {
		count = "1"
	}
	_, startErr := strconv.Atoi(start)
	n, err := strconv.Atoi(count)
	if !ok || startErr != nil || err != nil || n < 0 {
		return 0, errors.New("has an invalid @@ hunk header")
	}
	return n, nil
}

// decodePath decodes one path field of a git header and strips its a/ or b/ prefix.
// Git ends an unquoted path that holds a space with a tab, and quotes any path with
// a tab, quote, backslash, control, or (by default) non-ASCII character.
func decodePath(field, prefix string) (string, error) {
	field = strings.TrimSuffix(field, "\t")
	if strings.HasPrefix(field, `"`) {
		// Git writes only C escapes and three-digit octal bytes, fewer than strconv.Unquote accepts.
		for i := 0; i < len(field); i++ {
			if field[i] != '\\' {
				continue
			}
			i++
			switch {
			case i < len(field) && strings.IndexByte(`abtnvfr"\`, field[i]) >= 0:
			case i+3 <= len(field) && strings.Trim(field[i:i+3], "01234567") == "":
				i += 2
			default:
				return "", errors.New("has an invalid quoted path")
			}
		}
		unquoted, err := strconv.Unquote(field)
		if err != nil {
			return "", errors.New("has an invalid quoted path")
		}
		field = unquoted
	}
	path, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return "", fmt.Errorf("has a path without the %s prefix", prefix)
	}
	return path, nil
}

// Printable quotes s when it holds a control character, so a file name cannot forge an output line.
func Printable(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return strconv.Quote(s)
	}
	return s
}
