package main

// bgmusic.go — background music: a quiet loop under the menus, the way a
// console plays something while you browse its home screen.
//
// Settings → Sound & haptics has the switch, the music and its level. The
// music is one of three built-in songs (Calm, Lo-fi, Chiptune), the song the
// user made in the Synth, or a shuffle of the music library. The built-in
// songs ship no audio: they are Synth projects (synth_engine.go) written
// below, rendered live by a private engine, so they cost a few kilobytes of
// code and sound the same on the handheld and on Windows. Library songs are
// decoded by ffmpeg, as the music player does, but never auto-download it.
//
// The music gets out of the way of every other sound. It fades out while the
// music player or a video plays, while the Synth page is open, under Quiet
// Mode, while the screen sleeps, while the voice orb listens or an alarm
// rings, and it ducks under Juka's voice. A launched game (an emulator,
// Remote Play, a Wine app, anything that takes the screen) closes its device
// at once: the game needs the speaker, and the handheld's ALSA is not always
// shared. It comes back, faded in, when the menus are back, and a library
// song carries on where it stopped.
//
// Threads: bgmTick runs on the main thread once a second (alarmCheck) and
// owns the main-thread-only checks; it starts bgmLoop, which owns the SDL
// device and closes it whenever there is nothing to play. Everything shared
// is an atomic or sits under bgm.mu.

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// The Custom settings: on/off, source, level and optional chosen path.
	bgmEnabledKey  = "bgMusic"
	bgmSourceKey   = "bgMusicSource"
	bgmVolumeKey   = "bgMusicVolume"
	bgmSelectedKey = "bgMusicSelectedPath"

	bgmDefaultVolume = 40

	bgmSrcCalm     = "calm"
	bgmSrcLofi     = "lofi"
	bgmSrcChip     = "chip"
	bgmSrcSynth    = "synth"
	bgmSrcLibrary  = "library"
	bgmSrcSelected = "selected"

	// bgmFade is how long a fade in or out takes.
	bgmFade = 700 * time.Millisecond

	// bgmIdleClose is how long the device stays open, silent, before the loop
	// closes it (a short pause — a toast's voice, a menu sound — keeps it).
	bgmIdleClose = 2 * time.Second

	// bgmDuck is the level under Juka's voice; bgmBlock the frames per render.
	bgmDuck  = 0.3
	bgmBlock = 512
)

// bgmSourceOptions are the stops of the Music row.
func bgmSourceOptions(*Config) []setOption {
	return []setOption{
		{Value: bgmSrcCalm, Label: "Calm", Note: "Soft pads and bells"},
		{Value: bgmSrcLofi, Label: "Lo-fi", Note: "Mellow keys over a lazy beat"},
		{Value: bgmSrcChip, Label: "Chiptune", Note: "Bright 8-bit arpeggios"},
		{Value: bgmSrcSynth, Label: "My Synth song", Note: "What you made in Tools → Synth"},
		{Value: bgmSrcLibrary, Label: "My music", Note: "Your music library, shuffled"},
		{Value: bgmSrcSelected, Label: "Selected music", Note: "A file or folder you choose in Files"},
	}
}

// bgmSourceIndex is the Music row's current stop.
func bgmSourceIndex(c *Config) int {
	want := bgmSourceIn(c)
	for i, o := range bgmSourceOptions(c) {
		if o.Value == want {
			return i
		}
	}
	return 0
}

// bgmSourceLabel is a source's name for read-outs.
func bgmSourceLabel(id string) string {
	for _, o := range bgmSourceOptions(nil) {
		if o.Value == id {
			return o.Label
		}
	}
	return "Calm"
}

// ── Settings ─────────────────────────────────────────────────────────────

type bgmChoice struct {
	source string
	path   string
}

var (
	bgmOn          atomic.Bool
	bgmLevel       atomic.Int32
	bgmChoiceState atomic.Value // bgmChoice: source and chosen path change together

	// bgmGen moves when the music changes, so the loop switches songs.
	bgmGen atomic.Uint64

	// bgmMainHold is the main thread's verdict (screen asleep, voice orb,
	// alarm, dictation), refreshed once a second and on every bind.
	bgmMainHold atomic.Bool

	// bgmHalt asks the loop to close its device now, without a fade.
	bgmHalt atomic.Bool

	// bgmSkipReq asks the loop for the next library or selected-folder song.
	bgmSkipReq atomic.Bool
)

func init() {
	bgmLevel.Store(bgmDefaultVolume)
	bgmChoiceState.Store(bgmChoice{source: bgmSrcCalm})
}

