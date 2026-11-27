package main

// syncthing.go — Syncthing built in, for save files.
//
// The Save Manager used to stop at "install Syncthing yourself and add these
// folders with these IDs": a PC-side config dance most people never finished.
// Now JukaHub installs the official Syncthing release, runs its own instance and
// shares every save folder it knows; the other side (Syncthing-Fork on Android,
// Syncthing on a PC or Mac, or a second JukaHub) only adds this device's ID —
// shown as a QR code — and accepts the folders it is offered.
//
// The rules it keeps:
//
//   - Install: the newest github.com/syncthing/syncthing release for this
//     platform (linux-arm64 on the handheld, windows-amd64 on a PC), checked
//     against the SHA-256 the release lists in sha256sum.txt.asc. A release
//     without that line installs nothing. Only the syncthing binary is taken
//     from the archive, into the tools folder, beside the other helpers.
//   - Its own instance, never the user's: `syncthing serve` with its own home
//     (<data>/syncthing), its GUI/REST on 127.0.0.1 only, on 8386 (setting
//     syncthingGuiPort), with an API key JukaHub generated (0600 file in that
//     home) and a random GUI password nobody knows, so the web GUI is closed
//     even to a browser on the same machine. A Syncthing already answering on
//     8384 is someone else's: it is left alone, reported, and this instance
//     listens on 22100 instead of 22000 so the two do not fight over the port.
//   - Folders: the Save Manager's own discovery (saveManagerRoots) and folder
//     IDs (prepareSaveSync, "jukahub-<slug>"), so the IDs a user may already
//     have typed into another Syncthing stay the same. Every folder is shared
//     with every paired device; two JukaHub devices therefore sync matching
//     folders with no further step. FAT-friendly settings (ignorePerms, a 2 s
//     mtime window) and old versions kept outside the save folder, under the
//     home's versions/<id>, so snapshots never copy them.
//   - Never while a game runs: every device is paused (REST) the moment a game
//     takes the screen, and resumed with a rescan as soon as JukaHub is back,
//     so a save made in-game goes out right away. Also paused below 15 %
//     battery (not charging) and without Wi-Fi; on the handheld the process is
//     not started without Wi-Fi and is stopped after five minutes without it.
//     It runs at nice 19 (below-normal priority on Windows).
//   - Global discovery and relays stay on, so a phone on mobile data can pair
//     and sync; "LAN only" turns both off.
//
// One goroutine (syncthingWorker) owns the process and every REST call. The UI
// (syncthing_ui.go) only reads syncthingSnap and posts actions, so nothing here
// runs on the render thread. docs/SAVE_MANAGER.md → Syncthing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	syncthingRepo = "syncthing/syncthing"
	// syncthingDefaultPort is JukaHub's GUI/REST port. 8384 is Syncthing's
	// default and belongs to any Syncthing the user runs themselves.
	syncthingDefaultPort = 8386
	syncthingForeignPort = 8384
	// syncthingAltListenPort is the sync port used when another Syncthing on
	// this machine already has 22000.
	syncthingAltListenPort = 22100

	syncthingEnabledKey = "syncthingEnabled"
	syncthingLANOnlyKey = "syncthingLanOnly"
	syncthingPortKey    = "syncthingGuiPort"

	// Battery: pause below the first level unless charging, resume at the
	// second, so a level hovering at the line does not flap.
	syncthingLowBattery    = 15
	syncthingBatteryResume = 20
	// syncthingWifiGrace is how long the handheld keeps the process without
	// Wi-Fi before stopping it (a dropped link usually comes back).
	syncthingWifiGrace = 5 * time.Minute

	syncthingPollIdle  = 30 * time.Second
	syncthingPollPanel = 3 * time.Second
	syncthingStartWait = 45 * time.Second
	// syncthingConflictEvery spaces out the walk that counts conflict files.
	syncthingConflictEvery = 2 * time.Minute
	syncthingLogMax        = 1 << 20
)

// syncthingPhase is where the managed instance is.
type syncthingPhase int

const (
	syncthingPhaseChecking syncthingPhase = iota
	syncthingPhaseNotInstalled
	syncthingPhaseInstalling
	syncthingPhaseOff
	syncthingPhaseWaitingWifi
	syncthingPhaseStarting
	syncthingPhaseRunning
	syncthingPhaseFailed
)

// syncthingDevice is a paired device; syncthingPending one asking to pair.
type syncthingDevice struct {
	ID        string
	Name      string
	Connected bool
}

type syncthingPending struct {
	ID      string
	Name    string
	Address string
}

// syncthingSnapshot is what the UI and the Power tools row read. The worker
// replaces it under syncthingSnap.mu; readers take a copy.
type syncthingSnapshot struct {
	Installed bool
	Version   string // the release tag that was installed
	Phase     syncthingPhase
	Detail    string // why it is failed / waiting, in words
	Paused    string // "", "game", "battery", "wifi"
	MyID      string
	Port      int
	Devices   []syncthingDevice
	Pending   []syncthingPending
	Nearby    []string
	Folders   []saveSyncFolder
	// Completion is this device's completion over all folders in percent
	// (/rest/db/completion without a device is the local one), i.e. how much
	// it still has to download; -1 when nothing was read yet.
	Completion  float64
	NeedBytes   int64
	Conflicts   int
	OtherOffers int  // folder offers this device has no save folder for
	Foreign     bool // another Syncthing answers on 8384
}

var syncthingSnap struct {
	mu sync.Mutex
	s  syncthingSnapshot
}

// syncthingSnapshotNow returns a copy of the current state.
func syncthingSnapshotNow() syncthingSnapshot {
	syncthingSnap.mu.Lock()
	defer syncthingSnap.mu.Unlock()
	s := syncthingSnap.s
	s.Devices = append([]syncthingDevice(nil), s.Devices...)
	s.Pending = append([]syncthingPending(nil), s.Pending...)
	s.Nearby = append([]string(nil), s.Nearby...)
	s.Folders = append([]saveSyncFolder(nil), s.Folders...)
	return s
}

// syncthingUpdate edits the snapshot under its lock.
func syncthingUpdate(fn func(s *syncthingSnapshot)) {
	syncthingSnap.mu.Lock()
	fn(&syncthingSnap.s)
	syncthingSnap.mu.Unlock()
}

// The settings the worker needs, mirrored from config.Variables.Custom on the
// main thread (the Custom map is the main thread's).
var (
	syncthingWantOn    atomic.Bool
	syncthingLANOnly   atomic.Bool
	syncthingPortPref  atomic.Int32
	syncthingPanelOpen atomic.Bool
	// syncthingQuitting is set by the exit hook. The instance's exit wakes
	// the worker, which would otherwise start a new one that outlives JukaHub.
	syncthingQuitting atomic.Bool
)

