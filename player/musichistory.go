package main

// musichistory.go — what the music player played (backlog MED-17): a bounded
// local log of listens, and the Music screen's History shelf built from it —
// Continue listening, Recently played and Top artists.
//
// A listen begins when a track starts (musicPlayTrackAt) or plays on after a
// pause, and ends when it is paused, stopped, replaced by the next track or
// the list runs out. Its seconds are the time it was heard, not how far the
// position moved, so a seek neither adds nor takes away. A pause ends one
// listen and Play carries the same entry on (musicHistoryMergeGap), so a song
// paused twice is still one play. A listen shorter than musicHistoryMinSecs was
// a skip and is not kept.
//
// The log is musichistory.json in the data folder: 400 days of it, and never
// more than musicHistoryMaxEntries entries (about 1 MB), so the backup's
// "Library & history" carries it like the other music stores (bkupCollect
// takes every store of the data folder under 2 MB). It is read and written on
// goroutines, never on the render thread, through AtomicWrite and
// readJSONWithBackup like the favorites. Remote audio — radio, podcasts — is
// not logged: its URLs can carry tokens, and the podcast screen keeps its own
// episode places.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	musicHistoryFileName = "musichistory.json"
	// musicHistoryKeepDays is how long a listen is kept: a year in review and
	// a margin (the window LIB-13 asks of playtime).
	musicHistoryKeepDays = 400
	// musicHistoryMaxEntries bounds the file whatever the dates say: about
	// 1 MB of JSON, under the 2 MB a backup takes from the data folder.
	musicHistoryMaxEntries = 4000
	// musicHistoryMinSecs is the shortest listen that counts; less was a skip.
	musicHistoryMinSecs = 10
	// musicHistoryMergeGap is how soon after a listen ended the same track
	// must play on for both to be one listen (a pause, then Play).
	musicHistoryMergeGap = 30 * time.Minute
	// musicHistoryMaxListen bounds one listen's seconds whatever the clock
	// says (a list replaced while nothing played, a length never probed).
	musicHistoryMaxListen = 8 * time.Hour
	// musicHistoryPendingMax bounds the listens that end before the file has
	// been read.
	musicHistoryPendingMax = 64
	// The History shelf's sections.
	musicHistoryContinueRows = 6
	musicHistoryRecentRows   = 20
	musicHistoryArtistRows   = 10
)

// musicListen is one entry of the log.
type musicListen struct {
	Path   string  `json:"path"`
	Title  string  `json:"title,omitempty"`
	Artist string  `json:"artist,omitempty"`
	Start  int64   `json:"start"`          // when it began, Unix seconds
	Secs   int64   `json:"secs"`           // seconds heard
	Pos    float64 `json:"pos,omitempty"`  // where it stopped, seconds into the track
	Dur    float64 `json:"dur,omitempty"`  // the track's length; 0 while unknown
	Done   bool    `json:"done,omitempty"` // it played to its end
}

// musicHistoryFile is the file's shape.
type musicHistoryFile struct {
	Version int           `json:"version"`
	Listens []musicListen `json:"listens"` // oldest first
}

// musicListenOpen is the listen in progress.
type musicListenOpen struct {
	active bool
	path   string
	title  string
	artist string
	began  time.Time // read with its monotonic clock: the seconds heard
	at     float64   // the position it began at
}

// What is known of the file.
const (
	musicHistUnread = iota
	musicHistReading
	musicHistRead
)

var musicHist struct {
	mu      sync.Mutex
	state   int
	loadGen int           // bumps on a clear or a restore: a read in flight is stale
	listens []musicListen // oldest first; only while state is musicHistRead
	pending []musicListen // ended while the file was still being read
	open    musicListenOpen
	// lastPath and lastEnd are the latest listen kept, so a pause and Play
	// carry the same entry on.
	lastPath string
	lastEnd  time.Time
	// dirty asks the writer for another save; saving is a writer running;
	// wipeBak removes the .bak AtomicWrite keeps, once a clear is saved.
	dirty, saving, wipeBak bool
	// disk is the file as this process last read or wrote it: a save never
	// replaces a file someone else (a backup restore) wrote since.
	disk musicHistStamp
}

