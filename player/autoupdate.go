package main

// autoupdate.go implements the automatic JukaHub updater and the release build
// stamp shown in the UI.
//
// On startup (and from MISC -> Patch) the app asks GitHub for the latest
// JukaHub release and compares its publication time with the compile stamp of
// the running installation. A newer release is only advertised: nothing is
// downloaded, staged or installed until the user confirms the install in the
// Patch scene. After that confirmation the platform archive
//
//	JukaHub-Windows.zip   (Windows x64)
//	JukaHub-Trimui.zip    (TrimUI Linux arm64)
//
// is extracted into the Patch staging area, and the replacement of the files
// (binary, SDL DLLs, config, fonts, backgrounds) is handed to a detached
// helper script that restarts JukaHub. A journal is written before any
// destructive step, so an interrupted update is rolled back at next startup.
//
// GitHub release tags in this repository are timestamps (JukaHub-20261017…),
// not semantic versions, so update decisions compare release times, not
// semver. release-info.json (written by the release build and by every
// successful update) carries the applied release stamp.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// buildDate and buildTime are stamped by the release build and by build.bat:
//
//	go build -ldflags "-X main.buildDate=2026-10-17 -X main.buildTime=2026-10-17T18:30:00Z"
//
// buildTime is the precise UTC compile time, so update decisions can compare
// against a same-day release instead of assuming the build happened at the end
// of the day. buildDate is kept for builds that only carry a date.
// release-info.json (bundled in the archive and written on every update) is
// still the primary source when it exists.
var (
	buildDate string
	buildTime string
)

const (
	jukaHubReleaseDownloadBase = "https://github.com/jukaLang/JukaHubV2/releases/latest/download/"
	releaseInfoFileName        = "release-info.json"
	// autoUpdateCheckInterval is the anti-flap cooldown between background
	// release checks. The check itself runs at every startup; the cooldown
	// only stops a crash-restart loop from hammering the GitHub API.
	autoUpdateCheckInterval = 30 * time.Minute
	autoUpdateMaxFiles      = 128
	autoUpdateStartupDelay  = 4 * time.Second
)

// releaseInfo records the release an installation was built from or updated to.
type releaseInfo struct {
	Tag     string `json:"tag,omitempty"`
	Date    string `json:"date,omitempty"` // RFC3339 publication time
	Version string `json:"version,omitempty"`
}

// releaseInfoPath returns the stamp file for the running installation.
func releaseInfoPath(dir string) string {
	return filepath.Join(dir, releaseInfoFileName)
}

// releaseInfoOnce caches the installation stamp: it cannot change while the
// process runs, and the UI reads it every frame.
var (
	releaseInfoOnce  sync.Once
	releaseInfoCache releaseInfo
)

// readInstalledReleaseInfo reads the stamp file next to the executable.
// A missing or malformed file is simply an unknown stamp.
func readInstalledReleaseInfo() releaseInfo {
	releaseInfoOnce.Do(func() {
		dir, err := P().ExecutableDir()
		if err != nil || dir == "" {
			return
		}
		data, err := os.ReadFile(releaseInfoPath(dir))
		if err != nil {
			return
		}
		var info releaseInfo
		if json.Unmarshal(data, &info) != nil {
			return
		}
		releaseInfoCache = info
	})
	return releaseInfoCache
}

// readReleaseInfoFromDir reads a release stamp from any directory. The staged
// tree carries the same file as an installation, so the ready marker and the
// installed stamp are the same format.
func readReleaseInfoFromDir(dir string) releaseInfo {
	var info releaseInfo
	data, err := os.ReadFile(releaseInfoPath(dir))
	if err != nil {
		return releaseInfo{}
	}
	if json.Unmarshal(data, &info) != nil {
		return releaseInfo{}
	}
	return info
}

// writeReleaseInfo writes the stamp file into dir atomically.
func writeReleaseInfo(dir string, info releaseInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return AtomicWrite(releaseInfoPath(dir), append(data, '\n'), 0o644)
}

