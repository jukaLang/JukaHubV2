package main

// voicememo.go — Voice Memos: record, name, replay and send short recordings
// to a phone, with a microphone test before the first one.
//
// The app is one scene in the tool screens' style (tbxui.go): a recorder card
// at the top, the memos under it newest first, and the Microphone test page.
// Its parts live in their own files:
//
//   - voicememo_files.go: the folder (osAppsUserDir("VoiceMemos"), which the
//     FTP server and the Phone Companion's Files card share), the listing with
//     each memo's length read from its WAV header off the render thread, names
//     made safe for the card's FAT file system, rename, delete, and the
//     recovery of a recording a crash cut off;
//   - voicememo_rec.go: recording, streamed to a hidden temporary file so a
//     long memo never lives in RAM and a crash leaves at most that file;
//   - voicememo_play.go: playback on a goroutine through the shared outputs
//     (audioroute.go, so a Bluetooth headset hears it), and the music, video
//     and menu music that are paused while a memo records or plays;
//   - voicememo_mictest.go: the Microphone test;
//   - voicememo_phone.go: "Send to phone", a QR code whose link downloads the
//     memo through the Phone Companion;
//   - voicememo_ui.go: drawing, the Options sheet and mouse hit-testing.
//
// The microphone is the shared one (voicemic.go). micOpen makes "Hey Juka",
// the stick lights and Chat's voice capture stand aside while a memo records,
// and they come back by themselves once the last voice stream closes.
//
// Everything here runs on the main thread, apart from the listing and
// playback goroutines, which hand their results back through tbxPost or their
// own locks.

import (
	"encoding/json"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vmStateFile keeps the test result and the next default name number.
	vmStateFile = "voicememos.json"
	// vmFolderName is the card folder (and the FTP / Phone Companion share).
	vmFolderName = "VoiceMemos"
	// vmRate is what a memo is recorded at: the microphone's own rate, mono
	// 16-bit, about 1.9 MB a minute.
	vmRate          = micRate
	vmBytesPerSec   = vmRate * 2
	vmMaxRecord     = 30 * time.Minute
	vmMinFreeBytes  = 20 << 20
	vmSeekStep      = 5 * time.Second
	vmTestSample    = 3 * time.Second
	vmTestWait      = 10 * time.Second
	vmStallStop     = 5 * time.Second
	vmNameMaxRunes  = 80
	vmFreeCheckGap  = 2 * time.Second
	vmChimeSkipMs   = 300
	vmMicLostAfter  = 3 * time.Second
	vmTestVoice     = 0.02
	vmWaveBars      = 96
	vmWaveBarFrames = vmRate / 20
)

// vmMode is the page on show.
type vmMode int

const (
	vmModeList vmMode = iota
	vmModeTest
)

// vmSaved is voicememos.json.
type vmSaved struct {
	TestPassed  bool   `json:"micTestPassed,omitempty"`
	TestSkipped bool   `json:"micTestSkipped,omitempty"`
	TestResult  string `json:"micTestResult,omitempty"`
	TestDevice  string `json:"micTestDevice,omitempty"`
	Next        int    `json:"nextNumber,omitempty"`
}

// vm is the app's state. Main thread only.
var vm struct {
	entered bool
	visit   tbxVisit
	mode    vmMode
	dir     string
	nav     tbxNav

	memos    []vmMemo
	loaded   bool
	scanGen  int
	scanBusy bool

	saved       vmSaved
	savedLoaded bool

	rec  *vmRecording
	play *vmPlayer

	// lastTick is the previous frame's time: a long gap while recording means
	// the handheld slept or the app stalled (vmTick).
	lastTick time.Time

	menu vmMenu
}

// The Tools hub reads these (voiceMemoHubValue) without touching vm.
var (
	vmHubCount  atomic.Int32 // memos listed; -1 until the folder was read
	vmHubRecSec atomic.Int32 // seconds recorded; -1 when not recording
)

func init() {
	vmHubCount.Store(-1)
	vmHubRecSec.Store(-1)
	registerScene(voiceMemoScene{sceneBase{name: voiceMemoSceneName}})
}

// ── Scene ────────────────────────────────────────────────────────────────

type voiceMemoScene struct{ sceneBase }

func (voiceMemoScene) render(r *sdl.Renderer, c *Config) bool {
	if vm.visit.begin() && !vm.entered {
		// The scene hooks call voiceMemoEnter; a visit that bypassed them
		// still starts right. Only a new visit does: the fade-out after
		// voiceMemoLeave draws the page too, and must not start it again.
		voiceMemoEnter(c)
	}
	vmTick()
	vmRender(r, c)
	return true
}

