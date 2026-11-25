package main

// ppsspp_picture.go — did a PSP game's picture reach the panel?
//
// Juka, 2026-11-23: "I tried psp, but the screen was black and only played game
// sound." The third black PSP screen on his Smart Pro: none of them came with a
// log, and nothing on the device could tell a picture the panel showed from one
// it did not. Three causes fit a black panel over a game that plays:
//
//   - the picture is there but transparent: PPSSPP keeps the PSP's alpha, and
//     the A133P's framebuffer is ARGB (ppsspp_opaque.go makes it opaque);
//   - the panel never took PPSSPP's picture while JukaHub's own window stayed
//     open on the framebuffer beside it — the way hakuX's never showed
//     (hakux_handoff.go);
//   - nothing was drawn at all.
//
// So a PSP game started beside JukaHub on a framebuffer model (the A133P's
// PowerVR, /dev/fb0) is watched for its first 45 seconds: one sparse look at
// the page the panel shows every two seconds (every eighth pixel of every
// eighth row — about 15,000 pixels, half a megabyte read), each judged
// against the picture JukaHub left on the panel when the game started
// (ppssppPictureClassify):
//
//   - picture: lit pixels, opaque (or a framebuffer without alpha) — the check
//     ends there and says so in the log;
//   - transparent: lit pixels whose alpha is 0 on an ARGB framebuffer;
//   - unchanged: the page JukaHub left, pixel for pixel;
//   - black: nothing lit.
//
// Transparent or unchanged three looks running from 24 s, or black three
// looks running from 45 s, is the verdict. It goes into JukaHub's log with
// everything a report needs — the counts, the framebuffer's format, the
// display engine's own description of its layers (/sys/class/disp/disp/attr/
// sys) and PPSSPP's last lines — and PSP games then get the panel to
// themselves: JukaHub closes while one plays and comes back after, as for the
// Xbox (hakux_handoff.go), the way MainUI, MinUI and NextUI start PPSSPP with
// nothing else on the display. The game that was black is ended and started
// again that way at once, so the press that found the problem also plays the
// game. The PPSSPP card says why, and turns it back (ppsspp_managed_ui.go).
//
// A look is skipped, and the watch given up, while the handheld sleeps or
// JukaHub draws over the paused game (the in-game menu, the assistant, an
// alarm): the panel is not the game's then. A Select + Start force quit that
// ends a PSP game the watch never saw a picture of chooses the panel-to-itself
// way too, instead of blaming the PPSSPP build (ppssppNoteForceQuit).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ppssppPictureFirstLook is the first look after the start: PPSSPP has
	// opened its window and is booting the game.
	ppssppPictureFirstLook = 6 * time.Second
	// ppssppPictureEvery is the gap between looks.
	ppssppPictureEvery = 2 * time.Second
	// ppssppPictureJudgeClear is when a transparent or unchanged panel is
	// judged; ppssppPictureJudgeBlack when a black one is (games boot through
	// black screens, but not for long).
	ppssppPictureJudgeClear = 24 * time.Second
	ppssppPictureJudgeBlack = 45 * time.Second
	// ppssppPictureStreak is how many looks running must agree.
	ppssppPictureStreak = 3
	// ppssppPictureStep samples every eighth pixel of every eighth row.
	ppssppPictureStep = 8
	// ppssppPictureRule numbers the rule an automatic choice was made under:
	// a JukaHub with a better rule (a higher number) tries PSP games beside
	// it again.
	ppssppPictureRule = 1
	// ppssppPictureDumpMax bounds the display engine's description in the log.
	ppssppPictureDumpMax = 1600
)

// What a look found (ppssppPictureClassify).
const (
	ppssppPictureShown       = "picture"
	ppssppPictureTransparent = "transparent"
	ppssppPictureUnchanged   = "unchanged"
	ppssppPictureBlack       = "black"
)

// ppssppPictureSample is one sparse look at the page the panel shows.
type ppssppPictureSample struct {
	Samples  int    // pixels read
	Lit      int    // with a colour channel above the dark threshold
	LitClear int    // lit, with alpha under 16 (an ARGB framebuffer only)
	HasAlpha bool   // the framebuffer has an alpha channel
	Sum      uint64 // the sampled pixels' bytes, hashed
}

// String is the look for the log.
func (s ppssppPictureSample) String() string {
	line := fmt.Sprintf("%d of %d sampled pixels lit", s.Lit, s.Samples)
	if s.HasAlpha {
		line += fmt.Sprintf(", %d of those with alpha 0", s.LitClear)
	}
	return line
}