// allDigits reports whether s is non-empty and entirely ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// releaseTimeFromTag extracts the UTC timestamp embedded in a release tag of
// the form "JukaHub-20261017120000" (the format the release workflow creates).
func releaseTimeFromTag(tag string) (time.Time, bool) {
	s := strings.TrimSpace(tag)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "JukaHub-")
	if len(s) < 14 || !allDigits(s[:14]) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102150405", s[:14], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseReleaseTimestamp parses an RFC3339 release timestamp.
func parseReleaseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// releasePublishedTime returns the best-known publication time of a release.
func releasePublishedTime(rel *githubRelease) (time.Time, bool) {
	if rel == nil {
		return time.Time{}, false
	}
	if t, ok := parseReleaseTimestamp(rel.PublishedAt); ok {
		return t, true
	}
	return releaseTimeFromTag(rel.TagName)
}

// releaseDateLabel returns the YYYY-MM-DD stamp shown in the UI.
func releaseDateLabel(rel *githubRelease) string {
	if t, ok := releasePublishedTime(rel); ok {
		return t.Format("2006-01-02")
	}
	return ""
}

// releaseStampISO returns the RFC3339 stamp stored in release-info.json.
func releaseStampISO(rel *githubRelease) string {
	if t, ok := releasePublishedTime(rel); ok {
		return t.Format(time.RFC3339)
	}
	return ""
}

// minSaneClock is the oldest timestamp believed to come from a correct clock.
// The handheld has no RTC battery: after a power cycle its clock can sit at a
// factory epoch until NTP catches up, and a stamp taken from such a clock must
// never suppress a check.
var minSaneClock = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// autoUpdateCheckDue reports whether the background release check may run,
// given the stamp of the last successful check. Only a recent, sane, past
// stamp suppresses a check: a missing, garbled, future (a clock that rolled
// backwards after a power cycle) or implausibly old stamp all allow it.
func autoUpdateCheckDue(last string, now time.Time) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok {
		return true
	}
	if now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return true
	}
	return now.Sub(t) >= autoUpdateCheckInterval
}

