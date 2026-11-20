//go:build linux

package main

// usbpad_linux.go — the USB side of Controller mode (usbpad.go).
//
// Controller mode makes a gadget of its own ("jukapad") rather than joining
// the firmware's, because a computer picks its driver from the device's IDs:
// the Xbox 360 shape needs Microsoft's 045E:028E on the device itself. The
// firmware's gadget (ADB) is only unbound while ours holds the port — never
// changed — and a marker in the temp directory (RAM: a reboot clears it, and
// a reboot resets the gadgets too) names it, so a run that dies with
// controller mode on is cleaned up at the next start: our gadget removed, the
// firmware's bound again (usbPadClearStale, also called by USB file
// transfer's start before it looks at the gadgets).
//
// Three transports, one session:
//
//   - Xbox 360 and HID over FunctionFS: descriptors written to ep0, the
//     endpoints as files, as usbmtp_linux.go does — and like there, the
//     blocking endpoint calls run on threads of their own that a stop wakes
//     with SIGURG (tgkill), because Go cannot interrupt them otherwise.
//   - HID through the kernel's own function (usb_f_hid): the report
//     descriptor goes into configfs and reports are written to /dev/hidgN,
//     which the Go poller can wait on, so no thread is needed.
//
// The pad is read from its evdev node through the Go poller as well (closing
// the file wakes the reader). It is grabbed only while a computer is
// connected, so JukaHub's own screen keeps its buttons the rest of the time;
// before a grab the buttons JukaHub saw held are released into the node, so
// none stays stuck behind it.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	usbPadGadget    = "jukapad"
	usbPadProbeName = "jukapad-probe"
	usbPadFFSInst   = "jukapad"
	usbPadFFSFunc   = "ffs." + usbPadFFSInst
	usbPadFFSMount  = "/dev/usb-ffs/" + usbPadFFSInst
	usbPadHIDFunc   = "hid." + usbPadFFSInst
	usbPadWatchStep = time.Second
)

// The transports.
const (
	usbPadKindXbox = iota + 1
	usbPadKindHIDFFS
	usbPadKindHIDKernel
)

// Which blocking thread a slot in usbPadSession.tids is.
const (
	usbPadThreadEP0 = iota
	usbPadThreadIn
	usbPadThreadOut
	usbPadThreadCount
)

// usbPadGadgetMu keeps the stale cleanup, the capability check and a start
// from working on the gadgets at the same time.
var usbPadGadgetMu sync.Mutex

// usbPadLeaked is set when a stop could not end its USB threads: their files
// stay open, so the port waits for a restart of JukaHub.
var usbPadLeaked atomic.Bool

// usbPadSession is one run of controller mode.
type usbPadSession struct {
	cfg  usbPadStartConfig
	kind int

	gadget  string // ours
	cfgDir  string // its configuration
	udc     string
	fw      string // the firmware's gadget we unbound, "" for none
	fwUDC   string
	unbound bool
	bound   bool
	mounted bool

	ep0, in, out int      // FunctionFS files, -1 when closed
	hidg         *os.File // the kernel HID function's node

	padMu   sync.Mutex
	pad     *os.File // the built-in pad's evdev node
	padPath string
	padRW   bool // opened read-write: releases can be written into it
	grabbed bool // guarded by padMu
	eng     usbPadEngine
	ext     usbPadState // cfg.external: the newest usbPadSubmit state, guarded by padMu

	// connMu makes a change of the computer's presence and the grab that
	// follows it one step: the ep0 thread and the watch both report it, and
	// two crossing calls must not leave the pad grabbed with no computer.
	connMu sync.Mutex

	repMu  sync.Mutex
	report []byte // the latest report to send

	dirty     chan struct{}
	enabledCh chan struct{}
	rumble    chan uint8
	enabled   atomic.Bool // FunctionFS: the computer enabled the function
	suspended atomic.Bool
	connected atomic.Bool

	stopping atomic.Bool
	done     chan struct{}
	tids     [usbPadThreadCount]atomic.Int32
	wg       sync.WaitGroup
}

// ── The capability check ──────────────────────────────────────────────────

// usbPadProbe checks what the kernel offers: the gadget framework, a device
// controller, and whether a HID and a FunctionFS function can be made in a
// scratch gadget (which is removed again at once; making a function loads
// its module when the firmware has one). /proc/config.gz, when there, is
// read for the log.
func usbPadProbe() usbPadCaps {
	var c usbPadCaps
	c.kernel = mtpReadAttr("/proc/sys/kernel/osrelease")
	c.hidConfig, c.ffsConfig = usbPadKernelConfig()
	if err := mtpEnsureConfigfs(); err != nil {
		c.problem = err.Error()
		return c
	}
	c.configfs = true
	c.udc = mtpFirstUDC()
	usbPadGadgetMu.Lock()
	defer usbPadGadgetMu.Unlock()
	g := filepath.Join(mtpGadgetBase, usbPadProbeName)
	if dirExists(g) {
		mtpRemoveGadgetTree(g)
	}
	if err := os.Mkdir(g, 0o755); err != nil && !os.IsExist(err) {
		c.problem = "could not make a test USB gadget: " + err.Error()
		return c
	}
	defer mtpRemoveGadgetTree(g)
	c.hid = usbPadTryFunction(g, "hid.probe")
	c.ffs = usbPadTryFunction(g, "ffs."+usbPadProbeName)
	return c
}

