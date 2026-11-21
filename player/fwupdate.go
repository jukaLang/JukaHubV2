package main

// fwupdate.go — Settings → About → Stock firmware: the TrimUI system under
// JukaHub, and how to update it.
//
// Users of JukaHub ask the same two questions — "which firmware do I have?" and "I
// downloaded the update, now what?" — so this row answers them on the TrimUI
// handhelds:
//
//   - the stock version, read from /etc/version (it tells Smart Pro S firmware
//     1.0.1 from 1.0.2). Its exact format is not
//     documented, so the first line that holds a version number is shown as
//     it is, never parsed into something it might not be;
//   - an update image on the card: TrimUI's updater, in the handheld's own
//     bootloader, reads trimui_<model>.awimg from the root of a FAT32 card.
//     The row finds such a file at the root, a copy left one folder down (a
//     zip unpacked into its own folder) and a download still packed, and says
//     what is wrong with each: wrong place, still packed, a name that is not
//     the one the updater looks for, or — the dangerous one — the image of
//     another model (the file name carries the model code: tg5040 Smart Pro,
//     tg5050 Smart Pro S, tg3040 Brick and Hammer, tg4040 Brick Pro);
//   - the steps to start the flash for this model, the battery level (TrimUI
//     asks for more than 50%) and the card's format.
//
// JukaHub never flashes anything: the update runs in the bootloader, before
// any system starts, and only the user's buttons start it. The one write this
// file makes is deleting an update file the user asked to delete.
//
// The JukaHub OS image (os/plastron, os/smartpro) is something else: a card
// that starts its own copy of the system, so the handheld's internal system —
// what TrimUI's updater writes — is not what runs, and the sheet says so
// instead of offering steps. A JukaHub OS session on the stock firmware
// (jukaos.go) runs on the stock system, so the updater applies there as usual.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// fwUpdateScanTTL is how long the row trusts its last look at the card.
	fwUpdateScanTTL = 30 * time.Second
	// fwUpdateMinBattery is the charge TrimUI asks for before a flash.
	fwUpdateMinBattery = 50
	// fwVersionMaxRunes bounds the version line the sheet shows.
	fwVersionMaxRunes = 48
	// fwVersionFileMax bounds a version file read; the real one is one line.
	fwVersionFileMax = 4 << 10
)

// fwStockVersionFiles are where a TrimUI image may keep its version, most
// likely first. A variable so a test can point it elsewhere.
var fwStockVersionFiles = []string{
	"/etc/version",
	"/usr/trimui/version",
	"/etc/trimui_version",
	"/etc/trimui/version",
}

var (
	// fwModelCodeRe finds a TrimUI model code in a file name.
	fwModelCodeRe = regexp.MustCompile(`(?i)tg[0-9]{4}`)
	// fwVersionNumberRe is a version number inside a line of text.
	fwVersionNumberRe = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)
)

// fwImagePlace says where an update file was found.
type fwImagePlace int

const (
	fwAtRoot   fwImagePlace = iota // the card's root: where the updater looks
	fwInFolder                     // one folder down: needs moving up
	fwPacked                       // a .zip or .7z: needs unpacking
)

// fwImage is one update file on the card.
type fwImage struct {
	Path  string
	Name  string
	Size  int64
	Code  string // the model code its name carries ("TG5040"), "" when none
	Place fwImagePlace
}

// fwStockVersion is the stock firmware's version line, "" when no file names
// one or this is not Linux. The file only changes with a flash, which reboots.
var fwStockVersion = sync.OnceValue(func() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	for _, path := range fwStockVersionFiles {
		if v := fwParseVersionText(fwReadSmall(path)); v != "" {
			return v
		}
	}
	return ""
})

// fwReadSmall reads at most fwVersionFileMax bytes of path, "" on any error.
func fwReadSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, fwVersionFileMax))
	if err != nil {
		return ""
	}
	return strings.Trim(string(data), "\x00")
}

