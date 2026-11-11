package main

// boxart_auto.go — box art without a press.
//
// Covers used to arrive only when someone asked for them: the tour's "Fetch
// game artwork" step or the library's Fill gaps (L1 / F). A card filled from a
// PC after the tour, or a game copied over FTP, showed initials on an accent
// tile until the user found that action, and most people never look for it.
//
// So the library's own fetcher (romsetup.go) now also runs by itself:
//
//   - a first pass about a minute and a half after the interface is up, on a
//     card that has never had one;
//   - then at most once a day, which is also what asks again about a title the
//     thumbnail set did not have a month ago (the not-found cache's window);
//   - and early when the ROM folders gained games: every half hour a cheap look
//     at them (one directory listing per console folder, no walk of the card)
//     is compared with the one the last pass saw.
//
// A pass scans the card once, keeps the games that have no cover and that the
// thumbnail server has not already answered "not found" for, and hands them to
// startLibraryArtworkFetch in batches. It waits for a moment that costs the user
// nothing:
//
//   - the setting is on (Settings → Games & emulators → Find box art
//     automatically) and safe mode allows background network work;
//   - online, with a clock that has been set (the not-found cache is dated);
//   - never while a game or emulator owns the screen, and a game that starts
//     mid-pass stops it after the cover it is on;
//   - never beside another artwork fetch or a working install;
//   - on battery only above 20 % and once the user has been away for a minute
//     and a half; on the charger or a desktop, at once.
//
// Only a pass in which every title got an answer — a saved cover or a real
// "not found" — is stamped. A stopped or interrupted pass is not, so the next
// good moment resumes it; the covers already saved and the not-found cache are
// what make that cheap. A pass that hit network errors backs off (half an hour,
// doubling, at most a day) so a server that keeps failing one title is not
// asked every half hour, and a pass the user stopped from the library is left
// alone for a day.
//
// What leaves the device is what Fill gaps sends: game titles, as GET requests
// to the public libretro thumbnails server (thumbnails.libretro.com). Nothing is
// uploaded.

import (
	"encoding/json"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// boxArtAutoKey is the Custom variable behind the Settings switch.
	boxArtAutoKey = "artAutoFetch"
	// boxArtStartupDelay keeps the first look out of the boot: by then the
	// network monitor has probed, the helper download a first start often
	// begins has had its head start, and the home screen has its covers drawn.
	boxArtStartupDelay = 100 * time.Second
	// boxArtInterval is how often a pass may run with no other reason.
	boxArtInterval = 24 * time.Hour
	// boxArtCheckEvery is how often the ROM folders are looked at for new games.
	boxArtCheckEvery = 30 * time.Minute
	// boxArtPoll is how often the scheduler wakes to look for a good moment. A
	// wake with nothing to do reads a few atomics and the cached battery level.
	boxArtPoll = time.Minute
	// boxArtIdle is how long the user must have been away before a pass on
	// battery may start. Once started, a pass carries on when they come back:
	// it is small downloads in the background, not work on the render thread.
	boxArtIdle = 90 * time.Second
	// boxArtMinBattery is the charge (percent) under which a pass waits for the
	// charger.
	boxArtMinBattery = 20
	// boxArtBatch is how many titles one fetch is given. The fetcher saves the
	// not-found cache when a fetch ends, so a batch bounds what a power cut in
	// the middle of a first pass over a big library can lose, and the moment is
	// checked again between batches.
	boxArtBatch = 200
	// boxArtBatchLimit stops a batch that is still running after this long.
	// A healthy batch takes a few minutes; one this slow is a network that
	// times out on every request, and it backs off like a network error.
	boxArtBatchLimit = 30 * time.Minute
	// boxArtWatchEvery is how often a running batch looks for a game start.
	boxArtWatchEvery = 2 * time.Second
	// boxArtRetryFirst is the wait after the first pass that hit network errors.
	boxArtRetryFirst = 30 * time.Minute
	// boxArtStampFile is the pass stamp. It sits beside the covers and the
	// not-found cache, so deleting data/game-art starts everything over.
	boxArtStampFile = ".auto-art.json"
	// boxArtStampVersion is the stamp's layout.
	boxArtStampVersion = 1
)

// boxArtAutoOff mirrors the Settings switch for the scheduler goroutine: the
// Custom map belongs to the main thread. The zero value is "on", the default.
var boxArtAutoOff atomic.Bool

