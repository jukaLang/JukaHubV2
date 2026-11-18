package main

// stallwatch.go: the frame-stall watchdog. The main loop stamps a heartbeat
// at the top of every iteration (the parked loop behind a game and the dark
// dock's idle loop included); a goroutine notices when no stamp has come for
// stallWatchLimit and writes ONE warning per stall into the app log with the
// main goroutine's stack, so a device report shows where the UI hung. When
// the loop moves again a second line says how long it was stuck. Cheap: a
// clock read per frame and a 250 ms ticker; no UI.

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	stallWatchLimit = 1500 * time.Millisecond
	stallWatchTick  = 250 * time.Millisecond
	// stallWatchStackMax caps the main goroutine's stack in the log line; the
	// frames that matter are at the top.
	stallWatchStackMax = 6 << 10
)

// stallBeatNanos is the monotonic time of the last heartbeat (nanoseconds
// since stallWatchEpoch); 0 = not armed (before the first frame, after the
// loop ended).
var stallBeatNanos atomic.Int64

var stallWatchEpoch = time.Now()

var stallWatchOnce sync.Once

// stallWatchBeat is called by the main loop once per iteration. The first
// call starts the watcher.
func stallWatchBeat() {
	stallBeatNanos.Store(int64(time.Since(stallWatchEpoch)) | 1)
	stallWatchOnce.Do(func() { go stallWatchRun() })
}

// stallWatchStop disarms the watchdog: the loop has ended and the teardown,
// which has its own exit watchdog, may legitimately take longer.
func stallWatchStop() {
	stallBeatNanos.Store(0)
}

func stallWatchRun() {
	t := time.NewTicker(stallWatchTick)
	defer t.Stop()
	var reported int64 // the beat a warning was already written for
	// lastTick and resumed tell a frozen process from a stuck UI thread: when
	// this goroutine itself missed its ticks for longer than the limit, the
	// whole process was stopped (a desktop's sleep, where Windows' monotonic
	// clock keeps counting; a debugger), and the wait is measured from then.
	lastTick := time.Since(stallWatchEpoch)
	var resumed time.Duration
	for range t.C {
		now := time.Since(stallWatchEpoch)
		if now-lastTick > stallWatchLimit {
			resumed = now
		}
		lastTick = now
		beat := stallBeatNanos.Load()
		if beat == 0 {
			reported = 0
			continue
		}
		from := time.Duration(beat)
		if resumed > from {
			from = resumed
		}
		since := now - from
		if reported != 0 && beat != reported {
			// The loop moved on: say how long the stall lasted.
			logWarn("stall", "UI thread responsive again after %s",
				time.Duration(beat-reported).Round(10*time.Millisecond))
			reported = 0
		}
		if since < stallWatchLimit || beat == reported {
			continue
		}
		reported = beat
		logWarn("stall", "UI thread has not finished a frame for %s; main goroutine:\n%s",
			since.Round(10*time.Millisecond), stallWatchMainStack())
	}
}

// stallWatchMainStack returns goroutine 1 (main, which runs the SDL loop)
// trimmed to stallWatchStackMax, plus a one-line count of the others by
// state, so a lock or a channel the main thread waits on can be matched to
// the goroutine holding it on a later report.
func stallWatchMainStack() string {
	buf := make([]byte, 256<<10)
	n := runtime.Stack(buf, true)
	buf = buf[:n]
	var mainG []byte
	states := map[string]int{}
	for _, g := range bytes.Split(buf, []byte("\n\n")) {
		if bytes.HasPrefix(g, []byte("goroutine 1 [")) {
			mainG = g
			continue
		}
		head := g
		if i := bytes.IndexByte(head, '\n'); i >= 0 {
			head = head[:i]
		}
		if a, b := bytes.IndexByte(head, '['), bytes.IndexByte(head, ']'); a >= 0 && b > a {
			st := string(head[a+1 : b])
			if c := strings.IndexByte(st, ','); c >= 0 {
				st = st[:c]
			}
			states[st]++
		}
	}
	if mainG == nil {
		mainG = []byte("(main goroutine not found)")
	}
	if len(mainG) > stallWatchStackMax {
		mainG = append(mainG[:stallWatchStackMax:stallWatchStackMax], "\n…"...)
	}
	var sb strings.Builder
	sb.Write(mainG)
	if len(states) > 0 {
		sb.WriteString("\nother goroutines:")
		for st, c := range states {
			sb.WriteString(" ")
			sb.WriteString(st)
			sb.WriteString("=")
			sb.WriteString(strconv.Itoa(c))
		}
	}
	return sb.String()
}
