package main

// rfkill_sysfs.go — the radio switches, read and written through the kernel's
// own interfaces instead of the rfkill program.
//
// Quick Settings' Wi-Fi and Airplane Mode switches ran `rfkill`, found on PATH
// and started with JukaHub's own environment. A handheld can carry an rfkill
// that is there and cannot run: a BusyBox link whose BusyBox was built without
// the applet ("rfkill: applet not found"), or a build that cannot load its
// libraries under JukaHub's library path. Both exit with status 127, so the
// Wi-Fi switch failed with "rfkill unblock wifi: exit status 127", and every
// reading failed the same way: the panel drew Wi-Fi and Airplane Mode off
// whatever they were (2026-11-19). BusyBox's rfkill knows no "wifi" type
// either, only "wlan".
//
// The kernel lists the same switches in /sys/class/rfkill/rfkillN: type
// ("wlan", "bluetooth", …), name, soft (1 = blocked by software, which root
// may write) and hard (1 = held by a physical switch), both since 2.6.34, and
// the deprecated state every kernel keeps (0 = soft-blocked, 1 = unblocked,
// 2 = hard-blocked). A switch is now:
//
//   - read from those files, with no process;
//   - switched the way both rfkill programs do it, one "change all" event
//     written to /dev/rfkill (which also sets the kernel's default for a radio
//     registered later), and otherwise through each switch's own soft file
//     (state on a kernel without one) — JukaHub runs as root on the handhelds
//     — then read back: a driver can refuse;
//   - switched by the program only where neither could (a kernel without the
//     files, a desktop session that may write neither). It runs the way the
//     Bluetooth tools do (btToolRun: found beyond PATH, rfkill.elf too, with
//     the firmware's environment), and one that cannot run is not asked again
//     this session.
//
// On the TrimUI handhelds a radio whose network stack has registered its own
// switch (cfg80211's "phyN", the Bluetooth core's "hciN") is switched off
// through that switch only. The board's own Bluetooth switch ("sunxi-bt") is
// the chip's power: blocking it cut the XR829 off while hci0 stayed
// registered, and a later bring-up, finding the adapter there, never attached
// the chip again. A kind with no stack switch has nothing attached to cut off,
// and all its switches move. Airplane Mode there follows the same rule both
// ways; switching one radio on unblocks every switch of its kind, so a power
// switch something else blocked comes back too.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The switch kinds, as the kernel's type files name them. rfkillAnyKind
// matches every switch (Airplane Mode); it is also the programs' word for it.
const (
	rfkillWlan      = "wlan"
	rfkillBluetooth = "bluetooth"
	rfkillAnyKind   = "all"
)

// rfkillDevPath is the kernel's rfkill control device.
const rfkillDevPath = "/dev/rfkill"

// errRfkillNoSwitch is a radio the kernel lists no switch for.
var errRfkillNoSwitch = errors.New("no radio switch in " + btRfkillRoot)

// errRfkillHard is a radio a physical switch holds off: software cannot
// unblock it.
var errRfkillHard = errors.New("a hardware switch holds the radio off")

// rfkillSwitch is one switch the kernel lists.
type rfkillSwitch struct {
	name  string // "rfkill0"
	kind  string // its type file
	soft  bool   // blocked by software
	hard  bool   // held by a physical switch
	stack bool   // a network stack's own switch ("phy0", "hci0"), not a board's
}

// rfkillSysfsNames lists root's "rfkillN" entries; nothing else there is a
// switch. root is a constant, never a config value.
func rfkillSysfsNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if num, ok := strings.CutPrefix(name, "rfkill"); ok && btAllDigits(num) {
			names = append(names, name)
		}
	}
	return names
}

// rfkillSysfsList lists root's switches of kind (rfkillAnyKind: all of them),
// reading only their type, name, soft, hard and state files.
func rfkillSysfsList(root, kind string) []rfkillSwitch {
	var out []rfkillSwitch
	for _, name := range rfkillSysfsNames(root) {
		dir := filepath.Join(root, name)
		t, ok := rfkillSysfsRead(dir, "type")
		if !ok || t == "" || (kind != rfkillAnyKind && t != kind) {
			continue
		}
		sw := rfkillSwitch{name: name, kind: t}
		label, _ := rfkillSysfsRead(dir, "name")
		sw.stack = rfkillStackName(label)
		soft, softOK := rfkillSysfsRead(dir, "soft")
		hard, hardOK := rfkillSysfsRead(dir, "hard")
		sw.soft = softOK && soft == "1"
		sw.hard = hardOK && hard == "1"
		if !softOK || !hardOK {
			// A kernel older than 2.6.34 has only state.
			state, _ := rfkillSysfsRead(dir, "state")
			if !softOK && state == "0" {
				sw.soft = true
			}
			if !hardOK && state == "2" {
				sw.hard = true
			}
		}
		out = append(out, sw)
	}
	return out
}

