// Command tzohar-installer-android is a Windows helper that sideloads and sets
// up the Tzohar Android agent on a plugged-in phone over ADB, walking the
// technician through only the steps Android will not let software do for it
// (turning on USB debugging, accepting the debugging prompt).
//
// It runs as a tiny local web server and opens the wizard in the default
// browser — that keeps the UI pleasant without a GUI toolkit, and the whole
// thing cross-compiles to a single self-contained .exe from the Linux build
// box (this project has no Windows build path of its own).
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gitmorgen/tzohar-installer-android/internal/adb"
	"github.com/gitmorgen/tzohar-installer-android/internal/apk"
	"github.com/gitmorgen/tzohar-installer-android/internal/enroll"
)

//go:embed web
var webFS embed.FS

// The agent package and its accessibility service, as `adb` addresses them.
const (
	pkg        = "com.tzohar.agent"
	accService = pkg + "/" + pkg + ".ScreenReaderService"
)

const defaultAPIBase = "https://api.tzohar.tech"

// version is stamped by scripts/build.sh via -ldflags.
var version = "dev"

// step keys, in order.
var stepOrder = []string{"install", "configure", "enroll"}

type server struct {
	apiBase  string
	cacheDir string
	token    string

	mu         sync.Mutex
	adb        *adb.ADB
	adbReady   bool
	adbError   string
	device     *adb.Device
	enrollment *enroll.Enrollment
	steps      map[string]string // key -> pending|active|done|failed
	logs       []string
	busy       bool
	finished   bool
	runErr     string
}

func main() {
	apiBase := flag.String("api", defaultAPIBase, "backend base URL")
	noOpen := flag.Bool("no-open", false, "do not open the browser automatically")
	flag.Parse()

	cache := defaultCacheDir()
	if err := os.MkdirAll(cache, 0o755); err != nil {
		log.Fatalf("cannot create cache dir %s: %v", cache, err)
	}

	s := &server{
		apiBase:  strings.TrimRight(*apiBase, "/"),
		cacheDir: cache,
		token:    randToken(),
		steps:    map[string]string{},
	}
	for _, k := range stepOrder {
		s.steps[k] = "pending"
	}

	// Bind a random loopback port so two runs never collide.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("cannot listen on loopback: %v", err)
	}
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), s.token)

	go s.setupADB()   // download platform-tools if needed, then poll devices
	go s.pollDevices()

	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/state", s.guard(s.handleState))
	mux.HandleFunc("/api/enroll", s.guard(s.handleEnroll))
	mux.HandleFunc("/api/install", s.guard(s.handleInstall))

	fmt.Printf("Tzohar Android Installer %s\n", version)
	fmt.Println("Open this in your browser if it doesn't open by itself:")
	fmt.Println("  " + url)

	if !*noOpen {
		openBrowser(url)
	}
	log.Fatal(http.Serve(ln, mux))
}

// ---- HTTP layer ----------------------------------------------------------

// guard rejects any request not carrying our per-run token. The server is
// loopback-only, but other local processes (and, in theory, a browser page)
// can still reach 127.0.0.1, so the token gates every control endpoint.
func (s *server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("t")
		if t == "" {
			t = r.Header.Get("X-Token")
		}
		if t != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

type stateDTO struct {
	AdbReady   bool              `json:"adb_ready"`
	AdbError   string            `json:"adb_error"`
	Device     *adb.Device       `json:"device"`
	Enrollment *enroll.Enrollment `json:"enrollment"`
	Steps      map[string]string `json:"steps"`
	StepOrder  []string          `json:"step_order"`
	Log        string            `json:"log"`
	Busy       bool              `json:"busy"`
	Finished   bool              `json:"finished"`
	RunError   string            `json:"run_error"`
}

func (s *server) snapshot() stateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	steps := make(map[string]string, len(s.steps))
	for k, v := range s.steps {
		steps[k] = v
	}
	var dev *adb.Device
	if s.device != nil {
		d := *s.device
		dev = &d
	}
	var enr *enroll.Enrollment
	if s.enrollment != nil {
		e := *s.enrollment
		e.InstallKey = "" // never expose the credential to the page
		enr = &e
	}
	return stateDTO{
		AdbReady:   s.adbReady,
		AdbError:   s.adbError,
		Device:     dev,
		Enrollment: enr,
		Steps:      steps,
		StepOrder:  stepOrder,
		Log:        strings.Join(s.logs, "\n"),
		Busy:       s.busy,
		Finished:   s.finished,
		RunError:   s.runErr,
	}
}

