package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// packagesURL points to the upstream JukaLang package registry.
const packagesURL = "https://raw.githubusercontent.com/jukaLang/Packages/refs/heads/main/packages.json"

// Package is a single entry in the JukaLang package registry.
type Package struct {
	Name         string   `json:"name"`
	Author       string   `json:"author"`
	Description  string   `json:"description"`
	Date         string   `json:"date"`
	Version      string   `json:"version"`
	Dependencies []string `json:"dependencies"`
	MainFilename string   `json:"mainfilename"`
	Download     string   `json:"download"`
	Device       []string `json:"device"`
}

// --- Packages global state ---

var (
	packagesList       []Package
	packagesMutex      sync.Mutex
	packagesFocusIndex int
)

// fetchPackages downloads and parses the package registry, then publishes it
// so the list element can render it. Runs in its own goroutine.
func fetchPackages(config *Config) {
	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Loading packages..."
	defer func() { config.Variables.LoadingSpinner = false }()

	raw, err := fetchURL(packagesURL, 15*time.Second)
	if err != nil {
		log.Printf("packages: fetch error: %v", err)
		publishCustom("packages_error", fmt.Sprintf("Failed to load packages: %v", err))
		publishCustom("packages_list", []Package{})
		showToast("Packages load failed. Check network.", ToastError())
		return
	}

	var pkgs []Package
	if err := json.Unmarshal([]byte(raw), &pkgs); err != nil {
		log.Printf("packages: parse error: %v", err)
		publishCustom("packages_error", "Failed to parse packages.json")
		publishCustom("packages_list", []Package{})
		showToast("Packages parse failed.", ToastError())
		return
	}

	// Keep the list deterministic: sort by name.
	sort.Slice(pkgs, func(i, j int) bool {
		return strings.ToLower(pkgs[i].Name) < strings.ToLower(pkgs[j].Name)
	})

	packagesMutex.Lock()
	packagesList = pkgs
	if packagesFocusIndex >= len(packagesList) {
		packagesFocusIndex = 0
	}
	if packagesFocusIndex < 0 {
		packagesFocusIndex = 0
	}
	packagesMutex.Unlock()

	publishCustom("packages_list", pkgs)
	publishCustom("packages_error", nil)
	showToast(fmt.Sprintf("Loaded %d packages", len(pkgs)), ToastSuccess())
}

// renderPackageList renders the packagelist element: a two-pane layout with a
// scrollable package list on the left and details for the focused package on
// the right.
func renderPackageList(renderer *sdl.Renderer, config *Config, element Element) {
	packagesMutex.Lock()
	pkgs := packagesList
	packagesMutex.Unlock()

	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 500)

	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), accentColor)

	if len(pkgs) == 0 {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, "No packages loaded. Press Refresh to fetch the registry.", ColorTextTertiary(), element.X+20, element.Y+20)
		}
		return
	}

	if packagesFocusIndex >= len(pkgs) {
		packagesFocusIndex = len(pkgs) - 1
	}
	if packagesFocusIndex < 0 {
		packagesFocusIndex = 0
	}

	listFont, _ := getCachedFont(config, element.Font)
	smallFont, _ := getCachedFont(config, "small")
	if listFont == nil {
		listFont = smallFont
	}

	// Layout: list column + details column
	gap := int32(16)
	listW := elemW*3/5 - gap
	detailX := element.X + listW + gap
	detailW := elemW - listW - gap

	// --- List column ---
	lineH := int32(52)
	maxVisible := int((elemH - 20) / lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := 0
	if packagesFocusIndex >= maxVisible {
		start = packagesFocusIndex - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(pkgs) {
		end = len(pkgs)
	}

	for i := start; i < end; i++ {
		p := pkgs[i]
		iy := element.Y + 10 + int32(i-start)*lineH
		rowX := element.X + 8
		rowY := iy + 2
		rowW := listW - 16
		rowH := lineH - 4
		drawRow(renderer, rowX, rowY, rowW, rowH, 8, i == packagesFocusIndex, false)

		if listFont != nil {
			name := p.Name
			if len(name) > 34 {
				name = name[:31] + "..."
			}
			renderText(renderer, config, listFont, name, ColorTextPrimary(), rowX+12, rowY+6)

			meta := ""
			if p.Version != "" {
				meta += "v" + p.Version
			}
			if p.Author != "" {
				if meta != "" {
					meta += "  ·  "
				}
				meta += p.Author
			}
			if smallFont != nil && meta != "" {
				renderText(renderer, config, smallFont, meta, ColorTextTertiary(), rowX+12, rowY+28)
			}
		}
	}

	if len(pkgs) > maxVisible {
		thumbFrac := float64(maxVisible) / float64(len(pkgs))
		scrollFrac := 0.0
		if len(pkgs) > maxVisible {
			scrollFrac = float64(start) / float64(len(pkgs)-maxVisible)
		}
		drawScrollbar(renderer, element.X+listW-6, element.Y+8, 3, elemH-16, thumbFrac, scrollFrac)
	}

	// --- Details column ---
	p := pkgs[packagesFocusIndex]
	drawPanel(renderer, detailX, element.Y+8, detailW, elemH-16, PanelFill(190), accentColor)

	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = listFont
	}
	y := element.Y + 20
	if titleFont != nil {
		name := p.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}
		renderText(renderer, config, titleFont, name, accentColor, detailX+14, y)
	}
	y += 32

	if smallFont != nil {
		if p.Version != "" || p.Author != "" {
			meta := ""
			if p.Version != "" {
				meta += "Version: " + p.Version
			}
			if p.Author != "" {
				if meta != "" {
					meta += "   "
				}
				meta += "Author: " + p.Author
			}
			renderText(renderer, config, smallFont, meta, ColorTextSecondary(), detailX+14, y)
			y += 22
		}
		if p.Date != "" {
			renderText(renderer, config, smallFont, "Released: "+p.Date, ColorTextSecondary(), detailX+14, y)
			y += 22
		}
		if len(p.Device) > 0 {
			devs := strings.Join(p.Device, ", ")
			if len(devs) > 40 {
				devs = devs[:37] + "..."
			}
			renderText(renderer, config, smallFont, "Devices: "+devs, ColorTextSecondary(), detailX+14, y)
			y += 22
		}
		if p.MainFilename != "" {
			renderText(renderer, config, smallFont, "Entry: "+p.MainFilename, ColorTextSecondary(), detailX+14, y)
			y += 22
		}
		if len(p.Dependencies) > 0 {
			deps := strings.Join(p.Dependencies, ", ")
			if len(deps) > 40 {
				deps = deps[:37] + "..."
			}
			renderText(renderer, config, smallFont, "Dependencies: "+deps, ColorTextTertiary(), detailX+14, y)
			y += 22
		}

		if p.Description != "" {
			y += 8
			renderText(renderer, config, smallFont, "Description", ColorTextTertiary(), detailX+14, y)
			y += 20
			availH := element.Y + elemH - 24 - y
			for _, line := range wrapPackageText(p.Description, smallFont, detailW-28) {
				if availH <= 0 {
					break
				}
				renderText(renderer, config, smallFont, line, ColorTextPrimary(), detailX+14, y)
				y += 20
				availH -= 20
			}
		}

		if p.Download != "" {
			url := p.Download
			if len(url) > 44 {
				url = url[:41] + "..."
			}
			renderText(renderer, config, smallFont, "Download: "+url, sdl.Color{R: 110, G: 200, B: 255, A: 255}, detailX+14, element.Y+elemH-24)
		}
	}
}

