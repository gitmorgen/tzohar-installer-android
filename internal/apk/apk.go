// Package apk keeps a single cached copy of the Tzohar APK and re-downloads it
// only when it is out of date. "Out of date" is decided by the enrollment's
// published sha256 when we have one; otherwise by HTTP validators (ETag /
// Last-Modified) against the stable-alias URL.
package apk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type meta struct {
	Sha256       string `json:"sha256"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}

// Ensure returns a local path to an APK that matches expectedSha (when given),
// downloading from url only if the cache is stale. cacheDir must already exist.
func Ensure(cacheDir, url, expectedSha string, log func(string)) (string, error) {
	dst := filepath.Join(cacheDir, "tzohar-agent.apk")
	metaPath := dst + ".meta"

	var m meta
	if b, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(b, &m)
	}

	// Fast path: we know the published digest and the cached file matches it.
	if expectedSha != "" && fileExists(dst) {
		if got, _ := hashFile(dst); got == expectedSha {
			log("APK already current (verified by checksum) — using the cached copy.")
			return dst, nil
		}
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// With no published digest, lean on HTTP caching so we don't re-pull an
	// unchanged file on every run.
	if expectedSha == "" && fileExists(dst) {
		if m.ETag != "" {
			req.Header.Set("If-None-Match", m.ETag)
		}
		if m.LastModified != "" {
			req.Header.Set("If-Modified-Since", m.LastModified)
		}
	}

	log("Checking for the latest APK…")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading APK: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && fileExists(dst) {
		log("APK unchanged since last download — using the cached copy.")
		return dst, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("APK download failed: HTTP %d", resp.StatusCode)
	}

	log("Downloading the latest APK…")
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	out.Close()
	got := hex.EncodeToString(h.Sum(nil))
	if expectedSha != "" && got != expectedSha {
		os.Remove(tmp)
		return "", fmt.Errorf("APK integrity check failed — expected %s, got %s", expectedSha, got)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	nm := meta{Sha256: got, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	if b, err := json.Marshal(nm); err == nil {
		_ = os.WriteFile(metaPath, b, 0o644)
	}
	log("APK downloaded and verified.")
	return dst, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
