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
	qsActionQuiet
	qsActionFullscreen
	qsActionNetplay
	// The zoom lens (magnifier.go) is a comfort switch, not a radio: it has no
	// backend to probe and works on every device.
	qsActionMagnifier
	// The WireGuard VPN (vpn.go), offered once a configuration is set.
	qsActionVPN
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
	Value int // timeout or a mode index; each cycle row defines its own scale
}

// qsLinkAction names a row that opens another panel instead of changing a
// value. Keeping it a separate row type means the switch stays a switch.
type qsLinkAction int

const (
	qsLinkNone qsLinkAction = iota
	qsLinkWiFi
	qsLinkControls
	qsLinkLEDPanel
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
	cycles  []qsCycle
	enter   uiEnter // rise + fade entrance

	// lastVolume/lastBrightness remember what was already written to the host,
	// so a held direction key does not fork one mixer process per repeat.
	lastVolume     int
	lastBrightness int

	// scroll is the first row's offset in the list, in pixels. The panel caps
	// its height and scrolls the rows when they do not fit a short screen.
	scroll int32

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
			{Label: "VPN", Icon: "[K]", Action: qsActionVPN,
				OnColor: sdl.Color{R: 70, G: 190, B: 150, A: 255}, OffColor: qsToggleOff},
			{Label: "LED Sync", Icon: "[R]", Action: qsActionLedSync,
				OnColor: sdl.Color{R: 255, G: 105, B: 180, A: 255}, OffColor: qsToggleOff},
			{Label: "Low Power", Icon: "[=]", Action: qsActionLowPower,
				OnColor: sdl.Color{R: 80, G: 200, B: 120, A: 255}, OffColor: qsToggleOff},
			{Label: "Reduced Motion", Icon: "[M]", Action: qsActionReducedMotion,
				OnColor: sdl.Color{R: 200, G: 160, B: 80, A: 255}, OffColor: qsToggleOff},
			{Label: "Quiet Mode", Icon: "[Q]", Action: qsActionQuiet,
				OnColor: sdl.Color{R: 150, G: 120, B: 230, A: 255}, OffColor: qsToggleOff},
			{Label: "Fullscreen", Icon: "[F]", Action: qsActionFullscreen,
				OnColor: sdl.Color{R: 180, G: 100, B: 255, A: 255}, OffColor: qsToggleOff},
			// LAN play is a hub feature, not a radio: the toggle only decides
			// whether the Game Library offers the panel.
			{Label: "LAN Play", Icon: "[G]", Action: qsActionNetplay,
				OnColor: sdl.Color{R: 80, G: 200, B: 170, A: 255}, OffColor: qsToggleOff},
			{Label: "Magnifier", Icon: "[+]", Action: qsActionMagnifier,
				OnColor: sdl.Color{R: 120, G: 200, B: 255, A: 255}, OffColor: qsToggleOff},
		},
		sliders: []qsSlider{
			{Label: "Volume", Icon: "[V]", Action: "volume", Value: 0.8, Min: 0, Max: 1},
			{Label: "Brightness", Icon: "[L]", Action: "brightness",
				Value: 1, Min: float64(qsBrightnessFloorPct) / 100, Max: 1},
		},
		links: []qsLink{
			{Label: "Wi-Fi Networks", Icon: "[N]", Action: qsLinkWiFi},
			// The controller legend's card needs nothing from the device, so
			// its row is offered on every host (see qsLinkControls below).
			{Label: "Controller Legend", Icon: "[?]", Action: qsLinkControls},
			// Colour, effect and brightness live in their own panel; the LED
			// Mode cycler below stays the one-press shortcut.
			{Label: "LED Style", Icon: "[R]", Action: qsLinkLEDPanel},
		},
		// The app's idle timer drives the screensaver overlay, not a display
		// blank, so the row says what it actually controls.
		cycles: []qsCycle{
			{Label: "Screensaver", Icon: "[T]", Value: 25},
			{Label: "Power Mode", Icon: "[P]", Value: qsPowerModeRowIndex()},
			{Label: "Spatial audio", Icon: "[S]", Value: qsSpatialModeRowIndex(appConfig)},
			{Label: "LED Mode", Icon: "[R]", Value: ledPolicyIndex(ledPolicyFromConfig(nil))},
			// The experience pack in force; Left/Right apply the next one at
			// once (xpack.go).
			{Label: "Experience", Icon: "[X]"},
		},
		// -1 means "nothing written yet", so the first move always applies.
		lastVolume:     -1,
		lastBrightness: -1,
	}
	qsRefreshAvailability()
	qsLoadPersisted()
	// The saved brightness waits for the brightness controller to know its
	// backend, and is only pushed where JukaHub owns the level
	// (brightnessRestoreFor): the window does not exist yet, and on Windows
	// finding the backend takes a PowerShell run.
	if user := loadUserConfig(); user != nil && user.Variables.BrightnessSet {
		brightnessNoteSavedLevel(user.Variables.Brightness)
	}
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
		qsActionQuiet:         true,
		qsActionFullscreen:    true,
		qsActionNetplay:       true,
		qsActionMagnifier:     true,
		qsActionVPN:           vpnQSAvailable() || vpnEnabledIn(appConfig), // on can always go off
	}
	detail := map[qsAction]string{
		qsActionWifi:      "no nmcli/rfkill",
		qsActionBluetooth: "no bluetoothctl/rfkill",
		qsActionAirplane:  "no rfkill",
		qsActionLedSync:   "no addressable LED",
		qsActionVPN:       vpnQSDetail(),
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
			// Reachable, not merely live: with the radio off the picker is what
			// turns it on, so the row must not go dim just because nothing is
			// listening on the control socket yet.
			l.Available = wifiEntryReachable()
			// The row is 400 px wide; the full reason lives in the picker's
			// header and in the toast, so the row only says there is no backend.
			l.Detail = "Unavailable"
		case qsLinkControls:
			// The legend card runs local to the app, so this row is always
			// offered, even on a host with no radios at all.
			l.Available = true
		case qsLinkLEDPanel:
			l.Available = ledSyncAvailable()
			if !l.Available {
				l.Detail = "Unavailable"
			}
		}
	}
	volumeOK := volumeBackend() != ""
	qs.sliders[0].Available = volumeOK
	qs.sliders[0].Detail = "no pactl/amixer"
	qsRefreshBrightnessRow()
}

