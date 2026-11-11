package main

// trophy_celebrate.go — what happens when a trophy is earned.
//
// A card slides in at the top right, under the bar: the trophy in its tier's
// metal on a medallion that pops in, a shine that sweeps across the cup, the
// caption, the name and the tier. Confetti bursts out of the medallion as the
// card lands, the chime plays (trophy_sound.go) and the motor gives a short
// double pulse. The card stays about four seconds, the platinum longer, with a
// fanfare and a second wave of confetti falling from the top.
//
// It is drawn above every scene and panel; only a ringing alarm covers it (the
// main loop draws the alarm after this). Unlocks queue: each gets its own card
// in turn, a burst of many folds the rest into one "+N more" card, and nothing
// starts while JukaHub is not on screen (a game owns the framebuffer, the window
// is minimised), while the alarm rings or while the screensaver is up — the
// queue simply waits, so a trophy earned in the background is celebrated when
// the user is back. It never takes the pad or the keyboard: the card is a
// notice, not a dialog. A tap or a click on it is a deliberate press, though,
// and opens the trophy room (trophyCardClick).
//
// Reduced motion (or the "Calm" celebration setting) keeps the card and the
// chime but drops the slide, the pop, the shine and the confetti: the card
// fades in and out where it stands.

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// trophyToast is one queued celebration.
type trophyToast struct {
	id      string // the trophy's id; empty for RetroAchievements and summaries
	title   string
	caption string // the small line above the title
	detail  string // the line under it: tier and points, or the game
	tier    trophyTier
	more    int // a summary card: how many unlocks it stands for
}

const (
	// trophyQueueCap is how many individual cards may wait. Beyond it, unlocks
	// fold into one summary card: twenty cards in a row after a big import would
	// be a minute and a half of the screen's corner.
	trophyQueueCap = 4

	trophyCardMS         = 4000 // an ordinary card on screen
	trophyCardPlatinumMS = 6200 // the platinum stays for its fanfare
	trophyCardSummaryMS  = 3600
	trophyEnterMS        = 420 // the slide in, with a small overshoot
	trophyExitMS         = 320 // the slide out
	trophyFadeInMS       = 220 // the calm card's fade in
	trophyFadeOutMS      = 360 // and out

	// trophyBurstAtMS is when the confetti leaves the medallion: as the card
	// settles, so the paper visibly comes out of the trophy.
	trophyBurstAtMS = 240
	// trophyRainAtMS is the platinum's second wave.
	trophyRainAtMS = 900
	// trophyEchoAtMS is the second haptic pulse of a gold or platinum.
	trophyEchoAtMS = 170
)

// The two haptic pulses: a firm one as the card arrives, and for the top two
// tiers a lighter echo right after it — a "da-dum" you feel without looking.
var (
	trophyHapticPulse = hapticPulse{strength: 0.75, ms: 70}
	trophyHapticEcho  = hapticPulse{strength: 0.55, ms: 45}
)

// trophyQueue holds celebrations waiting for the screen. Unlocks arrive from
// the render thread (playtime) and from the RetroAchievements sync goroutine.
var trophyQueue struct {
	mu       sync.Mutex
	items    []trophyToast
	folded   int // unlocks beyond trophyQueueCap, shown as one summary card
	foldTier trophyTier
}

// trophyEnqueue adds a celebration. The platinum always gets its own card.
func trophyEnqueue(t trophyToast) {
	trophyQueue.mu.Lock()
	defer trophyQueue.mu.Unlock()
	if t.tier == trophyPlatinum || len(trophyQueue.items) < trophyQueueCap {
		trophyQueue.items = append(trophyQueue.items, t)
		return
	}
	trophyQueue.folded++
	if t.tier > trophyQueue.foldTier {
		trophyQueue.foldTier = t.tier
	}
}

