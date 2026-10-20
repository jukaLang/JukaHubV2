package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// openFileInCanvasSandbox loads a .html or .js file into the Canvas Sandbox.
// For .html files it reads the content and checks for canvas-related code.
// For .js files it wraps the content in a basic HTML template with a canvas.
func openFileInCanvasSandbox(path string) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".html" && ext != ".js" {
		showToast("Only .html and .js files can open in Canvas Sandbox", ToastError())
		return
	}
	path, pathErr := resolveExplorerEntry(appConfig, path)
	if pathErr != nil {
		showToast("Cannot open file: "+pathErr.Error(), ToastError())
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		showToast("Failed to read file: "+err.Error(), ToastError())
		return
	}
	content := string(data)
	if ext == ".js" {
		content = "<canvas id=\"c\" width=\"400\" height=\"300\"></canvas>\n<script>\n" + content + "\n</script>"
	} else if ext == ".html" {
		if !strings.Contains(strings.ToLower(content), "<canvas") && !strings.Contains(content, "getContext") {
			showToast("No canvas element found in HTML file", ToastError())
			return
		}
	}
	for _, e := range appConfig.Scenes {
		if e.Name == "CanvasSandbox" {
			for _, el := range e.Elements {
				if el.Variable == "canvas_code" {
					appConfig.Variables.Custom["canvas_code"] = content
				}
			}
		}
	}
	canvasCode = content
	if canvasSurface != nil {
		canvasSurface.Free()
		canvasSurface = nil
	}
	canvasSurface = executeCanvasCode(canvasCode)
	for i, e := range appConfig.Scenes {
		if e.Name == "CanvasSandbox" {
			changeSceneTo(appConfig, i)
			break
		}
	}
}

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

// resolveExplorerEntry validates a FileEntry path selected from a scene list:
// it must be an existing regular file below the File Explorer root. Callers
// use the returned (absolute) path for the actual open so a tampered list
// value cannot reach outside the browsable tree.
func resolveExplorerEntry(config *Config, path string) (string, error) {
	root := "."
	if config != nil {
		root = feRoot(config)
	}
	return regularFileWithin(root, path)
}

