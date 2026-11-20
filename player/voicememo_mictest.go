package main

// voicememo_mictest.go — Voice Memos' Microphone test.
//
// The TrimUI handhelds' microphones have been the least predictable part of
// the voice features (voicemic.go: a default capture device that cannot
// record, codec inputs shipped switched off), so the first visit opens this
// page before the list, and it stays one press away (the Options sheet, and
// the empty list). It runs by itself: it opens the microphone the way a
// recording does (micOpen), shows which device opened and a live level, waits
// up to ten seconds for a voice, records a three-second sample (with the
// moment before the voice started), plays it back, and ends on one outcome:
//
//   - works (or works but quiet: it heard sound, never a voice-loud one);
//   - "No microphone found" — nothing opened; whether anything that records
//     is listed at all (micPresence) picks the explanation;
//   - "The microphone opens but hears nothing" — on Linux with the hint that
//     JukaHub turns the codec's microphone switches on, so a silence that
//     survives that is the hardware or the firmware.
//
// The outcome and what led to it go to the log under "mic", next to
// voicemic.go's own lines about every device it tried. A pass is remembered
// in voicememos.json and the page stops opening first; so does a skip.

import (
	"errors"
	"runtime"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// vmTestPhase is where the test is.
type vmTestPhase int

const (
	vmTestOff vmTestPhase = iota
	vmTestListening
	vmTestSampling
	vmTestPlaying
	vmTestDone
)

// vmTestResult is the test's outcome.
type vmTestResult int

const (
	vmResultNone vmTestResult = iota
	vmResultWorks
	vmResultQuiet
	vmResultNoMic
	vmResultNoOpen
	vmResultSilent
)

// vmTestPreRoll is how much sound from before the voice the sample keeps
// (the first syllable).
const vmTestPreRoll = vmBytesPerSec * 3 / 10

// vmTest is the test's state. Main thread only.
var vmTest struct {
	phase   vmTestPhase
	result  vmTestResult
	mic     *micStream
	via     string
	started time.Time
	level   float64
	peak    float64
	heard   bool
	quiet   bool
	pre     []byte
	pcm     []byte
	buf     []byte
	player  *vmPlayer
	playErr string
	focus   int
	rects   []sdl.Rect
	wrap    tbxWrapCache
}

// vmTestBusy reports whether the test holds the microphone or the speaker.
func vmTestBusy() bool {
	switch vmTest.phase {
	case vmTestListening, vmTestSampling, vmTestPlaying:
		return true
	}
	return false
}

// vmTestStart (re)starts the test.
func vmTestStart() {
	vmTestStop()
	vmTest.result = vmResultNone
	vmTest.via, vmTest.playErr = "", ""
	vmTest.level, vmTest.peak = 0, 0
	vmTest.heard, vmTest.quiet = false, false
	vmTest.pre = vmTest.pre[:0]
	vmTest.pcm = vmTest.pcm[:0]
	vmTest.focus = 0
	vmTest.started = time.Now()
	jukaSpeakStop() // the microphone must not hear Juka (tts_api.go)
	mic, err := micOpen()
	if err != nil {
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			vmTestFinish(vmResultNoMic, err)
		} else {
			vmTestFinish(vmResultNoOpen, err)
		}
		return
	}
	vkMic.Store(vkMicPresent)
	vmTest.mic = mic
	vmTest.via = mic.Via()
	// The ten seconds count from the open: on a handheld micOpen may spend
	// seconds trying devices before one starts.
	vmTest.started = time.Now()
	vmTest.phase = vmTestListening
	vmMediaSync()
	logInfo("mic", "Voice Memos microphone test: listening on %s", vmTest.via)
}

