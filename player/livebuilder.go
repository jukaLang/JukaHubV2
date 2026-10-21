package main

// livebuilder.go — LAN receiver for the browser-based JukaHub GUI builder.
//
// The builder is a static page: it cannot write to the SD card, and copying
// jukaconfig.json across by hand defeats the point of designing on a laptop.
// This server gives the page two endpoints on the handheld:
//
//	GET  /api/health   what device this is (no code required)
//	GET  /api/design   the current design (pairing code required)
//	POST /api/design   replace the design and hot-reload it (code required)
//
// It is plain HTTP on the LAN, guarded by a short pairing code stored next to
// the config. That is deliberately not internet-grade security: the code keeps
// a shared Wi-Fi network from overwriting someone's layout by accident, while
// still being short enough to retype from a handheld screen.
//
// The pushed bytes are written back exactly as the builder produced them (after
// validation and a schemaVersion stamp), not re-marshalled through Config:
// fields this build does not know about must survive a push, or opening an
// older layout in the builder and pushing it would silently strip features.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	liveBuilderDefaultPort = 8422
	liveBuilderMaxBody     = 4 << 20 // 4 MiB: a full 33-scene config is ~120 KiB
	liveBuilderCodeLen     = 6
	liveBuilderCodeFile    = ".livebuilder-code"
	liveBuilderVersion     = "1"
)

// liveBuilderServer is the HTTP listener plus the last-push metadata the status
// page and health response report.
type liveBuilderServer struct {
	configPath string
	code       string
	onReload   func()

	mu         sync.Mutex
	ln         net.Listener
	srv        *http.Server
	port       int
	lastPush   time.Time
	lastScenes int
	lastElems  int
	pushCount  int
}

func newLiveBuilderServer(configPath, code string, onReload func()) *liveBuilderServer {
	if strings.TrimSpace(configPath) == "" {
		configPath = "jukaconfig.json"
	}
	return &liveBuilderServer{configPath: configPath, code: code, onReload: onReload}
}

// liveBuilderEnvEnabled reports whether the feature is on. It defaults to on:
// the server is local-only and code-guarded, and the whole point is that the
// browser can find the handheld without a rebuild. Opt out with
// JUKAHUB_LIVE_BUILDER=0 (or liveBuilderDisabled in the config Custom map).
func liveBuilderEnvEnabled() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_LIVE_BUILDER")))
	switch raw {
	case "0", "false", "off", "no", "disabled":
		return false
	}
	return true
}

func liveBuilderPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_LIVE_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return liveBuilderDefaultPort
}

func liveBuilderCodePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), liveBuilderCodeFile)
}

// loadOrCreateLiveBuilderCode reads the pairing code, creating a random one on
// first use. Six digits is short enough to copy from the device and long enough
// that guessing it on a LAN is not practical within the rate a person can type.
func loadOrCreateLiveBuilderCode(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		code := strings.TrimSpace(string(data))
		if len(code) >= 4 && len(code) <= 12 {
			return code, nil
		}
	}
	code, err := randomLiveBuilderCode()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func randomLiveBuilderCode() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *liveBuilderServer) Start() error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(liveBuilderPort()))
	if err != nil {
		// A second instance, or another app on the default port. The status page
		// and startup toast carry the real port.
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.ln, s.srv = ln, srv
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[livebuilder] server stopped: %v", err)
		}
	}()
	return nil
}

