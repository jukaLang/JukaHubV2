package main

// bootframe_ubootlogo.go — Home from power-on on the Smart Pro JukaHub OS image.
//
// The Smart Pro image boots the handheld's own U-Boot from the card
// (os/smartpro/build_image.py copies every partition in front of the system to
// the same place on the card). Allwinner's U-Boot draws bootlogo.bmp from the
// small FAT partition called bootloader (boot-resource on other boards) of the
// medium it booted from, and the display driver keeps that picture up while
// the kernel starts. So when the card's copy of that file is Home, Home is on
// screen from the moment the bootloader lights the panel, seconds before the
// launcher can write the boot frame (bootframe.go) into the framebuffer.
//
// Whenever the boot frame is saved for the next boot (the first save of a
// card, the teardown), the same picture is written over the card's
// bootlogo.bmp: in place, at its own length, in its own size, depth and row
// order (bmpPatch keeps the header bytes). A file whose size is not the
// panel's (the stock 396x66 banner) is never written: the image must carry a
// full-screen logo. Before the first write the image's own logo is copied to
// the data folder; it goes back whenever Home must not show before JukaHub
// does — a startup lock, a boot animation, or Settings → JukaHub OS → "Show
// Home from power-on" off.
//
// Only the card JukaHub OS booted from is ever written: the FAT logo partition
// on the SD disk (sysfs type SD) that also holds the running system and the
// data folder, never the internal eMMC (whose partitions carry the same
// names), and only on the Smart Pro image (bootframe_ubootlogo_linux.go).
// bootlogo.go's installer, which writes the eMMC's logo on the stock firmware,
// keeps its own rules; this shares only its FAT mount and in-place write.
//
// Every write is recorded first ("interrupted"), read back and compared; a
// write that does not read back puts the image's logo back, and nothing more
// is written this boot. A write cut short (the power went mid-teardown) is
// put right at the next start: the record says so, and the first check after
// the start writes the card's boot frame again, or the image's logo.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// bootFrameUbootKey is the Custom setting behind Settings → JukaHub OS →
	// "Show Home from power-on" (on unless it is false).
	bootFrameUbootKey = "jukaosHomeAtPowerOn"
	// The files in jukaosNativeDataDir: the image's own logo, copied before
	// the first write, and the record of what the bootloader's file holds.
	bootFrameUbootBackupFile   = "ubootlogo-original.bmp"
	bootFrameUbootStateFile    = "ubootlogo.json"
	bootFrameUbootStateVersion = 1
	// bootFrameUbootPristine is the image's own logo in its read-only system,
	// when the builder keeps one there: the logo to put back even when the
	// data folder lost its copy.
	bootFrameUbootPristine = "/jukahub/bootlogo.bmp"
	// bootFrameUbootShutdownWait is the teardown row's wait when the
	// bootloader's picture is written after the boot frame.
	bootFrameUbootShutdownWait = 3 * time.Second
	// bootFrameUbootShownText is the row's line once Home is written.
	bootFrameUbootShownText = "Your last Home shows the moment the handheld turns on"
)

// What the bootloader's file holds, against the record (bootFrameUbootClassify).
const (
	bootFrameUbootNew      = "new"      // no record yet: taken as the image's own logo
	bootFrameUbootOriginal = "original" // the image's own logo
	bootFrameUbootOurs     = "ours"     // the Home picture last written
	bootFrameUbootCut      = "cut"      // a write that was not seen to finish
	bootFrameUbootOther    = "other"    // changed outside JukaHub
)

// bootFrameUbootState is ubootlogo.json in the data folder.
type bootFrameUbootState struct {
	Version     int    `json:"version"`
	Part        string `json:"part"`           // the partition's name, e.g. bootloader
	Size        int    `json:"size"`           // the logo file's length
	OriginalSHA string `json:"originalSha256"` // the image's own logo (the copy beside this file)
	OursSHA     string `json:"oursSha256,omitempty"`
	// Installed: the Home picture is in the bootloader's file. Interrupted: a
	// write began and was not seen to finish.
	Installed   bool `json:"installed"`
	Interrupted bool `json:"interrupted,omitempty"`
	// Frame names the boot frame the picture came from (bootFrameUbootFrameKey).
	Frame     string    `json:"frame,omitempty"`
	WrittenAt time.Time `json:"writtenAt"`
	// Unfit is the size of a logo Home cannot replace ("396×66"): not tried
	// again on this card unless the Settings switch is turned on again.
	Unfit string `json:"unfit,omitempty"`
}

