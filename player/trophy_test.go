package main

// trophy_test.go — the trophy rules as plain values: tiers, the unlock pass and
// its announcements, the platinum and availability, the celebration queue, the
// RetroAchievements hook, the chime, the confetti pool and the game-session
// credit. The pixels are in trophy_render_test.go.

import (
	"math"
	"strings"
	"testing"
	"time"
)

// resetTrophyQueue empties the celebration queue and the screen around a test.
func resetTrophyQueue(t *testing.T) {
	t.Helper()
	trophyResetForTest()
	t.Cleanup(trophyResetForTest)
}

// withTrophyData installs a snapshot (and an availability that offers every
// trophy) and restores the previous state afterwards.
func withTrophyData(t *testing.T, data playtimeData) {
	t.Helper()
	prev := playtime
	t.Cleanup(func() {
		playtime = prev
		playtime.rev++
		trophyAvail.known = false
		trophyAvail.cfg = nil
	})
	playtime = playtimeState{loaded: true, data: data}
	trophyAvail.known = false
	trophyAvail.cfg = nil
}

// keepNotifHistory restores the notification centre's history after a test
// that files trophies in it.
func keepNotifHistory(t *testing.T) {
	t.Helper()
	saved := notifHistorySnapshot()
	t.Cleanup(func() {
		notifHistoryMu.Lock()
		nc.history = saved
		notifHistoryMu.Unlock()
	})
}

// trophyByID returns a trophy of the set by id, failing the test if it is not one.
func trophyByID(t *testing.T, id string) playtimeAchievement {
	t.Helper()
	i := trophyIndexByID(id)
	if i < 0 {
		t.Fatalf("no trophy %q in the set", id)
	}
	return playtimeAchievements[i]
}

func TestTrophyTiersFollowEffort(t *testing.T) {
	cases := []struct {
		id   string
		tier trophyTier
	}{
		{"first_steps", trophyBronze},
		{"one_hour", trophyBronze},
		{"five_hours", trophySilver},
		{"twentyfive_hours", trophyGold},
		{"seven_days", trophyBronze},
		{"thirty_days", trophySilver},
		{"streak_three", trophyBronze},
		{"streak_month", trophyGold},
		{"best_day_2h", trophySilver},
		{"week_20h", trophyGold},
		{"explorer", trophyBronze},
		{"explorer_35", trophyGold},
		{"gamer", trophyBronze},
		{"games_1h", trophySilver},
		{"games_25h", trophyGold},
		{"misc_10m", trophyBronze},
		{"badges_5", trophyBronze},
		{"badges_30", trophySilver},
		{"badges_90", trophyGold},
		{trophyPlatinumID, trophyPlatinum},
	}
	for _, c := range cases {
		if got := trophyByID(t, c.id).Tier; got != c.tier {
			t.Errorf("%s is %s, want %s", c.id, trophyTierName(got), trophyTierName(c.tier))
		}
	}

	// Within one ladder (same metric, same screen) a bigger goal never earns a
	// lesser tier.
	type ladder struct {
		metric trophyMetric
		scene  string
	}
	best := map[ladder]playtimeAchievement{}
	for _, a := range playtimeAchievements {
		key := ladder{a.Metric, a.Scene}
		prev, seen := best[key]
		if seen && a.Goal > prev.Goal && a.Tier < prev.Tier {
			t.Errorf("%s (goal %d) is %s but the easier %s (goal %d) is %s",
				a.ID, a.Goal, trophyTierName(a.Tier), prev.ID, prev.Goal, trophyTierName(prev.Tier))
		}
		if !seen || a.Goal > prev.Goal {
			best[key] = a
		}
	}

	if !(trophyTierPoints(trophyBronze) < trophyTierPoints(trophySilver) &&
		trophyTierPoints(trophySilver) < trophyTierPoints(trophyGold) &&
		trophyTierPoints(trophyGold) < trophyTierPoints(trophyPlatinum)) {
		t.Fatal("tier points must rise with the tier")
	}
	for _, id := range []string{"thousand_hours", "streak_two_months", "about_2m"} {
		if !trophyByID(t, id).Hidden {
			t.Errorf("%s should be a hidden trophy", id)
		}
	}
	if trophyByID(t, "first_steps").Hidden {
		t.Error("first_steps must not be hidden")
	}
}

