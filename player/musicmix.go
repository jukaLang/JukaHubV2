package main

// musicmix.go — MED-7: crossfade between tracks and ReplayGain.
//
// The signal work is pure and deterministic so it can be checked without a
// sound card: musicCrossfadeMixTo sums two S16LE stereo buffers under an
// equal-power envelope, and the ReplayGain helpers turn ffprobe's tags into a
// gain, cap it at the tagged peak and apply it to samples.
//
// Crossfade playback pre-decodes only the first N seconds of the next track,
// while the current track still plays. The decoder mixes those samples into
// the current track's last N seconds, then starts the next track at N seconds.
// Nothing is decoded twice and the overlap is sample-accurate.
//
// Both features are off on the bit-perfect path: musicDecode returns through
// musicDecodeBitPerfect before either is reached, and the pre-decode filters
// on the same route.

import (
	"encoding/binary"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	musicCrossfadeKey    = "musicCrossfadeSeconds"
	musicReplayGainKey   = "musicReplayGain"
	musicCrossfadeMax    = 10
	musicCrossfadeSilent = 0.05
)

// musicCrossfadeSeconds is the configured overlap, 0 (off) to 10 seconds.
func musicCrossfadeSeconds(config *Config) int {
	sec, err := strconv.Atoi(strings.TrimSpace(customStringValue(customMap(config), musicCrossfadeKey)))
	if err != nil || sec < 0 {
		return 0
	}
	if sec > musicCrossfadeMax {
		return musicCrossfadeMax
	}
	return sec
}

func musicSetCrossfadeSeconds(config *Config, sec int) {
	if config == nil {
		return
	}
	if sec < 0 {
		sec = 0
	}
	if sec > musicCrossfadeMax {
		sec = musicCrossfadeMax
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicCrossfadeKey] = strconv.Itoa(sec)
}

func musicReplayGainEnabled(config *Config) bool {
	return customBool(customMap(config), musicReplayGainKey, false)
}

// musicCrossfadeAvailable and musicReplayGainAvailable are the single gate
// for both features. The bit-perfect path also returns before musicDecode's
// normal body; keeping the check here makes the promise explicit and testable.
func musicCrossfadeAvailable(config *Config) bool {
	return !musicEQ.bitPerfect && musicCrossfadeSeconds(config) > 0
}

func musicReplayGainAvailable(config *Config) bool {
	return !musicEQ.bitPerfect && musicReplayGainEnabled(config)
}

func musicSetReplayGainEnabled(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicReplayGainKey] = boolString(on)
}

func customMap(config *Config) map[string]interface{} {
	if config == nil {
		return nil
	}
	return config.Variables.Custom
}

// musicCrossfadeFrames is the overlap length in S16LE stereo frames. A rate of
// zero is the player's default (musicDecode does the same).
func musicCrossfadeFrames(seconds int, rate int32) int {
	if seconds < 0 {
		seconds = 0
	}
	if seconds > musicCrossfadeMax {
		seconds = musicCrossfadeMax
	}
	if rate <= 0 {
		rate = 44100
	}
	return seconds * int(rate)
}

// musicCrossfadeGains returns the equal-power fade-out and fade-in gains at
// frame pos of total. Their squares sum to one, so the mix keeps its loudness
// through the join; at the ends one source is exactly silent.
func musicCrossfadeGains(pos, total int) (out, in float64) {
	if total <= 0 {
		return 1, 0
	}
	if pos <= 0 {
		return 1, 0
	}
	if pos >= total {
		return 0, 1
	}
	t := float64(pos) / float64(total)
	a := t * math.Pi / 2
	return math.Cos(a), math.Sin(a)
}

// musicCrossfadeMixTo writes cur and next mixed under the envelope into dst,
// which is allocated when it is too small. Both buffers are S16LE stereo;
// missing samples are silence. The result is clipped to int16, so a loud pair
// cannot wrap around.
func musicCrossfadeMixTo(dst, cur, next []byte, pos, total int) []byte {
	n := len(cur)
	if len(next) > n {
		n = len(next)
	}
	if n <= 0 {
		return dst[:0]
	}
	if cap(dst) < n {
		dst = make([]byte, n)
	} else {
		dst = dst[:n]
	}
	for i := 0; i+1 < n; i += 2 {
		outGain, inGain := musicCrossfadeGains(pos+i/2, total)
		a, b := int32(0), int32(0)
		if i+1 < len(cur) {
			a = int32(int16(binary.LittleEndian.Uint16(cur[i:]))) // sign through int16
		}
		if i+1 < len(next) {
			b = int32(int16(binary.LittleEndian.Uint16(next[i:])))
		}
		v := int32(math.Round(float64(a)*outGain + float64(b)*inGain))
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(dst[i:], uint16(int16(v)))
	}
	return dst
}

