package main

// gamepause_ra.go — RetroArch pauses itself before JukaHub freezes it.
//
// The in-game menu, the game assistant (gameassist.go) and a ringing alarm
// (alarmvol.go) stop the game with SIGSTOP and let it run on with SIGCONT
// (alarm_proc_unix.go). That freezes the emulator in the middle of playing:
// its ALSA stream runs dry while it is stopped, and the first write after the
// continue meets an underrun. On the TrimUI Brick a game continued that way
// played on in silence (reported 2026-11-12). RetroArch gives up on sound for
// the rest of the session the first time its audio driver fails a write
// ("Audio backend failed to write. Will continue without sound."), so one
// recovery the driver did not manage is enough to lose it until the next
// launch.
//
// RetroArch's own pause does not have the problem: pausing stops its audio
// driver (CMD_EVENT_AUDIO_STOP) and unpausing starts it again, the path its
// own menu takes every time it opens. So when the game on screen is a
// RetroArch the library started with the command interface on (the launch
// that gives the in-game menu its save slots, gamemenu_states.go), it is
// asked to pause first — PAUSE_TOGGLE, confirmed with GET_STATUS — and only
// then frozen. On the way back it is continued first and unpaused the same
// way, on a goroutine, and only while GET_STATUS says PAUSED, so a toggle can
// never pause a game that is running. Anything else (PPSSPP, a port, a
// RetroArch started outside the library, a build that does not answer
// GET_STATUS) is frozen exactly as before.
//
// After the game is given back, the new part of RetroArch's log for this
// launch (gamediag.go writes it into JukaHub's data folder) is read for the
// audio driver's error lines, so a game that lost its sound anyway says why
// in JukaHub's log instead of leaving only "no sound" to go on.

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// raCoopAskWait is how long one GET_STATUS waits for RetroArch's answer.
	// A running RetroArch reads its command socket every frame.
	raCoopAskWait = 150 * time.Millisecond
	// raCoopPauseFor bounds the wait for the pause to show. The overlay (or
	// the alarm) is not drawn until it returns, and the game keeps its own
	// picture meanwhile, so the only cost of a slow answer is a short delay.
	raCoopPauseFor = 500 * time.Millisecond
	// raCoopPoll spaces the GET_STATUS questions.
	raCoopPoll = 40 * time.Millisecond
	// raCoopResumeFor bounds the unpause once nothing holds the game any more;
	// raCoopResendAfter is how long a PAUSE_TOGGLE may take to show before it
	// is taken as lost (UDP) and sent once more.
	raCoopResumeFor   = 3 * time.Second
	raCoopResendAfter = 700 * time.Millisecond
	// raCoopSettle is how long PLAYING must hold before the unpause is done
	// (see raCoopUnpause), and raCoopStartDelay how long the unpause waits
	// before its first look.
	raCoopSettle     = 150 * time.Millisecond
	raCoopStartDelay = 60 * time.Millisecond
	// raAudioCheckAfter is how long after the hand-back RetroArch's log is
	// read: long enough for the audio driver to have written a few times.
	raAudioCheckAfter = 3 * time.Second
	// raAudioCheckMax bounds the part of the log read.
	raAudioCheckMax = 64 << 10
)

// raCoopState is one freeze of a library RetroArch: the command port (0 when
// the game is not one), whether JukaHub asked it to pause (so the unpause is
// owed), and where RetroArch's log stood when it was frozen.
type raCoopState struct {
	port    int
	paused  bool
	logPath string
	logAt   int64
}

// raStatusWord is the state word of a GET_STATUS answer ("GET_STATUS PLAYING
// nes,Game,crc32=…" → "PLAYING"; also PAUSED and CONTENTLESS); "" for any
// other answer. Pure.
func raStatusWord(reply string) string {
	f := strings.Fields(reply)
	if len(f) < 2 || !strings.EqualFold(f[0], "GET_STATUS") {
		return ""
	}
	return strings.ToUpper(f[1])
}

// raLibraryPort is RetroArch's command port when the game on screen is a
// RetroArch game the library started with the command interface on, else 0 —
// the same test as the in-game menu's save slots (gmStatesAvailable), without
// needing the overlay to be up. Main thread: it reads gaTrack.
func raLibraryPort() int {
	if runtime.GOOS != "linux" {
		return 0
	}
	gmStates.mu.Lock()
	rom, port, at := gmStates.rom, gmStates.port, gmStates.at
	gmStates.mu.Unlock()
	if rom == "" || port <= 0 || at.IsZero() || gaTrack.launchAt.IsZero() {
		return 0
	}
	if d := gaTrack.launchAt.Sub(at); d < -gmStateLaunchMatch || d > gmStateLaunchMatch {
		return 0
	}
	if !retroArchRunning() {
		return 0
	}
	return port
}

