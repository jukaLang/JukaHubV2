package main

// vpn.go — Settings → Network → VPN: a WireGuard tunnel that the device's
// traffic goes through while the switch is on and a configuration is set.
//
// The pieces:
//
//   - the profile, a wg-quick .conf (wg_config.go) imported from a file on the
//     card (Settings or Files) or sent from the phone companion's VPN card. It
//     lives in data/vpn.json — not in jukauser.json, which Settings → Save can
//     put into the config the live builder serves over Wi-Fi, and not in
//     backups (bkup.go leaves it out) — and its private key is never shown,
//     logged or sent back;
//   - the switch ("vpnEnabled") and what goes through the tunnel ("vpnRoute":
//     everything, the default, or only the networks the configuration lists),
//     saved like every other setting;
//   - a worker goroutine that makes the system match them through the
//     platform: on Linux — the handheld — JukaHub's own WireGuard in a tunnel
//     process of its own (vpn_linux.go, vpnd_linux.go), brought back on its
//     own if it stops; on Windows the tunnel service of WireGuard for Windows,
//     installed and removed only when the user asks (each needs the Windows
//     permission prompt) (vpn_windows.go);
//   - what the user sees: the Settings rows, a Quick Settings switch, a lock
//     beside the Wi-Fi meter (the meter's colour when the tunnel is up, the
//     warning colour while traffic is not going where the switch says), and
//     the log lines the tunnel writes.
//
// Safe mode keeps the handheld's tunnel down for that start without touching
// the switch. (On Windows the tunnel is WireGuard's service, which safe mode
// does not start or stop.)

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	vpnEnabledKey  = "vpnEnabled"
	vpnRouteKey    = "vpnRoute"
	vpnRouteConfig = "config" // only the configuration's own AllowedIPs
	vpnProfileFile = "vpn.json"
	// vpnDaemonVersion is part of every payload, so a tunnel process an older
	// build started is replaced rather than adopted after an update.
	vpnDaemonVersion = 1
)

// What the tunnel is doing, as the status line says it.
const (
	vpnStateOff        = ""
	vpnStateStarting   = "starting"
	vpnStateConnecting = "connecting"
	vpnStateConnected  = "connected"
	vpnStateActive     = "active" // running, handshakes not visible (Windows)
	vpnStateNoAnswer   = "no-answer"
	vpnStateError      = "error"
)

// vpnProfile is the saved configuration.
type vpnProfile struct {
	Name   string `json:"name"`
	Config string `json:"config"`
	Source string `json:"source,omitempty"`
}

// vpnPayload is what the platform is asked to run.
type vpnPayload struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Config   string `json:"config"`
	RouteAll bool   `json:"routeAll"`
}

// vpnPayloadHash names a payload, so the app can tell whether the tunnel that
// runs is the one the settings describe.
func vpnPayloadHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// vpnEvent is one line the tunnel wrote.
type vpnEvent struct {
	Seq  uint64
	Text string
	Warn bool
}

// vpnProbe is what the platform reports about the tunnel.
type vpnProbe struct {
	Running bool
	// Installed: there is a tunnel to stop even though it does not run
	// (WireGuard's service, stopped but still set to start with Windows).
	Installed bool
	// Instance tells one tunnel process from the next (its pid), so each
	// one's lines are logged once.
	Instance  string
	Hash      string // the payload it runs; "" when the platform cannot tell
	State     string
	Detail    string // what State is about: an error, what it waits for
	Warning   string // what the user should know while it is up
	Fatal     bool   // retrying will not help until the profile changes
	Endpoint  string
	Addresses []string
	// Handshake is the time since the last handshake, when HasHandshake.
	Handshake    time.Duration
	HasHandshake bool
	Rx, Tx       uint64
	DNS          bool
	Events       []vpnEvent
}

// vpnMgr is the VPN's state. The main thread writes the switch; the worker
// writes the rest; both under mu.
var vpnMgr struct {
	mu      sync.Mutex
	started bool
	wake    chan struct{}
	dataDir string

	enabled, routeAll, safe bool
	// explicit is a user action the worker has not acted on yet: Windows only
	// ever changes the tunnel then, and a profile that failed for good is
	// tried again.
	explicit bool

	loaded       bool
	profile      *vpnProfile
	parsed       *wgConfig
	hash         string // the payload the settings describe
	probe        vpnProbe
	wasRunning   bool
	seenInstance string
	seenSeq      uint64
	failures     int
	retryAt      time.Time
	fatalHash    string
	applying     time.Time // when a change was asked for that has not shown yet
	stopping     time.Time // when a stop was asked for that has not finished
	recovered    string    // the dead tunnel whose leftovers were put back
	removeReq    bool      // Remove was chosen: stop, then delete the profile
	lastErr      string
}

