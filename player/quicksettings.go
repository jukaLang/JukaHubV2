package main

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
//
// This file owns the switches and what they do. How they are laid out — the
// Tiles, Compact and List layouts, the order, sizes and hidden buttons, and
// the walk of the cursor — is qs_layout.go; L2's editor is qs_edit.go and
// the drawing qs_draw.go.

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
	// Background music (bgmusic.go): the same switch as Settings → Sound &
	// haptics. A tester heard library songs start under the menus after an
	// Extract audio (which installed ffmpeg, so "My music" could finally
	// play) and could not find what was playing or how to stop it.
	qsActionBgMusic
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
	// The Bluetooth page (bluetooth_devices_ui.go): pads, headphones and
	// keyboards are paired without leaving JukaHub.
	qsLinkBluetooth
	// Close all apps & games (closeall.go): a two-step press, not a panel.
	qsLinkCloseAll
	// Power: the Exit card (Sleep, Restart, Power off), from any screen — the
	// switch-off a tester missed.
	qsLinkPower
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
			{Label: "Background music", Icon: "[~]", Action: qsActionBgMusic,
				OnColor: sdl.Color{R: 230, G: 130, B: 200, A: 255}, OffColor: qsToggleOff},
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
			// The Stopwatch app, on every host; the button shows the running
			// clock (stopwatch.go).
			{Label: "Stopwatch", Icon: "[W]", Action: qsLinkStopwatch, Available: true},
			// The handheld as a PC's game controller, on every host
			// (ctlmode_ui.go).
			{Label: "Controller Mode", Icon: "[G]", Action: qsLinkControllerMode, Available: true},
			// Frees memory when Juka's offline AI needs it (closeall.go).
			{Label: "Close all apps & games", Icon: "[X]", Action: qsLinkCloseAll},
			{Label: "Power", Icon: "[O]", Action: qsLinkPower},
		},
		// The app's idle timer brings up the lock screen (the clock dock or
		// the starfield), not a display blank; Settings → Display calls it
		// "Lock screen after" too.
		cycles: []qsCycle{
			{Label: "Lock screen", Icon: "[T]", Value: 25},
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
	qs.links = qsWithBluetoothLink(qs.links)
	// Record screen, where the panel can be recorded (screenrec.go).
	qs.toggles = screenRecQSToggles(qs.toggles)
	// Power off after sleeping, one press away (quickresume.go): A turns it
	// off or back on at the last delay, Left/Right step it. Only where the
	// RTC can wake the handheld; last, so the rows above keep their indices.
	if sleepPowerOffQuickAvailable() {
		qs.cycles = append(qs.cycles, qsCycle{Label: "Auto power off", Icon: "[Z]"})
	}
	// JukaHub OS brings its radios up after the first frame, so a Wi-Fi probe
	// here could only fork wpa_cli to find nothing (and remember that); the
	// panel probes again every time it opens.
	qsBootSkipWifiProbe = jukaosNativeActive()
	qsRefreshAvailability()
	qsBootSkipWifiProbe = false
	qsLoadPersisted()
	// The saved brightness waits for the brightness controller to know its
	// backend, and is only pushed where JukaHub owns the level
	// (brightnessRestoreFor): the window does not exist yet, and on Windows
	// finding the backend takes a PowerShell run.
	if user := loadUserConfig(); user != nil && user.Variables.BrightnessSet {
		brightnessNoteSavedLevel(user.Variables.Brightness)
	}
}

// qsWithBluetoothLink puts "Bluetooth Devices" after "Wi-Fi Networks" where
// this system can pair devices (btDevicesAvailable: Linux with bluetoothctl,
// looked up once and cached). A tester asked for a way to connect a device
// without leaving JukaHub — the page was there, behind Settings → Network and
// the Start menu, but not next to the switch people reach for. The row is
// decided once, when the panel is built: adding it later would move the rows
// under an open panel's cursor.
func qsWithBluetoothLink(links []qsLink) []qsLink {
	if !btDevicesAvailable() {
		return links
	}
	at := 0
	for i, l := range links {
		switch l.Action {
		case qsLinkBluetooth:
			return links
		case qsLinkWiFi:
			at = i + 1
		}
	}
	out := make([]qsLink, 0, len(links)+1)
	out = append(out, links[:at]...)
	out = append(out, qsLink{Label: "Bluetooth Devices", Icon: "[B]", Action: qsLinkBluetooth})
	return append(out, links[at:]...)
}

