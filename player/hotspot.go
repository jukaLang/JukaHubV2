package main

// hotspot.go — Tools → Hotspot: this handheld as a Wi-Fi network of its own.
//
// The stock TrimUI firmware has a hotspot (Settings → Network; it is how two
// handhelds play netplay with no router), and JukaHub now has the same switch,
// on the same machinery. The handheld's one radio does one job at a time, so
// while the hotspot is on the device's own Wi-Fi connection is off; turning it
// off brings the saved network back (the Wi-Fi keeper rejoins it).
//
// The backend (hotspot_linux.go) tries, in order:
//
//   - Allwinner's own tool, `softap_up <ssid> <psk> broadcast` / `softap_down`,
//     which the A133/A523 board support package the TrimUI firmware is built on
//     ships for exactly this (it runs hostapd and a DHCP server itself);
//   - otherwise the firmware's hostapd, driven by JukaHub: wpa_supplicant and
//     udhcpc step off the radio, hostapd gets a WPA2 config written under
//     hotspotRunDir, the radio takes hotspotIP, and a DHCP server hands out
//     addresses — busybox udhcpd, else dnsmasq, else the small one in this file.
//
// When neither softap_up nor hostapd exists the screen says so; nothing
// pretends to work. The network name and password default to the firmware's
// own hotspot (its hostapd config, when one is on the card), else
// "JukaHub-XXXX" from the radio's MAC and a random password, kept in the user
// config under underscore keys (out of cfgdoc's exported settings).
//
// Everything slow runs on one goroutine at a time (hotspotState.busy); the UI
// reads a snapshot under the mutex. The Wi-Fi keeper, the wake watcher, the
// supplicant start and the Wi-Fi page stand aside while the hotspot holds the
// radio (hotspotHoldsRadio); the Wi-Fi page says to turn it off in Tools →
// Hotspot, so nothing tears it down behind the user's back. A sleep turns it
// off and the wake gives the radio back (hotspotBeforeSleep/AfterSleep);
// quitting turns it off on its own teardown row (hotspotShutdownStepAdd).

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Custom keys. The leading underscore keeps them out of the exported settings
// (cfgdoc.go): the password is a secret, and both describe this device.
const (
	hotspotSSIDKey = "_hotspot_ssid"
	hotspotPassKey = "_hotspot_pass"
)

// The network the hotspot hands out. 192.168.43.x is Android's tethering
// subnet, so it rarely collides with the phone's own networks.
const (
	hotspotIP        = "192.168.43.1"
	hotspotPrefixLen = 24
	hotspotNetmask   = "255.255.255.0"
	hotspotPoolFirst = 10
	hotspotPoolLast  = 60
	hotspotChannel   = 6
	hotspotLeaseSecs = 12 * 60 * 60
)

// hotspotRunDir holds the configs, pid files and leases JukaHub writes. tmpfs
// on the handheld, so a reboot (which also ends the processes) clears it.
func hotspotRunDir() string { return filepath.Join(os.TempDir(), "jukahub-hotspot") }

// hotspotStockConfs are where a firmware's own hostapd config can live; the
// first that names an ssid gives the hotspot's defaults.
var hotspotStockConfs = []string{
	"/etc/hostapd.conf",
	"/etc/wifi/hostapd.conf",
	"/etc/hostapd/hostapd.conf",
	"/usr/trimui/etc/hostapd.conf",
	"/mnt/UDISK/hostapd.conf",
	"/tmp/hostapd.conf",
}

type hotspotPhase int32

const (
	hotspotOff hotspotPhase = iota
	hotspotStarting
	hotspotOn
	hotspotStopping
)

// Backends, as hotspotRun.method names them.
const (
	hotspotMethodSoftAP  = "softap"
	hotspotMethodHostapd = "hostapd"
)

// hotspotRun is what a start set up, so the stop undoes exactly that.
type hotspotRun struct {
	method string
	iface  string
	ip     string
	ssid   string
	// dhcp names the DHCP server: "udhcpd", "dnsmasq", "built-in" or "" (the
	// softap tool runs its own).
	dhcp string
	// addedIP is set when JukaHub put hotspotIP on the radio itself.
	addedIP bool
	// hadStation: wpa_supplicant served the radio before the start, so the
	// stop starts it again. radioWasOff: the start switched the radio on, so
	// the stop switches it off again. A failed start carries both too.
	hadStation  bool
	radioWasOff bool
}

// hotspotGiveBack says what a stop does with the radio afterwards.
type hotspotGiveBack int

const (
	// hotspotGiveBackPrior puts back what the start found: station mode, or
	// the radio off.
	hotspotGiveBackPrior hotspotGiveBack = iota
	// hotspotGiveBackNone leaves the radio alone (a restart, or the radio was
	// switched off under the hotspot).
	hotspotGiveBackNone
)

// hotspotClient is one device on the hotspot.
type hotspotClient struct {
	IP   string
	MAC  string
	Name string
}

