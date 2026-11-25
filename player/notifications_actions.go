package main

// notifications_actions.go — notifications you can act on, and when toasts
// wait.
//
// A row may carry an action: "Install update" on an update offer, "Open" on a
// download that landed, "Retry" on one that failed, "Play" on a friend's Ghost
// Challenge. The action is stored by name (a kind and its argument), not as a
// function, so it is still there after a restart (notifications_store.go);
// each feature registers what its kinds do from init() (notifActionRegister).
//
// In the open centre:
//
//	A      runs the highlighted row's action (and the row goes: it is done);
//	       a row without one closes the centre, as A always did
//	X      dismisses the highlighted row
//	Y      clears every row — pressed twice, the first press says so
//	B      closes
//
// with Enter, Delete / X, Y and Esc on a keyboard, and a tap on the row's
// action pill or its "×".
//
// When toasts wait (Settings → General):
//
//   - Quiet hours: a nightly window (22:00–07:00 and a few others) in which
//     toasts nobody asked for wait in the bell. Errors and the answer to a
//     press still show, as during a full-screen video (videonotif.go), and
//     the window only counts once the clock is set (the handheld boots before
//     NTP). Quiet Mode in Quick Settings is the switch for "now".
//   - While playing: a game owns the screen, so a toast cannot be seen. "When
//     the game ends" (the default) is the summary JukaHub already shows on
//     the way back (whileaway.go), now counting the alerts too; "Over the
//     game" draws each one into the game with In-game indicators
//     (ingamehud.go); "Only in the bell" shows nothing.

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// notifAction is what A does on a row: the words on its pill, the registered
// kind and the kind's argument (a path, an id, a scene name).
type notifAction struct {
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Arg   string `json:"arg,omitempty"`
}

// notifActionFunc runs one kind; it reports whether it went somewhere, which
// closes the centre.
type notifActionFunc func(c *Config, arg string) bool

// notifActions are the registered kinds. Written only from init().
var notifActions = map[string]notifActionFunc{}

// notifActionRegister adds a kind. Call it from init().
func notifActionRegister(kind string, fn notifActionFunc) {
	if kind != "" && fn != nil {
		notifActions[kind] = fn
	}
}

// notifActionKnown reports a registered kind.
func notifActionKnown(kind string) bool {
	_, ok := notifActions[kind]
	return ok
}

// The kinds JukaHub's own rows use.
const (
	notifActUpdate        = "update.offer"
	notifActOpenFile      = "file.open"
	notifActDownloads     = "downloads.open"
	notifActDownloadRetry = "download.retry"
	notifActScene         = "scene.open"
)

func init() {
	notifActionRegister(notifActUpdate, func(*Config, string) bool {
		openUpdatePrompt()
		if !updatePrompt.open {
			ShowToast("JukaHub is up to date", ToastKindInfo)
		}
		return true
	})
	notifActionRegister(notifActOpenFile, func(c *Config, path string) bool {
		if !regularFile(path) {
			ShowToast("That file is no longer there", ToastKindInfo)
			return false
		}
		homeSearchOpenFile(c, path, filepath.Base(path), false)
		return true
	})
	notifActionRegister(notifActDownloads, func(c *Config, _ string) bool {
		mediaOpenDownloads(c)
		return true
	})
	notifActionRegister(notifActDownloadRetry, func(c *Config, arg string) bool {
		if id, err := strconv.ParseInt(arg, 10, 64); err == nil && downloadRetry(id) {
			ShowToast("Trying the download again", ToastKindInfo)
			return false
		}
		// The queue forgets its rows when JukaHub quits: the screen shows
		// what is left.
		mediaOpenDownloads(c)
		return true
	})
	notifActionRegister(notifActScene, func(c *Config, name string) bool {
		if c == nil {
			return false
		}
		idx := sceneIndexExact(c, name)
		if idx < 0 {
			ShowToast("That screen is not part of this setup", ToastKindInfo)
			return false
		}
		if idx != currentSceneIndex {
			changeSceneTo(c, idx)
		}
		return true
	})
}

// notifHighlighted is the highlighted row, if any.
func notifHighlighted() (notification, bool) {
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	if nc.cursor < 0 || nc.cursor >= len(nc.history) {
		return notification{}, false
	}
	return nc.history[nc.cursor], true
}