func (s *server) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.snapshot())
}

func (s *server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	e, err := enroll.Resolve(body.Input, s.apiBase)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.enrollment = e
	s.mu.Unlock()
	ver := e.Artefact.Version
	if ver == "" {
		ver = "(latest published)"
	}
	s.appendLog(fmt.Sprintf("Enrollment loaded via %s. Agent version: %s.", e.Source, ver))
	writeJSON(w, s.snapshot())
}

func (s *server) handleInstall(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	switch {
	case s.busy:
		s.mu.Unlock()
		writeJSON(w, map[string]string{"error": "already running"})
		return
	case s.enrollment == nil:
		s.mu.Unlock()
		writeJSON(w, map[string]string{"error": "load an enrollment link or install key first"})
		return
	case s.device == nil || !s.device.Authorized():
		s.mu.Unlock()
		writeJSON(w, map[string]string{"error": "no authorized device — enable USB debugging and accept the prompt on the phone"})
		return
	}
	s.busy = true
	s.finished = false
	s.runErr = ""
	for _, k := range stepOrder {
		s.steps[k] = "pending"
	}
	s.mu.Unlock()

	go s.runInstall()
	writeJSON(w, s.snapshot())
}

// ---- Orchestration -------------------------------------------------------

func (s *server) setupADB() {
	path := adb.Locate(s.cacheDir)
	if path == "" {
		p, err := adb.Download(s.cacheDir, s.appendLog)
		if err != nil {
			s.mu.Lock()
			s.adbError = err.Error()
			s.mu.Unlock()
			return
		}
		path = p
	}
	a := &adb.ADB{Path: path}
	if err := a.StartServer(); err != nil {
		s.mu.Lock()
		s.adbError = "adb start-server failed: " + err.Error()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.adb = a
	s.adbReady = true
	s.mu.Unlock()
	s.appendLog("ADB ready. Plug in the phone and turn on USB debugging.")
}

// pollDevices refreshes the selected device every couple of seconds so the
// wizard can react to plug-in / authorization without the user clicking.
func (s *server) pollDevices() {
	for {
		time.Sleep(2 * time.Second)
		s.mu.Lock()
		a := s.adb
		busy := s.busy
		s.mu.Unlock()
		if a == nil || busy { // don't contend with an in-flight install
			continue
		}
		devs, err := a.Devices()
		if err != nil {
			continue
		}
		best := pickDevice(devs)
		s.mu.Lock()
		s.device = best
		s.mu.Unlock()
	}
}

// pickDevice prefers an authorized device, else the first thing plugged in, so
// the UI can say "accept the prompt on your phone" for an unauthorized one.
func pickDevice(devs []adb.Device) *adb.Device {
	if len(devs) == 0 {
		return nil
	}
	for i := range devs {
		if devs[i].Authorized() {
			return &devs[i]
		}
	}
	return &devs[0]
}

func (s *server) runInstall() {
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	s.mu.Lock()
	a := s.adb
	e := s.enrollment
	dev := *s.device
	s.mu.Unlock()

	fail := func(step string, err error) {
		s.setStep(step, "failed")
		s.mu.Lock()
		s.runErr = err.Error()
		s.mu.Unlock()
		s.appendLog("ERROR: " + err.Error())
	}

	// Step 1 — install.
	s.setStep("install", "active")
	apkPath, err := apk.Ensure(s.cacheDir, e.Artefact.URL, e.Artefact.Sha256, s.appendLog)
	if err != nil {
		fail("install", err)
		return
	}
	s.appendLog("Installing the app on " + describe(dev) + "…")
	if out, err := a.Install(dev.Serial, apkPath); err != nil {
		fail("install", fmt.Errorf("adb install failed: %s", firstLine(out, err)))
		return
	}
	s.setStep("install", "done")

	// Step 2 — configure grants that adb can set without a phone tap.
	s.setStep("configure", "active")
	if err := s.configure(a, dev.Serial); err != nil {
		fail("configure", err)
		return
	}
	s.setStep("configure", "done")

	// Step 3 — hand the install key to the app via its deep link. With the
	// on-device auto-start (0.1.15+) this both saves the key and starts the
	// agent; on older builds it prefills the key for a two-tap finish.
	s.setStep("enroll", "active")
	uri := fmt.Sprintf("tzohar://install?key=%s", sanitizeKey(e.InstallKey))
	line := fmt.Sprintf("am start -a android.intent.action.VIEW -d '%s'", uri)
	s.appendLog("Sending the install key to the app…")
	if out, err := a.ShellLine(dev.Serial, line); err != nil {
		fail("enroll", fmt.Errorf("launching the app failed: %s", firstLine(out, err)))
		return
	}
	s.setStep("enroll", "done")

	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	s.appendLog("Done. The device should appear on your Tzohar dashboard within a minute.")
}

// configure applies every grant ADB is allowed to make on a plugged-in device:
// enable the accessibility service (the reboot-safe capture path on Android
// 11+), exempt the app from battery optimization, and allow it to install its
// own updates. None of these need a tap on the phone.
func (s *server) configure(a *adb.ADB, serial string) error {
	// Accessibility: append our service to whatever is already enabled.
	cur, _ := a.Shell(serial, "settings", "get", "secure", "enabled_accessibility_services")
	cur = strings.TrimSpace(cur)
	if cur == "null" {
		cur = ""
	}
	if !containsService(cur, accService) {
		next := accService
		if cur != "" {
			next = cur + ":" + accService
		}
		if out, err := a.Shell(serial, "settings", "put", "secure", "enabled_accessibility_services", next); err != nil {
			return fmt.Errorf("enabling accessibility: %s", firstLine(out, err))
		}
	}
	if out, err := a.Shell(serial, "settings", "put", "secure", "accessibility_enabled", "1"); err != nil {
		return fmt.Errorf("enabling accessibility framework: %s", firstLine(out, err))
	}
	s.appendLog("Accessibility service enabled (screen text + reboot-safe screenshots).")

	// Battery: keep the agent alive in the background. Best-effort.
	if _, err := a.Shell(serial, "dumpsys", "deviceidle", "whitelist", "+"+pkg); err != nil {
		s.appendLog("Note: could not set battery exemption automatically; it can be granted in the app.")
	} else {
		s.appendLog("Battery optimization exemption granted.")
	}

	// Let the agent install its own updates later. Best-effort.
	if _, err := a.Shell(serial, "appops", "set", pkg, "REQUEST_INSTALL_PACKAGES", "allow"); err != nil {
		s.appendLog("Note: could not pre-grant app-update permission; the app will ask once.")
	} else {
		s.appendLog("App-update permission granted.")
	}
	return nil
}

// ---- helpers -------------------------------------------------------------

func (s *server) setStep(key, status string) {
	s.mu.Lock()
	s.steps[key] = status
	s.mu.Unlock()
}

func (s *server) appendLog(line string) {
	s.mu.Lock()
	s.logs = append(s.logs, time.Now().Format("15:04:05")+"  "+line)
	if len(s.logs) > 500 {
		s.logs = s.logs[len(s.logs)-500:]
	}
	s.mu.Unlock()
}

func containsService(list, svc string) bool {
	for _, p := range strings.Split(list, ":") {
		if strings.EqualFold(strings.TrimSpace(p), svc) {
			return true
		}
	}
	return false
}

// sanitizeKey strips anything that isn't a normal install-key character, so the
// value is safe to place inside the single-quoted device-shell command.
func sanitizeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func describe(d adb.Device) string {
	if d.Model != "" {
		return d.Model + " (" + d.Serial + ")"
	}
	return d.Serial
}

func firstLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out != "" {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			return out[:i]
		}
		return out
	}
	return err.Error()
}

func defaultCacheDir() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "Tzohar", "installer")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tzohar-installer")
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openBrowser(url string) {
	// rundll32 is the most reliable "open in default browser" on Windows.
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
