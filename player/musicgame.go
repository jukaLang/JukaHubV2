package main

// musicgame.go — the music keeps playing while a game runs.
//
// A DAP that is also a game console plays its music under the game. Before
// this file a game launch closed every JukaHub output (audioRouteReleaseForChild):
// the game must be able to open the sound card, and on the handhelds the card
// takes one client (the Brick's speaker stayed busy while JukaHub held it).
// The music decoder then had nowhere to play — and, with nothing pacing it,
// ran through the playlist in silence.
//
// ALSA's dmix plugin lets several programs share one card: each opens the
// same dmix definition (the same ipc_key) and the plugin sums them into the
// card. The first program to open it sets the card's buffer for everyone.
// alsa-lib's stock "dmix:CARD=<id>,DEV=0" asks for 16 periods of 1024 frames,
// about 341 ms at 48 kHz — a game played through that lags its own sound. So
// JukaHub defines its own, "jhgamemix" (musicGameOutText): hw on the card,
// 48 kHz S16_LE stereo, 512-frame periods in a 2048-frame buffer (about
// 43 ms; dmix takes the nearest the codec accepts), its own ipc_key, the key
// not per user and the memory open to every user (0666), so a game running as
// another user still joins. The definition sits in the child's file, and
// JukaHub's own open names the same file (outGameMixBegin's conf), so both
// mean exactly the same mix. If that open fails, the stock dmix is tried
// before giving up (musicGameMixOpen). So when music plays at a launch and
// "Keep music playing in games" is on (the default):
//
//   - the child's ALSA setup (the same mechanism as spatial_alsa.go and the
//     headset route: ALSA_CONFIG_PATH = the system's alsa.conf plus files in
//     /tmp/jukahub-audio, hook 1 overriding "default" after the firmware's
//     own files) points "default" and AUDIODEV at "jhgameout", a plug in
//     front of that dmix. With spatial audio for games armed, the spatial
//     chain ends in jhgameout instead of the card (spatialALSALaunchConf).
//     A 32-bit program is left as it was and the music pauses
//     (musicGameNoteChild): its own alsa-lib may not share the 64-bit mix.
//   - at the hand-off, after every JukaHub output is closed and before the
//     game starts, JukaHub opens the same dmix itself and its mixer plays only
//     the music's virtual device there (outGameMixBegin, audioroute.go), at
//     "Music volume in games" (default 60 %) on JukaHub's side.
//   - that open is the probe: when it fails, jhgameout is rewritten to what
//     the game would have had anyway (the card's own default, or the USB
//     DAC's plughw route) before the game starts, and the music pauses —
//     the old behaviour, minus the silent run through the playlist.
//
// The card is the active output's: a routed USB DAC (usbdac.go), else the
// speaker card when "default" is the card's own (no firmware file redefines
// it — the spatial route's rule; a redefined "default" may carry the
// firmware's own volume or routing that a raw dmix would skip). A Bluetooth
// headset is not shared: BlueALSA takes one client per PCM (audioroute.go),
// so the music pauses there, and says why.
//
// A launch with no music playing is untouched: no plan, no files, no env.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// musicGameKeepKey and musicGameLevelKey are the two settings
	// (Audio settings: "Keep music playing in games", "Music volume in games").
	musicGameKeepKey  = "musicInGames"
	musicGameLevelKey = "musicInGamesLevel"
	// musicGameLevelDefault leaves the game's own sound on top.
	musicGameLevelDefault = 60
	// musicGameOutPCM is the PCM the child's "default" plays through.
	musicGameOutPCM = "jhgameout"
	// musicGameMixPCM is JukaHub's own dmix, defined in the out file beside
	// jhgameout; musicGameMixIPCKey its shared-memory key ("JHGM"), apart
	// from alsa-lib's default 5678293 so it never meets the stock mix.
	musicGameMixPCM    = "jhgamemix"
	musicGameMixIPCKey = 1246250829
	// musicGameMixPeriod and musicGameMixBuffer are its slave's sizes in
	// frames at 48 kHz: ~10.7 ms periods, ~43 ms in all.
	musicGameMixPeriod = 512
	musicGameMixBuffer = 2048
	// The child's files: the jhgameout definition, the hook file and the
	// "default" its hook loads (audioChildConfDir, in RAM).
	musicGameOutName  = "asound-game-out.conf"
	musicGameHookName = "asound-game.conf"
	musicGameDefName  = "asound-game-default.conf"
	// musicGamePlanReuse is how long one launch's plan answers both of its
	// callers (spatial, then the headset/route step); musicGamePlanTTL how
	// long the hand-off may follow the environment.
	musicGamePlanReuse = 3 * time.Second
	musicGamePlanTTL   = 30 * time.Second
)

