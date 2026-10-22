package main

import (
	"fmt"
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Quick Settings Panel — the device's own switches, not a mock-up
// ──────────────────────────────────────────────────────────────────────
//
// Every row here changes something that outlives the panel: the radios through
// nmcli/rfkill/bluetoothctl, the mixer through pactl/amixer, the backlight
// through one allowlisted sysfs device, and the rest through the config that is
// saved to jukauser.json. Rows whose backend is missing on this machine render
// dimmed and explain themselves instead of pretending to work.

type qsAction int

const (
	qsActionNone qsAction = iota
	qsActionWifi
	qsActionBluetooth
	qsActionAirplane
	qsActionLedSync
	qsActionLowPower
	qsActionReducedMotion
	qsActionFullscreen
)

type qsToggle struct {
	Label     string
	Icon      string
	Value     bool
	Action    qsAction
	OnColor   sdl.Color
	OffColor  sdl.Color
	Available bool
	Detail    string // short reason shown when the backend is missing
}

type qsSlider struct {
	Label     string
	Icon      string
	Action    string // "volume" | "brightness"
	Value     float64
	Min       float64
	Max       float64
	Available bool
	Detail    string
}

type qsCycle struct {
	Label string
	Icon  string
	Value int // seconds
}

// qsLinkAction names a row that opens another panel instead of changing a
// value. Keeping it a separate row type means the switch stays a switch.
type qsLinkAction int

const (
	qsLinkNone qsLinkAction = iota
	qsLinkWiFi
)

type qsLink struct {
	Label     string
	Icon      string
	Action    qsLinkAction
	Available bool
	Detail    string
}

type qsState struct {
	open    bool
	cursor  int
	toggles []qsToggle
	sliders []qsSlider
	links   []qsLink
	cycle   qsCycle
	enter   uiEnter // rise + fade entrance

	// lastVolume/lastBrightness remember what was already written to the host,
	// so a held direction key does not fork one mixer process per repeat.
	lastVolume     int
	lastBrightness int

	// Airplane mode remembers what the radios were doing before it blocked them,
	// so switching it off restores the user's setup instead of switching every
	// radio on behind their back.
	airplaneSavedWifi bool
	airplaneWifiWasOn bool
	airplaneSavedBT   bool
	airplaneBTWasOn   bool
}

var qs qsState

var (
	qsToggleOn  = sdl.Color{R: 80, G: 160, B: 255, A: 255}
	qsToggleOff = sdl.Color{R: 60, G: 65, B: 80, A: 200}
)

func qsInit() {
	qs = qsState{
		toggles: []qsToggle{
			{Label: "Wi-Fi", Icon: "[W]", Action: qsActionWifi,
				OnColor: qsToggleOn, OffColor: qsToggleOff},
			{Label: "Bluetooth", Icon: "[B]", Action: qsActionBluetooth,
				OnColor: sdl.Color{R: 90, G: 130, B: 245, A: 255}, OffColor: qsToggleOff},
			{Label: "Airplane Mode", Icon: "[A]", Action: qsActionAirplane,
				OnColor: sdl.Color{R: 240, G: 180, B: 60, A: 255}, OffColor: qsToggleOff},
			{Label: "LED Sync", Icon: "[R]", Action: qsActionLedSync,
				OnColor: sdl.Color{R: 255, G: 105, B: 180, A: 255}, OffColor: qsToggleOff},
			{Label: "Low Power", Icon: "[=]", Action: qsActionLowPower,
				OnColor: sdl.Color{R: 80, G: 200, B: 120, A: 255}, OffColor: qsToggleOff},
			{Label: "Reduced Motion", Icon: "[M]", Action: qsActionReducedMotion,
				OnColor: sdl.Color{R: 200, G: 160, B: 80, A: 255}, OffColor: qsToggleOff},
			{Label: "Fullscreen", Icon: "[F]", Action: qsActionFullscreen,
				OnColor: sdl.Color{R: 180, G: 100, B: 255, A: 255}, OffColor: qsToggleOff},
		},
		sliders: []qsSlider{
			{Label: "Volume", Icon: "[V]", Action: "volume", Value: 0.8, Min: 0, Max: 1},
			{Label: "Brightness", Icon: "[L]", Action: "brightness",
				Value: 1, Min: float64(qsBrightnessFloorPct) / 100, Max: 1},
		},
		links: []qsLink{
			{Label: "Wi-Fi Networks", Icon: "[N]", Action: qsLinkWiFi},
		},
		// The app's idle timer drives the screensaver overlay, not a display
		// blank, so the row says what it actually controls.
		cycle: qsCycle{Label: "Screensaver", Icon: "[T]", Value: 300},
		// -1 means "nothing written yet", so the first move always applies.
		lastVolume:     -1,
		lastBrightness: -1,
	}
	qsRefreshAvailability()
	qsLoadPersisted()
}

// qsRefreshAvailability probes which backends exist. It only looks for binaries
// (cheap) — reading their state happens when the panel opens.
func qsRefreshAvailability() {
	available := map[qsAction]bool{
		qsActionWifi:          wifiBackend() != "",
		qsActionBluetooth:     bluetoothBackend() != "",
		qsActionAirplane:      airplaneBackend(),
		qsActionLedSync:       ledSyncAvailable(),
		qsActionLowPower:      true,
		qsActionReducedMotion: true,
		qsActionFullscreen:    true,
	}
	detail := map[qsAction]string{
		qsActionWifi:      "no nmcli/rfkill",
		qsActionBluetooth: "no bluetoothctl/rfkill",
		qsActionAirplane:  "no rfkill",
		qsActionLedSync:   "no addressable LED",
	}
	for i := range qs.toggles {
		t := &qs.toggles[i]
		t.Available = available[t.Action]
		t.Detail = detail[t.Action]
	}
	for i := range qs.links {
		l := &qs.links[i]
		switch l.Action {
		case qsLinkWiFi:
			l.Available = wifiControlAvailable()
			// The row is 400 px wide; the full reason lives in the picker's
			// header and in the toast, so the row only says there is no backend.
			l.Detail = "N/A"
		}
	}
	volumeOK := volumeBackend() != ""
	brightnessOK := brightnessAvailable()
	qs.sliders[0].Available = volumeOK
	qs.sliders[0].Detail = "no pactl/amixer"
	qs.sliders[1].Available = brightnessOK
	qs.sliders[1].Detail = "no backlight device"
}

// qsLoadPersisted restores the saved levels so the panel opens where the user
// left it, even before any live probe succeeds.
func qsLoadPersisted() {
	if user := loadUserConfig(); user != nil {
		if user.Variables.VolumeSet {
			qs.sliders[0].Value = clampQSPercent(user.Variables.Volume)
		}
		if user.Variables.BrightnessSet {
			qs.sliders[1].Value = clampQSPercent(user.Variables.Brightness)
			// A saved 0 is a floor, not a black screen: the panel never starts
			// below the value the device itself would refuse to go under.
			if qs.sliders[1].Value < qs.sliders[1].Min {
				qs.sliders[1].Value = qs.sliders[1].Min
			}
		}
	}
	if appConfig != nil && appConfig.Variables.ScreensaverTimeout > 0 {
		qs.cycle.Value = appConfig.Variables.ScreensaverTimeout
	}
}

// qsApplyPersistedSystemState pushes the saved volume and brightness to the
// device at launch. Nothing is written unless the user has actually set a
// value, so a fresh install keeps the device's own defaults.
func qsApplyPersistedSystemState() {
	user := loadUserConfig()
	if user == nil {
		return
	}
	if user.Variables.VolumeSet && volumeBackend() != "" {
		if err := setSystemVolume(user.Variables.Volume); err != nil {
			LogScene("quicksettings").Warn("restore volume failed", "err", err)
		}
	}
	if user.Variables.BrightnessSet && brightnessAvailable() {
		if err := setScreenBrightness(user.Variables.Brightness); err != nil {
			LogScene("quicksettings").Warn("restore brightness failed", "err", err)
		}
	}
}

func clampQSPercent(v int) float64 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return float64(v) / 100
}