// syncthingAction is a UI request the worker runs with a live REST client.
type syncthingAction struct {
	name string
	fn   func(ctx context.Context, api syncthingAPI) (string, error)
}

var (
	syncthingKickCh   = make(chan struct{}, 1)
	syncthingActions  = make(chan syncthingAction, 8)
	syncthingWorkOnce sync.Once
)

// syncthingKick wakes the worker: a game started, a setting changed. It never
// blocks and is a no-op before the worker exists.
func syncthingKick() {
	select {
	case syncthingKickCh <- struct{}{}:
	default:
	}
}

// syncthingPost queues an action for the worker. A full queue is reported
// instead of waiting on the render thread.
func syncthingPost(name string, fn func(ctx context.Context, api syncthingAPI) (string, error)) bool {
	syncthingStartWorker()
	select {
	case syncthingActions <- syncthingAction{name: name, fn: fn}:
		syncthingKick()
		return true
	default:
		ShowToast("Syncthing is busy — try again in a moment", ToastKindInfo)
		return false
	}
}

// ── Settings ─────────────────────────────────────────────────────────────

// syncthingEnabled reports the user's On/Off choice. Off by default: it is a
// download and a background process the user asks for.
func syncthingEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, syncthingEnabledKey, false)
}

func syncthingLANOnlySetting(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, syncthingLANOnlyKey, false)
}

// syncthingPortSetting is the GUI/REST port: the setting when it is a sane
// unprivileged port that is not the foreign default, else 8386.
func syncthingPortSetting(config *Config) int {
	if config == nil {
		return syncthingDefaultPort
	}
	n, err := strconv.Atoi(strings.TrimSpace(customStringValue(config.Variables.Custom, syncthingPortKey)))
	if err != nil || n < 1024 || n > 65535 || n == syncthingForeignPort {
		return syncthingDefaultPort
	}
	return n
}

// syncthingSetSetting writes one bool setting and saves the user config. Main
// thread only.
func syncthingSetSetting(config *Config, key string, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[key] = boolString(on)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// syncthingBoot mirrors the settings and starts the worker when Syncthing is
// on. Main thread (a deferred startup task); the worker does the rest.
func syncthingBoot(config *Config) {
	syncthingWantOn.Store(syncthingEnabled(config))
	syncthingLANOnly.Store(syncthingLANOnlySetting(config))
	syncthingPortPref.Store(int32(syncthingPortSetting(config)))
	// "Also sync" folders and the web GUI choice (syncthing_more.go).
	syncthingExtrasBoot(config)
	if syncthingWantOn.Load() {
		syncthingStartWorker()
	}
}

func init() { bootOnShutdown(syncthingShutdown) }

// ── Paths ────────────────────────────────────────────────────────────────

// syncthingHome is the managed instance's own config/database folder.
func syncthingHome() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "syncthing")
	}
	return filepath.Join("data", "syncthing")
}