// hotspotState is the switch, main thread and goroutines alike (under mu).
var hotspotState struct {
	mu    sync.Mutex
	phase hotspotPhase
	// busy is set while the start or stop goroutine runs; a second request
	// waits for the next press instead of racing it.
	busy    bool
	run     hotspotRun
	since   time.Time
	err     string // why the last start failed, or why it went off by itself
	note    string // a calm line: "Turned off for sleep"
	clients []hotspotClient
	rev     uint64
	// monitorGen ends a monitor loop from an earlier start.
	monitorGen uint64
	// carry holds what the first start of a restart found on the radio (a new
	// name or password restarts the hotspot), so the final stop still puts
	// it back: the second start finds the station already gone.
	carry    hotspotRun
	carrySet bool
	// stopAfterStart is set by a sleep that found a start still running: the
	// start turns itself off the moment it finishes (hotspotFinishStart).
	stopAfterStart bool
	// asleep is set from hotspotBeforeSleep to hotspotAfterSleep; sleepRun is
	// what a stop during that time took off the radio, given back at the wake.
	asleep    bool
	sleepRun  hotspotRun
	sleepHeld bool
}

// hotspotQuick is set by the quit teardown: kills wait less and the softap tool
// gets a shorter leash, so the row fits the teardown's budget.
var hotspotQuick atomic.Bool

// hotspotPhaseNow mirrors hotspotState.phase for lock-free readers: the keeper
// and the supplicant start ask on their own goroutines.
var hotspotPhaseNow atomic.Int32

func hotspotSetPhaseLocked(p hotspotPhase) {
	hotspotState.phase = p
	hotspotPhaseNow.Store(int32(p))
	hotspotState.rev++
}

// hotspotHoldsRadio reports whether the hotspot owns the radio right now:
// starting, on, or tearing down. Whatever would start wpa_supplicant or rejoin
// a network waits while it does.
func hotspotHoldsRadio() bool { return hotspotPhase(hotspotPhaseNow.Load()) != hotspotOff }

// hotspotSnapshot is what the screen and the tile draw from.
type hotspotSnapshot struct {
	phase   hotspotPhase
	busy    bool
	run     hotspotRun
	since   time.Time
	err     string
	note    string
	clients []hotspotClient
	rev     uint64
}

func hotspotCurrent() hotspotSnapshot {
	hotspotState.mu.Lock()
	defer hotspotState.mu.Unlock()
	return hotspotSnapshot{
		phase:   hotspotState.phase,
		busy:    hotspotState.busy,
		run:     hotspotState.run,
		since:   hotspotState.since,
		err:     hotspotState.err,
		note:    hotspotState.note,
		clients: append([]hotspotClient(nil), hotspotState.clients...),
		rev:     hotspotState.rev,
	}
}

// hotspotTileValue is the Tools tile's and the Settings row's one-liner.
func hotspotTileValue() string {
	s := hotspotCurrent()
	switch s.phase {
	case hotspotStarting:
		return "Starting…"
	case hotspotStopping:
		return "Turning off…"
	case hotspotOn:
		switch n := len(s.clients); n {
		case 0:
			return "On · no one joined yet"
		case 1:
			return "On · 1 device"
		default:
			return "On · " + strconv.Itoa(n) + " devices"
		}
	}
	if s.err != "" {
		return "Off · could not start"
	}
	return "Off"
}

// ── Starting and stopping ────────────────────────────────────────────────

// hotspotStartAsync turns the hotspot on with the stored name and password,
// and reports whether a start began. Main thread (it reads the config); the
// work runs on a goroutine.
func hotspotStartAsync(c *Config) bool {
	if !hotspotSupported() {
		ShowToast("The hotspot is for the handheld", ToastKindInfo)
		return false
	}
	ssid, pass := hotspotCredentials(c)
	hotspotState.mu.Lock()
	if hotspotState.busy || hotspotState.phase != hotspotOff {
		hotspotState.mu.Unlock()
		return false
	}
	hotspotState.busy = true
	hotspotState.err, hotspotState.note = "", ""
	hotspotState.clients = nil
	hotspotSetPhaseLocked(hotspotStarting)
	hotspotState.mu.Unlock()
	logInfo("hotspot", "starting %q", ssid)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("hotspot", "start failed: %v", r)
				hotspotFinishStart(hotspotRun{}, errors.New("the start crashed; see the log"))
			}
		}()
		run, err := hotspotBackendStart(ssid, pass)
		hotspotFinishStart(run, err)
	}()
	return true
}