// qsRowCount is the number of interactive rows: toggles, sliders, links, then
// the timeout cycle.
func qsRowCount() int { return len(qs.toggles) + len(qs.sliders) + len(qs.links) + 1 }

// qsSyncFromConfig refreshes the rows whose truth lives in the config, then
// asks the host for the radio and level state.
func qsSyncFromConfig(config *Config) {
	if config != nil {
		for i := range qs.toggles {
			switch qs.toggles[i].Action {
			case qsActionLowPower:
				qs.toggles[i].Value = config.Variables.LowPower
			case qsActionReducedMotion:
				qs.toggles[i].Value = config.Variables.ReducedMotion
			case qsActionFullscreen:
				qs.toggles[i].Value = config.Variables.Fullscreen
			case qsActionLedSync:
				qs.toggles[i].Value = config.Variables.LEDSync
			}
		}
		if config.Variables.ScreensaverTimeout > 0 {
			qs.cycle.Value = config.Variables.ScreensaverTimeout
		}
	}
	qsRefreshAvailability()
	qsStartProbe()
}

// ── Live state probing ─────────────────────────────────────────────────────
//
// Reading the radios means forking nmcli/bluetoothctl/rfkill, and one of those
// can take a second to answer on a 1 GB handheld. The probe therefore runs off
// the event thread and hands its result back through a one-slot channel that
// the render loop drains without blocking. A result that never arrives leaves
// the saved values in place, which is better than a row that flaps.