// musicReplayGainTags is what ffprobe's format tags say about one file.
type musicReplayGainTags struct {
	TrackGain, AlbumGain float64
	TrackPeak, AlbumPeak float64
	HasTrackGain         bool
	HasAlbumGain         bool
	HasTrackPeak         bool
	HasAlbumPeak         bool
}

// musicParseReplayGainTags reads the default ffprobe output
// (`TAG:REPLAYGAIN_TRACK_GAIN=-7.23 dB`, ...). Keys are case-insensitive and
// a trailing dB is optional.
func musicParseReplayGainTags(raw string) musicReplayGainTags {
	var tags musicReplayGainTags
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "TAG:")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.IndexAny(val, " \t"); i >= 0 {
			val = val[:i]
		}
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(val, "dB"), "DB"), 64)
		if err != nil {
			continue
		}
		switch key {
		case "REPLAYGAIN_TRACK_GAIN":
			tags.TrackGain, tags.HasTrackGain = v, true
		case "REPLAYGAIN_ALBUM_GAIN":
			tags.AlbumGain, tags.HasAlbumGain = v, true
		case "REPLAYGAIN_TRACK_PEAK":
			tags.TrackPeak, tags.HasTrackPeak = math.Abs(v), true
		case "REPLAYGAIN_ALBUM_PEAK":
			tags.AlbumPeak, tags.HasAlbumPeak = math.Abs(v), true
		}
	}
	return tags
}

