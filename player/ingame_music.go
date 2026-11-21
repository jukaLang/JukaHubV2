package main

// ingame_music.go — the in-game menu's Music panel, and the game kept quiet
// while the music plays.
//
// The menu's Music row (gamemenu.go; there while JukaHub has a track, playing
// or paused) opens a small now-playing panel over the paused game: the song
// and artist, Left/Right previous and next, A play or pause, B back to the
// menu. Left/Right also work on the row itself. The controls are the music
// player's own (musicTogglePause, musicPrev, musicNext).
//
// Music can only sound under a game that shares its sound with JukaHub: one
// started while music played (musicgame.go's shared mix). Under any other
// game the card is the game's alone, so the panel says so instead of
// starting music that would fall silent the moment the game resumes.
//
// "Quiet the game while music plays" (Music settings, on unless off): music
// started from the panel over a RetroArch game the library launched mutes the
// game — one MUTE network command (RetroArch's toggle) — so the music is what
// you hear. JukaHub remembers that it muted, and toggles back exactly once:
// when the music is paused or ends (the panel at once; otherwise a watch
// notices within half a second, the game running or not). A RetroArch whose
// config already says muted is never toggled, which would unmute it. If the
// game ends while muted, RetroArch may have saved the mute into retroarch.cfg
// on its way out (config_save_on_exit), which would start every later game
// silent: once RetroArch is gone, audio_mute_enable goes back to false in
// the configs where JukaHub's mute put it.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// ingameQuietKey is "Quiet the game while music plays" (on unless "false").
	ingameQuietKey = "musicQuietGame"
	// ingameMuteWatchEvery is how often the watch looks at the music and game.
	ingameMuteWatchEvery = 500 * time.Millisecond
	// ingameMuteExitWait bounds the wait for RetroArch to finish quitting
	// before its config is checked.
	ingameMuteExitWait = 15 * time.Second
	// ingameMuteCfgKey is RetroArch's own mute setting.
	ingameMuteCfgKey = "audio_mute_enable"
)

var (
	ingameQuietOn    atomic.Bool
	ingameQuietBound atomic.Bool
)

// ingameQuietEnabled reports the setting.
func ingameQuietEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, ingameQuietKey, true)
}

// ingameQuietSet stores the setting and saves it. Switching it off while the
// game is muted gives the game its sound back at once.
func ingameQuietSet(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[ingameQuietKey] = boolString(on)
	ingameQuietBind(config)
	saveUserConfigDebounced(userConfigFromVariables(config))
	if !on {
		ingameMuteApply(false, 0, "")
	}
}

// ingameQuietBind publishes the setting for the watch. Main thread
// (musicGameBind, once a second).
func ingameQuietBind(config *Config) {
	if config == nil {
		return
	}
	ingameQuietOn.Store(ingameQuietEnabled(config))
	ingameQuietBound.Store(true)
}

// ingameQuietNow is the published setting (on before the first bind).
func ingameQuietNow() bool {
	if !ingameQuietBound.Load() {
		return true
	}
	return ingameQuietOn.Load()
}

// ── The panel ────────────────────────────────────────────────────────────────

var ingameMusic struct {
	open bool
	// at is the overlay opening the panel belongs to: a new opening of the
	// in-game menu starts on the menu, not on a panel left open last time.
	at time.Time
}

// ingameMusicActive reports whether the panel is up. Main thread.
func ingameMusicActive() bool {
	return ingameMusic.open && gaOverlay.active && gaOverlay.mode == gaModeMenu &&
		ingameMusic.at.Equal(gaOverlay.openedAt)
}

// ingameMusicOpen shows the panel (the Music row's A).
func ingameMusicOpen() {
	ingameMusic.open = true
	ingameMusic.at = gaOverlay.openedAt
	PlayActivateSound()
}

// ingameMusicNow is what the panel shows: the song, its artist when the media
// library knows the file, and whether it is paused. ok false with no track.
func ingameMusicNow() (title, artist string, paused, ok bool) {
	music.mu.Lock()
	if !music.playing || music.current < 0 || music.current >= len(music.tracks) {
		music.mu.Unlock()
		return "", "", false, false
	}
	t := music.tracks[music.current]
	paused = music.paused
	music.mu.Unlock()
	title = t.Name
	if isAudioFile(title) {
		title = strings.TrimSuffix(title, filepath.Ext(title))
	}
	if m := mlibCurrentModel(); m != nil {
		if i, found := m.byPath[dlPathKey(t.Path)]; found && i >= 0 && i < len(m.items) {
			artist = strings.TrimSpace(m.items[i].Artist)
			if s := strings.TrimSpace(m.items[i].Title); s != "" {
				title = s
			}
		}
	}
	if strings.TrimSpace(title) == "" {
		title = filepath.Base(t.Path)
	}
	return title, artist, paused, true
}

