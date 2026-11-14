package main

// Config schema versioning.
//
// The config format is JSON on disk and is edited by hand as often as by the
// builder, so a build must be explicit about which format it understands. Every
// config carries a schemaVersion; loading migrates older files forward in
// memory, and refuses a file written by a newer build instead of downgrading
// (and later overwriting) it.

import (
	"errors"
	"fmt"
	"strings"
)

// ConfigSchemaVersion is the config format this build writes. Bump it when a
// change cannot be expressed as a default, and add the step to MigrateConfig.
const ConfigSchemaVersion = 1

// MigrateConfig upgrades cfg to ConfigSchemaVersion in place and reports the
// version it started from.
func MigrateConfig(cfg *Config) (int, error) {
	if cfg == nil {
		return 0, errors.New("nil config")
	}
	if cfg.SchemaVersion > ConfigSchemaVersion {
		return cfg.SchemaVersion, fmt.Errorf(
			"config schema %d is newer than this build supports (%d)",
			cfg.SchemaVersion, ConfigSchemaVersion)
	}
	from := cfg.SchemaVersion
	for cfg.SchemaVersion < ConfigSchemaVersion {
		switch cfg.SchemaVersion {
		case 0:
			// v0 is every config written before schema stamping existed. The
			// scene/variables layout is unchanged; only the marker is missing.
			cfg.SchemaVersion = 1
		default:
			return from, fmt.Errorf("no migration from config schema %d", cfg.SchemaVersion)
		}
	}
	return from, nil
}

// removeRetiredVaultElements drops the Vault button from layouts saved by an
// older build. The feed behind it no longer exists, so leaving the button in a
// user config would only present a dead control. Only the retired trigger is
// matched; unrelated buttons and themes are untouched.
func removeRetiredVaultElements(cfg *Config) {
	if cfg == nil {
		return
	}
	for i := range cfg.Scenes {
		elements := cfg.Scenes[i].Elements
		kept := elements[:0]
		for _, elem := range elements {
			if strings.EqualFold(strings.TrimSpace(elem.Trigger), "vault_search") {
				continue
			}
			kept = append(kept, elem)
		}
		cfg.Scenes[i].Elements = kept
	}
	for key := range cfg.Variables.Custom {
		if retiredVaultVar(key) {
			delete(cfg.Variables.Custom, key)
		}
	}
}

// retiredVaultVar reports a custom variable that only the retired Vault feed
// used. Matching is case-insensitive because older configs may have been
// hand-edited.
func retiredVaultVar(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "vault_search_query", "vault_top", "vault_error":
		return true
	}
	return false
}
