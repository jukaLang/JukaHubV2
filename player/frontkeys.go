package main

// frontkeys.go — what the Brick's front F1 / F2 keys do (Settings → Controls
// → "F1 key" and "F2 key").
//
// brickkeys.go made the two small keys under a Brick's screen useful: F1 the
// Controller card, F2 Quick Settings, outside the screens that use the stick
// clicks the firmware reports them as (L3 / R3). Now the user picks what
// those keys do: each key runs one of the
// openers the app already has — the Controller card, Quick Settings, a
// screenshot, the game switcher, search, the Start menu, sleep, mute, Home —
// or nothing. The defaults are the old fixed pair, so nothing changes for
// anyone who never opens the rows.
//
// Every action is the call another button or key already makes, never a
// second copy: qsToggleOpen as Select on Home, startMenuToggle as Start,
// homeSearchToggle as the keyboard's F2, requestScreenshot as the Start menu's
// screenshot, gameSwitcherOpen as X on Home, sleepNow as the power key,
// volumeToggleMute as the volume card's A. A key whose panel is up closes it
// again, the way F1 and F2 always did: Quick Settings, the Controller card,
// the Start menu and search own every press while they are up, so they ask
// frontKeyIs (qs_layout.go, qs_edit.go, controlslegend.go, startmenu.go,
// globalsearch.go).
//
// "Nothing" hands the press back to the screen, which sees L3 / R3 as before.
// The screens that own the stick clicks (sceneClaimsStickClicks: the web
// browser, the audio player; the library's modal) keep them whatever is
// chosen here. The FN switch on the side is another setting (fnswitch*.go).
//
// The Smart Pro S's Home key, on the same Settings page, is in
// frontkeys_home.go.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The Custom settings behind the two rows.
const (
	frontKeyF1Setting = "brickF1Key"
	frontKeyF2Setting = "brickF2Key"
)

// The stored actions. The Home key's choices (frontkeys_home.go) reuse the
// ones it shares.
const (
	frontActControls = "controls"
	frontActQS       = "quickSettings"
	frontActShot     = "screenshot"
	frontActSwitcher = "switcher"
	frontActSearch   = "search"
	frontActStart    = "startMenu"
	frontActSleep    = "sleep"
	frontActMute     = "mute"
	frontActHome     = "home"
	frontActOff      = "off"
	frontActQMenu    = "quickMenu" // the Quick menu (quickmenu.go)
)

// frontKeys are the two keys, F1 then F2, as SDL reports them.
var frontKeys = [...]struct {
	name   string
	button uint8
}{
	{"F1", sdl.CONTROLLER_BUTTON_LEFTSTICK},
	{"F2", sdl.CONTROLLER_BUTTON_RIGHTSTICK},
}

// frontKeyOptions are the actions a front key can run, in the row's order.
func frontKeyOptions() []setOption {
	return []setOption{
		{Value: frontActControls, Label: "Controller card", Note: "What every button does on this screen"},
		{Value: frontActQS, Label: "Quick Settings", Note: "Wi-Fi, volume, brightness"},
		{Value: frontActShot, Label: "Screenshot", Note: "Save the screen as a picture"},
		{Value: frontActSwitcher, Label: "Game switcher", Note: "Your recent games, each where you left it"},
		{Value: frontActSearch, Label: "Search", Note: "Games, files, settings and screens"},
		{Value: frontActStart, Label: "Start menu"},
		{Value: frontActQMenu, Label: "Quick menu", Note: "Recents, Games, Tools, Wi-Fi and power"},
		{Value: frontActSleep, Label: "Sleep"},
		{Value: frontActMute, Label: "Mute", Note: "Sound off, or back to its level"},
		{Value: frontActHome, Label: "Home"},
		{Value: frontActOff, Label: "Nothing", Note: "The screen gets the key as L3 / R3"},
	}
}

// frontKeyLabel is an action's words.
func frontKeyLabel(action string) string {
	for _, o := range frontKeyOptions() {
		if o.Value == action {
			return o.Label
		}
	}
	return "Nothing"
}

// frontKeyChoice is a front key's stored action (button is L3 for F1, R3 for
// F2); missing or unknown is the key's old fixed meaning.
func frontKeyChoice(c *Config, button uint8) string {
	def, key := frontActControls, frontKeyF1Setting
	if button == sdl.CONTROLLER_BUTTON_RIGHTSTICK {
		def, key = frontActQS, frontKeyF2Setting
	}
	if c == nil {
		return def
	}
	v, _ := customString(c.Variables.Custom, key)
	v = strings.TrimSpace(v)
	for _, o := range frontKeyOptions() {
		if o.Value == v {
			return v
		}
	}
	return def
}

// frontKeyIs reports whether button is a Brick front key set to action: the
// panels that own every press while they are up ask it, so the key that
// opened them closes them again.
func frontKeyIs(button uint8, action string) bool {
	if button != sdl.CONTROLLER_BUTTON_LEFTSTICK && button != sdl.CONTROLLER_BUTTON_RIGHTSTICK {
		return false
	}
	return brickHasFunctionKeys() && frontKeyChoice(appConfig, button) == action
}

