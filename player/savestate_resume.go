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
// The effect is global to RetroArch, not per game: RetroArch names the auto
// state after the loaded content, so each game still has its own file.
// `savestateResume: false` in the config turns the automation off.

import (
	"fmt"
	"io"
	"log"
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
// the script's content is the reliable signal. Direct retroarch(.exe) launches
// and a folder named RetroArch are matched by path.
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
	return strings.Contains(strings.ToLower(string(head)), "retroarch")
}

// setRetroArchConfigBool edits one `key = value` line in a RetroArch config.
// Only the value is replaced; indentation and a trailing comment are kept. A
// missing key is appended, because RetroArch uses its built-in default when the
// line is absent.
func setRetroArchConfigBool(cfg, key string) (string, bool) {
	newline := "\n"
	if strings.Contains(cfg, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(cfg, newline)
	changed, found := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(trimmed[:eq]), key) {
			continue
		}
		found = true
		rest := strings.TrimSpace(trimmed[eq+1:])
		comment := ""
		if hash := strings.IndexByte(rest, '#'); hash >= 0 {
			comment = " " + strings.TrimSpace(rest[hash:])
			rest = strings.TrimSpace(rest[:hash])
		}
		if strings.EqualFold(strings.Trim(rest, `"'`), "true") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + ` = "true"` + comment
		changed = true
	}
	if found {
		if !changed {
			return cfg, false
		}
		return strings.Join(lines, newline), true
	}
	if cfg != "" && !strings.HasSuffix(cfg, "\n") {
		cfg += newline
	}
	return cfg + key + ` = "true"` + newline, true
}

// ensureSavestateResume is the pure edit: both switches on, everything else
// byte-for-byte intact. It reports whether the text actually changed so a
// launch does not rewrite the card on every play.
func ensureSavestateResume(cfg string) (string, bool) {
	updated, changedSave := setRetroArchConfigBool(cfg, savestateAutoSaveKey)
	updated, changedLoad := setRetroArchConfigBool(updated, savestateAutoLoadKey)
	return updated, changedSave || changedLoad
}

// enableSavestateResume writes the edit through a temporary file so a power
// loss cannot truncate retroarch.cfg. The original is copied to the backup once.
func enableSavestateResume(path string) (bool, error) {
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
	updated, changed := ensureSavestateResume(string(raw))
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
// still starts. It reports whether a RetroArch config was found and configured.
func prepareSavestateResume(launcher string) bool {
	if !savestateResumeEnabled() || !retroArchLauncher(launcher) {
		return false
	}
	for _, path := range savestateResumeCandidates() {
		if !regularFile(path) {
			continue
		}
		changed, err := enableSavestateResume(path)
		if err != nil {
			log.Printf("[resume] cannot enable save-state resume in %s: %v", path, err)
			return false
		}
		if changed {
			Log().Info("save-state resume enabled", "config", path)
		}
		return true
	}
	return false
}
