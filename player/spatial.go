package main

// spatial.go — spatial audio ("virtual surround") for everything JukaHub
// itself plays: videos, music, podcasts, YouTube and audiobooks.
//
// All of that audio reaches the device through one function, QueueVideoAudio
// (sound.go): stereo S16 at the device's rate, under videoAudioMutex. That is
// where the DSP (spatial_dsp.go) runs, through spatialQueueHook, and where
// ClearVideoAudioQueue resets it on a seek or a stop (spatialResetLocked).
// Background music has a device of its own and its own instance
// (spatialSideStream, bgmusic.go). Games and apps are separate processes: the
// native plugin in player/spatialfx reaches them, as RetroArch's audio filter
// (spatial_games.go) and, on the handheld, through ALSA for every program
// (spatial_alsa.go); both read the same settings through spatialSettingsFor.
//
// The settings are four Custom variables:
//
//	spatialMode      "off" (default), "auto", "headphones", "speakers"
//	spatialStrength  25..100 percent (default 75)
//	spatialRoom      "small", "medium" (default), "large"
//	spatialGames     true/false (default true): games and apps too, while a
//	                 mode is on (spatial_games.go, spatial_alsa.go)
//
// "auto" follows the output the header names (audiooutput.go): a headset, the
// 3.5 mm jack or Bluetooth headphones get the headphone model, and the built-in
// speakers (or a Bluetooth device whose name says it is a speaker) get the wide
// stereo mode. Both TrimUI models have a stereo pair of speakers, so the speaker
// mode is the same everywhere. Windows has no jack probe, so "auto" there is the
// speaker mode; pick Headphones by hand on a PC.
//
// Bit-perfect (musicpeq.go) means nothing touches the decoded samples, so
// while it is on the DSP stands aside for everything JukaHub plays itself —
// videos too, because QueueVideoAudio cannot tell the two players apart, and a
// lock to ask the music player from under videoAudioMutex would risk a
// deadlock. Choosing a spatial mode switches Bit-perfect off, the way enabling
// the equaliser does. RetroArch games are a separate route (the plugin) and
// keep the filter whatever Bit-perfect says: it is a music-player switch.
//
// Threads: the UI thread owns the settings and publishes an immutable
// snapshot (spatialLive) through spatialBind, which rides bootSfxBind — the
// once-a-second tick, hot reload, restore and pack changes all call it, and
// the panel rows call it at once. The decoder goroutines only ever read that
// snapshot; the DSP instance itself (spatialRun) is touched only with
// videoAudioMutex held.

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// The Custom variables.
const (
	spatialModeKey     = "spatialMode"
	spatialStrengthKey = "spatialStrength"
	spatialRoomKey     = "spatialRoom"
	spatialGamesKey    = "spatialGames"
)

// spatialDefaultStrength is the strength in percent a fresh install uses.
const spatialDefaultStrength = 75

// spatialSettings is the user's choice, as the DSP and the game launcher read
// it.
type spatialSettings struct {
	Mode     string  // "off", "auto", "headphones", "speakers"
	Strength float64 // 0..1
	Room     string  // "small", "medium", "large"
	Games    bool    // also in games and apps (spatial_games.go, spatial_alsa.go)
}

// spatialNormalizeMode maps a stored mode to one of the four names; anything
// unknown is "off".
func spatialNormalizeMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "auto":
		return "auto"
	case "headphones", "headphone", "headset", "binaural", "on", "true":
		return "headphones"
	case "speakers", "speaker", "wide", "stereo":
		return "speakers"
	}
	return "off"
}

// spatialNormalizeRoom maps a stored room to one of the three names; anything
// unknown is "medium".
func spatialNormalizeRoom(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "small":
		return "small"
	case "large", "big":
		return "large"
	}
	return "medium"
}

// spatialSettingsFor reads the four variables, with the defaults for missing
// or malformed ones.
func spatialSettingsFor(config *Config) spatialSettings {
	s := spatialSettings{Mode: "off", Strength: spatialDefaultStrength / 100.0, Room: "medium", Games: true}
	if config == nil {
		return s
	}
	m := config.Variables.Custom
	if v, ok := customValue(m, spatialModeKey); ok {
		s.Mode = spatialNormalizeMode(coerceString(v))
	}
	if v, ok := customValue(m, spatialStrengthKey); ok {
		if pct, ok := bootSfxParsePercent(v); ok {
			s.Strength = float64(pct) / 100.0
		}
	}
	if v, ok := customValue(m, spatialRoomKey); ok {
		s.Room = spatialNormalizeRoom(coerceString(v))
	}
	if v, ok := customValue(m, spatialGamesKey); ok {
		s.Games = bootSfxParseBool(v, true)
	}
	return s
}