// fwParseVersionText is the first line of a version file that holds a
// version number, trimmed and bounded. Pure.
func fwParseVersionText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || !fwVersionNumberRe.MatchString(line) {
			continue
		}
		if r := []rune(line); len(r) > fwVersionMaxRunes {
			line = string(r[:fwVersionMaxRunes]) + "…"
		}
		return line
	}
	return ""
}

// fwShortVersion is the number alone, for the row's right-hand value.
func fwShortVersion(line string) string {
	if v := fwVersionNumberRe.FindString(line); v != "" {
		return v
	}
	return line
}

// fwModelCode is the upper-case model code a file name carries, "" for none.
func fwModelCode(name string) string {
	return strings.ToUpper(fwModelCodeRe.FindString(name))
}

// fwCodeFits reports whether an image's model code is this model's: known is
// false when either side has no code. The Hammer shares the Brick's TG3040
// board (hardwareModelID), and TG4070 is another name of the Brick Pro.
func fwCodeFits(code string, d DeviceModel) (fits, known bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	want := d.hardwareModelID()
	if code == "" || want == "" {
		return false, false
	}
	if code == want {
		return true, true
	}
	if alias, ok := deviceModelCodeAliases[code]; ok {
		return alias == d, true
	}
	return false, true
}

// fwBrickDoubt reports an image for the other Brick-family model. The Brick
// and the Brick Pro share the chip and the panel resolution, and a Brick Pro
// whose image names neither its model nor its code reads as a Brick
// (device_probe.go), so here "another model" may be the detection's mistake.
// The Smart Pro and the Smart Pro S are told apart by their chip, which does
// not err, and TrimUI warns against mixing their files: no doubt there.
func fwBrickDoubt(code string, d DeviceModel) bool {
	other := DeviceModelForHardwareID(code)
	return other != DeviceUnknown && other != d &&
		deviceFamily(other) == DeviceTrimuiBrick && deviceFamily(d) == DeviceTrimuiBrick
}

// fwExpectedName is the file name TrimUI's updater looks for on this model,
// "" when the model has no code.
func fwExpectedName(d DeviceModel) string {
	code := d.hardwareModelID()
	if code == "" {
		return ""
	}
	return "trimui_" + strings.ToLower(code) + ".awimg"
}

// fwPackedName reports a download that still holds the image: a .zip or .7z
// named after TrimUI's firmware. Recovery-card images are another procedure
// and are left out.
func fwPackedName(lower string) bool {
	if !strings.HasSuffix(lower, ".zip") && !strings.HasSuffix(lower, ".7z") {
		return false
	}
	if strings.Contains(lower, "recovery") {
		return false
	}
	return strings.Contains(lower, "awimg") || (strings.Contains(lower, "trimui") && fwModelCodeRe.MatchString(lower))
}

// fwFolderName reports a top-level folder an unpacked firmware download may
// have landed in. Only these are opened, so a full card is never walked.
func fwFolderName(lower string) bool {
	if strings.HasPrefix(lower, ".") {
		return false
	}
	return strings.Contains(lower, "trimui") || strings.Contains(lower, "firmware") || fwModelCodeRe.MatchString(lower)
}

