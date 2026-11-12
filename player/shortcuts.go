package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Keyboard Shortcuts Reference — the full keyboard list. F1 opens the
// Controls card for the screen on display (controlslegend.go, in keyboard
// keys while the hints name keys); F1 again on that card opens this sheet.
// The pad has no route here: it is the keyboard's reference.
// ──────────────────────────────────────────────────────────────────────

type shortcutEntry struct {
	keys string
	desc string
}

var shortcutsOpen bool

var shortcutCategories = []struct {
	title string
	items []shortcutEntry
}{
	{
		title: "Everywhere",
		items: []shortcutEntry{
			{"Arrows", "Move the selection / scroll"},
			{"Enter / Space", "Open / confirm"},
			{"Esc", "Back / cancel"},
			{"X / Y", "The screen's second and third action"},
			{"PgUp / PgDn", "Page a list"},
			{"Home / End", "First / last row"},
			{"Tab", "Next tab (Shift+Tab: previous)"},
			{"Q / E", "Previous / next screen"},
		},
	},
	{
		title: "Global",
		items: []shortcutEntry{
			{"F1", "Controls for this screen, then this list"},
			{"F10", "Start menu"},
			{"F2", "Search"},
			{"F4", "Voice: talk (hold to talk; Voice command page: speak / stop)"},
			{"Ctrl+Q", "Quick Settings"},
			{"Ctrl+K", "Media search"},
			{"Ctrl+Shift+S", "Shorts feed"},
			{"Ctrl+Shift+V", "Clipboard history"},
			{"Ctrl+L", "Log viewer"},
			{"F6", "Mouse mode (pad: hold Select + L1)"},
			{"F8", "Watch Party"},
			{"F11 / Alt+Enter", "Full screen"},
			{"F12", "Screenshot"},
			{"Alt+F4", "Quit JukaHub (PC)"},
		},
	},
	{
		title: "Home",
		items: []shortcutEntry{
			{"Enter", "Open the focused card"},
			{"/", "Search"},
			{"Q", "Edit Home (pad: L2); in it, X or R resizes a tile"},
			{"E", "Next section"},
			{"Esc", "Close the calendar or the playing video"},
			{"J U K A", "Fireworks"},
		},
	},
	{
		title: "Video player",
		items: []shortcutEntry{
			{"Enter / Space", "Press the selected control"},
			{"← / →", "Walk the control bar"},
			{"Shift+← / →", "Skip -10 / +10 s"},
			{"N / P", "Next / previous video (Shorts too)"},
			{"C or X", "Subtitles on / off"},
			{"S or Y", "Video settings"},
			{"F", "Full screen"},
			{"Esc", "Step down a size"},
			{"Q", "End playback"},
		},
	},
	{
		title: "Music player",
		items: []shortcutEntry{
			{"Enter", "Play the highlighted track"},
			{"P", "Play / pause"},
			{"N / B", "Next / previous track"},
			{"← / →", "Seek -10 / +10 s"},
			{"- / +", "Volume"},
			{"L / V", "Lyrics / visualizer"},
			{"M / R", "Shuffle / repeat"},
			{"T", "Sleep timer"},
			{"G", "Equalizer"},
			{"/ or F2", "Search the library"},
		},
	},
	{
		title: "Browser",
		items: []shortcutEntry{
			{"Tab", "Scroll mode / link mode"},
			{"← / →", "Walk the links on the page"},
			{"Enter", "Open the link / edit the address"},
			{"PgUp / PgDn", "Scroll a page"},
			{"/ or Y", "Search the web"},
			{"- / +", "Zoom the text"},
			{"F", "Find on the page (N / P: next / previous)"},
		},
	},
	{
		title: "Terminal",
		items: []shortcutEntry{
			{"Enter", "Run the command"},
			{"Tab", "Complete"},
			{"↑ / ↓", "Command history"},
			{"F7 / Ctrl+R", "Search the history"},
			{"PgUp / PgDn", "Scroll the output"},
			{"Ctrl+C", "Stop the running command"},
			{"Ctrl+L / Ctrl+U", "Clear the screen / the line"},
		},
	},
	{
		title: "Tools",
		items: []shortcutEntry{
			{"Notes", "N or Y new, X delete, F folder, S search"},
			{"Calculator", "G or Y graph, X insert x, C clear"},
			{"Pomodoro", "Enter start / pause, X skip, Y reset"},
			{"Synth", "Space play, Tab page, [ ] pattern, Enter / Y / X edit, 1-7 pads"},
			{"Alarm", "Y new alarm, X on / off, Del delete, ↑ ↓ adjust"},
			{"Watch Party", "Enter host / join / play, X remove"},
		},
	},
}

