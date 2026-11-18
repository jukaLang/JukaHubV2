package main

// wifi_keeper.go — keeping a saved Wi-Fi network joined on the TrimUI.
//
// On the stock TrimUI firmware (and CrossMix, which keeps it) the Wi-Fi
// logic that turns "associated" into "online" is not in wpa_supplicant: it is
// Allwinner's wifimanager library inside MainUI, whose event thread runs the
// DHCP client when the supplicant reports CONNECTED and re-selects a network
// when the link drops. JukaHub stops MainUI, so a join the supplicant makes on
// its own — at boot, when the router comes back, after a wake — ended at
// COMPLETED with no address, and nothing re-selected a saved network that the
// supplicant had given up on (temporarily disabled, or every entry marked
// disabled=1 by an earlier select_network + save_config). Only the Wi-Fi page
// ran DHCP, which is why connecting "had to be done manually".
//
// The keeper does that job, gently:
//
//   - joined (COMPLETED) but no IPv4 address or no default route on the radio
//     for a few seconds: ask DHCP — SIGUSR1 to a udhcpc already serving the
//     interface, else the one-shot client a join runs (wifiRunDHCP) — with
//     back-off;
//   - not joined for 15 s and the supplicant idle (DISCONNECTED, INACTIVE,
//     SCANNING): pick the strongest saved network in the last scan and
//     select_network it (the other saved networks are enabled again straight
//     away, and the config file is never written). None in range: ask for a
//     scan, so the supplicant joins by itself when one appears. Attempts back
//     off from 15 s to 5 min. A network the supplicant has TEMP-DISABLED is
//     left to it, and one that failed twice (a changed password) is left
//     until the user picks a network or the handheld wakes;
//   - no supplicant answering for 20 s (well after boot): start it the way the
//     Wi-Fi page does (ensureWifiBackend), at most three times per wake,
//     unless the radio is switched off, one is running but not answering, or
//     system.json has Wi-Fi off and Wi-Fi was not switched on in JukaHub;
//   - joined with a route but no internet for minutes on an old lease: ask
//     DHCP again (the one-shot udhcpc never renews).
//
// It stands aside while a join from the Wi-Fi page is waiting or any Wi-Fi
// page operation runs (wifiUserOpBegin; wifiOpMu makes an attempt one step
// against them), for a minute after, after the user pressed Disconnect (until
// they join again), and while the wake watcher (wifi_resume.go) runs. Status is
// read over the supplicant's socket where there is one (no fork): every 5–15 s
// while offline, every 30 s while online. Every attempt and why it failed is
// logged with the "wifi" tag (the Logs screen and the bug report; warnings
// also land in errors.txt next to the app).
//
// It runs on the TrimUI only: Windows and desktop Linux have a network manager
// of their own.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	wifiKeeperFirstLook   = 8 * time.Second
	wifiKeeperBusyPoll    = 5 * time.Second
	wifiKeeperQuietPoll   = 30 * time.Second
	wifiKeeperIdleGrace   = 15 * time.Second
	wifiKeeperJoinWait    = 30 * time.Second
	wifiKeeperDHCPGrace   = 4 * time.Second
	wifiKeeperStartGrace  = 20 * time.Second
	wifiKeeperBootQuiet   = 25 * time.Second
	wifiKeeperHandsOff    = time.Minute
	wifiKeeperBackoffBase = 15 * time.Second
	wifiKeeperBackoffMax  = 5 * time.Minute
	wifiKeeperMaxStarts   = 3
	wifiKeeperTrailMax    = 8
	// wifiKeeperMaxFails is how many attempts at one network may fail before
	// the keeper leaves it until the user acts or the handheld wakes: a
	// changed password must not be retried for ever.
	wifiKeeperMaxFails = 2
	// wifiKeeperNoRouteTries is how many DHCP tries a joined radio that has
	// an address but no default route gets before it is left alone.
	wifiKeeperNoRouteTries = 3
	// wifiKeeperOfflinePoll is the longest look interval while offline and
	// backing off: a join the supplicant makes by itself still gets its DHCP
	// soon.
	wifiKeeperOfflinePoll = 15 * time.Second
	// A link with an address and a route but no internet for
	// wifiKeeperStaleNet, on a lease at least wifiKeeperLeaseAge old, asks
	// DHCP again: the one-shot udhcpc never renews, so the lease can run out.
	wifiKeeperStaleNet = 3 * time.Minute
	wifiKeeperLeaseAge = 30 * time.Minute
)

