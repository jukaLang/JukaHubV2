//go:build linux

package main

// usbmtp_linux.go — the USB plumbing under "USB file transfer" (usbmtp.go).
//
// The kernel's gadget framework turns the handheld's USB-C port into a USB
// device; configfs (/sys/kernel/config/usb_gadget) says what the device is
// made of, and FunctionFS lets a program be one of those parts: JukaHub
// writes the interface's descriptors to ep0, then reads and writes the
// endpoints ep1 (bulk in), ep2 (bulk out) and ep3 (interrupt in) as files.
// usbmtp_proto.go speaks MTP over them.
//
// TrimUI firmwares already run adbd over the same port from a gadget of their
// own. Only one gadget can hold the port, so when one is bound JukaHub adds
// its function to that gadget's configuration (unbind, link, bind again):
// the computer then sees one device with both ADB and MTP, as Android phones
// do. With no gadget bound, JukaHub makes its own ("jukahub"). Stopping takes
// out only what was added and binds the firmware's gadget again; a start
// first clears whatever a crashed run left behind. With "Browser over USB"
// on, a network function joins MTP in the same configuration
// (usbnet_linux.go); it is dropped again whenever the port will not bind
// with it, so file transfer never depends on it.
//
// Endpoint reads and writes block in the kernel, and Go cannot interrupt a
// blocking read on such a file. Each I/O loop therefore runs on a thread of
// its own, and a cancel from the computer or a stop sends that thread SIGURG
// (tgkill): FunctionFS gives up the transfer with EINTR. The Go runtime uses
// SIGURG for its own preemption, so the signal is harmless anywhere else.
// The raw syscalls are used on purpose — os.File would retry on EINTR.

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	mtpConfigfsRoot  = "/sys/kernel/config"
	mtpGadgetBase    = mtpConfigfsRoot + "/usb_gadget"
	mtpOwnGadget     = "jukahub"
	mtpFFSInstance   = "jukamtp"
	mtpFFSFunction   = "ffs." + mtpFFSInstance
	mtpFFSMount      = "/dev/usb-ffs/" + mtpFFSInstance
	mtpUDCClass      = "/sys/class/udc"
	mtpHighSpeedPkt  = 512
	mtpBindTries     = 25
	mtpBindPause     = 200 * time.Millisecond
	mtpWatchInterval = 3 * time.Second
)

// FunctionFS event types (linux/usb/functionfs.h).
const (
	ffsEventBind    = 0
	ffsEventUnbind  = 1
	ffsEventEnable  = 2
	ffsEventDisable = 3
	ffsEventSetup   = 4
	ffsEventSuspend = 5
	ffsEventResume  = 6
	ffsEventSize    = 12
)

// Which I/O thread a slot in mtpFFS.tids is.
const (
	mtpThreadEP0 = iota
	mtpThreadBulk
	mtpThreadEvent
	mtpThreadCount
)

// mtpFFS is one running USB file transfer: the gadget it lives in, the
// endpoint files and the threads that use them. It is the server's
// transport.
type mtpFFS struct {
	srv *mtpServer

	ep0, in, out, intr int // file descriptors, -1 when closed

	gadget  string // the gadget our function was added to
	cfg     string // the configuration that links it
	own     bool   // the gadget is ours to remove
	unbound bool   // we unbound the firmware's gadget, so we bind it again
	udc     string // the controller the gadget is bound to

	stopping atomic.Bool
	done     chan struct{}
	events   chan []byte
	tids     [mtpThreadCount]atomic.Int32
	wg       sync.WaitGroup
	enabled  atomic.Bool
	packet   atomic.Int32 // bulk packet size at the speed the port came up at
	giveUp   atomic.Bool  // the firmware gadget would not bind: try once on teardown

	// net is the "Browser over USB" network function next to MTP
	// (usbnet_linux.go), nil when there is none. Set before the threads
	// start and cleared only after they end.
	net *usbNetLink

	// bindMu keeps the watchdog's bind, a network swap and stop's unbind
	// from crossing.
	bindMu sync.Mutex
}