// vpnSaveMu keeps two imports (phone and Files) from writing the profile at
// once.
var vpnSaveMu sync.Mutex

// ── Settings values ──────────────────────────────────────────────────────

func vpnEnabledIn(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, vpnEnabledKey, false)
}

func vpnRouteAllIn(c *Config) bool {
	return c == nil || !strings.EqualFold(strings.TrimSpace(customStringValue(c.Variables.Custom, vpnRouteKey)), vpnRouteConfig)
}

// vpnStartup publishes the saved switch and starts the worker. A deferred
// startup task: it touches no disk on the main thread beyond the data folder.
func vpnStartup(config *Config) {
	dir, err := P().DataDir()
	if err != nil {
		logWarn("vpn", "no data folder for the VPN profile: %v", err)
	}
	vpnMgr.mu.Lock()
	vpnMgr.dataDir = dir
	vpnMgr.mu.Unlock()
	vpnPublish(config, false)
	vpnEnsureWorker()
}

// vpnPublish hands the switch to the worker. explicit marks a user action.
func vpnPublish(c *Config, explicit bool) {
	on, all := vpnEnabledIn(c), vpnRouteAllIn(c)
	safe := !safeModeAllows(SafeFeatureNetwork)
	vpnMgr.mu.Lock()
	vpnMgr.enabled, vpnMgr.routeAll, vpnMgr.safe = on, all, safe
	if explicit {
		vpnMgr.explicit = true
	}
	vpnMgr.mu.Unlock()
	vpnWake()
}

func vpnEnsureWorker() {
	vpnMgr.mu.Lock()
	if vpnMgr.started {
		vpnMgr.mu.Unlock()
		return
	}
	vpnMgr.started = true
	vpnMgr.wake = make(chan struct{}, 1)
	vpnMgr.mu.Unlock()
	go vpnWorker()
}

func vpnWake() {
	vpnMgr.mu.Lock()
	ch := vpnMgr.wake
	vpnMgr.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ── The profile on disk ──────────────────────────────────────────────────

func vpnProfilePath() (string, error) {
	vpnMgr.mu.Lock()
	dir := vpnMgr.dataDir
	vpnMgr.mu.Unlock()
	if dir == "" {
		var err error
		if dir, err = P().DataDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, vpnProfileFile), nil
}

func vpnLoadProfile() {
	vpnSaveMu.Lock()
	defer vpnSaveMu.Unlock()
	var prof *vpnProfile
	var parsed *wgConfig
	if path, err := vpnProfilePath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			var p vpnProfile
			if err := json.Unmarshal(data, &p); err != nil {
				logWarn("vpn", "the saved VPN profile is unreadable: %v", err)
			} else if c, err := wgParseConfig(p.Config); err != nil {
				logWarn("vpn", "the saved VPN profile no longer parses: %v", err)
			} else {
				prof, parsed = &p, c
			}
		}
	}
	vpnMgr.mu.Lock()
	vpnMgr.profile, vpnMgr.parsed, vpnMgr.loaded = prof, parsed, true
	vpnMgr.mu.Unlock()
}

// vpnWriteDurable writes a file through a temporary one that is flushed to
// the card before it replaces the old one, so a power cut leaves either.
// (The mode is set at creation only: FAT refuses chmod.)
func vpnWriteDurable(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

// vpnBackupExcluded reports whether a data-folder file is the VPN's own: the
// configuration (its private key) and the DNS settings the tunnel keeps aside
// stay on their device, out of backups and restores (bkup.go).
func vpnBackupExcluded(name string) bool {
	name = strings.ToLower(filepath.Base(filepath.FromSlash(name)))
	return name == vpnProfileFile || name == vpnProfileFile+".tmp" || name == "vpn-resolv.json"
}

// vpnCleanName is a short display name: the file's name, or the server.
func vpnCleanName(name string, c *wgConfig) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name))
	if name == "" && c != nil {
		name = c.EndpointHost
	}
	if r := []rune(name); len(r) > 32 {
		name = string(r[:32])
	}
	return name
}

