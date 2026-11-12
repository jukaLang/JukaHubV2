package main

// wifi_resume.go — helping the handheld's radio back after a wake.
//
// The stock TrimUI firmware turns Wi-Fi off for a sleep (firmware 1.0.4: "Turn
// off Wi-Fi when suspend") and brings it back on the way up. On the TG5040
// family that means wpa_supplicant is stopped and started again and wlan0 goes
// down and up — which drops the default route even though the address stays.
// Usually the supplicant joins again by itself within a few seconds. When it is
// slow, this watcher nudges it, gently, because the firmware owns this radio and
// JukaHub must never fight its own reconnect:
//
//   - 2 s after the wake, if the supplicant is not COMPLETED: RECONNECT. The
//     supplicant only acts on it when it was told to disconnect; for one that
//     is already scanning or joining it changes nothing.
//   - 10 s after the wake, if it is still idle (DISCONNECTED, INACTIVE,
//     SCANNING): REASSOCIATE, a fresh scan-and-join. Never in the middle of a
//     handshake.
//   - 3 s after COMPLETED with no IPv4 address or no default route on the
//     interface: the DHCP client is asked again — SIGUSR1 to a udhcpc already
//     serving the interface (busybox's "renew now"), else the one-shot client a
//     join runs (wifiRunDHCP).
//
// A supplicant that is not running at all is reported, not started: the
// firmware starts its own on the way up, and two on one radio fight over it.
// The Wi-Fi page can still start one, as it always could (ensureWifiBackend).
//
// Status is read over the supplicant's control socket where there is one (no
// fork per second), once a second for the minute after a wake and only until
// the internet answers. The supplicant's ATTACH events are not used: the
// firmware stops and restarts the supplicant around every sleep, so a monitor
// socket would die with it each time, and the minute after a wake is exactly
// when polling costs nothing that matters.
//
// It runs on the TrimUI only. On Windows (and a Linux desktop with a network
// manager of its own) the operating system reconnects, and JukaHub only probes
// fast (netmon.go).

import (
	"path"
	"strings"
	"sync"
	"time"
)

const (
	wifiResumePoll        = time.Second
	wifiResumeReconnectAt = 2 * time.Second
	wifiResumeReassocAt   = 10 * time.Second
	wifiResumeDHCPAfter   = 5 * time.Second
	wifiResumeMissingAt   = 20 * time.Second
	// wifiResumeBootQuiet is how long after JukaHub starts a "wake" never
	// touches the radio: a stall while the device boots reads like a short
	// sleep, and the supplicant is still on its first scan then.
	wifiResumeBootQuiet = 30 * time.Second
	// wifiStateDisabled is the supplicant's word for an interface that is down.
	wifiStateDisabled = "INTERFACE_DISABLED"
)

// wifiResume is the watcher of the latest wake. gen tells a watcher a newer
// wake has started its own; actions is what it did, for the "back online" log
// line (netmon.go).
var wifiResume struct {
	mu      sync.Mutex
	gen     uint64
	ran     bool
	actions []string
}

// wifiResumeKicks reports whether the radio is JukaHub's to nudge: a TrimUI
// whose Wi-Fi the firmware drives through wpa_supplicant.
func wifiResumeKicks() bool {
	return !winWifiActive() && IsTrimuiDevice() && startupElapsed() >= wifiResumeBootQuiet
}

// wifiResumeAfterWake starts the watcher for a wake (wake_resume.go).
func wifiResumeAfterWake() {
	// The supplicant may be a new process with a new socket, and the cached
	// answers are from before the sleep, or from a read while it was gone.
	resetWiFiInterfaceCache()
	kicks := wifiResumeKicks()
	wifiResume.mu.Lock()
	wifiResume.gen++
	gen := wifiResume.gen
	wifiResume.ran = kicks
	wifiResume.actions = nil
	wifiResume.mu.Unlock()
	if kicks {
		go wifiResumeWatch(gen)
	}
}

