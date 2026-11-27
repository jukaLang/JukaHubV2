package main

// fnswitch.go — what the Smart Pro's FN switch does: mute, RGB lights,
// airplane mode.
//
// The switch was Travel lock's alone (travellock.go). Settings → Power &
// battery → "FN switch does" now picks one of:
//
//   - Nothing.
//   - Travel lock: travellock.go, unchanged — its setting, its calibration and
//     its boot hook. Picking it turns that setting on, picking anything else
//     turns it off (which takes the boot hook off the card). A config with
//     Travel lock on reads as this choice: nothing to migrate.
//   - Mute: slid, the speaker goes to 0 through the header speaker's own path
//     (volumectl.go); slid back, the level it had comes back.
//   - Lights off: the stick lights go dark, over games too, and come back to
//     the user's mode (ledsilky.go).
//   - Airplane mode: Wi-Fi and Bluetooth off; slid back, exactly the radios
//     that were on come back (one the user had off stays off).
//
// Calibration. Which reading of the pin is which side is not documented, so
// this does what Travel lock does: picking an action stores the position the
// switch is in right then as "off" (the device is in use as it is) and the
// other as "on" (fnSwitchAtKey). That also makes it fail safe: a pin that is
// not a switch never changes, so it never mutes anything.
//
// Set, not toggled. The switch's position decides: "on" mutes, darkens or
// blocks only what is not so already, "off" undoes only what the switch did
// (fnSwitchHeldKey keeps that across a restart: the level it muted from, the
// radios it blocked). A start with the switch on applies the action again (a
// reboot brings the radios back); a start with it off undoes what an earlier
// run left. An unchanged switch never mutes or unmutes by itself.
//
// The reader. A goroutine reads the pin every 250 ms with Travel lock's reader
// (travelLockReadSwitch), only while an action is picked, and believes a new
// position once two readings agree. The main loop applies it (fnSwitchTick,
// from powerKeyTick: every frame, and every parked iteration while a game owns
// the screen), so the switch works in games too; toasts only when JukaHub has
// the screen. Only on the boards Travel lock supports (travelLockHardware): the
// row is hidden elsewhere. The Brick's F1/F2 keys are frontkeys*.go's, not
// this switch.

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// fnSwitchModeKey is the setting: one of the fnSwitch* modes below.
	// Travel lock is read from travelLockKey, its own setting.
	fnSwitchModeKey = "fnSwitchMode"
	// fnSwitchAtKey is the reading that means "on", "0" or "1", stored when
	// an action is picked. Bookkeeping (cfgdocRuntimeKeys): another device's
	// switch may read the other way round.
	fnSwitchAtKey = "fnSwitchAt"
	// fnSwitchHeldKey is what the switch holds now: "mute" or
	// "mute:<level>" (the level it muted from), "lights", "airplane" or
	// "airplane:<radios>" (fnSwitchRadios.String), "" for nothing.
	// Bookkeeping too.
	fnSwitchHeldKey = "fnSwitchHeld"

	fnSwitchNothing  = "off"
	fnSwitchTravel   = "travel"
	fnSwitchMute     = "mute"
	fnSwitchLights   = "lights"
	fnSwitchAirplane = "airplane"

	// fnSwitchPoll is the reader's pace while an action is picked,
	// fnSwitchIdlePoll its check for one while none is.
	fnSwitchPoll     = 250 * time.Millisecond
	fnSwitchIdlePoll = 2 * time.Second
	// fnSwitchTickEvery throttles the main loop's part; fnSwitchConfigEvery is
	// how often it reads the setting again (a pick refreshes it at once).
	fnSwitchTickEvery   = 100 * time.Millisecond
	fnSwitchConfigEvery = 500 * time.Millisecond
)

// fnSwitch is the switch's live state.
var fnSwitch struct {
	// pos is the settled reading, -1 before there is one; the reader writes
	// it. armed tells the reader to read.
	pos   atomic.Int32
	armed atomic.Bool
	start sync.Once

	// The rest belongs to the main thread.
	tickAt   time.Time
	configAt time.Time
	mode     string // fnSwitchModeOf as last read
	onAt     int32  // the "on" reading, -1 when none is stored
	held     string // the action the switch holds now, "" for none
	// muteLevel is the level Mute took the speaker from, 0 when it found it
	// muted already (then sliding back leaves it so). Kept in held's value,
	// so a restart puts back that level, not the default one.
	muteLevel int
	loaded    bool // held was read from the config
	decided   bool // the first decision since start has run
}

