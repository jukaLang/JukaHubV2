package main

// plugcatalog.go — what the Plugins page knows about each plugin, the on/off
// switch the user controls, and running an action without freezing the UI.
//
// LoadPlugins (plugins.go) used to load only the manifests that said
// "enabled": true and drop the rest, so a plugin could not be switched on from
// the device at all, and the list was a read-only text log behind a Refresh
// button. Now every plugin folder is listed; the manifest's "enabled" is only
// the default, and the user's own choice — stored in the user config, never
// written into the plugin's files — wins. Actions run on a goroutine with a
// timeout and report back through the page and a toast.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// plugStateKey is the custom variable holding the user's on/off choices:
// {"<folder>": true|false}. A plugin missing from it follows its manifest.
const plugStateKey = "pluginState"

// plugAction is one button a manifest offers: the action name passed to the
// script and the label the Plugins page shows.
type plugAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// plugEntry is one plugin as the Plugins page lists it, loaded or not.
type plugEntry struct {
	Name        string // folder name, the id buttons and the state use
	Title       string
	Version     string
	Author      string
	Description string
	Entry       string
	Icon        string
	Actions     []plugAction
	Default     bool // the manifest's "enabled"
	Enabled     bool // what is in force: the user's choice, else Default
	// Problem says why the plugin cannot run here (no shell on this system,
	// a missing entry file, a broken manifest); empty when it can.
	Problem string
	// RunNote is why its buttons cannot be pressed right now (Problem, or
	// switched off, or no buttons), "" when they can. Built with the scan so
	// the page never builds the sentence per frame.
	RunNote string
	// Provides summarises what the plugin adds to JukaHub itself — themes,
	// ROM formats, input profiles, experience packs (plugx.go) — and is ""
	// for a plugin that only has actions.
	Provides string
	// ExtLines are the card's lines about each of those, and about any entry
	// that was skipped and why. Built with the scan.
	ExtLines []string
}

// plugRunNote is the RunNote for e.
func plugRunNote(e plugEntry) string {
	switch {
	case e.Problem != "":
		return "Can't run here: " + e.Problem
	case !e.Enabled:
		return "Switch " + e.Title + " on to use its actions"
	case len(e.Actions) == 0 && e.Provides != "":
		return "Adds " + e.Provides + "; it has nothing to run"
	case len(e.Actions) == 0:
		return "This plugin has no buttons; a config button can still run it"
	}
	return ""
}

// plugCatalog is the last scan. It is written and read on the main thread only
// (LoadPlugins runs on a press or a scene entry, the page reads it to draw).
var plugCatalog []plugEntry

func plugSetCatalog(entries []plugEntry) { plugCatalog = entries }

// plugCatalogEntries returns the last scan.
func plugCatalogEntries() []plugEntry { return plugCatalog }

// plugTitle is the name a plugin is shown under: the manifest's title, else
// its folder name with a capital letter.
func plugTitle(folder, title string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if folder == "" {
		return "Plugin"
	}
	return strings.ToUpper(folder[:1]) + folder[1:]
}

// plugEntryFor builds the page's row for a parsed manifest.
func plugEntryFor(config *Config, info PluginInfo) plugEntry {
	row := plugEntry{
		Name:        info.Name,
		Title:       plugTitle(info.Name, info.Title),
		Version:     strings.TrimSpace(info.Version),
		Author:      strings.TrimSpace(info.Author),
		Description: strings.TrimSpace(info.Description),
		Entry:       info.Entry,
		Icon:        strings.TrimSpace(info.Icon),
		Actions:     plugCleanActions(info.Actions),
		Default:     info.Enabled,
	}
	row.Enabled = row.Default
	if on, set := plugUserChoice(config, info.Name); set {
		row.Enabled = on
	}
	row.Problem = plugProblem(info)
	return row
}

// plugCleanActions drops actions without an id and labels the rest.
func plugCleanActions(in []plugAction) []plugAction {
	out := make([]plugAction, 0, len(in))
	for _, a := range in {
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			continue
		}
		if strings.TrimSpace(a.Label) == "" {
			a.Label = plugTitle(a.ID, "")
		}
		out = append(out, a)
	}
	return out
}

// plugLookPath is exec.LookPath, replaceable by tests.
var plugLookPath = exec.LookPath

// plugProblem reports what stops a plugin from running on this system: an
// entry that is not a script the runner knows, or the interpreter it needs
// missing from PATH (Windows has no sh unless Git for Windows or similar is
// installed). The entry file itself is checked when the action runs.
func plugProblem(info PluginInfo) string {
	entry := strings.TrimSpace(info.Entry)
	if entry == "" {
		return "plugin.json names no entry script"
	}
	ext := strings.ToLower(filepath.Ext(entry))
	if ext != ".sh" && ext != ".py" {
		return "only .sh and .py entries can run"
	}
	bin := plugInterpreter(entry)
	if _, err := plugLookPath(bin); err != nil {
		if bin == "sh" {
			return "needs sh, which this system does not have (Git for Windows installs one)"
		}
		return "needs Python 3, which this system does not have"
	}
	return ""
}