// wifiResumeCurrent reports whether gen is still the latest wake.
func wifiResumeCurrent(gen uint64) bool {
	wifiResume.mu.Lock()
	defer wifiResume.mu.Unlock()
	return wifiResume.gen == gen
}

// wifiResumeNote records what the watcher of wake gen did.
func wifiResumeNote(gen uint64, action string) {
	if action == "" {
		return
	}
	wifiResume.mu.Lock()
	if wifiResume.gen == gen {
		wifiResume.actions = append(wifiResume.actions, action)
	}
	wifiResume.mu.Unlock()
}

// wifiResumeSummary is the bracketed part of the log line after a wake: what
// was done to the radio, "no kick needed" when it came back by itself, or
// "probe only" where JukaHub does not touch the radio.
func wifiResumeSummary() string {
	wifiResume.mu.Lock()
	defer wifiResume.mu.Unlock()
	switch {
	case len(wifiResume.actions) > 0:
		return strings.Join(wifiResume.actions, ", ")
	case wifiResume.ran:
		return "no kick needed"
	}
	return "probe only"
}

// wifiResumeWatch follows the supplicant for the minute after a wake and
// nudges it when it is slow (see the top of the file). It stops as soon as the
// internet answers (the wake window closes) or a newer wake takes over.
func wifiResumeWatch(gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("wifi", "wake watcher stopped: %v", r)
		}
	}()
	start := time.Now()
	ticker := time.NewTicker(wifiResumePoll)
	defer ticker.Stop()
	var (
		completedAt, lastReset                                 time.Time
		lastState                                              string
		reconnected, reassociated, renewed, told, radioChecked bool
	)
	for range ticker.C {
		elapsed := time.Since(start)
		if !wifiResumeCurrent(gen) || !netReconnecting() || elapsed >= netWakeWindowLen {
			return
		}
		st, iface, ok := wifiResumeStatus()
		if !ok {
			completedAt, lastState = time.Time{}, ""
			if !told && elapsed >= wifiResumeMissingAt {
				told = true
				if iface == "" {
					logWarn("wifi", "no Wi-Fi interface %.0f s after the wake — the firmware has not brought the driver back", elapsed.Seconds())
					wifiResumeNote(gen, "no interface")
				} else {
					logWarn("wifi", "wpa_supplicant is not answering on %s %.0f s after the wake — the firmware has not started it again", iface, elapsed.Seconds())
					wifiResumeNote(gen, "supplicant not running")
				}
			}
			// It comes back with a new socket: look for it afresh now and
			// then, not once a second.
			if time.Since(lastReset) >= 3*time.Second {
				lastReset = time.Now()
				resetWiFiInterfaceCache()
			}
			continue
		}
		if st.State != lastState {
			logDebug("wifi", "after wake: %s at %.1f s", st.State, elapsed.Seconds())
			lastState = st.State
		}
		if st.State == wifiCompleteState {
			if completedAt.IsZero() {
				completedAt = time.Now()
				// Joined: the internet is probably one DHCP answer away, so the
				// probe looks now rather than at its next turn.
				netNudge()
			}
			if !renewed && time.Since(completedAt) >= wifiResumeDHCPAfter && !wifiIfaceRouted(iface) {
				renewed = true
				wifiResumeNote(gen, wifiRenewLease(iface))
				netNudge()
			}
			continue
		}
		completedAt = time.Time{}
		if st.State == wifiStateDisabled {
			// The interface is down: the firmware's to bring up, or Wi-Fi
			// was switched off.
			continue
		}
		if !radioChecked {
			radioChecked = true
			if on, known := wifiRadioState(); known && !on {
				logInfo("wifi", "after wake: the radio is switched off — leaving it alone")
				return
			}
		}
		if !reconnected && elapsed >= wifiResumeReconnectAt {
			reconnected = true
			if wifiResumeCommand("reconnect") {
				wifiResumeNote(gen, "reconnect sent")
			}
		}
		if !reassociated && elapsed >= wifiResumeReassocAt && wifiResumeIdle(st.State) {
			reassociated = true
			if wifiResumeCommand("reassociate") {
				wifiResumeNote(gen, "reassociate sent")
			}
		}
	}
}

