package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
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

func secretsCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(stdout, "Usage: jev-check secrets PATCH...   exit 0 clean, 1 looks like a secret, 2 error")
		return 0, nil
	}
	if len(args) == 0 {
		return 0, errors.New("expected at least one PATCH")
	}
	found := 0
	for _, path := range args {
		patch, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		for _, report := range scanSecrets(path, string(patch)) {
			fmt.Fprintln(stdout, report)
			found = 1
		}
	}
	return found, nil
}

// scanSecrets returns one line per kind of secret in a patch, including removed and context lines.
// It names the patch line numbers, never the value.
func scanSecrets(name, patch string) []string {
	var reports []string
	lines := strings.Split(patch, "\n")
	for _, p := range secretPatterns {
		var hits []string
		for i, line := range lines {
			// Strip one diff marker so anchored patterns also match removed lines.
			if len(line) > 0 && strings.ContainsRune("+- ", rune(line[0])) {
				line = line[1:]
			}
			if p.re.MatchString(line) {
				hits = append(hits, strconv.Itoa(i+1))
			}
		}
		if hits != nil {
			reports = append(reports, fmt.Sprintf("SECRET  %s line %s looks like %s", name, strings.Join(hits, ","), p.kind))
		}
	}
	return reports
}
