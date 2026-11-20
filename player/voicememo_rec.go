package main

// voicememo_rec.go — Voice Memos' recorder.
//
// The microphone is the shared one (voicemic.go), opened with micOpen so the
// wake word, the stick lights and Chat's voice capture stand aside for it. It
// is read every frame, up to eight reads a frame so a slow frame loses
// nothing, and what it gives is written straight to a hidden temporary WAV in
// the memos' folder through a 64 KB buffer: a thirty-minute memo (55 MB)
// never sits in RAM, and a crash or a power cut leaves at most that file,
// which the next visit recovers (voicememo_files.go). The header goes in with
// zero sizes; on stop the buffer is flushed, the real sizes are written, the
// file is synced and renamed to its default name "Memo n". The memo is saved
// before the naming keyboard opens, so B there only keeps the default name.
//
// A recording stops by itself, saved, at thirty minutes, when free space on
// the card drops under 20 MB, when the handheld sleeps or the app stalls
// (the memo would have a hole), when the voice orb takes the microphone, when
// the microphone stops giving anything, and when the page is left.

import (
	"bufio"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// vmMaxBytes is a recording's cap in bytes of sound.
const vmMaxBytes = int64(vmMaxRecord/time.Second) * vmBytesPerSec

// vmStopWhy is why a recording ended; it picks the toast.
type vmStopWhy int

const (
	vmStopUser vmStopWhy = iota
	vmStopCap
	vmStopSpace
	vmStopSlept
	vmStopOrb
	vmStopMicLost
	vmStopWriteErr
	vmStopLeft
	vmStopExit
)

// vmRecording is the recording in progress. Main thread only.
type vmRecording struct {
	mic *micStream
	f   *os.File
	w   *bufio.Writer
	tmp string
	via string
	// data is the bytes of sound taken so far; the clock is read from it, so
	// a moment the microphone gave nothing (a device swap) does not count.
	data   int64
	paused bool
	// skipUntil drops the start (and resume) chime, which the speaker plays
	// into the microphone.
	skipUntil uint64
	// level is the meter (fast attack, slow release); peak the loudest chunk.
	level float64
	peak  float64
	// wave is the rolling waveform, one bar per vmWaveBarFrames samples.
	wave    []float32
	waveSum float64
	waveN   int
	buf     []byte
	freeAt  time.Time
	lostAt  time.Time
	wErr    error
}

// elapsed is how much sound has been recorded.
func (r *vmRecording) elapsed() time.Duration {
	if r == nil {
		return 0
	}
	return time.Duration(r.data * int64(time.Second) / vmBytesPerSec)
}

// vmRecStart starts a recording, unless the card is full or no microphone
// opens — a toast says which.
func vmRecStart() {
	if vm.rec != nil || vm.mode != vmModeList {
		return
	}
	vmPlayStop()
	dir := vm.dir
	if dir == "" {
		dir = osAppsUserDir(vmFolderName)
		vm.dir = dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("voicememo", "cannot create %s: %v", dir, err)
		ShowToast("Couldn't create the "+vmFolderName+" folder", ToastKindError)
		PlayErrorSound()
		return
	}
	if free, err := filesFreeSpace(dir); err == nil && free < vmMinFreeBytes {
		ShowToast("The card is almost full — free some space to record", ToastKindWarning)
		PlayErrorSound()
		return
	}
	jukaSpeakStop() // the microphone must not hear Juka (tts_api.go)
	mic, err := micOpen()
	if err != nil {
		msg := "Couldn't open the microphone — the Microphone test (X) says more"
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			msg = "No microphone found — the Microphone test (X) says more"
		}
		logWarn("voicememo", "no recording: %v", err)
		ShowToast(msg, ToastKindWarning)
		PlayErrorSound()
		return
	}
	vkMic.Store(vkMicPresent)
	tmp := filepath.Join(dir, vmTempPrefix+strconv.FormatInt(time.Now().UnixNano(), 36)+".wav")
	// Marked before it exists, so a recovery running from this visit's
	// listing can never take it for a crash's leftover.
	vmSetActiveTemp(tmp)
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		if _, werr := f.Write(vmWAVHeader(0)); werr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			f, err = nil, werr
		}
	}
	if err != nil {
		mic.Close()
		vmSetActiveTemp("")
		logWarn("voicememo", "cannot write %s: %v", tmp, err)
		ShowToast("Couldn't write to the "+vmFolderName+" folder", ToastKindError)
		PlayErrorSound()
		return
	}
	// Not marked hidden on Windows (filesSetHidden): the attribute would
	// survive the rename and hide the finished memo. The dot hides it from
	// the list, and from the card's own file browsers.
	r := &vmRecording{
		mic:       mic,
		f:         f,
		w:         bufio.NewWriterSize(f, 64<<10),
		tmp:       tmp,
		via:       mic.Via(),
		skipUntil: sdl.GetTicks64() + vmChimeSkipMs,
		wave:      make([]float32, 0, vmWaveBars),
		buf:       make([]byte, micRate*2),
		freeAt:    time.Now(),
	}
	vm.rec = r
	vm.nav.focus = vmIDRecStop
	vm.menu.open = false
	// The stall check (vmTick) counts from here: a micOpen that tried device
	// after device for seconds is not the handheld sleeping, and must not end
	// the recording on its first frame.
	vm.lastTick = time.Now()
	vmMediaSync()
	PlayActivateSound()
	logInfo("voicememo", "recording from %s into %s", r.via, filepath.Base(tmp))
}

