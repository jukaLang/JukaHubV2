package main

// ipstreams.go — the IP stream library behind the IPStream screen.
//
// The screen used to have a single URL box: a stream was forgotten as soon as
// the next link was typed. This keeps a small library in the app data directory,
// lets the user add and remove entries, and can bulk-import public IPTV channels
// from the iptv-org playlist on GitHub.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// IPStream is one saved stream: a display name and a playable URL.
type IPStream struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

const (
	// ipStreamGitHubURL is the public playlist the "import" action reads.
	ipStreamGitHubURL = "https://iptv-org.github.io/iptv/index.m3u"
	// ipStreamImportLimit keeps the import from filling the SD card with a
	// ten-thousand-entry playlist.
	ipStreamImportLimit = 200
	// ipStreamLibraryFileName is the persisted library file.
	ipStreamLibraryFileName = "ipstreams.json"
)

var (
	ipStreamsMu     sync.Mutex
	ipStreams       []IPStream
	ipStreamsLoaded bool
	ipStreamsCursor int
	ipPanelOpen     bool
	ipPanelStatus   string
)

// ipStreamsFilePath is the library location. It sits next to the other app data
// so it survives updates (which never touch the data directory).
func ipStreamsFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ipStreamLibraryFileName)
}

// loadIPStreams reads the library once. A missing file is an empty library.
func loadIPStreams() {
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	if ipStreamsLoaded {
		return
	}
	ipStreamsLoaded = true
	data, err := os.ReadFile(ipStreamsFilePath())
	if err != nil {
		return
	}
	var list []IPStream
	if err := json.Unmarshal(data, &list); err != nil {
		log.Printf("[IPSTREAM] ignoring unreadable library: %v", err)
		return
	}
	ipStreams = sanitizeIPStreams(list)
}

// sanitizeIPStreams drops unusable entries and duplicates, keeping the first
// occurrence of each URL.
func sanitizeIPStreams(list []IPStream) []IPStream {
	seen := map[string]bool{}
	out := make([]IPStream, 0, len(list))
	for _, s := range list {
		url := strings.TrimSpace(s.URL)
		if !looksLikeStreamURL(url) || seen[url] {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = url
		}
		seen[url] = true
		out = append(out, IPStream{Name: name, URL: url})
	}
	return out
}

