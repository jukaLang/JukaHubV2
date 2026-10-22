package main

// musicuifeatures.go — the scene side of the audio player additions: the
// initialism search panel, the auto-exit timer, hold-to-skip and the screen-off
// modes. musicfeatures.go holds the rules; this file owns the state, input and
// drawing so musicplayer.go stays about playback.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Search state
// ──────────────────────────────────────────────────────────────────────

// musicSetSearchQuery re-filters as the user types, so results appear before
// the search is submitted.
func musicSetSearchQuery(query string) {
	music.mu.Lock()
	defer music.mu.Unlock()
	music.searchQuery = query
	music.searchHits = musicSearchHits(music.tracks, query, 40)
	if len(music.searchHits) == 0 {
		music.searchSel = -1
		return
	}
	if music.searchSel < 0 || music.searchSel >= len(music.searchHits) {
		music.searchSel = 0
	}
}

// musicSearchQueryValue is what the on-screen keyboard edits.
func musicSearchQueryValue() string {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.searchQuery
}

// musicSearchActive reports whether the search panel is on screen.
func musicSearchActive() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.searchOpen
}

// musicOpenSearch shows the panel and opens the on-screen keyboard, because the
// handheld has no hardware keyboard to type the initials on.
func musicOpenSearch() {
	music.mu.Lock()
	music.searchOpen = true
	music.searchQuery = ""
	music.searchHits = nil
	music.searchSel = -1
	music.mu.Unlock()
	PlayNavSound()
	vkOpenMusicSearch()
}

// musicCloseSearch dismisses the panel and any keyboard over it.
func musicCloseSearch() {
	music.mu.Lock()
	wasOpen := music.searchOpen
	music.searchOpen = false
	music.searchQuery = ""
	music.searchHits = nil
	music.searchSel = -1
	music.mu.Unlock()
	if virtualKeyboardActive {
		vkCloseKeyboard()
	}
	if wasOpen {
		PlayBackSound()
	}
}

// musicSearchRowHits is filled by the render pass so a tap plays the row it
// lands on, not the row that was highlighted before the tap.
var musicSearchRowHits []struct {
	track int
	rect  sdl.Rect
}

// musicSelectSearchHit highlights one visible row and plays it.
func musicSelectSearchHit(track int) {
	music.mu.Lock()
	for i, idx := range music.searchHits {
		if idx == track {
			music.searchSel = i
			break
		}
	}
	music.mu.Unlock()
	if !musicSearchPlay() {
		showToast("No song matches those initials", ToastInfo())
	}
}

// musicSearchPlay plays the highlighted hit and closes the panel. It reports
// false when the query matched nothing.
func musicSearchPlay() bool {
	music.mu.Lock()
	hits := append([]int(nil), music.searchHits...)
	sel := music.searchSel
	music.mu.Unlock()
	if sel < 0 || sel >= len(hits) {
		return false
	}
	musicPlayTrack(hits[sel])
	musicCloseSearch()
	return true
}

// musicSearchMoveSelection moves through the hits while the keyboard is closed.
func musicSearchMoveSelection(delta int) {
	music.mu.Lock()
	defer music.mu.Unlock()
	next := musicSearchMove(music.searchHits, music.searchSel, delta)
	music.searchSel = next
}

// musicHandleSearchKey drives the panel after the keyboard is dismissed. It
// reports whether the key belonged to the panel.
func musicHandleSearchKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !musicSearchActive() {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		musicSearchMoveSelection(-1)
		PlayNavSound()
	case sdl.K_DOWN:
		musicSearchMoveSelection(1)
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if !musicSearchPlay() {
			showToast("No song matches those initials", ToastInfo())
		}
	case sdl.K_F2, sdl.K_SLASH:
		vkOpenMusicSearch()
	case sdl.K_ESCAPE, sdl.K_b:
		musicCloseSearch()
	default:
		return false
	}
	return true
}

// Controller chord: X followed by D-Pad Right opens search. Both buttons do
// something on their own (pause and seek), so the chord is timed and the pause
// is undone when it fires, leaving the player exactly as it was.
var musicSearchChord struct {
	armed bool
	at    time.Time
}

const musicSearchChordWindow = 450 * time.Millisecond