// rfkillStackName reports a switch name a network stack registers: cfg80211's
// "phyN" for a Wi-Fi chip, the Bluetooth core's "hciN" for an adapter.
func rfkillStackName(name string) bool {
	for _, prefix := range []string{"phy", "hci"} {
		if num, ok := strings.CutPrefix(name, prefix); ok && btAllDigits(num) {
			return true
		}
	}
	return false
}

// rfkillMoves is the switches a change moves. With stackOnly (the handhelds)
// a kind whose network stack registered a switch moves through that one
// alone, and a kind with none through all of its switches.
func rfkillMoves(switches []rfkillSwitch, stackOnly bool) []rfkillSwitch {
	if !stackOnly {
		return switches
	}
	hasStack := map[string]bool{}
	for _, sw := range switches {
		if sw.stack {
			hasStack[sw.kind] = true
		}
	}
	var out []rfkillSwitch
	for _, sw := range switches {
		if sw.stack || !hasStack[sw.kind] {
			out = append(out, sw)
		}
	}
	return out
}

// rfkillSysfsRead is one attribute of a switch, trimmed, and whether it could
// be read.
func rfkillSysfsRead(dir, attr string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, attr))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// rfkillSysfsBlocked reports whether kind's radio is blocked — any of its
// switches, by software or by hand — and whether the kernel lists a switch for
// it at all.
func rfkillSysfsBlocked(root, kind string) (blocked, ok bool) {
	switches := rfkillSysfsList(root, kind)
	if len(switches) == 0 {
		return false, false
	}
	for _, sw := range switches {
		if sw.soft || sw.hard {
			return true, true
		}
	}
	return false, true
}

// rfkillSysfsAirplane reports whether every switch Airplane Mode moves
// (rfkillMoves) is blocked by software — nothing is allowed to transmit — and
// whether there is any.
func rfkillSysfsAirplane(root string, stackOnly bool) (on, ok bool) {
	moves := rfkillMoves(rfkillSysfsList(root, rfkillAnyKind), stackOnly)
	if len(moves) == 0 {
		return false, false
	}
	for _, sw := range moves {
		if !sw.soft {
			return false, true
		}
	}
	return true, true
}

// rfkillSysfsSet blocks or unblocks the switches of kind it moves
// (rfkillMoves) still in the other state through their soft files (state on a
// kernel without one), then reads them back (rfkillSysfsSettled). A switch
// that went away meanwhile (a USB adapter unregistered by its own block) is
// skipped.
func rfkillSysfsSet(root, kind string, block, stackOnly bool) error {
	switches := rfkillSysfsList(root, kind)
	if len(switches) == 0 {
		return errRfkillNoSwitch
	}
	for _, sw := range rfkillMoves(switches, stackOnly) {
		if sw.soft == block {
			continue
		}
		dir := filepath.Join(root, sw.name)
		// soft: 1 blocks. state, its older form: 1 is unblocked.
		path, value := filepath.Join(dir, "soft"), "0"
		if block {
			value = "1"
		}
		if _, err := os.Stat(path); err != nil {
			path, value = filepath.Join(dir, "state"), "1"
			if block {
				value = "0"
			}
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot write %s: %w", path, err)
		}
	}
	return rfkillSysfsSettled(root, kind, block, stackOnly)
}

// rfkillSysfsSettled checks the switches of kind a change moves
// (rfkillMoves): every one blocked by software, or none. A driver
// that refuses a change leaves its switch where it was, and only this reading
// says so. A physical switch still holding one radio off is errRfkillHard;
// Airplane Mode's every-radio unblock has lifted all that software can, so
// there it is no error. No switch listed is nil.
func rfkillSysfsSettled(root, kind string, block, stackOnly bool) error {
	held := false
	for _, sw := range rfkillMoves(rfkillSysfsList(root, kind), stackOnly) {
		if sw.soft != block {
			return fmt.Errorf("%s did not change (the driver refused it)", filepath.Join(root, sw.name))
		}
		if !block && sw.hard {
			held = true
		}
	}
	if held && kind != rfkillAnyKind {
		return errRfkillHard
	}
	return nil
}

