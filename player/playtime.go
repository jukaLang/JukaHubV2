package main

// Playtime & achievements. Tracking is deliberately cheap: one wall-clock
// sample per frame, integer seconds, and a structured snapshot saved through
// the existing user-config debounce. The screen is a registered scene so it
// owns its own rendering without adding config elements.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
}

type playtimeState struct {
	loaded   bool
	data     playtimeData
	lastTick time.Time
	lastSave time.Time
	carry    float64
}

var playtime playtimeState

func newPlaytimeData() playtimeData {
	return playtimeData{
		Scenes:   map[string]int64{},
		Days:     map[string]int64{},
		Unlocked: map[string]string{},
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
					playtime.data = data
				}
			}
		}
	}
	playtime.loaded = true
	playtime.lastTick = time.Now()
	playtime.lastSave = time.Now()
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
// any freshly unlocked achievement are not lost.
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

func playtimePruneDays() {
	cutoff := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	for day := range playtime.data.Days {
		if day < cutoff {
			delete(playtime.data.Days, day)
		}
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
	playtime.data.Days[playtimeTodayKey()] += seconds
	playtimeCheckAchievements()
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
	now := time.Now()
	if playtime.lastTick.IsZero() {
		playtime.lastTick = now
		return
	}
	delta := now.Sub(playtime.lastTick)
	playtime.lastTick = now
	if delta <= 0 || delta > playtimeMaxFrameDelta {
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

// playtimeAchievement is one badge. The checks read only accumulated data, so a
// badge stays true after a restart and can be recomputed from the snapshot.
type playtimeAchievement struct {
	ID    string
	Title string
	Desc  string
	Icon  string
	Check func(playtimeData) bool
}

// ── Check helpers ─────────────────────────────────────────────────────────────
//
// Every check goes through these, so no badge can panic on a snapshot whose maps
// were never initialised (an old save, or a test fixture).

// sceneCheck builds a check for one screen's accumulated time.
func sceneCheck(scene string, seconds int64) func(playtimeData) bool {
	return func(d playtimeData) bool { return d.Scenes[scene] >= seconds }
}

// totalCheck builds a check for the lifetime total.
func totalCheck(seconds int64) func(playtimeData) bool {
	return func(d playtimeData) bool { return d.Total >= seconds }
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

// playtimeSceneAchievements are the per-screen badges: one table instead of 57
// copies of the same closure. The scene names are the config's own, so a screen
// that is not in a particular config simply never accumulates time.
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
	// Emulation and maintenance
	{"emulator_10m", "Emulator", "Emulator Room", "Spend 10 minutes in Emulators", 10 * 60},
	{"curation_10m", "Curation", "Curator", "Spend 10 minutes in Curation", 10 * 60},
	{"ports_10m", "Ports", "Port Authority", "Spend 10 minutes in Ports", 10 * 60},
	{"saves_10m", "SaveManager", "Save Scummer", "Spend 10 minutes in Save Manager", 10 * 60},
	// Time and focus
	{"pomodoro_30m", "Pomodoro", "Focus Session", "Spend 30 minutes in Pomodoro", 30 * 60},
	{"alarm_5m", "Alarm", "Early Bird", "Spend 5 minutes in Alarm", 5 * 60},
	// The rest of the shell
	{"favorites_10m", "Favorites", "Collector", "Spend 10 minutes in Favorites", 10 * 60},
	{"apps_10m", "Apps", "App Hopper", "Spend 10 minutes in Apps", 10 * 60},
	{"misc_10m", "Misc", "Toolbox", "Spend 10 minutes in Tools", 10 * 60},
	{"jukaland_10m", "JukaLand", "Amusement Park", "Spend 10 minutes in JukaLand", 10 * 60},
	{"dashboard_10m", "Dashboard", "At a Glance", "Spend 10 minutes in Dashboard", 10 * 60},
	{"about_2m", "About", "Curious", "Read the About screen", 2 * 60},
}

// playtimeAchievementCount is what the BADGES read-out and the tests use.
const playtimeAchievementCount = 100

// buildPlaytimeAchievements assembles the badge set: lifetime time, days used,
// streaks, records, screens explored, one table of per-screen badges and finally
// the badges that count the other badges. There are exactly
// playtimeAchievementCount of them; the count is pinned by a test so the read-out
// on screen cannot drift from the list.
func buildPlaytimeAchievements() []playtimeAchievement {
	list := []playtimeAchievement{
		// Lifetime time in the app.
		{"first_steps", "First Steps", "Spend 10 minutes in JukaHub", "", totalCheck(10 * 60)},
		{"one_hour", "One More Level", "Spend 1 hour in JukaHub", "", totalCheck(3600)},
		{"five_hours", "Five Hour Club", "Spend 5 hours in JukaHub", "", totalCheck(5 * 3600)},
		{"ten_hours", "Home Screen Hero", "Spend 10 hours in JukaHub", "", totalCheck(10 * 3600)},
		{"twentyfive_hours", "A Day and a Bit", "Spend 25 hours in JukaHub", "", totalCheck(25 * 3600)},
		{"fifty_hours", "Regular", "Spend 50 hours in JukaHub", "", totalCheck(50 * 3600)},
		{"hundred_hours", "Devoted", "Spend 100 hours in JukaHub", "", totalCheck(100 * 3600)},
		{"two_hundred_hours", "Resident", "Spend 200 hours in JukaHub", "", totalCheck(200 * 3600)},
		{"five_hundred_hours", "Permanent Resident", "Spend 500 hours in JukaHub", "", totalCheck(500 * 3600)},
		{"thousand_hours", "Thousand Hours", "Spend 1000 hours in JukaHub", "", totalCheck(1000 * 3600)},
		// Days used.
		{"three_days", "Three Visits", "Use JukaHub on 3 days", "", func(d playtimeData) bool { return daysUsed(d) >= 3 }},
		{"seven_days", "Week Habit", "Use JukaHub on 7 days", "", func(d playtimeData) bool { return daysUsed(d) >= 7 }},
		{"fortnight", "Fortnight", "Use JukaHub on 14 days", "", func(d playtimeData) bool { return daysUsed(d) >= 14 }},
		{"thirty_days", "Monthly Regular", "Use JukaHub on 30 days", "", func(d playtimeData) bool { return daysUsed(d) >= 30 }},
		{"sixty_days", "Two Months In", "Use JukaHub on 60 days", "", func(d playtimeData) bool { return daysUsed(d) >= 60 }},
		{"ninety_days", "Quarter Year", "Use JukaHub on 90 days", "", func(d playtimeData) bool { return daysUsed(d) >= 90 }},
		// Streaks. The window the tracker keeps is 90 days, so 60 is the ceiling.
		{"streak_three", "Three in a Row", "Use JukaHub 3 days in a row", "", func(d playtimeData) bool { return currentStreak(d, time.Now()) >= 3 }},
		{"streak_week", "Seven Straight", "Use JukaHub 7 days in a row", "", func(d playtimeData) bool { return currentStreak(d, time.Now()) >= 7 }},
		{"streak_fortnight", "Two Weeks Strong", "Use JukaHub 14 days in a row", "", func(d playtimeData) bool { return currentStreak(d, time.Now()) >= 14 }},
		{"streak_month", "Month Streak", "Use JukaHub 30 days in a row", "", func(d playtimeData) bool { return currentStreak(d, time.Now()) >= 30 }},
		{"streak_two_months", "Unbroken", "Use JukaHub 60 days in a row", "", func(d playtimeData) bool { return currentStreak(d, time.Now()) >= 60 }},
		// Records.
		{"best_day_30m", "Half Hour Day", "Spend 30 minutes in a single day", "", func(d playtimeData) bool { return bestDaySeconds(d) >= 30*60 }},
		{"best_day_1h", "Solid Session", "Spend 1 hour in a single day", "", func(d playtimeData) bool { return bestDaySeconds(d) >= 3600 }},
		{"best_day_2h", "Long Session", "Spend 2 hours in a single day", "", func(d playtimeData) bool { return bestDaySeconds(d) >= 2*3600 }},
		{"best_day_4h", "All Nighter", "Spend 4 hours in a single day", "", func(d playtimeData) bool { return bestDaySeconds(d) >= 4*3600 }},
		// The last seven days.
		{"week_5h", "Busy Week", "Spend 5 hours in the last 7 days", "", func(d playtimeData) bool { return playtimeWeekSeconds(d) >= 5*3600 }},
		{"week_10h", "Full Week", "Spend 10 hours in the last 7 days", "", func(d playtimeData) bool { return playtimeWeekSeconds(d) >= 10*3600 }},
		{"week_20h", "Week Off", "Spend 20 hours in the last 7 days", "", func(d playtimeData) bool { return playtimeWeekSeconds(d) >= 20*3600 }},
		// Screens explored.
		{"explorer", "Explorer", "Use 8 different screens", "", func(d playtimeData) bool { return len(d.Scenes) >= 8 }},
		{"explorer_15", "Wanderer", "Use 15 different screens", "", func(d playtimeData) bool { return len(d.Scenes) >= 15 }},
		{"explorer_25", "Cartographer", "Use 25 different screens", "", func(d playtimeData) bool { return len(d.Scenes) >= 25 }},
		{"explorer_35", "Grand Tour", "Use 35 different screens", "", func(d playtimeData) bool { return len(d.Scenes) >= 35 }},
		{"explorer_50", "Every Corner", "Use 50 different screens", "", func(d playtimeData) bool { return len(d.Scenes) >= 50 }},
	}

	for _, s := range playtimeSceneAchievements {
		list = append(list, playtimeAchievement{
			ID:    s.id,
			Title: s.title,
			Desc:  s.desc,
			Check: sceneCheck(s.scene, s.seconds),
		})
	}

	// Badges that count the other badges. They sit last, and the check loop walks
	// the list in order, so each one sees the unlocks before it.
	for _, n := range []int{5, 10, 20, 30, 40, 50, 60, 70, 80, 90} {
		count := n
		list = append(list, playtimeAchievement{
			ID:    fmt.Sprintf("badges_%d", count),
			Title: fmt.Sprintf("%d Badges", count),
			Desc:  fmt.Sprintf("Unlock %d achievements", count),
			Check: func(d playtimeData) bool { return len(d.Unlocked) >= count },
		})
	}
	return list
}

var playtimeAchievements = buildPlaytimeAchievements()

// playtimeAchievementsOrdered puts what the user has already earned first. The
// groups keep their definition order, so a badge never moves between the two
// halves and the numbering on the icon stays put.
func playtimeAchievementsOrdered(data playtimeData) []playtimeAchievement {
	done := make([]playtimeAchievement, 0, len(playtimeAchievements))
	todo := make([]playtimeAchievement, 0, len(playtimeAchievements))
	for _, a := range playtimeAchievements {
		if _, ok := data.Unlocked[a.ID]; ok {
			done = append(done, a)
			continue
		}
		todo = append(todo, a)
	}
	return append(done, todo...)
}

func playtimeCheckAchievements() {
	for _, a := range playtimeAchievements {
		if _, ok := playtime.data.Unlocked[a.ID]; ok {
			continue
		}
		if !a.Check(playtime.data) {
			continue
		}
		playtime.data.Unlocked[a.ID] = time.Now().Format(time.RFC3339)
		ShowToast("Achievement unlocked: "+a.Title, ToastKindSuccess)
		noteAwayAchievement(a.Title)
	}
}

// sortedPlaytimeScenes returns scene totals in descending order.
func sortedPlaytimeScenes(data playtimeData) []struct {
	Name    string
	Seconds int64
} {
	out := make([]struct {
		Name    string
		Seconds int64
	}, 0, len(data.Scenes))
	for name, seconds := range data.Scenes {
		if seconds <= 0 {
			continue
		}
		out = append(out, struct {
			Name    string
			Seconds int64
		}{Name: name, Seconds: seconds})
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

// ensurePlaytimeScene adds the stats screen to configs that predate it.
func ensurePlaytimeScene(config *Config) {
	if config == nil || sceneByName(config, playtimeSceneName) != nil {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        playtimeSceneName,
		Icon:        "T",
		Description: "Playtime and achievements",
		Background:  "#0B0F17",
	})
}

// ensurePlaytimeEntry adds a button to the Tools (Misc) page. It uses the free
// slot in the second row so existing buttons keep their positions.
func ensurePlaytimeEntry(config *Config) {
	scene := sceneByName(config, "Misc")
	if scene == nil {
		return
	}
	for _, el := range scene.Elements {
		if el.Trigger == "change_scene:"+playtimeSceneName || strings.EqualFold(el.TriggerTarget, playtimeSceneName) {
			return
		}
	}
	scene.Elements = append(scene.Elements, Element{
		Type:          "button",
		Text:          "Playtime & Achievements",
		X:             336,
		Y:             202,
		Width:         "296",
		Height:        "55",
		Trigger:       "change_scene:" + playtimeSceneName,
		TriggerTarget: playtimeSceneName,
		Color:         "$labelColor",
		BgColor:       "$buttonColor",
	})
}

// ── Page geometry ─────────────────────────────────────────────────────────────
//
// The page scrolls: the title band is pinned, and everything from
// playtimePageY down moves with playtimeScroll. A hundred badges cannot fit a
// 720px screen, so the whole page (summary, usage, insights, badges) scrolls
// rather than turning the badge card into a second, nested scroller.
const (
	playtimePageY   = int32(64)  // first scrolling row, below the pinned band
	playtimeAchY    = int32(448) // where the badge card starts at scroll 0
	playtimeAchCols = 3
	// playtimeAchRowH is tall enough for an earned badge's three lines (number,
	// title, description, unlock date). At 62 the third line was clipped by the
	// row below it.
	playtimeAchRowH = int32(74)
	// playtimeBadgeLinePitch is the gap between a badge's text lines. It is a
	// constant rather than a font-height fraction because the small role is 30px
	// on the wide canvas and 22px on a Brick, and the row has to hold three
	// lines of either.
	playtimeBadgeLinePitch = int32(20)
	playtimeAchPadTop      = int32(48)
	playtimeAchPadBot      = int32(24)
	playtimeCardX          = int32(28)
	// playtimeScrollStep is one D-pad press. Two badge rows: small enough to
	// stop where you meant to, large enough that 100 badges do not need 40 taps.
	playtimeScrollStep = int32(124)
)

var (
	playtimeRARect sdl.Rect
	// playtimeHeatRect is where this frame drew the insights grid, so a probe
	// can assert against the real geometry instead of a copied layout.
	playtimeHeatRect sdl.Rect
	// playtimeScrollbarRect is this frame's scrollbar thumb; empty when the page
	// fits. A probe reads it to prove the bar tracks the offset.
	playtimeScrollbarRect sdl.Rect

	// playtimeScroll is the page offset in pixels, clamped every frame.
	playtimeScroll int32
	// playtimeScrollFor is the scene the offset belongs to. Entering the screen
	// again starts at the top instead of wherever the last visit stopped.
	playtimeScrollFor = -2
)

// playtimeAchievementGridRows is how many badge rows the full list needs.
func playtimeAchievementGridRows() int32 {
	return (int32(len(playtimeAchievements)) + playtimeAchCols - 1) / playtimeAchCols
}

// playtimeContentHeight is the height of the scrolling page.
func playtimeContentHeight() int32 {
	achH := playtimeAchPadTop + playtimeAchievementGridRows()*playtimeAchRowH + playtimeAchPadBot
	return playtimeAchY + achH + 16 - playtimePageY
}

// playtimeViewportHeight is how much of the page is on screen at once.
func playtimeViewportHeight() int32 {
	if screenHeight <= playtimePageY+8 {
		return 0
	}
	return screenHeight - 8 - playtimePageY
}

// playtimeMaxScroll is the furthest the page can scroll; 0 when it fits.
func playtimeMaxScroll() int32 {
	if extra := playtimeContentHeight() - playtimeViewportHeight(); extra > 0 {
		return extra
	}
	return 0
}

// playtimeScrollBy moves the page, clamped to the real content.
func playtimeScrollBy(delta int32) {
	playtimeScroll += delta
	if max := playtimeMaxScroll(); playtimeScroll > max {
		playtimeScroll = max
	}
	if playtimeScroll < 0 {
		playtimeScroll = 0
	}
}

// playtimeBadgeCell is the rect of badge i in the ordered list, in content
// coordinates (add -scroll for the screen position). Pure, so the layout can be
// tested without a renderer.
func playtimeBadgeCell(i int, cardW, achY int32) sdl.Rect {
	cellW := (cardW - 48) / playtimeAchCols
	col := int32(i) % playtimeAchCols
	row := int32(i) / playtimeAchCols
	return sdl.Rect{
		X: playtimeCardX + 24 + col*cellW,
		Y: achY + playtimeAchPadTop + row*playtimeAchRowH,
		W: cellW,
		H: playtimeAchRowH - 8,
	}
}

// renderPlaytime draws the stats screen. All numbers are formatted, never raw
// seconds, so the handheld's tiny screen stays readable.
func renderPlaytime(renderer *sdl.Renderer, config *Config) {
	playtimeRARect = sdl.Rect{}
	playtimeHeatRect = sdl.Rect{}
	playtimeScrollbarRect = sdl.Rect{}
	renderer.SetDrawColor(9, 12, 20, 255)
	renderer.Clear()
	renderAmbientBackground(renderer)

	font, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	big, _ := getCachedFont(config, "big")
	if font == nil {
		return
	}
	if med == nil {
		med = font
	}
	if big == nil {
		big = med
	}

	// A fresh visit starts at the top.
	if playtimeScrollFor != currentSceneIndex {
		playtimeScrollFor = currentSceneIndex
		playtimeScroll = 0
	}
	maxScroll := playtimeMaxScroll()
	if playtimeScroll > maxScroll {
		playtimeScroll = maxScroll
	}
	if playtimeScroll < 0 {
		playtimeScroll = 0
	}
	scroll := playtimeScroll

	// Pinned band: the title, the way out and the scroll/pairing hints. The page
	// scrolls under it, so none of them can leave the screen.
	renderText(renderer, config, med, "Playtime & Achievements", ColorTextPrimary(), 28, 18)
	back := hintLine(FooterHint{"B", "Back"})
	bw, _, _ := font.SizeUTF8(back)
	renderText(renderer, config, font, back, ColorTextTertiary(), screenWidth-int32(bw)-28, 22)
	x := screenWidth - int32(bw) - 28 - 24
	if maxScroll > 0 {
		scrollHint := hintLine(FooterHint{"D-pad", "Scroll"})
		sw, _, _ := font.SizeUTF8(scrollHint)
		x -= int32(sw)
		renderText(renderer, config, font, scrollHint, ColorTextTertiary(), x, 22)
		x -= 24
	}
	if retroAchievementsSupported() {
		raHint := hintLine(FooterHint{"Y", "RetroAchievements"})
		raw, _, _ := font.SizeUTF8(raHint)
		raHintW := int32(raw)
		x -= raHintW
		playtimeRARect = sdl.Rect{X: x - 8, Y: 16, W: raHintW + 16, H: 30}
		col := ColorTextTertiary()
		if raSettingsReady() {
			col = accentColor
		}
		renderText(renderer, config, font, raHint, col, x, 22)
	}

	cardW := screenWidth - 56
	viewportTop := playtimePageY
	viewportBottom := screenHeight - 8

	// The page body is clipped to the band below the pinned header: scrolling it
	// up must not paint over the title, and a card taller than the screen must
	// not paint into the bottom edge.
	renderWithClip(renderer, 0, viewportTop, screenWidth, viewportBottom-viewportTop, func(_ *sdl.Renderer) {
		at := func(y int32) int32 { return y - scroll }

		// Summary strip.
		summaryY := at(playtimePageY)
		drawCard(renderer, playtimeCardX, summaryY, cardW, 112, 16)
		total := formatPlaytime(playtime.data.Total)
		renderText(renderer, config, font, "TOTAL TIME", ColorTextTertiary(), 52, summaryY+18)
		renderText(renderer, config, big, total, ColorTextPrimary(), 52, summaryY+40)

		week := playtimeWeekSeconds(playtime.data)
		renderText(renderer, config, font, "THIS WEEK", ColorTextTertiary(), 360, summaryY+18)
		renderText(renderer, config, big, formatPlaytime(week), ColorTextPrimary(), 360, summaryY+40)

		unlocked := len(playtime.data.Unlocked)
		renderText(renderer, config, font, "BADGES", ColorTextTertiary(), 680, summaryY+18)
		renderText(renderer, config, big, fmt.Sprintf("%d / %d", unlocked, len(playtimeAchievements)), ColorTextPrimary(), 680, summaryY+40)

		// Top screens.
		rowY := at(194)
		leftW := int32(460)
		drawCard(renderer, playtimeCardX, rowY, leftW, 238, 16)
		renderText(renderer, config, font, "MOST USED", ColorTextTertiary(), 52, rowY+20)
		rows := sortedPlaytimeScenes(playtime.data)
		if len(rows) == 0 {
			empty := "Start exploring — your stats appear here."
			renderText(renderer, config, font, ellipsize(font, empty, leftW-48), ColorTextSecondary(), 52, rowY+60)
		} else {
			max := rows[0].Seconds
			for i := 0; i < len(rows) && i < 5; i++ {
				y := rowY + 54 + int32(i)*34
				name := sceneDisplayName(rows[i].Name)
				renderText(renderer, config, font, ellipsize(font, name, 180), ColorTextSecondary(), 52, y)
				// The bar ends at 394 so the right-aligned value (at the card's
				// inner edge, 464) never lands on top of it. It used to: "6h 40m"
				// was drawn over the accent fill and became unreadable.
				barW := int32(0)
				if max > 0 {
					barW = int32(float64(rows[i].Seconds) / float64(max) * 150)
				}
				fillRoundedRect(renderer, 244, y+5, 150, 8, 4, WithAlpha(ColorBorder, 90))
				if barW > 0 {
					fillRoundedRect(renderer, 244, y+5, barW, 8, 4, accentColor)
				}
				val := formatPlaytime(rows[i].Seconds)
				vw, _, _ := font.SizeUTF8(val)
				renderText(renderer, config, font, val, ColorTextTertiary(), 464-int32(vw), y)
			}
		}

		// Insights: the same Days series the old seven bars used, over the whole
		// 13-week window the pruner keeps, with the streak and best day the bars
		// could not show. See wrapped.go.
		rightX := int32(504)
		rightW := screenWidth - rightX - 28
		drawCard(renderer, rightX, rowY, rightW, 238, 16)
		ins := buildWrappedInsights(playtime.data, time.Now())
		renderText(renderer, config, font, "LAST 13 WEEKS", ColorTextTertiary(), rightX+24, rowY+20)
		// The grid and the stat column share the card. On the wide canvas the
		// grid is 300px (13 columns of ~23px, seven rows of ~22px) because a 12px
		// cell is unreadable at arm's length on the handheld's 4.96" panel, and
		// there is still room for two 190px stat columns beside it. The compact
		// canvas is 320px narrower and only has room for one, so the grid shrinks
		// to 200px and the five stats stack in a single column — the previous
		// fixed offsets put "DAILY AVERAGE" and "ACTIVE DAYS" off the card edge
		// on a Brick.
		gridW := int32(300)
		if rightW < 660 {
			gridW = 200
		}
		heat := sdl.Rect{X: rightX + 24, Y: rowY + 50, W: gridW, H: 155}
		playtimeHeatRect = heat
		drawWrappedHeatmap(renderer, config, heat, ins)

		statsX := heat.X + heat.W + 20
		if rightW < 660 {
			// One column, 38px pitch and the small role for the values: five
			// label/value pairs fit the card's 188px of body without a value
			// touching the label below it (the medium role is 24px tall and needed
			// 46px rows, which is more than the card has).
			const pitch = int32(40)
			labels := []string{"STREAK", "LONGEST", "BEST DAY", "DAILY AVG", "ACTIVE DAYS"}
			values := []string{
				wrappedDaysLabel(ins.CurrentStreak),
				wrappedDaysLabel(ins.LongestStreak),
				wrappedBestDayLabel(ins),
				formatPlaytime(ins.DailyAverage),
				fmt.Sprintf("%d of %d", ins.ActiveDays, wrappedDays),
			}
			for i := range labels {
				off := int32(40 + int(i)*int(pitch))
				renderText(renderer, config, font, labels[i], ColorTextTertiary(), statsX, rowY+off)
				renderText(renderer, config, font, values[i], ColorTextPrimary(), statsX, rowY+off+18)
			}
		} else {
			// The pitch is 64px because the value is drawn at the medium role
			// (24px) with room for an accessibility step before it touches the row
			// below.
			statsColW := int32(190)
			renderText(renderer, config, font, "STREAK", ColorTextTertiary(), statsX, rowY+46)
			renderText(renderer, config, med, wrappedDaysLabel(ins.CurrentStreak), ColorTextPrimary(), statsX, rowY+64)
			renderText(renderer, config, font, "LONGEST", ColorTextTertiary(), statsX+statsColW, rowY+46)
			renderText(renderer, config, med, wrappedDaysLabel(ins.LongestStreak), ColorTextPrimary(), statsX+statsColW, rowY+64)
			renderText(renderer, config, font, "BEST DAY", ColorTextTertiary(), statsX, rowY+110)
			renderText(renderer, config, med, wrappedBestDayLabel(ins), ColorTextPrimary(), statsX, rowY+128)
			renderText(renderer, config, font, "DAILY AVERAGE", ColorTextTertiary(), statsX+statsColW, rowY+110)
			renderText(renderer, config, med, formatPlaytime(ins.DailyAverage), ColorTextPrimary(), statsX+statsColW, rowY+128)
			renderText(renderer, config, font, "ACTIVE DAYS", ColorTextTertiary(), statsX, rowY+174)
			renderText(renderer, config, med, fmt.Sprintf("%d of %d", ins.ActiveDays, wrappedDays), ColorTextPrimary(), statsX, rowY+192)
		}

		// Achievements, earned first. The card is as tall as the badge grid needs,
		// so the page scrolls instead of hiding badges behind a break.
		achY := at(playtimeAchY)
		achH := playtimeAchPadTop + playtimeAchievementGridRows()*playtimeAchRowH + playtimeAchPadBot
		drawCard(renderer, playtimeCardX, achY, cardW, achH, 16)
		renderText(renderer, config, font, fmt.Sprintf("ACHIEVEMENTS  %d / %d", unlocked, len(playtimeAchievements)), ColorTextTertiary(), 52, achY+18)
		if unlocked > 0 {
			tag := "Achieved first"
			tw, _, _ := font.SizeUTF8(tag)
			renderText(renderer, config, font, tag, WithAlpha(accentColor, 210), playtimeCardX+cardW-24-int32(tw), achY+18)
		}

		ordered := playtimeAchievementsOrdered(playtime.data)
		for i, a := range ordered {
			cell := playtimeBadgeCell(i, cardW, achY)
			if cell.Y+cell.H < viewportTop || cell.Y > viewportBottom {
				continue
			}
			_, done := playtime.data.Unlocked[a.ID]
			iconCol := WithAlpha(ColorTextTertiary(), 150)
			titleCol := ColorTextTertiary()
			if done {
				iconCol = accentColor
				titleCol = ColorTextPrimary()
			}
			fillRoundedRect(renderer, cell.X, cell.Y, 38, 38, 10, WithAlpha(iconCol, 30))
			iw, ih, _ := font.SizeUTF8(a.Icon)
			renderText(renderer, config, font, a.Icon, iconCol, cell.X+(38-int32(iw))/2, cell.Y+(38-int32(ih))/2)
			textX := cell.X + 48
			textW := cell.W - 58
			renderText(renderer, config, font, ellipsize(font, a.Title, textW), titleCol, textX, cell.Y+2)
			renderText(renderer, config, font, ellipsize(font, a.Desc, textW), ColorTextTertiary(), textX, cell.Y+2+playtimeBadgeLinePitch)
			// Earned badges say when: the unlock time is already saved, and "when
			// did I get this?" is the question the list raises.
			if done {
				if label := playtimeUnlockLabel(playtime.data.Unlocked[a.ID]); label != "" {
					renderText(renderer, config, font, ellipsize(font, label, textW),
						WithAlpha(accentColor, 220), textX, cell.Y+2+2*playtimeBadgeLinePitch)
				}
			}
		}
	})

	// Scrollbar: the whole page's position, drawn over the clip so it stays at
	// the screen's edge while the cards move.
	if maxScroll > 0 && viewportBottom-viewportTop > 0 {
		barX := screenWidth - 10
		barY := viewportTop + 4
		barH := viewportBottom - viewportTop - 8
		fillRoundedRect(renderer, barX, barY, 4, barH, 2, WithAlpha(ColorBorder, 70))
		visible := int64(viewportBottom - viewportTop)
		total := visible + int64(maxScroll)
		thumbH := int32(visible * int64(barH) / total)
		if thumbH < 32 {
			thumbH = 32
		}
		if thumbH > barH {
			thumbH = barH
		}
		thumbY := barY + int32(int64(barH-thumbH)*int64(scroll)/int64(maxScroll))
		fillRoundedRect(renderer, barX, thumbY, 4, thumbH, 2, accentColor)
		playtimeScrollbarRect = sdl.Rect{X: barX, Y: thumbY, W: 4, H: thumbH}
	}
}

// playtimeUnlockLabel formats a badge's saved unlock time for its card row.
// The timestamp is the RFC3339 string playtimeCheckAchievements wrote; a save
// from before dates were kept, or one that cannot be read back, gets no label
// rather than a broken one.
func playtimeUnlockLabel(stamp string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp))
	if err != nil {
		return ""
	}
	return "Unlocked " + t.Format("2 Jan 2006")
}

func playtimeWeekSeconds(data playtimeData) int64 {
	var total int64
	for i := 0; i < 7; i++ {
		total += data.Days[time.Now().AddDate(0, 0, -i).Format("2006-01-02")]
	}
	return total
}

// Scene registration: the Playtime screen has no config elements, so it owns
// rendering and B/Escape navigation.
type playtimeScene struct{ sceneBase }

func (playtimeScene) render(r *sdl.Renderer, c *Config) bool {
	renderPlaytime(r, c)
	return true
}

func (playtimeScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		goBackScene(c)
		PlayBackSound()
	case sdl.K_y:
		openRetroAchievementsPanel()
	case sdl.K_UP:
		playtimeScrollBy(-playtimeScrollStep)
		PlayNavSound()
	case sdl.K_DOWN:
		playtimeScrollBy(playtimeScrollStep)
		PlayNavSound()
	case sdl.K_PAGEUP:
		playtimeScrollBy(-playtimeViewportHeight())
		PlayNavSound()
	case sdl.K_PAGEDOWN:
		playtimeScrollBy(playtimeViewportHeight())
		PlayNavSound()
	case sdl.K_HOME:
		playtimeScroll = 0
		PlayNavSound()
	case sdl.K_END:
		playtimeScroll = playtimeMaxScroll()
		PlayNavSound()
	default:
		return false
	}
	return true
}

func (playtimeScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(c)
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_Y:
		openRetroAchievementsPanel()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		playtimeScrollBy(-playtimeScrollStep)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		playtimeScrollBy(playtimeScrollStep)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		playtimeScrollBy(-playtimeViewportHeight())
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		playtimeScrollBy(playtimeViewportHeight())
		PlayNavSound()
	default:
		return false
	}
	return true
}

// handlePlaytimeWheel scrolls the page from a mouse wheel or a touchpad drag.
// Up is one step back, the same as the D-pad.
func handlePlaytimeWheel(y int32) bool {
	if y == 0 {
		return false
	}
	playtimeScrollBy(-y * 48)
	return true
}

// handlePlaytimeMouse opens RetroAchievements from the touch target in the
// header. The scene has no config elements, so main.go forwards the click here.
func handlePlaytimeMouse(mx, my int32) bool {
	if playtimeRARect.W > 0 && pointInRect(mx, my, playtimeRARect) {
		openRetroAchievementsPanel()
		return true
	}
	return false
}

func init() {
	// The badge icon is the badge's number in the fixed set. It is assigned here
	// rather than written into 100 literals, and it keeps the number when the
	// earned-first ordering moves the badge up the page.
	for i := range playtimeAchievements {
		playtimeAchievements[i].Icon = fmt.Sprintf("%02d", i+1)
	}
	if len(playtimeAchievements) != playtimeAchievementCount {
		LogScene("playtime").Error("badge set does not match its published count",
			"built", len(playtimeAchievements), "declared", playtimeAchievementCount)
	}
	registerScene(playtimeScene{sceneBase{name: playtimeSceneName}})
}
