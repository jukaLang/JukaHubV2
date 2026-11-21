package main

// playtime_periods.go — game time by day, so the Playtime screen can say how
// long each game (or console) was played today, this week, this month and this
// year (playtime_periods_ui.go draws it as the By game tab).
//
// The lifetime ledger (playtimeData.Games) has one figure per game and no
// dates, and the 90-day Days window has dates but no games. This store keeps
// the cross of the two: day → game key → seconds, for about 400 days, in its own
// file (playtime-days.json in the data folder, a person's own folder under
// profiles) rather than in jukauser.json, which every playtime save rewrites
// whole. It is fed where a game's measured time is credited
// (playtimeCreditGameGap, chip8CreditPlay), written whenever playtime itself
// saves (playtimePersist, playtimeFlush) through AtomicWrite (temp file, fsync,
// rename, .bak), and re-anchored by the rules playtimeReanchorDays follows when
// network time moves the date. "Ever" comes mostly from the lifetime ledger,
// which has it from day one (this store starts empty on the first run): a
// game's Ever is the larger of its ledger figure and its kept days.
//
// Main thread only, apart from the file writes, which a goroutine does from a
// snapshot.

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	playtimePeriodsVersion  = 1
	playtimePeriodsFileName = "playtime-days.json"
	// playtimePeriodsKeepDays is how far back days are kept: a whole year with
	// a month to spare, so This year still reaches 1 January on 31 December.
	playtimePeriodsKeepDays = 400
	// playtimePeriodsRunDays is how many day keys of this run are remembered
	// for a re-anchor (playtimeRunNote keeps as many).
	playtimePeriodsRunDays = 4
)

// The periods the By game tab shows, in the order Left/Right steps through.
const (
	playtimePeriodToday = iota
	playtimePeriodWeek
	playtimePeriodMonth
	playtimePeriodYear
	playtimePeriodEver
	playtimePeriodCount
)

// playtimePeriodLabels are the periods' names on the tab's pills.
var playtimePeriodLabels = [playtimePeriodCount]string{"Today", "This week", "This month", "This year", "Ever"}

// playtimePeriodsFile is the file's shape.
type playtimePeriodsFile struct {
	Version int `json:"version"`
	// Since is the first day this store counted on (a clock that could be
	// believed), so a period that began before it can say it is partial.
	Since string `json:"since,omitempty"`
	// Days is day ("2006-01-02") → game key (playtimeGameKey) → seconds.
	Days map[string]map[string]int64 `json:"days"`
}

// playtimePeriodsRunEntry is what this run credited to one game on one day:
// the seconds, and when (an order), so a short re-anchor moves the newest.
type playtimePeriodsRunEntry struct {
	secs int64
	seq  uint64
}

var playtimePeriods struct {
	loaded bool
	// path is the file data was read from; saves go back there, so a profile
	// switch can never write one person's days into another's folder.
	path  string
	data  playtimePeriodsFile
	dirty bool
	// rev changes whenever data does, so a view rebuilds only then.
	rev uint64
	// run is what this run credited since the clock was last known right or
	// last re-anchored, by day key and game (playtimeRun's per-game twin).
	run    map[string]map[string]playtimePeriodsRunEntry
	runSeq uint64
	// view and confirm follow the clock's jumps and confirmations on their
	// own (clockjump.go), checked on the same tick as playtime's.
	view    clockJumpView
	confirm uint64
}

// playtimePeriodsWriter orders the background writes: a write that starts
// after a newer snapshot is on the card does nothing.
var playtimePeriodsWriter struct {
	mu      sync.Mutex
	seq     uint64 // last snapshot taken (main thread)
	written uint64 // newest snapshot on disk (under mu)
	pending sync.WaitGroup
}

// playtimePeriodsTestPath replaces the file's path in tests.
var playtimePeriodsTestPath string