func hotspotFinishStart(run hotspotRun, err error) {
	hotspotState.mu.Lock()
	hotspotState.busy = false
	if hotspotState.carrySet {
		run.hadStation = run.hadStation || hotspotState.carry.hadStation
		run.radioWasOff = run.radioWasOff || hotspotState.carry.radioWasOff
		if run.iface == "" {
			run.iface = hotspotState.carry.iface
		}
		hotspotState.carry, hotspotState.carrySet = hotspotRun{}, false
	}
	stopNow := hotspotState.stopAfterStart
	hotspotState.stopAfterStart = false
	if err != nil {
		hotspotState.err = firstLine(err.Error())
		hotspotState.run = hotspotRun{}
		hotspotSetPhaseLocked(hotspotOff)
		hotspotState.mu.Unlock()
		logWarn("hotspot", "could not start: %v", err)
		tbxPost(func() { ShowToast("Hotspot: "+firstLine(err.Error()), ToastKindWarning) })
		// Whatever the start took off the radio comes back (at the wake, if
		// the device went to sleep meanwhile).
		go hotspotHoldForWake(run)
		return
	}
	hotspotState.run = run
	hotspotState.since = time.Now()
	hotspotState.monitorGen++
	gen := hotspotState.monitorGen
	hotspotSetPhaseLocked(hotspotOn)
	hotspotState.mu.Unlock()
	logInfo("hotspot", "on: %q on %s at %s (%s, dhcp %s)", run.ssid, run.iface, run.ip, run.method, setOr(run.dhcp, "by the tool"))
	if stopNow {
		// A sleep began while this start ran: off again at once, and the
		// radio goes back at the wake.
		go func() {
			if hotspotBeginStop() {
				hotspotHoldForWake(hotspotStopNow("Turned off when the device slept", "", hotspotGiveBackNone))
			}
		}()
		return
	}
	tbxPost(func() { ShowToast("Hotspot on: "+run.ssid, ToastKindSuccess) })
	go hotspotMonitor(gen)
}

// hotspotStopAsync turns the hotspot off and gives the radio back to Wi-Fi.
// note, when set, is the calm line the screen shows afterwards.
func hotspotStopAsync(note string) {
	if !hotspotBeginStop() {
		return
	}
	go func() {
		hotspotStopNow(note, "", hotspotGiveBackPrior)
		tbxPost(func() { ShowToast("Hotspot off", ToastKindInfo) })
	}()
}

// hotspotBeginStop claims the stop; false when nothing is on or a start or
// stop is running.
func hotspotBeginStop() bool {
	hotspotState.mu.Lock()
	defer hotspotState.mu.Unlock()
	if hotspotState.busy || hotspotState.phase != hotspotOn {
		return false
	}
	hotspotState.busy = true
	hotspotState.monitorGen++
	hotspotSetPhaseLocked(hotspotStopping)
	return true
}

// hotspotStopNow tears the hotspot down on the calling goroutine (a claim from
// hotspotBeginStop is held), then deals with the radio as give says. errText,
// when set, is why it went off by itself; it is in place before the phase
// reads Off, so a new start cannot have it written over. It returns what the
// start had set up, for a caller that gives the radio back later.
func hotspotStopNow(note, errText string, give hotspotGiveBack) hotspotRun {
	hotspotState.mu.Lock()
	run := hotspotState.run
	hotspotState.mu.Unlock()
	func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("hotspot", "stop failed: %v", r)
			}
		}()
		hotspotBackendStop(run)
	}()
	hotspotState.mu.Lock()
	hotspotState.busy = false
	hotspotState.run = hotspotRun{}
	hotspotState.clients = nil
	hotspotState.note = note
	hotspotState.err = errText
	hotspotSetPhaseLocked(hotspotOff)
	hotspotState.mu.Unlock()
	if note != "" {
		logInfo("hotspot", "off: %s", note)
	} else {
		logInfo("hotspot", "off")
	}
	hotspotGiveBackRadio(run, give)
	return run
}

// hotspotGiveBackRadio puts the radio where give says after a stop or a
// failed start.
func hotspotGiveBackRadio(run hotspotRun, give hotspotGiveBack) {
	switch {
	case give == hotspotGiveBackNone:
	case run.hadStation:
		hotspotRestoreWiFi()
	case run.radioWasOff:
		logInfo("hotspot", "the radio was off before the hotspot; switching it off again")
		if err := setWifiEnabled(false); err != nil {
			logWarn("hotspot", "could not switch the radio off again: %v", err)
		}
	case run.iface != "":
		logInfo("hotspot", "Wi-Fi was not connected before the hotspot; leaving it off")
	}
}

