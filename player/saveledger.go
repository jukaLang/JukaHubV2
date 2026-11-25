package main

// saveledger.go — the save ledger: which saves of a game JukaHub saw land on
// the card, read back and found whole, so the game's page can say "Saved 4 min
// ago" and mean it (saveledger_ui.go).
//
// A library game's session (gameLaunchRegisterStarted / Exited; ports, web
// games, Android apps and the built-in games keep their saves elsewhere and
// are left out) is watched with stat calls only:
//
//   - at the start, the game's save files are listed: battery saves, save
//     states, the auto state and NextUI's and DraStic's own names, in every
//     save folder the Save Manager knows (shoFileKind, the save handoff's
//     rules), as size and modification time;
//   - every saveLedgerPoll while the game runs (not while the device sleeps)
//     the list is taken again; a file that is new or changed, and has stopped
//     changing, is read back: it must exist, be non-empty, have a believable
//     size and be readable to its last byte; a RetroArch state also has to
//     match its own layout where it has one (the compressed "#RZIPv" container
//     must hold every chunk its header promises, an uncompressed "RASTATE"
//     must hold its whole memory block). A file up to saveLedgerCRCMax gets a
//     CRC-32. The verdict is an entry: what kind of save, its file name (never
//     its folder), size, CRC, what led to it (during play, before sleep, low
//     battery, power off, quit) and when;
//   - the before-sleep hook (sleephooks.go) takes the list once more within
//     its budget, so a save made just before the power button is in the
//     ledger, on the card, before the sync;
//   - at the end — the game quit, crashed, or was saved and closed by the
//     low-battery guard or a power off (quickresume.go calls
//     saveLedgerFinishNow before the power off itself) — the list is taken a
//     last time and how the session ended is written down, with whether it
//     produced any confirmed save. A session that outlives two minutes is
//     also marked "open" on the card; one still open at the next start ended
//     with JukaHub or the power (a crash, a battery pulled), which the page
//     then says honestly when no save came of it.
//
// When. TrimUI boots before NTP, so nothing here orders or ages by wall-clock
// time: entries count up (seq), and carry the boot id, CLOCK_BOOTTIME and
// CLOCK_MONOTONIC (wakeClocks) and the sleep this run counted, so "4 min ago"
// is only said between two points of the same boot. The date is kept only
// when the clock is believable (vfDateNow).
//
// The ledger is <data>/save-ledger.json, at most saveLedgerMaxGames games ×
// saveLedgerKeep entries, written through AtomicWrite: at once for the first
// save of a session, before sleep and at the end; at most every
// saveLedgerWriteDelay for saves during play.
//
// Two more things keep progress safe, both here so the policy lives in one
// place: a game held asleep on a low battery gets a nearer power-off time
// (saveLedgerSleepCap, used by quickSleepDeadline: the existing deadline path
// saves it and powers off before the battery dies), and RetroArch flushes a
// changed battery save every saveFlushSeconds during JukaHub's launches
// (savestate_resume.go).
//
// Nothing here was run on a device from this desk.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	saveLedgerFileName = "save-ledger.json"
	saveLedgerVersion  = 1
	// saveLedgerMaxGames and saveLedgerKeep bound the file: the games touched
	// longest ago go first, and each game keeps its newest entries.
	saveLedgerMaxGames = 2000
	saveLedgerKeep     = 5
	// saveLedgerPoll is how often a running game's saves are looked at.
	saveLedgerPoll = 45 * time.Second
	// saveLedgerSettle is how long a changed file must stay as it was before
	// it is read back: RetroArch writes states in place, not by rename.
	saveLedgerSettle = 1500 * time.Millisecond
	// saveLedgerWriteDelay spaces the ledger's writes for saves during play.
	saveLedgerWriteDelay = 2 * time.Minute
	// saveLedgerLongRun is how long a session must run before an end without
	// a save is worth a warning, and before it is marked open on the card.
	saveLedgerLongRun = 2 * time.Minute
	// saveLedgerSleepBudget is this file's share of the before-sleep budget.
	saveLedgerSleepBudget = 1200 * time.Millisecond
	// saveLedgerSleepFullMax: before sleep, only files this small are read
	// through; bigger states get the layout check, the rest at the next look.
	saveLedgerSleepFullMax = 8 << 20
	// saveLedgerFinishBudget bounds the last look at the end of a session.
	saveLedgerFinishBudget = 5 * time.Second
	// saveLedgerFinishWait bounds a wait for another goroutine's end.
	saveLedgerFinishWait = 7 * time.Second
	// saveLedgerCRCMax: files up to this size are read through for a CRC-32;
	// bigger ones get the layout check and their last block read.
	saveLedgerCRCMax = 32 << 20
	// saveLedgerSaveMax and saveLedgerStateMax are believable sizes: battery
	// saves and memory cards are kilobytes, states megabytes.
	saveLedgerSaveMax  = 64 << 20
	saveLedgerStateMax = saveManagerMaxFile
	// saveLedgerWalkMax and saveLedgerWalkDepth bound one look through the
	// save folders.
	saveLedgerWalkMax   = 20000
	saveLedgerWalkDepth = 4
	// saveLedgerReadMax bounds the ledger read.
	saveLedgerReadMax = 8 << 20

	// saveLedgerCapCache is how long the low-battery sleep cap's reading is
	// reused: the NextUI sleep guard asks four times a second.
	saveLedgerCapCache = 10 * time.Second

	// saveFlushKey is the user setting: RetroArch's SRAM autosave interval
	// in seconds for JukaHub's launches (0 leaves RetroArch's own alone).
	saveFlushKey     = "saveFlushSeconds"
	saveFlushDefault = 10
	saveFlushMax     = 3600
)

// The kinds of save.
const (
	saveLedgerKindSave  = "save"  // the game's own battery save (SRAM, memory card)
	saveLedgerKindState = "state" // a RetroArch save state in a slot
	saveLedgerKindAuto  = "auto"  // RetroArch's auto state, written on quit
	saveLedgerKindEmu   = "emu"   // a standalone emulator's own state
)

