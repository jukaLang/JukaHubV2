package main

// tailscaled.go — Tailscale built in: reach the handheld from anywhere.
//
// Smart Home's Tailscale row only worked with a tailscale binary on PATH,
// which a TrimUI never has. Now JukaHub
// installs Tailscale's own static build and runs it as a child it supervises:
//
//   - Install: the newest stable tarball pkgs.tailscale.com lists for this
//     CPU (?mode=json: TarballsVersion and Tarballs[arch]), checked against
//     the SHA-256 published beside it (<tarball>.sha256). A tarball without a
//     readable hash, or one that does not match it, installs nothing. Only
//     tailscale and tailscaled are taken from it, into the tools folder, each
//     placed with toolboxPlace (the old copy renamed aside, then the new one
//     in). The Emulator updates pass keeps it current (registered below);
//     the screen has Update too.
//   - Run: tailscaled --tun=userspace-networking. No kernel TUN, no routes,
//     no DNS changes: the handheld's own traffic does not go through the
//     tailnet, but a connection from a tailnet device to this handheld's
//     100.x address is handed to 127.0.0.1 on the same port, so the SSH and
//     FTP servers, the phone companion, VNC and the SMB share answer from
//     anywhere the user's devices are. State lives in <data>/tailscale; the
//     control socket in /tmp (the card is exFAT, which holds no sockets).
//   - When: only while the switch is on (off by default), Wi-Fi is up and the
//     battery is not low — Syncthing's rules: below 15 % off the charger it
//     stops, at 20 % it comes back, five minutes without Wi-Fi stop it. A
//     game does not stop it: it idles until someone connects. nice 10, two
//     cores, no log upload. Three quick exits stop the restarts for ten
//     minutes.
//   - Log in: `tailscale up` waits for a browser sign-in; the worker reads
//     the sign-in address from `tailscale status --json` and the screen shows
//     it as a QR code. A reusable auth key works too (masked keyboard, handed
//     to the CLI through a 0600 file in /tmp, never on its command line).
//
// On a PC nothing is installed: Tailscale for Windows (or a tailscale on PATH)
// is read through its own CLI for the screen and Smart Home. One goroutine
// (tailscaledWorker) owns the process and the polls; the screen
// (tailscaled_ui.go) reads tailscaledSnapshotNow() and posts actions.
// docs/TAILSCALE.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tailscaledIndexURL   = "https://pkgs.tailscale.com/stable/?mode=json"
	tailscaledPkgBase    = "https://pkgs.tailscale.com/stable/"
	tailscaledEnabledKey = "tailscaleEnabled"
	// tailscaledUDPPort is Tailscale's usual WireGuard port: a fixed one
	// keeps a router's port forward or UPnP mapping working.
	tailscaledUDPPort = 41641

	// Battery: stop below the first level unless charging, start again at
	// the second, so a level hovering at the line does not flap.
	tailscaledLowBattery    = 15
	tailscaledBatteryResume = 20
	// tailscaledWifiGrace is how long the daemon outlives a lost Wi-Fi link
	// (a dropped link usually comes back).
	tailscaledWifiGrace = 5 * time.Minute

	tailscaledPollIdle  = 5 * time.Minute
	tailscaledPollPanel = 3 * time.Second
	tailscaledPollLogin = 2 * time.Second
	tailscaledPollWait  = 30 * time.Second
	tailscaledStartWait = 20 * time.Second
	// tailscaledLoginWait is how long a sign-in may take in the browser.
	tailscaledLoginWait = 10 * time.Minute
	// tailscaledPeersMax caps the devices the screen lists.
	tailscaledPeersMax = 60
)

// tailscaledPhase is where JukaHub's Tailscale is.
type tailscaledPhase int

const (
	tailscaledPhaseChecking tailscaledPhase = iota
	tailscaledPhaseNotInstalled
	tailscaledPhaseInstalling
	tailscaledPhaseOff
	tailscaledPhaseWaitingWifi
	tailscaledPhaseLowBattery
	tailscaledPhaseStarting
	tailscaledPhaseRunning
	tailscaledPhaseFailed
)

// tailscaledSnapshot is what the screen, the tile and Smart Home read. The
// worker replaces it under tailscaledSnap.mu; readers take a copy.
type tailscaledSnapshot struct {
	Installed bool
	// Version is the installed release ("1.104.7"); Latest the newest one
	// listed at the last check ("" before one).
	Version string
	Latest  string
	Phase   tailscaledPhase
	// Detail says why it failed or waits, in words.
	Detail string
	// What `tailscale status --json` said last: BackendState (NeedsLogin,
	// Running, Stopped…), the sign-in address while a login waits, this
	// device's addresses and MagicDNS name, the tailnet and its devices.
	Backend   string
	AuthURL   string
	LoggingIn bool
	IPs       []string
	DNSName   string
	Tailnet   string
	Peers     []tailscalePeer
	// System is a PC's own Tailscale, read through its CLI.
	System bool
}

var tailscaledSnap struct {
	mu sync.Mutex
	s  tailscaledSnapshot
}

// tailscaledSnapshotNow returns a copy of the current state.
func tailscaledSnapshotNow() tailscaledSnapshot {
	tailscaledSnap.mu.Lock()
	defer tailscaledSnap.mu.Unlock()
	s := tailscaledSnap.s
	s.IPs = append([]string(nil), s.IPs...)
	s.Peers = append([]tailscalePeer(nil), s.Peers...)
	return s
}

// tailscaledUpdate edits the snapshot under its lock.
func tailscaledUpdate(fn func(s *tailscaledSnapshot)) {
	tailscaledSnap.mu.Lock()
	fn(&tailscaledSnap.s)
	tailscaledSnap.mu.Unlock()
}

// tailscaledClearStatus forgets what the daemon said: it is not running.
func tailscaledClearStatus(s *tailscaledSnapshot) {
	s.Backend, s.AuthURL, s.DNSName, s.Tailnet = "", "", "", ""
	s.IPs, s.Peers = nil, nil
	s.LoggingIn = false
}

