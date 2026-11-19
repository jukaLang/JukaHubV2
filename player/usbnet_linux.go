//go:build linux

package main

// usbnet_linux.go — the USB plumbing under "Browser over USB" (usbnet.go): a
// network function in the gadget USB file transfer sets up
// (usbmtp_linux.go), the address on its interface, and the DHCP server
// (usbnet_dhcp.go) that gives the computer one too.
//
// One configuration only. Windows treats a device as composite — a driver
// per interface — only when it has a single configuration, so the usual
// "RNDIS in one configuration, ECM in another" recipe would cost file
// transfer and ADB on Windows. The network function sits next to them
// instead (adb 0, MTP 1, network 2-3 on the firmware's gadget):
//
//   - RNDIS first. Windows 10/11 bind their own driver to it when the gadget
//     carries Microsoft OS descriptors (os_desc: vendor code 0xcd, "MSFT100",
//     the configuration linked in; interface.rndis compatible "RNDIS",
//     sub-compatible "5162001") and says it uses interface associations
//     (device class EF/02/01). Linux binds rndis_host.
//   - macOS has no RNDIS driver. When the computer set the device up but sent
//     nothing over the network for usbNetQuietWait, the function is swapped
//     for the next one the kernel has (ECM, which macOS and Linux bind) and
//     the port re-enumerates; another silent computer swaps it back. NCM is
//     only tried when RNDIS or ECM is not built.
//
// The controller has few endpoints; when the gadget will not bind with the
// network function, it is taken out and the port is bound exactly as before
// (file transfer and whatever the firmware had), and that is remembered for
// this boot. Everything changed on the firmware's gadget — device class,
// bcdDevice (a new revision makes Windows read the OS descriptors instead of
// a cached "none"), the os_desc settings — is saved to a file first, put back
// on the way out, and by the next start after a crash.

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	usbNetInstance   = "jukanet"
	usbNetStateFile  = "jukahub-usbnet.state"
	usbNetNoRoomFile = "jukahub-usbnet-noroom"
	// usbNetBcdDevice must be valid BCD: the gadget framework refuses others.
	usbNetBcdDevice = "0x0626"
	usbNetQuietWait = 20 * time.Second
)

// usbNetKindsAll are the network functions in the order they are tried.
var usbNetKindsAll = []string{"rndis", "ecm", "ncm"}

// usbNetDeviceAttrs are the gadget settings the link changes and puts back.
var usbNetDeviceAttrs = []string{"bDeviceClass", "bDeviceSubClass", "bDeviceProtocol", "bcdDevice"}

// usbNetKernel is what the kernel offers, probed once per run, and the
// function a computer last talked over (the next start begins with it).
var usbNetKernel struct {
	mu       sync.Mutex
	probed   bool
	kinds    []string
	lastGood string
	noRoom   map[string]bool // gadgets that did not fit it, when the boot cannot be told apart
}

// usbNetSaved is a gadget's settings before the link changed them.
type usbNetSaved struct {
	gadget    string
	attrs     map[string]string
	osTouched bool
	osUse     string
	osVendor  string
	osSign    string
	osLink    string // the configuration link added to os_desc, "" when none
}

// usbNetLink is the network function in one running gadget.
type usbNetLink struct {
	gadget, cfg string
	own         bool
	serial      string
	kinds       []string
	saved       usbNetSaved

	// opMu keeps one change of the function at a time: bringing it up, a
	// swap, leaving. Order: opMu, then the transport's bindMu, then mu.
	opMu sync.Mutex

	mu         sync.Mutex
	left       bool
	kind       int    // index into kinds of the linked function, -1 when none
	fn         string // its directory, "" when none
	ifname     string
	dhcp       *usbNetDHCPServer
	swaps      int       // swaps since a computer last talked, or since an unplug
	checkGen   int       // ends a pending silence check
	heard      bool      // the computer sent something since it set the device up
	rxBase     uint64    // received packets when it did
	rxKnown    bool      // rxBase was read
	quietUntil time.Time // a disconnect before this is the swap's own
}