// bgmEnabledIn is the switch as stored (off unless turned on).
func bgmEnabledIn(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bgmEnabledKey); ok {
			return bootSfxParseBool(v, false)
		}
	}
	return false
}

// bgmSourceIn is the music as stored.
func bgmSourceIn(c *Config) string {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bgmSourceKey); ok {
			if s, ok := v.(string); ok {
				s = strings.ToLower(strings.TrimSpace(s))
				for _, o := range bgmSourceOptions(c) {
					if o.Value == s {
						return s
					}
				}
			}
		}
	}
	return bgmSrcCalm
}

// bgmVolumeIn is the level as stored, in percent.
func bgmVolumeIn(c *Config) int {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bgmVolumeKey); ok {
			if n, ok := bootSfxParsePercent(v); ok {
				return n
			}
		}
	}
	return bgmDefaultVolume
}

// bgmSelectedPathIn is the user's saved file or folder. It is kept separate
// from the source choice so switching to a built-in song does not forget it.
func bgmSelectedPathIn(c *Config) string {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bgmSelectedKey); ok {
			if path, ok := v.(string); ok {
				return strings.TrimSpace(path)
			}
		}
	}
	return ""
}

// bgmBind reads the settings into the live state. Cheap; the once-a-
// second tick calls it, so a change from any path applies within a second.
func bgmBind(c *Config) {
	if c == nil {
		return
	}
	bgmOn.Store(bgmEnabledIn(c))
	bgmLevel.Store(int32(bgmVolumeIn(c)))
	choice := bgmChoice{source: bgmSourceIn(c), path: bgmSelectedPathIn(c)}
	if old, _ := bgmChoiceState.Load().(bgmChoice); old != choice {
		bgmChoiceState.Store(choice)
		bgmGen.Add(1)
		bgm.mu.Lock()
		bgm.libFails = 0
		bgm.lib, bgm.libIdx, bgm.libPos = nil, 0, 0
		bgm.mu.Unlock()
	}
}

// bgmSetSelectedPath saves a file or folder chosen in Files and makes it the
// active background music source. The picker validates containment first;
// this checks the path again before it is persisted.
func bgmSetSelectedPath(c *Config, path string) error {
	if c == nil {
		return os.ErrInvalid
	}
	clean, err := cleanUserPath(path)
	if err != nil {
		return err
	}
	clean, err = filepath.Abs(clean)
	if err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return err
	}
	if !info.IsDir() && (!info.Mode().IsRegular() || !isAudioFile(clean)) {
		return os.ErrInvalid
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[bgmSelectedKey] = clean
	bgmSetPrefs(c, true, bgmSrcSelected, bgmVolumeIn(c))
	return nil
}

// bgmSetPrefs is what the Settings rows call: store, apply, save.
func bgmSetPrefs(c *Config, on bool, source string, pct int) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	pct = synthClampInt(pct, 0, 100)
	switched := on && (!bgmEnabledIn(c) || bgmSourceIn(c) != source)
	c.Variables.Custom[bgmEnabledKey] = strconv.FormatBool(on)
	c.Variables.Custom[bgmSourceKey] = source
	c.Variables.Custom[bgmVolumeKey] = strconv.Itoa(pct)
	// The user is choosing right now: the music that results needs no toast
	// naming it (bgmAnnounce). A level step opens no new music, so it leaves
	// the flag alone. Off forgets what was named.
	bgm.mu.Lock()
	if switched {
		bgm.quietNext = true
	}
	if !on {
		bgm.quietNext = false
		bgm.announced = ""
	}
	bgm.mu.Unlock()
	bgmBind(c)
	saveUserConfigDebounced(userConfigFromVariables(c))
	bgmTick(c)
}

// ── When it may play ─────────────────────────────────────────────────────

// bgmTick is the main thread's once-a-second look (alarmCheck): it refreshes
// the settings and the main-thread-only reasons to be quiet, and starts the
// loop when there is something to play.
func bgmTick(c *Config) {
	bgmPickTick(c)
	bgmBind(c)
	bgmMainHold.Store(screensaverAsleep() || powerSleepActive() || voiceOrbActive() ||
		vkVoiceActive() || alarmFiringActive())
	if !bgmOn.Load() || bgmLevel.Load() <= 0 || bgmQuietReason() != "" {
		return
	}
	bgm.mu.Lock()
	start := !bgm.running && (bgm.failAt.IsZero() || time.Since(bgm.failAt) > 10*time.Second)
	if start {
		bgm.running = true
	}
	bgm.mu.Unlock()
	if start {
		go bgmLoop()
	}
}