// vmRecPump takes what the microphone captured since the last frame and
// applies the automatic stops.
func vmRecPump(now time.Time) {
	r := vm.rec
	if r == nil {
		return
	}
	for i := 0; i < 8; i++ {
		n := r.mic.Read(r.buf)
		if n <= 0 {
			break
		}
		vmRecTake(r, r.buf[:n])
		if n < len(r.buf) {
			break
		}
	}
	if via := r.mic.Via(); via != "" {
		r.via = via
	}
	switch {
	case r.wErr != nil:
		vmRecFinish(vmStopWriteErr)
		return
	case r.data >= vmMaxBytes:
		vmRecFinish(vmStopCap)
		return
	}
	if r.mic.Open() {
		r.lostAt = time.Time{}
	} else if r.lostAt.IsZero() {
		r.lostAt = now
	} else if now.Sub(r.lostAt) > vmMicLostAfter {
		// No device left to swap to (voicemic.go tries them in turn).
		vmRecFinish(vmStopMicLost)
		return
	}
	if now.Sub(r.freeAt) >= vmFreeCheckGap {
		r.freeAt = now
		if free, err := filesFreeSpace(vm.dir); err == nil && free < vmMinFreeBytes {
			vmRecFinish(vmStopSpace)
		}
	}
}

// vmRecTake writes one chunk of mono 16-bit sound and feeds the meter and the
// waveform. A paused recording only moves the meter.
func vmRecTake(r *vmRecording, pcm []byte) {
	if sdl.GetTicks64() < r.skipUntil {
		return
	}
	level := vkPCMLevel(pcm)
	if level > r.level {
		r.level = level
	} else {
		r.level = r.level*0.8 + level*0.2
	}
	if r.paused || r.wErr != nil {
		return
	}
	if room := vmMaxBytes - r.data; int64(len(pcm)) > room {
		pcm = pcm[:room]
	}
	if len(pcm) == 0 {
		return
	}
	if level > r.peak {
		r.peak = level
	}
	if _, err := r.w.Write(pcm); err != nil {
		r.wErr = err
		return
	}
	r.data += int64(len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8)) / 32768
		r.waveSum += s * s
		r.waveN++
		if r.waveN >= vmWaveBarFrames {
			bar := float32(math.Sqrt(r.waveSum / float64(r.waveN)))
			if len(r.wave) >= vmWaveBars {
				copy(r.wave, r.wave[1:])
				r.wave = r.wave[:len(r.wave)-1]
			}
			r.wave = append(r.wave, bar)
			r.waveSum, r.waveN = 0, 0
		}
	}
}

// vmRecTogglePause pauses or resumes the recording. The microphone stays
// open (the meter keeps moving), and the resume chime is not recorded.
func vmRecTogglePause() {
	r := vm.rec
	if r == nil {
		return
	}
	r.paused = !r.paused
	PlayNavSound()
	r.skipUntil = sdl.GetTicks64() + vmChimeSkipMs
}

// vmRecStopAndName is Stop & save: the memo is saved under its default name,
// then the keyboard offers a better one (B keeps the default).
func vmRecStopAndName() {
	m, ok := vmRecFinish(vmStopUser)
	if !ok {
		return
	}
	file := m.file
	osPromptOpen("Name this memo", "Memo name", "Save", m.name, vmNameMaxRunes, func(text string) {
		vmRename(file, text)
	})
}

