package main

// savestate_resume.go — put the player back where they stopped.
//
// The library already remembers which game was played last (game-recents.json)
// and the Continue row relaunches it. What it could not do is restore the game
// itself: that is a save state, and the state file belongs to the emulator.
// RetroArch can produce and consume it without help when two switches are on:
//
//	savestate_auto_save = "true"   write a state when RetroArch exits
//	savestate_auto_load = "true"   load that state when the game starts
//
// JukaHub's job is to make sure those switches are set for a RetroArch launch,
// without rewriting the rest of the user's config. The edit is line-based, so
// comments, ordering and every unrelated key survive; the original file is kept
// once as `retroarch.cfg.jukahub-backup`. Standalone emulators (mGBA, PPSSPP,
// DraStic) own their state formats, so they are left alone rather than promised
// a resume that cannot work.
//
// The switches are set for JukaHub's launch only (racfgscope.go): the user's
// own values go back when the game exits, so a game started from the
// firmware's menu behaves exactly as it did before JukaHub — it used to
// auto-load and auto-save there too after one JukaHub launch. RetroArch names
// the auto state after the loaded content, so each game has its own file.
// `savestateResume: false` in the config turns the automation off.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	savestateAutoSaveKey = "savestate_auto_save"
	savestateAutoLoadKey = "savestate_auto_load"

	// savestateResumeConfigKey is the user-config switch. Missing means on:
	// resume is the behaviour the library is selling, not an opt-in extra.
	savestateResumeConfigKey = "savestateResume"

	// savestateBackupSuffix keeps the pre-JukaHub file next to the original.
	// It is written once and never overwritten, so the first backup stays the
	// one to restore from.
	savestateBackupSuffix = ".jukahub-backup"
)

// savestateResumeCandidates and the scan limit are variables so the tests can
// point at a temporary config and a fake launcher.
var (
	savestateResumeCandidates        = retroArchConfigCandidates
	savestateResumeLauncherScanLimit = int64(64 << 10)
)

// savestateResumeEnabled reads the opt-out switch. A config that predates this
// feature has no key and gets the behaviour.
func savestateResumeEnabled() bool {
	if appConfig == nil {
		return true
	}
	if v, ok := customValue(appConfig.Variables.Custom, savestateResumeConfigKey); ok {
		return coerceBool(v)
	}
	return true
}

// retroArchLauncher reports whether starting launcher starts RetroArch. The
// stock firmware calls a per-system launch.sh, so the path alone is not enough:
// the script's content is the reliable signal — the word RetroArch, TrimUI's
// own binary names (ra64.trimui does not contain it) or a libretro core.
// Direct retroarch(.exe) launches and a folder named RetroArch are matched by
// path.
func retroArchLauncher(launcher string) bool {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		return false
	}
	lowerPath := strings.ToLower(filepath.ToSlash(launcher))
	base := strings.ToLower(filepath.Base(launcher))
	if base == "retroarch" || base == "retroarch.exe" {
		return true
	}
	// A firmware keeps the emulator in its own folder (Emus/RetroArch/), but a
	// ROM or download called "retroarch-demo" must not count.
	for _, segment := range strings.Split(lowerPath, "/") {
		if segment == "retroarch" {
			return true
		}
	}
	f, err := os.Open(launcher)
	if err != nil {
		return false
	}
	defer f.Close()
	limit := savestateResumeLauncherScanLimit
	if limit <= 0 {
		limit = 64 << 10
	}
	head, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return false
	}
	text := strings.ToLower(string(head))
	for _, marker := range []string{"retroarch", "ra64.", "ra32.", "_libretro."} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// savestateResumeValues are the two switches behind Continue, as the scoped
// edit takes them (racfgscope.go).
func savestateResumeValues() []raScopeValue {
	return []raScopeValue{
		{Key: savestateAutoSaveKey, Value: "true"},
		{Key: savestateAutoLoadKey, Value: "true"},
	}
}

// editRetroArchConfig applies a pure text edit to a RetroArch config and writes
// it through a temporary file so a power loss cannot truncate retroarch.cfg. The
// original is copied to the backup once, before the first edit.
//
// Every edit goes through here — a launch's scoped values, their restore and
// the one-time repair (racfgscope.go) — so they share one crash-safe write.
func editRetroArchConfig(path string, edit func(string) (string, bool)) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s is not a file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	updated, changed := edit(string(raw))
	if !changed {
		return false, nil
	}

	backup := path + savestateBackupSuffix
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := os.WriteFile(backup, raw, info.Mode().Perm()); err != nil {
			return false, fmt.Errorf("back up %s: %w", path, err)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".retroarch-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

// prepareSavestateResume is the launch-time entry point. It never blocks or
// fails a launch: a read-only card or a missing config is logged and the game
// still starts. The switches hold for this launch only (racfgscope.go). It
// reports whether a RetroArch config was found and configured.
func prepareSavestateResume(launcher string) bool {
	if !savestateResumeEnabled() || !retroArchLauncher(launcher) {
		return false
	}
	configured := false
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		changed, err := raScopeApply(path, savestateResumeValues())
		if err != nil {
			logWarn("resume", "cannot enable save-state resume in %s: %v", path, err)
			continue
		}
		configured = true
		if changed {
			Log().Info("save-state resume enabled for this launch", "config", path)
		}
	}
	return configured
}