func musicArmSearchChord() {
	musicSearchChord.armed = true
	musicSearchChord.at = time.Now()
}

func musicTakeSearchChord() bool {
	if !musicSearchChord.armed || time.Since(musicSearchChord.at) > musicSearchChordWindow {
		musicSearchChord.armed = false
		return false
	}
	musicSearchChord.armed = false
	return true
}

// Controller chord: Y followed by D-Pad Left toggles background playback. Y
// alone still favorites the track, and Left alone still seeks, so the chord is
// timed: a press that arrives too late leaves both of those untouched.
var musicBackgroundChord struct {
	armed bool
	at    time.Time
}

func musicArmBackgroundChord() {
	musicBackgroundChord.armed = true
	musicBackgroundChord.at = time.Now()
}

func musicTakeBackgroundChord() bool {
	if !musicBackgroundChord.armed || time.Since(musicBackgroundChord.at) > musicSearchChordWindow {
		musicBackgroundChord.armed = false
		return false
	}
	musicBackgroundChord.armed = false
	return true
}

// musicToggleBackground flips background playback and reports the new state.
func musicToggleBackground(config *Config) bool {
	if config == nil {
		config = appConfig
	}
	on := !musicBackgroundEnabled(config)
	musicSetBackgroundEnabled(config, on)
	if on {
		showToast("Music keeps playing in the background", ToastSuccess())
	} else {
		showToast("Music stops when you leave the player", ToastInfo())
	}
	PlayNavSound()
	return on
}

// musicLeavePlayer is the single exit point: it keeps playback alive when the
// user asked for background audio and stops it otherwise.
func musicLeavePlayer(config *Config) {
	if musicBackgroundEnabled(config) {
		return
	}
	musicStop()
}

// ──────────────────────────────────────────────────────────────────────
// Auto-exit timer
// ──────────────────────────────────────────────────────────────────────

// musicSetExitTimer arms (or cancels) closing the app after d. Playback stops
// first so the goodbye animation is not competing with an audio decoder, and
// the timer is cancelled by leaving the player? No: it deliberately survives,
// like the sleep timer, because "close after this album" is the point.
func musicSetExitTimer(d time.Duration) {
	music.mu.Lock()
	music.exitGen++
	gen := music.exitGen
	if d <= 0 {
		music.exitUntil = time.Time{}
		music.mu.Unlock()
		return
	}
	music.exitUntil = time.Now().Add(d)
	music.mu.Unlock()

	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		<-timer.C
		music.mu.Lock()
		valid := gen == music.exitGen
		if valid {
			music.exitUntil = time.Time{}
		}
		music.mu.Unlock()
		if !valid {
			return
		}
		musicStop()
		showToast("Auto-exit timer closed the player", ToastInfo())
		requestAppExit()
	}()
}

// musicCycleExitTimer steps off → 15 → 30 → 60 → 90 minutes and returns the
// armed minutes (0 = off).
func musicCycleExitTimer() int {
	next := musicTimerCycle(musicExitRemaining(), musicExitOptions)
	musicSetExitTimer(next)
	return int(next.Minutes())
}

// musicExitRemaining is the time left on the auto-exit timer (0 when none).
func musicExitRemaining() time.Duration {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.exitUntil.IsZero() {
		return 0
	}
	left := time.Until(music.exitUntil)
	if left < 0 {
		return 0
	}
	return left
}

// ──────────────────────────────────────────────────────────────────────
// Hold-to-skip
// ──────────────────────────────────────────────────────────────────────

// musicSceneActive reports whether the audio player is the scene on screen, so
// a key release on another screen is not routed into the player's hold tracker.
func musicSceneActive() bool {
	return appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) &&
		appConfig.Scenes[currentSceneIndex].Name == "MusicPlayer"
}

