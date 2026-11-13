package main

// voicemic.go — the microphone, shared by every voice feature: the voice orb
// (voiceorb.go), the Voice command page (voicecmd.go) and voice typing on the
// keyboard (vkvoice.go).
//
// All three used to open SDL's default capture device and nothing else. On the
// TrimUI Smart Pro that one call failed — "No microphone found" — although the
// handheld has a mono microphone: SDL's default capture device is ALSA's
// "default" PCM, which a firmware is free to point at a playback-only mixer,
// and the codec's own capture device was never tried. micOpen now tries, in
// order, until one opens:
//
//  1. SDL's default capture device (what a PC uses, and enough wherever the
//     default PCM can record);
//  2. on Linux with SDL's ALSA driver, every capture PCM the kernel lists in
//     /proc/asound/pcm, by its ALSA name through the plug layer ("plughw:0,0",
//     which converts the codec's own rate and channel count) — handed to SDL
//     the one way its ALSA driver takes a device name of our choosing, the
//     AUDIODEV variable, which is put back as soon as the open returns;
//  3. every capture device SDL lists, by name;
//  4. on Linux, arecord (alsa-utils) on the same PCMs, read from a pipe — for
//     an SDL whose ALSA driver will not record at all.
//
// A device that opens but gives no sound — no bytes, or nothing above a few
// least significant bits, which no live microphone produces — within
// micNoSoundMs is closed and the next one is tried; the last one is kept, so
// the caller's own "didn't hear anything" covers the rest. The device that gave
// sound is tried first the next time. Every attempt and its error go to the
// log (category "mic"), so a report from a handheld names the device that
// worked and why the others did not.
//
// The codec of the Smart Pro and the Brick (Allwinner's sun50iw10 driver,
// card "audiocodec") ships with its microphone inputs switched off: capture
// runs, but the ADC hears nothing until "ADCL Input MIC1 Boost Switch" is on.
// Before the first open on Linux the capture-side microphone switches of every
// card that can record are switched on — only switches named as microphone
// inputs to the ADC, never a playback path (a microphone looped to the speaker
// would howl) — and a microphone gain or ADC level found at the very bottom of
// its range is lifted to two thirds; everything changed here is put back when
// JukaHub quits.
//
// The capture is 16 kHz, 16-bit, two channels, and Read returns one channel:
// the one that carries the voice (a mono microphone on one ADC input leaves the
// other near silent, and the average of the two would halve it), or the
// average when both carry it.
//
// Main thread only, apart from the arecord reader goroutine, which touches its
// own buffer under its own lock, and micPresence, which only reads.

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// micRate and micChannels are what every device is opened (or converted)
	// to; Read hands back micRate mono.
	micRate       = vkVoiceRate
	micChannels   = 2
	micFrameBytes = 2 * micChannels
	// micNoSoundMs is how long an open device may stay silent before the next
	// one is tried.
	micNoSoundMs = 1200
	// micHeardPeak is the sample size (in least significant bits) that counts
	// as sound: a dead input reads exact zeros or a bit of dither, a live
	// microphone's own noise is far above it.
	micHeardPeak = 8
	// micPipeMaxBytes bounds what the arecord reader keeps while nobody reads:
	// two seconds.
	micPipeMaxBytes = micRate * micFrameBytes * 2
	// micDecideLevel is the RMS (0..1) a chunk must reach to count as voice
	// for the channel choice, and micDecideFrames of such frames decide it.
	micDecideLevel  = 0.02
	micDecideFrames = micRate / 2
	// micMaxSDLNames bounds how many of SDL's own capture devices are tried.
	micMaxSDLNames = 6
	// micReadMaxFrames bounds one Read: two seconds, as much as the arecord
	// pipe keeps, so a caller's last read after a stalled frame still gets the
	// end of the clip.
	micReadMaxFrames = micRate * 2
	// micSwapWaitMs is how long the next candidate waits for a dropped device
	// to finish closing; micExitWait how long the exit path waits for it.
	micSwapWaitMs = 1500
	micExitWait   = 11 * time.Second
)

var (
	// errMicNone: nothing that can record exists — SDL lists no capture
	// device and the kernel no capture PCM.
	errMicNone = errors.New("no microphone found")
	// errMicOpen: devices exist, but none of them opened.
	errMicOpen = errors.New("the microphone could not be opened")
)

// micKind is how a candidate is opened.
type micKind int

const (
	micKindSDLDefault micKind = iota
	micKindALSA               // SDL with AUDIODEV set to an ALSA PCM name
	micKindSDLNamed           // a device from SDL's own list
	micKindArecord            // arecord on an ALSA PCM name
)

// micCandidate is one way to reach a microphone.
type micCandidate struct {
	kind micKind
	name string // the ALSA PCM or SDL device name ("" for SDL's default)
}

