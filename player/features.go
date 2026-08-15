package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// FileEntry represents a single entry in the File Explorer.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

// focusedFileIndex is the currently highlighted entry inside a dynamiclist.
var focusedFileIndex = -1

var mediaExtensions = []string{
	".mp4", ".mkv", ".avi", ".mov", ".webm", ".m3u8", ".flv", ".wmv",
	".mpg", ".mpeg", ".ts", ".m2ts", ".mp3", ".wav", ".ogg", ".m4a",
}

func isMediaFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range mediaExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// feRoot returns the configured File Explorer root, defaulting to "." so it
// works both on the device and during Windows development. If the configured
// root does not exist (e.g. /mnt on a dev PC), fall back to the current dir.
func feRoot(config *Config) string {
	root := strings.TrimSpace(config.Variables.FileExplorerRoot)
	if root == "" {
		root = "."
	}
	if _, err := os.Stat(root); err != nil {
		return "."
	}
	return root
}

// feCurrentPath returns the directory currently being browsed.
func feCurrentPath(config *Config) string {
	if p, ok := config.Variables.Custom["fe_path"].(string); ok && p != "" {
		return p
	}
	return feRoot(config)
}

// feListDirectory reads the current path and stores its entries in fe_entries.
func feListDirectory(config *Config) {
	path := feCurrentPath(config)
	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("File Explorer: cannot read %q: %v", path, err)
		config.Variables.Custom["fe_entries"] = []FileEntry{}
		config.Variables.Custom["fe_path"] = path
		return
	}

	var dirs, files []FileEntry
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		fe := FileEntry{Name: e.Name(), Path: full, IsDir: e.IsDir()}
		if e.IsDir() {
			dirs = append(dirs, fe)
		} else {
			files = append(files, fe)
		}
	}

	// ".." entry to navigate up, unless we are already at the configured root.
	parent := filepath.Dir(path)
	if path != feRoot(config) && parent != path {
		dirs = append([]FileEntry{{Name: "..", Path: parent, IsDir: true}}, dirs...)
	}

	list := append(dirs, files...)
	config.Variables.Custom["fe_entries"] = list
	config.Variables.Custom["fe_path"] = path

	if focusedFileIndex < 0 && len(list) > 0 {
		focusedFileIndex = 0
	}
	if focusedFileIndex >= len(list) {
		focusedFileIndex = len(list) - 1
	}
}

// feUp moves one directory up.
func feUp(config *Config) {
	path := feCurrentPath(config)
	parent := filepath.Dir(path)
	if parent == path || parent == "" {
		parent = feRoot(config)
	}
	config.Variables.Custom["fe_path"] = parent
	focusedFileIndex = 0
	feListDirectory(config)
}

// feEnterFocused acts on the highlighted entry of the current scene's
// dynamiclist: enter directories, unzip archives, or play media/URLs.
func feEnterFocused(config *Config) {
	entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
	if !ok || focusedFileIndex < 0 || focusedFileIndex >= len(entries) {
		return
	}
	entry := entries[focusedFileIndex]
	if entry.IsDir {
		config.Variables.Custom["fe_path"] = entry.Path
		focusedFileIndex = 0
		feListDirectory(config)
		return
	}
	if strings.EqualFold(filepath.Ext(entry.Path), ".zip") {
		if err := unzipFile(entry.Path); err != nil {
			log.Printf("Unzip error: %v", err)
		} else {
			log.Printf("Unzipped %s", entry.Path)
			feListDirectory(config)
		}
		return
	}
	if isMediaFile(entry.Name) || hasMediaExtension(entry.Path) {
		recordPlayed(entry.Path)
		playSmartURL(config, entry.Path)
	} else {
		log.Printf("[DEBUG] File Explorer: skipping non-media file: %s", entry.Name)
	}
}

// --- dynamiclist helpers (config-driven, mirrors searchresults helpers) ---

func sceneDynamicListElement(scene SceneConfig) (Element, bool) {
	for _, e := range scene.Elements {
		if e.Type == "dynamiclist" {
			return e, true
		}
	}
	return Element{}, false
}

func getSceneFileEntries(config *Config, scene SceneConfig) ([]FileEntry, bool) {
	e, ok := sceneDynamicListElement(scene)
	if !ok {
		return nil, false
	}
	entries, ok := config.Variables.Custom[e.Variable].([]FileEntry)
	return entries, ok
}

// renderDynamicList draws a vertical, scrollable list of file entries.
func renderDynamicList(renderer *sdl.Renderer, config *Config, element Element) {
	entries, ok := config.Variables.Custom[element.Variable].([]FileEntry)
	if !ok {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, "No items.", sdl.Color{R: 255, G: 255, B: 255, A: 255}, element.X, element.Y)
		}
		return
	}

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}

	renderText(renderer, config, font, "Path: "+feCurrentPath(config),
		sdl.Color{R: 180, G: 180, B: 255, A: 255}, element.X, element.Y)

	elemW := getElementWidth(element, 600)
	elemH := getElementHeight(element, 500)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, sdl.Color{R: 16, G: 19, B: 26, A: 220}, accentColor)
	lineH := int32(34)
	maxVisible := int((elemH - 30) / lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}

	start := 0
	if focusedFileIndex >= maxVisible {
		start = focusedFileIndex - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(entries) {
		end = len(entries)
	}

	for i := start; i < end; i++ {
		y := element.Y + 30 + int32(i-start)*lineH
		if isRecentlyPlayed(entries[i].Path) {
			renderer.SetDrawColor(200, 30, 30, 255)
			renderer.FillRect(&sdl.Rect{X: element.X, Y: y - 2, W: 4, H: lineH})
		}
		if i == focusedFileIndex {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
			renderer.FillRect(&sdl.Rect{X: element.X, Y: y - 2, W: elemW, H: lineH})
		}
		prefix := "[ ] "
		color := sdl.Color{R: 255, G: 255, B: 255, A: 255}
		if entries[i].IsDir {
			prefix = "[D] "
			color = sdl.Color{R: 255, G: 230, B: 120, A: 255}
		} else if isMediaFile(entries[i].Name) {
			prefix = "[>] "
		}
		txt := prefix + entries[i].Name
		if len(txt) > 60 {
			txt = txt[:57] + "..."
		}
		renderText(renderer, config, font, txt, color, element.X+8, y+4)
	}
}

// applyColorVar parses an "r,g,b" or "#rrggbb" string and updates a named
// theme variable.
func applyColorVar(config *Config, name, value string) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		r, g, b := hexToRGB(value)
		value = fmt.Sprintf("%d,%d,%d", r, g, b)
	}
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		log.Printf("applyColorVar: expected r,g,b got %q", value)
		return
	}
	r, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	g, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	b, e3 := strconv.Atoi(strings.TrimSpace(parts[2]))
	if e1 != nil || e2 != nil || e3 != nil {
		log.Printf("applyColorVar: invalid number in %q", value)
		return
	}
	switch name {
	case "buttonColor":
		config.Variables.ButtonColor.R = r
		config.Variables.ButtonColor.G = g
		config.Variables.ButtonColor.B = b
	case "labelColor":
		config.Variables.LabelColor.R = r
		config.Variables.LabelColor.G = g
		config.Variables.LabelColor.B = b
	case "inputColor":
		config.Variables.InputColor.R = r
		config.Variables.InputColor.G = g
		config.Variables.InputColor.B = b
	}
	log.Printf("[DEBUG] Set %s = %d,%d,%d", name, r, g, b)
}