// bootFrameUbootPart is one named partition as sysfs reports it.
type bootFrameUbootPart struct {
	Name string // GPT name (PARTNAME)
	Dev  string // kernel name, e.g. mmcblk0p1
	Disk string // the disk it is on, e.g. mmcblk0; "" when sysfs does not say
	Num  uint64 // device number
}

// bootFrameUboot is the live state. wanted is kept current by the main thread
// (bootFrameUbootTick) for the workers; ticked and lastWant are the main
// thread's; status is the Settings row's line.
var bootFrameUboot struct {
	wanted     atomic.Bool
	writeOff   atomic.Bool // a write failed or cannot fit: none more this boot
	restoreOff atomic.Bool // putting the image's logo back failed: none more this boot
	ours       atomic.Bool // the last record read or written says Home is (or may be) in the file
	known      atomic.Bool // ours has been set from the record at this boot
	ticked     bool
	lastWant   bool
	mu         sync.Mutex
	status     string
}

// bootFrameUbootSupported reports the Smart Pro image, the only one whose
// bootloader reads its logo from the card JukaHub can write.
func bootFrameUbootSupported() bool { return jukaosNativeSmartPro() }

// bootFrameUbootSettingOn reports the Settings switch (on by default).
func bootFrameUbootSettingOn(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, bootFrameUbootKey, true)
}

// bootFrameUbootWhyNot is why Home must not be the bootloader's picture now,
// "" when it may. Main thread.
func bootFrameUbootWhyNot(c *Config) string {
	switch {
	case !bootFrameUbootSupported():
		return "not the Smart Pro image"
	case !bootFrameUbootSettingOn(c):
		return "switched off in Settings"
	case bootLockKindOf(c) != "":
		return "the startup lock is on"
	case !bootStyleOff(c):
		return "a boot animation is chosen"
	}
	return ""
}

// bootFrameUbootTick keeps the bootloader's picture in step with the settings;
// bootFrameCheck calls it at every look. The boot's first check waits for the
// start-up warm-up; after it, a change (the switch, a lock, a boot animation)
// is acted on at once. Main thread.
func bootFrameUbootTick(c *Config) {
	if !bootFrameUbootSupported() {
		return
	}
	why := bootFrameUbootWhyNot(c)
	want := why == ""
	bootFrameUboot.wanted.Store(want)
	if bootFrameUboot.ticked {
		if want == bootFrameUboot.lastWant {
			return
		}
	} else if startupWarmupHeld() {
		return
	}
	boot := !bootFrameUboot.ticked
	bootFrameUboot.ticked, bootFrameUboot.lastWant = true, want
	go bootFrameUbootReconcile(why, boot, false)
}

// bootFrameUbootChanged acts on the Settings switch at once. Turning it on is
// a fresh try for this boot. Main thread.
func bootFrameUbootChanged(c *Config) {
	why := bootFrameUbootWhyNot(c)
	want := why == ""
	bootFrameUboot.wanted.Store(want)
	bootFrameUboot.ticked, bootFrameUboot.lastWant = true, want
	if want {
		bootFrameUboot.writeOff.Store(false)
	}
	go bootFrameUbootReconcile(why, false, want)
}

// bootFrameShutdownWait is the teardown row's wait: longer when the
// bootloader's picture is written after the boot frame. It also refreshes the
// wish the worker reads. Main thread.
func bootFrameShutdownWait(c *Config) time.Duration {
	const plain = 1500 * time.Millisecond
	if !bootFrameUbootSupported() {
		return plain
	}
	want := bootFrameUbootWhyNot(c) == ""
	bootFrameUboot.wanted.Store(want)
	if want && !bootFrameUboot.writeOff.Load() {
		return bootFrameUbootShutdownWait
	}
	return plain
}

