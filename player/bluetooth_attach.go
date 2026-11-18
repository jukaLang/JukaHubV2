package main

// bluetooth_attach.go — attaching the handheld's Bluetooth chip to its serial
// port when no firmware script does it.
//
// The TrimUI A133P handhelds (Smart Pro, Brick, Brick Pro, Hammer) carry an
// XRadio XR829 Wi-Fi + Bluetooth combo. Its Bluetooth half sits on the UART
// ttyS1 and only becomes the kernel adapter hci0 once a user-space program,
// hciattach, opens that port, loads the chip's patch and hands the line to the
// kernel (Knulli's Brick: `rtk_hciattach -s 3000000 ttyS1 xradio`; Allwinner's
// Tina guide for the XR829: `hciattach -n ttyS1 xradio`). The firmware does this
// itself while its own Bluetooth switch is on. With that switch off there is no
// hci0 at all, and the bring-up used to depend on a generic Tina script,
// /etc/bluetooth/bt_init.sh, that no TrimUI image was ever shown to carry — so
// Turn on, Search and Connect all ended in "Bluetooth is not started".
//
// btAttachChip does what that script does, for the models whose port and chip
// are known, using only the attach programs the firmware ships, found at fixed
// paths: a stuck attach is stopped, the chip is woken through btwrite, its power
// is pulsed through its rfkill switch, and each attach program is tried until
// hci0 appears. Everything is bounded and logged.
//
// The Smart Pro S (TG5050, Allwinner A523) carries an AICSemi AIC8800D80
// instead: Wi-Fi on SDIO, Bluetooth on the UART ttyAS1 (spruceOS attaches it
// there). Its Bluetooth firmware is loaded over SDIO by the aic8800_bsp driver,
// so the UART needs no vendor setup: the AIC8800 boards' recipe (Bit-Brick K1,
// Radxa Zero 3W, Armbian's A733 service) is the helper module aic8800_btlpm
// (rfkill switch + /proc/bluetooth/sleep), rfkill unblock, a write to btwrite
// (Armbian: without it the chip never answers HCI_Reset), then
// `hciattach -s 1500000 <tty> any 1500000 flow nosleep`.
//
// Nothing is attached on a guess: the model's table names a port and a chip,
// the kernel must show that chip (or at least not another one), the port must
// exist and no other program may hold it — a firmware manager attaching it
// under a name this file does not know is waited for, never doubled.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// btAttachWait is how long one attach program gets to make hci0 appear:
	// the patch download over the UART takes a few seconds.
	btAttachWait = 10 * time.Second
	// The XR829's sleep controller must allow writes before the firmware is
	// loaded. NextUI and NX Redux set this before resetting the radio. The
	// AIC8800's helper module (aic8800_btlpm) creates the same node.
	btChipWritePath = "/proc/bluetooth/sleep/btwrite"
	// btModuleLoadWait bounds one modprobe of the AIC8800's Bluetooth helper.
	btModuleLoadWait = 5 * time.Second
)

// The chips, as btUART.chip and the per-chip firmware scripts spell them.
const (
	btChipXR829   = "xr829"
	btChipAIC8800 = "aic8800"
)

// btUART is where a model's Bluetooth chip is wired: the serial port under /dev,
// the hciattach protocol (chip type) that talks to it, and the chip's name as
// Allwinner's per-chip scripts spell it ("bt_init.sh_xr829").
type btUART struct {
	tty   string
	proto string
	chip  string
	// speed is the rate hciattach opens and keeps the port at; "" leaves the
	// tool's default, as the XR829 recipes do.
	speed string
	// pulse power-cycles the radio through rfkill before each attach, as the
	// XR829 scripts do. Without it the switch is only unblocked, which is all
	// the AIC8800 recipes do.
	pulse bool
}

// btChipUART is the chip's port on this model, or false where it is not known
// well enough to attach anything. The Brick Pro's radio is XRadio's too (its
// interrupt list names xradio_irq, mosaik_cfw issue #1), on the Brick's board
// layout; btChipMismatch still refuses it if the kernel shows another chip.
func btChipUART(model DeviceModel) (btUART, bool) {
	switch model {
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return btUART{tty: "ttyS1", proto: "xradio", chip: btChipXR829, pulse: true}, true
	case DeviceTrimuiSmartProS:
		return btUART{tty: "ttyAS1", proto: "any", chip: btChipAIC8800, speed: "1500000"}, true
	}
	return btUART{}, false
}

