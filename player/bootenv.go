package main

// bootenv.go — Silent boot: the one bootloader setting JukaHub changes.
//
// The TrimUI firmware's bootloader (Allwinner's U-Boot) starts Linux with
// loglevel=8 on its command line, so every kernel message, debug ones too, is
// printed to the serial port on the board's test pads at 115200 baud, and the
// kernel waits for each character to leave (about 87 µs apiece) with nothing
// listening. On a stock Smart Pro the kernel took 4.2 s to reach its root file
// system. A kernel that boots with loglevel=1 prints emergencies only and
// sends everything else to its message buffer.
//
// Silent boot changes that one value in U-Boot's saved environment — the
// loglevel variable U-Boot builds the command line from — to 1, and back.
// Nothing else in the bootloader, the kernel or the partitions changes, and the
// kernel's message buffer (dmesg) still gets every message. The write is
// narrower than the boot logo's:
//
//   - The environment is found by partition NAME with bootlogo_linux.go's
//     finder: env and env-redund, both on the internal eMMC, never a card.
//   - Only a bootloader that keeps TWO copies is changed (U-Boot's redundant
//     environment; the Smart Pro's U-Boot looks for env-redund). The new copy
//     goes to the one U-Boot is not loading, with the next serial number, as
//     U-Boot's own saveenv does: until it is complete and its checksum holds,
//     U-Boot keeps loading the old one, so a power cut mid-write changes
//     nothing. A one-copy environment is left alone: rewriting it in place
//     could leave the bootloader with no settings at all.
//   - It is changed only where it demonstrably drives the running kernel: the
//     copy U-Boot loads (by U-Boot's own rule) has the variable exactly once,
//     its value is the level the running kernel's command line ended with, a
//     "setenv bootargs" step hands ${loglevel} on, and the kernel image's own
//     command line (the boot partition's header) names no level of its own.
//   - The new copy is the loaded copy byte for byte except the value's byte,
//     the serial and the checksum; it is parsed again and compared setting by
//     setting before it is written, compared byte by byte after (read back),
//     and U-Boot's rule must then pick it, or the slot is put back as it was.
//   - Both copies are saved to data/bootenv/ on the SD card before a write, and
//     state.json keeps the original value, which switching it off writes back
//     the same way.
//   - The next boot's /proc/cmdline is the proof: a bootloader that still
//     passes another level shows "No effect" and is offered the way back.
//
// Settings → General → Silent boot switches it (A twice); `JukaHub silent-boot
// [status|on|off]` does the same from an SSH shell.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// bootEnvQuiet is the console log level Silent boot sets: emergencies
	// only, so a panic still prints (0 would hide that too).
	bootEnvQuiet = "1"
	// bootEnvVar is the variable U-Boot builds loglevel= from.
	bootEnvVar = "loglevel"
	// bootEnvHeader is a two-copy environment's header: the crc32 of the data
	// area (little-endian), then the copy's serial byte.
	bootEnvHeader = 5
	// bootEnvReadMax caps a read of one copy's partition.
	bootEnvReadMax = 1 << 20
	// bootEnvImageHead is how much of the boot partition is read for the
	// kernel image's header.
	bootEnvImageHead = 2048
	// bootEnvStateVersion is state.json's layout.
	bootEnvStateVersion = 1
	// bootEnvArmWindow is how long the first press arms the second.
	bootEnvArmWindow = 4 * time.Second
)

// bootEnvSizes are the environment sizes tried, U-Boot's usual one first.
var bootEnvSizes = []int{0x20000, 0x10000, 0x8000, 0x4000, 0x2000, 0x40000, 0x80000, 0x100000}

// The statuses the Settings row shows.
const (
	bootEnvStatusChecking    = "Checking…"
	bootEnvStatusWriting     = "Writing…"
	bootEnvStatusOn          = "On"
	bootEnvStatusOff         = "Off"
	bootEnvStatusOnNext      = "On from next start"
	bootEnvStatusOffNext     = "Off from next start"
	bootEnvStatusNoEffect    = "No effect"
	bootEnvStatusUnavailable = "Unavailable"
)

// The operations the row runs.
const (
	bootEnvOpProbe = "probe"
	bootEnvOpOn    = "on"
	bootEnvOpOff   = "off"
)

// The reasons a look at the device gives up, short enough for the row's
// detail line; the platform code wraps them with the specifics for the log.
var (
	errBootEnvUnsupported = errors.New("only TrimUI handhelds have a bootloader JukaHub can set")
	errBootEnvNoStore     = errors.New("this firmware keeps its bootloader settings where JukaHub can't reach them")
	errBootEnvOneCopy     = errors.New("this bootloader keeps one copy of its settings; JukaHub only changes two-copy ones, which a power cut can't break")
	errBootEnvFormat      = errors.New("the bootloader settings are in a form JukaHub won't change")
	errBootEnvNotDriving  = errors.New("this bootloader sets the kernel's log level somewhere JukaHub doesn't change")
	errBootEnvChanged     = errors.New("the bootloader settings changed while JukaHub was preparing; nothing was written — try again")
)

