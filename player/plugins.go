package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PluginInfo describes a loaded plugin module.
type PluginInfo struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Description string                 `json:"description"`
	Author      string                 `json:"author"`
	Entry       string                 `json:"entry"`
	Enabled     bool                   `json:"enabled"`
	Config      map[string]interface{} `json:"config"`
	// Title is the name the Plugins page shows; the folder name stays the id.
	Title string `json:"title,omitempty"`
	// Icon names a shared glyph (DrawHomeIcon) for the plugin's row.
	Icon string `json:"icon,omitempty"`
	// Actions are the buttons the Plugins page offers. A manifest without any
	// still loads; its actions can be run from a config button as before.
	Actions []plugAction `json:"actions,omitempty"`
}

// PluginContext is passed to plugin entry points.
type PluginContext struct {
	Config      *Config
	ShowToast   func(msg string, color ...interface{})
	ChangeScene func(name string)
	RenderText  func(renderer interface{}, config *Config, font interface{}, text string, color interface{}, x, y int32)
}

var (
	pluginDir = "plugins"
	plugins   = make(map[string]*PluginInfo)
)

// plugActionTimeout bounds one action. The runner used to wait forever, and a
// script that hung froze the whole interface with it.
const plugActionTimeout = 15 * time.Second

// LoadPlugins scans the plugins directory: every folder with a readable
// manifest is listed on the Plugins page (plugCatalog), and the ones that are
// switched on are loaded for plugin_action buttons. The map is rebuilt on every
// scan, so a plugin that was switched off, deleted or broken leaves the list
// straight away instead of after a restart. Safe mode skips the scan entirely
// so a broken plugin cannot block startup.
func LoadPlugins(config *Config) {
	if !safeModeAllows(SafeFeaturePlugins) {
		plugins = make(map[string]*PluginInfo)
		plugSetCatalog(nil)
		plugxInstall(config, nil, nil)
		return
	}
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			plugins = make(map[string]*PluginInfo)
			plugSetCatalog(nil)
			plugxInstall(config, nil, nil)
			return
		}
	}
	// The plugins that ship with JukaHub are copied in once (plugbundle.go),
	// so a fresh install has something to try.
	plugSeedBundled(config, pluginDir)
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return
	}
	loaded := make(map[string]*PluginInfo)
	catalog := make([]plugEntry, 0, len(entries))
	// The typed extensions (themes, ROM formats, input profiles, packs) of the
	// plugins that are switched on, made live after the scan (plugx.go).
	var extensions []plugxExtensions
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		manifestPath := filepath.Join(pluginDir, entry.Name(), "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var info PluginInfo
		if err := json.Unmarshal(data, &info); err != nil {
			catalog = append(catalog, plugEntry{
				Name:    entry.Name(),
				Title:   plugTitle(entry.Name(), ""),
				Problem: "plugin.json is not valid JSON",
			})
			continue
		}
		info.Name = entry.Name()
		ext := plugxParse(entry.Name(), filepath.Join(pluginDir, entry.Name()), data)
		row := plugEntryFor(config, info)
		plugxDecorate(&row, info, ext)
		catalog = append(catalog, row)
		if !row.Enabled {
			continue
		}
		extensions = append(extensions, ext)
		info.Enabled = true
		copied := info
		loaded[entry.Name()] = &copied
	}
	sort.SliceStable(catalog, func(i, j int) bool {
		return strings.ToLower(catalog[i].Title) < strings.ToLower(catalog[j].Title)
	})
	for i := range catalog {
		catalog[i].RunNote = plugRunNote(catalog[i])
	}
	plugxInstall(config, extensions, catalog)
	plugins = loaded
	plugSetCatalog(catalog)
}

// GetPlugins returns all loaded plugins.
func GetPlugins() map[string]*PluginInfo {
	return plugins
}

// ExecutePluginAction runs a plugin-defined action by name.
func ExecutePluginAction(pluginName, action string, vars map[string]interface{}) string {
	// A plugin name is a single directory below the plugins root; anything else
	// (separators, "..", absolute paths) is rejected before touching disk.
	if pluginName == "" || filepath.Base(pluginName) != pluginName || strings.ContainsAny(pluginName, `\/`) {
		return fmt.Sprintf("Plugin %q not found", pluginName)
	}
	plugin, ok := plugins[pluginName]
	if !ok {
		return fmt.Sprintf("Plugin %q not found", pluginName)
	}
	return plugExecute(*plugin, action, vars)
}

// plugExecute runs one action of a plugin already looked up. The Plugins page
// calls it from a goroutine with a copy of the manifest, so the run never reads
// the plugin map a rescan may be replacing.
func plugExecute(plugin PluginInfo, action string, vars map[string]interface{}) string {
	pluginName := plugin.Name
	pluginRoot := filepath.Join(pluginDir, pluginName)
	entryPath, err := resolveWithinRoot(pluginRoot, plugin.Entry)
	if err != nil {
		return fmt.Sprintf("Plugin %q has an unsafe entry path", pluginName)
	}
	info, statErr := os.Stat(entryPath)
	if statErr != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("Plugin entry %q not found", plugin.Entry)
	}
	if strings.HasSuffix(entryPath, ".py") || strings.HasSuffix(entryPath, ".sh") {
		// The child runs from the entry's directory, so a relative path from
		// the app directory would otherwise be resolved a second time there.
		entryPath, err = filepath.Abs(entryPath)
		if err != nil {
			return fmt.Sprintf("Plugin %q has an invalid entry path: %v", pluginName, err)
		}
		return runPluginScript(entryPath, action, vars)
	}
	return fmt.Sprintf("Plugin %q action %q executed (entry=%s)", pluginName, action, entryPath)
}

func runPluginScript(path, action string, vars map[string]interface{}) string {
	args := []string{path, action}
	for k, v := range vars {
		args = append(args, fmt.Sprintf("--%s=%v", k, v))
	}
	interpreter := plugInterpreter(path)
	ctx, cancel := context.WithTimeout(context.Background(), plugActionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, interpreter, args...)
	cmd.Dir = filepath.Dir(path)
	// A script that leaves a child holding the output pipe must not keep the
	// action open after its own exit or the timeout.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Plugin error: the action took longer than %d seconds and was stopped\n%s",
			int(plugActionTimeout/time.Second), string(out))
	}
	if err != nil {
		return fmt.Sprintf("Plugin error: %v\n%s", err, string(out))
	}
	return string(out)
}

// plugInterpreter is the program a plugin entry runs with: sh for .sh, python3
// for .py.
func plugInterpreter(entry string) string {
	if strings.HasSuffix(entry, ".py") {
		return "python3"
	}
	return "sh"
}

// InstallPlugin copies a plugin from source to the plugins directory.
func InstallPlugin(sourcePath, destName string) error {
	if destName == "" {
		destName = filepath.Base(sourcePath)
	}
	// Install into exactly one new directory directly below the plugins root.
	base, err := safeArchiveBaseName(destName)
	if err != nil {
		return err
	}
	destName = base
	destDir, err := safeJoinFile(pluginDir, destName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("plugin %q already installed", destName)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	destPath := filepath.Join(destDir, filepath.Base(sourcePath))
	return os.WriteFile(destPath, data, 0755)
}

// UninstallPlugin removes a plugin from the plugins directory.
func UninstallPlugin(name string) error {
	dir := filepath.Join(pluginDir, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("plugin %q not installed", name)
	}
	return os.RemoveAll(dir)
}
