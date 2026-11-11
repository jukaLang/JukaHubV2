package main

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// --- UI sound effects ---
//
// The interface's sounds are soft "tocks" in the iOS manner, not beeps. Each one
// is a short sine body (a little triangle mixed in for presence) whose pitch
// glides down from the attack, an exponential decay, and about a millisecond of
// band-passed noise on the leading edge: the tap a finger makes on glass. The
// old navigation sound was a flat sine with a linear envelope, which the
// handheld's small speaker turned into a hard click.
//
// The six variants are one family: navigate is the lightest and shortest,
// activate the fullest, back a softer step down, toggle a two-part click-clack,
// error two low falling knocks and success a tock answered by a brighter, longer
// tone. Every buffer starts and ends at silence (a 1.5 ms raised-cosine attack,
// a 3 ms fade to zero), so nothing clicks.
//
// They are synthesised once, when the device opens (bootSfxPrepare, a boot
// step), at the device's own rate. A key press then only copies a finished
// buffer into SDL's queue: no maths and no allocation on the press.
//
// The device is a plain SDL2 queue (SDL_QueueAudio) in mono S16 — the format
// the trophy chime and the alarm queue too. Every function here is
// non-blocking and silent when no audio device is available, so the app never
// hangs or crashes on mute hardware.

var (
	audioInitOnce   sync.Once
	audioDev        sdl.AudioDeviceID
	audioSampleRate int32 = 48000
	audioAvailable  bool
	audioMutex      sync.Mutex
)

// initAudioDevice lazily opens the default output device. It runs once and
// only ever touches SDL audio APIs; if opening fails, audioAvailable stays
// false and all subsequent calls are no-ops.
//
// 48 kHz is asked for because it is what the video device and most mixers run
// at (no resampling in ALSA's plug layer) and a 512-sample buffer is then about
// 11 ms of latency. Only the rate may change: everything queued here is mono
// S16, and a device that came up stereo (the old AUDIO_ALLOW_CHANNELS_CHANGE)
// played every sound an octave high and half as long, so SDL converts the
// channel count itself instead.
func initAudioDevice() {
	desired := sdl.AudioSpec{
		Freq:     audioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  512,
	}
	var obtained sdl.AudioSpec
	id, err := sdl.OpenAudioDevice("", false, &desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || id == 0 {
		logWarn("audio", "no UI sound device: %v", err)
		return
	}
	sdl.PauseAudioDevice(id, false)
	audioDev = id
	if obtained.Freq > 0 {
		audioSampleRate = obtained.Freq
	}
	audioAvailable = true
}

// ── UI sound settings ────────────────────────────────────────────────────

const (
	// bootSfxEnabledKey and bootSfxVolumeKey are the Custom settings for the
	// UI sounds: on/off (missing means on) and a 0..100 volume (missing means
	// 100, the level the sounds were designed at). Settings shows them as
	// "Interface sounds" (setmodel.go); the trophy chime follows them too
	// (trophy_sound.go). The alarm is not a UI sound and never reads them.
	bootSfxEnabledKey = "uiSounds"
	bootSfxVolumeKey  = "uiSoundVolume"
)

var (
	// bootSfxMuted is the inverse of the on/off setting, so its zero value is
	// "sounds on" before any config has been read.
	bootSfxMuted atomic.Bool
	// bootSfxLevel is the volume in percent; bootSfxLevelSet tells a stored 0
	// (muted by the slider) from "not read yet".
	bootSfxLevel    atomic.Int32
	bootSfxLevelSet atomic.Bool
)

// bootSfxBind reads the two settings from config. It is cheap (two map
// lookups) and runs on the main thread once a second (alarmCheck), so a switch
// flipped in Settings — config scene or Go-owned page — applies within a second
// without either having to call into this file.
func bootSfxBind(config *Config) {
	if config == nil {
		return
	}
	on := true
	if v, ok := customValue(config.Variables.Custom, bootSfxEnabledKey); ok {
		on = bootSfxParseBool(v, true)
	}
	pct := 100
	if v, ok := customValue(config.Variables.Custom, bootSfxVolumeKey); ok {
		if n, ok := bootSfxParsePercent(v); ok {
			pct = n
		}
	}
	bootSfxApply(on, pct)
	// Spatial audio's settings ride the same bind (spatial.go).
	spatialBind(config)
}

// bootSfxApply sets the live state without touching any config.
func bootSfxApply(on bool, pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	bootSfxMuted.Store(!on)
	bootSfxLevel.Store(int32(pct))
	bootSfxLevelSet.Store(true)
}

// bootSfxSetPrefs is the setter a settings page calls: it writes both values
// into config (as the strings a toggle writes) and applies them at once. Saving
// the user config is the caller's job, through its usual save path.
func bootSfxSetPrefs(config *Config, on bool, pct int) {
	bootSfxApply(on, pct)
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[bootSfxEnabledKey] = strconv.FormatBool(on)
	config.Variables.Custom[bootSfxVolumeKey] = strconv.Itoa(bootSfxVolume())
}

// bootSfxEnabled reports whether UI sounds play at all.
func bootSfxEnabled() bool { return !bootSfxMuted.Load() && bootSfxVolume() > 0 }

// bootSfxVolume is the UI sound volume in percent.
func bootSfxVolume() int {
	if !bootSfxLevelSet.Load() {
		return 100
	}
	return int(bootSfxLevel.Load())
}

// bootSfxParseBool reads a switch value in any of the shapes a config holds
// (bool, "true"/"on"/"yes", a number); anything unreadable keeps def.
func bootSfxParseBool(v interface{}, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "on", "yes", "1":
			return true
		case "false", "off", "no", "0":
			return false
		}
	}
	return def
}

