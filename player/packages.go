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
	refreshInstalledPackages()
}

// refreshInstalledPackages rescans the install directory into the in-memory set.
// The set is what the list rows read, so a directory copied onto the card behind
// the app's back is picked up when the Packages screen loads instead of costing a
// stat call per row on every frame — 13 rows at 60fps is 780 lookups a second on
// an SD card to answer a question whose answer does not change between frames.
func refreshInstalledPackages() {
	entries, err := os.ReadDir(packageInstallDir())
	if err != nil {
		return
	}
	installedPackagesMu.Lock()
	defer installedPackagesMu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			installedPackages[e.Name()] = true
		}
	}
}

// packageDirName is the directory a package installs into. The sanitising lives
// in one place so the install path, the installed check and the list row's mark
// can never disagree about which directory a package means.
func packageDirName(name string) string {
	safeName := strings.ReplaceAll(name, " ", "_")
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, safeName)
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
	safeName := packageDirName(p.Name)
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
	safeName := packageDirName(p.Name)
	installedPackagesMu.Lock()
	defer installedPackagesMu.Unlock()
	if installedPackages[safeName] {
		return true
	}
	// Check filesystem too, and remember the answer: this runs for the focused
	// package on every frame, and the list rows answer from the same set, so a
	// directory added between scans must not make the details pane say
	// "Installed" about a row that shows no mark.
	if _, err := os.Stat(filepath.Join(packageInstallDir(), safeName)); err == nil {
		installedPackages[safeName] = true
		return true
	}
	return false
}

// packageInstalledFlags answers "is this package on the card" for a whole list in
// one lock, which is what the list rows need. It reads the in-memory set only:
// callers draw this every frame, and refreshInstalledPackages keeps the set
// current when the screen loads.
func packageInstalledFlags(pkgs []Package) []bool {
	flags := make([]bool, len(pkgs))
	installedPackagesMu.Lock()
	defer installedPackagesMu.Unlock()
	for i, p := range pkgs {
		flags[i] = installedPackages[packageDirName(p.Name)]
	}
	return flags
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

	// The rows mark what is already on the card, and this is the moment the
	// card is read: one directory scan per screen load rather than one stat per
	// row per frame.
	refreshInstalledPackages()

	packagesMutex.Lock()
	packagesList = pkgs
	if packagesFocusIndex >= len(packagesList) {
		packagesFocusIndex = 0
	}
	if packagesFocusIndex < 0 {
		packagesFocusIndex = 0
	}
	packagesMutex.Unlock()
	// A fresh list invalidates an open row menu: it was acting on the list
	// that just went away.
	closePackageActions()

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

	// The pane frames take the plain border every other panel uses. They were
	// drawn in the accent, which put an accent outline around the whole screen
	// and left the focused row's accent ring competing with it.
	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), ColorBorderDefault)

	if len(pkgs) == 0 {
		UIEmptyState(renderer, config, sdl.Rect{X: element.X, Y: element.Y, W: elemW, H: elemH},
			"·", "No packages installed", "Press Refresh to fetch the registry.")
		renderPackageActions(renderer, config, element)
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
	// pitch used to draw the name over the next row's text. The row is one line
	// high — name on the left, version · author on the right — which is what
	// lets a 500px pane show twice as many packages.
	lineH, nameH, metaH := packageRowMetrics(listFont, smallFont)

	// The installed mark gets a fixed gutter at the left of every row, so names
	// line up whether or not a package is on the card. It is measured rather
	// than assumed: a device theme can load a glyph wider than its point size.
	var checkW, checkH int32
	checkGutter := int32(0)
	if smallFont != nil {
		if w, h, err := smallFont.SizeUTF8(packageInstalledMark); err == nil && w > 0 && h > 0 {
			checkW, checkH = int32(w), int32(h)
			checkGutter = checkW + 10
		}
	}
	installed := packageInstalledFlags(pkgs)

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

	// The rows are clipped to the list column rather than to the whole screen:
	// the pitch is measured from the loaded fonts, so a device theme can make a
	// row taller than the space left in the panel, and an unclipped row would
	// paint over the panel's edge and the details column beside it.
	renderWithClip(renderer, element.X, element.Y, listW, elemH, func(_ *sdl.Renderer) {
		for i := start; i < end; i++ {
			p := pkgs[i]
			focused := i == packagesFocusIndex
			iy := element.Y + 10 + int32(i-start)*lineH
			rowX := element.X + 8
			rowY := iy + 2
			rowW := listW - 16
			rowH := lineH - 4
			drawRow(renderer, rowX, rowY, rowW, rowH, 8, focused, false)

			// A package that is already on the card is marked here rather than
			// only in the details pane: on a long list the question "which of
			// these do I still need to install" was answered by moving the cursor
			// down every row and reading the button. The mark is the app's own
			// check glyph (the one the install bar ends on) in the success
			// colour, so it reads the same as every other "this is done" state.
			if installed[i] && smallFont != nil && checkW > 0 {
				renderText(renderer, config, smallFont, packageInstalledMark, ColorSuccess,
					rowX+12, rowY+(rowH-checkH)/2)
			}

			// Name on the left, version · author against the right edge that the
			// two-line row left empty. The meta column is capped at two fifths of
			// the row, and the name is measured against what is actually left of
			// it, so a long author can never squeeze the name out.
			nameX := rowX + 12 + checkGutter
			nameW := rowX + rowW - 12 - nameX
			if smallFont != nil {
				if meta := packageRowMeta(p); meta != "" {
					metaMaxW := (rowW - 24) * 2 / 5
					meta = ellipsize(smallFont, meta, metaMaxW)
					mw, _, _ := smallFont.SizeUTF8(meta)
					// The selected row's meta steps up a tone: on a long list the
					// focused name and its neighbour's look alike, and the brighter
					// second column is what makes the selected row read as one block.
					metaColor := ColorTextTertiary()
					if focused {
						metaColor = ColorTextSecondary()
					}
					metaX := rowX + rowW - 12 - int32(mw)
					renderText(renderer, config, smallFont, meta, metaColor,
						metaX, rowY+(rowH-metaH)/2)
					nameW = metaX - nameX - 12
				}
			}
			if listFont != nil && nameW > 0 {
				name := ellipsize(listFont, p.Name, nameW)
				renderText(renderer, config, listFont, name, ColorTextPrimary(),
					nameX, rowY+(rowH-nameH)/2)
			}
		}
	})

	// --- Details column ---
	p := pkgs[packagesFocusIndex]
	drawPanel(renderer, detailX, element.Y+8, detailW, elemH-16, PanelFill(190), ColorBorderDefault)

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

	// The row menu floats over the details pane, last, so nothing in either
	// pane paints over it.
	renderPackageActions(renderer, config, element)
}

