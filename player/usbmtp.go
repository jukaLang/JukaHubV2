package main

// usbmtp.go — "USB file transfer": plug the handheld into a computer with a
// USB cable and its SD card opens there the way a phone's storage does —
// This PC → the handheld in Windows' File Explorer, the file manager on
// Linux, OpenMTP or Android File Transfer on a Mac. Drag ROMs, BIOS files,
// saves and screenshots either way while JukaHub keeps running.
//
// It is MTP (usbmtp_proto.go) over the kernel's USB gadget framework
// (usbmtp_linux.go), served by JukaHub itself: no helper to download. It is
// on by default on the handheld (Settings → Network → USB file transfer) and
// starts after the first frame; turning it off gives the port back to
// whatever the firmware had on it (ADB) and leaves the cable charging only.
// While the startup lock is up the computer sees the handheld but no card.

import (
	"runtime"
	"strings"
	"sync"
	"time"
)

// usbMtpSettingKey is the Settings switch. Missing means on.
const usbMtpSettingKey = "usbFileTransfer"

// usbMtpStartConfig is what the platform side needs to start serving.
type usbMtpStartConfig struct {
	root      string        // the SD card
	name      func() string // what the computer calls the handheld
	serial    string
	locked    func() bool   // the startup lock is up
	connected func(on bool) // a computer came or went (any goroutine)
	restart   func()        // start over, e.g. on a gadget of its own (any goroutine)
	net       bool          // add the network function too ("Browser over USB", usbnet.go)
}

// usbMtp is the service's state. mu guards it; usbMtpOpMu serialises the
// slow start and stop so a quick off-on-off always ends where the switch is.
var usbMtp struct {
	mu        sync.Mutex
	want      bool
	running   bool
	starting  bool
	stop      func()
	err       string
	connected bool
	lastToast time.Time
	gen       int
	wantNet   bool // the "Browser over USB" switch, copied on the main thread
	netUsed   bool // what the running instance was started with
}

var usbMtpOpMu sync.Mutex

func init() {
	// After the first frame, on the main thread, where the setting may be
	// read (startup.go); the gadget is set up on a goroutine after that.
	deferStartupTask("usb-file-transfer", func() {
		if usbMtpSupported() && usbMtpEnabled(appConfig) {
			usbMtpNoteNetSwitch(appConfig)
			usbMtpApply(true)
		}
	})
	// Give the port back on the way out, so ADB keeps working after JukaHub
	// closes.
	Cleanup = append(Cleanup, usbMtpShutdown)
}

// usbMtpSupported: the handheld only. A Linux computer running JukaHub has
// no business turning its own USB port into a device.
func usbMtpSupported() bool { return runtime.GOOS == "linux" && IsTrimuiDevice() }

// usbMtpEnabled reports the Settings switch (main thread). It is on unless
// turned off; only an explicit off counts, so a garbled value keeps the
// default.
func usbMtpEnabled(config *Config) bool { return usbSwitchOn(config, usbMtpSettingKey) }

// usbSwitchOn reads a USB switch that is on unless turned off (main thread).
func usbSwitchOn(config *Config, key string) bool {
	if config == nil {
		return true
	}
	v, ok := customValue(config.Variables.Custom, key)
	if !ok || v == nil {
		return true
	}
	if s, isString := v.(string); isString {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "off", "no", "0":
			return false
		}
		return true
	}
	return coerceBool(v)
}

// usbMtpSetEnabled stores the switch and starts or stops the service.
func usbMtpSetEnabled(config *Config, on bool) {
	setStoreBool(config, usbMtpSettingKey, on)
	usbMtpNoteNetSwitch(config)
	usbMtpApply(on)
}

// usbMtpNoteNetSwitch copies the "Browser over USB" switch for the next
// start (main thread; the start runs on a goroutine).
func usbMtpNoteNetSwitch(config *Config) {
	on := usbNetEnabled(config)
	usbMtp.mu.Lock()
	usbMtp.wantNet = on
	usbMtp.mu.Unlock()
}

