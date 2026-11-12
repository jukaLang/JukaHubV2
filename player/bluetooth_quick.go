package main

// bluetooth_quick.go — the Bluetooth state the top bar's button draws.
//
// The button sits left of the Wi-Fi meter on every screen (statusbar_bluetooth.go)
// and is drawn every frame, so its reading must cost nothing on the render
// thread and must not wake bluetoothd: btRadioCachedState runs `bluetoothctl
// show`, which on the handheld is a process start and a D-Bus round trip, and
// which waits for its timeout whenever bluetoothd is not running. The reading
// here comes from the kernel instead:
//
//   - an adapter is there when /sys/class/bluetooth lists an "hciN" entry — the
//     firmware's own switch takes the chip away entirely when Bluetooth is off;
//   - every live link (a pad, a headset, a keyboard) is one "hciN:handle" entry
//     next to it, so the number of devices connected right now is a directory
//     listing;
//   - whether the radio is powered is the HCI_UP flag, which one ioctl on an
//     HCI socket reports for every adapter at once (bluetooth_bringup_linux.go).
//     Only where that socket cannot be opened does the reading fall back to
//     the bluetoothctl probe;
//   - on the handhelds a radio whose bluetoothd is not running reads off, the
//     way the page's switch does (a look in /proc stands for ten seconds).
//
// The reading is refreshed off the render thread, at most every two seconds and
// only while something asks: the bar is not drawn while a game owns the screen,
// so nothing polls then. The Bluetooth page kicks it after its own actions, so
// the button follows a connection made there at once.

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// btQuick is the Bluetooth state the top bar's button draws.
type btQuick struct {
	Available bool // the bar shows the button (Linux with bluetoothctl; Windows and macOS)
	Known     bool // the radio's state is known (Linux only)
	On        bool // the radio is powered
	Connected int  // devices connected right now
}

const (
	// btQuickRefresh is the shortest time between two readings.
	btQuickRefresh = 2 * time.Second
	// btSysfsRoot lists the kernel's Bluetooth adapters and links.
	btSysfsRoot = "/sys/class/bluetooth"
)

// btQuickCache is the last reading. kicks counts btQuickKick calls, so a kick
// that lands while a reading is in flight is not lost when that (older) reading
// is stored.
var btQuickCache struct {
	mu    sync.Mutex
	q     btQuick
	at    time.Time
	busy  bool
	kicks uint64
}

// btQuickRead takes one reading of the host. A variable so a test can pin the
// state the bar shows instead of depending on the machine's radio.
var btQuickRead = btQuickReadHost

// btQuickState is what the bar's button shows. It never blocks: a reading older
// than btQuickRefresh is refreshed in the background, and until the first one
// lands the radio's state is unknown.
//
// The button is there wherever the Bluetooth page can do something: on Linux
// where bluetoothctl exists (the page pairs and connects through it), and on
// Windows and macOS, where the page opens the system's own Bluetooth settings.
func btQuickState() btQuick {
	switch runtime.GOOS {
	case "windows", "darwin":
		return btQuick{Available: true}
	case "linux":
	default:
		return btQuick{}
	}
	if !btDevicesAvailable() {
		return btQuick{}
	}
	c := &btQuickCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy && (c.at.IsZero() || time.Since(c.at) >= btQuickRefresh) {
		c.busy = true
		go btQuickRefreshNow(c.kicks)
	}
	q := c.q
	q.Available = true
	return q
}

// btQuickRefreshNow takes a reading and stores it. A kick that arrived while it
// was being taken leaves the reading marked stale, so the next ask reads again.
func btQuickRefreshNow(kicks uint64) {
	q := btQuickRead()
	c := &btQuickCache
	c.mu.Lock()
	c.q = q
	c.at = time.Now()
	if c.kicks != kicks {
		c.at = time.Time{}
	}
	c.busy = false
	c.mu.Unlock()
}

// btQuickKick makes the next btQuickState refresh the reading: the Bluetooth
// page calls it after it switched the radio or connected, disconnected or
// forgot a device.
func btQuickKick() {
	c := &btQuickCache
	c.mu.Lock()
	c.kicks++
	c.at = time.Time{}
	c.mu.Unlock()
}

// btQuickReadHost reads the adapter, its links and its power from the kernel.
func btQuickReadHost() btQuick {
	adapters, links := btSysfsCount(btSysfsNames(btSysfsRoot))
	q := btQuick{Connected: links}
	if adapters == 0 {
		// No adapter registered: the firmware's switch is off (it takes the
		// chip away, not only the power), or the chip was never attached.
		// Either way nothing can connect, which is what "off" tells the user.
		q.Known = true
		return q
	}
	if links == 0 && IsTrimuiDevice() && !btDaemonRunningCached() {
		// No bluetoothd on the handheld: nothing pairs or connects, so the
		// button reads off, as the page's switch does (btProbeRadio).
		q.Known = true
		return q
	}
	if up, ok := btHCIUp(); ok {
		q.Known, q.On = true, up
	} else {
		q.On, q.Known = btRadioCachedState()
	}
	if links > 0 {
		// A live link is the radio's own proof that it is on, whatever an
		// older probe said.
		q.Known, q.On = true, true
	}
	return q
}

// btSysfsNames lists the entries of the kernel's Bluetooth class directory, or
// nothing where there is none (no Bluetooth in the kernel, not Linux).
func btSysfsNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// btSysfsCount counts adapters ("hci0") and live links ("hci0:11", one per
// connection the kernel holds) among the entries of /sys/class/bluetooth.
// Anything else there (a "rfkill" link on some kernels) counts as neither.
func btSysfsCount(names []string) (adapters, links int) {
	for _, name := range names {
		dev, handle, isLink := strings.Cut(name, ":")
		if !btIsHCIName(dev) {
			continue
		}
		switch {
		case !isLink:
			adapters++
		case btAllDigits(handle):
			links++
		}
	}
	return adapters, links
}

// btIsHCIName reports an adapter name: "hci" and its number.
func btIsHCIName(name string) bool {
	num, ok := strings.CutPrefix(name, "hci")
	return ok && btAllDigits(num)
}

// btAllDigits reports a non-empty run of decimal digits.
func btAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// btSysfsLinkCount is the number of live Bluetooth links right now. The page
// compares it between two readings: a change means a device connected or left
// without the page doing it (a pad woken with its PS button, a headset switched
// off), and the list is read again.
func btSysfsLinkCount() int {
	_, links := btSysfsCount(btSysfsNames(btSysfsRoot))
	return links
}

// btAdapterPresent reports whether the kernel has registered an adapter.
func btAdapterPresent() bool {
	adapters, _ := btSysfsCount(btSysfsNames(btSysfsRoot))
	return adapters > 0
}

// btHeaderActivate answers the bar's Bluetooth button (A, a click, a tap): it
// opens the Bluetooth page, where a paired device connects or disconnects with
// one press. Pressed while the page is already up, it reads the devices again
// instead — the one thing a second press can usefully mean there.
func btHeaderActivate(config *Config) {
	btQuickKick()
	if config == nil {
		config = appConfig
	}
	if config != nil && tbxCurrentScene(config) == btSceneName {
		btRelist(false)
		return
	}
	btScreenOpen(config)
}
