//go:build linux

package main

// bt_identify_linux.go — "Identify" on the Bluetooth page: the pad in the
// sheet buzzes twice and blinks its lights, so the one in your hands can be
// told from the one on the sofa.
//
// It goes to the kernel directly rather than through SDL: SDL knows a pad by
// its mapping name ("PS5 Controller"), which two identical pads share, while
// the kernel's input device carries the pad's Bluetooth address (Uniq). The
// rumble is a force-feedback effect uploaded to the pad's event node
// (EVIOCSFF) and played twice; the lights are the LED class devices whose HID
// parent has the same address (the DualSense's player lights and light bar,
// the DualShock 4's light bar), switched off and on three times and put back.

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	// btFFRumble and btEVFF are the kernel's FF_RUMBLE and EV_FF.
	btFFRumble = 0x50
	btEVFF     = 0x15
	// btIdentifyBuzz is one buzz; two are played, btIdentifyGap apart.
	btIdentifyBuzz = 260 * time.Millisecond
	btIdentifyGap  = 480 * time.Millisecond
	// btLEDClassRoot is where the kernel lists LEDs.
	btLEDClassRoot = "/sys/class/leds"
)

// btIOW is the kernel's _IOW('E', nr, size).
func btIOW(nr, size uintptr) uintptr {
	return 1<<30 | size<<16 | uintptr('E')<<8 | nr
}

// btIdentifyPad buzzes and blinks the controller with address mac. It blocks
// for about two seconds: run it off the main loop. The error says that the
// pad offered neither.
func btIdentifyPad(mac string) error {
	mac = btNormMAC(mac)
	if mac == "" {
		return errors.New("not a Bluetooth address")
	}
	done := make(chan bool, 1)
	go func() { done <- btBlinkPadLEDs(mac) }()
	buzzed := btRumblePad(mac)
	blinked := <-done
	if !buzzed && !blinked {
		return errors.New("it has no rumble or light JukaHub can reach")
	}
	return nil
}

// btRumblePad plays two short buzzes on mac's event node.
func btRumblePad(mac string) bool {
	node := btPadEventNode(bzReadInputDevices(), mac, bits.UintSize)
	if node == "" {
		return false
	}
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	ptr := int(unsafe.Sizeof(uintptr(0)))
	// struct ff_effect: type, id, direction, trigger{button, interval},
	// replay{length, delay}, then the union at the pointer alignment, whose
	// largest member (ff_periodic_effect) ends in a pointer.
	unionAt := 16
	unionSize := 32
	if ptr == 4 {
		unionSize = 28
	}
	effect := make([]byte, unionAt+unionSize)
	le := binary.LittleEndian
	le.PutUint16(effect[0:], btFFRumble)
	le.PutUint16(effect[2:], 0xffff) // id -1: a new effect
	le.PutUint16(effect[10:], uint16(btIdentifyBuzz/time.Millisecond))
	le.PutUint16(effect[unionAt:], 0xc000)   // strong magnitude
	le.PutUint16(effect[unionAt+2:], 0xc000) // weak magnitude
	req := btIOW(0x80, uintptr(len(effect)))
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&effect[0]))); errno != 0 {
		return false
	}
	id := int16(le.Uint16(effect[2:]))
	defer syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), btIOW(0x81, 4), uintptr(id))
	// struct input_event: a timeval (two longs), type, code, value.
	ev := make([]byte, 2*ptr+8)
	le.PutUint16(ev[2*ptr:], btEVFF)
	le.PutUint16(ev[2*ptr+2:], uint16(id))
	le.PutUint32(ev[2*ptr+4:], 1)
	played := false
	for i := 0; i < 2; i++ {
		if i > 0 {
			time.Sleep(btIdentifyGap)
		}
		if _, err := f.Write(ev); err == nil {
			played = true
		}
	}
	time.Sleep(btIdentifyBuzz)
	return played
}

// btPadLEDs lists the LED class devices of the controller with address mac.
func btPadLEDs(mac string) []string {
	entries, err := os.ReadDir(btLEDClassRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		dir := filepath.Join(btLEDClassRoot, e.Name())
		for _, uevent := range []string{
			filepath.Join(dir, "device", "uevent"),
			filepath.Join(dir, "device", "device", "uevent"),
		} {
			found := false
			for _, line := range strings.Split(btReadSmall(uevent), "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "HID_UNIQ="); ok && btNormMAC(v) == mac {
					found = true
					break
				}
			}
			if found {
				out = append(out, dir)
				break
			}
		}
	}
	return out
}

// btBlinkPadLEDs switches mac's lights off and on three times and puts each
// back to the brightness it had.
func btBlinkPadLEDs(mac string) bool {
	type led struct {
		path      string
		was, full string
	}
	var leds []led
	for _, dir := range btPadLEDs(mac) {
		path := filepath.Join(dir, "brightness")
		was := btReadSmall(path)
		full := btReadSmall(filepath.Join(dir, "max_brightness"))
		if was == "" {
			continue
		}
		if n, err := strconv.Atoi(full); err != nil || n <= 0 {
			full = "1"
		}
		leds = append(leds, led{path: path, was: was, full: full})
	}
	if len(leds) == 0 {
		return false
	}
	write := func(path, value string) bool {
		return os.WriteFile(path, []byte(value), 0o644) == nil
	}
	ok := false
	for i := 0; i < 3; i++ {
		for _, l := range leds {
			ok = write(l.path, "0") || ok
		}
		time.Sleep(220 * time.Millisecond)
		for _, l := range leds {
			write(l.path, l.full)
		}
		time.Sleep(220 * time.Millisecond)
	}
	for _, l := range leds {
		write(l.path, l.was)
	}
	return ok
}