// label names the candidate in the log.
func (c micCandidate) label() string {
	switch c.kind {
	case micKindSDLDefault:
		return "SDL default device"
	case micKindALSA:
		return "ALSA " + c.name
	case micKindSDLNamed:
		return "SDL device \"" + c.name + "\""
	case micKindArecord:
		return "arecord " + c.name
	}
	return c.name
}

// micStream is an open microphone.
type micStream struct {
	// cands are the candidates not tried yet, in order; cur is the open one.
	cands []micCandidate
	cur   micCandidate
	dev   sdl.AudioDeviceID
	pipe  *micPipe
	// openedAt is when cur opened; heard and gotBytes are what it has given.
	openedAt uint64
	heard    bool
	gotBytes bool
	picker   micPicker
	stereo   []byte
	closed   bool
	// swapAt is set while the next candidate waits for the one just dropped
	// to finish closing in the background (it may hold the same codec).
	swapAt uint64
	// voice is set on a voice feature's stream (micOpen); Close takes it off
	// micVoiceUsers.
	voice bool
}

// micOpen opens the first capture device that works and starts it. errMicNone
// and errMicOpen say which way it failed (the log has the details). Main
// thread.
func micOpen() (*micStream, error) {
	// The stick lights may be listening (ledmic.go): a voice feature gets the
	// codec to itself, and the lights wait until every voice stream closes.
	// So does the "Hey Juka" listener (wakeword.go).
	ledMicYield()
	hjYield()
	m, err := micOpenShared()
	if err != nil {
		return nil, err
	}
	m.voice = true
	micVoiceUsers.Add(1)
	return m, nil
}

// micVoiceUsers counts the voice features' open streams (micOpen), not the
// lights' own (ledmic.go opens with micOpenShared).
var micVoiceUsers atomic.Int32

// adoptVoice makes a stream opened with micOpenShared a voice feature's, as if
// micOpen had opened it: "Hey Juka" hands its open microphone to the orb
// (wakeword.go), so not a syllable is lost to closing and reopening the
// codec. Main thread.
func (m *micStream) adoptVoice() {
	if m == nil || m.voice || m.closed {
		return
	}
	ledMicYield()
	m.voice = true
	micVoiceUsers.Add(1)
}

// micOpenShared is micOpen without taking the microphone from the lights or
// counting as a voice feature. Main thread.
func micOpenShared() (*micStream, error) {
	micPrepareMixer()
	cands := micCandidates()
	m := &micStream{}
	if err := m.openFrom(cands); err != nil {
		if errors.Is(err, errMicNone) || micNothingToOpen(cands) {
			return nil, errMicNone
		}
		return nil, err
	}
	return m, nil
}

// micNothingToOpen reports whether the candidates are only SDL's default
// device with nothing behind it: no SDL capture device and no kernel PCM.
func micNothingToOpen(cands []micCandidate) bool {
	for _, c := range cands {
		if c.kind != micKindSDLDefault {
			return false
		}
	}
	return sdl.GetNumAudioDevices(true) <= 0
}

// openFrom tries cands in order and keeps the first that opens; the rest stay
// queued for the silence check (Read).
func (m *micStream) openFrom(cands []micCandidate) error {
	if len(cands) == 0 {
		return errMicNone
	}
	for i, c := range cands {
		if err := m.openOne(c); err != nil {
			logInfo("mic", "%s: %v", c.label(), err)
			continue
		}
		m.cur = c
		m.cands = cands[i+1:]
		m.openedAt = sdl.GetTicks64()
		m.heard, m.gotBytes = false, false
		m.picker = micPicker{}
		logInfo("mic", "recording from %s (SDL audio driver %q)", c.label(), sdl.GetCurrentAudioDriver())
		return nil
	}
	m.cands = nil
	return errMicOpen
}

// openOne opens one candidate and starts it.
func (m *micStream) openOne(c micCandidate) error {
	if c.kind == micKindArecord {
		p, err := micStartArecord(c.name)
		if err != nil {
			return err
		}
		m.pipe = p
		return nil
	}
	name, alsa := "", ""
	switch c.kind {
	case micKindALSA:
		alsa = c.name
	case micKindSDLNamed:
		name = c.name
	}
	dev, err := micOpenSDL(name, alsa)
	if err != nil {
		return err
	}
	m.dev = dev
	sdl.PauseAudioDevice(dev, false)
	return nil
}

// micOpenSDL opens an SDL capture device: name "" is the default device, and
// alsa, when set, is the ALSA PCM that SDL's ALSA driver opens in place of
// "default" (it reads AUDIODEV while it opens, and only then).
func micOpenSDL(name, alsa string) (sdl.AudioDeviceID, error) {
	desired := sdl.AudioSpec{Freq: micRate, Format: sdl.AUDIO_S16LSB, Channels: micChannels, Samples: 1024}
	var obtained sdl.AudioSpec
	// Outputs set AUDIODEV for a Bluetooth headset too (audioroute.go).
	audioDevEnvMu.Lock()
	defer audioDevEnvMu.Unlock()
	if alsa != "" {
		prev, had := os.LookupEnv("AUDIODEV")
		_ = os.Setenv("AUDIODEV", alsa)
		defer func() {
			if had {
				_ = os.Setenv("AUDIODEV", prev)
			} else {
				_ = os.Unsetenv("AUDIODEV")
			}
		}()
	}
	// No allowed changes: SDL converts whatever the device records into 16 kHz,
	// 16-bit, two channels.
	dev, err := sdl.OpenAudioDevice(name, true, &desired, &obtained, 0)
	if err != nil || dev == 0 {
		if err == nil {
			err = errors.New("SDL opened no device")
		}
		return 0, err
	}
	return dev, nil
}