// ── The environment ──────────────────────────────────────────────────────

// bootEnvCopy is one stored copy of a two-copy U-Boot environment: the header,
// then name=value strings each ending in NUL, the list ending in an empty
// one, padded to Size.
type bootEnvCopy struct {
	Raw    []byte // exactly Size bytes
	Size   int
	Serial byte
}

// bootEnvParseCopy finds a two-copy environment at the start of raw: the first
// size, in U-Boot's usual order, whose checksum holds. ok is false when none
// does (a copy never written, or one in another format).
func bootEnvParseCopy(raw []byte) (bootEnvCopy, bool) {
	if len(raw) < bootEnvHeader {
		return bootEnvCopy{}, false
	}
	want := binary.LittleEndian.Uint32(raw[:4])
	for _, size := range bootEnvSizes {
		if size > len(raw) {
			continue
		}
		if crc32.ChecksumIEEE(raw[bootEnvHeader:size]) == want {
			return bootEnvCopy{Raw: append([]byte(nil), raw[:size]...), Size: size, Serial: raw[4]}, true
		}
	}
	return bootEnvCopy{}, false
}

// bootEnvOneCopy reports whether raw holds a one-copy environment instead: the
// checksum straight after itself, no serial byte.
func bootEnvOneCopy(raw []byte) bool {
	if len(raw) < 4 {
		return false
	}
	want := binary.LittleEndian.Uint32(raw[:4])
	for _, size := range bootEnvSizes {
		if size <= len(raw) && crc32.ChecksumIEEE(raw[4:size]) == want {
			return true
		}
	}
	return false
}

// bootEnvEntry is one setting of a copy and where its value starts in Raw.
type bootEnvEntry struct {
	Name  string
	Value string
	At    int
}

// bootEnvEntries lists a copy's settings in order. A string with no name or no
// "=", or a list with no empty string to end it, is a copy JukaHub does not
// rewrite.
func bootEnvEntries(c bootEnvCopy) ([]bootEnvEntry, error) {
	if c.Size <= bootEnvHeader || len(c.Raw) != c.Size {
		return nil, fmt.Errorf("%w (no settings to read)", errBootEnvFormat)
	}
	var out []bootEnvEntry
	for i := bootEnvHeader; ; {
		if i >= c.Size {
			return nil, fmt.Errorf("%w (the list of settings has no end)", errBootEnvFormat)
		}
		if c.Raw[i] == 0 {
			return out, nil
		}
		n := bytes.IndexByte(c.Raw[i:c.Size], 0)
		if n < 0 {
			return nil, fmt.Errorf("%w (a setting runs past the end)", errBootEnvFormat)
		}
		item := c.Raw[i : i+n]
		eq := bytes.IndexByte(item, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("%w (a setting has no name)", errBootEnvFormat)
		}
		out = append(out, bootEnvEntry{Name: string(item[:eq]), Value: string(item[eq+1:]), At: i + eq + 1})
		i += n + 1
	}
}

// bootEnvActive is the copy U-Boot loads (env_import_redund): the one whose
// checksum holds; with both good, the higher serial, where 0 follows 255 (the
// serial wraps); equal serials load the first. -1: neither holds.
func bootEnvActive(valid [2]bool, serial [2]byte) int {
	switch {
	case !valid[0] && !valid[1]:
		return -1
	case !valid[1]:
		return 0
	case !valid[0]:
		return 1
	}
	a, b := serial[0], serial[1]
	switch {
	case a == 255 && b == 0:
		return 1
	case b == 255 && a == 0:
		return 0
	case b > a:
		return 1
	}
	return 0
}

// bootEnvRewrite is c with the value of e (one of its settings) replaced by
// value — the same length, so every other byte stays where it was — the serial
// set and the checksum recomputed: the bytes for the other copy's partition.
// It parses the result again and refuses unless it holds the same settings in
// the same places with only that one changed.
func bootEnvRewrite(c bootEnvCopy, e bootEnvEntry, value string, serial byte) ([]byte, error) {
	switch {
	case value == e.Value:
		return nil, errors.New("nothing to change")
	case len(value) != len(e.Value) || strings.IndexByte(value, 0) >= 0:
		return nil, fmt.Errorf("%w (the new value must be as long as %q)", errBootEnvFormat, e.Value)
	case len(c.Raw) != c.Size || e.At < bootEnvHeader || e.At+len(e.Value) > c.Size ||
		string(c.Raw[e.At:e.At+len(e.Value)]) != e.Value:
		return nil, fmt.Errorf("%w (the setting is not where it was read)", errBootEnvFormat)
	}
	out := append([]byte(nil), c.Raw...)
	copy(out[e.At:], value)
	out[4] = serial
	binary.LittleEndian.PutUint32(out[:4], crc32.ChecksumIEEE(out[bootEnvHeader:]))
	back, ok := bootEnvParseCopy(out)
	if !ok || back.Size != c.Size || back.Serial != serial {
		return nil, errors.New("the new settings do not check out")
	}
	before, err := bootEnvEntries(c)
	if err != nil {
		return nil, err
	}
	after, err := bootEnvEntries(back)
	if err != nil {
		return nil, err
	}
	if len(after) != len(before) {
		return nil, errors.New("the new settings lost or gained a setting")
	}
	changed := 0
	for i := range before {
		b, a := before[i], after[i]
		if a.Name != b.Name || a.At != b.At {
			return nil, errors.New("the new settings moved a setting")
		}
		if a.Value == b.Value {
			continue
		}
		if b.At != e.At || a.Value != value {
			return nil, fmt.Errorf("the new settings changed %s", b.Name)
		}
		changed++
	}
	if changed != 1 {
		return nil, errors.New("the new settings did not change the log level")
	}
	return out, nil
}