// installedReleaseTime returns the stamped release of the running install.
// The second result is false when no stamp is known (a fresh source build or a
// pre-stamp installation).
func installedReleaseTime() (time.Time, bool) {
	if info := readInstalledReleaseInfo(); info.Date != "" || info.Tag != "" {
		if t, ok := parseReleaseTimestamp(info.Date); ok {
			return t, true
		}
		if t, ok := releaseTimeFromTag(info.Tag); ok {
			return t, true
		}
	}
	patchStateMu.Lock()
	stamp := patchState.InstalledReleaseAt
	patchStateMu.Unlock()
	if t, ok := parseReleaseTimestamp(stamp); ok {
		return t, true
	}
	// The compile stamp is last: it describes a source build, not a release.
	if t, ok := parseReleaseTimestamp(buildTime); ok {
		return t, true
	}
	if t, ok := parseReleaseTimestamp(buildDate); ok {
		// A build stamp is date-only: treat it as the end of that day so a
		// release published earlier the same day is not "newer".
		if len(strings.TrimSpace(buildDate)) == len("2006-01-02") {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t, true
	}
	return time.Time{}, false
}

// installedBuildDate returns the YYYY-MM-DD build/release date of the running
// installation, or "" when unknown.
func installedBuildDate() string {
	if t, ok := installedReleaseTime(); ok {
		return t.Format("2006-01-02")
	}
	return ""
}

// releaseIsNewerThanInstalled reports whether the release should be installed.
// An unknown installed stamp means an old build worth updating.
func releaseIsNewerThanInstalled(rel *githubRelease) bool {
	relTime, ok := releasePublishedTime(rel)
	if !ok {
		return false
	}
	installed, known := installedReleaseTime()
	if !known {
		return true
	}
	return relTime.After(installed)
}

// markUpdateAvailable records that a newer release exists. It deliberately does
// not download or stage anything: the Patch scene turns this into an "Install
// update" action that only runs after the user confirms.
// storeReleaseNotes caches the release markdown for the in-app changelog. It
// never blocks the caller on disk: state is written only when the notes for
// this tag actually changed, so routine update checks do not rewrite state.json
// every launch.
func storeReleaseNotes(rel *githubRelease) {
	if rel == nil || strings.TrimSpace(rel.TagName) == "" {
		return
	}
	body := strings.TrimSpace(rel.Body)
	if body == "" {
		return
	}
	if len(body) > 200_000 {
		body = body[:200_000]
	}
	patchStateMu.Lock()
	if patchState.ReleaseNotesTag == rel.TagName && patchState.ReleaseNotesText == body {
		patchStateMu.Unlock()
		return
	}
	patchState.ReleaseNotesTag = rel.TagName
	patchState.ReleaseNotesText = body
	patchStateMu.Unlock()
	_ = savePatchState()
}

func markUpdateAvailable(rel *githubRelease) {
	if rel == nil {
		return
	}
	asset := ""
	if target, err := jukaHubUpdateTargetFor(DetectDevice(), runtime.GOOS, runtime.GOARCH); err == nil {
		asset = target.Asset
	}
	patchStateMu.Lock()
	patchState.AvailableUpdateTag = rel.TagName
	patchState.AvailableUpdateAt = releaseStampISO(rel)
	patchState.AvailableUpdateAsset = asset
	patchStateMu.Unlock()
	_ = savePatchState()
}

// clearAvailableUpdate forgets an advertised release (already current, or the
// install has been applied).
func clearAvailableUpdate() {
	patchStateMu.Lock()
	had := patchState.AvailableUpdateTag != ""
	patchState.AvailableUpdateTag = ""
	patchState.AvailableUpdateAt = ""
	patchState.AvailableUpdateAsset = ""
	patchStateMu.Unlock()
	if had {
		_ = savePatchState()
	}
}

// availableUpdateTag returns the advertised release tag, if any.
func availableUpdateTag() string {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return patchState.AvailableUpdateTag
}

// availableUpdateLabel is the short human label for an advertised release
// ("2026-10-18", falling back to the tag).
func availableUpdateLabel() string {
	patchStateMu.Lock()
	tag, at := patchState.AvailableUpdateTag, patchState.AvailableUpdateAt
	patchStateMu.Unlock()
	if t, ok := parseReleaseTimestamp(at); ok {
		return t.Format("2006-01-02")
	}
	if t, ok := releaseTimeFromTag(tag); ok {
		return t.Format("2006-01-02")
	}
	if tag != "" {
		return tag
	}
	return "new release"
}

// errAlreadyUpToDate reports that the advertised release is not newer than the
// running build (a stale advertisement, or a build that already applied it).
var errAlreadyUpToDate = errors.New("already up to date")

// InstallAvailableJukaHubUpdate downloads, stages and applies the newest release.
// It runs only after the user confirms in the Patch scene; nothing in the
// background path calls it.
func InstallAvailableJukaHubUpdate(ctx context.Context, config *Config) error {
	rel, err := FetchLatestJukaHubRelease(ctx)
	if err != nil {
		return fmt.Errorf("checking JukaHub releases: %w", err)
	}
	if !releaseIsNewerThanInstalled(rel) {
		clearAvailableUpdate()
		installed := installedBuildDate()
		if installed == "" {
			installed = versionString(config)
		}
		return fmt.Errorf("%w (%s)", errAlreadyUpToDate, installed)
	}
	if err := StageJukaHubUpdate(ctx, config, rel); err != nil {
		return err
	}
	return OpApplyStagedSwap(ctx, config)
}

// releaseAssetDigest turns a release asset's API digest ("sha256:<hex>") into
// the bare hash downloadToFile verifies. Any other algorithm, an empty value or
// a malformed hash yields "", so the caller falls back to no check rather than
// rejecting a release it cannot verify.
func releaseAssetDigest(digest string) string {
	const prefix = "sha256:"
	d := strings.TrimSpace(digest)
	if len(d) != len(prefix)+64 || !strings.EqualFold(d[:len(prefix)], prefix) {
		return ""
	}
	h := d[len(prefix):]
	for _, c := range []byte(h) {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return ""
		}
	}
	return strings.ToLower(h)
}

// announceAvailableUpdate surfaces a pending update: an immediate toast plus a
// notification that stays in the bell until read, so a missed toast still
// leaves a visible sign that an update is waiting. notifPush draws the toast.
func announceAvailableUpdate() {
	label := availableUpdateLabel()
	logPatch("auto-update: offering %s", label)
	notifPush("JukaHub "+label+" is available — press the download icon in the header", notifInfo)
}