type qsProbe struct {
	wifi, bluetooth, airplane       bool
	wifiOK, bluetoothOK, airplaneOK bool
	volume, brightness              int
	volumeOK, brightnessOK          bool
}

var (
	qsProbeCh      = make(chan qsProbe, 1)
	qsProbePending bool
)

// qsCollectLiveState asks the host what the radios and levels are doing. It runs
// on a worker goroutine, so it takes everything it needs as arguments and never
// reads (or writes) the live qs state.
func qsCollectLiveState(wantWifi, wantBT, wantAirplane, wantVolume, wantBrightness bool) qsProbe {
	r := qsProbe{}
	if wantWifi {
		r.wifi, r.wifiOK = wifiEnabled()
	}
	if wantBT {
		r.bluetooth, r.bluetoothOK = bluetoothEnabled()
	}
	if wantAirplane {
		r.airplane, r.airplaneOK = airplaneEnabled()
	}
	if wantVolume {
		r.volume, r.volumeOK = systemVolume()
	}
	if wantBrightness {
		r.brightness, r.brightnessOK = screenBrightness()
	}
	return r
}

func qsStartProbe() {
	if qsProbePending {
		return
	}
	qsProbePending = true
	wantWifi := qs.toggles[0].Available
	wantBT := qs.toggles[1].Available
	wantAirplane := qs.toggles[2].Available
	wantVolume := qs.sliders[0].Available
	wantBrightness := qs.sliders[1].Available
	go func() {
		qsProbeCh <- qsCollectLiveState(wantWifi, wantBT, wantAirplane, wantVolume, wantBrightness)
	}()
}

// qsDrainProbe applies a finished probe. Called from the render loop, which runs
// on the event thread, so the state it writes is never read concurrently.
func qsDrainProbe() {
	select {
	case r := <-qsProbeCh:
		qsProbePending = false
		qsApplyProbe(r)
	default:
	}
}