// bootEnvPassesLevel reports whether a kernel command line the environment
// builds — a "setenv bootargs …" step of any setting, or bootargs itself —
// hands the loglevel variable on.
func bootEnvPassesLevel(entries []bootEnvEntry) bool {
	pass := func(args string) bool {
		for _, w := range strings.Fields(args) {
			w = strings.Trim(w, `"'`)
			if w == "loglevel=${loglevel}" || w == "loglevel=$loglevel" {
				return true
			}
		}
		return false
	}
	for _, e := range entries {
		if e.Name == "bootargs" && pass(e.Value) {
			return true
		}
		for _, step := range strings.Split(e.Value, ";") {
			f := strings.Fields(step)
			if len(f) > 2 && f[0] == "setenv" && f[1] == "bootargs" && pass(strings.Join(f[2:], " ")) {
				return true
			}
		}
	}
	return false
}

// bootEnvKernelLevel is the console log level a kernel command line leaves.
// The kernel reads its words in order: loglevel=N sets N, quiet 4, debug 10,
// and the last one wins. last is the value of the last loglevel= when nothing
// came after it ("" otherwise), n counts the loglevel= words; level is -1 when
// none of them is there (the kernel's built-in default then applies).
func bootEnvKernelLevel(cmdline string) (level int, last string, n int) {
	level = -1
	for _, w := range strings.Fields(cmdline) {
		switch {
		case w == "quiet":
			level, last = 4, ""
		case w == "debug":
			level, last = 10, ""
		case strings.HasPrefix(w, "loglevel="):
			n++
			v := strings.TrimPrefix(w, "loglevel=")
			if x, err := strconv.Atoi(v); err == nil {
				level, last = x, v
			}
		}
	}
	return level, last, n
}

// bootEnvImageLevel reports whether a kernel image's own command line names a
// level (loglevel=, quiet or debug). head is the start of an Android boot image
// — "ANDROID!", the command line at 64 (512 bytes) and its extra part at 608
// (1024) — which U-Boot adds to the line the environment built, so a level
// there would decide instead. ok is false for anything but such a header.
func bootEnvImageLevel(head []byte) (named, ok bool) {
	if len(head) < 64+512 || string(head[:8]) != "ANDROID!" {
		return false, false
	}
	field := func(from, to int) string {
		if to > len(head) {
			to = len(head)
		}
		if from >= to {
			return ""
		}
		b := head[from:to]
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return string(b)
	}
	for _, w := range strings.Fields(field(64, 64+512) + " " + field(608, 608+1024)) {
		if w == "quiet" || w == "debug" || strings.HasPrefix(w, "loglevel=") {
			return true, true
		}
	}
	return false, true
}

// bootEnvLevelOK is a level Silent boot can swap with "1": one digit, 2 to 9.
func bootEnvLevelOK(v string) bool {
	return len(v) == 1 && v[0] >= '2' && v[0] <= '9'
}

// ── What a look at the device found ──────────────────────────────────────

// bootEnvPlan is the device's bootloader settings and the running kernel.
type bootEnvPlan struct {
	Names  [2]string // the partitions: env, env-redund
	Devs   [2]string
	Raw    [2][]byte // each partition's first Size bytes, as read
	Copies [2]bootEnvCopy
	Valid  [2]bool
	Size   int
	Active int // the copy U-Boot loads
	// Level is the loglevel setting in the loaded copy; Passes, whether a
	// bootargs step hands it on.
	Level  bootEnvEntry
	Passes bool
	// The running kernel's command line: its console level, the last
	// loglevel= value and how many there were (bootEnvKernelLevel).
	Kernel     int
	KernelLast string
	KernelN    int
	// The kernel image's own command line names a level; ImageKnown is false
	// when the boot partition could not be read.
	Image      bool
	ImageKnown bool
}