// micCandidates lists what micOpen tries, in the order of the file comment.
func micCandidates() []micCandidate {
	out := []micCandidate{{kind: micKindSDLDefault}}
	var pcms []micPCM
	if runtime.GOOS == "linux" {
		pcms = micALSACapturePCMs()
	}
	driver := sdl.GetCurrentAudioDriver()
	if strings.EqualFold(driver, "alsa") {
		for _, p := range pcms {
			out = append(out, micCandidate{kind: micKindALSA, name: p.plug()})
		}
	}
	n := sdl.GetNumAudioDevices(true)
	for i := 0; i < n && i < micMaxSDLNames; i++ {
		if name := sdl.GetAudioDeviceName(i, true); name != "" {
			out = append(out, micCandidate{kind: micKindSDLNamed, name: name})
		}
	}
	if len(pcms) > 0 && micArecordPath() != "" {
		for _, p := range pcms {
			out = append(out, micCandidate{kind: micKindArecord, name: p.plug()})
		}
	}
	micLogInventory(driver, n, pcms)
	out = micDemoteCandidates(out, micNoData)
	if micLastGood.set {
		out = micPreferCandidate(out, micLastGood.c)
	}
	return out
}

// micDemoteCandidates moves the candidates in bad to the end, keeping the order
// of both groups. Pure.
func micDemoteCandidates(cands []micCandidate, bad map[micCandidate]bool) []micCandidate {
	if len(bad) == 0 {
		return cands
	}
	good := make([]micCandidate, 0, len(cands))
	var last []micCandidate
	for _, c := range cands {
		if bad[c] {
			last = append(last, c)
		} else {
			good = append(good, c)
		}
	}
	return append(good, last...)
}

// micLastGood is the candidate that last gave sound. The next open tries it
// first, so a handheld whose default device stays silent does not lose the
// first second of every clip to the silence check. Main thread.
var micLastGood struct {
	set bool
	c   micCandidate
}

// micPreferCandidate moves want to the front of cands, keeping the others in
// order; a list without it is returned as it was. Pure.
func micPreferCandidate(cands []micCandidate, want micCandidate) []micCandidate {
	for i, c := range cands {
		if c != want {
			continue
		}
		if i == 0 {
			return cands
		}
		out := make([]micCandidate, 0, len(cands))
		out = append(out, c)
		out = append(out, cands[:i]...)
		return append(out, cands[i+1:]...)
	}
	return cands
}

// micInventoryLogged keeps the inventory line to once a run.
var micInventoryLogged bool

// micLogInventory writes, once, what there was to choose from: the line a
// report from a handheld needs to say why its microphone did or did not open.
func micLogInventory(driver string, sdlCount int, pcms []micPCM) {
	if micInventoryLogged {
		return
	}
	micInventoryLogged = true
	names := make([]string, 0, len(pcms))
	for _, p := range pcms {
		names = append(names, p.plug())
	}
	sdlNames := make([]string, 0, 4)
	for i := 0; i < sdlCount && i < micMaxSDLNames; i++ {
		sdlNames = append(sdlNames, strconv.Quote(sdl.GetAudioDeviceName(i, true)))
	}
	logInfo("mic", "SDL audio driver %q, SDL capture devices %d [%s], kernel capture PCMs [%s], arecord %v",
		driver, sdlCount, strings.Join(sdlNames, ", "), strings.Join(names, ", "), micArecordPath() != "")
}

// micPresence answers whether a microphone can be expected at all, for the
// keyboard's mic chip and the Voice command page's header: SDL's own list,
// and on Linux the kernel's capture PCMs too (SDL's ALSA list can leave out a
// codec that "plughw" records from). Safe off the main thread.
func micPresence() int32 {
	n := sdl.GetNumAudioDevices(true)
	switch {
	case n > 0:
		return vkMicPresent
	case runtime.GOOS == "linux" && len(micALSACapturePCMs()) > 0:
		return vkMicPresent
	case n == 0:
		return vkMicAbsent
	}
	return vkMicUnknown
}

// Via names the device that is recording, for the log and the orb.
func (m *micStream) Via() string {
	if m == nil {
		return ""
	}
	return m.cur.label()
}

// Heard reports whether the open device has given any sound yet.
func (m *micStream) Heard() bool { return m != nil && m.heard }