// mtpLeaked is set when a stop could not end its USB threads: their files
// stay open, so the function cannot be removed or set up again until
// JukaHub restarts.
var mtpLeaked atomic.Bool

// ── Transport ────────────────────────────────────────────────────────────

func (f *mtpFFS) read(p []byte) (int, error)  { return f.io(f.out, p, false) }
func (f *mtpFFS) write(p []byte) (int, error) { return f.io(f.in, p, true) }
func (f *mtpFFS) maxPacket() int {
	if n := f.packet.Load(); n > 0 {
		return int(n)
	}
	return mtpHighSpeedPkt
}

// io is one blocking transfer. A stray EINTR is retried only while waiting
// for the next command, when nothing can have moved yet; inside a
// transaction part of the data may already be across, so the error ends the
// transaction instead of sending or losing bytes twice.
func (f *mtpFFS) io(fd int, p []byte, write bool) (int, error) {
	for {
		var n int
		var err error
		if write {
			n, err = syscall.Write(fd, p)
		} else {
			n, err = syscall.Read(fd, p)
		}
		if err == syscall.EINTR && !f.stopping.Load() && f.srv != nil && !f.srv.cancel.Load() && !f.srv.busy.Load() {
			continue
		}
		return n, err
	}
}

// mtpPacketSize is the bulk packet size for the speed the controller
// reports: 64 at full speed, else 512 — only full- and high-speed
// descriptors are offered, so a faster port runs them at high speed.
func mtpPacketSize(udc string) int32 {
	if strings.HasPrefix(mtpReadAttr(filepath.Join(mtpUDCClass, udc, "current_speed")), "full") {
		return 64
	}
	return mtpHighSpeedPkt
}

func (f *mtpFFS) event(p []byte) {
	select {
	case f.events <- p:
	default:
	}
}

// ── Descriptors ──────────────────────────────────────────────────────────

// mtpFFSDescriptors is the interface the computer sees: class 6/1/1 (Still
// Image, the class Windows binds its MTP driver to), a bulk pipe each way
// and an interrupt pipe for events, at full and high speed.
func mtpFFSDescriptors() []byte {
	iface := []byte{9, 4, 0, 0, 3, 6, 1, 1, 1}
	ep := func(addr, attr byte, maxp uint16, interval byte) []byte {
		return []byte{7, 5, addr, attr, byte(maxp), byte(maxp >> 8), interval}
	}
	var fs, hs []byte
	fs = append(fs, iface...)
	fs = append(fs, ep(0x81, 2, 64, 0)...)
	fs = append(fs, ep(0x02, 2, 64, 0)...)
	fs = append(fs, ep(0x83, 3, 28, 6)...)
	hs = append(hs, iface...)
	hs = append(hs, ep(0x81, 2, mtpHighSpeedPkt, 0)...)
	hs = append(hs, ep(0x02, 2, mtpHighSpeedPkt, 0)...)
	hs = append(hs, ep(0x83, 3, 28, 6)...)

	const magicV2, hasFS, hasHS = 3, 1, 2
	head := make([]byte, 20)
	binary.LittleEndian.PutUint32(head[0:], magicV2)
	binary.LittleEndian.PutUint32(head[4:], uint32(len(head)+len(fs)+len(hs)))
	binary.LittleEndian.PutUint32(head[8:], hasFS|hasHS)
	binary.LittleEndian.PutUint32(head[12:], 4) // descriptors at full speed
	binary.LittleEndian.PutUint32(head[16:], 4) // and at high speed
	out := append(head, fs...)
	return append(out, hs...)
}

