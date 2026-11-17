package main

// bt_autoreconnect.go — remembered devices come back by themselves.
//
// A headset or a pad paired on the Bluetooth page stayed disconnected after a
// restart or a sleep until someone opened the page and pressed Connect (a pad
// could still wake itself with its PS button; a headset waits to be called).
// This loop does the calling, in the background, without any screen:
//
//   - When: shortly after JukaHub starts, after every wake from sleep, and
//     when the radio comes on. Each of those gets three rounds at most (now,
//     20 s later, a minute after that), then it gives up quietly until the
//     next one. Nothing is said on screen either way: the header's Bluetooth
//     and output chips already show what connected.
//   - What: paired, trusted headphones and controllers that are not
//     connected — at most four a round, so a drawer of old pads does not
//     keep the radio paging (Wi-Fi shares it).
//   - Never: with the Bluetooth switch off (the loop does not switch it on),
//     while a game owns the screen, while the Bluetooth page is open (it runs
//     its own bluetoothctl), or for a device the user disconnected on the page
//     during this run — until it connects again some other way.
//
// The same loop reads the pads' batteries once a minute and warns once when a
// pad drops to 15 % (bt_padinfo.go), again only after it was charged.

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// btAutoFirstDelay lets the first frames and the radio's bring-up go
	// first after a start.
	btAutoFirstDelay = 15 * time.Second
	// btAutoTick is how often the loop looks at the radio and its schedule.
	btAutoTick = 10 * time.Second
	// btAutoMaxDevices bounds the devices called in one round.
	btAutoMaxDevices = 4
	// btAutoRoundLimit bounds one round's bluetoothctl work.
	btAutoRoundLimit = 2 * time.Minute
	// btBatteryEvery is how often the pads' batteries are read.
	btBatteryEvery = time.Minute
	// btBatteryLow warns; btBatteryRearm (or a charge) re-arms the warning.
	btBatteryLow   = 15
	btBatteryRearm = 25
)

// btAutoBackoff is the wait before each round of one trigger: the first runs
// at once.
var btAutoBackoff = []time.Duration{0, 20 * time.Second, time.Minute}

// btAuto is the loop's state shared with the page.
var btAuto struct {
	once sync.Once
	mu   sync.Mutex
	// manual are the addresses the user disconnected on the page this run.
	manual map[string]bool
	// lowWarned are the pads already warned about a low battery.
	lowWarned map[string]bool
}

// btAutoPageAt is when the Bluetooth page last drew a frame (Unix ns).
var btAutoPageAt atomic.Int64

// btAutoKicks asks the loop for a fresh set of rounds.
var btAutoKicks = make(chan struct{}, 1)

// startBluetoothAutoReconnect starts the loop once (main.go). Linux only:
// elsewhere the system's own Bluetooth reconnects devices.
func startBluetoothAutoReconnect() {
	if runtime.GOOS != "linux" {
		return
	}
	btAuto.once.Do(func() { go btAutoLoop() })
}

// btAutoKick starts a fresh set of rounds (the radio was just switched on
// from Quick Settings, for instance). Never blocks.
func btAutoKick() {
	select {
	case btAutoKicks <- struct{}{}:
	default:
	}
}

// btAutoNotePage is called by every frame of the Bluetooth page.
func btAutoNotePage() { btAutoPageAt.Store(time.Now().UnixNano()) }

// btAutoPageUp reports a Bluetooth page frame in the last few seconds.
func btAutoPageUp() bool {
	at := btAutoPageAt.Load()
	return at != 0 && time.Since(time.Unix(0, at)) < 3*time.Second
}

// btAutoNoteManualDisconnect keeps the loop away from a device the user just
// disconnected on purpose.
func btAutoNoteManualDisconnect(mac string) {
	mac = btNormMAC(mac)
	if mac == "" {
		return
	}
	btAuto.mu.Lock()
	if btAuto.manual == nil {
		btAuto.manual = map[string]bool{}
	}
	btAuto.manual[mac] = true
	btAuto.mu.Unlock()
}

// btAutoClearManual lets the loop call a device again: it was connected or
// paired on purpose, or came back by itself.
func btAutoClearManual(mac string) {
	mac = btNormMAC(mac)
	btAuto.mu.Lock()
	delete(btAuto.manual, mac)
	btAuto.mu.Unlock()
}