func syncthingBinaryPath() string {
	dir := toolboxToolsDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	name := "syncthing"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

// syncthingInstalled reports whether the binary is present (stat only).
func syncthingInstalled() bool {
	p := syncthingBinaryPath()
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// syncthingInstallRecord is what was installed, kept in the home.
type syncthingInstallRecord struct {
	Tag    string `json:"tag"`
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
	At     string `json:"installedAt"`
}

func syncthingRecordPath() string { return filepath.Join(syncthingHome(), "jukahub-install.json") }

func syncthingReadRecord() syncthingInstallRecord {
	var rec syncthingInstallRecord
	if data, err := os.ReadFile(syncthingRecordPath()); err == nil {
		_ = json.Unmarshal(data, &rec)
	}
	return rec
}

// syncthingAPIKey returns the instance's API key, creating it (0600) on first
// use. The key is only ever sent to 127.0.0.1.
func syncthingAPIKey() (string, error) {
	file := filepath.Join(syncthingHome(), "jukahub-apikey")
	if data, err := os.ReadFile(file); err == nil {
		if key := strings.TrimSpace(string(data)); len(key) >= 32 {
			return key, nil
		}
	}
	if err := os.MkdirAll(syncthingHome(), 0o700); err != nil {
		return "", err
	}
	key, err := syncthingRandomHex(24)
	if err != nil {
		return "", err
	}
	if err := AtomicWrite(file, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func syncthingRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ── Install ──────────────────────────────────────────────────────────────

// syncthingAssetTokens are the stable parts of the release archive name for a
// build target (syncthing-linux-arm64-v2.0.11.tar.gz, …).
func syncthingAssetTokens(goos, goarch string) ([][]string, error) {
	switch goos + "/" + goarch {
	case "linux/arm64":
		return [][]string{{"syncthing-linux-arm64-", ".tar.gz"}}, nil
	case "linux/amd64":
		return [][]string{{"syncthing-linux-amd64-", ".tar.gz"}}, nil
	case "linux/arm":
		return [][]string{{"syncthing-linux-arm-", ".tar.gz"}}, nil
	case "windows/amd64":
		return [][]string{{"syncthing-windows-amd64-", ".zip"}}, nil
	case "windows/arm64":
		return [][]string{{"syncthing-windows-arm64-", ".zip"}}, nil
	case "darwin/arm64", "darwin/amd64":
		return [][]string{{"syncthing-macos-universal-", ".zip"}, {"syncthing-macos-" + goarch + "-", ".zip"}}, nil
	}
	return nil, fmt.Errorf("Syncthing publishes no build for %s/%s", goos, goarch)
}

// syncthingChecksumAsset finds the release's SHA-256 list. Syncthing signs it
// (sha256sum.txt.asc, a clear-signed text); the hash lines read as usual.
func syncthingChecksumAsset(assets []releaseAsset) (releaseAsset, bool) {
	for _, want := range []string{"sha256sum.txt.asc", "sha256sum.txt", "sha256sums.txt"} {
		for _, a := range assets {
			if strings.EqualFold(a.Name, want) {
				return a, true
			}
		}
	}
	return releaseAsset{}, false
}

// syncthingInstallStart begins an install from the UI: the install strip is
// claimed here, on the main thread, and the work runs on a goroutine.
func syncthingInstallStart() {
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi to download Syncthing", ToastKindInfo)
		return
	}
	syncthingUpdate(func(s *syncthingSnapshot) {
		s.Phase = syncthingPhaseInstalling
		s.Detail = ""
	})
	run, ctx := installBarBeginRun("Syncthing")
	go func() {
		err := syncthingInstall(ctx)
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
			logWarn("syncthing", "install failed: %v", err)
			syncthingUpdate(func(s *syncthingSnapshot) {
				s.Phase = syncthingPhaseFailed
				s.Detail = "Install failed: " + firstLine(err.Error())
			})
			ShowToast("Could not install Syncthing: "+firstLine(err.Error()), ToastKindError)
			syncthingKick()
			return
		}
		installBarDoneRun(run)
		syncthingUpdate(func(s *syncthingSnapshot) {
			s.Installed = true
			s.Version = syncthingReadRecord().Tag
			s.Phase = syncthingPhaseOff
		})
		ShowToast("Syncthing installed — starting", ToastKindSuccess)
		syncthingStartWorker()
		syncthingKick()
	}()
}

// syncthingInstall downloads the release archive, verifies it against the
// published SHA-256 and places the binary in the tools folder. A copy that is
// already there is replaced only after the new one is verified and unpacked.
func syncthingInstall(ctx context.Context) error {
	groups, err := syncthingAssetTokens(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	target := syncthingBinaryPath()
	if target == "" {
		return errors.New("tools directory unavailable")
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, PermDir); err != nil {
		return fmt.Errorf("create tools directory: %w", err)
	}
	installBarPhase(installPhaseResolving)
	rel, err := toolboxReleaseFetch(ctx, syncthingRepo)
	if err != nil {
		return fmt.Errorf("find the Syncthing release: %w", err)
	}
	asset, ok := pickToolAsset(rel.Assets, groups)
	if !ok {
		return fmt.Errorf("Syncthing %s has no download for %s/%s", rel.Tag, runtime.GOOS, runtime.GOARCH)
	}
	sumAsset, ok := syncthingChecksumAsset(rel.Assets)
	if !ok {
		return fmt.Errorf("Syncthing %s publishes no SHA-256 list; nothing was installed", rel.Tag)
	}
	logInfo("syncthing", "installing %s %s from %s", rel.Tag, asset.Name, asset.URL)

	// Staged in the tools folder: the handheld's /tmp is a small tmpfs.
	tmp, err := os.CreateTemp(dir, ".syncthing-*"+toolboxArchiveExt(asset.Name))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, asset.URL, tmpName, installBarProgress); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	installBarPhase(installPhaseVerify)
	got, err := hashFile(tmpName)
	if err != nil {
		return fmt.Errorf("hash the download: %w", err)
	}
	list, err := toolboxFetchSmall(ctx, sumAsset.URL)
	if err != nil {
		return fmt.Errorf("download the published checksum: %w", err)
	}
	want, ok := toolboxParseChecksum(string(list), asset.Name, false)
	if !ok {
		return fmt.Errorf("%s does not list %s; nothing was installed", sumAsset.Name, asset.Name)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("the download does not match the SHA-256 Syncthing publishes for %s", asset.Name)
	}

	installBarPhase(installPhaseExtract)
	stage, err := os.MkdirTemp(dir, ".syncthing-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	base := filepath.Base(target)
	found, err := extractToolFilesCtx(ctx, tmpName, stage, []string{base}, nil)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	if len(found) == 0 {
		return fmt.Errorf("the archive has no %s", base)
	}
	staged := filepath.Join(stage, strings.ToLower(base))
	if runtime.GOOS != "windows" {
		_ = os.Chmod(staged, PermExec)
	}
	// A running instance keeps its old binary (renamed aside) until restart.
	if err := toolboxPlace(staged, target, syncthingInstalled()); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if err := os.MkdirAll(syncthingHome(), 0o700); err != nil {
		return err
	}
	rec := syncthingInstallRecord{Tag: rel.Tag, Asset: asset.Name, SHA256: got, At: time.Now().UTC().Format(time.RFC3339)}
	if data, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = AtomicWrite(syncthingRecordPath(), data, 0o600)
	}
	logInfo("syncthing", "installed %s at %s (sha256 verified)", rel.Tag, target)
	return nil
}

// ── REST client ──────────────────────────────────────────────────────────

// syncthingAPI talks to the managed instance on loopback.
type syncthingAPI struct {
	port int
	key  string
}

// syncthingHTTP never uses a proxy: every call goes to 127.0.0.1.
var syncthingHTTP = &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}}

func (a syncthingAPI) base() string { return "http://127.0.0.1:" + strconv.Itoa(a.port) }

// call sends one request and decodes the JSON answer into out (when non-nil).
func (a syncthingAPI) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, a.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", a.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := syncthingHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// syncthingHealthy reports whether some Syncthing answers on port (no key).
func syncthingHealthy(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/rest/noauth/health", nil)
	if err != nil {
		return false
	}
	resp, err := syncthingHTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

// syncthingOurs reports whether the instance on port takes our API key.
func syncthingOurs(api syncthingAPI) bool {
	var st struct {
		MyID string `json:"myID"`
	}
	return api.call(context.Background(), http.MethodGet, "/rest/system/status", nil, &st) == nil && st.MyID != ""
}

// ── Process ──────────────────────────────────────────────────────────────

// syncthingProc is the running instance: a child JukaHub started, or one an
// earlier JukaHub left running and this one adopted (cmd nil).
var syncthingProc struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan struct{}
	adopted bool
	api     syncthingAPI
	// fails counts exits within a minute of starting; three stop the retries.
	fails     int
	startedAt time.Time
}

// syncthingCappedLog is the child's stdout/stderr, cut off at syncthingLogMax
// so a chatty instance cannot fill the card.
type syncthingCappedLog struct {
	mu sync.Mutex
	f  *os.File
	n  int64
}

func (w *syncthingCappedLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil || w.n >= syncthingLogMax {
		return len(p), nil
	}
	// A write error (a full card) only loses log lines; the child must never
	// see its stdout fail.
	n, _ := w.f.Write(p)
	w.n += int64(n)
	return len(p), nil
}

// syncthingRunningLocked reports whether the managed instance is up as far as
// the process bookkeeping knows. syncthingProc.mu is held.
func syncthingRunningLocked() bool {
	if syncthingProc.adopted {
		return true
	}
	if syncthingProc.cmd == nil || syncthingProc.done == nil {
		return false
	}
	select {
	case <-syncthingProc.done:
		return false
	default:
		return true
	}
}