// mtpFFSStrings names the interface "MTP" (string 1) in US English.
func mtpFFSStrings() []byte {
	const magic = 2
	name := []byte("MTP\x00")
	b := make([]byte, 16, 16+2+len(name))
	binary.LittleEndian.PutUint32(b[0:], magic)
	binary.LittleEndian.PutUint32(b[4:], uint32(16+2+len(name)))
	binary.LittleEndian.PutUint32(b[8:], 1)  // strings per language
	binary.LittleEndian.PutUint32(b[12:], 1) // languages
	b = binary.LittleEndian.AppendUint16(b, 0x0409)
	return append(b, name...)
}

// ── Starting and stopping ────────────────────────────────────────────────

// usbMtpPlatformStart sets the gadget up and starts serving. The returned
// stop undoes it; it is safe to call more than once.
func usbMtpPlatformStart(cfg usbMtpStartConfig) (*mtpServer, func(), error) {
	if mtpLeaked.Load() {
		return nil, nil, errors.New("restart JukaHub to use the USB port again")
	}
	if err := mtpEnsureConfigfs(); err != nil {
		return nil, nil, err
	}
	mtpClearStale()
	f := &mtpFFS{ep0: -1, in: -1, out: -1, intr: -1, done: make(chan struct{}), events: make(chan []byte, 8)}
	if err := f.attach(cfg); err != nil {
		f.teardown()
		return nil, nil, err
	}
	f.srv = newMTPServer(f, mtpServerConfig{
		root:   cfg.root,
		name:   cfg.name,
		serial: cfg.serial,
		locked: cfg.locked,
		space:  mtpDiskSpace,
	})
	f.wg.Add(3)
	go f.ep0Loop(cfg)
	go f.bulkLoop()
	go f.eventLoop()
	go f.watch(cfg.restart)
	return f.srv, f.stop, nil
}

// mtpEnsureConfigfs makes sure the gadget framework is there.
func mtpEnsureConfigfs() error {
	if dirExists(mtpGadgetBase) {
		return nil
	}
	if dirExists(mtpConfigfsRoot) {
		_ = syscall.Mount("none", mtpConfigfsRoot, "configfs", 0, "")
	}
	if !dirExists(mtpGadgetBase) {
		if p, err := exec.LookPath("modprobe"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(ctx, p, "libcomposite").Run()
			cancel()
		}
	}
	if !dirExists(mtpGadgetBase) {
		return errors.New("this firmware's kernel has no USB gadget support")
	}
	return nil
}

func mtpWriteAttr(path, value string) error {
	return os.WriteFile(path, []byte(value), 0o644)
}

