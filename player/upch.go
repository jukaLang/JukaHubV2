package main

// upch.go — the offline update channel.
//
// The updater used to need the network at the moment of installing: Install
// downloaded the release, then swapped it in. On a handheld that is often the
// wrong moment — no Wi-Fi on the train, a flaky hotspot, a battery at 15%. The
// channel splits the two halves:
//
//   - When a newer release is advertised and the device is online — and
//     charging or left alone for a couple of minutes, never while a game is on
//     screen — the scheduler downloads it into a bounded cache (update-cache/,
//     the newest two releases). It prefers the release's .jkdelta for the
//     installed build (jdelta_update.go), rebuilt into a full tree right away,
//     and falls back to the full archive. An interrupted download resumes.
//   - A cached file is verified exactly like a direct download: its size, and
//     the sha256 from the signed release.json when the release has one (kept
//     beside the file, so the signature is checked again offline) or else
//     GitHub's asset digest. The UI says which — "signed by JukaHub",
//     "checksum verified" — or that the release published neither.
//   - "Install now — no internet needed" verifies the cached copy again and
//     hands it to the staging, journal and helper path every update takes
//     (stageJukaHubArchive, OpApplyStagedSwap). The header chip's one-press
//     install uses the cached copy too (InstallAvailableJukaHubUpdate).
//
// Install from file (upch_sideload.go) is the same install from a file the
// user copied to the card. The Patch rows and sheets are in upch_ui.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jukahub/JukaHub/internal/jdelta"
	"jukahub/JukaHub/internal/releasesign"
)

const (
	// upchAutoSetting is the custom variable the Settings switch writes.
	upchAutoSetting = "updateAutoDownload"
	// upchCacheKeep is how many releases the cache holds.
	upchCacheKeep = 2
	// upchPoll is how often the scheduler looks for a good moment.
	upchPoll = 10 * time.Minute
	// upchStartupDelay keeps the first look behind the startup release check.
	upchStartupDelay = autoUpdateStartupDelay + 20*time.Second
	// upchIdle is how long the user must have been away before a download on
	// battery starts.
	upchIdle = 2 * time.Minute
	// upchSpaceReserve is what a download leaves free on the card.
	upchSpaceReserve = int64(96 << 20)
	// upchNotesMax bounds the release notes kept with a cached release.
	upchNotesMax = 16 << 10
)

// Kinds of cache entry, and how a cached file was verified.
const (
	upchKindArchive = "archive"
	upchKindDelta   = "delta"

	upchVerifiedSigned   = "signed"
	upchVerifiedChecksum = "checksum"
	upchVerifiedNone     = "unverified"
)