// musicHistStamp is how the log file looked: there or not, its size and its
// modification time. known is false when it could not be told.
type musicHistStamp struct {
	known  bool
	exists bool
	size   int64
	mod    time.Time
}

// musicHistoryStampOf stats the log file.
func musicHistoryStampOf(path string) musicHistStamp {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return musicHistStamp{known: true}
		}
		return musicHistStamp{}
	}
	return musicHistStamp{known: true, exists: true, size: info.Size(), mod: info.ModTime()}
}

// replacedSince reports whether the file now (cur) is no longer the one this
// process last saw (s). Unknown either way is not a replacement.
func (s musicHistStamp) replacedSince(cur musicHistStamp) bool {
	if !s.known || !cur.known {
		return false
	}
	return s.exists != cur.exists || s.size != cur.size || !s.mod.Equal(cur.mod)
}

// musicHistoryWipeTries and musicHistoryWipeRetry bound the saves a clear
// tries in a row when the card will not take the empty log: the wipe stays
// pending after that, for the next save.
const (
	musicHistoryWipeTries = 5
	musicHistoryWipeRetry = 5 * time.Second
)

// musicHistoryRev moves whenever the log changes; the History shelf rebuilds
// on it.
var musicHistoryRev atomic.Uint64

// musicHistoryFilePath is a variable so tests can point the log at a temp file.
var musicHistoryFilePath = defaultMusicHistoryFilePath

func defaultMusicHistoryFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, musicHistoryFileName)
}

// musicHistoryLogs reports whether a track is kept in the log: a file, not a
// stream.
func musicHistoryLogs(path string) bool {
	path = strings.TrimSpace(path)
	return path != "" && !isHTTPMediaURL(path) && !strings.Contains(path, "://")
}

// musicHistoryMeta is the title and the artist a listen of path is kept with:
// the library's tags when it knows the file, else the playlist's name for it
// or the file's.
func musicHistoryMeta(path, name string) (string, string) {
	title, artist := "", ""
	if m := mlibCurrentModel(); m != nil {
		if i, ok := m.byPath[dlPathKey(path)]; ok {
			title, artist = m.items[i].Title, m.items[i].Artist
		}
	}
	name = strings.TrimSpace(name)
	if isAudioFile(name) {
		name = strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
	}
	return dlFirstNonEmpty(title, name, dlTitleFromFile(path)), strings.TrimSpace(artist)
}

// ── Recording ────────────────────────────────────────────────────────────────

// musicHistoryBegin ends the listen in progress — prevPath, stopped at prevPos
// of prevDur as musicPlayTrackAt saw it — and starts one of path (name is the
// playlist's title for it) from position at. Any thread.
func musicHistoryBegin(prevPath string, prevPos, prevDur float64, path, name string, at float64) {
	now := time.Now()
	logged := musicHistoryLogs(path)
	title, artist := "", ""
	if logged {
		title, artist = musicHistoryMeta(path, name)
	}
	musicHist.mu.Lock()
	defer musicHist.mu.Unlock()
	musicHistoryEndLocked(prevPath, prevPos, prevDur, false, now)
	if !logged {
		return
	}
	musicHistoryReadLocked()
	musicHist.open = musicListenOpen{active: true, path: path, title: title, artist: artist, began: now, at: at}
}

// musicHistoryEnd ends the listen in progress: path stopped at pos of dur, and
// done says it played to its end. Any thread.
func musicHistoryEnd(path string, pos, dur float64, done bool) {
	now := time.Now()
	musicHist.mu.Lock()
	musicHistoryEndLocked(path, pos, dur, done, now)
	musicHist.mu.Unlock()
}

// musicHistoryPaused ends the listen at a pause and carries it on at Play.
func musicHistoryPaused(paused bool, path string, pos, dur float64) {
	if paused {
		musicHistoryEnd(path, pos, dur, false)
		return
	}
	musicHistoryBegin("", 0, 0, path, "", pos)
}