func qsApplyProbe(r qsProbe) {
	if r.wifiOK {
		qs.toggles[0].Value = r.wifi
	}
	if r.bluetoothOK {
		qs.toggles[1].Value = r.bluetooth
	}
	if r.airplaneOK {
		qs.toggles[2].Value = r.airplane
		if r.airplane {
			// Whatever the individual daemons think, nothing is transmitting while
			// every radio is blocked.
			qs.toggles[0].Value = false
			qs.toggles[1].Value = false
		}
	}
	if r.volumeOK && qs.sliders[0].Available {
		qs.sliders[0].Value = float64(r.volume) / 100
		qs.lastVolume = r.volume
	}
	if r.brightnessOK && qs.sliders[1].Available {
		qs.sliders[1].Value = clampQSPercent(r.brightness)
		qs.lastBrightness = r.brightness
	}
}

func qsToggleOpen() {
	qs.open = !qs.open
	if qs.open {
		qs.cursor = 0
		qs.enter.begin(uiClockMS(), uiPanelEnterMS)
		if appConfig != nil {
			qsSyncFromConfig(appConfig)
		} else {
			qsRefreshAvailability()
		}
	}
}

func qsNavigate(dr int) {
	total := qsRowCount()
	if total == 0 {
		return
	}
	qs.cursor += dr
	if qs.cursor < 0 {
		qs.cursor = total - 1
	}
	if qs.cursor >= total {
		qs.cursor = 0
	}
	PlayNavSound()
}

func qsSelect() {
	if qs.cursor < len(qs.toggles) {
		qsToggleRow(qs.cursor)
		return
	}
	idx := qs.cursor - len(qs.toggles)
	if idx < len(qs.sliders) {
		qsStepSlider(idx, 10)
		return
	}
	idx -= len(qs.sliders)
	if idx < len(qs.links) {
		qsOpenLink(qs.links[idx])
		return
	}
	qsCycleTimeout(1)
}

// qsOpenLink opens the panel behind a link row. It is a no-op with a reason on
// devices where that panel's backend is missing.
func qsOpenLink(l qsLink) {
	if !l.Available {
		reason := l.Detail
		if l.Action == qsLinkWiFi {
			reason = wifiUnavailableReason()
		}
		ShowToast(l.Label+" is unavailable on this device ("+reason+")", ToastKindInfo)
		return
	}
	switch l.Action {
	case qsLinkWiFi:
		openWiFiPanel()
	}
}

// qsNavigateSlider handles the left/right input. On the sliders dr is the
// percentage step (the controller sends ±5, the keyboard ±5); on the timeout
// row it moves one step in that direction.
func qsNavigateSlider(dr int) {
	if dr == 0 {
		return
	}
	idx := qs.cursor - len(qs.toggles)
	if idx >= 0 && idx < len(qs.sliders) {
		qsStepSlider(idx, dr)
		return
	}
	if qs.cursor >= len(qs.toggles)+len(qs.sliders)+len(qs.links) {
		if dr > 0 {
			qsCycleTimeout(1)
		} else {
			qsCycleTimeout(-1)
		}
	}
}

