package main

// game_daily.go — the daily challenge (GAME-12): one game from the library a
// day with a simple goal, play it for 15 minutes, offered on Home's Continue
// card when nothing is in progress there, and a trophy for meeting it.
//
// "Nothing in progress" is the card with nothing to resume, or with only
// something last touched before today and more than dailyReplaceAfter ago (a
// game quit just after midnight is still the one to resume): once anything is
// played or watched today the card is Continue again (and says how far the
// challenge is when it shows the challenge's game). Yesterday's game is still
// one press away in the game switcher and the library's Continue row. On the
// Tabs Home the challenge is Recent's own first item, beside the Continue item
// rather than in its place (homeTabsRecentItems, hometabs_items.go).
//
// The pick skips games hidden in the library, games no emulator here can
// start, and, on a handheld, consoles only a desktop plays. A picked file
// that is gone later (deleted, its card taken out) drops the challenge for
// the day instead of offering a game A cannot start.
//
// The day comes from the wall clock, and only once the clock is believed
// (dailyClockTrusted): the handheld starts at its power-on default and network
// time sets it later, so until then there is no challenge at all rather than
// one for 1 January 2000. The progress is game time the playtime tracker
// credits to that very file (playtimeCreditGameGap: a session it measured on
// the monotonic clock, 30 seconds or more), never the wall clock, so the
// trophy is earned only by playing.
//
// The pick is made off the render thread: the challenge file is read and, on a
// new day, the library scanned once and one game chosen from the day's hash —
// the same day gives the same game. The pick and its progress are kept in
// daily-challenge.json in JukaHub's data folder, so a restart neither forgets
// the minutes nor counts a met goal twice.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// dailyGoalSeconds is the goal: 15 minutes of the day's game.
	dailyGoalSeconds = 15 * 60
	// dailyStateName is the challenge file in JukaHub's data folder.
	dailyStateName = "daily-challenge.json"
	// dailyStateMax bounds the read; a real file is a few hundred bytes.
	dailyStateMax = 16 << 10
	// dailyRetryAfter spaces the picks of a day whose library had no game.
	dailyRetryAfter = 10 * time.Minute
	// dailyDayLayout is the day key, local time.
	dailyDayLayout = "2006-01-02"
	// dailyReplaceAfter is how long ago a Continue item from before today
	// must have been touched for the challenge to take its place.
	dailyReplaceAfter = 8 * time.Hour
	// dailyRecheckAfter spaces the checks that today's file is still there.
	dailyRecheckAfter = 2 * time.Minute
	// dailyPickTries bounds the games the pick vets from the day's place.
	dailyPickTries = 32
)

// dailyChallenge is one day's challenge, as the file keeps it.
type dailyChallenge struct {
	Day    string `json:"day"`
	Path   string `json:"path"`
	System string `json:"system"`
	Title  string `json:"title"`
	// Played is the game time credited to Path on Day; Done is set once, when
	// it reaches the goal.
	Played int64 `json:"played"`
	Done   bool  `json:"done"`
	// Gone is set when Path was found missing: no card until tomorrow. Not
	// saved; a restart looks again.
	Gone bool `json:"-"`
}

// daily is the challenge state. cur is published by the preparing goroutine
// and updated by the credit on the render thread.
var daily struct {
	mu   sync.Mutex
	cur  dailyChallenge
	busy bool // a goroutine is reading or picking
	// fileDay is the day the file was read for; pickDay and pickAt the day
	// and the moment (monotonic) a pick was last tried; checkAt the moment
	// the challenge's file was last found; noneDay a day whose library had
	// games but none the pick could offer (no retry until the next day: the
	// same scan would find the same).
	fileDay string
	pickDay string
	pickAt  time.Time
	checkAt time.Time
	noneDay string
}

// dailySaveMu keeps two writes of the file from crossing.
var dailySaveMu sync.Mutex

// dailyTrusted latches once the clock has been believed. Render thread.
var dailyTrusted bool