func TestTrophyLevelFromPoints(t *testing.T) {
	cases := []struct {
		points int
		level  int
		frac   float64
	}{
		{0, 1, 0},
		{15, 1, 0.15},
		{99, 1, 0.99},
		{100, 2, 0},
		{345, 4, 0.45},
		{-5, 1, 0},
	}
	for _, c := range cases {
		level, frac := trophyLevel(c.points)
		if level != c.level || math.Abs(frac-c.frac) > 1e-9 {
			t.Errorf("trophyLevel(%d) = %d, %.2f; want %d, %.2f", c.points, level, frac, c.level, c.frac)
		}
	}
}

// TestToolsTrophyCountsLegacyMiscTime: the trophy that measured the old Misc
// page now measures the Tools hub, keeps its id, and still counts the minutes
// recorded under Misc.
func TestToolsTrophyCountsLegacyMiscTime(t *testing.T) {
	a := trophyByID(t, "misc_10m")
	if a.Scene != "Tools" || a.Metric != trophyMetricScene {
		t.Fatalf("misc_10m measures %q, want the Tools screen", a.Scene)
	}
	data := newPlaytimeData()
	data.Scenes["Misc"] = 400
	if a.Check(data) {
		t.Fatal("400 seconds unlocked a 10-minute trophy")
	}
	data.Scenes["Tools"] = 200
	if !a.Check(data) {
		t.Fatal("Misc and Tools time together did not unlock the Tools trophy")
	}
	rows := sortedPlaytimeScenes(data)
	if len(rows) != 1 || rows[0].Name != "Tools" || rows[0].Seconds != 600 {
		t.Fatalf("the most-used list did not fold Misc into Tools: %+v", rows)
	}
}

// TestUnlockPassAnnouncesEachTrophy: crossing a threshold awards the trophy
// once, with its date, queues its celebration and files it in the bell.
func TestUnlockPassAnnouncesEachTrophy(t *testing.T) {
	resetTrophyQueue(t)
	keepNotifHistory(t)
	data := newPlaytimeData()
	withTrophyData(t, data)

	playtimeRecord(590, "Home")
	if _, ok := playtime.data.Unlocked["first_steps"]; ok {
		t.Fatal("first_steps unlocked ten seconds early")
	}
	if trophyQueued() != 0 {
		t.Fatal("a celebration was queued before any trophy was earned")
	}
	playtimeRecord(10, "Home")
	stamp, ok := playtime.data.Unlocked["first_steps"]
	if !ok {
		t.Fatal("first_steps did not unlock at 10 minutes")
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Fatalf("the unlock date %q is not RFC3339", stamp)
	}
	card, ok := trophyDequeue()
	if !ok || card.title != "First Steps" || card.tier != trophyBronze || card.id != "first_steps" {
		t.Fatalf("the queued celebration is %+v", card)
	}
	if !strings.Contains(card.detail, "Bronze") {
		t.Fatalf("the card's tier line is %q", card.detail)
	}
	filed := false
	for _, n := range notifHistorySnapshot() {
		if strings.Contains(n.Message, "First Steps") {
			filed = true
		}
	}
	if !filed {
		t.Fatal("the trophy was not filed in the notification centre")
	}

	// A second pass awards nothing new.
	playtimeRecord(1, "Home")
	if trophyQueued() != 0 {
		t.Fatal("an earned trophy was announced twice")
	}
}