// usbPadTryFunction reports whether the kernel makes function name in g.
func usbPadTryFunction(g, name string) bool {
	fn := filepath.Join(g, "functions", name)
	if err := os.Mkdir(fn, 0o755); err != nil {
		return os.IsExist(err)
	}
	_ = os.Remove(fn)
	return true
}

// usbPadKernelConfig reads the HID and FunctionFS gadget options from
// /proc/config.gz: "y", "m", "n", or "" when the kernel does not say.
func usbPadKernelConfig() (hid, ffs string) {
	f, err := os.Open("/proc/config.gz")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", ""
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, 4<<20))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "CONFIG_USB_CONFIGFS_F_HID="):
			hid = strings.TrimPrefix(line, "CONFIG_USB_CONFIGFS_F_HID=")
		case line == "# CONFIG_USB_CONFIGFS_F_HID is not set":
			hid = "n"
		case strings.HasPrefix(line, "CONFIG_USB_CONFIGFS_F_FS="):
			ffs = strings.TrimPrefix(line, "CONFIG_USB_CONFIGFS_F_FS=")
		case line == "# CONFIG_USB_CONFIGFS_F_FS is not set":
			ffs = "n"
		}
	}
	return hid, ffs
}

// ── Crash recovery ────────────────────────────────────────────────────────

// usbPadMarkerPath names the firmware gadget controller mode unbound.
func usbPadMarkerPath() string { return filepath.Join(os.TempDir(), "jukahub-usbpad-port") }

func usbPadSaveMarker(gadget, udc string) {
	text := filepath.Base(gadget) + "\n" + udc + "\n"
	if err := os.WriteFile(usbPadMarkerPath(), []byte(text), 0o600); err != nil {
		logWarn("usbpad", "could not note the firmware's USB gadget: %v", err)
	}
}

func usbPadReadMarker() (gadget, udc string) {
	b, err := os.ReadFile(usbPadMarkerPath())
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(b), "\n")
	gadget = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		udc = strings.TrimSpace(lines[1])
	}
	// A name, nothing a path could be made of.
	if gadget == "" || gadget == "." || gadget == ".." || strings.ContainsAny(gadget, "/\\") || strings.ContainsAny(udc, "/\\") {
		return "", ""
	}
	// Never one of JukaHub's own gadgets: those are removed, not bound.
	switch gadget {
	case usbPadGadget, usbPadProbeName, mtpOwnGadget:
		return "", ""
	}
	return gadget, udc
}

// usbPadClearStale removes what a run that died with controller mode on left
// behind and binds the firmware's gadget again. Safe to call any time: a
// live session's gadget is left alone.
func usbPadClearStale() {
	if usbPadHolds.Load() || usbPadLeaked.Load() {
		return
	}
	usbPadGadgetMu.Lock()
	defer usbPadGadgetMu.Unlock()
	// Again under the lock: a start that began after the first look has
	// built its gadget by now (it sets usbPadHolds before it takes the lock),
	// and that gadget is live, not stale.
	if usbPadHolds.Load() || usbPadLeaked.Load() {
		return
	}
	usbPadClearStaleLocked()
}

func usbPadClearStaleLocked() {
	if !dirExists(mtpGadgetBase) {
		_ = os.Remove(usbPadMarkerPath())
		return
	}
	if p := filepath.Join(mtpGadgetBase, usbPadProbeName); dirExists(p) {
		mtpRemoveGadgetTree(p)
	}
	if g := filepath.Join(mtpGadgetBase, usbPadGadget); dirExists(g) {
		mtpUnbind(g)
		_ = syscall.Unmount(usbPadFFSMount, syscall.MNT_DETACH)
		mtpRemoveGadgetTree(g)
		logInfo("usbpad", "removed the controller gadget an earlier run left behind")
	}
	fw, udc := usbPadReadMarker()
	if fw == "" {
		_ = os.Remove(usbPadMarkerPath())
		return
	}
	g := filepath.Join(mtpGadgetBase, fw)
	if dirExists(g) && mtpReadAttr(filepath.Join(g, "UDC")) == "" {
		if udc == "" {
			udc = mtpFirstUDC()
		}
		if udc != "" {
			if err := mtpBind(g, udc); err != nil {
				logWarn("usbpad", "could not bind %s again: %v", fw, err)
			} else {
				logInfo("usbpad", "gave the USB port back to %s (an earlier run left it unbound)", fw)
			}
		}
	}
	_ = os.Remove(usbPadMarkerPath())
}

// ── Starting ──────────────────────────────────────────────────────────────

