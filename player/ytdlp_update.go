package main

// ytdlp_update.go — keeping yt-dlp current.
//
// The helper was downloaded once, on first use, and nothing ever replaced it.
// That is the wrong lifetime for this particular tool: YouTube changes how its
// players and formats work every few weeks, and yt-dlp ships the matching fix
// within days. On 2026-08-17 YouTube started refusing every android_vr format
// (HTTP 403); yt-dlp 2026.08.19 stopped using that client the next day, but a
// card that had installed yt-dlp in July kept asking for it, so every video
// failed until the user found and deleted the binary by hand. From the outside
// that reads as "the YouTube app does not load videos", and the guess people
// make is an API key — which nothing here needs.
//
// So the installed copy is checked against the newest release:
//
//   - routinely, when Media opens and the installed release is older than
//     ytdlpStaleAfter (its version is its release date), at most once per
//     ytdlpRoutineCheckEvery, remembered across restarts;
//   - right away after a YouTube failure that an extractor update is the
//     likely fix for (ytFailure.wantsHelperUpdate: a 403, no playable format,
//     the bot check, an extractor error), at most once per
//     ytdlpFailureCheckEvery. A failed search is retried once when the update
//     lands; a failed play says to try again.
//
// The download reuses the installer's pieces: the same release lookup through
// the approved-repo list, the same staged ".part" file in the tools folder, the
// same header check, plus the release's own SHA2-256SUMS, which the first-run
// installer never compared. The swap is a rename in the same folder, so a run
// that is using the old binary keeps it (Linux keeps the old inode; Windows is
// allowed to rename a running .exe, not to overwrite it, so the old copy is
// moved aside first). The replaced copy is kept as "yt-dlp.bak", and the new
// one is run once: a release that cannot run on this firmware (a newer C
// library than the card has, say) is put back and remembered, instead of
// leaving every search and play broken with no way back. The 30-odd megabyte
// transfer never starts while a video is playing or loading; a found update
// waits for the player instead of being given up. Only the copy JukaHub
// installed in its own tools folder is replaced; a yt-dlp the user put on PATH
// or in a custom tools path is theirs, and the failure message says to update
// it instead.

import (
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
	"time"
)

const (
	// ytdlpStaleAfter is how old an installed release may be before a routine
	// check asks GitHub for a newer one. yt-dlp releases every few weeks, and
	// YouTube breakage is usually fixed in the next release.
	ytdlpStaleAfter = 10 * 24 * time.Hour
	// ytdlpRoutineCheckEvery bounds routine checks, across restarts too.
	ytdlpRoutineCheckEvery = 12 * time.Hour
	// ytdlpFailureCheckEvery bounds checks started by failures: a network
	// where every video fails must not ask GitHub on every press.
	ytdlpFailureCheckEvery = 90 * time.Minute
	// ytdlpUpdateBudget bounds one check plus download on a slow handheld link.
	ytdlpUpdateBudget = 10 * time.Minute
	// ytdlpVersionProbeLimit is generous on purpose: the one-file binary
	// unpacks itself before it can print its version, which takes seconds on
	// a handheld card (the Patch screen's 3 s probe often gives up there).
	ytdlpVersionProbeLimit = 30 * time.Second
	// ytdlpPlaybackWait is how often a pending download re-checks whether the
	// video that was playing has finished.
	ytdlpPlaybackWait = 5 * time.Second
	// ytdlpPostponeLimit is how long a found update waits for the player to go
	// idle. Past it the attempt counts as failed, so the next trigger, minutes
	// later, checks again.
	ytdlpPostponeLimit = 4 * time.Hour
	// ytdlpFailedRetryAfter is the pause after an attempt that failed (GitHub
	// or the download did not answer, or the player stayed busy): long enough
	// that a dead network is not asked on every failed play, short next to the
	// hours a check that did answer is trusted for.
	ytdlpFailedRetryAfter = 10 * time.Minute
	// ytdlpVerifyLimit bounds the one run of a freshly installed binary. It is
	// longer than the version probe: a new one-file build unpacks itself from
	// scratch on its first run.
	ytdlpVerifyLimit = time.Minute
	// ytdlpStalePartAge is the age past which a staged download is a leftover
	// (a power-off mid-transfer) rather than one in flight.
	ytdlpStalePartAge = time.Hour
	// ytdlpBadReleaseTTL is how long a release that would not run here is
	// skipped; a newer release is tried as soon as it is out.
	ytdlpBadReleaseTTL = 30 * 24 * time.Hour

	ytdlpReleaseRepo         = "yt-dlp/yt-dlp"
	ytdlpChecksumAsset       = "SHA2-256SUMS"
	ytdlpCheckedCacheKey     = "ytdlp:update-checked"
	ytdlpBadReleaseKeyPrefix = "ytdlp:bad-release:"
	// ytdlpBackupSuffix names the one previous generation kept next to the
	// binary for a rollback.
	ytdlpBackupSuffix = ".bak"
)