func musicHistoryEndLocked(path string, pos, dur float64, done bool, now time.Time) {
	o := musicHist.open
	if !o.active {
		return
	}
	musicHist.open = musicListenOpen{}
	heard := now.Sub(o.began).Seconds()
	if strings.TrimSpace(path) == "" || !sameAudioPath(path, o.path) {
		// It ended without word of where (a new list took over): about where
		// the time heard took it.
		pos, dur, done = o.at+heard, 0, false
	}
	if dur > 0 {
		if pos >= dur-2 {
			done = true
		}
		// A list that replaced it without a stop can leave the clock running
		// with nothing playing: no track is heard for longer than it lasts at
		// the slowest speed (0.75x).
		if limit := (dur-o.at)/0.75 + 5; limit > 0 && heard > limit {
			heard = limit
		}
	}
	if heard > musicHistoryMaxListen.Seconds() {
		heard = musicHistoryMaxListen.Seconds()
	}
	if pos < 0 {
		pos = 0
	}
	l := musicListen{Path: o.path, Title: o.title, Artist: o.artist, Start: o.began.Unix(),
		Secs: int64(heard), Pos: musicHistoryRound(pos), Dur: musicHistoryRound(dur), Done: done}
	musicHistoryAddLocked(l, o.began, now)
}

// musicHistoryRound keeps a tenth of a second: enough for "stopped at".
func musicHistoryRound(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// musicHistoryAddLocked keeps a finished listen: carried into the latest entry
// when it is the same track playing on, else a new entry unless it was a skip.
func musicHistoryAddLocked(l musicListen, began, now time.Time) {
	merge := musicHist.lastPath != "" && sameAudioPath(musicHist.lastPath, l.Path) &&
		!musicHist.lastEnd.IsZero() && began.Sub(musicHist.lastEnd) <= musicHistoryMergeGap
	if !merge && l.Secs < musicHistoryMinSecs {
		return
	}
	musicHist.lastPath, musicHist.lastEnd = l.Path, now
	if musicHist.state != musicHistRead {
		musicHistoryReadLocked() // a restore left it unread
		musicHist.pending, _ = musicHistoryAppend(musicHist.pending, l, merge)
		if over := len(musicHist.pending) - musicHistoryPendingMax; over > 0 {
			musicHist.pending = musicHist.pending[over:]
		}
		return
	}
	list, changed := musicHistoryAppend(musicHist.listens, l, merge)
	if !changed {
		return
	}
	musicHist.listens = musicHistoryPrune(list, now)
	musicHistoryChangedLocked()
}

// musicHistoryAppend adds l to list (oldest first): into the last entry when
// merge is set and that entry is the same track, else as a new entry unless it
// was a skip. It reports whether list changed.
func musicHistoryAppend(list []musicListen, l musicListen, merge bool) ([]musicListen, bool) {
	// A track that played to its end and starts again (repeat one, a replay)
	// is a new play.
	if n := len(list); merge && n > 0 && !list[n-1].Done && sameAudioPath(list[n-1].Path, l.Path) {
		last := &list[n-1]
		last.Secs += l.Secs
		last.Pos = l.Pos
		if l.Dur > 0 {
			last.Dur = l.Dur
		}
		last.Done = l.Done
		return list, true
	}
	if l.Secs < musicHistoryMinSecs {
		return list, false
	}
	return append(list, l), true
}

// musicHistoryPrune drops what the log no longer keeps: entries without a
// path, listens older than musicHistoryKeepDays, and the oldest past
// musicHistoryMaxEntries. The age is only judged on a clock that has been set:
// a handheld still at its power-on date has no idea what 400 days ago was.
// list is oldest first; the result may share its array.
func musicHistoryPrune(list []musicListen, now time.Time) []musicListen {
	cutoff := int64(0)
	if !now.Before(clockFloor()) {
		cutoff = now.AddDate(0, 0, -musicHistoryKeepDays).Unix()
	}
	kept := list[:0]
	for _, l := range list {
		if strings.TrimSpace(l.Path) == "" || l.Secs < 0 || (cutoff > 0 && l.Start < cutoff) {
			continue
		}
		kept = append(kept, l)
	}
	if over := len(kept) - musicHistoryMaxEntries; over > 0 {
		kept = kept[over:]
	}
	return kept
}

// ── The file ─────────────────────────────────────────────────────────────────

// musicHistoryReadFile reads the log at path, oldest first.
func musicHistoryReadFile(path string) ([]musicListen, error) {
	var stored musicHistoryFile
	if _, err := readJSONWithBackup(path, &stored); err != nil {
		return nil, err
	}
	return stored.Listens, nil
}

// musicHistoryWriteFile writes list to path the crash-safe way (temp, fsync,
// rename, the previous version kept as .bak). Compact: the file can hold
// thousands of entries.
func musicHistoryWriteFile(path string, list []musicListen) error {
	data, err := json.Marshal(musicHistoryFile{Version: 1, Listens: list})
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o600)
}