// startAutoUpdateCheck runs the startup update check in the background. It is
// best-effort and never installs: a newer release is only advertised, and the
// Patch scene asks the user before anything is downloaded. Offline launches and
// API failures are logged, never surfaced as startup errors.
func startAutoUpdateCheck(config *Config) {
	go func() {
		time.Sleep(autoUpdateStartupDelay)
		if strings.TrimSpace(os.Getenv("JUKAHUB_NO_AUTOUPDATE")) != "" {
			logPatch("auto-update: disabled by JUKAHUB_NO_AUTOUPDATE")
			return
		}
		logPatch("auto-update: startup check")
		patchStateMu.Lock()
		last := patchState.AutoUpdateCheckedAt
		pending := patchState.AvailableUpdateTag != ""
		patchStateMu.Unlock()

		// Offer a known update on every boot. The toast used to fire only at
		// discovery time, so one missed toast left nothing visible and the
		// device looked like it never offered the release at all.
		if pending {
			announceAvailableUpdate()
		}

		if !autoUpdateCheckDue(last, time.Now()) {
			return
		}
		if !tryBeginPatchOp() {
			// Worth a log line: a skipped check means this boot never reaches
			// the release feed, which otherwise looks like a silent updater.
			logPatch("auto-update: check skipped, another Patch operation is running")
			return
		}
		defer endPatchOp()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		rel, err := FetchLatestJukaHubRelease(ctx)
		if err != nil {
			logPatch("auto-update check failed: %v", err)
			return
		}
		// Keep the release notes even when the user does not install this
		// build: the changelog screen can then show what is waiting.
		storeReleaseNotes(rel)
		patchStateMu.Lock()
		patchState.AutoUpdateCheckedAt = time.Now().UTC().Format(time.RFC3339)
		patchStateMu.Unlock()

		if !releaseIsNewerThanInstalled(rel) {
			clearAvailableUpdate()
			_ = savePatchState()
			logPatch("auto-update: %s is current", versionString(nil))
			return
		}
		markUpdateAvailable(rel)
		logPatch("auto-update: %s is available; waiting for the user to confirm", rel.TagName)
		if !pending {
			announceAvailableUpdate()
		}
		patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	}()
}

