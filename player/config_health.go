package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if config.AppName == "" || config.AppName == "JukaHub 0.4.0" {
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
		// 25 seconds: long enough to read a screen without the saver butting
		// in, short enough that a handheld put down is asleep quickly.
		config.Variables.ScreensaverTimeout = 25
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
	// An empty value is a placeholder in the obvious sense, and it must be
	// handled before the substring loop: `strings.Contains(s, "")` is true for
	// every string, so keeping "" in the list made every value look like a
	// placeholder and silenced the plaintext-secret warning completely.
	if strings.TrimSpace(s) == "" {
		return true
	}
	lower := strings.ToLower(s)
	placeholders := []string{
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

// --- Atomic config persistence ---

// AtomicWrite writes data to filename atomically using a temp file + fsync +
// rename, and keeps the previous content as filename.bak.
//
// The live file is *copied* to .bak rather than renamed there. Renaming it away
// first leaves the path missing until the new file lands, so a power cut or a
// full card between the two steps used to lose the config outright — which on a
// handheld is the normal way the machine stops, not an edge case.
func AtomicWrite(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdirall %s: %w", dir, err)
	}

	// Best-effort backup of the previous content. A failed backup is logged but
	// must not block the save: a read-only .bak is not a reason to lose the
	// change the user just made.
	if previous, err := os.ReadFile(filename); err == nil {
		if err := writeFileSynced(filename+".bak", previous, perm); err != nil {
			Log().Warn("config backup could not be written", "path", filename+".bak", "err", err)
		}
	}

	// Write to a temp file in the same directory: rename is only atomic within
	// one filesystem.
	tmp := filename + ".tmp"
	if err := writeFileSynced(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp failed: %w", err)
	}
	if err := os.Rename(tmp, filename); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename failed: %w", err)
	}
	syncDirectory(dir)
	return nil
}

// readJSONWithBackup parses a JSON store and, when the live file cannot be read
// or parsed, falls back to the .bak that AtomicWrite leaves beside it. It
// reports whether the backup was the one that answered, so the caller can log a
// recovery instead of pretending nothing was lost.
//
// The primary file is tried first and only a real parse failure walks to the
// backup: a store that is simply absent is a first run, not damage, and must not
// resurrect a stale backup from some earlier card.
func readJSONWithBackup(path string, v any) (recovered bool, err error) {
	failure := error(nil)
	if data, readErr := os.ReadFile(path); readErr != nil {
		// Absent is a first run on a fresh card, not damage: walking to the
		// .bak there would resurrect a list the user started over from.
		if os.IsNotExist(readErr) {
			return false, readErr
		}
		failure = readErr
	} else if jsonErr := json.Unmarshal(data, v); jsonErr != nil {
		failure = fmt.Errorf("parse %s: %w", path, jsonErr)
	} else {
		return false, nil
	}
	if backup, backupErr := os.ReadFile(path + ".bak"); backupErr == nil {
		if jsonErr := json.Unmarshal(backup, v); jsonErr == nil {
			Log().Warn("store is damaged; recovered the last backup", "path", path, "err", failure)
			return true, nil
		}
	}
	// Neither copy parses, so the caller is about to start from nothing. Say so
	// here, once, for every store: a silent reset is how a reader concludes the
	// app deleted their list.
	Log().Warn("store is unreadable and has no usable backup; starting empty", "path", path, "err", failure)
	return false, failure
}

