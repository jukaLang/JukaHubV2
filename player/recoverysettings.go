package main

// recoverysettings.go — the settings half of recovery: finding the last good
// settings, putting them back, resetting to the shipped settings, and starting
// JukaHub again afterwards.
//
// Everything that writes runs off the render thread and starts with a safety
// backup of the current settings (the same JukaHub-safety-*.jkbackup the
// Backup & restore screen writes before a restore, listed there afterwards);
// nothing is changed when that backup cannot be written. The settings are then
// installed by bkupCommit on the main thread — the path a restore from Backup &
// restore takes — so every store that keeps a copy in memory reads the new
// files and no pending save lands on top of them.
//
// "Settings" is jukauser.json (minus the library sections it also holds:
// favourites, chat, play history and playtime stay) and the design file
// jukaconfig.json. Games, saves, ROM folders, themes, plugins and input
// profiles are never touched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Where the last good settings can come from.
const (
	recoverySourceLastGood = "lastgood" // what the last healthy start read at boot (bootguard.go)
	recoverySourceBak      = "bak"      // the .bak AtomicWrite keeps of each settings file
	recoverySourceWeekly   = "weekly"   // the newest weekly backup (Tools → Your setup)
)

// recoverySettingsSource is one place the last good settings can come from.
type recoverySettingsSource struct {
	Kind string
	Path string    // the weekly backup's archive; for .bak the app folder, for the last good start its folder
	When time.Time // when it was written, by the device's clock
}

// recoveryBakFiles are the settings files whose .bak a restore reads.
var recoveryBakFiles = [...]string{"jukauser.json", "jukaconfig.json"}

// recoveryNewestOf is the newest time of the non-empty regular files dir/name+suffix.
func recoveryNewestOf(dir, suffix string) time.Time {
	var at time.Time
	for _, name := range recoveryBakFiles {
		if info, err := os.Stat(filepath.Join(dir, name+suffix)); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			if info.ModTime().After(at) {
				at = info.ModTime()
			}
		}
	}
	return at
}

// recoveryFindSettingsSources lists the settings the last healthy start read
// (lastGoodDir, bootguard.go) first — they are the ones known to work — then
// the .bak pair in appDir and the newest weekly backup in exportDir, newest
// first. Any of them may be missing.
func recoveryFindSettingsSources(appDir, exportDir, lastGoodDir string) []recoverySettingsSource {
	var out []recoverySettingsSource
	if lastGoodDir != "" {
		if at := recoveryNewestOf(lastGoodDir, ""); !at.IsZero() {
			out = append(out, recoverySettingsSource{Kind: recoverySourceLastGood, Path: lastGoodDir, When: at})
		}
	}
	if bakAt := recoveryNewestOf(appDir, ".bak"); !bakAt.IsZero() {
		out = append(out, recoverySettingsSource{Kind: recoverySourceBak, Path: appDir, When: bakAt})
	}
	if entries, err := os.ReadDir(exportDir); err == nil {
		var weekly recoverySettingsSource
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "JukaHub-backup-") || !strings.HasSuffix(name, "-auto"+bkupExt) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() == 0 {
				continue
			}
			if weekly.Path == "" || info.ModTime().After(weekly.When) {
				weekly = recoverySettingsSource{Kind: recoverySourceWeekly, Path: filepath.Join(exportDir, name), When: info.ModTime()}
			}
		}
		if weekly.Path != "" {
			out = append(out, weekly)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		// The last good start's settings lead whatever their age: the others
		// were written by saves, and a save may be what broke the start.
		if a, b := out[i].Kind == recoverySourceLastGood, out[j].Kind == recoverySourceLastGood; a != b {
			return a
		}
		return out[i].When.After(out[j].When)
	})
	return out
}

// recoverySourceLabel is how the card names a source.
func recoverySourceLabel(s recoverySettingsSource) string {
	when := recoveryWhen(s.When)
	switch s.Kind {
	case recoverySourceWeekly:
		return "Weekly backup" + when
	case recoverySourceLastGood:
		return "Settings from the last good start" + when
	}
	return "Settings before the last change" + when
}

// recoverySourceDetail is the line under a source on the chooser.
func recoverySourceDetail(s recoverySettingsSource) string {
	switch s.Kind {
	case recoverySourceWeekly:
		return "Settings and layout from the weekly backup · " + filepath.Base(s.Path)
	case recoverySourceLastGood:
		return "What JukaHub read when it last started and kept running"
	}
	return "The settings as they were before they were last saved"
}

// recoveryWhen is " · 9 Nov 14:03", or "" when the clock that wrote it was not
// set (a handheld without an RTC after a cold boot).
func recoveryWhen(t time.Time) string {
	if t.IsZero() || t.Before(minSaneClock) {
		return ""
	}
	return " · " + t.Format("2 Jan 15:04")
}

// recoverySettingsCats is the one backup category recovery reads and writes.
func recoverySettingsCats() map[string]bool {
	return map[string]bool{bkupCatSettings: true}
}

