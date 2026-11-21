package main

// home_dashboard.go — two Home dashboard touches: the
// Continue card shows where a game was left, and two play-time widgets.
//
//   - Continue (home_continue.go): a game's card shows its newest save-state
//     picture — the moment the game was left, what the game switcher shows
//     (gswStates) — and its box art when it has none. The states are listed
//     off the render thread and kept a few seconds per game; a new launch
//     looks again (the recents' stamp moves), so the picture after a game is
//     the one its quit wrote.
//   - Most played (widget "mostplayed"): the game with the most time in the
//     playtime ledger, its cover and its hours; A starts it the way the
//     library does, or opens Playtime while nothing has been played.
//   - This month (widget "thismonth"): game time this calendar month and how
//     many games it was spread over, from the days kept by game
//     (playtime_periods.go). A TrimUI boots before network time with its
//     clock at a default date: until the clock can be believed
//     (trophyClockSane) the widget says it is waiting instead of showing a
//     month that is not this one. The days themselves are re-anchored when
//     the clock jumps (playtimePeriodsClockCheck), so the figure is right
//     once it shows. A opens Playtime.
//
// The widgets are in homeWidgetSpecs (home_tiles.go) and drawn through
// drawHomeWidget (home_widgetdraw.go) like the others.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The widgets' names (the tile refs without "w:").
const (
	homeDashMostPlayed = "mostplayed"
	homeDashThisMonth  = "thismonth"
)

// homeDashWidgetKnown reports whether name is one of this file's widgets.
func homeDashWidgetKnown(name string) bool {
	return name == homeDashMostPlayed || name == homeDashThisMonth
}

// ── Continue: the save-state picture ─────────────────────────────────────

// homeDashShotTTL is how long a game's looked-up state picture is believed
// while no game is started.
const homeDashShotTTL = 15 * time.Second

// homeDashShot is one game's newest state picture ("" none) and its file's
// time (the preview's cache key), with when and under which recents stamp it
// was looked up.
type homeDashShot struct {
	file  string
	mod   int64
	at    time.Time
	stamp time.Time
}

var homeDashShots = struct {
	mu   sync.Mutex
	m    map[string]homeDashShot
	busy map[string]bool
}{m: map[string]homeDashShot{}, busy: map[string]bool{}}

// homeDashShotFor is the game's newest state picture as last looked up, and
// starts a look when that is stale. Cheap; any goroutine.
func homeDashShotFor(path string) (string, int64) {
	s := &homeDashShots
	stamp := gswRecentsStamp()
	s.mu.Lock()
	defer s.mu.Unlock()
	have, ok := s.m[path]
	if (!ok || !have.stamp.Equal(stamp) || time.Since(have.at) >= homeDashShotTTL) && !s.busy[path] {
		s.busy[path] = true
		go homeDashShotLook(path, stamp)
	}
	return have.file, have.mod
}

// homeDashShotLook lists a game's states (a worker).
func homeDashShotLook(path string, stamp time.Time) {
	shot, mod := "", int64(0)
	// Recorded even when the listing panics, so the game is never left
	// "being looked up" for good.
	defer func() {
		if r := recover(); r != nil {
			logWarn("home", "continue picture lookup failed: %v", r)
			shot, mod = "", 0
		}
		s := &homeDashShots
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, path)
		if len(s.m) > 64 {
			clear(s.m)
		}
		s.m[path] = homeDashShot{file: shot, mod: mod, at: time.Now(), stamp: stamp}
	}()
	_, shot = gswStates(path)
	if shot != "" {
		if info, err := os.Stat(shot); err == nil {
			mod = info.ModTime().UnixNano()
		} else {
			shot = ""
		}
	}
}

// homeDashContinueJob is the Continue card's preview of a game: its newest
// save-state picture, else its box art (homeArtPreviewJob).
func homeDashContinueJob(g recentGame) homePreviewJob {
	if strings.TrimSpace(g.Path) != "" && gswHasStates(g.System) {
		if shot, mod := homeDashShotFor(g.Path); shot != "" {
			return homePreviewJob{Key: fmt.Sprintf("s:%s|%d", shot, mod), File: shot, Art: true}
		}
	}
	return homeArtPreviewJob(g)
}

// ── Most played ──────────────────────────────────────────────────────────

// homeDashTopEveryMS is how often the ledger is read again for the top game.
const homeDashTopEveryMS = 5000