// bootFrameUbootReconcile makes the bootloader's file what the settings ask:
// the card's boot frame when Home may show, the image's own logo when it may
// not. why is the reason Home may not show ("" when it may); boot marks the
// start's own check, which logs what this power-on showed; retry (the switch
// turned on) tries a logo found unfit before once more. Workers.
func bootFrameUbootReconcile(why string, boot, retry bool) {
	bootFrameIO.Lock()
	defer bootFrameIO.Unlock()
	if why == "" && !bootFrameUboot.wanted.Load() {
		// The switch flipped back while this waited: the newest wish (wanted,
		// set on the main thread before every spawn) wins over this one.
		why = "a newer setting"
	}
	if why != "" || bootFrameBlocked.Load() {
		if why == "" {
			why = "the startup lock is on"
		}
		if boot {
			logInfo("bootframe", "U-Boot logo: Home is not the power-on picture (%s)", why)
		}
		bootFrameUbootRestoreLocked(why)
		return
	}
	st, err := bootFrameUbootLoad()
	if err != nil {
		bootFrameUbootStop("Stopped: its record can't be read (details in the log)", "%s cannot be read: %v", bootFrameUbootStateFile, err)
		return
	}
	if st.Unfit != "" && !retry {
		if boot {
			logInfo("bootframe", "U-Boot logo: not used, the image's logo is %s and not full-screen", st.Unfit)
		}
		bootFrameUbootSay(bootFrameUbootUnfitText(st.Unfit))
		return
	}
	key := bootFrameUbootFrameKey()
	if st.Installed && !st.Interrupted {
		if boot {
			logInfo("bootframe", "U-Boot logo: this power-on showed Home (written %s)", st.WrittenAt.Local().Format("2006-01-02 15:04"))
		}
		if key == "" || key == st.Frame {
			bootFrameUbootSay(bootFrameUbootShownText)
			return
		}
	} else if boot {
		what := "the image's own logo"
		if st.Interrupted {
			what = "a picture whose write was cut short"
		}
		logInfo("bootframe", "U-Boot logo: this power-on showed %s", what)
	}
	if key == "" {
		// No boot frame on the card yet: the first save writes both.
		return
	}
	pix, info, err := bootFrameUbootReadCard()
	if err != nil {
		logInfo("bootframe", "U-Boot logo: the card's boot frame can't be read (%v); left as it is", err)
		return
	}
	bootFrameUbootWriteLocked(pix, info, key, retry)
}

// bootFrameUbootAfterSave writes the boot frame bootFrameSave has just put on
// the card as the bootloader's picture too. Called with bootFrameIO held.
func bootFrameUbootAfterSave(pix []byte, info string) {
	if !bootFrameUbootSupported() || !bootFrameUboot.wanted.Load() || bootFrameBlocked.Load() {
		return
	}
	bootFrameUbootWriteLocked(pix, info, bootFrameUbootFrameKey(), false)
}

// bootFrameUbootForgetLocked is bootFrameForget's part: the boot frame is gone
// (a startup lock, a boot animation), so the bootloader's picture goes back to
// the image's logo. Called with bootFrameIO held.
func bootFrameUbootForgetLocked() {
	if bootFrameUbootSupported() {
		bootFrameUbootRestoreLocked("the startup lock or a boot animation")
	}
}

