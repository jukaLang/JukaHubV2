package main

// plugbundle.go — the plugins that ship inside JukaHub.
//
// The example plugins used to live only in the repository: no release package
// copied them, so the Plugins page of a real install was always empty and the
// documentation had to ask people to copy folders by hand. They are now built
// into the binary and copied into plugins/ the first time the folder is
// scanned, which is wherever the loader looks on every platform (the TrimUI
// launcher runs from Apps/JukaHub/, Windows from the folder JukaHub.exe is in).
//
// Seeding never overwrites: a folder that already exists is left alone (so a
// user's edits and the notes plugin's notes.txt survive an update), and a
// bundled plugin the user deleted is remembered in the user config and not
// brought back.

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Besides plugin.json and its script, a plugin folder holds its typed
// extensions (plugx.go): theme files and experience packs (*.json) and control
// profiles (*.jkinput).
//
//go:embed plugins/*/*.json plugins/*/*.sh plugins/*/*.jkinput
var plugBundleFS embed.FS

// plugSeededKey records, in the user config, the bundled plugins already copied
// out once, as a comma-separated list of folder names.
const plugSeededKey = "pluginsSeeded"

// plugBundledNames lists the plugins built into the binary, sorted.
func plugBundledNames() []string {
	entries, err := fs.ReadDir(plugBundleFS, "plugins")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// plugSeededSet reads the record of plugins already copied out.
func plugSeededSet(config *Config) map[string]bool {
	set := map[string]bool{}
	if config == nil || config.Variables.Custom == nil {
		return set
	}
	raw, _ := config.Variables.Custom[plugSeededKey].(string)
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
}

// plugSeedBundled copies every bundled plugin that has never been copied out
// into dir, and returns the names it wrote. A config without a custom block (a
// test's bare config) is left alone: there is nowhere to remember the result,
// and a scan of a scratch folder should not grow plugins of its own.
func plugSeedBundled(config *Config, dir string) []string {
	if config == nil || config.Variables.Custom == nil || dir == "" {
		return nil
	}
	seeded := plugSeededSet(config)
	var wrote []string
	changed := false
	for _, name := range plugBundledNames() {
		if seeded[name] {
			continue
		}
		seeded[name] = true
		changed = true
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			// Already there (an earlier copy, or the user's own folder of the
			// same name): never overwrite it.
			continue
		}
		if err := plugCopyBundled(name, target); err != nil {
			LogScene("plugins").Warn("bundled plugin not installed", "plugin", name, "err", err)
			continue
		}
		wrote = append(wrote, name)
	}
	if changed {
		// Recorded in memory only: the next settings save (the Plugins page's
		// own switches, Quick Settings, the playtime counter) carries it to
		// jukauser.json. A save from here would also fire from a test's scan.
		names := make([]string, 0, len(seeded))
		for name := range seeded {
			names = append(names, name)
		}
		sort.Strings(names)
		config.Variables.Custom[plugSeededKey] = strings.Join(names, ",")
	}
	return wrote
}

// plugCopyBundled writes one bundled plugin folder to target. Scripts get the
// executable bit; the runner does not need it, but a user running one from a
// terminal does.
func plugCopyBundled(name, target string) error {
	root := path.Join("plugins", name)
	entries, err := fs.ReadDir(plugBundleFS, root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(plugBundleFS, path.Join(root, e.Name()))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(e.Name(), ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(target, e.Name()), data, mode); err != nil {
			return err
		}
	}
	return nil
}