// feCurrentPath returns the directory currently being browsed. The stored
// fe_path is validated against the File Explorer root on every read, so a
// tampered or stale value falls back to the root instead of listing an
// arbitrary directory.
func feCurrentPath(config *Config) string {
	root := feRoot(config)
	if config == nil {
		return root
	}
	if p, ok := config.Variables.Custom["fe_path"].(string); ok && strings.TrimSpace(p) != "" {
		if dir, err := existingDirWithin(root, p); err == nil {
			return dir
		}
	}
	return root
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

// feDisplayCache remembers the validated path shown in the File Explorer
// toolbar. feCurrentPath stats the directory (and resolves it against the
// root) to validate it; the toolbar is redrawn every frame, and on the SD card
// that stat was the only disk I/O the render loop performed.
var feDisplayCache struct {
	raw     string
	root    string
	display string
}

// feDisplayPath is the cached variant of feCurrentPath for rendering only.
func feDisplayPath(config *Config) string {
	raw := ""
	root := ""
	if config != nil {
		raw, _ = config.Variables.Custom["fe_path"].(string)
		root = config.Variables.FileExplorerRoot
	}
	if feDisplayCache.display != "" && feDisplayCache.raw == raw && feDisplayCache.root == root {
		return feDisplayCache.display
	}
	path := feCurrentPath(config)
	feDisplayCache.raw = raw
	feDisplayCache.root = root
	feDisplayCache.display = path
	return path
}

// feBack handles B/Escape in the File Explorer: inside a directory it goes up
// one level; at the root it returns to the previous scene/Home. It never
// terminates the application.
func feBack(config *Config) {
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == "FileExplorer" &&
		feCurrentPath(config) != feRoot(config) {
		feUp(config)
		return
	}
	goBackScene(config)
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
// dynamiclist: enter directories, unzip archives, view images, or play media/URLs.
func feEnterFocused(config *Config) {
	entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
	if !ok || focusedFileIndex < 0 || focusedFileIndex >= len(entries) {
		return
	}
	entry := entries[focusedFileIndex]
	// The Apps list is a program launcher: entries open a built-in scene or
	// start an external application instead of browsing files.
	if el, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok && el.Variable == appsEntriesVar {
		launchAppEntry(config, entry)
		return
	}
	// Live TV channels are stream URLs, and many playlists use extension-less
	// URLs (http://host:8080/user/pass/1234). They must play even though the
	// media-extension check below cannot recognize them.
	if el, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok && el.Variable == tvEntriesVar {
		recordPlayed(config, entry.Path)
		playSmartURL(config, entry.Path)
		return
	}
	if entry.IsDir {
		// Only a directory that really lives below the explorer root is entered.
		dir, err := existingDirWithin(feRoot(config), entry.Path)
		if err != nil {
			showToast("Cannot open folder: "+err.Error(), ToastError())
			return
		}
		config.Variables.Custom["fe_path"] = dir
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
	ext := strings.ToLower(filepath.Ext(entry.Path))
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".bmp" {
		if config.Variables.Custom["chat_attach_image"] == "1" {
			// Send image as chat message attachment
			config.Variables.Custom["chat_attach_image"] = ""
			// Create a ChatMessage with ImageURL set to the file path
			chatMessages = append(chatMessages, ChatMessage{
				ID:        fmt.Sprintf("%x", []byte{}),
				Sender:    "You",
				Text:      "",
				Timestamp: time.Now(),
				ImageURL:  entry.Path,
			})
			saveChatMessages()
			showToast("Image attached to chat", ToastSuccess())
			return
		}
		imagePath, err := resolveExplorerEntry(config, entry.Path)
		if err != nil {
			showToast("Cannot open image: "+err.Error(), ToastError())
			return
		}
		openImageViewer(config, imagePath)
		return
	}
	if ext == ".html" || ext == ".js" {
		openFileInCanvasSandbox(entry.Path)
		return
	}
	if ext == ".pdf" {
		pdfOpenFile(entry.Path)
		if idx := findSceneIndex(config, "PDFReader"); idx >= 0 {
			changeSceneTo(config, idx)
		}
		return
	}
	if isMediaFile(entry.Name) || hasMediaExtension(entry.Path) {
		recordPlayed(config, entry.Path)
		// Audio files open the audio player instead of the video decoder: it
		// builds a playlist from the folder and keeps the track list usable
		// while the song plays.
		if isAudioFile(entry.Name) {
			openAudioPlayer(config, entry.Path)
			return
		}
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

// dynamicListRowH is the height of one row including the gap under it.
const dynamicListRowH = int32(40)

// dynamicListListOffset is where the first row starts inside a dynamic list.
// Live TV reserves the space for its filter bar; every other list leaves a
// small top margin. Rendering and click hit-testing share it so the two can
// never drift apart.
func dynamicListListOffset(element Element) int32 {
	if element.Variable == tvEntriesVar {
		return 8 + tvFilterBarH + 6
	}
	return 30
}

// dynamicListMaxVisible is how many rows fit in the element.
func dynamicListMaxVisible(element Element) int {
	elemH := getElementHeight(element, 500)
	n := int((elemH - dynamicListListOffset(element) - 4) / dynamicListRowH)
	if n < 1 {
		return 1
	}
	return n
}

// dynamicListRowAt reports which row a click at my selects, or -1 when the
// click lands above the first row (the filter bar or the list's top margin).
func dynamicListRowAt(element Element, start, end int, my int32) int {
	top := element.Y + dynamicListListOffset(element)
	for i := start; i < end; i++ {
		y := top + int32(i-start)*dynamicListRowH
		if my >= y-2 && my <= y+dynamicListRowH-2 {
			return i
		}
	}
	return -1
}

// renderDynamicList draws a vertical, scrollable list of file entries.
func renderDynamicList(renderer *sdl.Renderer, config *Config, element Element) {
	entries, ok := config.Variables.Custom[element.Variable].([]FileEntry)
	if !ok {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, "No items.", ColorTextPrimary(), element.X, element.Y)
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

	elemW := getElementWidth(element, 600)
	elemH := getElementHeight(element, 500)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), accentColor)

	// File Explorer gets a compact path toolbar above the list: one dark flat
	// field holding the current path (measured ellipsis, clipped). Other
	// dynamic lists (Live TV, Podcasts) have no path concept and draw nothing.
	isFileExplorer := currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == "FileExplorer"
	if isFileExplorer {
		toolbar := sdl.Rect{X: element.X, Y: element.Y - 52, W: elemW, H: 44}
		fillRoundedRect(renderer, toolbar.X, toolbar.Y, toolbar.W, toolbar.H, 10, ColorCard)
		strokeRoundedRect(renderer, toolbar.X, toolbar.Y, toolbar.W, toolbar.H, 10, 1, ColorBorder)
		pathText := feDisplayPath(config)
		renderWithClip(renderer, toolbar.X+12, toolbar.Y+4, toolbar.W-24, toolbar.H-8, func(ren *sdl.Renderer) {
			drawText(ren, font, ellipsize(font, pathText, toolbar.W-30), toolbar.X+12, toolbar.Y+(toolbar.H-int32(font.Height()))/2,
				ColorTextSecondary(), textAlignLeft)
		})
	}

	// Live TV adds a category/search bar inside the panel. It is drawn before
	// the rows so the list starts below it, and it must stay visible even when
	// the current filter has no matches.
	isLiveTV := element.Variable == tvEntriesVar
	if isLiveTV {
		renderTVFilterBar(renderer, config, element.X+8, element.Y+8, elemW-16)
	}
	listOffset := dynamicListListOffset(element)

	lineH := dynamicListRowH
	maxVisible := dynamicListMaxVisible(element)

	start := 0
	if focusedFileIndex >= maxVisible {
		start = focusedFileIndex - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(entries) {
		end = len(entries)
	}

	if isLiveTV && len(entries) == 0 {
		renderTVEmptyState(renderer, config, font, element, elemW, element.Y+listOffset)
	}

	for i := start; i < end; i++ {
		y := element.Y + listOffset + int32(i-start)*lineH
		rowW := elemW - 16
		rowH := lineH - 4
		rowX := element.X + 8
		rowY := y + 2

		drawRow(renderer, rowX, rowY, rowW, rowH, 8, i == focusedFileIndex, false)

		if isRecentlyPlayed(entries[i].Path) {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: rowX + rowW - 6, Y: rowY + 6, W: 4, H: rowH - 12})
		}

		prefix := ""
		color := ColorTextSecondary()
		nameMaxW := rowW - 40
		if entries[i].IsDir {
			// Use ▶ (U+25B6), which Inter contains — ▸ (U+25B8) does not exist
			// in Inter and renders as a missing-glyph box.
			prefix = "> "
			color = sdl.Color{R: 255, G: 230, B: 120, A: 255}
		} else if isMediaFile(entries[i].Name) {
			prefix = "> "
			color = ColorTextAccent()
		}
		if isLiveTV {
			// Favorites are marked with an asterisk (Inter has no star glyph)
			// and the channel's category sits on the right, so a long "All"
			// list still shows where each channel belongs.
			group, fav := tvRowMeta(entries[i].Path)
			if fav {
				prefix = "* "
				color = ColorTextAccent()
			}
			if group != "" {
				g := ellipsize(font, group, rowW/4)
				gw, _, _ := font.SizeUTF8(g)
				if int32(gw)+80 < rowW {
					renderText(renderer, config, font, g, ColorTextTertiary(),
						rowX+rowW-int32(gw)-14, rowY+9)
					nameMaxW = rowW - int32(gw) - 48
				}
			}
		}
		txt := ellipsize(font, prefix+entries[i].Name, nameMaxW)
		renderText(renderer, config, font, txt, color, rowX+14, rowY+9)
	}

	// scrollbar
	if len(entries) > maxVisible {
		thumbFrac := float64(maxVisible) / float64(len(entries))
		scrollFrac := 0.0
		if len(entries) > maxVisible {
			scrollFrac = float64(start) / float64(len(entries)-maxVisible)
		}
		drawScrollbar(renderer, element.X+elemW-6, element.Y+listOffset, 3, elemH-listOffset-4, thumbFrac, scrollFrac)
	}
}

// renderTVEmptyState explains an empty filtered list instead of leaving a
// blank panel: the user needs to know whether to load a playlist, relax the
// search, or star something first.
func renderTVEmptyState(renderer *sdl.Renderer, config *Config, font *ttf.Font, element Element, elemW, top int32) {
	msg := "No channels found. Put an .m3u playlist in the tv/ folder, then press Load Playlist."
	switch {
	case tvQuery() != "":
		msg = "No channels match \"" + tvQuery() + "\". Press X to change the search."
	case tvCategory() == tvFavoritesCategory:
		msg = "No favorites yet. Highlight a channel and press Y to star it."
	}
	wrapped := wrapMeasured([]rune(msg), elemW-56, func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	})
	y := top + 8
	for _, line := range wrapped {
		w, _, _ := font.SizeUTF8(line)
		renderText(renderer, config, font, line, ColorTextTertiary(),
			element.X+(elemW-int32(w))/2, y)
		y += int32(font.Height()) + 4
	}
}

// colorSettingVariables are the theme colors edited from Settings → Appearance.
// Their fields always render a swatch, even before the value is valid.
var colorSettingVariables = map[string]bool{
	"buttonColor": true,
	"labelColor":  true,
	"inputColor":  true,
}

// settingsFieldColor resolves the color a settings input currently represents.
// editing makes it follow the live text buffer, so the swatch updates while the
// value is typed rather than only after the config is saved.
func settingsFieldColor(config *Config, element Element, editing bool) (sdl.Color, bool, bool) {
	if element.Type != "input" || element.Variable == "" {
		return sdl.Color{}, false, false
	}
	isColorField := colorSettingVariables[element.Variable]
	value := inputVariableValue(config, element.Variable)
	if editing {
		value = inputTextBuffer
	}
	c, ok := parseHexColor(value)
	if ok {
		// Any field holding a valid #rrggbb gets a swatch, so custom color
		// variables added by a config get the same treatment.
		isColorField = true
	}
	return c, ok, isColorField
}

// applyColorVar parses an "r,g,b" or "#rrggbb" string and updates a named
// theme variable. The canonical colour string is stored in Custom too, which
// records an explicit choice (including black) and lets a later theme preset
// replace it instead of being overridden by a stale value.
func applyColorVar(config *Config, name, value string) {
	c, ok := parseColorValue(value)
	if !ok {
		log.Printf("applyColorVar: invalid colour %q", value)
		return
	}
	if !setThemeColorValue(config, name, c) {
		log.Printf("applyColorVar: unknown colour variable %q", name)
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[name] = rgbHex(c)
	log.Printf("[DEBUG] Set %s = %d,%d,%d", name, c.R, c.G, c.B)
}