// syncthingListenAddresses is the sync listen list for the options.
func syncthingListenAddresses(lanOnly, foreign bool) []string {
	port := 22000
	if foreign {
		port = syncthingAltListenPort
	}
	if !lanOnly && !foreign {
		return []string{"default"}
	}
	out := []string{fmt.Sprintf("tcp://0.0.0.0:%d", port), fmt.Sprintf("quic://0.0.0.0:%d", port)}
	if !lanOnly {
		out = append(out, "dynamic+https://relays.syncthing.net/endpoint")
	}
	return out
}

// syncthingStart starts (or adopts) the instance and waits for its REST API.
// Worker goroutine only.
func syncthingStart(ctx context.Context) (syncthingAPI, error) {
	key, err := syncthingAPIKey()
	if err != nil {
		return syncthingAPI{}, fmt.Errorf("API key: %w", err)
	}
	foreign := syncthingHealthy(syncthingForeignPort)
	syncthingUpdate(func(s *syncthingSnapshot) { s.Foreign = foreign })
	// Our port, or the first free one after it: a port held by something that
	// refuses our key is somebody else's.
	port := int(syncthingPortPref.Load())
	if port == 0 {
		port = syncthingDefaultPort
	}
	chosen := 0
	for p := port; p < port+10; p++ {
		api := syncthingAPI{port: p, key: key}
		if syncthingHealthy(p) {
			if syncthingOurs(api) {
				logInfo("syncthing", "adopting the instance already running on port %d", p)
				syncthingProc.mu.Lock()
				syncthingProc.adopted = true
				syncthingProc.api = api
				syncthingProc.mu.Unlock()
				return api, nil
			}
			continue
		}
		chosen = p
		break
	}
	if chosen == 0 {
		return syncthingAPI{}, fmt.Errorf("ports %d-%d are all in use", port, port+9)
	}
	bin := syncthingBinaryPath()
	home := syncthingHome()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return syncthingAPI{}, err
	}
	// Double-dash flags only: v2 dropped the single-dash spellings, and v1
	// reads both.
	cmd := hiddenCommand(bin, "serve",
		"--home="+home,
		"--no-browser",
		"--no-restart",
		"--no-upgrade",
		// Loopback, or every interface while "Web GUI on this network" is on.
		"--gui-address="+syncthingGuiHost()+":"+strconv.Itoa(chosen),
	)
	cmd.Dir = home
	// The API key goes in the environment, not argv: a command line is
	// readable by every user of the machine (ps, Task Manager).
	env := append(syncthingChildEnv(), "STGUIAPIKEY="+key, "STNODEFAULTFOLDER=1", "STNOUPGRADE=1", "STNORESTART=1")
	if IsTrimuiDevice() {
		// Two cores are plenty for save files and leave the rest to the UI.
		env = append(env, "GOMAXPROCS=2")
	}
	cmd.Env = env
	logFile, _ := os.OpenFile(filepath.Join(home, "syncthing.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	out := &syncthingCappedLog{f: logFile}
	cmd.Stdout = out
	cmd.Stderr = out
	syncthingProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return syncthingAPI{}, fmt.Errorf("start: %w", err)
	}
	syncthingLowerPriority(cmd)
	done := make(chan struct{})
	api := syncthingAPI{port: chosen, key: key}
	syncthingProc.mu.Lock()
	syncthingProc.cmd = cmd
	syncthingProc.done = done
	syncthingProc.adopted = false
	syncthingProc.api = api
	syncthingProc.startedAt = time.Now()
	syncthingProc.mu.Unlock()
	if syncthingQuitting.Load() {
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
		logInfo("syncthing", "instance exited: %v", err)
		syncthingKick()
	}()
	logInfo("syncthing", "started pid %d, GUI/REST on 127.0.0.1:%d (foreign Syncthing on 8384: %v)", cmd.Process.Pid, chosen, foreign)

	deadline := time.Now().Add(syncthingStartWait)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return syncthingAPI{}, fmt.Errorf("Syncthing stopped while starting — see %s", filepath.Join(home, "syncthing.log"))
		case <-ctx.Done():
			return syncthingAPI{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if syncthingOurs(api) {
			// Syncthing lowers its own priority at start-up; this puts it
			// back at the bottom once it has.
			syncthingLowerPriority(cmd)
			return api, nil
		}
	}
	syncthingKill()
	return syncthingAPI{}, fmt.Errorf("Syncthing did not answer within %s", syncthingStartWait)
}