func (voiceMemoScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return vmHandleKey(e, c)
}

func (voiceMemoScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return vmDispatch(c, tbxPadAction(e))
}

// ── Contract (the scene hooks, Tools hub and footer) ─────────────────────

// voiceMemoEnter starts a visit: the state file, the folder listing (which
// first recovers a recording a crash cut off), and the Microphone test first
// while it has neither passed nor been skipped.
func voiceMemoEnter(config *Config) {
	vm.entered = true
	vmLoadSaved()
	vm.dir = osAppsUserDir(vmFolderName)
	vm.menu.open = false
	vm.nav.scroll = 0
	vm.lastTick = time.Time{}
	vm.mode = vmModeList
	if vm.rec == nil && !vm.saved.TestPassed && !vm.saved.TestSkipped {
		vm.mode = vmModeTest
		vmTestStart()
	}
	vmScanAsync(true)
}

// voiceMemoLeave ends a visit: a recording in progress is stopped and saved
// (never lost), playback and the test stop, and the microphone is let go.
func voiceMemoLeave() {
	if vm.rec != nil {
		vmRecFinish(vmStopLeft)
	}
	vmTestStop()
	vmPlayStop()
	vm.menu.open = false
	vm.entered = false
	vmMediaSync()
}

// voiceMemoFlush is the exit path: a recording is finished on the spot and
// playback's device closed before SDL's audio goes away.
func voiceMemoFlush() {
	// The music and a video Voice Memos paused stay paused: the exit stops
	// them next (shutdownStopPlayback), and resuming them for that moment
	// would be a blip of sound on the way out.
	vmHush.music, vmHush.video = false, false
	if vm.rec != nil {
		vmRecFinish(vmStopExit)
	}
	players := []*vmPlayer{vm.play, vmTest.player}
	vmTestStop()
	vmPlayStop()
	for _, p := range players {
		if p != nil {
			p.wait(time.Second)
		}
	}
	vmMediaSync()
}

// voiceMemoHubValue is the Tools hub tile's line: the recording's clock while
// one runs (true: show it as live), else how many memos there are. Atomics
// only, no disk.
func voiceMemoHubValue() (string, bool) {
	if s := vmHubRecSec.Load(); s >= 0 {
		return "Recording · " + vmClock(time.Duration(s)*time.Second), true
	}
	switch n := vmHubCount.Load(); {
	case n < 0:
		return "Record, name and replay short recordings", false
	case n == 0:
		return "No memos yet", false
	case n == 1:
		return "1 memo", false
	default:
		return strconv.Itoa(int(n)) + " memos", false
	}
}

// ── State file ───────────────────────────────────────────────────────────

// vmLoadSaved reads voicememos.json once a run.
func vmLoadSaved() {
	if vm.savedLoaded {
		return
	}
	vm.savedLoaded = true
	data, err := os.ReadFile(osAppsDataPath(vmStateFile))
	if err != nil {
		return
	}
	var s vmSaved
	if json.Unmarshal(data, &s) == nil {
		vm.saved = s
	}
}

