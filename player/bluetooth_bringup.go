package main

// bluetooth_bringup.go — making the Bluetooth stack ready before the page talks
// to it.
//
// The Bluetooth page used to assume what a desktop has: an adapter the kernel
// knows, bluetoothd running, the radio one `power on` away. On the handhelds
// that only holds while the firmware's own switch is on. Allwinner's Tina SDK,
// the base of the TrimUI images, brings Bluetooth up with a script
// (/etc/bluetooth/bt_init.sh: power the chip through rfkill, attach it to its
// UART with hciattach, wait for hci0, start bluetoothd), and a firmware whose
// switch is off may have done none of it. Connect or "Turn on" then met "No
// default controller available", or a bluetoothctl waiting for a daemon that
// never came, and the page could only say so.
//
// btBringUp climbs the same ladder the firmware does, one rung at a time and
// only where a cheap check shows the rung is missing:
//
//   - no adapter registered: the firmware's own bring-up script;
//   - no bluetoothd running: the firmware's init script for it, else the
//     daemon binary itself, started in a session of its own;
//   - the radio soft-blocked: rfkill unblock;
//   - the adapter powered off: bluetoothctl power on.
//
// The first two rungs only run on the TrimUI handhelds: on a desktop a stopped
// bluetoothd is the owner's choice, and JukaHub is not root there anyway. Every
// rung is bounded (the stock BusyBox has no `timeout`, so the bound is
// JukaHub's), runs only programs at fixed paths that the firmware itself ships,
// never anything a config names, and is logged. It runs only for something the
// user asked for — the radio switch, Connect, Pair, Search — never on a timer.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// btInitScriptWait bounds the firmware's bring-up script: Tina's pulses
	// the chip's power for two seconds and then waits up to seven for hci0.
	btInitScriptWait = 20 * time.Second
	// btAdapterWait is how long an adapter gets to appear after the script.
	btAdapterWait = 5 * time.Second
	// btDaemonScriptWait bounds one init script starting bluetoothd.
	btDaemonScriptWait = 10 * time.Second
	// btDaemonWait is how long bluetoothd gets to show up as a process.
	btDaemonWait = 4 * time.Second
	// btPowerWait is how long the adapter gets to report itself powered.
	btPowerWait = 3 * time.Second
	// btPowerSlow is the length of a `power on` that did not fail at once but
	// ran into its timeout: retrying a daemon that hangs only hangs again.
	btPowerSlow = 2500 * time.Millisecond
	// btRfkillRoot lists the kernel's radio switches.
	btRfkillRoot = "/sys/class/rfkill"
)

var (
	// btFirmwareInitScripts are the firmware bring-up scripts, tried in order.
	btFirmwareInitScripts = []string{"/etc/bluetooth/bt_init.sh"}
	// btDaemonInitScripts start bluetoothd the firmware's way: Tina's procd
	// wrapper, then the usual init scripts.
	btDaemonInitScripts = []string{"/etc/bluetooth/bluetoothd", "/etc/init.d/bluetoothd", "/etc/init.d/bluetooth"}
	// btDaemonBinaries is where BlueZ installs the daemon itself.
	btDaemonBinaries = []string{
		"/usr/libexec/bluetooth/bluetoothd", "/usr/lib/bluetooth/bluetoothd",
		"/usr/sbin/bluetoothd", "/usr/bin/bluetoothd",
	}
	// btScriptShells are the interpreters a script's first line may name.
	btScriptShells = map[string]bool{"/bin/sh": true, "/bin/ash": true, "/bin/bash": true, "/usr/bin/bash": true}
)

var (
	errBtNoChip    = errors.New("Bluetooth is not started on this system — switch it on once in the firmware's own settings, or restart the handheld")
	errBtHardware  = errors.New("Bluetooth is switched off by a hardware switch")
	errBtNoPower   = errors.New("Bluetooth did not switch on — try again")
	errBtNoAdapter = errors.New("this computer has no Bluetooth adapter the system can use")
)