// usbPadPlatformStart opens the pad, makes the gadget, binds it and starts
// the session's goroutines. The returned stop undoes all of it.
func usbPadPlatformStart(cfg usbPadStartConfig) (usbPadRunInfo, func(), error) {
	var info usbPadRunInfo
	if usbPadLeaked.Load() || mtpLeaked.Load() {
		return info, nil, errors.New("restart JukaHub to use the USB port again")
	}
	if err := mtpEnsureConfigfs(); err != nil {
		return info, nil, err
	}
	usbPadGadgetMu.Lock()
	defer usbPadGadgetMu.Unlock()
	usbPadClearStaleLocked()
	s := &usbPadSession{
		cfg: cfg, ep0: -1, in: -1, out: -1,
		dirty: make(chan struct{}, 1), enabledCh: make(chan struct{}, 1), rumble: make(chan uint8, 1),
		done: make(chan struct{}),
	}
	s.eng.printed = cfg.printed
	if !cfg.external {
		if err := s.openPad(); err != nil {
			return info, nil, fmt.Errorf("could not read the built-in controller: %w", err)
		}
	}
	if err := s.attach(&info); err != nil {
		s.closePad()
		s.teardown()
		return info, nil, err
	}
	s.publish()
	n := 2 // sender, watch
	if !cfg.external {
		n++ // pad
	}
	if s.kind != usbPadKindHIDKernel {
		n++ // ep0
	}
	if s.kind == usbPadKindXbox {
		n += 2 // out, motor
	}
	s.wg.Add(n)
	if !cfg.external {
		go s.padLoop()
	} else {
		usbPadSetSink(s.submit)
	}
	go s.sendLoop()
	go s.watch()
	if s.kind != usbPadKindHIDKernel {
		go s.ep0Loop()
	}
	if s.kind == usbPadKindXbox {
		go s.outLoop()
		go s.motorLoop()
	}
	return info, s.stop, nil
}

// attach makes the gadget with the mode's function and binds it in place of
// the firmware's.
func (s *usbPadSession) attach(info *usbPadRunInfo) error {
	s.udc = mtpFirstUDC()
	if s.udc == "" {
		return errors.New("the port has no USB device controller")
	}
	for _, g := range mtpGadgets() {
		switch filepath.Base(g) {
		case usbPadGadget, usbPadProbeName, mtpOwnGadget:
			continue
		}
		if u := mtpReadAttr(filepath.Join(g, "UDC")); u != "" {
			s.fw, s.fwUDC = g, u
			break
		}
	}
	// Ours takes the port the firmware's gadget had (one UDC on a TrimUI,
	// but a board with two must not lose ADB on one and use the other).
	if s.fwUDC != "" {
		s.udc = s.fwUDC
	}
	if err := s.makeGadget(); err != nil {
		return err
	}
	fn, err := s.addFunction(info)
	if err != nil {
		return err
	}
	if err := os.Symlink(fn, filepath.Join(s.cfgDir, filepath.Base(fn))); err != nil && !os.IsExist(err) {
		return fmt.Errorf("could not add the controller to the USB gadget: %w", err)
	}
	if s.fw != "" {
		usbPadSaveMarker(s.fw, s.fwUDC)
		s.unbound = true
		mtpUnbind(s.fw)
	}
	if err := mtpBind(s.gadget, s.udc); err != nil {
		return fmt.Errorf("could not switch the USB port on: %w", err)
	}
	s.bound = true
	if s.kind == usbPadKindHIDKernel {
		if err := s.openHIDG(fn); err != nil {
			return err
		}
	}
	return nil
}

// makeGadget creates "jukapad": Microsoft's wired Xbox 360 pad IDs for the
// Xbox shape, the Linux Foundation's FunctionFS gadget IDs for the HID one.
func (s *usbPadSession) makeGadget() error {
	g := filepath.Join(mtpGadgetBase, usbPadGadget)
	if err := os.Mkdir(g, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("could not create the USB gadget: %w", err)
	}
	s.gadget = g
	vid, pid, bcd, class := "0x1d6b", "0x0105", "0x0100", "0x00"
	product := strings.TrimSpace(s.cfg.name + " controller")
	if s.cfg.mode == usbPadModeXbox {
		vid, pid, bcd, class = "0x045e", "0x028e", "0x0114", "0xff"
		product = "Controller"
	}
	attrs := [][2]string{
		{"idVendor", vid}, {"idProduct", pid}, {"bcdDevice", bcd}, {"bcdUSB", "0x0200"},
		{"bDeviceClass", class}, {"bDeviceSubClass", class}, {"bDeviceProtocol", class},
	}
	for _, a := range attrs {
		if err := mtpWriteAttr(filepath.Join(g, a[0]), a[1]); err != nil {
			return fmt.Errorf("could not set the USB gadget's %s: %w", a[0], err)
		}
	}
	str := filepath.Join(g, "strings", "0x409")
	if err := os.MkdirAll(str, 0o755); err != nil {
		return err
	}
	_ = mtpWriteAttr(filepath.Join(str, "manufacturer"), "JukaHub")
	_ = mtpWriteAttr(filepath.Join(str, "product"), product)
	_ = mtpWriteAttr(filepath.Join(str, "serialnumber"), s.cfg.serial)
	s.cfgDir = filepath.Join(g, "configs", "c.1")
	if err := os.MkdirAll(filepath.Join(s.cfgDir, "strings", "0x409"), 0o755); err != nil {
		return err
	}
	_ = mtpWriteAttr(filepath.Join(s.cfgDir, "strings", "0x409", "configuration"), "Controller")
	_ = mtpWriteAttr(filepath.Join(s.cfgDir, "MaxPower"), "500")
	return nil
}