// syncthingChildEnv is JukaHub's environment without the ST* variables a
// user's own Syncthing may have set: STCONFDIR beside --home is refused at
// start, and STMONITORED or STGUIADDRESS would change how this one runs.
func syncthingChildEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if syncthingEnvKey(key) {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// syncthingEnvKey matches Syncthing's variable names: "ST" and capitals.
func syncthingEnvKey(key string) bool {
	key = strings.ToUpper(key)
	if len(key) < 4 || !strings.HasPrefix(key, "ST") {
		return false
	}
	for _, r := range key[2:] {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// syncthingStop asks the instance to shut down and kills it if it does not
// go within budget.
func syncthingStop(budget time.Duration) {
	syncthingProc.mu.Lock()
	running := syncthingRunningLocked()
	api := syncthingProc.api
	done := syncthingProc.done
	adopted := syncthingProc.adopted
	syncthingProc.mu.Unlock()
	if !running {
		return
	}
	// A stop JukaHub asked for is not a crash for the restart counter.
	syncthingProc.mu.Lock()
	syncthingProc.startedAt = time.Time{}
	syncthingProc.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), budget/2)
	err := api.call(ctx, http.MethodPost, "/rest/system/shutdown", nil, nil)
	cancel()
	if err != nil {
		logInfo("syncthing", "shutdown request: %v", err)
	}
	if adopted || done == nil {
		syncthingProc.mu.Lock()
		syncthingProc.adopted = false
		syncthingProc.mu.Unlock()
		return
	}
	select {
	case <-done:
	case <-time.After(budget / 2):
		syncthingKill()
	}
}

func syncthingKill() {
	syncthingProc.mu.Lock()
	cmd := syncthingProc.cmd
	syncthingProc.mu.Unlock()
	// The whole tree: `syncthing serve` is a monitor process that runs the
	// real instance as its child, and that child would outlive the monitor.
	syncthingKillTree(cmd)
}

// syncthingShutdown is the app-exit hook: the instance belongs to JukaHub and
// stops with it (pausing for games only works while JukaHub runs).
func syncthingShutdown() {
	syncthingQuitting.Store(true)
	syncthingStop(1200 * time.Millisecond)
}

// ── Configuration over REST ──────────────────────────────────────────────

// syncthingConfigure applies JukaHub's options, closes the web GUI, names the
// device and shares the save folders. It runs after every start; each step is
// idempotent.
func syncthingConfigure(ctx context.Context, api syncthingAPI) error {
	foreign := syncthingSnapshotNow().Foreign
	if err := syncthingApplyOptions(ctx, api, syncthingLANOnly.Load(), foreign); err != nil {
		return err
	}
	var gui map[string]any
	if err := api.call(ctx, http.MethodGet, "/rest/config/gui", nil, &gui); err == nil {
		if user, _ := gui["user"].(string); user == "" {
			// A password nobody knows: the API key is how JukaHub gets in, and
			// the browser GUI stays shut even to other users of a PC.
			pw, err := syncthingRandomHex(24)
			if err == nil {
				_ = api.call(ctx, http.MethodPatch, "/rest/config/gui", map[string]any{"user": "jukahub", "password": pw}, nil)
			}
		}
	}
	// The shown login while the GUI is open on the network (syncthing_more.go).
	syncthingApplyWebGUI(ctx, api)
	var st struct {
		MyID string `json:"myID"`
	}
	if err := api.call(ctx, http.MethodGet, "/rest/system/status", nil, &st); err != nil {
		return err
	}
	var self map[string]any
	if err := api.call(ctx, http.MethodGet, "/rest/config/devices/"+url.PathEscape(st.MyID), nil, &self); err == nil {
		if name, _ := self["name"].(string); !strings.HasPrefix(name, "JukaHub") {
			_ = api.call(ctx, http.MethodPatch, "/rest/config/devices/"+url.PathEscape(st.MyID),
				map[string]any{"name": "JukaHub · " + deviceDisplayName(DetectDevice())}, nil)
		}
	}
	syncthingUpdate(func(s *syncthingSnapshot) { s.MyID = st.MyID })
	_, err := syncthingShareFolders(ctx, api, st.MyID)
	return err
}

// syncthingApplyOptions sets the instance-wide options JukaHub relies on.
func syncthingApplyOptions(ctx context.Context, api syncthingAPI, lanOnly, foreign bool) error {
	opts := map[string]any{
		"urAccepted":            -1, // no usage-report prompt nobody can answer
		"crashReportingEnabled": false,
		"autoUpgradeIntervalH":  0, // JukaHub installs updates, not the binary itself
		"startBrowser":          false,
		"globalAnnounceEnabled": !lanOnly,
		"relaysEnabled":         !lanOnly,
		"natEnabled":            !lanOnly,
		"listenAddresses":       syncthingListenAddresses(lanOnly, foreign),
	}
	if IsTrimuiDevice() {
		opts["maxFolderConcurrency"] = 1
	}
	return api.call(ctx, http.MethodPatch, "/rest/config/options", opts, nil)
}

// syncthingFolderObject is a new folder's full configuration. Every field
// that matters is set: an older Syncthing fills unset ones with zero values,
// not with the GUI's defaults.
func syncthingFolderObject(f saveSyncFolder, devices []string) map[string]any {
	devs := make([]map[string]any, 0, len(devices))
	for _, id := range devices {
		devs = append(devs, map[string]any{"deviceID": id})
	}
	return map[string]any{
		"id":               f.FolderID,
		"label":            f.Name + " (JukaHub)",
		"path":             f.Path,
		"type":             "sendreceive",
		"devices":          devs,
		"rescanIntervalS":  3600,
		"fsWatcherEnabled": true,
		"fsWatcherDelayS":  10,
		"ignorePerms":      true, // SD cards are FAT/exFAT
		"autoNormalize":    true,
		"modTimeWindowS":   2, // FAT keeps times to two seconds
		"maxConflicts":     10,
		"markerName":       ".stfolder",
		"minDiskFree":      map[string]any{"value": 1, "unit": "%"},
		// Replaced files go to the home, not a .stversions folder inside the
		// save folder that every snapshot would then copy.
		"versioning": map[string]any{
			"type":             "trashcan",
			"params":           map[string]string{"cleanoutDays": "30"},
			"cleanupIntervalS": 3600,
			"fsPath":           filepath.Join(syncthingHome(), "versions", f.FolderID),
			"fsType":           "basic",
		},
	}
}

// syncthingRemoteDevices lists the configured devices other than this one.
func syncthingRemoteDevices(ctx context.Context, api syncthingAPI, myID string) ([]map[string]any, error) {
	var devs []map[string]any
	if err := api.call(ctx, http.MethodGet, "/rest/config/devices", nil, &devs); err != nil {
		return nil, err
	}
	out := devs[:0]
	for _, d := range devs {
		if id, _ := d["deviceID"].(string); id != "" && id != myID {
			out = append(out, d)
		}
	}
	return out, nil
}

// syncthingShareFolders makes every save folder a Syncthing folder shared with
// every paired device. Paths follow the card (a folder that moved is updated);
// folders that vanished are left configured, so a card swap cannot unshare.
func syncthingShareFolders(ctx context.Context, api syncthingAPI, myID string) (int, error) {
	manifest, ok := syncthingSaveFolders()
	// The "Also sync" folders are shared the same way (syncthing_more.go).
	extras := syncthingExtraFolders()
	if !ok && len(extras) == 0 {
		syncthingUpdate(func(s *syncthingSnapshot) { s.Folders = nil })
		return 0, nil // no save folders yet: nothing to share, not a failure
	}
	devs, err := syncthingRemoteDevices(ctx, api, myID)
	if err != nil {
		return 0, err
	}
	remote := make([]string, 0, len(devs))
	for _, d := range devs {
		id, _ := d["deviceID"].(string)
		remote = append(remote, id)
	}
	var current []map[string]any
	if err := api.call(ctx, http.MethodGet, "/rest/config/folders", nil, &current); err != nil {
		return 0, err
	}
	byID := make(map[string]map[string]any, len(current))
	for _, f := range current {
		if id, _ := f["id"].(string); id != "" {
			byID[id] = f
		}
	}
	syncthingPruneExtras(ctx, api, byID, extras)
	for _, f := range syncthingWithExtras(manifest.Folders, extras) {
		have, ok := byID[f.FolderID]
		if !ok {
			if err := api.call(ctx, http.MethodPost, "/rest/config/folders", syncthingFolderObject(f, remote), nil); err != nil {
				return 0, fmt.Errorf("share %s: %w", f.Name, err)
			}
			continue
		}
		changed := false
		if p, _ := have["path"].(string); filepath.Clean(p) != filepath.Clean(f.Path) {
			have["path"] = f.Path
			changed = true
		}
		list, _ := have["devices"].([]any)
		present := map[string]bool{}
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				id, _ := m["deviceID"].(string)
				present[id] = true
			}
		}
		for _, id := range remote {
			if !present[id] {
				list = append(list, map[string]any{"deviceID": id})
				changed = true
			}
		}
		if changed {
			have["devices"] = list
			if err := api.call(ctx, http.MethodPut, "/rest/config/folders/"+url.PathEscape(f.FolderID), have, nil); err != nil {
				return 0, fmt.Errorf("update %s: %w", f.Name, err)
			}
		}
	}
	folders := manifest.Folders
	syncthingUpdate(func(s *syncthingSnapshot) { s.Folders = folders })
	return len(folders), nil
}