// bgmQuietReason is why the music must not play right now ("" = it may).
// Safe off the main thread: everything it reads is an atomic or locked.
func bgmQuietReason() string {
	switch {
	case gameScreenSuspended():
		return "a game is running"
	case quietModeOn():
		return "Quiet Mode is on"
	case bgmMainHold.Load():
		return "the screen is asleep or busy"
	case time.Since(time.Unix(0, synthLastSeen.Load())) < synthIdleStop:
		return "the Synth is open"
	case windowMinimised():
		return "JukaHub is minimised"
	}
	// A paused song counts as playing, like a paused video: the user means to
	// come back to it, and the menus' music popping in between is noise.
	music.mu.Lock()
	musicOn := music.playing
	music.mu.Unlock()
	if musicOn {
		return "music is playing"
	}
	switch playerPhase() {
	case "playing", "paused", "downloading":
		return "a video is open"
	}
	return ""
}

// bgmHaltForGame closes the device at once: a game is starting and needs the
// speaker (gamesuspend.go).
func bgmHaltForGame() {
	// Under the lock, so a loop that is just exiting (it clears the flag under
	// the same lock) cannot leave a stale halt for the next one.
	bgm.mu.Lock()
	if bgm.running {
		bgmHalt.Store(true)
	}
	bgm.mu.Unlock()
}

// bgmStatus is the Settings read-out: what plays, or why nothing does.
func bgmStatus(c *Config) string {
	if !bgmEnabledIn(c) {
		return "Music under the menus; it pauses for games, videos and your music"
	}
	if why := bgmQuietReason(); why != "" {
		return "Paused: " + why
	}
	if bgmVolumeIn(c) <= 0 {
		return "Music level is 0%"
	}
	bgm.mu.Lock()
	running, now, note, failAt := bgm.running, bgm.nowPlaying, bgm.note, bgm.failAt
	bgm.mu.Unlock()
	if !running && !failAt.IsZero() && time.Since(failAt) < 10*time.Second {
		return "No audio device right now — trying again shortly"
	}
	if note != "" {
		return note
	}
	if running && now != "" {
		return "Playing " + now
	}
	return "Starting…"
}

// ── The loop ─────────────────────────────────────────────────────────────

var bgm struct {
	mu         sync.Mutex
	running    bool
	failAt     time.Time
	nowPlaying string

	// note explains a fallback ("No songs in your library — playing Calm").
	note string

	// lib is the shuffled library and where it stands, kept across pauses
	// so a song resumes where it stopped.
	lib    []string
	libIdx int
	libPos float64

	// libFails counts songs in a row that decoded nothing; enough of them and
	// the library is given up for Calm instead of relaunching ffmpeg forever.
	libFails int

	// announced is the music last named in a toast (bgmAnnounce); quietNext
	// skips the next toast, because the user just chose that music.
	announced string
	quietNext bool
}

// bgmAnnounce names the music in a toast when it first becomes audible in
// this run, and again when it changes without the user asking — "My music"
// falls back to Calm until ffmpeg is installed, and the first Extract audio
// or video player run installs it, so the user's own tracks suddenly started
// playing under the menus with nothing on screen saying what they were or
// how to stop them. key is "library" for library songs (one toast for the
// whole shuffle, not one per song) or the built-in song's name. Safe off the
// main thread (ShowToast queues).
func bgmAnnounce(key string) {
	bgm.mu.Lock()
	if key == "" || key == bgm.announced {
		bgm.mu.Unlock()
		return
	}
	// A built-in song at the start of a run is what the user turned on and
	// knows; only their own files, or music that changed by itself, are said.
	quiet := bgm.quietNext || (bgm.announced == "" && key != "library")
	bgm.announced = key
	bgm.quietNext = false
	name := bgm.nowPlaying
	bgm.mu.Unlock()
	if quiet {
		return
	}
	if name == "" {
		name = key
	}
	ShowToast("Background music: "+dlShortTitle(name, 32)+" — turn it off in Start → Quick Settings", ToastKindInfo)
}

// bgmStream is one source of stereo samples.
type bgmStream interface {
	// fill writes frames stereo samples into out (silence when it has none
	// yet) and reports false once it has ended.
	fill(out []float32, frames int) bool
	close()
}

