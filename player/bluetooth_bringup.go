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
// user asked for — the radio switch, Connect, Pair, Search, Quick Settings'
// switch — never on a timer, and only one runs at a time.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	// btDaemonCacheFor is how long a look for bluetoothd in /proc stands for
	// the readings that ask often (the radio probe, the bar's button).
	btDaemonCacheFor = 10 * time.Second
	// btBringUpAsyncLimit bounds a bring-up that a switch starts and does not
	// wait for (Quick Settings).
	btBringUpAsyncLimit = 60 * time.Second
)

// btBringUpGate lets one bring-up run at a time. A second one started meanwhile
// (the switch pressed while a search brings Bluetooth up) would run the
// firmware's script again, and Tina's kills the hciattach the first one
// started; instead it waits here and then finds the stack up.
var btBringUpGate = make(chan struct{}, 1)

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
// Bluetooth…") and may be nil. A nil error means the radio is on. Ending ctx
// stops it between rungs; a bluetoothctl run already under way is killed with
// it (btPowerOn).
func btBringUp(ctx context.Context, stage func(string)) error {
	if runtime.GOOS != "linux" {
		return errBzNoTool
	}
	select {
	case btBringUpGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-btBringUpGate }()
	say := func(text string) {
		if stage != nil {
			stage(text)
		}
	}
	firmware := IsTrimuiDevice()
	// bluetoothd is only this code's business on the handhelds. On a desktop a
	// stopped daemon is the owner's choice, and a PID namespace or hidepid can
	// hide a running one from /proc, so bluetoothctl's own answer decides there.
	daemonOK := func() bool { return !firmware || btDaemonRunning() }
	// btRadioIsOn asks the kernel, or bluetoothctl where the kernel cannot be
	// asked: a radio that is on is never powered "on" again.
	if btAdapterPresent() && daemonOK() && btRadioIsOn() {
		return nil
	}

	if !btAdapterPresent() {
		if !firmware {
			// A computer's adapter stays registered even while it is blocked:
			// none at all is no Bluetooth hardware, or no driver for it.
			return errBtNoAdapter
		}
		// The firmware's own script where there is one; where there is none, or
		// it ran and no adapter came, the chip is attached the way that script
		// would (bluetooth_attach.go). The generic Tina script was never shown to
		// exist on a TrimUI image, and without this rung a Smart Pro whose menu
		// left Bluetooth off could never get an adapter from JukaHub.
		ran := btRunFirmwareInit(ctx, say)
		if !ran || !btWaitFor(ctx, btAdapterWait, btAdapterPresent) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ran {
				logWarn("bluetooth", "bring-up: no adapter after the firmware script; attaching the chip directly")
			}
			if !btAttachChip(ctx, say) {
				if err := ctx.Err(); err != nil {
					return err
				}
				btLogDiagnostics("no adapter after the bring-up")
				return errBtNoChip
			}
		}
	}

	if !daemonOK() {
		say("Starting the Bluetooth service…")
		btStartDaemon(ctx)
		if !btWaitFor(ctx, btDaemonWait, btDaemonRunning) {
			if err := ctx.Err(); err != nil {
				return err
			}
			logWarn("bluetooth", "bring-up: bluetoothd did not start")
			btLogDiagnostics("bluetoothd did not start")
			return errBzNoDaemon
		}
		// The daemon puts the adapter on the bus a moment after it starts;
		// asked sooner, bluetoothctl answers "No default controller".
		if !btSleep(ctx, 700*time.Millisecond) {
			return ctx.Err()
		}
	}

	// Left the page while the daemon came up: nothing is unblocked or powered
	// for a user who is no longer there (Bluetooth on costs Wi-Fi airtime).
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := btUnblockRfkill(); err != nil {
		return err
	}
	if btRadioIsOn() {
		btNoteOn()
		return nil
	}

	say("Turning Bluetooth on…")
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && !btSleep(ctx, 800*time.Millisecond) {
			return ctx.Err()
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		started := time.Now()
		if err = btPowerOn(ctx); err == nil || time.Since(started) > btPowerSlow || ctx.Err() != nil {
			break
		}
		logDebug("bluetooth", "bring-up: power on (try %d): %v", attempt+1, err)
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		btLogDiagnostics("power on failed: " + err.Error())
		return err
	}
	if !btWaitFor(ctx, btPowerWait, btRadioIsOn) {
		if err := ctx.Err(); err != nil {
			return err
		}
		btLogDiagnostics("the radio did not power on")
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

// btPowerOn asks bluetoothctl to power the radio, bounded by ctx as well as by
// the probes' own timeout, so leaving the page kills a `power on` still waiting
// for its answer. A radio switched by rfkill alone is setBluetoothEnabled's.
// The daemon's usual refusals come back in the page's words; the rfkill block
// the old fallback lifted is already gone (btUnblockRfkill runs first).
func btPowerOn(ctx context.Context) error {
	if bluetoothBackend() != "bluetoothctl" {
		return setBluetoothEnabled(true)
	}
	runCtx, cancel := context.WithTimeout(ctx, systemCommandTimeout)
	defer cancel()
	cmd := btToolCmd(runCtx, "bluetoothctl", "power", "on")
	if cmd == nil {
		return errBzNoTool
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	text := string(out)
	logDebug("bluetooth", "bring-up: power on: %v: %s", err, firstLine(strings.TrimSpace(text)))
	switch {
	case strings.Contains(text, bzNoDaemonPrompt):
		return errBzNoDaemon
	case strings.Contains(text, "No default controller available"):
		return errBzNoAdapter
	}
	return errBtNoPower
}

// btAsyncBringUp is the bring-up a switch started without waiting for it, so a
// later press can call it off (btCancelBringUpAsync).
var btAsyncBringUp struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	seq    int
}

// btBringUpAsync brings Bluetooth up off the caller's thread, for a switch that
// has to answer at once — Quick Settings' row runs on the render thread, and
// where the firmware left Bluetooth stopped the bring-up takes seconds. The
// result comes back to the main loop: a toast when it failed, the bar's button
// read again, then done (which may be nil). A newer call replaces an older one
// still running.
func btBringUpAsync(done func(error)) {
	ctx, cancel := context.WithTimeout(context.Background(), btBringUpAsyncLimit)
	a := &btAsyncBringUp
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.cancel = cancel
	a.seq++
	seq := a.seq
	a.mu.Unlock()
	go func() {
		defer cancel()
		err := btBringUp(ctx, nil)
		a.mu.Lock()
		if a.seq == seq {
			a.cancel = nil
		}
		a.mu.Unlock()
		tbxPost(func() {
			btQuickKick()
			if err != nil && !errors.Is(err, context.Canceled) {
				ShowToast("Could not switch Bluetooth on: "+bzErrText(err), ToastKindError)
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// btCancelBringUpAsync calls off a bring-up btBringUpAsync started, before the
// same switch turns Bluetooth off: the radio must not come on behind a press
// that asked for it off.
func btCancelBringUpAsync() {
	a := &btAsyncBringUp
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.mu.Unlock()
}

// btPowerOffAsync switches the radio off off the caller's thread, for Quick
// Settings' switch: `bluetoothctl power off` disconnects every device first
// and can take seconds, which on the render thread froze the panel. A bring-up
// the switch started earlier is called off first, and the switch-off waits
// (briefly) for it to let go of the stack, so a cancelled bring-up still
// finishing a rung cannot power the radio on after this. done runs on the main
// loop with the result (a toast already said what failed) and may be nil.
func btPowerOffAsync(done func(error)) {
	btCancelBringUpAsync()
	// A later On press (btBringUpAsync bumps seq) outranks this Off: one that
	// takes the gate first must not be switched off behind the user's back.
	a := &btAsyncBringUp
	a.mu.Lock()
	a.seq++
	seq := a.seq
	a.mu.Unlock()
	go func() {
		// Off-thread, so a long wait costs nothing: a page search's bring-up
		// (not cancellable from here) may hold the gate for a while.
		wait := time.NewTimer(btBringUpAsyncLimit)
		held := false
		select {
		case btBringUpGate <- struct{}{}:
			held = true
		case <-wait.C:
		}
		wait.Stop()
		a.mu.Lock()
		stale := a.seq != seq
		a.mu.Unlock()
		var err error
		if !stale {
			err = setBluetoothEnabled(false)
		}
		if held {
			<-btBringUpGate
		}
		tbxPost(func() {
			btQuickKick()
			if err != nil {
				ShowToast("Could not switch Bluetooth off: "+firstLine(err.Error()), ToastKindError)
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// btKernelRadio reads the radio from the kernel alone, without a program
// started: no adapter registered is off, on the handhelds no bluetoothd is off
// too (nothing pairs or connects, btProbeRadio), and otherwise the adapters'
// HCI_UP flag. known is false where the kernel cannot be asked (not Linux, no
// Bluetooth socket), and only then is bluetoothctl worth its timeout.
func btKernelRadio() (on, known bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	if !btAdapterPresent() {
		return false, true
	}
	if IsTrimuiDevice() && !btDaemonRunningCached() {
		return false, true
	}
	return btHCIUp()
}

// btSwitchOffWithoutDaemon switches the radio off where bluetoothctl has
// nobody to ask: with no adapter nothing transmits, and on the handhelds with
// no bluetoothd `bluetoothctl power off` only waited out its whole timeout
// ("Waiting to connect to bluetoothd") before the rfkill fallback ran. handled
// is false where bluetoothctl should decide.
func btSwitchOffWithoutDaemon() (handled bool, err error) {
	if runtime.GOOS != "linux" {
		return false, nil
	}
	if !btAdapterPresent() {
		return true, nil
	}
	if IsTrimuiDevice() && !btDaemonRunning() {
		if btRfkillTool() != "" {
			return true, btRfkillSet(false)
		}
		return true, nil
	}
	return false, nil
}

// btBringUpFailed reports a bring-up that ended without Bluetooth on: an error
// other than being called off (a later press, or a newer bring-up taking over,
// says what the switch shows then).
func btBringUpFailed(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}

// btRunFirmwareInit runs the first firmware bring-up script there is and
// reports whether there was one. Its result does not matter: what counts is
// whether an adapter appears after it.
func btRunFirmwareInit(ctx context.Context, say func(string)) bool {
	for _, script := range btFirmwareInitScriptList() {
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

// btFirmwareInitScriptList puts the board's chip-specific script first, then
// the generic script. The stock XR829 image ships bt_init.sh_xr829, while a
// generic Tina script may be configured for a different chip. Only the known
// chip's variant is tried: another chip's script would use the wrong UART
// protocol and could stop an attach already in progress.
func btFirmwareInitScriptList() []string {
	u, ok := btChipUART(btBoardModel())
	if !ok || u.chip == "" {
		return append([]string(nil), btFirmwareInitScripts...)
	}
	list := make([]string, 0, len(btFirmwareInitScripts)*2)
	for _, s := range btFirmwareInitScripts {
		list = append(list, s+"_"+u.chip)
	}
	return append(list, btFirmwareInitScripts...)
}

// btStartDaemon starts bluetoothd: through the first init script there is, else
// — or when that script did not bring it up — from the first daemon binary.
// Only one script is tried: on Tina the procd wrapper and the init.d script are
// often the same service, and trying each in turn tripled the wait.
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
		break
	}
	for _, bin := range btDaemonBinaries {
		if !btIsExecutable(bin) {
			continue
		}
		if _, err := btStartDetached(bin); err != nil {
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
// either way its exit is collected here, so it never lingers as a zombie. The
// channel closes when it has exited (hciattach -n that gave up on the chip).
func btStartDetached(bin string, args ...string) (<-chan struct{}, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = screenChildEnv(os.Environ())
	cmd.Dir = "/"
	cmd.SysProcAttr = btDetachedAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(exited)
		logInfo("bluetooth", "%s exited: %v", filepath.Base(bin), err)
	}()
	return exited, nil
}

// btScriptCommand is how to run the script at path, or false when there is no
// such script. An executable file with a "#!" line runs as itself, so that line
// picks the interpreter (Tina's init scripts name /etc/rc.common there). Any
// other script — one that lost its execute bit, or one with no "#!" line, which
// the kernel refuses to start — runs through the interpreter its first line
// names, or /bin/sh, as long as that is a shell at a known path; a binary never
// runs this way.
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
	if info.Mode().Perm()&0o111 != 0 && bytes.HasPrefix(head, []byte("#!")) {
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

// btDaemonCache is the last look for bluetoothd, kept for the readings that
// ask often.
var btDaemonCache struct {
	mu      sync.Mutex
	at      time.Time
	running bool
}

// btDaemonRunning reports whether bluetoothd is running, looking now; the
// answer also refreshes the cache btDaemonRunningCached reads.
func btDaemonRunning() bool {
	running := btProcessRunning("/proc", "bluetoothd")
	c := &btDaemonCache
	c.mu.Lock()
	c.running, c.at = running, time.Now()
	c.mu.Unlock()
	return running
}

// btDaemonRunningCached is btDaemonRunning at most btDaemonCacheFor old: the
// radio probe and the bar's button ask every few seconds, and a walk of /proc
// is too much for that. The bring-up always looks now.
func btDaemonRunningCached() bool {
	c := &btDaemonCache
	c.mu.Lock()
	fresh := !c.at.IsZero() && time.Since(c.at) < btDaemonCacheFor
	running := c.running
	c.mu.Unlock()
	if fresh {
		return running
	}
	return btDaemonRunning()
}

// btProcessRunning reports whether a live process called name is in proc. A
// zombie is not one: the stock BusyBox's pidof counts zombies, which is how a
// dead daemon once passed for a running one on these handhelds.
func btProcessRunning(proc, name string) bool {
	return len(btProcessPIDs(proc, name)) > 0
}

// btProcessPIDs lists the live processes called name in proc (zombies left
// out, btProcStatLive). The kernel keeps only the first 15 bytes of a name in
// comm ("trimui_btmanage"), so a longer name is matched on those.
func btProcessPIDs(proc, name string) []int {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	if len(name) > 15 {
		name = name[:15]
	}
	var pids []int
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
		if err != nil || !btProcStatLive(string(stat)) {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
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
// (or TrimUI's rfkill.elf) when the firmware ships it, the switches' own sysfs
// files otherwise. A hard block is a switch only a hand can move.
func btUnblockRfkill() error {
	soft, hard := btRfkillBlocked(btRfkillRoot)
	if hard {
		return errBtHardware
	}
	if len(soft) == 0 {
		return nil
	}
	if btRfkillTool() != "" {
		err := btRfkillSet(true)
		if err == nil {
			// A tool that exits 0 is not proof (TrimUI's rfkill.elf is not
			// the util-linux one): only switches still blocked go on to sysfs.
			if soft, _ = btRfkillBlocked(btRfkillRoot); len(soft) == 0 {
				return nil
			}
			logDebug("bluetooth", "bring-up: %s unblock left %v blocked", btRfkillTool(), soft)
		} else {
			logDebug("bluetooth", "bring-up: %v", err)
		}
	}
	for _, name := range soft {
		dir := filepath.Join(btRfkillRoot, name)
		path, value := filepath.Join(dir, "state"), "1"
		if _, err := os.Stat(path); err != nil {
			path, value = filepath.Join(dir, "soft"), "0"
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			return fmt.Errorf("cannot unblock Bluetooth through %s: %w", path, err)
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