// frontKeyPress runs a front key's action (handleBrickFunctionKey has checked
// the device and the screen) and reports whether the press was used. Main
// thread.
func frontKeyPress(button uint8, config *Config) bool {
	if config == nil {
		config = appConfig
	}
	return frontKeyRun(config, frontKeyChoice(config, button))
}

// frontKeyRun does one action. false: nothing was done, and the press goes
// on to the screen.
func frontKeyRun(config *Config, action string) bool {
	switch action {
	case frontActControls:
		openControlsLegend()
		if !controlsLegend.open {
			return false
		}
		PlayActivateSound()
	case frontActQS:
		qsToggleOpen()
		PlayActivateSound()
	case frontActShot:
		requestScreenshot()
	case frontActSwitcher:
		if gameSwitcherEnabled(config) {
			PlayActivateSound()
		}
		gameSwitcherOpen(config) // says so when the switcher is off
	case frontActSearch:
		homeSearchToggle(config)
	case frontActStart:
		startMenuToggle()
	case frontActQMenu:
		// Opens it, or closes it when it is up (quickmenu.go).
		PlayActivateSound()
		quickMenuToggle()
	case frontActSleep:
		sleepNow("front-key")
	case frontActMute:
		frontKeyMute(config)
	case frontActHome:
		return frontGoHome(config)
	default:
		return false
	}
	return true
}

// frontKeyMute mutes, or returns to the last audible level, and says which.
func frontKeyMute(config *Config) {
	if pct := volumeToggleMute(config); pct > 0 {
		ShowToast("Sound on · "+strconv.Itoa(pct)+"%", ToastKindInfo)
	} else {
		ShowToast("Muted", ToastKindInfo)
	}
	PlayActivateSound()
}

// frontGoHome puts away what floats over the screen and shows Home: a front
// key or the Smart Pro S's Home key set to Home. false: the config has no Home
// screen, or a panel with work of its own is open (the Home key reaches here
// over any panel; Home behind it would only show once it closes). Main thread.
func frontGoHome(config *Config) bool {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return false
	}
	home := homeSceneIndex(config)
	if home < 0 {
		return false
	}
	dismissWeatherPopover()
	dismissVolumePopover()
	dismissBrightnessPopover()
	if modalPanelBesideLibrary() != "" {
		return false
	}
	ambientLeave() // the dock, lit (a dark one only wakes: frontHomeInMenus)
	nc.open = false
	homeCalendarOpen = false
	if qs.open {
		qsToggleOpen() // saves an edit left open (qs_edit.go)
	}
	startMenuDismiss()
	controlsLegend.open = false
	shortcutsOpen = false
	if globalSearchOpen {
		homeSearchClose()
	}
	closeGameLibraryPanel()
	if home != currentSceneIndex {
		// Not on the scene history: B on Home has nowhere to go anyway.
		switchScene(config, home, false)
	}
	PlayActivateSound()
	return true
}

// frontKeyLegendRows are the Controller card's rows for the two keys on a
// Brick (controlslegend.go): what each is set to, leaving out "Nothing".
func frontKeyLegendRows() []FooterHint {
	var rows []FooterHint
	for _, k := range frontKeys {
		act := frontKeyChoice(appConfig, k.button)
		switch act {
		case frontActOff:
			continue
		case frontActControls:
			rows = append(rows, FooterHint{Button: k.name, Label: "This help"})
		default:
			rows = append(rows, FooterHint{Button: k.name, Label: frontKeyLabel(act)})
		}
	}
	return rows
}

// frontKeyLegendClose is the button that closes the Controller card on a
// Brick: B, and the front key that opens it.
func frontKeyLegendClose() string {
	out := "B"
	for _, k := range frontKeys {
		if frontKeyChoice(appConfig, k.button) == frontActControls {
			out += " / " + k.name
		}
	}
	return out
}

// frontKeySettingsRows are Settings → Controls' rows for the keys outside the
// usual pad: the Brick's F1 and F2, the Smart Pro S's Home key (each shown
// only on its own hardware). stickcal.go lists them.
func frontKeySettingsRows() []setRow {
	rows := make([]setRow, 0, len(frontKeys)+2)
	for _, k := range frontKeys {
		rows = append(rows, frontKeyRow(k.name, k.button))
	}
	return append(rows, frontHomeSettingsRows()...)
}

// frontKeyRow is one key's choice.
func frontKeyRow(name string, button uint8) setRow {
	key := frontKeyF1Setting
	if button == sdl.CONTROLLER_BUTTON_RIGHTSTICK {
		key = frontKeyF2Setting
	}
	return setRow{
		ID: key, Label: name + " key", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if frontKeyChoice(c, button) == frontActOff {
				return "The small " + name + " key under the screen does nothing"
			}
			return "The small " + name + " key under the screen · the browser and the music player keep their own"
		},
		Visible: func(*Config) bool { return brickHasFunctionKeys() },
		Options: func(*Config) []setOption { return frontKeyOptions() },
		Current: func(c *Config) int { return raTuneOptionIndex(frontKeyOptions(), frontKeyChoice(c, button)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, key, value)
			setToast(name + " key: " + frontKeyLabel(value))
		},
	}
}
