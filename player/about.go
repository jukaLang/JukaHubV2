package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// About / Credits screen
// ──────────────────────────────────────────────────────────────────────

func renderAbout(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if medFont == nil {
		medFont = font
	}
	if bigFont == nil {
		bigFont = medFont
	}

	cx := int32(screenWidth / 2)

	// Logo.
	if bigFont != nil {
		logo := "JukaHub"
		lw, lh, _ := bigFont.SizeUTF8(logo)
		lx := cx - int32(lw)/2
		ly := int32(40)

		// Glow.
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 25)
		renderer.FillRect(&sdl.Rect{X: lx - 16, Y: ly - 8, W: int32(lw) + 32, H: int32(lh) + 16})

		renderText(renderer, config, bigFont, logo,
			sdl.Color{R: 240, G: 245, B: 255, A: 255}, lx, ly)
	}

	// Version.
	ver := versionString(config)
	if medFont != nil {
		vw, _, _ := medFont.SizeUTF8(ver)
		renderText(renderer, config, medFont, ver,
			sdl.Color{R: 140, G: 160, B: 200, A: 200},
			cx-int32(vw)/2, int32(100))
	}

	// Tagline.
	if font != nil {
		tag := "Your device. Your way."
		tw, _, _ := font.SizeUTF8(tag)
		renderText(renderer, config, font, tag,
			sdl.Color{R: 110, G: 125, B: 150, A: 160},
			cx-int32(tw)/2, int32(130))
	}

	// Build info card.
	cardX := int32(200)
	cardY := int32(170)
	cardW := int32(screenWidth - 400)
	cardH := int32(200)
	drawCard(renderer, cardX, cardY, cardW, cardH, 16)

	if font != nil {
		infoX := cardX + 24
		y := cardY + 20
		lineH := int32(24)

		lines := []string{
			fmt.Sprintf("Version:    %s", config.Version),
			fmt.Sprintf("Platform:   %s", P().Name()),
			fmt.Sprintf("OS:         %s/%s", runtime.GOOS, runtime.GOARCH),
			fmt.Sprintf("CPUs:       %d", runtime.NumCPU()),
			fmt.Sprintf("Renderer:   SDL2"),
			fmt.Sprintf("Build:      %s", time.Now().Format("2006-01-02")),
		}
		for _, line := range lines {
			renderText(renderer, config, font, line,
				sdl.Color{R: 160, G: 175, B: 200, A: 200}, infoX, y)
			y += lineH
		}
	}

	// Credits card.
	credY := cardY + cardH + 20
	credH := int32(260)
	drawCard(renderer, cardX, credY, cardW, credH, 16)

	if font != nil {
		infoX := cardX + 24
		y := credY + 20
		lineH := int32(22)

		// Title.
		if medFont != nil {
			renderText(renderer, config, medFont, "Credits",
				sdl.Color{R: 200, G: 210, B: 230, A: 220}, infoX, y)
			y += 30
		}

		credits := []struct {
			name string
			role string
		}{
			{"JukaHub Team", "Core Development"},
			{"Go + SDL2", "Runtime & Rendering"},
			{"Inter Font", "Typography"},
			{"Trimui Smart Pro", "Primary Target Device"},
		}
		for _, c := range credits {
			// Name in accent color.
			renderText(renderer, config, font, c.name,
				sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220},
				infoX, y)
			// Role in secondary.
			nameW, _, _ := font.SizeUTF8(c.name)
			renderText(renderer, config, font, " - "+c.role,
				sdl.Color{R: 110, G: 125, B: 150, A: 160},
				infoX+int32(nameW), y)
			y += lineH
		}

		y += 10
		if medFont != nil {
			renderText(renderer, config, medFont, "Open Source",
				sdl.Color{R: 200, G: 210, B: 230, A: 220}, infoX, y)
			y += 28
		}

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
			renderText(renderer, config, font, lic,
				sdl.Color{R: 100, G: 115, B: 140, A: 160}, infoX+16, y)
			y += lineH - 4
		}
	}

	// Controls hint.
	if font != nil {
		hint := "B/Esc: Back"
		hw, _, _ := font.SizeUTF8(hint)
		renderText(renderer, config, font, hint,
			sdl.Color{R: 80, G: 90, B: 110, A: 120},
			cx-int32(hw)/2, screenHeight-30)
	}
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
