// Package secretscan finds text that looks like a secret, before anything is sent to Jev.
package secretscan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

type secretPattern struct {
	kind string
	re   *regexp.Regexp
}

// Generic assignments need a digit inside the value, so placeholders like your-api-key-here pass.
var secretPatterns = []secretPattern{
	{"private-key", regexp.MustCompile(`-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY( BLOCK)?-----`)},
	{"aws-access-key", regexp.MustCompile(`\b(AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{"aws-secret-key", regexp.MustCompile(`(?i)aws.{0,20}(secret|private).{0,20}[:=]\s*["']?[A-Za-z0-9/+]{40}\b`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`)},
	{"github-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{80,}`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"slack-webhook", regexp.MustCompile(`hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]{20,}`)},
	{"stripe-key", regexp.MustCompile(`\b(sk|rk)_(live|test)_[A-Za-z0-9]{20,}`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
	{"google-oauth-secret", regexp.MustCompile(`\bGOCSPX-[A-Za-z0-9_-]{28}`)},
	{"sk-api-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"npm-token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"pypi-token", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}`)},
	{"sendgrid-key", regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"url-credentials", regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s:@$]+:[^/\s@$<{]*[0-9][^/\s@$<{]*@`)},
	{"bearer-literal", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}`)},
	{"quoted-secret", regexp.MustCompile(`(?i)(api[_-]?key|secret|token|passw(or)?d|pwd|credential|access[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*["'][A-Za-z0-9_/+.=-]{5,}[0-9][A-Za-z0-9_/+.=-]{5,}["']`)},
	{"env-secret", regexp.MustCompile(`^\s*(export\s+)?[A-Z0-9_]*(KEY|SECRET|TOKEN|PASSWORD|PASSWD|PWD|CREDENTIALS?)[A-Z0-9_]*=["']?[A-Za-z0-9_/+.=-]{5,}[0-9][A-Za-z0-9_/+.=-]{5,}`)},
}

// Scan returns one line per kind of secret in a patch, including removed and context lines.
// It names the patch line numbers, never the value, and names the patch only when the name is safe to print.
func Scan(name, patch string) []string {
	return Reports(SafeLabel(name, "patch"), patch, true)
}

// Reports returns one line per kind of secret in text. With lines set, it names the line numbers.
func Reports(label, text string, lines bool) []string {
	var reports []string
	split := strings.Split(text, "\n")
	for _, p := range secretPatterns {
		var hits []string
		for i, line := range split {
			// Also try without one diff marker, so anchored patterns match removed lines,
			// while text that is not a patch keeps its first character.
			unmarked := line
			if len(line) > 0 && strings.ContainsRune("+- ", rune(line[0])) {
				unmarked = line[1:]
			}
			if p.re.MatchString(line) || p.re.MatchString(unmarked) {
				hits = append(hits, strconv.Itoa(i+1))
			}
		}
		switch {
		case hits == nil:
		case lines:
			reports = append(reports, fmt.Sprintf("SECRET  %s line %s looks like %s", label, strings.Join(hits, ","), p.kind))
		default:
			reports = append(reports, fmt.Sprintf("SECRET  %s looks like %s", label, p.kind))
		}
	}
	return reports
}

// SafeLabel returns name for a diagnostic, or fallback when name looks like a secret
// or holds a control character that could forge another output line.
func SafeLabel(name, fallback string) string {
	if strings.IndexFunc(name, unicode.IsControl) >= 0 || Reports("", name, false) != nil {
		return fallback
	}
	return name
}

// Found is a local refusal to send content that looks like it holds a secret.
// run prints its reports and exits 1, unlike other errors.
type Found []string

func (s Found) Error() string { return strings.Join(s, "\n") }

// ScanRequest scans everything a request would send: the model, question ids and
// definitions, and every key and value in the state, after JSON escapes are decoded.
// It returns nil when the request is clean.
func ScanRequest(req any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	var reports []string
	scanValue("request", v, &reports)
	if reports == nil {
		return nil
	}
	// The same location and kind can be found twice, as a value and as an assignment.
	var unique []string
	for _, report := range reports {
		if !slices.Contains(unique, report) {
			unique = append(unique, report)
		}
	}
	return Found(unique)
}

// scanValue scans each string with its line numbers, each object key, and each
// scalar entry or scalar array item as a quoted assignment, since a value like abc123def456
// only looks like a secret next to a key like password. Labels use safe keys or entry numbers.
func scanValue(label string, v any, reports *[]string) {
	switch v := v.(type) {
	case string:
		*reports = append(*reports, Reports(label, v, strings.Contains(v, "\n"))...)
	case []any:
		for i, e := range v {
			scanValue(fmt.Sprintf("%s[%d]", label, i), e, reports)
		}
	case map[string]any:
		for i, k := range slices.Sorted(maps.Keys(v)) {
			entry := SafeLabel(label+"."+k, fmt.Sprintf("%s entry %d", label, i+1))
			*reports = append(*reports, Reports(entry+" key", k, false)...)
			switch e := v[k].(type) {
			case string, json.Number, bool:
				*reports = append(*reports, Reports(entry, fmt.Sprintf("%q: \"%v\"", k, e), false)...)
			case []any:
				for j, item := range e {
					switch item.(type) {
					case string, json.Number, bool:
						*reports = append(*reports, Reports(fmt.Sprintf("%s[%d]", entry, j), fmt.Sprintf("%q: \"%v\"", k, item), false)...)
					}
				}
			}
			scanValue(entry, v[k], reports)
		}
	}
}

// StyleSecrets scans coding_style documents, labeled by their project path.
func StyleSecrets(docs map[string]string) error {
	var reports []string
	for _, path := range slices.Sorted(maps.Keys(docs)) {
		label := SafeLabel(path, "coding_style document")
		reports = append(reports, Reports(label+" path", path, false)...)
		reports = append(reports, Reports(label, docs[path], true)...)
	}
	if reports == nil {
		return nil
	}
	return Found(reports)
}
