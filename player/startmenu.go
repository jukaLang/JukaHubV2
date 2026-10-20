package main

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// App menu (Start)
//
// The old OS-style taskbar is gone: the bottom of the screen now belongs to
// the controller-hint buttons, and every system indicator (battery, clock,
// network, notifications) lives in the top bar. The Apps pill in that bar
// opens this menu, which floats below it like a start menu.
// ──────────────────────────────────────────────────────────────────────

const (
	startMenuW     = int32(380)
	startMenuItemH = int32(40)
	startMenuListY = int32(56)
	startMenuFootH = int32(40) // room for the version line at the bottom
)

type startMenuState struct {
	startRect sdl.Rect // Apps pill hit area, measured by renderStatusBar
	startOpen bool     // menu visible
	cursor    int      // focused menu row (D-pad/keyboard)
	scroll    int      // first visible menu row
	enter     uiEnter  // drop-down + fade entrance
}

var tb startMenuState

// startMenuEntry is a single item in the app menu.
type startMenuEntry struct {
	name   string
	icon   string
	scene  string
	action func(*Config)
}

// getStartMenuApps builds the menu from jukaconfig.json scenes. Non-hidden
// scenes with an icon get listed. Special entries that don't map to a scene
// (Wallpapers, Quick Settings) are appended at the end.
func getStartMenuApps(config *Config) []startMenuEntry {
	var apps []startMenuEntry
	// Order: Main first, then everything except settings sub-scenes.
	preferOrder := []string{
		"Main", "Tube", "LiveTV", "FileExplorer", "Chat", "Calculator",
		"Pomodoro", "Alarm", "Dashboard", "Cron", "Settings",
		"JukaLand", "CanvasSandbox", "Favorites", "Terminal",
		"Dictionary", "PDFReader", "MusicPlayer", "Notes",
		"Games", "Browser", "TextBrowser",
	}
	seen := make(map[string]bool)
	for _, name := range preferOrder {
		for _, sc := range config.Scenes {
			if sc.Name == name {
				if sc.Icon == "" || sc.Name == "Exit" || sc.Name == "Patch" {
					continue
				}
				apps = append(apps, startMenuEntry{
					name:  sceneDisplayName(sc.Name),
					icon:  sc.Icon,
					scene: sc.Name,
				})
				seen[sc.Name] = true
				break
			}
		}
	}
	// Append remaining scenes that weren't in preferOrder.
	for _, sc := range config.Scenes {
		if seen[sc.Name] || sc.Icon == "" || sc.Name == "Exit" || sc.Name == "Patch" {
			continue
		}
		if strings.HasPrefix(sc.Name, "Settings") || strings.HasPrefix(sc.Name, "Theme") {
			continue
		}
		apps = append(apps, startMenuEntry{
			name:  sceneDisplayName(sc.Name),
			icon:  sc.Icon,
			scene: sc.Name,
		})
	}
	// Special entries that open overlays, not scenes.
	apps = append(apps, startMenuEntry{name: "Game Profiles", icon: "G", scene: "", action: func(c *Config) { openGameProfilePanel(false) }})
	apps = append(apps, startMenuEntry{name: "PS Remote Play", icon: "C", scene: "", action: func(c *Config) { openChiakiPanel() }})
	if !IsWindows() {
		apps = append(apps, startMenuEntry{name: windowsAppsEntryName, icon: "W", scene: "", action: func(c *Config) { openWindowsAppsPanel(false) }})
		apps = append(apps, startMenuEntry{name: portMasterEntryName, icon: "P", scene: "", action: func(c *Config) { openPortsPanel(false) }})
	}
	if retroAchievementsSupported() {
		apps = append(apps, startMenuEntry{name: "RetroAchievements", icon: "R", scene: "", action: func(c *Config) { openRetroAchievementsPanel() }})
	}
	apps = append(apps, startMenuEntry{name: "Accessibility", icon: "A", scene: "", action: func(c *Config) { openAccessibilityPanel(c) }})
	apps = append(apps, startMenuEntry{name: "Wallpapers", icon: "P", scene: "", action: func(c *Config) { wpOpen = true; initWallpaperPicker() }})
	apps = append(apps, startMenuEntry{name: "Quick Settings", icon: "Q", scene: "", action: func(c *Config) { qs.open = !qs.open }})
	apps = append(apps, startMenuEntry{name: "About", icon: "i", scene: "About"})
	return apps
}

