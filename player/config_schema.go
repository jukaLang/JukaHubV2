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
