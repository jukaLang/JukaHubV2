package main

// bootgreet.go — the boot and the shutdown greet the user by name.
//
// When Settings → Your name is set (userName, chat.go), the boot shows
// "Welcome back, <name>" under the wordmark as it comes into focus, and the
// shutdown card ends on "Goodbye, <name>" ("See you soon, <name>" for a
// restart) before the light goes out. With no name set both look exactly as
// they did. A screenshot never carries the name (screenshotmode.go).

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// bootGreetPxFrac is the greeting's font size on the boot screen, of the
	// height: under the wordmark (0.06) and over the status line.
	bootGreetPxFrac = 0.03
	// bootGreetRise is how far (of the height) the greeting rises as it fades
	// in, with motion on.
	bootGreetRise = 0.008
	// shutdownFarewellHoldMS replaces shutdownHoldMS when there is a name to say
	// goodbye to: long enough to read it before the dissolve.
	shutdownFarewellHoldMS = 1100.0
	// shutdownFarewellSize is the farewell's design size on the card.
	shutdownFarewellSize = 40
)

// bootGreetInk is the greeting's ink on the boot's black-to-theme backdrop: a
// step under the wordmark's.
var bootGreetInk = sdl.Color{R: 200, G: 208, B: 226, A: 255}

// bootGreetName is the name the boot and the shutdown greet: Settings → Your
// name, or "" when none is set or a screenshot is being taken. Main thread
// only (userName reads the config's variables).
func bootGreetName(c *Config) string {
	if shotModeOn() {
		return ""
	}
	return userName(c)
}

// bootWelcomeText is the boot's greeting for name ("" without one). The phrase
// is in the interface language; the name is drawn as written (renderTextRaw),
// never matched against the catalogue.
func bootWelcomeText(name string) string {
	if name == "" {
		return ""
	}
	return uiT("Welcome back") + ", " + name
}

// shutdownFarewellText is the shutdown card's last line for name ("" without
// one): a restart comes straight back, so it is not a goodbye.
func shutdownFarewellText(name string) string {
	if name == "" {
		return ""
	}
	if devPowerAction(devPowerPending.Load()) == devPowerReboot {
		return uiT("See you soon") + ", " + name
	}
	return uiT("Goodbye") + ", " + name
}

// bootGreetFit is text as drawn in maxW: whole when it fits, else cut with an
// ellipsis. Measured as written, like renderTextRaw draws it (ellipsize would
// put it through the interface language).
func bootGreetFit(font *ttf.Font, text string, maxW int32) string {
	if w, _ := measureTextRaw(font, text); w <= maxW {
		return text
	}
	return ellipsizeLine(font, text, maxW)
}

// drawGreeting paints the boot's greeting under the wordmark at fade, centred
// in the gap between the wordmark's line and the progress track.
func (r *bootOSRun) drawGreeting(f *bootOSFrame, fade float64) {
	if r.greet == "" {
		return
	}
	a := bootClamp01(fade) * bootClamp01(f.greet)
	if a <= 0 {
		return
	}
	px := int(float64(screenHeight)*bootGreetPxFrac + 0.5)
	if px < 12 {
		px = 12
	}
	font := loadCachedFont(homeUIFontFile, px)
	if font == nil {
		return
	}
	text := bootGreetFit(font, r.greet, screenWidth-32)
	w, h := measureTextRaw(font, text)
	top := r.layout.WordTop + r.layout.WordLineH
	if r.layout.WordLineH <= 0 {
		top = r.layout.WordTop + int32(px*2)
	}
	y := top + (r.layout.Track.Y-top-h)/2
	if y < top {
		y = top
	}
	if r.motion {
		y += int32(math.Round(float64(screenHeight) * bootGreetRise * (1 - f.greet)))
	}
	ink := bootOSInkOn(bootGreetInk, ColorTextSecondary(), bootOSDawnColour(f.dawn), f.dawn, glassLightTheme())
	renderTextRaw(r.renderer, r.config, font, text, stageInk(ink, a), int32(r.cx)-w/2, y)
}

// shutdownDrawFarewell paints the farewell on the shutdown card: it comes in
// over the card's centre while the step list steps back (bye 0 → 1 over the
// held card) and leaves with the wordmark as the light goes out (dissolve).
func shutdownDrawFarewell(renderer *sdl.Renderer, config *Config, text string, bye, dissolve float64, motion bool) {
	if text == "" || config == nil {
		return
	}
	a := bootSmoothstep(0.2, 0.6, bye) * (1 - bootSmoothstep(0.3, 0.85, dissolve))
	if a <= 0 {
		return
	}
	card := stageCardRect()
	if card.W <= 0 || card.H <= 0 {
		return
	}
	font := getFontSized(config, "big", shutdownFarewellSize)
	if font == nil {
		if font, _ = getCachedFont(config, "small"); font == nil {
			return
		}
	}
	pad := scaleX(40, screenWidth)
	label := bootGreetFit(font, text, card.W-2*pad)
	w, h := measureTextRaw(font, label)
	y := card.Y + (card.H-h)/2
	if motion {
		y += int32(math.Round(float64(scaleY(10, screenHeight)) * (1 - bootSmoothstep(0.2, 0.6, bye))))
	}
	renderTextRaw(renderer, config, font, label, stageInk(ColorTextPrimary(), a), card.X+(card.W-w)/2, y)
}