// spatialRoomIndex is the DSP's room number: 0 small, 1 medium, 2 large.
func spatialRoomIndex(room string) int {
	switch spatialNormalizeRoom(room) {
	case "small":
		return spatialDSPRoomSmall
	case "large":
		return spatialDSPRoomLarge
	}
	return spatialDSPRoomMedium
}

// spatialEffectiveMode resolves the choice to the DSP mode in force now: 0 off,
// 1 headphones, 2 speakers. "auto" is resolved by the connected output.
func spatialEffectiveMode(s spatialSettings) int {
	if !(s.Strength > 0) {
		return spatialDSPOff
	}
	switch spatialNormalizeMode(s.Mode) {
	case "headphones":
		return spatialDSPHeadphones
	case "speakers":
		return spatialDSPSpeakers
	case "auto":
		return spatialAutoMode(spatialConnectedOutput())
	}
	return spatialDSPOff
}

// spatialAutoMode is "auto" for an output name as audiooutput.go records it:
// "" is the built-in speakers.
func spatialAutoMode(output string) int {
	if strings.TrimSpace(output) == "" || spatialOutputIsSpeaker(output) {
		return spatialDSPSpeakers
	}
	return spatialDSPHeadphones
}

// spatialSpeakerWords mark a Bluetooth audio device that is a loudspeaker, not
// headphones; binaural processing would only colour it.
var spatialSpeakerWords = []string{"speaker", "soundbar", "sound bar", "boombox", "soundlink", "partybox", "sonos", "homepod", "echo dot"}