// hotspotWaitIdle waits up to d for a start or stop in flight to finish.
func hotspotWaitIdle(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		hotspotState.mu.Lock()
		busy := hotspotState.busy
		hotspotState.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// hotspotStopSync is the synchronous stop for a restart: it waits out a start
// in flight, then tears down. Any goroutine but the main thread.
func hotspotStopSync(note string, give hotspotGiveBack) bool {
	if !hotspotHoldsRadio() {
		return false
	}
	hotspotWaitIdle(20 * time.Second)
	if !hotspotBeginStop() {
		return false
	}
	hotspotStopNow(note, "", give)
	return true
}

// hotspotBeforeSleep turns the hotspot off as the device goes to sleep
// (powersleep.go, with the screen already dark): the firmware drops the radio
// in suspend anyway, and a wake should come back to the saved network, not to
// a half-dead access point. Nothing is given back before the sleep — starting
// wpa_supplicant and a join would only hold the suspend up — the wake does it
// (hotspotAfterSleep). A start still running after a short wait turns itself
// off when it finishes.
func hotspotBeforeSleep() {
	hotspotState.mu.Lock()
	hotspotState.asleep = true
	hotspotState.mu.Unlock()
	if !hotspotHoldsRadio() {
		return
	}
	hotspotWaitIdle(3 * time.Second)
	if hotspotBeginStop() {
		hotspotHoldForWake(hotspotStopNow("Turned off when the device slept", "", hotspotGiveBackNone))
		return
	}
	hotspotState.mu.Lock()
	if hotspotState.busy && hotspotState.phase == hotspotStarting {
		hotspotState.stopAfterStart = true
		logInfo("hotspot", "a start is still running at sleep; it turns off when it finishes")
	}
	hotspotState.mu.Unlock()
}

// hotspotAfterSleep runs as the device wakes (powersleep.go): what the sleep's
// stop took off the radio comes back, on a goroutine.
func hotspotAfterSleep() {
	hotspotState.mu.Lock()
	hotspotState.asleep = false
	run, held := hotspotState.sleepRun, hotspotState.sleepHeld
	hotspotState.sleepRun, hotspotState.sleepHeld = hotspotRun{}, false
	hotspotState.mu.Unlock()
	if held {
		go hotspotGiveBackRadio(run, hotspotGiveBackPrior)
	}
}

// hotspotHoldForWake gives the radio back now, or — while the device sleeps —
// keeps run for hotspotAfterSleep.
func hotspotHoldForWake(run hotspotRun) {
	hotspotState.mu.Lock()
	if hotspotState.asleep {
		hotspotState.sleepRun, hotspotState.sleepHeld = run, true
		hotspotState.mu.Unlock()
		return
	}
	hotspotState.mu.Unlock()
	hotspotGiveBackRadio(run, hotspotGiveBackPrior)
}

// hotspotShutdownStepAdd puts the hotspot's own row on the quit teardown
// (shutdown.go) while one is up, beside the other first-group rows: inside the
// services row its teardown would be cut off by that row's 1.5 s.
func hotspotShutdownStepAdd(steps []shutdownStep) []shutdownStep {
	if !hotspotHoldsRadio() {
		return steps
	}
	at := 0
	for i, s := range steps {
		if s.group == 0 {
			at = i + 1
		}
	}
	step := shutdownStep{label: "Turning the hotspot off", group: 0, timeout: 3 * time.Second, fn: hotspotShutdownStep}
	out := make([]shutdownStep, 0, len(steps)+1)
	out = append(out, steps[:at]...)
	out = append(out, step)
	return append(out, steps[at:]...)
}

// hotspotShutdownStep is that row: a short wait for a start in flight, a quick
// teardown, and the firmware JukaHub hands back to gets its Wi-Fi the way it
// was before the hotspot. The row's timeout bounds it; wpa_supplicant
// daemonises early, so a cut-off wait for its socket loses nothing.
func hotspotShutdownStep() (string, error) {
	hotspotQuick.Store(true)
	hotspotWaitIdle(time.Second)
	if !hotspotBeginStop() {
		if hotspotHoldsRadio() {
			return "", errors.New("a start was still running")
		}
		return "already off", nil
	}
	hotspotStopNow("", "", hotspotGiveBackPrior)
	return "off, Wi-Fi given back", nil
}

func init() {
	// A crash or a kill while the hotspot was on leaves hostapd running from
	// our run dir; the next start tears that down and gives Wi-Fi back.
	deferStartupTask("hotspot-cleanup", func() { go hotspotStartupCleanup() })
}

// hotspotRestoreWiFi gives the radio back to wpa_supplicant and nudges the
// keeper, which rejoins the saved network (and runs DHCP after the join) —
// unless the user had pressed Disconnect, or the firmware has Wi-Fi off: the
// hotspot puts back what was there, it does not overrule those.
func hotspotRestoreWiFi() {
	if hotspotHoldsRadio() {
		return
	}
	resetWiFiInterfaceCache()
	ensureWifiBackendRetry(true)
	resetWiFiInterfaceCache()
	wifiKeeperKick()
	netNudge()
}

// hotspotMonitor watches a running hotspot: the access point still up, the
// radio not switched off (airplane mode), and who has joined.
func hotspotMonitor(gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("hotspot", "monitor stopped: %v", r)
		}
	}()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		hotspotState.mu.Lock()
		live := hotspotState.monitorGen == gen && hotspotState.phase == hotspotOn && !hotspotState.busy
		run := hotspotState.run
		hotspotState.mu.Unlock()
		if !live {
			return
		}
		if on, known := wifiRadioState(); known && !on {
			if hotspotBeginStop() {
				hotspotStopNow("Turned off: the Wi-Fi radio was switched off", "", hotspotGiveBackNone)
				tbxPost(func() { ShowToast("Hotspot off: the radio was switched off", ToastKindInfo) })
			}
			return
		}
		if !hotspotBackendAlive(run) {
			if hotspotBeginStop() {
				hotspotStopNow("", "the access point stopped by itself", hotspotGiveBackPrior)
				tbxPost(func() { ShowToast("Hotspot stopped by itself · Wi-Fi is coming back", ToastKindWarning) })
			}
			return
		}
		clients := hotspotBackendClients(run)
		hotspotState.mu.Lock()
		if hotspotState.monitorGen == gen && !hotspotSameClients(hotspotState.clients, clients) {
			hotspotState.clients = clients
			hotspotState.rev++
		}
		hotspotState.mu.Unlock()
	}
}