// notifRemoveID drops the row with id; the highlight stays at its index.
func notifRemoveID(id uint64) bool {
	notifHistoryMu.Lock()
	removed := false
	for i, n := range nc.history {
		if n.ID == id {
			nc.history = append(nc.history[:i], nc.history[i+1:]...)
			removed = true
			break
		}
	}
	nc.cursor = clampIndex(nc.cursor, len(nc.history))
	notifHistoryMu.Unlock()
	if removed {
		notifSyncCursorID()
		notifChanged()
	}
	return removed
}

// notifRunAction runs the highlighted row's action. The row goes first: it
// has done its job, and the action may file new rows of its own.
func notifRunAction(c *Config) bool {
	n, ok := notifHighlighted()
	if !ok || n.Action.Kind == "" {
		return false
	}
	fn := notifActions[n.Action.Kind]
	if fn == nil {
		PlayErrorSound()
		ShowToast("This can no longer be done from here", ToastKindInfo)
		return true
	}
	if c == nil {
		c = appConfig
	}
	PlayActivateSound()
	notifRemoveID(n.ID)
	if fn(c, n.Action.Arg) {
		notifDismiss()
	}
	return true
}

// notifDismissRow is X: the highlighted row goes.
func notifDismissRow() {
	n, ok := notifHighlighted()
	if !ok {
		return
	}
	if notifRemoveID(n.ID) {
		PlayNavSound()
	}
}

// notifClearArmedAt is when Y was pressed once (ticks; 0 = not armed): a
// second Y within notifClearArmMS clears the centre.
var notifClearArmedAt uint64

const notifClearArmMS = 3000

// notifClearArmed reports a first Y still waiting for its second.
func notifClearArmed() bool {
	return notifClearArmedAt != 0 && sdl.GetTicks64()-notifClearArmedAt < notifClearArmMS
}

// notifClearAllPress is Y.
func notifClearAllPress() {
	if notifHistoryLen() == 0 {
		return
	}
	if !notifClearArmed() {
		notifClearArmedAt = sdl.GetTicks64()
		PlayNavSound()
		return
	}
	notifClearArmedAt = 0
	notifClearAll()
	PlayActivateSound()
}

// notifPressA is A (Enter): the row's action, or close.
func notifPressA(c *Config) {
	if notifRunAction(c) {
		return
	}
	notifDismiss()
	PlayBackSound()
}

// notifPadButton handles a pad button while the centre is open (controller.go).
func notifPadButton(c *Config, button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		notifNavigate(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		notifNavigate(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		notifShoulder(padL1)
		notifSyncCursorID()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		notifShoulder(padR1)
		notifSyncCursorID()
	case sdl.CONTROLLER_BUTTON_A:
		notifPressA(c)
	case sdl.CONTROLLER_BUTTON_X:
		notifDismissRow()
	case sdl.CONTROLLER_BUTTON_Y:
		notifClearAllPress()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		notifDismiss()
		PlayBackSound()
	}
}

// notifKey handles a key while the centre is open (core.go).
func notifKey(c *Config, e *sdl.KeyboardEvent) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		notifNavigate(-1)
	case sdl.K_DOWN:
		notifNavigate(1)
	case sdl.K_PAGEUP, sdl.K_PAGEDOWN, sdl.K_HOME, sdl.K_END:
		notifShoulder(keyShoulder(e.Keysym.Sym))
		notifSyncCursorID()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		notifPressA(c)
	case sdl.K_DELETE, sdl.K_BACKSPACE, sdl.K_x:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 && e.Keysym.Sym == sdl.K_DELETE {
			// Shift+Delete clears everything, as a second Y would.
			notifClearArmedAt = sdl.GetTicks64()
			notifClearAllPress()
			return
		}
		notifDismissRow()
	case sdl.K_y:
		notifClearAllPress()
	case sdl.K_ESCAPE, sdl.K_b:
		notifDismiss()
	}
}

// notifFooterHint is the button line under the rows.
func notifFooterHint(history []notification, cursor int) string {
	a := "Close"
	if cursor >= 0 && cursor < len(history) && history[cursor].Action.Label != "" {
		a = history[cursor].Action.Label
	}
	y := "Clear all"
	if notifClearArmed() {
		y = "Again to clear all"
	}
	return hintLine(FooterHint{"A", a}, FooterHint{"X", "Dismiss"}, FooterHint{"Y", y}, FooterHint{"B", "Close"})
}

