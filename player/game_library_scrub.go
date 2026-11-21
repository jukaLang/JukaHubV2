package main

// game_library_scrub.go — letter scrubbing on a console's page of the Games
// library: L2 / R2 jump to the previous / next letter, and a big letter shows
// over the list for a moment so the eye knows where it landed.
//
// A console with a few hundred games was a long walk with the D-pad, even with
// the held-direction repeat (navrepeat.go). Handheld front ends answer that
// with alphabet jumps; here they are:
//
//   - L2 / R2 (PgUp / PgDn on a keyboard) in every layout of a console's page
//     — the list, the 3D shelf and the cover column. The library swallowed the
//     triggers until now (shoulders.go's modal blocker), so they were free.
//   - Right in the list and the cover column, where it did nothing: a column
//     has no row to the right. Left still goes back to the consoles.
//   - A held trigger keeps jumping (after the same first delay as a held
//     D-pad, Accessibility's repeat delay, then every libScrubRepeatMS); a
//     held Right or PgDn repeats through the ordinary repeat paths.
//
// What a "letter" is follows the sort in force (game_library_view.go), so a
// jump always lands at the start of a block the order really has:
//
//	Title           the first letter of the name on screen (after a leading
//	                "The", "A" or "An" when the sort ignores them,
//	                libsort_articles.go), upper case; digits
//	                and symbols are one "#" group. Letters outside A–Z are
//	                their own group ("É", "Ж") because the sort puts them
//	                where their bytes fall, not under A–Z.
//	Year            the decade ("1990s"); games without a year are "No year".
//	Rating          the stars ("4★"); unrated games are "Unrated".
//	Play time       10h+, 1h+, under an hour, never played.
//	Last played /   the month of the date itself ("Nov 2026"), never "today"
//	Recently added  or "this week": a TrimUI starts before NTP sets the clock,
//	                so nothing here is measured against now.
//
// A jump forward goes to the first game whose group differs from the focused
// one; a jump back goes to the start of the focused group, or, from there, to
// the start of the one before. That rule works on any order, so a group that
// appears twice (unrated games between rated ones) is simply visited twice.
// At either end of the list nothing moves and the bubble shows the group the
// focus is in. The Continue row (recently played, not sorted) does not scrub.
//
// The bubble is Home's glass: a rounded sheet over the middle of the games,
// full for libScrubHoldMS after the last jump and fading over libScrubFadeMS
// (four alpha steps, so the faded letter is four cached textures, not one per
// frame). While it shows, uiAnimating (anim.go) keeps the frame rate up.

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// libScrubHoldMS is how long the bubble stays fully visible after a jump,
	// and libScrubFadeMS how long it then takes to go.
	libScrubHoldMS = 380
	libScrubFadeMS = 220
	// libScrubRepeatMS is the cadence of a held trigger after its first
	// delay: slower than a held D-pad's rows, since every step is a block.
	libScrubRepeatMS = 150
	// libScrubFontSize is the bubble's letter, in design px.
	libScrubFontSize = 64
)

// gameLibScrub is the bubble and the held trigger. Main thread.
var gameLibScrub struct {
	label  string // the group the last jump showed
	system string // on which console's page
	at     uint64 // when, in SDL ticks
	held   bool   // a trigger that scrubbed is still down
	dir    int    // its direction: -1 L2, +1 R2
	next   uint64 // when the held trigger jumps again
}

// libraryScrubGroup is the block a game belongs to under a sort mode.
func libraryScrubGroup(g libraryGame, mode string) string {
	switch librarySortValue(mode) {
	case librarySortYear:
		if g.Year <= 0 {
			return "No year"
		}
		return strconv.Itoa(g.Year/10*10) + "s"
	case librarySortRate:
		if g.Rating <= 0 {
			return "Unrated"
		}
		return strconv.Itoa(g.Rating) + "★"
	case librarySortTime:
		switch {
		case g.Playtime >= 10*3600:
			return "10h+"
		case g.Playtime >= 3600:
			return "1h+"
		case g.Playtime > 0:
			return "Under 1h"
		}
		return "Not played"
	case librarySortLast:
		return libraryScrubMonth(g.LastPlayed, "Never played")
	case librarySortAdded:
		return libraryScrubMonth(g.AddedAt, "No date")
	}
	// The letter the name sorts by: Zelda's for "The Legend of Zelda"
	// (libsort_articles.go).
	return libraryScrubLetter(librarySortLead(g.Label()))
}

// libraryScrubMonth is a date's month and year, or none for the zero time.
func libraryScrubMonth(t time.Time, none string) string {
	if t.IsZero() {
		return none
	}
	return t.Local().Format("Jan 2006")
}