// bootFrameUbootWriteLocked writes pix (a framebuffer page described by info)
// over the bootloader's logo; retry also tries a logo recorded as unfit.
// Called with bootFrameIO held.
func bootFrameUbootWriteLocked(pix []byte, info, key string, retry bool) {
	if bootFrameUboot.writeOff.Load() {
		return
	}
	st, err := bootFrameUbootLoad()
	if err != nil {
		bootFrameUbootStop("Stopped: its record can't be read (details in the log)", "%s cannot be read: %v", bootFrameUbootStateFile, err)
		return
	}
	if st.Unfit != "" && !retry {
		bootFrameUboot.writeOff.Store(true)
		bootFrameUbootSay(bootFrameUbootUnfitText(st.Unfit))
		return
	}
	w, h, bpp, stride, err := bootFrameParseInfo(info)
	if err == nil && len(pix) < stride*h {
		err = fmt.Errorf("%d bytes for a %d-byte page", len(pix), stride*h)
	}
	if err != nil {
		bootFrameUbootStop("Stopped: the kept Home picture is incomplete", "the kept picture: %v", err)
		return
	}
	start := time.Now()
	t, err := bootFrameUbootOpen(true)
	if err != nil {
		bootFrameUbootStop("Not available: the bootloader's logo was not found on this card (details in the log)", "%v", err)
		return
	}
	where := t.Where()
	defer func() {
		if cerr := t.Close(); cerr != nil {
			logWarn("bootframe", "U-Boot logo: letting go of %s: %v", where, cerr)
		}
	}()
	cur, err := t.Read()
	if err != nil {
		bootFrameUbootStop("Stopped: the bootloader's logo can't be read (details in the log)", "reading %s: %v", where, err)
		return
	}
	bi, err := bmpParse(cur)
	if err != nil {
		bootFrameUbootStop("Not available: the bootloader's logo is a format JukaHub won't rewrite", "%s: %v", where, err)
		return
	}
	if bi.Width != w || bi.Height != h {
		// Recorded, so later boots of this card do not mount it to find out.
		st.Unfit = fmt.Sprintf("%d×%d", bi.Width, bi.Height)
		if err := bootFrameUbootSave(st); err != nil {
			logWarn("bootframe", "U-Boot logo: recording its size: %v", err)
		}
		bootFrameUbootStop(bootFrameUbootUnfitText(st.Unfit),
			"%s is %dx%d and Home is %dx%d: the image needs a full-screen logo", where, bi.Width, bi.Height, w, h)
		return
	}
	st.Unfit = ""
	curSHA := bootLogoSHA(cur)
	var orig []byte
	switch class := bootFrameUbootClassify(st, where.Part, len(cur), curSHA); class {
	case bootFrameUbootNew, bootFrameUbootOther:
		// The first write on this card, or a logo replaced outside JukaHub
		// (by hand, another tool): what is there is the one to put back. On
		// a first write the image's own copy wins when it has one — right
		// even if a lost record left an older Home in the file.
		orig = cur
		if p, perr := bootFrameUbootReadBounded(bootFrameUbootPristine); class == bootFrameUbootNew && perr == nil && bootFrameUbootSameSlot(p, cur, bi) {
			orig = p
		}
		st = bootFrameUbootState{Part: where.Part, Size: len(cur), OriginalSHA: bootLogoSHA(orig)}
		if err := bootFrameUbootKeepOriginal(orig); err != nil {
			bootFrameUbootStop("Stopped: the image's logo could not be copied to the card", "copying the logo to %s: %v", bootFrameUbootBackupFile, err)
			return
		}
		if err := bootFrameUbootSave(st); err != nil {
			bootFrameUbootStop("Stopped: its record could not be written", "recording the copy: %v", err)
			return
		}
		logInfo("bootframe", "U-Boot logo: the logo on %s (%dx%d %d-bit, %s) is kept as %s", where, bi.Width, bi.Height, bi.BitCount, class, bootFrameUbootBackupFile)
	default:
		orig, err = bootFrameUbootOriginalBytes(st)
		if err != nil && class == bootFrameUbootOriginal {
			// The file holds the logo the record names: copy it again.
			orig, err = cur, bootFrameUbootKeepOriginal(cur)
		}
		if err != nil {
			bootFrameUbootStop("Stopped: the copy of the image's logo is missing", "%v", err)
			return
		}
	}
	if !bootFrameUbootSameSlot(orig, cur, bi) {
		bootFrameUbootStop("Stopped: the image's logo changed shape", "the kept logo and %s differ in length or header", where)
		return
	}
	rgba, err := bootFrameUbootRGBA(pix, w, h, bpp, stride, bootFrameUbootLayout())
	if err != nil {
		bootFrameUbootStop("Stopped: the kept Home picture is incomplete", "%v", err)
		return
	}
	ours, err := bmpPatch(orig, bi, rgba)
	if err != nil {
		bootFrameUbootStop("Stopped: the picture does not fit the logo (details in the log)", "%v", err)
		return
	}
	oursSHA := bootLogoSHA(ours)
	st.OursSHA, st.Installed, st.Frame = oursSHA, true, key
	if oursSHA == curSHA {
		st.Interrupted = false
		if err := bootFrameUbootSave(st); err != nil {
			logWarn("bootframe", "U-Boot logo: recording it: %v", err)
		}
		bootFrameUbootSay(bootFrameUbootShownText)
		return
	}
	// The record goes first: a write cut short is known at the next start.
	st.Interrupted = true
	if err := bootFrameUbootSave(st); err != nil {
		bootFrameUbootStop("Stopped: its record could not be written", "recording the write: %v", err)
		return
	}
	if werr := bootLogoWriteVerified(t, ours, oursSHA); werr != nil {
		if rerr := bootLogoWriteVerified(t, orig, st.OriginalSHA); rerr != nil {
			bootFrameUboot.writeOff.Store(true)
			logError("bootframe", "U-Boot logo: the write on %s failed (%v) and putting the logo back failed too (%v); the next start tries again", where, werr, rerr)
			bootFrameUbootSay("Stopped: the last write failed; the next start puts the logo right")
			return
		}
		st.OursSHA, st.Installed, st.Interrupted, st.Frame = "", false, false, ""
		if err := bootFrameUbootSave(st); err != nil {
			logWarn("bootframe", "U-Boot logo: recording the logo put back: %v", err)
		}
		bootFrameUbootStop("Stopped for this start: the write did not read back (details in the log)", "%s: the write did not read back (%v); the logo that was there is back", where, werr)
		return
	}
	st.Interrupted = false
	st.WrittenAt = time.Now()
	if err := bootFrameUbootSave(st); err != nil {
		logWarn("bootframe", "U-Boot logo: recording the write: %v", err)
	}
	logInfo("bootframe", "U-Boot logo: Home picture written (%dx%d %d-bit) on %s in %.2f s", bi.Width, bi.Height, bi.BitCount, where, time.Since(start).Seconds())
	bootFrameUbootSay(bootFrameUbootShownText)
}

