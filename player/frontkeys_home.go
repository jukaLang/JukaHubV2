package main

// frontkeys_home.go — the Smart Pro S's Home key (Settings → Controls →
// "Home key" and "Home key in a game").
//
// The TrimUI Smart Pro S (TG5050) has a Home key on its top edge, beside the
// power key, apart from the Menu key on its face. JukaHub did not read it at
// all; now it is configurable, RetroArch's own menu on a Home tap among the
// choices.
//
// Reading it. The key's code is not published and could not be checked on a
// device here, so the reader is gated twice: only on a Smart Pro S, and only
// on an input device that declares KEY_HOMEPAGE (172, the usual code of a
// board's Home key), else KEY_HOME (102) — never a full keyboard (one with the
// letter keys), whose Home key is the PC one, nor JukaHub's own uinput mouse.
// Found nowhere, the two rows stay hidden and the log says so. The Custom
// "homeKeyCode" names another code for a device test (evtest shows it). The
// device is opened read-only and never grabbed, so the firmware's own
// listeners and a running game still see every press.
//
// A tap — down and up within frontHomeTapMax — counts; a longer hold is left
// alone. The reader only notes the time of the tap: the main loop acts on it
// (frontHomeKeyTick, from powerKeyTick on every frame and every parked
// iteration), so every action runs on the main thread like a button press,
// and a tap older than frontHomeFresh (a busy loop, a sleep) is dropped.
//
// In JukaHub's menus: Home (the default: what floats over the screen closes
// and Home comes up), Quick Settings, the game switcher, or nothing.
//
// In a game JukaHub launched: the in-game menu (the default) — the pad
// watcher takes the pad exactly as for Select + L2 held, because the menu's
// buttons come from the held pad (gameassist_linux.go asks
// frontHomeMenuWanted) — RetroArch's own menu (its MENU_TOGGLE network
// command to the library RetroArch, gamepause_ra.go's port; any other
// emulator gets the in-game menu instead, so the key never does nothing for
// it), the game switcher (the in-game menu's row: the game saves and quits),
// or nothing.

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// The Custom settings behind the two rows, and the device-test override
	// of the key's code.
	frontHomeSetting     = "homeKeyMenus"
	frontHomeGameSetting = "homeKeyGame"
	frontHomeCodeSetting = "homeKeyCode"

	// The in-game choices the Brick keys do not have.
	frontActGameMenu = "gameMenu"
	frontActRAMenu   = "retroarchMenu"

	// The key codes tried, in order, and KEY_A, which marks a full keyboard.
	frontHomeKeyHomepage = 172
	frontHomeKeyHome     = 102
	frontHomeKeyA        = 30

	// frontHomeTapMax is the longest press that is a tap.
	frontHomeTapMax = 800 * time.Millisecond
	// frontHomeFresh is how long a tap waits for the main loop.
	frontHomeFresh = time.Second
	// frontHomeDebounce: a second tap this soon is the same press seen
	// through another device that declares the key (the firmware's mirror),
	// which would toggle Quick Settings open and shut again.
	frontHomeDebounce = 250 * time.Millisecond
)

// frontHome is what the reader goroutines and the main thread share. found: a
// Home key device is being read (the rows show). tapAt: when the last tap
// ended (UnixNano, 0 = none taken yet). lastTap: the last tap any reader saw.
// menuAt: a tap that asks the pad watcher for the in-game menu (UnixNano, 0 =
// none).
var frontHome struct {
	found   atomic.Bool
	tapAt   atomic.Int64
	lastTap atomic.Int64
	menuAt  atomic.Int64
	once    sync.Once
}

// frontHomeDevice identifies the hardware; a variable so tests could stand in
// for a Smart Pro S.
var frontHomeDevice = DetectDevice

func init() {
	// After the first frame, beside the other device readers' start.
	deferStartupTask("homekey", frontHomeStart)
}

// frontHomeStart starts the reader once, on a Smart Pro S. Main thread (the
// deferred start-up queue), so the override is read from the config here.
func frontHomeStart() {
	if frontHomeDevice() != DeviceTrimuiSmartProS {
		return
	}
	frontHome.once.Do(func() {
		override := 0
		if appConfig != nil {
			if v, ok := customString(appConfig.Variables.Custom, frontHomeCodeSetting); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n < 0x300 {
					override = n
				}
			}
		}
		go frontHomeStartReaders(override)
	})
}

