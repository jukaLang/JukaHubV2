package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// LoadPlugins scans the plugins directory and loads enabled plugins.
func LoadPlugins(config *Config) {
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		os.MkdirAll(pluginDir, 0755)
		return
	}
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			manifestPath := filepath.Join(pluginDir, entry.Name(), "plugin.json")
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var info PluginInfo
			if err := json.Unmarshal(data, &info); err != nil {
				continue
			}
			info.Name = entry.Name()
			if !info.Enabled {
				continue
			}
			plugins[entry.Name()] = &info
		}
	}
}

// GetPlugins returns all loaded plugins.
func GetPlugins() map[string]*PluginInfo {
	return plugins
}

// ExecutePluginAction runs a plugin-defined action by name.
func ExecutePluginAction(pluginName, action string, vars map[string]interface{}) string {
	plugin, ok := plugins[pluginName]
	if !ok {
		return fmt.Sprintf("Plugin %q not found", pluginName)
	}
	entryPath := filepath.Join(pluginDir, pluginName, plugin.Entry)
	if _, err := os.Stat(entryPath); err != nil {
		return fmt.Sprintf("Plugin entry %q not found", entryPath)
	}
	if strings.HasSuffix(entryPath, ".py") || strings.HasSuffix(entryPath, ".sh") {
		return runPluginScript(entryPath, action, vars)
	}
	return fmt.Sprintf("Plugin %q action %q executed (entry=%s)", pluginName, action, entryPath)
}

func runPluginScript(path, action string, vars map[string]interface{}) string {
	args := []string{path, action}
	for k, v := range vars {
		args = append(args, fmt.Sprintf("--%s=%v", k, v))
	}
	var cmd *exec.Cmd
	if strings.HasSuffix(path, ".py") {
		cmd = exec.Command("python3", args...)
	} else {
		cmd = exec.Command("sh", args...)
	}
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Plugin error: %v\n%s", err, string(out))
	}
	return string(out)
}

// InstallPlugin copies a plugin from source to the plugins directory.
func InstallPlugin(sourcePath, destName string) error {
	if destName == "" {
		destName = filepath.Base(sourcePath)
	}
	destDir := filepath.Join(pluginDir, destName)
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