func hotspotSameClients(a, b []hotspotClient) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── Name and password ────────────────────────────────────────────────────

// hotspotCredentials is the stored name and password, choosing and saving the
// defaults the first time: the firmware's own hotspot's name when its config is
// on the card, else JukaHub-XXXX; its password only when it is not one of the
// shared factory ones (hotspotWeakPass), else a random one. Main thread.
func hotspotCredentials(c *Config) (ssid, pass string) {
	if c != nil {
		ssid, _ = customString(c.Variables.Custom, hotspotSSIDKey)
		pass, _ = customString(c.Variables.Custom, hotspotPassKey)
	}
	if validateWiFiSSID(ssid) == nil && validateWiFiPassphrase(pass) == nil {
		return ssid, pass
	}
	stockSSID, stockPass := hotspotStockDefaults()
	if validateWiFiSSID(ssid) != nil {
		ssid = stockSSID
		if validateWiFiSSID(ssid) != nil {
			ssid = "JukaHub-" + hotspotMACSuffix()
		}
	}
	if validateWiFiPassphrase(pass) != nil {
		pass = stockPass
		if validateWiFiPassphrase(pass) != nil || hotspotWeakPass(pass) {
			if stockPass != "" {
				logInfo("hotspot", "the firmware's hotspot password is a shared default; using a new random one")
			}
			pass = hotspotNewPassword()
		}
	}
	hotspotSaveCredentials(c, ssid, pass)
	return ssid, pass
}

// hotspotSaveCredentials writes both keys and saves (bookkeeping keys go to
// Custom directly, never through setStoreString's mirror re-apply).
func hotspotSaveCredentials(c *Config, ssid, pass string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[hotspotSSIDKey] = ssid
	c.Variables.Custom[hotspotPassKey] = pass
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// hotspotStockDefaults reads the firmware's hostapd config for its name and
// password; "" for each it does not hold.
func hotspotStockDefaults() (ssid, pass string) {
	for _, path := range hotspotStockConfs {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 64<<10 {
			continue
		}
		s, p := hotspotParseHostapdConf(string(data))
		if s != "" {
			logInfo("hotspot", "defaults from the firmware's %s", path)
			return s, p
		}
	}
	return "", ""
}

// hotspotParseHostapdConf pulls ssid= and wpa_passphrase= out of a hostapd
// config. Comments and blank lines are skipped; the last value wins, as in
// hostapd. Pure.
func hotspotParseHostapdConf(text string) (ssid, pass string) {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ssid":
			ssid = value
		case "wpa_passphrase":
			pass = value
		}
	}
	return ssid, pass
}

// hotspotWeakPasses are factory passwords many devices share: a stock config
// holding one would make every such handheld's hotspot open to anyone.
var hotspotWeakPasses = []string{
	"12345678", "123456789", "1234567890", "87654321", "password", "wifi1111",
}

// hotspotWeakPass reports a password too weak to adopt from a stock config:
// shorter than 8, a known factory one, or one character repeated. Pure.
func hotspotWeakPass(p string) bool {
	if len(p) < 8 {
		return true
	}
	for _, w := range hotspotWeakPasses {
		if strings.EqualFold(p, w) {
			return true
		}
	}
	return strings.Count(p, p[:1]) == len(p)
}

// hotspotPassAlphabet leaves out look-alikes (0/O, 1/l/I) so the password
// reads off the screen without a second look.
const hotspotPassAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// hotspotNewPassword is a random 8-character password.
func hotspotNewPassword() string {
	b := make([]byte, 8)
	limit := big.NewInt(int64(len(hotspotPassAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			// crypto/rand does not fail on Linux; time is a last resort that
			// still gives a valid password.
			n = big.NewInt(time.Now().UnixNano() % int64(len(hotspotPassAlphabet)))
		}
		b[i] = hotspotPassAlphabet[n.Int64()]
	}
	return string(b)
}

// hotspotMACSuffix is the radio MAC's last four hex digits, upper case, or
// four random ones when it cannot be read.
func hotspotMACSuffix() string {
	if iface := wifiInterface(); iface != "" && validIfaceName(iface) {
		if data, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "address")); err == nil {
			if s := hotspotMACTail(string(data)); s != "" {
				return s
			}
		}
	}
	var b [2]byte
	_, _ = rand.Read(b[:])
	const hexd = "0123456789ABCDEF"
	return string([]byte{hexd[b[0]>>4], hexd[b[0]&15], hexd[b[1]>>4], hexd[b[1]&15]})
}

