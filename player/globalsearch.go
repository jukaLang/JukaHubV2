package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Global Search — F2 overlay to search apps, notes, settings, files
// ──────────────────────────────────────────────────────────────────────

var globalSearchOpen bool
var gsQuery string
var gsCursor int
var gsResults []gsResult

type gsResult struct {
	Title    string
	Subtitle string
	Icon     string
	Category string
	Action   func(*Config)
}

func handleGlobalSearchTextInput(text string) {
	if !globalSearchOpen {
		return
	}
	for _, r := range text {
		if r >= 32 && r <= 126 {
			gsQuery += string(r)
		}
	}
	gsResults = gsSearch(gsQuery)
	gsCursor = 0
}

// gsPopulateApps lazily fills gsApps from the loaded config.
func gsPopulateApps() {
	if gsApps != nil || appConfig == nil {
		return
	}
	gsApps = gsBuildApps(appConfig)
}

func gsSearch(query string) []gsResult {
	gsPopulateApps()
	var results []gsResult
	q := strings.ToLower(strings.TrimSpace(query))

	if q == "" {
		// Show apps when empty
		for _, app := range gsApps {
			results = append(results, gsResult{
				Title:    app.Name,
				Subtitle: app.Desc,
				Icon:     app.Icon,
				Category: "Apps",
				Action:   gsMakeLauncher(app.Scene),
			})
		}
		return results
	}

	// Search apps
	for _, app := range gsApps {
		if strings.Contains(strings.ToLower(app.Name), q) || strings.Contains(strings.ToLower(app.Desc), q) {
			results = append(results, gsResult{
				Title:    app.Name,
				Subtitle: app.Desc,
				Icon:     app.Icon,
				Category: "Apps",
				Action:   gsMakeLauncher(app.Scene),
			})
		}
	}

	// Search notes
	if len(notes.store.Notes) > 0 {
		for _, n := range notes.store.Notes {
			if strings.Contains(strings.ToLower(n.Title), q) || strings.Contains(strings.ToLower(n.Content), q) {
				note := n
				results = append(results, gsResult{
					Title:    note.Title,
					Subtitle: "Note • " + note.UpdatedAt.Format("Jan 2"),
					Icon:     "[N]",
					Category: "Notes",
					Action: func(c *Config) {
						if idx := findSceneIndex(c, "Notes"); idx >= 0 {
							changeSceneTo(c, idx)
						}
					},
				})
			}
		}
	}

	// Search files (if query is >= 2 chars)
	if len(q) >= 2 {
		entries, _ := os.ReadDir(".")
		count := 0
		for _, e := range entries {
			if count >= 8 {
				break
			}
			name := strings.ToLower(e.Name())
			if strings.Contains(name, q) {
				icon := "[d]"
				if e.IsDir() {
					icon = "[+]"
				}
				results = append(results, gsResult{
					Title:    e.Name(),
					Subtitle: filepath.Clean(e.Name()),
					Icon:     icon,
					Category: "Files",
					Action:   gsMakeLauncher(""),
				})
				count++
			}
		}
	}

	return results
}

type gsApp struct {
	Name  string
	Icon  string
	Scene string
	Desc  string
}

// gsBuildApps builds the global search app list from config.
func gsBuildApps(config *Config) []gsApp {
	var apps []gsApp
	seen := make(map[string]bool)
	for _, sc := range config.Scenes {
		if sc.Icon == "" || sc.Name == "Exit" || sc.Name == "Patch" {
			continue
		}
		if strings.HasPrefix(sc.Name, "Settings") || strings.HasPrefix(sc.Name, "Theme") {
			continue
		}
		apps = append(apps, gsApp{
			Name:  sceneDisplayName(sc.Name),
			Icon:  sc.Icon,
			Scene: sc.Name,
			Desc:  sc.Description,
		})
		seen[sc.Name] = true
	}
	// Add special entries.
	apps = append(apps, gsApp{Name: "Wallpapers", Icon: "P", Scene: "Wallpapers", Desc: "Set custom wallpaper"})
	apps = append(apps, gsApp{Name: "Quick Settings", Icon: "Q", Scene: "QuickSettings", Desc: "Quick toggles"})
	return apps
}

var gsApps []gsApp // populated lazily from config