var (
	wifiKeeperStarted atomic.Bool
	// wifiKeeperKickCh asks the keeper to look now with a fresh back-off (a
	// wake). It holds one request; more add nothing.
	wifiKeeperKickCh = make(chan struct{}, 1)
	// wifiKeeperUserAt is when the user last joined, disconnected or forgot a
	// network (UnixNano, 0 for never); wifiKeeperUserOff is set by Disconnect
	// and cleared by the next join. It lives in memory only: after a restart
	// of JukaHub the keeper rejoins.
	wifiKeeperUserAt  atomic.Int64
	wifiKeeperUserOff atomic.Bool
	// wifiResumeRunning counts the wake watchers still running
	// (wifi_resume.go); the keeper waits while one is.
	wifiResumeRunning atomic.Int32
	// wifiUserOps counts the Wi-Fi page operations running now
	// (wifiUserOpBegin); the keeper waits while one is.
	wifiUserOps atomic.Int32
	// wifiKeeperRadioOnByUser is set when Wi-Fi was switched on in JukaHub
	// this session: that outranks system.json's Wi-Fi-off.
	wifiKeeperRadioOnByUser atomic.Bool
)

// wifiOpMu makes a keeper attempt (list, select, enable) one step against the
// user's operations: one that begins waits for the attempt in flight, and no
// attempt starts while one runs.
var wifiOpMu sync.Mutex

// wifiUserOpBegin marks a Wi-Fi page operation (join, select, reconnect,
// disconnect, forget) as running and starts the keeper's hands-off minute; the
// returned func ends it, and the minute counts again from there. Use as
// `defer wifiUserOpBegin()()`.
func wifiUserOpBegin() func() {
	wifiOpMu.Lock()
	wifiUserOps.Add(1)
	wifiOpMu.Unlock()
	wifiKeeperUserAt.Store(time.Now().UnixNano())
	return func() {
		wifiKeeperUserAt.Store(time.Now().UnixNano())
		wifiUserOps.Add(-1)
	}
}

// wifiKeeperRadioOn records that Wi-Fi was switched on in JukaHub (the Wi-Fi
// page, the Home tile): a Disconnect from before is over, system.json's
// Wi-Fi-off no longer holds the keeper back, and it looks now with a fresh
// back-off. No hands-off minute: the supplicant joins by itself, and that join
// needs its DHCP.
func wifiKeeperRadioOn() {
	wifiKeeperUserOff.Store(false)
	wifiKeeperRadioOnByUser.Store(true)
	wifiKeeperKick()
}

// wifiKeeperYield reports whether someone else drives the radio right now: a
// join waiting, a Wi-Fi page operation or its hands-off minute, the wake
// watcher, or the hotspot (hotspot.go), which holds the radio in AP mode.
func wifiKeeperYield() bool {
	return wifiJoinInProgress() || wifiUserOps.Load() > 0 || wifiKeeperHandsOffNow() || wifiResumeRunning.Load() > 0 || hotspotHoldsRadio()
}

// wifiKeeperFirmwareAllows reports whether the firmware's Wi-Fi switch lets
// the keeper act: system.json has Wi-Fi on, or it was switched on in JukaHub.
func wifiKeeperFirmwareAllows() bool {
	return wifiKeeperRadioOnByUser.Load() || wifiFirmwareWantsWiFi()
}