// musicHistoryReadLocked starts reading the file once.
func musicHistoryReadLocked() {
	if musicHist.state != musicHistUnread {
		return
	}
	musicHist.state = musicHistReading
	go musicHistoryRead(musicHist.loadGen)
}

// musicHistoryRead reads the file off the render thread and puts what ended
// meanwhile behind it.
func musicHistoryRead(gen int) {
	path := musicHistoryFilePath()
	// Stamped before the read: a file replaced during it differs from the
	// stamp, so the next save reads it again rather than saving over it.
	disk := musicHistoryStampOf(path)
	list, err := musicHistoryReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		logWarn("music", "listening history unreadable, starting a new one: %v", err)
	}
	musicHist.mu.Lock()
	defer musicHist.mu.Unlock()
	if gen != musicHist.loadGen {
		return // cleared or restored meanwhile
	}
	musicHist.disk = disk
	pending := musicHist.pending
	musicHist.pending = nil
	musicHist.listens = musicHistoryPrune(append(list, pending...), time.Now())
	musicHist.state = musicHistRead
	musicHistoryRev.Add(1)
	if len(pending) > 0 {
		musicHistoryChangedLocked()
	}
}

// musicHistoryChangedLocked marks the log changed and has the writer save it.
func musicHistoryChangedLocked() {
	musicHistoryRev.Add(1)
	musicHist.dirty = true
	if !musicHist.saving {
		musicHist.saving = true
		go musicHistoryWriter()
	}
}

// musicHistoryWriter saves the log until nothing new is left to save. The
// list is copied under the lock and written outside it.
func musicHistoryWriter() {
	failed := 0
	for {
		musicHist.mu.Lock()
		if !musicHist.dirty || musicHist.state != musicHistRead {
			musicHist.saving = false
			musicHist.mu.Unlock()
			return
		}
		musicHist.dirty = false
		wipe := musicHist.wipeBak
		musicHist.wipeBak = false
		gen, disk := musicHist.loadGen, musicHist.disk
		list := append([]musicListen(nil), musicHist.listens...)
		musicHist.mu.Unlock()
		path := musicHistoryFilePath()
		// Someone else wrote the file since it was read or saved here — a
		// backup restore, whose reload follows — so it is read again rather
		// than the list in memory saved over it.
		if disk.replacedSince(musicHistoryStampOf(path)) {
			logWarn("music", "listening history was replaced on disk; reading it again instead of saving over it")
			musicHistoryReload()
			continue
		}
		if err := musicHistoryWriteFile(path, list); err != nil {
			logWarn("music", "listening history not saved: %v", err)
			if wipe {
				failed++
				musicHistoryWipeFailed(path, gen, disk, failed)
			}
			continue
		}
		failed = 0
		if wipe {
			// A cleared history must not live on in the backup copy.
			if err := os.Remove(path + ".bak"); err != nil && !os.IsNotExist(err) {
				logWarn("music", "old listening history copy not removed: %v", err)
			}
		}
		stamp := musicHistoryStampOf(path)
		musicHist.mu.Lock()
		musicHist.disk = stamp
		if gen != musicHist.loadGen && musicHist.state == musicHistRead {
			// Cleared or restored (and read) while this was written: what
			// is on disk may be the list from before; save memory again.
			musicHist.dirty = true
		}
		musicHist.mu.Unlock()
	}
}

