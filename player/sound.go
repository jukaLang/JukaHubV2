package main

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// --- UI sound effects ---
//
// All UI sounds are short sine beeps synthesized in memory and queued to a
// plain SDL2 audio device via SDL_QueueAudio. This avoids any dependency on
// SDL_mixer while still giving the handheld immediate aural feedback for
// navigation, activation, and errors.
//
// Every function here is non-blocking and fails silently when no audio
// device is available, so the app never hangs or crashes on mute hardware.

var (
	audioInitOnce   sync.Once
	audioDev        sdl.AudioDeviceID
	audioSampleRate int32 = 22050
	audioAvailable  bool
	audioMutex      sync.Mutex
)

// initAudioDevice lazily opens the default output device. It runs once and
// only ever touches SDL audio APIs; if opening fails, audioAvailable stays
// false and all subsequent calls are no-ops.
func initAudioDevice() {
	desired := sdl.AudioSpec{
		Freq:     audioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  512,
	}
	var obtained sdl.AudioSpec
	id, err := sdl.OpenAudioDevice("", false, &desired, &obtained,
		sdl.AUDIO_ALLOW_FREQUENCY_CHANGE|sdl.AUDIO_ALLOW_CHANNELS_CHANGE)
	if err != nil || id == 0 {
		return
	}
	sdl.PauseAudioDevice(id, false)
	audioDev = id
	if obtained.Freq > 0 {
		audioSampleRate = obtained.Freq
	}
	audioAvailable = true
}

// playTone synthesizes a short sine wave with a soft attack/release envelope
// and queues it on the audio device. Rapid repeated calls are throttled so a
// held D-pad direction cannot pile up a long queue.
func playTone(freq float64, ms int, volume float64) {
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable || freq <= 0 || ms <= 0 {
		return
	}
	audioMutex.Lock()
	defer audioMutex.Unlock()
	// Keep at most ~0.5s of audio queued; drop effects when the queue is
	// already full (e.g. navigation held down).
	if sdl.GetQueuedAudioSize(audioDev) > uint32(audioSampleRate)*2 {
		return
	}
	n := int(float64(audioSampleRate) * float64(ms) / 1000.0)
	if n <= 0 {
		return
	}
	attack := n / 8
	if attack < 2 {
		attack = 2
	}
	release := n / 10
	if release < 2 {
		release = 2
	}
	data := make([]byte, n*2)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(audioSampleRate)
		env := 1.0
		if i < attack {
			env = float64(i) / float64(attack)
		} else if i > n-release {
			env = float64(n-i) / float64(release)
		}
		v := math.Sin(2*math.Pi*freq*t) * env * volume * 0.5
		s := int16(v * 32767.0)
		data[i*2] = byte(s)
		data[i*2+1] = byte(s >> 8)
	}
	_ = sdl.QueueAudio(audioDev, data)
}

// PlayNavSound plays on focus movement (D-pad / arrows).
func PlayNavSound() {
	playTone(880, 28, 0.28)
}

// PlayActivateSound plays when an action is triggered (A / Enter / click).
func PlayActivateSound() {
	playTone(1046, 55, 0.45)
}

// PlayBackSound plays when navigating back (B / Escape).
func PlayBackSound() {
	playTone(659, 45, 0.35)
}

// PlayToggleSound plays when a toggle switch flips.
func PlayToggleSound() {
	playTone(1318, 40, 0.35)
}

// PlaySuccessSound plays for success / scene-switch toasts.
func PlaySuccessSound() {
	playTone(1568, 60, 0.4)
}

// PlayErrorSound plays for error and warning toasts.
func PlayErrorSound() {
	playTone(233, 140, 0.5)
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
		log.Printf("[AUDIO] video device opened: %d Hz stereo S16 (wanted %d)", obtained.Freq, rate)
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
		log.Printf("[AUDIO] failed to open any video audio device")
		return
	}
	sdl.PauseAudioDevice(id, false)
	videoAudioDev = id
	videoAudioOK = true
	videoAudioRate = obtained.Freq
	log.Printf("[AUDIO] video device (fallback): %d Hz stereo S16 (wanted %d) — resampling active", obtained.Freq, desired.Freq)
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
	if volume < 0.01 {
		return
	}
	videoAudioMutex.Lock()
	defer videoAudioMutex.Unlock()
	maxQ := videoMaxQueuedBytes()
	for int(sdl.GetQueuedAudioSize(videoAudioDev)) > maxQ {
		time.Sleep(5 * time.Millisecond)
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
}