// musicGamePlan is one launch's decision.
type musicGamePlan struct {
	why      string  // "" when the music can stay; else why it pauses
	mixPCM   string  // what JukaHub opens: jhgamemix, or the stock mix once it took over
	stockMix string  // alsa-lib's "dmix:CARD=<id>,DEV=0", tried when jhgamemix will not open
	fallback string  // what jhgameout becomes when no mix opens
	gain     float64 // the music's level under the game
	outFile  string  // the jhgameout definition
	hookFile string  // the hook file naming the "default" file
	at       time.Time
	applied  bool // the child's environment names outFile
	// chanOnly marks a plan made only for Audio channels (audiochannels.go):
	// the game's "default" goes through the downmix, no mix is shared, and
	// music that played stays paused for musicWhy.
	chanOnly bool
	musicWhy string
}

var musicGame struct {
	mu   sync.Mutex
	plan *musicGamePlan
	// live is the plan the music plays under now, for a second child started
	// while it does.
	live *musicGamePlan
}

// ── Settings ─────────────────────────────────────────────────────────────

// musicGameKeepEnabled reports "Keep music playing in games" (on unless off).
func musicGameKeepEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, musicGameKeepKey, true)
}

func musicGameSetKeep(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicGameKeepKey] = boolString(on)
	musicGameBind(config)
}

// musicGameLevel is "Music volume in games", 10..100 percent.
func musicGameLevel(config *Config) int {
	if config == nil {
		return musicGameLevelDefault
	}
	v, err := strconv.Atoi(strings.TrimSpace(customStringValue(config.Variables.Custom, musicGameLevelKey)))
	if err != nil {
		return musicGameLevelDefault
	}
	if v < 10 {
		v = 10
	}
	if v > 100 {
		v = 100
	}
	return v
}

func musicGameSetLevel(config *Config, pct int) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicGameLevelKey] = strconv.Itoa(pct)
	musicGameBind(config)
}

// musicGameSettings is the two settings as a launch reads them.
type musicGameSettings struct {
	keep  bool
	level int
}

// musicGameSettingsLive is the snapshot musicGameBind publishes: a launch
// may run on a goroutine, where the config map must not be read (spatial
// audio's settings work the same way, spatial.go).
var musicGameSettingsLive atomic.Pointer[musicGameSettings]

// musicGameBind publishes the settings for the launches. Main thread: once a
// second with the other sound settings (bootSfxBind, sound.go) and at once
// when the Audio settings change them.
func musicGameBind(config *Config) {
	if config == nil {
		return
	}
	// The other launch-time and background sound settings ride along:
	// Audio channels, Tidy the Music folder, Quiet the game while music plays.
	audioChannelsPublish(a11y.audioChannels)
	musicTidyBind(config)
	ingameQuietBind(config)
	s := musicGameSettings{keep: musicGameKeepEnabled(config), level: musicGameLevel(config)}
	if old := musicGameSettingsLive.Load(); old != nil && *old == s {
		return
	}
	musicGameSettingsLive.Store(&s)
}