// wifiKeeperNote is the keeper's latest line, for the diagnostics sheet.
var wifiKeeperNote struct {
	mu   sync.Mutex
	text string
	at   time.Time
}

func init() {
	// After the first frame, on the main thread (startup.go): the device and
	// safe mode are known there; the keeper runs on its own goroutine after.
	deferStartupTask("wifi-keeper", wifiKeeperStart)
}

// wifiKeeperStart starts the keeper once, on a TrimUI only.
func wifiKeeperStart() {
	if winWifiActive() || !IsTrimuiDevice() || !safeModeAllows(SafeFeatureNetwork) {
		return
	}
	if !wifiKeeperStarted.CompareAndSwap(false, true) {
		return
	}
	go wifiKeeperRun()
}

// wifiKeeperUserAction records that the user drove the radio from the Wi-Fi
// page; disconnected is true for Disconnect, which the keeper then respects.
func wifiKeeperUserAction(disconnected bool) {
	wifiKeeperUserAt.Store(time.Now().UnixNano())
	wifiKeeperUserOff.Store(disconnected)
}

// wifiKeeperHandsOffNow reports whether the user acted within the last minute.
func wifiKeeperHandsOffNow() bool {
	at := wifiKeeperUserAt.Load()
	return at != 0 && time.Since(time.Unix(0, at)) < wifiKeeperHandsOff
}

// wifiKeeperKick asks the keeper to look again now (wifiResumeAfterWake).
func wifiKeeperKick() {
	select {
	case wifiKeeperKickCh <- struct{}{}:
	default:
	}
}

// wifiKeeperLastNote is the keeper's latest line and when it was written.
func wifiKeeperLastNote() (string, time.Time) {
	wifiKeeperNote.mu.Lock()
	defer wifiKeeperNote.mu.Unlock()
	return wifiKeeperNote.text, wifiKeeperNote.at
}

// wifiKeeperLog logs one keeper line (warn for failures) and keeps it for the
// diagnostics sheet.
func wifiKeeperLog(warn bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	wifiKeeperNote.mu.Lock()
	wifiKeeperNote.text, wifiKeeperNote.at = msg, time.Now()
	wifiKeeperNote.mu.Unlock()
	if warn {
		logWarn("wifi", "keeper: %s", msg)
		return
	}
	logInfo("wifi", "keeper: %s", msg)
}

// wifiKeeperBackoff is the wait after the n-th attempt: 15 s, 30 s, 1 min,
// 2 min, 4 min, then 5 min. Pure.
func wifiKeeperBackoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := wifiKeeperBackoffBase
	for i := 1; i < n && d < wifiKeeperBackoffMax; i++ {
		d *= 2
	}
	if d > wifiKeeperBackoffMax {
		d = wifiKeeperBackoffMax
	}
	return d
}

// wifiKeeperPick chooses the saved network to join from the last scan: the
// strongest one in range that is not disabled — or, when every saved network
// is disabled (what an earlier select_network + save_config leaves), the
// strongest saved one in range. Left out, and counted in held: a network the
// supplicant has TEMP-DISABLED after failed joins (it retries those itself,
// and select_network would clear its back-off), and one whose attempts failed
// wifiKeeperMaxFails times (failed, by SSID). Pure.
func wifiKeeperPick(saved []savedNetwork, scanned []wifiNetwork, failed map[string]int) (pick wifiNetwork, ok bool, held int) {
	allDisabled := true
	disabled := make(map[int]bool, len(saved))
	tempDisabled := make(map[int]bool, len(saved))
	for _, s := range saved {
		if wifiNetworkDisabled(s) {
			disabled[s.ID] = true
		} else {
			allDisabled = false
		}
		if strings.Contains(s.Flags, "[TEMP-DISABLED]") {
			tempDisabled[s.ID] = true
		}
	}
	for _, n := range mergeWiFiNetworks(scanned, saved, wifiStatus{}) {
		if !n.Saved || (!allDisabled && disabled[n.SaveID]) {
			continue
		}
		if tempDisabled[n.SaveID] || failed[n.SSID] >= wifiKeeperMaxFails {
			held++
			continue
		}
		if !ok {
			pick, ok = n, true
		}
	}
	return pick, ok, held
}