// startMenuApps is kept for backward compatibility with shortcuts.go.
var startMenuApps []struct{ name, scene string }

// bottomBarHeight is the height of the bottom strip (the controller-hint
// buttons) for the given scene, so content panels can reserve the margin.
func bottomBarHeight(sceneName string) int32 {
	if screenHeight < 600 {
		return HomeFooterHSmall
	}
	return HomeFooterH
}

// startMenuSize returns the menu card's width and height.
func startMenuSize() (int32, int32) {
	menuH := screenHeight - HomeTopBarH - HomeFooterH - 16
	if menuH > 560 {
		menuH = 560
	}
	if menuH < 200 {
		menuH = 200
	}
	return startMenuW, menuH
}

// startMenuOrigin anchors the menu under the Apps pill; without a measured
// pill (first frame) it falls back to the top-left corner of the content area.
func startMenuOrigin() (int32, int32) {
	x := tb.startRect.X
	if x < 0 {
		x = 0
	}
	y := tb.startRect.Y + tb.startRect.H + 8
	if tb.startRect.W <= 0 {
		x, y = 0, HomeTopBarH+8
	}
	if y+menuMinH() > screenHeight-HomeFooterH {
		y = screenHeight - HomeFooterH - menuMinH()
	}
	if y < HomeTopBarH+8 {
		y = HomeTopBarH + 8
	}
	return x, y
}

func menuMinH() int32 {
	return 240
}

// startMenuRows returns how many app rows fit in the menu.
func startMenuRows() int {
	_, menuH := startMenuSize()
	rows := int((menuH - startMenuListY - startMenuFootH) / startMenuItemH)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// renderStartMenu draws the app menu overlay. The focused row (D-pad) and the
// hovered row share the highlight, so touch and keys agree.
func renderStartMenu(renderer *sdl.Renderer, config *Config) {
	if !tb.startOpen {
		return
	}

	font, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	menuW, menuH := startMenuSize()
	menuX, menuY := startMenuOrigin()

	// Entrance keyframes: the scrim fades under the menu while the card itself
	// drops from under the Apps pill and fades as one layer. Reduced motion (or
	// a backend without render targets) draws the settled state.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = tb.enter.progress(uiClockMS())
	} else {
		tb.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiMenuDrop.at(progress)))

	// Dim everything behind the menu, keeping the top bar readable.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, uint8(140*uiScrimProgress(progress, uiMenuEnterMS)))
	renderer.FillRect(&sdl.Rect{X: 0, Y: HomeTopBarH, W: screenWidth, H: screenHeight - HomeTopBarH})

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH}, func() {

		// Menu card.
		UICard(renderer, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH}, UICardStyle{
			Fill:        ColorSurfaceAlt,
			Border:      ColorBorder,
			Radius:      12,
			BorderWidth: 1,
		})
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 90)
		renderer.FillRect(&sdl.Rect{X: menuX, Y: menuY, W: 3, H: menuH})

		// Title.
		renderText(renderer, config, font, "Apps", ColorTextPrimary(), menuX+20, menuY+16)
		if smallFont != nil {
			hint := "Y / Start opens this menu"
			hw, _, _ := smallFont.SizeUTF8(hint)
			renderText(renderer, config, smallFont, hint, ColorTextTertiary(),
				menuX+menuW-int32(hw)-16, menuY+22)
		}

		// Build app list from config.
		apps := getStartMenuApps(config)

		rows := startMenuRows()
		first := tb.scroll
		if first > len(apps)-rows {
			first = len(apps) - rows
		}
		if first < 0 {
			first = 0
		}
		for row := 0; row < rows; row++ {
			i := first + row
			if i >= len(apps) {
				break
			}
			app := apps[i]
			iy := menuY + startMenuListY + int32(row)*startMenuItemH
			if iy+startMenuItemH > menuY+menuH {
				break
			}

			focused := i == tb.cursor
			hovered := mouseX >= menuX && mouseX <= menuX+menuW &&
				mouseY >= iy && mouseY < iy+startMenuItemH
			if focused || hovered {
				// Themed fills so the menu is readable on light and dark palettes.
				fill := WithAlpha(ColorSurfaceRaised, 180)
				if focused {
					fill = WithAlpha(accentColor, 70)
				}
				fillRoundedRect(renderer, menuX+6, iy, menuW-12, startMenuItemH-4, 8, fill)
			}

			iconCol := ColorTextSecondary()
			nameCol := ColorTextPrimary()
			if focused || hovered {
				iconCol = ColorTextPrimary()
				nameCol = ColorTextPrimary()
			}
			renderText(renderer, config, font, app.icon, iconCol, menuX+18, iy+6)
			renderText(renderer, config, font, app.name, nameCol, menuX+52, iy+6)
		}

		// Footer: version + current scene.
		if smallFont != nil {
			ver := versionDisplayString(config)
			renderText(renderer, config, smallFont, ver, ColorTextTertiary(),
				menuX+20, menuY+menuH-28)
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
				sceneName := sceneDisplayName(config.Scenes[currentSceneIndex].Name)
				sw, _, _ := smallFont.SizeUTF8(sceneName)
				renderText(renderer, config, smallFont, sceneName, ColorTextTertiary(),
					menuX+menuW-int32(sw)-20, menuY+menuH-28)
			}
		}

		// Scrollbar when the list is longer than the menu.
		if len(apps) > rows {
			thumbFrac := float64(rows) / float64(len(apps))
			scrollFrac := 0.0
			if len(apps) > rows {
				scrollFrac = float64(first) / float64(len(apps)-rows)
			}
			drawScrollbar(renderer, menuX+menuW-6, menuY+startMenuListY, 3,
				menuH-startMenuListY-startMenuFootH, thumbFrac, scrollFrac)
		}
	})
}

