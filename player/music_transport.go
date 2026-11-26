package main

// music_transport.go — the music player's media controls on a pad or a
// keyboard, and Play / Stop for internet radio.
//
// The Auralis and StockOS layouts draw a row of media controls (previous,
// play, next, stop, shuffle, repeat, favourite and the volume) that only a tap
// or a mouse could press: on a handheld the D-pad walked the playlist,
// Left/Right seeked and no button stopped anything. A tester playing internet
// radio could not stop it and found its controls out of reach (2026-11-26).
// Up from the top of the playlist now steps onto the controls, and a station
// starts there: Left/Right choose one, A presses it, Down goes back to the
// playlist and Up on to the top bar. A keyboard does the same with the arrows
// and Enter.
//
// A radio station has no place to pause at. "Pause" left ffmpeg blocked on a
// full pipe with the station's connection open; Play then played what had
// piled up, or found the station had hung up and skipped to the next one. On
// a station Play / Pause is Play / Stop: Stop ends the decoder and the
// connection, Play tunes in again. A station cannot be seeked, and a decoder
// restart (speed, equaliser, effects) tunes in live instead of handing ffmpeg
// the seconds listened so far as -ss.
//
// The Radio directory and the Music screen's Radio shelf show what plays on a
// row of their own, with the same Play / Stop (radiodir_ui.go).

import "github.com/veandco/go-sdl2/sdl"

// musicCtl is the media controls' pad focus: "" while the playlist has it,
// else the focused control's id (musicCtlItems). Main thread, like the
// playlist cursor's keys.
var musicCtl struct {
	focus string
}

// ── Radio stations ───────────────────────────────────────────────────────

// musicMarkLive records tracks as radio stations (musicState.liveURLs). The
// radio entry points call it: the Radio directory and shelf (radiodir.go), the
// dock's radio (saverradio.go) and a saved channel opened as audio.
func musicMarkLive(tracks []musicTrack) {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.liveURLs == nil {
		music.liveURLs = map[string]bool{}
	}
	for _, t := range tracks {
		if isHTTPMediaURL(t.Path) {
			music.liveURLs[t.Path] = true
		}
	}
}

// musicLiveLocked reports whether the current track is a radio station.
// music.mu held.
func musicLiveLocked() bool {
	return music.current >= 0 && music.current < len(music.tracks) && music.liveURLs[music.tracks[music.current].Path]
}

// musicCurrentLive is musicLiveLocked for a caller without the lock.
func musicCurrentLive() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return musicLiveLocked()
}

// musicNowState is the loaded track as the radio screens and the footers show
// it: live is a radio station, playing that a decoder holds it (playing, or
// paused).
type musicNowState struct {
	title, path string
	live        bool
	playing     bool
	paused      bool
}

// musicNow is the loaded track's state; ok is false with nothing loaded.
func musicNow() (musicNowState, bool) {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.current < 0 || music.current >= len(music.tracks) {
		return musicNowState{}, false
	}
	t := music.tracks[music.current]
	return musicNowState{title: t.Name, path: t.Path, live: musicLiveLocked(), playing: music.playing, paused: music.paused}, true
}

// shown reports whether the radio screens offer the track on a Now playing
// row: while it plays or is paused, and a station that was stopped, which Play
// tunes in again. A song that ended is not playing now.
func (s musicNowState) shown() bool { return s.playing || s.live }

// action is what Play / Pause does now: Stop on a station on the air, Pause on
// anything else that plays, else Play.
func (s musicNowState) action() string {
	switch {
	case s.playing && !s.paused && s.live:
		return "Stop"
	case s.playing && !s.paused:
		return "Pause"
	}
	return "Play"
}

// state is where the track is: "Playing", "Paused" or "Stopped".
func (s musicNowState) state() string {
	switch {
	case !s.playing:
		return "Stopped"
	case s.paused:
		return "Paused"
	}
	return "Playing"
}

// musicPlayIcon is the Play / Pause control's mark (musicButtonIcons): Pause
// while a track plays, Stop while a station does, else Play.
func musicPlayIcon(playing, paused bool) string {
	switch {
	case playing && !paused && musicCurrentLive():
		return "[]"
	case playing && !paused:
		return "II"
	}
	return ">"
}

// ── The controls' focus ──────────────────────────────────────────────────

// musicCtlItem is one media control the layout drew this frame.
type musicCtlItem struct {
	id   string
	rect sdl.Rect
}