// wifiFirmwareWantsWiFi reports whether the firmware's own Wi-Fi switch
// (system.json "wifi") is on. A missing file or key counts as on.
func wifiFirmwareWantsWiFi() bool {
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		return true
	}
	v, ok, _ := trimuiSystemJSONIntFrom(data, "wifi", 1)
	return !ok || v != 0
}

// wifiKeeperState is the keeper's memory between looks. Only its goroutine
// touches it.
type wifiKeeperState struct {
	downSince    time.Time // first look that was not COMPLETED (zero while joined)
	completedAt  time.Time
	good         bool // joined with an address and a route at the last look
	attempts     int
	nextAttempt  time.Time
	pendingSSID  string // the network the last attempt selected
	pendingAt    time.Time
	trail        []string // states seen since that attempt
	dhcpTries    int
	nextDHCP     time.Time
	missingSince time.Time
	starts       int
	nextStart    time.Time
	told         map[string]bool
	failed       map[string]int // failed attempts by SSID, since the last user action or wake
	userAt       int64          // wifiKeeperUserAt when failed was last cleared
	radioOff     bool           // the radio was seen switched off
	leaseAt      time.Time      // when the link last came up or DHCP last answered
	offlineSince time.Time      // routed, but the internet probe has failed since
	staleAsked   bool           // staleLease asked DHCP in this offline spell
}

// wifiKeeperRun is the keeper's loop.
func wifiKeeperRun() {
	logInfo("wifi", "keeper started: saved networks are rejoined and DHCP follows every join")
	var k wifiKeeperState
	timer := time.NewTimer(wifiKeeperFirstLook)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-wifiKeeperKickCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			k.fresh()
		}
		timer.Reset(k.safeStep(time.Now()))
	}
}

// safeStep is step with a panic logged and turned into a quiet wait, so one
// bad look does not end the keeper for the session.
func (k *wifiKeeperState) safeStep(now time.Time) (wait time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("wifi", "keeper: look failed: %v", r)
			wait = wifiKeeperQuietPoll
		}
	}()
	return k.step(now)
}

// fresh forgets the back-off and the one-time lines: a wake is a new start.
func (k *wifiKeeperState) fresh() {
	*k = wifiKeeperState{}
}

// once logs a line the first time key is seen since the last fresh start or
// the last good link.
func (k *wifiKeeperState) once(key string, warn bool, format string, args ...any) {
	if k.told == nil {
		k.told = map[string]bool{}
	}
	if k.told[key] {
		return
	}
	k.told[key] = true
	wifiKeeperLog(warn, format, args...)
}

// step is one look; it returns the wait before the next one.
func (k *wifiKeeperState) step(now time.Time) time.Duration {
	if at := wifiKeeperUserAt.Load(); at != k.userAt {
		// The user acted: networks that failed get another chance.
		k.userAt, k.failed = at, nil
	}
	if wifiKeeperYield() {
		// A join from the Wi-Fi page, the user, or the wake watcher is
		// driving the radio right now; an attempt of ours is superseded.
		k.good, k.completedAt = false, time.Time{}
		k.pendingSSID, k.trail = "", nil
		return wifiKeeperBusyPoll
	}
	st, iface, ok := wifiResumeStatus()
	if iface == "" {
		// The graces start over when the radio is back.
		k.good, k.downSince, k.missingSince = false, time.Time{}, time.Time{}
		k.once("no-iface", false, "no Wi-Fi interface — the radio is off or the firmware has not loaded the driver")
		return wifiKeeperQuietPoll
	}
	if !ok {
		return k.supplicantMissing(now, iface)
	}
	k.missingSince = time.Time{}
	if st.State == wifiCompleteState {
		return k.joined(now, iface, st)
	}
	if !k.completedAt.IsZero() {
		// The radio left the network it was on (a join is nudged in joined):
		// the probe looks now rather than at its next turn (netmon.go).
		netNudge()
	}
	k.good, k.completedAt = false, time.Time{}
	if st.State == wifiStateDisabled {
		k.downSince = time.Time{}
		k.once("if-down", false, "wpa_supplicant reports %s down (INTERFACE_DISABLED) — left to the radio switch", iface)
		return wifiKeeperQuietPoll
	}
	if k.downSince.IsZero() {
		k.downSince = now
	}
	if k.pendingSSID != "" && (len(k.trail) == 0 || k.trail[len(k.trail)-1] != st.State) && len(k.trail) < wifiKeeperTrailMax {
		k.trail = append(k.trail, st.State)
	}
	return k.idle(now, st)
}