// bootFrameUbootRestoreLocked puts the image's own logo back when the Home
// picture is (or may be) in the bootloader's file. Called with bootFrameIO
// held.
func bootFrameUbootRestoreLocked(why string) {
	if !bootFrameUbootSupported() || bootFrameUboot.restoreOff.Load() {
		return
	}
	fail := func(format string, args ...any) {
		bootFrameUboot.restoreOff.Store(true)
		logError("bootframe", "U-Boot logo: Home could not be taken off the power-on picture (%s): %s", why, fmt.Sprintf(format, args...))
		bootFrameUbootSay("Home could not be taken off the power-on picture (details in the log)")
		ShowToast("Home could not be taken off the power-on picture: details in the log", ToastKindWarning)
	}
	st, err := bootFrameUbootLoad()
	if err != nil {
		fail("%s cannot be read: %v", bootFrameUbootStateFile, err)
		return
	}
	if st.OriginalSHA == "" || (!st.Installed && !st.Interrupted) {
		return
	}
	orig, err := bootFrameUbootOriginalBytes(st)
	if err != nil {
		fail("%v", err)
		return
	}
	t, err := bootFrameUbootOpen(true)
	if err != nil {
		fail("%v", err)
		return
	}
	where := t.Where()
	defer func() {
		if cerr := t.Close(); cerr != nil {
			logWarn("bootframe", "U-Boot logo: letting go of %s: %v", where, cerr)
		}
	}()
	cur, err := t.Read()
	if err != nil {
		fail("reading %s: %v", where, err)
		return
	}
	switch bootFrameUbootClassify(st, where.Part, len(cur), bootLogoSHA(cur)) {
	case bootFrameUbootOriginal:
		// Already back (a write that was put back, a record not updated).
	case bootFrameUbootOurs, bootFrameUbootCut:
		if len(orig) != len(cur) {
			fail("the kept logo is %d bytes, %s is %d", len(orig), where, len(cur))
			return
		}
		st.Interrupted = true
		if err := bootFrameUbootSave(st); err != nil {
			fail("recording the restore: %v", err)
			return
		}
		if err := bootLogoWriteVerified(t, orig, st.OriginalSHA); err != nil {
			fail("writing %s: %v", where, err)
			return
		}
		logInfo("bootframe", "U-Boot logo: the image's own logo is back on %s (%s)", where, why)
	default:
		// Not JukaHub's picture any more: whoever replaced it keeps it.
		logInfo("bootframe", "U-Boot logo: %s was replaced outside JukaHub; left as it is", where)
	}
	st.OursSHA, st.Installed, st.Interrupted, st.Frame = "", false, false, ""
	if err := bootFrameUbootSave(st); err != nil {
		logWarn("bootframe", "U-Boot logo: recording the restore: %v", err)
	}
	bootFrameUbootSay("")
}

// bootFrameUbootClassify names what the bootloader's file (sum, size bytes on
// the partition called part) holds, against the record.
func bootFrameUbootClassify(st bootFrameUbootState, part string, size int, sum string) string {
	switch {
	case st.OriginalSHA == "":
		return bootFrameUbootNew
	case st.Part != part || st.Size != size:
		return bootFrameUbootOther
	case sum == st.OriginalSHA:
		return bootFrameUbootOriginal
	case st.OursSHA != "" && sum == st.OursSHA:
		return bootFrameUbootOurs
	case st.Interrupted:
		return bootFrameUbootCut
	}
	return bootFrameUbootOther
}