// vpnImport makes text the VPN's configuration. Safe from any goroutine: it
// touches the disk and the worker's state, never the config.
func vpnImport(name, text, source string) (string, error) {
	text = strings.TrimSpace(text)
	c, err := wgParseConfig(text)
	if err != nil {
		return "", err
	}
	p := &vpnProfile{Name: vpnCleanName(name, c), Config: text + "\n", Source: source}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	path, err := vpnProfilePath()
	if err != nil {
		return "", err
	}
	vpnSaveMu.Lock()
	defer vpnSaveMu.Unlock()
	if err := vpnWriteDurable(path, data); err != nil {
		return "", fmt.Errorf("cannot save it: %v", err)
	}
	vpnMgr.mu.Lock()
	vpnMgr.profile, vpnMgr.parsed, vpnMgr.loaded = p, c, true
	vpnMgr.explicit = true
	vpnMgr.mu.Unlock()
	logInfo("vpn", "WireGuard configuration %q imported (%s); server %s", p.Name, source, c.Endpoint)
	vpnWake()
	return p.Name, nil
}

// vpnReadConfigFile reads a file that may be a configuration: a regular file
// (never a pipe, which would block), bounded.
func vpnReadConfigFile(path string) ([]byte, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, wgConfigMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > wgConfigMaxBytes {
		return nil, errors.New("that file is too big to be a WireGuard configuration")
	}
	return data, nil
}

// vpnImportFile imports a file and says how it went (from a goroutine).
func vpnImportFile(path string) {
	data, err := vpnReadConfigFile(path)
	name := ""
	if err == nil {
		name, err = vpnImport(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), string(data), "file")
	}
	tbxPost(func() { vpnImportToast(name, err) })
}

func vpnImportToast(name string, err error) {
	if err != nil {
		ShowToast("Not a usable WireGuard configuration: "+err.Error(), ToastKindError)
		return
	}
	if vpnEnabledIn(appConfig) {
		if vpnPlatformAuto() {
			ShowToast("VPN configuration "+name+" saved; reconnecting with it", ToastKindSuccess)
		} else {
			ShowToast("VPN configuration "+name+" saved; allow JukaHub in the Windows prompt to use it", ToastKindSuccess)
		}
		return
	}
	ShowToast("VPN configuration "+name+" saved; switch the VPN on in Settings > Network", ToastKindSuccess)
}

// ── The worker ───────────────────────────────────────────────────────────

func vpnWorker() {
	vpnLoadProfile()
	vpnPlatformRecover()
	for {
		wait := vpnReconcile()
		vpnMgr.mu.Lock()
		ch := vpnMgr.wake
		vpnMgr.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(wait):
		}
	}
}

// vpnReconcile makes the tunnel match the settings once and says when to look
// again.
func vpnReconcile() time.Duration {
	probe := vpnPlatformProbe()
	auto := vpnPlatformAuto()
	now := time.Now()
	present := probe.Running || probe.Installed

	vpnMgr.mu.Lock()
	m := &vpnMgr
	// Safe mode holds the handheld's own tunnel; WireGuard's service on
	// Windows is not JukaHub's to start or stop on its own.
	want := m.enabled && (!m.safe || !auto) && m.parsed != nil
	explicit := m.explicit
	m.explicit = false
	remove := m.removeReq
	m.removeReq = false
	var payload []byte
	if m.profile != nil {
		payload, _ = json.Marshal(vpnPayload{Version: vpnDaemonVersion, Name: m.profile.Name,
			Config: m.profile.Config, RouteAll: m.routeAll})
	}
	hash := ""
	if payload != nil {
		hash = vpnPayloadHash(payload)
	}
	m.hash = hash
	if probe.Instance != m.seenInstance {
		m.seenInstance, m.seenSeq = probe.Instance, 0
	}
	var fresh []vpnEvent
	for _, e := range probe.Events {
		if e.Seq > m.seenSeq {
			fresh = append(fresh, e)
			m.seenSeq = e.Seq
		}
	}
	if probe.State == vpnStateConnected || probe.State == vpnStateActive {
		m.failures = 0
		if probe.Hash == hash && m.enabled {
			m.lastErr = ""
		}
	}
	if probe.Fatal && probe.Hash != "" {
		m.fatalHash = probe.Hash
	}
	if explicit {
		m.failures, m.retryAt, m.fatalHash = 0, time.Time{}, ""
	}
	if probe.Running || probe.State == vpnStateError || now.Sub(m.applying) > time.Minute {
		m.applying = time.Time{}
	}
	// A tunnel that died — seen running before, or found dead with an error —
	// gets what it could not put back put back, once.
	died := (m.wasRunning && !probe.Running) ||
		(!probe.Running && probe.State == vpnStateError && probe.Instance != "" && probe.Instance != m.recovered)
	if died {
		m.recovered = probe.Instance
	}
	m.wasRunning = probe.Running
	due := explicit || (auto && !now.Before(m.retryAt))
	fatal := m.fatalHash
	m.probe = probe
	m.mu.Unlock()

	for _, e := range fresh {
		if e.Warn {
			logWarn("vpn", "%s", e.Text)
		} else {
			logInfo("vpn", "%s", e.Text)
		}
	}
	if died {
		// Linux: the DNS settings and routes a killed tunnel left behind.
		vpnPlatformRecover()
	}
	if remove {
		vpnFinishRemove(present)
		return time.Second
	}

	switch {
	case want && probe.Running && probe.Hash == hash:
		// Running what the settings say.
	case want && due && (probe.Running || hash != fatal):
		vpnApply(payload)
	case !want && present && (auto || explicit):
		_ = vpnStop()
	}
	if want || present {
		return time.Second
	}
	return 10 * time.Second
}