// dailyClockTrusted reports whether the wall clock can name today. Never
// before this build existed (the power-on default); where JukaHub keeps the
// time itself, only once network time has confirmed or set it; a PC, or a
// handheld whose owner sets the time by hand, is believed. The same rule as
// the calendar's reminders. Render thread.
func dailyClockTrusted(now time.Time) bool {
	if !trophyClockSane(now) {
		return false
	}
	if dailyTrusted {
		return true
	}
	if !netTime.enabled.Load() || !clockSetSupported() {
		return true
	}
	if clockConfirmCount.Load() > 0 {
		dailyTrusted = true
	} else if clockJumps.count.Load() > 0 {
		clockJumps.mu.Lock()
		dailyTrusted = clockJumps.total.clock != 0
		clockJumps.mu.Unlock()
	}
	return dailyTrusted
}

// dailyStatePath is the challenge file, "" when there is no data folder.
func dailyStatePath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, dailyStateName)
}

// dailyChallengeToday returns today's challenge once it is ready, and starts
// getting it ready: the file is read once a day, and when it holds no
// challenge for today and pick is set (the card would show one) the library
// is scanned and a game picked. Cheap enough for every card rebuild. Render
// thread.
func dailyChallengeToday(pick bool) (dailyChallenge, bool) {
	now := time.Now()
	if !dailyClockTrusted(now) {
		return dailyChallenge{}, false
	}
	day := now.Format(dailyDayLayout)
	daily.mu.Lock()
	cur := daily.cur
	start := false
	if cur.Day != day && !daily.busy {
		switch {
		case daily.fileDay != day:
			start = true
		case pick && daily.noneDay != day && (daily.pickDay != day || time.Since(daily.pickAt) > dailyRetryAfter):
			start = true
		}
	}
	if start {
		daily.busy = true
		if pick {
			daily.pickDay, daily.pickAt = day, now
		}
	}
	// While the card would show it, look now and then that the file is still
	// there (a stat, so off this thread).
	recheck := !start && pick && !daily.busy && cur.Day == day && cur.Path != "" && !cur.Gone && !cur.Done &&
		time.Since(daily.checkAt) > dailyRecheckAfter
	if recheck {
		daily.busy, daily.checkAt = true, now
	}
	daily.mu.Unlock()
	if start {
		go dailyPrepare(day, pick, dailyStatePath(), romLibraryRoots())
	}
	if recheck {
		go dailyRecheck(day, cur.Path)
	}
	return cur, cur.Day == day && cur.Path != "" && !cur.Gone
}

// dailyRecheck drops today's challenge when its file is gone: the card then
// offers nothing for the rest of the day rather than a game A cannot start.
// Off the render thread.
func dailyRecheck(day, path string) {
	gone := !regularFile(path)
	daily.mu.Lock()
	drop := gone && daily.cur.Day == day && dailySamePath(daily.cur.Path, path) && !daily.cur.Gone
	if drop {
		daily.cur.Gone = true
	}
	title := daily.cur.Title
	daily.busy = false
	daily.mu.Unlock()
	if drop {
		logInfo("daily", "today's challenge %s is gone from the library; none until tomorrow", title)
	}
}

// dailyPrepare reads the file and, when it has nothing for day and pick is
// set, picks a game from the library. Off the render thread.
func dailyPrepare(day string, pick bool, path string, roots []string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("daily", "preparing the daily challenge failed: %v", r)
		}
		daily.mu.Lock()
		daily.busy = false
		daily.mu.Unlock()
	}()
	saved, ok := dailyLoad(path)
	if ok && saved.Day == day {
		// The day's pick stays the day's pick; if its file is gone there is
		// no challenge today.
		saved.Gone = !regularFile(saved.Path)
	}
	daily.mu.Lock()
	daily.fileDay = day
	if ok && saved.Day == day && daily.cur.Day != day {
		daily.cur = saved
		daily.checkAt = time.Now()
	}
	have := daily.cur.Day == day
	daily.mu.Unlock()
	if have || !pick {
		return
	}
	games := scanROMLibrary(roots).Games
	game, ok := dailyPick(day, games, !IsWindows(), dailyPlayableFunc())
	if !ok {
		if len(games) == 0 {
			// Perhaps a card not mounted yet: look again in dailyRetryAfter.
			logDebug("daily", "no game to pick for %s", day)
			return
		}
		// Games, but none the card could offer: the same scan would find the
		// same, so wait for the next day.
		daily.mu.Lock()
		daily.noneDay = day
		daily.mu.Unlock()
		logInfo("daily", "no playable game among %d for %s; no challenge today", len(games), day)
		return
	}
	c := dailyChallenge{Day: day, Path: game.Path, System: game.System, Title: gameDisplayTitle(game.Path)}
	daily.mu.Lock()
	if daily.cur.Day != day {
		daily.cur = c
		daily.checkAt = time.Now()
	}
	daily.mu.Unlock()
	logInfo("daily", "today's challenge: %s (%s)", c.Title, c.System)
	dailyPersist(path)
}

