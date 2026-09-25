package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

const judgeUsage = `Usage: jev-check judge OUTPUT.json THRESHOLD [QUESTION=THRESHOLD]...
OUTPUT.json is a file saved under output/. Exit 0 ok, 1 below threshold, 2 error.
`

func judgeCmd(args []string, stdout, _ io.Writer) (int, error) {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprint(stdout, judgeUsage)
		return 0, nil
	}
	if len(args) < 2 {
		return 0, errors.New("expected OUTPUT.json THRESHOLD (see --help)")
	}
	var saved struct {
		Request  request  `json:"request"`
		Response response `json:"response"`
	}
	if err := readJSON(args[0], &saved); err != nil {
		return 0, err
	}
	answers := saved.Response.Answers
	if err := validateAnswers(answers, saved.Request.Questions); err != nil {
		return 0, fmt.Errorf("%s: %w", args[0], err)
	}
	threshold, err := parseThreshold(args[1])
	if err != nil {
		return 0, err
	}
	perQuestion := map[string]float64{}
	for _, pair := range args[2:] {
		id, value, ok := strings.Cut(pair, "=")
		limit, err := parseThreshold(value)
		if !ok || id == "" || err != nil {
			return 0, fmt.Errorf("expected QUESTION=THRESHOLD from 0 to 1, got %s", pair)
		}
		if _, ok := answers[id]; !ok {
			return 0, fmt.Errorf("no question %s in %s", id, args[0])
		}
		perQuestion[id] = limit
	}
	if printVerdicts(stdout, answers, threshold, perQuestion) {
		return 1, nil
	}
	return 0, nil
}

func parseThreshold(s string) (float64, error) {
	t, err := strconv.ParseFloat(s, 64)
	if err != nil || !(t >= 0 && t <= 1) {
		return 0, fmt.Errorf("threshold must be a number from 0 to 1, got %s", s)
	}
	return t, nil
}

// printVerdicts prints one line per answer and reports whether a yes/no answer
// is below its threshold. perQuestion overrides threshold for one question.
// A negative threshold prints no status.
func printVerdicts(w io.Writer, answers map[string]answer, threshold float64, perQuestion map[string]float64) bool {
	ids := slices.Sorted(maps.Keys(answers))
	failed := false
	for _, id := range ids {
		a := answers[id]
		if a.Type != "noul" {
			continue
		}
		limit, ok := perQuestion[id]
		if !ok {
			limit = threshold
		}
		status := "    "
		if limit >= 0 && *a.Noul < limit {
			status, failed = "FAIL", true
		} else if limit >= 0 {
			status = "ok  "
		}
		fmt.Fprintf(w, "%s  %s  %s\n", status, formatFloat(*a.Noul), id)
	}
	// Choice and score answers explain a verdict and never fail it.
	for _, id := range ids {
		switch a := answers[id]; a.Type {
		case "noul":
		case "choice":
			fmt.Fprintf(w, "info  %s %s  %s\n", a.Choice, formatFloat(a.Probabilities[a.Choice]), id)
		case "score":
			fmt.Fprintf(w, "info  %v  %s\n", a.Score, id)
		default:
			fmt.Fprintf(w, "info  %s  %s\n", a.Type, id)
		}
	}
	return failed
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