// syncthingFoldersCache is the last folder list, keyed by the roots it came
// from. Worker goroutine only.
var syncthingFoldersCache struct {
	key      string
	manifest saveSyncManifest
}

// syncthingSaveFolders is the Save Manager's discovery and folder IDs. The
// markers, folders.json and README are written again only when the set of
// save folders changed, not after every game.
func syncthingSaveFolders() (saveSyncManifest, bool) {
	roots := saveManagerRoots()
	var key strings.Builder
	for _, r := range roots {
		key.WriteString(r.Name + "\x00" + r.Path + "\x00")
	}
	if key.String() == syncthingFoldersCache.key && len(syncthingFoldersCache.manifest.Folders) > 0 {
		return syncthingFoldersCache.manifest, true
	}
	manifest, err := prepareSaveSync(roots)
	if err != nil {
		return saveSyncManifest{}, false
	}
	syncthingFoldersCache.key = key.String()
	syncthingFoldersCache.manifest = manifest
	return manifest, true
}

// syncthingAcceptDevice pairs with a device that asked, then shares every save
// folder with it.
func syncthingAcceptDevice(ctx context.Context, api syncthingAPI, id, name string) error {
	if name == "" {
		name = syncthingShortID(id)
	}
	dev := map[string]any{
		"deviceID":          id,
		"name":              name,
		"addresses":         []string{"dynamic"},
		"compression":       "metadata",
		"introducer":        false,
		"autoAcceptFolders": false,
		"paused":            false,
	}
	if err := api.call(ctx, http.MethodPost, "/rest/config/devices", dev, nil); err != nil {
		return err
	}
	_, err := syncthingShareFolders(ctx, api, syncthingSnapshotNow().MyID)
	return err
}

// syncthingShortID is the first block of a device ID, as Syncthing's own UI
// shortens it.
func syncthingShortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 7 {
		return id[:7]
	}
	return id
}

// ── Status ───────────────────────────────────────────────────────────────

// syncthingPoll reads devices, pairing requests, nearby devices and progress.
func syncthingPoll(ctx context.Context, api syncthingAPI) error {
	myID := syncthingSnapshotNow().MyID
	devs, err := syncthingRemoteDevices(ctx, api, myID)
	if err != nil {
		return err
	}
	var conns struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	_ = api.call(ctx, http.MethodGet, "/rest/system/connections", nil, &conns)
	configured := map[string]bool{myID: true}
	devices := make([]syncthingDevice, 0, len(devs))
	for _, d := range devs {
		id, _ := d["deviceID"].(string)
		name, _ := d["name"].(string)
		if name == "" {
			name = syncthingShortID(id)
		}
		configured[id] = true
		devices = append(devices, syncthingDevice{ID: id, Name: name, Connected: conns.Connections[id].Connected})
	}
	var pendingRaw map[string]struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	_ = api.call(ctx, http.MethodGet, "/rest/cluster/pending/devices", nil, &pendingRaw)
	pending := make([]syncthingPending, 0, len(pendingRaw))
	for id, p := range pendingRaw {
		if configured[id] {
			continue
		}
		pending = append(pending, syncthingPending{ID: id, Name: strings.TrimSpace(p.Name), Address: p.Address})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })

	// Devices seen on this network (local discovery) that are neither paired
	// nor asking: adding one makes the other side see a pairing request.
	var disco map[string]json.RawMessage
	_ = api.call(ctx, http.MethodGet, "/rest/system/discovery", nil, &disco)
	nearby := []string{}
	for id := range disco {
		if _, asking := pendingRaw[id]; configured[id] || asking {
			continue
		}
		if syncthingLooksLikeID(id) {
			nearby = append(nearby, id)
		}
	}
	sort.Strings(nearby)

	// Folder offers: one this device shares already only needs the device
	// added (the share pass does that); one it has no save folder for is
	// counted, never accepted into a guessed path.
	var offers map[string]json.RawMessage
	_ = api.call(ctx, http.MethodGet, "/rest/cluster/pending/folders", nil, &offers)
	known := map[string]bool{}
	for _, f := range syncthingSnapshotNow().Folders {
		known[f.FolderID] = true
	}
	other, reshare := 0, false
	for id := range offers {
		if known[id] {
			reshare = true
		} else {
			other++
		}
	}
	if reshare {
		if _, err := syncthingShareFolders(ctx, api, myID); err != nil {
			logInfo("syncthing", "accepting a matching folder offer: %v", err)
		}
	}

	var comp struct {
		Completion float64 `json:"completion"`
		NeedBytes  int64   `json:"needBytes"`
	}
	completion := -1.0
	if err := api.call(ctx, http.MethodGet, "/rest/db/completion", nil, &comp); err == nil {
		completion = comp.Completion
	}
	syncthingUpdate(func(s *syncthingSnapshot) {
		s.Devices = devices
		s.Pending = pending
		s.Nearby = nearby
		s.Completion = completion
		s.NeedBytes = comp.NeedBytes
		s.OtherOffers = other
	})
	return nil
}

