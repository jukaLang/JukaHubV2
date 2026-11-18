package main

// usbdac_bitperfect.go — bit-perfect playback on a USB DAC.
//
// "Bit-perfect DAC output" (Audio settings) used to mean only that ffmpeg ran
// no filter: the samples were still resampled to the output's 48 kHz, cut to
// 16 bits and scaled by the volume on the way. With a USB DAC routed
// (usbdac.go) the switch now means what DAP users expect: a local lossless
// track plays at its own rate and bit depth, unscaled, straight into the DAC.
//
// How: the decoder probes the track (ffprobe: rate, bits, channels) and asks
// the DAC's stream0 (the formats and rates its USB altsets list) whether it
// takes that rate. If so, it borrows the DAC from the route: the route's mixer
// device on the DAC closes, every JukaHub output is held back like for a game
// (outAudio.exclusive — UI sounds are muted while the music owns the DAC), and
// the decoder opens its own SDL device on
//
//   - "hw:CARD=<id>,DEV=0" when the DAC lists the decoded sample format
//     itself (S16_LE for a 16-bit track, S32_LE for 24/32-bit), so not even
//     ALSA touches the samples, or
//   - "plughw:CARD=<id>,DEV=0" at the same rate when the DAC takes only a
//     wider container (S24_3LE for 24-bit, any 24/32-bit one for 16-bit):
//     plug then only repacks the bits — the rate is the DAC's own, so nothing
//     is resampled, and a 24-bit sample in a 32-bit word loses only zeros.
//
// The device must open exactly as asked (rate, format, two channels); SDL is
// given every change allowed and the answer is compared, so it can never
// convert behind JukaHub's back. There is no volume scaling: the volume is the
// DAC's (its own buttons, or the headphones'). Tracks the DAC cannot take at
// their own rate, DSD (decoded to float), lossy files, streams, and anything
// but two channels (ffmpeg would spread mono at -3 dB) play through the normal
// route at 48 kHz, as before.
//
// The device stays open between tracks of the same rate and format, so an
// album plays gapless; it is let go when the next track needs another rate,
// when nothing has used it for dacExIdle, after dacExPauseHold of pause, and
// at once when a game starts, a video starts, the route moves, or the DAC is
// pulled out (the decoder then restarts on the normal route at its place).
// A device that failed is not tried again for dacExFailHold.
//
// Locks: outRouteMu, then dacEx.mu or outAudio.mu, never the other way. The
// decoder takes only dacEx.mu, around each SDL call on the device, so the
// route never closes it under a queue call; a generation number keeps a stale
// decoder off a device SDL reopened under the same id.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// dacExIdle is how long the device stays open with no track on it, so
	// the next track (gapless) or a skip finds it.
	dacExIdle = 1500 * time.Millisecond
	// dacExPauseHold lets go of the DAC while the music is paused, so UI
	// sounds come back; Play takes it again.
	dacExPauseHold = 5 * time.Second
	// dacExFailHold keeps a device that failed from being retried at once.
	dacExFailHold = time.Minute
	// dacExLead is how much the decoder keeps queued on the device: enough
	// to cover the next track's ffprobe and ffmpeg start, so tracks meet
	// without a gap.
	dacExLead = time.Second
)

// dacEx is the bit-perfect device.
var dacEx struct {
	mu       sync.Mutex
	dev      sdl.AudioDeviceID
	gen      int // bumps on every open, so a stale holder of an id is told apart
	pcm      string
	rate     int32
	format   sdl.AudioFormat
	users    int
	failPCM  string
	failTill time.Time
	label    string // "DAC 96 kHz / 24-bit", for Now Playing
}

// dacBitPerfect is one track's bit-perfect plan.
type dacBitPerfect struct {
	route  string // the route's PCM on the DAC (plughw)
	pcm    string // what the device opens
	rate   int32
	format sdl.AudioFormat
	ffFmt  string // ffmpeg's -f / -acodec: s16le/pcm_s16le or s32le/pcm_s32le
	ffCod  string
	frame  int // bytes per stereo frame
	label  string
}