// startMenuIsPointIn tests if a click is inside the app menu card.
func startMenuIsPointIn(x, y int32) bool {
	if !tb.startOpen {
		return false
	}
	menuW, menuH := startMenuSize()
	menuX, menuY := startMenuOrigin()
	return x >= menuX && x <= menuX+menuW && y >= menuY && y <= menuY+menuH
}

// startMenuHandleClick processes a click inside the app menu.
func startMenuHandleClick(x, y int32, config *Config) {
	if !tb.startOpen {
		return
	}

	_, menuY := startMenuOrigin()
	apps := getStartMenuApps(config)
	rows := startMenuRows()
	for row := 0; row < rows; row++ {
		i := tb.scroll + row
		if i >= len(apps) {
			break
		}
		iy := menuY + startMenuListY + int32(row)*startMenuItemH
		if y >= iy && y < iy+startMenuItemH {
			tb.cursor = i
			startMenuActivate(config)
			return
		}
	}
}

// startMenuToggle toggles the app menu open/closed.
func startMenuToggle() {
	tb.startOpen = !tb.startOpen
	if tb.startOpen {
		tb.cursor, tb.scroll = 0, 0
		tb.enter.begin(uiClockMS(), uiMenuEnterMS)
		// The top-bar overlays are exclusive: a dropdown behind the menu
		// would swallow the D-pad as soon as the menu closed.
		nc.open = false
		homeCalendarOpen = false
		dismissWeatherPopover()
	}
	PlayActivateSound()
}

// startMenuDismiss closes the app menu.
func startMenuDismiss() {
	tb.startOpen = false
}

// startMenuNavigate moves the focused row by d, scrolling the window so the
// focused app is always visible.
func startMenuNavigate(d int, config *Config) {
	if !tb.startOpen {
		return
	}
	apps := getStartMenuApps(config)
	if len(apps) == 0 {
		return
	}
	next := tb.cursor + d
	if next < 0 {
		next = 0
	}
	if next > len(apps)-1 {
		next = len(apps) - 1
	}
	if next != tb.cursor {
		tb.cursor = next
		PlayNavSound()
	}
	rows := startMenuRows()
	if tb.cursor < tb.scroll {
		tb.scroll = tb.cursor
	}
	if tb.cursor >= tb.scroll+rows {
		tb.scroll = tb.cursor - rows + 1
	}
}

// startMenuActivate opens the focused entry.
func startMenuActivate(config *Config) {
	apps := getStartMenuApps(config)
	if tb.cursor < 0 || tb.cursor >= len(apps) {
		startMenuDismiss()
		return
	}
	app := apps[tb.cursor]
	tb.startOpen = false
	if app.action != nil {
		app.action(config)
	} else if app.scene != "" {
		if idx := findSceneIndex(config, app.scene); idx >= 0 {
			changeSceneTo(config, idx)
		}
	}
	PlayActivateSound()
}
