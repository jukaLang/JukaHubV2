package main

// alarmtone.go — what a ringing alarm sounds like.
//
// A ringtone is either one of the synthesised motifs below or a track from the
// user's music ("Choose from music…" in the alarm editor). Both end up as mono
// PCM at the UI device's rate, looped by one player goroutine that queues it in
// short chunks: a dismiss is silent within a frame, and the gain can rise
// smoothly — every ring starts at about a fifth of full scale and reaches full
// scale over five seconds, with the mixer itself raised to 100% for the ring
// (alarmvol.go).
//
// A music ringtone is decoded once per ring with the bundled ffmpeg (the music
// player's decoder, never an auto-download: an alarm must not wait on the
// network), at most alarmMusicMaxSeconds of it, and looped. Playback starts
// with the first decoded samples. A file that is gone, a missing
// ffmpeg or a decode that produces nothing falls back to the default chime: an
// alarm that cannot play its music still rings.
//
// The same player previews a ringtone in the editor: no fade-in, a few seconds
// long, cut off as soon as another one is chosen.

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// alarmDefaultSound is the ringtone an alarm without a saved choice uses.
	alarmDefaultSound = "chime"
	// alarmMusicPrefix marks a ringtone from the user's music: "file:" + path.
	alarmMusicPrefix = "file:"
	// alarmMusicPickID is the editor's "Choose from music…" entry.
	alarmMusicPickID = "music"
	// alarmFadeIn is how long a ring takes to reach full scale, from
	// alarmFadeFloor of it.
	alarmFadeIn    = 5 * time.Second
	alarmFadeFloor = 0.22
	// alarmMusicMaxSeconds is how much of a music ringtone is decoded and
	// looped: a long intro is still heard, and memory stays bounded (4 MB).
	alarmMusicMaxSeconds = 45
	// alarmPreviewLength is how long the editor plays a ringtone.
	alarmPreviewLength = 6 * time.Second
	// alarmChunk is how much audio is queued at a time, and alarmQueueAhead how
	// much may wait in the device: small enough that a dismiss is immediate and
	// the fade-in moves in fine steps.
	alarmChunk      = 50 * time.Millisecond
	alarmQueueAhead = 160 * time.Millisecond
)

// ── Synthesised ringtones ────────────────────────────────────────────────

// alarmTone is one note of a ringtone motif.
type alarmTone struct {
	Freq float64
	MS   int
	Gap  int
}

// alarmSound is a selectable synthesised ringtone: a motif, the pause between
// repeats, its loudness, and whether its notes ring out like a bell or hold
// like a buzzer.
type alarmSound struct {
	ID     string
	Name   string
	Volume float64
	Pause  int
	Bell   bool
	Notes  []alarmTone
}

// alarmSoundList is the picker's list, in the order it cycles. Every entry is
// synthesised, so there are no audio assets to ship or lose.
var alarmSoundList = []alarmSound{
	{
		ID: "chime", Name: "Classic Chime", Volume: 1.0, Pause: 900, Bell: true,
		Notes: []alarmTone{{523.25, 200, 40}, {659.25, 200, 40}, {783.99, 200, 40}, {1046.5, 320, 60}},
	},
	{
		ID: "bell", Name: "Brass Bell", Volume: 1.0, Pause: 700, Bell: true,
		Notes: []alarmTone{{880, 420, 30}, {1108.73, 320, 30}, {1318.51, 560, 60}},
	},
	{
		ID: "pulse", Name: "Digital Pulse", Volume: 0.95, Pause: 450,
		Notes: []alarmTone{
			{1244.51, 90, 70}, {1244.51, 90, 70}, {1244.51, 90, 70}, {1244.51, 90, 70},
			{1046.5, 90, 70}, {1046.5, 90, 70}, {1046.5, 90, 70}, {1046.5, 90, 70},
		},
	},
	{
		ID: "rise", Name: "Sunrise", Volume: 1.0, Pause: 800, Bell: true,
		Notes: []alarmTone{{392, 160, 20}, {523.25, 160, 20}, {659.25, 160, 20}, {783.99, 160, 20}, {1046.5, 360, 60}},
	},
	{
		ID: "buzz", Name: "Loud Buzz", Volume: 1.0, Pause: 320,
		Notes: []alarmTone{{196, 620, 60}, {196, 620, 60}},
	},
	{
		ID: "birds", Name: "Morning Birds", Volume: 0.95, Pause: 750, Bell: true,
		Notes: []alarmTone{
			{1567.98, 90, 60}, {2093, 70, 40}, {1567.98, 90, 200},
			{1760, 90, 60}, {2349.32, 70, 40}, {1760, 90, 200},
		},
	},
}

