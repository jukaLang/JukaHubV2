package main

// bluetooth_diag.go — what the Bluetooth stack looks like on this system, in
// one block of text.
//
// Bluetooth on the handhelds was written from other projects' notes, never
// from a device: which programs the image ships and where, whether the chip is
// attached (hci0), whether bluetoothd and the firmware's own manager run, what
// the radio switches say, where pairings are kept. When it does not work, a
// guess about any of these is the bug, and the log is the only window onto the
// device. btDiagnosticsText reads all of them — files, /proc and one ioctl, no
// program started — so the log (once per run when the Bluetooth page first
// opens, and whenever a bring-up fails) and the diagnostics bundle carry the
// answer instead of a round trip to the tester.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// btDiagTools are the programs whose presence and place matter.
var btDiagTools = []string{
	"bluetoothctl", "bluetoothd", "hciattach", "rtk_hciattach", "hciconfig", "btmgmt",
	"rfkill", "bluealsa", "trimui_btmanager", "dbus-daemon",
}

// btDiagProcesses are the processes whose running matters.
var btDiagProcesses = []string{
	"bluetoothd", "hciattach", "rtk_hciattach", "trimui_btmanager", "bluealsa", "keymon", "dbus-daemon",
}

// btDiagPairingRoots are where BlueZ keeps pairings: TrimUI's /etc/lib (stock
// 5.54 is built with that state directory) and the usual /var/lib.
var btDiagPairingRoots = []string{"/etc/lib/bluetooth", "/var/lib/bluetooth"}

// btDiagnosticsText describes the Bluetooth stack.
func btDiagnosticsText() string {
	var sb strings.Builder
	line := func(key, format string, args ...any) {
		fmt.Fprintf(&sb, "%-13s: %s\n", key, fmt.Sprintf(format, args...))
	}
	sb.WriteString("Bluetooth\n---------\n")
	line("Device", "%s (%s/%s)", string(DetectDevice()), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS != "linux" {
		line("Stack", "not Linux: the page opens the system's own Bluetooth settings")
		return sb.String()
	}

	var tools []string
	for _, name := range btDiagTools {
		if p, ok := btToolPath(name); ok {
			if p == name {
				p = "PATH"
			}
			tools = append(tools, name+"="+p)
		} else {
			tools = append(tools, name+"=missing")
		}
	}
	line("Tools", "%s", strings.Join(tools, " "))

	var scripts []string
	for _, s := range append(btFirmwareInitScriptList(), btDaemonInitScripts...) {
		if _, _, ok := btScriptCommand(s); ok {
			scripts = append(scripts, s)
		}
	}
	if len(scripts) == 0 {
		scripts = []string{"none"}
	}
	line("Init scripts", "%s", strings.Join(scripts, " "))

	sysfs := btSysfsNames(btSysfsRoot)
	if sysfs == nil {
		line("Adapters", "%s unreadable (no Bluetooth in this kernel?)", btSysfsRoot)
	} else {
		adapters, links := btSysfsCount(sysfs)
		line("Adapters", "%d adapter(s), %d live link(s) [%s]", adapters, links, strings.Join(sysfs, " "))
	}
	if up, ok := btHCIUp(); ok {
		line("HCI_UP", "%v", up)
	} else {
		line("HCI_UP", "cannot ask the kernel (no Bluetooth socket)")
	}

	line("Radio switch", "%s", btDiagRfkill())

	board := btBoardModel()
	if board != DetectDevice() {
		line("Board", "%s (by its CPU)", string(board))
	}
	if u, ok := btChipUART(board); ok {
		_, err := os.Stat(filepath.Join("/dev", u.tty))
		line("Chip port", "/dev/%s %s (%s)", u.tty, map[bool]string{true: "present", false: "missing"}[err == nil], u.proto)
	}

	// One walk of /proc for every name (btDiagProcessTable).
	table := btDiagProcessTable("/proc", btDiagProcesses)
	var procs []string
	for _, name := range btDiagProcesses {
		pids := table[name]
		if len(pids) == 0 {
			procs = append(procs, name+"=no")
			continue
		}
		procs = append(procs, fmt.Sprintf("%s=%v", name, pids))
	}
	line("Processes", "%s", strings.Join(procs, " "))
	for _, name := range append([]string{"bluetoothd"}, btAttachProcessNames...) {
		if pids := table[name]; len(pids) > 0 {
			if args := btDiagCmdline(pids[0]); args != "" {
				line(name, "%s", btDiagMaskAddrs(args))
			}
		}
	}

	for _, root := range btDiagPairingRoots {
		line("Pairings", "%s: %s", root, btDiagPairings(root))
	}
	line("system.json", "%s", btDiagMaskAddrs(btDiagSystemJSON(trimuiSystemJSONPath)))
	line("PATH", "%s", os.Getenv("PATH"))
	return sb.String()
}

// btDiagRfkill lists the Bluetooth radio switches: name, soft and hard block.
func btDiagRfkill() string {
	entries, err := os.ReadDir(btRfkillRoot)
	if err != nil {
		return "no " + btRfkillRoot
	}
	var out []string
	for _, e := range entries {
		dir := filepath.Join(btRfkillRoot, e.Name())
		kind, err := os.ReadFile(filepath.Join(dir, "type"))
		if err != nil {
			continue
		}
		read := func(f string) string {
			b, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				return "?"
			}
			return strings.TrimSpace(string(b))
		}
		name := read("name")
		out = append(out, fmt.Sprintf("%s(%s,%s) soft=%s hard=%s", e.Name(), strings.TrimSpace(string(kind)), name, read("soft"), read("hard")))
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, "; ")
}