// playtimePeriodsPath is where the store lives for whoever is playing: the
// data folder, or the profile's own folder (profilesRedirect).
func playtimePeriodsPath() string {
	if playtimePeriodsTestPath != "" {
		return playtimePeriodsTestPath
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return profilesRedirect(filepath.Join(dir, playtimePeriodsFileName))
}

// playtimePeriodsValidDay reports whether s is a day key.
func playtimePeriodsValidDay(s string) bool {
	if len(s) != len(wrappedDayKey) {
		return false
	}
	_, err := time.Parse(wrappedDayKey, s)
	return err == nil
}

// playtimePeriodsClean drops what a hand-edited or damaged file may hold:
// keys that are not days, empty game keys and figures that are not positive.
func playtimePeriodsClean(f *playtimePeriodsFile) {
	if f.Days == nil {
		f.Days = map[string]map[string]int64{}
	}
	for day, games := range f.Days {
		if !playtimePeriodsValidDay(day) {
			delete(f.Days, day)
			continue
		}
		for key, secs := range games {
			if strings.TrimSpace(key) == "" || secs <= 0 {
				delete(games, key)
			}
		}
		if len(games) == 0 {
			delete(f.Days, day)
		}
	}
	if f.Since != "" && !playtimePeriodsValidDay(f.Since) {
		f.Since = ""
	}
}

// playtimePeriodsEnsure reads the store the first time it is needed after a
// start, a restore or a profile switch.
func playtimePeriodsEnsure() {
	p := &playtimePeriods
	if p.loaded {
		return
	}
	p.loaded = true
	p.dirty = false
	p.rev++
	p.data = playtimePeriodsFile{Version: playtimePeriodsVersion, Days: map[string]map[string]int64{}}
	p.path = playtimePeriodsPath()
	if p.path == "" {
		return
	}
	// A write still in flight (the person switched back) lands first.
	playtimePeriodsWriter.pending.Wait()
	var saved playtimePeriodsFile
	if _, err := readJSONWithBackup(p.path, &saved); err != nil {
		// Absent is the first run; a damaged file with no usable backup was
		// logged by the reader. Either way the days start empty.
		return
	}
	playtimePeriodsClean(&saved)
	saved.Version = playtimePeriodsVersion
	p.data = saved
}

// playtimePeriodsCredit adds a game's measured seconds to day: the choke
// points that credit the lifetime ledger call it with the same key and day.
func playtimePeriodsCredit(key, day string, secs int64) {
	key = strings.TrimSpace(key)
	if key == "" || day == "" || secs <= 0 {
		return
	}
	playtimePeriodsEnsure()
	p := &playtimePeriods
	games := p.data.Days[day]
	if games == nil {
		games = map[string]int64{}
		p.data.Days[day] = games
	}
	games[key] += secs
	playtimePeriodsRunNote(day, key, secs)
	p.dirty = true
	p.rev++
}

// playtimePeriodsRunNote remembers what this run credited under day, for a
// re-anchor. Like playtimeRunNote, days on end with neither a sync nor a jump
// start the record over.
func playtimePeriodsRunNote(day, key string, secs int64) {
	p := &playtimePeriods
	if p.run == nil || (len(p.run) >= playtimePeriodsRunDays && p.run[day] == nil) {
		p.run = make(map[string]map[string]playtimePeriodsRunEntry, 2)
	}
	games := p.run[day]
	if games == nil {
		games = map[string]playtimePeriodsRunEntry{}
		p.run[day] = games
	}
	p.runSeq++
	e := games[key]
	e.secs += secs
	e.seq = p.runSeq
	games[key] = e
}

// playtimePeriodsClockCheck runs on every playtime tick after playtime's own
// clock check (playtimeTick): it reads the store on the first tick, notes the
// first day a believable clock shows, and re-anchors the days when the clock
// jumped, the way playtimeClockJumpCheck re-anchors the day totals.
func playtimePeriodsClockCheck() {
	p := &playtimePeriods
	playtimePeriodsEnsure()
	if n := clockConfirmCount.Load(); n != p.confirm {
		// Network time found the clock right: what was credited went under
		// the right date.
		p.confirm = n
		p.run = nil
	}
	shift, jumped := clockJumpSeen(&p.view)
	now := time.Now()
	if jumped {
		today := now.Format(wrappedDayKey)
		before := now.Add(-shift.wall).Format(wrappedDayKey)
		var run map[string]playtimePeriodsRunEntry
		if p.run != nil {
			run = p.run[before]
		}
		if playtimePeriodsReanchor(p.data.Days, before, run, shift.wall, today) {
			p.dirty = true
			p.rev++
			logInfo("clock", "play time by game re-anchored to %s after a clock change", today)
		}
		if p.data.Since > today {
			p.data.Since = today
			p.dirty = true
		}
		p.run = nil
	}
	if p.data.Since == "" && trophyClockSane(now) {
		p.data.Since = now.Format(wrappedDayKey)
		p.dirty = true
	}
}

// playtimePeriodsReanchor is playtimeReanchorDays for time kept by game: days
// after today (a clock that ran ahead) fold into today, and when the jump
// changed the date, what this run credited under before (run) moves to today —
// never more in all than the jump could have misdated (|shift|), the newest
// credit first, nor more than a game's day holds. It reports whether anything
// moved.
func playtimePeriodsReanchor(days map[string]map[string]int64, before string, run map[string]playtimePeriodsRunEntry,
	shift time.Duration, today string) bool {
	if days == nil {
		return false
	}
	changed := false
	into := func(day string) map[string]int64 {
		games := days[day]
		if games == nil {
			games = map[string]int64{}
			days[day] = games
		}
		return games
	}
	for day, games := range days {
		if day <= today {
			continue
		}
		delete(days, day)
		dst := into(today)
		for key, secs := range games {
			dst[key] += secs
		}
		changed = true
	}
	if before == "" || before == today || len(run) == 0 {
		return changed
	}
	src := days[before]
	if len(src) == 0 {
		return changed
	}
	if shift < 0 {
		shift = -shift
	}
	budget := int64(shift / time.Second)
	keys := make([]string, 0, len(run))
	for key, e := range run {
		if e.secs > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return run[keys[i]].seq > run[keys[j]].seq })
	for _, key := range keys {
		if budget <= 0 {
			break
		}
		move := min(run[key].secs, budget, src[key])
		if move <= 0 {
			continue
		}
		src[key] -= move
		if src[key] <= 0 {
			delete(src, key)
		}
		into(today)[key] += move
		budget -= move
		changed = true
	}
	if len(src) == 0 {
		delete(days, before)
	}
	return changed
}