// notifDrawActionLabel draws a row's action at the end of its timestamp line,
// right-aligned to right: a pill on the highlighted row, the accent word on
// the others. It returns the drawn rect.
func notifDrawActionLabel(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, right, y int32, focused bool) sdl.Rect {
	w, h := measureText(font, label)
	if w <= 0 {
		return sdl.Rect{}
	}
	accent := glassAccent()
	if !focused {
		x := right - w
		renderText(renderer, config, font, label, WithAlpha(accent, 220), x, y)
		return sdl.Rect{X: x, Y: y, W: w, H: h}
	}
	padX, padY := int32(10), int32(2)
	r := sdl.Rect{X: right - w - 2*padX, Y: y - padY, W: w + 2*padX, H: h + 2*padY}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(accent, 235))
	renderText(renderer, config, font, label, sdl.Color{R: 255, G: 255, B: 255, A: 255}, r.X+padX, y)
	return r
}

// notifDrawDismissMark draws the highlighted row's "×" in its top-right
// corner, from two lines (the bundled fonts may lack U+00D7), and returns
// its rect.
func notifDrawDismissMark(renderer *sdl.Renderer, row sdl.Rect) sdl.Rect {
	const s = int32(10)
	x, y := row.X+row.W-s-10, row.Y+8
	setDrawColor(renderer, WithAlpha(ColorTextTertiary(), 220))
	for d := int32(0); d < 2; d++ {
		renderer.DrawLine(x+d, y, x+s+d, y+s)
		renderer.DrawLine(x+s+d, y, x+d, y+s)
	}
	return sdl.Rect{X: x - 6, Y: y - 6, W: s + 14, H: s + 12}
}

// ── When toasts wait ──────────────────────────────────────────────────────

const (
	// notifQuietHoursKey is the window: "" (off) or "<from>-<to>" in whole
	// hours ("22-7").
	notifQuietHoursKey = "notifQuietHours"
	// notifWhilePlayingKey: "after" (the default), "over" or "bell".
	notifWhilePlayingKey = "notifWhilePlaying"
)

// The while-playing modes, as stored in notifPrefs.whilePlaying.
const (
	notifPlayAfter int32 = iota
	notifPlayOver
	notifPlayBell
)

// notifPrefs mirrors the two settings for the goroutines that raise toasts:
// quiet is from*100+to, or -1 for off.
var notifPrefs struct {
	quiet        atomic.Int32
	whilePlaying atomic.Int32
}

func init() { notifPrefs.quiet.Store(-1) }

// notifParseQuietHours reads "22-7"; ok is false for off or nonsense.
func notifParseQuietHours(v string) (from, to int, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(v), "-")
	if !found {
		return 0, 0, false
	}
	from, e1 := strconv.Atoi(strings.TrimSpace(a))
	to, e2 := strconv.Atoi(strings.TrimSpace(b))
	if e1 != nil || e2 != nil || from < 0 || from > 23 || to < 0 || to > 23 || from == to {
		return 0, 0, false
	}
	return from, to, true
}

// notifPrefsBind reads the settings into notifPrefs. quietModeBind calls it
// once a second (main thread); a Settings change calls it at once.
func notifPrefsBind(c *Config) {
	if c == nil {
		return
	}
	q := int32(-1)
	if v, ok := customString(c.Variables.Custom, notifQuietHoursKey); ok {
		if from, to, ok := notifParseQuietHours(v); ok {
			q = int32(from*100 + to)
		}
	}
	notifPrefs.quiet.Store(q)
	mode := notifPlayAfter
	if v, ok := customString(c.Variables.Custom, notifWhilePlayingKey); ok {
		switch strings.TrimSpace(v) {
		case "over":
			mode = notifPlayOver
		case "bell":
			mode = notifPlayBell
		}
	}
	notifPrefs.whilePlaying.Store(mode)
}

// notifInQuietHours reports whether now falls in the window from..to (which
// may run past midnight). Pure.
func notifInQuietHours(from, to, hour int) bool {
	if from < to {
		return hour >= from && hour < to
	}
	return hour >= from || hour < to
}