// musicHistoryWipeFailed is a clear whose empty log the card would not take:
// the old log and the copy AtomicWrite keeps of it are removed meanwhile (an
// absent log reads as empty; a file someone else wrote since is left alone),
// and the clear stays pending, tried again in a while — after
// musicHistoryWipeTries, at the next save. The writer's goroutine.
func musicHistoryWipeFailed(path string, gen int, disk musicHistStamp, failed int) {
	if err := os.Remove(path + ".bak"); err != nil && !os.IsNotExist(err) {
		logWarn("music", "old listening history copy not removed: %v", err)
	}
	removed := false
	if !disk.replacedSince(musicHistoryStampOf(path)) {
		err := os.Remove(path)
		removed = err == nil || os.IsNotExist(err)
	}
	stamp := musicHistoryStampOf(path)
	musicHist.mu.Lock()
	retry := false
	if gen == musicHist.loadGen {
		// Not cleared again or restored meanwhile: the clear is still due.
		musicHist.wipeBak = true
		if removed {
			musicHist.disk = stamp
		}
		retry = failed < musicHistoryWipeTries
		if retry {
			musicHist.dirty = true
		}
	}
	musicHist.mu.Unlock()
	if retry {
		time.Sleep(time.Duration(failed) * musicHistoryWipeRetry)
	}
}

// musicHistoryClear forgets every listen, the one playing now too, and saves
// the empty log.
func musicHistoryClear() {
	musicHist.mu.Lock()
	defer musicHist.mu.Unlock()
	musicHist.loadGen++
	musicHist.state = musicHistRead
	musicHist.listens = nil
	musicHist.pending = nil
	musicHist.open = musicListenOpen{}
	musicHist.lastPath, musicHist.lastEnd = "", time.Time{}
	musicHist.wipeBak = true
	musicHistoryChangedLocked()
}

// musicHistoryReload has the log read its file again: a backup restore just
// replaced it (bkupReloadStores), or the writer found it replaced. The listen
// playing now carries on.
func musicHistoryReload() {
	musicHist.mu.Lock()
	musicHist.loadGen++
	musicHist.state = musicHistUnread
	musicHist.listens = nil
	musicHist.dirty = false
	// The restored file is the history now: a clear still pending is over,
	// and the file is stamped again when it is read.
	musicHist.wipeBak = false
	musicHist.disk = musicHistStamp{}
	musicHist.lastPath, musicHist.lastEnd = "", time.Time{}
	musicHist.mu.Unlock()
	musicHistoryRev.Add(1)
}

// ── The History shelf ────────────────────────────────────────────────────────

// The Music screen's shelves (music_shelf.go): Albums, Artists, Audiobooks,
// and History last.
const (
	musicShelfTabs       = 4
	musicShelfTabHistory = 3
)

// musicShelfRefreshHistory rebuilds the History rows when the log or the
// library moved (musicShelfRefresh, every frame: cheap until then).
func musicShelfRefreshHistory() {
	m := mlibCurrentModel()
	gen := uint64(1)
	if m != nil {
		gen = m.gen + 1 // 0 is musicShelfInvalidate's "rebuild"
	}
	rev := musicHistoryRev.Load()
	if musicShelf.gen == gen && musicShelf.histRev == rev {
		return
	}
	musicShelf.gen, musicShelf.histRev = gen, rev
	musicShelf.rows, musicShelf.histReady = musicHistoryShelfRows(m, musicShelf.histArmed)
	if musicShelf.cursor >= len(musicShelf.rows) {
		musicShelf.cursor = len(musicShelf.rows) - 1
	}
	if musicShelf.cursor < 0 {
		musicShelf.cursor = 0
	}
	musicShelfSkipHeading(1)
}

// musicShelfSkipHeading moves the cursor off a section heading: on in the
// direction it was going (dir, one row a step whatever its size), else back.
func musicShelfSkipHeading(dir int) {
	if dir < 0 {
		dir = -1
	} else {
		dir = 1
	}
	n := len(musicShelf.rows)
	for pass := 0; pass < 2; pass++ {
		for c := musicShelf.cursor; c >= 0 && c < n; c += dir {
			if musicShelf.rows[c].kind != "heading" {
				musicShelf.cursor = c
				// The heading over the first row of a section stays in view.
				if c > 0 && musicShelf.rows[c-1].kind == "heading" && musicShelf.scroll > c-1 {
					musicShelf.scroll = c - 1
				}
				return
			}
		}
		dir = -dir
	}
}

