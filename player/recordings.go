package main

// recordings.go — the emulator's own captures, listed where they can be played.
//
// JukaHub cannot draw over a running emulator: the emulator owns the framebuffer
// and the hub is blocked until it exits, so an in-game "record now" button in
// the hub would never appear (see docs/FEATURE-BACKLOG.md). What is buildable is
// the other half of the recording feature: point RetroArch's record toggle at a
// folder JukaHub knows, then list the files it produced in a scene that plays
// them through the existing video player.
//
// Only RetroArch's `recording_output_directory` is managed. It is a directory,
// not a key binding: writing `input_recording_toggle` would mean guessing button
// codes for a device whose mapping we have not measured, and a wrong guess
// breaks the user's controls. The hotkey therefore stays the user's (RetroArch's
// default already offers recording) and JukaHub only decides where the file
// lands, so the two sides agree on one folder.
//
// A folder the user chose always wins: an explicit `recording_output_directory`
// in retroarch.cfg is read, honoured and left untouched. Only "default"/unset is
// replaced, which is the case where RetroArch writes somewhere the hub cannot
// predict. `recordingsDir` overrides everything from JukaHub's own config, and
// `recordings: false` leaves retroarch.cfg alone.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// recordingsEntriesVar is the list variable the record list renders.
	recordingsEntriesVar = "recording_entries"

	// recordingsDirVar holds the folder currently being listed, for logs and
	// for the toast that says where the captures live.
	recordingsDirVar = "recording_entries_dir"

	// recordingsDirConfigKey names the user's own folder in jukauser.json.
	recordingsDirConfigKey = "recordingsDir"

	// recordingsEnabledConfigKey is the switch that stops JukaHub from touching
	// retroarch.cfg. Missing means on: listing captures is the feature.
	recordingsEnabledConfigKey = "recordings"

	// triggerRecordingsScan is the scene button that rescans the folder.
	triggerRecordingsScan = "recordings_scan"

	// retroArchRecordingDirKey is RetroArch's own setting for where the record
	// toggle writes. `default` means "the recordings folder next to the config".
	retroArchRecordingDirKey = "recording_output_directory"

	// recordingsFolderName is the folder created under the card root when
	// nothing else names one.
	recordingsFolderName = "Recordings"

	// maxRecordingEntries caps one scan so a card full of clips cannot turn the
	// list into a thousand rows of work per frame.
	maxRecordingEntries = 400
)

// recordingExtensions are the container formats RetroArch (and the embedded
// player) can both write and read back. Audio files are deliberately absent: a
// capture is video, and the audio player owns the music folder.
var recordingExtensions = []string{
	".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".wmv", ".mpg", ".mpeg",
	".ts", ".m2ts",
}

// recordingsCandidates and recordingsSDRoot are variables so tests can point at
// a temporary config and a temporary card instead of the real ones.
var (
	recordingsCandidates = retroArchConfigCandidates
	recordingsSDRoot     = saveManagerSDRoot
)

// recordingsEnabled reads the opt-out switch. A config written before this
// feature has no key and gets the behaviour.
func recordingsEnabled() bool {
	if appConfig == nil {
		return true
	}
	if v, ok := customValue(appConfig.Variables.Custom, recordingsEnabledConfigKey); ok {
		return coerceBool(v)
	}
	return true
}

// isRecordingFile reports whether a file name looks like a video capture.
func isRecordingFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, want := range recordingExtensions {
		if ext == want {
			return true
		}
	}
	return false
}

// retroArchRecordingDirFor resolves the folder RetroArch was told to write to.
// It returns "" when the value is absent, `default` or `disabled`, which is
// exactly the case JukaHub is allowed to take over.
func retroArchRecordingDirFor(cfg, cfgPath string) string {
	value, ok := retroArchConfigValue(cfg, retroArchRecordingDirKey)
	if !ok {
		return ""
	}
	// resolveRetroArchSaveDir already implements RetroArch's own directory
	// dialect (`:` = the config's folder, `~` = home, relative = next to the
	// config) and returns "" for default/disabled.
	return resolveRetroArchSaveDir(value, cfgPath)
}

// absoluteRecordingDir turns a configured folder into an absolute path. A
// relative value is resolved against the card root, because that is what a user
// on a handheld means by "Recordings".
func absoluteRecordingDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
		}
	}
	cleaned, err := cleanUserPath(dir)
	if err != nil {
		return ""
	}
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	root := strings.TrimSpace(recordingsSDRoot())
	if root == "" {
		root = exeDir()
	}
	return filepath.Join(root, cleaned)
}