// qsRefreshBrightnessRow takes the brightness controller's answer
// (brightnessctl.go): the row is live once the backend is known — the
// software dimmer is always there as the last resort — and its floor and grid
// are the backend's. It never starts the probe (qsInit runs before the window
// exists); the controller calls it again once the probe has answered.
func qsRefreshBrightnessRow() {
	if len(qs.sliders) < 2 || qs.sliders[1].Action != "brightness" {
		return
	}
	s := &qs.sliders[1]
	s.Available = brightnessKnownBackend() != ""
	s.Detail = "checking…"
	s.Min = float64(brightnessFloor()) / 100
	if s.Value < s.Min {
		s.Value = s.Min
	}
}

// qsNoteBrightnessLevel shows a level the brightness controller applied on
// the slider, so stepping it continues from there. Main thread.
func qsNoteBrightnessLevel(pct int) {
	if len(qs.sliders) < 2 || qs.sliders[1].Action != "brightness" {
		return
	}
	qs.sliders[1].Value = float64(pct) / 100
	qs.lastBrightness = pct
}

// qsSyncBrightnessFromController keeps the slider on the controller's cached
// level, which follows the hardware (its poll) and every change made from the
// header's card. The panel used to read the backlight itself, in its own
// probe; one owner of the reading keeps the two from disagreeing.
func qsSyncBrightnessFromController() {
	if len(qs.sliders) < 2 || !qs.sliders[1].Available {
		return
	}
	if pct, known := brightnessCurrent(); known {
		qsNoteBrightnessLevel(pct)
	}
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
		qs.cycles[qsCycleScreensaverRow].Value = appConfig.Variables.ScreensaverTimeout
	}
	qs.cycles[qsCycleRowPowerMode].Value = qsPowerModeRowIndex()
	qs.cycles[qsCycleRowSpatialMode].Value = qsSpatialModeRowIndex(appConfig)
}