// TestCollectionAndPlatinumUnlockInOnePass: the last missing trophy unlocks the
// collection trophy that was waiting for it and then the platinum, in order.
func TestCollectionAndPlatinumUnlockInOnePass(t *testing.T) {
	resetTrophyQueue(t)
	keepNotifHistory(t)
	data := newPlaytimeData()
	for _, a := range playtimeAchievements {
		if a.ID == "badges_90" || a.Metric == trophyMetricPlatinum {
			continue
		}
		data.Unlocked[a.ID] = "2026-01-01T10:00:00Z"
	}
	withTrophyData(t, data)

	platinum := trophyByID(t, trophyPlatinumID)
	if platinum.Check(playtime.data) {
		t.Fatal("the platinum unlocked with a trophy still missing")
	}
	playtimeCheckAchievements()
	if _, ok := playtime.data.Unlocked["badges_90"]; !ok {
		t.Fatal("90 Trophies did not unlock with 99 earned")
	}
	if _, ok := playtime.data.Unlocked[trophyPlatinumID]; !ok {
		t.Fatal("the platinum did not unlock once everything else was earned")
	}
	first, _ := trophyDequeue()
	second, _ := trophyDequeue()
	if first.id != "badges_90" || second.id != trophyPlatinumID || second.tier != trophyPlatinum {
		t.Fatalf("celebrations came as %q then %q, want badges_90 then the platinum", first.id, second.id)
	}
	if !strings.Contains(second.caption, "PLATINUM") {
		t.Fatalf("the platinum's caption is %q", second.caption)
	}
}

// TestPlatinumIgnoresTrophiesThisSetupCannotAward: a screen the config does not
// have cannot hold the platinum hostage.
func TestPlatinumIgnoresTrophiesThisSetupCannotAward(t *testing.T) {
	data := newPlaytimeData()
	withTrophyData(t, data)

	config := &Config{}
	for _, a := range playtimeAchievements {
		if a.Metric == trophyMetricScene && a.Scene != "Calculator" {
			config.Scenes = append(config.Scenes, SceneConfig{Name: a.Scene})
		}
	}
	for i := 0; i < 60; i++ {
		config.Scenes = append(config.Scenes, SceneConfig{Name: "Extra" + string(rune('A'+i%26)) + string(rune('a'+i/26))})
	}
	trophySyncAvailability(config)
	calc := trophyIndexByID("calc_5m")
	if trophyAvailable(calc) {
		t.Fatal("the Calculator trophy is offered on a config without a Calculator")
	}
	for _, a := range playtimeAchievements {
		if a.ID == "calc_5m" || a.Metric == trophyMetricPlatinum {
			continue
		}
		playtime.data.Unlocked[a.ID] = "2026-01-01T10:00:00Z"
	}
	got, need := trophyPlatinumProgress(playtime.data)
	if got != need || need == 0 {
		t.Fatalf("platinum progress %d/%d with every awardable trophy earned", got, need)
	}
	if !trophyByID(t, trophyPlatinumID).Check(playtime.data) {
		t.Fatal("the platinum waits for a trophy this setup cannot award")
	}
	// An earned trophy stays in the room even when its screen is gone.
	playtime.data.Unlocked["calc_5m"] = "2026-01-01T10:00:00Z"
	if !trophyShown(calc, playtime.data) {
		t.Fatal("an earned trophy disappeared from the room with its screen")
	}
}

// TestAvailabilityFollowsTheConfig: screens, panels and the collection ladder.
func TestAvailabilityFollowsTheConfig(t *testing.T) {
	withTrophyData(t, newPlaytimeData())
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Games"}, {Name: "Misc"}, {Name: "Exit"}}}
	trophySyncAvailability(config)
	check := func(id string, want bool) {
		t.Helper()
		if got := trophyAvailable(trophyIndexByID(id)); got != want {
			t.Errorf("%s available = %v, want %v", id, got, want)
		}
	}
	check("gamer", true)
	check("calc_5m", false)
	check("misc_10m", true)  // the old Misc page still counts for Tools
	check("saves_10m", true) // a panel, always there
	check("ports_10m", !IsWindows())
	check("explorer", false) // eight screens on a config that has five
	check("first_steps", true)
	check("badges_90", false) // there are not 90 other trophies to earn here
	check(trophyPlatinumID, true)

	// A hot reload hands over a new config; the answer follows it.
	gen := trophyAvail.gen
	config2 := &Config{Scenes: []SceneConfig{{Name: "Calculator"}}}
	trophySyncAvailability(config2)
	if trophyAvail.gen == gen {
		t.Fatal("a new config did not recompute availability")
	}
	check("calc_5m", true)
}