// qsToggleRow flips one switch and reverts it when the host refuses.
func qsToggleRow(i int) {
	if i < 0 || i >= len(qs.toggles) {
		return
	}
	t := &qs.toggles[i]
	if !t.Available {
		ShowToast(t.Label+" is unavailable on this device ("+t.Detail+")", ToastKindInfo)
		return
	}
	want := !t.Value
	if err := qsApplyToggle(t, want); err != nil {
		ShowToast(t.Label+": "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	t.Value = want
	PlayToggleSound()
	if appConfig != nil {
		qsPersist(appConfig)
	}
}

// qsApplyToggle performs the change behind one switch.
func qsApplyToggle(t *qsToggle, want bool) error {
	switch t.Action {
	case qsActionWifi:
		if err := setWifiEnabled(want); err != nil {
			return err
		}
		qsStartProbe()
		return nil
	case qsActionBluetooth:
		if err := setBluetoothEnabled(want); err != nil {
			return err
		}
		qsStartProbe()
		return nil
	case qsActionAirplane:
		if want {
			// Snapshot the radios before blocking so unblocking can restore them.
			if on, ok := wifiEnabled(); ok {
				qs.airplaneWifiWasOn, qs.airplaneSavedWifi = on, true
			}
			if on, ok := bluetoothEnabled(); ok {
				qs.airplaneBTWasOn, qs.airplaneSavedBT = on, true
			}
			if err := setAirplaneMode(true); err != nil {
				return err
			}
		} else {
			if err := setAirplaneMode(false); err != nil {
				return err
			}
			// rfkill unblocks everything; put back what the user had off.
			if qs.airplaneSavedWifi && !qs.airplaneWifiWasOn {
				_ = setWifiEnabled(false)
			}
			if qs.airplaneSavedBT && !qs.airplaneBTWasOn {
				_ = setBluetoothEnabled(false)
			}
			qs.airplaneSavedWifi, qs.airplaneSavedBT = false, false
		}
		qsStartProbe()
		return nil
	case qsActionLedSync:
		if err := SetLEDSync(want); err != nil {
			return err
		}
		if appConfig != nil {
			appConfig.Variables.LEDSync = want
		}
		return nil
	case qsActionLowPower:
		if appConfig != nil {
			appConfig.Variables.LowPower = want
			syncVariableOverrides(appConfig)
		}
		return nil
	case qsActionReducedMotion:
		if appConfig != nil {
			appConfig.Variables.ReducedMotion = want
			syncVariableOverrides(appConfig)
		}
		return nil
	case qsActionFullscreen:
		if appConfig != nil {
			appConfig.Variables.Fullscreen = want
			if mainWindow != nil {
				if want {
					mainWindow.SetFullscreen(sdl.WINDOW_FULLSCREEN_DESKTOP)
				} else {
					mainWindow.SetFullscreen(0)
				}
			}
			syncVariableOverrides(appConfig)
		}
		return nil
	}
	return nil
}

// qsStepSlider moves a level by deltaPct and applies it. The write is skipped
// when the rounded percentage has not changed, so key repeat cannot spawn a
// process per frame.
func qsStepSlider(idx, deltaPct int) {
	if idx < 0 || idx >= len(qs.sliders) {
		return
	}
	s := &qs.sliders[idx]
	if !s.Available {
		ShowToast(s.Label+" control is unavailable on this device ("+s.Detail+")", ToastKindInfo)
		return
	}
	value := s.Value + float64(deltaPct)/100
	if value < s.Min {
		value = s.Min
	}
	if value > s.Max {
		value = s.Max
	}
	s.Value = value
	pct := int(math.Round(s.Value * 100))
	switch s.Action {
	case "volume":
		if pct == qs.lastVolume {
			return
		}
		if err := setSystemVolume(pct); err != nil {
			ShowToast("Volume: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		qs.lastVolume = pct
	case "brightness":
		if pct == qs.lastBrightness {
			return
		}
		if err := setScreenBrightness(pct); err != nil {
			ShowToast("Brightness: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		qs.lastBrightness = pct
	}
	PlayNavSound()
	if appConfig != nil {
		qsPersist(appConfig)
	}
}

// qsCycleTimeout moves the screen timeout to the next (or previous) step.
func qsCycleTimeout(dir int) {
	var next int
	if dir >= 0 {
		next = nextScreenTimeout(qs.cycle.Value)
	} else {
		next = qs.cycle.Value
		for i := len(screenTimeoutSteps) - 1; i >= 0; i-- {
			if screenTimeoutSteps[i] < qs.cycle.Value {
				next = screenTimeoutSteps[i]
				break
			}
		}
		if next == qs.cycle.Value {
			next = screenTimeoutSteps[len(screenTimeoutSteps)-1]
		}
	}
	qs.cycle.Value = next
	if appConfig != nil {
		appConfig.Variables.ScreensaverTimeout = next
		screensaverIdleTimeout = time.Duration(next) * time.Second
		qsPersist(appConfig)
	}
	PlayNavSound()
}

// qsPersist snapshots the panel's values into jukauser.json through the same
// debounced writer the settings page uses. Untouched fields are left alone
// because the cached user config is updated in place.
func qsPersist(config *Config) {
	user := loadUserConfig()
	if user == nil {
		return
	}
	if qs.sliders[0].Available {
		user.Variables.Volume = int(math.Round(qs.sliders[0].Value * 100))
		user.Variables.VolumeSet = true
	}
	if qs.sliders[1].Available {
		user.Variables.Brightness = int(math.Round(qs.sliders[1].Value * 100))
		user.Variables.BrightnessSet = true
	}
	user.Variables.ScreensaverTimeout = qs.cycle.Value
	if config != nil {
		user.Variables.LedSync = config.Variables.LEDSync
		user.Variables.LowPower = config.Variables.LowPower
		user.Variables.ReducedMotion = config.Variables.ReducedMotion
		user.Variables.Fullscreen = config.Variables.Fullscreen
		user.Variables.Custom = config.Variables.Custom
	}
	saveUserConfigDebounced(user)
}

// ── Rendering ──────────────────────────────────────────────────────────────

const (
	qsPadX      = int32(16)
	qsHeaderH   = int32(56)
	qsFooterH   = int32(66)
	qsItemGap   = int32(4)
	qsPanelWide = int32(400)
)

func renderQuickSettings(renderer *sdl.Renderer, config *Config) {
	if !qs.open {
		return
	}
	// Pick up a finished radio/level probe before drawing this frame.
	qsDrainProbe()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	// Entrance keyframes: the scrim fades and the panel rises into place as a
	// single layer, so the whole overlay settles instead of popping.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = qs.enter.progress(uiClockMS())
	} else {
		qs.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiPanelRise.at(progress)))

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, uint8(160*uiScrimProgress(progress, uiPanelEnterMS)))
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	panelW := qsPanelWide
	if maxW := screenWidth - 24; panelW > maxW {
		panelW = maxW
	}

	// The panel grows with the row list and shrinks the rows before it would
	// run off a 480p screen.
	rows := qsRowCount()
	itemH := int32(44)
	panelH := qsPanelHeight(rows, itemH)
	maxH := screenHeight - 24
	for panelH > maxH && itemH > 32 {
		itemH--
		panelH = qsPanelHeight(rows, itemH)
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}, func() {
		UICard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}, UICardStyle{
			Fill:   sdl.Color{R: 14, G: 18, B: 28, A: 248},
			Border: ColorBorder,
			Radius: 20,
		})

		if medFont != nil {
			title := "Quick Settings"
			tw, _, _ := medFont.SizeUTF8(title)
			renderText(renderer, config, medFont, title,
				sdl.Color{R: 220, G: 230, B: 245, A: 240},
				panelX+(panelW-int32(tw))/2, panelY+16)
		}

		itemX := panelX + qsPadX
		itemW := panelW - 2*qsPadX
		y := panelY + qsHeaderH

		for i := range qs.toggles {
			drawQSToggleRow(renderer, config, font, itemX, y, itemW, itemH, &qs.toggles[i], qs.cursor == i)
			y += itemH + qsItemGap
		}
		for i := range qs.sliders {
			drawQSSliderRow(renderer, config, font, itemX, y, itemW, itemH, &qs.sliders[i],
				qs.cursor == len(qs.toggles)+i)
			y += itemH + qsItemGap
		}
		for i := range qs.links {
			drawQSLinkRow(renderer, config, font, itemX, y, itemW, itemH, &qs.links[i],
				qs.cursor == len(qs.toggles)+len(qs.sliders)+i)
			y += itemH + qsItemGap
		}
		drawQSCycleRow(renderer, config, font, itemX, y, itemW, itemH,
			qs.cursor == len(qs.toggles)+len(qs.sliders)+len(qs.links))

		// System info at the bottom: battery on the left, connectivity right.
		// Both lines are positioned from the font height rather than fixed
		// offsets: with a large UI scale the footer used to sit on the panel's
		// bottom edge and the connectivity line touched it.
		smallH := int32(font.Height())
		footerY := panelY + panelH - smallH - 8
		infoY := footerY - smallH - 8
		if bat := getBatteryPercent(); bat >= 0 {
			renderText(renderer, config, font, fmt.Sprintf("Battery %d%%", bat),
				sdl.Color{R: 140, G: 155, B: 180, A: 200}, panelX+qsPadX, infoY)
		}
		netStr := "Online"
		if !IsOnline() {
			netStr = "! Offline"
		}
		nw, _, _ := font.SizeUTF8(netStr)
		renderText(renderer, config, font, netStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200},
			panelX+panelW-int32(nw)-qsPadX, infoY)

		hint := "A change · Left/Right adjust · B close"
		hw, _, _ := font.SizeUTF8(hint)
		renderText(renderer, config, font, hint,
			sdl.Color{R: 90, G: 100, B: 125, A: 190},
			panelX+(panelW-int32(hw))/2, footerY)
	})
}

