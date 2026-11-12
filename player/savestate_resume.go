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
	"strconv"
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

// savestateSRAMFlushInterval is how often, in seconds, RetroArch writes a
// changed battery save (SRAM) while the game runs. RetroArch's default is 0 —
// SRAM only on exit — so a crash, a dead battery or a forced power-off lost
// every in-game save since the game started. Mosaik 0.9.18 writes it every 10
// seconds; JukaHub asks RetroArch for the same through autosave_interval,
// which only writes when the save actually changed.
const (
	savestateSRAMFlushKey      = "autosave_interval"
	savestateSRAMFlushInterval = "10"
)

// savestateSRAMFlushValue is the periodic SRAM flush as a scoped launch value:
// a floor (RaiseOnly), so a user who set their own nonzero interval keeps it,
// and like the resume switches it is put back when the game exits. It rides
// on the same savestateResume switch: one opt-out for "JukaHub touches my
// RetroArch saves".
func savestateSRAMFlushValue() raScopeValue {
	return raScopeValue{Key: savestateSRAMFlushKey, Value: savestateSRAMFlushInterval, RaiseOnly: true}
}

// savestateMenuValues are what the in-game menu's Save state and Load state
// need for a launch (gamemenu_states.go): RetroArch's network command
// interface on (its port is left as the user has it; RetroArch's default is
// 55355), a screenshot beside every state for the slot picker, and a known
// current slot for the older RetroArch builds whose only way to a slot is
// stepping to it. Kept apart from savestateResumeValues: those are also what
// the one-time repair of older versions' edits looks for (racfgscope.go), and
// no older version ever wrote these.
func savestateMenuValues() []raScopeValue {
	return []raScopeValue{
		{Key: "network_cmd_enable", Value: "true"},
		{Key: "savestate_thumbnail_enable", Value: "true"},
		{Key: "state_slot", Value: strconv.Itoa(gmStateSlotAtLaunch)},
	}
}

// prepareGameMenuStates is the second launch-time hook for a library game
// (game_library.go, right after prepareSavestateResume). For a RetroArch game
// it keeps the previous auto save (gmetaRotateAutoStates: RetroArch is about
// to replace the auto state when this game closes) and, where the in-game
// menu can open, scopes savestateMenuValues to this launch and tells the menu
// which game and port it is. Like prepareSavestateResume it never blocks or
// fails a launch.
//
// The command interface listens on every network the handheld is on while
// the game runs (RetroArch binds it that way); it is on only for JukaHub's
// launch, as the low-battery guard (lowbattery.go) already relied on.
func prepareGameMenuStates(launcher, romPath string) {
	gmStatesForget()
	if strings.TrimSpace(romPath) == "" || !retroArchLauncher(launcher) {
		return
	}
	gmetaRotateAutoStates(romPath)
	if !gameAssistSupported() || !gameMenuEnabled(appConfig) {
		return
	}
	port := 0
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, savestateMenuValues()); err != nil {
			logWarn("resume", "cannot enable the in-game save slots in %s: %v", path, err)
			continue
		}
		if port == 0 {
			if raw, err := os.ReadFile(path); err == nil {
				port = networkCmdPort(string(raw))
			}
		}
	}
	if port > 0 {
		gmStatesNoteLaunch(romPath, port)
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
		changed, err := raScopeApply(path, append(savestateResumeValues(), savestateSRAMFlushValue()))
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