// btChipLabel is a chip's name for people ("AIC8800"), "" for none.
func btChipLabel(chip string) string {
	switch chip {
	case btChipXR829:
		return "XR829"
	case btChipAIC8800:
		return "AIC8800"
	}
	return ""
}

// radioAICModules are the AIC8800 driver's modules as vendor trees name them:
// the SDIO core and firmware loader, the Wi-Fi driver and the Bluetooth helper
// (aic8800_btlpm; Armbian's SDIO build calls it aic8800_btlpm_sdio).
var radioAICModules = []string{"aic8800_bsp", "aic8800_fdrv", "aic8800_btlpm", "aic8800_btlpm_sdio"}

// radioAICFirmwareDir is where the TG5050 firmware keeps the AIC8800's blobs.
const radioAICFirmwareDir = "/lib/firmware/aic8800_sdio"

// radioChipModules reports which Wi-Fi + Bluetooth combo the running kernel
// has a driver loaded for: the AIC8800's, XRadio's (xradio*, the XR829's), and
// the loaded module names, for the diagnostics. A driver built into the kernel
// has no /sys/module entry with these names, so "neither" is not "no chip".
func radioChipModules() (aic, xradio bool, loaded []string) {
	for _, name := range radioAICModules {
		if _, err := os.Stat(filepath.Join("/sys/module", name)); err == nil {
			aic = true
			loaded = append(loaded, name)
		}
	}
	if matches, _ := filepath.Glob("/sys/module/xradio*"); len(matches) > 0 {
		xradio = true
		for _, m := range matches {
			loaded = append(loaded, filepath.Base(m))
		}
	}
	return aic, xradio, loaded
}

// btChipMismatch says why the kernel contradicts the chip the model's table
// names, or "" when it does not. The XR829 is only refused when the AIC8800's
// driver is loaded and XRadio's is not; the AIC8800 needs its own evidence (a
// module or its firmware directory), since its attach has no vendor handshake
// that would fail on the wrong chip.
func btChipMismatch(u btUART) string {
	aic, xradio, _ := radioChipModules()
	switch u.chip {
	case btChipXR829:
		if aic && !xradio {
			return "the kernel runs the AIC8800 driver, not XRadio's"
		}
	case btChipAIC8800:
		if aic {
			return ""
		}
		if xradio {
			return "the kernel runs XRadio's driver, not the AIC8800's"
		}
		if info, err := os.Stat(radioAICFirmwareDir); err != nil || !info.IsDir() {
			return "no AIC8800 driver is loaded and " + radioAICFirmwareDir + " is missing"
		}
	}
	return ""
}

// btBoardModel is DetectDevice corrected the way hakuxDevice corrects it: a
// Smart Pro S named only by its panel reads as a Smart Pro, and its A523 board
// must not get the A133P's attach.
func btBoardModel() DeviceModel {
	return hakuxDevice()
}

// btAttachCommand is one way to attach the chip: an attach program and its
// arguments.
type btAttachCommand struct {
	tool string
	args []string
}

// btAttachCommands are the attach programs to try for a port, in order: BlueZ's
// hciattach as Allwinner's Tina images run it for the XR829, then the Realtek
// fork Knulli uses on the same board. Knulli's startup script passes 115200
// baud to that fork. Both stay in the foreground (-n), so their exit is seen.
//
// The AIC8800 gets BlueZ's hciattach alone, with no vendor setup ("any": its
// firmware is already in, over SDIO), at its rate both ways, with hardware flow
// control and without the tool's own sleep handling: the line the AIC8800
// boards' guides and Armbian's service run. The Realtek fork's setup would
// talk the wrong protocol to it.
func btAttachCommands(u btUART) []btAttachCommand {
	if u.chip == btChipAIC8800 {
		return []btAttachCommand{
			{tool: "hciattach", args: []string{"-n", "-s", u.speed, u.tty, u.proto, u.speed, "flow", "nosleep"}},
		}
	}
	return []btAttachCommand{
		{tool: "hciattach", args: []string{"-n", u.tty, u.proto}},
		{tool: "rtk_hciattach", args: []string{"-n", "-s", "115200", u.tty, u.proto}},
	}
}

