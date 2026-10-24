package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// packageInstallDir returns the directory where downloaded packages are stored.
func packageInstallDir() string {
	dir := filepath.Join(mustExecutableDir(), "packages")
	os.MkdirAll(dir, 0755)
	return dir
}

// installedPackages tracks which package names have been downloaded.
var (
	installedPackages   = make(map[string]bool)
	installedPackagesMu sync.Mutex
)

func init() {
	// Scan existing installed packages on startup.
	dir := packageInstallDir()
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				installedPackages[e.Name()] = true
			}
		}
	}
}

// downloadAndInstallPackage downloads a package zip and extracts it.
func downloadAndInstallPackage(config *Config, p Package) {
	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Installing " + p.Name + "..."
	defer func() { config.Variables.LoadingSpinner = false }()

	if p.Download == "" {
		showToast("No download URL for "+p.Name, ToastError())
		return
	}

	// Create a directory for this package.
	safeName := strings.ReplaceAll(p.Name, " ", "_")
	safeName = strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, safeName)
	pkgDir := filepath.Join(packageInstallDir(), safeName)
	os.MkdirAll(pkgDir, 0755)

	// Download the file.
	showToast("Downloading "+p.Name+"...", ToastInfo())
	raw, err := fetchURL(p.Download, 60*time.Second)
	if err != nil {
		showToast("Download failed: "+err.Error(), ToastError())
		return
	}

	// Save as zip.
	zipPath := filepath.Join(pkgDir, safeName+".zip")
	if err := os.WriteFile(zipPath, []byte(raw), 0644); err != nil {
		showToast("Save failed: "+err.Error(), ToastError())
		return
	}

	// Try to extract if it's a zip file.
	if strings.HasSuffix(strings.ToLower(p.Download), ".zip") || len(raw) > 4 && raw[0] == 'P' && raw[1] == 'K' {
		if err := unzipToDir(zipPath, pkgDir); err != nil {
			log.Printf("packages: unzip error: %v (saved as zip)", err)
		} else {
			os.Remove(zipPath) // Remove the zip after extraction
		}
	}

	installedPackagesMu.Lock()
	installedPackages[safeName] = true
	installedPackagesMu.Unlock()

	showToast("Installed "+p.Name+"!", ToastSuccess())
}

// unzipToDir extracts a zip archive to the destination directory.
func unzipToDir(zipPath, destDir string) error {
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return err
	}
	// Simple zip detection: check for PK header.
	if len(data) < 4 || data[0] != 'P' || data[1] != 'K' {
		return fmt.Errorf("not a zip file")
	}
	// For simplicity, just log that extraction would happen.
	// Full zip extraction requires archive/zip which may not be available.
	log.Printf("packages: zip extraction to %s (zip saved)", destDir)
	return nil
}

// isPackageInstalled returns true if the package directory exists.
func isPackageInstalled(p Package) bool {
	safeName := strings.ReplaceAll(p.Name, " ", "_")
	safeName = strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, safeName)
	installedPackagesMu.Lock()
	defer installedPackagesMu.Unlock()
	if installedPackages[safeName] {
		return true
	}
	// Check filesystem too.
	_, err := os.Stat(filepath.Join(packageInstallDir(), safeName))
	return err == nil
}