// Open reports whether a device is still recording.
func (m *micStream) Open() bool { return m != nil && !m.closed && (m.dev != 0 || m.pipe != nil) }

// Read moves what the microphone captured since the last call into dst as
// mono 16-bit little-endian samples and returns how many bytes it wrote. It
// also runs the silence check: a device that has given no sound for
// micNoSoundMs is replaced by the next candidate. Main thread.
func (m *micStream) Read(dst []byte) int {
	if m == nil || m.closed {
		return 0
	}
	if m.swapAt != 0 {
		// The dropped device may still hold the codec: the next one opens once
		// it has let go, or after micSwapWaitMs in any case.
		if micClosing.n.Load() > 0 && sdl.GetTicks64()-m.swapAt < micSwapWaitMs {
			return 0
		}
		m.swapAt = 0
		if err := m.openFrom(m.cands); err != nil {
			logWarn("mic", "no other device opened: %v", err)
		}
		return 0
	}
	frames := len(dst) / 2
	if frames > micReadMaxFrames {
		// Voice typing hands in room for its whole clip; two seconds a call
		// keeps the stereo scratch small.
		frames = micReadMaxFrames
	}
	if frames <= 0 {
		return 0
	}
	need := frames * micFrameBytes
	if cap(m.stereo) < need {
		m.stereo = make([]byte, need)
	}
	src := m.stereo[:need]
	got := 0
	dead := false
	switch {
	case m.dev != 0:
		avail := int(sdl.GetQueuedAudioSize(m.dev))
		avail -= avail % micFrameBytes
		if avail > need {
			avail = need
		}
		if avail > 0 {
			got, _ = sdl.DequeueAudio(m.dev, src[:avail])
			got -= got % micFrameBytes
		} else if sdl.GetAudioDeviceStatus(m.dev) == sdl.AUDIO_STOPPED {
			// SDL stops a device whose reads fail.
			dead = true
		}
	case m.pipe != nil:
		got, dead = m.pipe.take(src)
	}
	if got > 0 {
		m.gotBytes = true
		if !m.heard && micHasSound(src[:got]) {
			m.heard = true
			if !micLastGood.set || micLastGood.c != m.cur {
				logInfo("mic", "%s gives sound", m.cur.label())
			}
			micLastGood.set, micLastGood.c = true, m.cur
		}
	}
	// A device that stopped, or gave no data at all, is replaced anywhere. One
	// that only gave silence is replaced on Linux, where that is a codec input
	// left off; on a PC silence is a muted microphone or noise suppression,
	// and the next device in SDL's list could be a webcam or "Stereo Mix".
	quiet := !m.heard && sdl.GetTicks64()-m.openedAt >= micNoSoundMs && (!m.gotBytes || runtime.GOOS == "linux")
	if (dead || quiet) && len(m.cands) > 0 {
		why := "only silence"
		switch {
		case dead:
			why = "stopped"
		case !m.gotBytes:
			why = "no data"
		}
		logInfo("mic", "%s gave %s; trying the next device", m.cur.label(), why)
		if dead || !m.gotBytes {
			// Not worth trying first again this run.
			micNoData[m.cur] = true
		}
		m.closeDevice()
		m.swapAt = sdl.GetTicks64()
		if m.swapAt == 0 {
			m.swapAt = 1
		}
		return 0
	}
	if got <= 0 {
		return 0
	}
	return m.picker.downmix(dst, src[:got])
}

// Close stops the microphone. Safe to call twice.
func (m *micStream) Close() {
	if m == nil || m.closed {
		return
	}
	m.closed = true
	m.closeDevice()
	m.cands = nil
	if m.voice {
		m.voice = false
		micVoiceUsers.Add(-1)
	}
}

// closeDevice closes the open device, whichever kind it is. An SDL device that
// never delivered is closed off the main thread (micCloseAsync): SDL's capture
// thread may sit in a read the kernel only gives up on after seconds, and
// closing waits for that thread.
func (m *micStream) closeDevice() {
	if m.dev != 0 {
		dev := m.dev
		m.dev = 0
		if m.gotBytes {
			sdl.PauseAudioDevice(dev, true)
			sdl.CloseAudioDevice(dev)
		} else {
			micCloseAsync(dev)
		}
	}
	if m.pipe != nil {
		m.pipe.close()
		m.pipe = nil
	}
}

// micClosing counts the devices closing in the background.
var micClosing struct {
	wg sync.WaitGroup
	n  atomic.Int32
}

// micNoData marks the candidates that opened and gave no data at all (or
// stopped) this run: micCandidates tries them last. Main thread.
var micNoData = map[micCandidate]bool{}

// micCloseAsync closes an SDL capture device on a goroutine.
func micCloseAsync(dev sdl.AudioDeviceID) {
	micClosing.n.Add(1)
	micClosing.wg.Add(1)
	go func() {
		defer micClosing.wg.Done()
		defer micClosing.n.Add(-1)
		sdl.PauseAudioDevice(dev, true)
		sdl.CloseAudioDevice(dev)
	}()
}