// addFunction adds the mode's function and returns its directory: the
// kernel's HID function for the HID shape when it has one, else FunctionFS.
func (s *usbPadSession) addFunction(info *usbPadRunInfo) (string, error) {
	if s.cfg.mode == usbPadModeHID {
		fn := filepath.Join(s.gadget, "functions", usbPadHIDFunc)
		if err := os.Mkdir(fn, 0o755); err == nil || os.IsExist(err) {
			herr := usbPadSetupHIDFunction(fn)
			if herr == nil {
				s.kind = usbPadKindHIDKernel
				info.transport = "the kernel's HID function"
				return fn, nil
			}
			logWarn("usbpad", "the kernel's HID function would not take the report descriptor: %v", herr)
			_ = os.Remove(fn)
		}
	}
	fn := filepath.Join(s.gadget, "functions", usbPadFFSFunc)
	if err := os.Mkdir(fn, 0o755); err != nil && !os.IsExist(err) {
		if s.cfg.mode == usbPadModeHID {
			return "", fmt.Errorf("this firmware's kernel has neither the HID gadget nor FunctionFS: %w", err)
		}
		return "", fmt.Errorf("this firmware's kernel has no FunctionFS, which the Xbox 360 shape needs: %w", err)
	}
	if err := os.MkdirAll(usbPadFFSMount, 0o755); err != nil {
		return "", err
	}
	if err := syscall.Mount(usbPadFFSInst, usbPadFFSMount, "functionfs", 0, ""); err != nil {
		return "", fmt.Errorf("could not mount FunctionFS: %w", err)
	}
	s.mounted = true
	var err error
	if s.ep0, err = syscall.Open(filepath.Join(usbPadFFSMount, "ep0"), syscall.O_RDWR|syscall.O_CLOEXEC, 0); err != nil {
		s.ep0 = -1
		return "", fmt.Errorf("could not open ep0: %w", err)
	}
	if s.cfg.mode == usbPadModeHID {
		s.kind = usbPadKindHIDFFS
		info.transport = "FunctionFS (HID)"
		if _, err := syscall.Write(s.ep0, usbPadHIDFFSDescriptors()); err != nil {
			return "", fmt.Errorf("the kernel refused the HID descriptors: %w", err)
		}
	} else {
		s.kind = usbPadKindXbox
		info.transport = "FunctionFS"
		if _, err := syscall.Write(s.ep0, usbPadXboxFFSDescriptors(true)); err != nil {
			// Most kernels' FunctionFS refuses a class descriptor after a
			// vendor interface; the interface goes without it then.
			logInfo("usbpad", "FunctionFS refused the Xbox 360 interface descriptor (%v); offering the interface without it", err)
			if _, err := syscall.Write(s.ep0, usbPadXboxFFSDescriptors(false)); err != nil {
				return "", fmt.Errorf("the kernel refused the controller's USB descriptors: %w", err)
			}
			info.note = "This kernel left out the Xbox 360 interface details; if Windows shows the controller with a warning, use Generic HID."
		}
	}
	if _, err := syscall.Write(s.ep0, usbPadFFSNoStrings()); err != nil {
		return "", fmt.Errorf("the kernel refused the USB strings: %w", err)
	}
	if s.in, err = syscall.Open(filepath.Join(usbPadFFSMount, "ep1"), syscall.O_RDWR|syscall.O_CLOEXEC, 0); err != nil {
		s.in = -1
		return "", fmt.Errorf("could not open ep1: %w", err)
	}
	if s.kind == usbPadKindXbox {
		if s.out, err = syscall.Open(filepath.Join(usbPadFFSMount, "ep2"), syscall.O_RDWR|syscall.O_CLOEXEC, 0); err != nil {
			s.out = -1
			return "", fmt.Errorf("could not open ep2: %w", err)
		}
	}
	return fn, nil
}

// usbPadSetupHIDFunction fills in the kernel HID function: no boot
// protocol, the report's length and its descriptor. Newer kernels can leave
// the out pipe off, which this report does not need.
func usbPadSetupHIDFunction(fn string) error {
	for _, a := range [][2]string{{"protocol", "0"}, {"subclass", "0"}, {"report_length", strconv.Itoa(usbPadHIDReportLen)}} {
		if err := mtpWriteAttr(filepath.Join(fn, a[0]), a[1]); err != nil {
			return err
		}
	}
	if fileExists(filepath.Join(fn, "no_out_endpoint")) {
		_ = mtpWriteAttr(filepath.Join(fn, "no_out_endpoint"), "1")
	}
	return os.WriteFile(filepath.Join(fn, "report_desc"), usbPadHIDReportDescriptor(), 0o644)
}