// StageJukaHubUpdate downloads and extracts a release for this device into the
// Patch staging area. It records the staged release in patch state so the
// Patch scene can offer "Apply staged update". It does not replace anything.
func StageJukaHubUpdate(ctx context.Context, config *Config, rel *githubRelease) error {
	if rel == nil {
		return errors.New("missing release metadata")
	}
	// Persist the notes before the swap: after the restart they are the only
	// copy on the device, and they power the "What's new" screen.
	storeReleaseNotes(rel)
	target, err := jukaHubUpdateTargetFor(DetectDevice(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	client := newPatchClient()

	assetURL := ""
	var assetSize int64
	expectSHA := ""
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, target.Asset) {
			assetURL = a.BrowserDownloadURL
			assetSize = a.Size
			expectSHA = releaseAssetDigest(a.Digest)
			break
		}
	}
	if assetURL == "" {
		// The release workflow always publishes these two archive names; fall
		// back to the stable "latest" URL when the API omits the asset list.
		assetURL = jukaHubReleaseDownloadBase + target.Asset
	}
	if expectSHA == "" {
		// Older releases and the URL fallback carry no digest. Say so in the log
		// instead of pretending the archive was verified.
		logPatch("update: no sha256 digest published for %s; archive is not checksum-verified", target.Asset)
	}
	// The API digest proves the download was not corrupted; the signature proves
	// who published it. Both are checked when the release carries a signed
	// manifest, and a manifest that does not verify stops the install instead of
	// quietly falling back to the digest.
	signedSHA, signedSize, signErr := signedReleaseTarget(ctx, client, rel, target.Asset)
	switch {
	case signErr == nil:
		if expectSHA != "" && !strings.EqualFold(expectSHA, signedSHA) {
			return fmt.Errorf("release digest for %s disagrees with its signed manifest", target.Asset)
		}
		expectSHA = signedSHA
		if signedSize > 0 {
			assetSize = signedSize
		}
		logPatch("update: %s matches the signed release manifest", target.Asset)
	case errors.Is(signErr, errNoReleaseManifest):
		logPatch("update: release has no signed manifest; relying on the GitHub digest for %s", target.Asset)
	default:
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("release verification failed: %w", signErr)
	}
	if assetSize > 0 && assetSize > patchMaxDownload {
		return fmt.Errorf("release asset %s is larger than the %d byte limit", target.Asset, patchMaxDownload)
	}
	if free, err := freeBytes(dir); err == nil && free < 96<<20 {
		return fmt.Errorf("insufficient free space for update staging (%d bytes free)", free)
	}

	patchRowStatus(ComponentApp, StatusDownloading)
	// The install bar is only shown when a caller opened it (the header chip
	// and the prompt do); these calls are no-ops elsewhere.
	installBarPhase(installPhaseDownload)
	archivePath := filepath.Join(dir, "downloads", sanitizeFileName(target.Asset))
	// A truncated or swapped archive is caught here, before it is unpacked: the
	// handheld's Wi-Fi drops mid-download often enough that a corrupt file used
	// to surface as "archive contains no JukaHub executable".
	if err := downloadToFile(ctx, client, assetURL, archivePath, expectSHA, patchMaxDownload); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("download failed: %w", err)
	}
	patchRowStatus(ComponentApp, StatusVerified)

	staging := filepath.Join(dir, "staging", "app-"+sanitizeFileName(rel.TagName))
	if err := stageDownloadedRelease(ctx, rel, archivePath, staging); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	stamp := releaseInfo{Tag: rel.TagName, Date: releaseStampISO(rel), Version: versionString(nil)}

	patchStateMu.Lock()
	patchState.LastUpdateVer = rel.TagName
	patchState.LastUpdateAt = time.Now().UTC().Format(time.RFC3339)
	patchState.LastUpdatePublishedAt = stamp.Date
	patchState.LastUpdateAsset = target.Asset
	patchStateMu.Unlock()
	_ = savePatchState()

	// Refresh the staged marker on disk so a cancelled staging is not mistaken
	// for a complete one: release-info.json is the "ready" marker.
	patchRowStatus(ComponentApp, StatusStaged)
	return nil
}

// stageDownloadedRelease unpacks a downloaded release archive into the staging
// folder and marks it ready. It is separate from the download so the
// unzip-verify-stamp half can be tested against a local archive, exactly as
// StageJukaHubUpdate uses it.
func stageDownloadedRelease(ctx context.Context, rel *githubRelease, archivePath, staging string) error {
	if rel == nil {
		return errors.New("missing release metadata")
	}
	// Reuse a complete staging tree: startup checks must not redownload the
	// same release after a failed or postponed apply.
	reused := false
	if bin := findStagedBinary(staging); bin != "" {
		if info, err := os.Stat(releaseInfoPath(filepath.Dir(bin))); err == nil && info.Mode().IsRegular() {
			reused = true
		}
	}
	if !reused {
		_ = os.RemoveAll(staging)
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return err
		}
		installBarPhase(installPhaseExtract)
		if err := extractSafe(ctx, archivePath, staging, patchMaxEntries, patchMaxExpandBytes); err != nil {
			return fmt.Errorf("extracting update: %w", err)
		}
	} else {
		logPatch("reusing staged release %s", rel.TagName)
	}
	newBin := findStagedBinary(staging)
	if newBin == "" {
		return errors.New("archive contains no JukaHub executable")
	}
	info, err := os.Stat(newBin)
	if err != nil || info.IsDir() {
		return errors.New("staged executable is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		if err := os.Chmod(newBin, 0o755); err != nil {
			return fmt.Errorf("staged executable is not executable: %w", err)
		}
	}

	// Stamp the staged tree so the applied installation can report its release
	// date without another network call.
	stamp := releaseInfo{Tag: rel.TagName, Date: releaseStampISO(rel), Version: versionString(nil)}
	if err := writeReleaseInfo(filepath.Dir(newBin), stamp); err != nil {
		logPatch("staged release stamp: %v", err)
	}
	// AtomicWrite keeps a .bak of the previous stamp; never ship that copy.
	_ = os.Remove(releaseInfoPath(filepath.Dir(newBin)) + ".bak")
	return nil
}