// vmStore writes voicememos.json.
func vmStore() {
	data, err := json.MarshalIndent(vm.saved, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(osAppsDataPath(vmStateFile), data, 0o644); err != nil {
		logWarn("voicememo", "could not save %s: %v", vmStateFile, err)
	}
}

// ── Frame tick ───────────────────────────────────────────────────────────

// vmTick runs once a frame while the page is drawn: it records, runs the
// test, notices the end of playback, keeps the idle screen away while sound is
// being made, and holds or gives back the other audio.
func vmTick() {
	now := time.Now()
	gap := time.Duration(0)
	if !vm.lastTick.IsZero() {
		gap = now.Sub(vm.lastTick)
	}
	vm.lastTick = now
	if vm.rec != nil {
		switch {
		case gap > vmStallStop || powerSleepActive():
			// The handheld slept or the app stood still: the memo has a hole,
			// so it ends here, saved, rather than running on after it.
			vmRecFinish(vmStopSlept)
		case voiceOrbActive():
			vmRecFinish(vmStopOrb)
		default:
			vmRecPump(now)
		}
	}
	vmTestTick()
	if p := vm.play; p != nil {
		if done, failed := p.finished(); done {
			vmPlayStop()
			if failed != "" {
				ShowToast("Couldn't play the memo — "+failed, ToastKindWarning)
				PlayErrorSound()
			}
		} else if alarmFiringActive() && !p.isPaused() {
			// The alarm has the speaker: the memo waits for the user.
			p.setPaused(true)
		}
	}
	vmMediaSync()
	if vmAudioBusy.Load() {
		// Use without input, like a video: no idle screen, no auto sleep, and
		// frames for the meter and the progress bar.
		noteUserInputTime(now)
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
	}
	if vm.rec != nil {
		vmHubRecSec.Store(int32(vm.rec.elapsed() / time.Second))
	} else {
		vmHubRecSec.Store(-1)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// vmHandleKey takes the keyboard's own keys first (R records, Space pauses a
// recording, Delete deletes, N renames), then the keys every tool screen
// shares (tbxKeyAction).
func vmHandleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	plain := e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) == 0
	if plain && !tbxSheetVisible(c) && !vm.menu.open && vm.mode == vmModeList {
		switch e.Keysym.Sym {
		case sdl.K_r:
			if e.Repeat == 0 {
				vmRecordKey()
			}
			return true
		case sdl.K_SPACE:
			if vm.rec != nil {
				if e.Repeat == 0 {
					vmRecTogglePause()
				}
				return true
			}
		case sdl.K_DELETE:
			if vm.rec == nil {
				if m, ok := vmFocusedMemo(); ok {
					vmAskDelete(c, m)
				}
			}
			return true
		case sdl.K_n:
			if vm.rec == nil {
				if m, ok := vmFocusedMemo(); ok {
					vmAskRename(m)
				}
			}
			return true
		}
	}
	act := tbxKeyAction(e)
	if e.Repeat != 0 && (act == tbxActConfirm || act == tbxActX || act == tbxActY || act == tbxActBack) {
		// A held key must not stop a recording and start the next one.
		return act != tbxActNone
	}
	return vmDispatch(c, act)
}

// vmDispatch routes one action: the delete question (a tbx sheet), the Options
// sheet, then the page. An unused Back leaves the scene; an unused Up at the
// top goes to the top bar.
func vmDispatch(c *Config, act tbxAction) bool {
	if act == tbxActNone {
		return false
	}
	if tbxSheetVisible(c) {
		tbxSheetAction(act)
		return true
	}
	if vm.menu.open {
		vmMenuAction(c, act)
		return true
	}
	handled := false
	if vm.mode == vmModeTest {
		handled = vmTestAction(act)
	} else {
		handled = vmListAction(c, act)
	}
	if !handled && act == tbxActBack {
		goBackScene(c)
		return true
	}
	return handled
}

// vmListAction is one action on the memo list.
func vmListAction(c *Config, act tbxAction) bool {
	if vm.rec != nil {
		// While recording: A and B stop and save, X pauses; the list waits.
		switch act {
		case tbxActConfirm, tbxActBack:
			vmRecStopAndName()
			return true
		case tbxActX:
			vmRecTogglePause()
			return true
		case tbxActY:
			return true
		}
		return tbxNavAction(&vm.nav, act)
	}
	if p := vm.play; p != nil {
		switch act {
		case tbxActLeft, tbxActPageUp:
			p.seekBy(-vmSeekStep)
			PlayNavSound()
			return true
		case tbxActRight, tbxActPageDown:
			p.seekBy(vmSeekStep)
			PlayNavSound()
			return true
		case tbxActBack:
			vmPlayStop()
			PlayBackSound()
			return true
		}
	}
	switch act {
	case tbxActConfirm:
		vmActivate(c, vm.nav.focus)
		return true
	case tbxActX:
		vmOpenMenu()
		return true
	case tbxActY:
		vmRecordKey()
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(&vm.nav, act)
}

// vmActivate is A (or a click) on a control of the list.
func vmActivate(c *Config, id string) {
	switch {
	case id == vmIDRecord:
		vmRecStart()
	case id == vmIDMicTest:
		vmOpenTest()
	case id == vmIDRecPause:
		vmRecTogglePause()
	case id == vmIDRecStop:
		vmRecStopAndName()
	default:
		if m, ok := vmMemoByID(id); ok {
			vmPlayToggle(m)
		}
	}
}

// vmRecordKey is Y (or R): record, after stopping a memo that plays.
func vmRecordKey() {
	if vm.rec != nil {
		return
	}
	vmRecStart()
}

// vmOpenTest shows the Microphone test page and starts it.
func vmOpenTest() {
	if vm.rec != nil {
		ShowToast("Stop the recording first", ToastKindInfo)
		return
	}
	vmPlayStop()
	vm.mode = vmModeTest
	vmTestStart()
}

// vmLeaveTest goes back to the list.
func vmLeaveTest() {
	vmTestStop()
	vm.mode = vmModeList
}