// recordingsDirFor is the folder the Recordings screen lists. Order: the user's
// own setting, then whatever retroarch.cfg already names, then a folder on the
// card JukaHub creates and points RetroArch at.
func recordingsDirFor(config *Config) string {
	if config != nil {
		if v, ok := customValue(config.Variables.Custom, recordingsDirConfigKey); ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				if dir := absoluteRecordingDir(s); dir != "" {
					return dir
				}
			}
		}
	}
	for _, path := range recordingsCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if dir := retroArchRecordingDirFor(string(raw), path); dir != "" {
			return dir
		}
	}
	root := strings.TrimSpace(recordingsSDRoot())
	if root == "" {
		root = exeDir()
	}
	return filepath.Join(root, recordingsFolderName)
}

// setRetroArchConfigString edits one `key = "value"` line in a RetroArch config.
// Only the value is replaced; indentation and a trailing comment survive. A
// missing key is appended, because RetroArch falls back to its built-in default
// when the line is absent.
func setRetroArchConfigString(cfg, key, value string) (string, bool) {
	newline := "\n"
	if strings.Contains(cfg, "\r\n") {
		newline = "\r\n"
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
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
		if rest == quoted {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + ` = ` + quoted + comment
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
	return cfg + key + ` = ` + quoted + newline, true
}

// applyRetroArchRecordingDir is the pure edit: one key, everything else
// byte-for-byte intact. Slashes are normalized so a Windows path cannot be
// mangled by escaping.
func applyRetroArchRecordingDir(cfg, dir string) (string, bool) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return cfg, false
	}
	return setRetroArchConfigString(cfg, retroArchRecordingDirKey, filepath.ToSlash(dir))
}

// prepareRecordings is the launch-time entry point, called next to
// prepareSavestateResume. It never blocks or fails a launch: a read-only card, a
// missing config or a standalone emulator is logged (or skipped) and the game
// still starts. It reports whether a RetroArch config was configured.
func prepareRecordings(launcher string) bool {
	if !recordingsEnabled() || !retroArchLauncher(launcher) {
		return false
	}
	dir := recordingsDirFor(appConfig)
	if dir == "" {
		return false
	}
	if !dirExists(dir) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("[recordings] cannot create %s: %v", dir, err)
			return false
		}
	}
	for _, path := range recordingsCandidates() {
		if !regularFile(path) {
			continue
		}
		changed, err := editRetroArchConfig(path, func(cfgText string) (string, bool) {
			// A folder the user picked is theirs: never rewrite it.
			if retroArchRecordingDirFor(cfgText, path) != "" {
				return cfgText, false
			}
			return applyRetroArchRecordingDir(cfgText, dir)
		})
		if err != nil {
			log.Printf("[recordings] cannot set the recording folder in %s: %v", path, err)
			return false
		}
		if changed {
			Log().Info("recording folder set", "config", path, "dir", dir)
		}
		return true
	}
	return false
}

// scanRecordings lists the captures in dir, newest first. Zero-byte files are
// skipped: a capture that is still being written has nothing to play yet, and
// listing it would only produce a failed open.
func scanRecordings(dir string) []FileEntry {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type capture struct {
		entry FileEntry
		at    time.Time
	}
	found := make([]capture, 0, len(entries))
	for _, de := range entries {
		if de.IsDir() || !isRecordingFile(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		found = append(found, capture{
			entry: FileEntry{Name: de.Name(), Path: filepath.Join(dir, de.Name())},
			at:    info.ModTime(),
		})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].at.Equal(found[j].at) {
			return found[i].at.After(found[j].at)
		}
		return strings.ToLower(found[i].entry.Name) < strings.ToLower(found[j].entry.Name)
	})
	if len(found) > maxRecordingEntries {
		found = found[:maxRecordingEntries]
	}
	list := make([]FileEntry, 0, len(found))
	for _, c := range found {
		list = append(list, c.entry)
	}
	return list
}

// loadRecordings rescans the folder and publishes it for the dynamiclist. It
// returns the number of captures so the rescan button can say what happened.
func loadRecordings(config *Config) int {
	dir := recordingsDirFor(config)
	entries := scanRecordings(dir)
	if config != nil {
		config.Variables.Custom[recordingsEntriesVar] = entries
		config.Variables.Custom[recordingsDirVar] = dir
	}
	focusedFileIndex = 0
	log.Printf("[recordings] %d capture(s) in %s", len(entries), dir)
	return len(entries)
}

// recordingsScanMessage is the toast the rescan button shows: it names the
// folder, because "no recordings" without a path is not actionable.
func recordingsScanMessage(config *Config, count int) string {
	dir := recordingsDirFor(config)
	switch count {
	case 0:
		return fmt.Sprintf("No captures yet in %s", dir)
	case 1:
		return fmt.Sprintf("1 capture in %s", dir)
	default:
		return fmt.Sprintf("%d captures in %s", count, dir)
	}
}