// openHIDG opens the HID function's character device. Its numbers are in
// the function's "dev"; the node is /dev/hidgN when the firmware's device
// manager made one, else JukaHub makes it.
func (s *usbPadSession) openHIDG(fn string) error {
	numbers := mtpReadAttr(filepath.Join(fn, "dev"))
	maj, min, ok := strings.Cut(numbers, ":")
	major, err1 := strconv.Atoi(maj)
	minor, err2 := strconv.Atoi(min)
	if !ok || err1 != nil || err2 != nil {
		return fmt.Errorf("the HID function has no device number (%q)", numbers)
	}
	name := ""
	if uevent, err := os.ReadFile("/sys/dev/char/" + numbers + "/uevent"); err == nil {
		for _, line := range strings.Split(string(uevent), "\n") {
			if v, found := strings.CutPrefix(line, "DEVNAME="); found {
				name = strings.TrimSpace(v)
			}
		}
	}
	if name == "" || strings.Contains(name, "..") {
		name = "hidg" + strconv.Itoa(minor)
	}
	path := "/dev/" + name
	var lastErr error
	for i := 0; i < 15; i++ {
		if !fileExists(path) {
			dev := (minor & 0xff) | (major&0xfff)<<8 | (minor&^0xff)<<12
			_ = syscall.Mknod(path, syscall.S_IFCHR|0o600, dev)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			s.hidg = f
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("could not open %s: %w", path, lastErr)
}

// ── The pad ───────────────────────────────────────────────────────────────

// openPad opens the built-in pad's evdev node (vcursor_evdev.go picks it)
// and loads its ranges and the buttons held now. Main session goroutines
// only, under padMu or before they start.
func (s *usbPadSession) openPad() error {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return err
	}
	dev, ok := evdevPickPad(evdevParseDevices(string(data)))
	if !ok {
		return errors.New("no gamepad in " + evdevDevicesPath)
	}
	path := "/dev/input/" + dev.Handler
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	rw := err == nil
	if err != nil {
		if f, err = os.OpenFile(path, os.O_RDONLY, 0); err != nil {
			return err
		}
	}
	s.pad, s.padPath, s.padRW, s.grabbed = f, path, rw, false
	s.padControl(func(fd int) {
		info := make([]byte, evdevAbsInfoSize)
		for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
			if mbridgeIoctlBuf(fd, evdevGetAbs(axis), info) != nil {
				continue
			}
			if v, r, ok := evdevParseAbsInfo(info); ok {
				s.eng.setAxis(axis, r, v)
			}
		}
		bitmap := make([]byte, evdevKeyBitmapSize)
		if mbridgeIoctlBuf(fd, evdevGetKey(evdevKeyBitmapSize), bitmap) == nil {
			s.eng.setKeys(bitmap)
		}
	})
	logInfo("usbpad", "controller mode reads %q (%s)", dev.Name, path)
	return nil
}

// padControl runs fn with the pad's descriptor without taking it out of the
// poller (os.File.Fd would make it blocking).
func (s *usbPadSession) padControl(fn func(fd int)) {
	if s.pad == nil {
		return
	}
	rc, err := s.pad.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) { fn(int(fd)) })
}

func (s *usbPadSession) closePad() {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	if s.pad == nil {
		return
	}
	if s.grabbed {
		s.padControl(func(fd int) { _ = mbridgeIoctl(fd, evdevGrab, 0) })
		s.grabbed = false
	}
	_ = s.pad.Close()
	s.pad = nil
}