// The switch, mirrored from config.Variables.Custom on the main thread, and
// the worker's other inputs.
var (
	tailscaledWantOn   atomic.Bool
	tailscaledQuitting atomic.Bool
	// tailscaledRestart asks the worker for a restart at the next quiet
	// moment: an update placed a new tailscaled beside the running one.
	tailscaledRestart atomic.Bool
	// tailscaledInstalling is held by an install from the screen or the
	// update pass, so two never write the same files.
	tailscaledInstalling atomic.Bool
	// tailscaledViewedAt is when the screen (or Smart Home) last looked, in
	// tailscaledNow units: polls are quick while someone looks.
	tailscaledViewedAt atomic.Int64
)

// tailscaledClock is the monotonic base the view stamps count from: the
// handheld boots before NTP, so wall-clock times are never compared.
var tailscaledClock = time.Now()

func tailscaledNow() int64 { return int64(time.Since(tailscaledClock)) + 1 }

// tailscaledNoteViewer marks that someone looks at Tailscale's state now.
func tailscaledNoteViewer() { tailscaledViewedAt.Store(tailscaledNow()) }

// tailscaledViewed reports whether someone looked in the last few seconds.
func tailscaledViewed() bool {
	at := tailscaledViewedAt.Load()
	return at != 0 && time.Duration(tailscaledNow()-at) < 5*time.Second
}

var (
	tailscaledKickCh   = make(chan struct{}, 1)
	tailscaledActions  = make(chan func(), 8)
	tailscaledWorkOnce sync.Once
)

// tailscaledKick wakes the worker. It never blocks.
func tailscaledKick() {
	select {
	case tailscaledKickCh <- struct{}{}:
	default:
	}
}

// tailscaledPost runs fn on the worker. A full queue is said, not waited on.
func tailscaledPost(fn func()) bool {
	tailscaledStartWorker()
	select {
	case tailscaledActions <- fn:
		return true
	default:
		ShowToast("Tailscale is busy — try again in a moment", ToastKindInfo)
		return false
	}
}

func tailscaledStartWorker() {
	tailscaledWorkOnce.Do(func() { go tailscaledWorker() })
}

// ── Settings ─────────────────────────────────────────────────────────────

// tailscaledEnabled is the user's switch. Off by default: a download and a
// background process the user asks for.
func tailscaledEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, tailscaledEnabledKey, false)
}

// tailscaledSetEnabled writes the switch and tells the worker. Main thread.
func tailscaledSetEnabled(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[tailscaledEnabledKey] = boolString(on)
	saveUserConfigDebounced(userConfigFromVariables(config))
	tailscaledWantOn.Store(on)
	tailscaledStartWorker()
	tailscaledKick()
}

// tailscaledBoot mirrors the switch and starts the worker when it is on. A
// deferred startup task (main.go); the worker does the rest.
func tailscaledBoot(config *Config) {
	tailscaledWantOn.Store(tailscaledEnabled(config))
	if tailscaledWantOn.Load() {
		tailscaledStartWorker()
	}
}

func init() {
	bootOnShutdown(tailscaledShutdown)
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "tailscale",
		Title:     "Tailscale",
		Supported: tailscaledCanInstall,
		Installed: tailscaledInstalled,
		Check:     tailscaledUpdateCheck,
		Install:   tailscaledUpdateInstall,
	})
}

// ── Paths ────────────────────────────────────────────────────────────────

// tailscaledHome is the daemon's state folder (its node key lives there).
func tailscaledHome() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "tailscale")
	}
	return filepath.Join("data", "tailscale")
}

