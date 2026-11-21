package main

// game_history_forget.go — the play history panel's "Delete this game's
// record" (X, then a confirm), and the per-game figures the Game Tracker of NX
// Redux shows: how many times a game was played and how long a session lasts
// on average.
//
// gameHistoryForget removes what the tracker knows about one game: its
// launches and sessions (game-history.json), its lifetime play time
// (playtime.data.Games, plus a bare-title entry from an older save that the
// game page counts as this game's, playtimeGameSecondsWith) and its days in the
// By game store (playtime-days.json, playtime_periods.go, which has no forget
// of its own). The game page's play count (autocoll.go) is read from the
// history, so it goes with it.
//
// It never touches saves or save states, the recents, favourites, tags or
// notes. What stays on purpose: the app-wide totals (time in JukaHub, the day
// totals and the trophies earned from them) and GameDays, the all-games day
// total the daily play limit reads — deleting a game's record must not hand
// back time a limit has already counted.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// gameHistoryForgotten says what gameHistoryForget removed.
type gameHistoryForgotten struct {
	Launches int
	Seconds  int64
	Days     bool
}

// gameHistoryForget deletes the record of the game under romKey
// (playtimeGameKey: "System|Title", or a bare title) and saves the stores it
// changed. Main thread: the playtime ledger is the render loop's.
func gameHistoryForget(romKey string) gameHistoryForgotten {
	var out gameHistoryForgotten
	key := strings.TrimSpace(romKey)
	if key == "" {
		return out
	}
	system, title := playtimeGameKeyParts(key)

	gameHistoryStore.mu.Lock()
	gameHistoryLoadLocked()
	if rec, ok := gameHistoryStore.data.Games[key]; ok {
		out.Launches = rec.Launches
		delete(gameHistoryStore.data.Games, key)
		gameHistorySaveLocked()
	}
	gameHistoryStore.mu.Unlock()

	// The keys whose time is this game's: its own, and a bare title from before
	// keys named the console, when no other console knows that title (the same
	// rule the game page adds it by).
	keys := []string{key}
	changed := false
	if playtime.loaded && playtime.data.Games != nil {
		games := playtime.data.Games
		if secs, ok := games[key]; ok {
			out.Seconds += secs
			delete(games, key)
			changed = true
		}
		if system != "" && title != "" {
			if legacy, ok := games[title]; ok && !playtimeTitleOnOtherConsole(games, recentGames(), gameLib.lib.Systems, system, title) {
				out.Seconds += legacy
				delete(games, title)
				keys = append(keys, title)
				changed = true
			}
		}
	}
	out.Days = gameHistoryForgetDays(keys)
	if changed || out.Days {
		playtime.rev++
		// Saves the ledger with the config, and the By game days beside it.
		playtimePersist(appConfig)
	}
	logInfo("library", "play record deleted for a game (%d launches, %d s)", out.Launches, out.Seconds)
	return out
}

// gameHistoryForgetDays removes the keys from the By game store, every day of
// it, and from what this run credited (its re-anchor notes). It reports whether
// a day held one of them.
func gameHistoryForgetDays(keys []string) bool {
	playtimePeriodsEnsure()
	p := &playtimePeriods
	removed := false
	for day, games := range p.data.Days {
		for _, k := range keys {
			if _, ok := games[k]; ok {
				delete(games, k)
				removed = true
			}
		}
		if len(games) == 0 {
			delete(p.data.Days, day)
		}
	}
	for _, games := range p.run {
		for _, k := range keys {
			delete(games, k)
		}
	}
	if removed {
		p.dirty = true
		p.rev++
	}
	return removed
}

// ── The panel's figures ────────────────────────────────────────────────────

// gameHistoryAverage is the average measured session: the time the history
// recorded over the sessions it measured. Launches that ended quickly or were
// interrupted are not sessions here, as in the panel's own count.
func gameHistoryAverage(rec gameHistoryRecord) int64 {
	if rec.Completed <= 0 || rec.Total <= 0 {
		return 0
	}
	return rec.Total / int64(rec.Completed)
}

// gameHistoryPlayedLabel is the play count the way the game page's chip says
// it (autocoll.go): "Played once", "Played 5 times".
func gameHistoryPlayedLabel(launches int) string {
	switch {
	case launches <= 0:
		return "Not played yet"
	case launches == 1:
		return "Played once"
	}
	return "Played " + strconv.Itoa(launches) + " times"
}

// gameHistoryStatRows are the summary lines at the top of the panel.
func gameHistoryStatRows(rec gameHistoryRecord, st gameHistorySummary, total int64) []string {
	sessions := strconv.Itoa(st.Sessions) + " sessions"
	if st.Sessions == 1 {
		sessions = "1 session"
	}
	average := "Average session —"
	if avg := gameHistoryAverage(rec); avg > 0 {
		average = "Average session " + formatPlaytime(avg)
	}
	return []string{
		"Total " + formatPlaytime(total) + "  ·  " + gameHistoryPlayedLabel(st.Launches) + "  ·  " + sessions,
		average + "  ·  Longest " + formatPlaytime(st.Longest),
		"Last 7 days " + formatPlaytime(st.Last7) + "  ·  Last 30 days " + formatPlaytime(st.Last30),
	}
}

// ── Delete this game's record: the confirm ─────────────────────────────────

// gameHistoryForgetUI is the confirm drawn over the panel. Render thread.
var gameHistoryForgetUI struct {
	open    bool
	yes, no sdl.Rect
	// ask is the panel footer's "X Delete record", set as the panel draws.
	ask sdl.Rect
}