func (s *liveBuilderServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv, s.ln = nil, nil
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (s *liveBuilderServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func (s *liveBuilderServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// LANURL is what the user types into the builder on their laptop.
func (s *liveBuilderServer) LANURL() string {
	ip := localIPv4()
	if ip == "" {
		ip = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(ip, strconv.Itoa(s.Port())))
}

// ── HTTP ────────────────────────────────────────────────────────────────────

type liveBuilderHealth struct {
	OK            bool   `json:"ok"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	App           string `json:"app,omitempty"`
	Device        string `json:"device,omitempty"`
	Scenes        int    `json:"scenes"`
	Elements      int    `json:"elements"`
	CodeRequired  bool   `json:"codeRequired"`
	SchemaVersion int    `json:"schemaVersion"`
	LastPush      string `json:"lastPush,omitempty"`
	Pushes        int    `json:"pushes"`
	BuilderURL    string `json:"builderUrl,omitempty"`
}

type liveBuilderResponse struct {
	OK            bool     `json:"ok"`
	Scenes        int      `json:"scenes,omitempty"`
	Elements      int      `json:"elements,omitempty"`
	Bytes         int      `json:"bytes,omitempty"`
	Reloaded      bool     `json:"reloaded,omitempty"`
	SavedAt       string   `json:"savedAt,omitempty"`
	SchemaVersion int      `json:"schemaVersion,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	Error         string   `json:"error,omitempty"`
	Details       []string `json:"details,omitempty"`
}

func (s *liveBuilderServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.withCORS(s.handleStatus))
	mux.HandleFunc("/api/health", s.withCORS(s.handleHealth))
	mux.HandleFunc("/api/design", s.withCORS(s.handleDesign))
	return mux
}

// withCORS allows the builder, served from localhost or any static host, to call
// the device. The pairing code is a header rather than a cookie, so the wildcard
// origin cannot expose an ambient credential.
func (s *liveBuilderServer) withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Live-Builder-Code")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (s *liveBuilderServer) authorized(r *http.Request) bool {
	got := strings.TrimSpace(r.Header.Get("X-Live-Builder-Code"))
	if got == "" {
		got = strings.TrimSpace(r.URL.Query().Get("code"))
	}
	if got == "" || s.code == "" || len(got) != len(s.code) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.code)) == 1
}

func (s *liveBuilderServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLiveBuilderJSON(w, http.StatusMethodNotAllowed, liveBuilderResponse{Error: "use GET"})
		return
	}
	builderURL := s.LANURL()
	s.mu.Lock()
	lastPush := ""
	if !s.lastPush.IsZero() {
		lastPush = s.lastPush.Format(time.RFC3339)
	}
	health := liveBuilderHealth{
		OK:            true,
		Service:       "jukahub-live-builder",
		Version:       liveBuilderVersion,
		App:           liveBuilderAppName(),
		Device:        liveBuilderDeviceName(),
		Scenes:        s.lastScenes,
		Elements:      s.lastElems,
		CodeRequired:  true,
		SchemaVersion: ConfigSchemaVersion,
		LastPush:      lastPush,
		Pushes:        s.pushCount,
		BuilderURL:    builderURL,
	}
	s.mu.Unlock()
	writeLiveBuilderJSON(w, http.StatusOK, health)
}