// musicShelfDisarm takes back a first press of Clear listening history when
// the cursor moves on.
func musicShelfDisarm() {
	if musicShelf.histArmed {
		musicShelf.histArmed = false
		musicShelf.gen = 0
	}
}

// musicShelfChooseHistory is A on a History row.
func musicShelfChooseHistory(config *Config, row musicShelfRow) {
	switch row.kind {
	case "listen":
		musicShelf.histArmed = false
		musicHistoryPlay(config, row.path)
	case "topartist":
		// That artist's albums, as the Artists shelf opens them; Back comes
		// here again.
		musicShelf.tab = 1
		musicShelf.artist = row.key
		musicShelfInvalidate()
		musicShelf.histArtist = true
	case "clearhistory":
		if !musicShelf.histArmed {
			musicShelf.histArmed = true
			musicShelf.gen = 0
			return
		}
		musicHistoryClear()
		musicShelfInvalidate()
		showToast("Listening history cleared", ToastInfo())
	}
}

// musicHistoryEmptyLine is what an empty History shelf says.
func musicHistoryEmptyLine() string {
	if !musicShelf.histReady {
		return "Reading your listening history…"
	}
	return "Nothing played yet. Songs you listen to show up here."
}

// musicHistoryShelfRows builds the History shelf (music_shelf.go) from the
// log: Continue listening — the newest listen of each folder, when it stopped
// part way — then Recently played, Top artists by time heard, and the row that
// clears it all. armed is the clear row's first press. ready is false while
// the file is still being read. m (nil before the library is read) gives the
// artwork, and the artist of a listen kept before the library knew it.
func musicHistoryShelfRows(m *mlibModel, armed bool) (rows []musicShelfRow, ready bool) {
	musicHist.mu.Lock()
	defer musicHist.mu.Unlock()
	if musicHist.state != musicHistRead {
		musicHistoryReadLocked()
		return nil, false
	}
	list := musicHist.listens
	if len(list) == 0 {
		return nil, true
	}
	type tally struct {
		name  string
		secs  int64
		plays int
	}
	var cont, recent []musicShelfRow
	artists := map[string]*tally{}
	seenDir := map[string]bool{}
	seenPath := map[string]bool{}
	for i := len(list) - 1; i >= 0; i-- {
		l := list[i]
		artist, art, book := strings.TrimSpace(l.Artist), "", false
		if m != nil {
			if j, ok := m.byPath[dlPathKey(l.Path)]; ok {
				artist = dlFirstNonEmpty(artist, m.items[j].Artist)
				art, book = m.items[j].Art, m.items[j].Book
			}
		}
		// An audiobook's author is no artist: the Artists shelf has no albums
		// to open for them.
		if key := strings.ToLower(artist); key != "" && !book {
			t := artists[key]
			if t == nil {
				t = &tally{name: artist}
				artists[key] = t
			}
			t.secs += l.Secs
			t.plays++
		}
		if dir := dlPathKey(filepath.Dir(l.Path)); !seenDir[dir] {
			seenDir[dir] = true
			if !l.Done && len(cont) < musicHistoryContinueRows {
				cont = append(cont, musicShelfRow{kind: "listen", title: musicHistoryTitle(l),
					sub: musicHistoryContinueSub(artist, l), art: art, path: l.Path})
			}
		}
		if len(recent) >= musicHistoryRecentRows {
			continue
		}
		if key := dlPathKey(l.Path); !seenPath[key] {
			seenPath[key] = true
			recent = append(recent, musicShelfRow{kind: "listen", title: musicHistoryTitle(l),
				sub: musicHistoryRecentSub(artist, l), art: art, path: l.Path})
		}
	}
	tops := make([]*tally, 0, len(artists))
	for _, t := range artists {
		tops = append(tops, t)
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].secs != tops[j].secs {
			return tops[i].secs > tops[j].secs
		}
		return strings.ToLower(tops[i].name) < strings.ToLower(tops[j].name)
	})
	if len(tops) > musicHistoryArtistRows {
		tops = tops[:musicHistoryArtistRows]
	}
	artFor := map[string]string{}
	if m != nil && len(tops) > 0 {
		for _, g := range m.albums {
			if key := strings.ToLower(g.sub); g.art != "" && artFor[key] == "" {
				artFor[key] = g.art
			}
		}
	}
	rows = make([]musicShelfRow, 0, len(cont)+len(recent)+len(tops)+4)
	if len(cont) > 0 {
		rows = append(rows, musicShelfRow{kind: "heading", title: "Continue listening"})
		rows = append(rows, cont...)
	}
	if len(recent) > 0 {
		rows = append(rows, musicShelfRow{kind: "heading", title: "Recently played"})
		rows = append(rows, recent...)
	}
	if len(tops) > 0 {
		rows = append(rows, musicShelfRow{kind: "heading", title: "Top artists"})
		for _, t := range tops {
			key := strings.ToLower(t.name)
			rows = append(rows, musicShelfRow{kind: "topartist", key: key, title: t.name,
				sub: pluralizeCount(t.plays, "play", "plays") + " · " + musicHistoryListened(t.secs), art: artFor[key]})
		}
	}
	wipe := musicShelfRow{kind: "clearhistory", title: "Clear listening history",
		sub: "Forgets every play on this device"}
	if armed {
		wipe.title = "Clear it? Choose again to confirm"
		wipe.sub = "Anything else keeps the history"
	}
	return append(rows, wipe), true
}

