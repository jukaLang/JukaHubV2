package main

// battgraph.go — the battery's history: what the level did over the last two
// days, for the Battery screen (battgraph_ui.go).
//
// battery_eta.go answers "how long is left" from the last 45 minutes and keeps
// nothing else; the percentage in the corner says nothing about the evening
// before. NextUI shows a graph of the level, and this is the recorder behind
// JukaHub's: once a minute it notes the time, the level, whether a cable was
// in, whether the screen was asleep, whether a game owned the screen, and the
// current the fuel gauge last reported (battery_eta.go's own 20-second read —
// no file is read here). Sampling rides widgetsTick, which keeps running while
// a game plays, so the history covers games too; no ticker of its own.
//
// The card: 48 hours at one sample a minute is under 3,000 twelve-byte records
// (about 35 KB). Samples collect in memory and reach the card every ten
// minutes — every two below 15 % on battery, where the last minutes before the
// low-battery guard suspends are the interesting ones — on exit, and before a
// suspend (battGraphFlushNow). Each write appends only the new records; the
// file is rewritten whole (atomically) only when it has grown to twice the
// window, when its tail was damaged (a power cut mid-append), or after a clock
// jump moved samples. A lost batch costs ten minutes of graph, nothing else.
//
// The clock: the handheld boots before network time, so a sample's wall-clock
// stamp can be wrong. The samples this run took since the clock was last known
// right are moved by exactly what a jump moved the clock (clockjump.go, the
// same bookkeeping playtime uses), samples an earlier run left in the future
// are dropped once the clock is set, and the screen measures every span as a
// difference of stamps and labels the axis "8 h ago … now", never with a time
// of day. Until the clock is plausible (after this build) the screen shows this
// run's samples only. Nothing is counted or badged by a timestamp.
//
// File format (battery_history.bin in the data folder): the 8-byte magic
// "JHBH" 1 0 0 0, then records of 12 bytes, little-endian: int64 Unix seconds,
// uint8 percent, uint8 flags (1 cable, 2 screen asleep, 4 game), uint16 mA.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// battGraphSampleEvery is the recording interval; a cable going in or out
	// is recorded at once as well, so a charging span starts where it did.
	battGraphSampleEvery = time.Minute
	// battGraphFlushEvery is how often new samples reach the card;
	// battGraphFlushLowEvery replaces it on battery at battGraphLowPct or less.
	battGraphFlushEvery    = 10 * time.Minute
	battGraphFlushLowEvery = 2 * time.Minute
	battGraphLowPct        = 15
	// battGraphKeep is the history kept; battGraphMaxSamples bounds it by
	// count as well, whatever the stamps say (a wrong clock cannot grow it).
	battGraphKeep       = 48 * time.Hour
	battGraphMaxSamples = 48*60 + 60
	// battGraphGapSecs is the silence that breaks the line: the device slept
	// or JukaHub was not running (batteryETAGap's three minutes).
	battGraphGapSecs = 180
	// battGraphLoadRetry is how long a missing data folder waits before the
	// file is looked for again.
	battGraphLoadRetry  = time.Minute
	battGraphFileName   = "battery_history.bin"
	battGraphRecordSize = 12
)

// battGraphMagic opens the file: "JHBH" and the format version.
var battGraphMagic = []byte{'J', 'H', 'B', 'H', 1, 0, 0, 0}

// Sample flags.
const (
	// battFlagCable: a cable was in.
	battFlagCable uint8 = 1 << iota
	// battFlagAsleep: the screen was off (the power key's sleep).
	battFlagAsleep
	// battFlagGame: a game, emulator or app owned the screen.
	battFlagGame
)

// battSample is one minute of history.
type battSample struct {
	at    int64 // Unix seconds, wall clock (moved after a jump)
	pct   uint8
	flags uint8
	ma    uint16 // current the gauge reported, mA; 0 unknown
}

