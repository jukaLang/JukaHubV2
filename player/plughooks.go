package main

// plughooks.go — what switching a plugin on actually changes.
//
// The Plugins page had an on/off switch per plugin, but for a plugin that only
// had buttons the switch did nothing anyone could see: the buttons lived on the
// Plugins page itself and nothing else in JukaHub knew the plugin existed. Now
// a plugin that is on is part of the system:
//
//   - Switching it on runs its "onEnable" action straight away and shows the
//     result ("Clock is on: Mon 09 Nov · 18:41"), so the switch answers.
//   - Its "quick" actions (the first action when none are named) are listed in
//     the Start menu under Plugins, so they can be pressed from any screen;
//     switching it off takes them out again and says so.
//   - Its "onStartup" action runs when JukaHub starts — "startupDaily" limits
//     that to the first start of the day (the quote of the day).
//   - Action ids starting with "@" are done by JukaHub itself, on the main
//     thread, with the plugin's own contributions: "@apply-theme" switches to
//     its theme, "@apply-pack" applies its experience pack, "@use-profile"
//     switches to its control profile, "@open-library" opens the game library.
//     Any plugin can offer them; no script runs.
//
// Every key is optional, so older manifests load as before.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// plugStartupDayKey is the custom variable holding the day each plugin's
// daily startup action last ran: {"<folder>": "2006-01-02"}.
const plugStartupDayKey = "pluginStartupDay"

// plugThemeReturnKey remembers the theme in use before "@apply-theme", so
// "@restore-theme" can go back to it.
const plugThemeReturnKey = "pluginThemeReturn"

// plugKnownAction returns id when the plugin has that action, "" otherwise,
// so a hook that names a missing action is ignored instead of failing later.
func plugKnownAction(actions []plugAction, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, a := range actions {
		if a.ID == id {
			return id
		}
	}
	return ""
}

