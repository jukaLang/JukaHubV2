package main

// memswap.go — a safety net for 1 GB handhelds: compressed swap in RAM (zram)
// when the kernel has it, otherwise a swap file on the card.
//
// Settings → Games & emulators → "Extra memory (swap)" (memorySwap). On by
// default on TrimUI handhelds with 1.5 GB of RAM or less (the Smart Pro and
// the Brick have 1 GB), off with more; the row shows on TrimUI handhelds
// only. While any swap is already active (/proc/swaps lists one: the
// firmware's or a CFW's) JukaHub adds none and only counts it.
//
// zram comes first: it never writes to the card, and on a 4.9 kernel it is
// often built in or a module away (/sys/block/zram0, /sys/class/zram-control,
// modprobe zram). Half the RAM, at most 512 MB, of capacity, lz4 when the
// kernel offers it, priority 100. An empty zram costs nothing; a full one
// holds its pages in about a third of the room.
//
// Otherwise jukahub.swap in JukaHub's data folder: 256 MB (512 MB with more
// than 16 GB free), only with its size plus 1 GB free on the card. It is
// written whole in 1 MB pieces (a swap file must have no holes), the header by
// JukaHub itself (memSwapHeader: no mkswap needed), synced and dropped from
// the page cache every 16 MB so the write never crowds memory, paused for a
// game or a change of mind, then switched on with swapon(2). FAT32 and exFAT
// may refuse a swap file on this kernel: the swapon error is logged, the file
// removed and the refusal kept in memswap.json for this kernel and file
// system, so the card is not written again on every start.
//
// SD wear: with a file, vm.swappiness goes to 10 (put back when the setting
// is turned off; the kernel resets it at boot), so the kernel drops page
// cache long before it writes a page to the card — the card is written only
// under real pressure, the moments the model or a game would otherwise not
// start or be killed. zram keeps the kernel's own swappiness: it costs no
// card writes.
//
// When: at a quiet moment, never at boot on the render path — the first
// memory poll memSwapQuietAfter after start with no game on screen
// (memWatchTick), on a goroutine. Turning the setting off switches JukaHub's
// own swap off (swapoff moves its pages back into RAM: with less free than
// the area holds it is not tried — the kernel makes swapoff's caller the OOM
// killer's first choice — and the swap stays until the next start) and
// deletes the file.
//
// The AI and speech checks count part of the free swap (memSwapCreditMB);
// the memory read-outs do not.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// memSwapSetting is the Settings switch.
	memSwapSetting = "memorySwap"
	// memSwapFileName lives in JukaHub's data folder; memSwapStateName beside
	// it remembers a refusal and what JukaHub switched on.
	memSwapFileName  = "jukahub.swap"
	memSwapStateName = "memswap.json"
	// memSwapQuietAfter: the setup waits this long after start.
	memSwapQuietAfter = 2 * time.Minute
	memSwapFileMB     = 256
	memSwapFileBigMB  = 512
	memSwapBigFreeGB  = 16 // free space that earns the bigger file
	memSwapReserveMB  = 1024
	memSwapZramMaxMB  = 512
	// memSwapFileSwappiness: the file is a safety net, not a habit.
	memSwapFileSwappiness = 10
)

// memSwap is the setup's state. status is the Settings row's line; want the
// switch's latest position; runMu runs one setup or teardown at a time.
var memSwap struct {
	mu     sync.Mutex
	status string
	runMu  sync.Mutex
	want   atomic.Bool
	asked  atomic.Bool  // the quiet-moment setup was started this run
	cut    atomic.Int32 // swap file writes cut short this run
}

// memSwapDefaultCache holds memSwapDefaultOn's answer: 0 unknown, 1 off, 2 on.
var memSwapDefaultCache atomic.Int32

// memSwapSupported: TrimUI handhelds, where JukaHub runs as root and owns the
// memory. A PC's swap is its owner's business.
func memSwapSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// memSwapDefaultOn is the switch's default: on with 1.5 GB of RAM or less.
// The RAM is read once.
func memSwapDefaultOn() bool {
	switch memSwapDefaultCache.Load() {
	case 1:
		return false
	case 2:
		return true
	}
	on := memSwapSupported()
	if on {
		ram := deviceRAMMB()
		on = ram > 0 && ram <= memWatchSmallMB
	}
	v := int32(1)
	if on {
		v = 2
	}
	memSwapDefaultCache.Store(v)
	return on
}