// btBringUp makes the Bluetooth stack ready to talk to: an adapter the kernel
// has registered, bluetoothd running, the radio unblocked and powered. On a
// stack that is already up it costs a directory listing, a walk of /proc and
// one ioctl. stage reports progress in words for the page ("Starting
// Bluetooth…") and may be nil. A nil error means the radio is on.
func btBringUp(ctx context.Context, stage func(string)) error {
	if runtime.GOOS != "linux" {
		return errBzNoTool
	}
	say := func(text string) {
		if stage != nil {
			stage(text)
		}
	}
	if btAdapterPresent() && btDaemonRunning() {
		if up, ok := btHCIUp(); ok && up {
			return nil
		}
	}
	firmware := IsTrimuiDevice()

	if !btAdapterPresent() {
		if !firmware {
			// A computer's adapter stays registered even while it is blocked:
			// none at all is no Bluetooth hardware, or no driver for it.
			return errBtNoAdapter
		}
		if !btRunFirmwareInit(ctx, say) {
			return errBtNoChip
		}
		if !btWaitFor(ctx, btAdapterWait, btAdapterPresent) {
			if err := ctx.Err(); err != nil {
				return err
			}
			logWarn("bluetooth", "bring-up: no adapter after the firmware script")
			return errBtNoChip
		}
	}

	if !btDaemonRunning() {
		if !firmware {
			return errBzNoDaemon
		}
		say("Starting the Bluetooth service…")
		btStartDaemon(ctx)
		if !btWaitFor(ctx, btDaemonWait, btDaemonRunning) {
			if err := ctx.Err(); err != nil {
				return err
			}
			logWarn("bluetooth", "bring-up: bluetoothd did not start")
			return errBzNoDaemon
		}
		// The daemon puts the adapter on the bus a moment after it starts;
		// asked sooner, bluetoothctl answers "No default controller".
		if !btSleep(ctx, 700*time.Millisecond) {
			return ctx.Err()
		}
	}

	if err := btUnblockRfkill(); err != nil {
		return err
	}
	if up, ok := btHCIUp(); ok && up {
		btNoteOn()
		return nil
	}

	say("Turning Bluetooth on…")
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && !btSleep(ctx, 800*time.Millisecond) {
			return ctx.Err()
		}
		started := time.Now()
		if err = setBluetoothEnabled(true); err == nil || time.Since(started) > btPowerSlow {
			break
		}
		logDebug("bluetooth", "bring-up: power on (try %d): %v", attempt+1, err)
	}
	if err != nil {
		return err
	}
	if !btWaitFor(ctx, btPowerWait, btRadioIsOn) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errBtNoPower
	}
	btNoteOn()
	logInfo("bluetooth", "bring-up: the radio is on")
	// A radio that has just been powered needs a moment before it searches or
	// connects: the first page of a scan otherwise finds nothing.
	if !btSleep(ctx, time.Second) {
		return ctx.Err()
	}
	return nil
}

// btNoteOn records a radio the bring-up has seen powered, for the page and the
// bar's button (bzNoteRadio also makes the button read it again).
func btNoteOn() {
	bzNoteRadio(true, true)
}

// btRadioIsOn reads whether the radio is powered: the HCI flag where the kernel
// can be asked, bluetoothctl otherwise.
func btRadioIsOn() bool {
	if up, ok := btHCIUp(); ok {
		return up
	}
	on, known := bluetoothEnabled()
	return on && known
}

// btRunFirmwareInit runs the first firmware bring-up script there is and
// reports whether there was one. Its result does not matter: what counts is
// whether an adapter appears after it.
func btRunFirmwareInit(ctx context.Context, say func(string)) bool {
	for _, script := range btFirmwareInitScripts {
		name, args, ok := btScriptCommand(script)
		if !ok {
			continue
		}
		say("Starting Bluetooth…")
		err := btRunBounded(ctx, btInitScriptWait, name, append(args, "start")...)
		logInfo("bluetooth", "bring-up: %s start: %v", script, err)
		return true
	}
	return false
}

// btStartDaemon starts bluetoothd: through the first init script that brings it
// up, else from the first daemon binary there is.
func btStartDaemon(ctx context.Context) {
	for _, script := range btDaemonInitScripts {
		name, args, ok := btScriptCommand(script)
		if !ok {
			continue
		}
		err := btRunBounded(ctx, btDaemonScriptWait, name, append(args, "start")...)
		logInfo("bluetooth", "bring-up: %s start: %v", script, err)
		if btWaitFor(ctx, btDaemonWait, btDaemonRunning) || ctx.Err() != nil {
			return
		}
	}
	for _, bin := range btDaemonBinaries {
		if !btIsExecutable(bin) {
			continue
		}
		if err := btStartDetached(bin); err != nil {
			logWarn("bluetooth", "bring-up: starting %s: %v", bin, err)
			continue
		}
		logInfo("bluetooth", "bring-up: started %s", bin)
		return
	}
	logWarn("bluetooth", "bring-up: no way to start bluetoothd here")
}

// btRunBounded runs one firmware command to its end, killed after limit. Its
// standard streams are /dev/null rather than pipes: a daemon the script leaves
// running would hold a pipe open, and the wait would last as long as it does.
// The command runs in a session of its own, with the firmware's library path
// and PATH rather than JukaHub's (childenv.go).
func btRunBounded(ctx context.Context, limit time.Duration, name string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Env = screenChildEnv(os.Environ())
	cmd.Dir = "/"
	cmd.SysProcAttr = btDetachedAttr()
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// btStartDetached starts a daemon binary in a session of its own and leaves it
// running. bluetoothd stays in the foreground or forks, depending on the build:
// either way its exit is collected here, so it never lingers as a zombie.
func btStartDetached(bin string) error {
	cmd := exec.Command(bin)
	cmd.Env = screenChildEnv(os.Environ())
	cmd.Dir = "/"
	cmd.SysProcAttr = btDetachedAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		logInfo("bluetooth", "%s exited: %v", filepath.Base(bin), err)
	}()
	return nil
}