// qsApplyPersistedSystemState pushes the saved volume to the device at
// launch. Nothing is written unless the user has actually set a value, so a
// fresh install keeps the device's own defaults.
func qsApplyPersistedSystemState() {
	user := loadUserConfig()
	if user == nil {
		return
	}
	// The TrimUI firmware keeps its own level across reboots (system.json,
	// which every change here also updates) and its keys may have moved it
	// since JukaHub last saved one: restoring the saved level would jump the
	// volume at every launch. Elsewhere JukaHub's saved level is the only one.
	if user.Variables.VolumeSet {
		switch volumeBackend() {
		case "", "trimui":
		default:
			if err := setSystemVolume(user.Variables.Volume); err != nil {
				LogScene("quicksettings").Warn("restore volume failed", "err", err)
			}
		}
	}
	// The saved brightness is the brightness controller's to restore
	// (brightnessNoteSavedLevel, queued by qsInit), on the main thread.
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
// the timeout and mode cycles.
func qsRowCount() int { return len(qs.toggles) + len(qs.sliders) + len(qs.links) + len(qs.cycles) }

// The cycle rows, in row order. The screensaver row owns arrays and labels
// via the shared helper sets; named indices keep later rows from silently
// changing each other's behavior.
const (
	qsCycleScreensaverRow = 0
	qsCycleRowPowerMode   = 1
	qsCycleRowSpatialMode = 2
	qsCycleRowLED         = 3
	qsCycleRowXPack       = 4
)

// qsSyncFromConfig refreshes the rows whose truth lives in the config, then
// asks the host for the radio and level state. While a named Power Mode is
// active, the two levers it manages show what actually happens (the mode's
// verdict), not the saved toggle — the pill a user sees must match the
// behaviour they get.
func qsSyncFromConfig(config *Config) {
	if config != nil {
		managed := currentPowerMode(config) != PowerModeDefault
		for i := range qs.toggles {
			switch qs.toggles[i].Action {
			case qsActionLowPower:
				qs.toggles[i].Value = config.Variables.LowPower
				if managed {
					qs.toggles[i].Value = effectiveLowPower(config)
				}
			case qsActionReducedMotion:
				qs.toggles[i].Value = config.Variables.ReducedMotion
				if managed {
					qs.toggles[i].Value = effectiveReducedMotion(config)
				}
			case qsActionQuiet:
				qs.toggles[i].Value = quietModeFromConfig(config)
			case qsActionFullscreen:
				qs.toggles[i].Value = config.Variables.Fullscreen
			case qsActionLedSync:
				qs.toggles[i].Value = config.Variables.LEDSync
			case qsActionNetplay:
				qs.toggles[i].Value = netplayEnabled(config)
			case qsActionMagnifier:
				qs.toggles[i].Value = a11y.magnifier
			case qsActionVPN:
				qs.toggles[i].Value = vpnSettingOn(config)
			}
		}
		if config.Variables.ScreensaverTimeout > 0 {
			qs.cycles[qsCycleScreensaverRow].Value = config.Variables.ScreensaverTimeout
		}
		qs.cycles[qsCycleRowPowerMode].Value = qsPowerModeRowIndex()
		qs.cycles[qsCycleRowSpatialMode].Value = qsSpatialModeRowIndex(config)
		qs.cycles[qsCycleRowLED].Value = ledPolicyIndex(ledPolicyFromConfig(config))
	}
	qsRefreshAvailability()
	qsStartProbe()
	// The brightness controller reads the level again for the slider; the
	// panel takes it from the controller's cache as it draws.
	brightnessRefreshNow()
	qsSyncBrightnessFromController()
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
	volume                          int
	volumeOK                        bool
	// gen is qsEditGen when the probe started.
	gen int
	// volGen and volApplied are the speaker's change counters when the probe
	// started (volumeEditCounters): a step made from the header's card while
	// it ran makes its volume reading stale too.
	volGen, volApplied int
}

var (
	qsProbeCh      = make(chan qsProbe, 1)
	qsProbePending bool
	// qsEditGen counts the changes made from the panel. A probe that started
	// before a change read the host before it, so its answer is stale: applied,
	// it snapped the volume slider back to the old level (and the next step
	// jumped from there) or flipped a radio switch the user had just changed.
	qsEditGen int
	// qsProbeRerun records a probe asked for while one was in flight; it runs
	// as soon as that one is drained instead of being dropped.
	qsProbeRerun bool
)

// qsNoteEdit marks a change made from the panel (see qsEditGen).
func qsNoteEdit() { qsEditGen++ }

// qsCollectLiveState asks the host what the radios and the volume are doing.
// It runs on a worker goroutine, so it takes everything it needs as arguments
// and never reads (or writes) the live qs state. The brightness is the
// brightness controller's to read (qsSyncBrightnessFromController).
func qsCollectLiveState(wantWifi, wantBT, wantAirplane, wantVolume bool) qsProbe {
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
	return r
}

func qsStartProbe() {
	if qsProbePending {
		qsProbeRerun = true
		return
	}
	qsProbePending = true
	wantWifi := qs.toggles[0].Available
	wantBT := qs.toggles[1].Available
	wantAirplane := qs.toggles[2].Available
	wantVolume := qs.sliders[0].Available
	gen := qsEditGen
	volGen, volApplied := volumeEditCounters()
	go func() {
		r := qsCollectLiveState(wantWifi, wantBT, wantAirplane, wantVolume)
		r.gen = gen
		r.volGen, r.volApplied = volGen, volApplied
		qsProbeCh <- r
	}()
}

// qsDrainProbe applies a finished probe. Called from the render loop, which runs
// on the event thread, so the state it writes is never read concurrently. A
// result older than the latest change from the panel is dropped and a fresh
// probe takes its place.
func qsDrainProbe() {
	select {
	case r := <-qsProbeCh:
		qsProbePending = false
		stale := r.gen != qsEditGen
		if !stale {
			qsApplyProbe(r)
		}
		if stale || qsProbeRerun {
			qsProbeRerun = false
			qsStartProbe()
		}
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
	// The header's speaker takes the same reading (volumectl.go), and a
	// reading that raced one of its steps moves neither it nor the slider.
	if r.volumeOK && qs.sliders[0].Available && volumeNoteReadingAt(r.volume, r.volGen, r.volApplied) {
		qs.sliders[0].Value = float64(r.volume) / 100
		qs.lastVolume = r.volume
	}
	qsSyncBrightnessFromController()
}

func qsToggleOpen() {
	qs.open = !qs.open
	if qs.open {
		trophyEvent(tkQuickSettings)
		qs.cursor = 0
		qs.enter.begin(uiClockMS(), uiPanelEnterMS)
		// The top-bar cards would otherwise stay open under the panel: a
		// control profile can open it while one is up, and then the pad went
		// to the card and the keys to the panel.
		dismissWeatherPopover()
		dismissVolumePopover()
		dismissBrightnessPopover()
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
	idx -= len(qs.links)
	qsStepCycle(idx, 1)
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
		// The Wi-Fi page replaces the panel (wifiScrOpen closes it).
		wifiScrOpen(appConfig)
	case qsLinkControls:
		// The legend is a modal of its own: the panel steps aside so two
		// cards are never stacked.
		qs.open = false
		openControlsLegend()
	case qsLinkLEDPanel:
		qs.open = false
		openLEDPanel()
	}
}

// qsNavigateSlider handles the left/right input. On the sliders dr is the
// percentage step (the controller sends ±5, the keyboard ±5); on the cycles
// it moves one step in that direction.
func qsNavigateSlider(dr int) {
	if dr == 0 {
		return
	}
	idx := qs.cursor - len(qs.toggles)
	if idx >= 0 && idx < len(qs.sliders) {
		qsStepSlider(idx, dr)
		return
	}
	idx -= len(qs.sliders)
	if idx < 0 {
		return
	}
	// Left/Right walk a cycle row one step at a time; dr carries the ±5 the
	// sliders use, so only its sign matters here.
	step := 1
	if dr < 0 {
		step = -1
	}
	idx -= len(qs.links)
	qsStepCycle(idx, step)
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
	// A named Power Mode answers for the levers it manages: pressing the
	// switch would either be undone on the next sync or fight the mode, so
	// the row explains where the control lives instead of lying quietly.
	if mode := currentPowerMode(appConfig); mode != PowerModeDefault {
		switch t.Action {
		case qsActionLowPower, qsActionReducedMotion:
			ShowToast("Managed by Power Mode ("+powerModeLabel(mode)+
				") — set it to Default to control this toggle.", ToastKindInfo)
			return
		}
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
		qsNoteEdit()
		qsStartProbe()
		return nil
	case qsActionBluetooth:
		if err := setBluetoothEnabled(want); err != nil {
			return err
		}
		qsNoteEdit()
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
		qsNoteEdit()
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
	case qsActionMagnifier:
		// The lens reads its own switch, so the panel writes the same store
		// the Accessibility page does; qsPersist saves it with the batch.
		if a11ySetMagnifier(want) {
			a11yStore(appConfig)
		}
		return nil
	case qsActionVPN:
		// The same switch as Settings → Network → VPN: it saves, wakes the
		// tunnel's worker and says what happens next (vpn.go).
		if want && !vpnQSAvailable() {
			return fmt.Errorf("add a WireGuard configuration in Settings > Network first")
		}
		vpnSettingSet(appConfig, want)
		return nil
	case qsActionLowPower:
		if appConfig != nil {
			appConfig.Variables.LowPower = want
			dropCustomMirror(appConfig, "lowPower")
			syncVariableOverrides(appConfig)
		}
		return nil
	case qsActionReducedMotion:
		if appConfig != nil {
			appConfig.Variables.ReducedMotion = want
			dropCustomMirror(appConfig, "reducedMotion")
			// Switched by hand: the boot follows Reduce motion from now on
			// instead of the handheld's out-of-box default (bootOSMotion).
			if appConfig.Variables.Custom == nil {
				appConfig.Variables.Custom = map[string]interface{}{}
			}
			appConfig.Variables.Custom[bootOSMotionChosenKey] = "true"
			syncVariableOverrides(appConfig)
		}
		return nil
	case qsActionQuiet:
		quietModeSet(appConfig, want)
		return nil
	case qsActionNetplay:
		if appConfig != nil {
			setNetplayEnabled(appConfig, want)
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
			dropCustomMirror(appConfig, "fullscreen")
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
	before := int(math.Round(s.Value * 100))
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
		// The mixer write runs on the volume worker (volumectl.go), which
		// reports a failure itself: on the TrimUI a write is several amixer
		// calls and a card write, too slow for the render thread. The header's
		// speaker follows at once. The level applied can differ from the one
		// asked for — on the TrimUI it goes onto the firmware's 5% grid — and
		// the slider shows (and saves) the one applied.
		pct = volumeApplyLevel(pct)
		s.Value = float64(pct) / 100
		qs.lastVolume = pct
		qsNoteEdit()
	case "brightness":
		// The write runs on the brightness controller's worker
		// (brightnessctl.go), which reports a failure itself: on Windows it is
		// a PowerShell run or a DDC/CI exchange. The level applied is snapped
		// to the backend's grid (the TrimUI firmware's 10% steps) and kept
		// above its floor — and a step finer than the grid (the panel's 5%)
		// still moves one grid step, or rounding put it back where it was. The
		// slider shows (and saves) the one applied, and the header's sun
		// follows at once.
		pct = brightnessStepTarget(brightnessKnownBackend(), before, deltaPct)
		s.Value = float64(pct) / 100
		if pct == qs.lastBrightness {
			return
		}
		pct = brightnessApplyLevel(pct)
		s.Value = float64(pct) / 100
		qs.lastBrightness = pct
		qsNoteEdit()
	}
	PlayNavSound()
	if appConfig != nil {
		qsPersist(appConfig)
	}
}

// qsPowerModeRowIndex is where the config's Power Mode sits on the cycle.
func qsPowerModeRowIndex() int { return powerModeIndex(currentPowerMode(appConfig)) }

// qsSpatialModeRowIndex is where spatial audio's configured mode sits on its
// cycle. Auto's current output is shown separately in the row's value.
func qsSpatialModeRowIndex(config *Config) int {
	mode := spatialNormalizeMode(spatialSettingsFor(config).Mode)
	for i, name := range spatialModeNames {
		if name == mode {
			return i
		}
	}
	return 0
}

// qsStepCycle moves one cycle row. Screensaver, power and spatial modes each
// use their shared cycle helper; the remaining rows keep their own handlers.
func qsStepCycle(row, dir int) {
	if row < 0 || row >= len(qs.cycles) || dir == 0 {
		return
	}
	switch row {
	case qsCycleRowXPack:
		// Each step applies the next experience pack at once; it saves itself,
		// and the gallery's Undo puts the previous setup back (xpack.go).
		if appConfig != nil {
			xpackQSStep(appConfig, dir)
		}
		PlayNavSound()
		return
	case qsCycleRowLED:
		if appConfig == nil {
			return
		}
		next := nextLEDPolicy(ledPolicyFromConfig(appConfig), dir)
		if appConfig.Variables.Custom == nil {
			appConfig.Variables.Custom = map[string]interface{}{}
		}
		appConfig.Variables.Custom[ledKeyPolicy] = next
		qs.cycles[qsCycleRowLED].Value = ledPolicyIndex(next)
		ShowToast("LED Mode: "+ledPolicyLabel(next), ToastKindInfo)
		qsPersist(appConfig)
		PlayNavSound()
		return
	case qsCycleRowPowerMode:
		mode := nextPowerMode(currentPowerMode(appConfig), dir)
		label := cyclePowerModeTo(appConfig, mode)
		qs.cycles[qsCycleRowPowerMode].Value = qsPowerModeRowIndex()
		ShowToast("Power Mode: "+label, ToastKindInfo)
		if appConfig != nil {
			qsPersist(appConfig)
		}
		PlayNavSound()
		return
	case qsCycleRowSpatialMode:
		if appConfig == nil {
			return
		}
		ShowToast(spatialCycleMode(appConfig, dir), ToastKindInfo)
		qs.cycles[qsCycleRowSpatialMode].Value = qsSpatialModeRowIndex(appConfig)
		PlayNavSound()
		return
	}
	// The screensaver timeout: next (or previous) step.
	var next int
	current := qs.cycles[qsCycleScreensaverRow].Value
	if dir >= 0 {
		next = nextScreenTimeout(current)
	} else {
		next = current
		for i := len(screenTimeoutSteps) - 1; i >= 0; i-- {
			if screenTimeoutSteps[i] < current {
				next = screenTimeoutSteps[i]
				break
			}
		}
		if next == current {
			next = screenTimeoutSteps[len(screenTimeoutSteps)-1]
		}
	}
	qs.cycles[qsCycleScreensaverRow].Value = next
	if appConfig != nil {
		// The shared setter keeps the Custom copy and the Settings row's label
		// in step. Setting only the typed field left the old value in Custom,
		// and the next override pass (any Low Power toggle) read it back.
		applyScreensaverTimeout(appConfig, next)
		qsPersist(appConfig)
	} else {
		screensaverIdleTimeout = time.Duration(next) * time.Second
	}
	PlayNavSound()
}

// qsPersist snapshots the panel's values into jukauser.json through the same
// debounced writer the settings page uses.
//
// The snapshot starts from the live config (userConfigFromVariables), not from
// the cached user config. The cache's other settings are the ones read at boot
// — this saved them, so changing the volume put back a weather unit or colour
// changed in Settings earlier in the session — and assigning the live Custom
// map into the cache made the two one map, shared with the save goroutine.
// The levels are the panel's own, so they are written into the cache too,
// where every other save picks them up.
func qsPersist(config *Config) {
	if config == nil {
		return
	}
	cache := loadUserConfig()
	if cache != nil {
		if qs.sliders[0].Available {
			cache.Variables.Volume = int(math.Round(qs.sliders[0].Value * 100))
			cache.Variables.VolumeSet = true
		}
		if qs.sliders[1].Available {
			cache.Variables.Brightness = int(math.Round(qs.sliders[1].Value * 100))
			cache.Variables.BrightnessSet = true
		}
	}
	user := userConfigFromVariables(config)
	if user == nil {
		return
	}
	user.Variables.ScreensaverTimeout = qs.cycles[qsCycleScreensaverRow].Value
	on := config.Variables.NetPlayEnabled
	user.Variables.NetPlayEnabled = &on
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
	// Pick up a finished radio/level probe before drawing this frame, and the
	// brightness controller's level (its poll, the firmware's keys).
	qsDrainProbe()
	qsSyncBrightnessFromController()

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

	// The shared overlay dim (the backdrop's navy), faded in with the panel.
	drawOverlayScrimFade(renderer, uiScrimProgress(progress, uiPanelEnterMS))

	panelW := qsPanelWide
	if maxW := screenWidth - 24; panelW > maxW {
		panelW = maxW
	}

	// The panel grows with the row list and shrinks the rows before it would
	// run off a 480p screen. The floor of 30 keeps all seventeen rows on a
	// 720-line panel.
	rows := qsRowCount()
	itemH := int32(44)
	panelH := qsPanelHeight(rows, itemH)
	maxH := screenHeight - 24
	for panelH > maxH && itemH > 30 {
		itemH--
		panelH = qsPanelHeight(rows, itemH)
	}
	// The tenth toggle no longer fits a 720-line screen at the old floor:
	// cap the card and scroll the rows instead of running the title and the
	// footer off the glass.
	if panelH > maxH {
		panelH = maxH
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	listH := panelH - qsHeaderH - qsFooterH
	if listH < 0 {
		listH = 0
	}
	contentH := int32(rows) * (itemH + qsItemGap)
	maxScroll := contentH - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	// Keep the focused row in view: the cursor is the only thing that moves
	// the list.
	cursorTop := int32(qs.cursor) * (itemH + qsItemGap)
	if cursorTop < qs.scroll {
		qs.scroll = cursorTop
	}
	if cursorTop+itemH > qs.scroll+listH {
		qs.scroll = cursorTop + itemH - listH
	}
	if qs.scroll > maxScroll {
		qs.scroll = maxScroll
	}
	if qs.scroll < 0 {
		qs.scroll = 0
	}

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}, func() {
		// The shared overlay sheet: Home's glass, made opaque.
		drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})

		if medFont != nil {
			title := "Quick Settings"
			tw, _, _ := medFont.SizeUTF8(title)
			renderText(renderer, config, medFont, title, ColorTextPrimary(),
				panelX+(panelW-int32(tw))/2, panelY+16)
		}

		itemX := panelX + qsPadX
		itemW := panelW - 2*qsPadX
		y := panelY + qsHeaderH - qs.scroll

		// Rows that scroll past the list area stop at its edges, so none of
		// them draws over the title or the footer.
		prevClip := renderer.GetClipRect()
		if listH > 0 {
			listClip := sdl.Rect{X: panelX, Y: panelY + qsHeaderH, W: panelW, H: listH}
			_ = renderer.SetClipRect(&listClip)
		}

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
		for i := range qs.cycles {
			drawQSCycleRow(renderer, config, font, i, itemX, y, itemW, itemH,
				qs.cursor == len(qs.toggles)+len(qs.sliders)+len(qs.links)+i)
			y += itemH + qsItemGap
		}
		_ = renderer.SetClipRect(&prevClip)

		// System info at the bottom: battery on the left, connectivity right.
		// Both lines are positioned from the font height rather than fixed
		// offsets: with a large UI scale the footer used to sit on the panel's
		// bottom edge and the connectivity line touched it.
		smallH := int32(font.Height())
		footerY := panelY + panelH - smallH - 8
		infoY := footerY - smallH - 8
		if bat := getBatteryPercent(); bat >= 0 {
			// The level, the cable and the time left (battery_eta.go), in the
			// longest form that leaves the connectivity label its room.
			netW, _, _ := font.SizeUTF8("! Offline")
			room := panelW - 2*qsPadX - int32(netW) - 16
			line := batteryETALevelLine(bat, getBatteryCharging(), "Charging", "On cable", batteryETA(), func(s string) bool {
				w, _, _ := font.SizeUTF8(s)
				return int32(w) <= room
			})
			renderText(renderer, config, font, line,
				WithAlpha(ColorTextSecondary(), 200), panelX+qsPadX, infoY)
		}
		netStr := "Online"
		if !IsOnline() {
			netStr = "! Offline"
		}
		nw, _, _ := font.SizeUTF8(netStr)
		renderText(renderer, config, font, netStr,
			WithAlpha(ColorTextSecondary(), 200),
			panelX+panelW-int32(nw)-qsPadX, infoY)

		hint := hintLine(FooterHint{"A", "Change"}, FooterHint{"Left/Right", "Adjust"}, FooterHint{"B", "Close"})
		hw, _, _ := font.SizeUTF8(hint)
		renderText(renderer, config, font, hint,
			WithAlpha(ColorTextTertiary(), 190),
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
	// The focused row is a selected list row: Home's glass, an accent wash and
	// the accent ring (UIListRow), so the cursor reads the same here as in
	// every list.
	UIListRow(renderer, sdl.Rect{X: x, Y: y, W: w, H: h}, 12, true, false)
}

func drawQSToggleRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, t *qsToggle, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	labelCol := WithAlpha(ColorTextSecondary(), 220)
	iconCol := WithAlpha(ColorTextPrimary(), 220)
	detail := ""
	if !t.Available {
		// A row with no backend is dimmed; the switch is not drawn because
		// there is nothing for it to represent. The specific reason
		// ("no nmcli/rfkill") is too long for a 400 px row and is shown in the
		// toast when A is pressed, so the row says only what the state is.
		labelCol = WithAlpha(ColorTextTertiary(), 180)
		iconCol = labelCol
		detail = "Unavailable"
	}

	renderText(renderer, config, font, t.Icon, iconCol, x+8, y+10)
	renderText(renderer, config, font, ellipsize(font, t.Label, qsLabelWidth(font, w, detail)), labelCol, x+52, y+10)
	if detail != "" {
		dw, _, _ := font.SizeUTF8(detail)
		renderText(renderer, config, font, detail,
			WithAlpha(ColorTextTertiary(), 160), x+w-int32(dw)-12, y+10)
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

	labelCol := WithAlpha(ColorTextSecondary(), 220)
	iconCol := WithAlpha(ColorTextPrimary(), 220)
	if !s.Available {
		labelCol = WithAlpha(ColorTextTertiary(), 180)
		iconCol = labelCol
	}

	detail := ""
	if !s.Available {
		// See drawQSToggleRow: the full reason belongs in the toast, not in a
		// row that also has to fit its label.
		detail = "Unavailable"
	}
	renderText(renderer, config, font, s.Icon, iconCol, x+8, y+6)
	renderText(renderer, config, font, ellipsize(font, s.Label, qsLabelWidth(font, w, detail)), labelCol, x+52, y+6)

	if detail != "" {
		dw, _, _ := font.SizeUTF8(detail)
		renderText(renderer, config, font, detail,
			WithAlpha(ColorTextTertiary(), 160), x+w-int32(dw)-12, y+6)
		return
	}

	pct := int(math.Round(s.Value * 100))
	valStr := fmt.Sprintf("%d%%", pct)
	vw, _, _ := font.SizeUTF8(valStr)
	renderText(renderer, config, font, valStr,
		WithAlpha(ColorTextSecondary(), 200), x+w-int32(vw)-12, y+6)

	trackW := w - 130
	trackX := x + 100
	trackY := y + h - 16
	trackH := int32(6)
	fillRoundedRect(renderer, trackX, trackY, trackW, trackH, 3,
		glassChipFill())

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
		WithAlpha(ColorTextPrimary(), 240))
}

func drawQSLinkRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, l *qsLink, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	labelCol := WithAlpha(ColorTextSecondary(), 220)
	iconCol := WithAlpha(ColorTextPrimary(), 220)
	if !l.Available {
		labelCol = WithAlpha(ColorTextTertiary(), 180)
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
		WithAlpha(ColorTextTertiary(), 190), x+w-int32(rw)-12, y+10)
}

// drawQSCycleRow renders a cycler row (timeout steps, Power Mode). Its label
// gets the same measured treatment as the toggles: a translated label in a
// localized UI would otherwise run under the right-aligned value.
func drawQSCycleRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, row int, x, y, w, h int32, focused bool) {
	qsRowBackground(renderer, x, y, w, h, focused)

	c := &qs.cycles[row]
	renderText(renderer, config, font, c.Icon,
		WithAlpha(ColorTextPrimary(), 220), x+8, y+10)

	value := screenTimeoutLabel(c.Value)
	valCol := WithAlpha(ColorTextSecondary(), 220)
	if row == qsCycleRowLED {
		value = ledPolicyLabel(ledPolicyOrder[c.Value].key)
	}
	if row == qsCycleRowXPack {
		value = xpackQSLabel()
	}
	if row == qsCycleRowPowerMode {
		value = powerModeLabel(powerModeOrder[c.Value])
		if c.Value != powerModeIndex(PowerModeDefault) {
			// A named mode is live: tint the value with the accent so the
			// override reads as "on", the way a coloured switch does.
			valCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 240}
		}
	}
	if row == qsCycleRowSpatialMode {
		settings := spatialSettingsFor(config)
		value = spatialModeValue(config)
		if settings.Mode != "off" {
			valCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 240}
		}
	}
	labeled := ellipsize(font, c.Label, qsLabelWidth(font, w, value))
	renderText(renderer, config, font, labeled,
		WithAlpha(ColorTextSecondary(), 220), x+52, y+10)

	vw, _, _ := font.SizeUTF8(value)
	renderText(renderer, config, font, value, valCol, x+w-int32(vw)-12, y+10)
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
	case sdl.K_PAGEUP, sdl.K_PAGEDOWN, sdl.K_HOME, sdl.K_END:
		// The keyboard twins of the pad's shoulders (inputhints.go).
		qsShoulder(keyShoulder(e.Keysym.Sym))
	case sdl.K_ESCAPE, sdl.K_b:
		qs.open = false
		PlayBackSound()
	case sdl.K_q:
		// Ctrl+Q opened the panel; the same chord puts it away (not its
		// key repeats, which would shut the panel the press just opened).
		if e.Keysym.Mod&sdl.KMOD_CTRL != 0 && e.Repeat == 0 {
			qs.open = false
			PlayBackSound()
		}
	}
}