// tailscaledBinPath is where JukaHub keeps tailscale or tailscaled.
func tailscaledBinPath(name string) string {
	dir := toolboxToolsDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func tailscaledPIDFile() string { return filepath.Join(tailscaledHome(), "tailscaled.pid") }

// tailscaledCanInstall reports whether JukaHub installs and runs its own
// Tailscale here: the TrimUI handhelds. A PC uses its own Tailscale.
func tailscaledCanInstall() bool {
	return runtime.GOOS == "linux" && tailscaledArchKey(runtime.GOARCH) != "" && IsTrimuiDevice()
}

// tailscaledInstalled reports whether both programs are there (stat only).
func tailscaledInstalled() bool {
	for _, name := range []string{"tailscale", "tailscaled"} {
		p := tailscaledBinPath(name)
		if p == "" {
			return false
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

// tailscaledOwnsCLI reports whether Tailscale here is JukaHub's own copy.
func tailscaledOwnsCLI() bool { return tailscaledCanInstall() && tailscaledInstalled() }

// tailscaledInstallRecord is what was installed, kept in the state folder.
type tailscaledInstallRecord struct {
	Version string `json:"version"`
	Tarball string `json:"tarball"`
	SHA256  string `json:"sha256"`
}

func tailscaledRecordPath() string { return filepath.Join(tailscaledHome(), "jukahub-install.json") }

func tailscaledReadRecord() tailscaledInstallRecord {
	var rec tailscaledInstallRecord
	if data, err := os.ReadFile(tailscaledRecordPath()); err == nil {
		_ = json.Unmarshal(data, &rec)
	}
	return rec
}

// ── The command line ─────────────────────────────────────────────────────

// tailscaleCLI is the tailscale command line to run and the flags that come
// before its subcommand: JukaHub's own copy and its daemon's socket where
// JukaHub runs Tailscale, else a tailscale the computer has (on PATH, or in
// Tailscale for Windows' folder). smart_home.go uses it too.
func tailscaleCLI() (bin string, pre []string, ok bool) {
	if tailscaledOwnsCLI() {
		return tailscaledBinPath("tailscale"), []string{"--socket=" + tailscaledSocketPath()}, true
	}
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil, true
	}
	if p := tailscaledSystemCLI(); p != "" {
		return p, nil, true
	}
	return "", nil, false
}

// tailscaledRunCLI runs one tailscale command with a deadline and returns
// its standard output, or an error carrying the last line it wrote.
func tailscaledRunCLI(timeout time.Duration, args ...string) ([]byte, error) {
	bin, pre, ok := tailscaleCLI()
	if !ok {
		return nil, errors.New("tailscale is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append(pre, args...)...)
	schedHideWindow(cmd)
	// A child that keeps the pipe open cannot hold Wait past the kill.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return out, errors.New("tailscale did not answer in time")
	}
	if err != nil {
		if msg := tailscaledLastLine(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
		return out, err
	}
	return out, nil
}

// tailscaledLastLine is the last non-blank line of a command's output.
func tailscaledLastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// ── Install ──────────────────────────────────────────────────────────────

// tailscaledArchKey is the package list's Tarballs key for a Go
// architecture, or "" when Tailscale publishes no Linux tarball for it.
func tailscaledArchKey(goarch string) string {
	switch goarch {
	case "arm64", "arm", "amd64", "386":
		return goarch
	}
	return ""
}

// tailscaledIndex is the newest stable tarball for one architecture.
type tailscaledIndex struct {
	Version string
	Tarball string
}

// tailscaledParseIndex reads pkgs.tailscale.com's ?mode=json answer for one
// architecture. The tarball must be named the way Tailscale names them
// (tailscale_<version>_<arch>.tgz) with a version of digits and dots, so
// nothing in the answer can steer the download anywhere else.
func tailscaledParseIndex(data []byte, arch string) (tailscaledIndex, error) {
	var raw struct {
		TarballsVersion string
		Tarballs        map[string]string
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return tailscaledIndex{}, errors.New("the Tailscale package list is unreadable")
	}
	ver := strings.TrimSpace(raw.TarballsVersion)
	name := strings.TrimSpace(raw.Tarballs[arch])
	if ver == "" || name == "" {
		return tailscaledIndex{}, fmt.Errorf("Tailscale lists no build for %s", arch)
	}
	if !tailscaledPlainVersion(ver) || name != "tailscale_"+ver+"_"+arch+".tgz" {
		return tailscaledIndex{}, fmt.Errorf("unexpected Tailscale package %q", name)
	}
	return tailscaledIndex{Version: ver, Tarball: name}, nil
}

// tailscaledPlainVersion accepts digits and dots ("1.104.7").
func tailscaledPlainVersion(v string) bool {
	if v == "" || len(v) > 32 || v[0] == '.' {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// tailscaledFetch reads a small file from pkgs.tailscale.com over HTTPS.
func tailscaledFetch(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(rawURL, tailscaledPkgBase) {
		return nil, errors.New("refusing a download from outside pkgs.tailscale.com")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Always a client of its own, never the update pass's (client): that one
	// follows redirects only to its GitHub allowlist, and Tailscale moves
	// its files to dl.tailscale.com. The same certificate fallback as the
	// downloads (tools.go); HTTPS on Tailscale's own hosts only.
	own := &http.Client{Timeout: 30 * time.Second, Transport: ytTransport(), CheckRedirect: tailscaledRedirectOK}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := own.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.Scheme != "https" {
		return nil, errors.New("pkgs.tailscale.com redirected to a plain-HTTP address")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pkgs.tailscale.com answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// tailscaledRedirectOK lets tailscaledFetch follow a redirect only over
// HTTPS to tailscale.com or one of its subdomains, five at most.
func tailscaledRedirectOK(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	host := strings.ToLower(req.URL.Hostname())
	if req.URL.Scheme != "https" || (host != "tailscale.com" && !strings.HasSuffix(host, ".tailscale.com")) {
		return fmt.Errorf("refusing a redirect to %s", req.URL.Redacted())
	}
	return nil
}

// tailscaledCheckSum compares the downloaded file with the SHA-256 Tailscale
// publishes beside it (the hash alone, or "<hash>  <name>"). It returns the
// file's hash; a mismatch or an unreadable list is an error.
func tailscaledCheckSum(file, published, tarball string) (string, error) {
	want, ok := toolboxParseChecksum(published, tarball, true)
	if !ok {
		return "", errors.New("Tailscale's published SHA-256 is unreadable; nothing was installed")
	}
	got, err := hashFile(file)
	if err != nil {
		return "", fmt.Errorf("hash the download: %w", err)
	}
	if !strings.EqualFold(got, want) {
		return "", errors.New("the download does not match the SHA-256 Tailscale publishes; nothing was installed")
	}
	return got, nil
}

// tailscaledUnpack takes tailscale and tailscaled out of the tarball into
// stage. The shared extractor refuses absolute and ../ paths, links and
// devices; an archive without both programs is refused here.
func tailscaledUnpack(ctx context.Context, archive, stage string) error {
	card := emuRuntimeCardFrom(ctx)
	found, err := extractToolFilesCtx(ctx, archive, stage, []string{"tailscaled", "tailscale"},
		func(done, total int64) { card.bytes(installPhaseExtract, done, total) })
	if err != nil {
		return err
	}
	if len(found) != 2 {
		return errors.New("the Tailscale package has no tailscale and tailscaled")
	}
	return nil
}

// tailscaledInstall downloads the newest stable build, verifies it and
// places both programs; it returns the version now installed. A daemon that
// is running keeps its old file (renamed aside) until it restarts.
func tailscaledInstall(ctx context.Context, client *http.Client) (string, error) {
	if !tailscaledCanInstall() {
		return "", errors.New("JukaHub installs Tailscale on the handheld only")
	}
	if !tailscaledInstalling.CompareAndSwap(false, true) {
		return "", errors.New("Tailscale is already being installed")
	}
	defer tailscaledInstalling.Store(false)
	dir := toolboxToolsDir()
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("tools directory unavailable")
	}
	if err := os.MkdirAll(dir, PermDir); err != nil {
		return "", fmt.Errorf("create tools directory: %w", err)
	}
	// A download or stage a power cut left behind (the defers below never
	// ran): tens of MB on the card. Ours alone: tailscaledInstalling is held.
	for _, pattern := range []string{".tailscale-*.tgz", ".tailscale-stage-*"} {
		old, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, p := range old {
			_ = os.RemoveAll(p)
		}
	}
	card := emuRuntimeCardFrom(ctx)
	card.phase(installPhaseResolving)
	data, err := tailscaledFetch(ctx, client, tailscaledIndexURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf("find the Tailscale release: %w", err)
	}
	idx, err := tailscaledParseIndex(data, tailscaledArchKey(runtime.GOARCH))
	if err != nil {
		return "", err
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) { s.Latest = idx.Version })
	if rec := tailscaledReadRecord(); rec.Version == idx.Version && tailscaledInstalled() {
		return idx.Version, errEmuUpdaterCurrent
	}
	src := tailscaledPkgBase + idx.Tarball
	logInfo("tailscale", "installing %s from %s", idx.Version, src)

	// Staged in the tools folder: the handheld's /tmp is a small tmpfs.
	tmp, err := os.CreateTemp(dir, ".tailscale-*.tgz")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	card.phase(installPhaseDownload)
	progress := func(done, total int64) { card.bytes(installPhaseDownload, done, total) }
	if err := downloadFileWithProgressCtx(ctx, src, tmpName, progress); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	card.phase(installPhaseVerify)
	published, err := tailscaledFetch(ctx, client, src+".sha256", 4096)
	if err != nil {
		return "", fmt.Errorf("download the published SHA-256: %w", err)
	}
	sum, err := tailscaledCheckSum(tmpName, string(published), idx.Tarball)
	if err != nil {
		return "", err
	}

	card.restart(installPhaseExtract)
	stage, err := os.MkdirTemp(dir, ".tailscale-stage-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := tailscaledUnpack(ctx, tmpName, stage); err != nil {
		return "", fmt.Errorf("unpack: %w", err)
	}
	for _, name := range []string{"tailscaled", "tailscale"} {
		staged := filepath.Join(stage, name)
		_ = os.Chmod(staged, PermExec)
		target := tailscaledBinPath(name)
		if err := toolboxPlace(staged, target, fileExists(target)); err != nil {
			return "", fmt.Errorf("install %s: %w", name, err)
		}
	}
	if err := os.MkdirAll(tailscaledHome(), 0o700); err != nil {
		return "", err
	}
	rec := tailscaledInstallRecord{Version: idx.Version, Tarball: idx.Tarball, SHA256: sum}
	if data, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = AtomicWrite(tailscaledRecordPath(), data, 0o600)
	}
	logInfo("tailscale", "installed %s (sha256 verified)", idx.Version)
	return idx.Version, nil
}

// tailscaledInstallStart installs or updates from the screen: the install
// strip is claimed here, on the main thread; the work runs on a goroutine.
func tailscaledInstallStart() {
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi to download Tailscale", ToastKindInfo)
		return
	}
	if tailscaledInstalling.Load() {
		ShowToast("Tailscale is already being installed", ToastKindInfo)
		return
	}
	update := tailscaledInstalled()
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.Phase = tailscaledPhaseInstalling
		s.Detail = ""
	})
	run, ctx := installBarBeginRun("Tailscale")
	go func() {
		ver, err := tailscaledInstall(emuRuntimeWithRun(ctx, run), nil)
		// Already the newest: nothing was placed, so nothing to restart.
		current := errors.Is(err, errEmuUpdaterCurrent)
		if current {
			err = nil
		}
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
			logWarn("tailscale", "install failed: %v", err)
			tailscaledUpdate(func(s *tailscaledSnapshot) {
				s.Phase = tailscaledPhaseFailed
				s.Detail = "Install failed: " + firstLine(err.Error())
			})
			ShowToast("Could not install Tailscale: "+firstLine(err.Error()), ToastKindError)
			tailscaledStartWorker()
			tailscaledKick()
			return
		}
		installBarDoneRun(run)
		tailscaledUpdate(func(s *tailscaledSnapshot) {
			s.Installed = true
			s.Version = ver
			s.Phase = tailscaledPhaseOff
			s.Detail = ""
		})
		switch {
		case current:
			ShowToast("Tailscale "+ver+" is the newest version", ToastKindSuccess)
		case update:
			tailscaledRestart.Store(true)
			ShowToast("Tailscale "+ver+" installed", ToastKindSuccess)
		default:
			ShowToast("Tailscale installed — starting", ToastKindSuccess)
		}
		tailscaledStartWorker()
		tailscaledKick()
	}()
}

// tailscaledUpdateCheck is the update pass's question and the screen's
// Check: is a newer stable build listed than the one installed?
func tailscaledUpdateCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	data, err := tailscaledFetch(ctx, client, tailscaledIndexURL, 1<<20)
	if err != nil {
		return false, "", err
	}
	idx, err := tailscaledParseIndex(data, tailscaledArchKey(runtime.GOARCH))
	if err != nil {
		return false, "", err
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) { s.Latest = idx.Version })
	have := tailscaledReadRecord().Version
	return have == "" || portVersionNewer(idx.Version, have), idx.Version, nil
}

// tailscaledUpdateInstall is the pass's install. The running daemon keeps
// its old file until the worker restarts it at the next quiet moment.
func tailscaledUpdateInstall(ctx context.Context, client *http.Client, progress func(string)) error {
	if progress != nil {
		progress("Downloading")
	}
	ver, err := tailscaledInstall(ctx, client)
	if err != nil {
		return err
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) { s.Version = ver })
	tailscaledRestart.Store(true)
	tailscaledKick()
	return nil
}