// homeDashTop is the most played game as the widget shows it.
var homeDashTop struct {
	at    uint64
	key   string // the ledger key ("System|Title")
	secs  int64
	game  libraryGame
	ok    bool // the game was found and can start
	title string
	hours string
	art   string
}

// homeDashTitleOf is the title a recent game is kept under in the ledger
// (playtimeGameCredit's rule).
func homeDashTitleOf(r recentGame) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return t
	}
	return gameDisplayTitle(r.Path)
}

// homeDashFind finds the game a ledger key names: the recent games first,
// then the library snapshot and the Games library's scan.
func homeDashFind(key string) (libraryGame, bool) {
	for _, r := range recentGames() {
		if playtimeGameKey(r.System, homeDashTitleOf(r)) == key {
			return libraryGame{Path: r.Path, System: r.System, Title: r.Title}, true
		}
	}
	system, title := playtimeGameKeyParts(key)
	for _, systems := range [][]librarySystem{homeTabsLib.report.Systems, gameLib.lib.Systems} {
		for _, sys := range systems {
			if system != "" && playtimeKeySystem(sys.Name) != system {
				continue
			}
			for _, g := range sys.Games {
				if strings.TrimSpace(g.Title) == title {
					return g, true
				}
			}
		}
	}
	return libraryGame{}, false
}

// homeDashTopSync reads the ledger again when it is time. Render thread.
func homeDashTopSync() {
	t := &homeDashTop
	now := sdl.GetTicks64()
	if t.at != 0 && now-t.at < homeDashTopEveryMS {
		return
	}
	t.at = max(now, 1)
	best, bestSecs := "", int64(0)
	for key, secs := range playtime.data.Games {
		if secs > bestSecs || (secs == bestSecs && secs > 0 && key < best) {
			best, bestSecs = key, secs
		}
	}
	if best != t.key || (best != "" && !t.ok) {
		t.key = best
		t.game, t.ok = homeDashFind(best)
		_, title := playtimeGameKeyParts(best)
		t.title = title
		if t.ok && strings.TrimSpace(t.game.Label()) != "" {
			t.title = t.game.Label()
		}
		t.art = ""
		switch {
		case t.ok && t.game.HasArt:
			t.art = t.game.Art
		case t.ok:
			t.art = homeSwArtJobFor(recentGame{Path: t.game.Path, System: t.game.System, Title: t.game.Title}).File
		}
	}
	if bestSecs != t.secs || t.hours == "" {
		t.secs = bestSecs
		t.hours = formatPlaytime(bestSecs) + " played"
	}
}

// homeDashDrawMostPlayed paints Most played: the cover filling a small tile
// under a scrim, or beside the words on a wide one; the plain stat card while
// the cover is decoded, or before anything was played.
func (h *HomeLayout) homeDashDrawMostPlayed(renderer *sdl.Renderer, f hwpFonts, b hwpBox, draw sdl.Rect, focused bool) {
	homeDashTopSync()
	t := &homeDashTop
	if t.key == "" {
		s := hwpStat{glyph: "trophy", glyphInk: ColorTextMuted(), lit: true, name: "Most played",
			plain: "Play a game and it shows here"}
		h.hwpDrawStat(renderer, f, b, &s, focused)
		return
	}
	var tex *sdl.Texture
	if t.art != "" {
		tex = homeTabsArtLookup(t.art)
	}
	if tex == nil {
		s := hwpStat{glyph: "trophy", glyphInk: ColorTextMuted(), lit: true, name: "Most played",
			plain: t.title, capGlyph: "games", capInk: ColorTextMuted(), caption: t.hours}
		h.hwpDrawStat(renderer, f, b, &s, focused)
		return
	}
	in := b.inner
	if b.wide && in.H > 0 {
		// The cover square on the left, the words beside it.
		box := sdl.Rect{X: in.X, Y: in.Y, W: in.H, H: in.H}
		homeTabsCopyCover(renderer, tex, box, 255)
		tx := box.X + box.W + scaleX(14, h.Width)
		room := in.X + in.W - tx
		if room > scaleX(40, h.Width) {
			hdr := int32(f.meta.Height())
			ink := ColorTextMuted()
			if focused {
				ink = ColorAccent
			}
			hwpGlyph(renderer, "trophy", tx, in.Y, hdr, ink)
			drawText(renderer, f.meta, ellipsize(f.meta, "Most played", room-hdr-scaleX(6, h.Width)), tx+hdr+scaleX(6, h.Width), in.Y, ColorTextMuted(), textAlignLeft)
			lineH := int32(f.tile.Height())
			l1, l2 := hwpWrap2(f.tile, t.title, room)
			y := in.Y + hdr + scaleY(8, h.Height)
			drawText(renderer, f.tile, l1, tx, y, ColorTextPrimary(), textAlignLeft)
			if l2 != "" && y+2*lineH+int32(f.meta.Height()) <= in.Y+in.H {
				drawText(renderer, f.tile, ellipsize(f.tile, l2, room), tx, y+lineH, ColorTextPrimary(), textAlignLeft)
			}
			drawText(renderer, f.meta, ellipsize(f.meta, t.hours, room), tx, in.Y+in.H-int32(f.meta.Height()), ColorTextMuted(), textAlignLeft)
		}
		return
	}
	// A small or tall tile: the cover fills it, the words over a scrim.
	homeTabsCopyCover(renderer, tex, draw, 255)
	from, to := continueScrim(255)
	fillVerticalScrimUntil(renderer, draw, draw.Y+draw.H*40/100, draw.Y+draw.H*75/100, from, to)
	mh := int32(f.meta.Height())
	th := int32(f.tile.Height())
	drawText(renderer, f.meta, ellipsize(f.meta, t.hours, in.W), in.X, in.Y+in.H-mh, ColorTextMuted(), textAlignLeft)
	drawText(renderer, f.tile, ellipsize(f.tile, t.title, in.W), in.X, in.Y+in.H-mh-scaleY(2, h.Height)-th, ColorTextPrimary(), textAlignLeft)
}