// alarmIsMusic reports whether id names a music ringtone.
func alarmIsMusic(id string) bool {
	return strings.HasPrefix(id, alarmMusicPrefix) && len(id) > len(alarmMusicPrefix)
}

// alarmMusicPath is the file a music ringtone plays.
func alarmMusicPath(id string) string {
	if !alarmIsMusic(id) {
		return ""
	}
	return strings.TrimPrefix(id, alarmMusicPrefix)
}

// alarmSoundByID resolves an id, falling back to the default for an empty or
// unknown one (an alarm saved by an older build has no sound field at all). A
// music ringtone resolves to itself, named after its file, with no notes.
func alarmSoundByID(id string) alarmSound {
	if alarmIsMusic(id) {
		return alarmSound{ID: id, Name: alarmMusicName(alarmMusicPath(id)), Volume: 1}
	}
	for _, s := range alarmSoundList {
		if s.ID == id {
			return s
		}
	}
	for _, s := range alarmSoundList {
		if s.ID == alarmDefaultSound {
			return s
		}
	}
	return alarmSoundList[0]
}

// alarmMusicName is a track's display name: its file name without extension.
func alarmMusicName(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" && len(ext) < len(base) {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// alarmSoundName is the label the editor and the ringing card show.
func alarmSoundName(id string) string {
	if id == alarmMusicPickID {
		return "Choose from music…"
	}
	return alarmSoundByID(id).Name
}

// alarmCycleRingtone is the editor's Left/Right: the synthesised list with one
// more stop at its end for music — the chosen track if there is one, else the
// "Choose from music…" entry. chosen is the track the alarm already has ("" for
// none).
func alarmCycleRingtone(id, chosen string, dir int) string {
	musicStop := alarmMusicPickID
	if alarmIsMusic(chosen) {
		musicStop = chosen
	}
	n := len(alarmSoundList)
	if n == 0 {
		return musicStop
	}
	pos := n // the music stop
	for i, s := range alarmSoundList {
		if s.ID == id {
			pos = i
			break
		}
	}
	if dir < 0 {
		pos--
	} else {
		pos++
	}
	pos = (pos + n + 1) % (n + 1)
	if pos == n {
		return musicStop
	}
	return alarmSoundList[pos].ID
}

// alarmSoundPCM synthesises one motif and its pause as mono samples at rate,
// normalised to 95% of full scale times the sound's volume. Bell notes are a
// fundamental with two quicker-decaying partials; held notes add odd partials
// for a buzzer's edge. Every note starts and ends at zero.
func alarmSoundPCM(rate int32, s alarmSound) []int16 {
	if rate <= 0 || len(s.Notes) == 0 {
		return nil
	}
	sr := float64(rate)
	total := 0
	for _, n := range s.Notes {
		total += int(float64(n.MS+n.Gap) * sr / 1000)
	}
	total += int(float64(s.Pause) * sr / 1000)
	if total <= 0 {
		return nil
	}
	mix := make([]float64, total)
	at := 0
	attack := 0.006 * sr
	release := 0.025 * sr
	for _, n := range s.Notes {
		length := int(float64(n.MS) * sr / 1000)
		for i := 0; i < length && at+i < total; i++ {
			t := float64(i) / sr
			w := 2 * math.Pi * n.Freq * t
			var v float64
			if s.Bell {
				v = math.Sin(w) + 0.35*math.Exp(-t/0.12)*math.Sin(2*w) + 0.15*math.Exp(-t/0.06)*math.Sin(3*w)
				v *= math.Exp(-t / 0.45)
			} else {
				v = math.Sin(w) + 0.3*math.Sin(3*w) + 0.12*math.Sin(5*w)
			}
			env := 1.0
			if fi := float64(i); fi < attack {
				env = fi / attack
			}
			if rest := float64(length - i); rest < release {
				env *= rest / release
			}
			mix[at+i] += v * env
		}
		at += int(float64(n.MS+n.Gap) * sr / 1000)
	}
	peak := 0.0
	for _, v := range mix {
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	out := make([]int16, total)
	if peak <= 0 {
		return out
	}
	vol := s.Volume
	if vol <= 0 || vol > 1 {
		vol = 1
	}
	scale := 0.95 * vol / peak
	for i, v := range mix {
		out[i] = int16(math.Round(v * scale * 32767))
	}
	return out
}

// ── The player ───────────────────────────────────────────────────────────

// alarmPCM is a ringtone's samples as they become available: a synthesised one
// arrives whole, a decoded one grows until its decoder finishes.
type alarmPCM struct {
	mu      sync.Mutex
	samples []int16
	done    bool
	failed  bool
}

func (p *alarmPCM) view() ([]int16, bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.samples, p.done, p.failed
}

// alarmPlayGen numbers playbacks: a loop keeps going while the generation it was
// started with is current, so starting or stopping one is a single increment.
var alarmPlayGen atomic.Int64

// alarmPlayStart stops whatever the alarm player is doing and plays soundID: a
// ring (fading in, until stopped) or a preview (a few seconds).
func alarmPlayStart(soundID string, preview bool) {
	alarmPlayStop()
	gen := alarmPlayGen.Load()
	go alarmPlayLoop(gen, soundID, preview)
}

// alarmPlayStop silences the alarm player at once.
func alarmPlayStop() {
	alarmPlayGen.Add(1)
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable {
		return
	}
	audioMutex.Lock()
	outClear(audioDev)
	audioMutex.Unlock()
}

// alarmPlaying reports whether gen is still the current playback.
func alarmPlaying(gen int64) bool { return alarmPlayGen.Load() == gen }

// alarmGain is the ring's gain at age: the floor rising to full scale over
// alarmFadeIn on a smoothstep. A preview plays at a fixed, gentler level and
// fades its last 300 ms out.
func alarmGain(age time.Duration, preview bool) float64 {
	if preview {
		g := 0.7
		if rest := alarmPreviewLength - age; rest < 300*time.Millisecond {
			g *= bootClamp01(rest.Seconds() / 0.3)
		}
		return g
	}
	return alarmFadeFloor + (1-alarmFadeFloor)*bootSmoothstep(0, alarmFadeIn.Seconds(), age.Seconds())
}

// alarmPlayLoop is one playback: it keeps alarmQueueAhead of audio queued,
// looping the ringtone, until its generation is retired (or a preview ends).
func alarmPlayLoop(gen int64, soundID string, preview bool) {
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable || !alarmPlaying(gen) {
		return
	}
	rate := audioSampleRate
	src := &alarmPCM{}
	fellBack := false
	if alarmIsMusic(soundID) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		seconds := alarmMusicMaxSeconds
		if preview {
			seconds = int(alarmPreviewLength/time.Second) + 1
		}
		go alarmDecodeMusic(ctx, alarmMusicPath(soundID), rate, seconds, src)
	} else {
		src.samples = alarmSoundPCM(rate, alarmSoundByID(soundID))
		src.done = true
	}

	chunk := int(float64(rate) * alarmChunk.Seconds())
	if chunk < 64 {
		chunk = 64
	}
	buf := make([]byte, chunk*2)
	ahead := uint32(float64(rate) * 2 * alarmQueueAhead.Seconds())
	started := time.Now()
	pos := 0
	for alarmPlaying(gen) {
		age := time.Since(started)
		if preview && age >= alarmPreviewLength {
			return
		}
		samples, done, failed := src.view()
		if (failed || (done && len(samples) == 0)) && !fellBack {
			// The music could not be played: ring the default chime instead.
			logWarn("alarm", "ringtone %q could not be decoded; using the default", alarmMusicName(alarmMusicPath(soundID)))
			fellBack = true
			src = &alarmPCM{samples: alarmSoundPCM(rate, alarmSoundByID(alarmDefaultSound)), done: true}
			pos = 0
			continue
		}
		audioMutex.Lock()
		queued := outQueued(audioDev)
		audioMutex.Unlock()
		if queued >= ahead || len(samples) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		gain := alarmGain(age, preview)
		n := 0
		for n < chunk {
			if pos >= len(samples) {
				if !done {
					break // the decoder is still filling the buffer
				}
				pos = 0
			}
			v := float64(samples[pos]) * gain
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			s := int16(v)
			buf[2*n] = byte(s)
			buf[2*n+1] = byte(uint16(s) >> 8)
			n++
			pos++
		}
		if n == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		audioMutex.Lock()
		var queueErr error
		if alarmPlaying(gen) {
			queueErr = outQueue(audioDev, buf[:n*2])
		}
		audioMutex.Unlock()
		if queueErr != nil {
			logWarn("alarm", "ringtone output stopped: %v", queueErr)
			return
		}
		publishAudioLevel(buf[:n*2], int(rate), 1)
	}
}

// alarmDecodeMusic decodes up to seconds of path into dst as mono samples at
// rate with the bundled ffmpeg. It only uses an ffmpeg that is already there:
// an alarm must not start a download. Any failure marks dst failed (the
// player then rings the default chime); fewer than half a second of audio
// counts as a failure too.
func alarmDecodeMusic(ctx context.Context, path string, rate int32, seconds int, dst *alarmPCM) {
	fail := func(reason string, err error) {
		logWarn("alarm", "music ringtone %s: %s: %v", filepath.Base(path), reason, err)
		dst.mu.Lock()
		dst.failed, dst.done = true, true
		dst.mu.Unlock()
	}
	if _, err := os.Stat(path); err != nil {
		fail("file", err)
		return
	}
	ffmpeg, ok := checkToolPresence("ffmpeg")
	if !ok {
		fail("ffmpeg", os.ErrNotExist)
		return
	}
	args := []string{"-nostdin", "-v", "error", "-i", path, "-vn",
		"-t", strconv.Itoa(seconds), "-f", "s16le", "-acodec", "pcm_s16le",
		"-ac", "1", "-ar", strconv.Itoa(int(rate)), "pipe:1"}
	cmd := P().CommandContext(ctx, ffmpeg, args...)
	alarmHideConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail("pipe", err)
		return
	}
	if err := cmd.Start(); err != nil {
		fail("start", err)
		return
	}
	maxSamples := int(rate) * seconds
	readBuf := make([]byte, 32*1024)
	var carry []byte
	for {
		n, rerr := stdout.Read(readBuf)
		if n > 0 {
			data := readBuf[:n]
			if len(carry) > 0 {
				data = append(carry, data...)
				carry = nil
			}
			pairs := len(data) / 2
			dst.mu.Lock()
			for i := 0; i < pairs && len(dst.samples) < maxSamples; i++ {
				dst.samples = append(dst.samples, int16(uint16(data[2*i])|uint16(data[2*i+1])<<8))
			}
			full := len(dst.samples) >= maxSamples
			dst.mu.Unlock()
			if len(data)%2 == 1 {
				carry = []byte{data[len(data)-1]}
			}
			if full {
				break
			}
		}
		if rerr != nil {
			if rerr != io.EOF && ctx.Err() == nil {
				logWarn("alarm", "music ringtone read: %v", rerr)
			}
			break
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	dst.mu.Lock()
	dst.done = true
	if len(dst.samples) < int(rate)/2 {
		dst.failed = true
	}
	dst.mu.Unlock()
}

// ── Music to choose from ─────────────────────────────────────────────────

// alarmMusicFile is one track the editor's picker lists.
type alarmMusicFile struct {
	Path string
	Name string
}

// alarmMusicLimit bounds the scan: the picker is a list on a handheld, and a
// card full of albums must not stall it.
const alarmMusicLimit = 400

// alarmMusicExts are the formats offered as ringtones: what the brief names and
// what ffmpeg decodes quickly enough to start ringing at once.
var alarmMusicExts = map[string]bool{".mp3": true, ".ogg": true, ".wav": true, ".flac": true, ".m4a": true, ".aac": true, ".opus": true}

// alarmMusicRoots are the folders the music player uses: the folder its
// playlist came from, the downloads' music folder, a Music folder under the
// Files root, and the platform's usual Music folder.
func alarmMusicRoots(config *Config) []string {
	var roots []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		for _, r := range roots {
			if strings.EqualFold(r, dir) {
				return
			}
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			roots = append(roots, dir)
		}
	}
	add(musicCurrentDir())
	add(downloadsMusicDir())
	if config != nil {
		if root := strings.TrimSpace(config.Variables.FileExplorerRoot); root != "" {
			add(filepath.Join(root, "Music"))
		}
	}
	add("Music")
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			add(filepath.Join(home, "Music"))
		}
	} else {
		add("/mnt/SDCARD/Music")
	}
	return roots
}