// setGrab grabs the pad while a computer is connected and lets go when it
// leaves. Before a grab the buttons held are released into the node, so
// JukaHub's screen does not keep them pressed while it cannot see them.
func (s *usbPadSession) setGrab(on bool) {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	if s.pad == nil || s.grabbed == on {
		return
	}
	if on && s.padRW {
		// Keys, the trigger keys, the D-pad hat, the triggers and the
		// sticks: everything JukaHub saw held goes back to rest.
		var evs []byte
		for _, ev := range s.eng.releaseEvents(nil) {
			evs = evdevEncode(evs, ev)
		}
		if len(evs) > 0 {
			evs = evdevEncode(evs, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			_, _ = s.pad.Write(evs)
		}
	}
	var err error
	s.padControl(func(fd int) {
		arg := uintptr(0)
		if on {
			arg = 1
		}
		err = mbridgeIoctl(fd, evdevGrab, arg)
	})
	if err != nil {
		logWarn("usbpad", "could not %s the pad: %v", map[bool]string{true: "grab", false: "let go of"}[on], err)
		return
	}
	s.grabbed = on
}

// padLoop reads the pad until the stop. A pad that goes away (the
// firmware's input daemon restarted) is looked for again every two seconds.
func (s *usbPadSession) padLoop() {
	defer s.wg.Done()
	buf := make([]byte, 64*evdevEventSize)
	var evs []evdevEvent
	dropping := false
	for !s.stopping.Load() {
		s.padMu.Lock()
		f := s.pad
		s.padMu.Unlock()
		if f == nil {
			select {
			case <-s.done:
				return
			case <-time.After(2 * time.Second):
			}
			s.padMu.Lock()
			if !s.stopping.Load() && s.pad == nil && s.openPad() == nil {
				s.padMu.Unlock()
				s.connMu.Lock()
				s.setGrab(s.connected.Load())
				s.connMu.Unlock()
				s.publish()
				continue
			}
			s.padMu.Unlock()
			continue
		}
		n, err := f.Read(buf)
		if err != nil {
			if s.stopping.Load() {
				return
			}
			logWarn("usbpad", "lost the pad: %v", err)
			s.closePad()
			continue
		}
		evs = evdevDecode(buf[:n], evs[:0])
		send, resync := false, false
		s.padMu.Lock()
		for _, ev := range evs {
			// Events were dropped (our reads fell behind): skip to the next
			// report and read the pad's state whole instead.
			if ev.Type == evdevEvSyn && ev.Code == evdevSynDropped {
				dropping = true
				continue
			}
			if dropping {
				if ev.Type == evdevEvSyn && ev.Code == evdevSynReport {
					dropping = false
					resync, send = true, true
				}
				continue
			}
			if s.eng.feed(ev) {
				send = true
			}
		}
		s.padMu.Unlock()
		if resync {
			s.resyncPad()
		}
		if send {
			s.publish()
		}
	}
}

// resyncPad reads the held buttons and the axes again after dropped events.
func (s *usbPadSession) resyncPad() {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	s.padControl(func(fd int) {
		bitmap := make([]byte, evdevKeyBitmapSize)
		if mbridgeIoctlBuf(fd, evdevGetKey(evdevKeyBitmapSize), bitmap) == nil {
			s.eng.setKeys(bitmap)
		}
		info := make([]byte, evdevAbsInfoSize)
		for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
			if mbridgeIoctlBuf(fd, evdevGetAbs(axis), info) == nil {
				if v, r, ok := evdevParseAbsInfo(info); ok {
					s.eng.setAxis(axis, r, v)
				}
			}
		}
	})
}

// publish packs the pad's state for the computer, wakes the sender and tells
// the screen. The pad reader and the ep0 thread (a computer arriving) both
// publish, so the snapshot, the stored report and the screen's copy are one
// step under padMu: an older snapshot never lands after a newer one (a
// release lost that way would keep a button down on the computer, or a
// chord "held" that was let go).
func (s *usbPadSession) publish() {
	var phys, sent usbPadState
	var both bool
	s.padMu.Lock()
	if s.cfg.external {
		// Another reader owns the pad and its leave chord.
		phys, sent = s.ext, s.ext
	} else {
		phys, sent, both = s.eng.snapshot()
	}
	var rep []byte
	if s.kind == usbPadKindXbox {
		rep = usbPadXInputReport(sent)
	} else {
		rep = usbPadHIDReport(sent)
	}
	s.repMu.Lock()
	s.report = rep
	s.repMu.Unlock()
	usbPadPublish(phys, both)
	s.padMu.Unlock()
	s.markDirty()
}

// submit takes a state from usbPadSubmit (cfg.external sessions).
func (s *usbPadSession) submit(st usbPadState) {
	if s.stopping.Load() {
		return
	}
	s.padMu.Lock()
	s.ext = st
	s.padMu.Unlock()
	s.publish()
}

func (s *usbPadSession) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *usbPadSession) currentReport() []byte {
	s.repMu.Lock()
	defer s.repMu.Unlock()
	return s.report
}

// ── The computer ──────────────────────────────────────────────────────────

// setConnected follows the computer: the pad is grabbed while one is there,
// the state is sent again when it arrives, the motor stops when it leaves.
func (s *usbPadSession) setConnected(on bool) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.connected.Swap(on) == on {
		return
	}
	usbPadSetConnected(on)
	s.setGrab(on)
	if on {
		// Packed again: the grab may just have released held buttons.
		s.publish()
		logInfo("usbpad", "a computer is using the controller")
	} else {
		s.setRumble(0)
		logInfo("usbpad", "the computer left; the pad is the handheld's until it is back")
	}
}

func (s *usbPadSession) setRumble(level uint8) {
	if s.kind != usbPadKindXbox {
		return
	}
	usbPadSetRumble(level)
	select {
	case <-s.rumble:
	default:
	}
	select {
	case s.rumble <- level:
	default:
	}
}

