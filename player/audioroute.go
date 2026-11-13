package main

// audioroute.go — every JukaHub sound reaches a Bluetooth headset.
//
// Before this file, a connected headset stayed silent. Three things stood in
// the way, all on the handhelds (BlueALSA over BlueZ, no PulseAudio):
//
//   - Every output was opened once on ALSA's "default", which is the speaker
//     codec. Nothing ever pointed JukaHub at the headset, and alsa-lib reads
//     its configuration once per process, so even a firmware that rewrites an
//     .asoundrc on connect (spruce, NextUI) is not seen by an app already
//     running — the same bug spruce documents for its PyUI menu.
//   - BlueALSA takes ONE client per PCM ("Device or resource busy" for the
//     second, documented in its TROUBLESHOOTING.md; dmix cannot sit on it).
//     JukaHub keeps up to five outputs open at once: UI sounds (also trophy
//     chime, alarm, Juka's voice), video/music, background music, the synth and
//     the boot chime. Pointing each at the headset would give the first one
//     sound and the rest errors.
//   - Holding the headset while a game runs would starve the game the same way.
//
// So every output now goes through the out* functions below instead of SDL's.
// They return a virtual device id. On the speaker route each virtual device is
// backed by its own SDL device, exactly as before (passthrough). On the headset
// route all of them feed one small mixer, which owns the single SDL device
// opened on "bluealsa:SRV=org.bluealsa,DEV=<addr>,PROFILE=a2dp" (the PCM
// string spruceOS verified on the same firmware) and resamples, up-mixes and
// sums them. Callers keep their own format and rate and see the queue sizes
// they always saw, so pacing loops do not change.
//
// The output monitor (audiooutput.go) picks the route. A game launch drops back
// to the speaker route before the child starts (screenChildGroupConfigure) so
// the game can open the headset itself, and gets AUDIODEV pointing at it; the
// game's exit wakes the monitor, which takes the headset back. Windows never
// leaves the speaker route: there SDL's default device already follows the
// system's default output.

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// outVirtualBase starts the virtual ids, far above any id SDL hands out,
	// so an id that is not in the table is known to be a raw SDL device.
	outVirtualBase sdl.AudioDeviceID = 1 << 24
	// outMixTick is how often the mixer tops up the headset's queue, and
	// outMixLead how much it keeps queued there. BlueALSA adds its own buffer
	// on top, so a short lead keeps UI sounds from lagging further.
	outMixTick = 10 * time.Millisecond
	outMixLead = 120 * time.Millisecond
	// outFIFOMax caps one virtual device's backlog while mixed. Callers pace
	// themselves on the queue size long before this; the cap only matters when
	// the headset stopped consuming and the monitor has not switched yet.
	outFIFOMax = 4 * time.Second
	// outRetryAfter is how long a headset PCM that would not open is left
	// alone before the next attempt, and outQuickRetries how many attempts
	// are scheduled that soon before the monitor's own pace takes over.
	outRetryAfter   = 3 * time.Second
	outQuickRetries = 5
	// outLostHold is how long a headset whose stream died is left alone.
	outLostHold = 30 * time.Second
	// outBluealsaRestartAfter spaces attempts to start a missing bluealsa.
	outBluealsaRestartAfter = time.Minute
)

// outDev is one virtual output device.
type outDev struct {
	format   sdl.AudioFormat
	freq     int32
	channels int
	samples  uint16
	paused   bool
	// real is the device's own SDL device on the speaker route, 0 while mixed.
	real sdl.AudioDeviceID
	// draining is the speaker device still playing what it had queued when the
	// route moved to the headset; the mixer starts on this device's backlog
	// once it is empty, so nothing plays twice or out of order.
	draining sdl.AudioDeviceID
	// fifo[head:] is the backlog while mixed, in the caller's format; phase is
	// the resampler's fractional position in it, in input frames.
	fifo  []byte
	head  int
	phase float64
	// dead is set when the speaker device would not reopen after the headset
	// route ended; outQueue then drops instead of buffering.
	dead bool
}