// What led to a save, and how a session ended.
const (
	saveLedgerTrigPlay     = "play"
	saveLedgerTrigSleep    = "sleep"
	saveLedgerTrigBattery  = "battery"
	saveLedgerTrigPowerOff = "poweroff"
	saveLedgerTrigQuit     = "quit"

	saveLedgerEndCrash = "crash"  // the emulator died of a signal of its own
	saveLedgerEndLost  = "lost"   // still open at a start in another boot: the power went
	saveLedgerEndGone  = "closed" // still open at a start in the same boot: JukaHub went
)

// saveLedgerChanging is saveLedgerCheck's answer for a file that changed while
// it was read: not damaged, just not finished. It is never recorded.
const saveLedgerChanging = "changing"

// saveLedgerStamp is when something happened, in the forms that survive a
// handheld without NTP.
type saveLedgerStamp struct {
	Seq     uint64 `json:"seq"`
	Boot    string `json:"boot,omitempty"`
	Run     string `json:"run,omitempty"`
	BootNS  int64  `json:"bt,omitempty"` // CLOCK_BOOTTIME (with sleep)
	MonoNS  int64  `json:"mt,omitempty"` // CLOCK_MONOTONIC (without)
	SleptNS int64  `json:"sl,omitempty"` // sleep this run counted (wake hooks)
	Date    string `json:"date,omitempty"`
}

// saveLedgerEntry is one save JukaHub saw written.
type saveLedgerEntry struct {
	saveLedgerStamp
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	CRC     string `json:"crc,omitempty"`
	Trigger string `json:"trigger"`
	// Bad is why the file did not read back ("" when it did).
	Bad string `json:"bad,omitempty"`
	// Sess is the session's first seq: entries of one session share it.
	Sess uint64 `json:"sess,omitempty"`
}

// saveLedgerEnd is how a game's last session ended.
type saveLedgerEnd struct {
	saveLedgerStamp
	How   string `json:"how"`
	RanS  int64  `json:"ran,omitempty"`
	Long  bool   `json:"long,omitempty"`
	Saved bool   `json:"saved,omitempty"`
}

// saveLedgerOpen is a session that ran long enough to matter and has not
// ended yet, as far as the card knows.
type saveLedgerOpen struct {
	saveLedgerStamp
	Path   string `json:"path"`
	Title  string `json:"title,omitempty"`
	System string `json:"system,omitempty"`
	Saved  bool   `json:"saved,omitempty"`
}

// saveLedgerGame is one game's record.
type saveLedgerGame struct {
	Title  string            `json:"title,omitempty"`
	System string            `json:"system,omitempty"`
	Saves  []saveLedgerEntry `json:"saves,omitempty"` // oldest first
	End    *saveLedgerEnd    `json:"end,omitempty"`
	Seq    uint64            `json:"seq"` // last touched
}

// saveLedgerFile is save-ledger.json.
type saveLedgerFile struct {
	V     int                        `json:"v"`
	Seq   uint64                     `json:"seq"`
	Games map[string]*saveLedgerGame `json:"games,omitempty"`
	Open  *saveLedgerOpen            `json:"open,omitempty"`
}

// saveLedgerSeen is one save file as a look through the folders found it.
type saveLedgerSeen struct {
	size  int64
	mtime int64
}

// saveLedgerSession is one running game. game and launcher are fixed; base
// belongs to scanMu; the flags marked so belong to saveLedger.mu.
type saveLedgerSession struct {
	game         libraryGame
	g            shoGame
	launcher     string
	startedAt    time.Time
	actedAtStart int  // the low-battery guard's runs before it (lowBatteryGuardActs)
	resume       bool // started by the power-on resume

	retroArch atomic.Bool
	finishing atomic.Bool
	stop      chan struct{}
	done      chan struct{}

	scanMu sync.Mutex
	roots  []saveRoot
	base   map[string]saveLedgerSeen

	// saveLedger.mu:
	ready    bool
	startSeq uint64
	saved    bool
	long     bool
}

var saveLedger struct {
	mu     sync.Mutex
	loaded bool
	// dirty: file changed since it was last written (saveLedgerPersist
	// writes nothing otherwise — the teardown asks at every exit).
	dirty bool
	file  saveLedgerFile
	cur   *saveLedgerSession
	timer *time.Timer
}

var (
	// saveLedgerWriteMu keeps the writes in order: each writes the newest.
	saveLedgerWriteMu sync.Mutex
	// saveLedgerSlept is the sleep this run's wake hooks counted, in ns.
	saveLedgerSlept atomic.Int64
	// saveLedgerRun tells this process's stamps from an earlier one's.
	saveLedgerRun = strconv.FormatInt(time.Now().UnixNano(), 36)
	// saveLedgerPathOverride points the ledger elsewhere (tests).
	saveLedgerPathOverride string
	// saveLedgerGen counts the ledger's changes, for the page's words.
	saveLedgerGen atomic.Uint64
	// saveLedgerLoadKick: the page started the load.
	saveLedgerLoadKick atomic.Bool
)

// saveLedgerBoot is this boot's id, read once.
var saveLedgerBoot struct {
	once sync.Once
	id   string
}

func init() {
	gameLaunchRegisterStarted(saveLedgerStarted)
	gameLaunchRegisterExited(saveLedgerExited)
	sleepHookRegisterBefore(saveLedgerBeforeSleep)
	sleepHookRegisterWake(saveLedgerWake)
	// After the first frame: a session the last run left open is settled.
	deferStartupTask("save-ledger", func() { go saveLedgerEnsureLoaded() })
}

// ── The file ─────────────────────────────────────────────────────────────

func saveLedgerPath() string {
	if saveLedgerPathOverride != "" {
		return saveLedgerPathOverride
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, saveLedgerFileName)
}