// hotspotMACTail is the last two octets of "aa:bb:cc:dd:ee:ff" as "EEFF", or
// "". Pure.
func hotspotMACTail(mac string) string {
	parts := strings.Split(strings.TrimSpace(mac), ":")
	if len(parts) != 6 {
		return ""
	}
	tail := strings.ToUpper(parts[4] + parts[5])
	for _, r := range tail {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return ""
		}
	}
	if len(tail) != 4 || tail == "0000" {
		return ""
	}
	return tail
}

// hotspotWiFiQR is the join code phone cameras read:
// WIFI:T:WPA;S:<name>;P:<password>;; with \ ; , : " escaped. Pure.
func hotspotWiFiQR(ssid, pass string) string {
	esc := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if strings.ContainsRune(`\;,:"`, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	return "WIFI:T:WPA;S:" + esc(ssid) + ";P:" + esc(pass) + ";;"
}

// ── Config files ─────────────────────────────────────────────────────────

// hotspotHostapdConf is the hostapd config for a WPA2-PSK access point on
// iface. ctrlDir enables hostapd_cli; ht adds 802.11n and WMM, which the retry
// leaves out for a driver or an old hostapd that refuses them. Pure.
func hotspotHostapdConf(iface, ssid, pass, ctrlDir string, ht bool) string {
	var b strings.Builder
	b.WriteString("# Written by JukaHub (hotspot.go); removed when the hotspot turns off.\n")
	b.WriteString("interface=" + iface + "\n")
	b.WriteString("driver=nl80211\n")
	if ctrlDir != "" {
		b.WriteString("ctrl_interface=" + ctrlDir + "\n")
	}
	// hostapd reads an ssid= value that starts with a quote (or P") as a
	// quoted or printf string, so such a name goes as ssid2 in hex (hostapd
	// 2.0 and later); every other name stays plain ssid=, which any hostapd
	// reads.
	if strings.HasPrefix(ssid, `"`) || strings.HasPrefix(ssid, `P"`) {
		b.WriteString("ssid2=" + hex.EncodeToString([]byte(ssid)) + "\n")
	} else {
		b.WriteString("ssid=" + ssid + "\n")
	}
	b.WriteString("hw_mode=g\n")
	b.WriteString("channel=" + strconv.Itoa(hotspotChannel) + "\n")
	if ht {
		b.WriteString("ieee80211n=1\n")
		b.WriteString("wmm_enabled=1\n")
	}
	b.WriteString("auth_algs=1\n")
	b.WriteString("ignore_broadcast_ssid=0\n")
	b.WriteString("wpa=2\n")
	b.WriteString("wpa_key_mgmt=WPA-PSK\n")
	b.WriteString("rsn_pairwise=CCMP\n")
	if len(pass) == 64 {
		b.WriteString("wpa_psk=" + pass + "\n")
	} else {
		b.WriteString("wpa_passphrase=" + pass + "\n")
	}
	return b.String()
}

// hotspotPoolIP is the n-th address of the hotspot's /24.
func hotspotPoolIP(n int) string {
	ip := net.ParseIP(hotspotIP).To4()
	return net.IPv4(ip[0], ip[1], ip[2], byte(n)).String()
}

// hotspotUdhcpdConf is busybox udhcpd's config: the pool, no router and no DNS
// (there is no internet behind the hotspot, so a phone keeps its mobile data
// for that), leases and pid file in the run dir. Pure.
func hotspotUdhcpdConf(iface, dir string) string {
	var b strings.Builder
	b.WriteString("start " + hotspotPoolIP(hotspotPoolFirst) + "\n")
	b.WriteString("end " + hotspotPoolIP(hotspotPoolLast) + "\n")
	b.WriteString("interface " + iface + "\n")
	b.WriteString("max_leases " + strconv.Itoa(hotspotPoolLast-hotspotPoolFirst+1) + "\n")
	b.WriteString("lease_file " + filepath.Join(dir, "udhcpd.leases") + "\n")
	b.WriteString("pidfile " + filepath.Join(dir, "udhcpd.pid") + "\n")
	b.WriteString("opt subnet " + hotspotNetmask + "\n")
	b.WriteString("opt lease " + strconv.Itoa(hotspotLeaseSecs) + "\n")
	return b.String()
}

// ── Who has joined ───────────────────────────────────────────────────────

// hotspotParseARP lists the devices /proc/net/arp knows on iface: complete
// entries (flags not 0x0) with a real hardware address. Pure.
func hotspotParseARP(text, iface string) []hotspotClient {
	var out []hotspotClient
	for i, line := range strings.Split(text, "\n") {
		if i == 0 {
			continue // the header
		}
		f := strings.Fields(line)
		if len(f) < 6 || f[5] != iface || f[2] == "0x0" || f[3] == "00:00:00:00:00:00" {
			continue
		}
		if net.ParseIP(f[0]) == nil {
			continue
		}
		out = append(out, hotspotClient{IP: f[0], MAC: strings.ToLower(f[3])})
	}
	hotspotSortClients(out)
	return out
}

// hotspotParseDnsmasqLeases reads dnsmasq's lease file ("expiry mac ip
// hostname clientid") into MAC → name; "*" means no name. Pure.
func hotspotParseDnsmasqLeases(text string) map[string]string {
	names := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] == "*" {
			continue
		}
		names[strings.ToLower(f[1])] = f[3]
	}
	return names
}

