package main

// raach_unlocks.go — a RetroAchievements unlock in a running game, noticed
// by JukaHub: the stick lights celebrate it, and the screen JukaHub comes
// back to says how many there were.
//
// RetroArch shows its own pop-up for an unlock; JukaHub is not on screen
// then. What it can read is RetroArch's log: every library launch already
// points RetroArch at <data>/gamediag/retroarch.log at info level, emptied
// before the launch (gamediag.go prepareGameDiag), and RetroArch's rcheevos
// client writes one info line per unlock:
//
//	[INFO] [RCHEEVOS]: Awarding achievement 19851: Mu
//
// (the words since RetroArch 1.8; newer builds add "Achievement 19851
// awarded, new score: 136" once the server took it, which is not waited for:
// the pop-up the player saw is the moment). When a RetroArch game with
// achievements on and a login starts (gameLaunchRegisterStarted), one
// goroutine reads what the log grew by once a second; each achievement id
// seen for the first time in the launch flashes the lights
// (ledAchievementFlash, ledsilky.go) and counts. When the game's process
// group is gone (gameLaunchRegisterExited) the goroutine reads the log one
// last time — a RetroArch that buffers its log writes it at exit — and ends,
// and a toast says "2 achievements unlocked in <game>". The toast is filed
// in the notification center like every toast.
//
// The same start reads whether the game plays in hardcore, from the config
// as the game got it (after every launch hook, offline RetroAchievements'
// included), for raachHardcoreRunning: the in-game menu's Load state and
// Cheats, and the sleep power-off (quickresume.go), which must not promise
// a resume RetroArch will refuse. A game whose cheats load under
// RetroArch's own values does not play in hardcore: RetroArch turns it off
// (raachCheatsWin).

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// raachTailPoll is how often the log is read while the game runs.
	raachTailPoll = time.Second
	// raachTailMax bounds one read: a log that grew more than this since
	// the last look is read from its last raachTailMax bytes.
	raachTailMax = 512 << 10
	// raachTailLineMax bounds a line kept waiting for its end.
	raachTailLineMax = 8 << 10
	// raachTailStopWait bounds the exit's wait for the last read.
	raachTailStopWait = 3 * time.Second
)

// raachUnlockRe finds an unlock in a RetroArch log line.
var raachUnlockRe = regexp.MustCompile(`(?i)\bawarding achievement\s+(\d+)`)

// raachWatch is one launch being watched. The start runs on the main
// thread, the reads on their goroutine and the exit on the reaper, so the
// count is guarded by mu.
type raachWatch struct {
	rom   string
	title string
	stop  chan struct{}
	done  chan struct{}

	mu    sync.Mutex
	ids   map[string]bool
	count int
}

// raachRun is the launch running now: its ROM, which the hardcore flag
// belongs to, and its watch (nil when none). The reaper can run a game's
// exit after the next game started (a time-lapse still being made holds
// it), so an exit only ends what is still its own.
var raachRun struct {
	mu  sync.Mutex
	rom string
	w   *raachWatch
}

// raachHard is whether the running game plays in hardcore; written under
// raachRun.mu.
var raachHard atomic.Bool

func init() {
	gameLaunchRegisterStarted(raachStarted)
	gameLaunchRegisterExited(raachExited)
}

// raachHardcoreRunning reports whether the game running now plays in
// RetroAchievements hardcore, as its config stood when it started. Any
// goroutine.
func raachHardcoreRunning() bool { return raachHard.Load() }

