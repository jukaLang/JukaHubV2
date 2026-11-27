package main

// bt_bootswitch.go — the Bluetooth switch keeps its place across a restart.
//
// A stock TrimUI brings Bluetooth up at every boot by its own setting (its
// bring-up script and bluetoothd start before JukaHub does, and BlueZ powers
// the adapter on), so a radio switched off in JukaHub was on again after
// every restart. Every switch JukaHub makes is now remembered
// (data/bluetooth-switch.json), and on a TrimUI a radio last left off is
// switched off again after JukaHub starts: looked at a few times while the
// firmware's bring-up finishes, switched off at most twice, and never once
// anything this session switched it on. The firmware's own setting follows
// the switch too where system.json has one ("bluetooth" or "btswitch",
// holding 0 or 1), so its next boot can leave the radio off by itself; one
// switched on in the stock menu after JukaHub left it off is the newer choice
// and is kept.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	// btSwitchFileName is the remembered switch, in JukaHub's data folder.
	btSwitchFileName = "bluetooth-switch.json"
	// btSwitchMaxOffs bounds the switch-offs in the start-up burst: a firmware
	// that keeps switching the radio back on is not fought there. After that,
	// a wake watcher still honours an explicit Off (btSwitchWatchEvery).
	btSwitchMaxOffs = 2
	// btSwitchWatchEvery is the slow re-check of an explicit Off. It catches a
	// firmware or daemon that powers the adapter up long after the start-up
	// window, including after a suspend/resume that is not a full boot.
	btSwitchWatchEvery = 2 * time.Minute
)

// btSwitchChecks are when, after JukaHub starts, a radio left off is looked at:
// the firmware's bring-up can finish well after JukaHub's first frame.
var btSwitchChecks = []time.Duration{6 * time.Second, 20 * time.Second, 45 * time.Second, 90 * time.Second}

// btSwitchSystemKeys are the firmware's own Bluetooth setting in system.json,
// as far as it is known; only one already there with 0 or 1 is written.
var btSwitchSystemKeys = []string{"bluetooth", "btswitch"}

// btSwitchState is the switch as remembered, and whether anything switched
// the radio on since JukaHub started. Any goroutine.
var btSwitchState struct {
	mu            sync.Mutex
	known, on     bool
	onThisSession bool
}

// btSwitchRecord is the file's content.
type btSwitchRecord struct {
	On bool   `json:"on"`
	At string `json:"at,omitempty"`
	// Firmware is the firmware's own setting as JukaHub left it ("on",
	// "off", "" without one): a different one at the next start was changed
	// in the stock menu since, and that is the newer choice.
	Firmware string `json:"firmware,omitempty"`
}

// btSwitchPath is the remembered switch's file, "" without a data folder. A
// variable so a test can point it at a temporary file.
var btSwitchPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, btSwitchFileName)
}

// btSwitchRemember records where a switch JukaHub made left the radio (on
// true also for a bring-up the Bluetooth page or Controller Mode needed).
// Called by setBluetoothEnabled and btBringUp after they succeeded; any
// goroutine. Only a change is written.
func btSwitchRemember(on bool) {
	if runtime.GOOS != "linux" {
		return
	}
	if !btSwitchNote(on) {
		return
	}
	rec := btSwitchRecord{On: on, At: time.Now().UTC().Format(time.RFC3339)}
	if IsTrimuiDevice() {
		btSwitchSystemJSON(on)
		rec.Firmware = btSwitchFirmwareWord()
	}
	if path := btSwitchPath(); path != "" {
		data, err := json.Marshal(rec)
		if err == nil {
			err = renameIntoPlace(path+".tmp", path, data)
		}
		if err != nil {
			logDebug("bluetooth", "the Bluetooth switch was not remembered: %v", err)
		}
	}
}

// btSwitchNote updates the in-memory switch and reports whether it changed, so
// the file and firmware setting are only written on a real change. Kept apart
// from the platform guard so the session latch can be tested anywhere.
func btSwitchNote(on bool) bool {
	st := &btSwitchState
	st.mu.Lock()
	if on {
		st.onThisSession = true
	} else {
		// An explicit Off re-arms the start-up restore and the wake watcher:
		// once a user said off, an incidental bring-up or a later wake must
		// not be able to latch the radio on for the rest of the session.
		st.onThisSession = false
	}
	same := st.known && st.on == on
	st.known, st.on = true, on
	st.mu.Unlock()
	return !same
}

// btSwitchFirmwareWord is the firmware's own setting as a record keeps it.
func btSwitchFirmwareWord() string {
	on, known := btSwitchFirmwareSetting()
	switch {
	case !known:
		return ""
	case on:
		return "on"
	}
	return "off"
}