// ── The daemon ───────────────────────────────────────────────────────────

// tailscaledProc is the running daemon, a child JukaHub started.
var tailscaledProc struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
	// startedAt and fails count quick exits: three within a minute of a
	// start stop the restarts for ten minutes.
	startedAt time.Time
	fails     int
}

// tailscaledRunningLocked reports whether the daemon is up as far as the
// process bookkeeping knows. tailscaledProc.mu is held.
func tailscaledRunningLocked() bool {
	if tailscaledProc.cmd == nil || tailscaledProc.done == nil {
		return false
	}
	select {
	case <-tailscaledProc.done:
		return false
	default:
		return true
	}
}

func tailscaledRunning() bool {
	tailscaledProc.mu.Lock()
	defer tailscaledProc.mu.Unlock()
	return tailscaledRunningLocked()
}

// tailscaledStart starts the daemon and waits until its CLI answers. Worker
// goroutine only.
func tailscaledStart() error {
	home := tailscaledHome()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	// A daemon an earlier JukaHub left behind (a crash, a kill) holds the
	// state and the port: it goes first.
	tailscaledKillStale(tailscaledPIDFile(), home)
	sock := tailscaledSocketPath()
	cmd := exec.Command(tailscaledBinPath("tailscaled"),
		"--tun=userspace-networking",
		"--statedir="+home,
		"--socket="+sock,
		"--port="+strconv.Itoa(tailscaledUDPPort),
		// No upload of its logs to Tailscale: less traffic and battery, and
		// nothing about this handheld leaves it but the connection itself.
		"--no-logs-no-support",
	)
	cmd.Dir = home
	// Its log state and buffers beside its state: --statedir alone leaves
	// them in /var/lib/tailscale on the firmware's root file system.
	env := append(os.Environ(), "TS_LOGS_DIR="+home)
	if IsTrimuiDevice() {
		// Two cores are plenty for a VPN that mostly idles.
		env = append(env, "GOMAXPROCS=2")
	}
	cmd.Env = env
	logFile, _ := os.OpenFile(filepath.Join(home, "tailscaled.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	out := &syncthingCappedLog{f: logFile}
	cmd.Stdout = out
	cmd.Stderr = out
	// Its own process group (syncthing_proc_unix.go): a game's group kill
	// never reaches it, and the priority below covers all its threads.
	syncthingProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return fmt.Errorf("start: %w", err)
	}
	tailscaledLowerPriority(cmd)
	_ = os.WriteFile(tailscaledPIDFile(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	done := make(chan struct{})
	tailscaledProc.mu.Lock()
	tailscaledProc.cmd = cmd
	tailscaledProc.done = done
	tailscaledProc.startedAt = time.Now()
	tailscaledProc.mu.Unlock()
	if tailscaledQuitting.Load() {
		// The exit hook ran while this one was starting and saw nothing to stop.
		syncthingKillTree(cmd)
	}
	go func() {
		err := cmd.Wait()
		close(done)
		out.mu.Lock()
		if out.f != nil {
			out.f.Close()
			out.f = nil
		}
		out.mu.Unlock()
		logInfo("tailscale", "tailscaled exited: %v", err)
		tailscaledKick()
	}()
	logInfo("tailscale", "started tailscaled pid %d (userspace networking, socket %s)", cmd.Process.Pid, sock)

	deadline := time.Now().Add(tailscaledStartWait)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return fmt.Errorf("tailscaled stopped while starting — see %s", filepath.Join(home, "tailscaled.log"))
		case <-time.After(500 * time.Millisecond):
		}
		if _, err := tailscaledRunCLI(3*time.Second, "status", "--json"); err == nil {
			return nil
		}
	}
	tailscaledKill()
	return fmt.Errorf("tailscaled did not answer within %s", tailscaledStartWait)
}

func tailscaledKill() {
	tailscaledProc.mu.Lock()
	cmd := tailscaledProc.cmd
	tailscaledProc.mu.Unlock()
	syncthingKillTree(cmd)
}

// tailscaledStop asks the daemon to end (SIGINT: it closes its connections
// cleanly) and kills it when it is not gone within budget.
func tailscaledStop(budget time.Duration) {
	tailscaledProc.mu.Lock()
	running := tailscaledRunningLocked()
	cmd, done := tailscaledProc.cmd, tailscaledProc.done
	// A stop JukaHub asked for is not a crash for the restart counter.
	tailscaledProc.startedAt = time.Time{}
	tailscaledProc.mu.Unlock()
	if !running {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(budget):
		syncthingKillTree(cmd)
	}
	_ = os.Remove(tailscaledPIDFile())
}

// tailscaledShutdown is the app-exit hook: the daemon belongs to JukaHub and
// stops with it, and a sign-in waiting for a browser is given up.
func tailscaledShutdown() {
	tailscaledQuitting.Store(true)
	tailscaledCancelLogin()
	tailscaledStop(time.Second)
}

// ── Signing in ───────────────────────────────────────────────────────────

// tailscaledLogin is the `tailscale up` that waits for a sign-in. gen tells a
// finished command whether it is still the current one.
var tailscaledLogin struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int
}