// vmTestStop ends a test that is running and lets go of the microphone and
// the speaker. A finished test keeps its outcome on the page.
func vmTestStop() {
	if vmTest.mic != nil {
		vmTest.mic.Close()
		vmTest.mic = nil
	}
	if vmTest.player != nil {
		vmTest.player.stop()
		vmTest.player = nil
	}
	if vmTestBusy() {
		vmTest.phase = vmTestOff
	}
	vmMediaSync()
}

// vmTestTick runs the test, once a frame.
func vmTestTick() {
	switch vmTest.phase {
	case vmTestListening, vmTestSampling:
		vmTestRead()
	case vmTestPlaying:
		if vmTest.player == nil {
			vmTestFinish(vmTestVerdict(), nil)
			return
		}
		if done, failed := vmTest.player.finished(); done {
			vmTest.player = nil
			vmTest.playErr = failed
			vmTestFinish(vmTestVerdict(), nil)
		}
	}
}

// vmTestVerdict is the outcome of a test whose sample was recorded.
func vmTestVerdict() vmTestResult {
	if vmTest.quiet {
		return vmResultQuiet
	}
	return vmResultWorks
}

// vmTestRead takes what the microphone captured and moves the test on.
func vmTestRead() {
	m := vmTest.mic
	if m == nil {
		return
	}
	if cap(vmTest.buf) < micRate*2 {
		vmTest.buf = make([]byte, micRate*2)
	}
	buf := vmTest.buf[:micRate*2]
	for i := 0; i < 8; i++ {
		n := m.Read(buf)
		if n <= 0 {
			break
		}
		vmTestTake(buf[:n])
		if vmTest.phase != vmTestListening && vmTest.phase != vmTestSampling {
			return
		}
		if n < len(buf) {
			break
		}
	}
	if via := m.Via(); via != "" {
		vmTest.via = via
	}
	if m.Heard() {
		vmTest.heard = true
	}
	if vmTest.phase == vmTestListening && time.Since(vmTest.started) >= vmTestWait {
		if !vmTest.heard {
			vmTestFinish(vmResultSilent, nil)
			return
		}
		// Sound came in, but never as loud as a voice: the sample is taken
		// anyway, and the outcome says the microphone is quiet.
		vmTest.quiet = true
		vmTest.phase = vmTestSampling
		vmTest.pcm = append(vmTest.pcm[:0], vmTest.pre...)
	}
}

// vmTestTake feeds one chunk to the meter, the pre-roll and the sample.
func vmTestTake(pcm []byte) {
	if time.Since(vmTest.started) < vmChimeSkipMs*time.Millisecond {
		// The press that started the test, from the speaker.
		return
	}
	level := vkPCMLevel(pcm)
	if level > vmTest.level {
		vmTest.level = level
	} else {
		vmTest.level = vmTest.level*0.8 + level*0.2
	}
	if level > vmTest.peak {
		vmTest.peak = level
	}
	switch vmTest.phase {
	case vmTestListening:
		vmTest.pre = append(vmTest.pre, pcm...)
		if over := len(vmTest.pre) - vmTestPreRoll; over > 0 {
			over += over % 2
			vmTest.pre = append(vmTest.pre[:0], vmTest.pre[over:]...)
		}
		if level >= vmTestVoice {
			vmTest.phase = vmTestSampling
			vmTest.pcm = append(vmTest.pcm[:0], vmTest.pre...)
		}
	case vmTestSampling:
		want := int(vmTestSample/time.Second)*vmBytesPerSec - len(vmTest.pcm)
		if want > len(pcm) {
			want = len(pcm)
		}
		if want > 0 {
			vmTest.pcm = append(vmTest.pcm, pcm[:want]...)
		}
		if len(vmTest.pcm) >= int(vmTestSample/time.Second)*vmBytesPerSec {
			vmTestPlayBack()
		}
	}
}

// vmTestPlayBack closes the microphone and plays the sample.
func vmTestPlayBack() {
	if vmTest.mic != nil {
		vmTest.mic.Close()
		vmTest.mic = nil
	}
	sample := append([]byte(nil), vmTest.pcm...)
	vmTest.phase = vmTestPlaying
	vmTest.player = vmStartPlayer("", vmTestSample, vmPlaySource{mem: sample, memRate: vmRate})
	vmMediaSync()
}