// dailyPick chooses day's game: the games ranked by a hash of the day and
// each game's path, so the same day lands on the same game and the order is
// spread across consoles (a run of one console's games, none of which an
// emulator here starts, cannot use up the tries). On a handheld the consoles
// only a desktop plays (emusystems.go) are left out. playable, when set, vets
// the games in that order, at most dailyPickTries of them.
func dailyPick(day string, games []scannedROM, handheld bool, playable func(scannedROM) bool) (scannedROM, bool) {
	candidates := make([]scannedROM, 0, len(games))
	for _, g := range games {
		if strings.TrimSpace(g.Path) == "" || strings.TrimSpace(g.System) == "" || gameDisplayTitle(g.Path) == "" {
			continue
		}
		if _, desktopOnly := emuDesktopSystemFor(g.System); handheld && desktopOnly {
			continue
		}
		candidates = append(candidates, g)
	}
	if len(candidates) == 0 {
		return scannedROM{}, false
	}
	type ranked struct {
		g    scannedROM
		key  uint32
		path string // folded, for ties
	}
	order := make([]ranked, len(candidates))
	h := fnv.New32a()
	for i, g := range candidates {
		path := strings.ToLower(g.Path)
		h.Reset()
		_, _ = h.Write([]byte("jukahub-daily|" + day + "|" + path))
		order[i] = ranked{g: g, key: h.Sum32(), path: path}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].key != order[j].key {
			return order[i].key < order[j].key
		}
		return order[i].path < order[j].path
	})
	for i := 0; i < len(order) && i < dailyPickTries; i++ {
		if playable == nil || playable(order[i].g) {
			return order[i].g, true
		}
	}
	return scannedROM{}, false
}

// dailyPlayableFunc vets a pick the way A on the card would start it: the
// file is there, the user has not hidden it in the library, and an emulator
// here can start it (resolveGameLauncherFor, as the game page resolves it).
// It stats, hashes and reads launcher files, so off the render thread only.
func dailyPlayableFunc() func(scannedROM) bool {
	hidden := map[string]bool{}
	for key, view := range libraryViewSnapshot().Games {
		if view.Hidden {
			hidden[key] = true
		}
	}
	return func(g scannedROM) bool {
		if !regularFile(g.Path) {
			return false
		}
		// The hidden flag is keyed by the file's content; hash only when
		// something is hidden at all.
		if len(hidden) > 0 && hidden[libraryContentKey(g.Path)] {
			return false
		}
		return resolveGameLauncherFor(libraryGame{Path: g.Path, System: g.System, Title: gameDisplayTitle(g.Path)}) != ""
	}
}

