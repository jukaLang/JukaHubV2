package main

// home_quicktiles.go — the quick controls Home can pin: the Wi-Fi, Bluetooth
// and Power tiles (refs w:wifi, w:bluetooth and w:power).
//
// A tester coming from NextUI, whose quick menu carries Wi-Fi, Bluetooth,
// sleep, reboot and power-off icons, asked to pin those to Home (2026-11-13).
// They are widgets of the editable Home (home_tiles.go): Edit Home's add sheet
// lists them first, under "Quick controls", and they sit in the Widgets grid
// and the Carousel like any other tile — Edit Home works from every layout
// (Standard borrows Widgets while editing), so L2, "+", A pins one anywhere.
//
//   - Wi-Fi: A switches the radio. Off is rfkill/nmcli's switch; on is the
//     Wi-Fi page's way (wifi_ui.go wifiEnableRadioAsync): the switch, a moment
//     for the interface, then the supplicant started where the image has one
//     and nothing is listening, so the handheld rejoins its saved network. The
//     tile shows the network, "Not connected", "Connecting…" or "Off" from the
//     idle screen's reading (saver_status.go saverWifiNow), which never blocks;
//     which way A goes follows rfkill's or nmcli's answer in that reading.
//     Where nothing can switch the radio (Windows, an image with neither nmcli
//     nor rfkill) A opens the Wi-Fi page instead.
//   - Bluetooth: A switches the radio with Quick Settings' asynchronous
//     bring-up and power-off (bluetooth_bringup.go), and the tile shows the
//     kernel's reading the top bar's button draws (bluetooth_quick.go): Off,
//     On, or how many devices are connected. Where there is no switch
//     (Windows, macOS) A opens the Bluetooth page.
//   - Power: A opens Home's power card (home_power.go). On the handheld it
//     carries Sleep, Restart and Power off, and the tile's card opens on Power
//     off — the press a "Power" tile is for — behind the card's arming delay;
//     a desktop gets the Exit card.
//
// A switch runs off the render thread and lands back on the main loop
// (tbxPost). While it runs the tile says so, a second press waits for it (a
// held Enter does not repeat it: home.go), and Quick Settings' own switch is
// told the result, so the panel, voice commands and the tile agree — unless
// one of those switched the same radio meanwhile (qsEditGen), whose state then
// stands. A Wi-Fi radio whose state is not known yet is switched on: on is the
// harmless direction (a radio already on stays on), off could cut a link
// nobody asked to drop. Bluetooth without a kernel reading (an rfkill-only
// image) goes by Quick Settings' switch, which the tile keeps current.

