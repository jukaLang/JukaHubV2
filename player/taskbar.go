package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// OS-style Taskbar (bottom of every non-home scene)
// ──────────────────────────────────────────────────────────────────────

const (
	taskbarH   = 44
	taskbarPad = 12
)

type taskbarState struct {
	startRect   sdl.Rect // Start button hit area
	bellRect    sdl.Rect // bell icon area
	batteryPct  int
	batteryTime string
	startOpen   bool // Start menu visible
}

var tb taskbarState

// startMenuEntry is a single item in the OS-style Start menu.
type startMenuEntry struct {
	name   string
	icon   string
	scene  string
	action func(*Config)
}

// getStartMenuApps builds the start menu from jukaconfig.json scenes.
// Non-hidden scenes with an icon get listed. Special entries that don't
// map to a scene (Wallpapers, Quick Settings) are appended at the end.
func getStartMenuApps(config *Config) []startMenuEntry {
	var apps []startMenuEntry
	// Order: Main first, then everything except settings sub-scenes.
	preferOrder := []string{
		"Main", "Tube", "FileExplorer", "Chat", "Calculator",
		"Pomodoro", "Alarm", "Dashboard", "Cron", "Settings",
		"JukaLand", "CanvasSandbox", "Favorites", "Terminal",
		"Dictionary", "PDFReader", "MusicPlayer", "Notes",
		"Games", "TextBrowser",
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
	apps = append(apps, startMenuEntry{name: "Wallpapers", icon: "P", scene: "", action: func(c *Config) { wpOpen = true; initWallpaperPicker() }})
	apps = append(apps, startMenuEntry{name: "Quick Settings", icon: "Q", scene: "", action: func(c *Config) { qs.open = !qs.open }})
	apps = append(apps, startMenuEntry{name: "About", icon: "i", scene: "About"})
	return apps
}

// startMenuApps is kept for backward compatibility with shortcuts.go.
var startMenuApps []struct{ name, scene string }

// sceneUsesTaskbar reports whether the given scene renders the OS taskbar
// instead of the controller-hint footer. The two share the same screen
// real-estate so only one should render per frame.
func sceneUsesTaskbar(sceneName string) bool {
	switch sceneName {
	case "JukaLand", "Pomodoro", "Calculator", "Alarm", "Terminal", "Patch":
		return false
	default:
		return true
	}
}

// taskbarHeight returns the bar height used by the active bottom strip
// (taskbar or footer) for the given scene, so content panels can reserve
// the correct margin.
func taskbarHeight(sceneName string) int32 {
	if sceneUsesTaskbar(sceneName) {
		return taskbarH
	}
	return HomeFooterH
}

// renderTaskbar draws a persistent bottom bar across non-home scenes.
func renderTaskbar(renderer *sdl.Renderer, config *Config) {
	barY := screenHeight - taskbarH
	barW := screenWidth

	// Opaque surface.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(10, 13, 22, 240)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barY, W: barW, H: taskbarH})

	// Top hairline.
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 60)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barY, W: barW, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// --- Left: Start button ---
	startLabel := ">> JukaHub"
	sw, sh, _ := font.SizeUTF8(startLabel)
	startW := int32(sw) + 24
	startH := int32(taskbarH - 12)
	startX := int32(taskbarPad)
	startY := barY + (int32(taskbarH)-startH)/2

	startFill := sdl.Color{R: 20, G: 24, B: 36, A: 200}
	if tb.startOpen {
		startFill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60}
	}
	if mouseX >= startX && mouseX <= startX+startW && mouseY >= startY && mouseY <= startY+startH {
		startFill = sdl.Color{R: 30, G: 36, B: 50, A: 220}
	}
	fillRoundedRect(renderer, startX, startY, startW, startH, 8, startFill)
	strokeRoundedRect(renderer, startX, startY, startW, startH, 8, 1,
		sdl.Color{R: 60, G: 70, B: 90, A: 150})
	renderText(renderer, config, font, startLabel,
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
		startX+12, startY+(startH-int32(sh))/2)
	tb.startRect = sdl.Rect{X: startX, Y: startY, W: startW, H: startH}

	// --- Center: active scene name ---
	if currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) {
		sceneName := sceneDisplayName(appConfig.Scenes[currentSceneIndex].Name)
		nw, _, _ := font.SizeUTF8(sceneName)
		nx := (barW - int32(nw)) / 2
		ny := barY + (taskbarH-int32(font.Height()))/2
		renderText(renderer, config, font, sceneName,
			sdl.Color{R: 140, G: 155, B: 180, A: 180}, nx, ny)
	}

	// --- Right: system tray (battery, wifi, clock, notification bell) ---
	rightX := barW - taskbarPad

	// Battery.
	batStr := batteryDisplayString()
	bw, _, _ := font.SizeUTF8(batStr)
	rightX -= int32(bw)
	trayY := barY + (taskbarH-int32(font.Height()))/2
	renderText(renderer, config, font, batStr,
		sdl.Color{R: 160, G: 175, B: 200, A: 200}, rightX, trayY)
	rightX -= 16

	// WiFi indicator.
	wifiStr := wifiIconString()
	ww, _, _ := font.SizeUTF8(wifiStr)
	rightX -= int32(ww)
	wifiCol := sdl.Color{R: 100, G: 200, B: 140, A: 200}
	if !IsOnline() {
		wifiCol = sdl.Color{R: 180, G: 80, B: 80, A: 180}
	}
	renderText(renderer, config, font, wifiStr, wifiCol, rightX, trayY)
	rightX -= 16

	// Clock.
	now := time.Now()
	timeStr := now.Format("15:04")
	tw, _, _ := font.SizeUTF8(timeStr)
	rightX -= int32(tw)
	renderText(renderer, config, font, timeStr,
		sdl.Color{R: 180, G: 190, B: 210, A: 220}, rightX, trayY)
	rightX -= 20

	// Notification bell.
	bellRect := renderNotifBell(renderer, config, rightX, barY+6)
	tb.bellRect = bellRect

	// Notification dropdown (overlays everything).
	renderNotifDropdown(renderer, config)
}