// tailscaledCancelLogin gives up a sign-in that waits.
func tailscaledCancelLogin() {
	tailscaledLogin.mu.Lock()
	cancel := tailscaledLogin.cancel
	tailscaledLogin.cancel = nil
	tailscaledLogin.gen++
	tailscaledLogin.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.LoggingIn = false
		s.AuthURL = ""
	})
}

// tailscaledHostname is the name this handheld takes in the tailnet: the
// Device name (Settings → Network → Device name), folded to a DNS label.
func tailscaledHostname() string { return tailscaledDNSLabel(peerDeviceName()) }

// tailscaledDNSLabel folds a name to lower-case letters, digits and single
// dashes, at most 63 long ("JukaHub · Smart Pro" → "jukahub-smart-pro").
func tailscaledDNSLabel(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 63 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	if out := strings.Trim(b.String(), "-"); out != "" {
		return out
	}
	return "jukahub"
}

// tailscaledLoginStart runs `tailscale up` on its own goroutine: with a key
// it signs in at once, without one it waits for the browser sign-in whose
// address the worker reads from the status. keyFile ("" for none) is deleted
// when the command ends. --reset keeps `up` from refusing over settings it
// was not given again; the exit node and routes do not apply in userspace
// mode anyway.
func tailscaledLoginStart(keyFile string) {
	tailscaledCancelLogin()
	ctx, cancel := context.WithTimeout(context.Background(), tailscaledLoginWait)
	tailscaledLogin.mu.Lock()
	tailscaledLogin.cancel = cancel
	tailscaledLogin.gen++
	gen := tailscaledLogin.gen
	tailscaledLogin.mu.Unlock()
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.LoggingIn = true
		s.AuthURL = ""
		s.Detail = ""
	})
	args := []string{"up", "--reset", "--hostname=" + tailscaledHostname(), "--accept-dns=false"}
	if keyFile != "" {
		args = append(args, "--auth-key=file:"+keyFile)
	}
	go func() {
		defer cancel()
		if keyFile != "" {
			defer os.Remove(keyFile)
		}
		var err error
		msg := ""
		if bin, pre, ok := tailscaleCLI(); !ok {
			err = errors.New("tailscale is not installed")
		} else {
			cmd := exec.CommandContext(ctx, bin, append(pre, args...)...)
			schedHideWindow(cmd)
			cmd.WaitDelay = 2 * time.Second
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			err = cmd.Run()
			msg = tailscaledLastLine(buf.String())
		}
		tailscaledLogin.mu.Lock()
		current := tailscaledLogin.gen == gen
		if current {
			tailscaledLogin.cancel = nil
		}
		tailscaledLogin.mu.Unlock()
		if !current {
			return // given up for a newer sign-in, a log-out or the switch
		}
		tailscaledUpdate(func(s *tailscaledSnapshot) { s.LoggingIn = false })
		switch {
		case err == nil:
			ShowToast("Tailscale: this handheld is in your tailnet", ToastKindSuccess)
		case ctx.Err() == context.DeadlineExceeded:
			ShowToast("Tailscale: the sign-in was not finished in time — try again", ToastKindWarning)
		default:
			if msg == "" {
				msg = err.Error()
			}
			logWarn("tailscale", "up: %v (%s)", err, msg)
			tailscaledUpdate(func(s *tailscaledSnapshot) { s.Detail = "Log in failed: " + msg })
			ShowToast("Tailscale: "+msg, ToastKindError)
		}
		tailscaledKick()
	}()
	tailscaledKick()
}

