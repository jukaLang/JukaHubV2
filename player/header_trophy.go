package main

// header_trophy.go — the trophy room's cup in the top bar.
//
// The system corner on every screen ends in three fixed controls: the
// notification bell at the margin, Report a bug to its left, then the trophy
// room's cup. A press, a click or a tap on the cup opens Trophies from anywhere;
// before, the room was a row in Start → Screens, a tile in Tools and a search
// away. The cup is gold, an earned trophy's metal, so it reads as the room at a
// glance and not as a second bell. A gold count hangs on it while trophies have
// been earned since the room was last on screen, and it wears its open edge
// while the room is the screen. The release offer, which comes and goes, sits
// left of the cup, so the fixed controls stay in place.
//
// Both headers place it — renderStatusBar (extras.go) on a scene, drawHeader
// (homelayout.go) on Home — and both record the rect they drew in
// headerTrophyRect, the one rect the focus graphs (ChromeTrophyIndex,
// HomeFocusTrophy), the click handlers and the tests read. A frame that draws
// no bar clears it (brwClearChromeHits, the PDF reader).

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// headerTrophyLabel names the control in the focus graph.
const headerTrophyLabel = "Trophies"

// headerTrophyMinCluster is the least room a scene's bar keeps for the status
// cluster beside the cup (the clock and the battery). On a bar too crowded for
// that the cup gives way, the way Home drops the release offer.
const headerTrophyMinCluster = int32(120)

// trophySeenKey is the user-store key holding how many trophies of the set
// had been earned when the trophy room was last on screen; the cup's count is
// what has been earned beyond it. A count, not a time: a handheld that starts
// before it has network time can have a clock months out, and an unlock time
// compared against it made every earlier trophy new again — or none.
const trophySeenKey = "trophyRoomSeen"

// trophySeenOldKey is the visit time the first build of the cup stored. It is
// dropped the next time the record is written.
const trophySeenOldKey = "trophyRoomSeenAt"

// headerTrophyRect is where the cup was drawn this frame; empty when no bar
// drew it.
var headerTrophyRect sdl.Rect

// headerTrophyBadgeInk is the count's ink on the gold badge.
var headerTrophyBadgeInk = sdl.Color{R: 36, G: 24, B: 4, A: 255}

// trophyBaseline stands in for the record while the room has never been
// opened: the earned count the first time the cup was asked for this session,
// so the count is what is earned while JukaHub runs, not a whole history
// earned before the cup existed.
var trophyBaseline struct {
	set bool
	n   int
}

// trophySeenGen moves whenever the record is written, so the cached count is
// made again.
var trophySeenGen uint64

// trophyCupState is the cup's numbers and what they were made from. They are
// made again only when the snapshot (playtime.rev), the unlocks or the record
// changed — the keys trophySummary uses, plus the record.
type trophyCupState struct {
	valid    bool
	cfg      *Config
	rev      uint64
	unlocked int
	seenGen  uint64
	earned   int  // trophies of the set earned now
	seen     int  // the record (or the session's baseline)
	stored   bool // seen is the stored record
	n        int  // the cup's count
}

// trophyUnseen is the cached trophyCupState.
var trophyUnseen trophyCupState

// headerTrophySize is the cup chip's width and height: the bell's, so the
// corner's icon buttons are one size on every canvas.
func headerTrophySize(config *Config) (int32, int32) {
	return notifBellSize(config)
}

// headerTrophyInRoom reports whether the trophy room is the screen.
func headerTrophyInRoom(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == playtimeSceneName
}

// drawHeaderTrophy paints the cup chip with its right edge at rightX, centred
// in a bar barH tall, records it in headerTrophyRect and returns it. The chip
// is the shared icon button the bell is drawn with, so the focus ring, the
// open edge and the badge are the bell's own.
func drawHeaderTrophy(renderer *sdl.Renderer, config *Config, rightX, barH int32, focused bool) sdl.Rect {
	w, h := headerTrophySize(config)
	if renderer == nil || w <= 0 || h <= 0 {
		return sdl.Rect{}
	}
	rect := sdl.Rect{X: rightX - w, Y: (barH - h) / 2, W: w, H: h}
	badgeFont := notifBadgeFont(config)
	button := UIIconButton{
		Rect: rect,
		Style: UIIconButtonStyle{
			Surface:     ColorIconSurface,
			OpenSurface: ColorIconSurface,
			Ring:        accentColor,
			RingWidth:   glassFocusRingWidth,
			IconInset:   notifBellIconInset,
			// The earned cup in gold, from the room's own shape cache: one
			// textured copy a frame.
			Glyph: func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
				side := min32(box.W, box.H)
				if side < 12 {
					side = 12
				}
				cup := sdl.Rect{X: box.X + (box.W-side)/2, Y: box.Y + (box.H-side)/2, W: side, H: side}
				trophyDrawCup(r, cup, side, trophyGold)
			},
		},
		State:      UIIconButtonState{Focused: focused, Open: headerTrophyInRoom(config)},
		BadgeFont:  badgeFont,
		BadgeColor: trophyTierColor(trophyGold),
		BadgeInk:   headerTrophyBadgeInk,
	}
	if badgeFont != nil {
		button.Badge = notifBadgeLabel(trophyUnseenCount(config))
	}
	UIIconButtonDraw(renderer, button)
	headerTrophyRect = rect
	return rect
}