// bootEnvPlanFrom works the plan out of what was read: the two partitions'
// names, devices and first bytes (raw1 nil when the firmware names no second
// copy), the running kernel's command line (cmdlineOK false when it could not
// be read) and the start of the boot partition (nil when unread).
func bootEnvPlanFrom(names, devs [2]string, raw0, raw1 []byte, cmdline string, cmdlineOK bool, head []byte) (bootEnvPlan, error) {
	var p bootEnvPlan
	if raw1 == nil {
		if bootEnvOneCopy(raw0) {
			return p, errBootEnvOneCopy
		}
		return p, fmt.Errorf("%w (no second copy beside %s)", errBootEnvNoStore, names[0])
	}
	c0, v0 := bootEnvParseCopy(raw0)
	c1, v1 := bootEnvParseCopy(raw1)
	switch {
	case !v0 && !v1:
		if bootEnvOneCopy(raw0) || bootEnvOneCopy(raw1) {
			return p, errBootEnvOneCopy
		}
		return p, fmt.Errorf("%w (neither copy's checksum holds)", errBootEnvFormat)
	case v0 && v1 && c0.Size != c1.Size:
		return p, fmt.Errorf("%w (the copies are %d and %d bytes)", errBootEnvFormat, c0.Size, c1.Size)
	}
	size := c0.Size
	if !v0 {
		size = c1.Size
	}
	if len(raw0) < size || len(raw1) < size {
		return p, fmt.Errorf("%w (a partition is smaller than the %d-byte settings)", errBootEnvFormat, size)
	}
	p.Names, p.Devs = names, devs
	p.Raw = [2][]byte{raw0[:size:size], raw1[:size:size]}
	p.Copies = [2]bootEnvCopy{c0, c1}
	p.Valid = [2]bool{v0, v1}
	p.Size = size
	p.Active = bootEnvActive(p.Valid, [2]byte{c0.Serial, c1.Serial})
	entries, err := bootEnvEntries(p.Copies[p.Active])
	if err != nil {
		return p, err
	}
	found := 0
	for _, e := range entries {
		if e.Name == bootEnvVar {
			p.Level = e
			found++
		}
	}
	if found != 1 {
		return p, fmt.Errorf("%w (%d loglevel settings in %s)", errBootEnvNotDriving, found, p.Names[p.Active])
	}
	p.Passes = bootEnvPassesLevel(entries)
	p.Kernel = -1
	if cmdlineOK {
		p.Kernel, p.KernelLast, p.KernelN = bootEnvKernelLevel(cmdline)
	}
	if head != nil {
		p.Image, p.ImageKnown = bootEnvImageLevel(head)
	}
	return p, nil
}

// bootEnvDescribe is the plan in one line for the log and the command line.
func bootEnvDescribe(p bootEnvPlan) string {
	copyText := func(i int) string {
		if !p.Valid[i] {
			return fmt.Sprintf("%s (%s) not valid", p.Names[i], p.Devs[i])
		}
		return fmt.Sprintf("%s (%s) serial %d", p.Names[i], p.Devs[i], p.Copies[i].Serial)
	}
	image := "unread"
	if p.ImageKnown {
		image = "names none"
		if p.Image {
			image = "names one"
		}
	}
	kernel := "none named"
	if p.Kernel >= 0 {
		kernel = strconv.Itoa(p.Kernel)
	}
	return fmt.Sprintf("%s, %s, %d bytes; U-Boot loads %s: loglevel=%s (passed on: %t); running kernel %s (%d loglevel= words); kernel image's own line %s",
		copyText(0), copyText(1), p.Size, p.Names[p.Active], p.Level.Value, p.Passes, kernel, p.KernelN, image)
}

// bootEnvCanSwitchOn is nil when switching on would decide the kernel's level:
// see the file comment for each rule. The running kernel's level must be the
// setting's — except straight after Silent boot was switched off in this
// boot, when this kernel still runs with the 1 the setting gave it.
func bootEnvCanSwitchOn(p bootEnvPlan, st bootEnvState, bootID string) error {
	undo := st.WroteBoot != "" && st.WroteBoot == bootID && p.KernelLast == bootEnvQuiet && p.Level.Value == st.Original
	switch {
	case !bootEnvLevelOK(p.Level.Value):
		return fmt.Errorf("%w (loglevel=%q is not a level JukaHub swaps)", errBootEnvNotDriving, p.Level.Value)
	case !p.Passes:
		return fmt.Errorf("%w (no bootargs step passes ${loglevel} on)", errBootEnvNotDriving)
	case !undo && (p.KernelLast == "" || p.KernelLast != p.Level.Value):
		return fmt.Errorf("%w (the running kernel's level %q is not the setting's %q)", errBootEnvNotDriving, p.KernelLast, p.Level.Value)
	case p.Image:
		return fmt.Errorf("%w (the kernel image's own command line names a level)", errBootEnvNotDriving)
	}
	return nil
}

