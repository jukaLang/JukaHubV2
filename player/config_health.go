package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConfigValidator validates and sanitizes a Config before use.
type ConfigValidator struct {
	mu sync.Mutex
}

// NewConfigValidator creates a new validator.
func NewConfigValidator() *ConfigValidator {
	return &ConfigValidator{}
}

// Validate checks the config for required fields, valid values, and
// structural integrity. It applies defaults for missing optional fields.
func (cv *ConfigValidator) Validate(config *Config) error {
	cv.mu.Lock()
	defer cv.mu.Unlock()

	if config == nil {
		return fmt.Errorf("config is nil")
	}

	// Ensure top-level fields.
	if config.AppName == "" {
		config.AppName = "JukaHub"
	}
	if config.Version == "" {
		config.Version = "0.4.0"
	}
	if config.Width <= 0 {
		config.Width = 1280
	}
	if config.Height <= 0 {
		config.Height = 720
	}

	// Ensure Variables map is initialized.
	if config.Variables.Fonts == nil {
		config.Variables.Fonts = make(map[string]string)
	}
	if config.Variables.FontSizes == nil {
		config.Variables.FontSizes = make(map[string]int)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}

	// Validate scenes.
	if len(config.Scenes) == 0 {
		return fmt.Errorf("no scenes defined")
	}

	for i, scene := range config.Scenes {
		if scene.Name == "" {
			return fmt.Errorf("scene %d has empty name", i)
		}
		if scene.Layout != "home" && scene.Layout != "" {
			// Unknown layout; default to empty (legacy positioning).
			scene.Layout = ""
		}
		for j, elem := range scene.Elements {
			if elem.Type == "" {
				return fmt.Errorf("scene %q element %d has empty type", scene.Name, j)
			}
		}
	}

	return nil
}

// --- Atomic config persistence ---

// AtomicWrite writes data to filename atomically using a temp file + rename.
// It creates a backup of the existing file if one exists.
func AtomicWrite(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdirall %s: %w", dir, err)
	}

	// Create backup if file exists.
	if _, err := os.Stat(filename); err == nil {
		backup := filename + ".bak"
		if err := os.Rename(filename, backup); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
	}

	// Write to temp file in same directory (atomic rename requires same fs).
	tmp := filename + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write temp failed: %w", err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod temp failed: %w", err)
	}
	if err := os.Rename(tmp, filename); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename failed: %w", err)
	}
	return nil
}

// SaveConfig marshals config to JSON and writes it atomically.
func SaveConfig(filename string, config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return AtomicWrite(filename, data, 0644)
}

// LoadLastKnownGood attempts to load the config from the backup file if the
// primary file is corrupt or missing. It returns the loaded config and the
// path from which it was loaded.
func LoadLastKnownGood(filename string) (*Config, error) {
	cfg, err := loadConfig(filename)
	if err == nil {
		return cfg, nil
	}

	// Try backup.
	backup := filename + ".bak"
	Log().Warn("primary config failed, trying backup", "path", filename, "err", err)
	cfg, err = loadConfig(backup)
	if err == nil {
		return cfg, fmt.Errorf("loaded from backup: %s", backup)
	}
	return nil, fmt.Errorf("primary and backup config failed: %w", err)
}

// ConfigHealth tracks config load/save health for diagnostics.
type ConfigHealth struct {
	mu             sync.Mutex
	LastLoadTime   time.Time
	LastSaveTime   time.Time
	LastLoadPath   string
	LastSavePath   string
	LoadErrors     int
	SaveErrors     int
	BackupRestores int
}

var configHealth = &ConfigHealth{}

func (ch *ConfigHealth) RecordLoad(path string, err error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastLoadTime = time.Now()
	ch.LastLoadPath = path
	if err != nil {
		ch.LoadErrors++
	}
}

func (ch *ConfigHealth) RecordSave(path string, err error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastSaveTime = time.Now()
	ch.LastSavePath = path
	if err != nil {
		ch.SaveErrors++
	}
}

func (ch *ConfigHealth) RecordBackupRestore() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.BackupRestores++
}

func (ch *ConfigHealth) Snapshot() map[string]interface{} {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return map[string]interface{}{
		"last_load_time":  ch.LastLoadTime,
		"last_save_time":  ch.LastSaveTime,
		"last_load_path":  ch.LastLoadPath,
		"last_save_path":  ch.LastSavePath,
		"load_errors":     ch.LoadErrors,
		"save_errors":     ch.SaveErrors,
		"backup_restores": ch.BackupRestores,
	}
}
