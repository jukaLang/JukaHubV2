package main

// widescreen_ui.go — where Widescreen (16:9) (widescreen.go) is chosen, and
// the game options sheet's two rows of this pass (Widescreen and the
// PlayStation Analog controller, psxanalog_ui.go), which share the per-game
// record in widescreen_store.go.
//
// Settings → Emulation: with "Adjust for" on All consoles the row says which
// consoles play wide and how to change that; on Nintendo 64, PlayStation,
// Dreamcast or Mega Drive it is that console's On/Off switch. Both are hidden
// on a 4:3 screen. The sheet's row steps Default / On / Off like Buttons.

import (
	"strings"
	"time"
)

// widescreenSettingsRows are the Emulation page's rows (after Crop overscan).
func widescreenSettingsRows() []setRow {
	return []setRow{
		{
			ID: "widescreenAll", Label: "Widescreen (16:9)", Kind: setKindInfo,
			Visible: func(*Config) bool { return raTuneScope == "" && widescreenScreenWide() },
			Detail: setStatic("3D games drawn across the whole screen, no bars · pick Nintendo 64, PlayStation, " +
				"Dreamcast or Mega Drive in Adjust for to switch it on there"),
			Value: func(c *Config) string {
				var on []string
				for _, console := range widescreenConsoles {
					if widescreenConsoleOn(c, console) {
						on = append(on, console)
					}
				}
				if len(on) == 0 {
					return "Off"
				}
				return strings.Join(on, ", ")
			},
		},
		{
			ID: "widescreen", Label: "Widescreen (16:9)", Kind: setKindToggle,
			Visible: func(*Config) bool { return widescreenConsoleFor(raTuneScope) != "" && widescreenScreenWide() },
			Detail: func(*Config) string {
				return raTuneScope + " games drawn 16:9 with " + widescreenConsoleCores(raTuneScope) +
					" · a hack: edges may pop in, 2D and menus stretch · a game can choose its own"
			},
			Get: func(c *Config) bool { return widescreenConsoleOn(c, raTuneScope) },
			Set: func(c *Config, on bool) {
				if raTuneScope == "" {
					return
				}
				setStoreBool(c, raTuneConsoleKey(raTuneScope, widescreenWhat), on)
				setToast(raTuneScope + " widescreen: " + setOnOff(on))
			},
		},
	}
}

// ── Game options sheet ───────────────────────────────────────────────────

// widescreenGameHasRow reports the sheet's Widescreen row: a console with a
// hack, on a wide screen.
func widescreenGameHasRow(game libraryGame) bool {
	return widescreenConsoleFor(game.System) != "" && widescreenScreenWide()
}

// widescreenSheetCore caches the core the sheet's game starts in: finding
// its launcher reads the card, and the hint is drawn every frame. Main
// thread.
var widescreenSheetCore struct {
	path, emulator, core string
	at                   time.Time
}

func widescreenSheetCoreFor(game libraryGame) string {
	c := &widescreenSheetCore
	emulator := gameOptsUI.opts.Emulator
	if c.path == game.Path && c.emulator == emulator && time.Since(c.at) < 3*time.Second {
		return c.core
	}
	c.path, c.emulator, c.at, c.core = game.Path, emulator, time.Now(), ""
	if launcher := resolveGameLauncherFor(game); launcher != "" && retroArchLauncher(launcher) {
		c.core = gameCoreFromLauncher(launcher, game.System)
	}
	return c.core
}

func widescreenSheetValue(game libraryGame) string {
	switch emuExtrasFor(game.Path).Widescreen {
	case widescreenOn:
		return "On"
	case widescreenOff:
		return "Off"
	}
	return "Default · " + setOnOff(widescreenConsoleOn(appConfig, widescreenConsoleFor(game.System)))
}

func widescreenSheetHint(game libraryGame) string {
	console := widescreenConsoleFor(game.System)
	if core := widescreenSheetCoreFor(game); core != "" && len(widescreenCoreOptions(core)) == 0 {
		return "Needs " + widescreenConsoleCores(console) + " · pick it in the Emulator row"
	}
	return "Drawn 16:9 with no bars · a hack: edges may pop in"
}

// ── The two rows on the sheet (game_options_ui.go asks here) ─────────────

// emuExtrasRowHidden reports a row of this pass the sheet's game does not
// have (discpage.go's row list skips it).
func emuExtrasRowHidden(row int) bool {
	switch row {
	case gameOptionsRowAnalog:
		return !psxAnalogGameHasRow(gameOptsUI.game)
	case gameOptionsRowWidescreen:
		return !widescreenGameHasRow(gameOptsUI.game)
	}
	return false
}

func emuExtrasRowName(row int) string {
	if row == gameOptionsRowAnalog {
		return "Analog controller"
	}
	return "Widescreen (16:9)"
}

func emuExtrasSheetValue(game libraryGame, row int) string {
	if row == gameOptionsRowAnalog {
		return psxAnalogSheetValue(game)
	}
	return widescreenSheetValue(game)
}

func emuExtrasSheetHint(game libraryGame, row int) string {
	if row == gameOptionsRowAnalog {
		return psxAnalogSheetHint(game)
	}
	return widescreenSheetHint(game)
}

// emuExtrasSheetOwn reports a row holding the game's own choice (drawn in
// the focus colour).
func emuExtrasSheetOwn(game libraryGame, row int) bool {
	own := emuExtrasFor(game.Path)
	if row == gameOptionsRowAnalog {
		return own.Analog != ""
	}
	return own.Widescreen != ""
}

// emuExtrasSheetStep moves a row along its ladder; Left/Right stop at the
// ends, A (wrap) goes round. It reports whether anything changed.
func emuExtrasSheetStep(game libraryGame, row, delta int, wrap bool) bool {
	own := emuExtrasFor(game.Path)
	switch row {
	case gameOptionsRowAnalog:
		next := gameControlsStep(psxAnalogGameLadder, own.Analog, delta, wrap)
		if next == own.Analog {
			return false
		}
		own.Analog = next
	case gameOptionsRowWidescreen:
		next := gameControlsStep(widescreenGameLadder, own.Widescreen, delta, wrap)
		if next == own.Widescreen {
			return false
		}
		own.Widescreen = next
	default:
		return false
	}
	emuExtrasSet(game.Path, own)
	return true
}