// playtimePeriodsMoveKey moves a game's days to a new key: a renamed file
// (gameRenameMoveTitle) or a Game Boy title moved to Game Boy Color
// (gbcMoveTitle), the way they move the lifetime ledger.
func playtimePeriodsMoveKey(oldKey, newKey string) {
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	playtimePeriodsEnsure()
	p := &playtimePeriods
	moved := false
	for _, games := range p.data.Days {
		if secs, ok := games[oldKey]; ok {
			delete(games, oldKey)
			games[newKey] += secs
			moved = true
		}
	}
	for _, games := range p.run {
		if e, ok := games[oldKey]; ok {
			delete(games, oldKey)
			have := games[newKey]
			have.secs += e.secs
			have.seq = max(have.seq, e.seq)
			games[newKey] = have
		}
	}
	if moved {
		p.dirty = true
		p.rev++
		// Saved with playtime's next save, within its interval.
		playtime.dirty = true
	}
}

// playtimePeriodsPrune forgets days older than the window. Only on a clock
// that can be believed: a handheld still at its power-on date must not judge
// what is old, and one that ran ahead is re-anchored before it is believed.
func playtimePeriodsPrune(days map[string]map[string]int64, now time.Time) {
	if !trophyClockSane(now) {
		return
	}
	cutoff := now.AddDate(0, 0, -playtimePeriodsKeepDays).Format(wrappedDayKey)
	for day := range days {
		if day < cutoff {
			delete(days, day)
		}
	}
}

// playtimePeriodsPersist writes the store when it changed, beside playtime's
// own save. wait is the exit and a profile switch: the file is on the card
// when it returns. Otherwise a goroutine writes the snapshot, off the render
// thread.
func playtimePeriodsPersist(wait bool) {
	p := &playtimePeriods
	if !p.loaded || !p.dirty || p.path == "" {
		return
	}
	playtimePeriodsPrune(p.data.Days, time.Now())
	data, err := json.Marshal(p.data)
	if err != nil {
		return
	}
	data = append(data, '\n')
	p.dirty = false
	path := p.path
	w := &playtimePeriodsWriter
	w.seq++
	seq := w.seq
	write := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if seq <= w.written {
			return // a newer snapshot is already on the card
		}
		w.written = seq
		if err := AtomicWrite(path, data, 0o644); err != nil {
			logWarn("playtime", "cannot save play time by day: %v", err)
		}
	}
	if wait {
		write()
		return
	}
	w.pending.Add(1)
	go func() {
		defer w.pending.Done()
		write()
	}()
}

// playtimePeriodsReload drops the store so the next use reads it again: a
// backup restore (bkupReloadStores) or a profile switch, both through
// playtimeInit. Nothing is written first: a restore has just put the file it
// wants read in place, and a switch flushed the person leaving already
// (profilesFlushStores → playtimeFlush).
func playtimePeriodsReload() {
	p := &playtimePeriods
	if !p.loaded {
		return
	}
	p.loaded, p.dirty, p.run = false, false, nil
	p.data = playtimePeriodsFile{}
	p.path = ""
	p.rev++
}

// ── Reading it ───────────────────────────────────────────────────────────────