func spatialOutputIsSpeaker(name string) bool {
	low := strings.ToLower(name)
	for _, w := range spatialSpeakerWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// spatialConnectedOutput is the output audiooutput.go detected. It reads the
// cache directly: connectedAudioOutput hides the name in screenshot mode, which
// must not change what the ears hear.
func spatialConnectedOutput() string {
	audioOutputState.mu.Lock()
	defer audioOutputState.mu.Unlock()
	return audioOutputState.name
}

// ── The live snapshot ─────────────────────────────────────────────────────

// spatialLive is what the decoder goroutines read: immutable once published.
type spatialLive struct {
	mode       int     // spatialDSPOff / Headphones / Speakers, "auto" resolved
	strength   float64 // 0..1
	room       int     // spatialDSPRoom*
	bitPerfect bool    // Bit-perfect is on: the DSP stands aside
}

var spatialLiveState atomic.Pointer[spatialLive]

// spatialSettingsLive is the settings as last bound, for readers off the UI
// thread (spatialChildEnv).
var spatialSettingsLive atomic.Pointer[spatialSettings]

// spatialBind publishes the settings for the decoders. It runs on the UI
// thread from bootSfxBind once a second, on hot reload and on restore, and at
// once from the rows that change a setting: four map lookups (each a scan of
// the Custom map when the key is missing, which is microseconds) and no
// allocation unless something changed, when a new snapshot replaces the old.
func spatialBind(config *Config) {
	if config == nil {
		return
	}
	s := spatialSettingsFor(config)
	live := spatialLive{
		mode:       spatialEffectiveMode(s),
		strength:   s.Strength,
		room:       spatialRoomIndex(s.Room),
		bitPerfect: spatialBitPerfectOn(config),
	}
	// The launches read the settings from this snapshot: they may run on a
	// goroutine, where the config map must not be read (spatial_alsa.go).
	if old := spatialSettingsLive.Load(); old == nil || *old != s {
		p := new(spatialSettings)
		*p = s
		spatialSettingsLive.Store(p)
	}
	// Games and apps go through the plugin by ALSA once a probe has proven
	// that route here (spatial_alsa.go); the kick is a lock and a compare
	// once it has run.
	if s.Games && live.mode != spatialDSPOff {
		spatialSystemRouteKick()
	}
	if old := spatialLiveState.Load(); old != nil && *old == live {
		return
	}
	// A fresh copy, so live itself never escapes and an unchanged bind stays
	// on the stack.
	p := new(spatialLive)
	*p = live
	spatialLiveState.Store(p)
}

// spatialBitPerfectOn reports the music player's Bit-perfect switch as stored.
func spatialBitPerfectOn(config *Config) bool {
	return config != nil && customBool(config.Variables.Custom, musicBitPerfectKey, false)
}

// spatialRunState is the media path's DSP and the bytes it holds back. Only
// QueueVideoAudio and ClearVideoAudioQueue touch it, both with videoAudioMutex
// held.
type spatialRunState struct {
	dsp  *spatialDSP
	rate int
	// While the DSP stands aside, a read that ended mid-frame (the music
	// decoder reads a pipe) leaves its last one to three bytes here, so every
	// buffer handed on starts and ends on a frame boundary — and the DSP,
	// switched on mid-stream, starts on one too instead of pairing the wrong
	// bytes into samples. buf is the reused buffer such a buffer is built in.
	carry  [4]byte
	carryN int
	buf    []byte
}

var spatialRun spatialRunState

// spatialQueueHook runs inside QueueVideoAudio with videoAudioMutex held. It
// returns the bytes to queue and whether the player volume was applied (then the
// caller's own volume loop must be skipped). Off, the bytes come back
// unprocessed and in order; only a trailing partial frame waits for the next
// call (spatialAlignedBypass), so whole-frame input is the caller's own slice,
// untouched.
func spatialQueueHook(pcm []byte, rate int32, volume float64) ([]byte, bool) {
	live := spatialLiveState.Load()
	if live == nil || live.bitPerfect || live.mode == spatialDSPOff || !(live.strength > 0) || rate <= 0 {
		if d := spatialRun.dsp; d != nil {
			// The partial frame the DSP held leads the stream on. The DSP itself
			// goes: switched on again later, it starts clean instead of replaying
			// its old delay lines, filter states and reverb tail into new audio.
			spatialRun.carry, spatialRun.carryN = d.carry, d.carryN
			spatialRun.dsp = nil
		}
		return spatialAlignedBypass(pcm), false
	}
	d := spatialRun.dsp
	// The room only shapes the headphone model; in the speakers mode a room
	// change has nothing to rebuild (and a rebuild would reset the side filter,
	// a click for no change).
	if d == nil || spatialRun.rate != int(rate) || d.mode != live.mode ||
		(live.mode == spatialDSPHeadphones && d.room != live.room) {
		next := newSpatialDSP(int(rate), live.mode, live.strength, live.room)
		if d != nil {
			// A partial frame belongs to the stream, not to the old settings.
			next.carry, next.carryN = d.carry, d.carryN
			next.out = d.out[:0]
		} else {
			next.carry, next.carryN = spatialRun.carry, spatialRun.carryN
			spatialRun.carryN = 0
		}
		spatialRun.dsp = next
		spatialRun.rate = int(rate)
		d = next
	} else if d.strength != live.strength {
		d.setStrength(live.strength)
	}
	return d.processS16(pcm, volume), true
}

// spatialSideStream is a DSP instance for an output with a device of its own
// (background music, bgmusic.go), owned by that output's goroutine. It follows
// the same live snapshot as the media path.
type spatialSideStream struct {
	dsp  *spatialDSP
	rate int
}

// ready brings the instance in line with the live settings and returns it, or
// nil while spatial audio stands aside (the samples then go out untouched).
func (st *spatialSideStream) ready(rate int) *spatialDSP {
	live := spatialLiveState.Load()
	if live == nil || live.bitPerfect || live.mode == spatialDSPOff || !(live.strength > 0) || rate <= 0 {
		st.dsp = nil
		return nil
	}
	d := st.dsp
	if d == nil || st.rate != rate || d.mode != live.mode || (live.mode == spatialDSPHeadphones && d.room != live.room) {
		d = newSpatialDSP(rate, live.mode, live.strength, live.room)
		st.dsp, st.rate = d, rate
	} else if d.strength != live.strength {
		d.setStrength(live.strength)
	}
	return d
}

// frameGain runs one stereo frame with gain g applied before the limiter, as
// the S16 path applies the player volume.
func (d *spatialDSP) frameGain(l, r, g float64) (float64, float64) {
	ol, or := d.processRaw(l, r)
	return spatialLimit(ol * g), spatialLimit(or * g)
}

// spatialAlignedBypass hands pcm on unprocessed while the DSP stands aside,
// keeping a trailing partial frame back for the next call (spatialRunState).
// With nothing held it returns pcm itself (cut to whole frames), so the common
// case copies nothing; only bytes held from the last call need the buffer.
func spatialAlignedBypass(pcm []byte) []byte {
	r := &spatialRun
	if r.carryN == 0 {
		whole := len(pcm) &^ 3
		r.carryN = copy(r.carry[:], pcm[whole:])
		return pcm[:whole]
	}
	whole := (r.carryN + len(pcm)) &^ 3
	if whole == 0 {
		r.carryN += copy(r.carry[r.carryN:], pcm)
		return pcm[:0]
	}
	if cap(r.buf) < whole {
		r.buf = make([]byte, whole, whole+whole/4+64)
	}
	out := r.buf[:whole]
	n := copy(out, r.carry[:r.carryN])
	used := copy(out[n:], pcm)
	r.carryN = copy(r.carry[:], pcm[used:])
	return out
}

// spatialResetLocked clears the DSP for a new stream (a seek, a stop, the next
// track) and drops any held partial frame: the new stream starts on a frame
// boundary. Called by ClearVideoAudioQueue with videoAudioMutex held.
func spatialResetLocked() {
	if spatialRun.dsp != nil {
		spatialRun.dsp.reset()
	}
	spatialRun.carryN = 0
}

// ── Writing the settings ──────────────────────────────────────────────────

// spatialStore writes one variable, saves the settings and publishes the new
// snapshot at once.
func spatialStore(config *Config, key string, value interface{}) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[key] = value
	saveUserConfigDebounced(userConfigFromVariables(config))
	spatialBind(config)
}