// vmTestFinish ends the test on result, remembers a pass, and logs what the
// test saw.
func vmTestFinish(result vmTestResult, err error) {
	if vmTest.mic != nil {
		vmTest.mic.Close()
		vmTest.mic = nil
	}
	if vmTest.player != nil {
		vmTest.player.stop()
		vmTest.player = nil
	}
	vmTest.phase = vmTestDone
	vmTest.result = result
	// The focus starts on the outcome's primary button: "Record a memo" after
	// a pass, "Test again" after a failure.
	vmTest.focus = 0
	for i, b := range vmTestButtons() {
		if b.primary {
			vmTest.focus = i
		}
	}
	passed := result == vmResultWorks || result == vmResultQuiet
	vm.saved.TestPassed = passed
	vm.saved.TestResult = vmTestResultWord(result)
	vm.saved.TestDevice = vmTest.via
	vmStore()
	if passed {
		PlaySuccessSound()
	} else {
		PlayErrorSound()
	}
	logInfo("mic", "Voice Memos microphone test: %s — device %q, heard %t, peak %.3f, playback %q, presence %d, SDL audio driver %q, error %v",
		vm.saved.TestResult, vmTest.via, vmTest.heard, vmTest.peak, vmTest.playErr, micPresence(), sdl.GetCurrentAudioDriver(), err)
	vmMediaSync()
}

// vmTestResultWord names an outcome for the state file and the log.
func vmTestResultWord(r vmTestResult) string {
	switch r {
	case vmResultWorks:
		return "works"
	case vmResultQuiet:
		return "works, quiet"
	case vmResultNoMic:
		return "no microphone"
	case vmResultNoOpen:
		return "could not open"
	case vmResultSilent:
		return "hears nothing"
	}
	return ""
}

// vmTestSkip leaves the test for the list. A test that has not passed counts
// as skipped, so the page stops opening first.
func vmTestSkip() {
	vmTestStop()
	if !vm.saved.TestPassed && !vm.saved.TestSkipped {
		vm.saved.TestSkipped = true
		vmStore()
	}
	vm.mode = vmModeList
	PlayBackSound()
}

// ── Buttons and input ────────────────────────────────────────────────────

// vmTestButton is one button under the test.
type vmTestButton struct {
	id      string
	label   string
	primary bool
}

// vmTestButtons are the buttons for where the test is.
func vmTestButtons() []vmTestButton {
	switch {
	case vmTest.phase != vmTestDone:
		return []vmTestButton{{id: "skip", label: "Skip the test"}}
	case vmTest.result == vmResultWorks || vmTest.result == vmResultQuiet:
		return []vmTestButton{{id: "again", label: "Test again"}, {id: "record", label: "Record a memo", primary: true}}
	}
	return []vmTestButton{{id: "again", label: "Test again", primary: true}, {id: "memos", label: "Back to memos"}}
}

// vmTestAction is one action on the test page.
func vmTestAction(act tbxAction) bool {
	buttons := vmTestButtons()
	if vmTest.focus >= len(buttons) {
		vmTest.focus = len(buttons) - 1
	}
	if vmTest.focus < 0 {
		vmTest.focus = 0
	}
	switch act {
	case tbxActLeft:
		if vmTest.focus > 0 {
			vmTest.focus--
			PlayNavSound()
		}
	case tbxActRight:
		if vmTest.focus < len(buttons)-1 {
			vmTest.focus++
			PlayNavSound()
		}
	case tbxActConfirm:
		vmTestPress(buttons[vmTest.focus].id)
	case tbxActBack:
		vmTestSkip()
	case tbxActUp:
		// The top bar.
		return false
	}
	return true
}