// musicCtlItems are the media controls on screen this frame; one the card had
// no room for (no rect) is left out.
func musicCtlItems() []musicCtlItem {
	all := [...]musicCtlItem{
		{"prev", musicRects.prev}, {"play", musicRects.playPause}, {"next", musicRects.next},
		{"stop", musicRects.stop}, {"shuffle", musicRects.shuffle}, {"repeat", musicRects.repeat},
		{"fav", musicRects.favorite}, {"voldown", musicRects.volDown}, {"volup", musicRects.volUp},
	}
	items := make([]musicCtlItem, 0, len(all))
	for _, it := range all {
		if it.rect.W > 0 && it.rect.H > 0 {
			items = append(items, it)
		}
	}
	return items
}

// musicCtlUsable reports whether the media controls are on screen to take the
// pad: the Auralis or StockOS player itself, not the shelves, the iPod layout,
// a music game, song search or the full-screen visualizer.
func musicCtlUsable(config *Config) bool {
	if !musicRects.valid || musicShelf.open || musicPodActive(config) || mgameActive() || musicSearchActive() {
		return false
	}
	music.mu.Lock()
	full := music.visualizerFull
	music.mu.Unlock()
	return !full
}

// musicCtlCurrent is the focused control, or Play when the focus names one
// this layout did not draw; ok is false while the playlist has the pad.
func musicCtlCurrent(config *Config) (musicCtlItem, bool) {
	if musicCtl.focus == "" || !musicCtlUsable(config) {
		return musicCtlItem{}, false
	}
	items := musicCtlItems()
	for _, id := range [...]string{musicCtl.focus, "play"} {
		for _, it := range items {
			if it.id == id {
				return it, true
			}
		}
	}
	if len(items) > 0 {
		return items[0], true
	}
	return musicCtlItem{}, false
}

// musicCtlHasFocus reports whether a media control holds the pad.
func musicCtlHasFocus(config *Config) bool {
	_, ok := musicCtlCurrent(config)
	return ok
}

// musicCtlFocusPlay puts the pad on Play / Stop: where a station starts, so A
// stops it.
func musicCtlFocusPlay() { musicCtl.focus = "play" }

// musicCtlSceneEnter places the pad as the player opens (musicSceneEnter): on
// Play / Stop while a station is loaded, else on the playlist. A list opened
// just before its first track starts (musicPlayPlaylist, then the scene, then
// musicPlayTrack) is judged by its highlighted row.
func musicCtlSceneEnter() {
	music.mu.Lock()
	i := music.current
	if i < 0 {
		i = music.cursor
	}
	live := i >= 0 && i < len(music.tracks) && music.liveURLs[music.tracks[i].Path]
	music.mu.Unlock()
	musicCtl.focus = ""
	if live {
		musicCtlFocusPlay()
	}
}

// musicCtlTakeFocus is Up past the first playlist row: the pad steps onto
// Play / Pause. It reports false when no control is on screen, so the press
// goes on to the top bar.
func musicCtlTakeFocus(config *Config) bool {
	if !musicCtlUsable(config) || len(musicCtlItems()) == 0 {
		return false
	}
	musicCtlFocusPlay()
	PlayNavSound()
	return true
}

// musicCtlMove steps the focus to the nearest control in direction (dx, dy),
// one D-pad press; a control on the same line wins over a closer one on the
// line above or below. It reports whether the focus moved.
func musicCtlMove(config *Config, dx, dy int32) bool {
	cur, ok := musicCtlCurrent(config)
	if !ok {
		return false
	}
	cx, cy := cur.rect.X+cur.rect.W/2, cur.rect.Y+cur.rect.H/2
	best, bestScore := "", int32(0)
	for _, it := range musicCtlItems() {
		if it.id == cur.id {
			continue
		}
		ix, iy := it.rect.X+it.rect.W/2, it.rect.Y+it.rect.H/2
		along, across := (ix-cx)*dx, iy-cy
		if dx == 0 {
			along, across = (iy-cy)*dy, ix-cx
		}
		if along <= 0 {
			continue
		}
		if across < 0 {
			across = -across
		}
		if score := along + 4*across; best == "" || score < bestScore {
			best, bestScore = it.id, score
		}
	}
	if best == "" {
		return false
	}
	musicCtl.focus = best
	PlayNavSound()
	return true
}

// musicCtlDown is Down on the controls: the line below, else back to the
// playlist.
func musicCtlDown(config *Config) {
	if musicCtlMove(config, 0, 1) {
		return
	}
	musicCtl.focus = ""
	PlayNavSound()
}