// wifiResumeStatus reads the supplicant's state: over its control socket when
// the socket is where discovery found it (no fork), else through wpa_cli when
// the socket's directory is unknown — the CLI may know a path the guesses do
// not. A socket that is missing from a known directory means the supplicant is
// not running, and no CLI is forked to be told so.
func wifiResumeStatus() (wifiStatus, string, bool) {
	iface := wifiInterface()
	if iface == "" {
		return wifiStatus{}, "", false
	}
	dir := wifiCtrlDirCached(iface)
	if dir != "" {
		if !wpaCtrlSocketExists(dir, iface) {
			return wifiStatus{}, iface, false
		}
		out, err := wpaCtrlRequest(wpaCtrlSocketPath(dir, iface), "STATUS", wifiProbeTimeout)
		if st := parseWPAStatus(out); err == nil && st.State != "" {
			return st, iface, true
		}
		return wifiStatus{}, iface, false
	}
	if !wifiToolExists("wpa_cli") {
		return wifiStatus{}, iface, false
	}
	out, err := wifiWPA("status")
	if st := parseWPAStatus(out); err == nil && !wifiCommandFailed(out) && st.State != "" {
		return st, iface, true
	}
	return wifiStatus{}, iface, false
}

// wifiResumeCommand sends one command (wifiWPA picks wpa_cli or the socket) and
// reports whether the supplicant took it.
func wifiResumeCommand(cmd string) bool {
	out, err := wifiWPA(cmd)
	if err != nil || wifiCommandFailed(out) {
		reason := firstLine(strings.TrimSpace(out))
		if reason == "" && err != nil {
			reason = err.Error()
		}
		logDebug("wifi", "after wake: %s not taken: %s", cmd, reason)
		return false
	}
	logInfo("wifi", "after wake: %s sent", cmd)
	return true
}

// wifiResumeIdle reports whether a supplicant state is one where nothing is
// being joined, so a REASSOCIATE interrupts nothing. Pure.
func wifiResumeIdle(state string) bool {
	switch state {
	case "DISCONNECTED", "INACTIVE", "SCANNING":
		return true
	}
	return false
}

// procRouteHasDefault reports whether the kernel's IPv4 route table, as
// /proc/net/route prints it, has a default route through iface. The table is
// tab-separated under a header: Iface, Destination, Gateway, Flags, RefCnt,
// Use, Metric, Mask, … with addresses in hex; a default route has destination
// and mask 00000000. Pure.
func procRouteHasDefault(table, iface string) bool {
	for i, line := range strings.Split(table, "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		if f[0] == iface && f[1] == "00000000" && f[7] == "00000000" {
			return true
		}
	}
	return false
}

// udhcpcServes reports whether a process command line (argv, from
// /proc/<pid>/cmdline) is busybox's udhcpc for iface: "udhcpc -i wlan0 …",
// "busybox udhcpc -iwlan0 …" or "--interface=wlan0". Pure.
func udhcpcServes(argv []string, iface string) bool {
	if len(argv) == 0 || iface == "" {
		return false
	}
	args := argv[1:]
	switch path.Base(argv[0]) {
	case "udhcpc":
	case "busybox":
		if len(args) == 0 || path.Base(args[0]) != "udhcpc" {
			return false
		}
		args = args[1:]
	default:
		return false
	}
	for i, a := range args {
		switch a {
		case "-i", "--interface":
			if i+1 < len(args) && args[i+1] == iface {
				return true
			}
		case "-i" + iface, "--interface=" + iface:
			return true
		}
	}
	return false
}
