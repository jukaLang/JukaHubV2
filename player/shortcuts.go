package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Keyboard Shortcuts Reference — overlay showing all keybindings
// Toggle with F1 from any scene
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
		title: "Navigation",
		items: []shortcutEntry{
			{"← → ^ v", "Navigate / scroll"},
			{"A / Enter", "Select / confirm"},
			{"B / Esc", "Back / cancel"},
			{"X", "Secondary action (delete, toggle)"},
			{"Y", "Create / new"},
		},
	},
	{
		title: "Media & video",
		items: []shortcutEntry{
			{"L1 / R1", "Page lists; skip -10/+10 s in the player"},
			{"L2 / R2", "Results page; previous / next video"},
			{"N / P", "Next / previous video (Shorts feed too)"},
			{"PgUp/PgDn", "Previous / next results page"},
			{"Shift+Left/Right", "Skip -10 / +10 s"},
		},
	},
	{
		title: "Browser",
		items: []shortcutEntry{
			{"← →", "Walk links on the page"},
			{"A", "Open the link / edit the address"},
			{"Y", "Search the web"},
			{"X", "Full link list"},
			{"L1 / R1", "Scroll a page"},
			{"Up / Down at the ends", "Reach the header and the button row"},
		},
	},
	{
		title: "Global",
		items: []shortcutEntry{
			{"Select + Start", "Quit JukaHub (TrimUI)"},
			{"F1", "Keyboard shortcuts"},
			{"F2", "Search"},
			{"F5", "Refresh"},
			{"F11", "Toggle fullscreen"},
			{"Ctrl+Q", "Quick settings"},
			{"Ctrl+Shift+S", "Shorts feed (Media)"},
			{"Ctrl+L", "Log viewer"},
			{"PgUp/PgDn", "Page up / down"},
		},
	},
	{
		title: "Home Screen",
		items: []shortcutEntry{
			{"A", "Open focused tile"},
			{"B", "Show settings"},
			{"Y", "Toggle theme"},
			{"X", "Favorites menu"},
			{"J U K A", "Easter egg fireworks!"},
		},
	},
	{
		title: "Terminal",
		items: []shortcutEntry{
			{"Type command", "Run any command"},
			{"Tab", "Auto-complete"},
			{"^ / v", "Command history"},
			{"PgUp/PgDn", "Scroll output"},
			{"help", "Built-in commands"},
		},
	},
	{
		title: "Music Player",
		items: []shortcutEntry{
			{"A", "Play highlighted track"},
			{"X / Y", "Play / pause"},
			{"← →", "Seek −10 s / +10 s"},
			{"L1 / R1", "Previous / next track"},
			{"L3 / R3", "Lyrics / sleep timer"},
		},
	},
	{
		title: "Watch Party",
		items: []shortcutEntry{
			{"F8", "Open / close the LAN room"},
			{"A / Enter", "Host, join or play a queued item"},
			{"X", "Remove the highlighted queue item"},
			{"B / Esc", "Close the panel"},
		},
	},
	{
		title: "Community & support",
		items: []shortcutEntry{
			{"Tools → Community", "Changelog, bug report, diagnostics switch"},
			{"A / Enter", "Select a row or go back from the changelog"},
			{"↑ / ↓", "Move or scroll"},
			{"B / Esc", "Close"},
		},
	},
	{
		title: "Pomodoro",
		items: []shortcutEntry{
			{"A", "Start / pause"},
			{"B", "Skip to break"},
			{"Y", "Reset session"},
		},
	},
	{
		title: "Alarm Clock",
		items: []shortcutEntry{
			{"A", "Select / save"},
			{"Y", "New alarm"},
			{"X", "Toggle alarm"},
			{"^ v", "Adjust time"},
		},
	},
}

func renderShortcutsOverlay(renderer *sdl.Renderer, config *Config) {
	if !shortcutsOpen {
		return
	}

	// Full-screen dim
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 200)

	// Main card
	cardW := screenWidth - 200
	cardH := screenHeight - 120
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	drawRoundedRect(renderer, &sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, 16, 22, 22, 28, 245)

	// Title
	titleFont := getDisplayFont(config, 28)
	titleText := "[Keyboard] Shortcuts"
	titleW, _, _ := titleFont.SizeUTF8(titleText)
	drawText(renderer, titleFont, titleText, cardX+(cardW-int32(titleW))/2, cardY+20, getAccentColor(config), textAlignLeft)

	// Close hint
	closeFont := getBodyFont(config, 14)
	closeText := "Press F1 or B to close"
	closeW, _, _ := closeFont.SizeUTF8(closeText)
	drawText(renderer, closeFont, closeText, cardX+(cardW-int32(closeW))/2, cardY+56, colorNew(140, 140, 155, 255), textAlignLeft)

	// Column layout
	colW := (cardW - 60) / 2
	startY := cardY + 80
	cols := []int32{cardX + 20, cardX + 20 + colW + 20}

	keyFont := getBodyFont(config, 13)
	descFont := getBodyFont(config, 13)

	itemIdx := 0
	for _, cat := range shortcutCategories {
		col := itemIdx % 2
		colX := cols[col]

		// Calculate column Y offset by counting items before this in the same column
		colY := startY
		for _, prevCat := range shortcutCategories {
			if prevCat.title == cat.title {
				break
			}
			for j := 0; j < len(prevCat.items); j++ {
				if itemIdx%2 == col {
					colY += 22
				}
				itemIdx++
			}
			itemIdx++ // category header
		}

		// Category title
		drawText(renderer, keyFont, strings.ToUpper(cat.title), colX, colY, getAccentColor(config), textAlignLeft)
		colY += 22

		for _, item := range cat.items {
			if colY+22 > cardY+cardH-20 {
				break
			}
			// Key badge
			drawRoundedRect(renderer, &sdl.Rect{X: colX, Y: colY, W: 140, H: 18}, 4, 35, 35, 45, 255)
			drawText(renderer, keyFont, item.keys, colX+6, colY+2, colorNew(180, 180, 200, 255), textAlignLeft)
			// Description
			drawText(renderer, descFont, item.desc, colX+150, colY+2, colorNew(200, 200, 215, 255), textAlignLeft)
		}
	}

	// Footer
	footerText := "F1 / Esc / B to close"
	footerW, _, _ := closeFont.SizeUTF8(footerText)
	drawText(renderer, closeFont, footerText, cardX+(cardW-int32(footerW))/2, cardY+cardH-30, colorNew(120, 120, 135, 255), textAlignLeft)
}

func handleShortcutsInput(event *sdl.KeyboardEvent) bool {
	if !shortcutsOpen {
		return false
	}
	if event.Type == sdl.KEYDOWN {
		switch event.Keysym.Sym {
		case sdl.K_F1, sdl.K_ESCAPE, sdl.K_b:
			shortcutsOpen = false
			return true
		}
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
