package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// themeDir holds user-supplied theme files (one JSON file per theme).
var themeDir = "themes"

// themeLoaderColorPattern accepts #rrggbb and #rrggbbaa colours.
var themeLoaderColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// themeFile is the on-disk form of a theme. Keys mirror ThemePreset.
type themeFile struct {
	Name          string `json:"name"`
	Background    string `json:"background"`
	ButtonColor   string `json:"buttonColor"`
	LabelColor    string `json:"labelColor"`
	InputColor    string `json:"inputColor"`
	Surface       string `json:"surface"`
	SurfaceAlt    string `json:"surfaceAlt"`
	SurfaceRaised string `json:"surfaceRaised"`
	TextPrimary   string `json:"textPrimary"`
	TextSecondary string `json:"textSecondary"`
	TextTertiary  string `json:"textTertiary"`
	BorderSubtle  string `json:"borderSubtle"`
	BorderDefault string `json:"borderDefault"`
	BorderFocus   string `json:"borderFocus"`
	Success       string `json:"success"`
	Warning       string `json:"warning"`
	Danger        string `json:"danger"`
	Info          string `json:"info"`
	Overlay       string `json:"overlay"`
}

// toPreset validates every colour and converts the file into a ThemePreset.
func (f themeFile) toPreset() (ThemePreset, error) {
	if strings.TrimSpace(f.Name) == "" {
		return ThemePreset{}, fmt.Errorf("missing name")
	}
	colours := map[string]string{
		"background": f.Background, "buttonColor": f.ButtonColor,
		"labelColor": f.LabelColor, "inputColor": f.InputColor,
		"surface": f.Surface, "surfaceAlt": f.SurfaceAlt,
		"surfaceRaised": f.SurfaceRaised, "textPrimary": f.TextPrimary,
		"textSecondary": f.TextSecondary, "textTertiary": f.TextTertiary,
		"borderSubtle": f.BorderSubtle, "borderDefault": f.BorderDefault,
		"borderFocus": f.BorderFocus, "success": f.Success,
		"warning": f.Warning, "danger": f.Danger,
		"info": f.Info, "overlay": f.Overlay,
	}
	for key, value := range colours {
		if !themeLoaderColorPattern.MatchString(value) {
			return ThemePreset{}, fmt.Errorf("%s: %q is not #rrggbb or #rrggbbaa", key, value)
		}
	}
	// themeFile mirrors ThemePreset field for field, so a conversion replaces
	// the 18-line literal (and fails to compile if the two ever drift apart).
	return ThemePreset(f), nil
}

// LoadThemeFiles registers themes from themeDir/*.json. The file name (without
// extension) is the preset id. Built-in presets win on a collision, and invalid
// files are skipped. It returns the ids that were added and per-file errors.
func LoadThemeFiles(dir string) ([]string, map[string]error) {
	added := []string{}
	failed := map[string]error{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return added, failed
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		id := strings.ToLower(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			failed[path] = err
			continue
		}
		var file themeFile
		if err := json.Unmarshal(data, &file); err != nil {
			failed[path] = err
			continue
		}
		preset, err := file.toPreset()
		if err != nil {
			failed[path] = err
			continue
		}
		if _, exists := themePresets[id]; exists {
			failed[path] = fmt.Errorf("id %q is already a built-in theme", id)
			continue
		}
		themePresets[id] = preset
		themePresetOrder = append(themePresetOrder, id)
		added = append(added, id)
	}
	return added, failed
}