// saveIPStreams persists the library atomically.
func saveIPStreams() error {
	ipStreamsMu.Lock()
	list := append([]IPStream(nil), ipStreams...)
	ipStreamsMu.Unlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := ipStreamsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ipStreamList returns a copy of the saved streams.
func ipStreamList() []IPStream {
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	return append([]IPStream(nil), ipStreams...)
}

// addIPStream stores a stream. It reports whether the entry was added (a
// duplicate URL is refused rather than appended twice).
func addIPStream(name, url string) bool {
	url = strings.TrimSpace(url)
	if !looksLikeStreamURL(url) {
		return false
	}
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	for _, s := range ipStreams {
		if s.URL == url {
			return false
		}
	}
	if strings.TrimSpace(name) == "" {
		name = url
	}
	ipStreams = append(ipStreams, IPStream{Name: strings.TrimSpace(name), URL: url})
	return true
}

// removeIPStreamAt deletes one entry.
func removeIPStreamAt(idx int) bool {
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	if idx < 0 || idx >= len(ipStreams) {
		return false
	}
	ipStreams = append(ipStreams[:idx], ipStreams[idx+1:]...)
	if ipStreamsCursor >= len(ipStreams) {
		ipStreamsCursor = len(ipStreams) - 1
	}
	if ipStreamsCursor < 0 {
		ipStreamsCursor = 0
	}
	return true
}

// parseM3UStreams extracts channel entries from an M3U playlist. Entries without
// an http(s) URL are skipped (the player cannot open them).
func parseM3UStreams(data string, limit int) []IPStream {
	var out []IPStream
	name := ""
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF") {
			// #EXTINF:-1 tvg-id="..." group-title="...",Channel Name
			if comma := strings.LastIndex(line, ","); comma >= 0 && comma+1 < len(line) {
				name = strings.TrimSpace(line[comma+1:])
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "http://") && !strings.HasPrefix(strings.ToLower(line), "https://") {
			name = ""
			continue
		}
		entry := IPStream{Name: name, URL: line}
		if entry.Name == "" {
			entry.Name = line
		}
		out = append(out, entry)
		name = ""
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ipStreamFetchFn downloads the playlist; tests replace it.
var ipStreamFetchFn = func(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("playlist replied %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// importIPStreamsFromGitHub downloads the public playlist and merges new
// channels into the library. It reports how many entries were added.
func importIPStreamsFromGitHub(ctx context.Context, url string) (int, error) {
	if strings.TrimSpace(url) == "" {
		url = ipStreamGitHubURL
	}
	data, err := ipStreamFetchFn(ctx, url)
	if err != nil {
		return 0, err
	}
	list := parseM3UStreams(data, ipStreamImportLimit)
	if len(list) == 0 {
		return 0, fmt.Errorf("no channels found in the playlist")
	}
	added := 0
	for _, s := range list {
		if addIPStream(s.Name, s.URL) {
			added++
		}
	}
	if added > 0 {
		if err := saveIPStreams(); err != nil {
			return added, err
		}
	}
	return added, nil
}

// ipPanelRows builds the panel rows: a save row followed by the library.
func ipPanelRows() []IPStream {
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	return append([]IPStream(nil), ipStreams...)
}

// openIPStreamPanel shows the library panel.
func openIPStreamPanel() {
	qs.open = false
	toolsPanel.open = false
	processPanel.open = false
	loadIPStreams()
	ipPanelOpen = true
	ipPanelStatus = ""
	PlayActivateSound()
}

func closeIPStreamPanel() {
	ipPanelOpen = false
	PlayBackSound()
}

// ipPanelActivateAct runs an explicit action on the highlighted row. The first
// row (index -1) saves whatever link the scene's URL box holds.
func ipPanelSaveCurrentLink(config *Config) {
	url := ""
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		url = sceneInputURL(config, config.Scenes[currentSceneIndex])
	}
	if url == "" {
		ShowToast("Type a stream link in the URL box first", ToastKindWarning)
		return
	}
	name := channelNameFromURL(url)
	if !addIPStream(name, url) {
		ShowToast("That stream is already saved", ToastKindInfo)
		return
	}
	if err := saveIPStreams(); err != nil {
		ShowToast("Could not save the stream: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Saved "+name, ToastKindSuccess)
}

// ipPanelPlay plays the highlighted stream.
func ipPanelPlay(config *Config) {
	list := ipPanelRows()
	if ipStreamsCursor < 0 || ipStreamsCursor >= len(list) {
		return
	}
	entry := list[ipStreamsCursor]
	ipPanelOpen = false
	playVideoURL(config, entry.URL)
}

// ipPanelRemove deletes the highlighted stream.
func ipPanelRemove() {
	list := ipPanelRows()
	if ipStreamsCursor < 0 || ipStreamsCursor >= len(list) {
		return
	}
	name := list[ipStreamsCursor].Name
	if !removeIPStreamAt(ipStreamsCursor) {
		return
	}
	if err := saveIPStreams(); err != nil {
		ShowToast("Could not update the library: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast("Removed "+name, ToastKindInfo)
}

// ipPanelImport fetches the GitHub playlist in the background.
func ipPanelImport() {
	ipPanelStatus = "Importing from GitHub..."
	ShowToast("Importing channels from GitHub...", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		added, err := importIPStreamsFromGitHub(ctx, "")
		if err != nil {
			ipPanelStatus = "Import failed: " + firstLine(err.Error())
			showToast("Import failed: "+firstLine(err.Error()), ToastError())
			return
		}
		ipPanelStatus = fmt.Sprintf("Imported %d channels", added)
		showToast(fmt.Sprintf("Imported %d channels", added), ToastSuccess())
	}()
}

// channelNameFromURL derives a readable label from a stream URL.
func channelNameFromURL(url string) string {
	s := strings.TrimSpace(url)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "rtsp://")
	s = strings.TrimPrefix(s, "rtmp://")
	s = strings.TrimPrefix(s, "udp://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "stream"
	}
	return s
}

// ctrlIPPanel is called before every other controller overlay.
func ctrlIPPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	list := ipPanelRows()
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if ipStreamsCursor > 0 {
			ipStreamsCursor--
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if ipStreamsCursor < len(list)-1 {
			ipStreamsCursor++
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		ipPanelPlay(config)
	case sdl.CONTROLLER_BUTTON_X:
		ipPanelSaveCurrentLink(config)
	case sdl.CONTROLLER_BUTTON_Y:
		ipPanelImport()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeIPStreamPanel()
	case sdl.CONTROLLER_BUTTON_START:
		if len(list) > 0 {
			ipPanelRemove()
		}
	}
	return true
}

// handleIPPanelKey consumes every key while the panel is open.
func handleIPPanelKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !ipPanelOpen {
		return false
	}
	list := ipPanelRows()
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if ipStreamsCursor > 0 {
			ipStreamsCursor--
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if ipStreamsCursor < len(list)-1 {
			ipStreamsCursor++
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		ipPanelPlay(config)
	case sdl.K_a:
		ipPanelSaveCurrentLink(config)
	case sdl.K_y:
		ipPanelImport()
	case sdl.K_DELETE, sdl.K_BACKSPACE:
		if len(list) > 0 {
			ipPanelRemove()
		}
	case sdl.K_ESCAPE, sdl.K_b:
		closeIPStreamPanel()
	}
	return true
}

// ipPanelRowRects is filled by renderIPPanel so a click maps back to a row.
var ipPanelRowRects []sdl.Rect

var ipPanelRect sdl.Rect

// handleIPPanelMouse activates a clicked row, or closes on an outside click.
func handleIPPanelMouse(mx, my int32, config *Config) bool {
	if !ipPanelOpen {
		return false
	}
	for i, r := range ipPanelRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			ipStreamsCursor = i
			ipPanelPlay(config)
			return true
		}
	}
	if ipPanelRect.W > 0 && !pointInRect(mx, my, ipPanelRect) {
		closeIPStreamPanel()
	}
	return true
}

// renderIPPanel draws the library panel.
func renderIPPanel(renderer *sdl.Renderer, config *Config) {
	if !ipPanelOpen || renderer == nil {
		return
	}
	list := ipPanelRows()
	if ipStreamsCursor >= len(list) {
		ipStreamsCursor = len(list) - 1
	}
	if ipStreamsCursor < 0 {
		ipStreamsCursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	const padX = int32(24)
	lineH := smallH + 10
	visible := 10
	if screenHeight < 700 {
		visible = 8
	}
	if len(list) < visible {
		visible = len(list)
	}
	if visible < 1 {
		visible = 1
	}

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	sub := fmt.Sprintf("%d saved streams", len(list))
	if ipPanelStatus != "" {
		sub = ipPanelStatus
	}
	header := UIPanelHeader("IPStream Library", sub, "media")
	headerH := UIPanelHeaderBand(config, header, 8)
	footerH := 20 + smallH
	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + int32(visible)*lineH + footerH + smallH + 6
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	ipPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	UIPageHeaderDraw(renderer, config, header)

	rowW := panelW - 2*padX
	focusColor := installHighlight()
	ipPanelRowRects = make([]sdl.Rect, 0, visible)
	rowY := panelY + headerH
	if len(list) == 0 {
		renderText(renderer, config, font, "No streams yet — type a link, then press X to save it.",
			ColorTextTertiary(), panelX+padX, rowY)
	}
	first := 0
	if ipStreamsCursor >= visible {
		first = ipStreamsCursor - visible + 1
	}
	for i := 0; i < visible; i++ {
		idx := first + i
		if idx >= len(list) {
			break
		}
		entry := list[idx]
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		ipPanelRowRects = append(ipPanelRowRects, rect)
		focused := idx == ipStreamsCursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		textColor := ColorTextSecondary()
		if focused {
			textColor = ColorTextPrimary()
		}
		name := ellipsize(font, entry.Name, rowW-24)
		renderText(renderer, config, font, name, textColor, rect.X+12, rowY+3)
		url := ellipsize(font, entry.URL, rowW/2)
		uw, _, _ := font.SizeUTF8(url)
		renderText(renderer, config, font, url, ColorTextTertiary(), rect.X+rect.W-int32(uw)-12, rowY+3)
		rowY += lineH
	}

	footer := hintLine(FooterHint{"A", "Play"}, FooterHint{"X", "Save Link"},
		FooterHint{"Y", "Import from GitHub"}, FooterHint{"Start", "Remove"}, FooterHint{"B", "Close"})
	if fw, _, _ := font.SizeUTF8(footer); int32(fw) > rowW {
		footer = hintLine(FooterHint{"A", "Play"}, FooterHint{"X", "Save"},
			FooterHint{"Y", "Import"}, FooterHint{"Start", "Remove"}, FooterHint{"B", "Close"})
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// ensureIPStreamEntries gives the IPStream scene a library button and the Media
// (Tube) screen an IPStream entry point, in memory only.
func ensureIPStreamEntries(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		switch scene.Name {
		case "IPStream":
			if sceneHasTriggerElement(*scene, "ip_open") {
				continue
			}
			scene.Elements = append(scene.Elements, Element{
				Type: "button", Text: "Library", Color: "$labelColor", BgColor: "$buttonColor",
				X: 912, Y: 72, Width: "160", Height: "48", Trigger: "ip_open",
			})
		case "Tube":
			if sceneHasTriggerElement(*scene, "ip_open") {
				continue
			}
			// Sit after the last button on the search row.
			x := int32(1000)
			for _, e := range scene.Elements {
				if e.Type == "button" && e.Y == 72 {
					if right := e.X + getElementWidth(e, 116) + 8; right > x {
						x = right
					}
				}
			}
			// Clamp against the config's own canvas, not the global screenWidth:
			// this runs while the config loads, before the window size is known,
			// so screenWidth was still 0 and pushed the button off the left edge
			// (very visible on the Brick, where it should follow Shorts).
			canvasW := int32(config.Width)
			if canvasW <= 0 {
				canvasW = int32(config.Variables.ScreenWidth)
			}
			if canvasW <= 0 {
				canvasW = 1280
			}
			if x+116 > canvasW-16 {
				x = canvasW - 16 - 116
			}
			scene.Elements = append(scene.Elements, Element{
				Type: "button", Text: "IPStream", Color: "$labelColor", BgColor: "$buttonColor",
				X: x, Y: 72, Width: "116", Height: "48", Trigger: "ip_open",
			})
		}
	}
}

// sceneHasTriggerElement reports whether any element already uses trigger.
func sceneHasTriggerElement(scene SceneConfig, trigger string) bool {
	for _, e := range scene.Elements {
		if e.Trigger == trigger {
			return true
		}
	}
	return false
}