// recoverySafetyBackup writes the safety backup of the current settings.
func recoverySafetyBackup(ctx context.Context, appVersion string) (string, error) {
	path, _, err := bkupCreate(ctx, bkupSafetyDir(), bkupFileName("safety", time.Now()), recoverySettingsCats(),
		bkupOptions{}, "before recovery", appVersion, nil, nil)
	if err != nil {
		return "", fmt.Errorf("the safety backup could not be written, so nothing was changed: %w", err)
	}
	// The one just written always stays, whatever the clock said when it was
	// written (bkupPruneKeep).
	bkupPruneKeep(bkupSafetyDir(), "JukaHub-safety-", "", bkupSafetyKeep, path)
	return path, nil
}

// recoveryHereManifest describes this device the way a backup's manifest
// would, for a restore whose files never left it.
func recoveryHereManifest() bkupManifest {
	return bkupManifest{
		Format:     bkupFormat,
		Version:    bkupVersion,
		Platform:   P().Name(),
		Roots:      bkupHereRoots(),
		Categories: []string{bkupCatSettings},
	}
}

// recoveryInstallDesign checks data as a design config and, only when it loads
// and validates, writes it over the live jukaconfig.json and reads it back the
// way a boot does.
func recoveryInstallDesign(appDir string, data []byte) (*Config, error) {
	var probe Config
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("the saved layout is damaged: %w", err)
	}
	if err := NewConfigValidator().Validate(&probe); err != nil {
		return nil, fmt.Errorf("the saved layout is not usable: %w", err)
	}
	dest := filepath.Join(appDir, "jukaconfig.json")
	if err := AtomicWrite(dest, data, 0o644); err != nil {
		return nil, err
	}
	// A jukaconfig.xml export beside it wins when it is newer
	// (ResolveConfigPath) and would load instead of what was just put back, so
	// the restored file is dated just after it. Two seconds: a FAT time step.
	xmlPath := XMLSidecarPath(dest)
	if xmlInfo, err := os.Stat(xmlPath); err == nil {
		if info, err := os.Stat(dest); err == nil && !info.ModTime().After(xmlInfo.ModTime()) {
			at := xmlInfo.ModTime().Add(2 * time.Second)
			if err := os.Chtimes(dest, at, at); err != nil {
				logWarn("recovery", "%s is newer and may load instead of the restored layout: %v", filepath.Base(xmlPath), err)
			}
		}
	}
	if globalConfigWatcher != nil {
		// The reload below installs it; the watcher must not do it twice. It
		// knows the file by the name main.go gave it, relative to the folder
		// JukaHub runs in (as extras.go saveConfig does), and records the
		// time it has now, so this comes after the write.
		globalConfigWatcher.IgnoreWrite("jukaconfig.json", dest)
	}
	next, _, err := LoadXMLConfigWithFallback(dest)
	if err != nil {
		return nil, err
	}
	if err := NewConfigValidator().Validate(next); err != nil {
		return nil, err
	}
	return next, nil
}

// recoveryRestoreSource is the background half of "Last good settings".
func recoveryRestoreSource(ctx context.Context, src recoverySettingsSource, appDir, appVersion string) bkupRestoreResult {
	cats := recoverySettingsCats()
	if src.Kind == recoverySourceWeekly {
		m, err := bkupVerify(src.Path)
		if err != nil {
			return bkupRestoreResult{Cats: cats, Err: fmt.Errorf("the weekly backup %s", err.Error())}
		}
		// Verifies again, writes the safety backup, writes the design file and
		// hands the user settings back for the main thread to merge.
		return bkupRestoreFiles(ctx, bkupPlanRestore(src.Path, m, cats), cats, appVersion, nil)
	}
	res := bkupRestoreResult{Cats: cats, Manifest: recoveryHereManifest()}
	// The .bak pair beside the live files, or the copies the last healthy
	// start kept (same names, in their own folder).
	userPath := filepath.Join(appDir, "jukauser.json.bak")
	designPath := filepath.Join(appDir, "jukaconfig.json.bak")
	if src.Kind == recoverySourceLastGood {
		userPath, designPath = filepath.Join(src.Path, "jukauser.json"), filepath.Join(src.Path, "jukaconfig.json")
	}
	userData, userErr := os.ReadFile(userPath)
	if userErr == nil && !json.Valid(userData) {
		userErr = errors.New("damaged")
	}
	designData, designErr := os.ReadFile(designPath)
	if userErr != nil && designErr != nil {
		res.Err = errors.New("no saved copy of the settings could be read")
		return res
	}
	path, err := recoverySafetyBackup(ctx, appVersion)
	if err != nil {
		res.Err = err
		return res
	}
	res.SafetyPath = path
	if userErr == nil {
		res.UserData, res.HasUser = userData, true
	} else if !os.IsNotExist(userErr) {
		res.Skipped = append(res.Skipped, filepath.Base(userPath)+": "+userErr.Error())
	}
	if designErr == nil {
		next, err := recoveryInstallDesign(appDir, designData)
		if err != nil {
			res.Skipped = append(res.Skipped, filepath.Base(designPath)+": "+err.Error())
		} else {
			res.Next = next
			res.Written++
		}
	}
	if !res.HasUser && res.Written == 0 {
		res.Err = errors.New("the saved copies of the settings are damaged; nothing was changed")
	}
	return res
}