// bootEnvClassify names the row's status. err explains Unavailable; for On it
// is errBootEnvAlreadyQuiet when the kernel was quiet without the setting.
func bootEnvClassify(p bootEnvPlan, st bootEnvState, bootID string) (string, error) {
	quiet := p.Kernel >= 0 && p.Kernel <= 1
	thisBoot := st.WroteBoot != "" && st.WroteBoot == bootID
	if p.Level.Value == bootEnvQuiet {
		switch {
		case quiet:
			return bootEnvStatusOn, nil
		case thisBoot:
			return bootEnvStatusOnNext, nil
		}
		return bootEnvStatusNoEffect, nil
	}
	switch {
	case quiet && thisBoot:
		if err := bootEnvCanSwitchOn(p, st, bootID); err != nil {
			return bootEnvStatusUnavailable, err
		}
		return bootEnvStatusOffNext, nil
	case quiet:
		return bootEnvStatusOn, errBootEnvAlreadyQuiet
	}
	if err := bootEnvCanSwitchOn(p, st, bootID); err != nil {
		return bootEnvStatusUnavailable, err
	}
	return bootEnvStatusOff, nil
}

// errBootEnvAlreadyQuiet is a kernel that starts quietly without Silent boot.
var errBootEnvAlreadyQuiet = errors.New("the kernel already starts quietly")

// ── State and backups ────────────────────────────────────────────────────

// bootEnvState is data/bootenv/state.json: the level to put back, where the
// copies saved before a write are, and the boot that last wrote (the next
// boot is the one that shows the result). Writing is raised for the write
// itself, for the log of a write a power cut stopped (harmless: U-Boot kept
// loading the other copy).
type bootEnvState struct {
	Version   int       `json:"version"`
	Original  string    `json:"original,omitempty"`
	Model     string    `json:"model,omitempty"`
	Parts     []string  `json:"parts,omitempty"`
	Size      int       `json:"size,omitempty"`
	Backups   []string  `json:"backups,omitempty"`
	WroteBoot string    `json:"wroteBoot,omitempty"`
	Writing   string    `json:"writing,omitempty"`
	ChangedAt time.Time `json:"changedAt"`
}

// Overridable for tests and the other platforms: whether this device has a
// bootloader JukaHub can set, the look at the device, the write of one copy
// (plan, slot, the bytes it must still hold or nil, the new bytes; whether the
// partition was opened for writing), where the backups go, how work is
// started, the clock and the toasts.
var (
	bootEnvSupportedFn = func() bool { return bootEnvPlatformSupported() && IsTrimuiDevice() }
	bootEnvLoadFn      = bootEnvLoadPlatform
	bootEnvWriteFn     = bootEnvWriteCopyPlatform
	bootEnvDataDirFn   = defaultBootEnvDataDir
	bootEnvGo          = func(fn func()) { go fn() }
	bootEnvNow         = time.Now
	bootEnvToast       = ShowToast
)

// defaultBootEnvDataDir is data/bootenv beside the app.
func defaultBootEnvDataDir() (string, error) {
	dir, err := P().DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bootenv"), nil
}