// qsLabelWidth is the room left for a row label after the icon column and the
// right-aligned detail (a missing backend, a level or "Open >"). Measuring the
// detail instead of reserving a fixed strip keeps "Airplane Mode" whole while
// still stopping "Bluetooth" from running into "bluetoothctl/rfkill" on dev
// builds where every backend is missing.
func qsLabelWidth(font *ttf.Font, rowW int32, detail string) int32 {
	reserve := int32(0)
	if detail != "" {
		if dw, _, err := font.SizeUTF8(detail); err == nil {
			reserve = int32(dw) + 24
		}
	}
	w := rowW - 52 - 12 - reserve
	if w < 60 {
		w = 60
	}
	return w
}

func qsPanelHeight(rows int, itemH int32) int32 {
	return qsHeaderH + int32(rows)*(itemH+qsItemGap) + qsFooterH
}

func qsRowBackground(renderer *sdl.Renderer, x, y, w, h int32, focused bool) {
	if !focused {
		return
	}
	fillRoundedRect(renderer, x, y, w, h, 10, sdl.Color{R: 30, G: 36, B: 50, A: 200})
	strokeRoundedRect(renderer, x, y, w, h, 10, 1,
		sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 120})
}

func drawQSToggleRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, t *qsToggle, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	labelCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
	iconCol := sdl.Color{R: 200, G: 210, B: 230, A: 220}
	detail := ""
	if !t.Available {
		// A row with no backend is dimmed; the switch is not drawn because
		// there is nothing for it to represent. The row says only "N/A": the
		// specific reason ("no nmcli/rfkill") is too long for a 400 px row and
		// is shown in the toast when A is pressed.
		labelCol = sdl.Color{R: 110, G: 118, B: 135, A: 180}
		iconCol = labelCol
		detail = "N/A"
	}

	renderText(renderer, config, font, t.Icon, iconCol, x+8, y+10)
	renderText(renderer, config, font, ellipsize(font, t.Label, qsLabelWidth(font, w, detail)), labelCol, x+52, y+10)
	if detail != "" {
		dw, _, _ := font.SizeUTF8(detail)
		renderText(renderer, config, font, detail,
			sdl.Color{R: 90, G: 98, B: 115, A: 160}, x+w-int32(dw)-12, y+10)
		return
	}

	swW := int32(44)
	swH := int32(24)
	swX := x + w - swW - 8
	swY := y + (h-swH)/2
	swFill := t.OffColor
	if t.Value {
		swFill = t.OnColor
	}
	fillRoundedRect(renderer, swX, swY, swW, swH, swH/2, swFill)

	knobR := int32(8)
	knobX := swX + knobR + 4
	if t.Value {
		knobX = swX + swW - knobR - 4
	}
	fillCircle(renderer, knobX, swY+swH/2, knobR, sdl.Color{R: 255, G: 255, B: 255, A: 240})
}

func drawQSSliderRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, s *qsSlider, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	labelCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
	iconCol := sdl.Color{R: 200, G: 210, B: 230, A: 220}
	if !s.Available {
		labelCol = sdl.Color{R: 110, G: 118, B: 135, A: 180}
		iconCol = labelCol
	}

	detail := ""
	if !s.Available {
		// See drawQSToggleRow: the full reason belongs in the toast, not in a
		// row that also has to fit its label.
		detail = "N/A"
	}
	renderText(renderer, config, font, s.Icon, iconCol, x+8, y+6)
	renderText(renderer, config, font, ellipsize(font, s.Label, qsLabelWidth(font, w, detail)), labelCol, x+52, y+6)

	if detail != "" {
		dw, _, _ := font.SizeUTF8(detail)
		renderText(renderer, config, font, detail,
			sdl.Color{R: 90, G: 98, B: 115, A: 160}, x+w-int32(dw)-12, y+6)
		return
	}

	pct := int(math.Round(s.Value * 100))
	valStr := fmt.Sprintf("%d%%", pct)
	vw, _, _ := font.SizeUTF8(valStr)
	renderText(renderer, config, font, valStr,
		sdl.Color{R: 140, G: 155, B: 180, A: 200}, x+w-int32(vw)-12, y+6)

	trackW := w - 130
	trackX := x + 100
	trackY := y + h - 16
	trackH := int32(6)
	fillRoundedRect(renderer, trackX, trackY, trackW, trackH, 3,
		sdl.Color{R: 30, G: 36, B: 50, A: 200})

	frac := 0.0
	if s.Max > s.Min {
		frac = (s.Value - s.Min) / (s.Max - s.Min)
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fillW := int32(float64(trackW) * frac)
	if fillW > 0 {
		fillRoundedRect(renderer, trackX, trackY, fillW, trackH, 3,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200})
	}
	fillCircle(renderer, trackX+fillW, trackY+trackH/2, 6,
		sdl.Color{R: 240, G: 245, B: 255, A: 240})
}

func drawQSLinkRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, l *qsLink, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	labelCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
	iconCol := sdl.Color{R: 200, G: 210, B: 230, A: 220}
	if !l.Available {
		labelCol = sdl.Color{R: 110, G: 118, B: 135, A: 180}
		iconCol = labelCol
	}
	right := "Open >"
	if !l.Available {
		right = l.Detail
	}
	renderText(renderer, config, font, l.Icon, iconCol, x+8, y+10)
	renderText(renderer, config, font, ellipsize(font, l.Label, qsLabelWidth(font, w, right)), labelCol, x+52, y+10)
	rw, _, _ := font.SizeUTF8(right)
	renderText(renderer, config, font, right,
		sdl.Color{R: 120, G: 132, B: 152, A: 190}, x+w-int32(rw)-12, y+10)
}

func drawQSCycleRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	renderText(renderer, config, font, qs.cycle.Icon,
		sdl.Color{R: 200, G: 210, B: 230, A: 220}, x+8, y+10)
	renderText(renderer, config, font, qs.cycle.Label,
		sdl.Color{R: 180, G: 190, B: 210, A: 220}, x+52, y+10)

	value := screenTimeoutLabel(qs.cycle.Value)
	vw, _, _ := font.SizeUTF8(value)
	renderText(renderer, config, font, value,
		sdl.Color{R: 140, G: 155, B: 180, A: 220}, x+w-int32(vw)-12, y+10)
}

func handleQSInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN || !qs.open {
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_UP:
		qsNavigate(-1)
	case sdl.K_DOWN:
		qsNavigate(1)
	case sdl.K_LEFT:
		qsNavigateSlider(-5)
	case sdl.K_RIGHT:
		qsNavigateSlider(5)
	case sdl.K_RETURN, sdl.K_SPACE:
		qsSelect()
	case sdl.K_ESCAPE, sdl.K_b:
		qs.open = false
		PlayBackSound()
	}
}