// dailyLoad reads the challenge file. A missing or damaged file is no
// challenge.
func dailyLoad(path string) (dailyChallenge, bool) {
	if path == "" {
		return dailyChallenge{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return dailyChallenge{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, dailyStateMax))
	if err != nil {
		return dailyChallenge{}, false
	}
	var c dailyChallenge
	if err := json.Unmarshal(raw, &c); err != nil {
		logWarn("daily", "ignoring %s: %v", path, err)
		return dailyChallenge{}, false
	}
	if _, err := time.Parse(dailyDayLayout, c.Day); err != nil || strings.TrimSpace(c.Path) == "" {
		return dailyChallenge{}, false
	}
	if c.Played < 0 {
		c.Played = 0
	}
	return c, true
}

// dailyPersist writes the challenge as it is now. Off the render thread; the
// newest state always wins, since each write reads it under the lock.
func dailyPersist(path string) {
	if path == "" {
		return
	}
	dailySaveMu.Lock()
	defer dailySaveMu.Unlock()
	daily.mu.Lock()
	c := daily.cur
	daily.mu.Unlock()
	if c.Day == "" {
		return
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("daily", "cannot save the daily challenge: %v", err)
	}
}

// dailySamePath compares two game paths the way the card names them.
func dailySamePath(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// dailyChallengeCredit adds a game session the playtime tracker just credited
// (playtimeCreditGameGap) to today's challenge when it was that game, and
// meets the goal once. Render thread.
func dailyChallengeCredit(path string, secs int64) {
	if secs <= 0 {
		return
	}
	now := time.Now()
	if !dailyClockTrusted(now) {
		return
	}
	day := now.Format(dailyDayLayout)
	daily.mu.Lock()
	c := &daily.cur
	if c.Day != day || c.Done || !dailySamePath(c.Path, path) {
		daily.mu.Unlock()
		return
	}
	c.Played += secs
	met := c.Played >= dailyGoalSeconds
	if met {
		c.Done = true
	}
	title := c.Title
	daily.mu.Unlock()
	if met {
		trophyEvent(tkDailyChallenge)
		ShowToast("Daily challenge done · 15 minutes of "+title, ToastKindSuccess)
	}
	homeContinueInvalidate()
	go dailyPersist(dailyStatePath())
}

// dailyStale reports whether a Continue item was last touched before today:
// nothing in progress for the challenge's purposes. A music session loaded
// now is stamped now, so it is never stale.
func dailyStale(it FavoriteItem, today string) bool {
	return it.Timestamp.IsZero() || it.Timestamp.Local().Format(dailyDayLayout) < today
}

// homeContinueDaily is the challenge the Continue card offers instead of what
// it would resume: today's, not yet met, when the card has nothing in
// progress. The Tabs Home shows it beside what Continue resumes instead
// (homeTabsRecentItems). Render thread.
func homeContinueDaily(it FavoriteItem, ok bool) (dailyChallenge, bool) {
	// From before today and not just before midnight: a game launched at
	// 23:50 and quit at 00:30 is still the one to resume.
	stale := !ok || (dailyStale(it, time.Now().Format(dailyDayLayout)) && time.Since(it.Timestamp) > dailyReplaceAfter)
	c, ready := dailyChallengeToday(stale)
	if !ready || c.Done || !stale {
		return dailyChallenge{}, false
	}
	return c, true
}

// dailyGame is the challenge as a recent game, for the card's art and Play.
func (c dailyChallenge) dailyGame() recentGame {
	return recentGame{Path: c.Path, System: c.System, Title: c.Title}
}

// dailyMinutesLeft is the whole minutes still to play, rounded up.
func (c dailyChallenge) dailyMinutesLeft() int64 {
	left := int64(dailyGoalSeconds) - c.Played
	if left < 0 {
		left = 0
	}
	return (left + 59) / 60
}

// dailyRecentLine is the challenge's second line on the Tabs Home's Recent
// page: what it is and the minutes still to play.
func (c dailyChallenge) dailyRecentLine() string {
	return fmt.Sprintf("Daily challenge · %d min left", c.dailyMinutesLeft())
}

// dailyContinueState is the Continue card showing the challenge.
func dailyContinueState(c dailyChallenge) HomeContinueState {
	st := HomeContinueState{
		HasRecent: true,
		Kind:      "game",
		Chip:      "Daily challenge",
		Action:    "Play",
		Title:     c.Title,
		Subtitle:  "Play it for 15 minutes today · " + c.System,
		Preview:   homeArtPreviewJob(c.dailyGame()),
	}
	if c.Played > 0 {
		st.Progress = float32(c.Played) / float32(dailyGoalSeconds)
		if st.Progress > 1 {
			st.Progress = 1
		}
		st.Remaining = fmt.Sprintf("%d min left", c.dailyMinutesLeft())
	}
	return st
}

// dailyContinueNote is what the Continue card adds to its subtitle when the
// game it resumes is today's challenge: how far it is, or that it is met.
func dailyContinueNote(it FavoriteItem) string {
	g, isGame := it.Data.(recentGame)
	if it.Type != "game" || !isGame {
		return ""
	}
	daily.mu.Lock()
	c := daily.cur
	daily.mu.Unlock()
	if c.Day != time.Now().Format(dailyDayLayout) || !dailySamePath(c.Path, g.Path) {
		return ""
	}
	if c.Done {
		return "Daily challenge met"
	}
	return fmt.Sprintf("Daily challenge · %d of 15 min", c.Played/60)
}