// libraryScrubLetter is the first letter of a name, upper case, or "#" for a
// name that starts with a digit or a symbol.
func libraryScrubLetter(name string) string {
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) {
			return string(unicode.ToUpper(r))
		}
		return "#"
	}
	return "#"
}

// libraryScrubTarget is the index a jump from cursor in direction dir lands
// on, or -1 when there is no block that way.
func libraryScrubTarget(games []libraryGame, cursor, dir int, mode string) int {
	n := len(games)
	if n == 0 || dir == 0 {
		return -1
	}
	cursor = max(0, min(cursor, n-1))
	group := func(i int) string { return libraryScrubGroup(games[i], mode) }
	cur := group(cursor)
	if dir > 0 {
		for i := cursor + 1; i < n; i++ {
			if group(i) != cur {
				return i
			}
		}
		return -1
	}
	start := cursor
	for start > 0 && group(start-1) == cur {
		start--
	}
	if start < cursor {
		return start
	}
	if start == 0 {
		return -1
	}
	prev := group(start - 1)
	i := start - 1
	for i > 0 && group(i-1) == prev {
		i--
	}
	return i
}

// gameLibraryScrubSystem is the console page scrubbing works on: a page with
// at least two games that is not the Continue row.
func gameLibraryScrubSystem() (librarySystem, bool) {
	if !gameLib.open || gameLib.focus != 1 {
		return librarySystem{}, false
	}
	system, ok := selectedLibrarySystem()
	if !ok || system.Name == continueSystemName || len(system.Games) < 2 {
		return librarySystem{}, false
	}
	return system, true
}

// gameLibraryScrub jumps one block in direction dir and shows its bubble. It
// reports whether the page scrubs at all (so a press on the grid or on the
// Continue row is left to its old meaning).
func gameLibraryScrub(dir int) bool {
	system, ok := gameLibraryScrubSystem()
	if !ok || dir == 0 {
		return false
	}
	mode := librarySortMode()
	if target := libraryScrubTarget(system.Games, gameLib.cursor, dir, mode); target >= 0 {
		gameLib.cursor = target
		PlayNavSound()
	}
	cursor := max(0, min(gameLib.cursor, len(system.Games)-1))
	gameLibScrub.label = libraryScrubGroup(system.Games[cursor], mode)
	gameLibScrub.system = system.Name
	gameLibScrub.at = sdl.GetTicks64()
	return true
}

// gameLibraryScrubOwnsTriggers reports whether a trigger press belongs to the
// library's page: a console's games on screen with no sheet, keyboard or
// other modal panel above them.
func gameLibraryScrubOwnsTriggers() bool {
	if !gameLib.open || gameLib.focus != 1 || virtualKeyboardActive ||
		gameOptsUI.open || gameRandomUI.open || gmetaSheetOpen() || dlPanel.open {
		return false
	}
	// Nothing drawn above the page: the overlays that swallow the triggers,
	// the Start menu and the rest of the chrome (shoulders.go routes those
	// before the library, but a held trigger's repeat asks again), and no
	// modal panel besides the library, before or after it in shoulders.go's
	// list (LAN play, the Emulators page, a popover).
	if triggerOwnedByOverlay() || homeEditActive() || tb.startOpen || nc.open || clipSearchOpen ||
		globalSearchOpen || qs.open || wpOpen || imageViewerPath != "" || shortcutsOpen || fileEditor.open {
		return false
	}
	return modalPanelBesideLibrary() == ""
}

// gameLibraryScrubTriggerDown reports the trigger on axis still pressed on
// any open pad: the handheld's own, or a Bluetooth pad opened beside it
// (gamepad_hotplug.go) whose trigger started the scrub.
func gameLibraryScrubTriggerDown(axis sdl.GameControllerAxis) bool {
	if gameController != nil && gameController.Axis(axis) >= padTriggerUpThreshold {
		return true
	}
	for _, pad := range extraGameControllers {
		if pad != nil && pad.Axis(axis) >= padTriggerUpThreshold {
			return true
		}
	}
	return false
}

// gameLibraryTrigger is L2 / R2 on the library (handleShoulderPress). The
// shoulders L1 / R1 arrive as buttons and keep their library jobs.
func gameLibraryTrigger(s padShoulder) bool {
	if s != padL2 && s != padR2 {
		return false
	}
	if !gameLibraryScrubOwnsTriggers() {
		return false
	}
	dir := 1
	if s == padL2 {
		dir = -1
	}
	if !gameLibraryScrub(dir) {
		return false
	}
	gameLibScrub.held, gameLibScrub.dir = true, dir
	gameLibScrub.next = sdl.GetTicks64() + uint64(a11yRepeatDelayMs())
	return true
}