// btAttachProcessNames are the attach programs as /proc names them.
var btAttachProcessNames = []string{"hciattach", "rtk_hciattach"}

// btAttachChip attaches the chip and reports whether an adapter appeared.
func btAttachChip(ctx context.Context, say func(string)) bool {
	u, ok := btChipUART(btBoardModel())
	if !ok {
		logInfo("bluetooth", "bring-up: no known Bluetooth port for this model; nothing to attach")
		return false
	}
	// The firmware may have attached it meanwhile (its script, its manager):
	// an adapter that is there is never attached a second time.
	if btAdapterPresent() {
		return true
	}
	if why := btChipMismatch(u); why != "" {
		logWarn("bluetooth", "bring-up: not attaching the %s on this %s: %s", btChipLabel(u.chip), string(btBoardModel()), why)
		return false
	}
	dev := filepath.Join("/dev", u.tty)
	if _, err := os.Stat(dev); err != nil {
		logWarn("bluetooth", "bring-up: %s is missing; cannot attach the chip", dev)
		return false
	}
	if u.chip == btChipAIC8800 {
		btLoadAICHelper(ctx)
		if ctx.Err() != nil {
			return false
		}
	}
	var cmds []btAttachCommand
	for _, c := range btAttachCommands(u) {
		if btToolExists(c.tool) {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		logWarn("bluetooth", "bring-up: no hciattach or rtk_hciattach on this image")
		return false
	}
	say("Starting Bluetooth…")
	// An attach already running may be halfway through the patch (hci0 only
	// appears once the download ends): it gets a full attach's time — the
	// firmware's own, just after boot or its switch, must not be cut short —
	// and only then is it taken for stuck and stopped, since two programs on
	// one port only garble each other (Tina's script kills it the same way).
	if btAttachRunning() {
		if btWaitFor(ctx, btAttachWait, btAdapterPresent) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if !btStopAttach(ctx) {
			return false
		}
	}
	// Some other program has the port open — a firmware manager attaching it
	// under a name not listed above. It gets the same full attach's time, and
	// is then left alone: two programs on one port only garble each other.
	if holders := btPortHolders("/proc", dev); len(holders) > 0 {
		if btWaitFor(ctx, btAttachWait, btAdapterPresent) {
			return true
		}
		if ctx.Err() == nil {
			logWarn("bluetooth", "bring-up: %s is held by %s and no adapter came; not starting a second attach on it", dev, strings.Join(holders, ", "))
		}
		return false
	}
	// A chip that is not power-cycled is unblocked first: the AIC8800's helper
	// only creates its sleep controller and powers the radio once its switch
	// is on, and Armbian's service writes btwrite after the unblock.
	if !u.pulse {
		if err := btUnblockRfkill(); err != nil {
			logWarn("bluetooth", "bring-up: %v", err)
			if errors.Is(err, errBtHardware) {
				return false
			}
		}
		btWaitFor(ctx, 2*time.Second, func() bool {
			_, err := os.Stat(btChipWritePath)
			return err == nil
		})
		if ctx.Err() != nil {
			return false
		}
	}
	// The stock XR829 script assumes the firmware's manager has already made
	// this write. NX Redux does it explicitly before its rfkill reset. Restore
	// the previous value if every attach fails, so a failed attempt does not
	// leave the chip awake for no reason.
	restoreWake := btWakeChip(btChipWritePath)
	attached := false
	defer func() {
		if !attached && restoreWake != nil {
			restoreWake()
		}
	}()
	for _, c := range cmds {
		if ctx.Err() != nil {
			return false
		}
		if u.pulse && !btRfkillPulse(ctx) {
			return false
		}
		path, _ := btToolPath(c.tool)
		exited, err := btStartDetached(path, c.args...)
		if err != nil {
			logWarn("bluetooth", "bring-up: starting %s: %v", c.tool, err)
			continue
		}
		logInfo("bluetooth", "bring-up: started %s %v", c.tool, c.args)
		if btWaitAttach(ctx, exited) {
			logInfo("bluetooth", "bring-up: %s attached the chip", c.tool)
			attached = true
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		logWarn("bluetooth", "bring-up: %s did not bring an adapter up", c.tool)
		if !btStopAttach(ctx) {
			return false
		}
	}
	return false
}

// btAICHelperModules are the AIC8800's Bluetooth helper as vendor trees name
// it (the last two of radioAICModules).
var btAICHelperModules = []string{"aic8800_btlpm", "aic8800_btlpm_sdio"}

// btAICHelperTried makes the helper's modprobe one try per run: a module that
// would not load once will not load on the next press either.
var btAICHelperTried atomic.Bool

// btLoadAICHelper loads the AIC8800's Bluetooth helper module where it is
// installed for the running kernel and not loaded yet (a firmware whose own
// Bluetooth switch is off may never have loaded it). Only modprobe at a fixed
// path runs, only for a module modules.dep lists, once per run, bounded.
// aic8800_bsp and aic8800_fdrv are never touched: reloading the core while the
// chip runs breaks its Bluetooth until the next boot (NanoKVM's AIC8800 fix).
func btLoadAICHelper(ctx context.Context) {
	for _, name := range btAICHelperModules {
		if _, err := os.Stat(filepath.Join("/sys/module", name)); err == nil {
			return
		}
	}
	if btAICHelperTried.Swap(true) {
		return
	}
	modprobe, ok := btToolPath("modprobe")
	if !ok {
		logInfo("bluetooth", "bring-up: no modprobe; the AIC8800 Bluetooth helper is left to the firmware")
		return
	}
	rel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return
	}
	dep, err := os.ReadFile(filepath.Join("/lib/modules", strings.TrimSpace(string(rel)), "modules.dep"))
	if err != nil {
		logInfo("bluetooth", "bring-up: no modules.dep for this kernel; the AIC8800 Bluetooth helper is left to the firmware")
		return
	}
	for _, name := range btAICHelperModules {
		if !modulesDepHas(string(dep), name) {
			continue
		}
		err := btRunBounded(ctx, btModuleLoadWait, modprobe, name)
		logInfo("bluetooth", "bring-up: modprobe %s: %v", name, err)
		return
	}
	logInfo("bluetooth", "bring-up: no AIC8800 Bluetooth helper module is installed for this kernel")
}

// modulesDepHas reports whether a modules.dep text lists the module name
// ("kernel/drivers/…/aic8800_btlpm.ko: …", compressed or not). Pure.
func modulesDepHas(dep, name string) bool {
	for _, line := range strings.Split(dep, "\n") {
		file, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		base := path.Base(strings.TrimSpace(file))
		for _, ext := range []string{".ko", ".ko.gz", ".ko.xz", ".ko.zst"} {
			if base == name+ext {
				return true
			}
		}
	}
	return false
}

// btPortHolders lists the processes other than this one that have dev open, as
// "name (pid)". It reads /proc/<pid>/fd, which needs the root JukaHub runs as
// on the handhelds; a process it may not look into is not listed.
func btPortHolders(proc, dev string) []string {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var holders []string
	for _, e := range entries {
		if !btAllDigits(e.Name()) {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		fdDir := filepath.Join(proc, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && target == dev {
				comm, _ := os.ReadFile(filepath.Join(proc, e.Name(), "comm"))
				holders = append(holders, fmt.Sprintf("%s (%d)", strings.TrimSpace(string(comm)), pid))
				break
			}
		}
	}
	return holders
}

// btWakeChip allows the XR829 sleep controller to accept the firmware upload.
// A missing proc node is normal on other firmware. If the node was off, the
// returned function restores it after a failed attach; a successful attach
// leaves it on, as the firmware's own start script does.
func btWakeChip(path string) func() {
	old, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("bluetooth", "bring-up: reading %s: %v", path, err)
		}
		return nil
	}
	before := strings.TrimSpace(string(old))
	if before == "1" {
		return nil
	}
	if before != "0" {
		logWarn("bluetooth", "bring-up: unexpected value in %s: %q", path, before)
		return nil
	}
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		logWarn("bluetooth", "bring-up: waking the chip through %s: %v", path, err)
		return nil
	}
	logInfo("bluetooth", "bring-up: enabled %s", path)
	return func() {
		if err := os.WriteFile(path, []byte("0"), 0o644); err != nil {
			logWarn("bluetooth", "bring-up: restoring %s: %v", path, err)
		}
	}
}