// ── Joining the gadget ───────────────────────────────────────────────────

// usbNetJoin adds the network function to the gadget, which is unbound and
// already links MTP. nil means no link this time; the status says why.
func usbNetJoin(gadget, cfg string, own bool, serial string) *usbNetLink {
	if usbNetNoRoomThisBoot(gadget) {
		usbNetSetUnavailable(usbNetNoRoomReason(own))
		return nil
	}
	if fw := usbNetFirmwareFunction(cfg); fw != "" {
		logInfo("usbnet", "the firmware's gadget already has %s; not adding another", fw)
		usbNetSetUnavailable("The firmware already runs its own network over USB")
		return nil
	}
	kinds := usbNetKinds(gadget)
	if len(kinds) == 0 {
		usbNetSetUnavailable("This firmware's kernel has no USB network function")
		return nil
	}
	l := &usbNetLink{gadget: gadget, cfg: cfg, own: own, serial: serial, kinds: kinds, kind: -1}
	l.saveAndSet()
	first := 0
	usbNetKernel.mu.Lock()
	for i, k := range kinds {
		if k == usbNetKernel.lastGood {
			first = i
		}
	}
	usbNetKernel.mu.Unlock()
	if err := l.addFunction(first); err != nil {
		logWarn("usbnet", "could not add %s: %v", kinds[first], err)
		l.restore()
		usbNetSetStatus(usbNetNone, "", "Could not add the USB network: "+firstLine(err.Error()))
		return nil
	}
	return l
}

// usbNetNoRoomReason is the Settings line when the endpoints ran out.
func usbNetNoRoomReason(own bool) string {
	if own {
		return "Not enough USB endpoints next to file transfer — file transfer only"
	}
	return "Not enough USB endpoints next to adb and file transfer — file transfer only"
}

// usbNetFirmwareFunction names a network function the firmware already
// linked into cfg, "" when there is none.
func usbNetFirmwareFunction(cfg string) string {
	entries, err := os.ReadDir(cfg)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(filepath.Join(cfg, e.Name()))
		if err != nil {
			continue
		}
		base := strings.ToLower(filepath.Base(target))
		if strings.HasSuffix(base, "."+usbNetInstance) {
			continue
		}
		for _, p := range []string{"rndis.", "ecm.", "ncm.", "eem.", "geth.", "gsi.rndis", "gsi.ecm", "gsi.ncm"} {
			if strings.HasPrefix(base, p) {
				return base
			}
		}
	}
	return ""
}

// usbNetKinds lists the network functions the kernel has, at most two:
// RNDIS and ECM, with NCM standing in for one that is missing.
func usbNetKinds(gadget string) []string {
	usbNetKernel.mu.Lock()
	defer usbNetKernel.mu.Unlock()
	if usbNetKernel.probed {
		return usbNetKernel.kinds
	}
	var have []string
	for _, k := range usbNetKindsAll {
		if len(have) < 2 && usbNetProbeKind(gadget, k) {
			have = append(have, k)
		}
	}
	usbNetKernel.probed, usbNetKernel.kinds = true, have
	if len(have) == 0 {
		logInfo("usbnet", "the kernel has no RNDIS, ECM or NCM gadget function")
	} else {
		logInfo("usbnet", "USB network functions: %s", strings.Join(have, ", "))
	}
	return have
}

// usbNetProbeKind makes and removes a function of kind: ENOENT means the
// kernel was built without it (the module is asked for once).
func usbNetProbeKind(gadget, kind string) bool {
	fn := filepath.Join(gadget, "functions", kind+"."+usbNetInstance)
	try := func() error {
		err := os.Mkdir(fn, 0o755)
		if err == nil || os.IsExist(err) {
			_ = os.Remove(fn)
			return nil
		}
		return err
	}
	err := try()
	if err != nil && errors.Is(err, os.ErrNotExist) {
		if p, lookErr := exec.LookPath("modprobe"); lookErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(ctx, p, "usb_f_"+kind).Run()
			cancel()
			err = try()
		}
	}
	return err == nil
}