// btDiagProcessTable walks proc once and lists, for each of names, the live
// processes called that (comm keeps only 15 bytes of a name, btProcessPIDs).
func btDiagProcessTable(proc string, names []string) map[string][]int {
	want := map[string]string{}
	for _, name := range names {
		short := name
		if len(short) > 15 {
			short = short[:15]
		}
		want[short] = name
	}
	table := map[string][]int{}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return table
	}
	for _, e := range entries {
		if !btAllDigits(e.Name()) {
			continue
		}
		dir := filepath.Join(proc, e.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			continue
		}
		name, ok := want[strings.TrimSpace(string(comm))]
		if !ok {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil || !btProcStatLive(string(stat)) {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			table[name] = append(table[name], pid)
		}
	}
	return table
}

// btDiagCmdline is the command line of process pid, or "".
func btDiagCmdline(pid int) string {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

// btDiagAddrPattern matches a Bluetooth (or any EUI-48) address.
var btDiagAddrPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}`)
})

// btDiagMaskAddrs cuts every address in s to its last two bytes, as the
// pairing summary does: a command line (hciattach takes the chip's address)
// or a firmware setting can carry one.
func btDiagMaskAddrs(s string) string {
	return btDiagAddrPattern().ReplaceAllStringFunc(s, func(addr string) string {
		return "…" + btDiagShortAddr(addr)
	})
}

// btDiagPairings summarises a pairing store: one entry per adapter address
// BlueZ has used, each with the number of devices it remembers. More than one
// adapter there means the chip came up with a new address — and the old
// pairings were left behind with the old one. Addresses are cut to their last
// two bytes.
func btDiagPairings(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "none"
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !looksLikeBluetoothAddress(e.Name()) {
			continue
		}
		devs := 0
		if sub, err := os.ReadDir(filepath.Join(root, e.Name())); err == nil {
			for _, d := range sub {
				if d.IsDir() && looksLikeBluetoothAddress(d.Name()) {
					devs++
				}
			}
		}
		out = append(out, fmt.Sprintf("adapter …%s with %d device(s)", btDiagShortAddr(e.Name()), devs))
	}
	if len(out) == 0 {
		return "empty"
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// btDiagShortAddr is the last two bytes of an address ("EE:FF").
func btDiagShortAddr(addr string) string {
	if len(addr) >= 5 {
		return addr[len(addr)-5:]
	}
	return addr
}

// btDiagSystemJSON lists the firmware settings whose names mention Bluetooth
// ("bluetooth", "bt…"), with their values: what the firmware's own switch
// stores is not known yet.
func btDiagSystemJSON(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	var found []string
	text := string(data)
	for _, raw := range strings.Split(text, ",") {
		key, value, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), "{}\"\n\r\t ")
		lower := strings.ToLower(key)
		if strings.Contains(lower, "blue") || strings.HasPrefix(lower, "bt") {
			value = strings.Trim(strings.TrimSpace(value), "{}\n\r\t ")
			if len(value) > 40 {
				value = value[:40] + "…"
			}
			found = append(found, key+"="+value)
		}
	}
	if len(found) == 0 {
		return "no Bluetooth key"
	}
	return strings.Join(found, " ")
}

// btDiagLog throttles btLogDiagnostics per reason: the same failure retried
// does not fill the log, while a failure right after the page-open snapshot —
// the usual open-the-page, press-Turn-on sequence — is still written, since
// that later snapshot is the one that shows what went wrong.
var btDiagLog struct {
	mu   sync.Mutex
	at   map[string]time.Time
	once bool
}

// btLogDiagnostics writes the description to the log, with why, at most every
// thirty seconds per reason. It reads files and /proc, so it runs off the
// render thread.
func btLogDiagnostics(why string) {
	d := &btDiagLog
	d.mu.Lock()
	if d.at == nil {
		d.at = map[string]time.Time{}
	}
	if last, ok := d.at[why]; ok && time.Since(last) < 30*time.Second {
		d.mu.Unlock()
		return
	}
	d.at[why] = time.Now()
	d.mu.Unlock()
	logInfo("bluetooth", "stack (%s):\n%s", why, btDiagnosticsText())
}

// btLogDiagnosticsOnce writes the description once per run, the first time the
// Bluetooth page opens.
func btLogDiagnosticsOnce() {
	d := &btDiagLog
	d.mu.Lock()
	done := d.once
	d.once = true
	d.mu.Unlock()
	if !done {
		btLogDiagnostics("the Bluetooth page opened")
	}
}

// btDiagBundleFacts is the description for the diagnostics bundle.
func btDiagBundleFacts() string {
	return redactDiagnosticText(btDiagnosticsText())
}