// btScriptCommand is how to run the script at path, or false when there is no
// such script. An executable file runs as itself, so its own first line picks
// the interpreter (Tina's init scripts name /etc/rc.common there). One that
// lost its execute bit runs through the interpreter its first line names, as
// long as that is a shell at a known path; a binary never runs this way.
func btScriptCommand(path string) (string, []string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, false
	}
	head := make([]byte, 256)
	n, _ := f.Read(head)
	_ = f.Close()
	head = head[:n]
	if !btLooksLikeScript(head) {
		return "", nil, false
	}
	if info.Mode().Perm()&0o111 != 0 {
		return path, nil, true
	}
	first, _, _ := bytes.Cut(head, []byte("\n"))
	return btShebangCommand(string(first), path)
}

// btLooksLikeScript reports text: a "#!" line, or no NUL byte and no ELF magic
// in the first bytes.
func btLooksLikeScript(head []byte) bool {
	if bytes.HasPrefix(head, []byte("#!")) {
		return true
	}
	return len(head) > 0 && !bytes.HasPrefix(head, []byte("\x7fELF")) && bytes.IndexByte(head, 0) < 0
}

// btShebangCommand turns a script's first line into the command that runs it
// the way the kernel would: the interpreter, its one optional argument, the
// script. A script without a "#!" line runs under /bin/sh. Only the shells in
// btScriptShells may interpret one.
func btShebangCommand(first, path string) (string, []string, bool) {
	first = strings.TrimSpace(strings.TrimSuffix(first, "\r"))
	line, ok := strings.CutPrefix(first, "#!")
	if !ok {
		return "/bin/sh", []string{path}, true
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || !btScriptShells[fields[0]] {
		return "", nil, false
	}
	args := make([]string, 0, 3)
	if len(fields) > 1 {
		// The kernel passes everything after the interpreter as one argument.
		args = append(args, strings.Join(fields[1:], " "))
	}
	return fields[0], append(args, path), true
}

// btIsExecutable reports a regular file with an execute bit.
func btIsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// btDaemonRunning reports whether bluetoothd is running.
func btDaemonRunning() bool { return btProcessRunning("/proc", "bluetoothd") }

// btProcessRunning reports whether a live process called name is in proc. A
// zombie is not one: the stock BusyBox's pidof counts zombies, which is how a
// dead daemon once passed for a running one on these handhelds.
func btProcessRunning(proc, name string) bool {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !btAllDigits(e.Name()) {
			continue
		}
		dir := filepath.Join(proc, e.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != name {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err == nil && btProcStatLive(string(stat)) {
			return true
		}
	}
	return false
}

// btProcStatLive reads the state out of a /proc/<pid>/stat line ("1234
// (bluetoothd) S 1 …"): anything but a zombie (Z) or a dead task (X) is live.
// The name is skipped up to its last ")", so a name with spaces or brackets
// cannot shift the field.
func btProcStatLive(stat string) bool {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "Z", "X", "x":
		return false
	}
	return true
}

// btUnblockRfkill lifts a soft block from the Bluetooth radio switches: rfkill
// when the firmware ships it, the switches' own sysfs files otherwise. A hard
// block is a switch only a hand can move, and is reported as such.
func btUnblockRfkill() error {
	soft, hard := btRfkillBlocked(btRfkillRoot)
	if hard {
		return errBtHardware
	}
	if len(soft) == 0 {
		return nil
	}
	if commandExists("rfkill") {
		err := runRfkill("bluetooth", true)
		if err == nil {
			return nil
		}
		logDebug("bluetooth", "bring-up: %v", err)
	}
	for _, name := range soft {
		if err := os.WriteFile(filepath.Join(btRfkillRoot, name, "soft"), []byte("0"), 0o644); err != nil {
			logWarn("bluetooth", "bring-up: unblocking %s: %v", name, err)
		}
	}
	return nil
}

// btRfkillBlocked lists the soft-blocked Bluetooth switches in root and reports
// whether any is hard-blocked. Only direct "rfkillN" children are read, and
// only their type, soft and hard files.
func btRfkillBlocked(root string) (soft []string, hard bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		name := e.Name()
		if num, ok := strings.CutPrefix(name, "rfkill"); !ok || !btAllDigits(num) {
			continue
		}
		kind, err := os.ReadFile(filepath.Join(root, name, "type"))
		if err != nil || strings.TrimSpace(string(kind)) != "bluetooth" {
			continue
		}
		if v, err := os.ReadFile(filepath.Join(root, name, "hard")); err == nil && strings.TrimSpace(string(v)) == "1" {
			hard = true
		}
		if v, err := os.ReadFile(filepath.Join(root, name, "soft")); err == nil && strings.TrimSpace(string(v)) == "1" {
			soft = append(soft, name)
		}
	}
	return soft, hard
}

// btWaitFor polls cond every quarter second until it holds, wait runs out or
// ctx ends.
func btWaitFor(ctx context.Context, wait time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(wait)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if !btSleep(ctx, 250*time.Millisecond) {
			return false
		}
	}
}

// btSleep waits d, or less when ctx ends first (false).
func btSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