// tailscaledLoginWithKey signs in with an auth key. The key reaches the CLI
// in a 0600 file in the temp folder (tmpfs on the handheld), never on its
// command line, which every process can read; the file goes when the command
// ends. Worker goroutine.
func tailscaledLoginWithKey(key string) {
	f, err := os.CreateTemp("", "jukahub-tskey-*")
	if err != nil {
		ShowToast("Tailscale: "+firstLine(err.Error()), ToastKindError)
		return
	}
	name := f.Name()
	_ = f.Chmod(0o600)
	_, werr := f.WriteString(key)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(name)
		ShowToast("Tailscale: the key could not be handed over", ToastKindError)
		return
	}
	tailscaledLoginStart(name)
}

// tailscaledLogout signs this handheld out of the tailnet. Worker goroutine.
func tailscaledLogout() {
	tailscaledCancelLogin()
	if _, err := tailscaledRunCLI(20*time.Second, "logout"); err != nil {
		ShowToast("Tailscale: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast("Logged out of Tailscale", ToastKindSuccess)
	tailscaledKick()
}

// tailscaledRemoveAll signs out when it can, stops the daemon and deletes
// both programs and the state folder, node key included: a later install is
// a new device. Worker goroutine.
func tailscaledRemoveAll() error {
	tailscaledCancelLogin()
	if tailscaledRunning() {
		if _, err := tailscaledRunCLI(10*time.Second, "logout"); err != nil {
			logInfo("tailscale", "logout before removal: %v", err)
		}
	}
	tailscaledStop(3 * time.Second)
	for _, name := range []string{"tailscale", "tailscaled"} {
		p := tailscaledBinPath(name)
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
		_ = os.Remove(p + ".old")
	}
	home := tailscaledHome()
	if filepath.Base(home) != "tailscale" {
		return fmt.Errorf("refusing to delete %s", home)
	}
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	tailscaledProc.mu.Lock()
	tailscaledProc.cmd = nil
	tailscaledProc.done = nil
	tailscaledProc.fails = 0
	tailscaledProc.startedAt = time.Time{}
	tailscaledProc.mu.Unlock()
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		*s = tailscaledSnapshot{Phase: tailscaledPhaseNotInstalled}
	})
	return nil
}

// ── Status ───────────────────────────────────────────────────────────────

// tailscaledStatusExtra is what the screen needs from `tailscale status
// --json` beyond parseTailscaleStatus (smart_home.go): the sign-in address,
// this device's MagicDNS name and the tailnet's name.
type tailscaledStatusExtra struct {
	AuthURL string
	Self    struct {
		DNSName string
	}
	CurrentTailnet *struct {
		Name string
	}
}

// tailscaledPoll reads the status into the snapshot.
func tailscaledPoll() error {
	out, err := tailscaledRunCLI(8*time.Second, "status", "--json")
	if err != nil {
		return err
	}
	st, err := parseTailscaleStatus(out)
	if err != nil {
		return err
	}
	var extra tailscaledStatusExtra
	_ = json.Unmarshal(out, &extra)
	tailnet := ""
	if extra.CurrentTailnet != nil {
		tailnet = strings.TrimSpace(extra.CurrentTailnet.Name)
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.Backend = st.State
		s.AuthURL = strings.TrimSpace(extra.AuthURL)
		s.IPs = st.IPs
		s.DNSName = strings.TrimSuffix(strings.TrimSpace(extra.Self.DNSName), ".")
		s.Tailnet = tailnet
		s.Peers = st.Peers
		if st.State == "Running" && strings.HasPrefix(s.Detail, "Log in failed") {
			s.Detail = ""
		}
	})
	return nil
}

// tailscaledIPv4 is the first IPv4 address of a list ("" when none).
func tailscaledIPv4(ips []string) string {
	for _, ip := range ips {
		if strings.Count(ip, ".") == 3 {
			return ip
		}
	}
	return ""
}

// tailscaledBackendText says the daemon's state in words.
func tailscaledBackendText(s tailscaledSnapshot) string {
	switch s.Backend {
	case "Running":
		if ip := tailscaledIPv4(s.IPs); ip != "" {
			return "Connected · " + ip
		}
		return "Connected"
	case "NeedsLogin":
		if s.LoggingIn {
			return "Waiting for the sign-in"
		}
		return "Log in to connect"
	case "NeedsMachineAuth":
		return "Waiting for approval in the admin console"
	case "Stopped":
		return "Disconnected"
	case "", "NoState", "Starting":
		return "Connecting…"
	}
	return s.Backend
}