// playtimePeriodStart is the first day of period around now ("" for Ever).
// Weeks start on Monday, like the Playtime tab's week and the heatmap.
func playtimePeriodStart(period int, now time.Time) string {
	switch period {
	case playtimePeriodToday:
		return now.Format(wrappedDayKey)
	case playtimePeriodWeek:
		weekday := (int(now.Weekday()) + 6) % 7 // Monday = 0
		return now.AddDate(0, 0, -weekday).Format(wrappedDayKey)
	case playtimePeriodMonth:
		return time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, now.Location()).Format(wrappedDayKey)
	case playtimePeriodYear:
		return time.Date(now.Year(), time.January, 1, 12, 0, 0, 0, now.Location()).Format(wrappedDayKey)
	}
	return ""
}

// playtimePeriodSums is each game's seconds over the days from start to today
// (every day kept when start is "").
func playtimePeriodSums(days map[string]map[string]int64, start, today string) map[string]int64 {
	out := map[string]int64{}
	for day, games := range days {
		if (start != "" && day < start) || day > today {
			continue
		}
		for key, secs := range games {
			if secs > 0 {
				out[key] += secs
			}
		}
	}
	return out
}

// playtimePeriodTotals is each game's time in period. Ever is the lifetime
// ledger, complete from before this store existed; a game the ledger let go
// (it keeps the 64 longest) keeps what the days still hold.
func playtimePeriodTotals(period int, now time.Time) map[string]int64 {
	playtimePeriodsEnsure()
	days := playtimePeriods.data.Days
	today := now.Format(wrappedDayKey)
	if period != playtimePeriodEver {
		return playtimePeriodSums(days, playtimePeriodStart(period, now), today)
	}
	out := playtimePeriodSums(days, "", today)
	for key, secs := range playtime.data.Games {
		if secs > out[key] {
			out[key] = secs
		}
	}
	return out
}

// playtimePeriodRow is one line of the By game list.
type playtimePeriodRow struct {
	Name    string // the title, or the console in the Consoles view
	Sub     string // the console, or how many games the console's time is
	Seconds int64
}

// playtimePeriodOtherConsole names the time kept under a bare title (from
// before keys named the console) in the Consoles view.
const playtimePeriodOtherConsole = "Other"

// playtimePeriodRows ranks totals, longest first, by game or summed by
// console. Equal times sort by name, so rows never trade places.
func playtimePeriodRows(totals map[string]int64, consoles bool) []playtimePeriodRow {
	var out []playtimePeriodRow
	if consoles {
		secs := map[string]int64{}
		games := map[string]int{}
		for key, s := range totals {
			if s <= 0 {
				continue
			}
			system, _ := playtimeGameKeyParts(key)
			if system == "" {
				system = playtimePeriodOtherConsole
			}
			secs[system] += s
			games[system]++
		}
		out = make([]playtimePeriodRow, 0, len(secs))
		for system, s := range secs {
			out = append(out, playtimePeriodRow{Name: system, Sub: pluralizeCount(games[system], "game", "games"), Seconds: s})
		}
	} else {
		out = make([]playtimePeriodRow, 0, len(totals))
		for key, s := range totals {
			if s <= 0 {
				continue
			}
			system, title := playtimeGameKeyParts(key)
			out = append(out, playtimePeriodRow{Name: title, Sub: system, Seconds: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Sub < out[j].Sub
	})
	return out
}

// playtimePeriodsGameCache keeps the game options' line between frames.
var playtimePeriodsGameCache struct {
	key     string
	rev     uint64
	playRev uint64
	day     string
	line    string
}

// playtimePeriodsGameLine is the line the game options show about a game's
// time: "This week 1h 20m · Ever 14h", "Ever 14h" when it was not played this
// week, "" when it was never played. Render thread; rebuilt only when the
// figures or the day change.
func playtimePeriodsGameLine(system, title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	playtimePeriodsEnsure()
	now := time.Now()
	today := now.Format(wrappedDayKey)
	key := playtimeGameKey(system, title)
	c := &playtimePeriodsGameCache
	if c.key == key && c.rev == playtimePeriods.rev && c.playRev == playtime.rev && c.day == today {
		return c.line
	}
	c.key, c.rev, c.playRev, c.day = key, playtimePeriods.rev, playtime.rev, today
	start := playtimePeriodStart(playtimePeriodWeek, now)
	var week, kept int64
	for day, games := range playtimePeriods.data.Days {
		secs := games[key]
		if secs <= 0 || day > today {
			continue
		}
		kept += secs
		if day >= start {
			week += secs
		}
	}
	ever := max(playtimeGameSecondsFor(system, title), kept)
	switch {
	case ever <= 0:
		c.line = ""
	case week > 0:
		c.line = "This week " + formatPlaytime(week) + " · Ever " + formatPlaytime(ever)
	default:
		c.line = "Ever " + formatPlaytime(ever)
	}
	return c.line
}
