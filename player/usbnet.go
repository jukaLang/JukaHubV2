package main

// usbnet.go — "Browser over USB": the USB cable to a computer also carries a
// tiny network of its own, so the computer's browser opens the Phone
// Companion page at http://10.73.73.1 — drag games, covers and music in the
// same way as over Wi-Fi, with no Wi-Fi at all.
//
// It rides on "USB file transfer" (usbmtp.go): when that service sets up the
// USB gadget it also adds a network function (usbnet_linux.go) — RNDIS, which
// Windows 10/11 and Linux use with drivers they already have, swapped for ECM
// when the computer stays silent on it (macOS). The handheld takes
// 10.73.73.1/29 on that interface and answers DHCP (usbnet_dhcp.go) with an
// address and a mask only, so the computer keeps its own internet route. The
// page itself is the Phone Companion, which listens on every interface and
// starts by itself once the handheld has an address (companionEnsureStarted).
//
// On by default on the handheld (Settings → Network → Browser over USB, under
// USB file transfer). When the USB controller has too few endpoints for the
// network function next to adb and file transfer, file transfer is kept and
// the Settings line says why.

import (
	"strings"
	"sync"
)

// usbNetSettingKey is the Settings switch. Missing means on.
const usbNetSettingKey = "usbBrowserLink"

// What the network link is doing, for the Settings line.
const (
	usbNetOff      = iota // not part of the gadget (switched off, or file transfer is off)
	usbNetStarting        // being added
	usbNetUp              // the interface has its address
	usbNetNone            // cannot be had here; reason says why
)

// usbNet is the link's state. mu guards it; the USB side writes it from its
// goroutines and the Settings line reads it on the main thread.
var usbNet struct {
	mu     sync.Mutex
	state  int
	kind   string // "RNDIS", "ECM" or "NCM" while up
	reason string // why there is no link (usbNetNone)
	host   bool   // a computer talks over the link
	// final: usbNetNone with nothing of the link on the port, and trying
	// again this run would not change that (no kernel function, no room,
	// the firmware's own network). Switching then leaves the port alone.
	final bool
}

func usbNetSetStatus(state int, kind, reason string) {
	usbNet.mu.Lock()
	usbNet.state, usbNet.kind, usbNet.reason, usbNet.final = state, kind, reason, false
	if state != usbNetUp {
		usbNet.host = false
	}
	usbNet.mu.Unlock()
}

// usbNetSetUnavailable: no link this run, and nothing of it on the port.
func usbNetSetUnavailable(reason string) {
	usbNet.mu.Lock()
	usbNet.state, usbNet.kind, usbNet.reason, usbNet.final = usbNetNone, "", reason, true
	usbNet.host = false
	usbNet.mu.Unlock()
}

// usbNetUnavailable reports usbNetSetUnavailable's state.
func usbNetUnavailable() bool {
	usbNet.mu.Lock()
	defer usbNet.mu.Unlock()
	return usbNet.state == usbNetNone && usbNet.final
}

func usbNetSetHost(on bool) {
	usbNet.mu.Lock()
	usbNet.host = on && usbNet.state == usbNetUp
	usbNet.mu.Unlock()
}

// usbNetEnabled reports the Settings switch (main thread): on unless turned
// off, like USB file transfer.
func usbNetEnabled(config *Config) bool { return usbSwitchOn(config, usbNetSettingKey) }

// usbNetSetEnabled stores the switch. A running USB service is set up again
// with or without the network function, which the computer sees as the
// handheld being plugged in again — unless the link cannot be had in this
// run at all: then file transfer is left alone.
func usbNetSetEnabled(config *Config, on bool) {
	setStoreBool(config, usbNetSettingKey, on)
	unavailable := usbNetUnavailable()
	usbMtp.mu.Lock()
	usbMtp.wantNet = on
	running, used, gen := usbMtp.running, usbMtp.netUsed, usbMtp.gen
	if running && unavailable {
		usbMtp.netUsed = on
	}
	usbMtp.mu.Unlock()
	if running && used != on && !unavailable {
		usbMtpRestart(gen)
	}
}

// usbNetSettingDetail is the line under the switch (main thread): what to do,
// or why it cannot be done.
func usbNetSettingDetail(c *Config) string {
	if !usbNetEnabled(c) {
		return "Off: the cable carries file transfer only"
	}
	usbNet.mu.Lock()
	state, reason, host := usbNet.state, usbNet.reason, usbNet.host
	usbNet.mu.Unlock()
	usbMtp.mu.Lock()
	mtpErr, starting, running := usbMtp.err, usbMtp.starting, usbMtp.running
	usbMtp.mu.Unlock()
	switch {
	case state == usbNetNone && reason != "":
		return reason
	case mtpErr != "":
		return "Needs USB file transfer, which is not available here"
	case starting || state == usbNetStarting || (state == usbNetOff && running):
		return "Getting the USB port ready…"
	case state != usbNetUp:
		return "Starts with USB file transfer"
	case !safeModeAllows(SafeFeatureNetwork):
		return "Phone companion is off in safe mode, so there is no page to open"
	}
	if companion.srv == nil || !companion.srv.Running() {
		// The companion starts by itself once the handheld has an address
		// (main loop); only an error keeps it off.
		if companion.err != "" && !strings.HasPrefix(companion.err, "No network address") {
			return "Phone companion is off: " + companion.err
		}
		return "Starting the phone companion…"
	}
	pin := companion.pin
	if shotModeOn() {
		pin = shotPairPIN
	}
	url := companionBaseURL(usbNetDeviceIP(), companion.srv.BrowserPort())
	if host {
		return "Connected: open " + url + " in the computer's browser (PIN " + pin + ")"
	}
	return "Plug into a computer and open " + url + " (PIN " + pin + ")"
}

// usbNetSettingRow is the switch in Settings → Network, under USB file
// transfer, which it needs.
func usbNetSettingRow() setRow {
	return setRow{
		ID: usbNetSettingKey, Label: "Browser over USB", Kind: setKindToggle,
		Visible: func(c *Config) bool { return usbMtpSupported() && usbMtpEnabled(c) },
		Detail:  usbNetSettingDetail,
		Get:     usbNetEnabled,
		Set: func(c *Config, on bool) {
			usbNetSetEnabled(c, on)
			setToast("Browser over USB: " + setOnOff(on))
		},
	}
}