// musicReplayGainLevel returns the effective linear gain and the peak it was
// checked against. A track tag wins over the album tag; a missing peak means
// the samples are only clipped, not gain-limited. ok is false when neither
// gain nor peak was tagged (the file plays unchanged).
func musicReplayGainLevel(tags musicReplayGainTags) (gain, peak float64, ok bool) {
	gain = 1
	switch {
	case tags.HasTrackGain:
		gain = math.Pow(10, tags.TrackGain/20)
		ok = true
	case tags.HasAlbumGain:
		gain = math.Pow(10, tags.AlbumGain/20)
		ok = true
	}
	switch {
	case tags.HasTrackPeak:
		peak = tags.TrackPeak
		ok = true
	case tags.HasAlbumPeak:
		peak = tags.AlbumPeak
		ok = true
	}
	if math.IsNaN(gain) || math.IsInf(gain, 0) || gain <= 0 {
		gain = 1
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak < 0 {
		peak = 0
	}
	if peak > 0 && gain*peak > 1 {
		// Peak limiting: lower the whole file rather than clip it.
		gain = 1 / peak
	}
	return gain, peak, ok
}

// musicReplayGainApply scales S16LE stereo samples in place and clips the
// result. A gain of exactly one is left untouched, so the common no-tag path
// costs nothing.
func musicReplayGainApply(pcm []byte, gain float64) {
	if len(pcm) < 2 || gain == 1 {
		return
	}
	if gain < 0 {
		gain = 0
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int32(int16(binary.LittleEndian.Uint16(pcm[i:])))
		v := int32(math.Round(float64(s) * gain))
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(pcm[i:], uint16(int16(v)))
	}
}

// musicReplayGainFor probes a local file and returns its linear gain, or 1
// when ReplayGain is unavailable. It never holds a lock while ffprobe runs.
func musicReplayGainFor(path string) float64 {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	probe := getToolPath("ffprobe", cfg)
	if probe == "" {
		return 1
	}
	cmd := execCommand(probe, "-v", "error",
		"-show_entries", "format_tags=REPLAYGAIN_TRACK_GAIN,REPLAYGAIN_TRACK_PEAK,REPLAYGAIN_ALBUM_GAIN,REPLAYGAIN_ALBUM_PEAK",
		"-of", "default=noprint_wrappers=1", path)
	if cmd == nil {
		return 1
	}
	out, err := cmd.Output()
	if err != nil {
		return 1
	}
	gain, _, ok := musicReplayGainLevel(musicParseReplayGainTags(string(out)))
	if !ok {
		return 1
	}
	return gain
}

// musicCrossfadeState is the pre-decoded head of the next track.
type musicCrossfadeState struct {
	gen    int
	next   int
	path   string
	head   []byte
	frames int
}

// musicPeekNextLocked is the next track the transport would choose without
// consuming the play-next queue or moving the shuffle cursor. Repeat-one does
// not crossfade: the track would overlap itself.
func (m *musicState) musicPeekNextLocked() (int, bool) {
	if m.repeatMode == musicRepeatOne {
		return 0, false
	}
	for _, q := range m.queue {
		for i, t := range m.tracks {
			if sameAudioPath(t.Path, q) {
				return i, true
			}
		}
	}
	n := len(m.tracks)
	if n == 0 {
		return 0, false
	}
	if n == 1 {
		if m.repeatMode == musicRepeatAll {
			return m.current, true
		}
		return 0, false
	}
	if m.shuffle && len(m.order) == n {
		pos := m.orderPos + 1
		if pos >= n {
			if m.repeatMode != musicRepeatAll {
				return 0, false
			}
			pos = 0
		}
		return m.order[pos], true
	}
	idx := m.current + 1
	if idx >= n {
		if m.repeatMode != musicRepeatAll {
			return 0, false
		}
		idx = 0
	}
	return idx, true
}

// musicCrossfadeStartPreload begins decoding the next track's first N seconds
// when the current decode knows its rate and duration. It is a no-op for live
// streams, chiptunes (their own decoder has no PCM file), repeat-one, the
// bit-perfect path (which returns before this) and when crossfade is off.
func musicCrossfadeStartPreload(current string, gen int, rate int32, speed float64) {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	seconds := musicCrossfadeSeconds(cfg)
	if !musicCrossfadeAvailable(cfg) || seconds <= 0 || isHTTPMediaURL(current) || chiptuneHandles(current) {
		return
	}
	music.mu.Lock()
	if gen != music.gen || !music.playing || music.duration <= float64(seconds) || music.crossfadeLoading == gen {
		music.mu.Unlock()
		return
	}
	next, ok := music.musicPeekNextLocked()
	if !ok || next < 0 || next >= len(music.tracks) {
		music.mu.Unlock()
		return
	}
	nextPath := music.tracks[next].Path
	already := music.crossfade != nil && music.crossfade.gen == gen && music.crossfade.next == next
	if already || sameAudioPath(current, nextPath) || isHTTPMediaURL(nextPath) || chiptuneHandles(nextPath) {
		music.mu.Unlock()
		return
	}
	music.crossfadeLoading = gen
	music.mu.Unlock()
	go musicCrossfadePreload(gen, next, nextPath, rate, speed, seconds)
}

// musicCrossfadePreload decodes exactly the overlap length of the next track.
// The process is private: it writes no SDL device and the playback decoder is
// untouched. A failed or short read simply leaves no crossfade.
func musicCrossfadePreload(gen, next int, path string, rate int32, speed float64, seconds int) {
	defer func() {
		music.mu.Lock()
		if music.crossfadeLoading == gen {
			music.crossfadeLoading = 0
		}
		music.mu.Unlock()
	}()
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	ffmpegPath, err := helperToolPath("ffmpeg", cfg)
	if err != nil {
		return
	}
	cmd := execCommand(ffmpegPath, musicFFmpegArgs(path, 0, speed, rate)...)
	if cmd == nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return
	}
	want := musicCrossfadeFrames(seconds, rate) * 4
	head := make([]byte, want)
	n, _ := io.ReadFull(stdout, head)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	head = head[:n]
	if n != want || len(head) < 4 {
		// A short track (or a failed read) has no complete head to mix; a
		// partial fade would jump when the next decoder took over.
		return
	}
	if musicReplayGainAvailable(cfg) && !isHTTPMediaURL(path) {
		musicReplayGainApply(head, musicReplayGainFor(path))
	}
	music.mu.Lock()
	defer music.mu.Unlock()
	if gen != music.gen || !music.playing || music.current < 0 || music.current >= len(music.tracks) ||
		next < 0 || next >= len(music.tracks) || !sameAudioPath(music.tracks[next].Path, path) {
		// The track moved while the head was decoding.
		return
	}
	music.crossfade = &musicCrossfadeState{
		gen:    gen,
		next:   next,
		path:   path,
		head:   head,
		frames: len(head) / 4,
	}
}

// musicCrossfadeFor returns the ready preload for this decode generation.
func musicCrossfadeFor(gen int) *musicCrossfadeState {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.crossfade == nil || music.crossfade.gen != gen {
		return nil
	}
	return music.crossfade
}