// handleGlobalKeys runs the keyboard keys that hold on every screen before the
// screen sees them: F1 (the Controls card for this screen, then this list), F2
// (search), F4 (the voice orb — voiceorb.go reads it before the panels and
// stands aside for a modal one, which keeps it here too; Alt+F4 stays the
// window's quit), F6 (mouse mode, the twin of the pad's hold
// Select + L1 — vcursor_mode.go), F12 (screenshot) and Ctrl+Q (Quick
// Settings). They used to be read after the scene's own dispatch, which a
// screen that consumes every key (Notes, the calculator, Pomodoro) or has
// nothing focusable never
// reached, so keys the Controls card lists under "Every screen" did nothing
// there. The music player keeps F2 for its own library search. Key repeats are
// ignored, so a held key does not open and close a panel over and over.
func handleGlobalKeys(renderer *sdl.Renderer, e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 {
		return false
	}
	switch {
	case e.Keysym.Sym == sdl.K_F1:
		globalSearchOpen = false
		openControlsLegend()
		if !controlsLegend.open {
			shortcutsOpen = !shortcutsOpen
		}
		return true
	case e.Keysym.Sym == sdl.K_F2:
		if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
			config.Scenes[currentSceneIndex].Name == "MusicPlayer" {
			return false
		}
		shortcutsOpen = false
		homeSearchToggle(config)
		return true
	case e.Keysym.Sym == sdl.K_F4 && e.Keysym.Mod&(sdl.KMOD_ALT|sdl.KMOD_CTRL) == 0:
		// The voice orb's key (voiceorb.go) is read before every panel
		// (main.go); it only gets here when the orb could not come up.
		return voiceOrbF4(config)
	case e.Keysym.Sym == sdl.K_F6:
		return vcursorModeKeyToggle(config)
	case e.Keysym.Sym == sdl.K_F12:
		captureScreenshot(renderer)
		return true
	case e.Keysym.Sym == sdl.K_q && e.Keysym.Mod&sdl.KMOD_CTRL != 0:
		qsToggleOpen()
		return true
	}
	return false
}

// The sheet scrolls: its categories are taller than the card on a 720-line
// canvas. These are written by the renderer (the measured layout) and read by
// the key handlers. Main thread only.
var (
	shortcutsScroll    int32
	shortcutsScrollMax int32
	shortcutsRowStep   int32 = 22
	shortcutsPageStep  int32 = 400
)

// shortcutsScrollBy moves the sheet and keeps it inside its content.
func shortcutsScrollBy(d int32) {
	shortcutsScroll += d
	if shortcutsScroll > shortcutsScrollMax {
		shortcutsScroll = shortcutsScrollMax
	}
	if shortcutsScroll < 0 {
		shortcutsScroll = 0
	}
}