// batteryDisplayString returns a formatted battery string with icon.
func batteryDisplayString() string {
	bat := getBatteryPercent()
	if bat < 0 {
		return "[BAT N/A]"
	}
	if bat > 75 {
		return fmt.Sprintf("[= %d%%]", bat)
	} else if bat > 25 {
		return fmt.Sprintf("[- %d%%]", bat)
	}
	return fmt.Sprintf("[-- %d%%]", bat)
}

// wifiIconString returns a WiFi symbol using font-safe ASCII.
func wifiIconString() string {
	if !IsOnline() {
		return "(X)"
	}
	return "(OK)"
}

// renderStartMenu draws the Start menu overlay.
func renderStartMenu(renderer *sdl.Renderer, config *Config) {
	if !tb.startOpen {
		return
	}

	font, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	menuW := int32(360)
	menuH := screenHeight - taskbarH - 20
	menuX := int32(0)
	menuY := int32(0)

	// Dark backdrop.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 140)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight - taskbarH})

	// Menu card.
	fillRoundedRect(renderer, menuX, menuY, menuW, menuH, 0,
		sdl.Color{R: 12, G: 16, B: 26, A: 245})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 80)
	renderer.FillRect(&sdl.Rect{X: menuW - 1, Y: 0, W: 1, H: menuH})

	// Title.
	title := "Apps"
	renderText(renderer, config, font, title,
		sdl.Color{R: 200, G: 210, B: 230, A: 220}, 20, 16)

	// Build app list from config.
	apps := getStartMenuApps(config)

	startListY := int32(56)
	itemH := int32(40)
	for i, app := range apps {
		iy := startListY + int32(i)*itemH
		if iy+itemH > menuY+menuH {
			break
		}

		// Icon + name.
		renderText(renderer, config, font, app.icon,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, 16, iy+6)
		renderText(renderer, config, font, app.name,
			sdl.Color{R: 180, G: 190, B: 210, A: 220}, 48, iy+6)

		// Hover highlight.
		if mouseX >= menuX && mouseX <= menuX+menuW &&
			mouseY >= iy && mouseY < iy+itemH {
			fillRoundedRect(renderer, menuX+4, iy, menuW-8, itemH, 8,
				sdl.Color{R: 30, G: 36, B: 50, A: 180})
			// Re-draw text on top.
			renderText(renderer, config, font, app.icon,
				sdl.Color{R: 255, G: 255, B: 255, A: 255}, 16, iy+6)
			renderText(renderer, config, font, app.name,
				sdl.Color{R: 255, G: 255, B: 255, A: 255}, 48, iy+6)
		}
	}

	// Version at bottom.
	if smallFont != nil {
		ver := versionString(config)
		vw, _, _ := smallFont.SizeUTF8(ver)
		renderText(renderer, config, smallFont, ver,
			sdl.Color{R: 60, G: 70, B: 90, A: 120},
			(menuW-int32(vw))/2, menuH-30)
	}
}

// startMenuIsPointIn tests if a click is inside the start menu.
func startMenuIsPointIn(x, y int32) bool {
	if !tb.startOpen {
		return false
	}
	menuW := int32(360)
	menuH := screenHeight - taskbarH - 20
	return x >= 0 && x <= menuW && y >= 0 && y <= menuH
}

// startMenuHandleClick processes a click inside the start menu.
func startMenuHandleClick(x, y int32, config *Config) {
	if !tb.startOpen {
		return
	}

	apps := getStartMenuApps(config)

	startListY := int32(56)
	itemH := int32(40)
	for i, app := range apps {
		iy := startListY + int32(i)*itemH
		if y >= iy && y < iy+itemH {
			tb.startOpen = false
			if app.action != nil {
				app.action(config)
			} else if app.scene != "" {
				if idx := findSceneIndex(config, app.scene); idx >= 0 {
					changeSceneTo(config, idx)
				}
			}
			PlayActivateSound()
			return
		}
	}
}

// startMenuToggle toggles the start menu open/closed.
func startMenuToggle() {
	tb.startOpen = !tb.startOpen
	PlayActivateSound()
}

// startMenuDismiss closes the start menu.
func startMenuDismiss() {
	tb.startOpen = false
}