// gameHistoryForgetAsk opens the confirm for the game on the page. A game with
// nothing recorded has nothing to delete, which is said instead.
func gameHistoryForgetAsk() {
	if !gameHistoryUI.open {
		return
	}
	system, title := gmetaUI.game.System, gmetaUI.game.Title
	rec := gameHistoryFor(system, title)
	// The By game days can outlive the ledger's entry (it keeps the longest
	// 64), so they count as something to delete too.
	if rec.Launches == 0 && len(rec.Sessions) == 0 && playtimeGameSecondsFor(system, title) <= 0 &&
		playtimePeriodsGameLine(system, title) == "" {
		PlayErrorSound()
		showToast("Nothing recorded for this game yet", ToastInfo())
		return
	}
	gameHistoryForgetUI.open = true
	gameHistoryForgetUI.yes, gameHistoryForgetUI.no = sdl.Rect{}, sdl.Rect{}
	PlayActivateSound()
}

// gameHistoryForgetCancel closes the confirm and keeps the record.
func gameHistoryForgetCancel() {
	gameHistoryForgetUI.open = false
	PlayBackSound()
}

// gameHistoryForgetConfirm deletes the record of the game on the page.
func gameHistoryForgetConfirm() {
	gameHistoryForgetUI.open = false
	game := gmetaUI.game
	gone := gameHistoryForget(playtimeGameKey(game.System, game.Title))
	// The page's "Played N times" chip was read when it opened (autocoll.go).
	if autoCollPlays.path == game.Path {
		autoCollPlays.n = 0
	}
	gameHistoryUI.scroll = 0
	PlaySuccessSound()
	msg := "Play record deleted"
	if gone.Seconds > 0 {
		msg += " — " + formatPlaytime(gone.Seconds) + " removed"
	}
	showToast(msg+" · saves are kept", ToastSuccess())
}

// gameHistoryForgetPad answers the pad while the confirm is up: A deletes, B
// keeps. Every other button is swallowed.
func gameHistoryForgetPad(e *sdl.ControllerButtonEvent) bool {
	if !gameHistoryForgetUI.open || e == nil {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		gameHistoryForgetConfirm()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		gameHistoryForgetCancel()
	}
	return true
}

// gameHistoryForgetKey is the keyboard's twin: Enter deletes, Esc keeps.
func gameHistoryForgetKey(e *sdl.KeyboardEvent) bool {
	if !gameHistoryForgetUI.open || e == nil {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if e.Repeat == 0 {
			gameHistoryForgetConfirm()
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		gameHistoryForgetCancel()
	}
	return true
}

// gameHistoryForgetMouse answers a click while the confirm is up.
func gameHistoryForgetMouse(mx, my int32) bool {
	if !gameHistoryForgetUI.open {
		return false
	}
	switch {
	case pointInRect(mx, my, gameHistoryForgetUI.yes):
		gameHistoryForgetConfirm()
	case pointInRect(mx, my, gameHistoryForgetUI.no):
		gameHistoryForgetCancel()
	}
	return true
}

// gameHistoryForgetRender draws the confirm over the panel's card.
func gameHistoryForgetRender(renderer *sdl.Renderer, config *Config, card sdl.Rect) {
	if !gameHistoryForgetUI.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height())
	w := card.W - 80
	if w > 620 {
		w = 620
	}
	h := 4*lineH + 112
	box := sdl.Rect{X: card.X + (card.W-w)/2, Y: card.Y + (card.H-h)/2, W: w, H: h}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 16, sdl.Color{A: 120})
	drawOverlayCard(renderer, box)
	x, y := box.X+22, box.Y+20
	inner := box.W - 44
	renderText(renderer, config, font, "Delete this game's record?", WarningInk(), x, y)
	y += lineH + 10
	name := ellipsize(font, gmetaUI.game.Label(), inner)
	filesDrawPlainText(renderer, config, font, name, ColorTextPrimary(), x, y)
	y += lineH + 8
	renderText(renderer, config, font, ellipsize(font, "Its play time, launches and sessions are removed.", inner), ColorTextSecondary(), x, y)
	y += lineH + 4
	renderText(renderer, config, font, ellipsize(font, "Saves and save states are kept.", inner), ColorTextSecondary(), x, y)

	btnH := lineH + 16
	btnY := box.Y + box.H - btnH - 16
	keep := hintLine(FooterHint{Button: "B", Label: "Keep"})
	del := hintLine(FooterHint{Button: "A", Label: "Delete"})
	keepW, _ := measureText(font, keep)
	delW, _ := measureText(font, del)
	gameHistoryForgetUI.no = sdl.Rect{X: box.X + box.W - 22 - keepW - 24, Y: btnY, W: keepW + 24, H: btnH}
	gameHistoryForgetUI.yes = sdl.Rect{X: gameHistoryForgetUI.no.X - 12 - delW - 24, Y: btnY, W: delW + 24, H: btnH}
	for _, b := range []struct {
		r      sdl.Rect
		label  string
		danger bool
	}{{gameHistoryForgetUI.yes, del, true}, {gameHistoryForgetUI.no, keep, false}} {
		fill := glassChipFill()
		ink := ColorTextPrimary()
		if b.danger {
			fill = WithAlpha(ColorToastError, 70)
			ink = ToastErrorInk()
		}
		fillRoundedRect(renderer, b.r.X, b.r.Y, b.r.W, b.r.H, 8, fill)
		renderText(renderer, config, font, b.label, ink, b.r.X+12, b.r.Y+8)
	}
}