// battGraph is the recorder's state. The tick writes it on the main thread;
// the load and the flushes run on goroutines and take the lock; the screen
// copies the samples under the lock.
var battGraph struct {
	mu      sync.Mutex
	samples []battSample // oldest first, at most battGraphMaxSamples
	// runStart is the first sample this run took; runFrom the first one
	// taken since the clock was last known right (a sync confirmed it or a
	// jump moved what came before). Both move with trims and the load.
	runStart int
	runFrom  int

	loaded    bool
	loading   bool
	loadTryAt time.Time
	path      string // "" until the data folder is known

	fileRecs int  // records in the file
	pending  int  // the newest samples not yet in the file
	rewrite  bool // the file must be rewritten whole
	gen      int  // bumped whenever rewrite is set by a change of the samples
	flushing bool
	flushAt  time.Time
	failed   bool // the last write failed (the first failure warns)

	lastAt    time.Time // monotonic reading of the last sample
	lastCable bool
	view      clockJumpView
	confirm   uint64
	rev       uint64 // bumped on every change, for the screen's cache
}

// battGraphJob is one write: the new records appended, or all of them.
type battGraphJob struct {
	path string
	all  bool
	recs []battSample
	n    int // pending samples the write covers
	gen  int
}

// battGraphPath is the history file, "" when there is no data folder.
func battGraphPath() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, battGraphFileName)
	}
	return ""
}

// battGraphNote is widgetsTick's once-a-second hook with the reading it
// already took (one level and one cable state for the estimate and this). It
// records a sample once a minute and on a cable change, and starts the load
// and the rate-limited writes on goroutines. A device with no battery (pct
// -1) records nothing and never creates the file.
func battGraphNote(pct int, cableIn bool) {
	if pct < 0 {
		return
	}
	now := time.Now()
	ma := battGraphCurrentMA(now)
	var flags uint8
	if cableIn {
		flags |= battFlagCable
	}
	if powerSleepActive() {
		flags |= battFlagAsleep
	}
	if gameScreenSuspended() {
		flags |= battFlagGame
	}
	if pct > 100 {
		pct = 100
	}

	s := &battGraph
	s.mu.Lock()
	battGraphClockLocked(now)
	due := s.lastAt.IsZero() || now.Sub(s.lastAt) >= battGraphSampleEvery ||
		(cableIn != s.lastCable && now.Sub(s.lastAt) >= 5*time.Second)
	if due {
		s.lastAt, s.lastCable = now, cableIn
		battGraphAppendLocked(battSample{at: now.Unix(), pct: uint8(pct), flags: flags, ma: ma})
	}
	wantLoad := !s.loaded && !s.loading && (s.loadTryAt.IsZero() || now.Sub(s.loadTryAt) >= battGraphLoadRetry)
	if wantLoad {
		s.loading, s.loadTryAt = true, now
	}
	job := battGraphFlushJobLocked(now, pct, cableIn, false)
	s.mu.Unlock()

	if wantLoad {
		go battGraphLoad()
	}
	if job != nil {
		go battGraphRunFlush(job)
	}
}

// battGraphFlushNow writes what is pending at once, on the caller's
// goroutine: for the exit path and before a suspend, where a batch still in
// memory would be lost with the power.
func battGraphFlushNow() {
	s := &battGraph
	s.mu.Lock()
	// A periodic write still running would make this one a no-op and leave
	// what came after it in memory: it gets a second to finish.
	for i := 0; s.flushing && i < 50; i++ {
		s.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		s.mu.Lock()
	}
	job := battGraphFlushJobLocked(time.Now(), -1, true, true)
	s.mu.Unlock()
	if job != nil {
		battGraphRunFlush(job)
	}
}

// battGraphCurrentMA is the current battery_eta.go last read, in mA, when the
// read is recent; 0 when there is none. Its lock is taken alone, never inside
// battGraph's.
func battGraphCurrentMA(now time.Time) uint16 {
	s := &batteryETAState
	s.mu.Lock()
	ua, at := s.t.rich.currentUA, s.t.richAt
	s.mu.Unlock()
	if at.IsZero() || now.Sub(at) > 2*batteryETARichEvery || ua <= 0 {
		return 0
	}
	ma := math.Round(ua / 1000)
	if ma > math.MaxUint16 {
		ma = math.MaxUint16
	}
	return uint16(ma)
}

// battGraphAppendLocked adds a sample and trims the history to its bound.
func battGraphAppendLocked(x battSample) {
	s := &battGraph
	s.samples = append(s.samples, x)
	s.pending++
	if over := len(s.samples) - battGraphMaxSamples; over > 0 {
		s.samples = append(s.samples[:0], s.samples[over:]...)
		s.runStart = max(0, s.runStart-over)
		s.runFrom = max(0, s.runFrom-over)
	}
	if s.pending > len(s.samples) {
		s.pending = len(s.samples)
	}
	s.rev++
}

