//go:build linux

package main

// System calls for each step the shell init ran as a command: mount, the card
// search, read-ahead, the writable copies (cp -a), hostname, GPIO, insmod
// (finit_module), the kernel log; and the timeline and the logs.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ── time and logs ──────────────────────────────────────────────────────────

// bootNow is CLOCK_BOOTTIME in nanoseconds: /proc/uptime's clock.
func bootNow() int64 {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts) != nil {
		if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
			return 0
		}
	}
	return int64(ts.Sec)*1e9 + int64(ts.Nsec)
}

// secs is a boot time as /proc/uptime writes it (seconds, hundredths).
func secs(ns int64) string {
	return strconv.FormatFloat(float64(ns)/1e9, 'f', 2, 64)
}

type tlEntry struct {
	name       string
	start, end int64 // end 0: a moment, not a span
}

// timeline is every step with its CLOCK_BOOTTIME times, kept in memory and
// written to os.log once the boot is done (or at a shutdown before that).
type timeline struct {
	mu      sync.Mutex
	entries []tlEntry
}

func (t *timeline) span(name string, start, end int64) {
	t.mu.Lock()
	t.entries = append(t.entries, tlEntry{name: name, start: start, end: end})
	t.mu.Unlock()
}

func (t *timeline) mark(name string) {
	t.span(name, bootNow(), 0)
}

// begin starts a step; the returned func ends it.
func (t *timeline) begin(name string) func() {
	start := bootNow()
	return func() { t.span(name, start, bootNow()) }
}

func (t *timeline) text() string {
	t.mu.Lock()
	entries := append([]tlEntry(nil), t.entries...)
	t.mu.Unlock()
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].start < entries[b].start })
	var sb strings.Builder
	sb.WriteString("timeline (seconds since boot, CLOCK_BOOTTIME; how long each step took):")
	for _, e := range entries {
		if e.end > 0 {
			fmt.Fprintf(&sb, "\n  %8.3f  %9.1f ms  %s", float64(e.start)/1e9, float64(e.end-e.start)/1e6, e.name)
		} else {
			fmt.Fprintf(&sb, "\n  %8.3f               %s", float64(e.start)/1e9, e.name)
		}
	}
	return sb.String()
}

// cardLog is os.log on the card: lines wait in memory until the card is
// mounted (open), then go straight to the file, opened and closed per line as
// the shell's ">>" did, so nothing holds the card at a shutdown.
type cardLog struct {
	mu      sync.Mutex
	path    string
	pending []string
}

func (l *cardLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		if len(l.pending) < 400 {
			l.pending = append(l.pending, line)
		}
		return
	}
	appendLine(l.path, line)
}

// open keeps the previous boot's log as .1 and writes what waited.
func (l *cardLog) open(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if isFile(path) {
		_ = os.Rename(path, path+".1")
	}
	var sb strings.Builder
	for _, line := range l.pending {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	_ = os.WriteFile(path, []byte(sb.String()), 0o644)
	l.path = path
	l.pending = nil
}

// close stops writing to the card (before it is unmounted).
func (l *cardLog) close() {
	l.mu.Lock()
	l.path = ""
	l.mu.Unlock()
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}

var (
	kmsgMu   sync.Mutex
	kmsgFile *os.File
)

// kmsg writes one line to the kernel log (dmesg); nothing before /dev is up.
func kmsg(line string) {
	kmsgMu.Lock()
	defer kmsgMu.Unlock()
	if kmsgFile == nil {
		f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0)
		if err != nil {
			return
		}
		kmsgFile = f
	}
	if len(line) > 900 {
		line = line[:900]
	}
	_, _ = kmsgFile.WriteString(line + "\n")
}

// tty0 writes a line on the panel's text console (the fatal message).
func tty0(line string) {
	f, err := os.OpenFile("/dev/tty0", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}

// ── small file helpers ─────────────────────────────────────────────────────

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isCharDev(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// firstLine is a file's first line without the blanks around it, or "" (the
// shell's "read -r WORD < FILE").
func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return strings.TrimSpace(line)
}

func writeFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	value, found := "", false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value, found = kv[len(prefix):], true
		}
	}
	return value, found
}

// lookPath finds a command without a slash in path, as sh's exec does.
func lookPath(name, path string) string {
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			dir = "."
		}
		candidate := dir + "/" + name
		st, err := os.Stat(candidate)
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func syncAll() {
	unix.Sync()
}

func execInit(path string, args, env []string) error {
	return syscall.Exec(path, append([]string{path}, args...), env)
}

// ── mounts ─────────────────────────────────────────────────────────────────

func (b *booter) addMount(target string) {
	b.mountsMu.Lock()
	b.mounts = append(b.mounts, target)
	b.mountsMu.Unlock()
}