// bgmLoop owns the device: it fades the music in and out as bgmQuietReason
// allows and closes everything once it has been silent for bgmIdleClose.
//
// The output is env × lvl: env is the fade (0..1, linear over bgmFade, so a
// fade takes the same time at any level) and lvl the level with the duck,
// smoothed so a step of the Music level row or Juka starting to talk glides.
func bgmLoop() {
	var stream bgmStream
	defer func() {
		if r := recover(); r != nil {
			logWarn("bgmusic", "music loop stopped: %v", r)
			bgm.mu.Lock()
			bgm.failAt = time.Now()
			bgm.mu.Unlock()
		}
		if stream != nil {
			stream.close()
		}
		bgm.mu.Lock()
		bgm.running = false
		bgm.nowPlaying = ""
		bgmHalt.Store(false)
		bgm.mu.Unlock()
	}()
	// A Next song pressed while nothing played must not skip the song this
	// loop resumes.
	bgmSkipReq.Store(false)
	desired := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16LSB, Channels: 2, Samples: 1024}
	var obtained sdl.AudioSpec
	dev, err := outOpen(&desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logWarn("bgmusic", "no audio device: %v", err)
		bgm.mu.Lock()
		bgm.failAt = time.Now()
		bgm.mu.Unlock()
		return
	}
	defer outClose(dev)
	rate := int(obtained.Freq)
	if rate <= 0 {
		rate = int(desired.Freq)
	}
	fbuf := make([]float32, bgmBlock*2)
	bbuf := make([]byte, bgmBlock*4)
	ahead := uint32(max(8*bgmBlock, 2*int(obtained.Samples)) * 4)
	fadeStep := 1 / (bgmFade.Seconds() * float64(rate))
	lvlCoef := 1 - math.Exp(-1/(0.15*float64(rate)))
	env, lvl := 0.0, -1.0
	gen := bgmGen.Load() - 1 // opens the first stream below
	var silentSince time.Time
	started := false
	announce := "" // the music to name once it is heard (bgmAnnounce)
	// Spatial audio (spatial.go) plays the music in the same space as the
	// rest of what JukaHub plays.
	var spatial spatialSideStream
	for {
		if bgmHalt.Swap(false) {
			outClear(dev)
			return
		}
		// A game must find the speaker free: the device goes now, unfaded.
		if gameScreenSuspended() {
			outClear(dev)
			return
		}
		quiet := !bgmOn.Load() || bgmLevel.Load() <= 0 || bgmQuietReason() != ""
		switching := gen != bgmGen.Load()
		if switching && (env < 0.001 || stream == nil) {
			if stream != nil {
				stream.close()
			}
			gen = bgmGen.Load()
			stream = bgmOpenStream(rate)
			announce = bgmStreamKey(stream)
			env = 0
			continue
		}
		if bgmSkipReq.Swap(false) && stream != nil {
			if ls, ok := stream.(*bgmLibStream); ok {
				ls.skip()
			}
		}
		envTarget := 1.0
		if quiet || switching {
			envTarget = 0
		}
		lvlTarget := math.Pow(float64(bgmLevel.Load())/100, 1.7) * 0.9
		if jukaSpeaking() {
			lvlTarget *= bgmDuck
		}
		if lvl < 0 {
			lvl = lvlTarget
		}
		silent := envTarget == 0 && env < 0.001
		if silent {
			// Faded out: stop rendering, so a library song keeps its place and
			// nothing burns CPU; SDL plays silence once the queue drains.
			if silentSince.IsZero() {
				silentSince = time.Now()
			} else if time.Since(silentSince) > bgmIdleClose {
				return
			}
		} else {
			silentSince = time.Time{}
		}
		sd := spatial.ready(rate)
		for !silent && stream != nil && outQueued(dev) < ahead {
			if !stream.fill(fbuf, bgmBlock) {
				// A library song ended: the next one opens straight away.
				stream.close()
				stream = bgmOpenStream(rate)
				announce = bgmStreamKey(stream)
			}
			// A library song fades in from its first decoded sample, not from
			// the silence while ffmpeg starts.
			ready := true
			if ls, ok := stream.(*bgmLibStream); ok && !ls.got {
				ready = false
			}
			// Named once it is really heard, not while a game holds it quiet.
			if ready && envTarget > 0 && announce != "" {
				bgmAnnounce(announce)
				announce = ""
			}
			for i := 0; i < bgmBlock; i++ {
				if env < envTarget && ready {
					env = math.Min(envTarget, env+fadeStep)
				} else if env > envTarget {
					env = math.Max(envTarget, env-fadeStep)
				}
				lvl += (lvlTarget - lvl) * lvlCoef
				g := env * lvl
				xl, xr := float64(fbuf[2*i])*g, float64(fbuf[2*i+1])*g
				if sd != nil {
					xl, xr = sd.frameGain(float64(fbuf[2*i]), float64(fbuf[2*i+1]), g)
				}
				for ch, v := range [2]float64{xl, xr} {
					s := int16(math.Max(-1, math.Min(1, v)) * 32767)
					binary.LittleEndian.PutUint16(bbuf[4*i+2*ch:], uint16(s))
				}
			}
			if err := outQueue(dev, bbuf); err != nil {
				logWarn("bgmusic", "queue: %v", err)
				bgm.mu.Lock()
				bgm.failAt = time.Now()
				bgm.mu.Unlock()
				return
			}
			publishAudioLevel(bbuf, rate, 2)
		}
		if !started {
			outPause(dev, false)
			started = true
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// bgmOpenStream opens the chosen music. A library with nothing playable, or
// no ffmpeg, falls back to Calm and says so in Settings.
func bgmOpenStream(rate int) bgmStream {
	choice, _ := bgmChoiceState.Load().(bgmChoice)
	src := choice.source
	note := ""
	var s bgmStream
	switch src {
	case bgmSrcLibrary, bgmSrcSelected:
		ls, why := bgmOpenLibrary(rate)
		if ls != nil {
			s = ls
		} else {
			note = why + " — playing Calm"
			src = bgmSrcCalm
		}
	case bgmSrcSynth:
		p, ok := bgmLoadSynthSong()
		if !ok {
			note = "No Synth song yet — playing the Synth's demo"
		}
		s = bgmNewSongStream(p, rate)
	}
	if s == nil {
		s = bgmNewSongStream(bgmBuiltinSong(src), rate)
	}
	bgm.mu.Lock()
	bgm.note = note
	if src != bgmSrcLibrary {
		bgm.nowPlaying = bgmSourceLabel(src)
	}
	bgm.mu.Unlock()
	return s
}

// bgmStreamKey is what bgmAnnounce compares: "library" for any library or
// chosen-file song, else the built-in song's name.
func bgmStreamKey(s bgmStream) string {
	if _, ok := s.(*bgmLibStream); ok {
		return "library"
	}
	bgm.mu.Lock()
	defer bgm.mu.Unlock()
	return bgm.nowPlaying
}

// ── Song streams (built-in songs and the Synth song) ─────────────────────

type bgmSongStream struct {
	proj *synthProject
	eng  *synthEngine
}

func bgmNewSongStream(p synthProject, rate int) *bgmSongStream {
	p.sanitize()
	s := &bgmSongStream{proj: &p, eng: newSynthEngine(rate)}
	s.eng.proj = s.proj
	s.eng.start()
	return s
}

func (s *bgmSongStream) fill(out []float32, frames int) bool {
	s.eng.render(out, frames)
	return true
}

func (s *bgmSongStream) close() {}

// bgmLoadSynthSong is the Synth's saved project (synth.json, synth.go). ok is
// false when there is none, and the demo is returned instead.
func bgmLoadSynthSong() (synthProject, bool) {
	data, err := os.ReadFile(synthStoreFile)
	if err != nil {
		return synthDefaultProject(), false
	}
	p := synthDefaultProject()
	p.Patterns = [synthPatterns]synthPattern{}
	if json.Unmarshal(data, &p) != nil {
		return synthDefaultProject(), false
	}
	empty := true
	for i := range p.Patterns {
		if !p.Patterns[i].empty() {
			empty = false
			break
		}
	}
	if empty {
		return synthDefaultProject(), false
	}
	// A one-pattern song loops what was on screen; with a chain it plays the
	// chain. Either way nothing is muted in the menus that was not muted there.
	return p, true
}

// bgmBuiltinSong writes one of the built-in songs: eight bars (patterns),
// an A section and a B section, chained.
func bgmBuiltinSong(id string) synthProject {
	p := synthProject{Version: synthProjectVersion, Steps: synthMaxSteps, Chain: synthPatterns, Master: 0.62}
	on := func(pat, lane, step, note int, accent bool) {
		if step >= 0 && step < synthMaxSteps && note >= synthMinDegree && note <= synthMaxDegree {
			p.Patterns[pat].Lanes[lane][step] = synthStep{On: true, Note: note, Accent: accent}
		}
	}
	switch id {
	case bgmSrcLofi:
		// A Dorian, swung, lo-fi kit: i iv VII III, then i VII iv v. The bass
		// sits on the root and the fifth above it: the handheld's speaker has
		// nothing below about 80 Hz.
		p.BPM, p.Swing, p.Key, p.Scale = 78, 0.38, 9, 2
		p.DelayTime, p.DelayFB = 2, 0.3
		synthApplyPreset(&p, synthInstDrums, 2)
		synthApplyPreset(&p, synthInstBass, 1)
		synthApplyPreset(&p, synthInstLead, 2)
		synthApplyPreset(&p, synthInstChords, 3)
		p.Drums[0].Volume, p.Drums[1].Volume, p.Drums[2].Volume, p.Drums[3].Volume = 0.72, 0.5, 0.28, 0
		p.Tones[synthInstChords].Release, p.Tones[synthInstChords].Length = 0.62, 0.9
		p.Tones[synthInstChords].Cutoff = 0.45
		p.Tones[synthInstLead].Volume = 0.42
		roots := [synthPatterns]int{0, 3, 6, 2, 0, 6, 3, 4}
		for pat, r := range roots {
			on(pat, synthLaneKick, 0, 0, true)
			on(pat, synthLaneKick, 7, 0, false)
			on(pat, synthLaneKick, 10, 0, false)
			on(pat, synthLaneSnare, 4, 0, false)
			on(pat, synthLaneSnare, 12, 0, false)
			for s := 2; s < 16; s += 4 {
				on(pat, synthLaneHat, s, 0, false)
			}
			on(pat, synthLaneChord, 0, r, false)
			on(pat, synthLaneChord, 6, r, false)
			on(pat, synthLaneChord, 11, r, false)
			on(pat, synthLaneBass, 0, r, true)
			on(pat, synthLaneBass, 7, r, false)
			on(pat, synthLaneBass, 10, r+4, false)
			if pat%2 == 1 {
				on(pat, synthLaneLead, 3, r+4, false)
				on(pat, synthLaneLead, 9, r+2, false)
				on(pat, synthLaneLead, 14, r+7, false)
			}
		}
	case bgmSrcChip:
		// C major at 126: I V vi IV, then IV I ii V, square arpeggios.
		p.BPM, p.Key, p.Scale = 126, 0, 1
		p.DelayTime, p.DelayFB = 1, 0.2
		synthApplyPreset(&p, synthInstDrums, 1)
		synthApplyPreset(&p, synthInstBass, 2)
		synthApplyPreset(&p, synthInstLead, 3)
		synthApplyPreset(&p, synthInstChords, 1)
		p.Drums[0].Volume, p.Drums[1].Volume, p.Drums[2].Volume, p.Drums[3].Volume = 0.6, 0.45, 0.25, 0
		p.Tones[synthInstLead].Volume = 0.4
		p.Tones[synthInstChords].Volume = 0.35
		roots := [synthPatterns]int{0, 4, 5, 3, 3, 0, 1, 4}
		arpA := [8]int{0, 2, 4, 7, 4, 2, 4, 7}
		arpB := [8]int{7, 4, 2, 4, 9, 7, 4, 2}
		for pat, r := range roots {
			on(pat, synthLaneKick, 0, 0, true)
			on(pat, synthLaneKick, 8, 0, false)
			on(pat, synthLaneSnare, 4, 0, false)
			on(pat, synthLaneSnare, 12, 0, false)
			for s := 2; s < 16; s += 4 {
				on(pat, synthLaneHat, s, 0, false)
			}
			arp := arpA
			if pat >= 4 {
				arp = arpB
			}
			for i, d := range arp {
				on(pat, synthLaneLead, 2*i, r+d, i == 0)
			}
			for s := 0; s < 16; s += 2 {
				n := r
				if s%4 == 2 {
					n = r + 4
				}
				on(pat, synthLaneBass, s, n, s == 0)
			}
			if pat >= 4 {
				on(pat, synthLaneChord, 0, r, false)
				on(pat, synthLaneChord, 10, r, false)
			}
		}
	default:
		// Calm: F major at 72, pads that overlap, a bell and a sub.
		// I vi IV V, then IV I ii V.
		p.BPM, p.Key, p.Scale = 72, 5, 1
		p.DelayTime, p.DelayFB = 4, 0.45
		synthApplyPreset(&p, synthInstDrums, 0)
		synthApplyPreset(&p, synthInstBass, 1)
		synthApplyPreset(&p, synthInstLead, 2)
		synthApplyPreset(&p, synthInstChords, 0)
		p.Mute[synthLaneKick], p.Mute[synthLaneSnare], p.Mute[synthLaneClap] = true, true, true
		p.Drums[2].Volume, p.Drums[2].Decay = 0.18, 0.1
		pad := &p.Tones[synthInstChords]
		pad.Attack, pad.Release, pad.Length, pad.Cutoff, pad.Volume = 0.6, 0.9, 1, 0.34, 0.5
		bass := &p.Tones[synthInstBass]
		bass.Release, bass.Length, bass.Volume = 0.6, 1, 0.7
		p.Tones[synthInstLead].Volume = 0.5
		roots := [synthPatterns]int{0, 5, 3, 4, 3, 0, 1, 4}
		bellA := [4]int{4, 7, 9, 7}
		bellB := [4]int{9, 8, 7, 4}
		for pat, r := range roots {
			on(pat, synthLaneChord, 0, r, false)
			on(pat, synthLaneChord, 8, r, false)
			on(pat, synthLaneBass, 0, r, false)
			on(pat, synthLaneBass, 8, r, false)
			bell := bellA
			if pat >= 4 {
				bell = bellB
			}
			for i, d := range bell {
				on(pat, synthLaneLead, 2+4*i, r+d, i == 0)
			}
			if pat%4 == 3 {
				for s := 0; s < 16; s += 4 {
					on(pat, synthLaneHat, s+2, 0, false)
				}
			}
		}
	}
	return p
}

// ── Library stream ───────────────────────────────────────────────────────

// bgmLibStream decodes one library song through ffmpeg; a reader goroutine
// keeps a few chunks ready so a slow card never stalls the device loop.
type bgmLibStream struct {
	cmd    *exec.Cmd
	chunks chan []byte
	cur    []byte
	rate   int
	path   string
	start  float64
	frames int64
	ended  bool
	got    bool
	once   sync.Once
}

// bgmLibraryTracks lists the library's songs (not videos, not audiobooks),
// with the downloads folder as the fallback for a library never scanned.
func bgmLibraryTracks() []string {
	mlibLoad()
	var out []string
	mlibState.mu.Lock()
	for _, it := range mlibState.items {
		if !it.Video && !it.Book && isAudioFile(it.Path) {
			out = append(out, it.Path)
		}
	}
	mlibState.mu.Unlock()
	if len(out) == 0 {
		for _, f := range audioFilesIn(downloadsMusicDir()) {
			out = append(out, f.Path)
		}
	}
	return out
}

// bgmSelectedTracks reads the chosen file or walks the chosen folder. It does
// not need a Media library scan, and a folder can live anywhere Files browses.
func bgmSelectedTracks(path string) ([]string, string) {
	if path == "" {
		return nil, "Choose a music file or folder"
	}
	clean, err := cleanUserPath(path)
	if err != nil || !filepath.IsAbs(clean) {
		return nil, "The selected music path is invalid"
	}
	info, err := os.Stat(clean)
	if err != nil {
		return nil, "The selected music is no longer available"
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() && isAudioFile(clean) {
			return []string{clean}, ""
		}
		return nil, "The selected file is not playable music"
	}
	var tracks []string
	visited := 0
	_ = filepath.WalkDir(clean, func(p string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // one unreadable subfolder must not hide the rest
		}
		visited++
		if visited > mlibMaxFiles {
			return io.EOF // bound a mistaken whole-drive selection
		}
		if entry.IsDir() {
			if p != clean && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && isAudioFile(p) {
			tracks = append(tracks, p)
		}
		return nil
	})
	if len(tracks) == 0 {
		return nil, "No songs in the selected folder"
	}
	return tracks, ""
}

// bgmOpenLibrary starts the current library or selected song where it was left, or
// returns why it cannot.
func bgmOpenLibrary(rate int) (*bgmLibStream, string) {
	ffmpeg := getToolPath("ffmpeg", appConfig)
	if st, err := os.Stat(ffmpeg); ffmpeg == "" || err != nil || st.Size() == 0 {
		return nil, "Your music needs ffmpeg (open the music player once to install it)"
	}
	bgm.mu.Lock()
	if bgm.libFails >= 5 {
		bgm.mu.Unlock()
		return nil, "Your music could not be played"
	}
	if len(bgm.lib) == 0 || bgm.libIdx >= len(bgm.lib) {
		bgm.mu.Unlock()
		generation := bgmGen.Load()
		choice, _ := bgmChoiceState.Load().(bgmChoice)
		var tracks []string
		why := "No songs in your music library"
		if choice.source == bgmSrcSelected {
			tracks, why = bgmSelectedTracks(choice.path)
		} else {
			tracks = bgmLibraryTracks()
		}
		if len(tracks) == 0 {
			return nil, why
		}
		rand.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
		bgm.mu.Lock()
		if generation != bgmGen.Load() {
			bgm.mu.Unlock()
			return nil, "Music changed"
		}
		bgm.lib, bgm.libIdx, bgm.libPos = tracks, 0, 0
	}
	if len(bgm.lib) == 0 {
		bgm.mu.Unlock()
		return nil, "No songs in your music library"
	}
	path, pos := bgm.lib[bgm.libIdx], bgm.libPos
	bgm.mu.Unlock()

	args := []string{"-nostdin", "-v", "error"}
	if pos > 0.5 {
		args = append(args, "-ss", strconv.FormatFloat(pos, 'f', 2, 64))
	}
	args = append(args, "-i", path, "-vn", "-f", "s16le", "-acodec", "pcm_s16le",
		"-ac", "2", "-ar", strconv.Itoa(rate), "pipe:1")
	cmd := execCommand(ffmpeg, args...)
	if cmd == nil {
		return nil, "ffmpeg could not start"
	}
	cmd.Stderr = nil
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "ffmpeg could not start"
	}
	if err := cmd.Start(); err != nil {
		return nil, "ffmpeg could not start"
	}
	s := &bgmLibStream{cmd: cmd, chunks: make(chan []byte, 8), rate: rate, path: path, start: pos}
	go func() {
		defer close(s.chunks)
		for {
			buf := make([]byte, 16*1024)
			n, err := io.ReadFull(out, buf)
			if n > 0 {
				s.chunks <- buf[:n&^3]
			}
			if err != nil {
				return
			}
		}
	}()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	bgm.mu.Lock()
	bgm.nowPlaying = name
	bgm.mu.Unlock()
	return s, ""
}

func (s *bgmLibStream) fill(out []float32, frames int) bool {
	for i := 0; i < frames; i++ {
		if len(s.cur) < 4 {
			s.cur = nil
			if s.ended {
				out[2*i], out[2*i+1] = 0, 0
				continue
			}
			select {
			case c, ok := <-s.chunks:
				if !ok {
					s.ended = true
				} else {
					s.cur = c
					s.got = true
				}
			default:
			}
			if len(s.cur) < 4 {
				// Nothing decoded yet (or the end): silence for the rest.
				for ; i < frames; i++ {
					out[2*i], out[2*i+1] = 0, 0
				}
				break
			}
		}
		out[2*i] = float32(int16(binary.LittleEndian.Uint16(s.cur[0:]))) / 32768
		out[2*i+1] = float32(int16(binary.LittleEndian.Uint16(s.cur[2:]))) / 32768
		s.cur = s.cur[4:]
		s.frames++
	}
	if s.ended {
		bgm.mu.Lock()
		switch {
		case s.got:
			bgm.libIdx++
			bgm.libFails = 0
		case s.start > 0:
			// Resumed at (or past) its end: the song is fine, it is just over.
			bgm.libIdx++
		default:
			// Nothing decoded from the top: an unreadable file. Drop it so the
			// shuffle does not try it again and again.
			if bgm.libIdx < len(bgm.lib) && bgm.lib[bgm.libIdx] == s.path {
				bgm.lib = append(bgm.lib[:bgm.libIdx:bgm.libIdx], bgm.lib[bgm.libIdx+1:]...)
			}
			bgm.libFails++
		}
		bgm.libPos = 0
		bgm.mu.Unlock()
		return false
	}
	return true
}

// skip ends this song early; the loop opens the next.
// fill's end-of-song path then moves the shuffle on.
func (s *bgmLibStream) skip() {
	s.kill()
	s.got = true
	s.ended = true
	s.cur = nil
}

// close stops the decoder and remembers where the song was.
func (s *bgmLibStream) close() {
	if !s.ended {
		pos := s.start + float64(s.frames)/float64(s.rate)
		bgm.mu.Lock()
		if bgm.libIdx < len(bgm.lib) && bgm.lib[bgm.libIdx] == s.path {
			bgm.libPos = pos
		}
		bgm.mu.Unlock()
	}
	s.kill()
}

func (s *bgmLibStream) kill() {
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		// Drain so the reader goroutine can finish, then reap ffmpeg.
		go func() {
			for range s.chunks {
			}
			if s.cmd != nil {
				_ = s.cmd.Wait()
			}
		}()
	})
}

// bgmSkip is the Settings "Next song" press.
func bgmSkip() { bgmSkipReq.Store(true) }