// vpnApply asks the platform to run payload, backing off after failures.
func vpnApply(payload []byte) {
	vpnMgr.mu.Lock()
	vpnMgr.applying = time.Now()
	vpnMgr.lastErr = ""
	vpnMgr.mu.Unlock()
	err := vpnPlatformApply(payload)
	vpnMgr.mu.Lock()
	vpnMgr.failures++
	backoff := 5 * time.Second
	for i := 1; i < vpnMgr.failures && backoff < time.Minute; i++ {
		backoff *= 2
	}
	vpnMgr.retryAt = time.Now().Add(backoff)
	vpnMgr.lastErr = ""
	if err != nil {
		vpnMgr.lastErr = err.Error()
		vpnMgr.applying = time.Time{}
	}
	vpnMgr.mu.Unlock()
	if err != nil {
		logWarn("vpn", "cannot start the VPN: %v", err)
	}
}

// vpnStop asks the platform to stop the tunnel and keeps a refusal visible.
func vpnStop() error {
	vpnMgr.mu.Lock()
	vpnMgr.stopping = time.Now()
	vpnMgr.lastErr = ""
	vpnMgr.mu.Unlock()
	err := vpnPlatformStop()
	vpnMgr.mu.Lock()
	vpnMgr.stopping = time.Time{}
	if err != nil {
		vpnMgr.lastErr = err.Error()
	}
	vpnMgr.mu.Unlock()
	if err != nil {
		logWarn("vpn", "cannot stop the VPN: %v", err)
	}
	return err
}

// ── What the screens show ────────────────────────────────────────────────

// vpnView is a snapshot for the screens.
type vpnView struct {
	Loaded, HasProfile, Safe, Applying, Stopping bool
	Name                                         string
	Hash                                         string // the payload the settings describe
	Config                                       *wgConfig
	Probe                                        vpnProbe
	LastErr                                      string
}

func vpnSnapshot() vpnView {
	vpnMgr.mu.Lock()
	defer vpnMgr.mu.Unlock()
	v := vpnView{
		Loaded:     vpnMgr.loaded,
		HasProfile: vpnMgr.parsed != nil,
		Safe:       vpnMgr.safe,
		Applying:   !vpnMgr.applying.IsZero(),
		Stopping:   !vpnMgr.stopping.IsZero(),
		Hash:       vpnMgr.hash,
		Config:     vpnMgr.parsed,
		Probe:      vpnMgr.probe,
		LastErr:    vpnMgr.lastErr,
	}
	if vpnMgr.profile != nil {
		v.Name = vpnMgr.profile.Name
	}
	return v
}

// vpnStatusLine is the switch's second line while it is on.
func vpnStatusLine(v vpnView, all bool) string {
	p := v.Probe
	route := "everything goes through it"
	if !all {
		route = "the configuration's networks go through it"
	}
	if v.Applying && !vpnPlatformAuto() {
		return "Changing: allow JukaHub in the Windows prompt if it asks"
	}
	// Windows only changes the tunnel when asked; a change the prompt turned
	// down leaves the earlier setup running, and the line says so.
	if p.Running && p.Hash != "" && v.Hash != "" && p.Hash != v.Hash && !vpnPlatformAuto() {
		return "Running the earlier setup: " + setOr(v.LastErr, "switch the VPN off and on to use the new one")
	}
	switch p.State {
	case vpnStateConnected:
		if p.Warning != "" {
			return "Connected · " + p.Warning
		}
		return "Connected · " + route
	case vpnStateActive:
		return "On · " + setOr(p.Detail, route)
	case vpnStateNoAnswer:
		return "The server is not answering: check the configuration and the network"
	case vpnStateError:
		return "Not connected: " + setOr(p.Detail, "the tunnel stopped")
	case vpnStateStarting, vpnStateConnecting:
		if p.Detail != "" {
			return "Connecting · " + p.Detail
		}
		if v.Config != nil {
			return "Connecting to " + v.Config.EndpointHost + "…"
		}
		return "Connecting…"
	}
	switch {
	case v.LastErr != "":
		return "Not connected: " + v.LastErr
	case runtime.GOOS == "windows":
		return "Not running: switch it off and on again to start it"
	}
	return "Starting…"
}