// raCoop is the one owner of RetroArch's own pause. Two freezes can overlap
// (the in-game overlay, a ringing alarm) and a new one can start while the
// last one's unpause is still on its way, so the pause is counted rather than
// kept per freeze: holds is how many freezes want it paused, owed that an
// unpause is due, and gen ends an unpause worker a newer one replaced. Without
// it, a menu reopened within the unpause's few hundred milliseconds read
// RetroArch's PAUSED as "already paused, nothing to undo" and the game stayed
// paused for good.
var raCoop struct {
	mu    sync.Mutex
	port  int
	holds int
	owed  bool
	gen   uint64
}

// raCoopPause asks the library RetroArch on screen, if that is what it is, to
// pause itself, and returns what the freeze that follows has to undo. Call it
// on the main thread right before the SIGSTOP. It blocks for at most about
// raCoopPauseFor plus one answer; any failure leaves the game to be frozen as
// it is.
func raCoopPause(why string) raCoopState {
	port := raLibraryPort()
	if port == 0 {
		return raCoopState{}
	}
	st := raCoopState{port: port}
	st.logPath, st.logAt = raLogMark()
	raCoop.mu.Lock()
	if raCoop.owed && raCoop.port == port {
		// Paused by JukaHub already: another freeze holds it, or the last
		// one's unpause has not finished. This freeze shares that pause, and
		// the worker of the last one stands down (it sees the hold).
		raCoop.holds++
		raCoop.mu.Unlock()
		st.paused = true
		logDebug("gamepause", "RetroArch is still in JukaHub's pause; %s keeps it", why)
		return st
	}
	raCoop.mu.Unlock()
	reply, err := askRetroArchNetCmd(port, "GET_STATUS", raCoopAskWait)
	if err != nil {
		logInfo("gamepause", "RetroArch did not answer GET_STATUS (%v); freezing it without its own pause", err)
		return st
	}
	if raStatusWord(reply) != "PLAYING" {
		// Paused by the player (its pause key) or no content: nothing to undo.
		logDebug("gamepause", "RetroArch says %q; freezing it as it is", reply)
		return st
	}
	if err := sendRetroArchNetCmd(port, "PAUSE_TOGGLE"); err != nil {
		logInfo("gamepause", "could not ask RetroArch to pause: %v", err)
		return st
	}
	// From here the unpause is owed even if the pause never shows in time: the
	// toggle may still be read after the continue, and the unpause only
	// toggles while RetroArch says PAUSED.
	st.paused = true
	raCoop.mu.Lock()
	raCoop.port, raCoop.owed = port, true
	raCoop.holds++
	raCoop.mu.Unlock()
	deadline := time.Now().Add(raCoopPauseFor)
	for time.Now().Before(deadline) {
		time.Sleep(raCoopPoll)
		reply, err := askRetroArchNetCmd(port, "GET_STATUS", raCoopAskWait)
		if err == nil && raStatusWord(reply) == "PAUSED" {
			logInfo("gamepause", "RetroArch paused itself for %s", why)
			return st
		}
	}
	logInfo("gamepause", "RetroArch's pause did not show within %s for %s; freezing it anyway", raCoopPauseFor, why)
	return st
}

// raCoopResume undoes st once the game has been continued. When it was the
// last freeze holding RetroArch's pause, a worker goroutine unpauses it; the
// log check for the audio driver giving up follows either way.
func raCoopResume(st raCoopState, why string) {
	if st.port == 0 {
		return
	}
	var gen uint64
	work := false
	if st.paused {
		raCoop.mu.Lock()
		if raCoop.holds > 0 {
			raCoop.holds--
		}
		if raCoop.holds == 0 && raCoop.owed && raCoop.port == st.port {
			raCoop.gen++
			gen, work = raCoop.gen, true
		}
		raCoop.mu.Unlock()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("gamepause", "giving RetroArch back failed: %v", r)
			}
		}()
		if work && !raCoopUnpause(st.port, gen, why) {
			return
		}
		time.Sleep(raAudioCheckAfter)
		raAudioCheck(st, why)
	}()
}

// raCoopCurrent reports whether worker gen still owns the unpause: no newer
// worker started and no new freeze took the pause over.
func raCoopCurrent(gen uint64) bool {
	raCoop.mu.Lock()
	defer raCoop.mu.Unlock()
	return raCoop.gen == gen && raCoop.holds == 0 && raCoop.owed
}

// raCoopSettled ends what the unpause owes once gen saw the game play (or
// gave up on it).
func raCoopSettled(gen uint64) {
	raCoop.mu.Lock()
	if raCoop.gen == gen && raCoop.holds == 0 {
		raCoop.owed = false
	}
	raCoop.mu.Unlock()
}

