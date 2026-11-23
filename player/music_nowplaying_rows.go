package main

// music_nowplaying_rows.go — the Audio panel's "Now playing" rows.
//
// Start in the player (the Auralis and StockOS layouts) opens the Audio panel
// (musicsettings.go) with these rows on top: the full-screen visualizer,
// lyrics, the sleep timer, shuffle, repeat, the listening speed, the
// auto-exit timer and search. Lyrics and the sleep timer used to be the stick
// clicks, L3 and R3, which the original Smart Pro does not have
// (stickclicks.go), and most of the rest only had keyboard keys; every
// handheld now reaches all of them with the D-pad and A. The rows show only
// while the player is the screen: opened from anywhere else the panel is the
// settings it always was. The iPod layout keeps its own Now Playing menu (X),
// which has the same switches.

import "fmt"

// The rows' kinds, after the other groups' (spatial 100, tidy 200, panels 300).
const (
	msRowNowVisualizer musicSettingsRowKind = 400 + iota
	msRowNowLyrics
	msRowNowSleep
	msRowNowShuffle
	msRowNowRepeat
	msRowNowSpeed
	msRowNowExit
	msRowNowSearch
)

// musicNowPlayingRows are the rows, or none away from the player.
func musicNowPlayingRows() []musicSettingsRow {
	if !musicSceneActive() {
		return nil
	}
	return []musicSettingsRow{
		{msRowNowVisualizer, "Full-screen visualizer", "A shows it; Left/Right here pick one of the ten styles."},
		{msRowNowLyrics, "Lyrics", "The song's lyrics over the artwork: a .lrc file beside it, or the lyrics stored inside."},
		{msRowNowSleep, "Sleep timer", "Stop playing after 15, 30 or 60 minutes."},
		{msRowNowShuffle, "Shuffle", "Play the list in a random order, starting from the song playing now."},
		{msRowNowRepeat, "Repeat", "Off, the whole list, or this song."},
		{msRowNowSpeed, "Speed", "Listening speed. Each audiobook keeps its own."},
		{msRowNowExit, "Auto-exit", "Close the player after 15, 30, 60 or 90 minutes."},
		{msRowNowSearch, "Search songs", "Type the first letters of a title to find it in the list."},
	}
}

// musicNowPlayingValue is a row's right-hand text; ok is false for a row of
// another group.
func musicNowPlayingValue(kind musicSettingsRowKind, config *Config) (string, bool) {
	switch kind {
	case msRowNowVisualizer:
		return musicVizNames[musicVizStyle(config)], true
	case msRowNowLyrics:
		music.mu.Lock()
		has, on := len(music.lyrics) > 0, music.showLyrics
		music.mu.Unlock()
		switch {
		case !has:
			return "None for this song", true
		case on:
			return "Shown", true
		}
		return "Hidden", true
	case msRowNowSleep:
		if left := musicSleepRemaining(); left > 0 {
			return formatSleepRemaining(left), true
		}
		return "Off", true
	case msRowNowShuffle:
		music.mu.Lock()
		on := music.shuffle
		music.mu.Unlock()
		return onOff(on), true
	case msRowNowRepeat:
		music.mu.Lock()
		mode := music.repeatMode
		music.mu.Unlock()
		return mode.short(), true
	case msRowNowSpeed:
		music.mu.Lock()
		speed := music.speed
		music.mu.Unlock()
		if speed <= 0 {
			speed = 1
		}
		return formatSpeedLabel(speed), true
	case msRowNowExit:
		if left := musicExitRemaining(); left > 0 {
			return formatSleepRemaining(left), true
		}
		return "Off", true
	case msRowNowSearch:
		return "", true
	}
	return "", false
}

// musicNowPlayingAdjust is Left/Right (and A, through musicSettingsActivate)
// on a row; it saves and sounds for itself, and reports false for a row of
// another group.
func musicNowPlayingAdjust(kind musicSettingsRowKind, dir int, config *Config) bool {
	switch kind {
	case msRowNowVisualizer:
		musicVizStep(config, dir) // names the style and ticks
		return true
	case msRowNowLyrics:
		if !musicToggleLyrics() {
			showToast("No lyrics for this song", ToastInfo())
		}
	case msRowNowSleep:
		if minutes := musicCycleSleepTimer(); minutes == 0 {
			showToast("Sleep timer off", ToastInfo())
		} else {
			showToast(fmt.Sprintf("Sleep timer: %d min", minutes), ToastSuccess())
		}
	case msRowNowShuffle:
		if musicToggleShuffle() {
			showToast("Shuffle on", ToastSuccess())
		} else {
			showToast("Shuffle off", ToastInfo())
		}
	case msRowNowRepeat:
		showToast(musicCycleRepeatMode().label(), ToastInfo())
	case msRowNowSpeed:
		showToast("Speed "+formatSpeedLabel(musicCycleSpeed(dir)), ToastInfo())
	case msRowNowExit:
		showToast(musicTimerToast("Auto-exit", musicCycleExitTimer()), ToastInfo())
	case msRowNowSearch:
		return musicNowPlayingActivate(kind)
	default:
		return false
	}
	PlayNavSound()
	return true
}

// musicNowPlayingActivate is A on the two rows that open something: the
// panel closes so the visualizer or the search keyboard has the screen.
func musicNowPlayingActivate(kind musicSettingsRowKind) bool {
	switch kind {
	case msRowNowVisualizer:
		musicSettings.open = false
		music.mu.Lock()
		music.visualizerFull = true
		music.mu.Unlock()
		PlayActivateSound()
	case msRowNowSearch:
		musicSettings.open = false
		musicOpenSearch()
	default:
		return false
	}
	return true
}