// rfkillDevType is kind as /dev/rfkill's type number (enum rfkill_type).
func rfkillDevType(kind string) (byte, bool) {
	switch kind {
	case rfkillAnyKind:
		return 0, true
	case rfkillWlan:
		return 1, true
	case rfkillBluetooth:
		return 2, true
	}
	return 0, false
}

// rfkillDevChangeAll writes one "change all" event to /dev/rfkill, exactly as
// `rfkill block|unblock <type>` does: every switch of kind, and the kernel's
// default for one registered later, goes to block. The event is struct
// rfkill_event's first eight bytes: idx (u32, unused here), type, op (3 =
// RFKILL_OP_CHANGE_ALL), soft, hard (ignored).
func rfkillDevChangeAll(kind string, block bool) error {
	t, ok := rfkillDevType(kind)
	if !ok {
		return fmt.Errorf("unknown radio kind %q", kind)
	}
	f, err := os.OpenFile(rfkillDevPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	ev := [8]byte{4: t, 5: 3}
	if block {
		ev[6] = 1
	}
	_, err = f.Write(ev[:])
	return err
}

// rfkillHasCache is which kinds the kernel lists a switch for and whether
// this process may switch them, for rfkillAvailable: Quick Settings asks on
// every open and Home's Wi-Fi tile every frame. A Wi-Fi driver loaded late
// shows within rfkillHasCacheFor.
var rfkillHasCache struct {
	mu       sync.Mutex
	at       time.Time
	kinds    map[string]bool
	canWrite bool
}

const rfkillHasCacheFor = 5 * time.Second

// rfkillSysfsHas reports whether the kernel lists a switch of kind that this
// process can switch: root writes the files, and a desktop gives the user at
// its seat /dev/rfkill. Only the type files are read (soft and state take the
// kernel's rfkill lock on newer kernels).
func rfkillSysfsHas(kind string) bool {
	c := &rfkillHasCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kinds == nil || time.Since(c.at) > rfkillHasCacheFor {
		c.kinds, c.at = map[string]bool{}, time.Now()
		for _, name := range rfkillSysfsNames(btRfkillRoot) {
			if t, ok := rfkillSysfsRead(filepath.Join(btRfkillRoot, name), "type"); ok && t != "" {
				c.kinds[t] = true
				c.kinds[rfkillAnyKind] = true
			}
		}
		c.canWrite = false
		if len(c.kinds) > 0 {
			c.canWrite = os.Geteuid() == 0
			if !c.canWrite {
				// Opening the device changes nothing; a write would.
				if f, err := os.OpenFile(rfkillDevPath, os.O_WRONLY, 0); err == nil {
					f.Close()
					c.canWrite = true
				}
			}
		}
	}
	return c.canWrite && c.kinds[kind]
}

// rfkillToolBad holds the rfkill programs that could not run this session
// (rfkillToolRun); btRfkillTool passes over them.
var rfkillToolBad sync.Map

// rfkillToolUsable reports whether the rfkill program name has not failed to
// run this session.
func rfkillToolUsable(name string) bool {
	_, bad := rfkillToolBad.Load(name)
	return !bad
}

// rfkillToolCannotRun reports an error that says the program can never run
// its own code here: it exited 126/127 (a BusyBox without the applet, a
// library that would not load), or could not be started for good (gone, not
// executable, not this machine's format). A start that failed for want of
// memory or processes is not counted.
func rfkillToolCannotRun(err error) bool {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode() == 126 || exit.ExitCode() == 127
	}
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "exec format error")
}