// saveAndSet records the gadget's settings (to a file first, for a crash)
// and sets what Windows needs: interface associations, a revision of our
// own, Microsoft OS descriptors — unless the firmware has those on already.
func (l *usbNetLink) saveAndSet() {
	s := usbNetSaved{gadget: l.gadget, attrs: map[string]string{}}
	for _, a := range usbNetDeviceAttrs {
		if v := mtpReadAttr(filepath.Join(l.gadget, a)); v != "" {
			s.attrs[a] = v
		}
	}
	od := filepath.Join(l.gadget, "os_desc")
	if dirExists(od) && !usbNetOSDescOn(od) {
		s.osTouched = true
		s.osUse = mtpReadAttr(filepath.Join(od, "use"))
		s.osVendor = mtpReadAttr(filepath.Join(od, "b_vendor_code"))
		s.osSign = mtpReadAttr(filepath.Join(od, "qw_sign"))
	}
	usbNetWriteSaved(s)
	_ = mtpWriteAttr(filepath.Join(l.gadget, "bDeviceClass"), "0xEF")
	_ = mtpWriteAttr(filepath.Join(l.gadget, "bDeviceSubClass"), "0x02")
	_ = mtpWriteAttr(filepath.Join(l.gadget, "bDeviceProtocol"), "0x01")
	if err := mtpWriteAttr(filepath.Join(l.gadget, "bcdDevice"), usbNetBcdDevice); err != nil {
		logDebug("usbnet", "bcdDevice: %v", err)
	}
	if s.osTouched {
		_ = mtpWriteAttr(filepath.Join(od, "b_vendor_code"), "0xcd")
		_ = mtpWriteAttr(filepath.Join(od, "qw_sign"), "MSFT100")
		_ = mtpWriteAttr(filepath.Join(od, "use"), "1")
		// The link is written down before it is made, so a crash right
		// after it still has it taken out; one already there is not ours.
		link := filepath.Join(od, filepath.Base(l.cfg))
		if _, err := os.Lstat(link); err != nil {
			s.osLink = filepath.Base(l.cfg)
			usbNetWriteSaved(s)
			if err := os.Symlink(l.cfg, link); err != nil {
				s.osLink = ""
				usbNetWriteSaved(s)
				logWarn("usbnet", "no Microsoft OS descriptors (%v): Windows may not find a driver", err)
			}
		}
	}
	l.saved = s
}