// musicTrackFormat is what ffprobe says about a track's first audio stream.
type musicTrackFormat struct {
	codec    string
	rate     int
	bits     int // 0 when float or unknown
	channels int
	float    bool
}

// parseMusicTrackFormat reads ffprobe's default-format output
// (key=value lines) for the fields dacBitPerfectPlan needs.
func parseMusicTrackFormat(out string) musicTrackFormat {
	var f musicTrackFormat
	raw, coded := 0, 0
	sampleFmt := ""
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(val))
		switch key {
		case "codec_name":
			if f.codec == "" {
				f.codec = strings.TrimSpace(val)
			}
		case "sample_rate":
			if f.rate == 0 {
				f.rate = n
			}
		case "channels":
			if f.channels == 0 {
				f.channels = n
			}
		case "bits_per_raw_sample":
			if raw == 0 {
				raw = n
			}
		case "bits_per_sample":
			if coded == 0 {
				coded = n
			}
		case "sample_fmt":
			if sampleFmt == "" {
				sampleFmt = strings.TrimSpace(val)
			}
		}
	}
	switch strings.TrimSuffix(sampleFmt, "p") {
	case "flt", "dbl":
		f.float = true
	case "s16":
		f.bits = 16
	case "s32":
		f.bits = 32
	case "u8":
		f.bits = 8
	}
	if !f.float {
		// FLAC/ALAC 24-bit decode to s32 with 24 raw bits; WAV/AIFF name
		// the coded width.
		if raw > 0 {
			f.bits = raw
		} else if coded > 0 {
			f.bits = coded
		}
	}
	return f
}

// musicLosslessCodec reports whether a codec carries exact samples.
func musicLosslessCodec(codec string) bool {
	switch {
	case codec == "flac", codec == "alac", codec == "wavpack", codec == "tta", codec == "ape", codec == "mlp", codec == "truehd":
		return true
	case strings.HasPrefix(codec, "pcm_s"), strings.HasPrefix(codec, "pcm_u"):
		return true
	}
	return false
}