// ppssppPictureClassify judges a look against the page JukaHub left on the
// panel when the game started. "" when nothing was read. Pure.
func ppssppPictureClassify(before, now ppssppPictureSample) string {
	switch {
	case now.Samples == 0:
		return ""
	case now.Lit*200 < now.Samples: // under 0.5% lit
		return ppssppPictureBlack
	case before.Samples == now.Samples && before.Sum == now.Sum:
		return ppssppPictureUnchanged
	case now.HasAlpha && now.LitClear*100 >= now.Lit*95:
		return ppssppPictureTransparent
	}
	return ppssppPictureShown
}

// ppssppPictureJudged reports whether a verdict stands: a failed look held for
// ppssppPictureStreak looks running, from its judging time on. Pure.
func ppssppPictureJudged(verdict string, streak int, elapsed time.Duration) bool {
	if streak < ppssppPictureStreak {
		return false
	}
	switch verdict {
	case ppssppPictureTransparent, ppssppPictureUnchanged:
		return elapsed >= ppssppPictureJudgeClear
	case ppssppPictureBlack:
		return elapsed >= ppssppPictureJudgeBlack
	}
	return false
}

// ppssppPictureReason is what the PPSSPP card and the log say a verdict was.
func ppssppPictureReason(verdict string) string {
	switch verdict {
	case ppssppPictureTransparent:
		return "its picture reached the panel transparent, which shows as black"
	case ppssppPictureUnchanged:
		return "the panel kept JukaHub's last picture and never took PPSSPP's"
	case ppssppPictureBlack:
		return "the panel stayed black while the game played"
	}
	return "its picture did not reach the panel"
}

// ── Reading the panel ───────────────────────────────────────────────────────

// ppssppPictureAlphaFromVar reads struct fb_var_screeninfo (little-endian):
// the byte that holds alpha in a 32-bit pixel, from its transp field, or -1
// for a framebuffer without one. Pure.
func ppssppPictureAlphaFromVar(v []byte) int {
	if len(v) < 76 {
		return -1
	}
	bpp := binary.LittleEndian.Uint32(v[24:])
	off := binary.LittleEndian.Uint32(v[68:])
	length := binary.LittleEndian.Uint32(v[72:])
	if bpp != 32 || length != 8 || off%8 != 0 || off > 24 {
		return -1
	}
	return int(off / 8)
}

// ppssppPictureReader reads the page the panel shows, sparsely.
type ppssppPictureReader struct {
	f     *os.File
	geom  castFBGeom
	alpha int // byte index of alpha in a 32-bit pixel, -1 for none
	row   []byte
}

// ppssppPictureOpen opens the framebuffer for looks.
func ppssppPictureOpen() (*ppssppPictureReader, error) {
	f, err := os.Open(castFBDevice)
	if err != nil {
		return nil, err
	}
	hw := deviceHardware(DetectDevice())
	g, err := castProbeFB(f, int(hw.PanelW), int(hw.PanelH))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	r := &ppssppPictureReader{f: f, geom: g, alpha: -1, row: make([]byte, g.W*g.BPP/8)}
	if g.BPP == 32 {
		r.alpha = ppssppPictureAlphaByte(f)
	}
	return r, nil
}

// Close lets the framebuffer go.
func (r *ppssppPictureReader) Close() { _ = r.f.Close() }

// format is the framebuffer for the log: "1280x720, 32 bpp, alpha in byte 3".
func (r *ppssppPictureReader) format() string {
	line := fmt.Sprintf("%dx%d, %d bpp", r.geom.W, r.geom.H, r.geom.BPP)
	switch {
	case r.geom.BPP != 32:
	case r.alpha >= 0:
		line += fmt.Sprintf(", alpha in byte %d", r.alpha)
	default:
		line += ", no alpha channel"
	}
	return line
}