// usbNetOSDescOn reports whether a gadget already serves Microsoft OS
// descriptors for one of its configurations.
func usbNetOSDescOn(od string) bool {
	use := mtpReadAttr(filepath.Join(od, "use"))
	if use != "1" && !strings.EqualFold(use, "y") {
		return false
	}
	entries, _ := os.ReadDir(od)
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// addFunction makes the function of kinds[i] and links it after MTP.
func (l *usbNetLink) addFunction(i int) error {
	kind := l.kinds[i]
	fn := filepath.Join(l.gadget, "functions", kind+"."+usbNetInstance)
	if err := os.Mkdir(fn, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	// Fixed addresses: the computer sees the same network adapter each time
	// instead of a new one ("Network 2", "Network 3"…).
	dev, host := usbNetMACs(l.serial)
	_ = mtpWriteAttr(filepath.Join(fn, "dev_addr"), dev)
	_ = mtpWriteAttr(filepath.Join(fn, "host_addr"), host)
	if kind == "rndis" {
		in := filepath.Join(fn, "os_desc", "interface.rndis")
		if dirExists(in) {
			_ = mtpWriteAttr(filepath.Join(in, "compatible_id"), "RNDIS")
			_ = mtpWriteAttr(filepath.Join(in, "sub_compatible_id"), "5162001")
		} else {
			logInfo("usbnet", "this kernel's RNDIS has no OS descriptors: Windows may ask for a driver")
		}
		// RNDIS sits after adb and MTP here, and Windows is reported to
		// start it by OS descriptor alone only as the first function. The
		// wireless-controller class (E0/01/03, as Android tethering uses)
		// is matched by Windows' own RNDIS driver and by Linux's anywhere
		// in the configuration — when this kernel lets it be set.
		if _, err := os.Stat(filepath.Join(fn, "wceis")); err == nil {
			_ = mtpWriteAttr(filepath.Join(fn, "wceis"), "1")
		} else if _, err := os.Stat(filepath.Join(fn, "class")); err == nil {
			_ = mtpWriteAttr(filepath.Join(fn, "class"), "e0")
			_ = mtpWriteAttr(filepath.Join(fn, "subclass"), "01")
			_ = mtpWriteAttr(filepath.Join(fn, "protocol"), "03")
		}
	}
	if err := os.Symlink(fn, filepath.Join(l.cfg, filepath.Base(fn))); err != nil && !os.IsExist(err) {
		_ = os.Remove(fn)
		return err
	}
	l.mu.Lock()
	l.kind, l.fn, l.ifname = i, fn, ""
	l.mu.Unlock()
	return nil
}

// removeFunction stops the DHCP server and takes the function out (the
// gadget is unbound). Its network interface goes with it.
func (l *usbNetLink) removeFunction() {
	l.mu.Lock()
	fn, srv := l.fn, l.dhcp
	l.fn, l.kind, l.ifname, l.dhcp = "", -1, "", nil
	l.mu.Unlock()
	if srv != nil {
		srv.stop()
	}
	if fn == "" {
		return
	}
	_ = os.Remove(filepath.Join(l.cfg, filepath.Base(fn)))
	if err := os.Remove(fn); err != nil && !os.IsNotExist(err) {
		logWarn("usbnet", "could not remove %s: %v", filepath.Base(fn), err)
	}
}

// restore puts the gadget's settings back and forgets the saved copy.
func (l *usbNetLink) restore() {
	usbNetRestoreSaved(l.saved)
	usbNetDropSaved()
}

// leave takes the network out for good (the gadget is unbound): the
// function, then the gadget's settings.
func (l *usbNetLink) leave() {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	l.mu.Lock()
	l.left = true
	l.checkGen++
	l.mu.Unlock()
	l.removeFunction()
	l.restore()
	usbNetSetStatus(usbNetOff, "", "")
}

// ── Up and running ───────────────────────────────────────────────────────

// bound is called once the gadget is bound with the function in it.
func (l *usbNetLink) bound() {
	go func() {
		l.opMu.Lock()
		defer l.opMu.Unlock()
		l.bringUpLocked()
	}()
}

// bringUpLocked gives the function's interface its address and starts the
// DHCP server on it (opMu held).
func (l *usbNetLink) bringUpLocked() {
	l.mu.Lock()
	fn, kind, left := l.fn, l.kind, l.left
	l.mu.Unlock()
	if left || fn == "" || kind < 0 {
		return
	}
	name := ""
	for i := 0; i < 30 && name == ""; i++ {
		n := mtpReadAttr(filepath.Join(fn, "ifname"))
		if n != "" && !strings.Contains(n, "%") && dirExists(filepath.Join("/sys/class/net", n)) {
			name = n
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if name == "" {
		logWarn("usbnet", "the USB network interface did not appear")
		usbNetSetStatus(usbNetNone, "", "The USB network interface did not appear")
		return
	}
	if err := usbNetIfaceUp(name); err != nil {
		logWarn("usbnet", "could not set up %s: %v", name, err)
		usbNetSetStatus(usbNetNone, "", "Could not give the USB network its address")
		return
	}
	srv, err := usbNetDHCPStart(name, l.noteHeard)
	if err != nil {
		// The computer can still be given 10.73.73.2/29 by hand.
		logWarn("usbnet", "DHCP server on %s: %v", name, err)
	}
	l.mu.Lock()
	l.ifname, l.dhcp = name, srv
	heard := l.heard
	l.mu.Unlock()
	usbNetSetStatus(usbNetUp, strings.ToUpper(l.kinds[kind]), "")
	usbNetSetHost(heard)
	logInfo("usbnet", "%s up on %s at %s/%d", strings.ToUpper(l.kinds[kind]), name, usbNetDeviceIP(), usbNetPrefixLen)
}

// noteHeard is the DHCP server telling that the computer spoke.
func (l *usbNetLink) noteHeard() {
	l.mu.Lock()
	first := !l.heard
	l.heard = true
	l.mu.Unlock()
	if first {
		usbNetSetHost(true)
	}
}

// hostCame: the computer set the device up (ep0 thread; must not block).
// Unless it talks over the network within usbNetQuietWait, the function may
// be the wrong one for it.
func (l *usbNetLink) hostCame(f *mtpFFS) {
	l.mu.Lock()
	l.checkGen++
	gen := l.checkGen
	l.heard = false
	l.rxBase, l.rxKnown = 0, false
	if l.ifname != "" {
		l.rxBase, l.rxKnown = usbNetRxPackets(l.ifname)
	}
	l.mu.Unlock()
	usbNetSetHost(false)
	go l.checkQuiet(f, gen)
}

// hostLeft: the computer let go of the device (ep0 thread). A disconnect the
// swap did not cause is an unplug: the next computer gets its own tries.
func (l *usbNetLink) hostLeft() {
	l.mu.Lock()
	l.checkGen++
	l.heard = false
	if time.Now().After(l.quietUntil) {
		l.swaps = 0
	}
	l.mu.Unlock()
	usbNetSetHost(false)
}

// checkQuiet swaps the function when the computer that set the device up
// stays silent on it, up to once per function the kernel has.
func (l *usbNetLink) checkQuiet(f *mtpFFS, gen int) {
	wait := usbNetQuietWait
	for {
		select {
		case <-f.done:
			return
		case <-time.After(wait):
		}
		l.mu.Lock()
		if gen != l.checkGen || l.left || l.kind < 0 {
			l.mu.Unlock()
			return
		}
		heard := l.heard
		if !heard && l.ifname != "" {
			if n, ok := usbNetRxPackets(l.ifname); ok && (!l.rxKnown || n != l.rxBase) && n > 0 {
				heard = true
			}
		}
		if heard {
			l.heard = true
			l.swaps = 0
			kind := l.kinds[l.kind]
			l.mu.Unlock()
			usbNetSetHost(true)
			usbNetKernel.mu.Lock()
			usbNetKernel.lastGood = kind
			usbNetKernel.mu.Unlock()
			return
		}
		more := l.swaps < len(l.kinds) && len(l.kinds) > 1
		l.mu.Unlock()
		if !more || !f.enabled.Load() {
			return
		}
		if f.srv != nil && f.srv.busy.Load() {
			// A file is on its way; the swap would cut it off.
			wait = 5 * time.Second
			continue
		}
		l.swap(f, gen)
		return
	}
}

// swap replaces the function with the next kind and binds the port again.
// If the port will not come up with it, file transfer comes back alone.
func (l *usbNetLink) swap(f *mtpFFS, gen int) {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	l.mu.Lock()
	if gen != l.checkGen || l.left || l.kind < 0 {
		l.mu.Unlock()
		return
	}
	from := l.kinds[l.kind]
	next := (l.kind + 1) % len(l.kinds)
	l.swaps++
	l.quietUntil = time.Now().Add(15 * time.Second)
	l.mu.Unlock()

	f.bindMu.Lock()
	if f.stopping.Load() {
		f.bindMu.Unlock()
		return
	}
	logInfo("usbnet", "the computer did not use %s; trying %s", strings.ToUpper(from), strings.ToUpper(l.kinds[next]))
	usbNetSetStatus(usbNetStarting, "", "")
	mtpUnbind(f.gadget)
	l.removeFunction()
	err := l.addFunction(next)
	if err == nil {
		err = mtpBind(f.gadget, f.udc)
	}
	if err != nil {
		logWarn("usbnet", "the USB port would not start with %s (%v); file transfer only", l.kinds[next], err)
		mtpUnbind(f.gadget)
		l.removeFunction()
		if bindErr := mtpBind(f.gadget, f.udc); bindErr != nil {
			logWarn("usbmtp", "could not switch the USB port on again: %v", bindErr)
		}
		f.bindMu.Unlock()
		usbNetSetStatus(usbNetNone, "", usbNetNoRoomReason(l.own))
		return
	}
	f.bindMu.Unlock()
	l.mu.Lock()
	l.quietUntil = time.Now().Add(5 * time.Second)
	l.mu.Unlock()
	l.bringUpLocked()
}

// ── Interface and DHCP socket ────────────────────────────────────────────

// usbNetIfaceUp puts 10.73.73.1/29 on the interface and brings it up:
// ioctls, else ip, else ifconfig.
func usbNetIfaceUp(name string) error {
	err := usbNetIfaceIoctl(name)
	if err == nil {
		return nil
	}
	cidr := usbNetDeviceIP() + "/" + strconv.Itoa(usbNetPrefixLen)
	if ip := hotspotFindTool("ip"); ip != "" {
		// The interface is the link's own. An ioctl that set the address
		// but not the mask left it on 10.0.0.0/8, which must not stay
		// next to the /29.
		_, _ = hotspotExec(5*time.Second, ip, "addr", "flush", "dev", name)
		out, e := hotspotExec(5*time.Second, ip, "addr", "add", cidr, "dev", name)
		if e == nil || strings.Contains(out, "File exists") {
			if _, e = hotspotExec(5*time.Second, ip, "link", "set", name, "up"); e == nil {
				return nil
			}
		}
	}
	if ifc := hotspotFindTool("ifconfig"); ifc != "" {
		mask := usbNetMaskAddr()
		if _, e := hotspotExec(5*time.Second, ifc, name, usbNetDeviceIP(), "netmask", net.IP(mask[:]).String(), "up"); e == nil {
			return nil
		}
	}
	return err
}

func usbNetIfaceIoctl(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	set := func(req uint, addr [4]byte) error {
		ifr, err := unix.NewIfreq(name)
		if err != nil {
			return err
		}
		if err := ifr.SetInet4Addr(addr[:]); err != nil {
			return err
		}
		return unix.IoctlIfreq(fd, req, ifr)
	}
	if err := set(unix.SIOCSIFADDR, usbNetDeviceAddr); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	if err := set(unix.SIOCSIFNETMASK, usbNetMaskAddr()); err != nil {
		return fmt.Errorf("netmask: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

// usbNetRxPackets is how many packets the interface has received.
func usbNetRxPackets(ifname string) (uint64, bool) {
	n, err := strconv.ParseUint(mtpReadAttr(filepath.Join("/sys/class/net", ifname, "statistics", "rx_packets")), 10, 64)
	return n, err == nil
}

// usbNetDHCPServer is the DHCP socket on the cable's interface.
type usbNetDHCPServer struct {
	conn net.PacketConn
	pool *usbNetDHCPPool
}

// usbNetDHCPStart listens on UDP 67 bound to ifname (root, which JukaHub is
// on the handheld). heard is called for each well-formed request.
func usbNetDHCPStart(ifname string, heard func()) (*usbNetDHCPServer, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); serr != nil {
				return
			}
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); serr != nil {
				return
			}
			serr = syscall.BindToDevice(int(fd), ifname)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := lc.ListenPacket(ctx, "udp4", "0.0.0.0:67")
	if err != nil {
		return nil, err
	}
	s := &usbNetDHCPServer{conn: conn, pool: newUSBNetDHCPPool()}
	go s.serve(heard)
	return s, nil
}

func (s *usbNetDHCPServer) serve(heard func()) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("usbnet", "DHCP server stopped: %v", r)
		}
	}()
	// One byte more than the longest packet read, so a longer one is seen
	// as such and dropped instead of read cut short.
	buf := make([]byte, usbNetDHCPMaxLen+1)
	bcast := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	for {
		n, _, err := s.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		req, ok := usbNetDHCPParse(buf[:n])
		if !ok {
			continue
		}
		if heard != nil {
			heard()
		}
		reply, to := s.pool.handle(req, time.Now())
		if reply == nil {
			continue
		}
		dst := bcast
		if to != ([4]byte{}) {
			dst = &net.UDPAddr{IP: net.IPv4(to[0], to[1], to[2], to[3]), Port: 68}
		}
		if _, err := s.conn.WriteTo(reply, dst); err != nil {
			logDebug("usbnet", "DHCP reply failed: %v", err)
		}
	}
}

func (s *usbNetDHCPServer) stop() { _ = s.conn.Close() }

// usbNetMACs are the two Ethernet addresses of the link, the handheld's and
// the computer's: locally administered, from the USB serial number, so they
// stay the same across starts.
func usbNetMACs(serial string) (dev, host string) {
	sum := sha1.Sum([]byte("jukahub-usbnet:" + serial))
	d := net.HardwareAddr{sum[0]&0xfc | 0x02, sum[1], sum[2], sum[3], sum[4], sum[5]}
	h := net.HardwareAddr{sum[6]&0xfc | 0x02, sum[7], sum[8], sum[9], sum[10], sum[11]}
	return d.String(), h.String()
}

// ── Saved settings and this boot's memory ────────────────────────────────

func usbNetBootID() string { return mtpReadAttr("/proc/sys/kernel/random/boot_id") }

// usbNetRunFile is a run-state file in JukaHub's data folder (not the
// shared temporary folder, where another program could plant a link for
// root to write through), "" when there is none. Each is checked against
// the boot ID when read back.
func usbNetRunFile(name string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func usbNetStatePath() string { return usbNetRunFile(usbNetStateFile) }

// usbNetWriteSaved stores s for a start after a crash.
func usbNetWriteSaved(s usbNetSaved) {
	path := usbNetStatePath()
	if path == "" {
		return
	}
	var b strings.Builder
	b.WriteString("boot=" + usbNetBootID() + "\n")
	b.WriteString("gadget=" + s.gadget + "\n")
	for _, a := range usbNetDeviceAttrs {
		if v, ok := s.attrs[a]; ok {
			b.WriteString("attr." + a + "=" + v + "\n")
		}
	}
	if s.osTouched {
		b.WriteString("os.use=" + s.osUse + "\n")
		b.WriteString("os.b_vendor_code=" + s.osVendor + "\n")
		b.WriteString("os.qw_sign=" + s.osSign + "\n")
		b.WriteString("os.link=" + s.osLink + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		logDebug("usbnet", "could not save the gadget's settings: %v", err)
	}
}

// usbNetLoadSaved reads the settings an earlier run of this boot saved.
func usbNetLoadSaved() (usbNetSaved, bool) {
	s := usbNetSaved{attrs: map[string]string{}}
	path := usbNetStatePath()
	if path == "" {
		return s, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return s, false
	}
	boot := ""
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch {
		case k == "boot":
			boot = v
		case k == "gadget":
			s.gadget = v
		case strings.HasPrefix(k, "attr."):
			for _, a := range usbNetDeviceAttrs {
				if k == "attr."+a {
					s.attrs[a] = v
				}
			}
		case k == "os.use":
			s.osTouched, s.osUse = true, v
		case k == "os.b_vendor_code":
			s.osVendor = v
		case k == "os.qw_sign":
			s.osSign = v
		case k == "os.link":
			s.osLink = v
		}
	}
	// Only a gadget directory, and only this boot's: configfs starts empty
	// after a reboot.
	if boot == "" || boot != usbNetBootID() || filepath.Dir(s.gadget) != mtpGadgetBase ||
		strings.ContainsAny(s.osLink, "/\\") || s.osLink == "." || s.osLink == ".." {
		return s, false
	}
	return s, true
}

func usbNetDropSaved() {
	if path := usbNetStatePath(); path != "" {
		_ = os.Remove(path)
	}
}

// usbNetRestoreSaved writes s back to its gadget (unbound).
func usbNetRestoreSaved(s usbNetSaved) {
	if s.gadget == "" {
		return
	}
	od := filepath.Join(s.gadget, "os_desc")
	if s.osLink != "" {
		_ = os.Remove(filepath.Join(od, s.osLink))
	}
	if s.osTouched {
		for _, kv := range [][2]string{{"use", s.osUse}, {"b_vendor_code", s.osVendor}, {"qw_sign", s.osSign}} {
			if kv[1] != "" {
				_ = mtpWriteAttr(filepath.Join(od, kv[0]), kv[1])
			}
		}
	}
	for _, a := range usbNetDeviceAttrs {
		if v, ok := s.attrs[a]; ok {
			_ = mtpWriteAttr(filepath.Join(s.gadget, a), v)
		}
	}
}

// usbNetStaleLinks are the network functions an earlier run linked into g.
func usbNetStaleLinks(g string) []string {
	var out []string
	for _, k := range usbNetKindsAll {
		links, _ := filepath.Glob(filepath.Join(g, "configs", "*", k+"."+usbNetInstance))
		out = append(out, links...)
	}
	return out
}

// usbNetSavedFor reports whether a crashed run left g's settings changed.
func usbNetSavedFor(g string) bool {
	s, ok := usbNetLoadSaved()
	return ok && s.gadget == g
}

// usbNetClearStale takes out what an earlier run's link left in g (unbound
// by the caller) and puts g's saved settings back.
func usbNetClearStale(g string) {
	for _, l := range usbNetStaleLinks(g) {
		_ = os.Remove(l)
	}
	for _, k := range usbNetKindsAll {
		_ = os.Remove(filepath.Join(g, "functions", k+"."+usbNetInstance))
	}
	if s, ok := usbNetLoadSaved(); ok && s.gadget == g {
		usbNetRestoreSaved(s)
		usbNetDropSaved()
	}
}

// usbNetNoRoomThisBoot reports whether the gadget would not bind with the
// network function earlier in this boot.
func usbNetNoRoomThisBoot(gadget string) bool {
	name := filepath.Base(gadget)
	usbNetKernel.mu.Lock()
	known := usbNetKernel.noRoom[name]
	usbNetKernel.mu.Unlock()
	if known {
		return true
	}
	boot, path := usbNetBootID(), usbNetRunFile(usbNetNoRoomFile)
	if boot == "" || path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == boot+" "+name {
			return true
		}
	}
	return false
}

// usbNetNoteNoRoom remembers for this boot that gadget did not fit the
// network function, and says so in Settings.
func usbNetNoteNoRoom(gadget string, own bool) {
	name := filepath.Base(gadget)
	usbNetKernel.mu.Lock()
	if usbNetKernel.noRoom == nil {
		usbNetKernel.noRoom = map[string]bool{}
	}
	usbNetKernel.noRoom[name] = true
	usbNetKernel.mu.Unlock()
	if boot, path := usbNetBootID(), usbNetRunFile(usbNetNoRoomFile); boot != "" && path != "" {
		old, _ := os.ReadFile(path)
		if len(old) > 2048 {
			old = nil
		}
		_ = os.WriteFile(path, append(old, []byte(boot+" "+name+"\n")...), 0o600)
	}
	usbNetSetUnavailable(usbNetNoRoomReason(own))
}