// saveLedgerDecode reads a ledger. A newer version, or a file that is not a
// ledger, is an error. Pure.
func saveLedgerDecode(data []byte) (saveLedgerFile, error) {
	var f saveLedgerFile
	if len(data) > saveLedgerReadMax {
		return saveLedgerFile{}, errors.New("save ledger too large")
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return saveLedgerFile{}, err
	}
	if f.V < 1 || f.V > saveLedgerVersion {
		return saveLedgerFile{}, fmt.Errorf("save ledger version %d", f.V)
	}
	for path, g := range f.Games {
		if g == nil || strings.TrimSpace(path) == "" {
			delete(f.Games, path)
			continue
		}
		if len(g.Saves) > saveLedgerKeep {
			g.Saves = g.Saves[len(g.Saves)-saveLedgerKeep:]
		}
		for _, e := range g.Saves {
			f.Seq = max(f.Seq, e.Seq)
		}
		if g.End != nil {
			f.Seq = max(f.Seq, g.End.Seq)
		}
		f.Seq = max(f.Seq, g.Seq)
	}
	if f.Open != nil && strings.TrimSpace(f.Open.Path) == "" {
		f.Open = nil
	}
	saveLedgerTrim(&f)
	return f, nil
}

// saveLedgerLoadAt reads the ledger at path, falling back to the .bak
// AtomicWrite keeps; a missing or unreadable one is an empty ledger. A live
// file that is damaged (or a newer version's) is kept aside as .damaged:
// left in place, the next write would copy it over the good backup.
func saveLedgerLoadAt(path string) saveLedgerFile {
	empty := saveLedgerFile{V: saveLedgerVersion}
	if path == "" {
		return empty
	}
	for i, p := range []string{path, path + ".bak"} {
		data, err := saveLedgerReadFile(p)
		if err != nil {
			if i == 0 && errors.Is(err, os.ErrNotExist) {
				return empty // a first run, not damage: no old backup comes back
			}
			continue
		}
		f, err := saveLedgerDecode(data)
		if err != nil {
			logWarn("saves", "ignoring a damaged save ledger (%s): %v", filepath.Base(p), err)
			if i == 0 {
				_ = os.Rename(p, p+".damaged")
			}
			continue
		}
		if i > 0 {
			logWarn("saves", "the save ledger was damaged; its backup was used")
		}
		return f
	}
	return empty
}

func saveLedgerReadFile(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	// One byte past the bound: saveLedgerDecode turns a file that big down
	// as damage, so it is kept aside like any other.
	return io.ReadAll(io.LimitReader(fh, saveLedgerReadMax+1))
}

// saveLedgerTrim drops the games touched longest ago past the bound. Pure.
func saveLedgerTrim(f *saveLedgerFile) {
	if len(f.Games) <= saveLedgerMaxGames {
		return
	}
	type aged struct {
		path string
		seq  uint64
	}
	all := make([]aged, 0, len(f.Games))
	for p, g := range f.Games {
		all = append(all, aged{p, g.Seq})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].seq != all[j].seq {
			return all[i].seq < all[j].seq
		}
		return all[i].path < all[j].path
	})
	for _, a := range all[:len(all)-saveLedgerMaxGames] {
		delete(f.Games, a.path)
	}
}

// saveLedgerGameFor is the record of path, made when missing. Pure.
func saveLedgerGameFor(f *saveLedgerFile, path, title, system string) *saveLedgerGame {
	if f.Games == nil {
		f.Games = map[string]*saveLedgerGame{}
	}
	g := f.Games[path]
	if g == nil {
		g = &saveLedgerGame{}
		f.Games[path] = g
	}
	if title != "" {
		g.Title = title
	}
	if system != "" {
		g.System = system
	}
	return g
}

// saveLedgerAdd adds an entry (its stamp already set) to path's record. A
// save during play replaces the same file's earlier one of the same session,
// so a game that writes its battery save every few seconds keeps one line,
// not five. Pure.
func saveLedgerAdd(f *saveLedgerFile, path, title, system string, e saveLedgerEntry) {
	g := saveLedgerGameFor(f, path, title, system)
	if e.Trigger == saveLedgerTrigPlay && e.Sess != 0 {
		kept := g.Saves[:0]
		for _, old := range g.Saves {
			if old.Sess == e.Sess && old.Trigger == saveLedgerTrigPlay && old.File == e.File && old.Kind == e.Kind {
				continue
			}
			kept = append(kept, old)
		}
		g.Saves = kept
	}
	g.Saves = append(g.Saves, e)
	if len(g.Saves) > saveLedgerKeep {
		g.Saves = append([]saveLedgerEntry(nil), g.Saves[len(g.Saves)-saveLedgerKeep:]...)
	}
	g.Seq = max(g.Seq, e.Seq)
	f.Seq = max(f.Seq, e.Seq)
	saveLedgerTrim(f)
}

// saveLedgerEnsureLoaded reads the ledger once, and settles a session the
// last run left open. Off the main thread.
func saveLedgerEnsureLoaded() {
	saveLedger.mu.Lock()
	loaded := saveLedger.loaded
	saveLedger.mu.Unlock()
	if loaded {
		return
	}
	f := saveLedgerLoadAt(saveLedgerPath())
	saveLedger.mu.Lock()
	if saveLedger.loaded {
		saveLedger.mu.Unlock()
		return
	}
	saveLedger.file, saveLedger.loaded = f, true
	settled := false
	if o := saveLedger.file.Open; o != nil {
		saveLedger.file.Open = nil
		settled = true
		how := saveLedgerEndGone
		if b := saveLedgerBootID(); o.Boot == "" || b == "" || o.Boot != b {
			how = saveLedgerEndLost
		}
		saveLedger.file.Seq++
		end := &saveLedgerEnd{saveLedgerStamp: saveLedgerStampNow(saveLedger.file.Seq), How: how, Long: true, Saved: o.Saved}
		g := saveLedgerGameFor(&saveLedger.file, o.Path, o.Title, o.System)
		g.End, g.Seq = end, end.Seq
		saveLedger.dirty = true
		if !o.Saved {
			logWarn("saves", "%s closed unexpectedly last time (%s) with no new save", saveLedgerTitle(o.Title, o.Path), how)
		}
	}
	saveLedgerGen.Add(1)
	saveLedger.mu.Unlock()
	if settled {
		saveLedgerPersist(true)
	}
}