// boxArtStarted makes the scheduler a single goroutine however often the start
// hook runs, so two passes can never stack.
var boxArtStarted atomic.Bool

// ── The setting ───────────────────────────────────────────────────────────

// boxArtAutoEnabled reports the Settings switch (main thread). It is on unless
// the user turned it off: the key is read in any casing (the web builder
// capitalises), and only an explicit off — false, "false", "off", "no", 0 —
// counts, so a garbled value keeps the default.
func boxArtAutoEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	v, ok := customValue(config.Variables.Custom, boxArtAutoKey)
	if !ok || v == nil {
		return true
	}
	if s, isString := v.(string); isString {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "off", "no", "0":
			return false
		}
		return true
	}
	return coerceBool(v)
}

// boxArtSetAutoEnabled stores the switch and tells the scheduler (main thread).
// Turning it off also stops a pass that is running, within a couple of seconds;
// a fetch the user started from the library is theirs and keeps going.
func boxArtSetAutoEnabled(config *Config, on bool) {
	boxArtAutoOff.Store(!on)
	setStoreBool(config, boxArtAutoKey, on)
}

// ── The stamp ─────────────────────────────────────────────────────────────

// boxArtStamp records the last pass that finished: when, what the ROM folders
// looked like when it began, and what it came to (for the log and the next
// reader of the file).
type boxArtStamp struct {
	Version  int       `json:"version"`
	LastPass time.Time `json:"lastPass"`
	Library  string    `json:"library"`
	Games    int       `json:"games"`
	Found    int       `json:"found"`
}

// boxArtStampPath is where the stamp lives, "" when the app has no data folder.
func boxArtStampPath() string {
	dir := romArtworkDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, boxArtStampFile)
}

// loadBoxArtStamp reads the stamp. A missing, damaged or foreign file is no
// stamp, which makes the next pass due: an extra pass costs a scan, a lost one
// costs covers.
func loadBoxArtStamp(path string) (boxArtStamp, bool) {
	if strings.TrimSpace(path) == "" {
		return boxArtStamp{}, false
	}
	var st boxArtStamp
	if _, err := readJSONWithBackup(path, &st); err != nil || st.Version != boxArtStampVersion {
		return boxArtStamp{}, false
	}
	return st, true
}

// saveBoxArtStamp writes the stamp through the crash-safe path the other small
// stores use.
func saveBoxArtStamp(path string, st boxArtStamp) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	payload, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, payload, 0o644)
}

// ── Decisions (pure, so they have table tests) ────────────────────────────

// boxArtPassDue reports whether a pass should run now, and why. It follows the
// rule the update checks use (autoUpdateCheckDue): the handheld has no RTC
// battery, so a missing, garbled, pre-2020 or future stamp never holds a pass
// back. library is the ROM folders' signature as last looked at; "" (not looked
// at yet, or unreadable) is no reason to run.
func boxArtPassDue(stamp boxArtStamp, have bool, library string, now time.Time) (bool, string) {
	if !have || stamp.LastPass.IsZero() {
		return true, "first pass"
	}
	last := stamp.LastPass
	if last.Before(minSaneClock) || now.Before(last) {
		return true, "the last pass has no usable date"
	}
	if now.Sub(last) >= boxArtInterval {
		return true, "daily pass"
	}
	if library != "" && library != stamp.Library {
		return true, "the ROM folders changed"
	}
	return false, ""
}

// boxArtConditions is everything the scheduler asks before a pass or a batch,
// read in one place so the policy below is a pure function.
type boxArtConditions struct {
	Enabled    bool          // the Settings switch
	Network    bool          // safe mode allows background network work
	Online     bool          // the network monitor's last answer
	ClockSet   bool          // the clock is past minSaneClock
	InGame     bool          // a game or emulator owns the screen
	Fetching   bool          // an artwork fetch is running
	Installing bool          // the install bar shows a working install
	Handheld   bool          // battery rules apply even when the level is unknown
	Battery    int           // percent, -1 when unknown
	Charging   bool          // on external power
	Idle       time.Duration // since the last button, key or touch
}

