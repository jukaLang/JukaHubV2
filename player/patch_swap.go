package main

// patch_swap.go implements the JukaHub application self-update. The running
// process cannot replace its own binary, so the swap is delegated to a tiny
// external helper script (bash on the device, batch on Windows) that:
//
//   1. moves the current binary to a backup name,
//   2. renames the staged new binary into place (same filesystem),
//   3. marks the journal committed, then relaunches JukaHub.
//
// The journal is written BEFORE the destructive step, so an interruption at
// any point is detected at next startup (checkInterruptedJournal) and rolled
// back automatically. A failing swap is never left half-applied.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	patchMaxEntries     = 50000
	patchMaxExpandBytes = 2 << 30 // 2 GiB staged expansion cap
)

// jukaHubAssetNames are the device/OS-specific archive names JukaHub publishes.
func jukaHubAssetName(device, goos, arch string) string {
	return fmt.Sprintf("JukaHub-%s-%s-%s.tar.gz", device, goos, arch)
}

// OpUpdateJukaHub performs the full JukaHub application update flow:
// fetch release metadata -> resolve manifest -> download -> verify -> stage
// -> backup -> swap script. The user confirms the swap through the modal;
// this operation stops at staging.
func OpUpdateJukaHub(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	client := newPatchClient()
	rel, err := FetchLatestJukaHubRelease(ctx)
	if err != nil {
		return err
	}
	installed := installedPatchVersions(config)[ComponentApp]
	available := strings.TrimPrefix(rel.TagName, "v")
	if !NewerVersion(installed, available) {
		return fmt.Errorf("installed %s is already current", installed)
	}
	patchRowStatus(ComponentApp, StatusDownloading)

	device := "dev-build"
	if IsTSP() {
		device = "trimui-smart-pro"
	}
	assetName := jukaHubAssetName(device, runtime.GOOS, runtime.GOARCH)

	// Prefer the machine-readable manifest when the release ships one; it
	// carries the authoritative sha256. Without a manifest we refuse to label
	// the download "verified" and stop before the swap.
	var expectSHA string
	if mAsset := rel.FindManifestAsset(); mAsset != nil {
		mData, err := fetchBounded(ctx, client, mAsset.BrowserDownloadURL, patchMaxMetadata)
		if err != nil {
			return fmt.Errorf("fetching manifest: %w", err)
		}
		manifest, err := ParsePatchManifest(mData)
		if err != nil {
			return fmt.Errorf("manifest rejected: %w", err)
		}
		target := manifest.ManifestTargetFor(device, runtime.GOOS, runtime.GOARCH)
		if target == nil {
			return fmt.Errorf("release manifest has no target for %s/%s/%s", device, runtime.GOOS, runtime.GOARCH)
		}
		assetName = target.Asset
		expectSHA = target.SHA256
	} else {
		return errors.New("release does not publish manifest.json yet — update disabled until a signed manifest is available")
	}

	// Locate the asset on the release.
	var assetURL string
	var assetSize int64
	found := false
	for _, a := range rel.Assets {
		if a.Name == assetName {
			assetURL = a.BrowserDownloadURL
			assetSize = a.Size
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("release %q does not contain asset %q", rel.TagName, assetName)
	}
	_ = assetSize

	// Free-space sanity check before downloading.
	if free, err := freeBytes(dir); err == nil && free < 64<<20 {
		return fmt.Errorf("insufficient free space for update staging (%d bytes free)", free)
	}

	archivePath := filepath.Join(dir, "downloads", sanitizeFileName(assetName))
	if err := downloadToFile(ctx, client, assetURL, archivePath, expectSHA, patchMaxDownload); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("download/verify failed: %w", err)
	}
	patchRowStatus(ComponentApp, StatusVerified)

	// Stage: extract into a fresh staging dir.
	staging := filepath.Join(dir, "staging", "app-"+rel.TagName)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	if err := extractSafe(ctx, archivePath, staging, patchMaxEntries, patchMaxExpandBytes); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return fmt.Errorf("staging failed: %w", err)
	}

	// Locate the new binary inside the staged tree.
	newBin := findStagedBinary(staging)
	if newBin == "" {
		patchRowStatus(ComponentApp, StatusFailed)
		return errors.New("staged package contains no JukaHub executable")
	}
	// Validate: must be a regular executable file.
	info, err := os.Stat(newBin)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		patchRowStatus(ComponentApp, StatusFailed)
		return errors.New("staged binary is not an executable regular file")
	}

	// Persist the staged-update marker for the confirm modal.
	patchStateMu.Lock()
	patchState.LastUpdateVer = rel.TagName
	patchState.LastUpdateAt = rel.TagName
	patchStateMu.Unlock()
	savePatchState()

	patchRowStatus(ComponentApp, StatusStaged)
	patchEventSafe(PatchEvent{Kind: PatchEventModal, Modal: &PatchModalRequest{
		ID:       "confirm-swap",
		Title:    "Update JukaHub to " + rel.TagName + "?",
		Body:     "The new binary is staged and verified. Applying replaces the running executable and restarts JukaHub.\n\nBackup and rollback are automatic.",
		Confirm:  "Apply & Restart",
		HighRisk: true,
	}})
	return nil
}