// vpnDetailFor is the switch's second line (Settings, the phone).
func vpnDetailFor(v vpnView, enabled, all bool) string {
	ok, why := vpnPlatformSupport()
	switch {
	case !v.Loaded:
		return "Checking…"
	case !enabled && v.Stopping && !vpnPlatformAuto():
		return "Stopping: allow JukaHub in the Windows prompt if it asks"
	case !enabled && (v.Probe.Running || v.Probe.Installed) && !vpnPlatformAuto():
		return "Still set up in WireGuard: " + setOr(v.LastErr, "VPN configuration removes it")
	case !ok:
		return why
	case !v.HasProfile:
		return "Add a WireGuard configuration below first"
	case !enabled:
		return "Off: traffic goes straight out over Wi-Fi"
	case v.Safe && vpnPlatformAuto():
		return "Safe mode: the VPN stays off for this start"
	}
	return vpnStatusLine(v, all)
}

func vpnSettingOn(c *Config) bool {
	return vpnEnabledIn(c) && vpnSnapshot().HasProfile
}

// vpnSettingDisabled greys the switch out while it cannot be switched on; a
// switch that is on can always be switched off.
func vpnSettingDisabled(c *Config) bool {
	v := vpnSnapshot()
	if !v.Loaded || vpnEnabledIn(c) {
		return false
	}
	ok, _ := vpnPlatformSupport()
	return !v.HasProfile || !ok
}

func vpnSettingDetail(c *Config) string {
	return vpnDetailFor(vpnSnapshot(), vpnEnabledIn(c), vpnRouteAllIn(c))
}

// vpnSettingSet is the switch (Settings, Quick Settings).
func vpnSettingSet(c *Config, on bool) {
	if c == nil {
		return
	}
	v := vpnSnapshot()
	if on {
		if !v.Loaded {
			setToast("Reading the VPN configuration; try again in a moment")
			return
		}
		if ok, why := vpnPlatformSupport(); !ok {
			setToast(why)
			return
		}
		if !v.HasProfile {
			setToast("Add a WireGuard configuration first")
			return
		}
	}
	setStoreBool(c, vpnEnabledKey, on)
	vpnPublish(c, true)
	p := v.Probe
	prompt := !vpnPlatformAuto() && ((on && !(p.Running && p.Hash == v.Hash)) || (!on && (p.Running || p.Installed)))
	switch {
	case prompt && on:
		setToast("VPN: On — allow JukaHub in the Windows prompt")
	case prompt:
		setToast("VPN: Off — allow JukaHub in the Windows prompt")
	case !on:
		setToast("VPN: Off")
	case v.Safe && vpnPlatformAuto():
		setToast("VPN: On after the next normal start (safe mode)")
	default:
		setToast("VPN: On — connecting to " + v.Name)
	}
}

func vpnHasProfileRow(*Config) bool { return vpnSnapshot().HasProfile }

func vpnProfileValue(*Config) string {
	v := vpnSnapshot()
	if !v.HasProfile {
		return "Not set"
	}
	return v.Name
}

func vpnProfileDetail(*Config) string {
	v := vpnSnapshot()
	if !v.HasProfile || v.Config == nil {
		return "Import a WireGuard .conf from the card, or send one from your phone"
	}
	c := v.Config
	return "Server " + c.Endpoint + " · address " + wgJoinPrefixes(c.Addresses)
}

func vpnRouteOptions(*Config) []setOption {
	return []setOption{
		{Value: "", Label: "Everything", Note: "All traffic; your Wi-Fi network itself stays reachable"},
		{Value: vpnRouteConfig, Label: "Only the configuration's networks", Note: "Just the AllowedIPs the file lists"},
	}
}

func vpnRouteCurrent(c *Config) int {
	if vpnRouteAllIn(c) {
		return 0
	}
	return 1
}

func vpnRouteChoose(c *Config, value string) {
	setStoreString(c, vpnRouteKey, value)
	vpnPublish(c, true)
	if value == vpnRouteConfig {
		setToast("VPN: only the configuration's networks")
	} else {
		setToast("VPN: everything goes through it")
	}
}