// raachUnlockLine is the achievement id an unlock line names. Pure.
func raachUnlockLine(line string) (string, bool) {
	m := raachUnlockRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// raachUnlockToast is the line after the game. Pure.
func raachUnlockToast(n int, title string) string {
	msg := pluralizeCount(n, "achievement", "achievements") + " unlocked"
	if title = strings.TrimSpace(title); title != "" {
		msg += " in " + title
	}
	return msg
}

// raachLogPath is RetroArch's log for the launch of rom, "" when gamediag
// did not set it up for this launch.
func raachLogPath(rom string) string {
	gameDiagState.mu.Lock()
	last := gameDiagState.lastGame
	gameDiagState.mu.Unlock()
	dir := gameDiagLogDir()
	if rom == "" || last != rom || dir == "" {
		return ""
	}
	return filepath.Join(dir, "retroarch.log")
}

// raachStarted notes whether the launch plays in hardcore and starts the
// watch (gameLaunchRegisterStarted). Main thread.
func raachStarted(game libraryGame, launcher string, _ *exec.Cmd) {
	ra := retroArchLauncher(launcher)
	cfg, hard := "", false
	if ra {
		cfg = raachConfigText(launcher)
		hard = raachOwnerPlaying() && raachHardcoreIn(cfg) && !raachCheatsWin(launcher, game)
	}
	raachRun.mu.Lock()
	old := raachRun.w
	raachRun.rom, raachRun.w = game.Path, nil
	raachHard.Store(hard)
	raachRun.mu.Unlock()
	if old != nil {
		// The last game's exit has not run yet: its count is said now.
		go raachFinish(old)
	}
	if !ra || !raachAchievementsIn(cfg) {
		return
	}
	path := raachLogPath(game.Path)
	if path == "" {
		return
	}
	title := strings.TrimSpace(game.DisplayTitle)
	if title == "" {
		title = game.Title
	}
	w := &raachWatch{
		rom: game.Path, title: title,
		stop: make(chan struct{}), done: make(chan struct{}),
		ids: map[string]bool{},
	}
	raachRun.mu.Lock()
	raachRun.w = w
	raachRun.mu.Unlock()
	go raachTail(w, path)
}

// raachExited ends the launch's hardcore flag and watch
// (gameLaunchRegisterExited). The reaper goroutine.
func raachExited(game libraryGame, _ string, _ time.Duration, _ error) {
	raachRun.mu.Lock()
	if raachRun.rom != game.Path {
		// A later launch started: the flag and the watch are its own.
		raachRun.mu.Unlock()
		return
	}
	w := raachRun.w
	raachRun.rom, raachRun.w = "", nil
	raachHard.Store(false)
	raachRun.mu.Unlock()
	if w != nil {
		raachFinish(w)
	}
}

// raachFinish stops a watch taken out of raachRun (so by one caller only),
// lets the goroutine read the log one last time and says what unlocked.
func raachFinish(w *raachWatch) {
	close(w.stop)
	select {
	case <-w.done:
	case <-time.After(raachTailStopWait):
	}
	w.mu.Lock()
	n := w.count
	w.ids = nil // a read still under way counts nothing more
	w.mu.Unlock()
	if n > 0 {
		logInfo("achievements", "%d achievement(s) unlocked in %s", n, w.title)
		ShowToast(raachUnlockToast(n, w.title), ToastKindSuccess)
	}
}

// note counts an id the first time the launch sees it and flashes the
// lights. The watch goroutine.
func (w *raachWatch) note(id string) {
	w.mu.Lock()
	fresh := w.ids != nil && !w.ids[id]
	if fresh {
		w.ids[id] = true
		w.count++
	}
	w.mu.Unlock()
	if fresh {
		logInfo("achievements", "achievement %s unlocked", id)
		ledAchievementFlash()
	}
}

// raachTail reads the log until the watch stops, then once more, and
// closes its done.
func raachTail(w *raachWatch, path string) {
	defer close(w.done)
	t := raachTailer{path: path, w: w}
	tick := time.NewTicker(raachTailPoll)
	defer tick.Stop()
	for {
		select {
		case <-w.stop:
			t.read(true)
			return
		case <-tick.C:
			t.read(false)
		}
	}
}

// raachTailer is where the last read of the log stopped.
type raachTailer struct {
	path    string
	w       *raachWatch
	off     int64
	partial string
}

// read handles what the log grew by. final also takes a last line that
// has no line break yet.
func (t *raachTailer) read(final bool) {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	size := info.Size()
	if size < t.off {
		// RetroArch started the file again.
		t.off, t.partial = 0, ""
	}
	if size-t.off > raachTailMax {
		t.off, t.partial = size-raachTailMax, ""
	}
	var text string
	if size > t.off {
		buf := make([]byte, size-t.off)
		n, _ := f.ReadAt(buf, t.off)
		t.off += int64(n)
		text = string(buf[:n])
	}
	lines := strings.Split(t.partial+text, "\n")
	t.partial = lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	if final && t.partial != "" {
		lines = append(lines, t.partial)
		t.partial = ""
	}
	if len(t.partial) > raachTailLineMax {
		t.partial = ""
	}
	for _, line := range lines {
		if id, ok := raachUnlockLine(line); ok {
			t.w.note(id)
		}
	}
}