// sample takes one look at the page on screen now.
func (r *ppssppPictureReader) sample() (ppssppPictureSample, error) {
	s := ppssppPictureSample{HasAlpha: r.alpha >= 0}
	bpp := r.geom.BPP / 8
	if bpp != 2 && bpp != 4 {
		return s, fmt.Errorf("a %d-bit framebuffer", r.geom.BPP)
	}
	x, y := castFBPan(r.f, r.geom)
	lay := r.geom.Layout
	h := fnv.New64a()
	for row := 0; row < r.geom.H; row += ppssppPictureStep {
		off := int64(y+row)*int64(r.geom.Stride) + int64(x*bpp)
		n, err := r.f.ReadAt(r.row, off)
		if n < len(r.row) {
			if err != nil && !errors.Is(err, io.EOF) {
				return s, err
			}
			clear(r.row[n:])
		}
		for col := 0; col+bpp <= len(r.row); col += ppssppPictureStep * bpp {
			px := r.row[col : col+bpp]
			_, _ = h.Write(px)
			s.Samples++
			if bpp == 2 {
				v := uint16(px[0]) | uint16(px[1])<<8
				if v>>11&0x1f > 3 || v>>5&0x3f > 6 || v&0x1f > 3 {
					s.Lit++
				}
				continue
			}
			if px[lay.r] <= 24 && px[lay.g] <= 24 && px[lay.b] <= 24 {
				continue
			}
			s.Lit++
			if r.alpha >= 0 && px[r.alpha] < 16 {
				s.LitClear++
			}
		}
	}
	s.Sum = h.Sum64()
	return s, nil
}

// ppssppPictureDisplayDump is the display engine's own description of its
// layers, on one line for the log ("" where the driver has none).
func ppssppPictureDisplayDump() string {
	f, err := os.Open("/sys/class/disp/disp/attr/sys")
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, 16<<10))
	line := strings.Join(strings.Fields(string(raw)), " ")
	if len(line) > ppssppPictureDumpMax {
		line = line[:ppssppPictureDumpMax] + "…"
	}
	return line
}

// ppssppPictureLogTail is the last lines PPSSPP printed in this run, from the
// log JukaHub's launcher keeps beside the build ("" for none).
func ppssppPictureLogTail(lines int) string {
	st := ppssppSnapshot()
	b, ok := st.build(st.Active)
	if !ok {
		return ""
	}
	f, err := os.Open(filepath.Join(b.Dir, ppssppLogName))
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 8<<10 {
		_, _ = f.Seek(info.Size()-8<<10, io.SeekStart)
	}
	raw, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	all := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(raw), "\r", "")), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	for i, l := range all {
		if len(l) > 160 {
			all[i] = l[:160] + "…"
		}
	}
	return strings.Join(all, " | ")
}

// ── The watch ───────────────────────────────────────────────────────────────

// ppssppPictureRun is one PSP start's watch.
type ppssppPictureRun struct {
	game     libraryGame
	launcher string
	cmd      *exec.Cmd
	started  time.Time
	done     atomic.Bool // the game's process group is gone
	watching atomic.Bool // the framebuffer answered: looks are coming
	mu       sync.Mutex
	looks    int    // looks taken
	seen     bool   // a look found the picture
	verdict  string // the failed verdict, once judged
	restart  bool   // the game is being ended to start again with the panel to itself
}

// ppssppPictureCur is the start being watched (one game at a time).
var ppssppPictureCur atomic.Pointer[ppssppPictureRun]

func init() {
	gameLaunchRegisterStarted(ppssppPictureStarted)
	gameLaunchRegisterExited(ppssppPictureExited)
}

// ppssppIsPSPGame reports a PSP game start: the library's PSP row, or
// JukaHub's own PPSSPP launcher.
func ppssppIsPSPGame(game libraryGame, launcher string) bool {
	return strings.EqualFold(strings.TrimSpace(game.System), ppssppLibrarySystem) || ppssppIsManagedLauncher(launcher)
}

// ppssppPictureWanted reports whether a start is watched: a PSP game on a
// handheld whose panel is the framebuffer JukaHub can read.
func ppssppPictureWanted(game libraryGame, launcher string) bool {
	if IsWindows() || !IsTrimuiDevice() || !ppssppIsPSPGame(game, launcher) {
		return false
	}
	if deviceHardware(DetectDevice()).GPUStack != gpuStackPowerVRFbdev {
		return false
	}
	_, err := os.Stat(castFBDevice)
	return err == nil
}

// ppssppPictureStarted is the Started hook: a PSP game beside JukaHub gets
// its watch. Main thread.
func ppssppPictureStarted(game libraryGame, launcher string, cmd *exec.Cmd) {
	if cmd == nil || !ppssppPictureWanted(game, launcher) {
		return
	}
	run := &ppssppPictureRun{game: game, launcher: launcher, cmd: cmd, started: time.Now()}
	if old := ppssppPictureCur.Swap(run); old != nil {
		old.done.Store(true)
	}
	go ppssppPictureWatch(run)
}