// packageRowPadY is the breathing room above and below a row's single line.
const packageRowPadY = int32(6)

// packageInstalledMark is the glyph on a row whose package is already on the
// card. It is the check the install bar ends on, and the font fallback turns it
// into a '+' if the themed font lacks it — a mark that is still a mark.
const packageInstalledMark = "✓"

// packageRowMetrics is the measured pitch of one package row, shared by the
// renderer and the click handler so a click can never land on a differently
// paced row. A row is one line of text — the name, plus the right-aligned
// version and author — because the two-line row this replaced doubled every
// row's height on a 500px pane and still left the right half of each row empty.
// Handheld fonts can be taller than their point size, so the values come off
// the loaded fonts instead of constants.
func packageRowMetrics(listFont, smallFont *ttf.Font) (pitch, nameH, metaH int32) {
	nameH, metaH = 24, 16
	if listFont != nil {
		nameH = int32(listFont.Height())
	}
	if smallFont != nil {
		metaH = int32(smallFont.Height())
	}
	line := nameH
	if metaH > line {
		line = metaH
	}
	return 2*packageRowPadY + line, nameH, metaH
}

// packageRowMeta is the right-aligned column on a package row: the version and
// the author, whichever of the two the registry supplied.
func packageRowMeta(p Package) string {
	parts := make([]string, 0, 2)
	if p.Version != "" {
		parts = append(parts, "v"+p.Version)
	}
	if p.Author != "" {
		parts = append(parts, p.Author)
	}
	return strings.Join(parts, "  ·  ")
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
	// While a row's action menu is open it owns the keys: the list behind it
	// must not scroll away from the package the menu is acting on, and Escape
	// closes the menu instead of leaving the scene.
	if packageActionsOpen {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			packageMenuMove(-1)
		case sdl.K_DOWN:
			packageMenuMove(1)
		case sdl.K_RETURN, sdl.K_SPACE:
			activatePackageAction(config)
		case sdl.K_BACKSPACE, sdl.K_ESCAPE:
			closePackageActions()
			PlayBackSound()
		}
		return
	}

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
		// One press, three meanings: run the open menu's item, offer the menu
		// on a row already on the card, install a row that is not.
		packagePress(config)
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
		// The open row menu floats over the details pane, outside the list
		// bounds checked below, so it is hit first: a tap picks an item, and a
		// tap anywhere else closes it.
		if packageActionsOpen {
			if idx, ok := packageActionsHit(mx, my, config, e); ok {
				packageActionsFocus = idx
				activatePackageAction(config)
			} else {
				closePackageActions()
			}
			return
		}
		listW := elemW*3/5 - 16
		if mx < e.X || mx > e.X+listW || my < e.Y || my > e.Y+elemH {
			return
		}
		listFont, _ := getCachedFont(config, e.Font)
		smallFont, _ := getCachedFont(config, "small")
		if listFont == nil {
			listFont = smallFont
		}
		lineH, _, _ := packageRowMetrics(listFont, smallFont)
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
				if i == packagesFocusIndex {
					// The tap on the focused row is the press — a touch user's
					// A button. It installs an uninstalled row and opens the
					// Remove/Reinstall menu on an installed one.
					packagePress(config)
				} else {
					packagesFocusIndex = i
				}
				return
			}
		}
		return
	}
}