// qsBootSkipWifiProbe is set by qsInit around its first availability pass on
// JukaHub OS (main thread only).
var qsBootSkipWifiProbe bool

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
		qsActionBgMusic:       true,
		qsActionScreenRec:     true, // only added where it works (screenRecQSToggles)
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
			l.Available = qsBootSkipWifiProbe || wifiEntryReachable()
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
		case qsLinkBluetooth:
			// Offered only where bluetoothctl was found; one that went away
			// since leaves the row dim instead of opening an empty page.
			l.Available = btDevicesAvailable()
			l.Detail = "Unavailable"
		case qsLinkCloseAll, qsLinkPower:
			l.Available = true
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
	// On a TrimUI a Bluetooth radio left off in JukaHub goes off again after
	// the firmware's boot switched it on (bt_bootswitch.go), and the
	// firmware's own saved level goes back on the codec when the boot left it
	// elsewhere (volume_trimui.go; it may wait for the codec, so on its own
	// goroutine).
	btSwitchRestoreAtStart()
	go trimuiRestoreVolumeAtLaunch()
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

// qsRowCount is the number of buttons the panel shows: the arrangement's
// buttons this device has, less the hidden ones outside Edit (qs_layout.go).
func qsRowCount() int { return len(qsLay.slots) }

// The cycle rows, in row order. The screensaver row owns arrays and labels
// via the shared helper sets; named indices keep later rows from silently
// changing each other's behavior.
const (
	qsCycleScreensaverRow = 0
	qsCycleRowPowerMode   = 1
	qsCycleRowSpatialMode = 2
	qsCycleRowLED         = 3
	qsCycleRowXPack       = 4
	// qsCycleRowAutoOff exists only where sleepPowerOffQuickAvailable.
	qsCycleRowAutoOff = 5
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
			case qsActionBgMusic:
				qs.toggles[i].Value = bgmEnabledIn(config)
			case qsActionScreenRec:
				qs.toggles[i].Value = screenRecActive()
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
		// The kernel's answer first (btProbeRadio): no adapter, or no
		// bluetoothd on the handhelds, is off without a bluetoothctl that
		// would only wait out its timeout.
		r.bluetooth, r.bluetoothOK = btProbeRadio()
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
	// A switch whose command is still running keeps what the press showed:
	// this reading may predate the change (qsRadioBusy). Airplane Mode moves
	// all three radios.
	airBusy := qsRadioBusy[qsRadioAirplane] > 0
	wifiBusy := airBusy || qsRadioBusy[qsRadioWifi] > 0
	if r.wifiOK && !wifiBusy {
		qs.toggles[0].Value = r.wifi
	}
	if r.bluetoothOK && !airBusy {
		qs.toggles[1].Value = r.bluetooth
	}
	if r.airplaneOK && !airBusy {
		qs.toggles[2].Value = r.airplane
		if r.airplane {
			// Whatever the individual daemons think, nothing is transmitting while
			// every radio is blocked.
			if !wifiBusy {
				qs.toggles[0].Value = false
			}
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
	if !qs.open && startupLocked() { // not over the startup lock (bootlock.go)
		return
	}
	qs.open = !qs.open
	// An edit left open when a screen closed the panel by clearing qs.open
	// is saved and ended here, on the way in as on the way out (qs_edit.go).
	qsEditClose(appConfig)
	qsLay.adjusting = false
	if qs.open {
		trophyEvent(tkQuickSettings)
		qs.cursor = 0
		qs.scroll = 0
		qsLay.wantCol = 0
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
		// The first button of this layout's arrangement takes the focus.
		qsLay.slots = qsLay.slots[:0]
		qsLayoutSync(appConfig)
		qs.cursor = 0
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

// qsSelect is A on the focused button (qs_layout.go keeps the list of
// buttons the cursor walks).
func qsSelect() {
	s, ok := qsFocused()
	if !ok {
		return
	}
	switch s.Kind {
	case qsKindToggle:
		qsToggleRow(s.Idx)
	case qsKindSlider:
		if qsLay.cols > 0 {
			// On a grid A starts adjusting the slider (Left/Right move it,
			// A or B end), so the D-pad can keep walking the grid.
			if s.Idx >= 0 && s.Idx < len(qs.sliders) && !qs.sliders[s.Idx].Available {
				qsStepSlider(s.Idx, 0) // says why
				return
			}
			qsLay.adjusting = !qsLay.adjusting
			return
		}
		qsStepSlider(s.Idx, 10)
	case qsKindLink:
		if s.Idx >= 0 && s.Idx < len(qs.links) {
			qsOpenLink(qs.links[s.Idx])
		}
	case qsKindCycle:
		if s.Idx == qsCycleRowAutoOff && s.Idx < len(qs.cycles) {
			// A is the on/off switch here; Left/Right pick the delay.
			if appConfig != nil {
				d := sleepPowerOffToggle(appConfig)
				ShowToast("Power off after sleeping: "+sleepPowerOffLabel(d), ToastKindInfo)
			}
			PlayNavSound()
			return
		}
		qsStepCycle(s.Idx, 1)
	}
}

// qsOpenLink opens the panel behind a link row. It is a no-op with a reason on
// devices where that panel's backend is missing.
func qsOpenLink(l qsLink) {
	if !l.Available {
		reason := l.Detail
		switch l.Action {
		case qsLinkWiFi:
			reason = wifiUnavailableReason()
		case qsLinkBluetooth:
			reason = "no bluetoothctl"
		}
		ShowToast(l.Label+" is unavailable on this device ("+reason+")", ToastKindInfo)
		return
	}
	switch l.Action {
	case qsLinkWiFi:
		// The Wi-Fi page replaces the panel (wifiScrOpen closes it).
		wifiScrOpen(appConfig)
	case qsLinkBluetooth:
		// So does the Bluetooth page (btScreenOpen closes it).
		btScreenOpen(appConfig)
	case qsLinkControls:
		// The legend is a modal of its own: the panel steps aside so two
		// cards are never stacked.
		qs.open = false
		openControlsLegend()
	case qsLinkLEDPanel:
		qs.open = false
		openLEDPanel()
	case qsLinkCloseAll:
		// The panel stays: the toast names what would close, the second
		// press closes it (closeall.go).
		closeAllArmPress("qs", hintKeyFor("A", ""), false, nil)
	case qsLinkPower:
		// The power key's Exit card: the main loop opens it, going Home
		// first from another screen (powerkey.go powerKeyTick).
		qs.open = false
		powerKeyCardRequested.Store(true)
	case qsLinkStopwatch:
		// The app replaces the panel, as the LED panel does.
		qs.open = false
		osAppsOpen(appConfig, stopwatchSceneName)
	case qsLinkControllerMode:
		// So does Controller Mode's page (ctlmode_ui.go); a game on screen
		// keeps the panel up with the reason.
		if ctlModeGameRunning() {
			ShowToast(ctlUIGameRunningMsg, ToastKindWarning)
			return
		}
		qs.open = false
		ctlModeOpen(appConfig)
	}
}

// qsNavigateSlider handles the left/right input. On the sliders dr is the
// percentage step (the controller sends ±5, the keyboard ±5); on the cycles
// it moves one step in that direction.
func qsNavigateSlider(dr int) {
	s, ok := qsFocused()
	if dr == 0 || !ok {
		return
	}
	switch s.Kind {
	case qsKindSlider:
		qsStepSlider(s.Idx, dr)
	case qsKindCycle:
		// Left/Right walk a cycle row one step at a time; dr carries the ±5
		// the sliders use, so only its sign matters here.
		step := 1
		if dr < 0 {
			step = -1
		}
		qsStepCycle(s.Idx, step)
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

// qsRadioMu runs the Wi-Fi and Airplane switches' background commands one at
// a time. A mutex does not queue its waiters in order, so each press also
// takes a number (qsRadioSeq): a command whose press is no longer the newest
// is skipped, and a quick on-off-on always finishes as the last press.
var qsRadioMu sync.Mutex

const (
	qsRadioWifi = iota
	qsRadioAirplane
)

// qsRadioSeq is the newest press per switch (qsRadioWifi, qsRadioAirplane).
var qsRadioSeq [2]atomic.Uint64

// qsRadioBusy counts each switch's commands still running. Main thread only:
// a probe that lands meanwhile read the radio before the change and leaves
// that switch alone (qsApplyProbe); the probe each command ends with does not.
var qsRadioBusy [2]int

// qsAirplaneSnap is what the radios were before Airplane Mode blocked them,
// for unblocking to put back. Guarded by qsRadioMu: the background commands
// read and write it, so a fast on-off sees the snapshot the "on" just took.
var qsAirplaneSnap struct {
	savedWifi, wifiWasOn bool
	savedBT, btWasOn     bool
}

// qsSetToggleValue puts a switch back where a failed background change left
// the radio. Main thread only (called from a tbxPost closure).
func qsSetToggleValue(action qsAction, value bool) {
	for i := range qs.toggles {
		if qs.toggles[i].Action == action {
			qs.toggles[i].Value = value
		}
	}
}

// qsApplyToggle performs the change behind one switch.
func qsApplyToggle(t *qsToggle, want bool) error {
	switch t.Action {
	case qsActionWifi:
		// nmcli/rfkill take up to seconds: they run off the render thread, as
		// Bluetooth does below. The switch shows the new state meanwhile; a
		// failure is a toast and puts it back, and the probe shows the truth.
		label := t.Label
		seq := qsRadioSeq[qsRadioWifi].Add(1)
		qsRadioBusy[qsRadioWifi]++
		go func() {
			qsRadioMu.Lock()
			var err error
			ran := seq == qsRadioSeq[qsRadioWifi].Load()
			if ran {
				err = setWifiEnabled(want)
			}
			qsRadioMu.Unlock()
			tbxPost(func() {
				qsRadioBusy[qsRadioWifi]--
				if !ran {
					// A newer press ran instead and reports; read the radio
					// again in case its probe landed while this was counted.
					qsStartProbe()
					return
				}
				if err != nil {
					ShowToast(label+": "+firstLine(err.Error()), ToastKindWarning)
					if seq == qsRadioSeq[qsRadioWifi].Load() {
						qsSetToggleValue(qsActionWifi, !want)
					}
				} else if want && seq == qsRadioSeq[qsRadioWifi].Load() {
					// Saved networks reconnect by themselves again (wifi_keeper.go)
					// — unless an "off" pressed since is about to follow.
					wifiKeeperRadioOn()
				}
				qsNoteEdit()
				qsStartProbe()
				// Home's Wi-Fi tile and the idle screens read the radio again now,
				// not at their next eight-second refresh (home_quicktiles.go).
				saverWifiInvalidate()
			})
		}()
		qsNoteEdit()
		return nil
	case qsActionBluetooth:
		if want {
			// Switching on may have to start what the firmware left stopped
			// (bluetooth_bringup.go), which takes seconds: it runs off the
			// render thread, the switch shows on meanwhile, and the probe the
			// bring-up ends with shows what really happened.
			btBringUpAsync(func(err error) {
				// A start that failed puts the switch back off: the probe
				// cannot always tell (no adapter, no bluetoothd to ask).
				if btBringUpFailed(err) {
					for i := range qs.toggles {
						if qs.toggles[i].Action == qsActionBluetooth {
							qs.toggles[i].Value = false
						}
					}
				}
				qsNoteEdit()
				qsStartProbe()
			})
			qsNoteEdit()
			return nil
		}
		// Off runs off the render thread too (`power off` disconnects every
		// device first); a failure is a toast, and the probe it ends with
		// puts the switch back where the radio really is.
		btPowerOffAsync(func(error) {
			qsNoteEdit()
			qsStartProbe()
		})
		qsNoteEdit()
		return nil
	case qsActionAirplane:
		// rfkill, bluetoothctl and nmcli run off the render thread (several
		// seconds in all); the panel's own fields are only written back on it.
		label := t.Label
		seq := qsRadioSeq[qsRadioAirplane].Add(1)
		qsRadioBusy[qsRadioAirplane]++
		go func() {
			qsRadioMu.Lock()
			var err error
			ran := seq == qsRadioSeq[qsRadioAirplane].Load()
			if ran && want {
				// Snapshot the radios before blocking so unblocking can restore
				// them — unless every radio is blocked already (an "on" after
				// a skipped "off"), when the snapshot would read them all off.
				if on, ok := airplaneEnabled(); !ok || !on {
					qsAirplaneSnap.wifiWasOn, qsAirplaneSnap.savedWifi = wifiEnabled()
					qsAirplaneSnap.btWasOn, qsAirplaneSnap.savedBT = bluetoothEnabled()
				}
				err = setAirplaneMode(true)
			} else if ran {
				if err = setAirplaneMode(false); err == nil {
					// rfkill unblocks everything; put back what the user had off.
					if qsAirplaneSnap.savedWifi && !qsAirplaneSnap.wifiWasOn {
						_ = setWifiEnabled(false)
					}
					if qsAirplaneSnap.savedBT && !qsAirplaneSnap.btWasOn {
						_ = setBluetoothEnabled(false)
					}
					qsAirplaneSnap.savedWifi, qsAirplaneSnap.savedBT = false, false
				}
			}
			snap := qsAirplaneSnap
			qsRadioMu.Unlock()
			tbxPost(func() {
				qsRadioBusy[qsRadioAirplane]--
				if !ran {
					// A newer press ran instead and reports; read the radio
					// again in case its probe landed while this was counted.
					qsStartProbe()
					return
				}
				// The panel's copy of the snapshot, for display and diagnostics.
				qs.airplaneSavedWifi, qs.airplaneWifiWasOn = snap.savedWifi, snap.wifiWasOn
				qs.airplaneSavedBT, qs.airplaneBTWasOn = snap.savedBT, snap.btWasOn
				if err != nil {
					ShowToast(label+": "+firstLine(err.Error()), ToastKindWarning)
					if seq == qsRadioSeq[qsRadioAirplane].Load() {
						qsSetToggleValue(qsActionAirplane, !want)
					}
				}
				qsNoteEdit()
				qsStartProbe()
			})
		}()
		qsNoteEdit()
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
	case qsActionScreenRec:
		// Starts or stops the screen recorder; a game started meanwhile is
		// recorded too (screenrec.go).
		return screenRecQSApply(want)
	case qsActionBgMusic:
		// bgmSetPrefs stores, saves and applies: off fades the music out
		// within the loop's next block.
		if appConfig != nil {
			bgmSetPrefs(appConfig, want, bgmSourceIn(appConfig), bgmVolumeIn(appConfig))
		}
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
	case qsCycleRowAutoOff:
		// The next or previous delay, saved at once (quickresume.go).
		if appConfig != nil {
			d := sleepPowerOffStep(sleepPowerOffDelay(appConfig), dir)
			sleepPowerOffSet(appConfig, d)
			ShowToast("Power off after sleeping: "+sleepPowerOffLabel(d), ToastKindInfo)
		}
		PlayNavSound()
		return
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

// handleQSInput is the keyboard while the panel is open (qs_layout.go qsKey).
func handleQSInput(e *sdl.KeyboardEvent, config *Config) {
	qsKey(e, config)
}