// fnSwitchRadios is what the radios were before the switch blocked them:
// already, Airplane mode was on; else whether Wi-Fi and Bluetooth were on (a
// reading that failed counts as on, so slid back never leaves one off that
// might have been on).
type fnSwitchRadios struct {
	already  bool
	wifi, bt bool
}

// String is the form fnSwitchHeldKey keeps: "was", or "wifi", "bt", "wifi,bt"
// or "" for none.
func (r fnSwitchRadios) String() string {
	if r.already {
		return "was"
	}
	var parts []string
	if r.wifi {
		parts = append(parts, "wifi")
	}
	if r.bt {
		parts = append(parts, "bt")
	}
	return strings.Join(parts, ",")
}

// fnSwitchRadio is Airplane mode's snapshot, shared by the radio workers and
// the main thread. save is set when a worker changed it, for the main thread
// to store.
var fnSwitchRadio struct {
	mu   sync.Mutex
	snap fnSwitchRadios
	have bool
	save bool
}

// fnSwitchRadioSeq is the newest Airplane mode request: a worker whose request
// is no longer the newest leaves the radios to the newer one.
var fnSwitchRadioSeq atomic.Uint64

func init() { fnSwitch.pos.Store(-1) }

// ── The setting ────────────────────────────────────────────────────────────

// fnSwitchIsAction reports whether mode is one of this file's actions (not
// Nothing, not Travel lock).
func fnSwitchIsAction(mode string) bool {
	return mode == fnSwitchMute || mode == fnSwitchLights || mode == fnSwitchAirplane
}

// fnSwitchOnValue is the stored "on" reading; ok false when there is none.
func fnSwitchOnValue(c *Config) (int, bool) {
	if c == nil {
		return 0, false
	}
	s, ok := customString(c.Variables.Custom, fnSwitchAtKey)
	if !ok {
		return 0, false
	}
	switch strings.TrimSpace(s) {
	case "0":
		return 0, true
	case "1":
		return 1, true
	}
	return 0, false
}

// fnSwitchModeOf is what the switch does under c. Travel lock on (with its
// reading stored, i.e. turned on on this device) is "travel" whatever else is
// stored; an action needs its own reading, or it is Nothing here.
func fnSwitchModeOf(c *Config) string {
	if c == nil {
		return fnSwitchNothing
	}
	if _, ok := travelLockLockedValue(c); ok && travelLockEnabled(c) {
		return fnSwitchTravel
	}
	raw, _ := customString(c.Variables.Custom, fnSwitchModeKey)
	if m := strings.ToLower(strings.TrimSpace(raw)); fnSwitchIsAction(m) {
		if _, ok := fnSwitchOnValue(c); ok {
			return m
		}
	}
	return fnSwitchNothing
}

// fnSwitchParseHeld reads fnSwitchHeldKey: the action held and, for Airplane
// mode, the radios' snapshot. A bare "airplane" (stored before the snapshot was
// taken) counts both radios as on. Pure.
func fnSwitchParseHeld(raw string) (action string, radios fnSwitchRadios, have bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	head, list, withList := strings.Cut(raw, ":")
	switch head {
	case fnSwitchMute, fnSwitchLights:
		return head, fnSwitchRadios{}, false
	case fnSwitchAirplane:
		if !withList {
			return head, fnSwitchRadios{wifi: true, bt: true}, true
		}
		for _, p := range strings.Split(list, ",") {
			switch strings.TrimSpace(p) {
			case "was":
				radios.already = true
			case "wifi":
				radios.wifi = true
			case "bt":
				radios.bt = true
			}
		}
		return head, radios, true
	}
	return "", fnSwitchRadios{}, false
}