// syncthingLooksLikeID accepts the 8×7-character dashed device ID form.
func syncthingLooksLikeID(id string) bool {
	if len(id) != 63 {
		return false
	}
	for i, r := range id {
		if i%8 == 7 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= 'A' && r <= 'Z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

// syncthingCountConflicts counts Syncthing's conflict copies in the shared
// folders. The walk is bounded: save folders are small, a card is not.
func syncthingCountConflicts(folders []saveSyncFolder) int {
	n, seen := 0, 0
	for _, f := range folders {
		_ = filepath.WalkDir(f.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			seen++
			if seen > 50000 {
				return filepath.SkipAll
			}
			if d.IsDir() && strings.EqualFold(d.Name(), ".stversions") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.Contains(d.Name(), ".sync-conflict-") {
				n++
			}
			return nil
		})
	}
	return n
}

// syncthingStatusText is the one-line state for rows and the Power tools
// check.
func syncthingStatusText(s syncthingSnapshot, want bool) string {
	switch {
	case s.Phase == syncthingPhaseChecking:
		return "Checking…"
	case s.Phase == syncthingPhaseInstalling:
		return "Installing…"
	case !s.Installed:
		return "Not installed"
	case !want:
		return "Off"
	case s.Phase == syncthingPhaseWaitingWifi:
		return "Waiting for Wi-Fi"
	case s.Phase == syncthingPhaseFailed:
		return "Not running"
	case s.Phase != syncthingPhaseRunning:
		return "Starting…"
	}
	switch s.Paused {
	case "game":
		return "Paused for game"
	case "battery":
		return "Paused · battery low"
	case "wifi":
		return "Paused · no Wi-Fi"
	}
	if len(s.Devices) == 0 {
		return "Ready to pair"
	}
	connected := 0
	for _, d := range s.Devices {
		if d.Connected {
			connected++
		}
	}
	if connected == 0 {
		return "Waiting for your devices"
	}
	if s.Completion >= 0 && s.Completion < 99.95 {
		return fmt.Sprintf("Syncing %d%%", int(s.Completion))
	}
	return "Up to date"
}

// ── Worker ───────────────────────────────────────────────────────────────

func syncthingStartWorker() {
	syncthingWorkOnce.Do(func() { go syncthingWorker() })
}

// syncthingWorkerState is the worker's own memory between steps.
type syncthingWorkerState struct {
	api          syncthingAPI
	configured   bool
	pausedFor    string
	wifiDownAt   time.Time
	lowBattery   bool
	lastPoll     time.Time
	lastConflict time.Time
	retryAt      time.Time
}

// syncthingWorker owns the process and every REST call.
func syncthingWorker() {
	wake := subscribeForegroundWake()
	var w syncthingWorkerState
	installed := syncthingInstalled()
	rec := syncthingReadRecord()
	syncthingUpdate(func(s *syncthingSnapshot) {
		s.Installed = installed
		s.Version = rec.Tag
		s.Completion = -1
		if s.Phase == syncthingPhaseChecking {
			s.Phase = syncthingPhaseOff
		}
	})
	for {
		syncthingStep(&w)
		interval := syncthingPollIdle
		if syncthingPanelOpen.Load() {
			interval = syncthingPollPanel
		}
		timer := time.NewTimer(interval)
		select {
		case <-syncthingKickCh:
		case <-wake:
		case a := <-syncthingActions:
			syncthingRunAction(&w, a)
		case <-timer.C:
		}
		timer.Stop()
	}
}

// syncthingRunAction runs one UI request against the live instance.
func syncthingRunAction(w *syncthingWorkerState, a syncthingAction) {
	syncthingProc.mu.Lock()
	running := syncthingRunningLocked()
	syncthingProc.mu.Unlock()
	if !running && a.name != "remove" {
		ShowToast("Syncthing is not running yet", ToastKindInfo)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	msg, err := a.fn(ctx, w.api)
	if err != nil {
		logWarn("syncthing", "%s: %v", a.name, err)
		ShowToast("Syncthing: "+firstLine(err.Error()), ToastKindError)
		return
	}
	w.lastPoll = time.Time{} // show the result at once
	if a.name == "pair" {
		// The new device was added unpaused: apply the pause state again.
		w.pausedFor = "?"
	}
	if msg != "" {
		ShowToast(msg, ToastKindSuccess)
	}
}

// syncthingPauseReason is why syncing should wait right now, or "".
func syncthingPauseReason(w *syncthingWorkerState, wifiUp bool) string {
	if gameScreenSuspended() {
		return "game"
	}
	pct := getBatteryPercent()
	charging := pct < 0 || getBatteryCharging()
	if w.lowBattery {
		w.lowBattery = !charging && pct < syncthingBatteryResume
	} else {
		w.lowBattery = !charging && pct < syncthingLowBattery
	}
	if w.lowBattery {
		return "battery"
	}
	if !wifiUp {
		return "wifi"
	}
	return ""
}

// syncthingStep brings the instance to where the settings say it should be.
func syncthingStep(w *syncthingWorkerState) {
	if syncthingQuitting.Load() {
		return
	}
	installed := syncthingInstalled()
	want := syncthingWantOn.Load()
	wifiUp := !IsTrimuiDevice() || wifiLinkIsUp()
	if wifiUp {
		w.wifiDownAt = time.Time{}
	} else if w.wifiDownAt.IsZero() {
		w.wifiDownAt = time.Now()
	}
	wifiGone := !w.wifiDownAt.IsZero() && time.Since(w.wifiDownAt) > syncthingWifiGrace

	syncthingProc.mu.Lock()
	running := syncthingRunningLocked()
	adopted := syncthingProc.adopted
	syncthingProc.mu.Unlock()
	// An adopted instance is not our child, so its exit is only seen here.
	if adopted && !syncthingHealthy(w.api.port) {
		syncthingProc.mu.Lock()
		syncthingProc.adopted = false
		syncthingProc.mu.Unlock()
		running = false
	}
	phase := syncthingSnapshotNow().Phase

	switch {
	case phase == syncthingPhaseInstalling:
		return
	case !installed:
		if running {
			syncthingStop(3 * time.Second)
		}
		syncthingUpdate(func(s *syncthingSnapshot) {
			// An install the UI began since the phase was read keeps its
			// phase, or the row would offer a second install.
			if s.Phase != syncthingPhaseInstalling {
				s.Installed = false
				s.Phase = syncthingPhaseNotInstalled
			}
		})
		return
	case !want || wifiGone:
		if running {
			syncthingStop(3 * time.Second)
			w.configured = false
			w.pausedFor = ""
		}
		syncthingUpdate(func(s *syncthingSnapshot) {
			s.Installed = true
			s.Paused = ""
			if want {
				s.Phase = syncthingPhaseWaitingWifi
			} else {
				s.Phase = syncthingPhaseOff
			}
		})
		return
	}

	if !running {
		if !wifiUp {
			syncthingUpdate(func(s *syncthingSnapshot) { s.Phase = syncthingPhaseWaitingWifi })
			return
		}
		// A start hashes every folder: not under a game. It starts as soon
		// as JukaHub is back on screen (the foreground wake).
		if time.Now().Before(w.retryAt) || gameScreenSuspended() {
			return
		}
		syncthingProc.mu.Lock()
		quick := !syncthingProc.startedAt.IsZero() && time.Since(syncthingProc.startedAt) < time.Minute
		if quick {
			syncthingProc.fails++
		} else {
			syncthingProc.fails = 0
		}
		fails := syncthingProc.fails
		syncthingProc.mu.Unlock()
		if fails >= 3 {
			syncthingUpdate(func(s *syncthingSnapshot) {
				s.Phase = syncthingPhaseFailed
				s.Detail = "Syncthing keeps stopping — see " + filepath.Join(syncthingHome(), "syncthing.log")
			})
			w.retryAt = time.Now().Add(10 * time.Minute)
			syncthingProc.mu.Lock()
			syncthingProc.fails = 0
			syncthingProc.startedAt = time.Time{}
			syncthingProc.mu.Unlock()
			return
		}
		syncthingUpdate(func(s *syncthingSnapshot) {
			s.Installed = true
			s.Phase = syncthingPhaseStarting
			s.Detail = ""
		})
		api, err := syncthingStart(context.Background())
		if err != nil {
			logWarn("syncthing", "start failed: %v", err)
			syncthingUpdate(func(s *syncthingSnapshot) {
				s.Phase = syncthingPhaseFailed
				s.Detail = firstLine(err.Error())
			})
			w.retryAt = time.Now().Add(time.Minute)
			return
		}
		w.api = api
		w.configured = false
		// Unknown: a pause is stored in Syncthing's config, so one left by
		// a JukaHub that died during a game would otherwise never be lifted.
		w.pausedFor = "?"
		syncthingUpdate(func(s *syncthingSnapshot) { s.Port = api.port })
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !w.configured {
		if err := syncthingConfigure(ctx, w.api); err != nil {
			logWarn("syncthing", "configure: %v", err)
			syncthingUpdate(func(s *syncthingSnapshot) {
				s.Phase = syncthingPhaseFailed
				s.Detail = "Setup failed: " + firstLine(err.Error())
			})
			return
		}
		w.configured = true
		w.lastConflict = time.Time{}
	}

	reason := syncthingPauseReason(w, wifiUp)
	if reason != w.pausedFor {
		path := "/rest/system/resume"
		if reason != "" {
			path = "/rest/system/pause"
		}
		if err := w.api.call(ctx, http.MethodPost, path, nil, nil); err != nil {
			logWarn("syncthing", "%s: %v", path, err)
		} else {
			verb := "resumed"
			if reason != "" {
				verb = "paused"
			}
			logInfo("syncthing", "sync %s (%s)", verb, syncthingFirstNonEmpty(reason, w.pausedFor))
			if w.pausedFor == "game" && reason == "" {
				// The game is over: pick up a save folder that appeared with
				// it, and send the new saves now instead of at the next
				// watcher tick.
				if _, err := syncthingShareFolders(ctx, w.api, syncthingSnapshotNow().MyID); err != nil {
					logInfo("syncthing", "share after game: %v", err)
				}
				_ = w.api.call(ctx, http.MethodPost, "/rest/db/scan", nil, nil)
				w.lastConflict = time.Time{}
			}
			w.pausedFor = reason
		}
	}
	syncthingUpdate(func(s *syncthingSnapshot) {
		s.Phase = syncthingPhaseRunning
		s.Detail = ""
		s.Paused = w.pausedFor
	})

	// Nobody reads the status behind a game or a sleeping screen.
	if backgroundPaused() {
		return
	}
	every := syncthingPollIdle
	if syncthingPanelOpen.Load() {
		every = syncthingPollPanel
	}
	if time.Since(w.lastPoll) >= every-100*time.Millisecond {
		w.lastPoll = time.Now()
		if err := syncthingPoll(ctx, w.api); err != nil {
			logInfo("syncthing", "status: %v", err)
		}
	}
	if syncthingPanelOpen.Load() && time.Since(w.lastConflict) >= syncthingConflictEvery {
		w.lastConflict = time.Now()
		n := syncthingCountConflicts(syncthingSnapshotNow().Folders)
		syncthingUpdate(func(s *syncthingSnapshot) { s.Conflicts = n })
	}
}

// syncthingFirstNonEmpty returns the first non-blank string.
func syncthingFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// syncthingRemoveAll stops the instance and deletes the binary and its home,
// device key included: a later install is a new device. Worker goroutine.
func syncthingRemoveAll() error {
	syncthingStop(3 * time.Second)
	if p := syncthingBinaryPath(); p != "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", filepath.Base(p), err)
		}
		_ = os.Remove(p + ".old")
	}
	home := syncthingHome()
	if filepath.Base(home) != "syncthing" {
		return fmt.Errorf("refusing to delete %s", home)
	}
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	syncthingProc.mu.Lock()
	syncthingProc.cmd = nil
	syncthingProc.done = nil
	syncthingProc.adopted = false
	syncthingProc.fails = 0
	syncthingProc.startedAt = time.Time{}
	syncthingProc.mu.Unlock()
	syncthingUpdate(func(s *syncthingSnapshot) {
		*s = syncthingSnapshot{Phase: syncthingPhaseNotInstalled, Completion: -1}
	})
	return nil
}