// mount mounts and remembers it, so a hand-over to the shell init can let it go.
func (b *booter) mount(source, target, fstype string, flags uintptr, data string) error {
	if err := unix.Mount(source, target, fstype, flags, data); err != nil {
		return err
	}
	b.addMount(target)
	return nil
}

// undoMounts lets go of everything jukaboot mounted, newest first, before the
// shell init starts over.
func (b *booter) undoMounts() {
	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	for i := len(b.mounts) - 1; i >= 0; i-- {
		_ = unix.Unmount(b.mounts[i], unix.MNT_DETACH)
	}
	b.mounts = nil
}

func mountedAt(target string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == target {
			return true
		}
	}
	return false
}

// earlyMounts: proc, sys, the hotplug helper off (a stock rule could mount the
// internal storage when a disk or module appears), devtmpfs, stdio on
// /dev/null, pts, shm, tmp.
func (b *booter) earlyMounts() {
	done := b.tl.begin("proc, sys, dev, tmp")
	defer done()
	if err := b.mount("proc", "/proc", "proc", 0, ""); err != nil {
		b.fallback("cannot mount /proc: " + err.Error())
	}
	if err := b.mount("sysfs", "/sys", "sysfs", 0, ""); err != nil {
		b.fallback("cannot mount /sys: " + err.Error())
	}
	_ = writeFile("/proc/sys/kernel/hotplug", "\n")
	_ = writeFile("/sys/kernel/uevent_helper", "\n")
	if !mountedAt("/dev") {
		_ = b.mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	}
	// Valid standard descriptors whatever the kernel could open at exec.
	if isCharDev("/dev/null") {
		if fd, err := unix.Open("/dev/null", unix.O_RDWR|unix.O_CLOEXEC, 0); err == nil {
			b.nullFD = fd
			for target := 0; target <= 2; target++ {
				_ = unix.Dup3(fd, target, 0)
			}
		}
	}
	_ = os.MkdirAll("/dev/pts", 0o755)
	_ = os.MkdirAll("/dev/shm", 0o755)
	_ = b.mount("devpts", "/dev/pts", "devpts", 0, "")
	_ = b.mount("tmpfs", "/dev/shm", "tmpfs", 0, "")
	_ = b.mount("tmpfs", "/tmp", "tmpfs", 0, "mode=1777")
}

// rwSetup: the root is read-only; /etc, /var and /root get a writable copy in
// memory (side by side), then /run, the /var folders and the hostname.
func (b *booter) rwSetup() {
	done := b.tl.begin("writable /etc /var /root, /run, hostname")
	defer done()
	var wg sync.WaitGroup
	for _, dir := range []string{"/etc", "/var", "/root"} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() {
			continue // missing, or a link (to /tmp, say): nothing to copy
		}
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			b.safe("the copy of "+dir, func() {
				if err := b.rwCopy(dir); err != nil {
					b.log("no writable %s: %v", dir, err)
				}
			})
		}(dir)
	}
	wg.Wait()
	if st, err := os.Lstat("/run"); err == nil && st.IsDir() {
		_ = b.mount("tmpfs", "/run", "tmpfs", 0, "")
	}
	for _, dir := range []string{"/var/run", "/var/log", "/var/lock"} {
		_ = os.MkdirAll(dir, 0o755)
	}
	setHostname()
}

// rwCopy is the shell's "cp -a DIR/. RW/DIR/ && mount -o bind RW/DIR DIR".
func (b *booter) rwCopy(dir string) error {
	dst := rwRoot + dir
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if err := copyTree(dir, dst, &st); err != nil {
		return err
	}
	return b.mount(dst, dir, "", unix.MS_BIND, "")
}

// copyTree copies the contents of src into the existing folder dst, then
// gives dst src's owner, mode and times (st).
func copyTree(src, dst string, st *unix.Stat_t) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyEntry(src+"/"+e.Name(), dst+"/"+e.Name()); err != nil {
			return err
		}
	}
	return copyAttrs(dst, st)
}

func copyEntry(src, dst string) error {
	var st unix.Stat_t
	if err := unix.Lstat(src, &st); err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		if err := unix.Mkdir(dst, 0o700); err != nil && err != unix.EEXIST {
			return err
		}
		return copyTree(src, dst, &st)
	case unix.S_IFREG:
		if err := copyFile(src, dst); err != nil {
			return err
		}
	case unix.S_IFLNK:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
	case unix.S_IFCHR, unix.S_IFBLK, unix.S_IFIFO:
		if err := unix.Mknod(dst, st.Mode, int(st.Rdev)); err != nil {
			return err
		}
	default:
		return nil // a socket belongs to a running program, not to the copy
	}
	return copyAttrs(dst, &st)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// copyAttrs gives path the owner, mode and times in st (cp -a's -p). The mode