// frontHomePickDevices chooses the devices to read the Home key from (their
// "eventN" nodes) and the code it sends there: the first code of the list
// that a device declares wins. override > 0 is the only code tried, on any
// device but JukaHub's mouse. Pure.
func frontHomePickDevices(devs []evdevDevice, override int) ([]string, int) {
	codes := []int{frontHomeKeyHomepage, frontHomeKeyHome}
	if override > 0 {
		codes = []int{override}
	}
	for _, code := range codes {
		var out []string
		for _, d := range devs {
			if d.Handler == "" || d.Name == mbridgeMouseName || !evdevHas(d.Key, code) {
				continue
			}
			if override <= 0 && evdevHas(d.Key, frontHomeKeyA) {
				continue // a keyboard's Home key is not the handheld's
			}
			out = append(out, d.Handler)
		}
		if len(out) > 0 {
			return out, code
		}
	}
	return nil, 0
}

// frontHomeTap is a tap a reader saw. Any goroutine.
func frontHomeTap() {
	now := time.Now().UnixNano()
	if prev := frontHome.lastTap.Swap(now); prev != 0 && now-prev < int64(frontHomeDebounce) {
		return
	}
	frontHome.tapAt.Store(now)
}

// frontHomeKeyTick acts on a fresh tap. Main thread, every frame and every
// parked iteration (powerKeyTick); parked: a game owns the screen.
func frontHomeKeyTick(config *Config, parked bool) {
	at := frontHome.tapAt.Swap(0)
	if at == 0 || time.Since(time.Unix(0, at)) >= frontHomeFresh {
		return
	}
	// Not over the startup lock (bootlock.go), a sleep or a ringing alarm.
	if startupLocked() || powerSleepActive() || alarmFiringActive() {
		return
	}
	if parked {
		frontHomeInGame(config)
		return
	}
	frontHomeInMenus(config)
}

// frontHomeInMenus is a tap on JukaHub's own screens.
func frontHomeInMenus(config *Config) {
	if virtualKeyboardActive {
		return // the keyboard is typing into something; B closes it
	}
	if frontHomeMenuChoice(config) == frontActOff {
		return
	}
	// Like any press, a tap on the idle screen or the dark dock only wakes it
	// (and counts as activity for auto sleep).
	if screensaverWakeOnPress() {
		return
	}
	switch frontHomeMenuChoice(config) {
	case frontActHome:
		frontGoHome(config)
	case frontActQS:
		// Not over a panel that owns the pad: Quick Settings would come up
		// with no button reaching it.
		if !qs.open && modalPanelBlocksShoulders() {
			return
		}
		qsToggleOpen()
		PlayActivateSound()
	case frontActSwitcher:
		if modalPanelBesideLibrary() != "" {
			return
		}
		frontKeyRun(config, frontActSwitcher)
	case frontActQMenu:
		// The Quick menu, or closed again (quickmenu.go).
		if modalPanelBesideLibrary() != "" {
			return
		}
		frontKeyRun(config, frontActQMenu)
	}
}

// frontHomeInGame is a tap while a game JukaHub launched has the screen. Not
// over the in-game menu or the assistant (their own rows are there), nor
// while the pad watcher holds the pad for one about to open (a second tap
// would reopen the menu right after it closes), nor while the game is
// already quitting.
func frontHomeInGame(config *Config) {
	if gaOverlay.active || gaLink.held.Load() || gmQuitting.Load() || len(liveScreenChildren()) == 0 {
		return
	}
	switch frontHomeGameChoice(config) {
	case frontActRAMenu:
		if port := raLibraryPort(); port > 0 {
			logInfo("controls", "Home key: RetroArch's menu")
			go func() {
				if err := sendRetroArchNetCmd(port, "MENU_TOGGLE"); err != nil {
					logInfo("controls", "Home key: RetroArch did not take MENU_TOGGLE: %v", err)
				}
			}()
			return
		}
		// Not a library RetroArch: the in-game menu instead.
		frontHomeAskGameMenu(config)
	case frontActGameMenu:
		frontHomeAskGameMenu(config)
	case frontActSwitcher:
		if gameSwitcherEnabled(config) {
			logInfo("controls", "Home key: quitting to the game switcher")
			gameSwitcherFromGameMenu()
		}
	}
}

// frontHomeAskGameMenu asks the pad watcher for the in-game menu, which it
// opens as for Select + L2 held. Main thread.
func frontHomeAskGameMenu(config *Config) {
	if !gameAssistSupported() || !gameMenuEnabled(config) {
		logInfo("controls", "Home key: the in-game menu is off (Settings → Controls → In-game menu)")
		return
	}
	logInfo("controls", "Home key: asking for the in-game menu")
	frontHome.menuAt.Store(time.Now().UnixNano())
}