// btSwitchSystemJSON sets the firmware's own Bluetooth setting, where
// system.json has one of btSwitchSystemKeys with 0 or 1.
func btSwitchSystemJSON(on bool) {
	v := 0
	if on {
		v = 1
	}
	err := trimuiSystemJSONRewrite(func(data []byte) ([]byte, bool) {
		for _, key := range btSwitchSystemKeys {
			if _, ok, inRange := trimuiSystemJSONIntFrom(data, key, 1); ok && inRange {
				return trimuiSystemJSONWithInt(data, key, v, 1)
			}
		}
		return data, false
	})
	if err != nil && !os.IsNotExist(err) {
		logDebug("bluetooth", "the firmware's Bluetooth setting was not updated: %v", err)
	}
}

// btSwitchFirmwareSetting reads the firmware's own Bluetooth setting from
// system.json; known is false when the file has none of btSwitchSystemKeys
// holding 0 or 1.
func btSwitchFirmwareSetting() (on, known bool) {
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		return false, false
	}
	for _, key := range btSwitchSystemKeys {
		if v, ok, inRange := trimuiSystemJSONIntFrom(data, key, 1); ok && inRange {
			return v == 1, true
		}
	}
	return false, false
}

// btSwitchLoad reads the remembered switch; ok is false when there is none.
func btSwitchLoad() (btSwitchRecord, bool) {
	path := btSwitchPath()
	if path == "" {
		return btSwitchRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4<<10 {
		return btSwitchRecord{}, false
	}
	var rec btSwitchRecord
	if json.Unmarshal(data, &rec) != nil {
		return btSwitchRecord{}, false
	}
	return rec, true
}

// btSwitchStillOff is the authoritative answer: an explicit Off is remembered
// on disk, and nothing this session switched on has cancelled it since. The
// persisted file is re-read every time because a firmware service can power
// the adapter up after the in-memory state was set.
func btSwitchStillOff() bool {
	st := &btSwitchState
	st.mu.Lock()
	onThisSession := st.onThisSession
	st.mu.Unlock()
	if onThisSession {
		return false
	}
	rec, ok := btSwitchLoad()
	return ok && !rec.On
}

// btSwitchEnforceOff puts the radio back off when the user's stored choice is
// off and something powered it up. Main thread (it posts the power-off).
func btSwitchEnforceOff() {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() || !btSwitchStillOff() {
		return
	}
	if on, known := btKernelRadio(); !known || !on {
		return
	}
	logInfo("bluetooth", "Bluetooth came on though it was left off; switching it off again")
	btPowerOffAsync(nil)
}

// btSwitchWatchWake keeps an explicit Off after a long sleep. A suspend/resume
// is not a boot, so the start-up burst below never runs; the firmware (or a
// bluetoothd restart) can power the adapter up on wake. One watcher per run,
// same as the auto-reconnect loop.
func btSwitchWatchWake() {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return
	}
	go func() {
		wake := subscribeWake()
		tick := time.NewTicker(btSwitchWatchEvery)
		defer tick.Stop()
		for {
			select {
			case <-wake:
				// bluetoothd and the chip need a moment after a resume.
				time.Sleep(5 * time.Second)
			case <-tick.C:
			}
			btSwitchEnforceOff()
		}
	}()
}

// btSwitchRestoreAtStart switches the radio off again after a start on a
// TrimUI when JukaHub last left it off and the firmware's boot switched it on
// (see the file comment). It returns at once; the checks run on a goroutine.
func btSwitchRestoreAtStart() {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return
	}
	rec, ok := btSwitchLoad()
	if !ok {
		return
	}
	if now := btSwitchFirmwareWord(); now == "on" && rec.Firmware == "off" {
		// The firmware's own setting was switched on in the stock menu since
		// JukaHub explicitly left it off: that is the newer choice. Persist it,
		// or the wake watcher would keep enforcing the stale Off in the file.
		rec.On = true
		btSwitchRemember(true)
	}
	st := &btSwitchState
	st.mu.Lock()
	if !st.known {
		st.known, st.on = true, rec.On
	}
	st.mu.Unlock()
	btSwitchWatchWake()
	if rec.On {
		return
	}
	go func() {
		start := time.Now()
		offs := 0
		for _, at := range btSwitchChecks {
			time.Sleep(time.Until(start.Add(at)))
			if !btSwitchStillOff() {
				return
			}
			if on, known := btKernelRadio(); !known || !on {
				continue
			}
			offs++
			logInfo("bluetooth", "Bluetooth was off when JukaHub last ran and came on with the firmware's start; switching it off again")
			done := make(chan struct{})
			tbxPost(func() {
				// Looked at again here: a press that switched it on since wins.
				if btSwitchStillOff() {
					btPowerOffAsync(nil)
				}
				close(done)
			})
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			if offs >= btSwitchMaxOffs {
				return
			}
		}
	}()
}