// vmTestPress runs a button.
func vmTestPress(id string) {
	switch id {
	case "skip", "memos":
		vmTestSkip()
	case "again":
		PlayActivateSound()
		vmTestStart()
	case "record":
		vmLeaveTest()
		vmRecStart()
	}
}

// ── Words ────────────────────────────────────────────────────────────────

// vmTestHeadline is the test's big line and the sentence under it.
func vmTestHeadline() (string, string) {
	switch vmTest.phase {
	case vmTestListening:
		return "Say something…", "Talk as you would into a phone. JukaHub listens for up to ten seconds."
	case vmTestSampling:
		return "Recording a 3-second sample…", "Keep talking."
	case vmTestPlaying:
		return "Playing it back…", "You should hear what you just said."
	case vmTestOff:
		return "Microphone test", "Checks that the microphone opens, hears you and plays back."
	}
	playback := ""
	if vmTest.playErr != "" {
		playback = " It could not play the sample back, though: " + vmTest.playErr + "."
	}
	switch vmTest.result {
	case vmResultWorks:
		return "The microphone works", "It heard you on " + vmTestDevice() + ". Memos will record from it." + playback
	case vmResultQuiet:
		return "The microphone works, but it is quiet",
			"It hears sound on " + vmTestDevice() + ", but your voice stayed faint — speak closer to the microphone." + playback
	case vmResultNoMic:
		if micPresence() == vkMicAbsent {
			return "No microphone found", "Nothing that can record is listed: SDL has no capture device and the kernel no capture PCM." + vmTestPCHint()
		}
		return "No microphone found", "A capture device is listed, but none of them opened. The log (category “mic”) names each one and why." + vmTestPCHint()
	case vmResultNoOpen:
		return "The microphone could not be opened", "Every capture device JukaHub tried refused to open. The log (category “mic”) names each one and why." + vmTestPCHint()
	case vmResultSilent:
		if runtime.GOOS == "windows" {
			return "The microphone opens but hears nothing", "It opened " + vmTestDevice() + ", but only silence came in." + vmTestPCHint()
		}
		return "The microphone opens but hears nothing", "It opened " + vmTestDevice() + ", but only silence came in. JukaHub switches the codec's microphone inputs on before it records, so the microphone may be off in hardware or the firmware gives it no capture path. The log (category “mic”) lists what was tried."
	}
	return "Microphone test", ""
}

// vmTestPCHint is the PC's half of a failure: Windows' own switch.
func vmTestPCHint() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return " Check that a microphone is plugged in and not muted, and that Windows lets apps use it (Settings → Privacy → Microphone)."
}

// vmTestDevice names the device the test used.
func vmTestDevice() string {
	if vmTest.via == "" {
		return "the microphone"
	}
	return vmTest.via
}

// vmTestSteps is the state of each line of the test's checklist: 0 waiting,
// 1 under way, 2 done, 3 failed.
func vmTestSteps() [4]int {
	var s [4]int
	switch vmTest.phase {
	case vmTestListening:
		s = [4]int{2, 1, 0, 0}
		if vmTest.heard {
			s[1] = 2
			s[2] = 1
		}
	case vmTestSampling:
		s = [4]int{2, 2, 1, 0}
	case vmTestPlaying:
		s = [4]int{2, 2, 2, 1}
	case vmTestDone:
		switch vmTest.result {
		case vmResultWorks, vmResultQuiet:
			s = [4]int{2, 2, 2, 2}
			if vmTest.playErr != "" {
				s[3] = 3
			}
		case vmResultNoMic, vmResultNoOpen:
			s = [4]int{3, 0, 0, 0}
		case vmResultSilent:
			s = [4]int{2, 3, 0, 0}
		}
	}
	return s
}

// vmTestStepLabels are the checklist's lines.
var vmTestStepLabels = [4]string{"Opens the microphone", "Hears sound", "Records a 3-second sample", "Plays it back"}