// battGraphClockLocked follows the clock (clockjump.go). A sync that found
// the clock right vouches for everything taken so far; a jump moves the
// samples taken since the clock was last known right by exactly the jump,
// drops what an earlier run's clock left in the future, and has the file
// rewritten.
func battGraphClockLocked(now time.Time) {
	s := &battGraph
	if n := clockConfirmCount.Load(); n != s.confirm {
		s.confirm = n
		s.runFrom = len(s.samples)
	}
	shift, jumped := clockJumpSeen(&s.view)
	if !jumped || shift.clock == 0 {
		return
	}
	secs := int64(shift.clock.Round(time.Second) / time.Second)
	for i := s.runFrom; i < len(s.samples); i++ {
		s.samples[i].at += secs
	}
	sort.SliceStable(s.samples, func(i, j int) bool { return s.samples[i].at < s.samples[j].at })
	limit := now.Unix() + 60
	k := len(s.samples)
	for k > 0 && s.samples[k-1].at > limit {
		k--
	}
	s.samples = s.samples[:k]
	// The order changed, so "this run" is no longer a suffix; the clock is
	// right now, which is all the screen needed runStart for.
	s.runStart, s.runFrom = 0, len(s.samples)
	if s.pending > len(s.samples) {
		s.pending = len(s.samples)
	}
	s.rewrite = true
	s.gen++
	s.rev++
}

// battGraphFlushJobLocked returns the write that is due, or nil: never before
// the file was read, one at a time, and at most every battGraphFlushEvery
// (battGraphFlushLowEvery on a low battery) unless forced. A file grown to
// twice the window is rewritten with the window alone.
func battGraphFlushJobLocked(now time.Time, pct int, cableIn, force bool) *battGraphJob {
	s := &battGraph
	if !s.loaded || s.path == "" || s.flushing || (s.pending == 0 && !s.rewrite) {
		return nil
	}
	if !force {
		every := battGraphFlushEvery
		if pct >= 0 && pct <= battGraphLowPct && !cableIn {
			every = battGraphFlushLowEvery
		}
		if !s.flushAt.IsZero() && now.Sub(s.flushAt) < every {
			return nil
		}
	}
	all := s.rewrite || s.fileRecs+s.pending > 2*battGraphMaxSamples
	src := s.samples[len(s.samples)-s.pending:]
	if all {
		src = s.samples
	}
	job := &battGraphJob{path: s.path, all: all, n: s.pending, gen: s.gen}
	job.recs = append([]battSample(nil), src...)
	s.flushing, s.flushAt = true, now
	return job
}

// battGraphRunFlush performs a write and books its outcome.
func battGraphRunFlush(job *battGraphJob) {
	var err error
	if job.all {
		err = battGraphWriteAll(job.path, job.recs)
	} else {
		err = battGraphAppendFile(job.path, job.recs)
	}
	s := &battGraph
	s.mu.Lock()
	s.flushing = false
	warn := false
	if err == nil {
		if job.all {
			s.fileRecs = len(job.recs)
			if s.gen == job.gen {
				s.rewrite = false
			}
		} else {
			s.fileRecs += len(job.recs)
		}
		s.pending = max(0, s.pending-job.n)
		s.failed = false
	} else {
		// A failed append may have left half a record: the next write
		// replaces the file instead of appending after it.
		s.rewrite = true
		warn = !s.failed
		s.failed = true
	}
	s.mu.Unlock()
	if warn {
		logWarn("battery", "battery history could not be saved: %v", err)
	}
}

