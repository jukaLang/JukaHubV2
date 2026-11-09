package main

// Playtime & trophies. Tracking is deliberately cheap: one wall-clock sample per
// frame, integer seconds, and a structured snapshot saved through the existing
// user-config debounce. The screen (the trophy room, trophy_room.go) is a
// registered scene so it owns its own rendering without adding config elements;
// an unlock is celebrated by trophy_celebrate.go.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	playtimeCustomKey     = "playtime_v1"
	playtimeSceneName     = "Playtime"
	playtimeSaveInterval  = 30 * time.Second
	playtimeMaxFrameDelta = 5 * time.Second
)

type playtimeData struct {
	Total    int64             `json:"total"`
	Scenes   map[string]int64  `json:"scenes"`
	Days     map[string]int64  `json:"days"`
	Unlocked map[string]string `json:"unlocked"`
	// Games is the time spent in games launched from JukaHub, by title. It is
	// absent from saves written before it existed, which read back as empty.
	Games map[string]int64 `json:"games,omitempty"`
}

type playtimeState struct {
	loaded   bool
	data     playtimeData
	lastTick time.Time
	lastSave time.Time
	carry    float64
	// rev changes whenever the snapshot does, so the trophy room rebuilds its
	// sorted list only when there is something new to show.
	rev uint64
}

var playtime playtimeState

func newPlaytimeData() playtimeData {
	return playtimeData{
		Scenes:   map[string]int64{},
		Days:     map[string]int64{},
		Unlocked: map[string]string{},
		Games:    map[string]int64{},
	}
}

// playtimeInit decodes the persisted snapshot, tolerating the map form that
// comes back from JSON and the typed struct form used in-process.
func playtimeInit(config *Config) {
	playtime = playtimeState{data: newPlaytimeData()}
	if config != nil {
		if raw, ok := config.Variables.Custom[playtimeCustomKey]; ok {
			if b, err := json.Marshal(raw); err == nil {
				var data playtimeData
				if err := json.Unmarshal(b, &data); err == nil {
					if data.Scenes == nil {
						data.Scenes = map[string]int64{}
					}
					if data.Days == nil {
						data.Days = map[string]int64{}
					}
					if data.Unlocked == nil {
						data.Unlocked = map[string]string{}
					}
					if data.Games == nil {
						data.Games = map[string]int64{}
					}
					playtime.data = data
				}
			}
		}
	}
	playtime.loaded = true
	playtime.lastTick = time.Now()
	playtime.lastSave = time.Now()
	playtime.rev++
}