// tailscaledStatusText is the one-line state for the tile, the Settings row
// and the screen's header.
func tailscaledStatusText(s tailscaledSnapshot, want bool) string {
	switch {
	case s.Phase == tailscaledPhaseChecking:
		return "Checking…"
	case s.Phase == tailscaledPhaseInstalling:
		return "Installing…"
	case s.System && s.Phase == tailscaledPhaseRunning:
		return tailscaledBackendText(s)
	case s.System:
		return "Not running"
	case !tailscaledCanInstall() || !s.Installed:
		return "Not installed"
	case !want:
		return "Off"
	case s.Phase == tailscaledPhaseWaitingWifi:
		return "Waiting for Wi-Fi"
	case s.Phase == tailscaledPhaseLowBattery:
		return "Paused · battery low"
	case s.Phase == tailscaledPhaseFailed:
		return "Not running"
	case s.Phase != tailscaledPhaseRunning:
		return "Starting…"
	}
	return tailscaledBackendText(s)
}

// tailscaledConnected reports whether this device is in its tailnet now.
func tailscaledConnected(s tailscaledSnapshot) bool {
	return s.Phase == tailscaledPhaseRunning && s.Backend == "Running"
}

// tailscaledTileValue is the Network tools tile's line and tone, and the
// Settings row's value.
func tailscaledTileValue() (string, toolsHubTone) {
	tailscaledStartWorker()
	s := tailscaledSnapshotNow()
	want := tailscaledWantOn.Load()
	switch {
	case !tailscaledCanInstall() && !s.System:
		if s.Phase == tailscaledPhaseChecking {
			return "Checking…", toolsHubToneNeutral
		}
		return "Install Tailscale for Windows to see it here", toolsHubToneNeutral
	case tailscaledConnected(s):
		if ip := tailscaledIPv4(s.IPs); ip != "" {
			return "On · " + ip, toolsHubToneGood
		}
		return "On", toolsHubToneGood
	case s.Phase == tailscaledPhaseInstalling, s.Phase == tailscaledPhaseStarting:
		return tailscaledStatusText(s, want), toolsHubToneBusy
	case s.Phase == tailscaledPhaseFailed, s.Phase == tailscaledPhaseRunning && s.Backend == "NeedsLogin":
		return tailscaledStatusText(s, want), toolsHubToneWarn
	case !s.System && (!s.Installed || !want):
		return "Off — reach this handheld from anywhere", toolsHubToneNeutral
	}
	return tailscaledStatusText(s, want), toolsHubToneNeutral
}

// ── Smart Home ───────────────────────────────────────────────────────────

// tailscaledSmartHomeSnap is Smart Home's Tailscale row for JukaHub's own
// copy, from the worker's last poll; ok is false when JukaHub runs no
// Tailscale here (smart_home.go then asks a tailscale it finds).
func tailscaledSmartHomeSnap() (tailscaleSnapshot, bool) {
	if !tailscaledOwnsCLI() {
		return tailscaleSnapshot{}, false
	}
	tailscaledNoteViewer()
	tailscaledStartWorker()
	tailscaledKick()
	s := tailscaledSnapshotNow()
	state := s.Backend
	if s.Phase != tailscaledPhaseRunning || state == "" {
		state = tailscaledStatusText(s, tailscaledWantOn.Load())
	}
	return tailscaleSnapshot{
		Installed: true,
		Running:   tailscaledConnected(s),
		State:     state,
		IPs:       s.IPs,
		Peers:     s.Peers,
	}, true
}

// tailscaledSmartHomeSet is Smart Home's toggle for JukaHub's own copy: the
// same switch as the Tailscale screen (signing in happens there). Any
// goroutine: the setting is written on the main thread.
func tailscaledSmartHomeSet(running bool) error {
	tbxPost(func() { tailscaledSetEnabled(appConfig, running) })
	if running && tailscaledSnapshotNow().Backend == "NeedsLogin" {
		return errors.New("log in first: Tools → Network tools → Tailscale")
	}
	return nil
}

// ── Worker ───────────────────────────────────────────────────────────────

// tailscaledWorkerState is the worker's own memory between steps.
type tailscaledWorkerState struct {
	wifiDownAt time.Time
	lowBattery bool
	lastPoll   time.Time
	retryAt    time.Time
}

// tailscaledWorker owns the daemon and the status polls.
func tailscaledWorker() {
	wake := subscribeForegroundWake()
	var w tailscaledWorkerState
	installed := tailscaledInstalled()
	rec := tailscaledReadRecord()
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.Installed = installed
		s.Version = rec.Version
		if s.Phase == tailscaledPhaseChecking {
			s.Phase = tailscaledPhaseOff
		}
	})
	for {
		tailscaledStep(&w)
		interval := tailscaledPollIdle
		if tailscaledSnapshotNow().LoggingIn {
			interval = tailscaledPollLogin
		} else if tailscaledViewed() {
			interval = tailscaledPollPanel
		} else if tailscaledWantOn.Load() && (!tailscaledRunning() || !w.wifiDownAt.IsZero()) {
			// Switched on but waiting (Wi-Fi after boot, the battery, a
			// retry) or Wi-Fi gone: look again soon, so it starts when Wi-Fi
			// comes up instead of up to five minutes later. Steps are cheap;
			// the status poll keeps its own pace.
			interval = tailscaledPollWait
		}
		timer := time.NewTimer(interval)
		select {
		case <-tailscaledKickCh:
		case <-wake:
		case fn := <-tailscaledActions:
			fn()
		case <-timer.C:
		}
		timer.Stop()
	}
}

// tailscaledBatteryLow applies the battery rule with its hysteresis.
func tailscaledBatteryLow(w *tailscaledWorkerState) bool {
	pct := getBatteryPercent()
	charging := pct < 0 || getBatteryCharging()
	if w.lowBattery {
		w.lowBattery = !charging && pct < tailscaledBatteryResume
	} else {
		w.lowBattery = !charging && pct < tailscaledLowBattery
	}
	return w.lowBattery
}