func gsMakeLauncher(scene string) func(*Config) {
	return func(c *Config) {
		if scene == "" || scene == "Main" {
			if idx := findSceneIndex(c, "Main"); idx >= 0 {
				changeSceneTo(c, idx)
			}
			return
		}
		if idx := findSceneIndex(c, scene); idx >= 0 {
			changeSceneTo(c, idx)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderGlobalSearch(renderer *sdl.Renderer, config *Config) {
	if !globalSearchOpen {
		return
	}

	// Dim background
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 180)

	// Card
	cardW := int32(640)
	cardH := int32(520)
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	drawRoundedRect(renderer, &sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, 20, 22, 22, 28, 245)

	// Search input
	inputH := int32(44)
	inputX := cardX + 16
	inputY := cardY + 16
	inputW := cardW - 32
	drawRoundedRect(renderer, &sdl.Rect{X: inputX, Y: inputY, W: inputW, H: inputH}, 12, 28, 32, 42, 255)

	searchFont := getDisplayFont(config, 18)
	if gsQuery == "" {
		drawText(renderer, searchFont, "[Search] Search apps, notes, files...", inputX+12, inputY+10, colorNew(90, 100, 120, 150), textAlignLeft)
	} else {
		drawText(renderer, searchFont, gsQuery, inputX+12, inputY+10, colorNew(230, 235, 250, 255), textAlignLeft)
		// Blinking cursor
		if int64(sdl.GetTicks64())%1000 < 500 {
			cw, _, _ := searchFont.SizeUTF8(gsQuery)
			cursorX := inputX + 12 + int32(cw)
			ac := getAccentColor(config)
			renderer.SetDrawColor(byte(ac.R), byte(ac.G), byte(ac.B), 200)
			renderer.FillRect(&sdl.Rect{X: cursorX, Y: inputY + 10, W: 2, H: inputH - 20})
		}
	}

	// Hint
	hintFont := getBodyFont(config, 11)
	hint := "↑↓ navigate • Enter select • Esc close"
	hw, _, _ := hintFont.SizeUTF8(hint)
	drawText(renderer, hintFont, hint, inputX+inputW-int32(hw)-4, inputY+14, colorNew(80, 90, 110, 120), textAlignLeft)

	// Results
	resultsY := inputY + inputH + 8
	resultsH := cardH - inputH - 32
	lineH := int32(44)
	maxVisible := int(resultsH / lineH)

	bodyFont := getBodyFont(config, 13)
	titleFont := getBodyFont(config, 15)

	// Clamp cursor
	if len(gsResults) == 0 {
		gsCursor = 0
	} else if gsCursor >= len(gsResults) {
		gsCursor = 0
	}

	currentCat := ""
	resultCount := 0
	for i, r := range gsResults {
		if resultCount >= maxVisible {
			break
		}
		// Category header
		if r.Category != currentCat {
			currentCat = r.Category
			hy := resultsY + int32(resultCount)*lineH
			if hy+20 > cardY+cardH {
				break
			}
			drawText(renderer, bodyFont, strings.ToUpper(currentCat), inputX+8, hy+4, getAccentColor(config), textAlignLeft)
			resultCount++
		}
		if resultCount >= maxVisible {
			break
		}

		ry := resultsY + int32(resultCount)*lineH
		focused := i == gsCursor

		if focused {
			drawRoundedRect(renderer, &sdl.Rect{X: inputX, Y: ry, W: inputW, H: lineH - 2}, 8, 30, 36, 50, 220)
		}

		// Icon
		drawText(renderer, titleFont, r.Icon, inputX+12, ry+6, colorNew(200, 210, 230, 220), textAlignLeft)

		// Title
		titleCol := colorNew(210, 220, 240, 240)
		if focused {
			titleCol = colorNew(255, 255, 255, 255)
		}
		drawText(renderer, titleFont, r.Title, inputX+44, ry+6, titleCol, textAlignLeft)

		// Subtitle
		if r.Subtitle != "" {
			sub := r.Subtitle
			if len(sub) > 48 {
				sub = sub[:45] + "..."
			}
			drawText(renderer, bodyFont, sub, inputX+44, ry+26, colorNew(120, 135, 160, 180), textAlignLeft)
		}

		resultCount++
	}

	// No results
	if len(gsResults) == 0 && gsQuery != "" {
		emptyFont := getBodyFont(config, 16)
		empty := fmt.Sprintf("No results for \"%s\"", gsQuery)
		ew, _, _ := emptyFont.SizeUTF8(empty)
		drawText(renderer, emptyFont, empty, inputX+(cardW-32-int32(ew))/2, resultsY+80, colorNew(140, 155, 180, 200), textAlignLeft)
	}

	// Result count
	countStr := fmt.Sprintf("%d results", len(gsResults))
	cw, _, _ := bodyFont.SizeUTF8(countStr)
	drawText(renderer, bodyFont, countStr, inputX+inputW-int32(cw)-8, cardY+cardH-28, colorNew(80, 90, 110, 120), textAlignLeft)
}

// ──────────────────────────────────────────────────────────────────────
// Input handling
// ──────────────────────────────────────────────────────────────────────

func handleGSInput(e *sdl.KeyboardEvent, config *Config) {
	if !globalSearchOpen {
		return
	}
	if e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if gsCursor > 0 {
			gsCursor--
		} else if len(gsResults) > 0 {
			gsCursor = len(gsResults) - 1
		}
	case sdl.K_DOWN:
		if gsCursor < len(gsResults)-1 {
			gsCursor++
		} else {
			gsCursor = 0
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if gsCursor >= 0 && gsCursor < len(gsResults) && gsResults[gsCursor].Action != nil {
			gsResults[gsCursor].Action(config)
			gsQuery = ""
			gsResults = nil
			globalSearchOpen = false
			PlayActivateSound()
		}
	case sdl.K_ESCAPE, sdl.K_b:
		gsQuery = ""
		gsResults = nil
		globalSearchOpen = false
		PlayBackSound()
	case sdl.K_BACKSPACE:
		if len(gsQuery) > 0 {
			gsQuery = gsQuery[:len(gsQuery)-1]
			gsResults = gsSearch(gsQuery)
			gsCursor = 0
		}
	}
}
