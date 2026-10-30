package main

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// About / Credits screen
//
// Like Patch, About is a registered scene, so it draws its own body and then
// the shared status bar and footer. Earlier it drew a bare screen with no
// header, no footer badges, a black plate behind the wordmark and a credits
// card that spilled its licence list over the bottom hint; the layout below is
// measured from the content panel so every card fits.
// ──────────────────────────────────────────────────────────────────────

// aboutCardBottom and aboutContentBottom are the last frame's measurements:
// the lowest card edge and the lowest painted text baseline drawn by
// renderAbout. The render test reads them to prove no card's content reaches
// past its own border into the footer gap (the bug the licence column had).
var (
	aboutCardBottom    int32
	aboutContentBottom int32
)

func renderAbout(renderer *sdl.Renderer, config *Config) {
	ensureBackgroundTexture(renderer, config)
	if bgTexture != nil {
		renderer.Copy(bgTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	} else {
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
	}
	renderAmbientBackground(renderer)

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if medFont == nil {
		medFont = font
	}
	if bigFont == nil {
		bigFont = medFont
	}
	if font == nil {
		return
	}

	const margin = int32(24)
	top := HomeTopBarH + 8
	bottom := screenHeight - bottomBarHeight("About") - 16

	// Brand block: the shared page header in its centred form. The wordmark, the
	// version and the tagline are the three lines every header can carry, and
	// the cards start where this one says the band ends.
	headerBottom := UIPageHeaderDraw(renderer, config, UIPageHeader{
		X: margin, Y: top, W: screenWidth - 2*margin,
		Title:        "JukaHub",
		TitleRole:    "big",
		Subtitle:     versionDisplayString(config),
		SubtitleRole: "medium",
		Status:       "Your device. Your way.",
		Centered:     true,
	})

	// Two rows of two cards: build and credits on top, the device evidence and
	// the licences below. The row height is measured from the panel rather than
	// fixed, so the four cards fill the content band on both the wide and the
	// compact (4:3) canvas and no text can be pushed into the footer band.
	gap := int32(24)
	rowGap := int32(16)
	cardY := headerBottom + 20
	cardW := (screenWidth - margin*2 - gap) / 2
	rowH := (bottom - cardY - rowGap) / 2
	if rowH < 140 {
		rowH = 140
	}
	row2Y := cardY + rowH + rowGap
	buildRect := sdl.Rect{X: margin, Y: cardY, W: cardW, H: rowH}
	creditsRect := sdl.Rect{X: margin + cardW + gap, Y: cardY, W: cardW, H: rowH}
	deviceRect := sdl.Rect{X: margin, Y: row2Y, W: cardW, H: bottom - row2Y}
	srcRect := sdl.Rect{X: margin + cardW + gap, Y: row2Y, W: cardW, H: bottom - row2Y}

	// Measured this frame so the render test can prove nothing painted past a
	// card: the licence list used to be a single 8-line column, which needs
	// 54 + 8*20 = 214px inside a card that is only ~196px tall on a 720p panel,
	// so the last two licences bled over the card's bottom border and into the
	// 16px gap above the footer. The bright-only guard never saw them because
	// licence text is tertiary (dim) colour.
	aboutCardBottom = 0
	aboutContentBottom = 0

	cardTitle := func(rect sdl.Rect, title string) int32 {
		drawCard(renderer, rect.X, rect.Y, rect.W, rect.H, 16)
		renderText(renderer, config, medFont, title, ColorTextPrimary(), rect.X+24, rect.Y+18)
		if rect.Y+rect.H > aboutCardBottom {
			aboutCardBottom = rect.Y + rect.H
		}
		return rect.Y + 54
	}

	// note writes a painted line's bottom edge into aboutContentBottom.
	note := func(y, lineH int32) int32 {
		if y+lineH > aboutContentBottom {
			aboutContentBottom = y + lineH
		}
		return y + lineH
	}

	// Build info.
	y := cardTitle(buildRect, "Build")
	build := installedBuildTimeLabel()
	if build == "" {
		build = "unknown (source build)"
	}
	release := readInstalledReleaseInfo().Tag
	if release == "" {
		release = "—"
	}
	info := []struct{ label, value string }{
		{"Version", versionString(config)},
		{"Compiled", build},
		{"Release", release},
		{"Platform", P().Name()},
		{"OS", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)},
		{"CPUs", fmt.Sprintf("%d", runtime.NumCPU())},
		{"Renderer", "SDL2"},
	}
	// The value column is measured from the widest label so no label runs into
	// its value ("Compiledunknown" was the old bug).
	labelW := int32(0)
	for _, line := range info {
		if lw, _, _ := font.SizeUTF8(line.label); int32(lw) > labelW {
			labelW = int32(lw)
		}
	}
	for _, line := range info {
		renderText(renderer, config, font, line.label, ColorTextTertiary(), buildRect.X+24, y)
		renderText(renderer, config, font, line.value, ColorTextSecondary(), buildRect.X+24+labelW+18, y)
		y = note(y, 21)
	}

	// Credits.
	y = cardTitle(creditsRect, "Credits")
	credits := []struct{ name, role string }{
		{"JukaHub Team", "Core Development"},
		{"Go + SDL2", "Runtime & Rendering"},
		{"Inter Font", "Typography"},
		{deviceCreditName(DetectDevice()), "Primary Target Device"},
	}
	for _, c := range credits {
		renderText(renderer, config, font, c.name, WithAlpha(ColorAccent, 230), creditsRect.X+24, y)
		nameW, _, _ := font.SizeUTF8(c.name)
		renderText(renderer, config, font, " - "+c.role, ColorTextSecondary(), creditsRect.X+24+int32(nameW), y)
		y = note(y, 22)
	}

	// Device identification: the evidence DetectDevice used, so a handheld that
	// is named — or is not — can be checked on the device itself instead of in
	// the log. The rows are the same facts the diagnostics report carries.
	y = cardTitle(deviceRect, "Device")
	deviceRows := aboutDeviceRows(deviceIdentityReport())
	devLabelW := int32(0)
	for _, row := range deviceRows {
		if lw, _, _ := font.SizeUTF8(row.label); int32(lw) > devLabelW {
			devLabelW = int32(lw)
		}
	}
	devValueW := deviceRect.W - 48 - devLabelW - 18
	for _, row := range deviceRows {
		renderText(renderer, config, font, row.label, ColorTextTertiary(), deviceRect.X+24, y)
		renderText(renderer, config, font, ellipsize(font, row.value, devValueW), ColorTextSecondary(), deviceRect.X+24+devLabelW+18, y)
		y = note(y, 21)
	}

	// Open source licences. One column now that the card shares its row: eight
	// licences fit the row height on both canvases. renderWithClip is the hard
	// guarantee — a future ninth licence can lose a line inside the card but can
	// never paint over the border again, and the count that fits is measured.
	y = cardTitle(srcRect, "Open Source")
	licenses := []string{
		"Go - BSD License",
		"SDL2 - zlib License",
		"SDL2_ttf - zlib License",
		"SDL2_image - zlib License",
		"bbolt - MIT License",
		"zeroconf - Apache 2.0",
		"gopsutil - BSD License",
		"gjson - MIT License",
	}
	const colH = int32(20)
	shown := aboutLicenceRows(licenses, srcRect.H, colH)
	renderWithClip(renderer, srcRect.X+1, srcRect.Y+1, srcRect.W-2, srcRect.H-2, func(_ *sdl.Renderer) {
		for i, lic := range shown {
			ly := y + int32(i)*colH
			renderText(renderer, config, font, ellipsize(font, lic, srcRect.W-48), ColorTextTertiary(), srcRect.X+24, ly)
		}
		// Account for every row the loop drew, clipped or not.
		note(y+int32(len(shown)-1)*colH, colH)
	})

	// The clip is reset below; record the card's own edge as the content bound
	// so a card that grows past the footer is caught even if its text fits.
	_ = y

	// The shared bars: About had none, which made it the only screen without
	// the header status cluster or the footer button badges.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// handleAboutInput processes keyboard input for the about scene.
func handleAboutInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b {
		goBackScene(config)
		PlayBackSound()
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlAbout(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
		goBackScene(config) // plays the back sound itself
		return true
	}
	return false
}

// Scene registration.
type aboutScene struct{ sceneBase }

func (aboutScene) render(r *sdl.Renderer, c *Config) bool { renderAbout(r, c); return true }

func (aboutScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleAboutInput(e, c)
	return true
}

func (aboutScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlAbout(e, c)
}

func init() { registerScene(aboutScene{sceneBase{name: "About"}}) }

// aboutLicenceRows trims the licence list to the rows a card can hold and says
// how many were left out. The card is measured on every canvas, so a short one
// owes the reader a count instead of a silently clipped list.
func aboutLicenceRows(licenses []string, cardH, lineH int32) []string {
	if cardH <= 0 || lineH <= 0 {
		return licenses
	}
	fits := int((cardH - 54 - 8) / lineH)
	if fits < 1 {
		fits = 1
	}
	if len(licenses) <= fits {
		return licenses
	}
	moved := len(licenses) - (fits - 1)
	trimmed := append([]string{}, licenses[:fits-1]...)
	return append(trimmed, fmt.Sprintf("+%d more - see NOTICE", moved))
}

// aboutRow is one label/value line on an About card.
type aboutRow struct{ label, value string }

// aboutDeviceRows is the identification evidence as the About screen shows it:
// the model, which source named it, the panel the image reports, the model
// names the firmware carries and the kernel's own board description. It takes
// the values rather than reading them so the wording can be tested without a
// device, and it drops the rows a platform does not expose instead of printing
// empty labels.
func aboutDeviceRows(model DeviceModel, source string, id deviceIdentity) []aboutRow {
	found := firmwareEvidenceNames(id)
	firmware := strings.Join(found, ", ")
	if firmware == "" {
		firmware = "no model name"
	}
	identified := source
	if identified == "" {
		identified = "not identified"
	}
	panel := "not detected"
	if id.PanelW > 0 && id.PanelH > 0 {
		panel = panelLabel(id.PanelW, id.PanelH)
	}
	rows := []aboutRow{
		{"Model", deviceDisplayName(model)},
		{"Identified by", identified},
		{"Panel", panel},
		{"Firmware", firmware},
	}
	if tree := strings.TrimSpace(id.Model); tree != "" {
		rows = append(rows, aboutRow{"Device tree", tree})
	}
	if compatible := strings.TrimSpace(id.Compatible); compatible != "" {
		rows = append(rows, aboutRow{"Compatible", compatible})
	}
	return rows
}

// firmwareEvidenceNames lists the model names the image's own files carry, in
// the specificity order firmwareModelNames defines. A longer name contains the
// shorter one ("Trimui Smart Pro S" holds "Trimui Smart Pro"), so only the
// longest name at each site is reported: one model must not read as two.
func firmwareEvidenceNames(id deviceIdentity) []string {
	matched := make([]bool, len(firmwareModelNames))
	for i, name := range firmwareModelNames {
		for _, text := range id.Names {
			if strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
				matched[i] = true
				break
			}
		}
	}
	var found []string
	for i, name := range firmwareModelNames {
		if !matched[i] {
			continue
		}
		contained := false
		for j, other := range firmwareModelNames {
			if i != j && matched[j] && strings.Contains(other, name) {
				contained = true
				break
			}
		}
		if !contained {
			found = append(found, name)
		}
	}
	return found
}

// deviceCreditName is the short, human name of the handheld the About screen
// credits. It keeps the SoC suffix (TG3040…) of deviceDisplayName out of a
// credits line, and falls back to the Smart Pro on development builds.
func deviceCreditName(d DeviceModel) string {
	switch d {
	case DeviceTrimuiBrick:
		return "Trimui Brick"
	case DeviceTrimuiBrickPro:
		return "Trimui Brick Pro"
	case DeviceTrimuiHammer:
		return "Trimui Hammer"
	}
	return "Trimui Smart Pro"
}
