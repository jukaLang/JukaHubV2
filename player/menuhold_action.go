package main

// menuhold_action.go — what holding Menu does in a game (Settings → Controls
// → "Hold Menu in a game"): Screenshot (the default, as before), Game
// switcher, or Nothing. JukaHub's hold has saved the screen since 2026-11-18
// (menushot.go), so that stays the default and the switcher is a choice.
//
// The pad watcher (gameassist_linux.go) times the hold exactly as for a
// screenshot — Menu alone, armed after menuShotHold with a tick of the motor,
// taken on the release, spoiled by any other button, a trigger, the D-pad or
// a volume key — so an emulator's Menu hotkeys never quit a game. With the
// switcher chosen it reads no picture at the press and, on the release, only
// notes the time (menuHoldSwitchRequest). The parked main loop takes that
// note (menuHoldGameTick) and quits to the switcher the way the in-game
// menu's Game switcher row does, the very same call (gameSwitcherFromGameMenu):
// the game is asked to quit with an interrupt, RetroArch writes its auto state
// on the way out, and the switcher opens once JukaHub has the screen back.
// Most emulators open their own menu the moment Menu goes down, so that menu
// is what shows for the short time before the game quits.
//
// "Hold Menu: screenshot" (menushot.go) stays the screenshot's master switch:
// with it off, Screenshot here means nothing in a game either, as before.
// Game switcher needs the switcher itself on (Settings → Games & emulators),
// or the hold would end the game for nothing.

import (
	"sync/atomic"
	"time"
)

const (
	// menuHoldGameKey is the Custom setting behind "Hold Menu in a game".
	menuHoldGameKey      = "menuHoldInGame"
	menuHoldGameShot     = "screenshot"
	menuHoldGameSwitcher = "switcher"
	menuHoldGameOff      = "off"
)

// menuHoldLive is what the pad watcher's goroutine and the main thread share.
// switcher: the hold means the game switcher (set by the main thread,
// menuHoldGameSync). switchAt: when such a hold was let go (UnixNano, 0 =
// none), for the main loop to act on while it is fresh.
var menuHoldLive struct {
	switcher atomic.Bool
	switchAt atomic.Int64
}

// menuHoldGameOptions are the setting's stops.
func menuHoldGameOptions() []setOption {
	return []setOption{
		{Value: menuHoldGameShot, Label: "Screenshot"},
		{Value: menuHoldGameSwitcher, Label: "Game switcher"},
		{Value: menuHoldGameOff, Label: "Nothing"},
	}
}

// menuHoldGameChoice is the stored choice; missing or unknown is Screenshot.
func menuHoldGameChoice(c *Config) string {
	if c == nil {
		return menuHoldGameShot
	}
	v, _ := customString(c.Variables.Custom, menuHoldGameKey)
	switch v {
	case menuHoldGameSwitcher, menuHoldGameOff:
		return v
	}
	return menuHoldGameShot
}

// menuHoldGameAction is what the hold does now: the choice, Nothing when
// what it needs is switched off (the package header).
func menuHoldGameAction(c *Config) string {
	switch choice := menuHoldGameChoice(c); choice {
	case menuHoldGameShot:
		if !menuShotEnabled(c) {
			return menuHoldGameOff
		}
		return choice
	case menuHoldGameSwitcher:
		if !gameSwitcherEnabled(c) {
			return menuHoldGameOff
		}
		return choice
	}
	return menuHoldGameOff
}

// menuHoldGameLabel is a choice's words.
func menuHoldGameLabel(v string) string {
	for _, o := range menuHoldGameOptions() {
		if o.Value == v {
			return o.Label
		}
	}
	return "Screenshot"
}

// menuHoldGameSettingsRow is the choice, where the pad watcher runs beside a
// game (Settings → Controls, under "Hold Menu: screenshot": stickcal.go).
func menuHoldGameSettingsRow() setRow {
	return setRow{
		ID: menuHoldGameKey, Label: "Hold Menu in a game", Kind: setKindChoice,
		Detail: func(c *Config) string {
			switch menuHoldGameChoice(c) {
			case menuHoldGameShot:
				if !menuShotEnabled(c) {
					return "Nothing while Hold Menu: screenshot is off"
				}
				return "Hold Menu, then let go to save the game's screen"
			case menuHoldGameSwitcher:
				if !gameSwitcherEnabled(c) {
					return "Nothing while the game switcher is off (Games & emulators)"
				}
				return "Hold Menu, then let go: the game saves and quits to the game switcher"
			}
			return "Menu in a game is only the game's own"
		},
		Visible: func(*Config) bool { return gameAssistSupported() },
		Options: func(*Config) []setOption { return menuHoldGameOptions() },
		Current: func(c *Config) int {
			cur := menuHoldGameChoice(c)
			for i, o := range menuHoldGameOptions() {
				if o.Value == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return menuHoldGameLabel(menuHoldGameChoice(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, menuHoldGameKey, value)
			setToast("Hold Menu in a game: " + menuHoldGameLabel(value))
		},
	}
}

// menuHoldLegendLabel is the controller card's words for Hold Menu
// (menuShotLegendRow, menushot.go), which shows while the screenshot is on.
func menuHoldLegendLabel() string {
	switch menuHoldGameAction(appConfig) {
	case menuHoldGameShot:
		return "Screenshot (in a game too)"
	case menuHoldGameSwitcher:
		return "Screenshot · in a game: game switcher"
	}
	return "Screenshot (not in a game)"
}

// menuHoldGameSync publishes the hold's meaning for the pad watcher and
// reports whether the watcher reads Menu holds at all. Main thread, every
// parked tick (gameAssistParkedTick).
func menuHoldGameSync(c *Config) bool {
	act := menuHoldGameAction(c)
	menuHoldLive.switcher.Store(act == menuHoldGameSwitcher)
	return act != menuHoldGameOff
}

// menuHoldSwitchRequest is an armed hold let go with the switcher chosen: the
// main loop is told. The pad watcher's goroutine.
func menuHoldSwitchRequest(pad string) {
	logInfo("gameassist", "Menu held in %q: quitting to the game switcher", pad)
	menuHoldLive.switchAt.Store(time.Now().UnixNano())
}

// menuHoldGameTick acts on a fresh request: the in-game menu's Game switcher
// row without the menu. Not over the overlay (its own row is there), during a
// quit already under way, a sleep or an alarm, nor with the switcher turned
// off since the hold. Main thread (gameAssistParkedTick).
func menuHoldGameTick(config *Config) {
	at := menuHoldLive.switchAt.Swap(0)
	if at == 0 || time.Since(time.Unix(0, at)) >= time.Second {
		return
	}
	if gaOverlay.active || gmQuitting.Load() || powerSleepActive() || alarmFiringActive() ||
		!gameSwitcherEnabled(config) || len(liveScreenChildren()) == 0 {
		return
	}
	gameSwitcherFromGameMenu()
}