// micCloseWait lets the background closes finish before SDL shuts audio down
// (Cleanup runs before sdl.Quit): SDL closing the same device again would
// join its capture thread twice. Bounded by micExitWait, which outlasts the
// kernel's own ten-second give-up on a stalled capture read.
func micCloseWait() {
	if micClosing.n.Load() == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		micClosing.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(micExitWait):
		logWarn("mic", "a capture device was still closing at exit")
	}
}

// micHasSound reports whether either channel of interleaved 16-bit stereo
// moves by at least micHeardPeak*2 within b: a dead input reads zeros, a few
// bits of dither or a constant offset, a live microphone's own noise moves
// far more. Pure.
func micHasSound(b []byte) bool {
	lo := [2]int{math.MaxInt16, math.MaxInt16}
	hi := [2]int{math.MinInt16, math.MinInt16}
	for i := 0; i+micFrameBytes <= len(b); i += micFrameBytes {
		for c := 0; c < 2; c++ {
			s := int(int16(binary.LittleEndian.Uint16(b[i+2*c:])))
			if s < lo[c] {
				lo[c] = s
			}
			if s > hi[c] {
				hi[c] = s
			}
		}
	}
	return hi[0]-lo[0] >= 2*micHeardPeak || hi[1]-lo[1] >= 2*micHeardPeak
}

// ── Channels ─────────────────────────────────────────────────────────────

// micPicker chooses which of the two captured channels Read returns. Until
// half a second of voice-loud audio has been heard it decides chunk by chunk
// (one channel when it carries clearly more, else the average); then it
// settles on left, right or the average for the rest of the clip.
type micPicker struct {
	mode       int // 0 undecided, micPickLeft, micPickRight, micPickMix
	sumL, sumR float64
	loudFrames int
}

const (
	micPickLeft = iota + 1
	micPickRight
	micPickMix
)

// micDecide is the choice for two channel energies: one with more than four
// times the other's (6 dB) carries the voice alone, otherwise both do. Pure.
func micDecide(eL, eR float64) int {
	switch {
	case eL > 4*eR:
		return micPickLeft
	case eR > 4*eL:
		return micPickRight
	}
	return micPickMix
}

// downmix writes src (16-bit stereo frames) into dst as mono and returns the
// bytes written. Pure apart from the picker's own state.
func (p *micPicker) downmix(dst, src []byte) int {
	frames := len(src) / micFrameBytes
	if frames*2 > len(dst) {
		frames = len(dst) / 2
	}
	if frames <= 0 {
		return 0
	}
	var eL, eR float64
	for i := 0; i < frames; i++ {
		l := float64(int16(binary.LittleEndian.Uint16(src[i*micFrameBytes:])))
		r := float64(int16(binary.LittleEndian.Uint16(src[i*micFrameBytes+2:])))
		eL += l * l
		eR += r * r
	}
	mode := p.mode
	if mode == 0 {
		loudest := math.Max(eL, eR) / float64(frames)
		if math.Sqrt(loudest)/32768 >= micDecideLevel {
			p.sumL += eL
			p.sumR += eR
			p.loudFrames += frames
			if p.loudFrames >= micDecideFrames {
				p.mode = micDecide(p.sumL, p.sumR)
				mode = p.mode
			}
		}
		if mode == 0 {
			mode = micDecide(eL, eR)
		}
	}
	for i := 0; i < frames; i++ {
		l := int32(int16(binary.LittleEndian.Uint16(src[i*micFrameBytes:])))
		r := int32(int16(binary.LittleEndian.Uint16(src[i*micFrameBytes+2:])))
		s := (l + r) / 2
		switch mode {
		case micPickLeft:
			s = l
		case micPickRight:
			s = r
		}
		binary.LittleEndian.PutUint16(dst[i*2:], uint16(int16(s)))
	}
	return frames * 2
}

// ── The kernel's capture devices ─────────────────────────────────────────

// micPCM is one capture PCM of /proc/asound/pcm.
type micPCM struct{ card, dev int }

// plug is its ALSA name through the plug layer, which converts the rate,
// the sample format and the channel count to whatever is asked for.
func (p micPCM) plug() string {
	return "plughw:" + strconv.Itoa(p.card) + "," + strconv.Itoa(p.dev)
}

// micProcPCMPath is the kernel's list of PCMs; a variable so a test can hand
// in its own.
var micProcPCMPath = "/proc/asound/pcm"

// micALSACapturePCMs lists the PCMs the kernel can record from. nil where
// there is no /proc/asound (Windows, a kernel without ALSA).
func micALSACapturePCMs() []micPCM {
	data, err := os.ReadFile(micProcPCMPath)
	if err != nil {
		return nil
	}
	return micParseProcPCM(string(data))
}