func vpnRouteDetail(c *Config) string {
	v := vpnSnapshot()
	if !vpnRouteAllIn(c) && v.Config != nil {
		if v.Config.coversEverything() {
			return "This configuration sends everything anyway"
		}
		return "Only " + wgJoinPrefixes(v.Config.AllowedIPs)
	}
	return "Games, the browser, downloads and Remote Play all use the tunnel"
}

func vpnStatusVisible(c *Config) bool {
	v := vpnSnapshot()
	return v.HasProfile && (vpnEnabledIn(c) || v.Probe.Running || v.Probe.Installed)
}

func vpnStatusValue(*Config) string {
	p := vpnSnapshot().Probe
	switch {
	case p.HasHandshake:
		return "Handshake " + vpnAgo(p.Handshake)
	case p.Running && p.State == vpnStateActive:
		return "Running"
	case p.Running:
		return "No handshake yet"
	}
	return "Off"
}

func vpnStatusDetail(*Config) string {
	v := vpnSnapshot()
	p := v.Probe
	var parts []string
	if len(p.Addresses) > 0 {
		parts = append(parts, strings.Join(p.Addresses, ", "))
	}
	if p.Endpoint != "" {
		parts = append(parts, "server "+p.Endpoint)
	}
	if p.Rx > 0 || p.Tx > 0 {
		parts = append(parts, "in "+humanBytes(int64(p.Rx))+" · out "+humanBytes(int64(p.Tx)))
	}
	if p.DNS {
		parts = append(parts, "DNS through the VPN")
	}
	if len(parts) == 0 {
		if v.Config != nil {
			return "Your public key: " + v.Config.publicKey()
		}
		return ""
	}
	return strings.Join(parts, " · ")
}

func vpnAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	}
	return fmt.Sprintf("%d h ago", int(d/time.Hour))
}

// The Settings rows (setmodel.go, Network).

func vpnToggleRow() setRow {
	return setRow{
		ID: "vpn", Label: "VPN", Kind: setKindToggle,
		Detail:   vpnSettingDetail,
		Disabled: vpnSettingDisabled,
		Get:      vpnSettingOn,
		Set:      vpnSettingSet,
	}
}

func vpnProfileRow() setRow {
	return setRow{
		ID: "vpnProfile", Label: "VPN configuration", Kind: setKindAction,
		Detail: vpnProfileDetail,
		Value:  vpnProfileValue,
		Open:   vpnOpenProfileSheet,
	}
}

func vpnRouteRow() setRow {
	return setRow{
		ID: "vpnRoute", Label: "Send through the VPN", Kind: setKindChoice,
		Visible: vpnHasProfileRow,
		Detail:  vpnRouteDetail,
		Options: vpnRouteOptions,
		Current: vpnRouteCurrent,
		Choose:  vpnRouteChoose,
	}
}

func vpnStatusRow() setRow {
	return setRow{
		ID: "vpnStatus", Label: "VPN status", Kind: setKindInfo,
		Visible: vpnStatusVisible,
		Detail:  vpnStatusDetail,
		Value:   vpnStatusValue,
	}
}

// ── The configuration sheet ──────────────────────────────────────────────

func vpnOpenProfileSheet(c *Config) {
	v := vpnSnapshot()
	opts := []setOption{
		{Value: "file", Label: "Import a .conf file", Note: "From the SD card, its Downloads or VPN folder, or what your phone sent"},
		{Value: "phone", Label: "Send one from your phone", Note: "The companion page's VPN card takes the text or the file"},
	}
	if ok, _ := vpnPlatformSupport(); !ok && runtime.GOOS == "windows" {
		opts = append(opts, setOption{Value: "wireguard", Label: "Get WireGuard for Windows", Note: "wireguard.com/install; JukaHub hands it the tunnel"})
	}
	if !vpnPlatformAuto() && !vpnEnabledIn(c) && (v.Probe.Running || v.Probe.Installed) {
		opts = append(opts, setOption{Value: "stop", Label: "Remove the tunnel from WireGuard", Note: "The VPN is off here; Windows asks for permission"})
	}
	note := "A WireGuard configuration (.conf) from your VPN provider or your own server"
	if v.HasProfile && v.Config != nil {
		opts = append(opts, setOption{Value: "remove", Label: "Remove " + v.Name, Note: "Switches the VPN off and deletes the configuration from this device"})
		// The public key is what a server of one's own registers.
		note = v.Name + " · server " + v.Config.Endpoint + " · your public key " + v.Config.publicKey()
		if len(v.Config.Skipped) > 0 {
			note += " · ignored: " + strings.Join(v.Config.Skipped, ", ")
		}
	}
	setSheetOpen(&setHub.sheet, "VPN configuration", note, opts, "", func(value string) {
		vpnProfileAction(c, value)
	})
}

