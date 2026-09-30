package jev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

type Request struct {
	Model     string                     `json:"model"`
	Questions map[string]json.RawMessage `json:"questions"`
	State     map[string]any             `json:"state"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
}

// Answer is a noul (the probability that the answer is yes), a choice, or a score.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Score         any                `json:"score"`
}

// NoulIDs returns the sorted ids of the yes/no (noul) questions.
func NoulIDs(questions map[string]json.RawMessage) []string {
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(questions)) {
		var q struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(questions[id], &q) == nil && q.Type == "noul" {
			ids = append(ids, id)
		}
	}
	return ids
}

// ValidateAnswers rejects incomplete or invalid answers before they can pass a check.
// Saved outputs without a request can still be checked for valid answer values.
func ValidateAnswers(answers map[string]Answer, questions map[string]json.RawMessage) error {
	if len(answers) == 0 {
		return errors.New("response needs a non-empty answers object")
	}
	for id, raw := range questions {
		var question struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &question); err != nil || question.Type == "" {
			return fmt.Errorf("question %s needs a type", id)
		}
		a, ok := answers[id]
		if !ok || a.Type != question.Type {
			return fmt.Errorf("question %s needs an answer of type %s", id, question.Type)
		}
	}
	for id, a := range answers {
		if questions != nil && questions[id] == nil {
			return fmt.Errorf("unexpected answer %s", id)
		}
		switch a.Type {
		case "noul":
			if a.Noul == nil || !(*a.Noul >= 0 && *a.Noul <= 1) {
				return fmt.Errorf("answer %s needs a noul number from 0 to 1", id)
			}
		case "choice":
			probability, ok := a.Probabilities[a.Choice]
			if !ok || !(probability >= 0 && probability <= 1) {
				return fmt.Errorf("answer %s needs a choice with a probability from 0 to 1", id)
			}
		case "score":
			if a.Score == nil {
				return fmt.Errorf("answer %s needs a score", id)
			}
		default:
			return fmt.Errorf("answer %s has unknown type %q", id, a.Type)
		}
	}
	return nil
}

// CallJev sends a request to cfg's endpoint and saves it, with the response, under project/.jev-check/output/.
// It returns the response and the absolute saved path.
func CallJev(project, name string, cfg Settings, req Request) (Response, string, error) {
	var res Response
	// Every path to the API passes here, so nothing that looks like a secret is sent.
	if err := secretscan.ScanRequest(req); err != nil {
		return res, "", err
	}
	if cfg.Key == "" {
		return res, "", cfg.MissingKey()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return res, "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return res, "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Key)
	httpReq.Header.Set("Content-Type", "application/json")
	// A redirect is returned as a 3xx error, so the key never follows it to another host.
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	httpRes, err := client.Do(httpReq)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	defer httpRes.Body.Close()
	raw, err := io.ReadAll(httpRes.Body)
	if err != nil {
		return res, "", fmt.Errorf("API call failed: %w", err)
	}
	if httpRes.StatusCode/100 != 2 {
		return res, "", fmt.Errorf("API call failed: %s: %s", httpRes.Status, bytes.TrimSpace(raw))
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Answers == nil {
		return res, "", fmt.Errorf("unexpected API response: %s", bytes.TrimSpace(raw))
	}

	if err := ValidateAnswers(res.Answers, req.Questions); err != nil {
		return res, "", fmt.Errorf("unexpected API response: %w", err)
	}

	dir := filepath.Join(project, workspace.JevDir, "output")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, "", err
	}
	// CreateTemp adds a random suffix, so two runs in the same second never overwrite each other.
	f, err := os.CreateTemp(dir, time.Now().Format("2006-01-02_15-04-05")+"-"+name+"-*.json")
	if err != nil {
		return res, "", err
	}
	defer f.Close()
	err = fsutil.WriteJSON(f, struct {
		Request  Request         `json:"request"`
		Response json.RawMessage `json:"response"`
	}{req, raw})
	if err == nil {
		err = f.Close()
	}
	return res, f.Name(), err
}
