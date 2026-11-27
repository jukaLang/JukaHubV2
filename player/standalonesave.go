package main

// standalonesave.go — a standalone emulator's own save-on-exit switch, turned
// on for one JukaHub launch and put back when the game exits.
//
// The session-safe recipes (sessionsafe_recipes.go) say whether an emulator
// writes a state as it quits, but until now JukaHub never changed that option:
// a player who had it off got the hold instead of a Save at the battery or a
// power off. This file edits the same config the emulator's last run read,
// through racfgscope.go's journal: the user's value is recorded before the
// file changes, restored when the last screen-taking child exits or at the
// next start after a crash, and a value the player changed during the game is
// left alone.
//
// Only the emulators whose save-on-exit option is documented are touched:
// DraStic (enable_autosavestate), Flycast (Dreamcast.AutoSaveState) and
// Mednafen (autosave). Flycast also gets Dreamcast.AutoLoadState, and
// Mednafen's autosave loads at the next start by itself; DraStic's resume is
// not documented, so nothing is claimed for it. PPSSPP's JukaHub build saves
// on request instead (instantresume.go) and is left alone.

import (
	"os"
	"path/filepath"
	"strings"
)

// raConfigFormat is how a journaled config writes a key. The empty value is
// RetroArch's `key = "value"`; it is also what journals written before this
// field existed carry, so they keep restoring as RetroArch configs.
type raConfigFormat string

const (
	raFormatRetroArch raConfigFormat = ""
	raFormatEquals    raConfigFormat = "equals" // DraStic and Flycast: key = value
	raFormatSpace     raConfigFormat = "space"  // Mednafen: key value
)

// sessionSafeSaveOnExitValues are the keys a launch sets for one emulator.
// A nil answer means the emulator is not one this file edits.
func sessionSafeSaveOnExitValues(id string) []raScopeValue {
	switch id {
	case "drastic":
		return []raScopeValue{{Key: "enable_autosavestate", Value: "1"}}
	case "flycast":
		return []raScopeValue{
			{Key: "Dreamcast.AutoSaveState", Value: "yes"},
			{Key: "Dreamcast.AutoLoadState", Value: "yes"},
		}
	case "mednafen":
		return []raScopeValue{{Key: "autosave", Value: "1"}}
	}
	return nil
}

// sessionSafeSaveOnExitFormat is the config syntax of that emulator.
func sessionSafeSaveOnExitFormat(id string) raConfigFormat {
	switch id {
	case "drastic", "flycast":
		return raFormatEquals
	case "mednafen":
		return raFormatSpace
	}
	return raFormatRetroArch
}

// standaloneSaveConfigBases are the folders an emulator's config can be
// under: its program's folder, the folder above it (CrossMix keeps emulators
// in Emus/<name> beside a config), and the user's home (Mednafen's
// ~/.mednafen, Flycast's ~/.config/flycast).
func standaloneSaveConfigBases(launcher string) []string {
	var bases []string
	if dir := filepath.Dir(strings.TrimSpace(launcher)); dir != "" && dir != "." {
		bases = append(bases, dir, filepath.Dir(dir))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		bases = append(bases, home)
	}
	return bases
}