// ytdlpVersion is a yt-dlp release. Releases are named by their date
// (2026.10.26; nightlies append a time, 2026.11.04.232659), which is what makes
// "how old is this" answerable without asking the network.
type ytdlpVersion struct {
	year, month, day int
}

// parseYtDlpVersion reads a version as `yt-dlp --version` prints it, as a
// release tag names it, or with a channel prefix ("stable@2026.10.26").
func parseYtDlpVersion(s string) (ytdlpVersion, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		s = s[:i]
	}
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		s = s[at+1:]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	parts := strings.Split(s, ".")
	if len(parts) < 3 {
		return ytdlpVersion{}, false
	}
	var nums [3]int
	for i := range nums {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return ytdlpVersion{}, false
		}
		nums[i] = n
	}
	v := ytdlpVersion{year: nums[0], month: nums[1], day: nums[2]}
	if v.year < 2020 || v.year > 2100 || v.month < 1 || v.month > 12 || v.day < 1 || v.day > 31 {
		return ytdlpVersion{}, false
	}
	return v, true
}

// date is the day the release was cut.
func (v ytdlpVersion) date() time.Time {
	return time.Date(v.year, time.Month(v.month), v.day, 0, 0, 0, 0, time.UTC)
}

// String formats the version the way yt-dlp names it.
func (v ytdlpVersion) String() string {
	return fmt.Sprintf("%04d.%02d.%02d", v.year, v.month, v.day)
}

// newerThan reports whether v is a later release than o.
func (v ytdlpVersion) newerThan(o ytdlpVersion) bool {
	return v.date().After(o.date())
}

// ytdlpAgeDue reports whether a routine check is due. The version's own date is
// the best age there is; when the version is not known yet (the probe has not
// run), the install time of the binary stands in for it.
func ytdlpAgeDue(installed string, installedAt, now time.Time) bool {
	if v, ok := parseYtDlpVersion(installed); ok {
		return now.Sub(v.date()) > ytdlpStaleAfter
	}
	if !installedAt.IsZero() {
		return now.Sub(installedAt) > ytdlpStaleAfter
	}
	return false
}

// ytdlpUpdateNeeded reports whether the latest release is newer than the
// installed copy. With the installed version unknown, a release published
// after the binary was written is newer; with nothing to compare, the answer
// is no, so a check can never turn into a download loop.
func ytdlpUpdateNeeded(latestTag string, published time.Time, installed string, installedAt time.Time) bool {
	latest, okLatest := parseYtDlpVersion(latestTag)
	if have, ok := parseYtDlpVersion(installed); ok && okLatest {
		return latest.newerThan(have)
	}
	if !installedAt.IsZero() && !published.IsZero() {
		return published.After(installedAt)
	}
	return false
}

// ytdlpTrigger is why an update check was asked for.
type ytdlpTrigger int

const (
	ytdlpTriggerRoutine ytdlpTrigger = iota // Media opened; update an old install
	ytdlpTriggerFailure                     // a YouTube failure an update can fix
)