var outAudio struct {
	mu      sync.Mutex
	devs    map[sdl.AudioDeviceID]*outDev
	nextID  sdl.AudioDeviceID
	pcm     string            // headset PCM while mixed, "" on the speaker route
	mixDev  sdl.AudioDeviceID // the one SDL device on the headset
	mixRate int32
	mixStop chan struct{}
	gain    float64 // headset volume, 0..1 (the header's volume, see audioRouteUpdate)
	acc     []int32
	out     []byte
	// failedPCM is held off until holdUntil: it would not open, or its
	// stream died; failedTries counts the quick retries spent on it.
	failedPCM   string
	holdUntil   time.Time
	failedTries int
}

// audioDevEnvMu serialises every SDL open that may set AUDIODEV (SDL's ALSA
// driver reads it during the open, and only then): the outputs here and the
// microphone (voicemic.go). Without it one open could pick up the other's PCM.
var audioDevEnvMu sync.Mutex

// outOpenReal opens a real SDL playback device, on pcm when it is set (via
// AUDIODEV) and on the default output otherwise.
func outOpenReal(desired, obtained *sdl.AudioSpec, allowed int, pcm string) (sdl.AudioDeviceID, error) {
	audioDevEnvMu.Lock()
	defer audioDevEnvMu.Unlock()
	if pcm != "" {
		prev, had := os.LookupEnv("AUDIODEV")
		_ = os.Setenv("AUDIODEV", pcm)
		defer func() {
			if had {
				_ = os.Setenv("AUDIODEV", prev)
			} else {
				_ = os.Unsetenv("AUDIODEV")
			}
		}()
	}
	id, err := sdl.OpenAudioDevice("", false, desired, obtained, allowed)
	if err == nil && id == 0 {
		err = errors.New("SDL opened no device")
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// outMixable reports whether the mixer can take a device of this shape:
// signed 16-bit native-endian, mono or stereo — what every output here uses.
func outMixable(format sdl.AudioFormat, channels uint8) bool {
	return format == sdl.AUDIO_S16SYS && (channels == 1 || channels == 2)
}

// outOpen is sdl.OpenAudioDevice("", false, ...) for JukaHub's outputs. The
// device starts paused, as SDL's do.
func outOpen(desired, obtained *sdl.AudioSpec, allowed int) (sdl.AudioDeviceID, error) {
	if desired == nil || obtained == nil {
		return 0, errors.New("no audio spec")
	}
	if !outMixable(desired.Format, desired.Channels) {
		return outOpenReal(desired, obtained, allowed, "")
	}
	d := &outDev{paused: true, samples: desired.Samples}
	for {
		outAudio.mu.Lock()
		mixed := outAudio.mixDev != 0
		outAudio.mu.Unlock()
		if mixed {
			// Mixed: the caller's own format is kept and converted by the mixer.
			*obtained = *desired
			if obtained.Freq <= 0 {
				obtained.Freq = 48000
			}
		} else if d.real == 0 {
			// SDL may change only the rate (the flags callers pass); the mixer
			// can take any rate later. Opened without outAudio.mu: an ALSA open
			// can be slow, and the render thread queues UI sounds under that lock.
			id, err := outOpenReal(desired, obtained, allowed&sdl.AUDIO_ALLOW_FREQUENCY_CHANGE, "")
			if err != nil {
				return 0, err
			}
			d.real = id
		}
		outAudio.mu.Lock()
		if d.real == 0 && outAudio.mixDev == 0 {
			// The headset route ended meanwhile, before this device was in the
			// table for outToSpeaker to reopen: open its speaker now.
			outAudio.mu.Unlock()
			continue
		}
		break
	}
	defer outAudio.mu.Unlock()
	if d.real != 0 && outAudio.mixDev != 0 {
		// The headset was routed meanwhile: the new speaker device is empty,
		// so as "draining" it is reaped by the mixer once unpaused.
		d.draining, d.real = d.real, 0
	}
	d.format, d.freq, d.channels = desired.Format, obtained.Freq, int(desired.Channels)
	if d.freq <= 0 {
		d.freq = 48000
	}
	if outAudio.devs == nil {
		outAudio.devs = make(map[sdl.AudioDeviceID]*outDev, 6)
		outAudio.nextID = outVirtualBase
	}
	outAudio.nextID++
	id := outAudio.nextID
	outAudio.devs[id] = d
	return id, nil
}

// outQueue is sdl.QueueAudio.
func outQueue(id sdl.AudioDeviceID, data []byte) error {
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	d := outAudio.devs[id]
	if d == nil {
		return sdl.QueueAudio(id, data)
	}
	if d.real != 0 {
		return sdl.QueueAudio(d.real, data)
	}
	if d.dead {
		// Its speaker device would not reopen: dropped, as on mute hardware,
		// so no caller waits on a queue that never drains. outSetRoute("")
		// tries the open again.
		return errors.New("no audio device")
	}
	d.compact()
	// Whole frames only: a stray byte the mixer cannot play would keep the
	// queue from ever reading empty.
	fsz := 2 * d.channels
	data = data[:len(data)/fsz*fsz]
	limit := int(float64(d.freq)*outFIFOMax.Seconds()) * fsz
	if len(d.fifo)-d.head+len(data) > limit {
		return errors.New("audio backlog full")
	}
	d.fifo = append(d.fifo, data...)
	return nil
}

// outQueued is sdl.GetQueuedAudioSize, in the caller's own format.
func outQueued(id sdl.AudioDeviceID) uint32 {
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	d := outAudio.devs[id]
	if d == nil {
		return sdl.GetQueuedAudioSize(id)
	}
	if d.real != 0 {
		return sdl.GetQueuedAudioSize(d.real)
	}
	n := uint32(len(d.fifo) - d.head)
	if d.draining != 0 {
		n += sdl.GetQueuedAudioSize(d.draining)
	}
	return n
}

// outClear is sdl.ClearQueuedAudio.
func outClear(id sdl.AudioDeviceID) {
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	d := outAudio.devs[id]
	if d == nil {
		sdl.ClearQueuedAudio(id)
		return
	}
	if d.real != 0 {
		sdl.ClearQueuedAudio(d.real)
	}
	if d.draining != 0 {
		sdl.ClearQueuedAudio(d.draining)
	}
	d.fifo, d.head, d.phase = d.fifo[:0], 0, 0
}

// outPause is sdl.PauseAudioDevice.
func outPause(id sdl.AudioDeviceID, paused bool) {
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	d := outAudio.devs[id]
	if d == nil {
		sdl.PauseAudioDevice(id, paused)
		return
	}
	d.paused = paused
	if d.real != 0 {
		sdl.PauseAudioDevice(d.real, paused)
	}
	if d.draining != 0 {
		sdl.PauseAudioDevice(d.draining, paused)
	}
}

// outClose is sdl.CloseAudioDevice. The SDL closes run after the lock is
// released: an ALSA close waits out its last buffers.
func outClose(id sdl.AudioDeviceID) {
	outAudio.mu.Lock()
	d := outAudio.devs[id]
	if d == nil {
		outAudio.mu.Unlock()
		sdl.CloseAudioDevice(id)
		return
	}
	delete(outAudio.devs, id)
	rdev, ddev := d.real, d.draining
	d.real, d.draining = 0, 0
	outAudio.mu.Unlock()
	if rdev != 0 {
		sdl.CloseAudioDevice(rdev)
	}
	if ddev != 0 {
		sdl.CloseAudioDevice(ddev)
	}
}

// compact drops the played part of the backlog once it is the larger half.
func (d *outDev) compact() {
	if d.head == 0 {
		return
	}
	if d.head == len(d.fifo) {
		d.fifo, d.head = d.fifo[:0], 0
		return
	}
	if d.head > 64<<10 && d.head > len(d.fifo)/2 {
		n := copy(d.fifo, d.fifo[d.head:])
		d.fifo, d.head = d.fifo[:n], 0
	}
}

// mixInto adds up to frames output frames of d's backlog to acc (stereo
// int32), resampling linearly from d.freq to rate. It returns whether anything
// was added. The last buffered frame is played on its own rather than held
// back for interpolation, so a backlog always drains to zero — callers wait
// for that (the boot chime, Juka's voice).
func (d *outDev) mixInto(acc []int32, frames int, rate int32) bool {
	fsz := 2 * d.channels
	data := d.fifo[d.head:]
	n := len(data) / fsz
	if n == 0 || rate <= 0 {
		return false
	}
	step := float64(d.freq) / float64(rate)
	pos := d.phase
	sample := func(frame, ch int) float64 {
		off := frame*fsz + ch*2
		return float64(int16(uint16(data[off]) | uint16(data[off+1])<<8))
	}
	i := 0
	for ; i < frames; i++ {
		i0 := int(pos)
		if i0 >= n {
			break
		}
		i1 := i0 + 1
		if i1 >= n {
			i1 = i0
		}
		frac := pos - float64(i0)
		l := sample(i0, 0) + (sample(i1, 0)-sample(i0, 0))*frac
		r := l
		if d.channels == 2 {
			r = sample(i0, 1) + (sample(i1, 1)-sample(i0, 1))*frac
		}
		acc[2*i] += int32(l)
		acc[2*i+1] += int32(r)
		pos += step
	}
	used := int(pos)
	if used >= n {
		d.head += n * fsz
		d.phase = 0
	} else {
		d.head += used * fsz
		d.phase = pos - float64(used)
	}
	d.compact()
	return i > 0
}

// outMixLoop tops up the headset's queue until the route leaves it. It takes
// the lock each tick and quits once mixStop is no longer its own channel, so a
// route switch never has to wait for it.
func outMixLoop(stop chan struct{}) {
	ticker := time.NewTicker(outMixTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		outAudio.mu.Lock()
		if outAudio.mixStop != stop {
			outAudio.mu.Unlock()
			return
		}
		lost, drained := outMixStepLocked()
		outAudio.mu.Unlock()
		for _, id := range drained {
			sdl.CloseAudioDevice(id)
		}
		if lost {
			// The headset went away under SDL (out of range, switched off):
			// back to the speaker at once instead of waiting for the monitor,
			// and no new attempt for a while, so a headset at the edge of range
			// does not make every output hop back and forth.
			outRouteMu.Lock()
			outAudio.mu.Lock()
			still := outAudio.mixStop == stop
			if still {
				outAudio.failedPCM, outAudio.failedTries = outAudio.pcm, outQuickRetries
				outAudio.holdUntil = time.Now().Add(outLostHold)
			}
			outAudio.mu.Unlock()
			if still {
				logInfo("audio", "headset output stopped; back to the speaker")
				outToSpeaker()
			}
			outRouteMu.Unlock()
			return
		}
	}
}

// outMixStepLocked mixes one top-up. lost is true when the headset device has
// stopped (SDL closes a device whose ALSA stream failed for good); drained are
// speaker devices that finished playing, for the caller to close once the lock
// is released.
func outMixStepLocked() (lost bool, drained []sdl.AudioDeviceID) {
	dev, rate := outAudio.mixDev, outAudio.mixRate
	if dev == 0 || rate <= 0 {
		return false, nil
	}
	if sdl.GetAudioDeviceStatus(dev) == sdl.AUDIO_STOPPED {
		return true, nil
	}
	for _, d := range outAudio.devs {
		if d.draining != 0 && !d.paused && sdl.GetQueuedAudioSize(d.draining) == 0 {
			drained = append(drained, d.draining)
			d.draining = 0
		}
	}
	target := uint32(float64(rate)*outMixLead.Seconds()) * 4
	queued := sdl.GetQueuedAudioSize(dev)
	if queued >= target {
		return false, drained
	}
	frames := int(target-queued) / 4
	if cap(outAudio.acc) < frames*2 {
		outAudio.acc = make([]int32, frames*2)
		outAudio.out = make([]byte, frames*4)
	}
	acc := outAudio.acc[:frames*2]
	for i := range acc {
		acc[i] = 0
	}
	mixed := false
	for _, d := range outAudio.devs {
		if d.paused || d.draining != 0 || d.real != 0 {
			continue
		}
		if d.mixInto(acc, frames, rate) {
			mixed = true
		}
	}
	if !mixed {
		// Nothing to play: SDL plays silence by itself, no need to queue it.
		return false, drained
	}
	gain := outAudio.gain
	out := outAudio.out[:frames*4]
	for i, v := range acc {
		f := float64(v) * gain
		if f > 32767 {
			f = 32767
		} else if f < -32768 {
			f = -32768
		}
		s := int16(f)
		out[2*i] = byte(s)
		out[2*i+1] = byte(uint16(s) >> 8)
	}
	_ = sdl.QueueAudio(dev, out)
	return false, drained
}

// outHandBackLocked queues d's unplayed backlog on its speaker device d.real
// and empties the backlog.
func outHandBackLocked(d *outDev) {
	frame := 2 * d.channels
	if rest := d.fifo[d.head:]; len(rest) >= frame {
		_ = sdl.QueueAudio(d.real, rest[:len(rest)/frame*frame])
	}
	d.fifo, d.head, d.phase = d.fifo[:0], 0, 0
}

// outToSpeaker leaves the headset: the mixer stops, its device closes, and
// every virtual device gets its own speaker device back, with whatever it
// still had to play queued there first. A device whose speaker would not open
// before is tried again. The caller holds outRouteMu, not outAudio.mu: the SDL
// closes and opens run outside that lock (the render thread queues UI sounds
// under it), and anything queued meanwhile waits in the backlog.
func outToSpeaker() {
	outAudio.mu.Lock()
	if outAudio.mixStop != nil {
		close(outAudio.mixStop)
		outAudio.mixStop = nil
	}
	mixDev := outAudio.mixDev
	outAudio.mixDev, outAudio.pcm = 0, ""
	var reopen []*outDev
	for _, d := range outAudio.devs {
		switch {
		case d.real != 0:
		case d.draining != 0:
			// Its old speaker device is still open: back in service at once.
			d.real, d.draining, d.dead = d.draining, 0, false
			outHandBackLocked(d)
		default:
			reopen = append(reopen, d)
		}
	}
	outAudio.mu.Unlock()
	if mixDev != 0 {
		sdl.CloseAudioDevice(mixDev)
	}
	ids := make([]sdl.AudioDeviceID, len(reopen))
	for i, d := range reopen {
		want := sdl.AudioSpec{Freq: d.freq, Format: d.format, Channels: uint8(d.channels), Samples: d.samples}
		var got sdl.AudioSpec
		id, err := outOpenReal(&want, &got, 0, "")
		if err != nil {
			if !d.dead {
				logWarn("audio", "speaker output would not reopen: %v", err)
			}
			continue
		}
		ids[i] = id
	}
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	for i, d := range reopen {
		id := ids[i]
		live := false
		for _, cur := range outAudio.devs {
			if cur == d {
				live = true
				break
			}
		}
		switch {
		case !live || d.real != 0 || outAudio.mixDev != 0:
			// Closed meanwhile (or, defensively, routed again): not needed.
			if id != 0 {
				sdl.CloseAudioDevice(id)
			}
		case id == 0:
			// Dropped from now on, as on mute hardware; retried next time.
			d.dead = true
			d.fifo, d.head, d.phase = d.fifo[:0], 0, 0
		default:
			d.real, d.dead = id, false
			sdl.PauseAudioDevice(id, d.paused)
			outHandBackLocked(d)
		}
	}
}

// outToHeadsetLocked moves every output onto id, the device already opened on
// the headset PCM at rate. The speaker route is current (outSetRoute).
func outToHeadsetLocked(pcm string, id sdl.AudioDeviceID, rate int32) {
	if rate <= 0 {
		rate = 48000
	}
	sdl.PauseAudioDevice(id, false)
	outAudio.mixDev, outAudio.mixRate, outAudio.pcm = id, rate, pcm
	for _, d := range outAudio.devs {
		// The mixer needs no device of its own: one without a speaker is
		// heard again.
		d.dead = false
		if d.real == 0 {
			continue
		}
		if d.draining != 0 {
			sdl.CloseAudioDevice(d.draining)
		}
		d.draining, d.real = d.real, 0
		d.fifo, d.head, d.phase = d.fifo[:0], 0, 0
	}
	stop := make(chan struct{})
	outAudio.mixStop = stop
	go outMixLoop(stop)
}

// outRouteMu serialises route changes. The headset device is opened before
// outAudio.mu is taken — a BlueALSA open is a D-Bus round trip, and the render
// thread queues UI sounds under that lock — so two changes must not interleave.
var outRouteMu sync.Mutex

// outSetRoute moves the outputs to pcm ("" = speaker) at headset volume gain.
// It reports whether the route is now pcm.
func outSetRoute(pcm string, gain float64) bool {
	outRouteMu.Lock()
	defer outRouteMu.Unlock()
	outAudio.mu.Lock()
	outAudio.gain = gain
	current := outAudio.pcm
	held := pcm != "" && pcm == outAudio.failedPCM && time.Now().Before(outAudio.holdUntil)
	dead := false
	for _, d := range outAudio.devs {
		dead = dead || d.dead
	}
	outAudio.mu.Unlock()
	if pcm == current {
		if pcm == "" && dead {
			// A speaker device that would not reopen: try again.
			outToSpeaker()
		}
		return true
	}
	if pcm == "" {
		logInfo("audio", "output route: speaker")
		outToSpeaker()
		return true
	}
	if held {
		return false
	}
	if current != "" {
		// BlueALSA would refuse a second client of the same headset; any other
		// headset's device closes before the new one opens.
		outToSpeaker()
	}
	want := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16SYS, Channels: 2, Samples: 1024}
	var got sdl.AudioSpec
	id, err := outOpenReal(&want, &got, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE, pcm)
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	if err == nil && gameScreenSuspended() {
		// A game started while the headset opened: it is the game's. (The
		// launch increments the count before it releases, so either this sees
		// the game or the release sees this route.)
		sdl.CloseAudioDevice(id)
		return false
	}
	if err != nil {
		if pcm != outAudio.failedPCM {
			logWarn("audio", "headset output %q would not open: %v", pcm, err)
			outAudio.failedTries = 0
		}
		outAudio.failedPCM, outAudio.holdUntil = pcm, time.Now().Add(outRetryAfter)
		// Right after a connect the headset's A2DP stream needs a moment to
		// appear in BlueALSA: try again soon, a few times, then fall back to
		// the monitor's own pace.
		if outAudio.failedTries < outQuickRetries {
			outAudio.failedTries++
			time.AfterFunc(outRetryAfter, audioRouteKick)
		}
		return false
	}
	outToHeadsetLocked(pcm, id, got.Freq)
	outAudio.failedPCM, outAudio.failedTries = "", 0
	logInfo("audio", "output route: headset (%s, %d Hz)", pcm, outAudio.mixRate)
	return true
}

// ── Route policy ─────────────────────────────────────────────────────────────

// bluealsaPCM is the ALSA PCM of a headset's A2DP stream.
func bluealsaPCM(addr string) string {
	return "bluealsa:SRV=org.bluealsa,DEV=" + addr + ",PROFILE=a2dp"
}

// outBluealsa remembers the last attempt to start bluealsa.
var outBluealsa struct {
	mu      sync.Mutex
	started time.Time
}

// bluealsaReady reports whether the BlueALSA daemon runs. When it does not and
// the firmware has it, it is started (A2DP source, the role a handheld plays)
// and the headset is asked to connect again so its audio profile attaches to
// the new endpoint; the route follows on a later probe.
func bluealsaReady(addr string) bool {
	// BlueALSA 4.2 renamed its daemon bluealsad.
	if len(btProcessPIDs("/proc", "bluealsa")) > 0 || len(btProcessPIDs("/proc", "bluealsad")) > 0 {
		return true
	}
	path, ok := btToolPath("bluealsa")
	if !ok {
		return false
	}
	outBluealsa.mu.Lock()
	if !outBluealsa.started.IsZero() && time.Since(outBluealsa.started) < outBluealsaRestartAfter {
		outBluealsa.mu.Unlock()
		return false
	}
	outBluealsa.started = time.Now()
	outBluealsa.mu.Unlock()
	if _, err := btStartDetached(path, "-p", "a2dp-source"); err != nil {
		logWarn("audio", "bluealsa did not start: %v", err)
		return false
	}
	logInfo("audio", "started bluealsa for the headset")
	go func() {
		time.Sleep(2 * time.Second)
		_, _ = btToolRun("bluetoothctl", "connect", addr)
		audioRouteKick()
	}()
	return false
}

// audioRouteKicks wakes the output monitor for an early probe.
var audioRouteKicks = make(chan struct{}, 1)

// audioRouteKick asks the output monitor to probe now (a headset was just
// connected from the Bluetooth page, bluealsa was started).
func audioRouteKick() {
	select {
	case audioRouteKicks <- struct{}{}:
	default:
	}
}

// audioRouteUpdate applies the monitor's finding: addr is the connected
// headset's address, "" for none. While a game holds the screen the speaker
// route is kept, so the game can open the headset itself.
func audioRouteUpdate(addr string) {
	// Only the handhelds: a desktop Linux has PipeWire or PulseAudio for its
	// headsets, and starting bluealsa there would take the headset from them.
	// Windows' default device already follows the system's output.
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return
	}
	if addr == "" {
		// Nothing connected: the next connect starts with fresh retries.
		outAudio.mu.Lock()
		outAudio.failedPCM, outAudio.failedTries = "", 0
		outAudio.mu.Unlock()
	}
	pcm := ""
	if addr != "" && !gameScreenSuspended() && bluealsaReady(addr) {
		pcm = bluealsaPCM(addr)
	}
	// The volume keys move the speaker codec, which the headset never hears:
	// the header's level is applied in the mixer instead. Linear, not a curve:
	// the headset has its own volume on top, and a quiet speaker setting must
	// not make the headset seem dead.
	gain := 1.0
	if pcm != "" {
		if v, ok := systemVolume(); ok {
			gain = math.Max(0, math.Min(100, float64(v))) / 100
		}
	}
	outSetRoute(pcm, gain)
}

