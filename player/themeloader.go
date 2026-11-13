package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// themeDir holds user-supplied theme files (one JSON file per theme).
var themeDir = "themes"

// builtinThemeIDs is the set of palettes compiled into the app, captured by the
// first LoadThemeFiles call — before any file is registered. It is what tells a
// contribution apart from a shipped palette across restarts: themePresets alone
// cannot, because a theme installed on an earlier run is registered there too.
var builtinThemeIDs map[string]bool

// captureBuiltinThemeIDs records the compiled palettes once, so a later load of
// themes/*.json cannot make an installed theme look built-in.
func captureBuiltinThemeIDs() {
	if builtinThemeIDs != nil {
		return
	}
	ids := make(map[string]bool, len(themePresets))
	for id := range themePresets {
		ids[id] = true
	}
	builtinThemeIDs = ids
}

// themeLoaderColorPattern accepts #rrggbb and #rrggbbaa colours.
var themeLoaderColorPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`) })

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
	// A theme file may also rearrange the interface: naming an arrangement
	// makes it a total conversion, which is what puts it in that gallery tab.
	// An unknown or "custom" value is ignored rather than stored.
	Summary     string `json:"summary"`
	Arrangement string `json:"arrangement"`
	// Bloom and Bloom2 tint the backdrop's two glows (#rrggbb). Both are
	// optional; a file without them keeps the shipped glows.
	Bloom  string `json:"bloom,omitempty"`
	Bloom2 string `json:"bloom2,omitempty"`
	// The console look (themeconsole.go), all optional: carouselStyle
	// "shelf", "strip" or "hub" (unknown is ignored), tileRadius 0..32
	// design px, focusRing and focusRing2 #rrggbb.
	CarouselStyle string `json:"carouselStyle,omitempty"`
	TileRadius    int32  `json:"tileRadius,omitempty"`
	FocusRing     string `json:"focusRing,omitempty"`
	FocusRing2    string `json:"focusRing2,omitempty"`
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
		if !themeLoaderColorPattern().MatchString(value) {
			return ThemePreset{}, fmt.Errorf("%s: %q is not #rrggbb or #rrggbbaa", key, value)
		}
	}
	// The glows are optional, but one that is given must be a plain colour:
	// the backdrop paints them opaque, so an alpha channel would be ignored.
	for key, value := range map[string]string{"bloom": f.Bloom, "bloom2": f.Bloom2, "focusRing": f.FocusRing, "focusRing2": f.FocusRing2} {
		if value == "" {
			continue
		}
		if _, ok := parseHexColor(value); !ok || !strings.HasPrefix(value, "#") {
			return ThemePreset{}, fmt.Errorf("%s: %q is not #rrggbb", key, value)
		}
	}
	if f.TileRadius < 0 || f.TileRadius > homeTileRadiusMax {
		return ThemePreset{}, fmt.Errorf("tileRadius: %d is not 0..%d", f.TileRadius, homeTileRadiusMax)
	}
	// Written out rather than converted: the preset carries the gallery's own
	// fields (kind, arrangement) that a theme file does not set directly, and
	// a field added to one type alone must be a decision, not a silent zero.
	preset := ThemePreset{
		Name:          f.Name,
		Summary:       strings.TrimSpace(f.Summary),
		Bloom:         f.Bloom,
		Bloom2:        f.Bloom2,
		CarouselStyle: homeThemeFileCarouselStyle(f.CarouselStyle),
		TileRadius:    f.TileRadius,
		FocusRing:     f.FocusRing,
		FocusRing2:    f.FocusRing2,
		Background:    f.Background,
		ButtonColor:   f.ButtonColor,
		LabelColor:    f.LabelColor,
		InputColor:    f.InputColor,
		Surface:       f.Surface,
		SurfaceAlt:    f.SurfaceAlt,
		SurfaceRaised: f.SurfaceRaised,
		TextPrimary:   f.TextPrimary,
		TextSecondary: f.TextSecondary,
		TextTertiary:  f.TextTertiary,
		BorderSubtle:  f.BorderSubtle,
		BorderDefault: f.BorderDefault,
		BorderFocus:   f.BorderFocus,
		Success:       f.Success,
		Warning:       f.Warning,
		Danger:        f.Danger,
		Info:          f.Info,
		Overlay:       f.Overlay,
	}
	if strings.TrimSpace(f.Arrangement) != "" {
		if arrangement := normalizeHomeArrangement(f.Arrangement); arrangement != homeArrangementCustom {
			preset.Arrangement = arrangement
			preset.Kind = themeKindConversion
			if preset.Summary == "" {
				preset.Summary = "Custom theme · " + homeArrangementLabel(arrangement)
			}
		}
	}
	return preset, nil
}

// LoadThemeFiles registers themes from themeDir/*.json. The file name (without
// extension) is the preset id. Built-in presets win on a collision, and invalid
// files are skipped. It returns the ids that were added and per-file errors.
func LoadThemeFiles(dir string) ([]string, map[string]error) {
	captureBuiltinThemeIDs()
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