// tailscaledStep brings the daemon to where the switch and the device say it
// should be, then reads its status when it is due.
func tailscaledStep(w *tailscaledWorkerState) {
	if tailscaledQuitting.Load() {
		return
	}
	if !tailscaledCanInstall() {
		tailscaledStepSystem(w)
		return
	}
	installed := tailscaledInstalled()
	want := tailscaledWantOn.Load()
	wifiUp := wifiLinkIsUp()
	if wifiUp {
		w.wifiDownAt = time.Time{}
	} else if w.wifiDownAt.IsZero() {
		w.wifiDownAt = time.Now()
	}
	wifiGone := !w.wifiDownAt.IsZero() && time.Since(w.wifiDownAt) > tailscaledWifiGrace
	low := tailscaledBatteryLow(w)
	running := tailscaledRunning()
	phase := tailscaledSnapshotNow().Phase

	switch {
	case phase == tailscaledPhaseInstalling || tailscaledInstalling.Load():
		return
	case !installed:
		if running {
			tailscaledStop(3 * time.Second)
		}
		tailscaledUpdate(func(s *tailscaledSnapshot) {
			// An install the screen began since the phase was read keeps its
			// phase, or the row would offer a second install.
			if s.Phase != tailscaledPhaseInstalling {
				s.Installed = false
				s.Phase = tailscaledPhaseNotInstalled
				tailscaledClearStatus(s)
			}
		})
		return
	case !want || wifiGone || low:
		if running {
			tailscaledCancelLogin()
			tailscaledStop(3 * time.Second)
		}
		next := tailscaledPhaseOff
		if want && low {
			next = tailscaledPhaseLowBattery
		} else if want {
			next = tailscaledPhaseWaitingWifi
		}
		tailscaledUpdate(func(s *tailscaledSnapshot) {
			s.Installed = true
			s.Phase = next
			tailscaledClearStatus(s)
		})
		return
	}

	if running && tailscaledRestart.Load() && !gameScreenSuspended() {
		logInfo("tailscale", "restarting tailscaled for the updated program")
		tailscaledCancelLogin()
		tailscaledStop(3 * time.Second)
		running = false
	}
	if !running {
		// A fresh start runs whatever program is installed now.
		tailscaledRestart.Store(false)
		if !wifiUp {
			tailscaledUpdate(func(s *tailscaledSnapshot) {
				s.Phase = tailscaledPhaseWaitingWifi
				tailscaledClearStatus(s)
			})
			return
		}
		if time.Now().Before(w.retryAt) {
			return
		}
		tailscaledProc.mu.Lock()
		quick := !tailscaledProc.startedAt.IsZero() && time.Since(tailscaledProc.startedAt) < time.Minute
		if quick {
			tailscaledProc.fails++
		} else {
			tailscaledProc.fails = 0
		}
		fails := tailscaledProc.fails
		tailscaledProc.mu.Unlock()
		if fails >= 3 {
			tailscaledUpdate(func(s *tailscaledSnapshot) {
				s.Phase = tailscaledPhaseFailed
				s.Detail = "tailscaled keeps stopping — see " + filepath.Join(tailscaledHome(), "tailscaled.log")
				tailscaledClearStatus(s)
			})
			w.retryAt = time.Now().Add(10 * time.Minute)
			tailscaledProc.mu.Lock()
			tailscaledProc.fails = 0
			tailscaledProc.startedAt = time.Time{}
			tailscaledProc.mu.Unlock()
			return
		}
		tailscaledUpdate(func(s *tailscaledSnapshot) {
			s.Installed = true
			s.Phase = tailscaledPhaseStarting
			s.Detail = ""
		})
		if err := tailscaledStart(); err != nil {
			logWarn("tailscale", "start failed: %v", err)
			tailscaledUpdate(func(s *tailscaledSnapshot) {
				s.Phase = tailscaledPhaseFailed
				s.Detail = firstLine(err.Error())
				tailscaledClearStatus(s)
			})
			w.retryAt = time.Now().Add(time.Minute)
			return
		}
		w.lastPoll = time.Time{}
	}
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		if s.Phase != tailscaledPhaseRunning {
			s.Phase = tailscaledPhaseRunning
			s.Detail = ""
		}
	})

	every := tailscaledPollIdle
	switch {
	case tailscaledSnapshotNow().LoggingIn:
		every = tailscaledPollLogin
	case tailscaledViewed():
		every = tailscaledPollPanel
	case backgroundPaused() && !w.lastPoll.IsZero():
		// Nobody reads the status behind a game or a sleeping screen.
		return
	}
	if w.lastPoll.IsZero() || time.Since(w.lastPoll) >= every-100*time.Millisecond {
		w.lastPoll = time.Now()
		if err := tailscaledPoll(); err != nil {
			logInfo("tailscale", "status: %v", err)
		}
	}
}

// tailscaledStepSystem reads a PC's own Tailscale: once, then while someone
// looks.
func tailscaledStepSystem(w *tailscaledWorkerState) {
	if _, _, ok := tailscaleCLI(); !ok {
		tailscaledUpdate(func(s *tailscaledSnapshot) {
			s.System = false
			s.Installed = false
			s.Phase = tailscaledPhaseNotInstalled
			tailscaledClearStatus(s)
		})
		return
	}
	if !w.lastPoll.IsZero() && (!tailscaledViewed() || time.Since(w.lastPoll) < tailscaledPollPanel-100*time.Millisecond) {
		return
	}
	w.lastPoll = time.Now()
	err := tailscaledPoll()
	tailscaledUpdate(func(s *tailscaledSnapshot) {
		s.System = true
		s.Installed = true
		if err != nil {
			s.Phase = tailscaledPhaseFailed
			s.Detail = firstLine(err.Error())
			tailscaledClearStatus(s)
			return
		}
		s.Phase = tailscaledPhaseRunning
		s.Detail = ""
	})
}