// ytdlpUpdateOutcome is how a check ended.
type ytdlpUpdateOutcome int

const (
	ytdlpOutcomeFailed  ytdlpUpdateOutcome = iota // the check or the download failed
	ytdlpOutcomeCurrent                           // the installed copy is the latest
	ytdlpOutcomeUpdated                           // a newer release was installed
)

var ytdlpUpdater struct {
	mu        sync.Mutex
	running   bool
	lastCheck time.Time // when a check last ended current or updated, this session
	current   bool      // that check found the installed copy current (or updated it)
	// retryAt holds the next attempt back after one that failed. It is not
	// lastCheck: a check that could not finish must not stand for the hours a
	// good answer is trusted (a newer release known but not installed used to
	// be "checked" for 90 minutes, and failing videos then said nothing).
	retryAt time.Time
	waiters []func(ytdlpUpdateOutcome)
	// routineNext keeps the routine gate cheap: Media can be entered many
	// times a minute, and the persisted check below is a database read.
	routineNext time.Time
}

// ytdlpManagedPath returns the yt-dlp the app runs and whether it is the copy
// JukaHub installed in its own tools folder (the only one it replaces). An
// empty path means none is installed: that is the installer's business.
func ytdlpManagedPath(config *Config) (string, bool) {
	resolved, err := helperExecutable("yt-dlp", config)
	if err != nil || resolved == "" {
		return "", false
	}
	dir := P().ToolsDir()
	if dir == "" {
		return resolved, false
	}
	exe := "yt-dlp"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	managed := filepath.Join(dir, exe)
	if !helperFileUsable(managed) || filepath.Clean(resolved) != filepath.Clean(managed) {
		return resolved, false
	}
	return managed, true
}

// ytdlpFileTime is when the binary was written: its install or last update.
func ytdlpFileTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ytdlpCheckedRecently reports whether a check ran within the routine interval,
// in this run or an earlier one (the handheld restarts the app often).
func ytdlpCheckedRecently() bool {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return false
	}
	v, err := cacheGet(db, ytdlpCheckedCacheKey)
	return err == nil && v != nil
}

// ytdlpNoteChecked persists that a check finished: the installed copy was found
// current, or was updated. A check that failed or an update still waiting on
// the player is not written, so the next launch asks again.
func ytdlpNoteChecked() {
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheSet(db, ytdlpCheckedCacheKey, []byte(time.Now().UTC().Format(time.RFC3339)), ytdlpRoutineCheckEvery)
	}
}

