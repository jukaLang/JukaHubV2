package main

// inmap_actions.go — the named actions a control profile may run
// ("a hold 500ms -> action:context_menu").
//
// Every action calls a code path the app already has (the Start menu's
// toggle, Quick Settings' sliders, the player's seek, the screenshot request
// …), so a profile can only do what a button somewhere already does. An
// action reports whether it applied: one with nothing to act on (a seek with
// nothing playing, a favourite on a screen without one) returns false and the
// button that triggered it does its usual job instead, so a profile that
// seeks with the D-pad still moves the focus when nothing plays.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// inmapActionArg is the value an action takes.
type inmapActionArg uint8

const (
	inmapArgNone inmapActionArg = iota
	// inmapArgSeconds is a signed time: seek:-10s.
	inmapArgSeconds
	// inmapArgPercent is a signed step in percent: volume:+10.
	inmapArgPercent
)

// inmapAction is one named action.
type inmapAction struct {
	name  string
	label string
	arg   inmapActionArg
	// run does the action with its value and reports whether it applied.
	run func(config *Config, value float64) bool
}

// inmapActionList is the registry, in the order the reference lists it, and
// inmapActionIndex finds an entry by name. Both are filled by init, not by
// variable initialisers: the actions reach most of the app, and an
// initialiser that did would make an initialisation cycle of any path back.
var (
	inmapActionList  []inmapAction
	inmapActionIndex map[string]int
)

// inmapActionAliases are other names people write for an action.
var inmapActionAliases = []struct {
	alias string
	name  string
}{
	{"menu", "start_menu"}, {"startmenu", "start_menu"},
	{"qs", "quick_settings"}, {"quicksettings", "quick_settings"}, {"settings", "quick_settings"},
	{"context", "context_menu"}, {"options", "context_menu"},
	{"favourite", "favorite"}, {"fav", "favorite"}, {"star", "favorite"},
	{"next_page", "page_down"}, {"prev_page", "page_up"}, {"previous_page", "page_up"},
	{"previous_tab", "prev_tab"}, {"previous_item", "prev_item"},
	{"pause", "play_pause"}, {"play", "play_pause"}, {"playpause", "play_pause"},
	{"vol_up", "volume_up"}, {"vol_down", "volume_down"},
	{"profiles", "controls"}, {"control_profiles", "controls"},
	{"hotseat", "local_play"}, {"multiplayer", "local_play"},
	{"next_turn", "pass_turn"}, {"end_turn", "pass_turn"},
	{"ok", "confirm"}, {"go_back", "back"},
}

func init() {
	inmapActionList = []inmapAction{
		{name: "back", label: "Back", run: inmapActBack},
		{name: "confirm", label: "Confirm", run: inmapActConfirm},
		{name: "home", label: "Home", run: inmapActHome},
		{name: "start_menu", label: "Start menu", run: inmapActStartMenu},
		{name: "quick_settings", label: "Quick Settings", run: inmapActQuickSettings},
		{name: "screenshot", label: "Screenshot", run: inmapActScreenshot},
		{name: "search", label: "Search", run: inmapActSearch},
		{name: "context_menu", label: "Context menu", run: inmapActContextMenu},
		{name: "favorite", label: "Favourite", run: inmapActFavorite},
		{name: "next_tab", label: "Next tab", run: inmapActNextTab},
		{name: "prev_tab", label: "Previous tab", run: inmapActPrevTab},
		{name: "page_up", label: "Page up", run: inmapActPageUp},
		{name: "page_down", label: "Page down", run: inmapActPageDown},
		{name: "next_item", label: "Next item", run: inmapActNextItem},
		{name: "prev_item", label: "Previous item", run: inmapActPrevItem},
		{name: "play_pause", label: "Play / pause", run: inmapActPlayPause},
		{name: "seek", label: "Seek", arg: inmapArgSeconds, run: inmapActSeek},
		{name: "volume", label: "Volume", arg: inmapArgPercent, run: inmapActVolume},
		{name: "volume_up", label: "Volume up", run: inmapActVolumeUp},
		{name: "volume_down", label: "Volume down", run: inmapActVolumeDown},
		{name: "brightness", label: "Brightness", arg: inmapArgPercent, run: inmapActBrightness},
		{name: "brightness_up", label: "Brightness up", run: inmapActBrightnessUp},
		{name: "brightness_down", label: "Brightness down", run: inmapActBrightnessDown},
		{name: "controls", label: "Control profiles", run: inmapActControls},
		{name: "local_play", label: "Local play", run: inmapActLocalPlay},
		{name: "pass_turn", label: "Pass the turn", run: inmapActPassTurn},
	}
	inmapActionIndex = make(map[string]int, len(inmapActionList))
	for i, a := range inmapActionList {
		inmapActionIndex[a.name] = i
	}
}

// inmapFindAction finds an action by its name or an alias.
func inmapFindAction(name string) *inmapAction {
	n := inmapNormName(name)
	if i, ok := inmapActionIndex[n]; ok {
		return &inmapActionList[i]
	}
	for _, a := range inmapActionAliases {
		if a.alias == n {
			if i, ok := inmapActionIndex[a.name]; ok {
				return &inmapActionList[i]
			}
		}
	}
	return nil
}

// inmapActionWords lists the action names, for suggestions and the editor.
func inmapActionWords() []string {
	out := make([]string, 0, len(inmapActionList))
	for _, a := range inmapActionList {
		out = append(out, a.name)
	}
	return out
}