// goes after the owner: chown clears set-user-ID bits.
func copyAttrs(path string, st *unix.Stat_t) error {
	if err := unix.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFLNK {
		if err := unix.Fchmodat(unix.AT_FDCWD, path, st.Mode&0o7777, 0); err != nil {
			return err
		}
	}
	times := []unix.Timespec{st.Atim, st.Mtim}
	_ = unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW)
	return nil
}

// setHostname is busybox's "hostname -F /etc/hostname": the first word of
// the first line that is not a comment.
func setHostname() {
	data, err := os.ReadFile("/etc/hostname")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		_ = unix.Sethostname([]byte(f[0]))
		return
	}
}

// ── the card ───────────────────────────────────────────────────────────────

// cardPart is the /dev node of the SD card partition with that GPT name, ""
// when there is none. Only partitions of a disk whose sysfs type is "SD": the
// internal eMMC ("MMC") is never used.
func cardPart(name string) string {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		base := "/sys/class/block/" + e.Name()
		data, err := os.ReadFile(base + "/uevent")
		if err != nil {
			continue
		}
		hit := false
		for _, line := range strings.Split(string(data), "\n") {
			if line == "PARTNAME="+name {
				hit = true
			}
		}
		if !hit {
			continue
		}
		// The link leads into the disk's folder; ".." is its disk. The path is
		// given to the kernel as it is (no filepath.Join, whose lexical ".."
		// would drop the link instead of following it).
		if firstLine(base+"/../device/type") != "SD" {
			continue
		}
		return "/dev/" + e.Name()
	}
	return ""
}

func (b *booter) waitCard() string {
	done := b.tl.begin("card partition found")
	deadline := time.Now().Add(cardWait)
	for {
		if dev := cardPart(b.board.DataName); dev != "" {
			done()
			b.tl.mark("card partition " + dev)
			return dev
		}
		if time.Now().After(deadline) {
			done()
			return ""
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (b *booter) mountCard(dev string) error {
	done := b.tl.begin("card mounted (" + b.board.DataFSType + ")")
	defer done()
	return b.mount(dev, cardDir, b.board.DataFSType, unix.MS_NOATIME, "")
}

// cardReady: the card's folders, os.log (the previous one kept as .1, the
// lines so far written), and /mnt/UDISK, where the stock input daemon reads
// its stick calibration.
func (b *booter) cardReady() {
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.MkdirAll(cardDir+"/.userdata/smartpro/udisk", 0o755)
	b.oslog.open(dataDir + "/os.log")
	b.log("process 1 at %ss, card ready at %ss", secs(b.start), secs(bootNow()))
	_ = b.mount(cardDir+"/.userdata/smartpro/udisk", udiskDir, "", unix.MS_BIND, "")
}

// readahead raises the card disk's read-ahead to READAHEAD_KB, only upwards
// and only for the SD card (cardPart proved it is one). JukaHub's binary is
// read by page faults; each one then reads a bigger piece around it in one
// card request instead of several.
func (b *booter) readahead(dev string) {
	kb := b.settings.ReadaheadKB
	if kb <= 0 {
		return
	}
	disk := strings.TrimPrefix(dev, "/dev/")
	p := strings.LastIndexByte(disk, 'p')
	if !strings.HasPrefix(disk, "mmcblk") || len(disk) < 7 || disk[6] < '0' || disk[6] > '9' ||
		p < 7 || p+1 >= len(disk) || disk[p+1] < '0' || disk[p+1] > '9' {
		return
	}
	disk = disk[:p]
	q := "/sys/block/" + disk + "/queue/read_ahead_kb"
	if unix.Access(q, unix.W_OK) != nil {
		return
	}
	cur := firstLine(q)
	n, err := strconv.Atoi(cur)
	if !allDigits(cur) || err != nil || n >= kb {
		return
	}
	if writeFile(q, strconv.Itoa(kb)+"\n") == nil {
		b.log("read-ahead of %s %s -> %d KB", disk, cur, kb)
	}
}

// ── drivers ────────────────────────────────────────────────────────────────

// loadModule is insmod without parameters, as the shell init ran it. A module
// already in (or built in) counts as loaded.
func loadModule(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	err = unix.FinitModule(fd, "", 0)
	if err == unix.ENOSYS {
		image, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		err = unix.InitModule(image, "")
	}
	if err == unix.EEXIST {
		return nil
	}
	return err
}

// replayGPIO sets the GPIO lines the stock system had set (the sticks' power).
func (b *booter) replayGPIO() {
	data, err := os.ReadFile(jRoot + "/gpio.list")
	if err != nil {
		return
	}
	done := b.tl.begin("GPIO lines")
	defer done()
	for _, g := range parseGPIO(string(data)) {
		dir := "/sys/class/gpio/gpio" + g.Num
		if !isDir(dir) {
			_ = writeFile("/sys/class/gpio/export", g.Num+"\n")
		}
		if !isDir(dir) {
			continue
		}
		value := "in"
		if g.Dir == "out" {
			value = "low"
			if g.Value == "1" {
				value = "high"
			}
		}
		_ = writeFile(dir+"/direction", value+"\n")
	}
}

// restoreMixer is "alsactl -f /jukahub/asound.state restore", five seconds at
// most.
func (b *booter) restoreMixer() {
	state := jRoot + "/asound.state"
	if !isFile(state) {
		return
	}
	alsactl := lookPath("alsactl", initPath)
	if alsactl == "" {
		return
	}
	done := b.tl.begin("mixer restored")
	defer done()
	_, status, err := b.spawn(alsactl, []string{"alsactl", "-f", state, "restore"}, spawnOpts{dir: "/", env: b.env, out: -1, wait: true})
	if err != nil {
		return
	}
	select {
	case <-status:
	case <-time.After(5 * time.Second):
		b.log("alsactl restore still running after 5 s: going on")
	}
}

// inputCount is how many input devices the kernel has made.
func inputCount() int {
	entries, err := os.ReadDir("/dev/input")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "event") {
			n++
		}
	}
	return n
}

func hasWlan() bool {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wlan") {
			return true
		}
	}
	return false
}

