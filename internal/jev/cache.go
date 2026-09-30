package jev

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/luisferrassini/jev-check/internal/fsutil"
	"github.com/luisferrassini/jev-check/internal/secretscan"
	"github.com/luisferrassini/jev-check/internal/workspace"
)

// cacheVersion names the cache key and entry format under output/cache/v2/.
// Entries in other folders, such as the unversioned ones in output/cache/, are never read.
const cacheVersion = 2

// cacheLifetime bounds reuse, because a model name such as jev-latest can change behind it.
// It is a policy, not proof that the model stayed the same.
const cacheLifetime = 24 * time.Hour

// cacheEntry is one file in output/cache/v2/.
type cacheEntry struct {
	Version   int      `json:"version"`
	CreatedAt string   `json:"created_at"`
	Response  Response `json:"response"`
}

// fresh reports whether an entry created at created can be reused at now.
// A future time counts as unknown age, so it is not reused.
func fresh(created, now time.Time) bool {
	age := now.Sub(created)
	return age >= 0 && age < cacheLifetime
}

// AskCached returns the answers from project/.jev-check/output/cache/v2/ when the same request went to the
// same endpoint less than cacheLifetime ago, else it calls Jev and caches the answers.
// The key is the whole request, so any change to the model, questions, or state misses.
// Thresholds are not in the request: the gate judges cached answers again on every run.
// noCache skips the lookup but still saves the new answers.
func AskCached(project, name string, cfg Settings, req Request, noCache bool, stderr io.Writer) (Response, bool, error) {
	// Scan before the cache, so an old answer never hides a secret.
	if err := secretscan.ScanRequest(req); err != nil {
		return Response{}, false, err
	}
	// json.Marshal sorts map keys, so the same request always gives the same key.
	key, err := json.Marshal(struct {
		Version  int     `json:"version"`
		Endpoint string  `json:"endpoint"`
		Request  Request `json:"request"`
	}{cacheVersion, cfg.Endpoint, req})
	if err != nil {
		return Response{}, false, err
	}
	sum := sha256.Sum256(key)
	path := filepath.Join(project, workspace.Dir, "output", "cache", "v2", hex.EncodeToString(sum[:])+".json")
	var entry cacheEntry
	if !noCache && fsutil.ReadJSON(path, &entry) == nil && entry.Version == cacheVersion {
		created, err := time.Parse(time.RFC3339, entry.CreatedAt)
		if err == nil && fresh(created, time.Now()) && ValidateAnswers(entry.Response.Answers, req.Questions) == nil {
			return entry.Response, true, nil
		}
	}
	res, _, err := Ask(project, name, cfg, req)
	if err != nil {
		return res, false, err
	}
	// A failed cache write only costs an API call next time, so it warns and keeps the answer.
	entry = cacheEntry{cacheVersion, time.Now().UTC().Format(time.RFC3339), res}
	if err := writeCache(path, entry); err != nil {
		// gate and eval both call here, so the warning names neither.
		fmt.Fprintf(stderr, "jev-check: cache not saved: %v\n", err)
	}
	return res, false, nil
}

// writeCache writes entry to a temporary file and renames it to path,
// so a reader never sees half an entry.
func writeCache(path string, entry cacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