// trophyDequeue takes the next card. Folded unlocks are shown before a waiting
// platinum, so the platinum still comes last, after the trophies it crowns.
func trophyDequeue() (trophyToast, bool) {
	trophyQueue.mu.Lock()
	defer trophyQueue.mu.Unlock()
	if trophyQueue.folded > 0 && (len(trophyQueue.items) == 0 || trophyQueue.items[0].tier == trophyPlatinum) {
		n := trophyQueue.folded
		tier := trophyQueue.foldTier
		trophyQueue.folded = 0
		trophyQueue.foldTier = trophyBronze
		return trophyToast{
			title:   "+" + strconv.Itoa(n) + " more " + trophyPlural(n),
			caption: "TROPHIES EARNED",
			detail:  "See them all in Trophies",
			tier:    tier,
			more:    n,
		}, true
	}
	if len(trophyQueue.items) == 0 {
		return trophyToast{}, false
	}
	t := trophyQueue.items[0]
	copy(trophyQueue.items, trophyQueue.items[1:])
	trophyQueue.items = trophyQueue.items[:len(trophyQueue.items)-1]
	return t, true
}

// trophyPlural is "trophy" or "trophies".
func trophyPlural(n int) string {
	if n == 1 {
		return "trophy"
	}
	return "trophies"
}

// trophyPointsLabel is a tier's line on the card: "Silver · 30 pts".
func trophyPointsLabel(t trophyTier) string {
	return trophyTierName(t) + " · " + strconv.Itoa(trophyTierPoints(t)) + " pts"
}

// trophyAnnounce queues the celebration for a trophy that was just earned and
// files it in the notification centre, so it can be read again from the bell.
func trophyAnnounce(a playtimeAchievement) {
	trophyAnnounceCard(a)
	notifRecord(trophyEarnedNotice(a), notifSuccess)
}

// trophyAnnounceCard queues a trophy's card and notes it for the "while you
// were playing" banner, without the notification row.
func trophyAnnounceCard(a playtimeAchievement) {
	caption := "TROPHY EARNED"
	if a.Tier == trophyPlatinum {
		caption = "PLATINUM TROPHY EARNED"
	}
	trophyEnqueue(trophyToast{
		id:      a.ID,
		title:   a.Title,
		caption: caption,
		detail:  trophyPointsLabel(a.Tier),
		tier:    a.Tier,
	})
	noteAwayAchievement(a.Title)
}

// trophyEarnedNotice is one trophy's row in the notification centre.
func trophyEarnedNotice(a playtimeAchievement) string {
	return "Trophy earned: " + a.Title + " (" + trophyTierName(a.Tier) + ")"
}

// trophyBurstSingles is the most trophies one unlock pass announces one by one.
const trophyBurstSingles = 3

// trophyBurstNamed is how many titles a burst's notification row names.
const trophyBurstNamed = 3

// trophyAnnounceBatch announces what one unlock pass awarded. A few are
// announced one by one. More at once — the first start of a version whose new
// trophies an old save already qualifies for, a restore, a big import — still
// queue a card each (the queue folds them into "+N more"), but the
// notification centre gets one row for them instead of a page of rows that
// would push everything else out of its fifty. A platinum keeps its own row.
func trophyAnnounceBatch(list []playtimeAchievement) {
	if len(list) <= trophyBurstSingles {
		for _, a := range list {
			trophyAnnounce(a)
		}
		return
	}
	// The rarest first: the queue gives the first few their own cards and the
	// row names the first three, so a burst opens with its golds rather than
	// with the first rungs of the Lifetime ladder.
	list = append([]playtimeAchievement(nil), list...)
	sort.SliceStable(list, func(x, y int) bool { return list[x].Tier > list[y].Tier })
	others := make([]playtimeAchievement, 0, len(list))
	for _, a := range list {
		trophyAnnounceCard(a)
		if a.Tier == trophyPlatinum {
			notifRecord(trophyEarnedNotice(a), notifSuccess)
			continue
		}
		others = append(others, a)
	}
	if msg := trophyBurstNotice(others); msg != "" {
		notifRecord(msg, notifSuccess)
	}
}

// trophyBurstNotice is a burst's notification row: "12 trophies earned: First
// Steps, Night Owl, Bookworm and 9 more". "" for none.
func trophyBurstNotice(list []playtimeAchievement) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return trophyEarnedNotice(list[0])
	}
	n := len(list)
	named := n
	if named > trophyBurstNamed {
		named = trophyBurstNamed
	}
	titles := make([]string, 0, named)
	for _, a := range list[:named] {
		titles = append(titles, a.Title)
	}
	msg := strconv.Itoa(n) + " trophies earned: "
	if rest := n - named; rest > 0 {
		return msg + strings.Join(titles, ", ") + " and " + strconv.Itoa(rest) + " more"
	}
	return msg + strings.Join(titles[:named-1], ", ") + " and " + titles[named-1]
}