// headerTrophyHit reports whether a click or a tap lands on the cup. The chip
// is header-sized, so the hit area is the shared touch target centred on it.
func headerTrophyHit(x, y int32) bool {
	if headerBugRect.W > 0 && pointInRect(x, y, headerBugRect) {
		return false
	}
	return headerTrophyRect.W > 0 && headerTrophyRect.H > 0 && hitTouchTarget(headerTrophyRect, x, y)
}

// headerTrophyClear drops the cup's hit area for a frame that draws no bar.
func headerTrophyClear() {
	headerTrophyRect = sdl.Rect{}
}

// headerTrophyActivate is the cup's press: it opens the trophy room. In the
// room already, the press starts the visit over — the Trophies tab, the top of
// the grid, no sheet — the way a tab bar's current tab scrolls back to the top.
func headerTrophyActivate(config *Config) {
	if config == nil {
		return
	}
	if headerTrophyInRoom(config) {
		playtimeScrollFor = -2
		trophyRoomSyncVisit()
		return
	}
	trophyOpenRoom(config)
}

// trophyEarnedCount is how many trophies of the set d has earned. An id that
// is not a trophy any more counts for nothing.
func trophyEarnedCount(d playtimeData) int {
	n := 0
	for i := range playtimeAchievements {
		if _, ok := d.Unlocked[playtimeAchievements[i].ID]; ok {
			n++
		}
	}
	return n
}

// trophySeenRecord is the stored record: the earned count when the room was
// last on screen, and whether there is one.
func trophySeenRecord(config *Config) (int, bool) {
	if config == nil {
		return 0, false
	}
	s, ok := customString(config.Variables.Custom, trophySeenKey)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// trophyCupCounts makes (or returns the cached) earned count, the record it
// is measured from and the cup's count: what has been earned beyond the
// record, never below none. A record above the earned count (a backup with
// fewer trophies restored, a trophy retired) counts nothing until the room is
// next on screen and writes the record again. Render thread only, like the
// playtime snapshot it reads.
func trophyCupCounts(config *Config) *trophyCupState {
	d := playtime.data
	c := &trophyUnseen
	if c.valid && c.cfg == config && c.rev == playtime.rev && c.unlocked == len(d.Unlocked) && c.seenGen == trophySeenGen {
		return c
	}
	earned := trophyEarnedCount(d)
	seen, stored := trophySeenRecord(config)
	if !stored {
		// The baseline is taken once the snapshot is loaded: before it, every
		// earned trophy would count as new the moment it loaded.
		if !trophyBaseline.set && playtime.loaded {
			trophyBaseline.set, trophyBaseline.n = true, earned
		}
		seen = trophyBaseline.n
	}
	n := earned - seen
	if n < 0 {
		n = 0
	}
	c.valid, c.cfg, c.rev, c.unlocked, c.seenGen = true, config, playtime.rev, len(d.Unlocked), trophySeenGen
	c.earned, c.seen, c.stored, c.n = earned, seen, stored, n
	return c
}

// trophyUnseenCount is the cup's count: trophies earned since the room was last
// on screen (since JukaHub started, while it never was).
func trophyUnseenCount(config *Config) int {
	return trophyCupCounts(config).n
}

// trophyRoomNoteSeen records that the room is on screen, so what it shows is
// seen and the cup's count goes. The room calls it every frame; it writes only
// when the record differs from the earned count (a trophy earned since, the
// first visit ever), straight to the user store and its saver — not through
// setStoreString, whose resync of every override has no business running from
// a screen that only looks.
func trophyRoomNoteSeen(config *Config) {
	if config == nil || !playtime.loaded {
		return
	}
	c := trophyCupCounts(config)
	if c.stored && c.seen == c.earned {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[trophySeenKey] = strconv.Itoa(c.earned)
	delete(config.Variables.Custom, trophySeenOldKey)
	trophySeenGen++
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// trophySeenClamp brings the record, and the session's baseline once taken,
// down to earned when they are above it: a loaded snapshot whose retired
// trophies left it (trophyRetire), or a restored backup with fewer. Left
// above, the cup would stay at nothing for that many new trophies.
func trophySeenClamp(config *Config, earned int) {
	if seen, ok := trophySeenRecord(config); ok && seen > earned {
		config.Variables.Custom[trophySeenKey] = strconv.Itoa(earned)
		trophySeenGen++
	}
	if trophyBaseline.set && trophyBaseline.n > earned {
		trophyBaseline.n = earned
		trophySeenGen++
	}
}

// trophySeenSkip keeps n trophies just recorded without a celebration (what a
// start finds the saved history already earned, trophyCatchUpQuietly) off the
// cup's count: the stored record moves up by n, and so does the session's
// baseline once it is taken. A baseline taken later includes them already. The
// record is saved with the playtime snapshot's next write.
func trophySeenSkip(config *Config, n int) {
	if n <= 0 {
		return
	}
	if seen, ok := trophySeenRecord(config); ok {
		config.Variables.Custom[trophySeenKey] = strconv.Itoa(seen + n)
	}
	if trophyBaseline.set {
		trophyBaseline.n += n
	}
	trophySeenGen++
}
