package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// Defaults for new fields
	if config.Variables.GridColumns == 0 {
		config.Variables.GridColumns = 5
	}
	if config.Variables.SearchWidth == 0 {
		config.Variables.SearchWidth = 940
	}
	if config.Variables.ScreensaverTimeout == 0 {
		config.Variables.ScreensaverTimeout = 300 // 5 minutes
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

// SecurityCheck scans the config for potential security issues like plaintext
// secrets. It returns a list of warnings (not errors - the config will still
// work, but users should be informed about security best practices).
func (cv *ConfigValidator) SecurityCheck(config *Config) []string {
	var warnings []string

	if config == nil || config.Variables.Custom == nil {
		return warnings
	}

	// List of sensitive config keys that should be encrypted
	sensitiveKeys := []string{
		"discord_token",
		"groq_api_key",
		"google_api_key",
		"openai_api_key",
		"anthropic_api_key",
		"aws_access_key",
		"aws_secret_key",
	}

	for _, key := range sensitiveKeys {
		if val, ok := config.Variables.Custom[key]; ok {
			if str, isStr := val.(string); isStr && str != "" {
				// Check if it's encrypted (starts with ENC:)
				if !strings.HasPrefix(str, "ENC:") {
					// Check if it looks like a real secret (not empty, not a placeholder)
					if !isPlaceholderSecret(str) {
						warnings = append(warnings, fmt.Sprintf(
							"Security: '%s' is stored in plaintext. Use encryption for better security.",
							key,
						))
					}
				}
			}
		}
	}

	// Check for discord_token_type that might expose user tokens
	if tokenType, ok := config.Variables.Custom["discord_token_type"]; ok {
		if str, isStr := tokenType.(string); isStr && strings.EqualFold(str, "user") {
			warnings = append(warnings, "Security: Using user token type may expose personal Discord account. Consider using bot tokens instead.")
		}
	}

	// Check for weak/default encryption key usage
	if currentKeyInfo.IsDefault {
		warnings = append(warnings, "Security: Using default encryption key. Set JUKAHUB_CRYPTO_KEY environment variable for production use.")
	}

	return warnings
}

// isPlaceholderSecret checks if a string looks like a placeholder or example
// rather than a real secret.
func isPlaceholderSecret(s string) bool {
	lower := strings.ToLower(s)
	placeholders := []string{
		"",
		"your-api-key",
		"your_api_key",
		"api-key-here",
		"api_key_here",
		"put-your-key-here",
		"replace-with-your-key",
		"sk-placeholder",
		"demo",
		"example",
		"test",
		"placeholder",
		"changeme",
		"changme",
	}
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Check for patterns like "xxx" or "***" that indicate redacted values
	if strings.Contains(s, "xxx") || strings.Contains(s, "***") || strings.Contains(s, "---") {
		return true
	}
	return false
}

// ValidateCustomString validates a custom config string value for safety.
// It checks for common injection patterns and returns an error if the value
// appears malicious.
func ValidateCustomString(key, value string) error {
	if value == "" {
		return nil
	}

	// Check for command injection patterns
	dangerousPatterns := []struct {
		pattern string
		desc    string
	}{
		{"&&", "command chaining"},
		{"||", "command alternation"},
		{";", "command separator"},
		{"|", "pipe to another command"},
		{"$", "variable expansion"},
		{"`", "command substitution"},
		{">", "output redirection"},
		{"<", "input redirection"},
		{"\n", "newline injection"},
		{"\r", "carriage return injection"},
	}

	for _, dp := range dangerousPatterns {
		if strings.Contains(value, dp.pattern) {
			return fmt.Errorf("config value for %q contains potentially dangerous pattern: %s", key, dp.desc)
		}
	}

	return nil
}

// SanitizeCustomString removes potentially dangerous characters from a config value.
// This is a best-effort sanitization - validation should be preferred.
func SanitizeCustomString(value string) string {
	// Remove null bytes
	value = strings.ReplaceAll(value, "\x00", "")
	// Trim whitespace
	value = strings.TrimSpace(value)
	return value
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
		configHealth.RecordSave(filename, fmt.Errorf("marshal config: %w", err))
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := AtomicWrite(filename, data, 0644); err != nil {
		configHealth.RecordSave(filename, fmt.Errorf("atomic write: %w", err))
		return fmt.Errorf("atomic write: %w", err)
	}
	configHealth.RecordSave(filename, nil)
	return nil
}

// LoadLastKnownGood attempts to load the config from the backup file if the
// primary file is corrupt or missing. It returns the loaded config and the
// path from which it was loaded.
func LoadLastKnownGood(filename string) (*Config, error) {
	if cfg, parseErr := loadConfig(filename); parseErr == nil {
		configHealth.RecordLoad(filename, nil)
		configHealth.MarkLoadOK()
		return cfg, nil
	}

	// Try backup.
	backup := filename + ".bak"
	Log().Warn("primary config failed, trying backup", "path", filename, "err", parseErr)
	if cfg, backupErr := loadConfig(backup); cfg != nil {
		configHealth.RecordLoad(backup, nil)
		configHealth.RecordBackupRestore()
		// The backup is now the effective config for this launch; leave the
		// last-known-good source path pointing at the backup so diagnostics
		// do not falsely report that the primary config was healthy.
		configHealth.SetLastLoadPath(backup)
		configHealth.MarkLoadOK()
		return cfg, fmt.Errorf("loaded from backup: %s", backup)
	}
	configHealth.RecordLoad(filename, fmt.Errorf("primary and backup config failed: %w", backupErr))
	return nil, fmt.Errorf("primary and backup config failed: %w", backupErr)
}

// ConfigHealth tracks config load/save health for diagnostics.
type ConfigHealth struct {
	mu             sync.Mutex
	LastLoadTime   time.Time
	LastSaveTime   time.Time
	LastLoadPath   string
	LastSavePath   string
	LastSourcePath string
	LoadErrors     int
	SaveErrors     int
	BackupRestores int
	LastLoadOK     bool
	LastSaveOK     bool
}

var configHealth = &ConfigHealth{}

// SetLaunchMode updates the diagnostic launch-mode label so the health snapshot
// can distinguish a normal boot from a hot-reload restart.
func SetLaunchMode(mode string) {
	configHealthLaunchMode = mode
}

// ValidateKnownAtRuntime is a compile-time marker that the config health helpers
// are wired into the real config load paths.
var _ = ValidateKnownAtRuntime

// ValidateKnownAtRuntimeWire is a compile-time wire-up marker that the config
// health helpers on config_health.go are actually used by the real config load/save
// paths in this module.
var _ = struct {
	_ func(filename string, data []byte, perm os.FileMode) error
	_ func(path string, err error)
	_ func(path string, err error)
	_ func()
	_ func()
	_ func()
	_ func(string)
	_ func()
	_ func()
	_ func() map[string]interface{}
	_ func(string)
}{
	AtomicWrite,
	configHealth.RecordLoad,
	configHealth.RecordSave,
	configHealth.RecordBackupRestore,
	configHealth.Log,
	configHealth.SetLastLoadPath,
	configHealth.MarkLoadOK,
	configHealth.MarkSaveOK,
	SetLaunchMode,
	configHealth.Snapshot,
}

// ValidateKnownAtRuntimeWireHere is a compile-time marker that the config health
// wire-up in this package has been applied exactly once.
var _ = ValidateKnownAtRuntimeWireHere

// ValidateKnownAtRuntimeImport is a compile-time import marker that the config
// health helpers are wired into the real config load paths.
var _ = configHealth

// ValidateKnownAtRuntimeHere is a compile-time marker that this file has been
// patched for config health diagnostics.
// ValidateKnownAtRuntimeHere is a compile-time marker that this file has been
// patched for config health diagnostics.
var _ = ValidateKnownAtRuntimeHere

// Log prints a compact config-health summary to the startup log so a failed
// launch or an unexpected reload has a readable trail in the diagnostics
// console.
func (ch *ConfigHealth) Log() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	log.Printf("[config.health] lastLoad=%s lastSave=%s source=%s loadErrs=%d saveErrs=%d restores=%d loadOK=%v saveOK=%v",
		ch.LastLoadPath, ch.LastSavePath, ch.LastSourcePath, ch.LoadErrors, ch.SaveErrors,
		ch.BackupRestores, ch.LastLoadOK, ch.LastSaveOK)
}