func (s *liveBuilderServer) handleDesign(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeLiveBuilderJSON(w, http.StatusUnauthorized, liveBuilderResponse{
			Error: "pairing code is missing or wrong",
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleDesignPull(w)
	case http.MethodPost:
		s.handleDesignPush(w, r)
	default:
		writeLiveBuilderJSON(w, http.StatusMethodNotAllowed, liveBuilderResponse{Error: "use GET or POST"})
	}
}

func (s *liveBuilderServer) handleDesignPull(w http.ResponseWriter) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "cannot read the device config: " + err.Error()})
		return
	}
	var probe map[string]interface{}
	if err := json.Unmarshal(data, &probe); err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "the device config is not valid JSON: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *liveBuilderServer) handleDesignPush(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, liveBuilderMaxBody+1))
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusBadRequest, liveBuilderResponse{Error: "could not read the request body"})
		return
	}
	if len(body) > liveBuilderMaxBody {
		writeLiveBuilderJSON(w, http.StatusRequestEntityTooLarge, liveBuilderResponse{Error: "design is larger than 4 MiB"})
		return
	}

	// Validate with the same loader the player uses before touching the file on
	// the SD card. A bad push must leave the working design intact.
	cfg := &Config{}
	if err := json.Unmarshal(body, cfg); err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "this is not a JukaHub config",
			Details: []string{err.Error()},
		})
		return
	}
	if _, err := MigrateConfig(cfg); err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "config schema is not supported",
			Details: []string{err.Error()},
		})
		return
	}
	validator := NewConfigValidator()
	if err := validator.Validate(cfg); err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "the design did not pass the player's validation",
			Details: []string{err.Error()},
		})
		return
	}

	// Keep the browser's own field set, only stamping the schema version. Going
	// through Config would drop fields this build does not know about.
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "this is not a JSON object"})
		return
	}
	doc["schemaVersion"] = ConfigSchemaVersion
	stamped, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "could not serialise the design"})
		return
	}

	if err := backupLiveBuilderConfig(s.configPath); err != nil {
		// A missing/unreadable old file is fine on first push; a failed backup
		// with a readable old file is not, because a push would be unrecoverable.
		if !os.IsNotExist(err) {
			writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not back up the current config: " + err.Error()})
			return
		}
	}
	if err := AtomicWrite(s.configPath, stamped, 0o644); err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not save the design: " + err.Error()})
		return
	}

	// Make the change live now instead of on the watcher's next two-second poll.
	// The callback records the new mtime first, so the poller does not reload a
	// second time and double the "Config reloaded" toast.
	if s.onReload != nil {
		s.onReload()
	}

	warnings := validator.SecurityCheck(cfg)
	s.mu.Lock()
	s.lastPush = time.Now()
	s.lastScenes = len(cfg.Scenes)
	s.lastElems = countLiveBuilderElements(cfg)
	s.pushCount++
	scenes, elems := s.lastScenes, s.lastElems
	s.mu.Unlock()

	writeLiveBuilderJSON(w, http.StatusOK, liveBuilderResponse{
		OK:            true,
		Scenes:        scenes,
		Elements:      elems,
		Bytes:         len(stamped),
		Reloaded:      s.onReload != nil,
		SavedAt:       time.Now().Format(time.RFC3339),
		SchemaVersion: ConfigSchemaVersion,
		Warnings:      warnings,
	})
	log.Printf("[livebuilder] design pushed: %d scenes, %d elements, %d bytes", scenes, elems, len(stamped))
}

func countLiveBuilderElements(cfg *Config) int {
	total := 0
	for i := range cfg.Scenes {
		total += len(cfg.Scenes[i].Elements)
	}
	return total
}

// backupLiveBuilderConfig keeps the previous design as .bak, the same file the
// config health path already knows how to restore from.
func backupLiveBuilderConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return AtomicWrite(path+".bak", data, 0o644)
}

func writeLiveBuilderJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *liveBuilderServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, s.statusPageHTML(isLoopbackRequest(r)))
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *liveBuilderServer) statusPageHTML(showCode bool) string {
	lanURL := s.LANURL()
	s.mu.Lock()
	lastPush := "never"
	if !s.lastPush.IsZero() {
		lastPush = s.lastPush.Format("15:04:05")
	}
	pushes := s.pushCount
	scenes, elems := s.lastScenes, s.lastElems
	s.mu.Unlock()

	codeBlock := `<p class="hint">The pairing code is shown on the handheld when Live Builder starts. It is stored in <code>` +
		html.EscapeString(liveBuilderCodeFile) + `</code> next to the config.</p>`
	if showCode {
		codeBlock = `<p class="code-label">Pairing code</p><p class="code">` + html.EscapeString(s.code) + `</p>` +
			`<p class="hint">Type this into the builder once. On the handheld it is also logged at startup.</p>`
	}

	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>JukaHub Live Builder</title>
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(1200px 600px at 20% -10%,#1b2340,#080a10 60%);color:#eef2ff;font:16px/1.55 Inter,system-ui,sans-serif}
main{width:min(680px,92vw);padding:32px;border:1px solid #2b3550;border-radius:20px;background:#111624cc;box-shadow:0 30px 80px -30px #000}
h1{margin:0 0 4px;font-size:1.6rem}
.sub{color:#9aa7c7;margin:0 0 24px}
.row{display:flex;justify-content:space-between;gap:16px;padding:10px 0;border-bottom:1px solid #232c42}
.row:last-of-type{border-bottom:0}
.k{color:#9aa7c7}
.code-label{margin:18px 0 2px;color:#9aa7c7;font-size:.8rem;text-transform:uppercase;letter-spacing:.08em}
.code{font:700 2.4rem/1.1 ui-monospace,monospace;letter-spacing:.25em;margin:0 0 6px;color:#8b7bff}
.hint{color:#8d99b8;font-size:.88rem}
code{background:#1b2233;padding:2px 6px;border-radius:6px}
.ok{color:#3ddc97}
strong{font-weight:600}
</style></head><body><main>
<h1>JukaHub <span style="color:#8b7bff">Live Builder</span></h1>
<p class="sub">This handheld accepts designs from the browser builder over Wi-Fi.</p>
<div class="row"><span class="k">Address for the builder</span><strong>` + html.EscapeString(lanURL) + `</strong></div>
<div class="row"><span class="k">Server</span><span class="ok">running</span></div>
<div class="row"><span class="k">Last push</span><span>` + html.EscapeString(lastPush) + ` · ` + strconv.Itoa(pushes) + ` total</span></div>
<div class="row"><span class="k">Last design</span><span>` + strconv.Itoa(scenes) + ` scenes · ` + strconv.Itoa(elems) + ` elements</span></div>
` + codeBlock + `
<p class="hint">In the builder open <strong>Device</strong>, paste the address above, enter the pairing code, then press <strong>Push to device</strong>. The handheld validates the design, saves it atomically and reloads it in place.</p>
</main></body></html>`
}

// liveBuilderAppName / liveBuilderDeviceName are cheap metadata for the health
// response. They tolerate the early-startup nil config.
func liveBuilderAppName() string {
	if appConfig != nil && strings.TrimSpace(appConfig.AppName) != "" {
		return appConfig.AppName
	}
	return "JukaHub"
}

func liveBuilderDeviceName() string {
	return deviceDisplayName(DetectDevice())
}

// startLiveBuilderIfEnabled is called once from main after the config watcher
// exists, so a push can request an immediate reload.
func startLiveBuilderIfEnabled() *liveBuilderServer {
	if !liveBuilderEnvEnabled() {
		log.Printf("[livebuilder] disabled by JUKAHUB_LIVE_BUILDER")
		return nil
	}
	if appConfig != nil {
		if disabled, ok := appConfig.Variables.Custom["liveBuilderDisabled"].(bool); ok && disabled {
			log.Printf("[livebuilder] disabled in config (liveBuilderDisabled)")
			return nil
		}
	}
	code, err := loadOrCreateLiveBuilderCode(liveBuilderCodePath("jukaconfig.json"))
	if err != nil {
		log.Printf("[livebuilder] could not prepare pairing code: %v", err)
		return nil
	}
	srv := newLiveBuilderServer("jukaconfig.json", code, func() {
		if globalConfigWatcher != nil {
			globalConfigWatcher.NotifyWritten("jukaconfig.json", XMLSidecarPath("jukaconfig.json"))
		}
	})
	if err := srv.Start(); err != nil {
		log.Printf("[livebuilder] could not start: %v", err)
		return nil
	}
	log.Printf("[livebuilder] listening on %s (pairing code %s)", srv.LANURL(), code)
	showToast("Live builder: "+srv.LANURL()+" · code "+code, ColorToastInfo)
	return srv
}