// sendLoop writes the newest report whenever the pad changed: one write at a
// time, each waiting for the computer to take it (it polls every 1 ms), so
// changes made meanwhile are sent together as the next report.
func (s *usbPadSession) sendLoop() {
	defer s.wg.Done()
	if s.kind != usbPadKindHIDKernel {
		runtime.LockOSThread()
		s.tids[usbPadThreadIn].Store(int32(syscall.Gettid()))
		defer s.tids[usbPadThreadIn].Store(0)
	}
	var last []byte
	for {
		select {
		case <-s.done:
			return
		case <-s.dirty:
		}
		if !s.connected.Load() {
			last = nil
			continue
		}
		rep := s.currentReport()
		if rep == nil || bytes.Equal(rep, last) {
			continue
		}
		var err error
		if s.kind == usbPadKindHIDKernel {
			_ = s.hidg.SetWriteDeadline(time.Now().Add(time.Second))
			_, err = s.hidg.Write(rep)
		} else {
			_, err = syscall.Write(s.in, rep)
		}
		if s.stopping.Load() {
			return
		}
		if err != nil {
			last = nil
			if errors.Is(err, syscall.EINTR) {
				s.markDirty()
			}
			continue
		}
		last = rep
	}
}

// watch follows the port once a second: for the kernel HID function the
// controller's state says whether a computer configured the device;
// FunctionFS says so itself (ep0Loop), and a port that dropped to "not
// attached" without a disable event counts as gone too.
func (s *usbPadSession) watch() {
	defer s.wg.Done()
	t := time.NewTicker(usbPadWatchStep)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		state := mtpReadAttr(filepath.Join(mtpUDCClass, s.udc, "state"))
		if s.kind == usbPadKindHIDKernel {
			// A controller that does not say its state is taken as
			// connected: reports are written, and fail harmlessly with no
			// computer there.
			s.setConnected(state == "configured" || state == "")
			continue
		}
		if state == "not attached" {
			s.setConnected(false)
		} else if s.enabled.Load() && !s.suspended.Load() {
			s.setConnected(true)
		}
	}
}