// rfkillToolRun runs the rfkill program with args and returns its output. A
// program that cannot run (rfkillToolCannotRun) is set aside for the session.
// The program's own first line goes into the error: "exit status 127" alone
// said nothing.
func rfkillToolRun(args ...string) (string, error) {
	tool := btRfkillTool()
	if tool == "" {
		return "", errors.New("no rfkill program on this device")
	}
	out, err := btToolRun(tool, args...)
	if err == nil {
		return out, nil
	}
	what := tool + " " + strings.Join(args, " ")
	line := firstLine(strings.TrimSpace(out))
	if rfkillToolCannotRun(err) {
		rfkillToolBad.Store(tool, struct{}{})
		logWarn("radio", "%s cannot run (%v: %s); %s is set aside for this session", what, err, line, tool)
	}
	if line != "" {
		return out, fmt.Errorf("%s: %s (%w)", what, line, err)
	}
	return out, fmt.Errorf("%s: %w", what, err)
}

// rfkillAvailable reports whether kind's radio can be switched here: the
// kernel lists a switch for it that this process may switch, or an rfkill
// program that has not failed to run is there.
func rfkillAvailable(kind string) bool {
	return rfkillSysfsHas(kind) || btRfkillTool() != ""
}

// rfkillRadioOn reads kind's radio: the kernel's switches first, the program
// only where the kernel lists none. known is false when neither can answer.
// Both util-linux's and BusyBox's rfkill take "wlan" (BusyBox has no "wifi").
func rfkillRadioOn(kind string) (on, known bool) {
	if blocked, ok := rfkillSysfsBlocked(btRfkillRoot, kind); ok {
		return !blocked, true
	}
	if btRfkillTool() == "" {
		return false, false
	}
	out, err := rfkillToolRun("list", kind)
	if err != nil {
		return false, false
	}
	return rfkillState(out)
}

// rfkillAirplaneOn reads Airplane Mode from the kernel's switches; ok is false
// where it lists none Airplane Mode moves.
func rfkillAirplaneOn() (on, ok bool) {
	return rfkillSysfsAirplane(btRfkillRoot, IsTrimuiDevice())
}

// rfkillSetMu makes each of this file's switches one step: the Home tile,
// Quick Settings, the Wi-Fi page and the Bluetooth bring-up can switch at
// once, and one read-back must not see another's half-done change. (The
// bring-up's power pulse, bluetooth_attach.go, writes its own switch apart.)
var rfkillSetMu sync.Mutex

// rfkillSetRadio switches kind's radio on (unblocks it) or off (blocks it):
// /dev/rfkill or the kernel's switch files first, the program where the kernel
// lists no switch or its files could not be written. rfkill's verbs are block
// verbs, not on/off flags — switching a radio ON is "unblock" (they were once
// swapped, and the Wi-Fi switch turned the radio off every time it was asked
// to turn it on).
func rfkillSetRadio(kind string, on bool) error {
	rfkillSetMu.Lock()
	defer rfkillSetMu.Unlock()
	block := !on
	// The handhelds: off, and Airplane Mode either way, through the stack
	// switches where there are any (rfkillMoves). /dev/rfkill's "change all"
	// cannot leave the board's power switch out, so it is not used for those.
	stackOnly := IsTrimuiDevice() && (block || kind == rfkillAnyKind)
	if !stackOnly {
		if err := rfkillDevChangeAll(kind, block); err == nil {
			// The kernel took it: the switches say whether it held (one
			// refused by its driver would refuse the files the same way).
			return rfkillSysfsSettled(btRfkillRoot, kind, block, false)
		}
	}
	sysErr := rfkillSysfsSet(btRfkillRoot, kind, block, stackOnly)
	switch {
	case sysErr == nil, errors.Is(sysErr, errRfkillHard):
		return sysErr
	case errors.Is(sysErr, errRfkillNoSwitch):
		sysErr = nil
	}
	if btRfkillTool() == "" || stackOnly {
		// The program cannot leave a power switch out either.
		if sysErr != nil {
			return sysErr
		}
		return errors.New("no rfkill switch on this device")
	}
	verb := "block"
	if on {
		verb = "unblock"
	}
	if _, toolErr := rfkillToolRun(verb, kind); toolErr != nil {
		// The program's answer first: a permission refusal from the files is
		// only why the program was asked.
		if sysErr != nil && !errors.Is(sysErr, fs.ErrPermission) {
			return errors.Join(toolErr, sysErr)
		}
		return toolErr
	}
	// A program that exits 0 is not proof (TrimUI's rfkill.elf is not the
	// util-linux one): the kernel's switches say.
	return rfkillSysfsSettled(btRfkillRoot, kind, block, false)
}