// recoveryResetSettings is the background half of "Reset settings (keep games
// and saves)": the safety backup, then the shipped design file. The shipped
// user settings are built from it on the main thread (recoveryResetUser),
// because they carry over the levels and device facts the main thread owns.
func recoveryResetSettings(ctx context.Context, appDir, appVersion string) bkupRestoreResult {
	res := bkupRestoreResult{Cats: recoverySettingsCats(), Manifest: recoveryHereManifest()}
	source, shipped := cfgdocShippedBytes()
	if len(shipped) == 0 {
		res.Err = errors.New("this build carries no shipped settings")
		return res
	}
	path, err := recoverySafetyBackup(ctx, appVersion)
	if err != nil {
		res.Err = err
		return res
	}
	res.SafetyPath = path
	next, err := recoveryInstallDesign(appDir, shipped)
	if err != nil {
		res.Err = fmt.Errorf("the shipped layout (%s) could not be installed: %w", source, err)
		return res
	}
	res.Next = next
	res.Written++
	return res
}

// recoveryResetUser is the shipped user settings, built from the shipped
// design config, keeping what describes this device rather than a choice: the
// fullscreen switch and the device keys (button swap, screen size, model).
// The library sections and the volume and brightness levels are kept by the
// merge itself (bkupMergeUser). Main thread.
func recoveryResetUser(shipped *Config) ([]byte, error) {
	reset := userConfigFromVariables(shipped)
	if reset == nil {
		return nil, errors.New("no shipped settings")
	}
	reset.Variables.Custom = bkupCopyMap(reset.Variables.Custom)
	if current := loadUserConfig(); current != nil {
		reset.Variables.Fullscreen = current.Variables.Fullscreen
		for k, v := range current.Variables.Custom {
			if bkupDeviceKey(k) {
				reset.Variables.Custom[k] = v
			}
		}
	}
	return json.Marshal(reset)
}

// ── Restart ────────────────────────────────────────────────────────────────

// recoveryRestartToken is the line launch.sh carries when it starts JukaHub
// again after an exit that left recoveryRestartMarker behind.
const (
	recoveryRestartToken  = "jukahub-restart-now: 1"
	recoveryRestartMarker = ".restart-now"
)

// recoveryRestartKind says how this install can start JukaHub again: "respawn"
// (a desktop: a new process once this one has torn down), "launcher" (the
// handheld's launch.sh runs it again), or "" (quit; the user opens it again).
// The launcher on disk cannot change under a running session, so it is read
// once.
var recoveryRestartKind = sync.OnceValue(func() string {
	if IsWindows() || !DetectDevice().IsTrimui() {
		return "respawn"
	}
	dir, err := P().ExecutableDir()
	if err != nil || !launchedByDeviceLauncher() {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "launch.sh"))
	if err != nil || !strings.Contains(string(data), recoveryRestartToken) {
		return ""
	}
	return "launcher"
})

// recoveryRestartApp quits through the normal teardown and starts JukaHub
// again where the platform allows it. It reports whether a restart follows.
func recoveryRestartApp() bool {
	restarts := false
	switch recoveryRestartKind() {
	case "launcher":
		if dir, err := P().ExecutableDir(); err == nil {
			marker := filepath.Join(dir, recoveryRestartMarker)
			if err := os.WriteFile(marker, []byte("JukaHub asked to be started again.\n"), 0o644); err == nil {
				restarts = true
			} else {
				logWarn("recovery", "restart marker not written: %v", err)
			}
		}
	case "respawn":
		self, err := os.Executable()
		if err == nil {
			args := append([]string(nil), os.Args[1:]...)
			wd, _ := os.Getwd()
			// Registered as a cleanup hook: it runs after the window is gone and
			// the servers are closed, so the new process does not meet them.
			Cleanup = append(Cleanup, func() { recoverySpawn(self, args, wd) })
			restarts = true
		}
	}
	requestAppExit()
	return restarts
}

// recoverySpawn starts JukaHub again, detached, in the folder it ran in (the
// config is read from there).
func recoverySpawn(self string, args []string, wd string) {
	cmd := exec.Command(self, args...)
	cmd.Dir = wd
	cmd.Env = os.Environ()
	configureChiakiDetached(cmd)
	if err := cmd.Start(); err != nil {
		logWarn("recovery", "could not start JukaHub again: %v", err)
		return
	}
	_ = cmd.Process.Release()
}