// fnSwitchPersistHeld stores what the switch holds. Main thread.
func fnSwitchPersistHeld(c *Config, action string) {
	if c == nil {
		return
	}
	v := action
	if action == fnSwitchMute && fnSwitch.muteLevel > 0 {
		v += ":" + strconv.Itoa(fnSwitch.muteLevel)
	}
	if action == fnSwitchAirplane {
		fnSwitchRadio.mu.Lock()
		snap, have := fnSwitchRadio.snap, fnSwitchRadio.have
		fnSwitchRadio.mu.Unlock()
		if have {
			v += ":" + snap.String()
		}
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	if old, ok := customString(c.Variables.Custom, fnSwitchHeldKey); ok && old == v {
		return
	}
	c.Variables.Custom[fnSwitchHeldKey] = v
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// fnSwitchRefresh reads the setting into fnSwitch's copy. Main thread.
func fnSwitchRefresh(c *Config, now time.Time) {
	fnSwitch.configAt = now
	fnSwitch.mode = fnSwitchModeOf(c)
	fnSwitch.onAt = -1
	if on, ok := fnSwitchOnValue(c); ok {
		fnSwitch.onAt = int32(on)
	}
}

// ── The reader ─────────────────────────────────────────────────────────────

// fnSwitchWatch reads the switch while an action is picked: a reading becomes
// the position once two in a row agree (a slide caught half way, or a contact
// bounce, never counts). Its own goroutine, for the whole run; idle it only
// checks the flag.
func fnSwitchWatch() {
	last, same := int32(-1), 0
	for {
		if !fnSwitch.armed.Load() {
			// A reading kept from before would be stale when an action is
			// picked again (a pick stores its own).
			last, same = -1, 0
			fnSwitch.pos.Store(-1)
			time.Sleep(fnSwitchIdlePoll)
			continue
		}
		if pos, ok := travelLockReadSwitch(); ok {
			if int32(pos) == last {
				same++
			} else {
				last, same = int32(pos), 1
			}
			if same >= 2 {
				fnSwitch.pos.Store(last)
			}
		} else {
			same = 0
		}
		time.Sleep(fnSwitchPoll)
	}
}

// ── The main loop's part ───────────────────────────────────────────────────

// fnSwitchTick applies the switch's position: powerKeyTick calls it every
// frame and every parked iteration (a game owns the screen). parked: no
// toasts. Main thread.
func fnSwitchTick(c *Config, parked bool) {
	now := time.Now()
	if !fnSwitch.tickAt.IsZero() && !now.Before(fnSwitch.tickAt) && now.Sub(fnSwitch.tickAt) < fnSwitchTickEvery {
		return
	}
	fnSwitch.tickAt = now
	if c == nil || !travelLockHardware() {
		return
	}
	if !fnSwitch.loaded {
		fnSwitch.loaded = true
		fnSwitchLoadHeld(c)
	}
	if fnSwitch.configAt.IsZero() || now.Before(fnSwitch.configAt) || now.Sub(fnSwitch.configAt) >= fnSwitchConfigEvery {
		fnSwitchRefresh(c, now)
	}
	fnSwitchStoreRadios(c)
	mode := fnSwitch.mode
	action := fnSwitchIsAction(mode)
	fnSwitch.armed.Store(action)
	if action {
		fnSwitch.start.Do(func() { go fnSwitchWatch() })
	}
	if fnSwitch.held == fnSwitchLights {
		ledSilkyKeepDark(c, now)
	}
	want := ""
	if action {
		pos := fnSwitch.pos.Load()
		if pos < 0 {
			return // no settled reading yet: nothing changes
		}
		if fnSwitch.onAt >= 0 && pos == fnSwitch.onAt {
			want = mode
		}
	}
	first := !fnSwitch.decided
	fnSwitch.decided = true
	if want == fnSwitch.held {
		if first && want != "" {
			// A start with the switch on: again, in case what the last run did
			// did not last (a reboot brings the radios back).
			fnSwitchEngage(c, want, false)
		}
		return
	}
	// Toasts only on JukaHub's own screen, and none for what a start finds.
	say := !first && !parked && !gameScreenSuspended() && !powerSleepActive()
	if fnSwitch.held != "" {
		fnSwitchRelease(c, say && want == "")
	}
	if want != "" {
		fnSwitchEngage(c, want, say)
	}
}

// fnSwitchLoadHeld reads what an earlier run left held. Main thread.
func fnSwitchLoadHeld(c *Config) {
	raw, _ := customString(c.Variables.Custom, fnSwitchHeldKey)
	action, radios, have := fnSwitchParseHeld(raw)
	fnSwitch.held = action
	fnSwitch.muteLevel = 0
	if action == fnSwitchMute {
		// "mute:<level>": the level to put back.
		if _, lvl, ok := strings.Cut(strings.TrimSpace(raw), ":"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(lvl)); err == nil && n > 0 && n <= 100 {
				fnSwitch.muteLevel = n
			}
		}
	}
	if action == fnSwitchAirplane {
		fnSwitchRadio.mu.Lock()
		fnSwitchRadio.snap, fnSwitchRadio.have = radios, have
		fnSwitchRadio.mu.Unlock()
	}
}

// fnSwitchStoreRadios stores a snapshot a radio worker took. Main thread.
func fnSwitchStoreRadios(c *Config) {
	fnSwitchRadio.mu.Lock()
	save := fnSwitchRadio.save
	fnSwitchRadio.save = false
	fnSwitchRadio.mu.Unlock()
	if save && fnSwitch.held == fnSwitchAirplane {
		fnSwitchPersistHeld(c, fnSwitchAirplane)
	}
}

// fnSwitchEngage applies action for the switch slid on. say: toast it. Main
// thread.
func fnSwitchEngage(c *Config, action string, say bool) {
	msg := ""
	switch action {
	case fnSwitchMute:
		if volumeControlAvailable() {
			if lvl := fnSwitchVolumeNow(); lvl > 0 {
				// A level held from before (a restart re-applying the switch)
				// stays the one to put back.
				if fnSwitch.muteLevel <= 0 {
					fnSwitch.muteLevel = lvl
				}
				volumeSetLevel(c, 0)
			}
		}
		msg = "Muted: slide FN back for sound"
	case fnSwitchLights:
		ledSilkySetDark(c, true)
		msg = "Stick lights off: slide FN back to light them"
	case fnSwitchAirplane:
		fnSwitchRadiosRequest(true)
		msg = "Airplane mode: Wi-Fi and Bluetooth off"
	default:
		return
	}
	logInfo("power", "FN switch: %s on", action)
	fnSwitch.held = action
	fnSwitchPersistHeld(c, action)
	if say {
		ShowToast(msg, ToastKindInfo)
	}
}

// fnSwitchRelease undoes what the switch holds. say: toast it. Main thread.
func fnSwitchRelease(c *Config, say bool) {
	action := fnSwitch.held
	msg := ""
	switch action {
	case fnSwitchMute:
		// Only a mute the switch made, and only while it still holds: the
		// level it took the speaker from, kept across a restart.
		if lvl := fnSwitch.muteLevel; lvl > 0 && volumeControlAvailable() && fnSwitchVolumeNow() == 0 {
			volumeSetLevel(c, lvl)
		}
		fnSwitch.muteLevel = 0
		msg = "Sound on"
	case fnSwitchLights:
		ledSilkySetDark(c, false)
		msg = "Stick lights on"
	case fnSwitchAirplane:
		fnSwitchRadiosRequest(false)
		msg = "Airplane mode off: Wi-Fi and Bluetooth as they were"
	}
	if action != "" {
		logInfo("power", "FN switch: %s off", action)
	}
	fnSwitch.held = ""
	fnSwitchPersistHeld(c, "")
	if say && msg != "" {
		ShowToast(msg, ToastKindInfo)
	}
}

// fnSwitchVolumeNow is the level as the speaker button knows it: the cache, or
// before the first reading where a step would start (volumeBase).
func fnSwitchVolumeNow() int {
	volumeLevel.mu.Lock()
	pct, known := volumeLevel.pct, volumeLevel.known
	volumeLevel.mu.Unlock()
	if !known {
		return volumeBase()
	}
	return pct
}

// ── Airplane mode ──────────────────────────────────────────────────────────

// fnSwitchRadiosRequest hands the radios to a worker: rfkill, nmcli and
// bluetoothctl take seconds. Main thread.
func fnSwitchRadiosRequest(on bool) {
	seq := fnSwitchRadioSeq.Add(1)
	go fnSwitchRadioRun(seq, on)
}

// fnSwitchRadioRun blocks or brings back the radios for request seq. One radio
// change at a time, Quick Settings' included (qsRadioMu). Slid on, the radios
// are read first unless a snapshot is held already (the switch went on again
// before they were brought back, or a restart found it on); slid off, they
// come back from the snapshot, and without one there is nothing to undo.
func fnSwitchRadioRun(seq uint64, on bool) {
	qsRadioMu.Lock()
	defer qsRadioMu.Unlock()
	if seq != fnSwitchRadioSeq.Load() {
		return // the switch moved again: the newer request runs instead
	}
	fnSwitchRadio.mu.Lock()
	snap, have := fnSwitchRadio.snap, fnSwitchRadio.have
	fnSwitchRadio.mu.Unlock()
	var err error
	switch {
	case on:
		if !have {
			snap = fnSwitchReadRadios()
			fnSwitchRadio.mu.Lock()
			fnSwitchRadio.snap, fnSwitchRadio.have, fnSwitchRadio.save = snap, true, true
			fnSwitchRadio.mu.Unlock()
		}
		err = fnSwitchRadiosOff(snap)
	case have:
		err = fnSwitchRadiosBack(snap)
		fnSwitchRadio.mu.Lock()
		fnSwitchRadio.have, fnSwitchRadio.save = false, true
		fnSwitchRadio.mu.Unlock()
	}
	if err != nil {
		logWarn("power", "FN switch airplane mode: %v", err)
		if !gameScreenSuspended() {
			ShowToast("Airplane mode: "+firstLine(err.Error()), ToastKindWarning)
		}
	}
	tbxPost(fnSwitchRadiosChanged)
}

// fnSwitchReadRadios is the snapshot: Airplane mode already on, else which of
// Wi-Fi and Bluetooth are on (a failed reading counts as on).
func fnSwitchReadRadios() fnSwitchRadios {
	if on, ok := airplaneEnabled(); ok && on {
		return fnSwitchRadios{already: true}
	}
	wifi, wifiOK := wifiEnabled()
	bt, btOK := bluetoothEnabled()
	return fnSwitchRadios{wifi: wifi || !wifiOK, bt: bt || !btOK}
}

// fnSwitchRadiosOff blocks the radios: every one at once where rfkill can
// (Quick Settings' Airplane Mode), else Wi-Fi and Bluetooth one by one.
func fnSwitchRadiosOff(r fnSwitchRadios) error {
	if r.already {
		return nil
	}
	if airplaneBackend() {
		return setAirplaneMode(true)
	}
	var errs []error
	if r.wifi {
		if err := setWifiEnabled(false); err != nil {
			errs = append(errs, err)
		}
	}
	if r.bt {
		if err := setBluetoothEnabled(false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fnSwitchRadiosBack puts the radios back as the snapshot had them. rfkill
// lets every radio go at once, so one that was off goes back off, as Quick
// Settings' Airplane Mode does.
func fnSwitchRadiosBack(r fnSwitchRadios) error {
	if r.already {
		return nil // Airplane mode was on before the switch: it stays on
	}
	if airplaneBackend() {
		if err := setAirplaneMode(false); err != nil {
			return err
		}
		if !r.wifi {
			_ = setWifiEnabled(false)
		}
		if !r.bt || btSwitchStillOff() {
			_ = setBluetoothEnabled(false)
		}
		return nil
	}
	var errs []error
	if r.wifi {
		if err := setWifiEnabled(true); err != nil {
			errs = append(errs, err)
		} else {
			wifiKeeperRadioOn() // saved networks join again (wifi_keeper.go)
		}
	}
	if r.bt && !btSwitchStillOff() {
		if err := setBluetoothEnabled(true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fnSwitchRadiosChanged has the screens that show the radios read them again.
// Main thread (tbxPost).
func fnSwitchRadiosChanged() {
	if qs.open && len(qs.toggles) > 2 && len(qs.sliders) > 0 {
		qsStartProbe()
	}
	// Home's Wi-Fi tile and the idle screens (home_quicktiles.go).
	saverWifiInvalidate()
}

// ── Settings → Power & battery ─────────────────────────────────────────────

// fnSwitchOptions are the row's stops. Mute and Lights off only where there
// is a mixer and lights to drive.
func fnSwitchOptions(c *Config) []setOption {
	out := []setOption{
		{Value: fnSwitchNothing, Label: "Nothing", Note: "JukaHub leaves the switch alone"},
		{Value: fnSwitchTravel, Label: "Travel lock", Note: "Slid, the power button can't turn the device on in a bag"},
	}
	if volumeControlAvailable() {
		out = append(out, setOption{Value: fnSwitchMute, Label: "Mute", Note: "Slid, the speaker is muted; slid back, the sound returns"})
	}
	if ledSettingsAvailable(c) {
		out = append(out, setOption{Value: fnSwitchLights, Label: "Lights off", Note: "Slid, the stick lights go dark, in games too"})
	}
	return append(out, setOption{Value: fnSwitchAirplane, Label: "Airplane mode", Note: "Slid, Wi-Fi and Bluetooth go off; slid back, as they were"})
}

// fnSwitchModeLabel names a mode for the row's value.
func fnSwitchModeLabel(mode string) string {
	switch mode {
	case fnSwitchTravel:
		return "Travel lock"
	case fnSwitchMute:
		return "Mute"
	case fnSwitchLights:
		return "Lights off"
	case fnSwitchAirplane:
		return "Airplane mode"
	}
	return "Nothing"
}

// fnSwitchSettingsRow is Settings → Power & battery → "FN switch does", in
// Travel lock's old place; shown on the boards Travel lock supports.
func fnSwitchSettingsRow() setRow {
	return setRow{
		ID: "fnSwitch", Label: "FN switch does", Kind: setKindChoice,
		Visible:    func(*Config) bool { return travelLockHardware() },
		Detail:     fnSwitchDetail,
		Options:    fnSwitchOptions,
		Sheet:      fnSwitchOptions,
		SheetValue: fnSwitchModeOf,
		SheetTitle: "FN switch does",
		Current: func(c *Config) int {
			return ledOptionIndex(fnSwitchOptions(c), fnSwitchModeOf(c))
		},
		Value:  func(c *Config) string { return fnSwitchModeLabel(fnSwitchModeOf(c)) },
		Choose: fnSwitchChoose,
	}
}

// fnSwitchDetail is the row's second line: what the switch does, and what it
// holds now. Cheap: drawn every frame.
func fnSwitchDetail(c *Config) string {
	held := fnSwitch.held
	switch fnSwitchModeOf(c) {
	case fnSwitchTravel:
		// Travel lock's own line (its old row, travellock.go).
		return travelLockSettingsRow().Detail(c)
	case fnSwitchMute:
		if held == fnSwitchMute {
			return "Muted by the FN switch: slide it back for sound"
		}
		return "Slide the FN switch to mute the speaker; slide it back for sound"
	case fnSwitchLights:
		if held == fnSwitchLights {
			return "Dark by the FN switch: slide it back to light the sticks"
		}
		return "Slide the FN switch to turn the stick lights off; slide it back to light them"
	case fnSwitchAirplane:
		if held == fnSwitchAirplane {
			return "Airplane mode by the FN switch: slide it back for Wi-Fi and Bluetooth"
		}
		return "Slide the FN switch for airplane mode; slide it back for Wi-Fi and Bluetooth as they were"
	}
	return "Travel lock, mute, stick lights off or airplane mode"
}

// fnSwitchChoose applies a pick. Whatever the switch holds is let go first:
// the pick calls the switch's present position off. Main thread.
func fnSwitchChoose(c *Config, value string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if fnSwitch.held != "" {
		fnSwitchRelease(c, false)
	}
	fnSwitch.configAt = time.Time{} // the next tick reads the setting again
	switch value {
	case fnSwitchTravel:
		// Travel lock's own switch: it reads the position, stores its
		// calibration, writes the boot hook and says so.
		travelLockSet(c, true)
		if !travelLockEnabled(c) {
			return
		}
		c.Variables.Custom[fnSwitchModeKey] = fnSwitchTravel
		saveUserConfigDebounced(userConfigFromVariables(c))
	case fnSwitchMute, fnSwitchLights, fnSwitchAirplane:
		pos, ok := travelLockReadSwitch()
		if !ok {
			setToast("JukaHub cannot read the FN switch here")
			return
		}
		fnSwitchTravelOff(c)
		c.Variables.Custom[fnSwitchModeKey] = value
		c.Variables.Custom[fnSwitchAtKey] = strconv.Itoa(1 - pos)
		saveUserConfigDebounced(userConfigFromVariables(c))
		// The position now is off: the reader starts from it.
		fnSwitch.pos.Store(int32(pos))
		fnSwitch.armed.Store(true)
		fnSwitch.start.Do(func() { go fnSwitchWatch() })
		setToast("FN switch: " + strings.ToLower(fnSwitchModeLabel(value)) + " when slid, back to undo")
	default:
		fnSwitchTravelOff(c)
		c.Variables.Custom[fnSwitchModeKey] = fnSwitchNothing
		saveUserConfigDebounced(userConfigFromVariables(c))
		setToast("FN switch: does nothing")
	}
}

// fnSwitchTravelOff turns Travel lock off quietly (the pick says what the
// switch does now): travelLockSet(false) without its toast. The caller saves.
func fnSwitchTravelOff(c *Config) {
	if !travelLockEnabled(c) {
		return
	}
	c.Variables.Custom[travelLockKey] = boolString(false)
	travelLockNoteConfig(c)
	if err := travelLockRemoveHook(); err != nil {
		logWarn("power", "travel lock: could not remove the boot hook: %v", err)
	}
}