// upchEntry is one cached release, update-cache/<tag>/entry.json. File is the
// archive or the .jkdelta in the entry's folder; a delta entry also holds the
// tree it rebuilt (tree/). Size and SHA256 are File's, as verified when it was
// downloaded; Verified says against what.
type upchEntry struct {
	Tag          string `json:"tag"`
	PublishedAt  string `json:"published_at,omitempty"`
	Asset        string `json:"asset"`
	Kind         string `json:"kind"`
	File         string `json:"file"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Verified     string `json:"verified"`
	FromTag      string `json:"from_tag,omitempty"`
	DownloadedAt string `json:"downloaded_at,omitempty"`
	Notes        string `json:"notes,omitempty"`

	dir string
}

// upchStateFile is update-cache/state.json: which release was installed from
// the cache or a file, so the channel can say so after the restart.
type upchStateFile struct {
	InstalledFrom string `json:"installed_from,omitempty"`
	Source        string `json:"source,omitempty"`
	At            string `json:"at,omitempty"`
}

// upchView is what the Patch row and the Settings line show. Progress is 0..1
// while a download with a known size runs, below 0 otherwise.
type upchView struct {
	Line       string
	Running    bool
	Progress   float64
	Ready      bool
	ReadyTag   string
	ReadyLabel string
	Verified   string
}

// upchState is the channel's live state. The UI reads it every frame, so it
// lives in memory; the cache folders are only touched off the render thread.
var upchState struct {
	mu       sync.Mutex
	loaded   bool
	entries  []upchEntry
	running  bool
	runTag   string
	done     int64
	total    int64
	lastErr  string
	cancel   context.CancelFunc
	fromTag  string
	fromKind string
	viewAt   time.Time
	view     upchView
}

// upchAutoOn mirrors the setting for the scheduler goroutine; the Custom map
// belongs to the main thread.
var upchAutoOn atomic.Bool

func init() { upchAutoOn.Store(true) }

// upchNudgeCh wakes the scheduler early (a check just found a release).
var upchNudgeCh = make(chan struct{}, 1)

var upchSchedulerOnce sync.Once

// upchDiskMu serialises what writes or deletes cache folders: a download being
// committed, a prune, an install reading an entry.
var upchDiskMu sync.Mutex

// ── Settings ─────────────────────────────────────────────────────────────────

// upchAutoDownloadEnabled reads the setting (main thread only): on unless it
// was switched off.
func upchAutoDownloadEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	if v, ok := customValue(config.Variables.Custom, upchAutoSetting); ok {
		if s, isString := v.(string); isString {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "false", "0", "off", "no":
				return false
			}
			return true
		}
		return coerceBool(v)
	}
	return true
}

// upchLoadPrefs mirrors the update settings for the background goroutines.
// Main thread only: the deferred startup check and the Patch scene call it.
func upchLoadPrefs(config *Config) {
	upchAutoOn.Store(upchAutoDownloadEnabled(config))
	uhistKeep.Store(int32(uhistKeepFromConfig(config)))
}

// upchSetAutoDownload switches the background download on or off and saves
// the choice (main thread only).
func upchSetAutoDownload(config *Config, on bool) {
	upchAutoOn.Store(on)
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[upchAutoSetting] = on
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	if on {
		upchNudge()
	} else {
		upchCancelBackground()
	}
}

// upchAutoDetail is the line under the Settings switch.
func upchAutoDetail(config *Config) string {
	if !upchAutoDownloadEnabled(config) {
		return "Off: an update downloads when you install it"
	}
	return "Ready to install offline · while charging or idle, never mid-game"
}

// ── Cache folders ────────────────────────────────────────────────────────────

// upchDir is update-cache/ in the Patch state folder. It is not downloads/ or
// staging/: Verify & repair clears those.
func upchDir() (string, error) {
	base, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "update-cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// upchWriteJSON writes a small JSON file through a temporary file and a rename,
// so a power cut leaves the old file or the new one, never half of one.
func upchWriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, append(data, '\n'), 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// upchReadEntry reads one entry's description.
func upchReadEntry(dir string) (upchEntry, error) {
	var e upchEntry
	data, err := os.ReadFile(filepath.Join(dir, "entry.json"))
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	if e.Tag == "" || e.File == "" || filepath.Base(e.File) != e.File {
		return e, errors.New("incomplete cache entry")
	}
	return e, nil
}

// upchScanEntries reads every complete entry under dir, newest release first.
// An entry whose file is gone or changed size is skipped.
func upchScanEntries(dir string) []upchEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []upchEntry
	for _, it := range items {
		if !it.IsDir() || strings.HasPrefix(it.Name(), ".") {
			continue
		}
		e, err := upchReadEntry(filepath.Join(dir, it.Name()))
		if err != nil {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, it.Name(), e.File))
		if err != nil || !info.Mode().IsRegular() || (e.Size > 0 && info.Size() != e.Size) {
			continue
		}
		e.dir = it.Name()
		out = append(out, e)
	}
	upchSortEntries(out)
	return out
}

// upchEntryTime is a cached release's publication time.
func upchEntryTime(e upchEntry) time.Time {
	if t, ok := parseReleaseTimestamp(e.PublishedAt); ok {
		return t
	}
	if t, ok := releaseTimeFromTag(e.Tag); ok {
		return t
	}
	t, _ := parseReleaseTimestamp(e.DownloadedAt)
	return t
}

// upchSortEntries orders entries newest release first.
func upchSortEntries(list []upchEntry) {
	sort.SliceStable(list, func(i, j int) bool { return upchEntryTime(list[i]).After(upchEntryTime(list[j])) })
}

// upchPrunePlan splits the cache (newest first) into what to keep and what to
// delete: a release the running build already is, or is newer than, is of no
// use; of the rest the newest keep stay.
func upchPrunePlan(entries []upchEntry, installed time.Time, known bool, isInstalled func(string) bool, keep int) (kept, dropped []upchEntry) {
	for _, e := range entries {
		switch {
		case isInstalled(e.Tag):
			dropped = append(dropped, e)
		case known && !upchEntryTime(e).After(installed):
			dropped = append(dropped, e)
		case len(kept) >= keep:
			dropped = append(dropped, e)
		default:
			kept = append(kept, e)
		}
	}
	return kept, dropped
}

// upchRemoveEntries deletes cache entries.
func upchRemoveEntries(dir string, list []upchEntry) {
	upchDiskMu.Lock()
	defer upchDiskMu.Unlock()
	for _, e := range list {
		if e.dir != "" && filepath.Base(e.dir) == e.dir {
			_ = os.RemoveAll(filepath.Join(dir, e.dir))
		}
	}
}

// upchSweepIncoming deletes partial downloads nobody resumed for two weeks.
// Younger ones stay: the next attempt continues where they stopped.
func upchSweepIncoming(dir string) {
	incoming := filepath.Join(dir, ".incoming")
	items, err := os.ReadDir(incoming)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	for _, it := range items {
		info, err := it.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(incoming, it.Name()))
	}
}

func upchReadState(dir string) upchStateFile {
	var st upchStateFile
	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

// upchNoteInstallSource records that tag is being installed from the cache or a
// file, just before the swap.
func upchNoteInstallSource(tag, source string) {
	dir, err := upchDir()
	if err != nil {
		return
	}
	st := upchStateFile{InstalledFrom: tag, Source: source, At: time.Now().UTC().Format(time.RFC3339)}
	if err := upchWriteJSON(filepath.Join(dir, "state.json"), st); err != nil {
		logPatch("update cache: %v", err)
	}
}

// upchStartup loads the cache index and drops what the running build no
// longer needs. It runs in InitPatchModule, before the first frame.
func upchStartup() {
	dir, err := upchDir()
	if err != nil {
		return
	}
	entries := upchScanEntries(dir)
	installed, known := installedReleaseTime()
	kept, dropped := upchPrunePlan(entries, installed, known, installedReleaseTagIs, upchCacheKeep)
	st := upchReadState(dir)
	fromTag, fromKind := "", ""
	if st.InstalledFrom != "" {
		if installedReleaseTagIs(st.InstalledFrom) {
			fromTag, fromKind = st.InstalledFrom, st.Source
		} else {
			// The install did not take (it was rolled back), or a later one
			// replaced it: nothing to report any more.
			_ = os.Remove(filepath.Join(dir, "state.json"))
		}
	}
	upchState.mu.Lock()
	upchState.entries = kept
	upchState.loaded = true
	upchState.fromTag, upchState.fromKind = fromTag, fromKind
	upchState.viewAt = time.Time{}
	upchState.mu.Unlock()
	go func() {
		if len(dropped) > 0 {
			upchRemoveEntries(dir, dropped)
		}
		upchSweepIncoming(dir)
	}()
}

// upchHasEntry reports whether tag is cached.
func upchHasEntry(tag string) bool {
	upchState.mu.Lock()
	defer upchState.mu.Unlock()
	for _, e := range upchState.entries {
		if strings.EqualFold(e.Tag, tag) {
			return true
		}
	}
	return false
}

// upchForgetEntry drops tag from the index (its folder is already gone).
func upchForgetEntry(tag string) {
	upchState.mu.Lock()
	defer upchState.mu.Unlock()
	kept := upchState.entries[:0]
	for _, e := range upchState.entries {
		if !strings.EqualFold(e.Tag, tag) {
			kept = append(kept, e)
		}
	}
	upchState.entries = kept
	upchState.viewAt = time.Time{}
}

// upchBestReady returns the newest cached release the running build is older
// than.
func upchBestReady() (upchEntry, bool) {
	upchState.mu.Lock()
	entries := append([]upchEntry(nil), upchState.entries...)
	upchState.mu.Unlock()
	installed, known := installedReleaseTime()
	for _, e := range entries {
		if installedReleaseTagIs(e.Tag) || uhistSkipped(e.Tag) {
			continue
		}
		if known && !upchEntryTime(e).After(installed) {
			continue
		}
		return e, true
	}
	return upchEntry{}, false
}

// ── Status ───────────────────────────────────────────────────────────────────

// upchStatusView is the channel's state as one sentence and a few flags. It is
// rebuilt at most four times a second: the Patch list draws it every frame.
func upchStatusView() upchView {
	now := time.Now()
	upchState.mu.Lock()
	if !upchState.viewAt.IsZero() && now.Sub(upchState.viewAt) < 250*time.Millisecond {
		v := upchState.view
		upchState.mu.Unlock()
		return v
	}
	running, runTag, done, total := upchState.running, upchState.runTag, upchState.done, upchState.total
	lastErr, fromTag, fromKind := upchState.lastErr, upchState.fromTag, upchState.fromKind
	upchState.mu.Unlock()

	v := upchBuildView(running, runTag, done, total, lastErr, fromTag, fromKind)

	upchState.mu.Lock()
	upchState.view, upchState.viewAt = v, now
	upchState.mu.Unlock()
	return v
}

// upchBuildView words the channel's state. The lines stay short: the Patch row
// shows them whole on the 960-wide canvas too.
func upchBuildView(running bool, runTag string, done, total int64, lastErr, fromTag, fromKind string) upchView {
	v := upchView{Progress: -1}
	if running {
		v.Running = true
		label := upchTagLabel(runTag, "")
		if total > 0 {
			frac := float64(done) / float64(total)
			if frac > 1 {
				frac = 1
			}
			v.Progress = frac
			v.Line = fmt.Sprintf("Downloading JukaHub %s for offline install · %d%% · %s of %s",
				label, int(frac*100+0.5), formatInstallBytes(done), formatInstallBytes(total))
		} else {
			v.Line = "Downloading JukaHub " + label + " for offline install…"
		}
		return v
	}
	if e, ok := upchBestReady(); ok {
		v.Ready = true
		v.ReadyTag = e.Tag
		v.ReadyLabel = upchTagLabel(e.Tag, e.PublishedAt)
		v.Verified = upchVerifiedPhrase(e.Verified)
		v.Line = "Ready — no internet needed · JukaHub " + v.ReadyLabel + " · " + v.Verified
		return v
	}
	if updateAvailable() {
		label := availableUpdateLabel()
		switch {
		case !IsOnline():
			v.Line = "JukaHub " + label + " is available · connect Wi-Fi to download it"
		case lastErr != "":
			v.Line = "JukaHub " + label + " · download failed: " + truncateRunesSuffix(lastErr, 44, "…")
		case !upchAutoOn.Load():
			v.Line = "JukaHub " + label + " is available · press A to download it for later"
		default:
			v.Line = "JukaHub " + label + " is available · downloads while charging or idle"
		}
		return v
	}
	if skip := uhistSkippedTag(); skip != "" {
		v.Line = "Up to date · " + upchTagLabel(skip, "") + " is not offered again (you rolled back)"
		return v
	}
	switch {
	case fromTag != "" && fromKind == "file":
		v.Line = "Up to date · installed offline from a file"
	case fromTag != "":
		v.Line = "Up to date · installed offline from the update cache"
	case !upchAutoOn.Load():
		v.Line = "Up to date · automatic downloads are off (Settings → About)"
	default:
		v.Line = "Up to date · new releases download ahead, ready to install offline"
	}
	return v
}

// upchTagLabel is a release's short label: its date, else its tag.
func upchTagLabel(tag, published string) string {
	if t, ok := parseReleaseTimestamp(published); ok {
		return t.Format("2006-01-02")
	}
	if t, ok := releaseTimeFromTag(tag); ok {
		return t.Format("2006-01-02")
	}
	if strings.TrimSpace(tag) != "" {
		return tag
	}
	return "release"
}

// upchVerifiedPhrase says how a cached file was verified, honestly: a release
// with no signed manifest is "checksum verified", one with no checksum at all
// "not verified".
func upchVerifiedPhrase(verified string) string {
	switch verified {
	case upchVerifiedSigned:
		return "signed by JukaHub"
	case upchVerifiedChecksum:
		return "checksum verified"
	}
	return "not verified"
}

// ── Scheduler ────────────────────────────────────────────────────────────────

// upchStartScheduler starts the background download loop once.
func upchStartScheduler() {
	upchSchedulerOnce.Do(func() {
		go func() {
			time.Sleep(upchStartupDelay)
			ticker := time.NewTicker(upchPoll)
			defer ticker.Stop()
			for {
				upchTick(false)
				select {
				case <-ticker.C:
				case <-upchNudgeCh:
				}
			}
		}()
	})
}

// upchNudge asks the scheduler to look again now.
func upchNudge() {
	select {
	case upchNudgeCh <- struct{}{}:
	default:
	}
}

// upchDownloadNow is the explicit "download it for later": it does not wait for
// the charger or an idle device and ignores the setting, but never runs over a
// game or without a network.
func upchDownloadNow() {
	go upchTick(true)
}

// upchMoment is the policy: online, never over a game or another install, and
// — unless the user asked — the setting on and the device charging or left
// alone.
func upchMoment(explicit, autoOn, online, inGame, busy, charging bool, idle time.Duration) bool {
	if !online || inGame || busy {
		return false
	}
	if explicit {
		return true
	}
	return autoOn && (charging || idle >= upchIdle)
}

// upchPatchOpRunning reports whether a Patch operation owns the device.
func upchPatchOpRunning() bool {
	patchBusy.Lock()
	defer patchBusy.Unlock()
	return patchBusy.active
}

// upchTick downloads the advertised release into the cache when the moment is
// right. explicit is a user's press.
func upchTick(explicit bool) {
	now := time.Now()
	online := IsOnline()
	inGame := gameScreenSuspended()
	// A desktop counts as plugged in; the handheld reports its charger.
	charging := !IsTrimuiDevice() || getBatteryCharging()
	busy := installBarActive() || upchPatchOpRunning()
	if !upchMoment(explicit, upchAutoOn.Load(), online, inGame, busy, charging, userInputAge(now)) {
		if explicit {
			switch {
			case !online:
				ShowToast(offlinePlainNotice, ToastKindWarning)
			case !inGame:
				ShowToast("Another install is running — try again when it is done", ToastKindInfo)
			}
		}
		return
	}
	if !explicit && !updateAvailable() {
		return
	}
	// Already cached: nothing to fetch, and no reason to ask GitHub every poll.
	if !explicit && upchHasEntry(availableUpdateTag()) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if !upchBeginRun(availableUpdateTag(), cancel) {
		if explicit {
			ShowToast("The update is already downloading", ToastKindInfo)
		}
		return
	}
	errText := ""
	defer func() { upchEndRun(errText) }()
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go upchWatchGame(ctx, cancel, stopWatch)

	rel, err := FetchLatestJukaHubRelease(ctx)
	if err != nil {
		errText = plainNetworkError(err)
		logPatch("update cache: release check failed: %v", err)
		if explicit {
			ShowToast(errText, ToastKindWarning)
		}
		return
	}
	if !releaseIsNewerThanInstalled(rel) {
		if explicit {
			ShowToast("JukaHub is up to date", ToastKindInfo)
		}
		return
	}
	if upchHasEntry(rel.TagName) {
		if explicit {
			ShowToast("Already downloaded — it installs without internet", ToastKindInfo)
		}
		return
	}
	upchSetRunTag(rel.TagName)
	if explicit {
		ShowToast("Downloading JukaHub "+releaseDateLabel(rel)+" for an offline install", ToastKindInfo)
	}
	if err := upchPrefetch(ctx, rel); err != nil {
		if ctx.Err() != nil {
			logPatch("update cache: download of %s paused (%v); it resumes next time", rel.TagName, ctx.Err())
			return
		}
		errText = plainNetworkError(err)
		logPatch("update cache: %s: %v", rel.TagName, err)
		if explicit {
			ShowToast("Download failed — "+errText, ToastKindError)
		}
		return
	}
	label := upchTagLabel(rel.TagName, rel.PublishedAt)
	logPatch("update cache: %s is ready to install offline", rel.TagName)
	notifPush("JukaHub "+label+" is ready to install — no internet needed", notifSuccess)
}

// upchWatchGame stops a download when a game takes the screen. The bytes
// already fetched stay for the next attempt to resume.
func upchWatchGame(ctx context.Context, cancel context.CancelFunc, stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if gameScreenSuspended() {
				logPatch("update cache: a game started, pausing the download")
				cancel()
				return
			}
		}
	}
}

func upchBeginRun(tag string, cancel context.CancelFunc) bool {
	upchState.mu.Lock()
	defer upchState.mu.Unlock()
	if upchState.running {
		return false
	}
	upchState.running = true
	upchState.runTag = tag
	upchState.done, upchState.total = 0, 0
	upchState.cancel = cancel
	upchState.viewAt = time.Time{}
	return true
}

func upchEndRun(errText string) {
	upchState.mu.Lock()
	upchState.running = false
	upchState.runTag = ""
	upchState.cancel = nil
	upchState.lastErr = errText
	upchState.viewAt = time.Time{}
	upchState.mu.Unlock()
}

func upchSetRunTag(tag string) {
	upchState.mu.Lock()
	upchState.runTag = tag
	upchState.mu.Unlock()
}

func upchSetProgress(done, total int64) {
	upchState.mu.Lock()
	upchState.done, upchState.total = done, total
	upchState.mu.Unlock()
}

// upchCancelBackground stops a running download. What it fetched stays for the
// next attempt to resume.
func upchCancelBackground() {
	upchState.mu.Lock()
	cancel := upchState.cancel
	upchState.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ── Download ─────────────────────────────────────────────────────────────────

// upchPrefetch downloads rel into the cache: the delta for the installed build
// when the release publishes one it vouches for, the full archive otherwise.
func upchPrefetch(ctx context.Context, rel *githubRelease) error {
	if rel == nil || strings.TrimSpace(rel.TagName) == "" {
		return errors.New("missing release metadata")
	}
	target, err := jukaHubUpdateTargetFor(DetectDevice(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if upchHasEntry(rel.TagName) {
		return nil
	}
	dir, err := upchDir()
	if err != nil {
		return err
	}
	tagDir := sanitizeFileName(rel.TagName)
	incoming := filepath.Join(dir, ".incoming", tagDir)
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		return err
	}
	// Appending to a partial file does not touch its folder; this does, so the
	// sweep judges a resumed download by its last attempt, not its first.
	now := time.Now()
	_ = os.Chtimes(incoming, now, now)
	client := newPatchClient()
	// A signed manifest that does not verify stops the download: a bad
	// signature is never traded for a weaker check.
	manifest, manifestRaw, err := upchFetchManifest(ctx, client, rel)
	if err != nil && !errors.Is(err, errNoReleaseManifest) {
		return fmt.Errorf("release verification failed: %w", err)
	}
	notes := truncateRunesSuffix(strings.TrimSpace(rel.Body), upchNotesMax, "")
	entry := upchEntry{Tag: rel.TagName, PublishedAt: releaseStampISO(rel), Asset: target.Asset, Notes: notes}
	if derr := upchDownloadDelta(ctx, client, rel, target.Asset, manifest, incoming, &entry); derr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(derr, errJdeltaNone) {
			logPatch("update cache: the delta for %s did not apply, downloading the full archive: %v", rel.TagName, derr)
		}
		if err := upchDownloadArchive(ctx, client, rel, target.Asset, manifest, incoming, &entry); err != nil {
			return err
		}
	}
	if manifestRaw != nil {
		if err := os.WriteFile(filepath.Join(incoming, releasesign.AssetName), manifestRaw, 0o600); err != nil {
			return err
		}
	}
	entry.DownloadedAt = time.Now().UTC().Format(time.RFC3339)
	if err := upchWriteJSON(filepath.Join(incoming, "entry.json"), entry); err != nil {
		return err
	}
	return upchCommitIncoming(dir, incoming, tagDir)
}

// upchFetchManifest fetches and verifies the release's signed manifest. It
// returns errNoReleaseManifest when the release publishes none.
func upchFetchManifest(ctx context.Context, client *http.Client, rel *githubRelease) (*releasesign.Manifest, []byte, error) {
	asset, ok := upchFindAsset(rel, releasesign.AssetName)
	if !ok {
		return nil, nil, errNoReleaseManifest
	}
	data, err := fetchBounded(ctx, client, asset.BrowserDownloadURL, patchMaxMetadata)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching the signed release manifest: %w", err)
	}
	var m releasesign.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Errorf("parsing the signed release manifest: %w", err)
	}
	if err := verifyReleaseManifest(&m); err != nil {
		return nil, nil, err
	}
	if m.Tag != "" && m.Tag != rel.TagName {
		return nil, nil, fmt.Errorf("the release manifest is for %s, not %s", m.Tag, rel.TagName)
	}
	return &m, data, nil
}

// upchFindAsset returns the release asset called name.
func upchFindAsset(rel *githubRelease, name string) (githubAsset, bool) {
	if rel == nil {
		return githubAsset{}, false
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, name) && strings.TrimSpace(a.BrowserDownloadURL) != "" {
			return a, true
		}
	}
	return githubAsset{}, false
}

// upchExpectedDigest resolves what a downloaded asset must hash to: the signed
// manifest's entry when the release is signed, else GitHub's digest, else
// nothing. A signed manifest that does not list the asset, or a digest that
// disagrees with it, is an error.
func upchExpectedDigest(manifest *releasesign.Manifest, asset githubAsset) (string, int64, string, error) {
	digest := releaseAssetDigest(asset.Digest)
	if manifest != nil {
		t, err := releasesign.FindTarget(manifest, asset.Name)
		if err != nil {
			return "", 0, "", err
		}
		if digest != "" && !strings.EqualFold(digest, t.SHA256) {
			return "", 0, "", fmt.Errorf("the digest of %s disagrees with the signed manifest", asset.Name)
		}
		return t.SHA256, t.Size, upchVerifiedSigned, nil
	}
	if digest != "" {
		return digest, asset.Size, upchVerifiedChecksum, nil
	}
	return "", asset.Size, upchVerifiedNone, nil
}

// upchDownloadArchive downloads the platform archive into incoming.
func upchDownloadArchive(ctx context.Context, client *http.Client, rel *githubRelease, assetName string, manifest *releasesign.Manifest, incoming string, entry *upchEntry) error {
	asset, ok := upchFindAsset(rel, assetName)
	if !ok {
		return fmt.Errorf("release %s does not list %s", rel.TagName, assetName)
	}
	sha, size, verified, err := upchExpectedDigest(manifest, asset)
	if err != nil {
		return err
	}
	if size > patchMaxDownload {
		return fmt.Errorf("release asset %s is larger than the %d byte limit", assetName, patchMaxDownload)
	}
	if verified == upchVerifiedNone {
		logPatch("update cache: %s publishes no checksum for %s; the copy is not verified", rel.TagName, assetName)
	}
	if err := upchCheckFree(incoming, size); err != nil {
		return err
	}
	dest := filepath.Join(incoming, sanitizeFileName(assetName))
	if !upchHaveVerified(dest, sha) {
		upchSetProgress(0, size)
		if err := upchDownloadWithProgress(ctx, client, asset.BrowserDownloadURL, dest, sha, size); err != nil {
			return err
		}
	}
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if size > 0 && info.Size() != size {
		_ = os.Remove(dest)
		return fmt.Errorf("%s is %d bytes, the release says %d", assetName, info.Size(), size)
	}
	got := strings.ToLower(sha)
	if got == "" {
		sum, err := SHA256File(dest)
		if err != nil {
			return err
		}
		got = sum
	}
	entry.Kind = upchKindArchive
	entry.File = filepath.Base(dest)
	entry.Size = info.Size()
	entry.SHA256 = got
	entry.Verified = verified
	return nil
}

// upchDownloadDelta downloads the release's delta for the installed build and
// rebuilds the release from it into incoming/tree. It returns errJdeltaNone
// when there is no delta this install can use and verify.
func upchDownloadDelta(ctx context.Context, client *http.Client, rel *githubRelease, archiveAsset string, manifest *releasesign.Manifest, incoming string, entry *upchEntry) error {
	from := jdeltaInstalledTag()
	name := jdelta.AssetName(jdelta.PlatformFromArchive(archiveAsset), from)
	if name == "" {
		return errJdeltaNone
	}
	asset, ok := upchFindAsset(rel, name)
	if !ok {
		return errJdeltaNone
	}
	sha, size, verified, err := upchExpectedDigest(manifest, asset)
	if err != nil || verified == upchVerifiedNone || size > patchMaxDownload {
		// A signed release that does not vouch for its delta, or one with no
		// checksum at all: the full archive is the honest download.
		return errJdeltaNone
	}
	exeDir, err := upchExeDir()
	if err != nil {
		return err
	}
	if err := upchCheckFree(incoming, size); err != nil {
		return err
	}
	dest := filepath.Join(incoming, name)
	if !upchHaveVerified(dest, sha) {
		upchSetProgress(0, size)
		if err := upchDownloadWithProgress(ctx, client, asset.BrowserDownloadURL, dest, sha, size); err != nil {
			return err
		}
	}
	if err := jdeltaMaterialize(ctx, dest, exeDir, filepath.Join(incoming, "tree")); err != nil {
		// A delta that does not fit this installation is of no use; one whose
		// rebuild was only interrupted (a game started) is kept for next time.
		if ctx.Err() == nil {
			_ = os.Remove(dest)
		}
		return err
	}
	entry.Kind = upchKindDelta
	entry.File = name
	entry.Size = fileSize(dest)
	entry.SHA256 = strings.ToLower(sha)
	entry.Verified = verified
	entry.FromTag = from
	logPatch("update cache: %s rebuilt from the delta for %s (%s instead of the full archive)", rel.TagName, from, humanBytes(entry.Size))
	return nil
}

// upchHaveVerified reports whether path is already a complete download that
// hashes to sha: an earlier attempt finished it before something else stopped.
// Without a checksum there is nothing to trust, so it is downloaded again.
func upchHaveVerified(path, sha string) bool {
	if sha == "" {
		return false
	}
	sum, err := SHA256File(path)
	return err == nil && strings.EqualFold(sum, sha)
}

// upchDownloadWithProgress runs downloadToFile (resume, size cap, sha256) and
// publishes how far its partial file has grown, for the Patch row's bar.
func upchDownloadWithProgress(ctx context.Context, client *http.Client, url, dest, sha string, total int64) error {
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if info, err := os.Stat(dest + ".part"); err == nil {
					upchSetProgress(info.Size(), total)
				}
			}
		}
	}()
	err := downloadToFile(ctx, client, url, dest, sha, patchMaxDownload)
	close(stop)
	<-finished
	if err == nil {
		upchSetProgress(total, total)
	}
	return err
}

// upchCommitIncoming turns a finished download into a cache entry and prunes
// the cache to its bound.
func upchCommitIncoming(dir, incoming, tagDir string) error {
	upchDiskMu.Lock()
	defer upchDiskMu.Unlock()
	final := filepath.Join(dir, tagDir)
	_ = os.RemoveAll(final)
	if err := os.Rename(incoming, final); err != nil {
		return err
	}
	installed, known := installedReleaseTime()
	kept, dropped := upchPrunePlan(upchScanEntries(dir), installed, known, installedReleaseTagIs, upchCacheKeep)
	for _, e := range dropped {
		_ = os.RemoveAll(filepath.Join(dir, e.dir))
	}
	upchState.mu.Lock()
	upchState.entries = kept
	upchState.viewAt = time.Time{}
	upchState.mu.Unlock()
	return nil
}

// upchCheckFree refuses work that cannot fit on the card with room to spare.
// Where free space cannot be measured (Windows) it lets the work try.
func upchCheckFree(dir string, need int64) error {
	free, err := freeBytes(dir)
	if err != nil {
		return nil
	}
	if need < 0 {
		need = 0
	}
	if free < need+upchSpaceReserve {
		return fmt.Errorf("not enough free space on the card (%s free, %s needed)", humanBytes(free), humanBytes(need+upchSpaceReserve))
	}
	return nil
}

// upchExeDir is the folder of the running executable.
func upchExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// ── Install from the cache ───────────────────────────────────────────────────

// upchInstallCachedOp installs the newest cached release (a Patch operation).
func upchInstallCachedOp(ctx context.Context, config *Config) error {
	e, ok := upchBestReady()
	if !ok {
		return errors.New("no update is waiting in the cache")
	}
	return upchInstallEntry(ctx, config, e)
}

// upchInstallEntry checks a cached release again and installs it through the
// staging, journal and helper path every update takes. No network is used.
func upchInstallEntry(ctx context.Context, config *Config, e upchEntry) error {
	dir, err := upchDir()
	if err != nil {
		return err
	}
	installBarPhase(installPhaseVerify)
	patchRowStatus(ComponentApp, StatusChecking)
	upchDiskMu.Lock()
	err = upchStageEntry(ctx, dir, e)
	upchDiskMu.Unlock()
	if err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	upchNoteInstallSource(e.Tag, "cache")
	return OpApplyStagedSwap(ctx, config)
}

// upchStageEntry verifies a cache entry and stages it. An entry that fails its
// check is deleted: it would only fail again. upchDiskMu is held.
func upchStageEntry(ctx context.Context, dir string, e upchEntry) error {
	if e.dir == "" || filepath.Base(e.dir) != e.dir {
		return errors.New("the cached update is no longer on the card")
	}
	entryDir := filepath.Join(dir, e.dir)
	drop := func(err error) error {
		_ = os.RemoveAll(entryDir)
		upchForgetEntry(e.Tag)
		return fmt.Errorf("the cached update failed its check and was removed: %w", err)
	}
	if err := upchVerifyCached(entryDir, e); err != nil {
		return drop(err)
	}
	rel := &githubRelease{TagName: e.Tag, PublishedAt: e.PublishedAt, Body: e.Notes}
	storeReleaseNotes(rel)
	if e.Kind == upchKindDelta {
		tree := filepath.Join(entryDir, "tree")
		if err := jdeltaVerifyTree(filepath.Join(entryDir, e.File), tree); err != nil {
			return drop(err)
		}
		work, err := upchStageWorkDir("cache")
		if err != nil {
			return err
		}
		installBarPhase(installPhaseExtract)
		if err := upchCopyTree(tree, work); err != nil {
			_ = os.RemoveAll(work)
			return err
		}
		if err := upchStageTree(ctx, rel, work, e.Asset); err != nil {
			_ = os.RemoveAll(work)
			return err
		}
		return nil
	}
	if err := stageJukaHubArchive(ctx, rel, filepath.Join(entryDir, e.File), e.Asset); err != nil {
		return err
	}
	staging, err := stagingDirFor(e.Tag)
	if err != nil {
		return err
	}
	if err := upchCheckHostBinary(findStagedBinary(staging)); err != nil {
		_ = OpCancelStagedSwap(ctx, nil)
		return err
	}
	return nil
}

// upchVerifyCached checks a cached file before it is installed: its size and
// sha256 as verified at download time, and — for a signed release — the
// signature of the manifest kept beside it, again, offline.
func upchVerifyCached(entryDir string, e upchEntry) error {
	file := filepath.Join(entryDir, e.File)
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	if e.Size > 0 && info.Size() != e.Size {
		return errors.New("its size changed")
	}
	got, err := SHA256File(file)
	if err != nil {
		return err
	}
	if e.SHA256 != "" && !strings.EqualFold(got, e.SHA256) {
		return errors.New("its checksum does not match")
	}
	data, err := os.ReadFile(filepath.Join(entryDir, releasesign.AssetName))
	if err != nil {
		if e.Verified == upchVerifiedSigned {
			return errors.New("the signed manifest kept with it is missing")
		}
		return nil
	}
	m, _, err := upchVerifyManifestLocal(data)
	if err != nil {
		return err
	}
	t, err := releasesign.FindTarget(m, e.File)
	if err != nil {
		return err
	}
	if !strings.EqualFold(t.SHA256, got) || t.Size != info.Size() {
		return errors.New("it does not match its signed manifest")
	}
	return nil
}

// upchVerifyManifestLocal checks a signed manifest kept on the card: the key
// and the signature exactly as online. The validity window guards a download
// against a replayed old manifest, which a release fetched earlier or a file
// the user picked cannot be, so a window that has passed is reported (expired)
// rather than refused, and a clock the handheld lost (it has no RTC battery)
// is not held against a valid signature.
func upchVerifyManifestLocal(data []byte) (*releasesign.Manifest, bool, error) {
	var m releasesign.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false, fmt.Errorf("release.json is not readable: %w", err)
	}
	at := time.Now()
	expired := false
	if t, err := time.Parse(time.RFC3339, m.ExpiresAt); err == nil && at.After(t) {
		expired = true
	}
	if issued, err := time.Parse(time.RFC3339, m.IssuedAt); err == nil && (expired || at.Before(issued) || at.Before(minSaneClock)) {
		at = issued
	}
	if err := releasesign.Verify(&m, ed25519.PublicKey(patchRepoPublicKey), patchRepoKeyID, at); err != nil {
		return nil, false, err
	}
	return &m, expired, nil
}

// upchStageWorkDir is an empty working folder in the staging area, on the same
// card as the staged trees, so moving a finished tree into place is a rename.
func upchStageWorkDir(name string) (string, error) {
	base, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "staging", "work-"+name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// upchStageTree moves an unpacked or rebuilt release tree into the staging
// area under rel's tag and records it as the staged update, exactly as a
// downloaded archive is staged. tree is consumed.
func upchStageTree(ctx context.Context, rel *githubRelease, tree, asset string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bin := findStagedBinary(tree)
	if bin == "" {
		return errors.New("the update holds no JukaHub for this device")
	}
	if err := upchCheckHostBinary(bin); err != nil {
		return err
	}
	staging, err := stagingDirFor(rel.TagName)
	if err != nil {
		return err
	}
	_ = os.Remove(stagedReadyMarker(staging))
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tree, staging); err != nil {
		return err
	}
	if err := finishStagedRelease(rel, staging); err != nil {
		return err
	}
	recordStagedRelease(rel, asset)
	return nil
}

// upchCopyTree copies a folder, keeping file modes where the card allows.
func upchCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		// FAT cards refuse chmod; the staged executable is made runnable by
		// finishStagedRelease either way.
		_ = os.Chmod(target, info.Mode().Perm())
		return nil
	})
}

// ── Platform checks ──────────────────────────────────────────────────────────

// upchIsExecutableName reports whether name is JukaHub's executable on this
// platform.
func upchIsExecutableName(name string) bool {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(name)))
	if runtime.GOOS == "windows" {
		return base == "jukahub.exe"
	}
	return base == "jukahub"
}

// upchCheckHostBinary refuses an executable built for another machine: the
// ELF machine on Linux, the PE machine on Windows. An x86 build, or a Windows
// archive copied to the handheld, would otherwise install and never start.
func upchCheckHostBinary(path string) error {
	if path == "" {
		return errors.New("the update holds no JukaHub for this device")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	name := filepath.Base(path)
	if !executableHeaderOK(head) {
		return fmt.Errorf("%s is not a program for this device", name)
	}
	switch runtime.GOOS {
	case "linux":
		want := upchELFMachine(runtime.GOARCH)
		if want == 0 {
			return nil
		}
		// EI_DATA 1 is little-endian, which every build JukaHub ships is.
		if len(head) < 20 || head[5] != 1 || binary.LittleEndian.Uint16(head[18:20]) != want {
			return fmt.Errorf("%s is built for another processor", name)
		}
	case "windows":
		want := upchPEMachine(runtime.GOARCH)
		if want == 0 {
			return nil
		}
		if len(head) < 64 {
			return fmt.Errorf("%s is not a Windows program", name)
		}
		peAt := int64(binary.LittleEndian.Uint32(head[0x3c:0x40]))
		var pe [6]byte
		if _, err := f.ReadAt(pe[:], peAt); err != nil || string(pe[0:4]) != "PE\x00\x00" {
			return fmt.Errorf("%s is not a Windows program", name)
		}
		if binary.LittleEndian.Uint16(pe[4:6]) != want {
			return fmt.Errorf("%s is built for another processor", name)
		}
	}
	return nil
}

// upchELFMachine is the ELF e_machine of a Go architecture (0 when unknown).
func upchELFMachine(goarch string) uint16 {
	switch goarch {
	case "arm64":
		return 183
	case "amd64":
		return 62
	case "arm":
		return 40
	case "386":
		return 3
	}
	return 0
}

// upchPEMachine is the PE machine of a Go architecture (0 when unknown).
func upchPEMachine(goarch string) uint16 {
	switch goarch {
	case "amd64":
		return 0x8664
	case "386":
		return 0x14c
	case "arm64":
		return 0xaa64
	}
	return 0
}