// boxArtConditionsAt reads the live conditions. Every source is safe to read
// off the main thread.
func boxArtConditionsAt(now time.Time) boxArtConditions {
	return boxArtConditions{
		Enabled:    !boxArtAutoOff.Load(),
		Network:    safeModeAllows(SafeFeatureNetwork),
		Online:     IsOnline(),
		ClockSet:   !now.Before(minSaneClock),
		InGame:     gameScreenSuspended(),
		Fetching:   artworkFetchRunning(),
		Installing: installBarActive(),
		Handheld:   P().IsHandheld(),
		Battery:    getBatteryPercent(),
		Charging:   getBatteryCharging(),
		Idle:       userInputAge(now),
	}
}

// boxArtMomentOK is the "is now a good time" policy. starting is true before a
// pass begins; between the batches of a running pass only the hard stops
// count (switched off, offline, a game, a battery running low), because the
// pass already waited for its moment and the fetch slot is its own.
func boxArtMomentOK(c boxArtConditions, starting bool) (bool, string) {
	switch {
	case !c.Enabled:
		return false, "switched off"
	case !c.Network:
		return false, "safe mode"
	case !c.Online:
		return false, "offline"
	case !c.ClockSet:
		return false, "the clock is not set yet"
	case c.InGame:
		return false, "a game has the screen"
	case starting && c.Fetching:
		return false, "another artwork fetch is running"
	case starting && c.Installing:
		return false, "an install is running"
	}
	// A desktop reads no battery at all: it is on mains. A handheld that cannot
	// read its level still waits for the user to step away.
	if c.Charging || (c.Battery < 0 && !c.Handheld) {
		return true, ""
	}
	if c.Battery >= 0 && c.Battery < boxArtMinBattery {
		return false, "battery under " + strconv.Itoa(boxArtMinBattery) + "%"
	}
	if starting && c.Idle < boxArtIdle {
		return false, "waiting for the user to step away"
	}
	return true, ""
}

// boxArtHeld reports whether a hold (a back-off, or a day of quiet after the
// user stopped a pass) still applies.
func boxArtHeld(until, now time.Time) bool {
	return !until.IsZero() && now.Before(until)
}

// boxArtRetryDelay is the wait after the n-th pass in a row that hit network
// errors: half an hour, doubling, never more than a day.
func boxArtRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := boxArtRetryFirst
	for i := 1; i < failures && d < boxArtInterval; i++ {
		d *= 2
	}
	if d > boxArtInterval {
		d = boxArtInterval
	}
	return d
}

// boxArtFoundMessage is the notification for the covers a pass saved, and ""
// when it saved none: a pass that found nothing new says nothing.
func boxArtFoundMessage(found int) string {
	if found <= 0 {
		return ""
	}
	return "Found box art for " + pluralizeCount(found, "game", "games")
}

// ── What is on the card ───────────────────────────────────────────────────

// boxArtLibrarySignature fingerprints the ROM folders cheaply enough to run
// every half hour: one directory listing per root and per console folder the
// scanner recognises (romSystemAliases), hashing the names of the games in it
// and, for a game kept in a sub-folder, that folder's name and modification
// time. Nothing is opened and the card is not walked.
//
// The names catch the common cases on any card — new files at the top of a
// console folder, or a new game folder — including games copied from a PC,
// where Windows does not move a FAT or exFAT folder's modification time when a
// file is added inside it. The sub-folder times add what Linux itself wrote (a
// second disc copied over FTP); anything they miss waits for the daily pass.
// Saves, cache databases and the firmware's Imgs folder are not games and are
// left out, so playing does not look like a new game. A root or folder that
// cannot be read hashes as such, so one that appears later changes the answer.
func boxArtLibrarySignature(roots []string) string {
	h := fnv.New64a()
	add := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
		h.Write([]byte{'\n'})
	}
	for _, root := range roots {
		systems, err := os.ReadDir(root)
		if err != nil {
			add("root", root, "unreadable")
			continue
		}
		add("root", root)
		for _, systemDir := range systems {
			name := systemDir.Name()
			if !systemDir.IsDir() || skipScanEntryName(name) {
				continue
			}
			if _, known := romSystemAliases[strings.ToLower(name)]; !known {
				continue
			}
			games, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				add("system", name, "unreadable")
				continue
			}
			add("system", name)
			for _, game := range games {
				gameName := game.Name()
				if skipScanEntryName(gameName) {
					continue
				}
				if game.IsDir() {
					if strings.EqualFold(gameName, "imgs") {
						continue
					}
					stamp := ""
					if info, err := game.Info(); err == nil {
						stamp = strconv.FormatInt(info.ModTime().UnixNano(), 10)
					}
					add("folder", gameName, stamp)
					continue
				}
				if romFileExtensions[strings.ToLower(filepath.Ext(gameName))] {
					add("game", gameName)
				}
			}
		}
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// boxArtMissKey is the key the fetcher files a ROM's "not found" under
// (fetchROMArtworkTracked): its collection and its file stem.
func boxArtMissKey(rom scannedROM) string {
	stem := strings.TrimSuffix(filepath.Base(rom.Path), filepath.Ext(rom.Path))
	return artworkMissKey(rom.Collection, stem)
}