// standaloneSaveConfigPath is the first config of recipe found under bases.
func standaloneSaveConfigPath(recipe *sessionSafeRecipe, bases []string) string {
	if recipe == nil || len(recipe.Configs) == 0 {
		return ""
	}
	for _, base := range bases {
		for _, rel := range recipe.Configs {
			p := filepath.Join(base, filepath.FromSlash(rel))
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

// prepareStandaloneSaveOnExit turns the emulator's save-on-exit option on for
// this launch. It is called from launchLibraryGameRun before the game starts;
// a launcher that is not one of the three emulators does nothing.
func prepareStandaloneSaveOnExit(launcher string, bases []string) {
	if bases == nil {
		bases = standaloneSaveConfigBases(launcher)
	}
	recipe, ok := sessionSafeRecipeFor(launcher)
	if !ok {
		return
	}
	values := sessionSafeSaveOnExitValues(recipe.ID)
	if len(values) == 0 {
		return
	}
	path := standaloneSaveConfigPath(recipe, bases)
	if path == "" {
		logDebug("launch", "%s has no config yet; its save-on-exit is left as it is", recipe.Label)
		return
	}
	format := sessionSafeSaveOnExitFormat(recipe.ID)
	changed, err := raScopeApplyFormat(path, values, format)
	if err != nil {
		logWarn("launch", "could not turn on %s's save on exit in %s: %v", recipe.Label, path, err)
		return
	}
	if changed {
		logInfo("launch", "%s saves on exit for this launch (%s)", recipe.Label, path)
	}
}

// ── Config text in the standalone emulators' syntaxes (pure) ──────────────

// standaloneConfigLine splits one line into key and value. equals is true for
// `key = value`, false for `key value`. A leading BOM, indentation, section
// headers and blank/comment lines are handled; a trailing comment is left in
// tail so a rewrite keeps it.
func standaloneConfigLine(line string, equals bool) (key, value, tail string, ok bool) {
	line = strings.TrimSuffix(line, "\r")
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' || trimmed[0] == '[' {
		return "", "", "", false
	}
	if equals {
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			return "", "", "", false
		}
		key = strings.TrimSpace(trimmed[:eq])
		rest := strings.TrimSpace(trimmed[eq+1:])
		if rest == "" {
			return "", "", "", false
		}
		value, tail = standaloneConfigValue(rest)
		return key, value, tail, key != ""
	}
	sp := strings.IndexAny(trimmed, " \t")
	if sp < 0 {
		return "", "", "", false
	}
	key = strings.TrimSpace(trimmed[:sp])
	rest := strings.TrimSpace(trimmed[sp+1:])
	if rest == "" {
		return "", "", "", false
	}
	value, tail = standaloneConfigValue(rest)
	return key, value, tail, key != ""
}

// standaloneConfigValue cuts a value from its trailing comment and quotes. A
// quoted value ends at its closing quote so a '#' inside it stays part of the
// value.
func standaloneConfigValue(rest string) (value, tail string) {
	if strings.HasPrefix(rest, `"`) {
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			return rest[1 : 1+end], strings.TrimSpace(rest[2+end:])
		}
		return rest[1:], ""
	}
	if i := strings.IndexAny(rest, "#;"); i >= 0 && (i == 0 || rest[i-1] == ' ' || rest[i-1] == '\t') {
		return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i:])
	}
	return rest, ""
}

// standaloneConfigGet is the last value of key, the way the emulators' own
// readers use a later duplicate. The format is chosen by the caller.
func standaloneConfigGet(cfg, key string, format raConfigFormat) (string, bool) {
	value, found := "", false
	for _, line := range strings.Split(cfg, "\n") {
		k, v, _, ok := standaloneConfigLine(line, format != raFormatSpace)
		if !ok || !strings.EqualFold(k, key) {
			continue
		}
		value, found = v, true
	}
	return value, found
}

// standaloneConfigSet gives every occurrence of key the value and appends the
// key when it is absent, keeping indentation and a trailing comment. The
// emulators' configs are not RetroArch's: their values are bare, not quoted.
func standaloneConfigSet(cfg, key, value string, format raConfigFormat) (string, bool) {
	equals := format != raFormatSpace
	lines := strings.Split(cfg, "\n")
	changed, found := false, false
	for i, line := range lines {
		k, current, tail, ok := standaloneConfigLine(line, equals)
		if !ok || !strings.EqualFold(k, key) {
			continue
		}
		found = true
		if current == value {
			continue
		}
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if tail != "" {
			tail = " " + tail
		}
		if equals {
			lines[i] = indent + key + " = " + value + tail + cr
		} else {
			lines[i] = indent + key + " " + value + tail + cr
		}
		changed = true
	}
	if found {
		if !changed {
			return cfg, false
		}
		return strings.Join(lines, "\n"), true
	}
	newline := raConfigNewline(cfg)
	if cfg != "" && !strings.HasSuffix(cfg, "\n") {
		cfg += newline
	}
	if equals {
		return cfg + key + " = " + value + newline, true
	}
	return cfg + key + " " + value + newline, true
}

// standaloneConfigUnset removes every line that sets key, so the emulator
// falls back to its own default when the user never had the key.
func standaloneConfigUnset(cfg, key string, format raConfigFormat) (string, bool) {
	equals := format != raFormatSpace
	lines := strings.Split(cfg, "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if k, _, _, ok := standaloneConfigLine(line, equals); ok && strings.EqualFold(k, key) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return cfg, false
	}
	return strings.Join(kept, "\n"), true
}

// configGetIn reads a key in any format the journal carries.
func configGetIn(cfg, key string, format raConfigFormat) (string, bool) {
	if format == raFormatRetroArch {
		return raConfigGet(cfg, key)
	}
	return standaloneConfigGet(cfg, key, format)
}

// configSetIn writes a key in any format the journal carries.
func configSetIn(cfg, key, value string, format raConfigFormat) (string, bool) {
	if format == raFormatRetroArch {
		return raConfigSet(cfg, key, value)
	}
	return standaloneConfigSet(cfg, key, value, format)
}

// configUnsetIn removes a key in any format the journal carries.
func configUnsetIn(cfg, key string, format raConfigFormat) (string, bool) {
	if format == raFormatRetroArch {
		return raConfigUnset(cfg, key)
	}
	return standaloneConfigUnset(cfg, key, format)
}