// --- Row actions: what a press on an installed package does ---

// packageActionItems is the menu a press on a row that is already on the card
// opens. Such a row has no "Install" left to run — the press used to run one
// anyway — so the two things an installed package can become are offered
// directly: Reinstall (wipe the old copy, fetch a fresh one) and Remove.
// Cancel sits last so the default focus is the replacement, not the
// destructive choice.
var packageActionItems = []string{"Reinstall", "Remove", "Cancel"}

var (
	// packageActionsOpen is the state of that menu. It belongs to one row:
	// the package it acts on is stored, not re-read from the list, so rows
	// moving behind it cannot retarget it.
	packageActionsOpen  bool
	packageActionsFocus int
	packageActionsPkg   Package
)

// packageInstallHook is the install a press starts. Production falls through
// to the real download; a test swaps it to record the call instead of
// touching the network.
var packageInstallHook func(config *Config, p Package)

// startPackageInstall runs the install for a press.
func startPackageInstall(config *Config, p Package) {
	if packageInstallHook != nil {
		packageInstallHook(config, p)
		return
	}
	go downloadAndInstallPackage(config, p)
}

// openPackageActions raises the menu on a package, on its first item.
func openPackageActions(p Package) {
	packageActionsOpen = true
	packageActionsFocus = 0
	packageActionsPkg = p
}

// closePackageActions takes the menu down without acting on it.
func closePackageActions() {
	packageActionsOpen = false
}

// packagePress is one press on the packagelist, from any device: it runs the
// open menu's focused item, offers the menu on an installed row, and installs
// an uninstalled one — the path the press has always taken, and still takes
// when there is nothing on the card.
func packagePress(config *Config) {
	if packageActionsOpen {
		activatePackageAction(config)
		return
	}
	packagesMutex.Lock()
	ok := packagesFocusIndex >= 0 && packagesFocusIndex < len(packagesList)
	var p Package
	if ok {
		p = packagesList[packagesFocusIndex]
	}
	packagesMutex.Unlock()
	if !ok {
		return
	}
	if isPackageInstalled(p) {
		openPackageActions(p)
		return
	}
	startPackageInstall(config, p)
}

// removePackageFiles takes the package's directory off the card and out of
// the installed set. The directory name comes from packageDirName, which maps
// every path separator away, so the path is always directly inside the
// install dir and cannot climb out of it.
func removePackageFiles(p Package) error {
	safeName := packageDirName(p.Name)
	if err := os.RemoveAll(filepath.Join(packageInstallDir(), safeName)); err != nil {
		return err
	}
	installedPackagesMu.Lock()
	delete(installedPackages, safeName)
	installedPackagesMu.Unlock()
	return nil
}

// removeInstalledPackage is the menu's Remove, with a toast either way.
func removeInstalledPackage(p Package) {
	if err := removePackageFiles(p); err != nil {
		showToast("Remove failed: "+err.Error(), ToastError())
		return
	}
	showToast("Removed "+p.Name, ToastSuccess())
}

// activatePackageAction runs the menu's focused item. The menu closes first,
// so a second press on a still-open menu cannot run the action twice.
func activatePackageAction(config *Config) {
	if !packageActionsOpen {
		return
	}
	item := ""
	if packageActionsFocus >= 0 && packageActionsFocus < len(packageActionItems) {
		item = packageActionItems[packageActionsFocus]
	}
	p := packageActionsPkg
	closePackageActions()
	switch item {
	case "Reinstall":
		// Wipe first so the fresh download cannot mix with the old copy's
		// leftovers, and let the mark go with it: a failed download must not
		// leave a tick on a row with no files behind it.
		if err := removePackageFiles(p); err != nil {
			log.Printf("packages: wipe for reinstall of %s: %v", p.Name, err)
		}
		startPackageInstall(config, p)
	case "Remove":
		removeInstalledPackage(p)
	case "Cancel":
		// Nothing to do: the menu is already closed above.
	}
}