// syncthingPowerStatus is the Power tools "Syncthing status" text: the
// managed instance first, then any other Syncthing on 8384.
func syncthingPowerStatus() string {
	syncthingStartWorker()
	s := syncthingSnapshotNow()
	if s.Phase == syncthingPhaseChecking {
		// The worker has not looked yet; this runs on the Power tools
		// goroutine, so a stat is fine here.
		s.Installed = syncthingInstalled()
		s.Phase = syncthingPhaseOff
	}
	var b strings.Builder
	fmt.Fprintf(&b, "JukaHub's Syncthing: %s", syncthingStatusText(s, syncthingWantOn.Load()))
	if s.Version != "" {
		fmt.Fprintf(&b, " (%s)", s.Version)
	}
	if s.Phase == syncthingPhaseRunning && s.Port > 0 {
		fmt.Fprintf(&b, "\nREST on 127.0.0.1:%d · %s · %s", s.Port,
			pluralizeCount(len(s.Folders), "folder", "folders"), pluralizeCount(len(s.Devices), "device", "devices"))
	}
	if s.Detail != "" {
		b.WriteString("\n" + s.Detail)
	}
	if syncthingHealthy(syncthingForeignPort) {
		b.WriteString("\nAnother Syncthing answers on 127.0.0.1:8384 (not managed by JukaHub).")
	}
	b.WriteString("\nSet it up in Save Manager → Syncthing.")
	return b.String()
}