// gameLibraryScrubTick repeats a held trigger; the main loop calls it once a
// frame through padTickRepeat (navrepeat.go). It reads the trigger itself
// rather than the press latch, so a release that some other handler took
// still stops the scrub. It reports whether it jumped.
func gameLibraryScrubTick() bool {
	if !gameLibScrub.held {
		return false
	}
	if !gameLibraryScrubOwnsTriggers() {
		gameLibScrub.held = false
		return false
	}
	axis := sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_TRIGGERRIGHT)
	if gameLibScrub.dir < 0 {
		axis = sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_TRIGGERLEFT)
	}
	if !gameLibraryScrubTriggerDown(axis) {
		gameLibScrub.held = false
		return false
	}
	now := sdl.GetTicks64()
	if now < gameLibScrub.next {
		return false
	}
	gameLibScrub.next = now + libScrubRepeatMS
	return gameLibraryScrub(gameLibScrub.dir)
}

// gameLibraryScrubReset forgets the bubble and the held trigger (the library
// closed, or the page changed).
func gameLibraryScrubReset() {
	gameLibScrub.label, gameLibScrub.system = "", ""
	gameLibScrub.at, gameLibScrub.held = 0, false
}

// gameLibraryScrubAlpha is the bubble's opacity now: 255 while it holds, then
// four steps down to 0.
func gameLibraryScrubAlpha(now uint64) uint8 {
	if gameLibScrub.label == "" || now < gameLibScrub.at {
		return 0
	}
	elapsed := now - gameLibScrub.at
	if elapsed < libScrubHoldMS {
		return 255
	}
	elapsed -= libScrubHoldMS
	if elapsed >= libScrubFadeMS {
		return 0
	}
	step := 4 - int(elapsed*4/libScrubFadeMS) // 4 … 1
	return uint8(step * 255 / 4)
}

// gameLibraryScrubAnimating reports whether the bubble is on screen or a
// trigger is held for the next jump, for the frame pacer (uiAnimating): an
// idle frame cadence would make a held trigger jump late.
func gameLibraryScrubAnimating() bool {
	if gameLibScrub.held {
		return true
	}
	return gameLibraryScrubOnPage() && gameLibraryScrubAlpha(sdl.GetTicks64()) > 0
}

// gameLibraryScrubOnPage reports whether the page on screen is the one the
// bubble belongs to: back on the grid, or on another console, it is gone.
func gameLibraryScrubOnPage() bool {
	if !gameLib.open || gameLib.focus != 1 || gameLibScrub.label == "" {
		return false
	}
	system, ok := selectedLibrarySystem()
	return ok && system.Name == gameLibScrub.system
}

// gameLibraryScrubHint is the footer's badge for the triggers, named after
// what a jump steps by under the sort in force.
func gameLibraryScrubHint() (FooterHint, bool) {
	if _, ok := gameLibraryScrubSystem(); !ok {
		return FooterHint{}, false
	}
	label := "Letters"
	switch librarySortValue(librarySortMode()) {
	case librarySortYear:
		label = "Decades"
	case librarySortRate:
		label = "Stars"
	case librarySortTime:
		label = "Play time"
	case librarySortLast, librarySortAdded:
		label = "Months"
	}
	return FooterHint{"L2/R2 / PgUp/PgDn", label}, true
}

// drawGameLibraryScrubBubble draws the bubble over the middle of area (the
// games column of the page).
func drawGameLibraryScrubBubble(renderer *sdl.Renderer, config *Config, area sdl.Rect) {
	if renderer == nil || area.W <= 0 || area.H <= 0 || !gameLibraryScrubOnPage() {
		return
	}
	alpha := gameLibraryScrubAlpha(sdl.GetTicks64())
	if alpha == 0 {
		return
	}
	font := getFontSized(config, "small", libScrubFontSize)
	if font == nil {
		return
	}
	label := gameLibScrub.label
	tw, th, err := font.SizeUTF8(label)
	if err != nil || th <= 0 {
		return
	}
	pad := int32(th) / 3
	side := int32(th) + 2*pad
	w := max(side, int32(tw)+2*pad)
	if w > area.W {
		w = area.W
	}
	rect := sdl.Rect{X: area.X + (area.W-w)/2, Y: area.Y + (area.H-side)/2, W: w, H: side}
	scale := func(c sdl.Color) sdl.Color {
		c.A = uint8(int(c.A) * int(alpha) / 255)
		return c
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	radius := side / 4
	fill := glassSheetFill()
	fill.A = 235
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, scale(fill))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 2, scale(glassAccent()))
	renderText(renderer, config, font, label, scale(ColorTextPrimary()),
		rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
}