// ingameMusicRowLabel is the menu row's words (gmLabel).
func ingameMusicRowLabel() (string, string) {
	title, _, paused, ok := ingameMusicNow()
	if !ok {
		return "Music", ""
	}
	if paused {
		return "Music", "Paused · " + title
	}
	return "Music", title
}

// ingameMusicCanPlay reports whether music can sound under this game; when it
// cannot, it says why.
func ingameMusicCanPlay() bool {
	if audioRouteGameMixActive() {
		return true
	}
	PlayErrorSound()
	showToast("Music plays under a game when it was already playing as the game started", ToastInfo())
	return false
}

// ingameMusicStep is previous (dir < 0) or next, from the row or the panel.
func ingameMusicStep(dir int) {
	if !ingameMusicCanPlay() {
		return
	}
	before := ingameMusicAudible()
	if dir < 0 {
		musicPrev()
	} else {
		musicNext()
	}
	ingameMuteSync(!before && ingameMusicAudible())
}

// ingameMusicToggle is play / pause.
func ingameMusicToggle() {
	_, _, paused, ok := ingameMusicNow()
	if !ok {
		ingameMusic.open = false
		return
	}
	if paused && !ingameMusicCanPlay() {
		return
	}
	musicTogglePause() // plays its own click
	ingameMuteSync(paused && ingameMusicAudible())
}

// ingameMusicAudible reports whether JukaHub's music sounds now: a track, not
// paused. Any goroutine.
func ingameMusicAudible() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.playing && !music.paused
}

// ingameMusicPress acts on one press while the panel is up (gmPress).
func ingameMusicPress(b gaButton) {
	if _, _, _, ok := ingameMusicNow(); !ok {
		ingameMusic.open = false // the music was stopped meanwhile
		return
	}
	switch b {
	case gaBtnB, gaBtnMenu:
		PlayBackSound()
		ingameMusic.open = false
	case gaBtnMenuChord:
		// Select + L2 again: straight back to the game.
		PlayBackSound()
		ingameMusic.open = false
		gaClose("the player went back", true)
	case gaBtnA:
		ingameMusicToggle()
	case gaBtnLeft:
		ingameMusicStep(-1)
	case gaBtnRight:
		ingameMusicStep(1)
	}
}