// frontHomeMenuWanted takes a fresh in-game menu request. The pad watcher's
// goroutine (gameassist_linux.go).
func frontHomeMenuWanted() bool {
	at := frontHome.menuAt.Swap(0)
	return at != 0 && time.Since(time.Unix(0, at)) < frontHomeFresh
}

// ── Settings ─────────────────────────────────────────────────────────────

func frontHomeMenuOptions() []setOption {
	return []setOption{
		{Value: frontActHome, Label: "Home", Note: "Close what is open and show Home"},
		{Value: frontActQS, Label: "Quick Settings"},
		{Value: frontActSwitcher, Label: "Game switcher"},
		{Value: frontActQMenu, Label: "Quick menu", Note: "Recents, Games, Tools, Wi-Fi and power"},
		{Value: frontActOff, Label: "Nothing"},
	}
}

func frontHomeGameOptions() []setOption {
	return []setOption{
		{Value: frontActGameMenu, Label: "In-game menu", Note: "Resume, save, switch or quit"},
		{Value: frontActRAMenu, Label: "RetroArch menu", Note: "RetroArch games; other emulators get the in-game menu"},
		{Value: frontActSwitcher, Label: "Game switcher", Note: "The game saves and quits to your recent games"},
		{Value: frontActOff, Label: "Nothing"},
	}
}

// frontHomeStored is a row's stored choice when it is one of options, else
// def.
func frontHomeStored(c *Config, key, def string, options []setOption) string {
	if c == nil {
		return def
	}
	v, _ := customString(c.Variables.Custom, key)
	v = strings.TrimSpace(v)
	for _, o := range options {
		if o.Value == v {
			return v
		}
	}
	return def
}

// frontHomeMenuChoice and frontHomeGameChoice are the two stored choices;
// missing or unknown is Home, and the in-game menu.
func frontHomeMenuChoice(c *Config) string {
	return frontHomeStored(c, frontHomeSetting, frontActHome, frontHomeMenuOptions())
}

func frontHomeGameChoice(c *Config) string {
	return frontHomeStored(c, frontHomeGameSetting, frontActGameMenu, frontHomeGameOptions())
}

// frontHomeOptionLabel is a stop's words.
func frontHomeOptionLabel(options []setOption, value string) string {
	if i := raTuneOptionIndex(options, value); i >= 0 {
		return options[i].Label
	}
	return "Nothing"
}

// frontHomeSettingsRows are the two rows, shown once the key is being read.
func frontHomeSettingsRows() []setRow {
	visible := func(*Config) bool { return frontHome.found.Load() }
	return []setRow{
		{
			ID: frontHomeSetting, Label: "Home key", Kind: setKindChoice,
			Detail:  setStatic("A tap on the Home key on top, in JukaHub's menus"),
			Visible: visible,
			Options: func(*Config) []setOption { return frontHomeMenuOptions() },
			Current: func(c *Config) int { return raTuneOptionIndex(frontHomeMenuOptions(), frontHomeMenuChoice(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, frontHomeSetting, value)
				setToast("Home key: " + frontHomeOptionLabel(frontHomeMenuOptions(), value))
			},
		},
		{
			ID: frontHomeGameSetting, Label: "Home key in a game", Kind: setKindChoice,
			Detail: func(c *Config) string {
				switch frontHomeGameChoice(c) {
				case frontActGameMenu:
					if !gameMenuEnabled(c) {
						return "Nothing while the in-game menu is off (In-game menu, above)"
					}
				case frontActRAMenu:
					if !gameMenuEnabled(c) {
						return "Other emulators need the in-game menu on (In-game menu, above)"
					}
				case frontActSwitcher:
					if !gameSwitcherEnabled(c) {
						return "Nothing while the game switcher is off (Games & emulators)"
					}
				case frontActOff:
					return "A tap on Home in a game is left to the game"
				}
				return "A tap on the Home key while a game you started here plays"
			},
			Visible: visible,
			Options: func(*Config) []setOption { return frontHomeGameOptions() },
			Current: func(c *Config) int { return raTuneOptionIndex(frontHomeGameOptions(), frontHomeGameChoice(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, frontHomeGameSetting, value)
				setToast("Home key in a game: " + frontHomeOptionLabel(frontHomeGameOptions(), value))
			},
		},
	}
}