// saveLedgerKickLoad starts the load from the main thread, once.
func saveLedgerKickLoad() {
	if saveLedgerLoadKick.CompareAndSwap(false, true) {
		go saveLedgerEnsureLoaded()
	}
}

// saveLedgerPersist writes the ledger now, or (now false) within
// saveLedgerWriteDelay. Off the main thread when now.
func saveLedgerPersist(now bool) {
	if !now {
		saveLedger.mu.Lock()
		if saveLedger.timer == nil {
			saveLedger.timer = time.AfterFunc(saveLedgerWriteDelay, func() { saveLedgerPersist(true) })
		}
		saveLedger.mu.Unlock()
		return
	}
	path := saveLedgerPath()
	if path == "" {
		return
	}
	saveLedgerWriteMu.Lock()
	defer saveLedgerWriteMu.Unlock()
	saveLedger.mu.Lock()
	if saveLedger.timer != nil {
		saveLedger.timer.Stop()
		saveLedger.timer = nil
	}
	if !saveLedger.loaded || !saveLedger.dirty {
		saveLedger.mu.Unlock()
		return
	}
	saveLedger.file.V = saveLedgerVersion
	data, err := json.Marshal(saveLedger.file)
	saveLedger.dirty = false
	saveLedger.mu.Unlock()
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		saveLedger.mu.Lock()
		saveLedger.dirty = true // the next write tries again
		saveLedger.mu.Unlock()
		logWarn("saves", "could not write the save ledger: %v", err)
	}
}

// saveLedgerFlushOnExit is the teardown's write (shutdown.go). A session
// still running is the game JukaHub's own exit is stopping, not one that
// ended unexpectedly: its open mark goes, so the next start does not call
// it lost when its own end could not be written in time. Writes only when
// something changed.
func saveLedgerFlushOnExit() {
	saveLedger.mu.Lock()
	if s, o := saveLedger.cur, saveLedger.file.Open; s != nil && o != nil && o.Path == s.game.Path {
		saveLedger.file.Open = nil
		saveLedger.dirty = true
	}
	saveLedger.mu.Unlock()
	saveLedgerPersist(true)
}

// ── Stamps ───────────────────────────────────────────────────────────────

func saveLedgerBootID() string {
	saveLedgerBoot.once.Do(func() { saveLedgerBoot.id = quickBootID() })
	return saveLedgerBoot.id
}

// saveLedgerStampNow is now, numbered seq. Any goroutine.
func saveLedgerStampNow(seq uint64) saveLedgerStamp {
	st := saveLedgerStamp{Seq: seq, Boot: saveLedgerBootID(), Run: saveLedgerRun, SleptNS: saveLedgerSlept.Load(), Date: vfDateNow()}
	if c, ok := wakeClocks(); ok {
		st.BootNS, st.MonoNS = int64(c.withSleep), int64(c.mono)
	}
	return st
}

// saveLedgerElapsed is how long ago st was, seen from now, when both are in
// one boot (one run where the kernel has no boot id); ok false otherwise.
// The longer of BOOTTIME's count and the monotonic one plus the sleep the
// wake hooks measured: a kernel that leaves the suspend out of BOOTTIME
// would otherwise make hours asleep look like minutes. Pure.
func saveLedgerElapsed(st, now saveLedgerStamp) (time.Duration, bool) {
	same := st.Boot != "" && st.Boot == now.Boot
	if st.Boot == "" && now.Boot == "" {
		same = st.Run != "" && st.Run == now.Run
	}
	if !same || st.BootNS == 0 || now.BootNS == 0 {
		return 0, false
	}
	d := time.Duration(now.BootNS - st.BootNS)
	if st.Run != "" && st.Run == now.Run {
		alt := time.Duration(now.MonoNS-st.MonoNS) + time.Duration(now.SleptNS-st.SleptNS)
		d = max(d, alt)
	}
	if d < 0 {
		return 0, false
	}
	return d, true
}

// ── The files of a game ──────────────────────────────────────────────────

// saveLedgerKindOf sorts one of the game's save files by its name. Pure.
func saveLedgerKindOf(name string, g shoGame) string {
	lower := strings.ToLower(name)
	if gameResetStateFile(lower, g.stem) {
		if strings.HasSuffix(lower, ".auto") {
			return saveLedgerKindAuto
		}
		return saveLedgerKindState
	}
	if strings.HasSuffix(lower, ".dss") {
		return saveLedgerKindEmu
	}
	if g.romFile != "" {
		if rest, ok := strings.CutPrefix(lower, g.romFile+"."); ok && len(rest) == 3 && strings.HasPrefix(rest, "st") && shoDigits(rest[2:]) {
			return saveLedgerKindEmu
		}
	}
	return saveLedgerKindSave
}