// notifQuietHoursNow reports the window now; never before the clock is set.
func notifQuietHoursNow(now time.Time) bool {
	q := notifPrefs.quiet.Load()
	if q < 0 || now.Year() < 2024 {
		return false
	}
	return notifInQuietHours(int(q/100), int(q%100), now.Hour())
}

// notifQuietHoursHold reports whether a toast of kind waits in the bell now:
// in the window, unless it is an error, a scene-switch label or the answer to
// a press. Any goroutine.
func notifQuietHoursHold(kind ToastKind) bool {
	if kind == ToastKindError || kind == ToastKindSwitch || !notifQuietHoursNow(time.Now()) {
		return false
	}
	var last uint64
	if frameRateCtrl != nil {
		last = frameRateCtrl.LastInput()
	}
	return !videoNotifByUser(sdl.GetTicks64(), last)
}

// notifWhilePlayingMode is the while-playing setting (notifPlay*).
func notifWhilePlayingMode() int32 { return notifPrefs.whilePlaying.Load() }

// notifQuietHoursOptions are the windows offered, labelled in the clock's
// format.
func notifQuietHoursOptions(c *Config) []setOption {
	out := []setOption{{Value: "", Label: "Off", Note: "Toasts show whenever they come"}}
	for _, w := range [][2]int{{22, 7}, {23, 7}, {21, 7}, {22, 8}, {0, 8}} {
		label := clockDigitsHM(c, w[0], 0) + " – " + clockDigitsHM(c, w[1], 0)
		out = append(out, setOption{Value: strconv.Itoa(w[0]) + "-" + strconv.Itoa(w[1]), Label: label,
			Note: "Errors and answers to a press still show"})
	}
	return out
}

// notifQuietHoursRow is Settings → General → Quiet hours.
func notifQuietHoursRow() setRow {
	return setRow{
		ID: notifQuietHoursKey, Label: "Quiet hours", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if notifPrefs.quiet.Load() < 0 {
				return "Off: toasts show whenever they come"
			}
			return "In this window toasts wait in the bell; errors still show"
		},
		Options: notifQuietHoursOptions,
		Current: func(c *Config) int {
			v, _ := customString(c.Variables.Custom, notifQuietHoursKey)
			for i, o := range notifQuietHoursOptions(c) {
				if o.Value == strings.TrimSpace(v) {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			setStoreString(c, notifQuietHoursKey, value)
			notifPrefsBind(c)
			label := "Off"
			for _, o := range notifQuietHoursOptions(c) {
				if o.Value == value {
					label = o.Label
				}
			}
			setToast("Quiet hours: " + label)
		},
	}
}

// notifWhilePlayingOptions are the while-playing choices.
func notifWhilePlayingOptions(*Config) []setOption {
	return []setOption{
		{Value: "after", Label: "When the game ends", Note: "One line on the way back, the rest in the bell"},
		{Value: "over", Label: "Over the game", Note: "Drawn into RetroArch and PPSSPP with In-game indicators"},
		{Value: "bell", Label: "Only in the bell", Note: "Nothing is shown; the bell counts them"},
	}
}

// notifWhilePlayingRow is Settings → General → Notifications while playing.
func notifWhilePlayingRow() setRow {
	return setRow{
		ID: notifWhilePlayingKey, Label: "Notifications while playing", Kind: setKindChoice,
		Detail: func(c *Config) string {
			switch notifWhilePlayingMode() {
			case notifPlayOver:
				if !ingameHUDEnabled(c) {
					return "Needs In-game indicators (Games & emulators); until then, when the game ends"
				}
				return "Drawn at the top of RetroArch and PPSSPP games"
			case notifPlayBell:
				return "Nothing is shown during or after a game; the bell counts them"
			}
			return "A summary when you are back from the game"
		},
		Visible: func(*Config) bool { return gameAssistSupported() },
		Options: notifWhilePlayingOptions,
		Current: func(c *Config) int { return int(notifWhilePlayingMode()) },
		Choose: func(c *Config, value string) {
			setStoreString(c, notifWhilePlayingKey, value)
			notifPrefsBind(c)
			for _, o := range notifWhilePlayingOptions(c) {
				if o.Value == value {
					setToast("Notifications while playing: " + o.Label)
				}
			}
		},
	}
}
