// Package adb locates (or downloads) Google's platform-tools and drives adb
// against a plugged-in device. It shells out to adb.exe rather than speaking
// the wire protocol: the whole point of the installer is to run the same
// commands a technician would type, so the surface stays auditable.
package adb

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Where Google publishes the current Windows platform-tools. We fetch this at
// runtime rather than bundling it: redistributing the Android SDK tools carries
// licensing weight, and a fresh download is always current.
const platformToolsURL = "https://dl.google.com/android/repository/platform-tools-latest-windows.zip"

// ADB wraps a path to an adb executable.
type ADB struct {
	Path string
}

// Device is one parsed line of `adb devices -l`.
type Device struct {
	Serial string `json:"serial"`
	State  string `json:"state"` // "device" (authorized), "unauthorized", "offline", ...
	Model  string `json:"model"`
}

// Authorized reports whether the device is ready to receive commands.
func (d Device) Authorized() bool { return d.State == "device" }

// Locate finds a usable adb without downloading: the installer's own cache
// first, then PATH, then the usual SDK spots. Returns "" if none is present.
func Locate(cacheDir string) string {
	cand := filepath.Join(cacheDir, "platform-tools", "adb.exe")
	if fileExists(cand) {
		return cand
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, "AppData", "Local", "Android", "Sdk", "platform-tools", "adb.exe"),
		`C:\Android\platform-tools\adb.exe`,
	} {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// Download fetches platform-tools into cacheDir and returns the adb.exe path.
// The zip lays itself out as platform-tools/adb.exe (+ the AdbWinApi DLLs it
// needs), so we extract the whole archive and keep the files together.
func Download(cacheDir string, log func(string)) (string, error) {
	log("Downloading Android platform-tools from Google (one-time setup)…")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(platformToolsURL)
	if err != nil {
		return "", fmt.Errorf("downloading platform-tools: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("platform-tools download failed: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading platform-tools: %w", err)
	}
	log(fmt.Sprintf("Extracting platform-tools (%d KB)…", len(data)/1024))
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening platform-tools zip: %w", err)
	}
	root := filepath.Clean(cacheDir) + string(os.PathSeparator)
	for _, f := range zr.File {
		dest := filepath.Join(cacheDir, filepath.Clean(f.Name))
		if !strings.HasPrefix(dest, root) { // zip-slip guard
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dest, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return "", err
		}
	}
	adbPath := filepath.Join(cacheDir, "platform-tools", "adb.exe")
	if !fileExists(adbPath) {
		return "", fmt.Errorf("adb.exe not found after extracting platform-tools")
	}
	return adbPath, nil
}

func (a *ADB) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, a.Path, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// Run executes an adb command with a default 60s timeout.
func (a *ADB) Run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return a.run(ctx, args...)
}

// StartServer boots the adb server (harmless if already running).
func (a *ADB) StartServer() error {
	_, err := a.Run("start-server")
	return err
}

// Devices parses `adb devices -l`.
func (a *ADB) Devices() ([]Device, error) {
	out, err := a.Run("devices", "-l")
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	var ds []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := Device{Serial: fields[0], State: fields[1]}
		for _, f := range fields[2:] {
			if strings.HasPrefix(f, "model:") {
				d.Model = strings.ReplaceAll(strings.TrimPrefix(f, "model:"), "_", " ")
			}
		}
		ds = append(ds, d)
	}
	return ds, nil
}

// Install runs `adb install -r -g` (reinstall keeping data, grant all runtime
// permissions). Installing via adb's PackageInstaller session also keeps the
// app off Android 13+'s "restricted settings" list, so the accessibility
// toggle we set later is not greyed out.
func (a *ADB) Install(serial, apkPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return a.run(ctx, "-s", serial, "install", "-r", "-g", apkPath)
}

// Shell runs `adb -s SERIAL shell ARGS...` with the args passed literally
// (no device-side shell parsing beyond adb's own reconstruction). Use this for
// commands whose arguments contain no shell metacharacters.
func (a *ADB) Shell(serial string, args ...string) (string, error) {
	return a.Run(append([]string{"-s", serial, "shell"}, args...)...)
}

// ShellLine runs a single command string on the device shell. Use this when an
// argument must be quoted for the device's /system/bin/sh — e.g. a URI whose
// '?' would otherwise be treated as a glob.
func (a *ADB) ShellLine(serial, line string) (string, error) {
	return a.Run("-s", serial, "shell", line)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