// alarmScanMusic lists the playable tracks under roots, a few folder levels
// deep, sorted by name, at most limit of them. Hidden folders are skipped.
func alarmScanMusic(roots []string, limit int) []alarmMusicFile {
	var files []alarmMusicFile
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(files) >= limit {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(files) >= limit {
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			path := filepath.Join(dir, name)
			if e.IsDir() {
				if depth < 3 {
					walk(path, depth+1)
				}
				continue
			}
			if !alarmMusicExts[strings.ToLower(filepath.Ext(name))] || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, alarmMusicFile{Path: path, Name: alarmMusicName(path)})
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	sort.SliceStable(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files
}

// alarmMusic is the picker's list, filled by a background scan.
var alarmMusic struct {
	mu       sync.Mutex
	scanning bool
	scanned  bool
	files    []alarmMusicFile
}

// alarmMusicRefresh starts a scan unless one is running. The roots are read
// here, on the main thread; the walk runs on its own goroutine.
func alarmMusicRefresh(config *Config) {
	alarmMusic.mu.Lock()
	if alarmMusic.scanning {
		alarmMusic.mu.Unlock()
		return
	}
	alarmMusic.scanning = true
	alarmMusic.mu.Unlock()
	roots := alarmMusicRoots(config)
	go func() {
		files := alarmScanMusic(roots, alarmMusicLimit)
		alarmMusic.mu.Lock()
		alarmMusic.files = files
		alarmMusic.scanning = false
		alarmMusic.scanned = true
		alarmMusic.mu.Unlock()
	}()
}

// alarmMusicSnapshot is the list as the picker should draw it: the tracks, and
// whether a scan has finished (an empty list before that is "Looking…").
func alarmMusicSnapshot() ([]alarmMusicFile, bool) {
	alarmMusic.mu.Lock()
	defer alarmMusic.mu.Unlock()
	return alarmMusic.files, alarmMusic.scanned && !alarmMusic.scanning
}