// requestYtDlpUpdate starts a background check, and an update when a newer
// release exists. It returns what the caller can tell the user right now:
// ytUpdateStarted while a check runs, ytUpdateCurrent when a recent check
// found the installed copy current, ytUpdateUnmanaged for a yt-dlp JukaHub did
// not install, ytUpdateNone otherwise. done, when not nil and a check was
// started or joined, runs on the update goroutine once it ends.
func requestYtDlpUpdate(config *Config, trigger ytdlpTrigger, done func(ytdlpUpdateOutcome)) ytUpdateState {
	now := time.Now()
	ytdlpUpdater.mu.Lock()
	if ytdlpUpdater.running {
		if done != nil {
			ytdlpUpdater.waiters = append(ytdlpUpdater.waiters, done)
		}
		ytdlpUpdater.mu.Unlock()
		return ytUpdateStarted
	}
	if trigger == ytdlpTriggerRoutine {
		if now.Before(ytdlpUpdater.routineNext) {
			ytdlpUpdater.mu.Unlock()
			return ytUpdateNone
		}
		ytdlpUpdater.routineNext = now.Add(10 * time.Minute)
	}
	if now.Before(ytdlpUpdater.retryAt) {
		ytdlpUpdater.mu.Unlock()
		return ytUpdateNone
	}
	interval := ytdlpRoutineCheckEvery
	if trigger == ytdlpTriggerFailure {
		interval = ytdlpFailureCheckEvery
	}
	if !ytdlpUpdater.lastCheck.IsZero() && now.Sub(ytdlpUpdater.lastCheck) < interval {
		current := ytdlpUpdater.current
		ytdlpUpdater.mu.Unlock()
		if current && trigger == ytdlpTriggerFailure {
			return ytUpdateCurrent
		}
		return ytUpdateNone
	}
	ytdlpUpdater.mu.Unlock()

	target, managed := ytdlpManagedPath(config)
	if target == "" {
		return ytUpdateNone
	}
	if !managed {
		if trigger == ytdlpTriggerFailure {
			return ytUpdateUnmanaged
		}
		return ytUpdateNone
	}
	// A check that cannot reach GitHub, or that would fail every certificate
	// on a clock reset to 1970, is not worth starting.
	if !IsOnline() || bugrepClockWrong(now) {
		return ytUpdateNone
	}
	if trigger == ytdlpTriggerRoutine {
		if playerPhase() != "idle" || videoPrepareActive() {
			return ytUpdateNone
		}
		if !ytdlpAgeDue(ytdlpCachedVersion(), ytdlpFileTime(target), now) || ytdlpCheckedRecently() {
			return ytUpdateNone
		}
	}

	ytdlpUpdater.mu.Lock()
	if ytdlpUpdater.running {
		if done != nil {
			ytdlpUpdater.waiters = append(ytdlpUpdater.waiters, done)
		}
		ytdlpUpdater.mu.Unlock()
		return ytUpdateStarted
	}
	ytdlpUpdater.running = true
	if done != nil {
		ytdlpUpdater.waiters = append(ytdlpUpdater.waiters, done)
	}
	ytdlpUpdater.mu.Unlock()
	go ytdlpRunUpdate(target, trigger)
	return ytUpdateStarted
}