// inmapRunAction runs a target's action and reports whether it applied.
func inmapRunAction(config *Config, t inmapTarget) bool {
	a := inmapFindAction(t.action)
	if a == nil || a.run == nil {
		return false
	}
	return a.run(config, t.arg)
}

// inmapCurrentScene names the scene on screen, or "".
func inmapCurrentScene(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	return config.Scenes[currentSceneIndex].Name
}

// ── The actions ──────────────────────────────────────────────────────────

func inmapActBack(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnB)
	return true
}

func inmapActConfirm(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnA)
	return true
}

// inmapActHome goes to the Home screen from anywhere.
func inmapActHome(c *Config, _ float64) bool {
	if c == nil {
		return false
	}
	home := homeSceneIndex(c)
	if home < 0 {
		return false
	}
	if home != currentSceneIndex {
		changeSceneTo(c, home)
	}
	return true
}

func inmapActStartMenu(*Config, float64) bool {
	startMenuToggle()
	return true
}

func inmapActQuickSettings(*Config, float64) bool {
	qsToggleOpen()
	PlayActivateSound()
	return true
}

func inmapActScreenshot(*Config, float64) bool {
	requestScreenshot()
	return true
}

// inmapActSearch opens the search sheet with its keyboard, from any screen.
func inmapActSearch(c *Config, _ float64) bool {
	homeSearchOpen(c, true)
	return true
}

// inmapActContextMenu opens the menu of the thing in focus where a screen
// has one: a game's options in the library, the action sheet in Files.
func inmapActContextMenu(c *Config, _ float64) bool {
	switch {
	case gameLib.open:
		return openGameOptions()
	case feSceneActive(c):
		inmapTapButton(c, inmapBtnY)
		return true
	}
	return false
}

// inmapActFavorite stars what is playing or focused where a screen keeps
// favourites: the song in Music, the channel in Live TV.
func inmapActFavorite(c *Config, _ float64) bool {
	switch inmapCurrentScene(c) {
	case "MusicPlayer":
		on, name, ok := musicFavoriteCurrent()
		if !ok {
			return false
		}
		if on {
			ShowToast("Added "+name+" to favourites", ToastKindSuccess)
		} else {
			ShowToast("Removed "+name+" from favourites", ToastKindInfo)
		}
		return true
	case "LiveTV":
		toggleTVFavorite()
		return true
	}
	return false
}

func inmapActNextTab(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnR1)
	return true
}

func inmapActPrevTab(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnL1)
	return true
}

func inmapActPageUp(*Config, float64) bool {
	inmapTapKey(sdl.K_PAGEUP)
	return true
}

func inmapActPageDown(*Config, float64) bool {
	inmapTapKey(sdl.K_PAGEDOWN)
	return true
}

func inmapActNextItem(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnDown)
	return true
}

func inmapActPrevItem(c *Config, _ float64) bool {
	inmapTapButton(c, inmapBtnUp)
	return true
}

// inmapMusicLoaded reports whether the audio player has a track to act on.
func inmapMusicLoaded() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.current >= 0 && music.current < len(music.tracks)
}

// inmapVideoOnScreen reports whether the video player is up in full (not
// floating in its mini window, where the D-pad belongs to the screen).
func inmapVideoOnScreen() (videoStateSnapshot, bool) {
	state := snapshotVideoState()
	return state, state.phase != "idle" && !videoMiniVisible()
}

func inmapActPlayPause(*Config, float64) bool {
	if _, ok := inmapVideoOnScreen(); ok {
		ToggleVideoPlayback()
		return true
	}
	if inmapMusicLoaded() {
		musicTogglePause()
		return true
	}
	return false
}

// inmapActSeek skips through the video on screen, or the song in Music.
func inmapActSeek(_ *Config, seconds float64) bool {
	if state, ok := inmapVideoOnScreen(); ok && state.duration > 0 {
		seekVideoBySeconds(state, seconds)
		revealVideoChrome()
		return true
	}
	if musicSceneActive() && inmapMusicLoaded() {
		musicSeekBy(seconds)
		return true
	}
	return false
}

// inmapActVolume steps the device volume through Quick Settings' own slider
// (slider 0), which applies and remembers it.
func inmapActVolume(_ *Config, pct float64) bool {
	qsStepSlider(0, int(math.Round(pct)))
	return true
}

func inmapActVolumeUp(c *Config, _ float64) bool {
	return inmapActVolume(c, 10)
}

func inmapActVolumeDown(c *Config, _ float64) bool {
	return inmapActVolume(c, -10)
}

// inmapActBrightness steps the backlight (Quick Settings slider 1).
func inmapActBrightness(_ *Config, pct float64) bool {
	qsStepSlider(1, int(math.Round(pct)))
	return true
}

func inmapActBrightnessUp(c *Config, _ float64) bool {
	return inmapActBrightness(c, 10)
}

func inmapActBrightnessDown(c *Config, _ float64) bool {
	return inmapActBrightness(c, -10)
}

func inmapActControls(c *Config, _ float64) bool {
	inmapOpenProfiles(c)
	return true
}

func inmapActLocalPlay(c *Config, _ float64) bool {
	hotseatOpen(c)
	return true
}

// inmapActPassTurn ends the turn of a pass-and-play game, from any screen.
func inmapActPassTurn(c *Config, _ float64) bool {
	return hotseatPassTurn(c)
}