// musicGameSettingsNow is the published snapshot; the defaults before the
// first bind. Any goroutine.
func musicGameSettingsNow() musicGameSettings {
	if s := musicGameSettingsLive.Load(); s != nil {
		return *s
	}
	return musicGameSettings{keep: true, level: musicGameLevelDefault}
}

// ── The plan ─────────────────────────────────────────────────────────────

// musicGamePlanNow is the plan for the launch being prepared, nil when no
// music plays (then nothing about the launch changes). Both env steps of one
// launch get the same plan. Launch time only: it reads small system files.
func musicGamePlanNow() *musicGamePlan {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return nil
	}
	musicGame.mu.Lock()
	if p := musicGame.plan; p != nil && time.Since(p.at) < musicGamePlanReuse {
		musicGame.mu.Unlock()
		return p
	}
	live := musicGame.live
	musicGame.mu.Unlock()

	var p *musicGamePlan
	if live != nil && audioRouteGameMixActive() {
		// A second child while the music plays under the first: it joins the
		// same mix.
		c := *live
		c.at, c.applied = time.Now(), false
		p = &c
	} else {
		music.mu.Lock()
		playing := music.playing && !music.paused
		music.mu.Unlock()
		if !playing {
			// No music: the launch stays untouched — unless Audio channels
			// downmixes every game (audiochannels.go; nil in Stereo).
			if p = audioChannelsPlanBuild(""); p == nil {
				return nil
			}
		} else {
			p = musicGamePlanBuild()
			if p.why != "" {
				// The music cannot stay; the downmix still applies.
				if c := audioChannelsPlanBuild(p.why); c != nil {
					p = c
				}
			}
		}
	}
	musicGame.mu.Lock()
	musicGame.plan = p
	musicGame.mu.Unlock()
	return p
}

// musicGamePlanBuild decides how the music that plays now can stay, and
// writes the child's files when it can. It may run on a launch goroutine, so
// it reads the settings snapshot musicGameBind publishes, never the config.
func musicGamePlanBuild() *musicGamePlan {
	p := &musicGamePlan{at: time.Now()}
	settings := musicGameSettingsNow()
	if !settings.keep {
		p.why = "Keep music playing in games is off"
		return p
	}
	base := spatialALSABaseConf()
	if base == "" {
		p.why = "this system has no alsa.conf"
		return p
	}
	route := audioRouteChildPCM()
	outAudio.mu.Lock()
	prior := outAudio.gain
	outAudio.mu.Unlock()
	card := ""
	switch {
	case usbDACPCMIs(route):
		card = usbDACCurrent().id
		p.fallback = route
		if prior <= 0 || prior > 1 {
			prior = 1
		}
	case route != "":
		p.why = "a Bluetooth headset plays one app at a time"
		return p
	default:
		if !spatialALSAHasSysdefault(base) {
			p.why = "this system's ALSA has no sysdefault"
			return p
		}
		if f := spatialALSACustomDefault(); f != "" {
			p.why = "the firmware routes its sound itself (" + f + ")"
			return p
		}
		card = musicGameSpeakerCard()
		p.fallback = "sysdefault"
		prior = 1 // the volume keys move the speaker codec itself
	}
	if card == "" || !spatialALSAQuotable(card) || strings.ContainsAny(card, ", ") {
		p.why = "no sound card to share"
		return p
	}
	p.mixPCM, p.stockMix = musicGameMixPCM, "dmix:CARD="+card+",DEV=0"
	p.gain = prior * float64(settings.level) / 100
	if err := musicGameWriteFiles(p, p.mixPCM, card, spatialALSAHasSysdefault(base)); err != nil {
		p.why = "could not write the game's sound setup: " + err.Error()
		return p
	}
	return p
}

// musicGameSpeakerCard is the speaker's ALSA card: the first card the kernel
// lists that is not a USB one ("" when /proc/asound says nothing).
func musicGameSpeakerCard() string {
	raw, err := os.ReadFile(usbDACProcCards)
	if err != nil {
		return ""
	}
	for _, c := range parseAsoundCards(string(raw)) {
		if !strings.EqualFold(c.driver, "USB-Audio") {
			return c.id
		}
	}
	return ""
}