// boxArtTitlesToTry drops the games the thumbnail server answered "not found"
// for within the cache's window. The fetcher would skip them without asking,
// so a pass made only of them is stamped at once instead of starting a fetch,
// and the library's "fetching 3/40" counts real lookups.
func boxArtTitlesToTry(missing []scannedROM, misses map[string]time.Time, now time.Time) []scannedROM {
	out := make([]scannedROM, 0, len(missing))
	for _, rom := range missing {
		if artworkRecentlyMissing(misses, boxArtMissKey(rom), now, artworkMissTTL) {
			continue
		}
		out = append(out, rom)
	}
	return out
}

// boxArtTally sorts one batch after its fetch: covers now on the card, titles
// the server answered "not found" for, and the rest — a network or server
// error, a cover that could not be written, or a title the fetch never reached
// because it was stopped. Only the last kind keeps a pass from being stamped.
// It counts from the card rather than the fetcher's counters, so a cover is
// counted whoever saved it.
func boxArtTally(batch []scannedROM, dest string, misses map[string]time.Time, now time.Time) (found, notFound, unanswered int) {
	for _, rom := range batch {
		switch {
		case artworkCoverPresent(romArtworkPath(rom, dest)):
			found++
		case artworkRecentlyMissing(misses, boxArtMissKey(rom), now, artworkMissTTL):
			notFound++
		default:
			unanswered++
		}
	}
	return found, notFound, unanswered
}

// ── The scheduler ─────────────────────────────────────────────────────────

// boxArtStop is why a pass's batches ended.
type boxArtStop int

const (
	boxArtRanAll boxArtStop = iota
	boxArtStopGame
	boxArtStopOff
	boxArtStopUser
	boxArtStopSlow
	boxArtStopMoment
	boxArtStopBusy
)

// boxArtRun is what the batches of one pass came to.
type boxArtRun struct {
	found      int
	notFound   int
	unanswered int
	stop       boxArtStop
}

// boxArtScheduler is the goroutine's own state; nothing else touches it.
type boxArtScheduler struct {
	stamp     boxArtStamp
	haveStamp bool
	// library is the ROM folders' signature as last looked at, and lookedAt
	// when that was.
	library  string
	lookedAt time.Time
	// failures counts passes in a row that hit network errors; retryAt holds
	// the next one back. quietUntil is a day of quiet after the user stopped a
	// pass from the library.
	failures   int
	retryAt    time.Time
	quietUntil time.Time
	// lastWait is the last reason logged for not running, so a long wait is
	// one log line rather than one a minute.
	lastWait string
	// gameSeenAt is the last poll that found a game on the screen (noteGame).
	gameSeenAt time.Time
}

// startBoxArtAuto starts the scheduler once. It runs from the deferred startup
// queue on the main thread (main.go), which is where the setting may be read;
// everything after that happens on the scheduler's goroutine.
func startBoxArtAuto(config *Config) {
	boxArtAutoOff.Store(!boxArtAutoEnabled(config))
	if !safeModeAllows(SafeFeatureNetwork) {
		logInfo("art", "automatic box art: not started in safe mode")
		return
	}
	if !boxArtStarted.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(boxArtStartupDelay)
		s := &boxArtScheduler{}
		s.stamp, s.haveStamp = loadBoxArtStamp(boxArtStampPath())
		for {
			s.tick(time.Now())
			time.Sleep(boxArtPoll)
		}
	}()
}

// wait logs why the scheduler is not running a pass, once per reason.
func (s *boxArtScheduler) wait(why string) {
	if why == s.lastWait {
		return
	}
	s.lastWait = why
	logDebug("art", "automatic box art waits: %s", why)
}