// writeFileSynced writes a file and fsyncs it before closing. Without the
// fsync, a rename can land while the data blocks are still in the page cache;
// a power cut then leaves the destination present but empty.
func writeFileSynced(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDirectory flushes the directory entry after the rename so the new name is
// durable too. Best-effort: Windows cannot fsync a directory handle and some
// filesystems refuse it; both keep the rename in their metadata journal.
func syncDirectory(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// SaveConfig marshals config to JSON and writes it atomically.
func SaveConfig(filename string, config *Config) error {
	// Same rule as saveConfig: a generated Tools page layout must not reach the
	// file (menutiles.go).
	defer menuTilesDetachForSave(config)()
	config.SchemaVersion = ConfigSchemaVersion
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

// configRecoveryNotice is set when the primary config had to be recovered from
// its backup. It is shown once the render loop is up (main.go) rather than
// during load, which runs before fonts and audio exist.
var configRecoveryNotice string

// takeConfigRecoveryNotice returns the pending recovery notice once.
func takeConfigRecoveryNotice() string {
	notice := configRecoveryNotice
	configRecoveryNotice = ""
	return notice
}

// configRecoveryMessage words the recovery so a reader knows what happened to
// their settings and where the damaged file went.
func configRecoveryMessage(aside string) string {
	if aside == "" {
		return "Settings were restored from the backup"
	}
	return "Settings were restored from the backup; the damaged file was kept as " + filepath.Base(aside)
}

// configCorruptKeep is how many damaged files are kept: enough to compare a
// repeat with the first, few enough that a card cannot fill with them.
const configCorruptKeep = 5

// preserveCorruptConfig renames an unparsable primary config aside so the next
// save cannot overwrite it. Returns the path it was kept at, or "" when there
// was nothing to keep (a missing primary is a first run, not damage).
func preserveCorruptConfig(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	aside := corruptAsidePath(path)
	if err := os.Rename(path, aside); err != nil {
		Log().Warn("damaged config could not be kept aside", "path", path, "err", err)
		return ""
	}
	pruneCorruptCopies(path, configCorruptKeep)
	return aside
}

// corruptAsidePath names the copy kept for inspection. Two damaged starts can
// land in the same second, so an existing name gets a numeric suffix instead of
// being replaced (os.Rename would lose the first file's evidence).
func corruptAsidePath(path string) string {
	base := path + ".corrupt-" + time.Now().Format("20060102-150405.000000000")
	aside := base
	for i := 2; ; i++ {
		if _, err := os.Stat(aside); os.IsNotExist(err) {
			return aside
		}
		aside = fmt.Sprintf("%s-%02d", base, i)
	}
}

// pruneCorruptCopies keeps the newest keep damaged copies of path. The names
// carry the timestamp (and a suffix for a same-second copy), so sorting them
// oldest-first does not depend on file times, which a clockless handheld can
// get wrong.
func pruneCorruptCopies(path string, keep int) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + ".corrupt-"
	var kept []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		kept = append(kept, e.Name())
	}
	if len(kept) <= keep {
		return
	}
	sort.Strings(kept)
	for _, name := range kept[:len(kept)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// LoadLastKnownGood attempts to load the config from the backup file if the
// primary file is corrupt or missing. It returns the loaded config and the
// path from which it was loaded.
func LoadLastKnownGood(filename string) (*Config, error) {
	cfg, err := loadConfig(filename)
	if err == nil {
		configHealth.RecordLoad(filename, nil)
		configHealth.MarkLoadOK()
		return cfg, nil
	}

	// Try backup.
	backup := filename + ".bak"
	Log().Warn("primary config failed, trying backup", "path", filename, "err", err)
	cfg, err = loadConfig(backup)
	if err == nil && cfg != nil {
		configHealth.RecordLoad(backup, nil)
		configHealth.RecordBackupRestore()
		// The backup is now the effective config for this launch; leave the
		// last-known-good source path pointing at the backup so diagnostics
		// do not falsely report that the primary config was healthy.
		configHealth.SetLastLoadPath(backup)
		configHealth.MarkLoadOK()
		// Keep the broken file instead of letting the next save overwrite the
		// evidence, and report the recovery once the render loop can show it.
		aside := preserveCorruptConfig(filename)
		configRecoveryNotice = configRecoveryMessage(aside)
		Log().Warn("config recovered from backup", "damaged", filename, "kept", aside)
		// A usable backup is a successful launch. Returning the error here made
		// main.go logFatal() before safe mode or the recovery chord existed, so
		// one bad write bricked startup until the file was fixed by hand.
		return cfg, nil
	}
	configHealth.RecordLoad(filename, err)
	return nil, fmt.Errorf("primary and backup config failed: %w", err)
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

// Log prints a compact config-health summary to the startup log so a failed
// launch or an unexpected reload has a readable trail in the diagnostics
// console.
func (ch *ConfigHealth) Log() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	logDebug("config.health", "lastLoad=%s lastSave=%s source=%s loadErrs=%d saveErrs=%d restores=%d loadOK=%v saveOK=%v",
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