// fwScanCard lists the update files on the card at root: images at the root,
// images one firmware-looking folder down, and packed downloads at the root.
// Sorted root first, then by name.
func fwScanCard(root string) []fwImage {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []fwImage
	add := func(dir, name string, place fwImagePlace) {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		out = append(out, fwImage{Path: path, Name: name, Size: info.Size(), Code: fwModelCode(name), Place: place})
	}
	for _, e := range entries {
		name := e.Name()
		lower := strings.ToLower(name)
		switch {
		case !e.IsDir() && strings.HasSuffix(lower, ".awimg"):
			add(root, name, fwAtRoot)
		case !e.IsDir() && fwPackedName(lower):
			add(root, name, fwPacked)
		case e.IsDir() && fwFolderName(lower):
			dir := filepath.Join(root, name)
			sub, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, s := range sub {
				if !s.IsDir() && strings.HasSuffix(strings.ToLower(s.Name()), ".awimg") {
					add(dir, s.Name(), fwInFolder)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Place != out[j].Place {
			return out[i].Place < out[j].Place
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// fwReady is the image the updater would take: at the root, this model's code,
// under the expected name. ok is false when there is none.
func fwReady(images []fwImage, d DeviceModel) (fwImage, bool) {
	want := fwExpectedName(d)
	for _, img := range images {
		if img.Place != fwAtRoot {
			continue
		}
		if fits, _ := fwCodeFits(img.Code, d); fits && strings.EqualFold(img.Name, want) {
			return img, true
		}
	}
	return fwImage{}, false
}

// fwImageLine is one file's verdict for the sheet.
func fwImageLine(img fwImage, d DeviceModel) string {
	line := img.Name + " · " + formatSize(img.Size)
	fits, known := fwCodeFits(img.Code, d)
	switch {
	case known && !fits && fwBrickDoubt(img.Code, d):
		other := deviceDisplayName(DeviceModelForHardwareID(img.Code))
		return line + " · for the " + other + ". This handheld reads as a " + deviceDisplayName(d) +
			": flash it only if it really is a " + other + ", otherwise delete it"
	case known && !fits:
		return line + " · for another model (" + img.Code + "): do not flash it on this " + deviceDisplayName(d) + "; delete it"
	case img.Place == fwPacked:
		return line + " · still packed: unpack it and put the .awimg file itself at the card's root"
	case img.Place == fwInFolder:
		return line + " · in the folder " + filepath.Base(filepath.Dir(img.Path)) + ": move it to the card's root"
	case !known:
		return line + " · its name names no model: TrimUI's files are called " + fwExpectedName(d)
	case !strings.EqualFold(img.Name, fwExpectedName(d)):
		return line + " · for this model, but the updater may only find it as " + fwExpectedName(d) + ": rename it"
	}
	return line + " · for this model, ready"
}

// fwFlashSteps are the buttons that start TrimUI's updater on model d, from
// TrimUI's own instructions (docs/DEVICES.md "Stock firmware updates").
func fwFlashSteps(d DeviceModel) []string {
	last := "Leave the handheld alone: it restarts by itself when the update is done. Never power it off or take the card out meanwhile."
	if d == DeviceTrimuiSmartProS {
		return []string{
			"With the card in, hold Volume − and briefly press the RESET button.",
			"Let go of Volume − when the green progress bar appears.",
			last,
		}
	}
	return []string{
		"Power the handheld off, with the card in.",
		"Hold Volume − and press Power.",
		"Let go of Power when the logo appears; keep holding Volume − until the green progress bar appears.",
		last,
	}
}

// fwReleasePage is TrimUI's download page for model d, "" when none is known.
func fwReleasePage(d DeviceModel) string {
	switch d {
	case DeviceTrimuiSmartPro:
		return "https://github.com/trimui/firmware_smartpro/releases"
	case DeviceTrimuiSmartProS:
		return "https://github.com/trimui/firmware_smartpro_s/releases"
	case DeviceTrimuiBrick, DeviceTrimuiHammer:
		return "https://github.com/trimui/firmware_brick/releases"
	}
	return ""
}

// ── The row ──────────────────────────────────────────────────────────────

// fwScanCache is the row's last look at the card, refreshed off the render
// thread when it is older than fwUpdateScanTTL.
var fwScanCache struct {
	mu     sync.Mutex
	at     time.Time
	images []fwImage
	busy   atomic.Bool
}

// fwCachedImages returns the last scan and starts a new one when it is stale.
func fwCachedImages() []fwImage {
	fwScanCache.mu.Lock()
	images, at := fwScanCache.images, fwScanCache.at
	fwScanCache.mu.Unlock()
	if (at.IsZero() || time.Since(at) > fwUpdateScanTTL) && fwScanCache.busy.CompareAndSwap(false, true) {
		go func() {
			defer fwScanCache.busy.Store(false)
			fwStoreScan(fwScanCard(bkupCardRoot()))
		}()
	}
	return images
}

// fwStoreScan keeps a scan for the row.
func fwStoreScan(images []fwImage) {
	fwScanCache.mu.Lock()
	fwScanCache.images, fwScanCache.at = images, time.Now()
	fwScanCache.mu.Unlock()
}

// fwUpdateRowVisible: only a TrimUI handheld has TrimUI firmware.
func fwUpdateRowVisible(*Config) bool {
	return P().IsHandheld() && IsTrimuiDevice()
}

// fwUpdateSettingsRow is Settings → About → Stock firmware.
func fwUpdateSettingsRow() setRow {
	return setRow{
		ID: "stockFirmware", Label: "Stock firmware", Kind: setKindAction,
		Visible: fwUpdateRowVisible,
		Detail:  fwUpdateDetail,
		Value: func(*Config) string {
			if jukaosNativeActive() {
				return "JukaHub OS"
			}
			if v := fwStockVersion(); v != "" {
				return fwShortVersion(v)
			}
			return "Unknown"
		},
		Open: fwUpdateOpen,
	}
}

// fwUpdateDetail is the row's second line: the update file's state first,
// since that is what the user came for.
func fwUpdateDetail(*Config) string {
	if jukaosNativeActive() {
		return "JukaHub OS image: TrimUI's updater does not apply"
	}
	d := DetectDevice()
	images := fwCachedImages()
	if img, ok := fwReady(images, d); ok {
		return "Update file ready: " + img.Name + " · open for the steps"
	}
	for _, img := range images {
		if fits, known := fwCodeFits(img.Code, d); known && !fits {
			if fwBrickDoubt(img.Code, d) {
				return img.Name + " may be for another model · open to check"
			}
			return img.Name + " is for another model: do not flash it"
		}
	}
	if len(images) > 0 {
		return "Update file found, not ready yet · open to see why"
	}
	return "TrimUI's own system under JukaHub · how to update it"
}

// fwUpdateOpen is the row's press: the sheet with the version, the files
// found and the steps. The card's root is small to list, so it is looked at
// again here rather than trusting a scan up to fwUpdateScanTTL old — off the
// render thread (a busy card can stall a listing); the sheet opens when the
// answer is in, a moment later.
func fwUpdateOpen(c *Config) {
	d := DetectDevice()
	if jukaosNativeActive() {
		tbxOpenSheet(c, tbxSheetSpec{
			title:    "Stock firmware",
			subtitle: deviceDisplayName(d),
			icon:     "chip",
			body:     fwNativeBody(),
			buttons:  []tbxSheetButton{{label: "Close", primary: true}},
		})
		return
	}
	go func() {
		root := bkupCardRoot()
		images := fwScanCard(root)
		fwStoreScan(images)
		fsType := fsTypeFor(readMounts(), root)
		tbxPost(func() { fwOpenSheet(c, d, images, fsType) })
	}()
}

// fwOpenSheet shows the sheet for a fresh look at the card. Main thread.
func fwOpenSheet(c *Config, d DeviceModel, images []fwImage, fsType string) {
	spec := tbxSheetSpec{
		title:    "Stock firmware",
		subtitle: deviceDisplayName(d) + " · JukaHub never flashes anything itself",
		icon:     "chip",
		body:     fwSheetBody(d, images, fsType, getBatteryPercent(), getBatteryCharging()),
		tall:     true,
	}
	if page := fwReleasePage(d); page != "" && len(images) == 0 {
		spec.qr = tbxQRModules(page)
		spec.qrLabel = "TrimUI's firmware downloads"
	}
	var atRoot []fwImage
	for _, img := range images {
		if img.Place == fwAtRoot {
			atRoot = append(atRoot, img)
		}
	}
	spec.buttons = []tbxSheetButton{{label: "Close", primary: true}}
	if len(atRoot) > 0 {
		label := "Delete the update file"
		if len(atRoot) > 1 {
			label = "Delete the update files"
		}
		spec.buttons = append(spec.buttons, tbxSheetButton{label: label, run: func() { fwConfirmDelete(c, atRoot) }})
	}
	tbxOpenSheet(c, spec)
}

// fwNativeBody explains why the JukaHub OS image has no stock update.
func fwNativeBody() string {
	var b strings.Builder
	b.WriteString("This handheld is running the JukaHub OS image from its card, which starts its own copy of the system.\n\n")
	b.WriteString("TrimUI's updater writes the handheld's internal storage, which this card does not use, so a firmware update changes nothing here. ")
	b.WriteString("To update the internal system, start the handheld from a card with the stock system and follow the steps under Settings → About → Stock firmware there.")
	if v := fwStockVersion(); v != "" {
		b.WriteString("\n\n## This card's system\n• Built from stock firmware " + v)
	}
	return b.String()
}

// fwSheetBody is the sheet's text. Pure apart from formatting: the caller
// reads the card, the battery and the mount table.
func fwSheetBody(d DeviceModel, images []fwImage, fsType string, battery int, charging bool) string {
	var b strings.Builder
	version := fwStockVersion()
	if version == "" {
		version = "not readable on this firmware"
	}
	b.WriteString("Installed: " + version + "\n")

	b.WriteString("\n## Update file on the card\n")
	if len(images) == 0 {
		b.WriteString("• None found. Download the newest firmware for the " + deviceDisplayName(d) + " from TrimUI")
		if fwReleasePage(d) != "" {
			b.WriteString(" (the code below)")
		}
		b.WriteString(", unpack it and copy " + setOr(fwExpectedName(d), "its .awimg file") + " to the card's root, then open this again.\n")
	}
	for _, img := range images {
		b.WriteString("• " + fwImageLine(img, d) + "\n")
	}

	b.WriteString("\n## Before you start\n")
	switch {
	case battery < 0:
		b.WriteString("• Charge the battery above " + strconv.Itoa(fwUpdateMinBattery) + "% first.\n")
	case battery <= fwUpdateMinBattery && !charging:
		b.WriteString("• Battery " + strconv.Itoa(battery) + "%: charge it above " + strconv.Itoa(fwUpdateMinBattery) + "% first. A flash cut short can leave the handheld unable to start.\n")
	default:
		b.WriteString("• Battery " + strconv.Itoa(battery) + "%: fine (TrimUI asks for more than " + strconv.Itoa(fwUpdateMinBattery) + "%).\n")
	}
	switch strings.ToLower(fsType) {
	case "vfat", "msdos", "fat", "fat32":
		b.WriteString("• Card: FAT32, which the updater reads.\n")
	case "":
	default:
		b.WriteString("• Card: " + fsType + ". TrimUI's updater reads FAT32 cards only: copy the file to the root of a small FAT32 card and update from that card instead.\n")
	}
	b.WriteString("• Your games, saves and JukaHub on the card stay as they are. TrimUI also publishes matching card files for its own menu with each firmware; update those too if you use the stock emulators.\n")

	b.WriteString("\n## How to install it\n")
	for i, step := range fwFlashSteps(d) {
		b.WriteString("• " + strconv.Itoa(i+1) + ". " + step + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fwConfirmDelete asks before removing update files from the card's root.
func fwConfirmDelete(c *Config, images []fwImage) {
	names := make([]string, 0, len(images))
	for _, img := range images {
		names = append(names, img.Name)
	}
	title := "Delete the update file?"
	if len(images) > 1 {
		title = "Delete the update files?"
	}
	setConfirm(c, title,
		strings.Join(names, ", ")+" will be deleted from the card. Download it again from TrimUI if you still want to update.",
		"trash", "Delete", func(*Config) {
			go fwDeleteImages(images)
		})
}

// fwDeleteImages removes the files (a large file on a slow card takes a
// moment, so this runs off the render thread) and toasts the result.
func fwDeleteImages(images []fwImage) {
	failed := 0
	for _, img := range images {
		if err := os.Remove(img.Path); err != nil && !os.IsNotExist(err) {
			logWarn("fwupdate", "cannot delete %s: %v", img.Path, err)
			failed++
			continue
		}
		logInfo("fwupdate", "deleted the update file %s", img.Path)
	}
	fwStoreScan(fwScanCard(bkupCardRoot()))
	if failed > 0 {
		ShowToast("Could not delete "+pluralizeCount(failed, "file", "files"), ToastKindWarning)
		return
	}
	ShowToast("Update file deleted", ToastKindInfo)
}