// bootEnvStatePath is the state file's path.
func bootEnvStatePath() (string, error) {
	dir, err := bootEnvDataDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// bootEnvLoadState reads the state; a missing or unreadable one is empty.
func bootEnvLoadState() bootEnvState {
	path, err := bootEnvStatePath()
	if err != nil {
		return bootEnvState{}
	}
	var st bootEnvState
	if _, err := readJSONWithBackup(path, &st); err != nil || st.Version != bootEnvStateVersion {
		return bootEnvState{}
	}
	return st
}

// bootEnvLoadStateStrict reads the state for a write: absent is a first
// change, but a record that exists and cannot be read stops the write — the
// level to put back is exactly what it keeps.
func bootEnvLoadStateStrict() (bootEnvState, error) {
	path, err := bootEnvStatePath()
	if err != nil {
		return bootEnvState{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return bootEnvState{}, nil
	}
	var st bootEnvState
	if _, err := readJSONWithBackup(path, &st); err != nil {
		return bootEnvState{}, fmt.Errorf("the Silent boot record (%s) cannot be read, so nothing was written: %w", path, err)
	}
	if st.Version != bootEnvStateVersion {
		return bootEnvState{}, fmt.Errorf("the Silent boot record (%s) was written by another JukaHub version, so nothing was written", path)
	}
	return st, nil
}

// bootEnvSaveState writes the state through the crash-safe save path.
func bootEnvSaveState(st bootEnvState) error {
	path, err := bootEnvStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	st.Version = bootEnvStateVersion
	payload, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, payload, 0o644)
}

// bootEnvWriteBackup copies one partition's bytes into dir as
// <part>-<sha>.bin, synced and read back, and returns the file's name. An
// identical copy already there is reused; another file is never replaced.
func bootEnvWriteBackup(dir, part string, data []byte) (string, error) {
	sum := bootLogoSHA(data)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := part + "-" + sum[:12] + ".bin"
	path := filepath.Join(dir, name)
	if existing, err := os.ReadFile(path); err == nil {
		if bootLogoSHA(existing) == sum {
			return name, nil
		}
		return "", fmt.Errorf("%s already exists with other contents", name)
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	syncDirectory(dir)
	back, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bootLogoSHA(back) != sum {
		return "", errors.New("the copy did not read back intact")
	}
	return name, nil
}

// ── Switching ────────────────────────────────────────────────────────────

// bootEnvSwitch sets the bootloader's level to 1 (on) or back to the level it
// had (off), and returns the message for the toast. See the file comment for
// what is checked; the copy U-Boot loads is never written.
func bootEnvSwitch(on bool) (string, error) {
	if !bootEnvSupportedFn() {
		return "", errBootEnvUnsupported
	}
	st, err := bootEnvLoadStateStrict()
	if err != nil {
		return "", err
	}
	p, err := bootEnvLoadFn()
	if err != nil {
		return "", err
	}
	model := string(DetectDevice())
	bootID := strings.TrimSpace(bootOSBootIDFn())
	value := bootEnvQuiet
	if on {
		if p.Level.Value == bootEnvQuiet {
			return "Silent boot is already on", nil
		}
		if err := bootEnvCanSwitchOn(p, st, bootID); err != nil {
			return "", err
		}
	} else {
		if p.Level.Value != bootEnvQuiet {
			return "Silent boot is already off", nil
		}
		if !bootEnvLevelOK(st.Original) || (st.Model != "" && st.Model != model) {
			return "", errors.New("JukaHub did not switch it on here, so it has no level of its own to put back")
		}
		value = st.Original
	}
	dir, err := bootEnvDataDirFn()
	if err != nil {
		return "", fmt.Errorf("no folder for the backups, so nothing was written: %w", err)
	}
	for i := range p.Raw {
		name, err := bootEnvWriteBackup(dir, p.Names[i], p.Raw[i])
		if err != nil {
			return "", fmt.Errorf("could not save %s to the SD card, so nothing was written: %w", p.Names[i], err)
		}
		if !slices.Contains(st.Backups, name) {
			st.Backups = append(st.Backups, name)
		}
	}
	if on {
		st.Original = p.Level.Value
	}
	op := bootEnvOpOff
	if on {
		op = bootEnvOpOn
	}
	st.Model, st.Parts, st.Size, st.Writing = model, []string{p.Names[0], p.Names[1]}, p.Size, op
	target := 1 - p.Active
	data, err := bootEnvRewrite(p.Copies[p.Active], p.Level, value, p.Copies[p.Active].Serial+1)
	if err != nil {
		return "", err
	}
	if err := bootEnvSaveState(st); err != nil {
		return "", fmt.Errorf("could not record the change, so nothing was written: %w", err)
	}
	logInfo("bootenv", "writing loglevel=%s to %s (U-Boot loads %s): %s", value, p.Names[target], p.Names[p.Active], bootEnvDescribe(p))
	touched, werr := bootEnvWriteFn(p, target, p.Raw[target], data)
	if werr == nil {
		// U-Boot's own rule must now load the new copy, with the new value.
		q, lerr := bootEnvLoadFn()
		switch {
		case lerr != nil:
			werr = fmt.Errorf("the settings could not be read again: %w", lerr)
		case q.Active != target || q.Level.Value != value:
			werr = fmt.Errorf("U-Boot would still load %s", q.Names[q.Active])
		}
	}
	if werr != nil && touched {
		// The slot goes back to what it held, so the copy U-Boot loaded
		// before stays the one it loads. (Even half written it would fail its
		// checksum and be passed over; this makes it exactly as it was.)
		if _, rerr := bootEnvWriteFn(p, target, nil, p.Raw[target]); rerr != nil {
			logError("bootenv", "putting %s back failed too: %v", p.Names[target], rerr)
			werr = fmt.Errorf("%v, and putting %s back failed (the copies saved before are in data/bootenv)", werr, p.Names[target])
		} else {
			werr = fmt.Errorf("%v; %s was put back as it was", werr, p.Names[target])
		}
	}
	st.Writing = ""
	if werr != nil {
		if serr := bootEnvSaveState(st); serr != nil {
			logWarn("bootenv", "could not record the failed write: %v", serr)
		}
		return "", werr
	}
	st.WroteBoot = bootID
	st.ChangedAt = bootEnvNow()
	if err := bootEnvSaveState(st); err != nil {
		logWarn("bootenv", "the change stands, but recording it failed: %v", err)
	}
	logInfo("bootenv", "loglevel=%s written to %s and read back; U-Boot loads it from the next start", value, p.Names[target])
	if on {
		return "Silent boot is on: the next power-on starts Linux quietly", nil
	}
	return "Silent boot is off: the bootloader's own log level is back from the next power-on", nil
}

// ── Probe ────────────────────────────────────────────────────────────────

// bootEnvProbe is what the Settings row knows.
type bootEnvProbe struct {
	Status   string
	Reason   string // why it is unavailable, or a note on On
	Settable bool   // A twice switches it
	Detail   string // bootEnvDescribe, for the command line
	Why      string // the whole error behind Reason, for the command line
}

// bootEnvReason turns a failure into the row's short reason; the full error
// goes to the log.
func bootEnvReason(err error) string {
	for _, known := range []error{errBootEnvUnsupported, errBootEnvNoStore, errBootEnvOneCopy, errBootEnvFormat, errBootEnvNotDriving, errBootEnvAlreadyQuiet} {
		if errors.Is(err, known) {
			return bootLogoSentence(known.Error())
		}
	}
	return "Could not read the bootloader settings (details in the log)"
}

// bootEnvRunProbe reads the device's settings and classifies them.
func bootEnvRunProbe() bootEnvProbe {
	if !bootEnvSupportedFn() {
		return bootEnvProbe{Status: bootEnvStatusUnavailable, Reason: bootEnvReason(errBootEnvUnsupported)}
	}
	p, err := bootEnvLoadFn()
	if err != nil {
		logInfo("bootenv", "unavailable: %v", err)
		return bootEnvProbe{Status: bootEnvStatusUnavailable, Reason: bootEnvReason(err), Why: err.Error()}
	}
	st := bootEnvLoadState()
	if st.Writing != "" {
		logInfo("bootenv", "a %q write was cut short earlier; U-Boot kept loading the other copy", st.Writing)
	}
	status, cerr := bootEnvClassify(p, st, strings.TrimSpace(bootOSBootIDFn()))
	detail := bootEnvDescribe(p)
	logInfo("bootenv", "%s → %s", detail, status)
	pr := bootEnvProbe{Status: status, Detail: detail, Settable: cerr == nil}
	if cerr != nil {
		logInfo("bootenv", "%v", cerr)
		pr.Reason, pr.Why = bootEnvReason(cerr), cerr.Error()
	}
	return pr
}

// bootEnvSafeProbe is bootEnvRunProbe with a panic turned into "Unavailable".
func bootEnvSafeProbe() (p bootEnvProbe) {
	defer func() {
		if r := recover(); r != nil {
			logError("bootenv", "the Silent boot check panicked: %v", r)
			p = bootEnvProbe{Status: bootEnvStatusUnavailable, Reason: "Could not read the bootloader settings (details in the log)"}
		}
	}()
	return bootEnvRunProbe()
}

// bootEnvRunOp runs one switch, with a panic turned into an error.
func bootEnvRunOp(op string) (msg string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logError("bootenv", "%s panicked: %v", op, r)
			msg, err = "", fmt.Errorf("it stopped unexpectedly (%v); details are in the log", r)
		}
	}()
	return bootEnvSwitch(op == bootEnvOpOn)
}