// TestTrophyQueueFoldsAndKeepsPlatinumLast: a burst keeps four cards, folds the
// rest into one, and the platinum still comes last.
func TestTrophyQueueFoldsAndKeepsPlatinumLast(t *testing.T) {
	resetTrophyQueue(t)
	for i := 0; i < 7; i++ {
		tier := trophyBronze
		if i == 5 {
			tier = trophyGold
		}
		trophyEnqueue(trophyToast{title: "T" + string(rune('0'+i)), tier: tier})
	}
	trophyEnqueue(trophyToast{title: "Platinum", tier: trophyPlatinum})
	if got := trophyQueued(); got != trophyQueueCap+2 {
		t.Fatalf("queued %d cards, want %d singles, the summary and the platinum", got, trophyQueueCap)
	}
	for i := 0; i < trophyQueueCap; i++ {
		card, ok := trophyDequeue()
		if !ok || card.more != 0 || card.title != "T"+string(rune('0'+i)) {
			t.Fatalf("card %d is %+v", i, card)
		}
	}
	summary, ok := trophyDequeue()
	if !ok || summary.more != 3 || summary.tier != trophyGold || !strings.Contains(summary.title, "3 more trophies") {
		t.Fatalf("the summary card is %+v", summary)
	}
	last, ok := trophyDequeue()
	if !ok || last.tier != trophyPlatinum {
		t.Fatalf("the last card is %+v, want the platinum", last)
	}
	if _, ok := trophyDequeue(); ok {
		t.Fatal("the queue did not empty")
	}
}

func TestRetroAchievementsNewUnlocks(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return base.Add(d).Format("2006-01-02 15:04:05") }
	prev := raCache{Username: "Juka", UpdatedAt: base.Unix(), Recent: []raAchievement{
		{AchievementID: 1, Title: "Old", Date: stamp(-time.Hour)},
	}}
	next := raCache{Username: "juka", UpdatedAt: base.Add(time.Hour).Unix(), Recent: []raAchievement{
		{AchievementID: 3, Title: "Newest", Date: stamp(40 * time.Minute), Points: 25, GameTitle: "Super Metroid"},
		{AchievementID: 4, Title: "New", Date: stamp(20 * time.Minute), Points: 5},
		{AchievementID: 2, Title: "Stale", Date: stamp(-30 * time.Minute)},
		{AchievementID: 1, Title: "Old", Date: stamp(-time.Hour)},
	}}
	got := trophyRANewUnlocks(prev, next)
	if len(got) != 2 || got[0].AchievementID != 4 || got[1].AchievementID != 3 {
		t.Fatalf("new unlocks = %+v, want New then Newest", got)
	}
	if trophyRANewUnlocks(raCache{}, next) != nil {
		t.Fatal("the first sync of an account celebrated its history")
	}
	other := prev
	other.Username = "SomeoneElse"
	if trophyRANewUnlocks(other, next) != nil {
		t.Fatal("a different account's sync celebrated")
	}

	for _, c := range []struct {
		points int
		tier   trophyTier
	}{{1, trophyBronze}, {5, trophyBronze}, {10, trophySilver}, {25, trophyGold}, {100, trophyGold}} {
		if got := trophyRATier(c.points); got != c.tier {
			t.Errorf("%d points is %s, want %s", c.points, trophyTierName(got), trophyTierName(c.tier))
		}
	}

	resetTrophyQueue(t)
	trophyCelebrateRA("Newest", "Super Metroid", 25)
	card, ok := trophyDequeue()
	if !ok || card.tier != trophyGold || card.caption != "RETROACHIEVEMENTS" || card.detail != "Super Metroid · 25 pts" {
		t.Fatalf("the RetroAchievements card is %+v", card)
	}
}