// plugQuickActions resolves the manifest's "quick" ids to actions; with none
// named, the first action is the plugin's Start menu shortcut.
func plugQuickActions(actions []plugAction, ids []string) []plugAction {
	if len(actions) == 0 {
		return nil
	}
	if len(ids) == 0 {
		return actions[:1:1]
	}
	out := make([]plugAction, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		for _, a := range actions {
			if a.ID == id {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// ── Running an action ────────────────────────────────────────────────────

// plugUIFunc is an action JukaHub does itself on the main thread. It returns
// the text to show as the result and whether it already told the user (the
// theme and control-profile switches toast on their own).
type plugUIFunc func(c *Config, info PluginInfo, vars map[string]interface{}) (out string, toasted bool)

// plugBuiltinActions are the "@" actions any plugin may offer.
var plugBuiltinActions map[string]plugUIFunc

// plugBundledUIActions are main-thread actions of individual bundled plugins
// (the notes plugin's keyboard), keyed "folder/action".
var plugBundledUIActions map[string]plugUIFunc

func init() {
	plugBuiltinActions = map[string]plugUIFunc{
		"@apply-theme":     plugApplyTheme,
		"@restore-theme":   plugRestoreTheme,
		"@apply-pack":      plugApplyPack,
		"@undo-pack":       plugUndoPack,
		"@use-profile":     plugUseProfile,
		"@default-profile": plugDefaultProfile,
		"@open-library":    plugOpenLibrary,
	}
	plugBundledUIActions = map[string]plugUIFunc{
		"notes/add": plugNotesPrompt,
	}
}

// plugUIActionFor returns the main-thread handler for an action, or nil when
// the action is the plugin's own (script or Go version).
func plugUIActionFor(info PluginInfo, action string) plugUIFunc {
	if strings.HasPrefix(action, "@") {
		return plugBuiltinActions[action]
	}
	if strings.TrimSpace(info.Author) == plugNativeAuthor {
		return plugBundledUIActions[info.Name+"/"+action]
	}
	return nil
}

// plugDispatch runs one action of a loaded plugin from the main thread: a
// JukaHub action inline, anything else on the plugin goroutine. It reports
// false only when the plugin is still busy with an earlier action.
func plugDispatch(c *Config, info PluginInfo, action, label string, vars map[string]interface{}) bool {
	if c != nil {
		plugNativeClock12.Store(c.Variables.Clock12)
	}
	if fn := plugUIActionFor(info, action); fn != nil {
		out, toasted := fn(c, info, vars)
		out = strings.TrimSpace(out)
		if out != "" {
			plugRecordOutput(info.Name, out)
			if !toasted {
				ShowToast(label+": "+plugFirstLine(out), ToastKindInfo)
			}
		}
		return true
	}
	if strings.HasPrefix(action, "@") {
		ShowToast(label+": JukaHub has no action "+action, ToastKindWarning)
		return true
	}
	return plugRunAsync(info, action, label, vars)
}

// plugFirstLine is the toast-sized part of an output.
func plugFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// plugRecordOutput stores a result the way a finished script's is stored, so
// the Plugins page shows it as the last result.
func plugRecordOutput(name, out string) {
	plugRunState.mu.Lock()
	defer plugRunState.mu.Unlock()
	if plugRunState.output == nil {
		plugRunState.running = map[string]string{}
		plugRunState.output = map[string]string{}
		plugRunState.at = map[string]time.Time{}
	}
	plugRunState.output[name] = out
	plugRunState.at[name] = time.Now()
}

// plugRunLoaded runs an action of a plugin by name, as the Start menu does.
func plugRunLoaded(c *Config, name, action string) {
	info, ok := GetPlugins()[name]
	if !ok || info == nil {
		ShowToast("That plugin is off now (Settings › Plugins)", ToastKindInfo)
		return
	}
	title := plugTitle(info.Name, info.Title)
	vars := map[string]interface{}{}
	if c != nil {
		vars = snapshotVars(c)
	}
	if !plugDispatch(c, *info, action, title, vars) {
		ShowToast(title+" is still running", ToastKindInfo)
	}
}

// ── Switching on and off ─────────────────────────────────────────────────

// plugAfterToggle tells the user what switching a plugin changed, and runs
// its onEnable action when it was switched on. It runs after the rescan, so
// the plugin is already loaded (or withdrawn).
func plugAfterToggle(c *Config, name string, on bool) {
	e, ok := plugEntryNamed(name)
	if !ok {
		return
	}
	if !on {
		msg := e.Title + " is off"
		switch {
		case e.Provides != "" && len(e.Quick) > 0:
			msg += " · " + e.Provides + " and its Start menu buttons are gone"
		case e.Provides != "":
			msg += " · " + e.Provides + " withdrawn"
		case len(e.Quick) > 0:
			msg += " · its Start menu buttons are gone"
		}
		ShowToast(msg, ToastKindInfo)
		return
	}
	info, loaded := GetPlugins()[name]
	if loaded && info != nil && e.RunNote == "" && e.OnEnable != "" {
		vars := map[string]interface{}{}
		if c != nil {
			vars = snapshotVars(c)
		}
		if plugDispatch(c, *info, e.OnEnable, e.Title+" is on", vars) {
			return
		}
	}
	msg := e.Title + " is on"
	switch {
	case e.Problem != "":
		msg += " · " + e.RunNote
	case len(e.Quick) > 0:
		msg += " · its buttons are in the Start menu under Plugins"
	case e.Provides != "":
		msg += " · adds " + e.Provides
	}
	ShowToast(msg, ToastKindSuccess)
}

// ── Startup ──────────────────────────────────────────────────────────────

// plugStartupRan is set by the first scan of a launch; the startup actions
// run once per launch, not on every rescan of the Plugins page.
var plugStartupRan bool

// plugRunStartupHooks runs the onStartup action of every plugin that is on. A
// config without a custom block (a test's) runs nothing.
func plugRunStartupHooks(c *Config) {
	if plugStartupRan || c == nil || c.Variables.Custom == nil {
		return
	}
	plugStartupRan = true
	today := time.Now().Format("2006-01-02")
	var days map[string]interface{}
	for _, e := range plugCatalogEntries() {
		if !e.Enabled || e.RunNote != "" || e.OnStartup == "" {
			continue
		}
		info, ok := GetPlugins()[e.Name]
		if !ok || info == nil {
			continue
		}
		if e.StartupDaily {
			if days == nil {
				// Replaced, never edited in place: the saver may be encoding
				// the previous map on another goroutine.
				days = map[string]interface{}{}
				if old, ok := c.Variables.Custom[plugStartupDayKey].(map[string]interface{}); ok {
					for k, v := range old {
						days[k] = v
					}
				}
			}
			if coerceString(days[e.Name]) == today {
				continue
			}
		}
		if plugDispatch(c, *info, e.OnStartup, e.Title, snapshotVars(c)) && e.StartupDaily {
			days[e.Name] = today
		}
	}
	if days != nil {
		c.Variables.Custom[plugStartupDayKey] = days
		saveUserConfigDebounced(userConfigFromVariables(c))
	}
}

// ── Start menu ───────────────────────────────────────────────────────────

// plugCatalogGen changes with every scan, so the Start menu rebuilds its
// Plugins group only when the list did.
var plugCatalogGen int

// plugMenuCache is the last Plugins group built.
var plugMenuCache struct {
	gen   int
	built bool
	items []startMenuEntry
}

// plugStartMenuEntries lists the quick actions of every plugin that is on and
// can run, then a row that opens the Plugins page.
func plugStartMenuEntries() []startMenuEntry {
	if plugMenuCache.built && plugMenuCache.gen == plugCatalogGen {
		return plugMenuCache.items
	}
	var items []startMenuEntry
	for _, e := range plugCatalogEntries() {
		if !e.Enabled || e.RunNote != "" {
			continue
		}
		glyph := e.Icon
		if glyph == "" {
			glyph = "plugin"
		}
		for _, a := range e.Quick {
			name, id := e.Name, a.ID
			label := e.Title
			if len(e.Quick) > 1 || !strings.EqualFold(a.Label, e.Title) {
				label += " · " + a.Label
			}
			items = append(items, startMenuEntry{
				name:   label,
				icon:   "+",
				glyph:  glyph,
				action: func(c *Config) { plugRunLoaded(c, name, id) },
			})
		}
	}
	// An action rather than a scene row: a config without the Plugins screen
	// then gets "That screen is not part of this setup" instead of a dead row.
	items = append(items, startMenuEntry{name: "Manage plugins", icon: "P", glyph: "plugin", action: setOpenScene(plugSceneName)})
	plugMenuCache.gen, plugMenuCache.built, plugMenuCache.items = plugCatalogGen, true, items
	return items
}

// ── JukaHub's own actions ("@…") ─────────────────────────────────────────

// plugThemeIDs are the theme presets a plugin registered (plugx.go), sorted.
func plugThemeIDs(name string) []string {
	var ids []string
	for id, owner := range plugxThemeOwners {
		if owner == name {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func plugApplyTheme(c *Config, info PluginInfo, _ map[string]interface{}) (string, bool) {
	ids := plugThemeIDs(info.Name)
	if c == nil || len(ids) == 0 {
		return "This plugin has no theme to apply (is it switched on?)", false
	}
	id := ids[0]
	cur := themePresetName(c)
	if cur == "" {
		// Never picked one: the palette in use is the fallback preset.
		cur = "dark"
	}
	if cur != id {
		if c.Variables.Custom == nil {
			c.Variables.Custom = map[string]interface{}{}
		}
		c.Variables.Custom[plugThemeReturnKey] = cur
	}
	// Saves the settings and announces the theme itself.
	ApplyThemePreset(c, id)
	return "Switched to " + GetThemePreset(id).Name + ". Back to my theme switches back.", true
}

func plugRestoreTheme(c *Config, info PluginInfo, _ map[string]interface{}) (string, bool) {
	if c == nil {
		return "", false
	}
	prev := strings.TrimSpace(coerceString(c.Variables.Custom[plugThemeReturnKey]))
	if prev == "" {
		return "No earlier theme to go back to: pick one in Settings › Display & theme › Theme presets", false
	}
	cur := themePresetName(c)
	if cur == prev {
		return "Already on " + GetThemePreset(prev).Name, false
	}
	owned := false
	for _, id := range plugThemeIDs(info.Name) {
		owned = owned || id == cur
	}
	if !owned {
		return "You have picked another theme since; it stays", false
	}
	ApplyThemePreset(c, prev)
	return "Back to " + GetThemePreset(prev).Name, true
}

// plugPackFor is the plugin's first experience pack.
func plugPackFor(name string) (xpackEntry, bool) {
	for _, e := range xpackEntries() {
		if e.Source == xpackSourcePlugin+name {
			return e, true
		}
	}
	return xpackEntry{}, false
}

func plugApplyPack(c *Config, info PluginInfo, _ map[string]interface{}) (string, bool) {
	e, ok := plugPackFor(info.Name)
	if c == nil || !ok {
		return "This plugin has no experience pack to apply (is it switched on?)", false
	}
	note, err := xpackApply(c, e)
	if err != nil {
		return "Could not apply " + e.Manifest.Name + ": " + err.Error(), false
	}
	PlayActivateSound()
	xpackRefreshUndo(c)
	out := e.Manifest.Name + " is on · Undo puts your previous look back"
	if note != "" {
		out += " · skipped " + note
	}
	ShowToast(out, ToastKindSuccess)
	return out, true
}

func plugUndoPack(c *Config, _ PluginInfo, _ map[string]interface{}) (string, bool) {
	if c == nil {
		return "", false
	}
	name, err := xpackUndo(c)
	if err != nil {
		return "Nothing to undo", false
	}
	PlayBackSound()
	xpackRefreshUndo(c)
	return "Took back " + name + " · your previous look is back", false
}

// plugProfileNames reads the names of the control profiles a plugin ships,
// from its manifest's "inputProfiles" and each file's "profile:" line.
func plugProfileNames(name string) []string {
	root := filepath.Join(pluginDir, name)
	data, err := os.ReadFile(filepath.Join(root, "plugin.json"))
	if err != nil {
		return nil
	}
	var m struct {
		InputProfiles []string `json:"inputProfiles"`
	}
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	var names []string
	for _, rel := range m.InputProfiles {
		file, err := resolveWithinRoot(root, rel)
		if err != nil {
			continue
		}
		text, err := os.ReadFile(file)
		if err != nil || len(text) > plugxMaxFileBytes {
			continue
		}
		for _, line := range strings.Split(string(text), "\n") {
			line = strings.TrimSpace(line)
			if len(line) > 8 && strings.EqualFold(line[:8], "profile:") {
				if n := strings.TrimSpace(line[8:]); n != "" {
					names = append(names, n)
				}
				break
			}
		}
	}
	return names
}

func plugUseProfile(_ *Config, info PluginInfo, _ map[string]interface{}) (string, bool) {
	names := plugProfileNames(info.Name)
	if len(names) == 0 {
		return "This plugin has no control profile", false
	}
	if !inmapActivateByName(names[0]) {
		return names[0] + " is not loaded yet; switch the plugin off and on again", false
	}
	return "Controls: " + names[0] + " · Use default controls switches back", true
}

func plugDefaultProfile(_ *Config, _ PluginInfo, _ map[string]interface{}) (string, bool) {
	if !inmapActivateByName("Default") {
		return "The default controls could not be found", false
	}
	return "Controls: Default", true
}

func plugOpenLibrary(_ *Config, _ PluginInfo, _ map[string]interface{}) (string, bool) {
	openGameLibraryPanel()
	return "", true
}

// plugNotesPrompt is the notes plugin's "Add note": the notes_text variable
// when one is set (a config button), else the on-screen keyboard.
func plugNotesPrompt(_ *Config, info PluginInfo, vars map[string]interface{}) (string, bool) {
	dir := filepath.Join(pluginDir, info.Name)
	title := plugTitle(info.Name, info.Title)
	save := func(text string) string {
		out, err := plugNotesAdd(dir, text)
		if err != nil {
			return "Nothing saved: " + err.Error()
		}
		return out
	}
	if text := plugVar(vars, "notes_text"); text != "" {
		return save(text), false
	}
	tbxOpenPrompt("New note", "Something to remember", "", func(text string) {
		if strings.TrimSpace(text) == "" {
			ShowToast(title+": nothing saved", ToastKindInfo)
			return
		}
		out := save(text)
		plugRecordOutput(info.Name, out)
		ShowToast(title+": "+out, ToastKindSuccess)
	})
	return "", true
}