// saveLedgerWalk lists the game's save files in roots (not their
// screenshots, nor the previous auto save JukaHub keeps), bounded in entries,
// depth and, when deadline is set, time.
func saveLedgerWalk(roots []saveRoot, g shoGame, deadline time.Time) map[string]saveLedgerSeen {
	out := map[string]saveLedgerSeen{}
	visited := 0
	for _, root := range roots {
		rootPath := root.Path
		if rootPath == "" {
			continue
		}
		stopped := false
		_ = filepath.WalkDir(rootPath, func(p string, d os.DirEntry, err error) error {
			visited++
			if visited > saveLedgerWalkMax || (!deadline.IsZero() && visited%32 == 0 && time.Now().After(deadline)) {
				stopped = true
				return filepath.SkipAll
			}
			if err != nil || d == nil {
				return nil
			}
			rel, relErr := filepath.Rel(rootPath, p)
			if relErr != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel != "." && strings.Count(rel, "/")+1 > saveLedgerWalkDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !isSaveFile(p) || strings.HasSuffix(strings.ToLower(rel), ".png") {
				return nil
			}
			if match, _ := shoFileKind(rel, g); !match {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out[p] = saveLedgerSeen{size: info.Size(), mtime: info.ModTime().UnixNano()}
			return nil
		})
		if stopped {
			break
		}
	}
	return out
}

// ── Reading a save back ──────────────────────────────────────────────────

// saveLedgerCheck reads a save back: its size, its CRC-32 (files up to
// saveLedgerCRCMax, when full) and why it failed ("" when it passed,
// saveLedgerChanging when it changed under the read). Without full, only the
// layout is checked. Off the main thread.
func saveLedgerCheck(path, kind string, full bool) (size int64, crc, bad string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", "gone"
		}
		return 0, "", "unreadable"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, "", "unreadable"
	}
	size = info.Size()
	limit := int64(saveLedgerStateMax)
	if kind == saveLedgerKindSave {
		limit = saveLedgerSaveMax
	}
	switch {
	case size == 0:
		return 0, "", "empty"
	case size > limit:
		return size, "", "too large to be a save"
	}
	head := make([]byte, 20)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return size, "", "unreadable"
	}
	if why := saveLedgerLayoutCheck(f, head[:n], size); why != "" {
		return size, "", why
	}
	if size > saveLedgerCRCMax {
		// The last block, so the end of a big state is known to be there.
		tail := make([]byte, min(size, 64<<10))
		if _, err := f.ReadAt(tail, size-int64(len(tail))); err != nil {
			return size, "", "unreadable"
		}
		return size, "", ""
	}
	if !full {
		return size, "", ""
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return size, "", "unreadable"
	}
	h := crc32.NewIEEE()
	read, err := io.Copy(h, f)
	if err != nil {
		return size, "", "unreadable"
	}
	if read != size {
		return size, "", saveLedgerChanging
	}
	if after, err := os.Stat(path); err != nil || after.Size() != size || !after.ModTime().Equal(info.ModTime()) {
		return size, "", saveLedgerChanging
	}
	return size, fmt.Sprintf("%08x", h.Sum32()), ""
}

// saveLedgerLayoutCheck checks a RetroArch state's own layout where the file
// has one: the "#RZIPv" container (header: magic, chunk size uint32, total
// size uint64, then chunks of uint32 length + deflate data) must hold every
// chunk the total needs and end with the last; an uncompressed "RASTATE"
// (magic, version, then the "MEM " block's uint32 length) must hold its
// memory block. Anything else — a battery save, an older RetroArch's raw
// state — has no layout to check. Little-endian throughout.
func saveLedgerLayoutCheck(r io.ReadSeeker, head []byte, size int64) string {
	switch {
	case bytes.HasPrefix(head, []byte("#RZIPv")):
		return saveLedgerRZIPCheck(r, head, size)
	case bytes.HasPrefix(head, []byte("RASTATE")):
		if len(head) < 16 {
			return "cut short"
		}
		if string(head[8:12]) != "MEM " {
			return "" // a layout this check does not know
		}
		if mem := int64(binary.LittleEndian.Uint32(head[12:16])); 16+mem > size {
			return "cut short"
		}
	}
	return ""
}

func saveLedgerRZIPCheck(r io.ReadSeeker, head []byte, size int64) string {
	if len(head) < 20 || head[7] != '#' {
		return "cut short"
	}
	chunk := int64(binary.LittleEndian.Uint32(head[8:12]))
	total := binary.LittleEndian.Uint64(head[12:20])
	if chunk <= 0 || total == 0 || total > 1<<36 {
		return "damaged"
	}
	want := (int64(total) + chunk - 1) / chunk
	pos, chunks := int64(20), int64(0)
	var lenBuf [4]byte
	for pos < size {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return "unreadable"
		}
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return "cut short"
		}
		n := int64(binary.LittleEndian.Uint32(lenBuf[:]))
		if n == 0 {
			// Deflate never writes an empty chunk: zeros a power cut left
			// (and a walk four bytes at a time through them).
			return "damaged"
		}
		pos += 4 + n
		chunks++
		if chunks > want {
			return "damaged"
		}
	}
	if pos != size || chunks != want {
		return "cut short"
	}
	return ""
}

// ── Sessions ─────────────────────────────────────────────────────────────