// bootFrameUbootPick chooses the bootloader's logo store among parts: the one
// FAT logo partition (boot-resource or bootloader) on the disk that holds both
// the running system (rootDev) and the data folder (dataDev), which must be an
// SD card. The internal eMMC carries partitions of the same names; they are
// never picked. A raw bootlogo partition on the card, two FAT stores, or a
// logo partition sysfs cannot place on a disk is a doubt, not a choice.
func bootFrameUbootPick(rootDev, dataDev uint64, rootDisk, dataDisk, diskType string, parts []bootFrameUbootPart) (bootFrameUbootPart, error) {
	switch {
	case rootDisk == "" || rootDisk != dataDisk:
		return bootFrameUbootPart{}, fmt.Errorf("the system (%s) and the data folder (%s) are not on one disk", setOr(rootDisk, "?"), setOr(dataDisk, "?"))
	case !strings.HasPrefix(rootDisk, "mmcblk") || diskType != "SD":
		return bootFrameUbootPart{}, fmt.Errorf("%s is not an SD card (sysfs type %q)", rootDisk, diskType)
	}
	var found []bootFrameUbootPart
	for _, p := range parts {
		// The names bootlogo_linux.go knows (bootLogoFATNames, bootLogoRawName).
		fat := p.Name == "boot-resource" || p.Name == "bootloader"
		if !fat && p.Name != "bootlogo" {
			continue
		}
		if p.Disk == "" {
			return bootFrameUbootPart{}, fmt.Errorf("sysfs does not say which disk %s (%s) is on", p.Dev, p.Name)
		}
		if p.Disk != rootDisk {
			continue // the internal storage's own logo: never touched here
		}
		if !fat {
			return bootFrameUbootPart{}, fmt.Errorf("%s has a raw %s partition (%s); only a FAT logo is written", rootDisk, p.Name, p.Dev)
		}
		found = append(found, p)
	}
	if len(found) != 1 {
		return bootFrameUbootPart{}, fmt.Errorf("%s has %d boot-resource/bootloader partitions, not one", rootDisk, len(found))
	}
	p := found[0]
	if p.Num == 0 || p.Num == rootDev || p.Num == dataDev {
		return bootFrameUbootPart{}, fmt.Errorf("%s (%s) holds the system or the data folder", p.Dev, p.Name)
	}
	return p, nil
}

// ── Pieces ────────────────────────────────────────────────────────────────

// bootFrameParseInfo reads bootframe.info: "xres yres bits_per_pixel stride".
func bootFrameParseInfo(info string) (w, h, bpp, stride int, err error) {
	f := strings.Fields(info)
	if len(f) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("an odd description %q", strings.TrimSpace(info))
	}
	var n [4]int
	for i, s := range f {
		v, e := strconv.Atoi(s)
		if e != nil || v <= 0 {
			return 0, 0, 0, 0, fmt.Errorf("an odd description %q", strings.TrimSpace(info))
		}
		n[i] = v
	}
	w, h, bpp, stride = n[0], n[1], n[2], n[3]
	if w > bootLogoMaxSide || h > bootLogoMaxSide || stride > bootFrameMaxBytes || (bpp != 32 && bpp != 16) ||
		stride < w*bpp/8 || stride*h > bootFrameMaxBytes {
		return 0, 0, 0, 0, fmt.Errorf("a %dx%d %d-bit page at stride %d", w, h, bpp, stride)
	}
	return w, h, bpp, stride, nil
}

// bootFrameUbootRGBA turns a kept framebuffer page into straight RGBA rows,
// top row first, for bmpPatch. A 32-bit page has its colours where lay says;
// a 16-bit one is RGB565.
func bootFrameUbootRGBA(pix []byte, w, h, bpp, stride int, lay castPixLayout) ([]byte, error) {
	if w <= 0 || h <= 0 || stride < w*bpp/8 || len(pix) < stride*h {
		return nil, fmt.Errorf("a %d-byte page for %dx%d at stride %d", len(pix), w, h, stride)
	}
	if bpp == 32 && (lay.bpp != 4 || lay.r < 0 || lay.r > 3 || lay.g < 0 || lay.g > 3 || lay.b < 0 || lay.b > 3) {
		return nil, errors.New("the framebuffer's colour layout is unknown")
	}
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*bpp/8]
		o := out[y*w*4 : (y+1)*w*4]
		switch bpp {
		case 32:
			for x := 0; x < w; x++ {
				p := row[x*4 : x*4+4]
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = p[lay.r], p[lay.g], p[lay.b], 0xFF
			}
		case 16:
			for x := 0; x < w; x++ {
				v := uint16(row[x*2]) | uint16(row[x*2+1])<<8
				r, g, b := uint8(v>>11)&0x1F, uint8(v>>5)&0x3F, uint8(v)&0x1F
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = r<<3|r>>2, g<<2|g>>4, b<<3|b>>2, 0xFF
			}
		default:
			return nil, fmt.Errorf("a %d-bit page", bpp)
		}
	}
	return out, nil
}