import (
	"errors"
	"runtime"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The quick controls' widget names (the refs without "w:").
const (
	hqWifiName      = "wifi"
	hqBluetoothName = "bluetooth"
	hqPowerName     = "power"
)

const (
	// hqFollowFor is how long after a Wi-Fi switch the tile keeps asking for
	// a fresh reading: the link takes seconds to come back (or to go), and
	// the idle screen's reading otherwise refreshes every eight.
	hqFollowFor = 15 * time.Second
	// hqFollowEvery is how often it asks during that time.
	hqFollowEvery = 2 * time.Second
	// hqAssumeFor is how long a finished switch is shown as done before a
	// reading taken after it has landed: without it the tile flicked back to
	// the old state for the moment that reading takes.
	hqAssumeFor = 2500 * time.Millisecond
)

// hqSwitch is one radio's switch from a tile. Main thread only: the workers
// report through tbxPost.
type hqSwitch struct {
	busy bool
	want bool
	// gen is qsEditGen when the switch started: Quick Settings or a voice
	// command switching the same radio meanwhile moves it, and the tile then
	// leaves the panel's switch to that newer change.
	gen int
	// done is when the last switch finished; ok whether it worked.
	done time.Time
	ok   bool
	// kicked is when the Wi-Fi reading was last asked for again.
	kicked time.Time
}

var hqWifi, hqBT hqSwitch

// hqLook is what a quick tile shows this frame.
type hqLook struct {
	glyph  string
	title  string
	sub    string
	on     bool // the badge is lit: the radio is on
	busy   bool // a switch is running
	danger bool // the power glyph turns the danger ink under the focus
}

// hqIsQuick reports whether a widget name is one of the quick controls.
func hqIsQuick(name string) bool {
	switch name {
	case hqWifiName, hqBluetoothName, hqPowerName:
		return true
	}
	return false
}

// hqAvailable reports whether this device can offer the quick control: a
// Wi-Fi radio to switch or a Wi-Fi page to open, a Bluetooth switch or page,
// and Power everywhere (a desktop's card is Exit).
func hqAvailable(name string) bool {
	switch name {
	case hqWifiName:
		return hqWifiSwitchable() || wifiEntryReachable()
	case hqBluetoothName:
		return hqBTSwitchable() || btQuickState().Available
	case hqPowerName:
		return true
	}
	return false
}

// hqCatalogNote is the add sheet's line for a quick control: the desktop's
// Power tile only exits.
func hqCatalogNote(name, note string) string {
	if name == hqPowerName && !devPowerSupported() {
		return "Exit JukaHub"
	}
	return note
}

// hqWifiSwitchable reports whether the radio itself can be switched here
// (Quick Settings' Wi-Fi switch: nmcli or rfkill).
func hqWifiSwitchable() bool {
	return runtime.GOOS == "linux" && wifiBackend() != ""
}

// hqBTSwitchable reports whether the Bluetooth radio can be switched here
// (Quick Settings' Bluetooth switch: bluetoothctl or rfkill).
func hqBTSwitchable() bool {
	return runtime.GOOS == "linux" && bluetoothBackend() != ""
}

// hqActivate is A (or a click) on a quick tile. homeActivate has played the
// activation sound.
func hqActivate(config *Config, name string) {
	switch name {
	case hqWifiName:
		hqWifiActivate(config)
	case hqBluetoothName:
		hqBTActivate(config)
	case hqPowerName:
		homePowerOpen()
		if homePowerConfirm.full {
			homePowerConfirm.focus = homePowerOff
		}
	}
}

// hqQSToggle is Quick Settings' switch for action, or nil.
func hqQSToggle(action qsAction) *qsToggle {
	for i := range qs.toggles {
		if qs.toggles[i].Action == action {
			return &qs.toggles[i]
		}
	}
	return nil
}

// hqBegin marks a switch started from a tile. A probe of Quick Settings'
// that started before it read the radio before it, so it is dropped as stale
// (qsEditGen, as qsApplyToggle does).
func hqBegin(s *hqSwitch, want bool) {
	qsNoteEdit()
	s.busy, s.want, s.gen = true, want, qsEditGen
}

// hqFinish takes a finished switch back on the main loop and reports whether
// it is still the latest change to that radio: when Quick Settings or a voice
// command switched it meanwhile, theirs is the state to show, not this one.
func hqFinish(s *hqSwitch, err error) bool {
	latest := s.gen == qsEditGen
	s.busy = false
	s.done, s.ok = time.Now(), err == nil && latest
	return latest
}

// hqNoteSwitched tells Quick Settings what a tile switched: its switch shows
// it at once, and a probe of the panel's that started before the switch is
// dropped as stale (qsEditGen) instead of flipping it back.
func hqNoteSwitched(action qsAction, on bool) {
	if t := hqQSToggle(action); t != nil {
		t.Value = on
	}
	qsNoteEdit()
}

// ── Wi-Fi ────────────────────────────────────────────────────────────────

// hqWifiOn reports what the Wi-Fi reading says: whether the radio is on and
// whether the reading knows. rfkill's (or nmcli's) answer comes first: with
// no supplicant and no link the reading's kind says off whatever the radio
// does, and the switch would then only ever go one way.
func hqWifiOn(st saverWifiState) (on, known bool) {
	if st.radioKnown {
		return st.radioOn, true
	}
	switch st.kind {
	case saverWifiConnected, saverWifiConnecting, saverWifiDisconnected:
		return true, true
	case saverWifiOff:
		return false, true
	}
	return false, false
}

// hqWifiActivate switches the radio, or opens the Wi-Fi page where nothing
// can switch it.
func hqWifiActivate(config *Config) {
	if hqWifi.busy {
		ShowToast("Wi-Fi is still switching", ToastKindInfo)
		return
	}
	if !hqWifiSwitchable() {
		if wifiEntryReachable() {
			wifiScrOpen(config)
			return
		}
		ShowToast("Wi-Fi is not available on this device", ToastKindInfo)
		return
	}
	// No reading yet: on (Quick Settings' switch is only read while the
	// panel is open, and could be hours old).
	on, _ := hqWifiOn(saverWifiNow(time.Now()))
	want := !on
	hqBegin(&hqWifi, want)
	go func() {
		err := errors.New("the switch stopped unexpectedly")
		defer func() {
			// A panic in a tool wrapper must not leave the tile switching
			// for ever.
			if r := recover(); r != nil {
				logWarn("home", "wi-fi quick tile: %v", r)
			}
			tbxPost(func() { hqWifiDone(want, err) })
		}()
		err = hqWifiSwitchNow(want)
	}()
}

// hqWifiSwitchNow switches the radio. It runs on a worker: rfkill, nmcli and
// the supplicant's start can each take seconds on the handheld.
func hqWifiSwitchNow(on bool) error {
	if !on {
		return setWifiEnabled(false)
	}
	// A supplicant start tried earlier in this session gets another go, as
	// the Wi-Fi page's own switch allows (resetSupplicantStart).
	if iface := wifiInterface(); iface != "" {
		resetSupplicantStart(iface)
	}
	if err := setWifiEnabled(true); err != nil {
		return err
	}
	// Give the interface a moment to come up, then start the supplicant where
	// the image has one and nothing listens yet, so the saved network is
	// joined again (wifi.go ensureWifiBackend).
	time.Sleep(600 * time.Millisecond)
	if wifiCommandBackend() == "" {
		ensureWifiBackend()
	}
	return nil
}

// hqWifiDone takes a finished switch back on the main loop.
func hqWifiDone(want bool, err error) {
	latest := hqFinish(&hqWifi, err)
	switch {
	case err != nil:
		ShowToast("Wi-Fi: "+firstLine(err.Error()), ToastKindWarning)
	case latest:
		PlayToggleSound()
		hqNoteSwitched(qsActionWifi, want)
	}
	saverWifiInvalidate()
	hqWifi.kicked = time.Now()
}

// hqWifiLook is the Wi-Fi tile this frame.
func hqWifiLook(now time.Time) hqLook {
	look := hqLook{glyph: "wifi", title: "Wi-Fi"}
	if hqWifi.busy {
		look.busy = true
		look.sub = "Turning off…"
		if hqWifi.want {
			look.sub = "Turning on…"
		}
		return look
	}
	// After a switch the reading is asked for again every few seconds, so
	// the network the radio rejoins shows when it does.
	if !hqWifi.done.IsZero() && now.Sub(hqWifi.done) < hqFollowFor && now.Sub(hqWifi.kicked) >= hqFollowEvery {
		hqWifi.kicked = now
		saverWifiInvalidate()
	}
	st := saverWifiNow(now)
	if hqWifi.ok && now.Sub(hqWifi.done) < hqAssumeFor {
		if on, known := hqWifiOn(st); !known || on != hqWifi.want {
			// The reading that follows the switch has not landed yet.
			return hqSwitchedLook(look, "wifi-off", hqWifi.want)
		}
	}
	switch st.kind {
	case saverWifiConnected:
		look.on = true
		look.sub = "Connected"
		switch {
		case shotModeOn():
			// A screenshot names a stand-in network (screenshotmode.go).
			look.sub = shotSSID
		case st.ssid != "":
			look.sub = st.ssid
		}
	case saverWifiConnecting:
		look.on, look.sub = true, "Connecting…"
	case saverWifiDisconnected:
		look.on, look.sub = true, "Not connected"
	case saverWifiOff:
		look.glyph, look.sub = "wifi-off", "Off"
		if st.radioKnown && st.radioOn {
			// The radio is on with no supplicant and no link: on, and A
			// switches it off (hqWifiOn).
			look.glyph = "wifi"
			look.on, look.sub = true, "Not connected"
		}
	case saverWifiWired:
		look.glyph, look.sub = "globe", "Online"
	case saverWifiOffline:
		look.glyph, look.sub = "wifi-off", "Offline"
	}
	return look
}

// hqSwitchedLook is a tile showing the state a switch just set.
func hqSwitchedLook(look hqLook, offGlyph string, on bool) hqLook {
	look.on = on
	look.sub = "On"
	if !on {
		look.glyph, look.sub = offGlyph, "Off"
	}
	return look
}

// ── Bluetooth ────────────────────────────────────────────────────────────

// hqBTActivate switches the radio, or opens the Bluetooth page where nothing
// can switch it.
func hqBTActivate(config *Config) {
	if hqBT.busy {
		ShowToast("Bluetooth is still switching", ToastKindInfo)
		return
	}
	if !hqBTSwitchable() {
		if btQuickState().Available {
			btScreenOpen(config)
			return
		}
		ShowToast("Bluetooth is not available on this device", ToastKindInfo)
		return
	}
	q := btQuickState()
	on := q.Known && q.On
	if !q.Known {
		// No kernel reading (no bluetoothctl, only rfkill): Quick Settings'
		// switch, which this tile keeps up to date, is the only memory there
		// is; else on.
		if t := hqQSToggle(qsActionBluetooth); t != nil {
			on = t.Value
		}
	}
	want := !on
	hqBegin(&hqBT, want)
	done := func(err error) { hqBTDone(want, err) }
	if want {
		btBringUpAsync(done)
		return
	}
	btPowerOffAsync(done)
}

// hqBTDone takes a finished switch back on the main loop. The bring-up and
// the power-off have toasted a failure and asked the bar's reading again
// (btQuickKick) already. A switch-off that a later switch-on outranked ends
// with no error and no work done (btPowerOffAsync): that later press moved
// qsEditGen, so it is not taken for this switch's success.
func hqBTDone(want bool, err error) {
	if !hqFinish(&hqBT, err) {
		return
	}
	switch {
	case err == nil:
		PlayToggleSound()
		hqNoteSwitched(qsActionBluetooth, want)
	case want && btBringUpFailed(err):
		// A failed start leaves the radio off, which is what the panel shows.
		hqNoteSwitched(qsActionBluetooth, false)
	}
}

// hqBTCount remembers the "N connected" line, so a steady frame formats
// nothing.
var hqBTCount struct {
	n    int
	text string
}

// hqBTConnectedText is the line for n connected devices.
func hqBTConnectedText(n int) string {
	if n != hqBTCount.n || hqBTCount.text == "" {
		hqBTCount.n = n
		hqBTCount.text = strconv.Itoa(n) + " connected"
	}
	return hqBTCount.text
}

// hqBTLook is the Bluetooth tile this frame.
func hqBTLook(now time.Time) hqLook {
	look := hqLook{glyph: "bluetooth", title: "Bluetooth"}
	if hqBT.busy {
		look.busy = true
		look.sub = "Turning off…"
		if hqBT.want {
			look.sub = "Turning on…"
		}
		return look
	}
	q := btQuickState()
	if hqBT.ok && now.Sub(hqBT.done) < hqAssumeFor && (!q.Known || q.On != hqBT.want) {
		return hqSwitchedLook(look, "bluetooth-off", hqBT.want)
	}
	switch {
	case !q.Known:
		// Windows and macOS, or no reading yet: the tile opens the page or
		// switches, and claims nothing.
	case !q.On:
		look.glyph, look.sub = "bluetooth-off", "Off"
	case q.Connected > 0:
		look.on = true
		look.glyph, look.sub = "bluetooth-connected", hqBTConnectedText(q.Connected)
	default:
		look.on, look.sub = true, "On"
	}
	return look
}

// ── Power ────────────────────────────────────────────────────────────────

// hqPowerLook is the Power tile: what its card offers on this device.
func hqPowerLook() hqLook {
	look := hqLook{glyph: "power", title: "Power", sub: "Exit JukaHub", danger: true}
	if devPowerSupported() {
		look.sub = "Sleep · Restart · Off"
	}
	return look
}

// ── Drawing ──────────────────────────────────────────────────────────────

// hqLookFor is the named quick tile this frame.
func hqLookFor(name string, now time.Time) hqLook {
	switch name {
	case hqWifiName:
		return hqWifiLook(now)
	case hqBluetoothName:
		return hqBTLook(now)
	}
	return hqPowerLook()
}

// drawHomeQuickTile paints a quick tile's face inside the glass card
// drawHomeWidget has drawn: a round switch badge — lit in the accent while the
// radio is on, half lit while it switches — and the name over the state, in
// the app tile's compositions (home_widgetdraw.go): the badge top-left on a
// small or a tall tile, at the left beside the text on a wide one.
func (h *HomeLayout) drawHomeQuickTile(renderer *sdl.Renderer, fonts HomeFonts, name string, item HomeItem, draw sdl.Rect, focused bool) {
	if h == nil || renderer == nil || fonts.Card == nil {
		return
	}
	look := hqLookFor(name, time.Now())
	titleInk, subInk := ColorTextPrimary(), ColorTextMuted()
	pad := scaleX(homeTilePad, h.Width)
	switch {
	case draw.H >= scaleY(250, h.Height):
		size := h.hwpTallBadge(h.hwpSettled(item.Rect, draw))
		hqDrawBadge(renderer, sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: size, H: size}, look, focused, true)
		subFont := fonts.Helper
		if subFont == nil {
			subFont = fonts.Meta
		}
		if textW := draw.W - pad*2; textW >= 24 {
			h.hwpLabels(renderer, fonts.Card, subFont, look.title, look.sub, draw.X+pad, draw.Y+draw.H-pad, textW, titleInk, subInk)
		}
	case draw.W >= scaleX(300, h.Width):
		size := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
		if maxIcon := draw.H - pad*2; size > maxIcon {
			size = maxIcon
		}
		if size < scaleY(22, h.Height) {
			return
		}
		badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + (draw.H-size)/2, W: size, H: size}
		hqDrawBadge(renderer, badge, look, focused, false)
		textX := badge.X + badge.W + scaleX(16, h.Width)
		textW := draw.X + draw.W - pad - textX
		subFont := fonts.Helper
		if subFont == nil {
			subFont = fonts.Meta
		}
		if textW < 24 {
			return
		}
		// The name and the state centred top to bottom as one block
		// (hwpLabels leaves out a state it has no font for).
		titleH, subH := int32(fonts.Card.Height()), int32(0)
		if look.sub != "" && subFont != nil {
			subH = int32(subFont.Height()) + scaleY(2, h.Height)
		}
		h.hwpLabels(renderer, fonts.Card, subFont, look.title, look.sub, textX, draw.Y+(draw.H+titleH+subH)/2, textW, titleInk, subInk)
	default:
		size := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
		if maxIcon := min32(draw.W-pad*2, draw.H-pad*2-scaleY(homeGlyphSize, h.Height)); size > maxIcon {
			size = maxIcon
		}
		if size < scaleY(22, h.Height) {
			return
		}
		badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: size, H: size}
		hqDrawBadge(renderer, badge, look, focused, false)
		titleFont, subFont := fonts.Tile, fonts.Meta
		if titleFont == nil {
			titleFont = fonts.Card
		}
		if subFont == nil {
			subFont = fonts.Helper
		}
		// Bottom-aligned, but never over the badge (hwpAppSmall's rule): a
		// short tile lets the text run to its bottom edge instead.
		bottom := draw.Y + draw.H - pad
		need := int32(titleFont.Height())
		if look.sub != "" && subFont != nil {
			need += int32(subFont.Height()) + scaleY(2, h.Height)
		}
		if top := badge.Y + badge.H + scaleY(4, h.Height); bottom-need < top {
			bottom = top + need
		}
		if textW := draw.W - pad*2; textW >= 24 {
			h.hwpLabels(renderer, titleFont, subFont, look.title, look.sub, draw.X+pad, bottom, textW, titleInk, subInk)
		}
	}
}

