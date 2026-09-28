// Package enroll turns a pasted enrollment link (or raw install key) into the
// key + APK artefact the installer needs. The backend already exposes exactly
// this at GET /install/:id (a public capability URL keyed by the enrollment id,
// never the 16-digit key), so there is nothing new to build server-side.
package enroll

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Artefact mirrors the `artefact` object in the /install/:id response.
type Artefact struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Sha256   string `json:"sha256"`
	Version  string `json:"version"`
}

// Enrollment is the subset of the /install/:id payload the installer uses.
type Enrollment struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	InstallKey string    `json:"install_key"`
	OS         string    `json:"os"`
	Artefact   *Artefact `json:"artefact"`
	// Source records how the enrollment was resolved, for the UI.
	Source string `json:"source,omitempty"`
}

// enr_ ids are "enr_" + 96 bits of hex (24 chars); see enrollmentStore.mint.
var enrIDRe = regexp.MustCompile(`(?i)enr_[a-f0-9]{24}`)

// rawKeyRe is a lenient shape for a hand-typed install key (letters/digits,
// optionally dash-grouped). Only used when no enr_ id is present.
var rawKeyRe = regexp.MustCompile(`^[A-Za-z0-9-]{6,40}$`)

// Resolve accepts an enrollment URL, a bare enr_ id, or a raw install key and
// returns an Enrollment. apiBase is the backend origin, e.g.
// "https://api.tzohar.tech".
func Resolve(input, apiBase string) (*Enrollment, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("paste an enrollment link or install key")
	}
	if id := enrIDRe.FindString(input); id != "" {
		return fetch(apiBase, id)
	}
	// Fallback: a raw install key with no enrollment id. We can still install —
	// the newest APK is at the public stable alias — but we have no published
	// sha256 to check against, so the APK layer falls back to HTTP validators.
	key := strings.TrimSpace(input)
	if !rawKeyRe.MatchString(key) {
		return nil, fmt.Errorf("that doesn't look like an enrollment link or install key")
	}
	return &Enrollment{
		Status:     "active",
		InstallKey: key,
		OS:         "android",
		Source:     "key",
		Artefact: &Artefact{
			URL:      strings.TrimRight(apiBase, "/") + "/downloads/tzohar-agent.apk",
			Filename: "tzohar-agent.apk",
		},
	}, nil
}

func fetch(apiBase, id string) (*Enrollment, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	url := strings.TrimRight(apiBase, "/") + "/install/" + id
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", apiBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("enrollment not found — it may be revoked, a test enrollment, or the link is wrong")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrollment lookup failed: HTTP %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var e Enrollment
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("unexpected enrollment response: %w", err)
	}
	if e.OS != "" && e.OS != "android" {
		return nil, fmt.Errorf("this is a %q enrollment, not Android — create an Android enrollment on the dashboard", e.OS)
	}
	if e.InstallKey == "" {
		return nil, fmt.Errorf("this enrollment has no live install key (a test enrollment can't be installed)")
	}
	if e.Artefact == nil || e.Artefact.URL == "" {
		return nil, fmt.Errorf("no Android APK has been published yet")
	}
	e.Source = "enrollment"
	return &e, nil
}