// noteGame keeps a game from reading as time away. The pad is the game's while
// one runs and the parked main loop does not note its presses, so the input
// clock would count a long game as a long absence and start a pass on battery
// the moment the player is back: the time since the last poll that saw a game
// caps the idle time instead.
func (s *boxArtScheduler) noteGame(c *boxArtConditions, now time.Time) {
	if c.InGame {
		s.gameSeenAt = now
		return
	}
	if !s.gameSeenAt.IsZero() {
		if since := now.Sub(s.gameSeenAt); since >= 0 && since < c.Idle {
			c.Idle = since
		}
	}
}

// tick makes one scheduling decision and runs the pass when it is due. A panic
// is logged and the next poll tries again: box art is never worth the app.
func (s *boxArtScheduler) tick(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			logError("art", "automatic box art: %v", r)
		}
	}()
	c := boxArtConditionsAt(now)
	s.noteGame(&c, now)
	if ok, why := boxArtMomentOK(c, true); !ok {
		s.wait(why)
		return
	}
	if boxArtHeld(s.retryAt, now) {
		s.wait("backing off after network errors")
		return
	}
	if boxArtHeld(s.quietUntil, now) {
		s.wait("a pass was stopped from the library today")
		return
	}
	// The folders are looked at here, at a good moment, rather than on a timer
	// of their own: even a listing is card work a game should not compete with.
	// A pass that is due anyway takes its own look, so this one is skipped.
	if s.lookedAt.IsZero() || now.Sub(s.lookedAt) >= boxArtCheckEvery {
		if dueAnyway, _ := boxArtPassDue(s.stamp, s.haveStamp, "", now); !dueAnyway {
			s.library = boxArtLibrarySignature(romLibraryRoots())
			s.lookedAt = now
		}
	}
	due, why := boxArtPassDue(s.stamp, s.haveStamp, s.library, now)
	if !due {
		s.wait("not due")
		return
	}
	s.lastWait = ""
	s.pass(why)
}

// pass runs one automatic pass: one scan of the card, then the titles worth
// asking about, in batches. Its outcome decides the stamp, the back-off and the
// notification.
func (s *boxArtScheduler) pass(why string) {
	dest := romArtworkDir()
	if strings.TrimSpace(dest) == "" {
		s.wait("no data folder for covers")
		return
	}
	started := time.Now()
	roots := romLibraryRoots()
	// The signature is taken before the scan, so games copied while the pass
	// runs make the next look see a change.
	library := boxArtLibrarySignature(roots)
	s.library, s.lookedAt = library, started
	report := scanROMLibrary(roots)
	todo := boxArtTitlesToTry(romsMissingArtwork(report.Games, dest), loadArtworkMisses(dest), started)
	logInfo("art", "automatic box art (%s): %d game(s) on the card, %d to look up", why, len(report.Games), len(todo))
	if len(todo) == 0 {
		s.finish(library, len(report.Games), 0)
		return
	}

	run := boxArtRunBatches(todo, dest)
	logInfo("art", "automatic box art: %d saved, %d not in the thumbnail set, %d unanswered (%s)",
		run.found, run.notFound, run.unanswered, boxArtStopWords(run.stop))
	switch {
	case run.stop == boxArtRanAll && run.unanswered == 0:
		s.finish(library, len(report.Games), run.found)
	case run.stop == boxArtRanAll || run.stop == boxArtStopSlow:
		// Every batch ran but some titles got no answer: network or server
		// errors. Their covers are still missing and they are not in the
		// not-found cache, so the retry asks only about them.
		s.failures++
		s.retryAt = time.Now().Add(boxArtRetryDelay(s.failures))
	case run.stop == boxArtStopUser:
		// The library's L1 / F stops whatever fetch is running, this one
		// included. The user said "not now"; a day later is soon enough. The
		// library's status line already says what the stopped fetch saved.
		s.quietUntil = time.Now().Add(boxArtInterval)
		return
	default:
		// A game, the switch, a weak moment or another fetch: not stamped and
		// no back-off, so the next good moment starts a fresh pass, and the
		// covers already saved and the not-found cache make it resume.
	}
	boxArtAnnounce(run.found)
}

// finish stamps a pass that got every title an answer (or had none to ask).
func (s *boxArtScheduler) finish(library string, games, found int) {
	s.stamp = boxArtStamp{
		Version:  boxArtStampVersion,
		LastPass: time.Now().UTC(),
		Library:  library,
		Games:    games,
		Found:    found,
	}
	s.haveStamp = true
	s.failures, s.retryAt = 0, time.Time{}
	if err := saveBoxArtStamp(boxArtStampPath(), s.stamp); err != nil {
		logWarn("art", "automatic box art: cannot save the pass stamp: %v", err)
	}
}