// music.volHold is only touched on the event thread, so it needs no lock.
func musicHandleVolumeKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !musicVolumeKey(e.Keysym.Sym) {
		return false
	}
	if e.Type == sdl.KEYUP {
		// Letting go ends the hold so the next press adjusts volume again
		// instead of looking like one long hold.
		music.volHold.release(e.Keysym.Sym)
		return true
	}
	if e.Type != sdl.KEYDOWN {
		return false
	}
	now := time.Now()
	// The same physical key can also arrive from the kernel input device, where
	// the long press is detected too. Record the SDL event so the watcher drops
	// its duplicate instead of skipping twice.
	if e.Keysym.Sym == sdl.K_VOLUMEUP || e.Keysym.Sym == sdl.K_VOLUMEDOWN {
		noteSDLVolumeKey()
	}
	delta, fresh := music.volHold.press(e.Keysym.Sym, now)
	if fresh {
		if dir, ok := musicVolumeDirection(e.Keysym.Sym); ok {
			musicAdjustVolume(0.1 * float64(dir))
		}
		return true
	}
	switch delta {
	case 1:
		musicNext()
		showToast("Next track", ToastInfo())
		PlayNavSound()
	case -1:
		musicPrev()
		showToast("Previous track", ToastInfo())
		PlayNavSound()
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Screen off
// ──────────────────────────────────────────────────────────────────────

func musicScreenOffActive() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.screenOff
}

// musicSetScreenOff darkens the panel without stopping playback. Turning it on
// closes the search panel first: a keyboard over a black screen is a trap.
func musicSetScreenOff(off bool) {
	music.mu.Lock()
	music.screenOff = off
	music.mu.Unlock()
	if off {
		musicCloseSearch()
	}
	if off {
		showToast("Screen off — Select wakes it", ToastInfo())
	}
}

func musicToggleScreenOff() {
	musicSetScreenOff(!musicScreenOffActive())
}

// musicHandleScreenOffKey gates keyboard input while the panel is dark. It
// returns true when the key was consumed (wake or blocked); false means the
// normal player handler should act on it (controls mode).
func musicHandleScreenOffKey(e *sdl.KeyboardEvent, mode musicScreenMode) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	action := musicScreenOffKey(e.Keysym.Sym)
	if action == musicActionVolume {
		// Volume keys never wake the panel: they belong to the mixer, and
		// holding one skips a track even while it is dark. This is the "locked
		// screen" behaviour phones have, and it works in every mode.
		return false
	}
	if mode == musicScreenAny {
		musicSetScreenOff(false)
		return true
	}
	if action == musicActionWake {
		musicSetScreenOff(false)
		return true
	}
	return !musicScreenActionAllowed(mode, action)
}

// musicHandleScreenOffButton is the controller equivalent.
func musicHandleScreenOffButton(button uint8, mode musicScreenMode) bool {
	action := musicScreenOffButton(button)
	if mode == musicScreenAny {
		musicSetScreenOff(false)
		return true
	}
	if action == musicActionWake {
		musicSetScreenOff(false)
		return true
	}
	return !musicScreenActionAllowed(mode, action)
}

// renderMusicScreenOff paints the panel black while audio keeps decoding.
func renderMusicScreenOff(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
}

// ──────────────────────────────────────────────────────────────────────
// Search panel
// ──────────────────────────────────────────────────────────────────────