// ── This month ───────────────────────────────────────────────────────────

// homeDashMonth caches This month's strings against the figures they were
// made from.
var homeDashMonth struct {
	made  bool
	sane  bool
	rev   uint64
	day   string
	secs  int64
	games int
	nums  [2]string
	units [2]string
	n     int
	plain string
	count string
}

// homeDashMonthStat is the This month widget's reading.
func homeDashMonthStat(now time.Time) hwpStat {
	c := &homeDashMonth
	s := hwpStat{glyph: "calendar", glyphInk: ColorTextMuted(), lit: true, name: "This month"}
	sane := trophyClockSane(now)
	day := now.Format(wrappedDayKey)
	if !c.made || c.sane != sane || c.rev != playtimePeriods.rev || c.day != day {
		c.made, c.sane, c.rev, c.day = true, sane, playtimePeriods.rev, day
		c.secs, c.games = 0, 0
		if sane {
			for _, secs := range playtimePeriodTotals(playtimePeriodMonth, now) {
				if secs > 0 {
					c.secs += secs
					c.games++
				}
			}
		}
		c.nums, c.units, c.n, c.plain = hwpPlayedParts(c.secs)
		switch {
		case !sane:
			c.n, c.plain = 0, "Waiting for the clock"
			c.count = "Shown once the time is set"
		case c.secs <= 0:
			c.plain = "No games yet this month"
			c.count = ""
		default:
			c.count = pluralizeCount(c.games, "game", "games")
		}
	}
	s.nums, s.units, s.n, s.plain = c.nums, c.units, c.n, c.plain
	if s.n > 0 {
		s.note = "played this month"
	}
	if c.count != "" {
		s.capGlyph, s.capInk, s.caption = "games", ColorTextMuted(), c.count
	}
	return s
}

// ── Hooks (home_widgetdraw.go) ───────────────────────────────────────────

// homeDashDrawWidget paints one of this file's widgets inside the glass card
// drawHomeWidget has drawn.
func (h *HomeLayout) homeDashDrawWidget(renderer *sdl.Renderer, f hwpFonts, b hwpBox, name string, draw sdl.Rect, focused bool) {
	switch name {
	case homeDashMostPlayed:
		h.homeDashDrawMostPlayed(renderer, f, b, draw, focused)
	case homeDashThisMonth:
		s := homeDashMonthStat(time.Now())
		h.hwpDrawStat(renderer, f, b, &s, focused)
	}
}

// homeDashWidgetActivate is A on one of this file's widgets.
func homeDashWidgetActivate(config *Config, name string) {
	if name == homeDashMostPlayed {
		homeDashTopSync()
		if t := &homeDashTop; t.ok {
			homeTabsPlayGame(config, t.game)
			return
		}
	}
	if !homeDashWidgetKnown(name) {
		return
	}
	if idx := sceneIndexExact(config, playtimeSceneName); idx >= 0 {
		changeSceneTo(config, idx)
		return
	}
	ShowToast("Playtime is not part of this setup", ToastKindInfo)
}