// vmRecFinish ends the recording and saves it. It reports the memo, or false
// when nothing worth keeping was recorded or the file could not be renamed
// (then it stays a temporary file, recovered on the next visit).
func vmRecFinish(why vmStopWhy) (vmMemo, bool) {
	r := vm.rec
	if r == nil {
		return vmMemo{}, false
	}
	vm.rec = nil
	vmHubRecSec.Store(-1)
	if why != vmStopMicLost && why != vmStopWriteErr {
		// The last moment of sound still queued in the device.
		if n := r.mic.Read(r.buf); n > 0 {
			vmRecTake(r, r.buf[:n])
		}
	}
	r.mic.Close()
	defer vmMediaSync()
	ferr := r.w.Flush()
	data := r.data
	if st, err := r.f.Stat(); err == nil && st.Size() >= vmWAVHeaderLen {
		// What reached the card, which after a write error is less than what
		// was taken.
		data = st.Size() - vmWAVHeaderLen
		data -= data % 2
	}
	herr := vmWriteWAVSizes(r.f, data)
	serr := r.f.Sync()
	cerr := r.f.Close()
	if ferr != nil || herr != nil || serr != nil || cerr != nil {
		logWarn("voicememo", "finishing %s: flush %v, header %v, sync %v, close %v",
			filepath.Base(r.tmp), ferr, herr, serr, cerr)
	}
	if data < vmBytesPerSec/10 {
		_ = os.Remove(r.tmp)
		vmSetActiveTemp("")
		switch why {
		case vmStopUser:
			ShowToast("Nothing was recorded", ToastKindInfo)
			PlayBackSound()
		case vmStopMicLost, vmStopWriteErr, vmStopSpace:
			// An automatic stop before any sound: without a word the recorder
			// would just vanish.
			msg := "The microphone stopped giving sound — nothing was recorded. The Microphone test (X) says more"
			if why != vmStopMicLost {
				msg = "The card would not take the recording — nothing was recorded"
			}
			ShowToast(msg, ToastKindWarning)
			PlayErrorSound()
		}
		return vmMemo{}, false
	}
	n := vmNextNumber()
	file := vmUniqueFile(vm.dir, "Memo "+strconv.Itoa(n), "")
	if err := os.Rename(r.tmp, filepath.Join(vm.dir, file)); err != nil {
		// Left as the temporary file; the next visit recovers it.
		vmSetActiveTemp("")
		logWarn("voicememo", "cannot rename %s to %q: %v", filepath.Base(r.tmp), file, err)
		if why != vmStopExit {
			ShowToast("Couldn't save the memo — it is recovered the next time Voice Memos opens", ToastKindError)
			PlayErrorSound()
		}
		return vmMemo{}, false
	}
	vmSetActiveTemp("")
	vm.saved.Next = n + 1
	vmStore()
	dur := time.Duration(data * int64(time.Second) / vmBytesPerSec)
	m := vmMemo{
		name: strings.TrimSuffix(file, ".wav"),
		file: file,
		size: vmWAVHeaderLen + data,
		mod:  time.Now(),
		dur:  dur,
		wav:  vmWAV{format: 1, channels: 1, rate: vmRate, bits: 16, dataOff: vmWAVHeaderLen, dataLen: data},
		ok:   true,
	}
	vm.memos = append([]vmMemo{m}, vm.memos...)
	vm.nav.focus = m.id()
	vmHubCount.Store(int32(len(vm.memos)))
	logInfo("voicememo", "saved %q: %s from %s, peak %.3f (stop: %d)", file, vmClock(dur), r.via, r.peak, int(why))
	if why == vmStopExit {
		return m, true
	}
	vmScanAsync(false)
	quote := "“" + m.name + "”"
	switch {
	case r.peak < vkVoiceSilence:
		ShowToast(quote+" is silent — the Microphone test (X) checks the microphone", ToastKindWarning)
	case why == vmStopCap:
		ShowToast("Memos stop at 30 minutes — saved as "+quote, ToastKindInfo)
	case why == vmStopSpace:
		ShowToast("The card is almost full — saved as "+quote, ToastKindWarning)
	case why == vmStopSlept:
		ShowToast("Recording stopped while the handheld slept — saved as "+quote, ToastKindInfo)
	case why == vmStopOrb:
		ShowToast("Juka needs the microphone — saved as "+quote, ToastKindInfo)
	case why == vmStopMicLost:
		ShowToast("The microphone stopped — saved as "+quote, ToastKindWarning)
	case why == vmStopWriteErr:
		ShowToast("The card stopped taking the recording — what was recorded is saved as "+quote, ToastKindWarning)
	case why == vmStopLeft:
		ShowToast("Recording saved as "+quote, ToastKindSuccess)
	}
	if why == vmStopUser {
		PlaySuccessSound()
	}
	return m, true
}