// audioRouteSetVolume applies a volume change (0..100) to the headset at once;
// setSystemVolume calls it. Harmless on the speaker route, where the next
// headset route reads the level afresh anyway.
func audioRouteSetVolume(pct int) {
	outAudio.mu.Lock()
	outAudio.gain = math.Max(0, math.Min(100, float64(pct))) / 100
	outAudio.mu.Unlock()
}

// audioRouteChildPCM is the headset PCM a child should be given as AUDIODEV,
// "" on the speaker route.
func audioRouteChildPCM() string {
	outAudio.mu.Lock()
	defer outAudio.mu.Unlock()
	return outAudio.pcm
}

// audioRouteReleaseForChild hands the headset over the moment a screen-taking
// child has started (noteGameChildStarted): JukaHub's outputs go back to the
// speaker, since BlueALSA would refuse the game a second client. An emulator
// takes far longer than this close to open its audio. The monitor takes the
// headset back when the child exits (its foreground wake).
func audioRouteReleaseForChild() {
	if audioRouteChildPCM() == "" {
		return
	}
	outSetRoute("", 1)
}

// audioRouteDiagFiles are where BlueALSA's ALSA plugin and its "bluealsa" PCM
// definition live on the firmwares seen so far; the headset PCM opens only
// when both exist.
var audioRouteDiagFiles = []string{
	"/usr/lib/alsa-lib/libasound_module_pcm_bluealsa.so",
	"/usr/share/alsa/alsa.conf.d/20-bluealsa.conf",
	"/etc/alsa/conf.d/20-bluealsa.conf",
}

// audioRouteDiag is the "Audio route" line of the Bluetooth diagnostics block:
// the route, the last headset PCM that would not open, the plugin files, and
// whether the firmware's own HOME has an .asoundrc (some firmwares route
// games to the headset that way).
func audioRouteDiag() string {
	outAudio.mu.Lock()
	route, failed := outAudio.pcm, outAudio.failedPCM
	outAudio.mu.Unlock()
	if route == "" {
		route = "speaker"
	}
	parts := []string{route}
	if failed != "" {
		parts = append(parts, "failed="+failed)
	}
	for _, p := range audioRouteDiagFiles {
		if _, err := os.Stat(p); err == nil {
			parts = append(parts, p)
		}
	}
	if home := os.Getenv(childEnvOrigPrefix + "HOME"); home != "" {
		if _, err := os.Stat(filepath.Join(home, ".asoundrc")); err == nil {
			parts = append(parts, filepath.Join(home, ".asoundrc"))
		}
	}
	return strings.Join(parts, " ")
}