// musicGameOutText is the jhgameout definition, a plug in front of slave,
// and with card set also jhgamemix, JukaHub's own dmix on that card (the
// header says why each value). One file for JukaHub's own open and the
// game's, so both name the very same mix.
func musicGameOutText(slave, card string) string {
	text := "# Written by JukaHub (musicgame.go) for the game it starts while music\n" +
		"# plays: the game's sound goes to the card through a mix JukaHub's music\n" +
		"# shares. Rewritten at every such launch; safe to delete.\n"
	if card != "" {
		text += "pcm." + musicGameMixPCM + " {\n" +
			"\ttype dmix\n" +
			"\tipc_key " + strconv.Itoa(musicGameMixIPCKey) + "\n" +
			"\tipc_key_add_uid false\n" +
			"\tipc_perm 0666\n" +
			"\tslave {\n" +
			"\t\tpcm \"hw:CARD=" + card + ",DEV=0\"\n" +
			"\t\tformat S16_LE\n" +
			"\t\trate 48000\n" +
			"\t\tchannels 2\n" +
			"\t\tperiod_size " + strconv.Itoa(musicGameMixPeriod) + "\n" +
			"\t\tbuffer_size " + strconv.Itoa(musicGameMixBuffer) + "\n" +
			"\t}\n" +
			"}\n"
	}
	// Audio channels other than Stereo: the game plays through the downmix
	// in front of the mix or card (audiochannels.go). JukaHub's own open of
	// jhgamemix skips it; a11yProcessAudio already mapped the music.
	if route := audioChannelsRouteText(audioChannelsPCM, slave, audioChannelsGameNow()); route != "" {
		return text + route + audioChannelsPlugText(musicGameOutPCM)
	}
	return text + "pcm." + musicGameOutPCM + " {\n\ttype plug\n\tslave.pcm \"" + slave + "\"\n}\n"
}

// musicGameWriteFiles writes the three files: the "default" file first, so a
// program reading the hook file always finds it. card is the card jhgamemix
// sits on ("" for none).
func musicGameWriteFiles(p *musicGamePlan, slave, card string, sysdefault bool) error {
	if !spatialALSAQuotable(slave) || (card != "" && !spatialALSAQuotable(card)) {
		return fmt.Errorf("odd PCM name %q", slave)
	}
	if err := os.MkdirAll(audioChildConfDir, 0o755); err != nil {
		return err
	}
	def := filepath.Join(audioChildConfDir, musicGameDefName)
	text := "# Written by JukaHub (musicgame.go): ALSA's \"default\" plays through the mix\n" +
		"# JukaHub's music shares, for the game it starts. Safe to delete.\n"
	if sysdefault {
		text += "pcm.!default {\n\ttype asym\n\tplayback.pcm \"" + musicGameOutPCM + "\"\n\tcapture.pcm \"sysdefault\"\n}\n"
	} else {
		text += "pcm.!default \"" + musicGameOutPCM + "\"\n"
	}
	if err := audioWriteFileAtomic(def, text); err != nil {
		return err
	}
	hook := filepath.Join(audioChildConfDir, musicGameHookName)
	if err := audioWriteFileAtomic(hook, "# Written by JukaHub (musicgame.go). Hook 1 runs after alsa.conf's hook 0\n"+
		"# (asound.conf, .asoundrc), so this \"default\" is loaded last. Safe to delete.\n"+
		"@hooks.1 {\n\tfunc load\n\tfiles [ \""+def+"\" ]\n\terrors false\n}\n"); err != nil {
		return err
	}
	out := filepath.Join(audioChildConfDir, musicGameOutName)
	if err := audioWriteFileAtomic(out, musicGameOutText(slave, card)); err != nil {
		return err
	}
	p.outFile, p.hookFile = out, hook
	return nil
}