// joined handles COMPLETED: fine when the radio has an address and the default
// route, else DHCP is asked, with back-off.
func (k *wifiKeeperState) joined(now time.Time, iface string, st wifiStatus) time.Duration {
	if k.completedAt.IsZero() {
		// A new association: DHCP starts over, not where the last network's
		// back-off left it.
		k.completedAt = now
		k.dhcpTries, k.nextDHCP = 0, time.Time{}
		delete(k.told, "no-route")
		netNudge()
	}
	if k.pendingSSID != "" {
		wifiKeeperLog(false, "attempt %d joined %q after %.0f s", k.attempts, st.SSID, now.Sub(k.pendingAt).Seconds())
		k.pendingSSID, k.trail = "", nil
	}
	delete(k.failed, st.SSID)
	k.downSince = time.Time{}
	if wifiIfaceRouted(iface) {
		if !k.good {
			k.good = true
			k.leaseAt, k.offlineSince = now, time.Time{}
			ip := st.IP
			if ip == "" {
				ip = "an address"
			}
			wifiKeeperLog(false, "online on %q with %s", st.SSID, ip)
		}
		k.attempts, k.nextAttempt = 0, time.Time{}
		k.dhcpTries, k.nextDHCP = 0, time.Time{}
		k.starts, k.nextStart = 0, time.Time{}
		k.told = nil
		k.staleLease(now, iface, st)
		return wifiKeeperQuietPoll
	}
	k.good = false
	if now.Sub(k.completedAt) < wifiKeeperDHCPGrace || now.Before(k.nextDHCP) {
		return wifiKeeperBusyPoll
	}
	if wifiKeeperYield() {
		// The user started something since this look began.
		return wifiKeeperBusyPoll
	}
	if st.IP != "" && k.dhcpTries >= wifiKeeperNoRouteTries {
		// An address but still no default route: a network without a
		// gateway (a LAN-only router, a phone hotspot with data off). Every
		// one-shot udhcpc drops the address for seconds, so this connection
		// is left as it is.
		k.once("no-route", false, "%q gave %s an address (%s) but no default route after %d DHCP tries — left alone (a network without a gateway)",
			st.SSID, iface, st.IP, k.dhcpTries)
		return wifiKeeperQuietPoll
	}
	k.dhcpTries++
	k.nextDHCP = now.Add(wifiKeeperBackoff(k.dhcpTries))
	var result string
	warn := false
	if k.dhcpTries == 1 {
		// The firmware's own client keeps its job when one is running.
		result = wifiRenewLease(iface)
	} else if wifiSignalRenew(iface) {
		// A udhcpc still waiting for a lease is asked again, not joined by a
		// second one.
		result = "asked the running DHCP client to renew"
	} else if ip, err := wifiRunDHCP(iface); err != nil {
		result, warn = "DHCP failed: "+firstLine(err.Error()), true
	} else {
		result = "DHCP answered with " + ip
	}
	wifiKeeperLog(warn, "joined %q but %s had no address or default route for %.0f s — DHCP try %d: %s (next try in %s)",
		st.SSID, iface, now.Sub(k.completedAt).Seconds(), k.dhcpTries, result, wifiKeeperBackoff(k.dhcpTries))
	netNudge()
	return wifiKeeperBusyPoll
}