// boxArtRunBatches hands the titles to the library's fetcher a batch at a
// time, and stops at the first batch that did not run to its end.
func boxArtRunBatches(todo []scannedROM, dest string) boxArtRun {
	var run boxArtRun
	for start := 0; start < len(todo); start += boxArtBatch {
		end := start + boxArtBatch
		if end > len(todo) {
			end = len(todo)
		}
		// Checked before the first batch too: the scan before it can take a
		// while on a big card, and a game may have started meanwhile.
		if ok, why := boxArtMomentOK(boxArtConditionsAt(time.Now()), false); !ok {
			logDebug("art", "automatic box art: pausing before a batch: %s", why)
			run.stop = boxArtStopMoment
			run.unanswered += len(todo) - start
			return run
		}
		batch := todo[start:end]
		gen := artworkFetchGeneration()
		if !startLibraryArtworkFetch(batch) {
			// The user started a fetch from the library between two batches;
			// it covers these titles too.
			run.stop = boxArtStopBusy
			run.unanswered += len(todo) - start
			return run
		}
		stop := boxArtWaitBatch(gen)
		found, notFound, unanswered := boxArtTally(batch, dest, loadArtworkMisses(dest), time.Now())
		run.found += found
		run.notFound += notFound
		run.unanswered += unanswered
		if stop != boxArtRanAll {
			run.stop = stop
			run.unanswered += len(todo) - end
			return run
		}
	}
	return run
}

// boxArtFetchState reads the shared fetch state in one lock, so "has the fetch
// started after generation gen ended, and did it stop early" is one consistent
// answer.
func boxArtFetchState() (running bool, finished uint64, stopped bool) {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	return artworkFetchState.running, artworkFetchState.finished, artworkFetchState.stopped
}

// boxArtWaitBatch waits for the fetch started after generation gen to end. A
// game taking the screen, the switch going off or a batch running far too long
// stops it after the cover it is on; a stop nobody here asked for came from
// the library. It reports why the batch ended.
func boxArtWaitBatch(gen uint64) boxArtStop {
	asked := boxArtRanAll
	deadline := time.Now().Add(boxArtBatchLimit)
	for {
		time.Sleep(boxArtWatchEvery)
		running, finished, stopped := boxArtFetchState()
		if !running && finished != gen {
			switch {
			case !stopped:
				return boxArtRanAll
			case asked != boxArtRanAll:
				return asked
			}
			return boxArtStopUser
		}
		if asked != boxArtRanAll {
			continue
		}
		switch {
		case gameScreenSuspended():
			logInfo("art", "automatic box art: a game started, stopping")
			asked = boxArtStopGame
		case boxArtAutoOff.Load():
			asked = boxArtStopOff
		case time.Now().After(deadline):
			logWarn("art", "automatic box art: a batch ran for %s, stopping", boxArtBatchLimit)
			asked = boxArtStopSlow
		default:
			continue
		}
		stopArtworkFetch()
	}
}

// boxArtStopWords names a batch outcome for the log.
func boxArtStopWords(stop boxArtStop) string {
	switch stop {
	case boxArtStopGame:
		return "a game started"
	case boxArtStopOff:
		return "switched off"
	case boxArtStopUser:
		return "stopped from the library"
	case boxArtStopSlow:
		return "too slow"
	case boxArtStopMoment:
		return "paused"
	case boxArtStopBusy:
		return "another fetch took over"
	}
	return "finished"
}

// boxArtAnnounce leaves one quiet notification (no sound; the notification
// centre keeps it) for the covers a pass saved. It waits until JukaHub has the
// screen again: a toast raised under a game would time out unseen.
func boxArtAnnounce(found int) {
	msg := boxArtFoundMessage(found)
	if msg == "" {
		return
	}
	waited := false
	for gameScreenSuspended() {
		waited = true
		time.Sleep(5 * time.Second)
	}
	if waited {
		// The first frames after a game hand the screen back; a toast raised
		// during them would be half over before anyone sees it.
		time.Sleep(3 * time.Second)
	}
	ShowToast(msg, ToastKindInfo)
	logInfo("art", "automatic box art: %s", msg)
}