// hotspotNameClients fills each client's name from names (by MAC). Pure.
func hotspotNameClients(clients []hotspotClient, names map[string]string) {
	for i := range clients {
		if n := strings.TrimSpace(names[clients[i].MAC]); n != "" && clients[i].Name == "" {
			clients[i].Name = n
		}
	}
}

func hotspotSortClients(c []hotspotClient) {
	sort.Slice(c, func(i, j int) bool {
		a, b := net.ParseIP(c[i].IP).To4(), net.ParseIP(c[j].IP).To4()
		if a == nil || b == nil {
			return c[i].IP < c[j].IP
		}
		return binary.BigEndian.Uint32(a) < binary.BigEndian.Uint32(b)
	})
}

// ── The built-in DHCP server ─────────────────────────────────────────────
//
// For a firmware that has hostapd but neither udhcpd nor dnsmasq: DISCOVER →
// OFFER, REQUEST → ACK or NAK, one pool, broadcast replies (a client without an
// address takes them; nothing has to be put in the ARP table). The socket lives
// in hotspot_linux.go; the packet rules here are pure.

const (
	hsDHCPDiscover = 1
	hsDHCPOffer    = 2
	hsDHCPRequest  = 3
	hsDHCPDecline  = 4
	hsDHCPAck      = 5
	hsDHCPNak      = 6
	hsDHCPRelease  = 7
)

var hsDHCPMagic = []byte{99, 130, 83, 99}

// hsDHCPPacket is the part of a BOOTP request the server reads.
type hsDHCPPacket struct {
	xid       [4]byte
	flags     [2]byte
	ciaddr    net.IP
	chaddr    [16]byte
	mac       string
	msgType   byte
	requested net.IP
	serverID  net.IP
	hostname  string
}

// hsDHCPParse reads a client's request; ok is false for anything that is not a
// well-formed BOOTREQUEST with a DHCP message type. Pure.
func hsDHCPParse(b []byte) (p hsDHCPPacket, ok bool) {
	if len(b) < 240 || b[0] != 1 || b[1] != 1 || b[2] != 6 {
		return p, false
	}
	if string(b[236:240]) != string(hsDHCPMagic) {
		return p, false
	}
	copy(p.xid[:], b[4:8])
	copy(p.flags[:], b[10:12])
	p.ciaddr = net.IPv4(b[12], b[13], b[14], b[15]).To4()
	copy(p.chaddr[:], b[28:44])
	p.mac = net.HardwareAddr(b[28:34]).String()
	opts := b[240:]
	for i := 0; i < len(opts); {
		code := opts[i]
		if code == 0 {
			i++
			continue
		}
		if code == 255 || i+1 >= len(opts) {
			break
		}
		n := int(opts[i+1])
		if i+2+n > len(opts) {
			break
		}
		val := opts[i+2 : i+2+n]
		switch code {
		case 53:
			if n == 1 {
				p.msgType = val[0]
			}
		case 50:
			if n == 4 {
				p.requested = net.IPv4(val[0], val[1], val[2], val[3]).To4()
			}
		case 54:
			if n == 4 {
				p.serverID = net.IPv4(val[0], val[1], val[2], val[3]).To4()
			}
		case 12:
			p.hostname = strings.TrimSpace(strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, string(val)))
		}
		i += 2 + n
	}
	return p, p.msgType != 0
}