// bootFrameUbootLayout is where the framebuffer keeps each colour of a 32-bit
// pixel, asked of the driver now (the kept page is this panel's). ARGB8888
// (B, G, R, A in memory) when the driver cannot say.
func bootFrameUbootLayout() castPixLayout {
	f, err := os.Open(castFBDevice)
	if err != nil {
		return castLayoutBGRA
	}
	defer f.Close()
	g, err := castFBIoctlGeom(f)
	if err != nil || g.BPP != 32 {
		return castLayoutBGRA
	}
	return g.Layout
}

// bootFrameUbootSameSlot reports whether a and b fit one logo slot: the same
// length and the same header bytes, so either can be written over the other.
func bootFrameUbootSameSlot(a, b []byte, bi bmpInfo) bool {
	return len(a) == len(b) && bi.DataOffset > 0 && bi.DataOffset <= len(a) && bytes.Equal(a[:bi.DataOffset], b[:bi.DataOffset])
}

// bootFrameUbootFrameKey names the boot frame on the card by its length and
// time ("" when there is none): the record says which one the bootloader
// shows. The time is in FAT's own steps of two seconds: the kernel keeps a
// finer time in memory than the card stores, so the key taken right after the
// save must still match the one read back after the restart.
func bootFrameUbootFrameKey() string {
	dir := jukaosNativeDataDir()
	fi, err := os.Stat(filepath.Join(dir, bootFrameFile))
	if err != nil || !fileExists(filepath.Join(dir, bootFrameInfoFile)) {
		return ""
	}
	return strconv.FormatInt(fi.Size(), 10) + " " + strconv.FormatInt(fi.ModTime().Unix()&^1, 10)
}

// bootFrameUbootReadCard reads the boot frame the card holds.
func bootFrameUbootReadCard() ([]byte, string, error) {
	dir := jukaosNativeDataDir()
	info, err := bootFrameUbootReadBounded(filepath.Join(dir, bootFrameInfoFile))
	if err != nil {
		return nil, "", err
	}
	_, h, _, stride, err := bootFrameParseInfo(string(info))
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, bootFrameFile)
	if fi, err := os.Stat(path); err != nil {
		return nil, "", err
	} else if fi.Size() != int64(stride*h) {
		return nil, "", fmt.Errorf("%s is %d bytes, not %d", bootFrameFile, fi.Size(), stride*h)
	}
	pix, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(pix) != stride*h {
		return nil, "", fmt.Errorf("%s changed while it was read", bootFrameFile)
	}
	return pix, string(info), nil
}