// ── Settings row ─────────────────────────────────────────────────────────

// bootEnvUI is the row's live state: the last probe, the operation running
// (one at a time) and the armed first press.
var bootEnvUI struct {
	mu      sync.Mutex
	started bool
	busy    string
	have    bool
	probe   bootEnvProbe
	armOp   string
	armAt   time.Time
}

// bootEnvBegin claims the row for op if nothing else is running.
func bootEnvBegin(op string) bool {
	u := &bootEnvUI
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy != "" {
		return false
	}
	u.busy = op
	u.started = true
	return true
}

// bootEnvFinish records a probe and frees the row.
func bootEnvFinish(p bootEnvProbe) {
	u := &bootEnvUI
	u.mu.Lock()
	u.probe, u.have, u.busy = p, true, ""
	u.mu.Unlock()
}

// bootEnvStartProbe checks the device off the render thread.
func bootEnvStartProbe() {
	if !bootEnvBegin(bootEnvOpProbe) {
		return
	}
	bootEnvGo(func() { bootEnvFinish(bootEnvSafeProbe()) })
}

// bootEnvSnapshot is the row's state under the lock.
func bootEnvSnapshot() (started bool, busy string, have bool, p bootEnvProbe) {
	u := &bootEnvUI
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.started, u.busy, u.have, u.probe
}

// bootEnvRowVisible shows the row on a TrimUI running its own firmware (a
// JukaHub OS card boots its own copy of the bootloader, already quiet).
func bootEnvRowVisible(*Config) bool { return !jukaosNativeActive() && bootEnvSupportedFn() }

// bootEnvRowValue is the row's read-out: cached state only; the first call
// starts the probe.
func bootEnvRowValue(*Config) string {
	started, busy, have, p := bootEnvSnapshot()
	if !started {
		bootEnvStartProbe()
		return bootEnvStatusChecking
	}
	switch {
	case busy == bootEnvOpOn || busy == bootEnvOpOff:
		return bootEnvStatusWriting
	case !have:
		return bootEnvStatusChecking
	}
	return p.Status
}

