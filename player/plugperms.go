package main

// plugperms.go — plugin permissions: what a plugin may do beyond its own
// folder, said before it runs.
//
// A manifest declares what its script needs:
//
//	"permissions": ["network", "files", "programs", "settings"]
//
//   - network:  the internet and the local network;
//   - files:    files outside the plugin's own folder (games, saves, …);
//   - programs: other programs and tools on the system;
//   - settings: JukaHub's look and controls (a plugin whose buttons switch
//     a theme, an experience pack or a control profile asks for it even when
//     its manifest forgets to).
//
// Switching a plugin on that asks for something not yet allowed opens a card
// listing it, with where the plugin comes from ("Comes with JukaHub" when the
// code that runs is JukaHub's own, "Added by you" otherwise), and the plugin
// only goes on with "Allow". The answer is kept per plugin
// (Custom "pluginPermissions"); a later version that asks for more asks
// again. A plugin that asks for something not allowed stays off — at a
// rescan too — so nothing a plugin declared runs without the owner's OK.
//
// What JukaHub enforces when a script runs (plugperms_linux.go):
//
//   - always: a small environment (PATH, the locale and time zone, the
//     system's library path and the Windows basics) instead of JukaHub's own,
//     JUKAHUB_PLUGIN, JUKAHUB_PLUGIN_DIR and JUKAHUB_PLUGIN_PERMISSIONS for the
//     script, HOME inside the plugin's folder unless "files" was allowed, its
//     folder as the working directory and the 15-second limit;
//   - on Linux run as root (the handheld): a script without "network" runs in
//     a network namespace of its own, with no way out; where the kernel has
//     no network namespaces the page says the network is not blocked.
//
// "files", "programs" and "settings" are declared and agreed to, not walled
// off: a script is still a program with JukaHub's rights. The Plugins page
// and docs/PLUGINS.md say exactly that.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// The permissions a manifest can ask for, in the order they are shown.
const (
	plugPermNetwork  = "network"
	plugPermFiles    = "files"
	plugPermPrograms = "programs"
	plugPermSettings = "settings"
)

var plugPermOrder = []string{plugPermNetwork, plugPermFiles, plugPermPrograms, plugPermSettings}

// plugPermsKey is the Custom key of the answers: {"<folder>": "network,files"}.
const plugPermsKey = "pluginPermissions"

// plugNetBlockFailed is set when the kernel refused a network namespace:
// the page then says the network is not blocked here.
var plugNetBlockFailed atomic.Bool

// plugPermLabel is a permission's title and one line on what it means.
func plugPermLabel(p string) (title, detail string) {
	switch p {
	case plugPermNetwork:
		return "The internet and your network", "Sends and receives data"
	case plugPermFiles:
		return "Files outside its own folder", "Reads and changes games, saves and other files"
	case plugPermPrograms:
		return "Other programs", "Starts programs and tools on the system"
	case plugPermSettings:
		return "JukaHub's look and controls", "Switches the theme, an experience pack or a control profile"
	}
	return p, ""
}

// plugSettingsActions are JukaHub's own actions that change the owner's
// settings: a plugin offering one asks for "settings".
var plugSettingsActions = map[string]bool{
	"@apply-theme": true, "@restore-theme": true, "@apply-pack": true, "@undo-pack": true,
	"@use-profile": true, "@default-profile": true,
}

// plugPermsDeclared is what a manifest asks for: the known names, once each,
// in plugPermOrder, with "settings" added for a settings action; unknown
// names are returned apart. Pure.
func plugPermsDeclared(info PluginInfo) (perms, unknown []string) {
	asked := map[string]bool{}
	for _, p := range info.Permissions {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "":
		case plugPermNetwork, plugPermFiles, plugPermPrograms, plugPermSettings:
			asked[p] = true
		default:
			unknown = append(unknown, p)
		}
	}
	for _, a := range info.Actions {
		if plugSettingsActions[strings.TrimSpace(a.ID)] {
			asked[plugPermSettings] = true
		}
	}
	for _, p := range plugPermOrder {
		if asked[p] {
			perms = append(perms, p)
		}
	}
	sort.Strings(unknown)
	return perms, unknown
}