// usbMtpApply asks for the service on or off; the work happens on a
// goroutine.
func usbMtpApply(on bool) {
	usbMtp.mu.Lock()
	usbMtp.want = on
	if on {
		usbMtp.err = ""
		usbMtp.starting = !usbMtp.running
	}
	usbMtp.mu.Unlock()
	go usbMtpSettle()
}

// usbMtpSettle brings the service to what the switch says.
func usbMtpSettle() {
	usbMtpOpMu.Lock()
	defer usbMtpOpMu.Unlock()
	usbMtp.mu.Lock()
	want, running := usbMtp.want, usbMtp.running
	usbMtp.mu.Unlock()
	switch {
	case want && !running:
		usbMtpStartNow()
	case !want && running:
		usbMtpStopNow()
	default:
		usbMtp.mu.Lock()
		usbMtp.starting = false
		usbMtp.mu.Unlock()
	}
}

func usbMtpStartNow() {
	fail := func(msg string) {
		usbMtp.mu.Lock()
		usbMtp.starting = false
		usbMtp.err = msg
		usbMtp.mu.Unlock()
		usbNetSetStatus(usbNetOff, "", "")
		logWarn("usbmtp", "USB file transfer not started: %s", msg)
	}
	// Controller mode has the port (usbpad.go); it starts this again after.
	if usbPadHolds.Load() {
		fail("controller mode is using the USB port")
		return
	}
	root := fileServerCardRoot()
	if root == "" {
		fail("no SD card found")
		return
	}
	// The generation this start will have once it succeeds (starts and
	// stops are serialised by usbMtpOpMu): a restart the platform asks for
	// later is only for this instance, not one the switch made since.
	usbMtp.mu.Lock()
	gen := usbMtp.gen + 1
	withNet := usbMtp.wantNet
	usbMtp.mu.Unlock()
	if withNet {
		usbNetSetStatus(usbNetStarting, "", "")
	} else {
		usbNetSetStatus(usbNetOff, "", "")
	}
	srv, stop, err := usbMtpPlatformStart(usbMtpStartConfig{
		root:      root,
		name:      usbMtpDeviceName,
		serial:    usbMtpSerial(),
		locked:    startupLocked,
		connected: usbMtpNoteConnected,
		restart:   func() { usbMtpRestart(gen) },
		net:       withNet,
	})
	if err != nil {
		fail(firstLine(err.Error()))
		return
	}
	unavailable := usbNetUnavailable()
	usbMtp.mu.Lock()
	usbMtp.starting = false
	usbMtp.running = true
	usbMtp.stop = stop
	usbMtp.gen = gen
	usbMtp.netUsed = withNet
	// "Browser over USB" was switched while this start was under way (and
	// the link can be had here at all).
	again := usbMtp.wantNet != withNet && !unavailable
	usbMtp.mu.Unlock()
	logInfo("usbmtp", "USB file transfer ready, sharing %s", root)
	go usbMtpWatchLock(srv, gen)
	if again {
		usbMtpRestart(gen)
	}
}

func usbMtpStopNow() {
	usbMtp.mu.Lock()
	stop := usbMtp.stop
	usbMtp.stop = nil
	usbMtp.running = false
	usbMtp.connected = false
	usbMtp.starting = false
	usbMtp.gen++
	usbMtp.mu.Unlock()
	if stop != nil {
		stop()
	}
	usbNetSetStatus(usbNetOff, "", "")
	logInfo("usbmtp", "USB file transfer off")
}