// battGraphLoad reads the file once the data folder is known and puts its
// samples in front of what this run recorded meanwhile. Samples older than
// the window go when the clock is plausible; a damaged file is rewritten by
// the first flush.
func battGraphLoad() {
	path := battGraphPath()
	var recs []battSample
	var fileRecs int
	damaged := false
	if path != "" {
		recs, fileRecs, damaged = battGraphReadFile(path)
	}
	now := time.Now()
	if !now.Before(clockFloor()) {
		cut := now.Add(-battGraphKeep).Unix()
		i := 0
		for i < len(recs) && recs[i].at < cut {
			i++
		}
		recs = recs[i:]
	}

	s := &battGraph
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = false
	if path == "" {
		return
	}
	s.loaded, s.path, s.flushAt, s.fileRecs = true, path, now, fileRecs
	merged := make([]battSample, 0, min(len(recs)+len(s.samples), battGraphMaxSamples))
	shift := len(recs)
	if over := len(recs) + len(s.samples) - battGraphMaxSamples; over > 0 {
		if over > len(recs) {
			over = len(recs)
		}
		recs = recs[over:]
		shift -= over
	}
	merged = append(merged, recs...)
	merged = append(merged, s.samples...)
	if over := len(merged) - battGraphMaxSamples; over > 0 {
		merged = merged[over:]
		shift -= over
	}
	s.samples = merged
	s.runStart = max(0, s.runStart+shift)
	s.runFrom = max(0, s.runFrom+shift)
	if s.pending > len(s.samples) {
		s.pending = len(s.samples)
	}
	if damaged {
		s.rewrite = true
		s.gen++
	}
	s.rev++
}

// battGraphReadFile parses the file: its valid records (the newest
// battGraphMaxSamples), how many records it holds, and whether it needs
// rewriting (a bad header, a torn tail, an invalid record, unreadable). A
// missing file is an empty history, not a damaged one. Only the header and the
// newest battGraphMaxSamples records are read, however large the file grew.
func battGraphReadFile(path string) (recs []battSample, fileRecs int, damaged bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, !errors.Is(err, fs.ErrNotExist)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, true
	}
	head := make([]byte, len(battGraphMagic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, battGraphMagic) {
		return nil, 0, true
	}
	size := info.Size() - int64(len(battGraphMagic))
	n := int(size / battGraphRecordSize)
	damaged = size%battGraphRecordSize != 0
	start := max(0, n-battGraphMaxSamples)
	body := make([]byte, (n-start)*battGraphRecordSize)
	if _, err := f.ReadAt(body, int64(len(battGraphMagic))+int64(start)*battGraphRecordSize); err != nil {
		return nil, 0, true
	}
	recs = make([]battSample, 0, n-start)
	for off := 0; off+battGraphRecordSize <= len(body); off += battGraphRecordSize {
		r, ok := battGraphDecode(body[off : off+battGraphRecordSize])
		if !ok {
			damaged = true
			continue
		}
		recs = append(recs, r)
	}
	return recs, n, damaged
}

// battGraphDecode reads one record; ok is false for one no recorder writes.
func battGraphDecode(b []byte) (battSample, bool) {
	r := battSample{
		at:    int64(binary.LittleEndian.Uint64(b[0:8])),
		pct:   b[8],
		flags: b[9],
		ma:    binary.LittleEndian.Uint16(b[10:12]),
	}
	ok := r.at > 0 && r.pct <= 100 && r.flags < 8
	return r, ok
}

// battGraphEncode appends one record to buf.
func battGraphEncode(buf []byte, r battSample) []byte {
	var b [battGraphRecordSize]byte
	binary.LittleEndian.PutUint64(b[0:8], uint64(r.at))
	b[8], b[9] = r.pct, r.flags
	binary.LittleEndian.PutUint16(b[10:12], r.ma)
	return append(buf, b[:]...)
}

// battGraphAppendFile appends records in one write, with the magic first when
// the file is new.
func battGraphAppendFile(path string, recs []battSample) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	buf := make([]byte, 0, len(battGraphMagic)+len(recs)*battGraphRecordSize)
	if info.Size() == 0 {
		buf = append(buf, battGraphMagic...)
	}
	for _, r := range recs {
		buf = battGraphEncode(buf, r)
	}
	_, werr := f.Write(buf)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// battGraphWriteAll replaces the file atomically with recs.
func battGraphWriteAll(path string, recs []battSample) error {
	buf := make([]byte, 0, len(battGraphMagic)+len(recs)*battGraphRecordSize)
	buf = append(buf, battGraphMagic...)
	for _, r := range recs {
		buf = battGraphEncode(buf, r)
	}
	return AtomicWrite(path, buf, 0o644)
}

// battGraphCopy copies the history into dst (reusing its array) for the
// screen, with the index of this run's first sample and the revision.
func battGraphCopy(dst []battSample) ([]battSample, int, uint64) {
	s := &battGraph
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst[:0], s.samples...)
	return dst, s.runStart, s.rev
}

// battGraphRevision is the history's revision, so the screen copies only when
// something changed.
func battGraphRevision() uint64 {
	s := &battGraph
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}
