package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/jev"
	"github.com/luisferrassini/jev-check/internal/verdict"
)

const judgeUsage = `Usage: jev-check judge OUTPUT.json THRESHOLD [QUESTION=THRESHOLD]...
OUTPUT.json is a file saved under .jev-check/output/. Exit 0 ok, 1 below threshold, 2 error.
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
		Request  jev.Request  `json:"request"`
		Response jev.Response `json:"response"`
	}
	if err := fsutil.ReadJSON(args[0], &saved); err != nil {
		return 0, err
	}
	answers := saved.Response.Answers
	if err := jev.ValidateAnswers(answers, saved.Request.Questions); err != nil {
		return 0, fmt.Errorf("%s: %w", args[0], err)
	}
	threshold, err := verdict.ParseThreshold(args[1])
	if err != nil {
		return 0, err
	}
	perQuestion := map[string]float64{}
	for _, pair := range args[2:] {
		id, value, ok := strings.Cut(pair, "=")
		limit, err := verdict.ParseThreshold(value)
		if !ok || id == "" || err != nil {
			return 0, fmt.Errorf("expected QUESTION=THRESHOLD from 0 to 1, got %s", pair)
		}
		if _, ok := answers[id]; !ok {
			return 0, fmt.Errorf("no question %s in %s", id, args[0])
		}
		perQuestion[id] = limit
	}
	if verdict.Print(stdout, answers, threshold, perQuestion) {
		return 1, nil
	}
	return 0, nil
}