// musicCtlPressFocused is A (Enter) on the focused control; false while the
// playlist has the pad.
func musicCtlPressFocused(config *Config) bool {
	it, ok := musicCtlCurrent(config)
	if !ok {
		return false
	}
	musicCtlPress(it.id)
	return true
}

// musicCtlPress presses one control, as a tap on it does (HandleMusicClick).
func musicCtlPress(id string) {
	switch id {
	case "play":
		musicTogglePause() // plays its own click
	case "prev":
		musicPrev()
		PlayNavSound()
	case "next":
		musicNext()
		PlayNavSound()
	case "stop":
		musicStop()
		PlayBackSound()
	case "shuffle":
		if musicToggleShuffle() {
			showToast("Shuffle on", ToastSuccess())
		} else {
			showToast("Shuffle off", ToastInfo())
		}
		PlayNavSound()
	case "repeat":
		showToast(musicCycleRepeatMode().label(), ToastInfo())
		PlayNavSound()
	case "fav":
		fav, name, ok := musicFavoriteCurrent()
		switch {
		case !ok:
			showToast("Nothing to favorite yet", ToastInfo())
		case fav:
			showToast("Favorite: "+name, ToastSuccess())
		default:
			showToast("Removed favorite: "+name, ToastInfo())
		}
		PlayNavSound()
	case "voldown":
		musicAdjustVolume(-0.1)
		PlayNavSound()
	case "volup":
		musicAdjustVolume(0.1)
		PlayNavSound()
	}
}

// musicCtlLabel names what A does on a control, for the footer.
func musicCtlLabel(id string) string {
	switch id {
	case "play":
		if np, ok := musicNow(); ok {
			return np.action()
		}
		return "Play"
	case "prev":
		return "Previous"
	case "next":
		return "Next"
	case "stop":
		return "Stop"
	case "shuffle":
		return "Shuffle"
	case "repeat":
		return "Repeat"
	case "fav":
		return "Favorite"
	case "voldown":
		return "Volume down"
	case "volup":
		return "Volume up"
	}
	return "Press"
}

// musicCtlDrawFocus rings the focused control, once the layout has drawn them.
// Not while the top bar holds the pad: its own ring is the one to follow.
func musicCtlDrawFocus(renderer *sdl.Renderer, config *Config) {
	it, ok := musicCtlCurrent(config)
	if !ok || renderer == nil || tbxChromeFocused() {
		return
	}
	drawGlassFocusRing(renderer, it.rect, RadiusMD)
}

// ── Footers ──────────────────────────────────────────────────────────────

// musicPlayerHintsNow is the Auralis footer for this frame (musicPlayerPadHints
// or musicPlayerKeyHints) and where its first line ends. On a focused control
// A (Enter) names what it presses and Left/Right choose; on a station X
// (Space) says Stop or Play, L1/R1 step stations and Seek is left out.
func musicPlayerHintsNow(config *Config) ([]FooterHint, int) {
	base, split := musicPlayerPadHints, 5
	if hintsUseKeyboard() {
		base, split = musicPlayerKeyHints, 8
	}
	ctl, focused := musicCtlCurrent(config)
	np, loaded := musicNow()
	live := loaded && np.live
	if !focused && !live {
		return base, split
	}
	hints := make([]FooterHint, 0, len(base))
	first := split
	for i, h := range base {
		switch h.Label {
		case "Play":
			if focused {
				h.Label = musicCtlLabel(ctl.id)
			}
		case "Pause":
			if live {
				h.Label = np.action()
			}
		case "Seek":
			if focused {
				h.Label = "Choose"
			} else if live {
				if i < split {
					first--
				}
				continue
			}
		case "Track":
			if live {
				h.Label = "Station"
			}
		}
		hints = append(hints, h)
	}
	return hints, first
}

// musicStockOSHintsNow is the StockOS footer's first hints: what A does (the
// highlighted row, or the focused control) and, on a station, X's Stop or Play.
func musicStockOSHintsNow(config *Config) []FooterHint {
	hints := []FooterHint{{Button: "A", Label: "Play"}}
	if ctl, ok := musicCtlCurrent(config); ok {
		hints[0].Label = musicCtlLabel(ctl.id)
	}
	if np, ok := musicNow(); ok && np.live {
		hints = append(hints, FooterHint{Button: "X", Label: np.action()})
	}
	return hints
}