// ytdlpRunUpdate is the check and, when needed, the update. It always releases
// the running flag and tells every waiter how it ended. Only an attempt that
// ended with the copy current or updated counts as a check for the hours that
// gate the next one (and is remembered across restarts): one that could not
// reach GitHub, a download that failed and an update the player kept waiting
// are tried again ytdlpFailedRetryAfter later.
func ytdlpRunUpdate(target string, trigger ytdlpTrigger) {
	outcome := ytdlpOutcomeFailed
	defer func() {
		now := time.Now()
		ytdlpUpdater.mu.Lock()
		ytdlpUpdater.running = false
		if outcome == ytdlpOutcomeFailed {
			ytdlpUpdater.retryAt = now.Add(ytdlpFailedRetryAfter)
		} else {
			ytdlpUpdater.lastCheck = now
			ytdlpUpdater.current = true
		}
		waiters := ytdlpUpdater.waiters
		ytdlpUpdater.waiters = nil
		ytdlpUpdater.mu.Unlock()
		for _, w := range waiters {
			w(outcome)
		}
	}()
	ytdlpSweepStale(filepath.Dir(target), filepath.Base(target), time.Now())
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), ytdlpUpdateBudget)
	defer cancelCheck()

	rel, err := ytdlpLatestRelease(checkCtx)
	if err != nil {
		logWarn("tools", "yt-dlp update check failed: %v", err)
		if trigger == ytdlpTriggerFailure {
			ShowToast("Could not check for a newer yt-dlp — "+ytdlpToastReason(err), ToastKindWarning)
		}
		return
	}
	installed := ytdlpInstalledVersion(checkCtx, target)
	if !ytdlpUpdateNeeded(rel.tag, rel.published, installed, ytdlpFileTime(target)) {
		outcome = ytdlpOutcomeCurrent
		ytdlpNoteChecked()
		logInfo("tools", "yt-dlp %s is current (latest release %s)", ytdlpVersionLabel(installed), rel.tag)
		if trigger == ytdlpTriggerFailure {
			// The failure's message said a check had started; it says how
			// that ended, or the user waits for an update that is not coming.
			ShowToast("yt-dlp is already the newest version — try again later", ToastKindInfo)
		}
		return
	}
	if ytdlpReleaseMarkedBad(rel.tag) {
		// This release would not run on this device last time; the copy that
		// works stays until a newer release is out.
		outcome = ytdlpOutcomeCurrent
		ytdlpNoteChecked()
		logInfo("tools", "yt-dlp %s did not run on this device before; staying on %s", rel.tag, ytdlpVersionLabel(installed))
		if trigger == ytdlpTriggerFailure {
			ShowToast("The newest yt-dlp ("+rel.tag+") does not run on this device — keeping the current one", ToastKindWarning)
		}
		return
	}
	// The transfer waits for the player: on the handheld's Wi-Fi it would
	// compete with the stream, and the old binary works for what is playing.
	// It waits on its own clock, not the check's: a film used to outlast the
	// ten-minute budget, and the update it had found was then dropped.
	if !ytdlpWaitForIdlePlayer(ytdlpPostponeLimit) {
		logInfo("tools", "yt-dlp update to %s postponed: the player stayed busy for %v", rel.tag, ytdlpPostponeLimit)
		return
	}
	logInfo("tools", "updating yt-dlp %s to %s", ytdlpVersionLabel(installed), rel.tag)
	if trigger == ytdlpTriggerFailure {
		ShowToast("Updating yt-dlp to "+rel.tag+"…", ToastKindInfo)
	}
	installCtx, cancelInstall := context.WithTimeout(context.Background(), ytdlpUpdateBudget)
	defer cancelInstall()
	if err := ytdlpInstallRelease(installCtx, rel, target); err != nil {
		logWarn("tools", "yt-dlp update to %s failed: %v", rel.tag, err)
		var broken *ytdlpBrokenReleaseError
		if errors.As(err, &broken) {
			ShowToast("yt-dlp "+rel.tag+" does not run on this device — kept the previous one", ToastKindWarning)
			return
		}
		ShowToast("yt-dlp update failed — "+ytdlpToastReason(err), ToastKindWarning)
		return
	}
	invalidateToolVersionCache()
	outcome = ytdlpOutcomeUpdated
	ytdlpNoteChecked()
	logInfo("tools", "yt-dlp updated to %s at %s", rel.tag, target)
	if trigger == ytdlpTriggerFailure {
		ShowToast("yt-dlp updated to "+rel.tag+" — try the video again", ToastKindSuccess)
		return
	}
	ShowToast("yt-dlp updated to "+rel.tag, ToastKindSuccess)
}

// ytdlpVersionLabel names an installed version in a log line.
func ytdlpVersionLabel(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(version unknown)"
	}
	return strings.TrimSpace(v)
}

// ytdlpToastReason is toolInstallFailureReason without the install card's
// "press A" ending: the card retries on A, an updater toast has nothing to
// press.
func ytdlpToastReason(err error) string {
	reason := toolInstallFailureReason(err)
	for _, tail := range []string{", then press A", " — press A to retry"} {
		reason = strings.TrimSuffix(reason, tail)
	}
	return reason
}

// ytdlpWaitForIdlePlayer waits until nothing is playing or being prepared,
// for at most limit, and reports whether the player went idle.
func ytdlpWaitForIdlePlayer(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for playerPhase() != "idle" || videoPrepareActive() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(ytdlpPlaybackWait)
	}
	return true
}

// ytdlpMarkBadRelease remembers a release that would not run on this device,
// so the next check does not download it again.
func ytdlpMarkBadRelease(tag string) {
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheSet(db, ytdlpBadReleaseKeyPrefix+tag, []byte(time.Now().UTC().Format(time.RFC3339)), ytdlpBadReleaseTTL)
	}
}

// ytdlpReleaseMarkedBad reports whether tag is a release ytdlpMarkBadRelease
// remembered.
func ytdlpReleaseMarkedBad(tag string) bool {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return false
	}
	v, err := cacheGet(db, ytdlpBadReleaseKeyPrefix+tag)
	return err == nil && v != nil
}