// plugUserChoice reads the user's switch for one plugin.
func plugUserChoice(config *Config, name string) (on bool, set bool) {
	if config == nil || config.Variables.Custom == nil {
		return false, false
	}
	m, ok := config.Variables.Custom[plugStateKey].(map[string]interface{})
	if !ok {
		return false, false
	}
	v, ok := m[name]
	if !ok {
		return false, false
	}
	return coerceBool(v), true
}

// plugSetEnabled stores the switch for one plugin, saves it with the rest of
// the user settings and rescans, so a plugin switched on can be used at once.
// The nested map is replaced rather than edited in place: the saver may be
// encoding the previous one on another goroutine.
func plugSetEnabled(config *Config, name string, on bool) {
	if config == nil || name == "" {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	next := map[string]interface{}{}
	if old, ok := config.Variables.Custom[plugStateKey].(map[string]interface{}); ok {
		for k, v := range old {
			next[k] = v
		}
	}
	next[name] = on
	config.Variables.Custom[plugStateKey] = next
	saveUserConfigDebounced(userConfigFromVariables(config))
	LoadPlugins(config)
}

// plugEntryNamed finds one plugin in the last scan.
func plugEntryNamed(name string) (plugEntry, bool) {
	for _, e := range plugCatalog {
		if e.Name == name {
			return e, true
		}
	}
	return plugEntry{}, false
}

// plugEnabledCount reports how many listed plugins are switched on.
func plugEnabledCount() (on, total int) {
	for _, e := range plugCatalog {
		total++
		if e.Enabled {
			on++
		}
	}
	return on, total
}

// ── Running actions ───────────────────────────────────────────────────────

// plugRunState is what the page shows about actions: which plugin is busy and
// the last thing each one printed. A goroutine writes it, the render thread
// reads it, so it sits behind a mutex.
var plugRunState struct {
	mu      sync.Mutex
	running map[string]string // plugin → action in progress
	output  map[string]string // plugin → last output, trimmed
	at      map[string]time.Time
}

// plugRunner runs one action; tests replace it.
var plugRunner = plugExecute

// plugRunAsync starts an action on a goroutine. It refuses a second action for
// a plugin that is still busy, and the result lands on the page and in a toast.
// info is the loaded manifest and vars the custom variables, both copied on
// the main thread so the goroutine shares nothing with it.
func plugRunAsync(info PluginInfo, action, label string, vars map[string]interface{}) bool {
	name := info.Name
	plugRunState.mu.Lock()
	if plugRunState.running == nil {
		plugRunState.running = map[string]string{}
		plugRunState.output = map[string]string{}
		plugRunState.at = map[string]time.Time{}
	}
	if _, busy := plugRunState.running[name]; busy {
		plugRunState.mu.Unlock()
		return false
	}
	plugRunState.running[name] = action
	plugRunState.mu.Unlock()

	go func() {
		out := strings.TrimSpace(plugRunner(info, action, vars))
		if out == "" {
			out = "Done (the action printed nothing)."
		}
		plugRunState.mu.Lock()
		delete(plugRunState.running, name)
		plugRunState.output[name] = out
		plugRunState.at[name] = time.Now()
		plugRunState.mu.Unlock()
		kind := ToastKindInfo
		if strings.HasPrefix(out, "Plugin error:") {
			kind = ToastKindWarning
		}
		first := out
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		ShowToast(label+": "+first, kind)
	}()
	return true
}

// plugRunTrigger is a config button's plugin_action. It used to run the script
// on the event thread, so the whole interface froze until the script ended; it
// now starts the same background run as the Plugins page, and the result
// arrives as a toast. vars is the custom-variable snapshot, taken by the caller
// on the main thread.
func plugRunTrigger(name, action string, vars map[string]interface{}) {
	info, ok := GetPlugins()[name]
	if !ok || info == nil {
		ShowToast("Plugin "+name+" is off or not installed (Settings › Plugins)", ToastKindInfo)
		return
	}
	title := plugTitle(info.Name, info.Title)
	if !plugRunAsync(*info, action, title, vars) {
		ShowToast(title+" is still running", ToastKindInfo)
	}
}

// plugRunStatus reports whether a plugin is busy and its last output.
func plugRunStatus(name string) (running string, output string, at time.Time) {
	plugRunState.mu.Lock()
	defer plugRunState.mu.Unlock()
	return plugRunState.running[name], plugRunState.output[name], plugRunState.at[name]
}

// plugWaitIdle blocks until no action is running or ctx ends. Tests use it.
func plugWaitIdle(ctx context.Context) bool {
	for {
		plugRunState.mu.Lock()
		busy := len(plugRunState.running) > 0
		plugRunState.mu.Unlock()
		if !busy {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}