// stagedInstallFile is one file copied by the swap helper.
type stagedInstallFile struct {
	Src  string // absolute path inside the staging tree
	Name string // destination path relative to the executable directory
	Dest string // absolute destination path
}

// preservedInstallDir is the helper-tool folder next to the executable. A
// release archive may ship a placeholder (or no tools at all), but the tools on
// the device were either downloaded by the user or installed with the package
// and are expensive to re-fetch on handheld Wi-Fi. An app update must leave
// them exactly as they are.
const preservedInstallDir = "required"

// isPreservedInstallPath reports whether a staged relative path belongs to a
// folder the updater keeps untouched.
func isPreservedInstallPath(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean == "" {
		return false
	}
	first, _, _ := strings.Cut(clean, "/")
	return strings.EqualFold(first, preservedInstallDir)
}

// stagedInstallFiles lists the regular files of a staged release, in copy
// order (release-info.json last, so it only marks a completed replacement).
// Files under required/ are skipped: installed helper tools survive an update.
func stagedInstallFiles(installRoot, exeDir string) ([]stagedInstallFile, error) {
	var files []stagedInstallFile
	keptTools := 0
	err := filepath.WalkDir(installRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), "apply-update") {
			// Generated swap helper, never part of the installation.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(installRoot, p)
		if err != nil {
			return err
		}
		if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("unexpected staged path %q", p)
		}
		if isPreservedInstallPath(rel) {
			keptTools++
			return nil
		}
		files = append(files, stagedInstallFile{
			Src:  p,
			Name: rel,
			Dest: filepath.Join(exeDir, rel),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if keptTools > 0 {
		logPatch("update: preserving %d installed tool file(s) in %s/", keptTools, preservedInstallDir)
	}
	if len(files) == 0 {
		return nil, errors.New("staged release is empty")
	}
	if len(files) > autoUpdateMaxFiles {
		return nil, fmt.Errorf("staged release has %d files (limit %d)", len(files), autoUpdateMaxFiles)
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Name != releaseInfoFileName && files[j].Name == releaseInfoFileName
	})
	return files, nil
}

// quoteWindowsArg quotes a path for use inside a generated .bat file.
func quoteWindowsArg(p string) string {
	return `"` + strings.ReplaceAll(p, `"`, ``) + `"`
}