// ppssppPictureExited is the Exited hook (the reaper goroutine): the watch
// stops, and a game ended for its picture starts again with the panel to
// itself, or the verdict is told.
func ppssppPictureExited(game libraryGame, launcher string, _ time.Duration, _ error) {
	run := ppssppPictureCur.Load()
	if run == nil || run.game.Path != game.Path || run.launcher != launcher {
		return
	}
	run.done.Store(true)
	ppssppPictureCur.CompareAndSwap(run, nil)
	run.mu.Lock()
	verdict, restart := run.verdict, run.restart
	run.mu.Unlock()
	switch {
	case restart:
		tbxPost(func() { ppssppPictureRelaunch(run.game, run.launcher, 0) })
	case verdict != "":
		title := hakuxGameTitle(run.game)
		tbxPost(func() {
			ShowToast("PSP: "+title+"'s picture did not reach the screen beside JukaHub — PSP games now get the screen to themselves (PPSSPP card on the Emulators page)", ToastKindWarning)
		})
	}
}

// ppssppPictureState is what the watch of the game on screen has seen, for a
// force quit: looked reports at least one look, seen one that found the
// picture, and early a watch that reads the panel but has not looked yet.
// All false when no watch runs (none was wanted, or the framebuffer did not
// answer).
func ppssppPictureState() (looked, seen, early bool) {
	run := ppssppPictureCur.Load()
	if run == nil || run.done.Load() {
		return false, false, false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.looks > 0 {
		return true, run.seen, false
	}
	return false, false, run.watching.Load()
}

// ppssppPicturePaused reports whether the panel is not the game's right now:
// the handheld sleeps, or JukaHub draws over the paused game.
func ppssppPicturePaused() bool {
	return powerSleepActive() || gameAssistOverlayActive() || alarmFiringActive()
}

// ppssppPictureWait sleeps d in short steps; false when the watch must stop.
func ppssppPictureWait(run *ppssppPictureRun, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if run.done.Load() {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !run.done.Load()
}

// ppssppPictureWatch looks at the panel until the picture is seen or a
// verdict stands (the file comment). Its own goroutine.
func ppssppPictureWatch(run *ppssppPictureRun) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("ppsspp", "picture check failed: %v", r)
		}
	}()
	title := hakuxGameTitle(run.game)
	r, err := ppssppPictureOpen()
	if err != nil {
		logInfo("ppsspp", "picture check: cannot read the panel's framebuffer (%v); %s is not checked", err, title)
		return
	}
	defer r.Close()
	before, err := r.sample()
	if err != nil {
		logInfo("ppsspp", "picture check: cannot read the panel's framebuffer (%v); %s is not checked", err, title)
		return
	}
	run.watching.Store(true)
	if !ppssppPictureWait(run, ppssppPictureFirstLook) {
		return
	}
	last, streak := "", 0
	for {
		if ppssppPicturePaused() {
			logInfo("ppsspp", "picture check: %s was paused (sleep or JukaHub's in-game menu) before its picture was seen; no verdict", title)
			return
		}
		s, err := r.sample()
		if err != nil {
			logInfo("ppsspp", "picture check: the framebuffer stopped answering (%v); no verdict for %s", err, title)
			return
		}
		v := ppssppPictureClassify(before, s)
		elapsed := time.Since(run.started)
		run.mu.Lock()
		run.looks++
		if v == ppssppPictureShown {
			run.seen = true
		}
		run.mu.Unlock()
		if v == ppssppPictureShown {
			logInfo("ppsspp", "picture check: %s is on the panel %s after the start (%s)", title, elapsed.Round(time.Second), s)
			return
		}
		if v == last {
			streak++
		} else {
			last, streak = v, 1
		}
		if ppssppPictureJudged(v, streak, elapsed) {
			ppssppPictureFailed(run, r, v, s, elapsed)
			return
		}
		if elapsed > ppssppPictureJudgeBlack+15*time.Second {
			// Looks that keep changing their mind (a flickering panel), or
			// read nothing: no verdict rather than a guess.
			logInfo("ppsspp", "picture check: no verdict for %s after %s (last look: %s, %s)", title, elapsed.Round(time.Second), valueOr(v, "nothing read"), s)
			return
		}
		if !ppssppPictureWait(run, ppssppPictureEvery) {
			return
		}
	}
}

