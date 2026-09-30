package verdict

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"

	"github.com/luisferrassini/jev-check/internal/jev"
)

func ParseThreshold(s string) (float64, error) {
	t, err := strconv.ParseFloat(s, 64)
	if err != nil || !(t >= 0 && t <= 1) {
		return 0, fmt.Errorf("threshold must be a number from 0 to 1, got %s", s)
	}
	return t, nil
}

// PrintVerdicts prints one line per answer and reports whether a yes/no answer
// is below its threshold. perQuestion overrides threshold for one question.
// A negative threshold prints no status.
func PrintVerdicts(w io.Writer, answers map[string]jev.Answer, threshold float64, perQuestion map[string]float64) bool {
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
		fmt.Fprintf(w, "%s  %s  %s\n", status, FormatFloat(*a.Noul), id)
	}
	// Choice and score answers explain a verdict and never fail it.
	for _, id := range ids {
		switch a := answers[id]; a.Type {
		case "noul":
		case "choice":
			fmt.Fprintf(w, "info  %s %s  %s\n", a.Choice, FormatFloat(a.Probabilities[a.Choice]), id)
		case "score":
			fmt.Fprintf(w, "info  %v  %s\n", a.Score, id)
		default:
			fmt.Fprintf(w, "info  %s  %s\n", a.Type, id)
		}
	}
	return failed
}

func FormatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