// quotePOSIXArg single-quotes a path for use inside a generated shell script.
func quotePOSIXArg(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// writeSwapScript writes the platform-specific detached helper that replaces
// the staged files, commits (deletes) the journal, and relaunches JukaHub.
// The helper waits for this process to exit and retries while the old
// executable is still locked.
func writeSwapScript(scriptPath, exePath string, files []stagedInstallFile, journalPath string) error {
	var b strings.Builder
	if runtime.GOOS == "windows" {
		b.WriteString("@echo off\r\n")
		b.WriteString("rem JukaHub apply-update helper (generated by Patch; do not edit)\r\n")
		b.WriteString("setlocal\r\n")
		b.WriteString("set \"EXE=" + strings.ReplaceAll(exePath, `"`, ``) + "\"\r\n")
		b.WriteString("set \"JOURNAL=" + strings.ReplaceAll(journalPath, `"`, ``) + "\"\r\n")
		b.WriteString("timeout /t 2 /nobreak >nul\r\n")
		for _, f := range files {
			b.WriteString("call :copyloop " + quoteWindowsArg(f.Src) + " " + quoteWindowsArg(f.Dest) + " || goto fail\r\n")
		}
		b.WriteString("del /f /q \"%JOURNAL%\" >nul 2>nul\r\n")
		b.WriteString("start \"\" \"%EXE%\"\r\n")
		b.WriteString("exit /b 0\r\n")
		b.WriteString(":copyloop\r\n")
		b.WriteString("set /a tries=0\r\n")
		b.WriteString(":retry\r\n")
		b.WriteString("copy /y %~1 %~2 >nul 2>nul && exit /b 0\r\n")
		b.WriteString("set /a tries+=1\r\n")
		b.WriteString("if %tries% geq 30 exit /b 9\r\n")
		b.WriteString("timeout /t 1 /nobreak >nul\r\n")
		b.WriteString("goto retry\r\n")
		b.WriteString(":fail\r\n")
		b.WriteString("exit /b 9\r\n")
	} else {
		b.WriteString("#!/bin/sh\n")
		b.WriteString("# JukaHub apply-update helper (generated by Patch; do not edit)\n")
		launcher := filepath.Join(filepath.Dir(exePath), "launch.sh")
		b.WriteString("EXE=" + quotePOSIXArg(exePath) + "\n")
		b.WriteString("LAUNCHER=" + quotePOSIXArg(launcher) + "\n")
		b.WriteString("JOURNAL=" + quotePOSIXArg(journalPath) + "\n")
		b.WriteString("sleep 2\n")
		for _, f := range files {
			// Write next to the target and rename: works even if the old binary
			// is still mapped by the exiting process.
			b.WriteString("cp -f " + quotePOSIXArg(f.Src) + " " + quotePOSIXArg(f.Dest+".new") + " || exit 5\n")
			b.WriteString("mv -f " + quotePOSIXArg(f.Dest+".new") + " " + quotePOSIXArg(f.Dest) + " || exit 6\n")
		}
		// FAT-formatted SD cards may not support chmod; the extracted mode is
		// already executable there, so never fail the swap on it.
		b.WriteString("chmod 755 \"$EXE\" 2>/dev/null || true\n")
		b.WriteString("rm -f \"$JOURNAL\" || true\n")
		// The device launcher exports LD_LIBRARY_PATH before starting JukaHub;
		// restart through it when present.
		b.WriteString("if [ -f \"$LAUNCHER\" ]; then nohup sh \"$LAUNCHER\" >/dev/null 2>&1 &\n")
		b.WriteString("else nohup \"$EXE\" >/dev/null 2>&1 & fi\n")
		b.WriteString("exit 0\n")
	}
	return os.WriteFile(scriptPath, []byte(b.String()), 0o700)
}

// releaseInfoFromState rebuilds a releaseInfo from persisted patch state, used
// when the staged tree has gone missing.
func releaseInfoFromState() releaseInfo {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return releaseInfo{Tag: patchState.LastUpdateVer, Date: patchState.LastUpdatePublishedAt}
}

// confirmAppliedUpdate clears a staged marker once the running installation is
// the staged release (the swap helper copied release-info.json last). It runs
// at startup, after interrupted journals have been rolled back.
func confirmAppliedUpdate() {
	patchStateMu.Lock()
	tag := patchState.LastUpdateVer
	stagedAt := patchState.LastUpdatePublishedAt
	patchStateMu.Unlock()
	if tag == "" {
		return
	}
	info := readInstalledReleaseInfo()
	if info.Tag != tag {
		return
	}
	patchStateMu.Lock()
	patchState.InstalledReleaseTag = tag
	patchState.InstalledReleaseAt = info.Date
	if patchState.InstalledReleaseAt == "" {
		patchState.InstalledReleaseAt = stagedAt
	}
	patchState.LastUpdateVer = ""
	patchState.AvailableUpdateTag = ""
	patchState.AvailableUpdateAt = ""
	patchState.AvailableUpdateAsset = ""
	patchStateMu.Unlock()
	_ = savePatchState()
	if dir, err := PatchStateDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(dir, "staging", "app-"+sanitizeFileName(tag)))
	}
	logPatch("JukaHub updated to release %s", tag)
}

// installedBuildTimeLabel renders the compile stamp for About: full UTC time
// when the build carried one, otherwise the release/build date, or "" when
// unknown.
func installedBuildTimeLabel() string {
	if t, ok := parseReleaseTimestamp(buildTime); ok {
		return t.Format("2006-01-02 15:04 UTC")
	}
	return installedBuildDate()
}

// formatBuildLabel renders the version display suffix ("v0.4.0 · 2026-10-17").
// It is split out so the UI call sites stay one-liners.
func formatBuildLabel(version, date string) string {
	if date == "" {
		return version
	}
	return version + " · " + date
}