// raCoopHeld reports whether something still keeps the game stopped, so no
// answer could come: the sleep (which a Sleep from the menu starts right after
// the hand-back) or a ringing alarm. Any goroutine.
func raCoopHeld() bool { return powerSleepActive() || alarmFiringActive() }

// raCoopUnpause sends PAUSE_TOGGLE while GET_STATUS says PAUSED, until it says
// PLAYING twice in a row raCoopSettle apart. GET_STATUS is answered the moment
// it is read while a toggle read in the same batch only takes effect later in
// that frame, so one PLAYING can be from before a toggle still on its way.
// It reports false when the game went away or never confirmed, or a newer
// freeze took the pause over. Its own goroutine.
func raCoopUnpause(port int, gen uint64, why string) bool {
	// A Sleep from the menu marks itself on the main thread right after the
	// hand-back: the first look waits for that.
	time.Sleep(raCoopStartDelay)
	deadline := time.Now().Add(raCoopResumeFor)
	sent := 0
	var sentAt, playingAt time.Time
	for {
		if !raCoopCurrent(gen) {
			return false // a newer freeze or worker owns it now
		}
		if gmQuitting.Load() {
			raCoopSettled(gen)
			return false // quitting: RetroArch acts on the interrupt, paused or not
		}
		now := time.Now()
		if raCoopHeld() {
			if !retroArchRunning() {
				raCoopSettled(gen)
				return false
			}
			// Stopped again: nothing answers until the sleep or the ring ends,
			// and time spent here must not read as a toggle that got lost.
			deadline = now.Add(raCoopResumeFor)
			sentAt, playingAt = now, time.Time{}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !now.Before(deadline) {
			break
		}
		time.Sleep(raCoopPoll)
		reply, err := askRetroArchNetCmd(port, "GET_STATUS", raCoopAskWait)
		if err != nil {
			playingAt = time.Time{}
			if !retroArchRunning() {
				raCoopSettled(gen)
				return false
			}
			continue
		}
		switch raStatusWord(reply) {
		case "PLAYING":
			if playingAt.IsZero() {
				playingAt = time.Now()
			} else if time.Since(playingAt) >= raCoopSettle {
				raCoopSettled(gen)
				logInfo("gamepause", "RetroArch plays again after %s", why)
				return true
			}
		case "PAUSED":
			playingAt = time.Time{}
			if sent == 0 || (sent < 3 && time.Since(sentAt) >= raCoopResendAfter) {
				if err := sendRetroArchNetCmd(port, "PAUSE_TOGGLE"); err != nil {
					logWarn("gamepause", "could not unpause RetroArch: %v", err)
					raCoopSettled(gen)
					return false
				}
				sent++
				sentAt = time.Now()
			}
		default:
			raCoopSettled(gen)
			return false
		}
	}
	raCoopSettled(gen)
	logWarn("gamepause", "RetroArch did not confirm it plays again after %s", why)
	return false
}

// raLogMark is RetroArch's log for this launch and its size now, so the check
// after the hand-back reads only what was written since.
func raLogMark() (string, int64) {
	dir := gameDiagLogDir()
	if dir == "" {
		return "", 0
	}
	path := filepath.Join(dir, "retroarch.log")
	info, err := os.Stat(path)
	if err != nil {
		return path, 0
	}
	return path, info.Size()
}

// raAudioCheck logs the audio driver's error lines RetroArch wrote since st
// was taken. Its own goroutine.
func raAudioCheck(st raCoopState, why string) {
	if st.logPath == "" {
		return
	}
	f, err := os.Open(st.logPath)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= st.logAt {
		return
	}
	start := st.logAt
	if info.Size()-start > raAudioCheckMax {
		start = info.Size() - raAudioCheckMax
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, raAudioCheckMax))
	if err != nil {
		return
	}
	lines := raAudioTrouble(string(data))
	if len(lines) == 0 {
		return
	}
	logWarn("gamepause", "RetroArch's sound reported trouble after %s: %s", why, strings.Join(lines, " | "))
}

// raAudioTrouble picks the lines of a RetroArch log that say its audio driver
// failed (the last eight, trimmed). Pure.
func raAudioTrouble(log string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(log, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case line == "":
			continue
		case strings.Contains(lower, "without sound"),
			strings.Contains(lower, "audio") && (strings.Contains(lower, "fail") || strings.Contains(lower, "error")),
			strings.Contains(lower, "alsa") && (strings.Contains(lower, "fail") || strings.Contains(lower, "error") ||
				strings.Contains(lower, "recover") || strings.Contains(lower, "underrun")):
		default:
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		out = append(out, line)
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return out
}