// bootFrameUbootReadBounded reads a small file, refusing one past the logo
// limit (the data folder is the player's to fill).
func bootFrameUbootReadBounded(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > bootLogoMaxBytes {
		return nil, fmt.Errorf("%s is not a file of at most 8 MiB", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// bootFrameUbootOriginalBytes is the image's own logo, from the copy in the
// data folder or the image's read-only one, checked against the record.
func bootFrameUbootOriginalBytes(st bootFrameUbootState) ([]byte, error) {
	for _, path := range []string{filepath.Join(jukaosNativeDataDir(), bootFrameUbootBackupFile), bootFrameUbootPristine} {
		if data, err := bootFrameUbootReadBounded(path); err == nil && bootLogoSHA(data) == st.OriginalSHA {
			return data, nil
		}
	}
	return nil, fmt.Errorf("the copy of the logo (%s) is missing or changed", bootFrameUbootBackupFile)
}

// bootFrameUbootKeepOriginal copies the logo to put back into the data folder,
// synced and read back.
func bootFrameUbootKeepOriginal(data []byte) error {
	dir := jukaosNativeDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, bootFrameUbootBackupFile)
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	back, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(back, data) {
		return errors.New("the copy did not read back intact")
	}
	return nil
}

// bootFrameUbootLoad reads the record: empty when there is none, an error when
// one exists and cannot be read (nothing is written then).
func bootFrameUbootLoad() (bootFrameUbootState, error) {
	var st bootFrameUbootState
	if _, err := readJSONWithBackup(filepath.Join(jukaosNativeDataDir(), bootFrameUbootStateFile), &st); err != nil {
		if os.IsNotExist(err) {
			bootFrameUboot.ours.Store(false)
			bootFrameUboot.known.Store(true)
			return bootFrameUbootState{}, nil
		}
		return bootFrameUbootState{}, err
	}
	if st.Version != bootFrameUbootStateVersion {
		return bootFrameUbootState{}, fmt.Errorf("written by another JukaHub version (%d)", st.Version)
	}
	bootFrameUboot.ours.Store(st.Installed || st.Interrupted)
	bootFrameUboot.known.Store(true)
	return st, nil
}

// bootFrameUbootSave writes the record through the crash-safe save path.
func bootFrameUbootSave(st bootFrameUbootState) error {
	st.Version = bootFrameUbootStateVersion
	payload, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(jukaosNativeDataDir(), bootFrameUbootStateFile), payload, 0o644); err != nil {
		return err
	}
	bootFrameUboot.ours.Store(st.Installed || st.Interrupted)
	bootFrameUboot.known.Store(true)
	return nil
}

// bootFrameUbootShutdownStep is the teardown's row while Home must not show
// at power-on but may still be the bootloader's picture: a restore still
// running is waited for here (bootFrameIO), or done now, before the power goes.
// A power-off before any worker read the record this boot (the first look
// waits for the warm-up) gets the row too: the record may say Home is there.
// ok is false when nothing needs it. Main thread.
func bootFrameUbootShutdownStep(c *Config) (step shutdownStep, ok bool) {
	if !bootFrameUbootSupported() || (bootFrameUboot.known.Load() && !bootFrameUboot.ours.Load()) {
		return shutdownStep{}, false
	}
	why := bootFrameUbootWhyNot(c)
	if why == "" {
		return shutdownStep{}, false
	}
	bootFrameUboot.wanted.Store(false)
	return shutdownStep{label: "Putting the power-on logo back", group: 2, timeout: 2 * time.Second, fn: func() (string, error) {
		bootFrameIO.Lock()
		defer bootFrameIO.Unlock()
		bootFrameUbootRestoreLocked(why)
		if !bootFrameUboot.known.Load() || bootFrameUboot.ours.Load() {
			return "", errors.New("the bootloader may still show Home")
		}
		return "the image's logo shows at power-on", nil
	}}, true
}

// bootFrameUbootStop ends the writes for this boot: logged, and said on the
// Settings row.
func bootFrameUbootStop(row, format string, args ...any) {
	bootFrameUboot.writeOff.Store(true)
	logWarn("bootframe", "U-Boot logo: %s; not written again this boot", fmt.Sprintf(format, args...))
	bootFrameUbootSay(row)
}

// bootFrameUbootUnfitText is the row's line for a logo Home cannot replace.
func bootFrameUbootUnfitText(size string) string {
	return "Not available: this image's power-on logo is " + size + ", not the full screen"
}

func bootFrameUbootSay(s string) {
	bootFrameUboot.mu.Lock()
	bootFrameUboot.status = s
	bootFrameUboot.mu.Unlock()
}

func bootFrameUbootStatus() string {
	bootFrameUboot.mu.Lock()
	defer bootFrameUboot.mu.Unlock()
	return bootFrameUboot.status
}

// ── Settings ──────────────────────────────────────────────────────────────

// bootFrameUbootSettingsRow is Settings → JukaHub OS → "Show Home from
// power-on", on the Smart Pro image.
func bootFrameUbootSettingsRow() setRow {
	return setRow{
		ID: bootFrameUbootKey, Label: "Show Home from power-on", Kind: setKindToggle,
		Detail:   bootFrameUbootRowDetail,
		Disabled: func(c *Config) bool { return bootLockKindOf(c) != "" || !bootStyleOff(c) },
		Get:      bootFrameUbootSettingOn,
		Set: func(c *Config, on bool) {
			setStoreBool(c, bootFrameUbootKey, on)
			bootFrameUbootChanged(c)
			if on {
				setToast("Home shows from the next power-on")
			} else {
				setToast("The image's own logo shows at power-on again")
			}
		},
	}
}

// bootFrameUbootRowDetail is the row's second line. Every frame: it reads only
// the config and the cached status.
func bootFrameUbootRowDetail(c *Config) string {
	switch {
	case bootLockKindOf(c) != "":
		return "Off while the startup lock is on: the image's logo shows until the code is entered"
	case !bootStyleOff(c):
		return "Off while a boot animation is chosen"
	case !bootFrameUbootSettingOn(c):
		return "The image's own logo shows at power-on, and Home once the card is read"
	}
	if s := bootFrameUbootStatus(); s != "" {
		return s
	}
	return "The bootloader shows your last Home, so it is there the moment the handheld turns on"
}