// bootSfxParsePercent reads a 0..100 volume: a number, a numeric string, with
// or without a trailing %, or a 0..1 fraction. Only a value written as one is a
// fraction — a number strictly between 0 and 1, or a string with a decimal
// point and no % ("0.5", "1.0") — so "1" stays 1% while "1.0" is full volume.
func bootSfxParsePercent(v interface{}) (int, bool) {
	var f float64
	fraction := false
	switch t := v.(type) {
	case float64:
		f = t
		fraction = t > 0 && t < 1
	case int:
		f = float64(t)
	case string:
		s := strings.TrimSpace(t)
		percent := strings.HasSuffix(s, "%")
		s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
		fraction = !percent && strings.Contains(s, ".") && parsed <= 1
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if fraction {
		f *= 100
	}
	if f < 0 {
		f = 0
	}
	if f > 100 {
		f = 100
	}
	return int(math.Round(f)), true
}

// ── Synthesis ────────────────────────────────────────────────────────────

// bootSfxKind names one UI sound.
type bootSfxKind int

const (
	bootSfxNav bootSfxKind = iota
	bootSfxActivate
	bootSfxBack
	bootSfxToggle
	bootSfxError
	bootSfxSuccess
	bootSfxKindCount
)

// bootSfxTock is one tap of a UI sound.
type bootSfxTock struct {
	at      float64 // start, seconds from the beginning of the sound
	startHz float64 // pitch at the attack
	endHz   float64 // pitch the glide settles on
	glide   float64 // time constant of the downward glide, seconds
	decay   float64 // time constant of the body's decay, seconds
	length  float64 // seconds until the tail has been faded to silence
	level   float64 // peak of the body, fraction of full scale
	click   float64 // level of the noise transient, relative to level
	tri     float64 // 0 = pure sine body, 1 = triangle (more presence)
}

// bootSfxScore is the family. Levels stay well under full scale: the handheld's
// speaker is small and close to the face, and a tap that is felt more than
// heard is the point.
func bootSfxScore(kind bootSfxKind) []bootSfxTock {
	switch kind {
	case bootSfxActivate:
		return []bootSfxTock{{0, 285, 172, 0.007, 0.011, 0.042, 0.30, 0.45, 0.25}}
	case bootSfxBack:
		return []bootSfxTock{{0, 238, 150, 0.008, 0.010, 0.038, 0.24, 0.30, 0.15}}
	case bootSfxToggle:
		return []bootSfxTock{
			{0, 320, 232, 0.004, 0.006, 0.024, 0.22, 0.50, 0.30},
			{0.032, 262, 196, 0.005, 0.008, 0.030, 0.17, 0.35, 0.20},
		}
	case bootSfxError:
		return []bootSfxTock{
			{0, 228, 160, 0.010, 0.014, 0.052, 0.30, 0.30, 0.35},
			{0.088, 198, 128, 0.012, 0.018, 0.062, 0.28, 0.25, 0.35},
		}
	case bootSfxSuccess:
		return []bootSfxTock{
			{0, 300, 252, 0.006, 0.012, 0.046, 0.24, 0.35, 0.15},
			{0.074, 452, 392, 0.006, 0.030, 0.112, 0.20, 0.22, 0.05},
		}
	}
	return []bootSfxTock{{0, 310, 205, 0.006, 0.008, 0.030, 0.20, 0.35, 0.15}}
}

const (
	bootSfxAttack  = 0.0015 // raised-cosine attack, seconds
	bootSfxRelease = 0.003  // fade to zero at the end of every tock, seconds
	bootSfxTapTau  = 0.0012 // decay of the noise transient, seconds
)

// bootSfxPCM synthesises one UI sound as mono signed 16-bit little-endian
// samples at rate. The noise is a fixed xorshift sequence, so a sound is the
// same buffer on every launch and the tests can pin it.
func bootSfxPCM(rate int32, kind bootSfxKind) []byte {
	if rate <= 0 {
		return nil
	}
	sr := float64(rate)
	score := bootSfxScore(kind)
	total := 0.0
	for _, t := range score {
		if end := t.at + t.length; end > total {
			total = end
		}
	}
	n := int(math.Ceil(total * sr))
	if n <= 0 {
		return nil
	}
	mix := make([]float64, n)
	seed := uint32(0x9E3779B9) ^ uint32(kind+1)*0x85EBCA6B
	// One-pole filter coefficients: a low-pass near 3.5 kHz and a slower one
	// near 600 Hz; their difference is the band the tap sits in.
	lpFast := 1 - math.Exp(-2*math.Pi*3500/sr)
	lpSlow := 1 - math.Exp(-2*math.Pi*600/sr)
	for _, tk := range score {
		start := int(tk.at * sr)
		length := int(tk.length * sr)
		phase := 0.0
		fast, slow := 0.0, 0.0
		for i := 0; i < length && start+i < n; i++ {
			t := float64(i) / sr
			freq := tk.endHz + (tk.startHz-tk.endHz)*math.Exp(-t/tk.glide)
			phase += 2 * math.Pi * freq / sr
			sine := math.Sin(phase)
			body := sine
			if tk.tri > 0 {
				body = (1-tk.tri)*sine + tk.tri*(2/math.Pi)*math.Asin(sine)
			}
			env := math.Exp(-t / tk.decay)
			if t < bootSfxAttack {
				env *= 0.5 - 0.5*math.Cos(math.Pi*t/bootSfxAttack)
			}
			if rest := tk.length - t; rest < bootSfxRelease {
				if rest < 0 {
					rest = 0
				}
				env *= 0.5 - 0.5*math.Cos(math.Pi*rest/bootSfxRelease)
			}
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			noise := float64(seed)/float64(math.MaxUint32)*2 - 1
			fast += lpFast * (noise - fast)
			slow += lpSlow * (noise - slow)
			tapEnv := math.Exp(-t / bootSfxTapTau)
			if t < bootSfxAttack/3 {
				tapEnv *= t / (bootSfxAttack / 3)
			}
			mix[start+i] += tk.level * (body*env + tk.click*(fast-slow)*2*tapEnv*env)
		}
	}
	out := make([]byte, n*2)
	for i, v := range mix {
		// A gentle soft clip: never reached at these levels, but a future
		// louder variant still cannot wrap around.
		if v > 0.98 {
			v = 0.98
		} else if v < -0.98 {
			v = -0.98
		}
		s := int16(math.Round(v * 32767))
		out[i*2] = byte(s)
		out[i*2+1] = byte(s >> 8)
	}
	return out
}

// bootSfxBank holds every UI sound rendered for the device's rate, plus one
// scratch buffer for the volume-scaled copy, so a press never allocates.
var bootSfxBank struct {
	once    sync.Once
	rate    int32
	pcm     [bootSfxKindCount][]byte
	scratch []byte
}

// bootSfxPrepare opens the device and renders the bank. The boot queue runs it
// on a goroutine during the splash; a press before that finished waits on the
// same Once (a few milliseconds) instead of rendering twice.
func bootSfxPrepare() {
	bootSfxBank.once.Do(func() {
		audioInitOnce.Do(initAudioDevice)
		if !audioAvailable {
			return
		}
		rate := audioSampleRate
		longest := 0
		for k := bootSfxKind(0); k < bootSfxKindCount; k++ {
			bootSfxBank.pcm[k] = bootSfxPCM(rate, k)
			if len(bootSfxBank.pcm[k]) > longest {
				longest = len(bootSfxBank.pcm[k])
			}
		}
		bootSfxBank.scratch = make([]byte, longest)
		bootSfxBank.rate = rate
	})
}

// bootSfxMaxQueued is how much audio may already be waiting before a UI sound
// is dropped instead of queued: a held D-pad must not build a backlog that
// keeps ticking after the key is let go.
const bootSfxMaxQueued = 150 * time.Millisecond

// bootSfxPlay queues one UI sound at the user's volume.
func bootSfxPlay(kind bootSfxKind) {
	if kind < 0 || kind >= bootSfxKindCount || !bootSfxEnabled() {
		return
	}
	// A ringing alarm owns the speaker: its own snooze/dismiss sounds play once
	// the ring has stopped.
	if alarmFiringActive() {
		return
	}
	bootSfxPrepare()
	if !audioAvailable {
		return
	}
	pcm := bootSfxBank.pcm[kind]
	if len(pcm) == 0 {
		return
	}
	rate := bootSfxBank.rate
	audioMutex.Lock()
	defer audioMutex.Unlock()
	if limit := uint32(float64(rate) * 2 * bootSfxMaxQueued.Seconds()); sdl.GetQueuedAudioSize(audioDev) > limit {
		return
	}
	data := pcm
	if level := bootSfxVolume(); level < 100 {
		data = bootSfxBank.scratch[:len(pcm)]
		gain := float64(level) / 100
		// Perceived loudness is roughly logarithmic: squaring the slider keeps
		// its lower half useful instead of all of it sounding the same.
		gain *= gain
		for i := 0; i+1 < len(pcm); i += 2 {
			s := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
			v := int16(math.Round(float64(s) * gain))
			data[i] = byte(v)
			data[i+1] = byte(uint16(v) >> 8)
		}
	}
	// Feed the LED sync with the same samples the speaker is about to play.
	publishAudioLevel(data, int(rate), 1)
	_ = sdl.QueueAudio(audioDev, data)
}

// Each Play* helper also fires the matching vibration pulse from haptics.go.
// These six functions are where every button, touch and toast already lands,
// so feedback stays consistent without adding haptics at call sites. The
// vibration follows its own switch, not the UI-sound one.

// PlayNavSound plays on focus movement (D-pad / arrows).
func PlayNavSound() {
	bootSfxPlay(bootSfxNav)
	playHaptic(hapticNavPulse)
}

// PlayActivateSound plays when an action is triggered (A / Enter / click).
func PlayActivateSound() {
	bootSfxPlay(bootSfxActivate)
	playHaptic(hapticActivatePulse)
}

// PlayBackSound plays when navigating back (B / Escape).
func PlayBackSound() {
	bootSfxPlay(bootSfxBack)
	playHaptic(hapticBackPulse)
}

// PlayToggleSound plays when a toggle switch flips.
func PlayToggleSound() {
	bootSfxPlay(bootSfxToggle)
	playHaptic(hapticTogglePulse)
}

// PlaySuccessSound plays for success / scene-switch toasts.
func PlaySuccessSound() {
	bootSfxPlay(bootSfxSuccess)
	playHaptic(hapticSuccessPulse)
}

// PlayErrorSound plays for error and warning toasts.
func PlayErrorSound() {
	bootSfxPlay(bootSfxError)
	playHaptic(hapticErrorPulse)
}

// --- Video playback audio ---
//
// The embedded video player decodes audio with ffmpeg to s16le stereo at
// the sample rate the SDL device actually obtained. A dedicated output
// device matches that format exactly so no resampling is needed; UI beeps
// keep using the mono device above.

// videoAudioRate is the actual sample rate the device was opened at.
// ffmpeg decodes at this exact rate so there is zero pitch drift.
var videoAudioRate int32

// videoMaxQueuedBytes returns ~1 s of stereo S16 at the actual device rate.
func videoMaxQueuedBytes() int {
	rate := GetVideoAudioRate()
	return int(rate) * 2 * 2 // bytes/sec = rate × channels × bytes_per_sample
}

var (
	videoAudioInitOnce sync.Once
	videoAudioDev      sdl.AudioDeviceID
	videoAudioOK       bool
	videoAudioMutex    sync.Mutex
)

// initVideoAudioDevice lazily opens a stereo S16 output device for decoded
// video PCM. It tries 48 kHz first (standard for video content and the
// native rate on most TSP hardware), then falls back to 44.1 kHz. The
// obtained rate is stored in videoAudioRate so ffmpeg can match it exactly.
func initVideoAudioDevice() {
	// Preferred rates in quality order.
	for _, rate := range []int32{48000, 44100, 22050} {
		desired := sdl.AudioSpec{
			Freq:     rate,
			Format:   sdl.AUDIO_S16SYS,
			Channels: 2,
			Samples:  4096, // ~85 ms at 48 kHz — smooth on low-end ARM64
		}
		var obtained sdl.AudioSpec
		id, err := sdl.OpenAudioDevice("", false, &desired, &obtained, 0)
		if err != nil || id == 0 {
			continue
		}
		sdl.PauseAudioDevice(id, false)
		videoAudioDev = id
		videoAudioOK = true
		videoAudioRate = obtained.Freq
		logDebug("audio", "video device opened: %d Hz stereo S16 (wanted %d)", obtained.Freq, rate)
		return
	}
	// Last resort: allow SDL to resample. Audio quality may suffer.
	desired := sdl.AudioSpec{
		Freq:     48000,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 2,
		Samples:  4096,
	}
	var obtained sdl.AudioSpec
	id, err := sdl.OpenAudioDevice("", false, &desired, &obtained,
		sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || id == 0 {
		logWarn("audio", "failed to open any video audio device")
		return
	}
	sdl.PauseAudioDevice(id, false)
	videoAudioDev = id
	videoAudioOK = true
	videoAudioRate = obtained.Freq
	logDebug("audio", "video device (fallback): %d Hz stereo S16 (wanted %d) — resampling active", obtained.Freq, desired.Freq)
}

// GetVideoAudioRate returns the actual sample rate the video audio device
// is running at. The caller should pass this to ffmpeg -ar so the decoded
// PCM matches the hardware exactly.
func GetVideoAudioRate() int32 {
	videoAudioInitOnce.Do(initVideoAudioDevice)
	if videoAudioRate <= 0 {
		return 44100 // safe default if device failed to open
	}
	return videoAudioRate
}

// QueueVideoAudio feeds decoded stereo S16 PCM to the video audio device,
// applying the player volume. It blocks briefly while the queue is full so
// the decoder paces itself instead of dropping samples; this runs on the
// decoder goroutine, never on the render loop.
func QueueVideoAudio(pcm []byte, volume float64) {
	if len(pcm) == 0 {
		return
	}
	videoAudioInitOnce.Do(initVideoAudioDevice)
	if !videoAudioOK {
		return
	}
	if volume < 0 {
		volume = 0
	}
	if volume > 1 {
		volume = 1
	}
	// A muted buffer is still queued (as silence, below): the wait for room is
	// what paces both decoders to real time. Returning early at volume 0 let
	// music race through whole playlists in silence and an audiobook "finish"
	// — losing its resume point — while muted.
	maxQ := videoMaxQueuedBytes()
	deadline := time.Now().Add(2 * time.Second)
	for {
		videoAudioMutex.Lock()
		if int(sdl.GetQueuedAudioSize(videoAudioDev)) <= maxQ {
			break // keep the lock for the queueing below
		}
		videoAudioMutex.Unlock()
		// The lock is not held while waiting: pause, resume and clear take it
		// from the UI thread. A paused device never drains, and a wait that
		// held the lock froze the whole UI on the next pause, stop or skip. A
		// device still full at the deadline (paused, stalled) gets this buffer
		// dropped instead; the decoders check their session and pause state
		// between buffers.
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer videoAudioMutex.Unlock()
	// Spatial audio (spatial.go) runs here, under the lock. It applies the
	// volume itself, before its limiter, so the loop below then has nothing to
	// do; off, pcm comes back unprocessed, cut to whole frames (a read that
	// ended mid-frame keeps its last bytes for the next call, which also keeps
	// the loop below from pairing bytes across samples).
	pcm, spatialScaled := spatialQueueHook(pcm, videoAudioRate, volume)
	if spatialScaled {
		volume = 1
	}
	if len(pcm) == 0 {
		return
	}
	if volume < 0.999 {
		n := len(pcm) / 2
		for i := 0; i < n; i++ {
			s := int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8)
			v := int32(float64(s) * volume)
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			pcm[i*2] = byte(uint16(int16(v)))
			pcm[i*2+1] = byte(uint16(int16(v)) >> 8)
		}
	}
	// Video and music share this path, so one publish covers both. The buffer is
	// stereo S16 at the device's rate; the analyser reads at most 2048 frames.
	publishAudioLevel(pcm, int(videoAudioRate), 2)
	_ = sdl.QueueAudio(videoAudioDev, pcm)
}

// PauseVideoAudio pauses/resumes playback of the queued video audio device.
func PauseVideoAudio(paused bool) {
	videoAudioInitOnce.Do(initVideoAudioDevice)
	if !videoAudioOK {
		return
	}
	videoAudioMutex.Lock()
	defer videoAudioMutex.Unlock()
	sdl.PauseAudioDevice(videoAudioDev, paused)
}

// ClearVideoAudioQueue discards any still-queued video PCM (used on stop and
// seek so old audio never bleeds into the next playback position).
func ClearVideoAudioQueue() {
	videoAudioInitOnce.Do(initVideoAudioDevice)
	if !videoAudioOK {
		return
	}
	videoAudioMutex.Lock()
	defer videoAudioMutex.Unlock()
	sdl.ClearQueuedAudio(videoAudioDev)
	// A new stream starts: no reverb tail or held byte from the old one.
	spatialResetLocked()
}