// saveLedgerStarted is the library launch's started hook (main thread): a
// session for a game that keeps emulator saves. No disk work here.
func saveLedgerStarted(game libraryGame, launcher string, _ *exec.Cmd) {
	if !shoApplies(game) {
		return
	}
	s := &saveLedgerSession{
		game:         game,
		g:            shoGameOf(game),
		launcher:     launcher,
		startedAt:    time.Now(),
		actedAtStart: lowBatteryGuardActs(),
		resume:       saveLedgerTakeResume(game.Path),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	saveLedger.mu.Lock()
	old := saveLedger.cur
	saveLedger.cur = s
	saveLedger.mu.Unlock()
	if old != nil {
		// Its exited hook has not run yet (the reaper still finishing up, a
		// time-lapse being assembled): the game ended, it did not crash with
		// JukaHub, so no "closed unexpectedly" for it.
		how, trigger := saveLedgerHowEnded(old, nil)
		go old.finish(how, trigger, time.Since(old.startedAt))
	}
	go s.run()
}

// saveLedgerExited is the exited hook (reaper goroutine): the session ends
// on its own goroutine.
func saveLedgerExited(game libraryGame, _ string, ranFor time.Duration, waitErr error) {
	saveLedger.mu.Lock()
	s := saveLedger.cur
	saveLedger.mu.Unlock()
	// ranFor counts from just after the started hooks, so the current
	// session is always older; an exit that ran longer than it is an earlier
	// launch's (the same game started again before that reaper was done).
	if s == nil || s.game.Path != game.Path || ranFor > time.Since(s.startedAt) {
		return
	}
	how, trigger := saveLedgerHowEnded(s, waitErr)
	go s.finish(how, trigger, ranFor)
}

// saveLedgerHowEnded tells how a session ended: a game that exits while a
// sleep holds it was saved and closed by the sleep's power off; Power off
// saves first too; the low-battery guard's run, whether it began in this
// session; then a crash signal; else the player quit.
func saveLedgerHowEnded(s *saveLedgerSession, waitErr error) (how, trigger string) {
	_, _, running := lowBatteryGuardFlags()
	switch {
	case powerSleepActive() || quickPowerOff.busy.Load() || quickPowerOff.silent.Load():
		return saveLedgerTrigPowerOff, saveLedgerTrigPowerOff
	case running || lowBatteryGuardActs() > s.actedAtStart:
		return saveLedgerTrigBattery, saveLedgerTrigBattery
	}
	if _, sig := gameLaunchExitStatus(waitErr); saveLedgerCrashSignal(sig) {
		return saveLedgerEndCrash, saveLedgerTrigPlay
	}
	return saveLedgerTrigQuit, saveLedgerTrigQuit
}

// saveLedgerCrashSignal reports a signal an emulator dies of by itself
// (SIGILL, SIGABRT, SIGBUS, SIGFPE, SIGSEGV); JukaHub's own stops send TERM,
// INT or KILL. Pure.
func saveLedgerCrashSignal(sig int) bool {
	switch sig {
	case 4, 6, 7, 8, 11:
		return true
	}
	return false
}

// saveLedgerFinishNow ends the running session at once, its saves read back
// and the ledger on the card: the power off calls it right before the device
// goes down (quickresume.go). trigger names what saved it. Off the main
// thread; bounded.
func saveLedgerFinishNow(trigger string) {
	saveLedger.mu.Lock()
	s := saveLedger.cur
	saveLedger.mu.Unlock()
	if s == nil {
		return
	}
	s.finish(trigger, trigger, time.Since(s.startedAt))
}

// saveLedgerCurrent is the running session, if any.
func saveLedgerCurrent() *saveLedgerSession {
	saveLedger.mu.Lock()
	defer saveLedger.mu.Unlock()
	return saveLedger.cur
}

// run takes the first list of the game's saves and looks again every
// saveLedgerPoll until the session ends.
func (s *saveLedgerSession) run() {
	saveLedgerEnsureLoaded()
	s.retroArch.Store(retroArchLauncher(s.launcher))
	roots := saveManagerRoots()
	s.scanMu.Lock()
	if s.finishing.Load() {
		s.scanMu.Unlock()
		return
	}
	s.roots = roots
	s.base = saveLedgerWalk(roots, s.g, time.Time{})
	saveLedger.mu.Lock()
	s.startSeq, s.ready = saveLedger.file.Seq, true
	saveLedger.mu.Unlock()
	s.scanMu.Unlock()
	if s.resume {
		go saveLedgerResumeMessage(s)
	}
	tick := time.NewTicker(saveLedgerPoll)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		if powerSleepActive() || s.finishing.Load() {
			continue
		}
		s.noteLong()
		first, n := s.scan(saveLedgerTrigPlay, time.Time{}, saveLedgerSettle)
		switch {
		case first:
			saveLedgerPersist(true)
		case n > 0:
			saveLedgerPersist(false)
		}
	}
}

// noteLong marks the session open on the card once it ran saveLedgerLongRun:
// one still open at the next start ended with JukaHub or the power.
func (s *saveLedgerSession) noteLong() {
	if time.Since(s.startedAt) < saveLedgerLongRun {
		return
	}
	saveLedger.mu.Lock()
	if s.long || s.finishing.Load() {
		saveLedger.mu.Unlock()
		return
	}
	s.long = true
	f := &saveLedger.file
	f.Open = &saveLedgerOpen{saveLedgerStamp: saveLedgerStampNow(f.Seq), Path: s.game.Path, Title: s.game.Title, System: s.game.System, Saved: s.saved}
	saveLedger.dirty = true
	saveLedger.mu.Unlock()
	saveLedgerPersist(true)
}

// scan looks through the game's saves and reads back each one that changed
// since the last look and has stopped changing (settle), within deadline. It
// reports whether this was the session's first confirmed save and how many
// entries it recorded.
func (s *saveLedgerSession) scan(trigger string, deadline time.Time, settle time.Duration) (first bool, recorded int) {
	if deadline.IsZero() {
		s.scanMu.Lock()
	} else if !saveLedgerLockBy(&s.scanMu, deadline) {
		return false, 0 // another look is under way: it records what changed
	}
	defer s.scanMu.Unlock()
	if s.base == nil {
		return false, 0
	}
	type change struct {
		path string
		seen saveLedgerSeen
	}
	var changes []change
	for p, seen := range saveLedgerWalk(s.roots, s.g, deadline) {
		if old, ok := s.base[p]; ok && old == seen {
			continue
		}
		changes = append(changes, change{p, seen})
	}
	if len(changes) == 0 {
		return false, 0
	}
	// Oldest first: the newest save gets the highest number.
	sort.Slice(changes, func(i, j int) bool { return changes[i].seen.mtime < changes[j].seen.mtime })
	if settle > 0 {
		time.Sleep(settle)
	}
	final := trigger != saveLedgerTrigPlay && trigger != saveLedgerTrigSleep
	for _, c := range changes {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		info, err := os.Stat(c.path)
		if err != nil || info.Size() != c.seen.size || info.ModTime().UnixNano() != c.seen.mtime {
			continue // still being written, or gone again: the next look
		}
		kind := saveLedgerKindOf(filepath.Base(c.path), s.g)
		full := trigger != saveLedgerTrigSleep || c.seen.size <= saveLedgerSleepFullMax
		size, crc, bad := saveLedgerCheck(c.path, kind, full)
		if bad == saveLedgerChanging || (bad != "" && !final && trigger == saveLedgerTrigSleep) {
			continue
		}
		if bad != "" && !final {
			// While the game runs, a failed read is read once more before it
			// counts: the emulator may have started writing again.
			time.Sleep(time.Second)
			if size, crc, bad = saveLedgerCheck(c.path, kind, full); bad == saveLedgerChanging {
				continue
			}
		}
		s.base[c.path] = c.seen
		if f, ok := s.record(saveLedgerEntry{Kind: kind, File: filepath.Base(c.path), Size: size, CRC: crc, Trigger: trigger, Bad: bad}); ok {
			recorded++
			first = first || f
		}
	}
	return first, recorded
}