func TestTrophyChimeIsShortSoftAndClickFree(t *testing.T) {
	const rate = int32(22050)
	prevLen := 0
	for tier := trophyBronze; tier <= trophyPlatinum; tier++ {
		pcm := trophyChimePCM(rate, tier)
		_, length := trophyChimeScore(tier)
		if want := int(float64(rate)*length) * 2; len(pcm) != want {
			t.Fatalf("%s chime is %d bytes, want %d", trophyTierName(tier), len(pcm), want)
		}
		if len(pcm) < prevLen {
			t.Errorf("%s chime is shorter than the tier below it", trophyTierName(tier))
		}
		prevLen = len(pcm)
		peak := 0
		for i := 0; i+1 < len(pcm); i += 2 {
			s := int(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
			if s < 0 {
				s = -s
			}
			if s > peak {
				peak = s
			}
		}
		if limit := int(math.Floor(trophyChimePeak*32767)) + 2; peak > limit {
			t.Errorf("%s chime peaks at %d, over the %d ceiling", trophyTierName(tier), peak, limit)
		}
		if peak < int(math.Floor(0.2*32767)) {
			t.Errorf("%s chime peaks at only %d: it would be inaudible", trophyTierName(tier), peak)
		}
		first := int(int16(uint16(pcm[0]) | uint16(pcm[1])<<8))
		last := int(int16(uint16(pcm[len(pcm)-2]) | uint16(pcm[len(pcm)-1])<<8))
		if first > 300 || first < -300 || last > 300 || last < -300 {
			t.Errorf("%s chime starts at %d and ends at %d: it would click", trophyTierName(tier), first, last)
		}
	}
	if _, plat := trophyChimeScore(trophyPlatinum); plat < 2 {
		t.Fatal("the platinum's fanfare should run about two seconds")
	}
	if trophyChimePCM(0, trophyGold) != nil {
		t.Fatal("a zero sample rate must produce no sound")
	}

	// The UI sound volume scales the chime on the UI sounds' own curve.
	pcm := trophyChimePCM(rate, trophyGold)
	if got := trophyChimeScaled(pcm, 100); &got[0] != &pcm[0] {
		t.Fatal("full volume must play the cached chime itself, not a copy")
	}
	if trophyChimeScaled(pcm, 0) != nil {
		t.Fatal("volume 0 must play nothing")
	}
	half := trophyChimeScaled(pcm, 50)
	peakOf := func(b []byte) int {
		peak := 0
		for i := 0; i+1 < len(b); i += 2 {
			s := int(int16(uint16(b[i]) | uint16(b[i+1])<<8))
			if s < 0 {
				s = -s
			}
			if s > peak {
				peak = s
			}
		}
		return peak
	}
	if full, quarter := peakOf(pcm), peakOf(half); quarter < full/4-2 || quarter > full/4+2 {
		t.Fatalf("half volume peaks at %d of %d, want a quarter", quarter, full)
	}
}

// TestConfettiPoolIsBoundedAndAllocationFree: a burst never exceeds the pool,
// a frame of physics allocates nothing, and the paper is gone a few seconds on.
func TestConfettiPoolIsBoundedAndAllocationFree(t *testing.T) {
	withScreen(t, probeW, probeH)
	trophyConfettiClear()
	t.Cleanup(trophyConfettiClear)
	trophyConfetti.seed = 12345

	trophyConfettiBurst(1100, 120, 400, trophyGold)
	if got := trophyConfettiAlive(); got != trophyConfettiMax {
		t.Fatalf("a 400-piece burst left %d pieces alive, want the pool's %d", got, trophyConfettiMax)
	}
	trophyConfettiRain(50, trophyGold)
	if got := trophyConfettiAlive(); got != trophyConfettiMax {
		t.Fatalf("rain over a full pool left %d pieces, want %d", got, trophyConfettiMax)
	}
	if allocs := testing.AllocsPerRun(50, func() { trophyConfettiUpdate(1.0 / 30) }); allocs != 0 {
		t.Fatalf("a physics step allocated %.1f times", allocs)
	}
	for i := 0; i < 30*6; i++ {
		trophyConfettiUpdate(1.0 / 30)
	}
	if got := trophyConfettiAlive(); got != 0 {
		t.Fatalf("%d pieces were still alive six seconds later", got)
	}
	// Moving forward: a burst from the top right travels left and falls.
	trophyConfettiBurst(1100, 120, 20, trophyBronze)
	startX := float32(0)
	for i := range trophyConfetti.pieces {
		if trophyConfetti.pieces[i].alive {
			startX += trophyConfetti.pieces[i].x
		}
	}
	for i := 0; i < 15; i++ {
		trophyConfettiUpdate(1.0 / 30)
	}
	endX := float32(0)
	for i := range trophyConfetti.pieces {
		if trophyConfetti.pieces[i].alive {
			endX += trophyConfetti.pieces[i].x
		}
	}
	if endX >= startX {
		t.Fatal("the burst did not fan out towards the screen's middle")
	}
}

func TestGameSessionCredit(t *testing.T) {
	gapStart := time.Date(2026, 11, 2, 20, 0, 0, 0, time.Local)
	game := func(title, path string, launched time.Time) []recentGame {
		return []recentGame{{Title: title, Path: path, PlayedAt: launched}}
	}
	cases := []struct {
		name  string
		games []recentGame
		gap   time.Duration
		title string
		secs  int64
		ok    bool
	}{
		{"launched as the gap began", game("Super Metroid", "/roms/snes/sm.sfc", gapStart.Add(-time.Second)), 2 * time.Hour, "Super Metroid", 7200, true},
		{"launched a while before", game("Super Metroid", "", gapStart.Add(-time.Minute)), time.Hour, "", 0, false},
		{"too short to be a session", game("Tetris", "", gapStart), 10 * time.Second, "", 0, false},
		{"capped overnight", game("Zelda", "", gapStart), 15 * time.Hour, "Zelda", int64(playtimeGameMaxSession / time.Second), true},
		{"title from the file name", game("", "/mnt/SDCARD/Roms/SFC/Chrono Trigger.sfc", gapStart), time.Hour, "Chrono Trigger", 3600, true},
		{"no game recorded", nil, time.Hour, "", 0, false},
	}
	for _, c := range cases {
		title, secs, ok := playtimeGameCredit(c.games, gapStart, gapStart.Add(c.gap))
		if ok != c.ok || title != c.title || secs != c.secs {
			t.Errorf("%s: got (%q, %d, %v), want (%q, %d, %v)", c.name, title, secs, ok, c.title, c.secs, c.ok)
		}
	}
}

func TestCelebrationStyleSetting(t *testing.T) {
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = false
	t.Cleanup(func() { userConfigWritesEnabled = prevWrites })

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if trophyCelebrationMode(config) != "auto" {
		t.Fatal("the default style is not auto")
	}
	config.Variables.ReducedMotion = false
	if !trophyCelebrationFull(config) {
		t.Fatal("auto without reduced motion must celebrate in full")
	}
	config.Variables.ReducedMotion = true
	if trophyCelebrationFull(config) {
		t.Fatal("auto must respect reduced motion")
	}
	if got := trophyCycleCelebrationMode(config); got != "full" || !trophyCelebrationFull(config) {
		t.Fatalf("cycling from auto gave %q; full must override reduced motion", got)
	}
	if got := trophyCycleCelebrationMode(config); got != "calm" || trophyCelebrationFull(config) {
		t.Fatalf("cycling from full gave %q; calm must never throw confetti", got)
	}
	if got := trophyCycleCelebrationMode(config); got != "auto" {
		t.Fatalf("cycling from calm gave %q, want auto", got)
	}
	config.Variables.Custom[trophyCelebrationVar] = "nonsense"
	if trophyCelebrationMode(config) != "auto" {
		t.Fatal("an unknown saved style must read as auto")
	}
}

// TestCardMotionLandsAndLeaves: the full card slides in from off screen, rests,
// and slides out fading; the calm card only fades, where it stands.
func TestCardMotionLandsAndLeaves(t *testing.T) {
	const dur = uint64(trophyCardMS)
	off, alpha := trophyCardMotion(true, 0, dur)
	if off < 0.99 || alpha != 1 {
		t.Fatalf("the card starts at offset %.2f, alpha %.2f; want off screen", off, alpha)
	}
	if off, _ = trophyCardMotion(true, trophyEnterMS, dur); math.Abs(off) > 0.01 {
		t.Fatalf("the card has not landed after its entrance (offset %.2f)", off)
	}
	if off, alpha = trophyCardMotion(true, dur/2, dur); off != 0 || alpha != 1 {
		t.Fatalf("the resting card is at %.2f, alpha %.2f", off, alpha)
	}
	if off, alpha = trophyCardMotion(true, dur-10, dur); off < 0.8 || alpha > 0.2 {
		t.Fatalf("the leaving card is at %.2f, alpha %.2f", off, alpha)
	}
	for _, el := range []uint64{0, 100, dur / 2, dur - 10} {
		if off, _ := trophyCardMotion(false, el, dur); off != 0 {
			t.Fatalf("the calm card moved (offset %.2f at %dms)", off, el)
		}
	}
	if _, alpha = trophyCardMotion(false, 0, dur); alpha != 0 {
		t.Fatal("the calm card does not fade in")
	}
}

// TestOverlayWaitsForTheScreen: nothing starts while a game owns the
// framebuffer; the queue keeps the trophy for when the user is back.
func TestOverlayWaitsForTheScreen(t *testing.T) {
	withScreenChild(t, true)
	if trophyOverlayReady() {
		t.Fatal("the overlay would start a card over a running game")
	}
	noteGameChildExited(nil)
	if alarmFiringActive() || screensaverActiveNow() || windowMinimised() {
		t.Skip("another test left the alarm, the screensaver or a minimised window up")
	}
	if !trophyOverlayReady() {
		t.Fatal("the overlay stays blocked with JukaHub on screen")
	}
}

// TestPlaytimeEntryTargetsTheToolsPage: the trophy room's button goes on a
// config-driven Tools page (or the legacy Misc one), once, and never onto a
// page that draws itself.
func TestPlaytimeEntryTargetsTheToolsPage(t *testing.T) {
	if _, owned := lookupScene("Misc"); owned {
		t.Skip("Misc is a registered module in this build")
	}
	cfg := &Config{Scenes: []SceneConfig{{Name: "Misc", Elements: []Element{{Type: "button", Text: "Terminal"}}}}}
	ensurePlaytimeEntry(cfg)
	ensurePlaytimeEntry(cfg)
	count := 0
	for _, el := range cfg.Scenes[0].Elements {
		if el.TriggerTarget == playtimeSceneName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the trophy room's button was added %d times, want once", count)
	}

	// A self-drawing page gets no phantom element.
	const owned = "TrophyProbeOwnedPage"
	sceneRegistry[owned] = playtimeScene{sceneBase{name: owned}}
	t.Cleanup(func() { delete(sceneRegistry, owned) })
	prevPages := playtimeEntryPages
	playtimeEntryPages = []string{owned}
	t.Cleanup(func() { playtimeEntryPages = prevPages })
	cfg2 := &Config{Scenes: []SceneConfig{{Name: owned}}}
	ensurePlaytimeEntry(cfg2)
	if len(cfg2.Scenes[0].Elements) != 0 {
		t.Fatal("a button was appended to a scene that draws itself")
	}
}