// hqDrawBadge paints the round switch badge and its glyph. big sets the glyph
// at half the badge (a tall tile), as hwpBadgeGlyph does.
func hqDrawBadge(renderer *sdl.Renderer, badge sdl.Rect, look hqLook, focused, big bool) {
	if badge.W <= 0 || badge.H <= 0 {
		return
	}
	accent := glassAccent()
	fill, edge, ink := homeGlass(20), homeGlass(16), ColorTextSecondary()
	switch {
	case look.on:
		fill, edge, ink = accent, accent, contrastText(accent)
	case look.busy:
		fill, edge, ink = WithAlpha(accent, 110), WithAlpha(accent, 170), ColorTextPrimary()
	case focused:
		// drawIconBadge's focused badge: a tinted well with the glyph in the
		// accent.
		fill, edge, ink = sdl.Color{R: 6, G: 32, B: 42, A: 140}, homeGlass(40), ColorAccent
	}
	if look.danger && focused {
		ink = DangerInk()
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	r := badge.W / 2
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, r, fill)
	strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, r, 1, edge)
	g := min32(homeGlyphSize, badge.W-badge.W/4)
	if big {
		g = badge.W / 2
	}
	drawHomeGlyphTinted(renderer, look.glyph, sdl.Rect{X: badge.X + (badge.W-g)/2, Y: badge.Y + (badge.H-g)/2, W: g, H: g}, ink)
}