// musicHistoryTitle is a listen's title on the shelf.
func musicHistoryTitle(l musicListen) string {
	return dlFirstNonEmpty(l.Title, dlTitleFromFile(l.Path))
}

// musicHistoryContinueSub says who and where a listen stopped.
func musicHistoryContinueSub(artist string, l musicListen) string {
	where := "Stopped at " + formatDuration(l.Pos)
	if l.Dur > 0 {
		where += " of " + formatDuration(l.Dur)
	}
	if artist == "" {
		return where
	}
	return artist + " · " + where
}

// musicHistoryRecentSub says who and when.
func musicHistoryRecentSub(artist string, l musicListen) string {
	when := ""
	if l.Start > 0 {
		when = timeAgo(time.Unix(l.Start, 0))
	}
	switch {
	case artist == "":
		return dlFirstNonEmpty(when, "Played")
	case when == "":
		return artist
	}
	return artist + " · " + when
}

// musicHistoryListened is time heard in words: "45 min", "3 h 20 min".
func musicHistoryListened(secs int64) string {
	mins := secs / 60
	switch {
	case mins < 1:
		return "under a minute"
	case mins < 60:
		return strconv.FormatInt(mins, 10) + " min"
	}
	return strconv.FormatInt(mins/60, 10) + " h " + strconv.FormatInt(mins%60, 10) + " min"
}

// musicHistoryPlay plays a listen again from the History shelf: its album or
// audiobook from that track when the library knows it (as the Albums and
// Audiobooks shelves play them), else its folder (as Files does). A long track
// picks up where it was left (audioresume.go); a song starts over and the
// album carries on after it.
func musicHistoryPlay(config *Config, path string) {
	if m := mlibCurrentModel(); m != nil {
		if i, ok := m.byPath[dlPathKey(path)]; ok && !m.items[i].Video {
			it := m.items[i]
			key := mlibAlbumKey(it.Artist, it.Album)
			if _, ok := m.bookIdx[key]; ok && it.Book {
				musicShelfPlayGroup(config, key, true, path)
				return
			}
			if _, ok := m.albumIdx[key]; ok && !it.Book {
				musicShelfPlayGroup(config, key, false, path)
				return
			}
		}
	}
	// Opening a folder replaces what is playing: not for a file that is gone.
	// A soundtrack's song ("game.nsf#3") is there when its file is (chiptune.go).
	if info, err := os.Stat(chiptuneFileOf(path)); err != nil || !info.Mode().IsRegular() {
		showToast("That track is not there any more", ToastInfo())
		return
	}
	openAudioPlayer(config, path)
	music.mu.Lock()
	played := len(music.tracks) > 0
	music.mu.Unlock()
	if played {
		musicShelf.fromShelf = true // B goes back to the shelf
	} else {
		musicShelf.open = true
	}
}