// renderShortcutsOverlay draws the sheet in the glass overlay style: the
// shared page header, then the categories in two columns, each category
// placed in the shorter column. Every row is a key chip and its action. The
// content scrolls under the header (Up/Down, PgUp/PgDn, Home/End). The old
// sheet never advanced a row, so each category's entries were drawn on top of
// one another, and it cut off what did not fit.
func renderShortcutsOverlay(renderer *sdl.Renderer, config *Config) {
	if !shortcutsOpen {
		return
	}
	cardW := screenWidth - 160
	if cardW > 1100 {
		cardW = 1100
	}
	cardH := screenHeight - 80
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)

	subtitle := "F1 or Esc closes this sheet"
	if shortcutsScrollMax > 0 {
		subtitle += " · Up / Down scroll"
	}
	header := overlayPageHeader(card, "Keyboard shortcuts", subtitle, "settings")
	headerH := UIPanelHeaderBand(config, header, 10)
	UIPageHeaderDraw(renderer, config, header)

	font := FontForRole(config, TypeCaption)
	if font == nil {
		return
	}
	fh := int32(font.Height())
	rowH := fh + 8
	titleH := fh + 6
	colGap := OverlayPadX
	colW := (card.W - 2*OverlayPadX - colGap) / 2
	keyW := int32(0)
	for _, cat := range shortcutCategories {
		for _, it := range cat.items {
			if w, _ := measureText(font, it.keys); w > keyW {
				keyW = w
			}
		}
	}
	keyW += 16
	if keyW > colW*2/5 {
		keyW = colW * 2 / 5
	}
	top := card.Y + headerH
	bottom := card.Y + card.H - 16
	if bottom <= top {
		return
	}

	// Lay out first (content coordinates from 0), so the scroll range is known
	// before anything is drawn.
	type placed struct {
		cat  int
		x, y int32
	}
	var places [16]placed
	n := 0
	colX := [2]int32{card.X + OverlayPadX, card.X + OverlayPadX + colW + colGap}
	colY := [2]int32{0, 0}
	for i, cat := range shortcutCategories {
		if n == len(places) {
			break
		}
		c := 0
		if colY[1] < colY[0] {
			c = 1
		}
		places[n] = placed{cat: i, x: colX[c], y: colY[c]}
		n++
		colY[c] += titleH + int32(len(cat.items))*rowH + 12
	}
	content := colY[0]
	if colY[1] > content {
		content = colY[1]
	}
	view := bottom - top
	shortcutsScrollMax = content - view
	if shortcutsScrollMax < 0 {
		shortcutsScrollMax = 0
	}
	shortcutsRowStep = rowH
	shortcutsPageStep = view - rowH
	if shortcutsPageStep < rowH {
		shortcutsPageStep = rowH
	}
	shortcutsScrollBy(0)

	clip := sdl.Rect{X: card.X, Y: top, W: card.W, H: view}
	_ = renderer.SetClipRect(&clip)
	accent := getAccentColor(config)
	for _, pl := range places[:n] {
		cat := shortcutCategories[pl.cat]
		x, y := pl.x, top+pl.y-shortcutsScroll
		if y+titleH > top && y < bottom {
			drawGlassText(renderer, config, font, strings.ToUpper(cat.title), accent, x, y)
		}
		y += titleH
		for _, it := range cat.items {
			if y+rowH > top && y < bottom {
				chip := sdl.Rect{X: x, Y: y, W: keyW, H: rowH - 4}
				fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, glassChipFill())
				strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, 1, glassChipEdge())
				keys := ellipsize(font, it.keys, keyW-12)
				kw, _ := measureText(font, keys)
				textY := chip.Y + (chip.H-fh)/2
				drawGlassText(renderer, config, font, keys, glassChipInk(), chip.X+(chip.W-kw)/2, textY)
				drawGlassText(renderer, config, font, ellipsize(font, it.desc, colW-keyW-12), ColorTextSecondary(), x+keyW+12, textY)
			}
			y += rowH
		}
	}
	_ = renderer.SetClipRect(nil)
	if shortcutsScrollMax > 0 && content > 0 {
		drawScrollbar(renderer, card.X+card.W-8, top, 3, view,
			float64(view)/float64(content), float64(shortcutsScroll)/float64(shortcutsScrollMax))
	}
}

// handleShortcutsInput owns every key while the sheet is up: F1 or Esc closes
// it (B does not — its letter would be typed into a field behind the sheet),
// Up/Down/PgUp/PgDn/Home/End scroll it.
func handleShortcutsInput(event *sdl.KeyboardEvent) bool {
	if !shortcutsOpen {
		return false
	}
	if event == nil || event.Type != sdl.KEYDOWN {
		return true
	}
	switch event.Keysym.Sym {
	case sdl.K_F1, sdl.K_ESCAPE:
		if event.Repeat == 0 {
			shortcutsOpen = false
			shortcutsScroll = 0
		}
	case sdl.K_UP:
		shortcutsScrollBy(-shortcutsRowStep)
	case sdl.K_DOWN:
		shortcutsScrollBy(shortcutsRowStep)
	case sdl.K_PAGEUP:
		shortcutsScrollBy(-shortcutsPageStep)
	case sdl.K_PAGEDOWN:
		shortcutsScrollBy(shortcutsPageStep)
	case sdl.K_HOME:
		shortcutsScroll = 0
	case sdl.K_END:
		shortcutsScroll = shortcutsScrollMax
	}
	return true // consume all input while open
}

// openLogScene jumps to the shipped log viewer. It reports false when the
// config has no LogExporter scene, so the shortcut can stay unhandled instead
// of consuming a key that does nothing.
func openLogScene(config *Config) bool {
	if config == nil {
		return false
	}
	idx := sceneIndexExact(config, "LogExporter")
	if idx < 0 {
		return false
	}
	changeSceneTo(config, idx)
	return true
}

// handleLogShortcut implements Ctrl+L for the in-app log viewer. The terminal
// keeps its own Ctrl+L (clear the output), so the global shortcut stands aside
// there; every other scene gets it.
func handleLogShortcut(event *sdl.KeyboardEvent, config *Config, inTerminal bool) bool {
	if event == nil || event.Type != sdl.KEYDOWN || inTerminal {
		return false
	}
	if event.Keysym.Sym != sdl.K_l || (event.Keysym.Mod&sdl.KMOD_CTRL) == 0 {
		return false
	}
	return openLogScene(config)
}

// renderShortcutHint shows Xbox 360 controller hints at the bottom of scenes.
func renderShortcutHint(renderer *sdl.Renderer, config *Config) {
	// Controller hints are rendered by renderFooter in extras.go.
	// Kept as no-op to avoid breaking callers.
	_ = renderer
	_ = config
}