// musicGameNoteChild drops the launch's plan for a 32-bit program
// (spatialLaunchLooks32, screengroup_unix.go, before both env steps): its own
// alsa-lib may not find the 64-bit one's dmix definition or share its memory
// layout, and a game without sound is worse than paused music. The child's
// environment then stays exactly what it was.
func musicGameNoteChild(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	p := musicGamePlanNow()
	if p == nil || p.why != "" || !spatialLaunchLooks32(cmd) {
		return
	}
	musicGame.mu.Lock()
	p.why = "a 32-bit program cannot share the music's mix"
	musicGame.mu.Unlock()
}

// musicGameSpatialSlave is spatial audio's way in (spatialALSALaunchConf): the
// PCM its chain should end in and the file that defines it, ok false when the
// music does not stay for this launch.
func musicGameSpatialSlave() (slave, file string, ok bool) {
	p := musicGamePlanNow()
	if p == nil || p.why != "" || p.outFile == "" {
		return "", "", false
	}
	return musicGameOutPCM, p.outFile, true
}

// musicGameChildEnv is the route step for a screen-taking child (the first
// thing audioRouteChildEnv does): with a plan whose music stays, the child's
// "default" and AUDIODEV become jhgameout — unless spatial audio already put
// it at the end of its chain, or the child names its own ALSA setup, in which
// case the music pauses at the hand-off. ok reports that env is final.
func musicGameChildEnv(env []string) ([]string, bool) {
	p := musicGamePlanNow()
	if p == nil || p.why != "" || p.outFile == "" {
		return env, false
	}
	if v, set := childEnvLookup(env, "ALSA_CONFIG_PATH"); set && v != "" {
		applied := strings.Contains(v, p.outFile)
		musicGameMarkApplied(p, applied)
		return env, applied
	}
	if v, set := childEnvLookup(env, "AUDIODEV"); set && v != "" && v != "default" {
		musicGameMarkApplied(p, false)
		return env, false
	}
	base := spatialALSABaseConf()
	if base == "" {
		musicGameMarkApplied(p, false)
		return env, false
	}
	env = childEnvWithout(env, "AUDIODEV")
	env = append(env, "ALSA_CONFIG_PATH="+base+":"+p.outFile+":"+p.hookFile, "AUDIODEV="+musicGameOutPCM)
	musicGameMarkApplied(p, true)
	audioDiagSet(func() {
		audioDiag.child = fmt.Sprintf("%s (music mix %s) at %s", musicGameOutPCM, p.mixPCM, time.Now().Format("15:04:05"))
	})
	return env, true
}

// musicGameMarkApplied records whether the launch's environment carries the
// plan, which the hand-off reads.
func musicGameMarkApplied(p *musicGamePlan, applied bool) {
	musicGame.mu.Lock()
	p.applied = applied
	musicGame.mu.Unlock()
}

// musicGameHold pauses music that plays at a launch for the hand-off
// (audioRouteReleaseForChild, outRouteMu held, before the outputs close):
// from the closes until the shared PCM is open nothing paces its decoder,
// which would race seconds ahead through the track. It reports whether it
// paused; musicGameAtRelease then plays on, or leaves it paused and says why.
func musicGameHold() bool {
	if audioRouteGameMixActive() {
		return false // an overlay ends; the music stays on its mix
	}
	music.mu.Lock()
	defer music.mu.Unlock()
	if !music.playing || music.paused {
		return false
	}
	music.paused = true
	return true
}