// spatialModeNames is the order the mode row cycles through.
var spatialModeNames = []string{"off", "auto", "headphones", "speakers"}

// spatialModeLabel is what a row shows for a mode name.
func spatialModeLabel(mode string) string {
	switch spatialNormalizeMode(mode) {
	case "auto":
		return "Auto"
	case "headphones":
		return "Headphones"
	case "speakers":
		return "Speakers"
	}
	return "Off"
}

// spatialModeToast is the confirmation after the mode changed.
func spatialModeToast(mode string) string {
	switch spatialNormalizeMode(mode) {
	case "auto":
		if IsWindows() {
			// A PC has no jack probe here (audiooutput.go), so Auto stays on
			// the speakers mode; promising headphone detection would be wrong.
			return "Spatial audio: Auto — wider stereo; a PC cannot tell when headphones are in, so pick Headphones then"
		}
		return "Spatial audio: Auto — virtual surround on headphones, wider stereo on the speakers"
	case "headphones":
		return "Spatial audio: Headphones — virtual surround"
	case "speakers":
		return "Spatial audio: Speakers — wider stereo"
	}
	return "Spatial audio off"
}

// spatialSetMode stores a mode. Any mode but off switches Bit-perfect off,
// which would otherwise keep the DSP out of the signal. It returns the toast.
func spatialSetMode(config *Config, mode string) string {
	mode = spatialNormalizeMode(mode)
	msg := spatialModeToast(mode)
	if mode != "off" && spatialBitPerfectOn(config) {
		// The equaliser state is saved as a whole (musicPEQSave), so it must be
		// the stored one before it is written back, or a curve the player never
		// loaded this session would be saved flat. musicSetBitPerfect also
		// re-opens the music decoder, so the change is heard at once.
		musicPEQEnsureLoaded()
		musicSetBitPerfect(false, config)
		msg += " (Bit-perfect is off now)"
	}
	spatialStore(config, spatialModeKey, mode)
	return msg
}

// spatialCycleMode steps the mode row: dir > 0 forward, dir < 0 back.
func spatialCycleMode(config *Config, dir int) string {
	cur := spatialNormalizeMode(spatialSettingsFor(config).Mode)
	idx := 0
	for i, name := range spatialModeNames {
		if name == cur {
			idx = i
		}
	}
	n := len(spatialModeNames)
	if dir < 0 {
		idx = (idx + n - 1) % n
	} else {
		idx = (idx + 1) % n
	}
	return spatialSetMode(config, spatialModeNames[idx])
}

