package main

import (
	"fmt"
	"runtime"

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
	cx := screenWidth / 2

	// Brand block: the wordmark is drawn like the header brand, with no plate
	// behind it, so the screen matches every other one.
	if bigFont != nil {
		logo := "JukaHub"
		lw, _, _ := bigFont.SizeUTF8(logo)
		renderText(renderer, config, bigFont, logo, ColorTextPrimary(), cx-int32(lw)/2, top+2)
	}
	ver := versionDisplayString(config)
	vw, _, _ := medFont.SizeUTF8(ver)
	renderText(renderer, config, medFont, ver, ColorTextSecondary(), cx-int32(vw)/2, top+52)
	tag := "Your device. Your way."
	tw, _, _ := font.SizeUTF8(tag)
	renderText(renderer, config, font, tag, ColorTextTertiary(), cx-int32(tw)/2, top+84)

	// Two columns of cards, then a full-width card for the licences. Heights
	// come from the panel, so adding a credit line cannot push text into the
	// footer band again.
	gap := int32(24)
	cardY := top + 112
	cardW := (screenWidth - margin*2 - gap) / 2
	rowH := int32(224)
	buildRect := sdl.Rect{X: margin, Y: cardY, W: cardW, H: rowH}
	creditsRect := sdl.Rect{X: margin + cardW + gap, Y: cardY, W: cardW, H: rowH}
	srcRect := sdl.Rect{X: margin, Y: cardY + rowH + 16, W: screenWidth - margin*2, H: bottom - (cardY + rowH + 16)}

	cardTitle := func(rect sdl.Rect, title string) int32 {
		drawCard(renderer, rect.X, rect.Y, rect.W, rect.H, 16)
		renderText(renderer, config, medFont, title, ColorTextPrimary(), rect.X+24, rect.Y+18)
		return rect.Y + 54
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
		y += 21
	}

	// Credits.
	y = cardTitle(creditsRect, "Credits")
	credits := []struct{ name, role string }{
		{"JukaHub Team", "Core Development"},
		{"Go + SDL2", "Runtime & Rendering"},
		{"Inter Font", "Typography"},
		{"Trimui Smart Pro", "Primary Target Device"},
	}
	for _, c := range credits {
		renderText(renderer, config, font, c.name, WithAlpha(ColorAccent, 230), creditsRect.X+24, y)
		nameW, _, _ := font.SizeUTF8(c.name)
		renderText(renderer, config, font, " - "+c.role, ColorTextSecondary(), creditsRect.X+24+int32(nameW), y)
		y += 22
	}

	// Open source licences.
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
	for _, lic := range licenses {
		renderText(renderer, config, font, lic, ColorTextTertiary(), srcRect.X+24, y)
		y += 20
	}

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