// wrapPackageText breaks text into lines that fit within maxWidth pixels using
// the given font's measured glyph widths.
func wrapPackageText(text string, font *ttf.Font, maxWidth int32) []string {
	var lines []string
	if font == nil {
		return []string{text}
	}
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := ""
		for _, w := range words {
			test := w
			if cur != "" {
				test = cur + " " + w
			}
			wid, _, _ := font.SizeUTF8(test)
			if cur != "" && int32(wid) > maxWidth {
				lines = append(lines, cur)
				cur = w
			} else {
				cur = test
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}

// handlePackageInput handles keyboard navigation for a packagelist element.
func handlePackageInput(e *sdl.KeyboardEvent, config *Config) {
	packagesMutex.Lock()
	n := len(packagesList)
	packagesMutex.Unlock()

	switch e.Keysym.Sym {
	case sdl.K_UP:
		if packagesFocusIndex > 0 {
			packagesFocusIndex--
		}
	case sdl.K_DOWN:
		if packagesFocusIndex < n-1 {
			packagesFocusIndex++
		}
	case sdl.K_PAGEUP:
		packagesFocusIndex -= 8
		if packagesFocusIndex < 0 {
			packagesFocusIndex = 0
		}
	case sdl.K_PAGEDOWN:
		packagesFocusIndex += 8
		if packagesFocusIndex >= n {
			packagesFocusIndex = n - 1
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if packagesFocusIndex >= 0 && packagesFocusIndex < n {
			packagesMutex.Lock()
			p := packagesList[packagesFocusIndex]
			packagesMutex.Unlock()
			showToast("Download: "+p.Download, ToastInfo())
		}
	case sdl.K_BACKSPACE, sdl.K_ESCAPE:
		for _, scene := range config.Scenes {
			if scene.Name == "Main" {
				changeSceneTo(config, findSceneIndex(config, "Main"))
				break
			}
		}
	}
}

// handlePackageMouseClick selects the package row under the cursor.
func handlePackageMouseClick(mx, my int32, config *Config) {
	packagesMutex.Lock()
	n := len(packagesList)
	packagesMutex.Unlock()
	if n == 0 {
		return
	}
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type != "packagelist" {
			continue
		}
		elemW := getElementWidth(e, 1160)
		elemH := getElementHeight(e, 500)
		listW := elemW*3/5 - 16
		if mx < e.X || mx > e.X+listW || my < e.Y || my > e.Y+elemH {
			return
		}
		lineH := int32(52)
		maxVisible := int((elemH - 20) / lineH)
		if maxVisible < 1 {
			maxVisible = 1
		}
		start := 0
		if packagesFocusIndex >= maxVisible {
			start = packagesFocusIndex - maxVisible + 1
		}
		for i := start; i < start+maxVisible && i < n; i++ {
			iy := e.Y + 10 + int32(i-start)*lineH
			if my >= iy && my <= iy+lineH {
				packagesFocusIndex = i
				return
			}
		}
		return
	}
}