// micParseProcPCM reads /proc/asound/pcm, one PCM a line:
//
//	00-00: sun50iw10codec-xxx : : playback 1 : capture 1
//
// and returns the ones with at least one capture substream, in order. Pure.
func micParseProcPCM(text string) []micPCM {
	var out []micPCM
	for _, line := range strings.Split(text, "\n") {
		head, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		card, dev, ok := strings.Cut(strings.TrimSpace(head), "-")
		if !ok {
			continue
		}
		c, err1 := strconv.Atoi(card)
		d, err2 := strconv.Atoi(dev)
		if err1 != nil || err2 != nil || c < 0 || d < 0 {
			continue
		}
		capture := false
		for _, field := range strings.Split(rest, ":") {
			if n, ok := strings.CutPrefix(strings.TrimSpace(field), "capture "); ok {
				if k, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && k > 0 {
					capture = true
				}
			}
		}
		if capture {
			out = append(out, micPCM{card: c, dev: d})
		}
	}
	return out
}

// micCaptureCards are the card numbers among pcms, each once, in order. Pure.
func micCaptureCards(pcms []micPCM) []int {
	var cards []int
	for _, p := range pcms {
		seen := false
		for _, c := range cards {
			if c == p.card {
				seen = true
				break
			}
		}
		if !seen {
			cards = append(cards, p.card)
		}
	}
	return cards
}

// ── arecord ──────────────────────────────────────────────────────────────

// micArecordPath is arecord's path, or "" (not Linux, or no alsa-utils).
func micArecordPath() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	p, err := exec.LookPath("arecord")
	if err != nil {
		return ""
	}
	return p
}

// micPipe is arecord recording raw audio into a pipe. A goroutine drains the
// pipe into a bounded buffer that take empties.
type micPipe struct {
	cmd    *exec.Cmd
	stderr *micTail
	mu     sync.Mutex
	buf    []byte
	done   bool
	killed bool // stopped here, so its end is not news for the log
}

// micStartArecord starts arecord on dev, raw 16 kHz 16-bit stereo to stdout.
func micStartArecord(dev string) (*micPipe, error) {
	path := micArecordPath()
	if path == "" {
		return nil, errors.New("arecord is not installed")
	}
	cmd := exec.Command(path, "-q", "-D", dev, "-t", "raw", "-f", "S16_LE",
		"-r", strconv.Itoa(micRate), "-c", strconv.Itoa(micChannels))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &micTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &micPipe{cmd: cmd, stderr: tail}
	go p.pump(out)
	return p, nil
}

// pump copies arecord's output into the buffer until the pipe ends, then
// reaps the process.
func (p *micPipe) pump(r io.Reader) {
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			p.mu.Lock()
			p.buf = append(p.buf, chunk[:n]...)
			if over := len(p.buf) - micPipeMaxBytes; over > 0 {
				// Nobody read for two seconds: drop the oldest whole frames.
				over = (over + micFrameBytes - 1) / micFrameBytes * micFrameBytes
				if over > len(p.buf) {
					over = len(p.buf) - len(p.buf)%micFrameBytes
				}
				p.buf = append(p.buf[:0], p.buf[over:]...)
			}
			p.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	werr := p.cmd.Wait()
	p.mu.Lock()
	p.done = true
	killed := p.killed
	p.mu.Unlock()
	switch msg := p.stderr.String(); {
	case killed:
	case msg != "":
		logInfo("mic", "arecord ended: %s", msg)
	case werr != nil:
		logInfo("mic", "arecord ended: %v", werr)
	}
}

// take moves whole frames into dst and reports how many bytes it moved, and
// whether arecord has ended with nothing left to give.
func (p *micPipe) take(dst []byte) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.buf) - len(p.buf)%micFrameBytes
	if n > len(dst) {
		n = len(dst) - len(dst)%micFrameBytes
	}
	if n <= 0 {
		return 0, p.done
	}
	copy(dst, p.buf[:n])
	p.buf = append(p.buf[:0], p.buf[n:]...)
	return n, false
}

// close stops arecord; pump reaps it.
func (p *micPipe) close() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	_ = p.cmd.Process.Kill()
}

// micTail keeps the first few hundred bytes a child writes to stderr: the
// line that says why it stopped.
type micTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *micTail) Write(b []byte) (int, error) {
	t.mu.Lock()
	if room := 300 - len(t.buf); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		t.buf = append(t.buf, b[:room]...)
	}
	t.mu.Unlock()
	return len(b), nil
}

func (t *micTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}

// ── The codec's microphone switches ──────────────────────────────────────

// micMixerControl is one control micPrepareMixer changed, with the value it
// had, which micRestoreMixer puts back.
type micMixerControl struct {
	card    int
	id      string // the control as amixer's cset names it: numid=N
	name    string
	restore string
}

// micMixer is what micPrepareMixer did, so the exit path can undo it.
var micMixer struct {
	mu       sync.Mutex
	prepared bool
	changed  []micMixerControl
}