func mtpReadAttr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// mtpFirstUDC names the port's device controller.
func mtpFirstUDC() string {
	entries, err := os.ReadDir(mtpUDCClass)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// mtpGadgets lists the gadget directories.
func mtpGadgets() []string {
	entries, err := os.ReadDir(mtpGadgetBase)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(mtpGadgetBase, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// mtpFirstConfig is a gadget's first configuration directory.
func mtpFirstConfig(gadget string) string {
	entries, err := os.ReadDir(filepath.Join(gadget, "configs"))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return filepath.Join(gadget, "configs", names[0])
}

// mtpBind binds gadget to udc, retrying while the firmware's own function
// (adbd) gets ready again after an unbind.
func mtpBind(gadget, udc string) error {
	var err error
	for i := 0; i < mtpBindTries; i++ {
		if err = mtpWriteAttr(filepath.Join(gadget, "UDC"), udc); err == nil {
			return nil
		}
		if mtpReadAttr(filepath.Join(gadget, "UDC")) == udc {
			return nil
		}
		time.Sleep(mtpBindPause)
	}
	return err
}

func mtpUnbind(gadget string) {
	if mtpReadAttr(filepath.Join(gadget, "UDC")) != "" {
		_ = mtpWriteAttr(filepath.Join(gadget, "UDC"), "\n")
	}
}

// mtpClearStale removes what an earlier run left in any gadget: the link,
// the mount, the function, and our own gadget — and the network function
// with the gadget settings it changed (usbnet_linux.go). A firmware gadget
// that held our functions is bound again, so ADB comes back.
func mtpClearStale() {
	type rebind struct{ gadget, udc string }
	var again []rebind
	for _, g := range mtpGadgets() {
		links, _ := filepath.Glob(filepath.Join(g, "configs", "*", mtpFFSFunction))
		touched := len(links) > 0 || len(usbNetStaleLinks(g)) > 0 || usbNetSavedFor(g)
		fn := filepath.Join(g, "functions", mtpFFSFunction)
		own := filepath.Base(g) == mtpOwnGadget
		if !touched && !dirExists(fn) && !own {
			continue
		}
		udc := mtpReadAttr(filepath.Join(g, "UDC"))
		if touched || own {
			mtpUnbind(g)
		}
		for _, l := range links {
			_ = os.Remove(l)
		}
		usbNetClearStale(g)
		if !own && touched {
			again = append(again, rebind{g, udc})
		}
	}
	_ = syscall.Unmount(mtpFFSMount, syscall.MNT_DETACH)
	for _, g := range mtpGadgets() {
		_ = os.Remove(filepath.Join(g, "functions", mtpFFSFunction))
		if filepath.Base(g) == mtpOwnGadget {
			mtpRemoveGadgetTree(g)
		}
	}
	for _, r := range again {
		udc := r.udc
		if udc == "" {
			udc = mtpFirstUDC()
		}
		if udc != "" {
			if err := mtpBind(r.gadget, udc); err != nil {
				logWarn("usbmtp", "could not bind %s again: %v", filepath.Base(r.gadget), err)
			}
		}
	}
}

// mtpRemoveGadgetTree takes our own gadget apart, innermost first: configfs
// directories go away with rmdir only, and a configuration only once the
// OS-descriptor link to it is gone.
func mtpRemoveGadgetTree(g string) {
	if ods, err := os.ReadDir(filepath.Join(g, "os_desc")); err == nil {
		for _, l := range ods {
			if l.Type()&os.ModeSymlink != 0 {
				_ = os.Remove(filepath.Join(g, "os_desc", l.Name()))
			}
		}
	}
	cfgs, _ := filepath.Glob(filepath.Join(g, "configs", "*"))
	for _, c := range cfgs {
		links, _ := os.ReadDir(c)
		for _, l := range links {
			if l.Type()&os.ModeSymlink != 0 {
				_ = os.Remove(filepath.Join(c, l.Name()))
			}
		}
		strs, _ := filepath.Glob(filepath.Join(c, "strings", "*"))
		for _, s := range strs {
			_ = os.Remove(s)
		}
		_ = os.Remove(c)
	}
	fns, _ := filepath.Glob(filepath.Join(g, "functions", "*"))
	for _, fn := range fns {
		_ = os.Remove(fn)
	}
	strs, _ := filepath.Glob(filepath.Join(g, "strings", "*"))
	for _, s := range strs {
		_ = os.Remove(s)
	}
	_ = os.Remove(g)
}

// mtpMakeOwnGadget creates the "jukahub" gadget with one configuration.
func mtpMakeOwnGadget(cfg usbMtpStartConfig) (string, error) {
	g := filepath.Join(mtpGadgetBase, mtpOwnGadget)
	if err := os.Mkdir(g, 0o755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("could not create the USB gadget: %w", err)
	}
	product := cfg.name()
	attrs := [][2]string{
		{"idVendor", "0x1d6b"}, // Linux Foundation
		{"idProduct", "0x0104"},
		{"bcdDevice", "0x0100"},
		{"bcdUSB", "0x0200"},
	}
	for _, a := range attrs {
		if err := mtpWriteAttr(filepath.Join(g, a[0]), a[1]); err != nil {
			return g, err
		}
	}
	str := filepath.Join(g, "strings", "0x409")
	if err := os.MkdirAll(str, 0o755); err != nil {
		return g, err
	}
	_ = mtpWriteAttr(filepath.Join(str, "manufacturer"), "JukaHub")
	_ = mtpWriteAttr(filepath.Join(str, "product"), product)
	_ = mtpWriteAttr(filepath.Join(str, "serialnumber"), cfg.serial)
	c := filepath.Join(g, "configs", "c.1")
	if err := os.MkdirAll(filepath.Join(c, "strings", "0x409"), 0o755); err != nil {
		return g, err
	}
	_ = mtpWriteAttr(filepath.Join(c, "strings", "0x409", "configuration"), "MTP")
	_ = mtpWriteAttr(filepath.Join(c, "MaxPower"), "500")
	return g, nil
}

// attach adds the MTP function to a gadget and binds it.
func (f *mtpFFS) attach(cfg usbMtpStartConfig) error {
	udc := mtpFirstUDC()
	if udc == "" {
		return errors.New("the port has no USB device controller")
	}
	for _, g := range mtpGadgets() {
		if filepath.Base(g) != mtpOwnGadget && mtpReadAttr(filepath.Join(g, "UDC")) != "" {
			f.gadget = g
			f.udc = mtpReadAttr(filepath.Join(g, "UDC"))
			break
		}
	}
	if f.gadget == "" {
		g, err := mtpMakeOwnGadget(cfg)
		f.gadget, f.own, f.udc = g, true, udc
		if err != nil {
			return err
		}
	}
	f.cfg = mtpFirstConfig(f.gadget)
	if f.cfg == "" {
		return errors.New("the USB gadget has no configuration")
	}
	fn := filepath.Join(f.gadget, "functions", mtpFFSFunction)
	if err := os.Mkdir(fn, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("this firmware's kernel has no FunctionFS: %w", err)
	}
	if err := os.MkdirAll(mtpFFSMount, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(mtpFFSInstance, mtpFFSMount, "functionfs", 0, ""); err != nil {
		return fmt.Errorf("could not mount FunctionFS: %w", err)
	}
	var err error
	if f.ep0, err = syscall.Open(filepath.Join(mtpFFSMount, "ep0"), syscall.O_RDWR|syscall.O_CLOEXEC, 0); err != nil {
		return fmt.Errorf("could not open ep0: %w", err)
	}
	if _, err := syscall.Write(f.ep0, mtpFFSDescriptors()); err != nil {
		return fmt.Errorf("the kernel refused the USB descriptors: %w", err)
	}
	if _, err := syscall.Write(f.ep0, mtpFFSStrings()); err != nil {
		return fmt.Errorf("the kernel refused the USB strings: %w", err)
	}
	for _, ep := range []struct {
		name string
		fd   *int
	}{{"ep1", &f.in}, {"ep2", &f.out}, {"ep3", &f.intr}} {
		if *ep.fd, err = syscall.Open(filepath.Join(mtpFFSMount, ep.name), syscall.O_RDWR|syscall.O_CLOEXEC, 0); err != nil {
			*ep.fd = -1
			return fmt.Errorf("could not open %s: %w", ep.name, err)
		}
	}
	if !f.own {
		f.unbound = true
		mtpUnbind(f.gadget)
	}
	if err := os.Symlink(fn, filepath.Join(f.cfg, mtpFFSFunction)); err != nil && !os.IsExist(err) {
		return fmt.Errorf("could not add MTP to the USB gadget: %w", err)
	}
	if cfg.net {
		f.net = usbNetJoin(f.gadget, f.cfg, f.own, cfg.serial)
	}
	if f.net != nil {
		err := mtpBind(f.gadget, f.udc)
		if err == nil {
			f.net.bound()
			if !f.own {
				logInfo("usbmtp", "MTP and the USB network added to the firmware's USB gadget %s", filepath.Base(f.gadget))
			}
			return nil
		}
		// File transfer is never lost to the network function: take it out
		// and bind exactly what was there before. Most likely the controller
		// ran out of endpoints; this boot does not try again.
		logWarn("usbnet", "the USB port would not start with the network function (%v); file transfer only", err)
		mtpUnbind(f.gadget)
		f.net.leave()
		f.net = nil
		if err := mtpBind(f.gadget, f.udc); err != nil {
			return fmt.Errorf("could not switch the USB port on: %w", err)
		}
		usbNetNoteNoRoom(f.gadget, f.own)
	} else if err := mtpBind(f.gadget, f.udc); err != nil {
		return fmt.Errorf("could not switch the USB port on: %w", err)
	}
	if !f.own {
		logInfo("usbmtp", "MTP added to the firmware's USB gadget %s", filepath.Base(f.gadget))
	}
	return nil
}

// stop ends the threads and takes the function out again.
func (f *mtpFFS) stop() {
	if !f.stopping.CompareAndSwap(false, true) {
		return
	}
	close(f.done)
	// Unbinding first makes every pending transfer fail, so the threads
	// wake; the signals cover one still waiting for the port to come up.
	f.bindMu.Lock()
	if f.own || f.unbound {
		mtpUnbind(f.gadget)
	}
	f.bindMu.Unlock()
	finished := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(finished)
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	clean := true
wait:
	for {
		for i := range f.tids {
			f.kick(i)
		}
		select {
		case <-finished:
			break wait
		case <-deadline.C:
			logWarn("usbmtp", "USB threads did not stop; leaving their files open")
			clean = false
			break wait
		case <-tick.C:
		}
	}
	if clean {
		f.closeFiles()
		f.teardown()
		return
	}
	// A thread still inside read or write owns its descriptor; closing it
	// could hand the number to another file. The files stay open, and so
	// does the function: removing a FunctionFS instance under open files
	// frees what they point to on older kernels. Only the port is given
	// back; the next start waits for a restart of JukaHub.
	mtpLeaked.Store(true)
	if f.gadget != "" && f.cfg != "" {
		mtpUnbind(f.gadget)
		_ = os.Remove(filepath.Join(f.cfg, mtpFFSFunction))
		if f.net != nil {
			f.net.leave()
		}
		if !f.own && f.udc != "" {
			_ = mtpBind(f.gadget, f.udc)
		}
	}
}

// kick interrupts the thread in slot i, if it is in a USB transfer.
func (f *mtpFFS) kick(i int) {
	if tid := f.tids[i].Load(); tid != 0 {
		_ = syscall.Tgkill(os.Getpid(), int(tid), syscall.SIGURG)
	}
}

func (f *mtpFFS) closeFiles() {
	for _, fd := range []*int{&f.in, &f.out, &f.intr, &f.ep0} {
		if *fd >= 0 {
			_ = syscall.Close(*fd)
			*fd = -1
		}
	}
}

// teardown removes the function and gives the port back: the firmware's
// gadget is bound again, our own is removed. Each step tolerates a step
// before it that never happened.
func (f *mtpFFS) teardown() {
	if f.gadget != "" && (f.own || f.unbound) {
		mtpUnbind(f.gadget)
	}
	if f.gadget != "" && f.cfg != "" {
		_ = os.Remove(filepath.Join(f.cfg, mtpFFSFunction))
	}
	if f.net != nil {
		f.net.leave()
		f.net = nil
	}
	f.closeFiles()
	_ = syscall.Unmount(mtpFFSMount, syscall.MNT_DETACH)
	if f.gadget == "" {
		return
	}
	_ = os.Remove(filepath.Join(f.gadget, "functions", mtpFFSFunction))
	if f.own {
		mtpRemoveGadgetTree(f.gadget)
		return
	}
	if f.unbound && f.udc != "" && f.giveUp.Load() {
		_ = mtpWriteAttr(filepath.Join(f.gadget, "UDC"), f.udc)
	} else if f.unbound && f.udc != "" {
		if err := mtpBind(f.gadget, f.udc); err != nil {
			logWarn("usbmtp", "could not give the USB port back to %s: %v", filepath.Base(f.gadget), err)
		}
	}
}

// ── Threads ──────────────────────────────────────────────────────────────

// lockThread pins the calling goroutine to its thread for good (the thread
// ends with it) and publishes the thread's ID for kick.
func (f *mtpFFS) lockThread(slot int) {
	runtime.LockOSThread()
	f.tids[slot].Store(int32(syscall.Gettid()))
}

// bulkLoop serves commands until the stop.
func (f *mtpFFS) bulkLoop() {
	defer f.wg.Done()
	f.lockThread(mtpThreadBulk)
	defer f.tids[mtpThreadBulk].Store(0)
	for !f.stopping.Load() {
		err := f.srv.serveOne()
		if err == nil || f.stopping.Load() || errors.Is(err, syscall.EINTR) {
			continue
		}
		// Unplugged, reset by the computer, or out of step with it: start
		// over with the next command. A read waits while the port is down.
		f.srv.endSession()
		select {
		case <-f.done:
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// eventLoop writes queued events to the interrupt pipe.
func (f *mtpFFS) eventLoop() {
	defer f.wg.Done()
	f.lockThread(mtpThreadEvent)
	defer f.tids[mtpThreadEvent].Store(0)
	for {
		select {
		case <-f.done:
			return
		case b := <-f.events:
			if !f.enabled.Load() {
				continue // nobody is listening; the write would wait for the next computer
			}
			_, _ = f.io(f.intr, b, true)
		}
	}
}

// ep0Loop reads the control endpoint's events: the port coming up and down,
// and the MTP class requests (cancel, reset, status).
func (f *mtpFFS) ep0Loop(cfg usbMtpStartConfig) {
	defer f.wg.Done()
	f.lockThread(mtpThreadEP0)
	defer f.tids[mtpThreadEP0].Store(0)
	buf := make([]byte, ffsEventSize*4)
	for !f.stopping.Load() {
		n, err := syscall.Read(f.ep0, buf)
		if err != nil {
			if err == syscall.EINTR || f.stopping.Load() {
				continue
			}
			select {
			case <-f.done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		for off := 0; off+ffsEventSize <= n; off += ffsEventSize {
			f.handleEvent(cfg, buf[off:off+ffsEventSize])
		}
	}
}

func (f *mtpFFS) handleEvent(cfg usbMtpStartConfig, ev []byte) {
	switch ev[8] {
	case ffsEventEnable:
		f.packet.Store(mtpPacketSize(f.udc))
		f.enabled.Store(true)
		if cfg.connected != nil {
			cfg.connected(true)
		}
		if f.net != nil {
			f.net.hostCame(f)
		}
	case ffsEventDisable, ffsEventUnbind:
		was := f.enabled.Swap(false)
		f.srv.reset.Store(true)
		if was && cfg.connected != nil {
			cfg.connected(false)
		}
		if was && f.net != nil {
			f.net.hostLeft()
		}
		// Whatever the computer wrote is flushed to the card now, not
		// whenever the kernel gets round to it.
		go syscall.Sync()
	case ffsEventSuspend:
		if cfg.connected != nil && f.enabled.Load() {
			cfg.connected(false)
		}
	case ffsEventResume:
		if cfg.connected != nil && f.enabled.Load() {
			cfg.connected(true)
		}
	case ffsEventSetup:
		f.handleSetup(ev[:8])
	case ffsEventBind:
	}
}

// handleSetup answers the MTP class requests on the control pipe. Anything
// else is stalled.
func (f *mtpFFS) handleSetup(setup []byte) {
	reqType, req := setup[0], setup[1]
	length := int(binary.LittleEndian.Uint16(setup[6:]))
	switch {
	case reqType == 0x21 && req == 0x64: // cancel
		data := make([]byte, length)
		f.ep0IO(func() { _, _ = syscall.Read(f.ep0, data) })
		f.srv.cancel.Store(true)
		f.kick(mtpThreadBulk)
	case reqType == 0x21 && req == 0x66: // device reset
		f.ep0IO(func() { _, _ = syscall.Read(f.ep0, nil) })
		f.srv.reset.Store(true)
		if f.srv.busy.Load() {
			f.srv.cancel.Store(true)
			f.kick(mtpThreadBulk)
		}
	case reqType == 0xA1 && req == 0x67: // device status
		code := uint16(mtpRespOK)
		if f.srv.busy.Load() && f.srv.cancel.Load() {
			code = mtpRespDeviceBusy
			f.kick(mtpThreadBulk)
		}
		resp := []byte{4, 0, byte(code), byte(code >> 8)}
		if length < len(resp) {
			resp = resp[:length]
		}
		f.ep0IO(func() { _, _ = syscall.Write(f.ep0, resp) })
	default:
		// FunctionFS stalls a request when it is answered in the wrong
		// direction.
		if reqType&0x80 != 0 {
			f.ep0IO(func() { _, _ = syscall.Read(f.ep0, nil) })
		} else {
			f.ep0IO(func() { _, _ = syscall.Write(f.ep0, nil) })
		}
	}
}

// ep0IO runs one answer to a control request on the ep0 thread, which it
// interrupts if the answer is still waiting after half a second. Some
// controllers finish a request's status stage on their own; an
// acknowledgement queued after that would never complete, and ep0 would
// hear nothing more — no cancel, no status — until the stop.
func (f *mtpFFS) ep0IO(answer func()) {
	t := time.AfterFunc(500*time.Millisecond, func() { f.kick(mtpThreadEP0) })
	answer()
	t.Stop()
}

// watch binds the gadget again when something unbound it behind our back —
// adbd closing and reopening its own function does that on some kernels,
// and would leave the port dead for both ADB and file transfer.
//
// When the firmware gadget cannot be bound any more — adbd died, and a
// FunctionFS function with no program behind it blocks the bind — the
// service is started again (restart), which then finds no bound gadget and
// makes its own.
func (f *mtpFFS) watch(restart func()) {
	t := time.NewTicker(mtpWatchInterval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-f.done:
			return
		case <-t.C:
		}
		f.bindMu.Lock()
		if f.stopping.Load() {
			f.bindMu.Unlock()
			return
		}
		rebound, failed := false, false
		if mtpReadAttr(filepath.Join(f.gadget, "UDC")) == "" {
			if err := mtpWriteAttr(filepath.Join(f.gadget, "UDC"), f.udc); err == nil {
				rebound = true
			} else {
				failed = true
			}
		}
		f.bindMu.Unlock()
		switch {
		case rebound:
			failures = 0
			logInfo("usbmtp", "USB gadget %s was unbound; bound it again", filepath.Base(f.gadget))
		case failed:
			failures++
			if failures == 1 {
				logWarn("usbmtp", "USB gadget %s is unbound and will not bind", filepath.Base(f.gadget))
			}
			if failures >= 5 && !f.own && restart != nil {
				logWarn("usbmtp", "starting USB file transfer again on a gadget of its own")
				f.giveUp.Store(true)
				restart()
				return
			}
		default:
			failures = 0
		}
	}
}

// ── Card and identity ────────────────────────────────────────────────────

// mtpDiskSpace is the card's size and the room left on it.
func mtpDiskSpace(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, true
}

// usbMtpSerial is a serial number that stays the same across starts, so
// the computer recognises the handheld instead of installing it again: a
// hash of the chip ID or machine ID, never the ID itself.
func usbMtpSerial() string {
	var seed string
	for _, p := range []string{"/sys/class/sunxi_info/sys_info", "/etc/machine-id", "/proc/cpuinfo"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := string(b)
		for _, line := range strings.Split(text, "\n") {
			l := strings.ToLower(line)
			if strings.Contains(l, "chipid") || strings.HasPrefix(l, "serial") {
				seed = line
				break
			}
		}
		if seed == "" && p == "/etc/machine-id" {
			seed = strings.TrimSpace(text)
		}
		if seed != "" {
			break
		}
	}
	if seed == "" {
		return "JUKAHUB0001"
	}
	sum := sha1.Sum([]byte("jukahub-mtp:" + seed))
	return strings.ToUpper(hex.EncodeToString(sum[:8]))
}