// memSwapEnabled reports the switch. Main thread (the config).
func memSwapEnabled(c *Config) bool {
	def := memSwapDefaultOn()
	if c == nil {
		return def
	}
	return customBool(c.Variables.Custom, memSwapSetting, def)
}

// memSwapSettingsRow is Settings → Games & emulators → "Extra memory
// (swap)", below Automatic CPU speed.
func memSwapSettingsRow() setRow {
	return setRow{
		ID: memSwapSetting, Label: "Extra memory (swap)", Kind: setKindToggle,
		Visible: func(*Config) bool { return memSwapSupported() },
		Detail:  memSwapSettingDetail,
		Get:     memSwapEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, memSwapSetting, on)
			memSwapApply(on)
			setToast("Extra memory (swap): " + setOnOff(on))
		},
	}
}

// memSwapSettingDetail is the row's second line: the last setup's outcome,
// or what the switch does. Cheap: it is drawn every frame.
func memSwapSettingDetail(c *Config) string {
	memSwap.mu.Lock()
	s := memSwap.status
	memSwap.mu.Unlock()
	if s != "" {
		return s
	}
	if memSwapEnabled(c) {
		return "Starts a minute or two after JukaHub, when no game is running"
	}
	return "Room for the AI and big games on 1 GB: compressed memory, or a swap file on the card"
}

// memSwapSetStatus sets the row's line. Any goroutine.
func memSwapSetStatus(s string) {
	memSwap.mu.Lock()
	memSwap.status = s
	memSwap.mu.Unlock()
}

// memSwapTick starts the setup once, at the first poll memSwapQuietAfter
// after start with no game on screen. The monitor's goroutine (memWatchTick);
// the switch is read on the main thread, which owns the config.
func memSwapTick(up time.Duration) {
	if up < memSwapQuietAfter || memSwap.asked.Load() || !memSwapSupported() || gameScreenSuspended() {
		return
	}
	memSwap.asked.Store(true)
	tbxPost(func() { memSwapApply(memSwapEnabled(appConfig)) })
}

// memSwapApply switches JukaHub's swap on or off on a goroutine. Any
// goroutine; it never waits.
func memSwapApply(on bool) {
	if !memSwapSupported() {
		return
	}
	memSwap.want.Store(on)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("mem", "swap setup stopped: %v", r)
			}
		}()
		memSwap.runMu.Lock()
		defer memSwap.runMu.Unlock()
		memSwapRun(memSwap.want.Load())
	}()
}

// memSwapShouldStop tells a swap file's write to stop: the switch went off,
// or a game took the screen (it needs the card and the memory).
func memSwapShouldStop() bool {
	return !memSwap.want.Load() || gameScreenSuspended()
}

// ── /proc/swaps and the credit ──────────────────────────────────────────

// memSwapEntry is one line of /proc/swaps.
type memSwapEntry struct {
	name   string
	kind   string // "partition" or "file"
	sizeKB uint64
	usedKB uint64
}

// zram reports a compressed RAM device.
func (e memSwapEntry) zram() bool { return strings.HasPrefix(path.Base(e.name), "zram") }

// memSwapUnescape undoes the octal escapes /proc writes into a path.
var memSwapUnescape = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

// parseProcSwaps reads /proc/swaps: a header line, then "name type size used
// priority" with sizes in kB. Malformed lines are skipped. Pure.
func parseProcSwaps(data []byte) []memSwapEntry {
	var out []memSwapEntry
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] == "Filename" {
			continue
		}
		size, e1 := strconv.ParseUint(f[2], 10, 64)
		used, e2 := strconv.ParseUint(f[3], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		out = append(out, memSwapEntry{name: memSwapUnescape.Replace(f[0]), kind: f[1], sizeKB: size, usedKB: used})
	}
	return out
}

// memSwapList is the active swap areas (none where /proc/swaps is missing).
func memSwapList() []memSwapEntry {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return nil
	}
	return parseProcSwaps(data)
}

// memSwapFreeKB is the free space of the active zram devices and of the other
// swap areas, in kB.
func memSwapFreeKB() (zramKB, otherKB uint64) {
	for _, e := range memSwapList() {
		if e.usedKB >= e.sizeKB {
			continue
		}
		if e.zram() {
			zramKB += e.sizeKB - e.usedKB
		} else {
			otherKB += e.sizeKB - e.usedKB
		}
	}
	return zramKB, otherKB
}