func playtimePersist(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	playtimePruneDays()
	config.Variables.Custom[playtimeCustomKey] = playtime.data
	playtime.lastSave = time.Now()
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// playtimeFlush is called on graceful exit so the last partial interval and
// any freshly unlocked trophy are not lost.
func playtimeFlush(config *Config) {
	if !playtime.loaded || config == nil {
		return
	}
	playtimeTick(config)
	playtimePruneDays()
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[playtimeCustomKey] = playtime.data
	// Synchronous on exit: the debounced writer can be still pending when the
	// process closes, which used to lose the last few seconds of stats.
	saveUserConfig(userConfigFromVariables(config))
}

// playtimeMaxGames bounds the per-game ledger. A handheld library is large, but
// the screen shows the top few and the save should not grow with every ROM
// ever started, so the shortest sessions make room for new titles.
const playtimeMaxGames = 64

func playtimePruneDays() {
	cutoff := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	for day := range playtime.data.Days {
		if day < cutoff {
			delete(playtime.data.Days, day)
		}
	}
	for len(playtime.data.Games) > playtimeMaxGames {
		least, leastSecs := "", int64(-1)
		for title, secs := range playtime.data.Games {
			if leastSecs < 0 || secs < leastSecs || (secs == leastSecs && title < least) {
				least, leastSecs = title, secs
			}
		}
		delete(playtime.data.Games, least)
	}
}

func playtimeTodayKey() string {
	return time.Now().Format("2006-01-02")
}

func playtimeRecord(seconds int64, scene string) {
	if seconds <= 0 {
		return
	}
	if scene == "" {
		scene = "Home"
	}
	if playtime.data.Scenes == nil {
		playtime.data.Scenes = map[string]int64{}
	}
	if playtime.data.Days == nil {
		playtime.data.Days = map[string]int64{}
	}
	if playtime.data.Unlocked == nil {
		playtime.data.Unlocked = map[string]string{}
	}
	playtime.data.Total += seconds
	playtime.data.Scenes[scene] += seconds
	day := playtimeTodayKey()
	playtime.data.Days[day] += seconds
	playtimeRunNote(day, seconds)
	playtime.rev++
	playtimeCheckAchievements()
}

// playtimePanelScene names the full-screen panel the user is in, if any. Save
// Manager, the Curation Hub, PortMaster and the Emulators list are panels over
// whatever scene opened them (usually Home), so the scene name would credit the
// wrong screen and their trophies could never be earned. Their time is recorded
// under the names those trophies use instead.
func playtimePanelScene() string {
	switch {
	case savePanel.open:
		return "SaveManager"
	case curationPanel.open:
		return "Curation"
	case portsPanel.open:
		return "Ports"
	case emuPanel.open:
		return "Emulator"
	}
	return ""
}

// playtimePanelSceneNames are the names playtimePanelScene records under.
var playtimePanelSceneNames = []string{"SaveManager", "Curation", "Ports", "Emulator"}

// playtimePanelSceneAvailable reports whether a panel exists on this platform:
// PortMaster is a handheld feature and is not offered on Windows.
func playtimePanelSceneAvailable(name string) bool {
	if name == "Ports" {
		return !IsWindows()
	}
	return true
}

// playtimeActiveScene reports the canonical scene key used for stats. The
// onboarding tour is not an app and is therefore not counted.
func playtimeActiveScene(config *Config) string {
	if config == nil || onboarding.active || screensaverActiveNow() {
		return ""
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	if panel := playtimePanelScene(); panel != "" {
		return panel
	}
	return config.Scenes[currentSceneIndex].Name
}

// screensaverActiveNow keeps the tracker out of the screensaver package's
// internals: the screensaver's active flag is the app-wide idle state.
func screensaverActiveNow() bool {
	return ss.active
}

func playtimeTick(config *Config) {
	if !playtime.loaded {
		playtimeInit(config)
		return
	}
	// Which trophies this setup can award follows the live config (scenes are
	// injected at startup and replaced by a hot reload). Cheap when unchanged.
	trophySyncAvailability(config)
	// Network time or a new zone moved the date: re-anchor (clockjump.go).
	playtimeClockJumpCheck()
	now := time.Now()
	if playtime.lastTick.IsZero() {
		playtime.lastTick = now
		return
	}
	delta := now.Sub(playtime.lastTick)
	gapStart := playtime.lastTick
	playtime.lastTick = now
	if delta > playtimeMaxFrameDelta {
		// No frame for a while: a game owned the screen (the loop parks while a
		// child runs) or the device was asleep. A game launched right before the
		// gap gets the time; anything else is dropped as before.
		playtimeCreditGameGap(gapStart, now)
		return
	}
	if delta <= 0 {
		return
	}
	scene := playtimeActiveScene(config)
	if scene == "" {
		return
	}
	playtime.carry += delta.Seconds()
	whole := int64(playtime.carry)
	if whole <= 0 {
		return
	}
	playtime.carry -= float64(whole)
	playtimeRecord(whole, scene)

	if now.Sub(playtime.lastSave) >= playtimeSaveInterval {
		playtimePersist(config)
	}
}

// ── Game sessions ─────────────────────────────────────────────────────────────
//
// The render loop parks while a launched game owns the framebuffer
// (gamesuspend.go), so the tick stream simply stops and resumes when the game
// exits. The gap is the session. It is credited only when the game library
// recorded a launch at the moment the gap began: an app started some other way,
// or a device that slept, leaves no such record and is not counted. Go's
// monotonic clock does not advance while a Linux device is suspended, so a
// handheld that sleeps mid-game is not credited for the night either.

const (
	// playtimeGameLaunchSlack is how far the recorded launch may be from the
	// last frame before the gap: the launch is recorded on the frame the game
	// was started from, give or take an emulator that needs a moment to start.
	playtimeGameLaunchSlack = 20 * time.Second
	// playtimeGameMinSession ignores a launch that failed straight away.
	playtimeGameMinSession = 30 * time.Second
	// playtimeGameMaxSession caps one session: a device left running a game
	// overnight is not a twelve-hour play session.
	playtimeGameMaxSession = 10 * time.Hour
)

// playtimeGameCredit decides what a gap in the tick stream is worth. It is pure
// so the attribution rules can be tested without a library on disk.
func playtimeGameCredit(games []recentGame, gapStart, gapEnd time.Time) (string, int64, bool) {
	if len(games) == 0 {
		return "", 0, false
	}
	g := games[0]
	if g.PlayedAt.IsZero() {
		return "", 0, false
	}
	offset := g.PlayedAt.Sub(gapStart)
	if offset < -playtimeGameLaunchSlack || offset > playtimeGameLaunchSlack {
		return "", 0, false
	}
	length := gapEnd.Sub(gapStart)
	if length < playtimeGameMinSession {
		return "", 0, false
	}
	if length > playtimeGameMaxSession {
		length = playtimeGameMaxSession
	}
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = strings.TrimSpace(g.Path)
		if i := strings.LastIndexAny(title, `/\`); i >= 0 {
			title = title[i+1:]
		}
		if dot := strings.LastIndex(title, "."); dot > 0 {
			title = title[:dot]
		}
	}
	if title == "" {
		return "", 0, false
	}
	return title, int64(length / time.Second), true
}

// playtimeCreditGameGap records a game session for the gap that just ended.
// The session counts toward the lifetime total and the day it ended on — the
// trophies say "in JukaHub", and a game started from JukaHub is time spent in it
// — and its own per-title ledger, but not toward any screen.
func playtimeCreditGameGap(gapStart, gapEnd time.Time) {
	title, secs, ok := playtimeGameCredit(recentGames(), gapStart, gapEnd)
	if !ok {
		return
	}
	if playtime.data.Games == nil {
		playtime.data.Games = map[string]int64{}
	}
	if playtime.data.Days == nil {
		playtime.data.Days = map[string]int64{}
	}
	if playtime.data.Unlocked == nil {
		playtime.data.Unlocked = map[string]string{}
	}
	playtime.data.Games[title] += secs
	playtime.data.Total += secs
	day := playtimeTodayKey()
	playtime.data.Days[day] += secs
	playtimeRunNote(day, secs)
	playtime.rev++
	playtimeCheckAchievements()
}

// ── Trophies ──────────────────────────────────────────────────────────────────

// trophyTier is a trophy's grade, from the easiest to the rarest. It decides the
// colour, the points it is worth and the celebration it gets.
type trophyTier uint8

const (
	trophyBronze trophyTier = iota
	trophySilver
	trophyGold
	trophyPlatinum
)

// trophyTierCount is the number of tiers, for per-tier tables and counts.
const trophyTierCount = 4

// trophyTierName is the tier as the UI says it.
func trophyTierName(t trophyTier) string {
	switch t {
	case trophySilver:
		return "Silver"
	case trophyGold:
		return "Gold"
	case trophyPlatinum:
		return "Platinum"
	}
	return "Bronze"
}

// trophyTierPoints is what a tier adds to the trophy level. The ratios are the
// familiar console ones, so a gold reads as worth six bronzes and the platinum
// as a level of its own.
func trophyTierPoints(t trophyTier) int {
	switch t {
	case trophySilver:
		return 30
	case trophyGold:
		return 90
	case trophyPlatinum:
		return 300
	}
	return 15
}

// trophyRarityLabel is the rarity line of the details sheet. There is no shared
// server counting players, so it is the tier's difficulty in rarity words rather
// than a made-up percentage.
func trophyRarityLabel(t trophyTier) string {
	switch t {
	case trophySilver:
		return "Rare"
	case trophyGold:
		return "Very rare"
	case trophyPlatinum:
		return "Ultra rare"
	}
	return "Common"
}

// trophyLevelStep is how many points one trophy level takes.
const trophyLevelStep = 100

// trophyLevel turns earned points into a level (1 at zero points) and the
// fraction of the way to the next one.
func trophyLevel(points int) (int, float64) {
	if points < 0 {
		points = 0
	}
	return 1 + points/trophyLevelStep, float64(points%trophyLevelStep) / float64(trophyLevelStep)
}

// trophyMetric is what a trophy measures. Every trophy is a threshold on one of
// these, so the same numbers drive both the unlock and the progress bar.
type trophyMetric uint8

const (
	trophyMetricTotal    trophyMetric = iota // lifetime seconds
	trophyMetricDays                         // days the app was used
	trophyMetricStreak                       // consecutive days ending today
	trophyMetricBestDay                      // longest single day, seconds
	trophyMetricWeek                         // the last seven days, seconds
	trophyMetricScreens                      // distinct screens used
	trophyMetricScene                        // seconds in one screen
	trophyMetricTrophies                     // other trophies earned
	trophyMetricPlatinum                     // every other trophy this setup offers
)

// playtimeAchievement is one trophy. The checks read only accumulated data, so
// a trophy stays earned after a restart and can be recomputed from the snapshot.
type playtimeAchievement struct {
	ID    string
	Title string
	Desc  string
	// Icon is the trophy's number in the fixed set ("07"). It is assigned in
	// init and never moves when a sort reorders the room.
	Icon string
	// Group is the family the trophy belongs to ("Lifetime", "Screens", ...).
	Group  string
	Tier   trophyTier
	Hidden bool // shown as a hidden trophy until it is earned
	Metric trophyMetric
	Scene  string // trophyMetricScene: the screen whose time counts
	Goal   int64  // seconds, days, screens or trophies, as Metric says
	// Check reports whether a snapshot earns the trophy on its own. The unlock
	// pass does not call it (it shares one stats snapshot across the set), but
	// it is the trophy's contract and what the tests hold it to.
	Check func(playtimeData) bool
}

// ── Check helpers ─────────────────────────────────────────────────────────────
//
// Every metric goes through these, so no trophy can panic on a snapshot whose
// maps were never initialised (an old save, or a test fixture).

// playtimeSceneAliases maps a screen to the older names its time was recorded
// under. The Tools hub replaced the Misc page; minutes spent on Misc still count
// toward the Tools trophy instead of being stranded under a name no screen uses.
var playtimeSceneAliases = map[string][]string{
	"Tools": {"Misc"},
}

// playtimeCanonicalScene is the name a recorded scene is shown and counted
// under: an old alias folds into the screen that replaced it.
func playtimeCanonicalScene(name string) string {
	for canonical, aliases := range playtimeSceneAliases {
		for _, alias := range aliases {
			if alias == name {
				return canonical
			}
		}
	}
	return name
}

// playtimeSceneSeconds is one screen's time including its older names.
func playtimeSceneSeconds(d playtimeData, scene string) int64 {
	total := d.Scenes[scene]
	for _, alias := range playtimeSceneAliases[scene] {
		total += d.Scenes[alias]
	}
	return total
}

// daysUsed counts the days the app was used at all.
func daysUsed(d playtimeData) int {
	n := 0
	for _, seconds := range d.Days {
		if seconds > 0 {
			n++
		}
	}
	return n
}

// bestDaySeconds is the longest single day in the retained window.
func bestDaySeconds(d playtimeData) int64 {
	var best int64
	for _, seconds := range d.Days {
		if seconds > best {
			best = seconds
		}
	}
	return best
}

// currentStreak counts consecutive days ending today. An empty today does not
// end the streak — the day is still in progress — so the walk starts at
// yesterday in that case.
func currentStreak(d playtimeData, now time.Time) int {
	streak := 0
	for i := 0; i < 400; i++ {
		key := now.AddDate(0, 0, -i).Format(wrappedDayKey)
		if d.Days[key] > 0 {
			streak++
			continue
		}
		if i == 0 {
			continue
		}
		break
	}
	return streak
}

// trophyStats is everything the metrics read, computed once per unlock pass:
// the streak walk formats a day key per day, so recomputing it for each of the
// streak trophies on every one-second tick was work for nothing.
type trophyStats struct {
	total   int64
	bestDay int64
	week    int64
	days    int
	streak  int
	screens int
	// earned counts the trophies already earned, platinum excluded; the unlock
	// pass bumps it as it awards, so the collection trophies at the end of the
	// set see what was earned earlier in the same pass.
	earned int
}

// trophyStatsOf takes the snapshot's measurements at now.
func trophyStatsOf(d playtimeData, now time.Time) trophyStats {
	st := trophyStats{
		total:   d.Total,
		bestDay: bestDaySeconds(d),
		days:    daysUsed(d),
		streak:  currentStreak(d, now),
		screens: len(d.Scenes),
	}
	for i := 0; i < 7; i++ {
		st.week += d.Days[now.AddDate(0, 0, -i).Format(wrappedDayKey)]
	}
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if a.Metric == trophyMetricPlatinum {
			continue
		}
		if _, ok := d.Unlocked[a.ID]; ok {
			st.earned++
		}
	}
	return st
}

// trophyProgressWith reports how far d is toward a, as (current, goal) in the
// metric's own unit. A trophy is earned when current reaches goal.
func trophyProgressWith(a *playtimeAchievement, d playtimeData, st *trophyStats) (int64, int64) {
	switch a.Metric {
	case trophyMetricTotal:
		return st.total, a.Goal
	case trophyMetricDays:
		return int64(st.days), a.Goal
	case trophyMetricStreak:
		return int64(st.streak), a.Goal
	case trophyMetricBestDay:
		return st.bestDay, a.Goal
	case trophyMetricWeek:
		return st.week, a.Goal
	case trophyMetricScreens:
		return int64(st.screens), a.Goal
	case trophyMetricScene:
		return playtimeSceneSeconds(d, a.Scene), a.Goal
	case trophyMetricTrophies:
		return int64(st.earned), a.Goal
	case trophyMetricPlatinum:
		return trophyPlatinumProgress(d)
	}
	return 0, 0
}

// trophyPlatinumProgress counts the other trophies this setup can award and how
// many of them are earned. A trophy the setup cannot award (its screen is not in
// the config) is left out of both, so the platinum is always reachable.
func trophyPlatinumProgress(d playtimeData) (int64, int64) {
	var got, need int64
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if a.Metric == trophyMetricPlatinum || !trophyAvailable(i) {
			continue
		}
		need++
		if _, ok := d.Unlocked[a.ID]; ok {
			got++
		}
	}
	return got, need
}

// trophyEarnedBy is a trophy's standalone check: a fresh stats snapshot and its
// threshold. It backs playtimeAchievement.Check.
func trophyEarnedBy(a playtimeAchievement, d playtimeData) bool {
	st := trophyStatsOf(d, time.Now())
	cur, goal := trophyProgressWith(&a, d, &st)
	return goal > 0 && cur >= goal
}

// trophySceneTier grades a per-screen trophy by the time it asks for.
func trophySceneTier(seconds int64) trophyTier {
	switch {
	case seconds >= 10*3600:
		return trophyGold
	case seconds >= 3600:
		return trophySilver
	}
	return trophyBronze
}

// playtimeSceneAchievements are the per-screen trophies: one table instead of
// 57 copies of the same closure. The scene names are the config's own, so a
// screen that is not in a particular config simply never accumulates time (and
// trophySyncAvailability leaves its trophies out of the room and the platinum).
var playtimeSceneAchievements = []struct {
	id, scene, title, desc string
	seconds                int64
}{
	// Games
	{"gamer", "Games", "Gamer", "Play games for 30 minutes", 30 * 60},
	{"games_1h", "Games", "Player One", "Play games for 1 hour", 3600},
	{"games_5h", "Games", "Game Night", "Play games for 5 hours", 5 * 3600},
	{"games_25h", "Games", "Marathon Runner", "Play games for 25 hours", 25 * 3600},
	{"games_100h", "Games", "Backlog Slayer", "Play games for 100 hours", 100 * 3600},
	// Music
	{"audiophile", "MusicPlayer", "Audiophile", "Listen to music for 30 minutes", 30 * 60},
	{"music_1h", "MusicPlayer", "First Set", "Listen to music for 1 hour", 3600},
	{"music_10h", "MusicPlayer", "Headliner", "Listen to music for 10 hours", 10 * 3600},
	{"music_50h", "MusicPlayer", "Roadie", "Listen to music for 50 hours", 50 * 3600},
	// Tube
	{"tube_10m", "Tube", "Channel Surfing", "Watch Tube for 10 minutes", 10 * 60},
	{"tube_1h", "Tube", "Binge Starter", "Watch Tube for 1 hour", 3600},
	{"tube_10h", "Tube", "Prime Time", "Watch Tube for 10 hours", 10 * 3600},
	{"tube_50h", "Tube", "Couch Legend", "Watch Tube for 50 hours", 50 * 3600},
	// Shorts
	{"shorts_10m", "Shorts", "Quick Scroll", "Watch Shorts for 10 minutes", 10 * 60},
	{"shorts_2h", "Shorts", "Doom Scroll", "Watch Shorts for 2 hours", 2 * 3600},
	{"shorts_10h", "Shorts", "Endless Feed", "Watch Shorts for 10 hours", 10 * 3600},
	// Browser
	{"browser_10m", "Browser", "Open Tab", "Browse for 10 minutes", 10 * 60},
	{"browser_1h", "Browser", "Reader Mode", "Browse for 1 hour", 3600},
	{"browser_5h", "Browser", "Web Walker", "Browse for 5 hours", 5 * 3600},
	// Files and packages
	{"files_5m", "FileExplorer", "File Clerk", "Spend 5 minutes in Files", 5 * 60},
	{"files_1h", "FileExplorer", "Deep Folders", "Spend 1 hour in Files", 3600},
	{"packages_5m", "Packages", "Package Manager", "Spend 5 minutes in Packages", 5 * 60},
	{"patch_10m", "Patch", "Patcher", "Spend 10 minutes in Patch", 10 * 60},
	// Settings
	{"settings_10m", "Settings", "Tinkerer", "Spend 10 minutes in Settings", 10 * 60},
	{"settings_gen_10m", "SettingsGeneral", "Option Fiddler", "Spend 10 minutes in General settings", 10 * 60},
	{"appearance_10m", "SettingsAppearance", "Style Council", "Spend 10 minutes in Appearance", 10 * 60},
	{"themes_5m", "ThemePresets", "Theme Tourist", "Spend 5 minutes in Theme Presets", 5 * 60},
	// Chat and notes
	{"chat_10m", "Chat", "Hello World", "Spend 10 minutes in Chat", 10 * 60},
	{"chat_2h", "Chat", "Conversationalist", "Spend 2 hours in Chat", 2 * 3600},
	{"notes_10m", "Notes", "Notetaker", "Spend 10 minutes in Notes", 10 * 60},
	{"notes_2h", "Notes", "Diarist", "Spend 2 hours in Notes", 2 * 3600},
	// Terminal and the applets
	{"terminal_5m", "Terminal", "Shell Explorer", "Spend 5 minutes in Terminal", 5 * 60},
	{"terminal_2h", "Terminal", "Power User", "Spend 2 hours in Terminal", 2 * 3600},
	{"calc_5m", "Calculator", "Number Cruncher", "Spend 5 minutes in Calculator", 5 * 60},
	{"unit_5m", "UnitConverter", "Unit Translator", "Spend 5 minutes in Unit Converter", 5 * 60},
	{"dictionary_10m", "Dictionary", "Word Smith", "Spend 10 minutes in Dictionary", 10 * 60},
	// Reading
	{"pdf_10m", "PDFReader", "Page Turner", "Read PDFs for 10 minutes", 10 * 60},
	{"pdf_2h", "PDFReader", "Bookworm", "Read PDFs for 2 hours", 2 * 3600},
	// Media
	{"livetv_10m", "LiveTV", "Channel Hopper", "Watch Live TV for 10 minutes", 10 * 60},
	{"podcasts_30m", "Podcasts", "Subscriber", "Listen to podcasts for 30 minutes", 30 * 60},
	{"recordings_10m", "Recordings", "Archivist", "Spend 10 minutes in Recordings", 10 * 60},
	// Diagnostics
	{"weather_5m", "Weather", "Forecaster", "Spend 5 minutes in Weather", 5 * 60},
	{"hardware_5m", "Hardware", "Spec Sheet", "Spend 5 minutes in Hardware", 5 * 60},
	{"benchmark_5m", "Benchmark", "Score Chaser", "Spend 5 minutes in Benchmark", 5 * 60},
	{"diskspace_5m", "DiskSpace", "Space Saver", "Spend 5 minutes in Disk Space", 5 * 60},
	// Emulation and maintenance (panels: see playtimePanelScene)
	{"emulator_10m", "Emulator", "Emulator Room", "Spend 10 minutes in Emulators", 10 * 60},
	{"curation_10m", "Curation", "Curator", "Spend 10 minutes in Curation", 10 * 60},
	{"ports_10m", "Ports", "Port Authority", "Spend 10 minutes in Ports", 10 * 60},
	{"saves_10m", "SaveManager", "Save Scummer", "Spend 10 minutes in Save Manager", 10 * 60},
	// Time and focus
	{"pomodoro_30m", "Pomodoro", "Focus Session", "Spend 30 minutes in Pomodoro", 30 * 60},
	{"alarm_5m", "Alarm", "Early Bird", "Spend 5 minutes in Alarm", 5 * 60},
	// The rest of the shell. The id misc_10m is kept from the Misc page this
	// trophy used to measure, so an earned one stays earned.
	{"favorites_10m", "Favorites", "Collector", "Spend 10 minutes in Favorites", 10 * 60},
	{"apps_10m", "Apps", "App Hopper", "Spend 10 minutes in Apps", 10 * 60},
	{"misc_10m", "Tools", "Toolbox", "Spend 10 minutes in Tools", 10 * 60},
	{"jukaland_10m", "JukaLand", "Amusement Park", "Spend 10 minutes in JukaLand", 10 * 60},
	{"dashboard_10m", "Dashboard", "At a Glance", "Spend 10 minutes in Dashboard", 10 * 60},
	{"about_2m", "About", "Curious", "Read the About screen", 2 * 60},
}

// playtimeHiddenTrophies stay secret until earned: the fun ones, and the long
// hauls whose target would only discourage on day one.
var playtimeHiddenTrophies = map[string]bool{
	"thousand_hours":    true,
	"streak_two_months": true,
	"best_day_4h":       true,
	"shorts_10h":        true,
	"about_2m":          true,
}

// playtimeAchievementCount is what the room's totals and the tests use: the 100
// trophies of the original set plus the platinum.
const playtimeAchievementCount = 101

// trophyPlatinumID is the platinum: earned for every other trophy.
const trophyPlatinumID = "platinum"

// trophyNew is one row of the fixed tables below.
func trophyNew(id, title, desc, group string, tier trophyTier, metric trophyMetric, goal int64) playtimeAchievement {
	return playtimeAchievement{ID: id, Title: title, Desc: desc, Group: group, Tier: tier, Metric: metric, Goal: goal}
}

// buildPlaytimeAchievements assembles the set: lifetime time, days used,
// streaks, records, the last seven days, screens explored, one table of
// per-screen trophies, the trophies that count the other trophies and finally
// the platinum. There are exactly playtimeAchievementCount of them; the count is
// pinned by a test so the read-out on screen cannot drift from the list.
//
// Tiers follow the effort a trophy asks for: minutes and first habits are
// bronze, hours and weeks silver, days of play and month-long habits gold.
func buildPlaytimeAchievements() []playtimeAchievement {
	const h = int64(3600)
	list := []playtimeAchievement{
		// Lifetime time in the app.
		trophyNew("first_steps", "First Steps", "Spend 10 minutes in JukaHub", "Lifetime", trophyBronze, trophyMetricTotal, 10*60),
		trophyNew("one_hour", "One More Level", "Spend 1 hour in JukaHub", "Lifetime", trophyBronze, trophyMetricTotal, h),
		trophyNew("five_hours", "Five Hour Club", "Spend 5 hours in JukaHub", "Lifetime", trophySilver, trophyMetricTotal, 5*h),
		trophyNew("ten_hours", "Home Screen Hero", "Spend 10 hours in JukaHub", "Lifetime", trophySilver, trophyMetricTotal, 10*h),
		trophyNew("twentyfive_hours", "A Day and a Bit", "Spend 25 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 25*h),
		trophyNew("fifty_hours", "Regular", "Spend 50 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 50*h),
		trophyNew("hundred_hours", "Devoted", "Spend 100 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 100*h),
		trophyNew("two_hundred_hours", "Resident", "Spend 200 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 200*h),
		trophyNew("five_hundred_hours", "Permanent Resident", "Spend 500 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 500*h),
		trophyNew("thousand_hours", "Thousand Hours", "Spend 1000 hours in JukaHub", "Lifetime", trophyGold, trophyMetricTotal, 1000*h),
		// Days used.
		trophyNew("three_days", "Three Visits", "Use JukaHub on 3 days", "Days", trophyBronze, trophyMetricDays, 3),
		trophyNew("seven_days", "Week Habit", "Use JukaHub on 7 days", "Days", trophyBronze, trophyMetricDays, 7),
		trophyNew("fortnight", "Fortnight", "Use JukaHub on 14 days", "Days", trophySilver, trophyMetricDays, 14),
		trophyNew("thirty_days", "Monthly Regular", "Use JukaHub on 30 days", "Days", trophySilver, trophyMetricDays, 30),
		trophyNew("sixty_days", "Two Months In", "Use JukaHub on 60 days", "Days", trophyGold, trophyMetricDays, 60),
		trophyNew("ninety_days", "Quarter Year", "Use JukaHub on 90 days", "Days", trophyGold, trophyMetricDays, 90),
		// Streaks. The window the tracker keeps is 90 days, so 60 is the ceiling.
		trophyNew("streak_three", "Three in a Row", "Use JukaHub 3 days in a row", "Streaks", trophyBronze, trophyMetricStreak, 3),
		trophyNew("streak_week", "Seven Straight", "Use JukaHub 7 days in a row", "Streaks", trophySilver, trophyMetricStreak, 7),
		trophyNew("streak_fortnight", "Two Weeks Strong", "Use JukaHub 14 days in a row", "Streaks", trophySilver, trophyMetricStreak, 14),
		trophyNew("streak_month", "Month Streak", "Use JukaHub 30 days in a row", "Streaks", trophyGold, trophyMetricStreak, 30),
		trophyNew("streak_two_months", "Unbroken", "Use JukaHub 60 days in a row", "Streaks", trophyGold, trophyMetricStreak, 60),
		// Records.
		trophyNew("best_day_30m", "Half Hour Day", "Spend 30 minutes in a single day", "Records", trophyBronze, trophyMetricBestDay, 30*60),
		trophyNew("best_day_1h", "Solid Session", "Spend 1 hour in a single day", "Records", trophyBronze, trophyMetricBestDay, h),
		trophyNew("best_day_2h", "Long Session", "Spend 2 hours in a single day", "Records", trophySilver, trophyMetricBestDay, 2*h),
		trophyNew("best_day_4h", "All Nighter", "Spend 4 hours in a single day", "Records", trophySilver, trophyMetricBestDay, 4*h),
		// The last seven days.
		trophyNew("week_5h", "Busy Week", "Spend 5 hours in the last 7 days", "This week", trophySilver, trophyMetricWeek, 5*h),
		trophyNew("week_10h", "Full Week", "Spend 10 hours in the last 7 days", "This week", trophySilver, trophyMetricWeek, 10*h),
		trophyNew("week_20h", "Week Off", "Spend 20 hours in the last 7 days", "This week", trophyGold, trophyMetricWeek, 20*h),
		// Screens explored.
		trophyNew("explorer", "Explorer", "Use 8 different screens", "Explorer", trophyBronze, trophyMetricScreens, 8),
		trophyNew("explorer_15", "Wanderer", "Use 15 different screens", "Explorer", trophySilver, trophyMetricScreens, 15),
		trophyNew("explorer_25", "Cartographer", "Use 25 different screens", "Explorer", trophySilver, trophyMetricScreens, 25),
		trophyNew("explorer_35", "Grand Tour", "Use 35 different screens", "Explorer", trophyGold, trophyMetricScreens, 35),
		trophyNew("explorer_50", "Every Corner", "Use 50 different screens", "Explorer", trophyGold, trophyMetricScreens, 50),
	}

	for _, s := range playtimeSceneAchievements {
		a := trophyNew(s.id, s.title, s.desc, "Screens", trophySceneTier(s.seconds), trophyMetricScene, s.seconds)
		a.Scene = s.scene
		list = append(list, a)
	}

	// Trophies that count the other trophies. They sit after everything they
	// count, and the unlock pass walks the list in order, so each one sees the
	// unlocks before it.
	ladder := []struct {
		n    int
		tier trophyTier
	}{
		{5, trophyBronze}, {10, trophyBronze}, {20, trophySilver}, {30, trophySilver}, {40, trophySilver},
		{50, trophyGold}, {60, trophyGold}, {70, trophyGold}, {80, trophyGold}, {90, trophyGold},
	}
	for _, step := range ladder {
		list = append(list, trophyNew(
			fmt.Sprintf("badges_%d", step.n),
			fmt.Sprintf("%d Trophies", step.n),
			fmt.Sprintf("Earn %d trophies", step.n),
			"Collection", step.tier, trophyMetricTrophies, int64(step.n)))
	}

	// The platinum comes last of all, after the collection trophies it needs.
	list = append(list, trophyNew(trophyPlatinumID, "Hub Legend", "Earn every other trophy", "Platinum",
		trophyPlatinum, trophyMetricPlatinum, 0))

	for i := range list {
		list[i].Hidden = playtimeHiddenTrophies[list[i].ID]
	}
	// Check is attached in init, not here: it reads the finished set (the
	// collection and platinum counts), and a closure built in this initializer
	// would make playtimeAchievements depend on itself.
	return list
}

var playtimeAchievements = buildPlaytimeAchievements()

// trophyIndexByID finds a trophy in the fixed set; -1 when it is not one.
func trophyIndexByID(id string) int {
	for i := range playtimeAchievements {
		if playtimeAchievements[i].ID == id {
			return i
		}
	}
	return -1
}

// playtimeCheckAchievements awards every trophy the snapshot now earns and
// announces each one. The stats are measured once for the whole pass; the
// collection trophies and the platinum see the unlocks made earlier in it.
func playtimeCheckAchievements() {
	if playtime.data.Unlocked == nil {
		playtime.data.Unlocked = map[string]string{}
	}
	now := time.Now()
	st := trophyStatsOf(playtime.data, now)
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if _, ok := playtime.data.Unlocked[a.ID]; ok {
			continue
		}
		cur, goal := trophyProgressWith(a, playtime.data, &st)
		if goal <= 0 || cur < goal {
			continue
		}
		playtime.data.Unlocked[a.ID] = now.Format(time.RFC3339)
		if a.Metric != trophyMetricPlatinum {
			st.earned++
		}
		playtime.rev++
		trophyAnnounce(*a)
	}
}

// ── Availability ──────────────────────────────────────────────────────────────
//
// A per-screen trophy for a screen this config does not have can never be
// earned, and a collection trophy can ask for more trophies than the setup
// offers. Those are left out of the room and of the platinum's count, so the
// platinum stays reachable on every config. An earned trophy is always shown.

var trophyAvail struct {
	known  bool
	ok     []bool
	cfg    *Config
	scenes int
	gen    uint64 // bumped on every recompute, so the room knows to rebuild
}

// trophyAvailable reports whether trophy i can be earned on this setup. Before
// the first config has been seen (tests, the first frame) everything can.
func trophyAvailable(i int) bool {
	if !trophyAvail.known || i < 0 || i >= len(trophyAvail.ok) {
		return true
	}
	return trophyAvail.ok[i]
}

// trophySyncAvailability recomputes availability when the config changes: a hot
// reload hands over a new config, and startup injects scenes after load.
func trophySyncAvailability(config *Config) {
	if config == nil {
		return
	}
	if trophyAvail.known && trophyAvail.cfg == config && trophyAvail.scenes == len(config.Scenes) {
		return
	}
	trophyAvail.cfg = config
	trophyAvail.scenes = len(config.Scenes)
	trophyAvail.ok = trophyComputeAvailability(config, trophyAvail.ok[:0])
	trophyAvail.known = true
	trophyAvail.gen++
}

// trophyComputeAvailability decides, for each trophy in order, whether config
// can award it. dst is reused when it has the room.
func trophyComputeAvailability(config *Config, dst []bool) []bool {
	present := make(map[string]bool, len(config.Scenes)+len(playtimePanelSceneNames))
	for _, sc := range config.Scenes {
		present[sc.Name] = true
	}
	for _, name := range playtimePanelSceneNames {
		if playtimePanelSceneAvailable(name) {
			present[name] = true
		}
	}
	screens := len(present)
	if present["Exit"] {
		screens-- // leaving the app is not a screen anyone spends time in
	}
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		ok := true
		switch a.Metric {
		case trophyMetricScene:
			ok = present[a.Scene]
			for _, alias := range playtimeSceneAliases[a.Scene] {
				ok = ok || present[alias]
			}
		case trophyMetricScreens:
			ok = a.Goal <= int64(screens)
		}
		dst = append(dst, ok)
	}
	// A collection trophy needs that many other trophies the setup can award.
	// Dropping one can drop the next, so settle before returning.
	for {
		awardable := 0
		for i := range playtimeAchievements {
			if playtimeAchievements[i].Metric != trophyMetricPlatinum && dst[i] {
				awardable++
			}
		}
		changed := false
		for i := range playtimeAchievements {
			a := &playtimeAchievements[i]
			if a.Metric == trophyMetricTrophies && dst[i] && a.Goal > int64(awardable-1) {
				dst[i] = false
				changed = true
			}
		}
		if !changed {
			return dst
		}
	}
}

// trophyShown reports whether the room lists trophy i: anything earned, and
// anything this setup can still award.
func trophyShown(i int, d playtimeData) bool {
	if i < 0 || i >= len(playtimeAchievements) {
		return false
	}
	if _, ok := d.Unlocked[playtimeAchievements[i].ID]; ok {
		return true
	}
	return trophyAvailable(i)
}

// ── Stats helpers ─────────────────────────────────────────────────────────────

// playtimeSceneTotal is one row of the most-used list.
type playtimeSceneTotal struct {
	Name    string
	Seconds int64
}

// sortedPlaytimeScenes returns scene totals in descending order. An old name
// folds into the screen that replaced it, so Misc time shows under Tools.
func sortedPlaytimeScenes(data playtimeData) []playtimeSceneTotal {
	merged := make(map[string]int64, len(data.Scenes))
	for name, seconds := range data.Scenes {
		if seconds <= 0 {
			continue
		}
		merged[playtimeCanonicalScene(name)] += seconds
	}
	out := make([]playtimeSceneTotal, 0, len(merged))
	for name, seconds := range merged {
		out = append(out, playtimeSceneTotal{Name: name, Seconds: seconds})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds == out[j].Seconds {
			return out[i].Name < out[j].Name
		}
		return out[i].Seconds > out[j].Seconds
	})
	return out
}

// sortedPlaytimeGames returns the per-game ledger, longest first.
func sortedPlaytimeGames(data playtimeData) []playtimeSceneTotal {
	out := make([]playtimeSceneTotal, 0, len(data.Games))
	for title, seconds := range data.Games {
		if seconds <= 0 {
			continue
		}
		out = append(out, playtimeSceneTotal{Name: title, Seconds: seconds})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds == out[j].Seconds {
			return out[i].Name < out[j].Name
		}
		return out[i].Seconds > out[j].Seconds
	})
	return out
}

func formatPlaytime(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %02dm", h, m)
}

// ── Scene ─────────────────────────────────────────────────────────────────────

// ensurePlaytimeScene adds the trophy room to configs that predate it.
func ensurePlaytimeScene(config *Config) {
	if config == nil || sceneByName(config, playtimeSceneName) != nil {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        playtimeSceneName,
		Icon:        "T",
		Description: "Trophies and playtime",
		Background:  "#0B0F17",
	})
}

// playtimeEntryPages are the config-driven pages the trophy room's button is
// added to, newest first: the Tools page, then the Misc page it replaced. The
// Tools hub is a Go-owned scene that lists the trophy room itself (it calls
// trophyOpenRoom), so a registered page is skipped: a config element appended
// to a scene that draws itself would only be a focus target nobody can see.
var playtimeEntryPages = []string{"Tools", "Misc"}

// ensurePlaytimeEntry adds the trophy room's button to a config-driven Tools
// page. It uses the free slot in the second row so existing buttons keep their
// positions, and it is idempotent.
func ensurePlaytimeEntry(config *Config) {
	if config == nil {
		return
	}
	for _, name := range playtimeEntryPages {
		scene := sceneByName(config, name)
		if scene == nil {
			continue
		}
		if _, owned := lookupScene(name); owned {
			continue
		}
		for _, el := range scene.Elements {
			if el.Trigger == "change_scene:"+playtimeSceneName || strings.EqualFold(el.TriggerTarget, playtimeSceneName) {
				return
			}
		}
		scene.Elements = append(scene.Elements, Element{
			Type:          "button",
			Text:          "Trophies & Playtime",
			X:             336,
			Y:             202,
			Width:         "296",
			Height:        "55",
			Trigger:       "change_scene:" + playtimeSceneName,
			TriggerTarget: playtimeSceneName,
			Color:         "$labelColor",
			BgColor:       "$buttonColor",
		})
		return
	}
}

// trophyOpenRoom opens the trophy room. It is the entry point other screens
// (the Tools hub, a celebration) use; the scene is injected first on a config
// that does not have it yet.
func trophyOpenRoom(config *Config) {
	if config == nil {
		return
	}
	idx := sceneIndexExact(config, playtimeSceneName)
	if idx < 0 {
		ensurePlaytimeScene(config)
		idx = sceneIndexExact(config, playtimeSceneName)
	}
	if idx >= 0 {
		changeSceneTo(config, idx)
	}
}

func init() {
	// The trophy's number is its place in the fixed set. It is assigned here
	// rather than written into 101 literals, and it keeps the number when a
	// sort moves the trophy around the room.
	for i := range playtimeAchievements {
		playtimeAchievements[i].Icon = fmt.Sprintf("%02d", i+1)
		a := playtimeAchievements[i]
		playtimeAchievements[i].Check = func(d playtimeData) bool { return trophyEarnedBy(a, d) }
	}
	if len(playtimeAchievements) != playtimeAchievementCount {
		LogScene("playtime").Error("trophy set does not match its published count",
			"built", len(playtimeAchievements), "declared", playtimeAchievementCount)
	}
	registerScene(playtimeScene{sceneBase{name: playtimeSceneName}})
}