// ytdlpCachedVersion is the version the Patch screen's probe last read, without
// starting a probe: every yt-dlp run unpacks the one-file binary onto the card,
// and opening Media must not cost one just to read a version.
func ytdlpCachedVersion() string {
	toolVersionCache.mu.Lock()
	defer toolVersionCache.mu.Unlock()
	return toolVersionCache.entries["yt-dlp"].value
}

// ytdlpInstalledVersion is the installed copy's version: the Patch screen's
// cached probe when it has one, otherwise a probe with a handheld-sized limit.
func ytdlpInstalledVersion(ctx context.Context, path string) string {
	if v := ytdlpCachedVersion(); v != "" {
		if _, ok := parseYtDlpVersion(v); ok {
			return v
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, ytdlpVersionProbeLimit)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "--version")
	cmd.Env = helperToolEnv(path)
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.Output()
	if err != nil {
		logDebug("tools", "yt-dlp version probe failed: %v", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ytdlpRelease is the part of a GitHub release the updater reads.
type ytdlpRelease struct {
	tag       string
	published time.Time
	assets    []releaseAsset
}

// ytdlpLatestRelease asks GitHub for yt-dlp's latest release: its tag (the
// version), when it was published and its files.
func ytdlpLatestRelease(ctx context.Context) (ytdlpRelease, error) {
	if !allowedToolRepos[ytdlpReleaseRepo] {
		return ytdlpRelease{}, fmt.Errorf("tool repo %q is not in the approved download list", ytdlpReleaseRepo)
	}
	apiURL := "https://api.github.com/repos/" + ytdlpReleaseRepo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return ytdlpRelease{}, err
	}
	req.Header.Set("User-Agent", "JukaHub-Patch-Tool/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	// ytTransport: the certificate fallback for a firmware with no store, the
	// same one the download and the checksum fetch use.
	resp, err := (&http.Client{Timeout: 45 * time.Second, Transport: ytTransport()}).Do(req)
	if err != nil {
		return ytdlpRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ytdlpRelease{}, fmt.Errorf("github api answered HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, patchMaxMetadata)).Decode(&rel); err != nil {
		return ytdlpRelease{}, fmt.Errorf("read the release: %w", err)
	}
	out := ytdlpRelease{tag: strings.TrimSpace(rel.TagName)}
	if t, err := time.Parse(time.RFC3339, rel.PublishedAt); err == nil {
		out.published = t
	}
	for _, a := range rel.Assets {
		out.assets = append(out.assets, releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	if out.tag == "" || len(out.assets) == 0 {
		return ytdlpRelease{}, fmt.Errorf("the latest release lists no version or files")
	}
	return out, nil
}

// ytdlpInstallRelease downloads this platform's binary from rel next to
// target, checks it, and swaps it in.
func ytdlpInstallRelease(ctx context.Context, rel ytdlpRelease, target string) error {
	asset, err := ytDlpAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	file, ok := pickReleaseAsset(rel.assets, []string{asset})
	if !ok {
		return fmt.Errorf("release %s has no %s", rel.tag, asset)
	}
	if !strings.HasPrefix(strings.ToLower(file.URL), "https://github.com/") {
		return fmt.Errorf("refusing a yt-dlp download from outside github.com")
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".yt-dlp-*.part")
	if err != nil {
		return fmt.Errorf("stage the download: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	// After a successful swap the staged name no longer exists and this is a
	// no-op; on every failure it removes the partial file.
	defer os.Remove(tmpPath)
	if err := downloadFileWithProgressCtx(ctx, file.URL, tmpPath, nil); err != nil {
		return fmt.Errorf("download yt-dlp %s: %w", rel.tag, err)
	}
	if err := verifyHelperTool(tmpPath); err != nil {
		return fmt.Errorf("the downloaded yt-dlp is unusable: %w", err)
	}
	if sums, ok := pickReleaseAsset(rel.assets, []string{ytdlpChecksumAsset}); ok {
		list, err := toolboxFetchSmall(ctx, sums.URL)
		if err != nil {
			return fmt.Errorf("download the published checksums: %w", err)
		}
		got, err := hashFile(tmpPath)
		if err != nil {
			return fmt.Errorf("hash the download: %w", err)
		}
		verified, err := toolboxMatchChecksum(got, string(list), asset, false)
		if err != nil {
			return err
		}
		if !verified {
			logWarn("tools", "%s of yt-dlp %s does not list %s; installing on the header check alone", ytdlpChecksumAsset, rel.tag, asset)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, PermExec); err != nil {
			logWarn("tools", "cannot mark the new yt-dlp executable: %v", err)
		}
	}
	backup, err := ytdlpReplaceBinary(tmpPath, target)
	if err != nil {
		return err
	}
	// The header and the checksum say the file is the release; they do not say
	// it runs on this firmware's loader and C library, and yt-dlp has raised
	// its Linux build base before. Installs used to happen once, by hand; now
	// they happen unattended every few weeks, so the new copy is run once here
	// and the previous one is put back on a clear failure.
	result, detail := ytdlpProbeBinary(target)
	switch result {
	case ytdlpProbeBroken:
		ytdlpMarkBadRelease(rel.tag)
		if rbErr := ytdlpRollback(backup, target); rbErr != nil {
			return fmt.Errorf("yt-dlp %s does not run on this device (%s), and the previous copy could not be put back: %w",
				rel.tag, detail, rbErr)
		}
		return &ytdlpBrokenReleaseError{tag: rel.tag, reason: detail}
	case ytdlpProbeInconclusive:
		logWarn("tools", "the new yt-dlp %s did not confirm that it runs (%s); keeping it", rel.tag, detail)
	}
	return nil
}

// ytdlpBrokenReleaseError is an update whose new binary would not run on this
// device and was rolled back.
type ytdlpBrokenReleaseError struct {
	tag    string
	reason string
}

func (e *ytdlpBrokenReleaseError) Error() string {
	return "yt-dlp " + e.tag + " does not run on this device (" + e.reason + "); the previous copy was put back"
}

// ytdlpProbeResult is what one run of a freshly installed binary showed.
type ytdlpProbeResult int

const (
	ytdlpProbeOK           ytdlpProbeResult = iota // it ran
	ytdlpProbeInconclusive                         // it timed out, or could not unpack (the old copy would fail too)
	ytdlpProbeBroken                               // it cannot run here: exec format, loader or C library, permission, an error exit
)

// classifyYtDlpProbe reads the outcome of `yt-dlp --version`. Only a clear
// failure condemns a release: a run that ran out of time on a slow card, a
// one-file unpack that failed for lack of space, or a process the kernel
// killed (the OOM killer on a 1 GB handheld) says nothing about it.
//
// A loader that cannot run the binary at all comes first, because its
// bootloader flavour ("[PYI-…] Failed to load Python shared library … GLIBC_x
// not found") carries the unpack prefix: read as an unpack failure, the one
// release the rollback exists for was kept, and every search afterwards blamed
// the SD card.
func classifyYtDlpProbe(stderr string, err error, timedOut bool) ytdlpProbeResult {
	switch {
	case err == nil:
		return ytdlpProbeOK
	case helperCannotRun(stderr) || helperCannotRun(err.Error()):
		return ytdlpProbeBroken
	case timedOut:
		return ytdlpProbeInconclusive
	case helperUnpackFailure(stderr):
		return ytdlpProbeInconclusive
	case ytdlpKilledBySignal(err):
		return ytdlpProbeInconclusive
	}
	return ytdlpProbeBroken
}

// ytdlpKilledBySignal reports a run that ended by a signal rather than by
// exiting. ExitError embeds the process state, whose Exited is false exactly
// then.
func ytdlpKilledBySignal(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		return !exitErr.Exited()
	}
	return false
}

// ytdlpProbeBinary runs the installed binary once, the way every search runs
// it, and returns the verdict with a short reason for the log and the toast.
func ytdlpProbeBinary(path string) (ytdlpProbeResult, string) {
	ctx, cancel := context.WithTimeout(context.Background(), ytdlpVerifyLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = helperToolEnv(path)
	cmd.WaitDelay = 3 * time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	timedOut := ctx.Err() == context.DeadlineExceeded
	result := classifyYtDlpProbe(stderr.String(), err, timedOut)
	switch result {
	case ytdlpProbeOK:
		return result, "version " + firstLine(strings.TrimSpace(stdout.String()))
	case ytdlpProbeInconclusive:
		if timedOut {
			return result, "no answer within " + ytdlpVerifyLimit.String()
		}
	}
	detail := firstLine(strings.TrimSpace(stderr.String()))
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return result, redactDiagnosticText(detail)
}

// ytdlpReplaceBinary moves the staged binary over target and returns where the
// copy it replaced was kept (one generation, target+".bak"; "" when there was
// none), so a release that cannot run here can be put back (ytdlpRollback).
// Both live in the tools folder, so this is a rename, never a copy over a file
// in use.
func ytdlpReplaceBinary(staged, target string) (string, error) {
	// The installer holds this lock while it writes into the tools folder.
	helperInstallMu.Lock()
	defer helperInstallMu.Unlock()
	if _, err := os.Stat(target); os.IsNotExist(err) {
		// Nothing to keep (removed by hand meanwhile): just install.
		if err := os.Rename(staged, target); err != nil {
			return "", fmt.Errorf("install the new yt-dlp: %w", err)
		}
		return "", nil
	}
	backup := target + ytdlpBackupSuffix
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		// The previous backup is still running (Windows will not delete a
		// running .exe): the old copy steps aside under a name nothing holds,
		// which ytdlpSweepStale removes at a later update.
		backup = target + ".old-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if runtime.GOOS != "windows" {
		// A second name for the old file, then one atomic rename, so a search
		// starting this instant always finds a yt-dlp. A FAT card has no hard
		// links; there the old copy is renamed aside below, a gap of one
		// rename.
		if err := os.Link(target, backup); err == nil {
			if err := os.Rename(staged, target); err != nil {
				_ = os.Remove(backup)
				return "", fmt.Errorf("install the new yt-dlp: %w", err)
			}
			return backup, nil
		}
	}
	// Windows refuses to overwrite a running .exe but lets it be renamed, so
	// the old copy steps aside first and is put back if the swap fails. A
	// search or preload still using it keeps running from the new name.
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("move the old yt-dlp aside: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(backup, target)
		return "", fmt.Errorf("install the new yt-dlp: %w", err)
	}
	return backup, nil
}

// ytdlpRollback puts the copy ytdlpReplaceBinary kept back over target.
func ytdlpRollback(backup, target string) error {
	if backup == "" {
		return errors.New("no previous copy was kept")
	}
	helperInstallMu.Lock()
	defer helperInstallMu.Unlock()
	if _, err := os.Stat(backup); err != nil {
		return err
	}
	return os.Rename(backup, target)
}

// ytdlpSweepStale removes what an interrupted or earlier update left in the
// tools folder: staged ".yt-dlp-*.part" downloads older than
// ytdlpStalePartAge (a power-off mid-transfer leaves ~35 MB on the card; a
// younger one may be an install in flight) and the ".old" copies a Windows
// swap could not delete while they ran. The ".bak" copy is the one generation
// kept on purpose. A file still in use stays until the next sweep.
func ytdlpSweepStale(dir, base string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		stalePart := strings.HasPrefix(name, ".yt-dlp-") && strings.HasSuffix(name, ".part")
		oldCopy := strings.HasPrefix(name, base+".old")
		if e.IsDir() || (!stalePart && !oldCopy) {
			continue
		}
		if stalePart {
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) < ytdlpStalePartAge {
				continue
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			logDebug("tools", "removed the leftover %s", name)
		}
	}
}