// saveLedgerLockBy takes mu, waiting until deadline at most: the sleep's
// budget must not wait out a look in progress.
func saveLedgerLockBy(mu *sync.Mutex, deadline time.Time) bool {
	for !mu.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// record adds an entry for the session: not when it is the same file, same
// size and CRC as the newest entry for that file (written again unchanged).
// It reports whether this was the session's first confirmed save and whether
// anything was added.
func (s *saveLedgerSession) record(e saveLedgerEntry) (first, added bool) {
	saveLedger.mu.Lock()
	f := &saveLedger.file
	if g := f.Games[s.game.Path]; g != nil && e.Bad == "" && e.CRC != "" {
		for i := len(g.Saves) - 1; i >= 0; i-- {
			if old := g.Saves[i]; old.File == e.File {
				if old.Bad == "" && old.CRC == e.CRC && old.Size == e.Size {
					saveLedger.mu.Unlock()
					return false, false
				}
				break
			}
		}
	}
	f.Seq++
	e.saveLedgerStamp = saveLedgerStampNow(f.Seq)
	e.Sess = s.startSeq + 1
	if e.Bad == "" && !s.saved {
		s.saved, first = true, true
		if f.Open != nil && f.Open.Path == s.game.Path {
			f.Open.Saved = true
		}
	}
	saveLedgerAdd(f, s.game.Path, s.game.Title, s.game.System, e)
	saveLedger.dirty = true
	saveLedgerGen.Add(1)
	saveLedger.mu.Unlock()
	title := saveLedgerTitle(s.game.Title, s.game.Path)
	switch {
	case e.Bad != "":
		logWarn("saves", "%s: %s %s did not read back (%s)", title, saveLedgerKindWord(e.Kind), e.File, e.Bad)
	case e.Trigger == saveLedgerTrigPlay:
		logDebug("saves", "%s: %s %s read back (%d bytes)", title, saveLedgerKindWord(e.Kind), e.File, e.Size)
	default:
		logInfo("saves", "%s: %s %s read back (%d bytes, %s)", title, saveLedgerKindWord(e.Kind), e.File, e.Size, e.Trigger)
	}
	if e.Bad == "" {
		// The Save Time Machine keeps a moment of it (savetimeline.go); the
		// scan lock held here guards s.roots.
		saveTimelineNoteSave(s.game, s.roots)
	}
	return first, true
}

// finish ends the session: a last look at the saves (the emulator is gone),
// then how it ended, and the ledger to the card. A second call waits for the
// first. Off the main thread.
func (s *saveLedgerSession) finish(how, trigger string, ran time.Duration) {
	if !s.finishing.CompareAndSwap(false, true) {
		select {
		case <-s.done:
		case <-time.After(saveLedgerFinishWait):
		}
		return
	}
	defer close(s.done)
	close(s.stop)
	saveLedgerEnsureLoaded()
	s.scan(trigger, time.Now().Add(saveLedgerFinishBudget), 0)
	saveLedger.mu.Lock()
	f := &saveLedger.file
	f.Seq++
	end := &saveLedgerEnd{
		saveLedgerStamp: saveLedgerStampNow(f.Seq),
		How:             how,
		RanS:            int64(ran / time.Second),
		Long:            ran >= saveLedgerLongRun,
		Saved:           s.saved,
	}
	g := saveLedgerGameFor(f, s.game.Path, s.game.Title, s.game.System)
	g.End, g.Seq = end, end.Seq
	if f.Open != nil && f.Open.Path == s.game.Path {
		f.Open = nil
	}
	if saveLedger.cur == s {
		saveLedger.cur = nil
	}
	saveLedger.dirty = true
	saveLedgerGen.Add(1)
	saveLedger.mu.Unlock()
	if how == saveLedgerEndCrash && end.Long && !end.Saved {
		logWarn("saves", "%s crashed after %s with no new save", saveLedgerTitle(s.game.Title, s.game.Path), ran.Round(time.Second))
	}
	saveLedgerPersist(true)
	// The Save Time Machine's last moment of the session (savetimeline.go).
	s.scanMu.Lock()
	roots := s.roots
	s.scanMu.Unlock()
	saveTimelineSessionEnded(s.game, roots, how)
}

// ── Sleep and wake ───────────────────────────────────────────────────────

// saveLedgerBeforeSleep takes the running game's saves into the ledger, and
// the ledger onto the card, before the sync (sleephooks.go). Never a save
// request: only what the game already wrote.
func saveLedgerBeforeSleep(string) {
	s := saveLedgerCurrent()
	if s == nil || s.finishing.Load() {
		return
	}
	s.scan(saveLedgerTrigSleep, time.Now().Add(saveLedgerSleepBudget), 300*time.Millisecond)
	// Writes only when something changed (a new entry, a delayed write).
	saveLedgerPersist(true)
}

// saveLedgerWake counts the sleep, and under a RetroArch game says on its
// screen that the progress is safe, once per wake, when this session has a
// confirmed save (sleephooks.go).
func saveLedgerWake(_ string, slept time.Duration, suspended bool) {
	if slept > 0 {
		saveLedgerSlept.Add(int64(slept))
	}
	if !suspended || quickPowerOffSilent() || exitRequested.Load() || quietModeOn() {
		return
	}
	s := saveLedgerCurrent()
	if s == nil || s.finishing.Load() || !s.retroArch.Load() {
		return
	}
	if msg, ok := saveLedgerWakeWords(s); ok {
		go saveLedgerShowInGame("Progress safe: "+msg, time.Second)
	}
}

// saveLedgerShowInGame puts a line on RetroArch's own message line, after
// wait. Nothing when RetroArch is gone or its commands are off.
func saveLedgerShowInGame(msg string, wait time.Duration) {
	time.Sleep(wait)
	cfg, _, ok := retroArchConfig()
	if !ok || !networkCmdEnabled(cfg) || !retroArchRunning() {
		return
	}
	_ = sendRetroArchNetCmd(networkCmdPort(cfg), "SHOW_MSG "+msg)
}

// saveLedgerResumeMessage says, once RetroArch plays the game the power-on
// resume started, where the game came back from. RetroArch that never
// answers GET_STATUS (an old build) gets the line after a while anyway.
func saveLedgerResumeMessage(s *saveLedgerSession) {
	if !s.retroArch.Load() || quietModeOn() {
		return
	}
	msg := saveLedgerResumeWords(s.game.Path)
	cfg, _, ok := retroArchConfig()
	if !ok || !networkCmdEnabled(cfg) {
		return
	}
	port := networkCmdPort(cfg)
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)
		if s.finishing.Load() {
			return
		}
		reply, err := askRetroArchNetCmd(port, "GET_STATUS", 500*time.Millisecond)
		if (err == nil && raStatusWord(reply) == "PLAYING") || (err != nil && i >= 12) {
			_ = sendRetroArchNetCmd(port, "SHOW_MSG "+msg)
			return
		}
	}
}