// memSwapCreditMB is how much free swap a start of needMB may count as
// memory. Swap makes room only by moving idle anonymous pages out, so no more
// than Inactive(anon) counts. zram keeps every page it takes compressed in
// RAM (about a third of it), so it nets two thirds; half of the rest is left
// as margin (the kernel picks what goes, and moving it takes time): a third
// of what zram can take counts, half of what a file or partition can. zram,
// the higher priority, is counted first. Never more than half of needMB: the
// model or recognizer itself must sit in RAM. Sizes in kB; MB out. Pure.
func memSwapCreditMB(info memInfo, zramFreeKB, otherFreeKB, needMB uint64) uint64 {
	anon := info.InactiveAnon
	z := min(anon, zramFreeKB)
	f := min(anon-z, otherFreeKB)
	return min((z/3+f/2)/1024, needMB/2)
}

// memSwapHeader is the first page of a swap area, the kernel's swap_header
// version 1 (little-endian, as on every TrimUI): no bad pages, uuid, no
// label, "SWAPSPACE2" in the page's last ten bytes. pages is the area's size
// in pages. Pure.
func memSwapHeader(pageSize int, pages uint64, uuid []byte) []byte {
	h := make([]byte, pageSize)
	binary.LittleEndian.PutUint32(h[1024:], 1)              // version
	binary.LittleEndian.PutUint32(h[1028:], uint32(pages-1)) // last_page
	// nr_badpages (1032) stays 0.
	copy(h[1036:1052], uuid)
	copy(h[pageSize-10:], "SWAPSPACE2")
	return h
}

// memSwapHeaderOK reports whether page is a version 1 swap header for an area
// of pages pages. Pure.
func memSwapHeaderOK(page []byte, pages uint64) bool {
	n := len(page)
	return n >= 4096 && pages >= 10 && string(page[n-10:]) == "SWAPSPACE2" &&
		binary.LittleEndian.Uint32(page[1024:]) == 1 &&
		uint64(binary.LittleEndian.Uint32(page[1028:])) == pages-1
}

// memSwapFileSizeMB is the swap file's size for a card with freeMB free: 0
// when the card cannot spare it and memSwapReserveMB beside it. Pure.
func memSwapFileSizeMB(freeMB uint64) uint64 {
	switch {
	case freeMB >= memSwapBigFreeGB<<10:
		return memSwapFileBigMB
	case freeMB >= memSwapFileMB+memSwapReserveMB:
		return memSwapFileMB
	}
	return 0
}

// ── State ────────────────────────────────────────────────────────────────

// memSwapState is memswap.json: a swap file the card refused (the swapon
// error, for which kernel and file system), what JukaHub switched on, and the
// swappiness it replaced.
type memSwapState struct {
	Refused    string `json:"refused,omitempty"`
	Kernel     string `json:"kernel,omitempty"`
	FS         string `json:"fs,omitempty"`
	File       string `json:"file,omitempty"`
	Zram       string `json:"zram,omitempty"`
	Boot       string `json:"boot,omitempty"` // boot_id when Zram was set up
	Swappiness int    `json:"swappinessBefore,omitempty"`
}

// memSwapDir is JukaHub's data folder ("" when unknown).
func memSwapDir() string {
	dir, err := P().DataDir()
	if err != nil {
		return ""
	}
	return dir
}

func memSwapLoadState() memSwapState {
	var st memSwapState
	if dir := memSwapDir(); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, memSwapStateName)); err == nil {
			_ = json.Unmarshal(data, &st)
		}
	}
	return st
}

func memSwapSaveState(st memSwapState) {
	dir := memSwapDir()
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	p := filepath.Join(dir, memSwapStateName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		logWarn("mem", "swap: could not save %s: %v", memSwapStateName, err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		logWarn("mem", "swap: could not save %s: %v", memSwapStateName, err)
	}
}

// memSwapSizeText is a size in kB for the row: "512 MB". Pure.
func memSwapSizeText(kb uint64) string {
	return memShort(kb * 1024)
}

// memSwapOnText is the row's line for active swap areas JukaHub set up
// (ours) or found. Pure.
func memSwapOnText(list []memSwapEntry, ours bool) string {
	var total uint64
	zram := false
	for _, e := range list {
		total += e.sizeKB
		zram = zram || e.zram()
	}
	size := memSwapSizeText(total)
	switch {
	case !ours:
		return fmt.Sprintf("Swap already on: %s", size)
	case zram:
		return fmt.Sprintf("On: %s compressed in RAM (zram)", size)
	}
	return fmt.Sprintf("On: %s swap file on the card", size)
}
