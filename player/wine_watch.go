package main

// wine_watch.go — the start of a WestonPack session, watched.
//
// westonwrap.sh (0.2.7.1) starts Weston in the background and then waits for
// Xwayland's socket with no time limit:
//
//	while ! [[ -e $xfile ]]; do sleep 0.1; done
//
// A Weston that exits while it starts, or never gets that far, leaves the
// wrapper in that loop for good, and with it JukaHub's screen parked over its
// last picture and the pad lent to the Windows program as its mouse: the
// handheld looks frozen until it is switched off (JH-JMGVDB, "wine freeze on
// start", 2026-11-25). wineWatchWestonStart follows the session until the
// program's private launch script (westonLaunchScript) runs — westonwrap.sh is
// past that wait then — or has already written its exit status. A Weston that
// was running, or that westonwrap.sh said it starts, and is gone before that
// point ends the session within seconds; no start by wineWestonStartLimit ends
// it too. Either way the launch-failure card says what happened, and the app's
// log holds Weston's own messages (the session's output goes there).

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// wineWestonStartLimit is how long westonwrap.sh may take to start the
	// program. Weston and Xwayland are up in about a second on a Smart Pro
	// (device log, 2026-11-25); the rest is the wrapper's own set-up.
	wineWestonStartLimit = 90 * time.Second
	// wineWestonWatchEvery paces the look at the session.
	wineWestonWatchEvery = 500 * time.Millisecond
	// wineWestonGoneGrace is how long Weston may be missing, once it was
	// running or about to, before its start counts as failed.
	wineWestonGoneGrace = 2 * time.Second
	// wineWestonStopWait bounds the wait for the watch to end once the session
	// is over (the watch may be stopping the session itself).
	wineWestonStopWait = 5 * time.Second
	// wineWestonStartLine is what westonwrap.sh prints just before it starts
	// Weston: a Weston that fails at once is gone before a look could see it.
	wineWestonStartLine = "Starting Weston"
)

// What one look at a starting WestonPack session found (wineWestonWatch.step).
const (
	wineWestonWaiting  = iota // still starting
	wineWestonStarted         // the program's script runs, or has run
	wineWestonOver            // nothing is left of the session: it ended by itself
	wineWestonDied            // Weston started and is gone, the program never did
	wineWestonTimedOut        // no start by wineWestonStartLimit
)

// wineDisplayStuckError is a session the start watch ended: its display never
// came up, so its program never started. wineRun shows it whatever the
// session's length.
type wineDisplayStuckError struct{ reason string }

func (e *wineDisplayStuckError) Error() string { return e.reason }

// wineWestonWatch is the watch's memory between looks.
type wineWestonWatch struct {
	westonUp  bool // a Weston process was seen, or westonwrap.sh said it starts one
	goneSince time.Time
}

// step judges one look at the session: procs (wineGroupProcs, its process
// group), whether the launch script has written its exit status, whether
// westonwrap.sh has said it starts Weston, and how long the session has run.
// A process whose second word is script is the launch script itself
// (/bin/sh script); westonwrap.sh names the script too, but as a later word.
func (w *wineWestonWatch) step(procs []wineGroupProc, script string, statusWritten, westonSaid bool, elapsed time.Duration, now time.Time) int {
	if statusWritten {
		return wineWestonStarted
	}
	if len(procs) == 0 {
		return wineWestonOver
	}
	weston := false
	for _, p := range procs {
		if len(p.argv) > 1 && p.argv[1] == script {
			return wineWestonStarted
		}
		if p.comm == "wp_weston" || p.comm == "weston" {
			weston = true
		}
	}
	switch {
	case weston:
		w.westonUp, w.goneSince = true, time.Time{}
	case !w.westonUp && !westonSaid:
	case w.goneSince.IsZero():
		w.westonUp, w.goneSince = true, now
	case now.Sub(w.goneSince) >= wineWestonGoneGrace:
		return wineWestonDied
	}
	if elapsed >= wineWestonStartLimit {
		return wineWestonTimedOut
	}
	return wineWestonWaiting
}

// launchScriptPath is the program's private launch script of a WestonPack
// session ("" for every other display).
func (d wineDisplayPlan) launchScriptPath() string {
	if d.launchDir == "" {
		return ""
	}
	return filepath.Join(d.launchDir, westonLaunchScriptName)
}

// wineLogSize is the log's size now (0 when it cannot be read): where a
// session's own output starts.
func wineLogSize(p string) int64 {
	if p == "" {
		return 0
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return info.Size()
}

// wineLogHas reports whether the log at p holds needle after offset; at most
// the 1 MB a log is rotated at is read.
func wineLogHas(p string, offset int64, needle string) bool {
	if p == "" {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return bytes.Contains(data, []byte(needle))
}

// wineWatchWestonStart watches a started WestonPack session — cmd runs
// westonwrap.sh and leads its own process group. logPath is where its output
// goes; logOffset is its size before the process started. The watch ends when
// the program's launch script runs (script; status is the file
// the script writes its exit status to). A failed start is logged and the
// session ended with quit, unless quitting reports that the player is ending
// it already. The returned stop ends the watch (call it once the session's
// command has returned) and gives the error a failed start ended the session
// with, nil otherwise. Any goroutine.
func wineWatchWestonStart(cmd *exec.Cmd, script, status, logPath string, logOffset int64, log wineLog, quit func(), quitting func() bool) func() error {
	if cmd == nil || cmd.Process == nil || script == "" || quit == nil {
		return func() error { return nil }
	}
	stop, over := make(chan struct{}), make(chan struct{})
	var stuck atomic.Pointer[wineDisplayStuckError]
	go func() {
		defer close(over)
		defer func() {
			if r := recover(); r != nil {
				log.printf("display: the start watch stopped after a fault: %v\n", r)
			}
		}()
		pgid := cmd.Process.Pid
		begun := time.Now()
		var w wineWestonWatch
		said := false
		tick := time.NewTicker(wineWestonWatchEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			now := time.Now()
			ran := now.Sub(begun)
			if !said {
				said = wineLogHas(logPath, logOffset, wineWestonStartLine)
			}
			var reason string
			switch w.step(wineGroupProcs(pgid), script, status != "" && regularFile(status), said, ran, now) {
			case wineWestonWaiting:
				continue
			case wineWestonStarted:
				log.printf("display: WestonPack started the program after %s\n", ran.Round(100*time.Millisecond))
				return
			case wineWestonOver:
				return
			case wineWestonDied:
				reason = "WestonPack's display (Weston) stopped while it was starting, so the program could not open a window; the log has Weston's own messages"
			case wineWestonTimedOut:
				reason = "WestonPack's display had not come up after " + wineWestonStartLimit.String() + ", so the program was not started; the log shows how far WestonPack got"
			}
			if quitting != nil && quitting() {
				return
			}
			stuck.Store(&wineDisplayStuckError{reason: reason})
			log.printf("display: %s; ending the session\n", reason)
			logWarn("windowsapps", "%s", reason)
			quit()
			return
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(func() { close(stop) })
		select {
		case <-over:
		case <-time.After(wineWestonStopWait):
		}
		if s := stuck.Load(); s != nil {
			return s
		}
		return nil
	}
}
