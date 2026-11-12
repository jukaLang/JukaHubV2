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
// paths: a stuck attach is stopped, the chip's power is pulsed through its
// rfkill switch (the XR829 must start from reset to take its patch), and each
// attach program is tried until hci0 appears. Everything is bounded and logged.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// btAttachWait is how long one attach program gets to make hci0 appear:
	// the patch download over the UART takes a few seconds.
	btAttachWait = 10 * time.Second
)

// btUART is where a model's Bluetooth chip is wired: the serial port under /dev,
// the hciattach protocol (chip type) that talks to it, and the chip's name as
// Allwinner's per-chip scripts spell it ("bt_init.sh_xr829").
type btUART struct {
	tty   string
	proto string
	chip  string
}

// btChipUART is the chip's port on this model, or false where it is not known
// well enough to attach anything. The Smart Pro S (TG5050) uses another board
// and port (ttyAS1, spruceOS) with a chip not confirmed here, so it is left to
// its firmware script.
func btChipUART(model DeviceModel) (btUART, bool) {
	switch model {
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return btUART{tty: "ttyS1", proto: "xradio", chip: "xr829"}, true
	}
	return btUART{}, false
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
// fork the TrimUI images also carry, as Knulli runs it on the same board. Both
// stay in the foreground (-n), so their exit is seen.
func btAttachCommands(u btUART) []btAttachCommand {
	return []btAttachCommand{
		{tool: "hciattach", args: []string{"-n", u.tty, u.proto}},
		{tool: "rtk_hciattach", args: []string{"-n", "-s", "3000000", u.tty, u.proto}},
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
	dev := filepath.Join("/dev", u.tty)
	if _, err := os.Stat(dev); err != nil {
		logWarn("bluetooth", "bring-up: %s is missing; cannot attach the chip", dev)
		return false
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
	for _, c := range cmds {
		if ctx.Err() != nil {
			return false
		}
		if !btRfkillPulse(ctx) {
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

// btRfkillPulse switches the Bluetooth radio switches off and on again, a
// second each way: on the XR829 that switch is the chip's power, and the chip
// only takes its patch from a reset. A switch that cannot be written is left
// as it is (the attach may still work). false means ctx ended.
func btRfkillPulse(ctx context.Context) bool {
	entries, err := os.ReadDir(btRfkillRoot)
	if err != nil {
		return true
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if num, ok := strings.CutPrefix(name, "rfkill"); !ok || !btAllDigits(num) {
			continue
		}
		kind, err := os.ReadFile(filepath.Join(btRfkillRoot, name, "type"))
		if err != nil || strings.TrimSpace(string(kind)) != "bluetooth" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return true
	}
	write := func(v string) {
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(btRfkillRoot, name, "soft"), []byte(v), 0o644); err != nil {
				logDebug("bluetooth", "bring-up: rfkill %s soft=%s: %v", name, v, err)
			}
		}
	}
	write("1")
	if !btSleep(ctx, time.Second) {
		write("0")
		return false
	}
	write("0")
	return btSleep(ctx, time.Second)
}