// staleLease asks DHCP again for a link that has an address and a route but
// has not reached the internet for wifiKeeperStaleNet, on a lease at least
// wifiKeeperLeaseAge old: the one-shot udhcpc a join runs never renews, so the
// lease may have run out and the router given the address to someone else.
// A working link is never touched (a new lease briefly drops the address).
func (k *wifiKeeperState) staleLease(now time.Time, iface string, st wifiStatus) {
	if IsOnline() {
		k.offlineSince, k.staleAsked = time.Time{}, false
		return
	}
	if k.staleAsked {
		// Once per offline spell: a router whose internet is down is not
		// fixed by a new lease, and every one-shot udhcpc drops the address
		// for seconds (LAN netplay, Companion, Remote Play would stutter).
		return
	}
	if k.offlineSince.IsZero() {
		k.offlineSince = now
		return
	}
	offline, age := now.Sub(k.offlineSince), now.Sub(k.leaseAt)
	if offline < wifiKeeperStaleNet || age < wifiKeeperLeaseAge {
		return
	}
	k.leaseAt, k.offlineSince, k.staleAsked = now, time.Time{}, true
	wifiKeeperLog(false, "%q has a route but no internet for %s, on a lease from %s ago — DHCP: %s",
		st.SSID, offline.Round(time.Second), age.Round(time.Minute), wifiRenewLease(iface))
	netNudge()
}

// idle handles every state but COMPLETED and INTERFACE_DISABLED.
func (k *wifiKeeperState) idle(now time.Time, st wifiStatus) time.Duration {
	if wifiKeeperUserOff.Load() {
		k.once("user-off", false, "Wi-Fi was disconnected on the Wi-Fi page — not rejoining until a network is chosen there")
		return wifiKeeperQuietPoll
	}
	if k.pendingSSID != "" {
		if now.Sub(k.pendingAt) < wifiKeeperJoinWait {
			return wifiKeeperBusyPoll
		}
		hint := ""
		for _, s := range k.trail {
			if wifiInHandshake(s) {
				hint = " — the handshake failed, so the saved password may be wrong"
				break
			}
		}
		if hint == "" && wifiKeeperTempDisabled(k.pendingSSID) {
			// The handshake is often over between two looks; the
			// supplicant's own back-off says the same.
			hint = " — wpa_supplicant marked it temporarily disabled (wrong password or weak signal)"
		}
		if k.failed == nil {
			k.failed = map[string]int{}
		}
		k.failed[k.pendingSSID]++
		n := k.failed[k.pendingSSID]
		if n >= wifiKeeperMaxFails {
			hint += fmt.Sprintf(" — %d failures, not tried again until a network is chosen on the Wi-Fi page or the handheld wakes", n)
		}
		// Warned once per network; repeats are information.
		wifiKeeperLog(n == 1, "attempt %d: %q did not join within %.0f s (states: %s)%s",
			k.attempts, k.pendingSSID, wifiKeeperJoinWait.Seconds(), strings.Join(k.trail, " → "), hint)
		k.pendingSSID, k.trail = "", nil
	}
	if now.Sub(k.downSince) < wifiKeeperIdleGrace || !wifiResumeIdle(st.State) {
		// Just dropped (the supplicant usually rejoins by itself), or the
		// supplicant is in the middle of a join of its own.
		return wifiKeeperBusyPoll
	}
	if now.Before(k.nextAttempt) {
		// Backing off: look less often the longer the wait, but often enough
		// that a join the supplicant makes by itself gets its DHCP soon.
		return min(max(time.Until(k.nextAttempt), wifiKeeperBusyPoll), wifiKeeperOfflinePoll)
	}
	if on, known := wifiRadioState(); known && !on {
		k.radioOff = true
		k.downSince = time.Time{}
		k.once("radio-off", false, "the Wi-Fi radio is switched off — leaving it alone")
		k.nextAttempt = now.Add(wifiKeeperQuietPoll)
		return wifiKeeperQuietPoll
	} else if known && k.radioOff {
		// Switched off, then on again in this session: that was JukaHub
		// (the firmware's menu is not running), so it outranks system.json.
		k.radioOff = false
		wifiKeeperRadioOnByUser.Store(true)
	}
	if !wifiKeeperFirmwareAllows() {
		k.once("fw-off-idle", false, "system.json has Wi-Fi off — not rejoining until Wi-Fi is switched on in JukaHub")
		k.nextAttempt = now.Add(wifiKeeperQuietPoll)
		return wifiKeeperQuietPoll
	}
	// One step against the user's operations (wifiOpMu): none starts while
	// this attempt runs, and none may be running when it starts.
	wifiOpMu.Lock()
	defer wifiOpMu.Unlock()
	if wifiKeeperYield() {
		return wifiKeeperBusyPoll
	}
	k.attempts++
	k.nextAttempt = now.Add(wifiKeeperBackoff(k.attempts))
	k.attempt(now, st)
	return wifiKeeperBusyPoll
}