// hsDHCPReply builds the server's answer to req: msgType (offer, ack or nak),
// yiaddr the address handed out (nil for a NAK). Pure.
func hsDHCPReply(req hsDHCPPacket, msgType byte, yiaddr, server net.IP) []byte {
	b := make([]byte, 240, 320)
	b[0], b[1], b[2] = 2, 1, 6
	copy(b[4:8], req.xid[:])
	copy(b[10:12], req.flags[:])
	if ip := yiaddr.To4(); ip != nil && msgType != hsDHCPNak {
		copy(b[16:20], ip)
	}
	if ip := server.To4(); ip != nil {
		copy(b[20:24], ip)
	}
	copy(b[28:44], req.chaddr[:])
	copy(b[236:240], hsDHCPMagic)
	opt := func(code byte, val ...byte) {
		b = append(b, code, byte(len(val)))
		b = append(b, val...)
	}
	opt(53, msgType)
	if ip := server.To4(); ip != nil {
		opt(54, ip...)
	}
	if msgType != hsDHCPNak {
		var lease [4]byte
		binary.BigEndian.PutUint32(lease[:], hotspotLeaseSecs)
		opt(51, lease[:]...)
		opt(58, binary.BigEndian.AppendUint32(nil, hotspotLeaseSecs/2)...)
		opt(59, binary.BigEndian.AppendUint32(nil, hotspotLeaseSecs*7/8)...)
		opt(1, net.ParseIP(hotspotNetmask).To4()...)
	}
	b = append(b, 255)
	for len(b) < 300 {
		b = append(b, 0)
	}
	return b
}

// hsDHCPNamesMax caps the hostnames the built-in server remembers.
const hsDHCPNamesMax = 256

// hsDHCPPool hands out the hotspot's addresses: one per MAC, kept for the run.
// A hostname is kept only for a MAC that holds an address, and goes with it.
type hsDHCPPool struct {
	mu     sync.Mutex
	byMAC  map[string]net.IP
	names  map[string]string
	server net.IP
}

func newHSDHCPPool() *hsDHCPPool {
	return &hsDHCPPool{byMAC: map[string]net.IP{}, names: map[string]string{}, server: net.ParseIP(hotspotIP).To4()}
}

// inPool reports whether ip is one of the pool's addresses.
func (p *hsDHCPPool) inPool(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || !ip.Mask(net.CIDRMask(hotspotPrefixLen, 32)).Equal(p.server.Mask(net.CIDRMask(hotspotPrefixLen, 32))) {
		return false
	}
	return int(ip[3]) >= hotspotPoolFirst && int(ip[3]) <= hotspotPoolLast
}

// takenLocked reports whether another MAC holds ip.
func (p *hsDHCPPool) takenLocked(ip net.IP, mac string) bool {
	for m, held := range p.byMAC {
		if m != mac && held.Equal(ip) {
			return true
		}
	}
	return false
}

// handle answers one request; nil means no answer (a REQUEST meant for another
// server, a RELEASE, a full pool).
func (p *hsDHCPPool) handle(req hsDHCPPacket) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.noteNameLocked(req)
	switch req.msgType {
	case hsDHCPDiscover:
		ip := p.assignLocked(req.mac, req.requested)
		if ip == nil {
			return nil
		}
		return hsDHCPReply(req, hsDHCPOffer, ip, p.server)
	case hsDHCPRequest:
		if req.serverID != nil && !req.serverID.Equal(p.server) {
			// The client chose another server's offer.
			delete(p.byMAC, req.mac)
			delete(p.names, req.mac)
			return nil
		}
		want := req.requested
		if want == nil && req.ciaddr != nil && !req.ciaddr.Equal(net.IPv4zero) {
			want = req.ciaddr
		}
		if want == nil || !p.inPool(want) || p.takenLocked(want, req.mac) {
			return hsDHCPReply(req, hsDHCPNak, nil, p.server)
		}
		p.byMAC[req.mac] = want
		return hsDHCPReply(req, hsDHCPAck, want, p.server)
	case hsDHCPRelease, hsDHCPDecline:
		delete(p.byMAC, req.mac)
		delete(p.names, req.mac)
	}
	return nil
}

// noteNameLocked keeps req's hostname when its MAC holds an address (after
// the request was handled), up to hsDHCPNamesMax names.
func (p *hsDHCPPool) noteNameLocked(req hsDHCPPacket) {
	if req.hostname == "" {
		return
	}
	if _, held := p.byMAC[req.mac]; !held {
		return
	}
	if _, known := p.names[req.mac]; !known && len(p.names) >= hsDHCPNamesMax {
		return
	}
	p.names[req.mac] = req.hostname
}

// assignLocked is the MAC's address: the one it holds, the one it asks for when
// free, else the first free one; nil when the pool is full.
func (p *hsDHCPPool) assignLocked(mac string, want net.IP) net.IP {
	if ip, ok := p.byMAC[mac]; ok {
		return ip
	}
	if want != nil && p.inPool(want) && !p.takenLocked(want, mac) {
		p.byMAC[mac] = want
		return want
	}
	for n := hotspotPoolFirst; n <= hotspotPoolLast; n++ {
		ip := net.ParseIP(hotspotPoolIP(n)).To4()
		if !p.takenLocked(ip, mac) {
			p.byMAC[mac] = ip
			return ip
		}
	}
	return nil
}

// namesByMAC is a copy of the hostnames clients sent.
func (p *hsDHCPPool) namesByMAC() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.names))
	for k, v := range p.names {
		out[k] = v
	}
	return out
}