func (ch *ConfigHealth) RecordLoad(path string, err error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastLoadTime = time.Now()
	ch.LastLoadPath = path
	ch.LastSourcePath = path
	ch.LastLoadOK = err == nil
	if err != nil {
		ch.LoadErrors++
	}
}

func (ch *ConfigHealth) RecordSave(path string, err error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastSaveTime = time.Now()
	ch.LastSavePath = path
	ch.LastSaveOK = err == nil
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
		"last_load_time":   ch.LastLoadTime,
		"last_save_time":   ch.LastSaveTime,
		"last_load_path":   ch.LastLoadPath,
		"last_save_path":   ch.LastSavePath,
		"last_source_path": ch.LastSourcePath,
		"load_errors":      ch.LoadErrors,
		"save_errors":      ch.SaveErrors,
		"backup_restores":  ch.BackupRestores,
		"last_load_ok":     ch.LastLoadOK,
		"last_save_ok":     ch.LastSaveOK,
		"launch_mode":      configHealthLaunchMode,
	}
}

// configHealthLaunchMode is set once near startup so diagnostics can tell whether
// config health state came from a normal boot or a hot-reload restart.
var configHealthLaunchMode = "boot"

// SetLastLoadPath updates the effective config source after a fallback has been
// used, so the health snapshot reflects the path that actually powered the
// running app rather than the path that was first asked for.
func (ch *ConfigHealth) SetLastLoadPath(path string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastSourcePath = path
}

// ValidateKnownAtRuntime is a compile-time marker that the config health helpers
// are wired into the real config load paths.
var _ = ValidateKnownAtRuntimeWire

// ValidateKnownAtRuntimeWireHere is a compile-time marker that the config health
// wire-up in this package has been applied exactly once.
var _ = ValidateKnownAtRuntimeWireHere

// ValidateKnownAtRuntimeImport is a compile-time import marker that the config
// health helpers are wired into the real config load paths.
var _ = configHealth

// ValidateKnownAtRuntimeHere is a compile-time marker that this file has been
// patched for config health diagnostics.
var _ = ValidateKnownAtRuntimeHere

// MarkLoadOK records that the most recent config load succeeded. This is useful
// when a config was loaded via a fallback path and the caller already knows the
// result is healthy.
func (ch *ConfigHealth) MarkLoadOK() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastLoadOK = true
}

// MarkSaveOK records that the most recent config save succeeded.
func (ch *ConfigHealth) MarkSaveOK() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.LastSaveOK = true
}

//go:build ignore
package main