// wifiKeeperTempDisabled reports whether the supplicant has a saved ssid in
// its TEMP-DISABLED back-off.
func wifiKeeperTempDisabled(ssid string) bool {
	saved, err := wifiReadSavedNetworks()
	if err != nil {
		return false
	}
	for _, s := range saved {
		if s.SSID == ssid && strings.Contains(s.Flags, "[TEMP-DISABLED]") {
			return true
		}
	}
	return false
}

// attempt is one auto-connect: select the best saved network in range, or ask
// for a scan when none is.
func (k *wifiKeeperState) attempt(now time.Time, st wifiStatus) {
	down := now.Sub(k.downSince).Round(time.Second)
	next := wifiKeeperBackoff(k.attempts)
	saved, err := wifiReadSavedNetworks()
	if err != nil {
		wifiKeeperLog(true, "attempt %d: could not list the saved networks: %s (next try in %s)", k.attempts, firstLine(err.Error()), next)
		return
	}
	if len(saved) == 0 {
		k.once("no-saved", false, "no saved networks — join one on the Wi-Fi page and it is rejoined from then on")
		return
	}
	scanned, _ := wifiReadScanResults()
	pick, ok, held := wifiKeeperPick(saved, scanned, k.failed)
	if !ok {
		// A fresh scan: the supplicant joins by itself when a saved network
		// shows up in it, and the next attempt reads it. Not through
		// wifiWPAChecked: a FAIL-BUSY (it is scanning already) is the usual
		// answer here and is not the diagnostics sheet's "Last error".
		note := ""
		if out, scanErr := wifiWPA("scan"); !wifiScanBusy(out) && (scanErr != nil || wifiCommandFailed(out)) {
			why := firstLine(strings.TrimSpace(out))
			if why == "" && scanErr != nil {
				why = firstLine(scanErr.Error())
			}
			note = "; scan refused: " + why
		}
		if held > 0 {
			note += fmt.Sprintf("; %d in range held back after failed joins", held)
		}
		wifiKeeperLog(false, "attempt %d: none of %d saved networks to join (%d visible, %s for %s)%s; next look in %s",
			k.attempts, len(saved), len(scanned), strings.ToLower(st.State), down, note, next)
		return
	}
	if _, err := wifiWPAChecked("select_network", strconv.Itoa(pick.SaveID)); err != nil {
		wifiKeeperLog(true, "attempt %d: select_network %d (%q) refused: %s (next try in %s)", k.attempts, pick.SaveID, pick.SSID, firstLine(err.Error()), next)
		return
	}
	// select_network disabled the others; they come back at once, and the
	// config file is not written.
	wifiReenableNetworks(saved, -1)
	k.pendingSSID, k.pendingAt, k.trail = pick.SSID, time.Now(), []string{st.State}
	wifiKeeperLog(false, "attempt %d: joining saved %q (%d dBm) — the supplicant was %s for %s",
		k.attempts, pick.SSID, pick.Signal, strings.ToLower(st.State), down)
}