// musicProbeTrackFormat runs ffprobe on a local track (bounded).
func musicProbeTrackFormat(path string) (musicTrackFormat, error) {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	probe := getToolPath("ffprobe", cfg)
	if probe == "" {
		return musicTrackFormat{}, errors.New("no ffprobe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := P().CommandContext(ctx, probe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,sample_fmt,bits_per_raw_sample,bits_per_sample,channels",
		"-of", "default=noprint_wrappers=1", path)
	out, err := cmd.Output()
	if err != nil {
		return musicTrackFormat{}, err
	}
	return parseMusicTrackFormat(string(out)), nil
}

// dacBitPerfectPlan decides whether path plays bit-perfect on the routed DAC.
// Decoder goroutine only: it runs ffprobe.
func dacBitPerfectPlan(path string) (dacBitPerfect, bool) {
	var bp dacBitPerfect
	if !musicEQ.bitPerfect || isHTTPMediaURL(path) || playerPhase() != "idle" {
		return bp, false
	}
	card := usbDACCurrent()
	if card.id == "" {
		return bp, false
	}
	route := usbDACPCMFor(card.id)
	outAudio.mu.Lock()
	onDAC := outAudio.pcm == route && !outAudio.suspended && outAudio.gameMix == ""
	outAudio.mu.Unlock()
	if !onDAC {
		return bp, false
	}
	dacEx.mu.Lock()
	held := dacEx.failPCM == route && time.Now().Before(dacEx.failTill)
	dacEx.mu.Unlock()
	if held {
		return bp, false
	}
	f, err := musicProbeTrackFormat(path)
	// Stereo only: ffmpeg's -ac 2 spreads a mono track at -3 dB (swresample's
	// centre level), which is a scaled copy, not the samples.
	if err != nil || f.float || f.bits <= 0 || f.bits > 32 || f.rate <= 0 || f.channels != 2 || !musicLosslessCodec(f.codec) {
		return bp, false
	}
	bp.route, bp.rate = route, int32(f.rate)
	want := []string{"S16_LE"}
	wider := []string{"S24_3LE", "S24_LE", "S32_LE"}
	bp.format, bp.ffFmt, bp.ffCod, bp.frame = sdl.AUDIO_S16LSB, "s16le", "pcm_s16le", 4
	if f.bits > 16 {
		want, wider = []string{"S32_LE"}, []string{"S24_3LE", "S24_LE"}
		bp.format, bp.ffFmt, bp.ffCod, bp.frame = sdl.AUDIO_S32LSB, "s32le", "pcm_s32le", 8
		if f.bits > 24 {
			wider = nil // 32-bit samples fit only a 32-bit container
		}
	}
	pick := func(formats []string) bool {
		for _, a := range card.alts {
			if a.channels != 0 && a.channels < 2 {
				continue
			}
			for _, w := range formats {
				if a.format == w && a.playsRate(f.rate) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case pick(want):
		bp.pcm = "hw:CARD=" + card.id + ",DEV=0"
	case pick(wider):
		bp.pcm = route // plug repacks the bits; the rate is the DAC's own
	default:
		return bp, false
	}
	bp.label = fmt.Sprintf("%s %d/%s → DAC %s kHz", strings.ToUpper(musicCodecLabel(f.codec)), f.bits, musicKHz(f.rate), musicKHz(f.rate))
	return bp, true
}

// musicCodecLabel is a codec's short name for Now Playing.
func musicCodecLabel(codec string) string {
	switch {
	case strings.HasPrefix(codec, "pcm_"):
		return "pcm"
	case codec == "":
		return "audio"
	}
	return codec
}

// musicKHz is a rate in kHz the way players print it: 44.1, 96, 192.
func musicKHz(rate int) string {
	if rate%1000 == 0 {
		return strconv.Itoa(rate / 1000)
	}
	return strconv.FormatFloat(float64(rate)/1000, 'f', 1, 64)
}

// ── The device ─────────────────────────────────────────────────────────────

// dacExclusiveAcquire opens (or keeps) the bit-perfect device for bp and
// returns it with its generation. The DAC is taken from the route's mixer.
func dacExclusiveAcquire(bp dacBitPerfect) (sdl.AudioDeviceID, int, error) {
	dacEx.mu.Lock()
	if dacEx.dev != 0 && dacEx.pcm == bp.pcm && dacEx.rate == bp.rate && dacEx.format == bp.format {
		dacEx.users++
		dacEx.label = bp.label
		id, gen := dacEx.dev, dacEx.gen
		dacEx.mu.Unlock()
		return id, gen, nil
	}
	dacEx.mu.Unlock()

	outRouteMu.Lock()
	defer outRouteMu.Unlock()
	outAudio.mu.Lock()
	if outAudio.suspended || outAudio.pcm != bp.route || outAudio.gameMix != "" {
		outAudio.mu.Unlock()
		return 0, 0, errors.New("the route left the DAC")
	}
	var mixDev sdl.AudioDeviceID
	if !outAudio.exclusive {
		if outAudio.mixStop != nil {
			close(outAudio.mixStop)
			outAudio.mixStop = nil
		}
		mixDev = outAudio.mixDev
		outAudio.mixDev = 0
		outAudio.exclusive = true
		for _, d := range outAudio.devs {
			d.fifo, d.head, d.phase = d.fifo[:0], 0, 0
		}
	}
	outAudio.mu.Unlock()
	if mixDev != 0 {
		sdl.CloseAudioDevice(mixDev)
	}
	dacExclusiveDrop() // another rate or format: the old device goes first

	want := sdl.AudioSpec{Freq: bp.rate, Format: bp.format, Channels: 2, Samples: 4096}
	var got sdl.AudioSpec
	id, err := outOpenReal(&want, &got, sdl.AUDIO_ALLOW_ANY_CHANGE, bp.pcm)
	if err == nil && (got.Freq != bp.rate || got.Format != bp.format || got.Channels != 2) {
		sdl.CloseAudioDevice(id)
		err = fmt.Errorf("the DAC opened at %d Hz, format %#x, %d channels instead", got.Freq, uint16(got.Format), got.Channels)
	}
	if err != nil {
		logInfo("audio", "bit-perfect: %s at %d Hz would not open: %v", bp.pcm, bp.rate, err)
		dacEx.mu.Lock()
		dacEx.failPCM, dacEx.failTill = bp.route, time.Now().Add(dacExFailHold)
		dacEx.mu.Unlock()
		outRestoreAfterExclusive()
		return 0, 0, err
	}
	sdl.PauseAudioDevice(id, false)
	dacEx.mu.Lock()
	dacEx.dev, dacEx.pcm, dacEx.rate, dacEx.format = id, bp.pcm, bp.rate, bp.format
	dacEx.gen++
	dacEx.users = 1
	dacEx.label = bp.label
	gen := dacEx.gen
	dacEx.mu.Unlock()
	logInfo("audio", "bit-perfect: %s on %s", bp.label, bp.pcm)
	return id, gen, nil
}

// dacExclusiveQueue queues data on the device of generation gen.
func dacExclusiveQueue(gen int, data []byte) error {
	dacEx.mu.Lock()
	defer dacEx.mu.Unlock()
	if dacEx.dev == 0 || dacEx.gen != gen {
		return errors.New("the DAC was let go")
	}
	if sdl.GetAudioDeviceStatus(dacEx.dev) == sdl.AUDIO_STOPPED {
		return errors.New("the DAC stopped")
	}
	return sdl.QueueAudio(dacEx.dev, data)
}

// dacExclusiveQueued is what the device of generation gen still has queued;
// ok false once it was let go.
func dacExclusiveQueued(gen int) (uint32, bool) {
	dacEx.mu.Lock()
	defer dacEx.mu.Unlock()
	if dacEx.dev == 0 || dacEx.gen != gen {
		return 0, false
	}
	return sdl.GetQueuedAudioSize(dacEx.dev), true
}

// dacExclusiveLeadBytes is what the device still has queued, in the 16-bit
// stereo bytes the music's position counts (a gapless start, musicplayer.go).
func dacExclusiveLeadBytes() int64 {
	dacEx.mu.Lock()
	defer dacEx.mu.Unlock()
	if dacEx.dev == 0 {
		return 0
	}
	frame := uint32(4)
	if dacEx.format == sdl.AUDIO_S32LSB {
		frame = 8
	}
	return int64(sdl.GetQueuedAudioSize(dacEx.dev) / frame * 4)
}

// dacExclusiveClear drops what the device has queued (stop, seek, pause).
func dacExclusiveClear() {
	dacEx.mu.Lock()
	defer dacEx.mu.Unlock()
	if dacEx.dev != 0 {
		sdl.ClearQueuedAudio(dacEx.dev)
	}
}

// dacExclusiveDone is a decoder leaving the device; the last one out lets go
// of it after dacExIdle unless the next track takes it first.
func dacExclusiveDone(gen int) {
	dacEx.mu.Lock()
	if dacEx.gen != gen || dacEx.users <= 0 {
		dacEx.mu.Unlock()
		return
	}
	dacEx.users--
	idle := dacEx.users == 0
	dacEx.mu.Unlock()
	if idle {
		time.AfterFunc(dacExIdle, func() { dacExclusiveReleaseIdle(gen) })
	}
}

// dacExclusiveReleaseIdle lets go of the device of generation gen when still
// nobody uses it. The check and the hand-back are one step under dacEx.mu, so
// a next track that took the device a moment ago (users 1) keeps it.
func dacExclusiveReleaseIdle(gen int) {
	outRouteMu.Lock()
	defer outRouteMu.Unlock()
	dacEx.mu.Lock()
	id := dacEx.dev
	idle := id != 0 && dacEx.gen == gen && dacEx.users == 0
	if idle {
		dacEx.dev, dacEx.label = 0, ""
	}
	dacEx.mu.Unlock()
	if !idle {
		return
	}
	sdl.CloseAudioDevice(id)
	outRestoreAfterExclusive()
}

// dacExclusiveYield lets go of the device now, whoever uses it (they find
// out at their next queue), and gives the outputs their DAC route back. Not
// for the render thread: it closes and opens ALSA devices.
func dacExclusiveYield() {
	outRouteMu.Lock()
	dacExclusiveDrop()
	outRestoreAfterExclusive()
	outRouteMu.Unlock()
}

// dacExclusiveYieldIfHeld is dacExclusiveYield when the device (or its hold
// on the outputs) is there: a decoder about to play the normal way must not
// queue into outputs that are held back, where nothing paces it.
func dacExclusiveYieldIfHeld() {
	dacEx.mu.Lock()
	open := dacEx.dev != 0
	dacEx.mu.Unlock()
	outAudio.mu.Lock()
	held := outAudio.exclusive
	outAudio.mu.Unlock()
	if open || held {
		dacExclusiveYield()
	}
}

// dacExclusiveDrop closes the device (the caller holds outRouteMu).
func dacExclusiveDrop() {
	dacEx.mu.Lock()
	id := dacEx.dev
	dacEx.dev, dacEx.users, dacEx.label = 0, 0, ""
	dacEx.mu.Unlock()
	if id != 0 {
		sdl.CloseAudioDevice(id)
	}
}

// dacExclusiveFailed holds the DAC's bit-perfect path back after its device
// stopped mid-track.
func dacExclusiveFailed(route string) {
	dacEx.mu.Lock()
	dacEx.failPCM, dacEx.failTill = route, time.Now().Add(dacExFailHold)
	dacEx.mu.Unlock()
}

// dacExclusiveLabel is the Now Playing line while bit-perfect plays.
func dacExclusiveLabel() string {
	dacEx.mu.Lock()
	defer dacEx.mu.Unlock()
	if dacEx.dev == 0 || dacEx.users == 0 {
		return ""
	}
	return dacEx.label
}

// musicOutputLabel is Now Playing's output note: the bit-perfect line, or the
// DAC route's own rate when the music is mixed there; "" on the speaker or a
// headset. Cheap: two short locks, no I/O (render thread).
func musicOutputLabel() string {
	if l := dacExclusiveLabel(); l != "" {
		return l
	}
	outAudio.mu.Lock()
	pcm, rate := outAudio.pcm, outAudio.mixRate
	outAudio.mu.Unlock()
	if usbDACPCMIs(pcm) && rate > 0 {
		return "DAC " + musicKHz(int(rate)) + " kHz (mixed)"
	}
	return ""
}

// outRestoreAfterExclusive gives the outputs their route on the DAC back after
// the bit-perfect device closed: the mixer device reopens on the route's PCM,
// or every output goes back to the speaker when it will not. The caller holds
// outRouteMu.
func outRestoreAfterExclusive() {
	outAudio.mu.Lock()
	if !outAudio.exclusive {
		outAudio.mu.Unlock()
		return
	}
	outAudio.exclusive = false
	pcm := outAudio.pcm
	outAudio.pcm = ""
	outAudio.mu.Unlock()
	if pcm != "" {
		want := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16SYS, Channels: 2, Samples: 1024}
		var got sdl.AudioSpec
		id, err := outOpenReal(&want, &got, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE, pcm)
		if err == nil {
			outAudio.mu.Lock()
			if !outAudio.suspended && outAudio.mixDev == 0 && outAudio.pcm == "" && !outAudio.exclusive {
				outToHeadsetLocked(pcm, id, got.Freq)
				outAudio.mu.Unlock()
				return
			}
			outAudio.mu.Unlock()
			sdl.CloseAudioDevice(id)
			return
		}
		logInfo("audio", "the DAC route would not reopen after bit-perfect play: %v", err)
	}
	outToSpeaker()
	audioRouteKick()
}

// ── The decoder ────────────────────────────────────────────────────────────

// musicDecodeBitPerfect is musicDecode on the bit-perfect device: ffmpeg
// decodes to the track's own rate in a 16- or 32-bit word, nothing scales or
// filters the samples, and the decoder paces itself on the device's queue. It
// reports false when the device could not be had, so the caller plays the
// normal way.
func musicDecodeBitPerfect(ffmpegPath, path string, startSec float64, gen int, bp dacBitPerfect) bool {
	_, devGen, err := dacExclusiveAcquire(bp)
	if err != nil {
		return false
	}
	args := []string{"-nostdin", "-v", "error"}
	if startSec > 0.05 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', 3, 64))
	}
	args = append(args, "-i", path, "-vn", "-f", bp.ffFmt, "-acodec", bp.ffCod, "-ac", "2", "pipe:1")
	cmd := execCommand(ffmpegPath, args...)
	if cmd == nil {
		dacExclusiveDone(devGen)
		return false
	}
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		dacExclusiveDone(devGen)
		logError("musicplayer", "bit-perfect: start ffmpeg: %v", err)
		return false
	}
	music.mu.Lock()
	if gen != music.gen {
		music.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		dacExclusiveDone(devGen)
		return true
	}
	music.proc = cmd
	music.rate = bp.rate
	music.mu.Unlock()

	maxQ := uint32(float64(bp.rate)*dacExLead.Seconds()) * uint32(bp.frame)
	buf := make([]byte, 32*1024)
	carry := 0 // bytes of a partial frame held over from the last read
	lost := false
	var pausedAt time.Time
	var savePos, saveDur float64
loop:
	for {
		n, readErr := stdout.Read(buf[carry:])
		n += carry
		whole := n / bp.frame * bp.frame
		if whole > 0 {
			for {
				music.mu.Lock()
				stale := gen != music.gen || !music.playing
				paused := music.paused
				music.mu.Unlock()
				if stale {
					break loop
				}
				if paused {
					if pausedAt.IsZero() {
						pausedAt = time.Now()
					} else if time.Since(pausedAt) > dacExPauseHold {
						// A long pause gives the DAC (and UI sounds) back;
						// Play restarts on it at the same place.
						if _, ok := dacExclusiveQueued(devGen); ok {
							dacExclusiveYield()
						}
					}
					time.Sleep(20 * time.Millisecond)
					continue
				}
				pausedAt = time.Time{}
				q, ok := dacExclusiveQueued(devGen)
				if !ok {
					lost = true
					break loop
				}
				if q <= maxQ {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := dacExclusiveQueue(devGen, buf[:whole]); err != nil {
				lost = true
				if err.Error() == "the DAC stopped" {
					dacExclusiveFailed(bp.route)
				}
				break loop
			}
			if bp.format == sdl.AUDIO_S16LSB {
				publishAudioLevel(buf[:whole], int(bp.rate), 2)
			}
			music.mu.Lock()
			if gen == music.gen {
				// Counted as 16-bit stereo bytes, the unit musicMediaElapsed
				// takes.
				music.pcmBytes += int64(whole / bp.frame * 4)
				heard := music.pcmBytes - music.pcmLead
				if heard < 0 {
					heard = 0
				}
				elapsed := musicMediaElapsed(music.pcmStart, heard, bp.rate, 1)
				if music.duration <= 0 || elapsed < music.duration {
					music.position = elapsed
				} else {
					music.position = music.duration
				}
				if time.Since(music.lastSave) > 10*time.Second {
					music.lastSave = time.Now()
					savePos, saveDur = elapsed, music.duration
				}
			}
			music.mu.Unlock()
			if savePos > 0 {
				audioRememberPosition(path, savePos, saveDur)
				savePos = 0
			}
		}
		carry = copy(buf, buf[whole:n])
		if readErr != nil {
			break
		}
	}
	if lost {
		// Let go of the DAC (a game, a video, an unplug or a long pause took
		// it) and carry on the normal way from here, unless the music moved
		// on meanwhile.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		dacExclusiveDone(devGen)
		music.mu.Lock()
		resume := gen == music.gen && music.playing
		pos := music.position
		if resume {
			music.proc = nil
		}
		music.mu.Unlock()
		if resume {
			go musicStartDecode(path, pos)
		}
		return true
	}
	musicDecodeFinish(cmd, path, gen)
	dacExclusiveDone(devGen)
	return true
}