// ── Wi-Fi networks ─────────────────────────────────────────────────────────

const (
	wifiConf     = "/etc/wifi/wpa_supplicant.conf"
	cardWifiConf = cardDir + "/.userdata/smartpro/wpa_supplicant.conf"
)

func (b *booter) wifiConfIn() {
	if !isFile(cardWifiConf) || !isDir("/etc/wifi") {
		return
	}
	if data, err := os.ReadFile(cardWifiConf); err == nil {
		_ = os.WriteFile(wifiConf, data, 0o600)
	}
}

// wifiConfOut keeps the networks joined in JukaHub on the card.
func (b *booter) wifiConfOut() {
	if !isFile(wifiConf) || !mountedAt(cardDir) {
		return
	}
	data, err := os.ReadFile(wifiConf)
	if err != nil {
		return
	}
	if os.WriteFile(cardWifiConf+".new", data, 0o600) == nil {
		_ = os.Rename(cardWifiConf+".new", cardWifiConf)
	}
}

// ── the kernel's log ───────────────────────────────────────────────────────

// saveKernelLog writes the kernel's message buffer to the card
// (boot-dmesg.txt, the previous one kept as .1), so the kernel's own start
// can be read even with "quiet". With initcall_debug (build_image.py
// --boot-profile) os.log also names the slowest kernel start-up calls.
func (b *booter) saveKernelLog() {
	done := b.tl.begin("kernel log saved")
	defer done()
	size, err := unix.Klogctl(unix.SYSLOG_ACTION_SIZE_BUFFER, nil)
	if err != nil || size <= 0 {
		size = 1 << 20
	}
	if size > 8<<20 {
		size = 8 << 20
	}
	buf := make([]byte, size)
	n, err := unix.Klogctl(unix.SYSLOG_ACTION_READ_ALL, buf)
	if err != nil || n <= 0 {
		b.log("kernel messages not saved: %v", err)
		return
	}
	cmdline := firstLine("/proc/cmdline")
	head := "# kernel messages at " + secs(bootNow()) + " s since boot, saved by jukaboot\n# /proc/cmdline: " + cmdline + "\n"
	path := dataDir + "/boot-dmesg.txt"
	if isFile(path) {
		_ = os.Rename(path, path+".1")
	}
	if err := os.WriteFile(path+".tmp", append([]byte(head), buf[:n]...), 0o644); err != nil {
		b.log("kernel messages not saved: %v", err)
		return
	}
	_ = os.Rename(path+".tmp", path)
	b.log("kernel messages saved to .userdata/jukahub/boot-dmesg.txt (%d KB)", (n+1023)/1024)
	if !strings.Contains(" "+cmdline+" ", " initcall_debug ") {
		return
	}
	var parts []string
	for _, c := range slowInitcalls(string(buf[:n]), 12) {
		parts = append(parts, fmt.Sprintf("%s %.1f ms", c.Name, float64(c.Usecs)/1000))
	}
	if len(parts) > 0 {
		b.log("slowest kernel start-up calls: %s", strings.Join(parts, ", "))
	}
}