// findStagedBinary locates the JukaHub executable inside a staged tree.
func findStagedBinary(staging string) string {
	var found string
	_ = filepath.WalkDir(staging, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if runtime.GOOS == "windows" {
			if name == "jukahub.exe" {
				found = p
			}
			return nil
		}
		if name == "jukahub" {
			found = p
			return nil
		}
		return nil
	})
	return found
}

// swapScriptPath returns where the swap helper should be written.
func swapScriptPath(staging, tag string) string {
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".bat"
	}
	return filepath.Join(staging, "apply-update"+ext)
}

// writeSwapScript writes the platform-specific swap helper. It returns the
// script path. The script performs the swap, commits the journal, and
// relaunches JukaHub. It never uses shell interpolation for paths (argument
// arrays / quoted env vars only).
func writeSwapScript(staging, tag, newBin, journalPath string) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	backupPath := exePath + ".bak-v" + sanitizeFileName(tag)
	script := swapScriptPath(staging, tag)
	quoted := func(p string) string { return "'" + strings.ReplaceAll(p, "'", "'\\''") + "'" }

	var content string
	if runtime.GOOS == "windows" {
		content = `@echo off
rem JukaHub apply-update helper (generated by Patch; do not edit)
setlocal
set "OLD=` + quoted(exePath) + `"
set "NEW=` + quoted(newBin) + `"
set "BAK=` + quoted(backupPath) + `"
set "JOURNAL=` + quoted(journalPath) + `"
if not exist "%NEW%" exit /b 2
if exist "%BAK%" del /f /q "%BAK%"
move /y "%OLD%" "%BAK%" >nul || exit /b 3
copy /y "%NEW%" "%OLD%" >nul || exit /b 4
del /f /q "%JOURNAL%" >nul 2>nul
start "" "%OLD%"
`
	} else {
		content = `#!/bin/sh
# JukaHub apply-update helper (generated by Patch; do not edit)
OLD=` + quoted(exePath) + `
NEW=` + quoted(newBin) + `
BAK=` + quoted(backupPath) + `
JOURNAL=` + quoted(journalPath) + `
[ -f "$NEW" ] || exit 2
[ -f "$OLD" ] || exit 3
if [ -f "$BAK" ]; then rm -f "$BAK" || exit 4; fi
mv -f "$OLD" "$BAK" || exit 5
cp -f "$NEW" "$OLD" || exit 6
chmod 755 "$OLD" || exit 7
rm -f "$JOURNAL" || true
nohup "$OLD" >/dev/null 2>&1 &
exit 0
`
	}
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		return "", err
	}
	return script, nil
}

// OpApplyStagedSwap moves the staged new binary into place using the swap
// helper. The journal guarantees automatic rollback on interruption.
func OpApplyStagedSwap(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	patchStateMu.Lock()
	tag := patchState.LastUpdateVer
	patchStateMu.Unlock()
	if tag == "" {
		return errors.New("no staged update")
	}
	staging := filepath.Join(dir, "staging", "app-"+tag)
	newBin := findStagedBinary(staging)
	if newBin == "" {
		return errors.New("staged binary missing")
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	patchRowStatus(ComponentApp, StatusBackingUp)
	journal := newJournal("juka-update", tag)
	// Record the replace so rollback restores the original binary.
	if err := journal.recordReplace(exePath); err != nil {
		return err
	}
	// Also back up the user config separately (never deleted).
	if _, err := os.Stat(P().UserConfigPath()); err == nil {
		if _, _, err := CreateBackupSet("pre-juka-update", []string{exePath, P().ConfigPath()}); err != nil {
			logPatch("backup warning: %v", err)
		}
	}

	patchRowStatus(ComponentApp, StatusApplying)
	script, err := writeSwapScript(staging, tag, newBin, journal.path)
	if err != nil {
		_ = journal.rollback()
		return err
	}
	// Run the helper detached: the running process exits as part of the swap.
	cmd := P().CommandContext(ctx, script)
	if err := cmd.Start(); err != nil {
		_ = journal.rollback()
		return fmt.Errorf("starting swap helper: %w", err)
	}
	patchRowStatus(ComponentApp, StatusRestartReq)
	return nil
}

// OpCancelStagedSwap removes the staged update and its marker.
func OpCancelStagedSwap(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	patchStateMu.Lock()
	tag := patchState.LastUpdateVer
	patchState.LastUpdateVer = ""
	patchStateMu.Unlock()
	savePatchState()
	if tag != "" {
		_ = os.RemoveAll(filepath.Join(dir, "staging", "app-"+tag))
	}
	patchRowStatus(ComponentApp, StatusCurrent)
	return nil
}