// bootEnvRowDetail is the line under the row's label.
func bootEnvRowDetail(*Config) string {
	_, busy, have, p := bootEnvSnapshot()
	switch {
	case busy == bootEnvOpOn:
		return "Setting the bootloader's log level to 1…"
	case busy == bootEnvOpOff:
		return "Putting the bootloader's log level back…"
	case !have:
		return "Linux starts without printing every message to an unused serial port"
	}
	switch p.Status {
	case bootEnvStatusOn:
		if !p.Settable {
			return setOr(p.Reason, "The kernel already starts quietly")
		}
		return "The kernel starts quietly · A twice puts the bootloader's setting back"
	case bootEnvStatusOff:
		return "The kernel prints every start-up message to an unused serial port, which costs seconds · A twice silences it"
	case bootEnvStatusOnNext:
		return "Set in the bootloader; the next power-on starts quietly · A twice undoes it"
	case bootEnvStatusOffNext:
		return "The bootloader's own level is back from the next power-on · A twice silences it again"
	case bootEnvStatusNoEffect:
		return "Set, but this bootloader still passes its own level · A twice puts the setting back"
	}
	return setOr(p.Reason, "Linux starts without printing every message to an unused serial port")
}

// bootEnvRowOpen is the row's press: the first arms, a second within four
// seconds switches Silent boot on a worker, and a toast reports the result.
func bootEnvRowOpen(*Config) {
	_, busy, have, p := bootEnvSnapshot()
	switch {
	case busy == bootEnvOpProbe:
		bootEnvToast("Still checking the bootloader…", ToastKindInfo)
		return
	case busy != "":
		bootEnvToast("The bootloader settings are still being written…", ToastKindInfo)
		return
	case !have:
		bootEnvStartProbe()
		bootEnvToast("Checking the bootloader…", ToastKindInfo)
		return
	case p.Status == bootEnvStatusUnavailable:
		bootEnvToast(setOr(p.Reason, "This bootloader can't be set"), ToastKindWarning)
		bootEnvStartProbe()
		return
	case p.Status == bootEnvStatusOn && !p.Settable:
		bootEnvToast(setOr(p.Reason, "The kernel already starts quietly")+"; nothing to change", ToastKindInfo)
		return
	}
	op := bootEnvOpOn
	if p.Status == bootEnvStatusOn || p.Status == bootEnvStatusOnNext || p.Status == bootEnvStatusNoEffect {
		op = bootEnvOpOff
	}
	now := bootEnvNow()
	u := &bootEnvUI
	u.mu.Lock()
	armed := u.armOp == op && !now.Before(u.armAt) && now.Sub(u.armAt) <= bootEnvArmWindow
	if armed {
		u.armOp = ""
	} else {
		u.armOp, u.armAt = op, now
	}
	u.mu.Unlock()
	if !armed {
		if op == bootEnvOpOn {
			bootEnvToast("Press A again to set the bootloader's log level to 1 (backed up first)", ToastKindInfo)
		} else {
			bootEnvToast("Press A again to put the bootloader's own log level back", ToastKindInfo)
		}
		return
	}
	if !bootEnvBegin(op) {
		bootEnvToast("The bootloader settings are busy; try again in a moment", ToastKindInfo)
		return
	}
	bootEnvToast("Writing the bootloader setting…", ToastKindInfo)
	bootEnvGo(func() {
		msg, err := bootEnvRunOp(op)
		bootEnvFinish(bootEnvSafeProbe())
		if err != nil {
			logWarn("bootenv", "%s: %v", op, err)
			bootEnvToast("Silent boot: "+err.Error(), ToastKindError)
			return
		}
		bootEnvToast(msg, ToastKindSuccess)
	})
}

// ── Command line ─────────────────────────────────────────────────────────

// handleSilentBootCommand runs `JukaHub silent-boot [status|on|off]` for an
// SSH shell: the same check and switch as Settings → General → Silent boot.
func handleSilentBootCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != "silent-boot" {
		return false, 0
	}
	// The logger before device detection: detection logs from inside its
	// sync.Once, and a first log line with no logger would re-enter it.
	InitLogging()
	op := "status"
	if len(args) > 1 {
		op = args[1]
	}
	code := 0
	switch op {
	case "status":
	case bootEnvOpOn, bootEnvOpOff:
		msg, err := bootEnvRunOp(op)
		if err != nil {
			fmt.Fprintln(os.Stderr, "silent-boot: "+err.Error())
			code = 1
		} else {
			fmt.Println(msg)
		}
	default:
		fmt.Fprintln(os.Stderr, "use: JukaHub silent-boot [status|on|off]")
		return true, 2
	}
	p := bootEnvSafeProbe()
	fmt.Println("Silent boot: " + p.Status)
	if p.Reason != "" {
		fmt.Println("  " + p.Reason)
	}
	if p.Why != "" && p.Why != p.Reason {
		fmt.Println("  why: " + p.Why)
	}
	if p.Detail != "" {
		fmt.Println("  " + p.Detail)
	}
	return true, code
}