// renderMusicSearchOverlay draws the query and the ranked hits over the player.
// It is called after the normal frame so the panel is always on top.
func renderMusicSearchOverlay(renderer *sdl.Renderer, config *Config) {
	music.mu.Lock()
	open := music.searchOpen
	query := music.searchQuery
	hits := append([]int(nil), music.searchHits...)
	sel := music.searchSel
	tracks := append([]musicTrack(nil), music.tracks...)
	music.mu.Unlock()
	if !open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medFont == nil {
		medFont = font
	}

	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	rows := len(hits)
	if rows > 7 {
		rows = 7
	}
	panelH := int32(150) + int32(rows)*34
	if panelH > screenHeight-80 {
		panelH = screenHeight - 80
	}
	px := (screenWidth - panelW) / 2
	py := (screenHeight - panelH) / 2
	drawCard(renderer, px, py, panelW, panelH, 18)

	title := "SEARCH SONGS"
	renderText(renderer, config, font, title, ColorTextTertiary(), px+24, py+18)
	renderText(renderer, config, medFont, ellipsize(medFont, "Type initials: iwyn = I Want You Now", panelW-48),
		ColorTextSecondary(), px+24, py+40)

	queryText := query
	if queryText == "" {
		queryText = "…"
	}
	field := sdl.Rect{X: px + 24, Y: py + 78, W: panelW - 48, H: 46}
	fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, 10, WithAlpha(ColorPanelRaised, 255))
	strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, 10, 1, WithAlpha(ColorBorder, 200))
	renderText(renderer, config, medFont, ellipsize(medFont, queryText, field.W-24),
		ColorTextPrimary(), field.X+14, field.Y+(field.H-int32(medFont.Height()))/2)

	listY := field.Y + field.H + 12
	musicSearchRowHits = musicSearchRowHits[:0]
	if len(hits) == 0 {
		msg := "No matches yet"
		if strings.TrimSpace(query) != "" {
			msg = "No song matches those initials"
		}
		renderText(renderer, config, font, msg, ColorTextTertiary(), px+24, listY+8)
	} else {
		// Keep the highlighted hit visible when the list is longer than the
		// panel.
		start := 0
		if sel >= rows {
			start = sel - rows + 1
		}
		for i := 0; i < rows && start+i < len(hits); i++ {
			idx := hits[start+i]
			if idx < 0 || idx >= len(tracks) {
				continue
			}
			row := sdl.Rect{X: px + 18, Y: listY + int32(i)*34, W: panelW - 36, H: 30}
			if start+i == sel {
				fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 8, WithAlpha(ColorCardFocus, 255))
			}
			musicSearchRowHits = append(musicSearchRowHits, struct {
				track int
				rect  sdl.Rect
			}{track: idx, rect: row})
			name := strings.TrimSuffix(tracks[idx].Name, filepath.Ext(tracks[idx].Name))
			col := ColorTextPrimary()
			if start+i == sel {
				col = accentColor
			}
			renderText(renderer, config, font, ellipsize(font, name, row.W-24), col, row.X+12,
				row.Y+(row.H-int32(font.Height()))/2)
		}
	}

	hint := hintLine(FooterHint{"A / Enter", "Play"}, FooterHint{"↑ ↓", "Choose"}, FooterHint{"B", "Close"})
	if virtualKeyboardActive {
		hint = "Type the initials on the keyboard, then A to search"
	}
	hintW, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(), px+(panelW-int32(hintW))/2, py+panelH-30)

	// Keep the exit timer visible behind the panel: a timer the user forgot is
	// worse than an empty corner.
	if left := musicExitRemaining(); left > 0 {
		label := "Exit " + formatSleepRemaining(left)
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextTertiary(), px+panelW-int32(lw)-24, py+18)
	}
}

// musicExitStatusLine is the compact timer text shared by the status line and
// the playlist header button.
func musicExitStatusLine() string {
	left := musicExitRemaining()
	if left <= 0 {
		return ""
	}
	return "Exit " + formatSleepRemaining(left)
}

// musicTimerToast describes a just-armed timer for the toast layer.
func musicTimerToast(kind string, minutes int) string {
	if minutes == 0 {
		return kind + " timer off"
	}
	return fmt.Sprintf("%s timer: %d min", kind, minutes)
}

// musicHeaderButton draws one small pill in the playlist header and returns its
// hit rect. active fills it with the accent so an armed timer and an open
// search are visible without another label.
func musicHeaderButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, label string, active bool) sdl.Rect {
	rect := sdl.Rect{X: x, Y: y, W: w, H: h}
	if renderer == nil || font == nil {
		return rect
	}
	fill := WithAlpha(ColorPanelRaised, 255)
	border := WithAlpha(ColorBorder, 200)
	fg := ColorTextSecondary()
	if active {
		fill = WithAlpha(accentColor, 40)
		border = accentColor
		fg = accentColor
	}
	fillRoundedRect(renderer, x, y, w, h, 8, fill)
	strokeRoundedRect(renderer, x, y, w, h, 8, 1, border)
	tw, _, _ := font.SizeUTF8(label)
	if int32(tw) > w-12 {
		label = ellipsize(font, label, w-12)
		tw, _, _ = font.SizeUTF8(label)
	}
	renderText(renderer, config, font, label, fg, x+(w-int32(tw))/2, y+(h-int32(font.Height()))/2)
	return rect
}