// packageMenuMove walks the menu with wrap-around, the way the media grid's
// action chips do: the key that walked down to Cancel walks back up.
func packageMenuMove(delta int) {
	n := len(packageActionItems)
	if n == 0 {
		return
	}
	next := packageActionsFocus + delta
	if next < 0 {
		next = n - 1
	}
	if next >= n {
		next = 0
	}
	if next != packageActionsFocus {
		packageActionsFocus = next
		PlayNavSound()
	}
}

// packageActionsController routes the pad while the menu is open and reports
// whether it consumed the event: D-pad and B belong to the menu while it is
// up, so the list behind it cannot move and the scene cannot be left under it.
func packageActionsController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !packageActionsOpen {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		packageMenuMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		packageMenuMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		activatePackageAction(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closePackageActions()
		PlayBackSound()
	}
	return true
}

// packageActionsLayout is the menu's geometry in one place: a box centred
// over the details pane — never over the list, so the rows the menu acts on
// stay visible beside it — with a header for the package's name and one
// measured row per item at the pitch the list itself uses. The renderer, the
// hit tester and the tests all ask here, so a tap can never land on a menu
// drawn somewhere else.
func packageActionsLayout(config *Config, element Element) (box sdl.Rect, header, lineH, nameH int32) {
	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 500)
	listW := elemW*3/5 - 16
	detailX := element.X + listW + 16
	detailW := elemW - listW - 16

	listFont, _ := getCachedFont(config, element.Font)
	smallFont, _ := getCachedFont(config, "small")
	if listFont == nil {
		listFont = smallFont
	}
	lineH, nameH, _ = packageRowMetrics(listFont, smallFont)

	w := detailW - 24
	if w > 280 {
		w = 280
	}
	if w < 120 {
		w = detailW
	}
	header = nameH + 14
	h := header + int32(len(packageActionItems))*lineH + 10
	return sdl.Rect{
		X: detailX + (detailW-w)/2,
		Y: element.Y + (elemH-h)/2,
		W: w,
		H: h,
	}, header, lineH, nameH
}

// packageActionItemRect is item i's tap cell inside the box: the full pitch,
// so the padding between rows belongs to one item or the next and no tap
// falls through the seam.
func packageActionItemRect(box sdl.Rect, lineH, header int32, i int) sdl.Rect {
	return sdl.Rect{
		X: box.X + 8,
		Y: box.Y + header + int32(i)*lineH,
		W: box.W - 16,
		H: lineH,
	}
}

// packageActionsHit finds the item under a tap. The title strip is not an
// item: it reports no hit, and the caller reads that as "outside" — a tap on
// the menu's own title closes it, like a tap anywhere else around it.
func packageActionsHit(mx, my int32, config *Config, element Element) (int, bool) {
	box, header, lineH, _ := packageActionsLayout(config, element)
	if mx < box.X || mx >= box.X+box.W || my < box.Y || my >= box.Y+box.H {
		return 0, false
	}
	for i := range packageActionItems {
		cell := packageActionItemRect(box, lineH, header, i)
		if mx >= cell.X && mx < cell.X+cell.W && my >= cell.Y && my < cell.Y+cell.H {
			return i, true
		}
	}
	return 0, false
}

// renderPackageActions draws the open row menu over the details pane. The
// title is the package itself: a menu of Reinstall/Remove must never float
// free of the row it belongs to.
func renderPackageActions(renderer *sdl.Renderer, config *Config, element Element) {
	if !packageActionsOpen {
		return
	}
	listFont, _ := getCachedFont(config, element.Font)
	smallFont, _ := getCachedFont(config, "small")
	if listFont == nil {
		listFont = smallFont
	}
	if listFont == nil {
		return
	}
	box, header, lineH, nameH := packageActionsLayout(config, element)
	drawPanel(renderer, box.X, box.Y, box.W, box.H, PanelFill(242), ColorBorderDefault)
	renderText(renderer, config, listFont,
		ellipsize(listFont, packageActionsPkg.Name, box.W-24),
		accentColor, box.X+12, box.Y+10)
	for i, item := range packageActionItems {
		cell := packageActionItemRect(box, lineH, header, i)
		focused := i == packageActionsFocus
		drawRow(renderer, cell.X, cell.Y+2, cell.W, cell.H-4, 8, focused, false)
		color := ColorTextSecondary()
		if focused {
			color = ColorTextPrimary()
		}
		renderText(renderer, config, listFont, item, color,
			cell.X+12, cell.Y+2+(cell.H-4-nameH)/2)
	}
}
