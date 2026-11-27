package main

// plug_market.go — a local-first plugin catalogue. JukaHub never fetches or
// runs code from a remote index: the user puts plugin folders in
// <data>/plugin-catalog, sees them on the Plugin catalog card, and copies one
// into the plugins folder with A. Everything stays on the card, and the card
// says plainly that plugins are not sandboxed.
//
// Only plugin.json manifests are read during a scan. An install copies regular
// files only, refuses symlinks, and bounds each file and the folder as a
// whole, so a "catalogue" cannot plant a link outside the plugins folder or
// fill the card in one press.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	plugMarketDirRel       = "plugin-catalog"
	plugMarketManifestMax  = 64 << 10
	plugMarketFileMax      = 2 << 20
	plugMarketFolderMax    = 16 << 20
	plugMarketNameMaxChars = 40
)

var plugMarketIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// plugMarketDir is replaceable in tests.
var plugMarketDir = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, plugMarketDirRel)
}

type plugMarketEntry struct {
	ID          string
	Title       string
	Version     string
	Author      string
	Description string
	Source      string
	Installed   bool
	Problem     string
}

// plugMarketScan lists the plugin folders on the card, without running any of
// their code. A bad manifest is listed with the reason rather than hidden.
func plugMarketScan() []plugMarketEntry {
	root := plugMarketDir()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []plugMarketEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		source := filepath.Join(root, e.Name())
		item := plugMarketEntry{ID: e.Name(), Source: source, Title: plugTitle(e.Name(), "")}
		data, err := os.ReadFile(filepath.Join(source, "plugin.json"))
		if err != nil {
			item.Problem = "no readable plugin.json"
			out = append(out, item)
			continue
		}
		if len(data) > plugMarketManifestMax {
			item.Problem = "plugin.json is too large"
			out = append(out, item)
			continue
		}
		var info PluginInfo
		if err := json.Unmarshal(data, &info); err != nil {
			item.Problem = "plugin.json is not valid JSON"
			out = append(out, item)
			continue
		}
		if id := strings.ToLower(strings.TrimSpace(info.Name)); id != "" && plugMarketIDRe.MatchString(id) {
			item.ID = id
		}
		item.Title = plugTitle(item.ID, info.Title)
		item.Version = strings.TrimSpace(info.Version)
		item.Author = strings.TrimSpace(info.Author)
		item.Description = strings.TrimSpace(info.Description)
		if len([]rune(item.Description)) > plugMarketNameMaxChars*4 {
			item.Description = string([]rune(item.Description)[:plugMarketNameMaxChars*4])
		}
		if !plugMarketIDRe.MatchString(item.ID) {
			item.Problem = "the folder name is not a safe plugin id"
		} else if strings.TrimSpace(info.Entry) == "" {
			item.Problem = "the manifest names no entry script"
		} else if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(info.Entry))); err != nil {
			item.Problem = "the entry script is missing"
		}
		if _, err := os.Stat(filepath.Join(pluginDir, item.ID, "plugin.json")); err == nil {
			item.Installed = true
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// plugMarketInstall copies one catalogue folder into the plugins folder. It
// never overwrites an installed plugin: a catalogue cannot replace code that
// is already trusted and switched on.
func plugMarketInstall(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if !plugMarketIDRe.MatchString(id) {
		return fmt.Errorf("not a safe plugin id")
	}
	var found *plugMarketEntry
	for _, item := range plugMarketScan() {
		if item.ID == id {
			copy := item
			found = &copy
			break
		}
	}
	if found == nil {
		return fmt.Errorf("that plugin is not in the catalogue")
	}
	if found.Problem != "" {
		return fmt.Errorf("that plugin has a problem: %s", found.Problem)
	}
	if found.Installed {
		return fmt.Errorf("that plugin is already installed")
	}
	dest := filepath.Join(pluginDir, id)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("that plugin is already installed")
	}
	total := int64(0)
	err := filepath.WalkDir(found.Source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(found.Source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("skipping %s: not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > plugMarketFileMax {
			return fmt.Errorf("skipping %s: over the file limit", rel)
		}
		total += info.Size()
		if total > plugMarketFolderMax {
			return fmt.Errorf("the plugin folder is over the size limit")
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, plugMarketFileMax+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		// A half-copied plugin must never look installed to LoadPlugins.
		_ = os.RemoveAll(dest)
		return err
	}
	return nil
}

// plugMarketUI is the card's own focus, separate from the catalogue scan.
var plugMarketUI struct{ cursor int }

func plugMarketMove(delta int) {
	items := plugMarketScan()
	if len(items) == 0 {
		return
	}
	plugMarketUI.cursor = (plugMarketUI.cursor + delta + len(items)) % len(items)
	PlayNavSound()
}

// plugMarketActivate installs the highlighted plugin, or says why it cannot.
func plugMarketActivate() {
	items := plugMarketScan()
	if plugMarketUI.cursor < 0 || plugMarketUI.cursor >= len(items) {
		return
	}
	item := items[plugMarketUI.cursor]
	if item.Installed {
		ShowToast(item.Title+" is already installed", ToastKindInfo)
		return
	}
	if item.Problem != "" {
		ShowToast("Cannot install "+item.Title+": "+item.Problem, ToastKindError)
		return
	}
	if err := plugMarketInstall(item.ID); err != nil {
		ShowToast("Could not install "+item.Title+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast(item.Title+" installed — switch it on in Settings → Plugins", ToastKindSuccess)
}

// plugMarketLines is the card's text, with the trust warning always on top.
func plugMarketLines(items []plugMarketEntry, cursor int) []string {
	lines := []string{
		"Plugins run with JukaHub's permissions. They are not sandboxed.",
		"Install only code you trust. Nothing here is downloaded automatically.",
		"Put plugin folders in <data>/" + plugMarketDirRel + ", then open this card again.",
		"",
	}
	if len(items) == 0 {
		return append(lines, "No plugins in the catalogue yet.")
	}
	lines = append(lines, "A installs the highlighted plugin · B goes back", "")
	for i, item := range items {
		mark := " "
		if i == cursor {
			mark = "›"
		}
		line := mark + " " + item.Title
		if item.Version != "" {
			line += " v" + item.Version
		}
		if item.Author != "" {
			line += " · " + item.Author
		}
		switch {
		case item.Installed:
			line += " · installed"
		case item.Problem != "":
			line += " · " + item.Problem
		}
		lines = append(lines, line)
		if i == cursor && item.Description != "" {
			lines = append(lines, "    "+item.Description)
		}
	}
	return lines
}