// plugPermsGranted is what the owner allowed for a plugin folder.
func plugPermsGranted(config *Config, name string) []string {
	if config == nil || config.Variables.Custom == nil {
		return nil
	}
	m, ok := config.Variables.Custom[plugPermsKey].(map[string]interface{})
	if !ok {
		return nil
	}
	s, _ := m[name].(string)
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// plugPermsMissing is what a plugin asks for and was not allowed. Pure.
func plugPermsMissing(declared, granted []string) []string {
	have := map[string]bool{}
	for _, p := range granted {
		have[p] = true
	}
	var out []string
	for _, p := range declared {
		if !have[p] {
			out = append(out, p)
		}
	}
	return out
}

// plugPermsGrant stores the owner's OK for perms. The nested map is replaced,
// not edited: the saver may be encoding the old one. Main thread.
func plugPermsGrant(config *Config, name string, perms []string) {
	if config == nil || name == "" {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	next := map[string]interface{}{}
	if old, ok := config.Variables.Custom[plugPermsKey].(map[string]interface{}); ok {
		for k, v := range old {
			next[k] = v
		}
	}
	next[name] = strings.Join(perms, ",")
	config.Variables.Custom[plugPermsKey] = next
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// plugHasPerm reports whether perms holds p. Pure.
func plugHasPerm(perms []string, p string) bool {
	for _, x := range perms {
		if x == p {
			return true
		}
	}
	return false
}

// plugFromJukaHub reports a plugin whose running code is JukaHub's own: a
// bundled plugin whose script is still the one shipped, or that runs its Go
// version. Its permissions are JukaHub's and need no question.
func plugFromJukaHub(info PluginInfo) bool {
	return plugPreferNative(info, filepath.Join(pluginDir, info.Name))
}

// plugPermsApply fills a scanned plugin's permission fields and keeps it off
// while it asks for something not allowed. Main thread (LoadPlugins).
func plugPermsApply(config *Config, info *PluginInfo, row *plugEntry) {
	declared, unknown := plugPermsDeclared(*info)
	row.Permissions, row.UnknownPerms = declared, unknown
	row.Bundled = plugFromJukaHub(*info)
	if row.Bundled {
		row.Granted = declared
	} else {
		row.Granted = plugPermsGranted(config, info.Name)
	}
	row.Missing = plugPermsMissing(declared, row.Granted)
	if len(row.Missing) > 0 {
		row.Enabled = false
	}
	info.Granted = row.Granted
}

// plugPermsCardLines are the Plugins page's lines about a plugin's
// permissions.
func plugPermsCardLines(e plugEntry) []string {
	var out []string
	if e.Bundled {
		out = append(out, "Comes with JukaHub")
	} else {
		out = append(out, "Added by you: JukaHub has not checked this plugin")
	}
	if len(e.Permissions) == 0 {
		out = append(out, "Asks for nothing beyond its own folder")
	}
	for _, p := range e.Permissions {
		title, _ := plugPermLabel(p)
		state := "allowed"
		if !plugHasPerm(e.Granted, p) {
			state = "asks when switched on"
		}
		out = append(out, title+" · "+state)
	}
	if !plugHasPerm(e.Granted, plugPermNetwork) {
		if plugNetworkBlockable() {
			out = append(out, "No network: blocked while its script runs")
		} else {
			out = append(out, "No network asked for (not blocked on this system)")
		}
	}
	if len(e.UnknownPerms) > 0 {
		out = append(out, "Unknown permissions ignored: "+strings.Join(e.UnknownPerms, ", "))
	}
	return out
}

// plugAskEnable switches a plugin on, first asking for what it needs when
// that was not allowed yet; after runs once it is on (the toast and the
// onEnable action). Main thread.
func plugAskEnable(c *Config, name string, after func()) {
	e, ok := plugEntryNamed(name)
	if !ok || c == nil {
		return
	}
	if len(e.Missing) == 0 {
		plugSetEnabled(c, name, true)
		if after != nil {
			after()
		}
		return
	}
	var b strings.Builder
	if e.Bundled {
		b.WriteString("Comes with JukaHub.\n\n")
	} else {
		b.WriteString("Added by you: JukaHub has not checked this plugin. Only allow it if you trust where it came from.\n\n")
	}
	b.WriteString("## It asks for\n")
	for _, p := range e.Missing {
		title, detail := plugPermLabel(p)
		b.WriteString("• " + title + " — " + detail + "\n")
	}
	b.WriteString("\nYou can switch it off at any time. Without \"network\", its script cannot reach the network on the handheld; files, programs and settings are its promise, not a wall.")
	declared := append([]string(nil), e.Permissions...)
	PlayActivateSound()
	tbxOpenSheet(c, tbxSheetSpec{
		title: "Allow " + e.Title + "?", subtitle: e.Name + "/", icon: "shield", body: b.String(),
		buttons: []tbxSheetButton{
			{label: "Allow and switch on", primary: true, run: func() {
				plugPermsGrant(c, name, declared)
				plugSetEnabled(c, name, true)
				if after != nil {
					after()
				}
			}},
			{label: "Cancel"},
		},
		focus: 1,
	})
}

// plugScriptEnv is a script's environment: the system's basics, never the
// rest of JukaHub's (keys and tokens some features keep there), and the
// plugin's own variables. Pure apart from os.Environ.
func plugScriptEnv(info PluginInfo, pluginRoot string) []string {
	keep := map[string]bool{
		"PATH": true, "LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "TMPDIR": true, "TEMP": true, "TMP": true, "LD_LIBRARY_PATH": true,
		"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
		"PROGRAMDATA": true, "PROGRAMFILES": true, "APPDATA": true, "LOCALAPPDATA": true, "USERPROFILE": true, "HOME": true,
	}
	files := plugHasPerm(info.Granted, plugPermFiles)
	var env []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		up := strings.ToUpper(k)
		if up == "HOME" && !files {
			continue
		}
		if keep[up] || strings.HasPrefix(up, "LC_") {
			env = append(env, kv)
		}
	}
	root, err := filepath.Abs(pluginRoot)
	if err != nil {
		root = pluginRoot
	}
	if !files {
		env = append(env, "HOME="+root)
	}
	return append(env,
		"JUKAHUB_PLUGIN="+info.Name,
		"JUKAHUB_PLUGIN_DIR="+root,
		"JUKAHUB_PLUGIN_PERMISSIONS="+strings.Join(info.Granted, ","),
	)
}

// plugPermsLine is the Plugins page's one line on a plugin's trust: where it
// comes from and what it may do. Cached per scan: the card draws it every
// frame.
func plugPermsLine(e plugEntry) string {
	c := &plugPermsLineCache
	if c.name == e.Name && c.gen == plugCatalogGen && c.blocked == plugNetBlockFailed.Load() {
		return c.line
	}
	parts := []string{"Added by you"}
	if e.Bundled {
		parts[0] = "Comes with JukaHub"
	}
	switch {
	case len(e.Permissions) == 0:
		parts = append(parts, "nothing beyond its own folder")
	case len(e.Missing) > 0:
		parts = append(parts, "asks for "+strings.Join(e.Permissions, ", "))
	default:
		parts = append(parts, "allowed: "+strings.Join(e.Permissions, ", "))
	}
	if !plugHasPerm(e.Granted, plugPermNetwork) && !e.Bundled {
		if plugNetworkBlockable() {
			parts = append(parts, "no network")
		} else {
			parts = append(parts, "network not blocked here")
		}
	}
	c.name, c.gen, c.blocked = e.Name, plugCatalogGen, plugNetBlockFailed.Load()
	c.line = strings.Join(parts, " · ")
	return c.line
}

// plugPermsLineCache is plugPermsLine's last answer. Main thread.
var plugPermsLineCache struct {
	name    string
	gen     int
	blocked bool
	line    string
}