// btAutoManual reports a device the user disconnected this run.
func btAutoManual(mac string) bool {
	btAuto.mu.Lock()
	defer btAuto.mu.Unlock()
	return btAuto.manual[btNormMAC(mac)]
}

// btAutoBusy reports a moment the loop must not use the radio: a game, the
// page, or JukaHub on its way out (a round's bluetoothctl must not outlive it).
func btAutoBusy() bool {
	return exitRequested.Load() || gameScreenSuspended() || btAutoPageUp()
}

// btAutoLoop is the background loop. One goroutine for the whole run.
func btAutoLoop() {
	time.Sleep(btAutoFirstDelay)
	wake := subscribeWake()
	ticker := time.NewTicker(btAutoTick)
	defer ticker.Stop()
	round, nextAt := 0, time.Now()
	radioWasOn := false
	var batteryAt time.Time
	for {
		select {
		case <-ticker.C:
		case <-wake:
			// bluetoothd and the chip need a moment after a resume.
			round, nextAt = 0, time.Now().Add(5*time.Second)
		case <-btAutoKicks:
			round, nextAt = 0, time.Now()
		}
		if !btDevicesAvailable() {
			continue
		}
		on, known := btRadioCachedState()
		if !on || !known {
			radioWasOn = false
			continue
		}
		if !radioWasOn {
			// On at start, or switched on since: that is a trigger too.
			radioWasOn = true
			round, nextAt = 0, time.Now()
		}
		if !gameScreenSuspended() && time.Since(batteryAt) >= btBatteryEvery {
			batteryAt = time.Now()
			btCheckPadBatteries()
		}
		if round >= len(btAutoBackoff) || time.Now().Before(nextAt) || btAutoBusy() {
			continue
		}
		pending := btAutoRound()
		round++
		if !pending {
			round = len(btAutoBackoff)
			continue
		}
		if round < len(btAutoBackoff) {
			nextAt = time.Now().Add(btAutoBackoff[round])
		}
	}
}

// btAutoRound calls the remembered devices that are not connected, and
// reports whether any is still worth another round.
func btAutoRound() (pending bool) {
	ctx, cancel := context.WithTimeout(context.Background(), btAutoRoundLimit)
	defer cancel()
	// A game starting or the page opening ends the round at once: its
	// bluetoothctl session is killed with the context.
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if btAutoBusy() {
					cancel()
					return
				}
			}
		}
	}()
	devs, err := bzList(ctx)
	if err != nil {
		logDebug("bluetooth", "auto-reconnect: could not list the devices: %v", err)
		return true
	}
	called := 0
	for _, d := range devs {
		if d.Connected {
			// Back by itself or by hand: a manual disconnect is over.
			btAutoClearManual(d.MAC)
			continue
		}
		if !d.Paired || !d.Trusted || btAutoManual(d.MAC) {
			continue
		}
		kind := d.kind()
		if kind != bzKindAudio && kind != bzKindController {
			continue
		}
		if called >= btAutoMaxDevices || btAutoBusy() || ctx.Err() != nil {
			return true
		}
		called++
		out, err := bzConnect(ctx, d, nil)
		if err != nil || !out.Connected {
			pending = true
			continue
		}
		logInfo("bluetooth", "auto-reconnect %s: connected", bzShortMAC(d.MAC))
		btQuickKick()
		if kind == bzKindAudio {
			// The sound moves over now rather than at the next probe.
			audioRouteKick()
		}
	}
	return pending
}

// btCheckPadBatteries warns once when a pad's battery runs low.
func btCheckPadBatteries() {
	for _, b := range btPadBatteries() {
		key := b.MAC
		if key == "" {
			key = b.Name
		}
		if key == "" || b.Percent < 0 {
			continue
		}
		btAuto.mu.Lock()
		if btAuto.lowWarned == nil {
			btAuto.lowWarned = map[string]bool{}
		}
		warned := btAuto.lowWarned[key]
		low := b.Percent <= btBatteryLow && !b.Charging
		switch {
		case low && !warned:
			btAuto.lowWarned[key] = true
		case warned && (b.Charging || b.Percent >= btBatteryRearm):
			delete(btAuto.lowWarned, key)
		}
		btAuto.mu.Unlock()
		if low && !warned {
			name := b.Name
			if name == "" {
				name = "A controller"
			}
			ShowToast(name+" battery is low ("+strconv.Itoa(b.Percent)+"%) — charge it soon", ToastKindWarning)
		}
	}
}