// usbMtpRestart stops the instance of generation gen and starts again if
// the switch is still on. Any goroutine; the work runs on its own. A
// generation that already ended (switched off, or off and on) is left be.
func usbMtpRestart(gen int) {
	go func() {
		usbMtpOpMu.Lock()
		defer usbMtpOpMu.Unlock()
		usbMtp.mu.Lock()
		running, current := usbMtp.running, usbMtp.gen
		usbMtp.mu.Unlock()
		if !running || current != gen {
			return
		}
		usbMtpStopNow()
		usbMtp.mu.Lock()
		want := usbMtp.want
		if want {
			usbMtp.starting = true
		}
		usbMtp.mu.Unlock()
		if want {
			usbMtpStartNow()
		}
	}()
}

// usbMtpShutdown is the exit hook: off, within a bound. A start still in
// progress is waited for and then stopped, or the process would leave with
// the port bound to a function nobody serves.
func usbMtpShutdown() {
	usbMtp.mu.Lock()
	usbMtp.want = false
	active := usbMtp.running || usbMtp.starting
	usbMtp.mu.Unlock()
	if !active {
		return
	}
	done := make(chan struct{})
	go func() {
		usbMtpSettle()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		logWarn("usbmtp", "USB file transfer did not stop in time")
	}
}

// usbMtpWatchLock tells a connected computer when the startup lock hides or
// shows the card again, until the service it was started for stops.
func usbMtpWatchLock(srv *mtpServer, gen int) {
	last := startupLocked()
	for {
		time.Sleep(time.Second)
		usbMtp.mu.Lock()
		current := usbMtp.gen
		usbMtp.mu.Unlock()
		if current != gen {
			return
		}
		if now := startupLocked(); now != last {
			last = now
			srv.lockChanged(now)
		}
	}
}

// usbMtpDeviceName is what the computer lists the handheld as: the name
// Nearby devices uses, so both say the same.
func usbMtpDeviceName() string {
	if name := strings.TrimSpace(peerDeviceName()); name != "" {
		return name
	}
	return "JukaHub"
}

// usbMtpNoteConnected follows the cable. A computer arriving gets one toast;
// a port that flaps (a hub, a sleeping laptop) does not repeat it.
func usbMtpNoteConnected(on bool) {
	usbMtp.mu.Lock()
	was := usbMtp.connected
	usbMtp.connected = on
	toast := on && !was && time.Since(usbMtp.lastToast) > 15*time.Second
	if toast {
		usbMtp.lastToast = time.Now()
	}
	usbMtp.mu.Unlock()
	if !toast {
		return
	}
	if startupLocked() {
		ShowToast("Connected to a computer — unlock JukaHub to open your files there", ToastKindInfo)
		return
	}
	ShowToast("Connected to a computer — open “"+usbMtpDeviceName()+"” there to copy files", ToastKindInfo)
}

// usbMtpSettingDetail is the line under the switch.
func usbMtpSettingDetail(c *Config) string {
	if !usbMtpEnabled(c) {
		return "Off: a cable to a computer only charges"
	}
	usbMtp.mu.Lock()
	defer usbMtp.mu.Unlock()
	switch {
	case usbMtp.err != "":
		return "Not available: " + usbMtp.err
	case usbMtp.starting:
		return "Getting the USB port ready…"
	case usbMtp.connected && startupLocked():
		return "Connected · unlock JukaHub to see your files on the computer"
	case usbMtp.connected:
		return "Connected: the SD card is open on the computer"
	}
	return "Plug into a computer to copy files to and from the SD card (on a Mac, with OpenMTP)"
}

// usbMtpSettingRow is the switch in Settings → Network.
func usbMtpSettingRow() setRow {
	return setRow{
		ID: "usbFileTransfer", Label: "USB file transfer", Kind: setKindToggle,
		Visible: func(*Config) bool { return usbMtpSupported() },
		Detail:  usbMtpSettingDetail,
		Get:     usbMtpEnabled,
		Set: func(c *Config, on bool) {
			usbMtpSetEnabled(c, on)
			setToast("USB file transfer: " + setOnOff(on))
		},
	}
}