// trophyRATier grades a RetroAchievements unlock by its points: the site's own
// scale runs 1–100, where 25 and up are the hard ones.
func trophyRATier(points int) trophyTier {
	switch {
	case points >= 25:
		return trophyGold
	case points >= 10:
		return trophySilver
	}
	return trophyBronze
}

// trophyCelebrateRA queues the celebration for a RetroAchievements unlock. It
// is safe to call from the sync goroutine.
func trophyCelebrateRA(title, game string, points int) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	detail := strings.TrimSpace(game)
	if points > 0 {
		pts := strconv.Itoa(points) + " pts"
		if detail == "" {
			detail = pts
		} else {
			detail += " · " + pts
		}
	}
	trophyEnqueue(trophyToast{
		title:   title,
		caption: "RETROACHIEVEMENTS",
		detail:  detail,
		tier:    trophyRATier(points),
	})
	// JukaHub's own RetroAchievements trophies count the unlocks a sync brings.
	trophyEvent(tkRAUnlock)
}

// trophyRANewUnlocksMax bounds the cards one sync can queue; the queue folds
// the rest into its summary card anyway.
const trophyRANewUnlocksMax = 12

// trophyRANewUnlocks lists the RetroAchievements unlocks a sync brought that
// the previous sync of the same account did not have, oldest first. The first
// sync of an account celebrates nothing: its "recent" list is history, not news.
func trophyRANewUnlocks(prev, next raCache) []raAchievement {
	if prev.UpdatedAt <= 0 || !strings.EqualFold(strings.TrimSpace(prev.Username), strings.TrimSpace(next.Username)) {
		return nil
	}
	known := make(map[int]bool, len(prev.Recent))
	for _, a := range prev.Recent {
		known[a.AchievementID] = true
	}
	// A little slack for a clock that disagrees with the server's.
	since := time.Unix(prev.UpdatedAt, 0).Add(-5 * time.Minute)
	var out []raAchievement
	for _, a := range next.Recent {
		if known[a.AchievementID] {
			continue
		}
		if t := raTimeValue(a.Date); !t.IsZero() && t.Before(since) {
			continue
		}
		known[a.AchievementID] = true
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return raTimeValue(out[i].Date).Before(raTimeValue(out[j].Date))
	})
	if len(out) > trophyRANewUnlocksMax {
		out = out[len(out)-trophyRANewUnlocksMax:]
	}
	return out
}

// trophyCelebrateTest queues a sample card of the given tier: the trophy room's
// hidden F9 key and the tests use it to see a celebration on demand.
func trophyCelebrateTest(tier trophyTier) {
	caption := "TROPHY EARNED"
	if tier == trophyPlatinum {
		caption = "PLATINUM TROPHY EARNED"
	}
	trophyEnqueue(trophyToast{
		title:   trophyTierName(tier) + " test trophy",
		caption: caption,
		detail:  trophyPointsLabel(tier),
		tier:    tier,
	})
}

// trophyReplay queues an earned trophy's celebration again, from the details
// sheet. It reports whether the trophy is one that was earned.
func trophyReplay(id string) bool {
	i := trophyIndexByID(id)
	if i < 0 {
		return false
	}
	if _, ok := playtime.data.Unlocked[id]; !ok {
		return false
	}
	a := playtimeAchievements[i]
	caption := "TROPHY EARNED"
	if a.Tier == trophyPlatinum {
		caption = "PLATINUM TROPHY EARNED"
	}
	trophyEnqueue(trophyToast{id: a.ID, title: a.Title, caption: caption, detail: trophyPointsLabel(a.Tier), tier: a.Tier})
	trophyEvent(tkReplay)
	return true
}

// ── Settings ──────────────────────────────────────────────────────────────────

// trophyCelebrationVar is the custom setting for the celebration style:
// "auto" (the default: full unless Reduced Motion is on), "full" (always the
// confetti, for someone who wants it despite reduced motion elsewhere) or
// "calm" (the card and the chime only).
const trophyCelebrationVar = "trophyCelebration"

// trophyCelebrationModes is the cycle order of the room's chip.
var trophyCelebrationModes = []string{"auto", "full", "calm"}

