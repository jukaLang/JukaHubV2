package main

// autoupdate.go implements the automatic JukaHub updater and the release build
// stamp shown in the UI.
//
// On startup (and from MISC -> Patch) the app asks GitHub for the latest
// JukaHub release, compares its publication time with the build stamp of the
// running installation, downloads the platform archive
//
//	JukaHub-Windows.zip   (Windows x64)
//	JukaHub-Trimui.zip    (TrimUI Linux arm64)
//
// extracts it into the Patch staging area, and hands the replacement of the
// files (binary, SDL DLLs, config, fonts, backgrounds) to a detached helper
// script that restarts JukaHub. A journal is written before any destructive
// step, so an interrupted update is rolled back at next startup.
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
	"time"
)

// buildDate is stamped by the release build:
//
//	go build -ldflags "-X main.buildDate=2026-10-17"
//
// It is optional: release-info.json (bundled in the archive and written on
// every update) is the primary source.
var buildDate string

const (
	jukaHubReleaseDownloadBase = "https://github.com/jukaLang/JukaHubV2/releases/latest/download/"
	releaseInfoFileName        = "release-info.json"
	autoUpdateCheckInterval    = 24 * time.Hour
	autoUpdateMaxFiles         = 128
	autoUpdateStartupDelay     = 4 * time.Second
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

// readInstalledReleaseInfo reads the stamp file next to the executable.
// A missing or malformed file is simply an unknown stamp.
func readInstalledReleaseInfo() releaseInfo {
	dir, err := P().ExecutableDir()
	if err != nil || dir == "" {
		return releaseInfo{}
	}
	data, err := os.ReadFile(releaseInfoPath(dir))
	if err != nil {
		return releaseInfo{}
	}
	var info releaseInfo
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

// installedReleaseTime returns the stamped release of the running install.
// The second result is false when no stamp is known (a fresh source build or a
// pre-stamp installation).
func installedReleaseTime() (time.Time, bool) {
	if info := readInstalledReleaseInfo(); info.Date != "" {
		if t, ok := parseReleaseTimestamp(info.Date); ok {
			return t, true
		}
	}
	patchStateMu.Lock()
	stamp := patchState.InstalledReleaseAt
	patchStateMu.Unlock()
	if t, ok := parseReleaseTimestamp(stamp); ok {
		return t, true
	}
	if t, ok := parseReleaseTimestamp(buildDate); ok {
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

// autoUpdateCanApply reports whether this installation may replace itself
// automatically. Only stamped installations (a shipped release, or a build
// where an update already ran) auto-apply; source/dev builds stage the update
// and let the user apply it from MISC -> Patch.
func autoUpdateCanApply() bool {
	if strings.TrimSpace(os.Getenv("JUKAHUB_NO_AUTOUPDATE")) != "" {
		return false
	}
	info := readInstalledReleaseInfo()
	if info.Tag != "" {
		return true
	}
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return patchState.InstalledReleaseAt != ""
}

// startAutoUpdateCheck runs the startup update check in the background. It is
// best-effort: offline launches and API failures are logged, never surfaced as
// startup errors.
func startAutoUpdateCheck(config *Config) {
	go func() {
		time.Sleep(autoUpdateStartupDelay)
		if strings.TrimSpace(os.Getenv("JUKAHUB_NO_AUTOUPDATE")) != "" {
			return
		}
		patchStateMu.Lock()
		last := patchState.AutoUpdateCheckedAt
		patchStateMu.Unlock()
		if t, err := time.Parse(time.RFC3339, last); err == nil && time.Since(t) < autoUpdateCheckInterval {
			return
		}
		if !tryBeginPatchOp() {
			return
		}
		defer endPatchOp()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		rel, err := FetchLatestJukaHubRelease(ctx)
		if err != nil {
			logPatch("auto-update check failed: %v", err)
			return
		}
		patchStateMu.Lock()
		patchState.AutoUpdateCheckedAt = time.Now().UTC().Format(time.RFC3339)
		patchStateMu.Unlock()
		_ = savePatchState()

		if !releaseIsNewerThanInstalled(rel) {
			logPatch("auto-update: %s is current", versionString(nil))
			return
		}
		label := releaseDateLabel(rel)
		if label == "" {
			label = rel.TagName
		}
		setPatchBusy("Updating JukaHub to " + label + "…")
		defer clearPatchBusy()
		if err := StageJukaHubUpdate(ctx, config, rel); err != nil {
			logPatch("auto-update staging failed: %v", err)
			patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "JukaHub update failed: " + err.Error()})
			return
		}
		if !autoUpdateCanApply() {
			patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "JukaHub " + label + " is ready — apply it from Patch"})
			return
		}
		patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "Installing JukaHub " + label + " — restarting…"})
		// Give the toast a moment to render before the swap helper stops us.
		time.Sleep(1500 * time.Millisecond)
		if err := OpApplyStagedSwap(ctx, config); err != nil {
			logPatch("auto-update apply failed: %v", err)
		}
	}()
}