func vpnProfileAction(c *Config, value string) {
	switch value {
	case "file":
		dirs := vpnImportDirs(c)
		go func() {
			found := vpnScanConfigs(dirs)
			tbxPost(func() { vpnOpenFileSheet(c, found) })
		}()
	case "phone":
		openCompanionOverlay()
		setToast("Open the page on your phone and use its VPN card")
	case "wireguard":
		if err := openExternalURL("https://www.wireguard.com/install/"); err != nil {
			setToast("Open wireguard.com/install in a browser")
		}
	case "stop":
		// The switch is already off: an explicit nudge has the worker remove
		// what WireGuard still runs.
		vpnMgr.mu.Lock()
		vpnMgr.explicit = true
		vpnMgr.mu.Unlock()
		vpnWake()
		setToast("Allow JukaHub in the Windows prompt to remove the tunnel")
	case "remove":
		v := vpnSnapshot()
		setSheetOpen(&setHub.sheet, "Remove "+v.Name+"?", "The VPN switches off and the configuration is deleted from this device",
			[]setOption{{Value: "keep", Label: "Keep it"}, {Value: "remove", Label: "Remove"}}, "keep",
			func(choice string) {
				if choice == "remove" {
					vpnRemove(c)
				}
			})
	}
}

// vpnRemove switches the VPN off and asks the worker to delete the profile.
// The worker does it, so a stop never races an apply: on Windows the tunnel
// service goes first, and the profile is kept if Windows refused — JukaHub
// would otherwise have nothing left to stop it with.
func vpnRemove(c *Config) {
	setStoreBool(c, vpnEnabledKey, false)
	vpnMgr.mu.Lock()
	vpnMgr.removeReq = true
	vpnMgr.mu.Unlock()
	vpnPublish(c, false)
}

// vpnFinishRemove is Remove's worker half. present says a tunnel runs or is
// installed.
func vpnFinishRemove(present bool) {
	var err error
	if present {
		err = vpnStop()
	}
	if err == nil {
		err = vpnForgetProfile()
	}
	tbxPost(func() {
		if err != nil {
			ShowToast("The VPN configuration was kept: "+err.Error(), ToastKindError)
			return
		}
		ShowToast("VPN configuration removed", ToastKindInfo)
	})
}

// vpnForgetProfile deletes the profile and what Windows kept beside it.
func vpnForgetProfile() error {
	path, err := vpnProfilePath()
	if err != nil {
		return err
	}
	vpnSaveMu.Lock()
	defer vpnSaveMu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Join(filepath.Dir(path), "vpn")
	_ = os.Remove(filepath.Join(dir, "request.json"))
	_ = os.Remove(filepath.Join(dir, "applied.json"))
	vpnMgr.mu.Lock()
	vpnMgr.profile, vpnMgr.parsed = nil, nil
	vpnMgr.mu.Unlock()
	vpnWake()
	return nil
}

// vpnFoundFile is a configuration found on the card.
type vpnFoundFile struct {
	Path, Name, Where string
}

// vpnImportDirs are the folders the import list looks in (not below them).
func vpnImportDirs(c *Config) []string {
	root := filesDefaultRoot(c)
	dirs := []string{root, filepath.Join(root, "Downloads"), filepath.Join(root, "VPN"),
		filepath.Join(root, "WireGuard"), filepath.Join(root, companionDropDir), downloadsRoot(), exeDir()}
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"))
		}
	}
	return dirs
}

func vpnScanConfigs(dirs []string) []vpnFoundFile {
	seen := map[string]bool{}
	var out []vpnFoundFile
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for i, e := range entries {
			if i >= 500 || len(out) >= 40 {
				break
			}
			if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".conf") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			key := strings.ToLower(filepath.Clean(path))
			if seen[key] {
				continue
			}
			seen[key] = true
			if data, err := vpnReadConfigFile(path); err != nil || !wgLooksLikeConfig(string(data)) {
				continue
			}
			out = append(out, vpnFoundFile{Path: path, Name: e.Name(), Where: dir})
		}
	}
	return out
}