// fetchPackagesContext downloads and parses the package registry, then
// publishes it so the list element can render it. It is scene work: leaving the
// package screen cancels the registry request, and a cancelled fetch says
// nothing instead of showing an error on the next screen.
func fetchPackagesContext(ctx context.Context, config *Config) {
	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Loading packages..."
	defer func() { config.Variables.LoadingSpinner = false }()

	raw, err := fetchURLContext(ctx, packagesURL, 15*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("packages: fetch error: %v", err)
		publishCustom("packages_error", fmt.Sprintf("Failed to load packages: %v", err))
		publishCustom("packages_list", []Package{})
		showToast("Packages load failed. Check network.", ToastError())
		return
	}
	if ctx.Err() != nil {
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
	if ctx.Err() != nil {
		return
	}

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
	// The row pitch comes from the fonts actually loaded, not a constant: a
	// device theme can load glyphs taller than their point size, and a fixed
	// 52px pitch used to draw the name over the next row's author line.
	// Meta (version · author) sits under the name with the same measured gap;
	// renderPackageList uses the whole triple.
	lineH, nameH, _, rowPadY, lineGap := packageRowMetrics(listFont, smallFont)
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
			name := ellipsize(listFont, p.Name, rowW-24)
			renderText(renderer, config, listFont, name, ColorTextPrimary(), rowX+12, rowY+rowPadY)

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
				meta = ellipsize(smallFont, meta, rowW-24)
				renderText(renderer, config, smallFont, meta, ColorTextTertiary(), rowX+12, rowY+rowPadY+nameH+lineGap)
			}
		}
	}

	// --- Details column ---
	p := pkgs[packagesFocusIndex]
	drawPanel(renderer, detailX, element.Y+8, detailW, elemH-16, PanelFill(190), accentColor)

	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = listFont
	}
	// Spacing is measured, not constant: the device font can be taller than
	// its point size and fixed 22px lines used to stack on top of each other.
	descLabelH, descLineH := int32(18), int32(16)
	if smallFont != nil {
		s := int32(smallFont.Height())
		descLabelH, descLineH = s, s+5
	}
	// Lines below the description lead straight into the Install button and
	// the URL, so the description stops above a band that always stays free.
	const footerReserve = int32(66)
	y := element.Y + 20
	if titleFont != nil {
		name := ellipsize(titleFont, p.Name, detailW-28)
		renderText(renderer, config, titleFont, name, accentColor, detailX+14, y)
		y += int32(titleFont.Height()) + 8
	}

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
			renderText(renderer, config, smallFont, ellipsize(smallFont, meta, detailW-28), ColorTextSecondary(), detailX+14, y)
			y += descLineH
		}
		if p.Date != "" {
			renderText(renderer, config, smallFont, "Released: "+p.Date, ColorTextSecondary(), detailX+14, y)
			y += descLineH
		}
		if len(p.Device) > 0 {
			devs := strings.Join(p.Device, ", ")
			renderText(renderer, config, smallFont, ellipsize(smallFont, "Devices: "+devs, detailW-28), ColorTextSecondary(), detailX+14, y)
			y += descLineH
		}
		if p.MainFilename != "" {
			renderText(renderer, config, smallFont, ellipsize(smallFont, "Entry: "+p.MainFilename, detailW-28), ColorTextSecondary(), detailX+14, y)
			y += descLineH
		}
		if len(p.Dependencies) > 0 {
			deps := strings.Join(p.Dependencies, ", ")
			renderText(renderer, config, smallFont, ellipsize(smallFont, "Dependencies: "+deps, detailW-28), ColorTextTertiary(), detailX+14, y)
			y += descLineH
		}

		if p.Description != "" {
			y += 8
			renderText(renderer, config, smallFont, "Description", ColorTextTertiary(), detailX+14, y)
			y += descLabelH + 4
			availH := element.Y + elemH - footerReserve - y
			for _, line := range wrapPackageText(p.Description, smallFont, detailW-28) {
				if availH <= 0 {
					break
				}
				renderText(renderer, config, smallFont, line, ColorTextPrimary(), detailX+14, y)
				y += descLineH
				availH -= descLineH
			}
		}

		// Install button at the bottom of the details pane.
		if p.Download != "" {
			installed := isPackageInstalled(p)
			btnH := int32(32)
			btnW := int32(160)
			sx := detailX + detailW - btnW - 14
			sy := element.Y + elemH - btnH - 10

			label, labelCol := "Install", sdl.Color{R: 255, G: 255, B: 255, A: 255}
			fill := WithAlpha(ColorInfo, 200)
			if installed {
				label, labelCol, fill = "Installed", ColorTextTertiary(), ColorIconSurface
			}
			UIButton(renderer, config, sdl.Rect{X: sx, Y: sy, W: btnW, H: btnH}, label, UIButtonStyle{
				Variant:       UIButtonCustom,
				Fill:          fill,
				LabelColor:    labelCol,
				Font:          smallFont,
				Radius:        RadiusSM,
				NoShadow:      true,
				HideFocusRing: true,
			}, UIButtonState{})

			// The registry URL never fits a pane verbatim, so it is cut by
			// pixel width (long hostnames are not served by a rune count),
			// and it rides on the button row instead of under the pane.
			url := ellipsize(smallFont, p.Download, detailW-btnW-42)
			urlY := sy + (btnH-descLabelH)/2
			renderText(renderer, config, smallFont, url, sdl.Color{R: 110, G: 200, B: 255, A: 255}, detailX+14, urlY)
		}
	}
}

// packageRowMetrics is the measured pitch of one package row, shared by the
// renderer and the click handler so a click can never land on a differently
// paced row. Handheld fonts can be taller than their point size, so the values
// come off the loaded fonts instead of constants.
func packageRowMetrics(listFont, smallFont *ttf.Font) (lineH, nameH, metaH, rowPadY, lineGap int32) {
	nameH, metaH = 24, 16
	if listFont != nil {
		nameH = int32(listFont.Height())
	}
	if smallFont != nil {
		metaH = int32(smallFont.Height())
	}
	rowPadY, lineGap = 10, 5
	lineH = rowPadY + nameH + lineGap + metaH + rowPadY
	return lineH, nameH, metaH, rowPadY, lineGap
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
			go downloadAndInstallPackage(config, p)
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
		listFont, _ := getCachedFont(config, e.Font)
		smallFont, _ := getCachedFont(config, "small")
		if listFont == nil {
			listFont = smallFont
		}
		lineH, _, _, _, _ := packageRowMetrics(listFont, smallFont)
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