// ingameMusicDraw is the panel: a card like the menu's, with the song, its
// artist, what the game's sound is doing, and the three controls.
func ingameMusicDraw(renderer *sdl.Renderer, fonts tbxFonts) {
	title, artist, paused, ok := ingameMusicNow()
	if !ok {
		return
	}
	pad := orbPad
	w := screenWidth * 42 / 100
	if minW := scaleX(420, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	inner := w - 2*pad

	state := "Playing"
	if paused {
		state = "Paused"
	}
	note := ""
	switch {
	case !audioRouteGameMixActive():
		note = "Music plays under a game only when it was playing as the game started"
	case ingameMuteIsOn():
		note = "The game is quiet while the music plays"
	}
	hint := "Left/Right previous / next · " + orbKeyName("A") + " play/pause · " + orbKeyName("B") + " back"

	btnH := fonts.bodyH + pad
	h := pad + fonts.tinyH + 2 + fonts.bodyH
	if artist != "" {
		h += 2 + fonts.smallH
	}
	h += 2 + fonts.smallH // state
	if note != "" {
		h += 2 + fonts.smallH
	}
	h += pad + btnH + pad + fonts.smallH + pad

	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	accent := glassAccent()
	x, y := card.X+pad, card.Y+pad
	drawText(renderer, fonts.tiny, "MUSIC", x, y, accent, textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, ellipsize(fonts.body, title, inner), x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH
	if artist != "" {
		drawText(renderer, fonts.small, ellipsize(fonts.small, artist, inner), x, y+2, ColorTextSecondary(), textAlignLeft)
		y += 2 + fonts.smallH
	}
	drawText(renderer, fonts.small, state, x, y+2, ColorTextSecondary(), textAlignLeft)
	y += 2 + fonts.smallH
	if note != "" {
		drawText(renderer, fonts.small, ellipsize(fonts.small, note, inner), x, y+2, ColorTextSecondary(), textAlignLeft)
		y += 2 + fonts.smallH
	}
	y += pad

	// Previous · Play/Pause · Next: the middle one is A's, so it is lit.
	middle := "Pause"
	if paused {
		middle = "Play"
	}
	labels := [3]string{"Previous", middle, "Next"}
	gap := pad / 2
	bw := (inner - 2*gap) / 3
	for i, label := range labels {
		bx := x + int32(i)*(bw+gap)
		f := accent
		f.A = 28
		if i == 1 {
			f.A = 72
		}
		fillRoundedRect(renderer, bx, y, bw, btnH, orbBubbleRadius-4, f)
		if i == 1 {
			strokeRoundedRect(renderer, bx, y, bw, btnH, orbBubbleRadius-4, 2, accent)
		}
		drawText(renderer, fonts.body, ellipsize(fonts.body, label, bw-pad/2), bx+bw/2, y+(btnH-fonts.bodyH)/2, ColorTextPrimary(), textAlignCenter)
	}
	y += btnH + pad
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
}

// ── The game kept quiet ──────────────────────────────────────────────────────

var ingameMute struct {
	mu    sync.Mutex
	muted bool
	port  int
	rom   string
	// cfgs are RetroArch's configs at the mute, each with whether it said
	// muted already.
	cfgs     map[string]bool
	watching bool
}

// ingameMuteIsOn reports whether JukaHub has the game muted.
func ingameMuteIsOn() bool {
	ingameMute.mu.Lock()
	defer ingameMute.mu.Unlock()
	return ingameMute.muted
}

// ingameMuteWanted reports whether the game should be quiet now: the setting
// on and the music sounding under it. Any goroutine.
func ingameMuteWanted() bool {
	if !ingameQuietNow() {
		return false
	}
	return ingameMusicAudible() && audioRouteGameMixActive()
}

// ingameMuteSync brings the game's mute in line after a panel action. Only
// music started there (started: silent before the press, sounding after it)
// mutes the game; music that already sounded under it — it played as the game
// started, and the press only skipped a track — leaves the mix as it is.
// Main thread: it asks the menu which RetroArch game is on screen.
func ingameMuteSync(started bool) {
	want := ingameMuteWanted()
	if want && !started && !ingameMuteIsOn() {
		return
	}
	port, rom := 0, ""
	if want {
		if gmStatesAvailable() {
			port, rom = gmPick.port, gmPick.rom
		} else {
			want = false // not a RetroArch game JukaHub can talk to
		}
	}
	ingameMuteApply(want, port, rom)
}

// ingameMuteApply mutes (want) or unmutes the game — each at most once, as the
// remembered state says. Any goroutine.
func ingameMuteApply(want bool, port int, rom string) {
	ingameMute.mu.Lock()
	defer ingameMute.mu.Unlock()
	if want == ingameMute.muted {
		return
	}
	if !want {
		// Forgotten whatever the send says: a second toggle could only mute
		// the game again.
		if err := sendRetroArchNetCmd(ingameMute.port, "MUTE"); err != nil {
			logWarn("ingamemusic", "could not give the game its sound back: %v", err)
		} else {
			logInfo("ingamemusic", "music stopped: the game's sound is back")
		}
		ingameMute.muted = false
		return
	}
	if port <= 0 {
		return
	}
	cfgs := ingameMuteConfigs()
	for path, on := range cfgs {
		if on {
			logInfo("ingamemusic", "%s already mutes RetroArch: left as it is", path)
			return
		}
	}
	if err := sendRetroArchNetCmd(port, "MUTE"); err != nil {
		logWarn("ingamemusic", "could not quiet the game: %v", err)
		return
	}
	ingameMute.muted, ingameMute.port, ingameMute.rom, ingameMute.cfgs = true, port, rom, cfgs
	logInfo("ingamemusic", "music started from the in-game menu: the game is quiet")
	ingameMuteJournal(cfgs)
	if !ingameMute.watching {
		ingameMute.watching = true
		go ingameMuteWatch()
	}
}

// ingameMuteConfigs reads RetroArch's mute setting from each config it may use.
func ingameMuteConfigs() map[string]bool {
	out := make(map[string]bool)
	for _, path := range raScopeExistingConfigs(retroArchConfigCandidates()) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, ok := raConfigGet(string(raw), ingameMuteCfgKey)
		out[path] = ok && strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return out
}

// ingameMuteJournal records the mute in the launch journal (racfgscope.go), as
// if the launch had written audio_mute_enable = "true" into each config that
// did not say muted. RetroArch may save the mute on its way out; the journal's
// restore takes it back out where the file still says "true" — after the game,
// and also at the next start when JukaHub itself crashed or the power went
// while the game was quiet, which the in-memory state below cannot survive.
// The record stays when the music stops: a MUTE sent to a RetroArch that was
// already quitting may never have been read.
func ingameMuteJournal(cfgs map[string]bool) {
	var edits []raScopeEdit
	for path, wasOn := range cfgs {
		if wasOn {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		before, had := raConfigGet(string(raw), ingameMuteCfgKey)
		edits = append(edits, raScopeEdit{Path: path, Key: ingameMuteCfgKey, Had: had, Before: before, Wrote: "true"})
	}
	if len(edits) == 0 {
		return
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	journal := raScopeLoadLocked()
	journal.Edits = raScopeMerge(journal.Edits, edits)
	if err := raScopeSaveLocked(journal); err != nil {
		logWarn("ingamemusic", "could not record the game's mute for a restart: %v", err)
	}
}

// ingameMuteWatch gives the game its sound back once the music stops, and
// tidies up after a game that ended muted.
func ingameMuteWatch() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("ingamemusic", "watching the game's mute failed: %v", r)
			ingameMute.mu.Lock()
			ingameMute.watching = false
			ingameMute.mu.Unlock()
		}
	}()
	for {
		time.Sleep(ingameMuteWatchEvery)
		ingameMute.mu.Lock()
		if !ingameMute.muted {
			ingameMute.watching = false
			ingameMute.mu.Unlock()
			return
		}
		rom := ingameMute.rom
		ingameMute.mu.Unlock()
		if !ingameGameStillOn(rom) {
			ingameMuteGameEnded()
			return
		}
		if !ingameMuteWanted() {
			ingameMuteApply(false, 0, "")
		}
	}
}

// ingameGameStillOn reports whether the RetroArch game that was muted still
// runs: the library's record still names it and a screen child lives.
func ingameGameStillOn(rom string) bool {
	gmStates.mu.Lock()
	cur := gmStates.rom
	gmStates.mu.Unlock()
	if cur == "" || cur != rom {
		return false
	}
	return len(liveScreenChildren()) > 0
}

// ingameMuteGameEnded forgets the mute of a game that is gone and, once
// RetroArch has finished writing its settings, takes a saved mute back out of
// the configs that were not muted before.
func ingameMuteGameEnded() {
	ingameMute.mu.Lock()
	cfgs := ingameMute.cfgs
	ingameMute.muted, ingameMute.port, ingameMute.rom, ingameMute.cfgs = false, 0, "", nil
	ingameMute.watching = false
	ingameMute.mu.Unlock()
	deadline := time.Now().Add(ingameMuteExitWait)
	for retroArchRunning() && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if retroArchRunning() {
		// Still quitting, or already the next game: an edit now would be
		// overwritten on its exit. The journal's restore waits for it.
		logInfo("ingamemusic", "RetroArch still runs: the launch journal takes the game's mute out after it")
		return
	}
	for path, wasOn := range cfgs {
		if wasOn {
			continue
		}
		// The launch-scope restore edits the same files (racfgscope.go).
		raScope.mu.Lock()
		changed, err := editRetroArchConfig(path, func(cfg string) (string, bool) {
			v, ok := raConfigGet(cfg, ingameMuteCfgKey)
			if !ok || !strings.EqualFold(strings.TrimSpace(v), "true") {
				return cfg, false
			}
			return raConfigSet(cfg, ingameMuteCfgKey, "false")
		})
		raScope.mu.Unlock()
		switch {
		case err != nil:
			logWarn("ingamemusic", "could not take the game's mute out of %s: %v", path, err)
		case changed:
			logInfo("ingamemusic", "the game ended muted: %s unmuted again", path)
		}
	}
}