// micHaveAmixer reports whether amixer is installed; a variable so a test can
// run the mixer steps against a stub.
var micHaveAmixer = func() bool { return commandExists("amixer") }

func init() {
	// What was changed for the microphone goes back when JukaHub quits, and a
	// capture device still closing in the background is waited for.
	Cleanup = append(Cleanup, micRestoreMixer, micCloseWait)
}

// micPrepareMixer switches on the microphone inputs of every card that can
// record, and lifts a microphone or ADC level that sits at its very bottom (a
// silent capture), once a run (see the file comment). Linux only; quiet
// without amixer.
func micPrepareMixer() {
	if runtime.GOOS != "linux" {
		return
	}
	micMixer.mu.Lock()
	defer micMixer.mu.Unlock()
	if micMixer.prepared {
		return
	}
	micMixer.prepared = true
	if !micHaveAmixer() {
		logInfo("mic", "amixer is not installed; the microphone switches are left as they are")
		return
	}
	for _, card := range micCaptureCards(micALSACapturePCMs()) {
		cardArg := strconv.Itoa(card)
		out, err := systemCommandRunner("amixer", "-c", cardArg, "contents")
		if err != nil {
			logInfo("mic", "amixer -c %d contents: %v", card, err)
			continue
		}
		for _, ctl := range micParseAmixerContents(out) {
			value, restore, ok := micMixerChange(ctl)
			if !ok {
				continue
			}
			id := ctl.ident()
			if _, err := systemCommandRunner("amixer", "-q", "-c", cardArg, "cset", id, value); err != nil {
				logInfo("mic", "could not set %q to %s (card %d): %v", ctl.name, value, card, err)
				continue
			}
			logInfo("mic", "set %q from %s to %s (card %d) for the microphone", ctl.name, restore, value, card)
			micMixer.changed = append(micMixer.changed, micMixerControl{card: card, id: id, name: ctl.name, restore: restore})
		}
	}
}

// micRestoreMixer puts back what micPrepareMixer changed (Cleanup).
func micRestoreMixer() {
	micMixer.mu.Lock()
	list := micMixer.changed
	micMixer.changed = nil
	micMixer.mu.Unlock()
	for _, c := range list {
		if _, err := systemCommandRunner("amixer", "-q", "-c", strconv.Itoa(c.card), "cset",
			c.id, c.restore); err != nil {
			logDebug("mic", "could not put %q back to %s: %v", c.name, c.restore, err)
		}
	}
}

// micMixerChange decides what to do with one control: a capture-side
// microphone switch that is off is switched on; a microphone gain or ADC level
// at the very bottom of its range, where that bottom is mute (or at least
// 60 dB down), goes to two thirds of it — a bottom that only means a quieter
// microphone is left alone. It returns the new value, the old one and whether
// to change anything. Pure.
func micMixerChange(ctl micAmixerControl) (value, restore string, ok bool) {
	switch {
	case ctl.boolean:
		if ctl.on || !micMixerSwitchWanted(ctl.name) {
			return "", "", false
		}
		return micRepeatValue("on", ctl.count), strings.Join(ctl.values, ","), true
	case ctl.integer:
		if !micMixerLevelWanted(ctl.name) || ctl.max <= ctl.min || len(ctl.values) == 0 || !ctl.minSilent {
			return "", "", false
		}
		for _, v := range ctl.values {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n != ctl.min {
				return "", "", false
			}
		}
		level := ctl.min + (ctl.max-ctl.min)*2/3
		return micRepeatValue(strconv.Itoa(level), len(ctl.values)), strings.Join(ctl.values, ","), true
	}
	return "", "", false
}

// micRepeatValue is v once per channel of a control ("on,on" for two). Pure.
func micRepeatValue(v string, count int) string {
	if count <= 1 {
		return v
	}
	if count > 8 {
		count = 8
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = v
	}
	return strings.Join(parts, ",")
}

// micAmixerControl is one control of `amixer contents`.
type micAmixerControl struct {
	numid     int
	name      string
	boolean   bool
	integer   bool
	count     int
	min, max  int
	values    []string
	on        bool // a boolean whose every value is on
	minSilent bool // its dB scale says the bottom is mute, or 60 dB down
}

// ident is the control as cset takes it: its numid, which also tells two
// controls of one name apart (index=1); the quoted name when there is none.
func (c micAmixerControl) ident() string {
	if c.numid > 0 {
		return "numid=" + strconv.Itoa(c.numid)
	}
	return "name='" + c.name + "'"
}