// StageJukaHubUpdate downloads and extracts a release for this device into the
// Patch staging area. It records the staged release in patch state so the
// Patch scene can offer "Apply staged update". It does not replace anything.
func StageJukaHubUpdate(ctx context.Context, config *Config, rel *githubRelease) error {
	if rel == nil {
		return errors.New("missing release metadata")
	}
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
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, target.Asset) {
			assetURL = a.BrowserDownloadURL
			assetSize = a.Size
			break
		}
	}
	if assetURL == "" {
		// The release workflow always publishes these two archive names; fall
		// back to the stable "latest" URL when the API omits the asset list.
		assetURL = jukaHubReleaseDownloadBase + target.Asset
	}
	if assetSize > 0 && assetSize > patchMaxDownload {
		return fmt.Errorf("release asset %s is larger than the %d byte limit", target.Asset, patchMaxDownload)
	}
	if free, err := freeBytes(dir); err == nil && free < 96<<20 {
		return fmt.Errorf("insufficient free space for update staging (%d bytes free)", free)
	}

	patchRowStatus(ComponentApp, StatusDownloading)
	archivePath := filepath.Join(dir, "downloads", sanitizeFileName(target.Asset))
	if err := downloadToFile(ctx, client, assetURL, archivePath, "", patchMaxDownload); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("download failed: %w", err)
	}
	patchRowStatus(ComponentApp, StatusVerified)

	staging := filepath.Join(dir, "staging", "app-"+sanitizeFileName(rel.TagName))
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	if err := extractSafe(ctx, archivePath, staging, patchMaxEntries, patchMaxExpandBytes); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("extracting update: %w", err)
	}
	newBin := findStagedBinary(staging)
	if newBin == "" {
		patchRowStatus(ComponentApp, StatusFailed)
		return errors.New("archive contains no JukaHub executable")
	}
	info, err := os.Stat(newBin)
	if err != nil || info.IsDir() {
		patchRowStatus(ComponentApp, StatusFailed)
		return errors.New("staged executable is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		if err := os.Chmod(newBin, 0o755); err != nil {
			patchRowStatus(ComponentApp, StatusFailed)
			return fmt.Errorf("staged executable is not executable: %w", err)
		}
	}

	// Stamp the staged tree so the applied installation can report its release
	// date without another network call.
	stamp := releaseInfo{Tag: rel.TagName, Date: releaseStampISO(rel), Version: versionString(nil)}
	if err := writeReleaseInfo(filepath.Dir(newBin), stamp); err != nil {
		logPatch("staged release stamp: %v", err)
	}

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

// stagedInstallFile is one file copied by the swap helper.
type stagedInstallFile struct {
	Src  string // absolute path inside the staging tree
	Name string // destination path relative to the executable directory
	Dest string // absolute destination path
}

// stagedInstallFiles lists the regular files of a staged release, in copy
// order (release-info.json last, so it only marks a completed replacement).
func stagedInstallFiles(installRoot, exeDir string) ([]stagedInstallFile, error) {
	var files []stagedInstallFile
	err := filepath.WalkDir(installRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
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
			b.WriteString("call :copyloop " + quoteWindowsArg(f.Src) + " " + quoteWindowsArg(f.Dest) + "\r\n")
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
	} else {
		b.WriteString("#!/bin/sh\n")
		b.WriteString("# JukaHub apply-update helper (generated by Patch; do not edit)\n")
		b.WriteString("EXE=" + quotePOSIXArg(exePath) + "\n")
		b.WriteString("JOURNAL=" + quotePOSIXArg(journalPath) + "\n")
		b.WriteString("sleep 2\n")
		for _, f := range files {
			b.WriteString("cp -f " + quotePOSIXArg(f.Src) + " " + quotePOSIXArg(f.Dest) + " || exit 5\n")
		}
		b.WriteString("chmod 755 \"$EXE\" || exit 7\n")
		b.WriteString("rm -f \"$JOURNAL\" || true\n")
		b.WriteString("nohup \"$EXE\" >/dev/null 2>&1 &\n")
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
	patchStateMu.Unlock()
	_ = savePatchState()
	if dir, err := PatchStateDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(dir, "staging", "app-"+sanitizeFileName(tag)))
	}
	logPatch("JukaHub updated to release %s", tag)
}

// formatBuildLabel renders the version display suffix ("v0.4.0 · 2026-10-17").
// It is split out so the UI call sites stay one-liners.
func formatBuildLabel(version, date string) string {
	if date == "" {
		return version
	}
	return version + " · " + date
}