func vpnOpenFileSheet(c *Config, found []vpnFoundFile) {
	if len(found) == 0 {
		ShowToast("No WireGuard .conf found: copy one to the card (or its VPN folder), or send it from your phone", ToastKindInfo)
		return
	}
	if setHub.scene == "" || setHubSceneName(c) != setHub.scene {
		return // Settings was left while the card was searched
	}
	opts := make([]setOption, len(found))
	for i, f := range found {
		opts[i] = setOption{Value: f.Path, Label: f.Name, Note: f.Where}
	}
	setSheetOpen(&setHub.sheet, "Import a WireGuard configuration", "Pick the file from your VPN provider or server",
		opts, "", func(path string) { go vpnImportFile(path) })
}

// vpnFilesConfirm is the WireGuard file Files pressed A on once.
var vpnFilesConfirm struct {
	path string
	at   time.Time
}

// vpnFilesOpen is A on a file in Files: a WireGuard configuration is offered
// as the VPN's on the first press and imported on a second one. It reports
// whether it took the press.
func vpnFilesOpen(_ *Config, path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".conf") {
		return false
	}
	data, err := vpnReadConfigFile(path)
	if err != nil || !wgLooksLikeConfig(string(data)) {
		return false
	}
	if vpnFilesConfirm.path != path || time.Since(vpnFilesConfirm.at) > 6*time.Second {
		vpnFilesConfirm.path, vpnFilesConfirm.at = path, time.Now()
		ShowToast(hintSentence("This is a WireGuard configuration. Press %s again to use it for the VPN", "A", "Use"), ToastKindInfo)
		return true
	}
	vpnFilesConfirm.path = ""
	go vpnImportFile(path)
	return true
}

// ── Quick Settings and the header ────────────────────────────────────────

// vpnQSAvailable: the switch is offered once a configuration is set.
func vpnQSAvailable() bool {
	ok, _ := vpnPlatformSupport()
	return ok && vpnSnapshot().HasProfile
}

// vpnQSDetail is the short reason Quick Settings gives when it is not.
func vpnQSDetail() string {
	if ok, _ := vpnPlatformSupport(); !ok {
		if runtime.GOOS == "windows" {
			return "needs WireGuard"
		}
		return "needs root"
	}
	return "no configuration"
}

// vpnHeaderBadge says whether the Wi-Fi meter wears the VPN's lock, and
// whether in the warning colour: on but not connected, or switched off while
// WireGuard's service on Windows still carries the traffic. It runs every
// frame, so it reads the published switch, not the config.
func vpnHeaderBadge() (show, warn bool) {
	auto := vpnPlatformAuto()
	vpnMgr.mu.Lock()
	on := vpnMgr.enabled && vpnMgr.parsed != nil && (!vpnMgr.safe || !auto)
	state, present := vpnMgr.probe.State, vpnMgr.probe.Running || vpnMgr.probe.Installed
	vpnMgr.mu.Unlock()
	if !on {
		stray := present && !auto
		return stray, stray
	}
	return true, state != vpnStateConnected && state != vpnStateActive
}

// ── The phone companion's VPN card ───────────────────────────────────────

// handleVPN answers the companion page's VPN card: GET says whether a
// configuration is saved (its name, never its keys) and whether the VPN is
// on; POST imports one.
func (s *companionServer) handleVPN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, vpnCompanionStatus(""))
	case http.MethodPost:
		var body struct {
			Name   string `json:"name"`
			Config string `json:"config"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 3*wgConfigMaxBytes)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		name, err := vpnImport(body.Name, body.Config, "phone")
		if err != nil {
			http.Error(w, "Not a usable WireGuard configuration: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		tbxPost(func() { vpnImportToast(name, nil) })
		writeCompanionJSON(w, vpnCompanionStatus("saved"))
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// vpnCompanionStatus is the card's view; after a save, its message says what
// happens next.
func vpnCompanionStatus(after string) map[string]interface{} {
	v := vpnSnapshot()
	vpnMgr.mu.Lock()
	enabled, all := vpnMgr.enabled, vpnMgr.routeAll
	vpnMgr.mu.Unlock()
	on := enabled && v.HasProfile
	out := map[string]interface{}{"ok": true, "set": v.HasProfile, "name": v.Name, "enabled": on}
	if v.HasProfile {
		out["state"] = strings.TrimSuffix(vpnDetailFor(v, enabled, all), "…")
	}
	switch {
	case after == "":
	case on && vpnPlatformAuto():
		out["message"] = "Saved on the handheld as " + v.Name + "; the VPN reconnects with it."
	case on:
		out["message"] = "Saved as " + v.Name + "; allow JukaHub in the Windows prompt on the PC to use it."
	default:
		out["message"] = "Saved on the handheld as " + v.Name + ". Switch the VPN on in Settings > Network."
	}
	return out
}