// ep0Loop reads FunctionFS's control events: the computer enabling and
// disabling the function, suspend and resume, and requests to answer.
func (s *usbPadSession) ep0Loop() {
	defer s.wg.Done()
	runtime.LockOSThread()
	s.tids[usbPadThreadEP0].Store(int32(syscall.Gettid()))
	defer s.tids[usbPadThreadEP0].Store(0)
	buf := make([]byte, ffsEventSize*4)
	for !s.stopping.Load() {
		n, err := syscall.Read(s.ep0, buf)
		if err != nil {
			if err == syscall.EINTR || s.stopping.Load() {
				continue
			}
			select {
			case <-s.done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		for off := 0; off+ffsEventSize <= n; off += ffsEventSize {
			s.handleEvent(buf[off : off+ffsEventSize])
		}
	}
}

func (s *usbPadSession) handleEvent(ev []byte) {
	switch ev[8] {
	case ffsEventEnable:
		s.enabled.Store(true)
		s.suspended.Store(false)
		select {
		case s.enabledCh <- struct{}{}:
		default:
		}
		s.setConnected(true)
	case ffsEventDisable, ffsEventUnbind:
		s.enabled.Store(false)
		s.setConnected(false)
	case ffsEventSuspend:
		s.suspended.Store(true)
		s.setConnected(false)
	case ffsEventResume:
		s.suspended.Store(false)
		if s.enabled.Load() {
			s.setConnected(true)
		}
	case ffsEventSetup:
		s.handleSetup(ev[:8])
	}
}

// handleSetup answers the requests that reach the function. The Xbox 360
// shape answers none — PC drivers do not need the vendor requests, and
// most third-party pads stall them too. The HID shape answers what a host's
// HID driver asks: the report descriptor, the current report, idle and
// protocol. Anything else is stalled.
func (s *usbPadSession) handleSetup(setup []byte) {
	reqType, req := setup[0], setup[1]
	value := binary.LittleEndian.Uint16(setup[2:])
	length := int(binary.LittleEndian.Uint16(setup[6:]))
	answer := func(b []byte) {
		if len(b) > length {
			b = b[:length]
		}
		s.ep0IO(func() { _, _ = syscall.Write(s.ep0, b) })
	}
	ack := func() {
		data := make([]byte, length)
		s.ep0IO(func() { _, _ = syscall.Read(s.ep0, data) })
	}
	if s.kind == usbPadKindHIDFFS {
		switch {
		case reqType == 0x81 && req == 0x06 && value>>8 == 0x22: // GET_DESCRIPTOR (Report)
			answer(usbPadHIDReportDescriptor())
			return
		case reqType == 0x81 && req == 0x06 && value>>8 == 0x21: // GET_DESCRIPTOR (HID)
			answer(usbPadHIDDescriptor())
			return
		case reqType == 0xA1 && req == 0x01: // GET_REPORT
			rep := s.currentReport()
			if rep == nil {
				rep = make([]byte, usbPadHIDReportLen)
			}
			answer(rep)
			return
		case reqType == 0xA1 && req == 0x02: // GET_IDLE
			answer([]byte{0})
			return
		case reqType == 0xA1 && req == 0x03: // GET_PROTOCOL: report
			answer([]byte{1})
			return
		case reqType == 0x21 && (req == 0x09 || req == 0x0A || req == 0x0B): // SET_REPORT, SET_IDLE, SET_PROTOCOL
			ack()
			return
		}
	}
	// FunctionFS stalls a request answered in the wrong direction.
	if reqType&0x80 != 0 {
		s.ep0IO(func() { _, _ = syscall.Read(s.ep0, nil) })
	} else {
		s.ep0IO(func() { _, _ = syscall.Write(s.ep0, nil) })
	}
}

// ep0IO runs one answer on the ep0 thread and interrupts it if it still
// waits after half a second (usbmtp_linux.go explains why).
func (s *usbPadSession) ep0IO(answer func()) {
	t := time.AfterFunc(500*time.Millisecond, func() { s.kick(usbPadThreadEP0) })
	answer()
	t.Stop()
}

// outLoop reads the computer's output reports (Xbox shape): rumble and the
// ring light, of which rumble is used.
func (s *usbPadSession) outLoop() {
	defer s.wg.Done()
	runtime.LockOSThread()
	s.tids[usbPadThreadOut].Store(int32(syscall.Gettid()))
	defer s.tids[usbPadThreadOut].Store(0)
	buf := make([]byte, 64)
	for !s.stopping.Load() {
		if !s.enabled.Load() {
			select {
			case <-s.done:
				return
			case <-s.enabledCh:
			}
			continue
		}
		n, err := syscall.Read(s.out, buf)
		if err != nil {
			if s.stopping.Load() {
				return
			}
			if err == syscall.EINTR {
				continue
			}
			select {
			case <-s.done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		if big, small, ok := usbPadParseRumble(buf[:n]); ok {
			s.setRumble(max(big, small))
		}
	}
}

// motorLoop keeps the motor going while the computer asks for rumble: a
// 300 ms pulse renewed every 200 ms, stopped when the level drops to 0.
func (s *usbPadSession) motorLoop() {
	defer s.wg.Done()
	var level uint8
	var tick *time.Ticker
	defer func() {
		if tick != nil {
			tick.Stop()
		}
		usbPadMotor(0)
	}()
	for {
		var renew <-chan time.Time
		if tick != nil {
			renew = tick.C
		}
		select {
		case <-s.done:
			return
		case level = <-s.rumble:
			usbPadMotor(level)
			switch {
			case level > 0 && tick == nil:
				tick = time.NewTicker(200 * time.Millisecond)
			case level == 0 && tick != nil:
				tick.Stop()
				tick = nil
			}
		case <-renew:
			usbPadMotor(level)
		}
	}
}

// ── Stopping ──────────────────────────────────────────────────────────────

// kick interrupts the thread in slot i, if it is in a USB transfer.
func (s *usbPadSession) kick(i int) {
	if tid := s.tids[i].Load(); tid != 0 {
		_ = syscall.Tgkill(os.Getpid(), int(tid), syscall.SIGURG)
	}
}

// stop ends the session and gives the port back. Safe to call twice.
func (s *usbPadSession) stop() {
	if !s.stopping.CompareAndSwap(false, true) {
		return
	}
	if s.cfg.external {
		usbPadSetSink(nil)
	}
	close(s.done)
	s.closePad()
	if s.hidg != nil {
		_ = s.hidg.Close()
	}
	// Unbinding makes every pending transfer fail, so the threads wake; the
	// signals cover one still waiting for the port.
	if s.bound {
		mtpUnbind(s.gadget)
	}
	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	clean := true
wait:
	for {
		for i := range s.tids {
			s.kick(i)
		}
		select {
		case <-finished:
			break wait
		case <-deadline.C:
			logWarn("usbpad", "controller threads did not stop; leaving their files open")
			clean = false
			break wait
		case <-tick.C:
		}
	}
	if clean {
		s.teardown()
		return
	}
	// A thread still inside a read or write owns its file (usbmtp_linux.go):
	// the files and the gadget stay, only the port goes back to the firmware.
	usbPadLeaked.Store(true)
	s.rebindFirmware()
}

func (s *usbPadSession) closeFiles() {
	for _, fd := range []*int{&s.in, &s.out, &s.ep0} {
		if *fd >= 0 {
			_ = syscall.Close(*fd)
			*fd = -1
		}
	}
}

// teardown removes our gadget and binds the firmware's again. Each step
// tolerates a step before it that never happened.
func (s *usbPadSession) teardown() {
	if s.gadget != "" {
		mtpUnbind(s.gadget)
	}
	s.closeFiles()
	if s.mounted {
		_ = syscall.Unmount(usbPadFFSMount, syscall.MNT_DETACH)
		s.mounted = false
	}
	if s.gadget != "" {
		mtpRemoveGadgetTree(s.gadget)
	}
	s.rebindFirmware()
}

// rebindFirmware gives the port back to the gadget that had it (ADB).
func (s *usbPadSession) rebindFirmware() {
	if s.fw != "" && s.unbound {
		udc := s.fwUDC
		if udc == "" {
			udc = s.udc
		}
		if err := mtpBind(s.fw, udc); err != nil {
			logWarn("usbpad", "could not give the USB port back to %s: %v", filepath.Base(s.fw), err)
		} else {
			logInfo("usbpad", "the USB port is %s's again", filepath.Base(s.fw))
		}
		s.unbound = false
	}
	_ = os.Remove(usbPadMarkerPath())
}
