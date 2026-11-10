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
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
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
		target := filepath.Join(dir, name)
		if seeded[name] {
			// A copy from an older JukaHub is brought up to date (new actions,
			// the hooks that make switching it on do something).
			if plugUpgradeBundled(name, target) {
				wrote = append(wrote, name)
			}
			continue
		}
		seeded[name] = true
		changed = true
		if _, err := os.Stat(target); err == nil {
			// Already there (an earlier copy, or the user's own folder of the
			// same name): never overwrite it, only upgrade an old stock copy.
			if plugUpgradeBundled(name, target) {
				wrote = append(wrote, name)
			}
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

// plugUpgradeBundled refreshes an installed copy of a bundled plugin when it
// is JukaHub's own (author "JukaHub") and older than the one in this binary.
// Only the files JukaHub ships are rewritten, so the notes plugin's notes.txt
// and anything else the user added stay. A folder that is missing (deleted by
// the user), someone else's, or already current is left alone.
func plugUpgradeBundled(name, target string) bool {
	disk, err := os.ReadFile(filepath.Join(target, "plugin.json"))
	if err != nil {
		return false
	}
	shipped, err := fs.ReadFile(plugBundleFS, path.Join("plugins", name, "plugin.json"))
	if err != nil {
		return false
	}
	var have, want struct {
		Author  string `json:"author"`
		Version string `json:"version"`
	}
	if json.Unmarshal(disk, &have) != nil || json.Unmarshal(shipped, &want) != nil {
		return false
	}
	if strings.TrimSpace(have.Author) != plugNativeAuthor || !plugVersionLess(have.Version, want.Version) {
		return false
	}
	if err := plugUpgradeFiles(name, target); err != nil {
		LogScene("plugins").Warn("bundled plugin not upgraded", "plugin", name, "err", err)
		return false
	}
	LogScene("plugins").Info("bundled plugin upgraded", "plugin", name, "from", have.Version, "to", want.Version)
	return true
}

// plugUpgradeFiles rewrites the shipped files of one plugin in target. A
// script or data file that differs from the new one is kept beside it as
// <file>.bak first, so edits made to an older copy are not lost. plugin.json
// is written last: if anything before it fails, the old version stays on disk
// and the upgrade is tried again on the next scan.
func plugUpgradeFiles(name, target string) error {
	root := path.Join("plugins", name)
	entries, err := fs.ReadDir(plugBundleFS, root)
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Name() != "plugin.json" && entries[j].Name() == "plugin.json"
	})
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(plugBundleFS, path.Join(root, e.Name()))
		if err != nil {
			return err
		}
		dest := filepath.Join(target, e.Name())
		if old, err := os.ReadFile(dest); err == nil {
			if plugSameText(old, data) {
				continue
			}
			if e.Name() != "plugin.json" {
				if err := os.WriteFile(dest+".bak", old, 0o644); err != nil {
					return err
				}
			}
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(e.Name(), ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return err
		}
	}
	return nil
}

// plugVersionLess compares dotted version numbers ("1.2.0" < "1.10.0"). A
// part that is not a number counts as 0.
func plugVersionLess(a, b string) bool {
	pa := strings.Split(strings.TrimSpace(a), ".")
	pb := strings.Split(strings.TrimSpace(b), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