// supplicantMissing handles a radio with no supplicant answering: after a grace
// (the firmware may be starting its own), start one the way the Wi-Fi page
// does, a few times at most.
func (k *wifiKeeperState) supplicantMissing(now time.Time, iface string) time.Duration {
	k.good, k.completedAt = false, time.Time{}
	if k.missingSince.IsZero() {
		k.missingSince = now
		// It may come back with a new socket somewhere else.
		resetWiFiInterfaceCache()
		return wifiKeeperBusyPoll
	}
	if now.Sub(k.missingSince) < wifiKeeperStartGrace || startupElapsed() < wifiKeeperBootQuiet || now.Before(k.nextStart) {
		return wifiKeeperBusyPoll
	}
	if k.starts >= wifiKeeperMaxStarts {
		k.once("start-gave-up", true, "wpa_supplicant still not answering on %s after %d starts — open the Wi-Fi page or switch Wi-Fi off and on to retry", iface, k.starts)
		return wifiKeeperQuietPoll
	}
	if on, known := wifiRadioState(); known && !on {
		// The grace starts over when the radio is back.
		k.radioOff, k.missingSince = true, time.Time{}
		k.once("radio-off", false, "the Wi-Fi radio is switched off — leaving it alone")
		return wifiKeeperQuietPoll
	} else if known && k.radioOff {
		// Off, then on again in this session: JukaHub's switch (see idle).
		k.radioOff = false
		wifiKeeperRadioOnByUser.Store(true)
	}
	if !wifiKeeperFirmwareAllows() {
		k.missingSince = time.Time{}
		k.once("fw-off", false, "wpa_supplicant is not running and system.json has Wi-Fi off — not starting it until Wi-Fi is switched on in JukaHub")
		return wifiKeeperQuietPoll
	}
	if pid := supplicantPIDFor(iface); pid > 0 {
		// Hung or still starting: a second one on the radio would fight it.
		k.once("hung", true, "wpa_supplicant (pid %d) runs on %s but does not answer — not starting a second one", pid, iface)
		return wifiKeeperQuietPoll
	}
	k.starts++
	k.nextStart = now.Add(wifiKeeperBackoff(k.starts + 1))
	wifiKeeperLog(false, "wpa_supplicant not answering on %s for %.0f s — starting it (try %d of %d)",
		iface, now.Sub(k.missingSince).Seconds(), k.starts, wifiKeeperMaxStarts)
	// The start claim is lifted inside ensureWifiBackend's lock, after its
	// own process check, so a start the Home tile or the Wi-Fi page is making
	// right now is not doubled.
	if !ensureWifiBackendRetry(true) {
		if wifiSupplicantAnswers() {
			// The tile, the page or the firmware won the race: fine.
			wifiKeeperLog(false, "wpa_supplicant on %s answers now (started elsewhere)", iface)
			resetWiFiInterfaceCache()
			k.missingSince = time.Time{}
			return wifiKeeperBusyPoll
		}
		why := "nothing to start"
		_, note := supplicantStartNote()
		switch {
		case strings.Contains(note, "not answering"):
			why = "one is " + note
		case wifiSupplicantConfigPath() == "":
			why = "no wpa_supplicant.conf on this image"
		case !wifiToolExists("wpa_supplicant"):
			why = "no wpa_supplicant binary on this image"
		}
		wifiKeeperLog(true, "could not start wpa_supplicant on %s: %s", iface, why)
	} else if _, note := supplicantStartNote(); note != "" {
		wifiKeeperLog(strings.Contains(note, "failed"), "wpa_supplicant on %s: %s", iface, note)
	}
	resetWiFiInterfaceCache()
	k.missingSince = time.Time{}
	return wifiKeeperBusyPoll
}