// btWaitAttach waits for an adapter after an attach program started: true once
// one is registered, false when the program exited first, the wait ran out or
// ctx ended.
func btWaitAttach(ctx context.Context, exited <-chan struct{}) bool {
	deadline := time.Now().Add(btAttachWait)
	for {
		if btAdapterPresent() {
			return true
		}
		select {
		case <-exited:
			// It may have registered the adapter just before it gave up.
			return btAdapterPresent()
		default:
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if !btSleep(ctx, 250*time.Millisecond) {
			return false
		}
	}
}

// btAttachRunning reports a live attach program.
func btAttachRunning() bool {
	for _, name := range btAttachProcessNames {
		if btProcessRunning("/proc", name) {
			return true
		}
	}
	return false
}

// btStopAttach stops every attach program: a polite signal, a second to go,
// then a kill. false means one is still running (not ours to signal, or ctx
// ended), and no second attach may start on the port it holds.
func btStopAttach(ctx context.Context) bool {
	btSignalAttach(ctx)
	if btAttachRunning() {
		logWarn("bluetooth", "bring-up: a chip attach is still running and could not be stopped")
		return false
	}
	return true
}

// btSignalAttach signals the attach programs, politely and then not.
func btSignalAttach(ctx context.Context) {
	signal := func(kill bool) int {
		n := 0
		for _, name := range btAttachProcessNames {
			for _, pid := range btProcessPIDs("/proc", name) {
				p, err := os.FindProcess(pid)
				if err != nil {
					continue
				}
				if kill {
					err = p.Kill()
				} else {
					err = p.Signal(syscall.SIGTERM)
				}
				if err == nil {
					n++
				}
			}
		}
		return n
	}
	if signal(false) == 0 {
		return
	}
	logInfo("bluetooth", "bring-up: stopped a stuck chip attach")
	if btWaitFor(ctx, time.Second, func() bool { return !btAttachRunning() }) {
		return
	}
	signal(true)
	btWaitFor(ctx, 500*time.Millisecond, func() bool { return !btAttachRunning() })
}

// btRfkillPulse resets the XR829's Bluetooth power, a second off and a second
// on. The stock and NextUI scripts write rfkillN/state (0 then 1). Only images
// without that legacy node use the inverse soft-block values. A switch that
// cannot be written is left as it is; the attach may still work.
func btRfkillPulse(ctx context.Context) bool {
	return btRfkillPulseAt(ctx, btRfkillRoot, btSleep)
}

func btRfkillPulseAt(ctx context.Context, root string, sleep func(context.Context, time.Duration) bool) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return true
	}
	type radioSwitch struct{ path, off, on string }
	var switches []radioSwitch
	for _, e := range entries {
		name := e.Name()
		if num, ok := strings.CutPrefix(name, "rfkill"); !ok || !btAllDigits(num) {
			continue
		}
		dir := filepath.Join(root, name)
		kind, err := os.ReadFile(filepath.Join(dir, "type"))
		if err != nil || strings.TrimSpace(string(kind)) != "bluetooth" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "state")); err == nil {
			switches = append(switches, radioSwitch{filepath.Join(dir, "state"), "0", "1"})
		} else {
			switches = append(switches, radioSwitch{filepath.Join(dir, "soft"), "1", "0"})
		}
	}
	if len(switches) == 0 {
		return true
	}
	write := func(on bool) {
		for _, sw := range switches {
			value := sw.off
			if on {
				value = sw.on
			}
			if err := os.WriteFile(sw.path, []byte(value), 0o644); err != nil {
				logWarn("bluetooth", "bring-up: rfkill %s=%s: %v", sw.path, value, err)
			}
		}
	}
	write(false)
	if !sleep(ctx, time.Second) {
		write(true)
		return false
	}
	write(true)
	return sleep(ctx, time.Second)
}