// spatialStrengthSteps are the stops of the strength row, in percent.
var spatialStrengthSteps = []int{25, 50, 75, 100}

// spatialStrengthPercent is the stored strength rounded to a whole percent.
func spatialStrengthPercent(config *Config) int {
	return spatialPercent(spatialSettingsFor(config).Strength)
}

// spatialPercent turns a 0..1 strength into a whole percent.
func spatialPercent(strength float64) int {
	return int(strength*100 + 0.5)
}

// spatialNextStrength is the stop after cur in the direction of dir: the first
// stop above it (dir > 0) or the last one below it, clamped at the ends, so a
// hand-written 60 steps to 75 or 50.
func spatialNextStrength(cur, dir int) int {
	steps := spatialStrengthSteps
	if dir > 0 {
		for _, v := range steps {
			if v > cur {
				return v
			}
		}
		return steps[len(steps)-1]
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i] < cur {
			return steps[i]
		}
	}
	return steps[0]
}

// spatialStepStrength moves the strength row one stop and returns the new
// percentage. It is stored as a string, like the other percentages in Custom
// (bootSfxSetPrefs, musicSetArtOpacity); bootSfxParsePercent reads either.
func spatialStepStrength(config *Config, dir int) int {
	pct := spatialNextStrength(spatialStrengthPercent(config), dir)
	spatialStore(config, spatialStrengthKey, strconv.Itoa(pct))
	return pct
}

// spatialRoomNames is the order the room row cycles through.
var spatialRoomNames = []string{"small", "medium", "large"}

func spatialRoomLabel(room string) string {
	switch spatialNormalizeRoom(room) {
	case "small":
		return "Small"
	case "large":
		return "Large"
	}
	return "Medium"
}

// spatialCycleRoom steps the room row and returns the new room's name.
func spatialCycleRoom(config *Config, dir int) string {
	cur := spatialNormalizeRoom(spatialSettingsFor(config).Room)
	idx := 1
	for i, name := range spatialRoomNames {
		if name == cur {
			idx = i
		}
	}
	n := len(spatialRoomNames)
	if dir < 0 {
		idx = (idx + n - 1) % n
	} else {
		idx = (idx + 1) % n
	}
	spatialStore(config, spatialRoomKey, spatialRoomNames[idx])
	return spatialRoomNames[idx]
}

// spatialModeValue is the mode row's read-out: the choice, what Auto resolved
// to, and whether Bit-perfect is holding the DSP back.
func spatialModeValue(config *Config) string {
	return spatialModeValueOf(spatialSettingsFor(config), spatialBitPerfectOn(config))
}

// spatialModeValueOf is spatialModeValue for settings already read.
func spatialModeValueOf(s spatialSettings, bitPerfect bool) string {
	mode := spatialNormalizeMode(s.Mode)
	label := spatialModeLabel(mode)
	if mode == "auto" {
		switch spatialEffectiveMode(s) {
		case spatialDSPHeadphones:
			label = "Auto (headphones)"
		case spatialDSPSpeakers:
			label = "Auto (speakers)"
		}
	}
	if mode != "off" && bitPerfect {
		label += " · bypassed"
	}
	return label
}

// spatialRoomHeard reports whether the room setting shapes what is heard now:
// only the headphone model has a room.
func spatialRoomHeard(s spatialSettings) bool {
	return spatialEffectiveMode(s) == spatialDSPHeadphones
}

// spatialStatusLine is a one-line summary for other screens (the Settings row,
// which asks on every frame it draws): the settings are read once.
func spatialStatusLine(config *Config) string {
	s := spatialSettingsFor(config)
	if spatialNormalizeMode(s.Mode) == "off" {
		return "Virtual surround on headphones, wider stereo on the speakers"
	}
	if spatialBitPerfectOn(config) {
		return "Bypassed while Bit-perfect is on (Tools → Audio)"
	}
	if !spatialRoomHeard(s) {
		return fmt.Sprintf("%s · %d%% — more in Tools → Audio", spatialModeValueOf(s, false), spatialPercent(s.Strength))
	}
	return fmt.Sprintf("%s · %d%% · %s room — more in Tools → Audio", spatialModeValueOf(s, false), spatialPercent(s.Strength), spatialRoomLabel(s.Room))
}