// trophyCelebrationMode reads the saved style; anything unknown is "auto".
func trophyCelebrationMode(config *Config) string {
	if config == nil {
		return "auto"
	}
	v, ok := customValue(config.Variables.Custom, trophyCelebrationVar)
	if !ok {
		return "auto"
	}
	s, isString := v.(string)
	if !isString {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "full":
		return "full"
	case "calm":
		return "calm"
	}
	return "auto"
}

// trophyCycleCelebrationMode steps the style and saves it with the other
// settings. It returns the new style.
func trophyCycleCelebrationMode(config *Config) string {
	if config == nil {
		return "auto"
	}
	cur := trophyCelebrationMode(config)
	next := trophyCelebrationModes[0]
	for i, m := range trophyCelebrationModes {
		if m == cur {
			next = trophyCelebrationModes[(i+1)%len(trophyCelebrationModes)]
			break
		}
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[trophyCelebrationVar] = next
	saveUserConfigDebounced(userConfigFromVariables(config))
	trophyEvent(tkConfetti)
	return next
}

// trophyCelebrationFull reports whether a celebration gets its motion and
// confetti.
func trophyCelebrationFull(config *Config) bool {
	switch trophyCelebrationMode(config) {
	case "full":
		return true
	case "calm":
		return false
	}
	return !effectiveReducedMotion(config)
}

// trophySoundAllowed reports whether the chime may play: the UI sounds switch
// in Settings (sound.go) silences it like every other interface sound, and its
// volume slider scales it (trophyPlayChime).
func trophySoundAllowed() bool {
	return bootSfxEnabled()
}

// ── On screen ─────────────────────────────────────────────────────────────────

// trophyShow is the card on screen. Render thread only.
var trophyShow struct {
	active bool
	toast  trophyToast
	start  uint64 // uiClockMS when the card began
	dur    uint64
	full   bool // slide, pop, shine and confetti; false fades in place
	burst  bool // the confetti burst has been thrown
	rain   bool // the platinum's second wave has fallen
	echo   bool // the second haptic pulse has played
	lastMS uint64
	// card is where the last frame drew the card, for the tests.
	card sdl.Rect
}

// trophyOverlayReady reports whether a new card may start now: JukaHub is on
// screen and nothing more important owns it.
func trophyOverlayReady() bool {
	return !alarmFiringActive() && !backgroundPaused() && !screensaverActiveNow()
}

// trophyOverlayActive reports whether the celebration is animating, so the
// main loop keeps its fast frame cadence until the confetti has landed.
func trophyOverlayActive() bool {
	return trophyShow.active || trophyConfettiAlive() > 0
}

// trophyCardClick takes a tap or click on the card on screen: the card ends
// and the trophy room opens, so a trophy just earned is one touch from the
// whole collection. main.go asks after the modal panels, the Start menu and the
// notification centre have had the click. It stands aside — the press goes on
// to whatever is under the card — while a video is open, because the player's
// own top-right controls sit under the card and a tap meant for them must not
// pull the user out of the video.
//
// Over anything that owns the screen (trophyCardOverOverlay) the tap only puts
// the card away: opening the room changes the scene, which would tear down the
// power confirmation, a sheet or Quick Settings the card happened to cover.
func trophyCardClick(mx, my int32, config *Config) bool {
	if config == nil || !trophyShow.active || trophyShow.card.W <= 0 || !pointInRect(mx, my, trophyShow.card) {
		return false
	}
	if playerPhase() != "idle" {
		return false
	}
	trophyShow.active = false
	trophyShow.card = sdl.Rect{}
	PlayActivateSound()
	if trophyCardOverOverlay(config) {
		return true
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == playtimeSceneName {
		// Already in the room: the tap only puts the card away.
		return true
	}
	trophyOpenRoom(config)
	return true
}

// trophyCardOverOverlay reports whether the card sits over something that owns
// the screen: a modal panel, Home's power confirmation, the hand-off card, a
// Tools sheet, Quick Settings, the controls legend, the shortcuts sheet, the
// wallpaper picker, search, the calendar, the tour, the Start menu or the
// notification centre.
func trophyCardOverOverlay(config *Config) bool {
	return modalPanelBlocksShoulders() || homePowerConfirm.open || (hoffCard.rect.W > 0 && hoffCardVisible()) ||
		tbxSheetVisible(config) || qs.open || controlsLegend.open || shortcutsOpen || wpOpen ||
		globalSearchOpen || clipSearchOpen || homeCalendarOpen || onboarding.active || tb.startOpen || nc.open
}

// trophyBegin puts a card on screen and fires its sound and pulse.
func trophyBegin(config *Config, t trophyToast, now uint64) {
	trophyShow.active = true
	trophyShow.toast = t
	trophyShow.start = now
	trophyShow.dur = trophyCardMS
	switch {
	case t.more > 0:
		trophyShow.dur = trophyCardSummaryMS
	case t.tier == trophyPlatinum:
		trophyShow.dur = trophyCardPlatinumMS
	}
	trophyShow.full = trophyCelebrationFull(config)
	trophyShow.burst = false
	trophyShow.rain = false
	trophyShow.echo = t.tier < trophyGold
	if trophySoundAllowed() {
		go trophyPlayChime(t.tier)
	}
	playHaptic(trophyHapticPulse)
}

// trophyRenderOverlay advances and draws the celebration. The main loop calls
// it every frame, after the toasts and before a ringing alarm.
func trophyRenderOverlay(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	now := uiClockMS()
	dt := float32(0)
	if trophyShow.lastMS > 0 && now > trophyShow.lastMS {
		dt = float32(now-trophyShow.lastMS) / 1000
	}
	trophyShow.lastMS = now

	if !trophyShow.active && trophyOverlayReady() {
		if t, ok := trophyDequeue(); ok {
			trophyBegin(config, t, now)
		}
	}
	if trophyShow.active {
		trophyAdvance(config, now)
	}
	trophyConfettiUpdate(dt)
	trophyConfettiDraw(renderer)
	if trophyShow.active {
		trophyDrawCard(renderer, config, now)
	}
}

// trophyAdvance runs the card's timeline: the burst, the platinum's rain, the
// haptic echo, and the end.
func trophyAdvance(config *Config, now uint64) {
	el := now - trophyShow.start
	t := trophyShow.toast
	if trophyShow.full && !trophyShow.burst && el >= trophyBurstAtMS {
		trophyShow.burst = true
		layout := trophyCardLayoutFor(config, t)
		cx := float32(layout.medal.X + layout.medal.W/2)
		cy := float32(layout.medal.Y + layout.medal.H/2)
		trophyConfettiBurst(cx, cy, trophyBurstSize(t.tier), t.tier)
	}
	if trophyShow.full && t.tier == trophyPlatinum && !trophyShow.rain && el >= trophyRainAtMS {
		trophyShow.rain = true
		trophyConfettiRain(trophyConfettiMax, t.tier)
	}
	if !trophyShow.echo && el >= trophyEchoAtMS {
		trophyShow.echo = true
		playHaptic(trophyHapticEcho)
	}
	if el >= trophyShow.dur {
		trophyShow.active = false
		trophyShow.card = sdl.Rect{}
	}
}

// trophyBurstSize is how much paper a tier throws; the pool caps it.
func trophyBurstSize(t trophyTier) int {
	switch t {
	case trophySilver:
		return 90
	case trophyGold:
		return 120
	case trophyPlatinum:
		return trophyConfettiMax
	}
	return 70
}

// trophyCardLayout is the card's geometry at rest.
type trophyCardLayout struct {
	card   sdl.Rect
	medal  sdl.Rect // the medallion's square
	textX  int32
	textW  int32
	capY   int32
	titleY int32
	lineY  int32
}

// The card's three text sizes, in design pixels (getFontSized scales them for
// the canvas and the accessibility text size).
const (
	// The caption asks for the Caption role's floor size; asking for 12 made
	// the font helper lift it silently to 14 anyway.
	trophyCardCaptionSize = uiMinTextSize
	trophyCardTitleSize   = 19
	trophyCardDetailSize  = 14
)

// trophyCardLayoutFor measures the card for this canvas: 420 wide (less on a
// narrow canvas), tall enough for its three lines at the current text size,
// pinned under the bar at the right margin.
func trophyCardLayoutFor(config *Config, t trophyToast) trophyCardLayout {
	margin := scaleX(24, screenWidth)
	if margin < 12 {
		margin = 12
	}
	w := int32(420)
	if maxW := screenWidth - 2*margin; w > maxW {
		w = maxW
	}
	capH, titleH, lineH := int32(15), int32(23), int32(18)
	if config != nil {
		if f := getFontSized(config, "small", trophyCardCaptionSize); f != nil {
			capH = int32(f.Height())
		}
		if f := getFontSized(config, "small", trophyCardTitleSize); f != nil {
			titleH = int32(f.Height())
		}
		if f := getFontSized(config, "small", trophyCardDetailSize); f != nil {
			lineH = int32(f.Height())
		}
	}
	const pad = int32(14)
	textH := capH + 2 + titleH + 2 + lineH
	h := textH + 2*pad
	medal := int32(60)
	if h < medal+2*pad {
		h = medal + 2*pad
	}
	x := screenWidth - margin - w
	y := headerBarHeight() + 10
	l := trophyCardLayout{card: sdl.Rect{X: x, Y: y, W: w, H: h}}
	l.medal = sdl.Rect{X: x + pad + 2, Y: y + (h-medal)/2, W: medal, H: medal}
	l.textX = l.medal.X + medal + 16
	l.textW = x + w - pad - l.textX
	l.capY = y + (h-textH)/2
	l.titleY = l.capY + capH + 2
	l.lineY = l.titleY + titleH + 2
	return l
}

// trophyEaseOutBack overshoots a little and settles: the card lands.
func trophyEaseOutBack(p float64) float64 {
	const c1 = 1.70158
	const c3 = c1 + 1
	p = uiClamp01(p) - 1
	return 1 + c3*p*p*p + c1*p*p
}

// trophyPop is the medallion's pop: small, a little too big, settled.
var trophyPop = uiKeyframe{{At: 0, Value: 0.55}, {At: 0.5, Value: 1.12}, {At: 1, Value: 1}}

// trophyPopScale is the pop's scale el ms into the card.
func trophyPopScale(el uint64) float64 {
	return trophyPop.at(float64(el) / 480)
}

// trophyCardMotion is where the card is and how opaque, el ms into a card of
// dur ms: offset is the fraction of the card's slide still to go (0 at rest,
// negative in the overshoot), alpha its opacity.
func trophyCardMotion(full bool, el, dur uint64) (offset, alpha float64) {
	alpha = 1
	if full {
		if el < trophyEnterMS {
			offset = 1 - trophyEaseOutBack(float64(el)/trophyEnterMS)
		}
		if exit := dur - trophyExitMS; el > exit {
			p := uiClamp01(float64(el-exit) / trophyExitMS)
			offset = p * p * p
			alpha = 1 - p*p
		}
		return offset, alpha
	}
	if el < trophyFadeInMS {
		alpha = float64(el) / trophyFadeInMS
	}
	if exit := dur - trophyFadeOutMS; el > exit {
		alpha = math.Min(alpha, 1-uiClamp01(float64(el-exit)/trophyFadeOutMS))
	}
	return 0, alpha
}

// trophyDrawCard draws the card at its point in the timeline.
func trophyDrawCard(renderer *sdl.Renderer, config *Config, now uint64) {
	t := trophyShow.toast
	el := now - trophyShow.start
	l := trophyCardLayoutFor(config, t)
	offset, alpha := trophyCardMotion(trophyShow.full, el, trophyShow.dur)
	if alpha <= 0.01 {
		trophyShow.card = sdl.Rect{}
		return
	}
	margin := screenWidth - (l.card.X + l.card.W)
	dx := int32(math.Round(offset * float64(l.card.W+margin+8)))
	l.card.X += dx
	l.medal.X += dx
	l.textX += dx
	trophyShow.card = l.card

	area := sdl.Rect{X: l.card.X - 36, Y: l.card.Y - 36, W: l.card.W + 72, H: l.card.H + 72}
	compositeUILayerIn(renderer, alpha, 0, area, func() {
		trophyPaintCard(renderer, config, l, t, el)
	})
}

// trophyPaintCard paints the card's pixels at layout l, el ms into its life.
func trophyPaintCard(renderer *sdl.Renderer, config *Config, l trophyCardLayout, t trophyToast, el uint64) {
	full := trophyShow.full
	metal := trophyMetalOf(t.tier)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// A halo of the tier's colour behind the card: it reads as light from the
	// trophy rather than a drop shadow, which Home's language does not use.
	haloA := uint8(70)
	if t.tier == trophyPlatinum {
		haloA = 110
	}
	homeGlowBlob(renderer, sdl.Rect{X: l.card.X - 30, Y: l.card.Y - 30, W: l.card.W + 60, H: l.card.H + 60}, metal.base, haloA)

	drawGlassSheet(renderer, l.card, glassSheetFill())
	// The metal catches the light along the card's top edge.
	edge := metal.light
	edge.A = 150
	fillRoundedRect(renderer, l.card.X+OverlayAccentInset, l.card.Y+1, l.card.W-2*OverlayAccentInset, 2, 1, edge)

	// The medallion pops in; a bloom of light opens behind it as it lands.
	scale := 1.0
	if full {
		scale = trophyPopScale(el)
		if el > 140 && el < 760 {
			p := float64(el-140) / 620
			size := int32(float64(l.medal.W) * (1 + 1.4*p))
			a := uint8(170 * (1 - p))
			cx := l.medal.X + l.medal.W/2
			cy := l.medal.Y + l.medal.H/2
			homeGlowBlob(renderer, sdl.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size}, metal.light, a)
		}
	}
	// The medallion keeps its size (its discs are cached per radius, and a
	// radius per frame would mint a texture per frame); the cup, cached once
	// and scaled in the blit, is what pops.
	cx := l.medal.X + l.medal.W/2
	cy := l.medal.Y + l.medal.H/2
	trophyDrawMedallion(renderer, cx, cy, l.medal.W/2, t.tier, true)
	base := l.medal.W * 72 / 100
	cup := int32(math.Round(float64(base) * scale))
	cupDst := sdl.Rect{X: cx - cup/2, Y: cy - cup/2 + l.medal.W/40, W: cup, H: cup}
	trophyDrawCup(renderer, cupDst, base, t.tier)

	if full {
		// The shine crosses the cup once it has settled; the platinum gets a
		// second pass while its fanfare holds.
		trophyDrawShine(renderer, cupDst, (float64(el)-380)/650, 175)
		if t.tier == trophyPlatinum {
			trophyDrawShine(renderer, cupDst, (float64(el)-1700)/650, 175)
		}
		// A glint where the light leaves the rim.
		if el > 860 && el < 1260 {
			p := float64(el-860) / 400
			a := uint8(255 * math.Sin(math.Pi*p))
			trophyDrawGlint(renderer, cupDst.X+cupDst.W*78/100, cupDst.Y+cupDst.H*14/100, l.medal.W/3, a)
		}
	}
	if t.more > 0 {
		// A summary card wears its count on the medallion.
		trophyDrawCountBadge(renderer, config, l.medal, t.more)
	}

	if config == nil {
		return
	}
	capFont := getFontSized(config, "small", trophyCardCaptionSize)
	titleFont := getFontSized(config, "small", trophyCardTitleSize)
	lineFont := getFontSized(config, "small", trophyCardDetailSize)
	if capFont != nil {
		renderText(renderer, config, capFont, ellipsize(capFont, t.caption, l.textW), trophyTierInk(t.tier), l.textX, l.capY)
	}
	if titleFont != nil {
		renderText(renderer, config, titleFont, ellipsize(titleFont, t.title, l.textW), ColorTextPrimary(), l.textX, l.titleY)
	}
	if lineFont != nil && t.detail != "" {
		dot := int32(4)
		lh := int32(lineFont.Height())
		fillCircle(renderer, l.textX+dot, l.lineY+lh/2, dot, metal.base)
		x := l.textX + 2*dot + 8
		renderText(renderer, config, lineFont, ellipsize(lineFont, t.detail, l.textX+l.textW-x), ColorTextSecondary(), x, l.lineY)
	}
}

// trophyDrawCountBadge hangs "+N" on a medallion's lower right.
func trophyDrawCountBadge(renderer *sdl.Renderer, config *Config, medal sdl.Rect, n int) {
	if config == nil || n <= 0 {
		return
	}
	font := getFontSized(config, "small", 12)
	if font == nil {
		return
	}
	label := "+" + strconv.Itoa(n)
	tw, th := measureText(font, label)
	h := th + 4
	w := tw + 10
	if w < h {
		w = h
	}
	x := medal.X + medal.W - w + 4
	y := medal.Y + medal.H - h + 2
	accent := glassAccent()
	fillRoundedRect(renderer, x, y, w, h, h/2, accent)
	renderText(renderer, config, font, label, contrastText(accent), x+(w-tw)/2, y+(h-th)/2)
}