// ── Low battery and sleep ────────────────────────────────────────────────

var saveLedgerCap struct {
	mu sync.Mutex
	// at is the reading's CLOCK_BOOTTIME (wakeClocks), not Go's monotonic
	// clock, which stands still in a suspend: a reading from before a sleep
	// (another charge, a charger since plugged in) is never reused after it.
	at     time.Duration
	have   bool
	val    time.Duration
	logged time.Duration
}

// saveLedgerCapFor is the nearer power-off time a sleep with a game gets on
// battery: 10 min at the guard's 5% or less (the low-battery hold's sleep,
// instantresume.go), 30 min at 15% or less, 2 h at 25% or less, none
// otherwise (also on a charger, at an unknown level, or with no game). Pure.
func saveLedgerCapFor(percent int, charging, game bool) time.Duration {
	switch {
	case !game || charging || percent < 0:
		return 0
	case percent <= lowBatteryWarnPct:
		return 10 * time.Minute
	case percent <= 15:
		return 30 * time.Minute
	case percent <= 25:
		return 2 * time.Hour
	}
	return 0
}

// saveLedgerSleepCap is saveLedgerCapFor for now, read at most every
// saveLedgerCapCache: quickSleepDeadline takes the nearer of it and Power off
// after sleeping (even when that is Off), so a game left asleep on a low
// battery is saved and the device powered off before the battery dies. Any
// goroutine.
func saveLedgerSleepCap() time.Duration {
	game := len(liveScreenChildren()) > 0
	saveLedgerCap.mu.Lock()
	defer saveLedgerCap.mu.Unlock()
	if !game {
		// Nothing to read, and nothing cached: a game started a moment ago
		// and put to sleep at once gets its own reading.
		saveLedgerCap.have, saveLedgerCap.logged = false, 0
		return 0
	}
	clocks, clocksOK := wakeClocks()
	if age := clocks.withSleep - saveLedgerCap.at; clocksOK && saveLedgerCap.have && age >= 0 && age < saveLedgerCapCache {
		return saveLedgerCap.val
	}
	// Read now, not getBatteryPercent's cache (quickSleepAtDeadline's rule).
	percent := batteryCalCorrect(batteryPercentIn(batterySysfsDir()))
	charging := batteryChargingIn(powerSupplyRoot)
	v := saveLedgerCapFor(percent, charging, game)
	saveLedgerCap.at, saveLedgerCap.have, saveLedgerCap.val = clocks.withSleep, clocksOK, v
	if v != saveLedgerCap.logged {
		saveLedgerCap.logged = v
		if v > 0 {
			logInfo("sleep", "battery at %d%% with a game running: a sleep powers off within %s, the game saved first", percent, sleepPowerOffLabel(v))
		}
	}
	return v
}

// ── RetroArch's battery-save flush ───────────────────────────────────────

// saveFlushSeconds is the SRAM autosave interval JukaHub asks RetroArch for
// in its launches (savestateSRAMFlushValue): saveFlushKey, default
// saveFlushDefault, 0 to leave RetroArch's own setting alone. Main thread (it
// reads appConfig).
func saveFlushSeconds() int {
	if appConfig == nil {
		return saveFlushDefault
	}
	v, ok := customValue(appConfig.Variables.Custom, saveFlushKey)
	if !ok {
		return saveFlushDefault
	}
	n, ok := coerceInt(v)
	if !ok || n < 0 {
		return saveFlushDefault
	}
	return min(n, saveFlushMax)
}

// ── Words shared with the page ───────────────────────────────────────────

func saveLedgerTitle(title, path string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// saveLedgerKindWord names a kind of save in plain words.
func saveLedgerKindWord(kind string) string {
	switch kind {
	case saveLedgerKindState:
		return "save state"
	case saveLedgerKindAuto:
		return "auto save state"
	case saveLedgerKindEmu:
		return "emulator save"
	}
	return "in-game save"
}