// micParseAmixerContents reads `amixer -c N contents`:
//
//	numid=12,iface=MIXER,name='ADCL Input MIC1 Boost Switch'
//	  ; type=BOOLEAN,access=rw------,values=1
//	  : values=off
//	numid=5,iface=MIXER,name='MIC1 gain volume'
//	  ; type=INTEGER,access=rw------,values=1,min=0,max=31,step=0
//	  : values=31
//	  | dBscale-min=-12.00dB,step=1.00dB,mute=0
//
// Pure.
func micParseAmixerContents(out string) []micAmixerControl {
	var list []micAmixerControl
	cur := -1
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "numid="):
			name := ""
			if i := strings.Index(line, "name='"); i >= 0 {
				rest := line[i+len("name='"):]
				if j := strings.LastIndex(rest, "'"); j >= 0 {
					name = rest[:j]
				}
			}
			numid := 0
			if rest, ok := strings.CutPrefix(line, "numid="); ok {
				if head, _, _ := strings.Cut(rest, ","); head != "" {
					numid, _ = strconv.Atoi(head)
				}
			}
			list = append(list, micAmixerControl{numid: numid, name: name})
			cur = len(list) - 1
		case cur >= 0 && strings.HasPrefix(line, "; type="):
			for _, kv := range strings.Split(strings.TrimPrefix(line, "; "), ",") {
				k, v, _ := strings.Cut(kv, "=")
				v = strings.TrimSpace(v)
				n, nerr := strconv.Atoi(v)
				switch strings.TrimSpace(k) {
				case "type":
					list[cur].boolean = v == "BOOLEAN"
					list[cur].integer = v == "INTEGER"
				case "values":
					if nerr == nil {
						list[cur].count = n
					}
				case "min":
					if nerr == nil {
						list[cur].min = n
					}
				case "max":
					if nerr == nil {
						list[cur].max = n
					}
				}
			}
		case cur >= 0 && strings.HasPrefix(line, ": values="):
			vals := strings.Split(strings.TrimPrefix(line, ": values="), ",")
			on := len(vals) > 0
			for i, v := range vals {
				vals[i] = strings.TrimSpace(v)
				if vals[i] != "on" {
					on = false
				}
			}
			list[cur].values = vals
			list[cur].on = on
		case cur >= 0 && strings.HasPrefix(line, "| dB"):
			list[cur].minSilent = list[cur].minSilent || micDBMinSilent(line)
		}
	}
	return list
}

// micDBMinSilent reads a dB-scale line of `amixer contents`
// ("| dBscale-min=-12.00dB,step=1.00dB,mute=0", "| dBminmax-min=-74.25dB,max=0.00dB",
// "| dBminmaxmute-min=-12.00dB,max=0.00dB")
// and reports whether the bottom of the range is mute or at least 60 dB down.
// Pure.
func micDBMinSilent(line string) bool {
	for _, kv := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "|")), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch {
		case strings.HasPrefix(k, "dBminmaxmute"):
			// A min/max scale whose bottom is mute, whatever its number says.
			return true
		case k == "mute":
			if v == "1" {
				return true
			}
		case strings.HasSuffix(k, "-min"):
			db, err := strconv.ParseFloat(strings.TrimSuffix(v, "dB"), 64)
			if err == nil && db <= -60 {
				return true
			}
		}
	}
	return false
}

// micMixerOutWords mark a control on the playback side, or one that silences,
// anywhere in its name; micMixerOutTokens do so as whole words ("HP Mixer",
// "Out Mixer"). Neither is ever changed for the microphone.
var (
	micMixerOutWords = [...]string{"playback", "output", "lineout", "line out", "speaker", "spk", "headphone",
		"hpout", "earpiece", "dac", "loop", "bypass", "monitor", "sidetone", "mute"}
	micMixerOutTokens = [...]string{"hp", "out", "ear", "lo", "pa"}
)

// micMixerOutside reports whether a lower-cased control name is on the
// playback side. Pure.
func micMixerOutside(n string) bool {
	for _, w := range micMixerOutWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	for _, word := range strings.FieldsFunc(n, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		for _, t := range micMixerOutTokens {
			if word == t {
				return true
			}
		}
	}
	return false
}

// micMixerSwitchWanted reports whether a mixer switch is a microphone input on
// the capture side — the kind a codec ships switched off ("ADCL Input MIC1
// Boost Switch", "Mic1 Capture Switch", "LADC input Mixer MIC1 boost Switch").
// Pure.
func micMixerSwitchWanted(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if !strings.Contains(n, "mic") || !strings.HasSuffix(n, "switch") || micMixerOutside(n) {
		return false
	}
	switch n {
	case "mic switch", "mic1 switch", "mic2 switch":
		return true
	}
	return strings.Contains(n, "adc") || strings.Contains(n, "capture") || strings.Contains(n, "boost") ||
		strings.Contains(n, "input")
}

// micMixerLevelWanted reports whether an integer control is a microphone gain
// or the ADC's level ("MIC1 gain volume", "ADC volume", "Mic Boost Volume"),
// never a playback level. Pure.
func micMixerLevelWanted(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if micMixerOutside(n) {
		return false
	}
	if !strings.Contains(n, "volume") && !strings.Contains(n, "gain") {
		return false
	}
	return strings.Contains(n, "mic") || strings.HasPrefix(n, "adc")
}