// musicGameAtRelease is the hand-off (audioRouteReleaseForChild, outRouteMu
// held, every other output closed): music that played (held, paused by
// musicGameHold) either moves onto the shared PCM, or stays paused.
func musicGameAtRelease(held bool) {
	musicGame.mu.Lock()
	p := musicGame.plan
	musicGame.plan = nil
	applied := p != nil && p.applied
	musicGame.mu.Unlock()
	if !held {
		return
	}
	switch {
	case p == nil || time.Since(p.at) > musicGamePlanTTL:
		musicGamePaused("the game took the sound")
		return
	case p.chanOnly:
		// Audio channels' plan: no shared mix to move onto.
		why := p.musicWhy
		if why == "" {
			why = "the game took the sound"
		}
		musicGamePaused(why)
		return
	case p.why != "":
		musicGamePaused(p.why)
		return
	case !applied:
		musicGamePaused("this program brings its own sound setup")
		return
	}
	videoAudioInitOnce.Do(initVideoAudioDevice)
	if videoAudioOK && musicGameMixOpen(p) {
		musicGame.mu.Lock()
		musicGame.live = p
		musicGame.mu.Unlock()
		music.mu.Lock()
		music.paused = false
		music.mu.Unlock()
		return
	}
	// The probe said no: the game gets what it would have had, and the music
	// waits.
	if err := audioWriteFileAtomic(p.outFile, musicGameOutText(p.fallback, "")); err != nil {
		logWarn("audio", "music under the game: could not restore the game's output: %v", err)
	}
	musicGamePaused("the sound card cannot be shared here")
}

// musicGameMixOpen is the probe: JukaHub opens the mix the game will share,
// its own jhgamemix through the out file that defines it; when that will not
// open (a codec that refuses it, an alsa-lib that rejects a field), alsa-lib's
// stock dmix on the same card, with jhgameout pointed at it first so the game
// joins the mix JukaHub really holds. Before the game starts (the hand-off).
func musicGameMixOpen(p *musicGamePlan) bool {
	if outGameMixBegin(p.mixPCM, p.outFile, videoAudioDev, p.gain) {
		return true
	}
	if p.stockMix == "" || p.mixPCM == p.stockMix {
		return false
	}
	if err := audioWriteFileAtomic(p.outFile, musicGameOutText(p.stockMix, "")); err != nil {
		logWarn("audio", "music under the game: could not switch the game to the stock mix: %v", err)
		return false
	}
	if !outGameMixBegin(p.stockMix, "", videoAudioDev, p.gain) {
		return false
	}
	logInfo("audio", "music under the game: %s would not open; the stock %s carries it (higher latency)", musicGameMixPCM, p.stockMix)
	musicGame.mu.Lock()
	p.mixPCM = p.stockMix
	musicGame.mu.Unlock()
	return true
}

// musicGamePause pauses the music for the game and says why (the toast shows
// once JukaHub is back on screen). Any goroutine.
func musicGamePause(why string) {
	music.mu.Lock()
	if !music.playing || music.paused {
		music.mu.Unlock()
		return
	}
	music.paused = true
	music.mu.Unlock()
	musicGamePaused(why)
}

// musicGamePaused keeps the place of music paused for the game and says why.
func musicGamePaused(why string) {
	music.mu.Lock()
	path, pos, dur := "", 0.0, 0.0
	if music.current >= 0 && music.current < len(music.tracks) {
		path, pos, dur = music.tracks[music.current].Path, music.position, music.duration
	}
	music.mu.Unlock()
	audioRememberPosition(path, pos, dur)
	logInfo("music", "music paused for the game: %s", why)
	showToast("Music paused for the game: "+why, ToastInfo())
}

// ── In-game menu ──────────────────────────────────────────────────────────

// musicGameMenuState is the in-game menu's Music row: the title and whether
// it plays, ok false when there is no track (no row then).
func musicGameMenuState() (title string, paused bool, ok bool) {
	music.mu.Lock()
	defer music.mu.Unlock()
	if !music.playing || music.current < 0 || music.current >= len(music.tracks) {
		return "", false, false
	}
	t := music.tracks[music.current]
	title = strings.TrimSuffix(t.Name, filepath.Ext(t.Name))
	if title == "" {
		title = t.Name
	}
	return title, music.paused, true
}