// ppssppPictureFailed records a verdict: the log says everything a report
// needs, PSP games get the panel to themselves from now on, and the game that
// was black is ended to start again that way, when it can be. Watch goroutine.
func ppssppPictureFailed(run *ppssppPictureRun, r *ppssppPictureReader, verdict string, s ppssppPictureSample, elapsed time.Duration) {
	title := hakuxGameTitle(run.game)
	logWarn("ppsspp", "picture check: %s ran %s but %s (%s: %s; framebuffer %s; launcher %s)",
		title, elapsed.Round(time.Second), ppssppPictureReason(verdict), verdict, s, r.format(), run.launcher)
	if dump := ppssppPictureDisplayDump(); dump != "" {
		logInfo("ppsspp", "picture check: display layers: %s", dump)
	}
	if tail := ppssppPictureLogTail(8); tail != "" {
		logInfo("ppsspp", "picture check: PPSSPP's last lines: %s", tail)
	}
	_, already := ppssppScreenAloneReason()
	if err := ppssppSetScreenAlone(ppssppPictureReason(verdict), true); err != nil {
		logWarn("ppsspp", "picture check: could not record that PSP games get the screen to themselves: %v", err)
	}
	// Only a start the hand-off takes (a shell launcher) is worth ending:
	// RetroArch's PPSSPP core started directly would come back beside JukaHub.
	restart := !already && launcherShellScript(run.launcher) && psLauncherWaitsForHandoff() && !run.done.Load()
	run.mu.Lock()
	run.verdict = verdict
	run.restart = restart
	run.mu.Unlock()
	if !restart {
		logInfo("ppsspp", "picture check: PSP games get the screen to themselves from the next start")
		return
	}
	// The game is useless as it is: ended the way the in-game menu's Quit
	// ends it, and started again with the panel to itself once its group is
	// gone (ppssppPictureExited).
	logInfo("ppsspp", "picture check: ending %s to start it again with the screen to itself", title)
	gameLaunchNoteStop(run.cmd)
	gmQuitting.Store(true)
	gmInterruptGame(run.cmd)
	go gmQuitWatch([]*exec.Cmd{run.cmd})
}

// ppssppPictureRelaunch starts a game the watch ended again, now with the
// panel to itself. Main thread (posted by the Exited hook); it waits a few
// frames while something else still holds the screen.
func ppssppPictureRelaunch(game libraryGame, launcher string, tries int) {
	title := hakuxGameTitle(game)
	if exitRequested.Load() {
		return
	}
	if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() {
		if tries < hakuxHandoffWaitFrames {
			requestFrameEvery(50)
			tbxPost(func() { ppssppPictureRelaunch(game, launcher, tries+1) })
			return
		}
		ShowToast("PSP games now get the screen to themselves — start "+title+" again to play", ToastKindInfo)
		return
	}
	ShowToast(title+"'s picture did not reach the screen beside JukaHub — starting it again with the screen to itself", ToastKindInfo)
	if err := launchLibraryGame(game, launcher); err != nil {
		gameLaunchErrorToast(title, err)
	}
}

// ── The choice ──────────────────────────────────────────────────────────────

// ppssppScreenAloneReason is why PSP games get the panel to themselves, and
// whether they do. A choice the picture check made under an older rule is
// not one any more: this JukaHub tries beside it again.
func ppssppScreenAloneReason() (string, bool) {
	st := ppssppSnapshot()
	if strings.TrimSpace(st.ScreenAlone) == "" {
		return "", false
	}
	if st.ScreenAloneAuto && st.ScreenAloneRule < ppssppPictureRule {
		return "", false
	}
	return st.ScreenAlone, true
}

// ppssppSetScreenAlone records the choice: reason "" plays PSP games beside
// JukaHub again; auto marks one the picture check made.
func ppssppSetScreenAlone(reason string, auto bool) error {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		st.ScreenAlone, st.ScreenAloneAt, st.ScreenAloneAuto, st.ScreenAloneRule = "", "", false, 0
	} else {
		st.ScreenAlone = reason
		st.ScreenAloneAt = time.Now().UTC().Format(time.RFC3339)
		st.ScreenAloneAuto = auto
		st.ScreenAloneRule = ppssppPictureRule
	}
	return ppssppSaveLocked()
}

// ppssppHandoffWanted reports whether a start goes the screen hand-off's way
// (hakux_handoff.go): a PSP game, by a shell launcher (JukaHub's PPSSPP
// script or the firmware's), on a TrimUI, while PSP games get the panel to
// themselves.
func ppssppHandoffWanted(game libraryGame, launcher string) bool {
	if IsWindows() || !IsTrimuiDevice() || !ppssppIsPSPGame(game, launcher) || !launcherShellScript(launcher) {
		return false
	}
	_, on := ppssppScreenAloneReason()
	return on
}
