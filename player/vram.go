package main

// vram.go — Settings → Games & emulators → "Video memory (VRAM)": memory
// assigned to a game's graphics.
//
// Juka (2026-11-25): "Can we add ability to add VRAM to JukaHub in settings?
// and assign it there!" A PC whose graphics are built into the processor
// carves its VRAM out of the RAM in the firmware (a ROG Ally assigns it in
// Armoury Crate), and Winlator asks for a "Video Memory Size" for Windows
// games. TrimUI's handhelds have no carve-out to move: the A133P's PowerVR
// GE8300 and the A523's Mali-G57 take their textures and buffers from the
// same 1 GB as everything else, when they need them, so a game's graphics get
// whatever is free at that moment. What a size assigned here does is:
//
//   - room. When a game, emulator, port or app takes the screen with less
//     than the size free (memHeadroomMB: MemAvailable plus a cautious share
//     of free swap, as the AI's checks count it), JukaHub gives back what only
//     it can: its own textures — held in that same memory — and byte caches
//     at once, on the main thread at the top of the frame where the child took
//     the screen (main.go, beside the animated wallpaper's pause), then on a
//     goroutine the Go heap, its idle helpers (the speech recognizer unless
//     "Hey Juka" keeps it, the offline AI's server, a Piper voice, the video
//     link prefetch, the media library's scan) and the kernel's page cache
//     (memFreeUp, purpose memFreeGame). It never closes an app. The row's
//     second line says how much the last game started with.
//   - what Windows programs see. Under Wine, Direct3D and DirectDraw report
//     the size as the graphics card's memory (WINE_D3D_CONFIG's
//     VideoMemorySize, set in planWineRuntime): a game that will not start
//     below some amount of VRAM, or picks its textures by it, sees that. The
//     variable wins over the prefix's registry and leaves the prefix alone; a
//     WINE_D3D_CONFIG a Wine profile sets keeps its own VideoMemorySize.
//
// Auto (the default) assigns nothing: games take what is free, as before
// (Heavy-game boost still empties JukaHub's caches for the heaviest), and
// Wine guesses the card's memory from the GPU it finds.
//
// The stops run from 64 MB to half the RAM (512 MB on the 1 GB handhelds). A
// bigger number set by hand still reaches Wine; the room kept is capped at
// half the RAM. The row shows on TrimUI handhelds only: a PC's VRAM is its
// firmware's business, and Windows runs .exe files without Wine.
//
// The size is read on the main thread (the config) and published in
// vramPublished for the goroutine that plans a Wine launch: after the first
// frame, on every change, at each hand-over and whenever Windows Apps takes
// its settings copy (wineLaunchSettingsTake).

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// vramSetting is the Settings key: "auto", or the size in MB.
	vramSetting = "videoMemory"
	// vramMaxMB caps a size set by hand.
	vramMaxMB = 16384
	// vramRoomWait bounds the free-up a hand-over starts.
	vramRoomWait = 30 * time.Second
)

// vramStopsMB are the sizes the row offers, up to half the RAM.
var vramStopsMB = []int{64, 128, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096}

// vramPublished is the assigned size in MB (0 = Auto), published by the main
// thread for goroutines.
var vramPublished atomic.Int32

// vramState is the row's second line after a hand-over. Under mu.
var vramState struct {
	mu     sync.Mutex
	status string
}

// vramRAM caches MemTotal (kB) for the row, which is drawn every frame.
var vramRAM struct {
	once sync.Once
	kb   uint64
}

func init() {
	// A Windows program started before any game or change gets the size too.
	deferStartupTask("video-memory", func() { vramPublish(vramAssignedMB(appConfig)) })
}

// vramSupported: TrimUI handhelds, where JukaHub owns the memory and Wine is
// what runs Windows programs.
func vramSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// vramTotalKB is the RAM (MemTotal, kB), read once; 0 when unknown.
func vramTotalKB() uint64 {
	vramRAM.once.Do(func() { vramRAM.kb = readProcMeminfo().MemTotal })
	return vramRAM.kb
}

// vramHalfRAMMB is half the RAM as sold: MemTotal rounded up to whole 256 MB
// (whole GB above 1 GB) — the kernel keeps some for itself, so 1 GB reads as
// a little less — and halved; 0 when unknown. Pure.
func vramHalfRAMMB(totalKB uint64) int {
	if totalKB == 0 {
		return 0
	}
	mb := (totalKB + 1023) / 1024
	step := uint64(256)
	if mb > 1024 {
		step = 1024
	}
	return int((mb + step - 1) / step * step / 2)
}

// vramSizesFor are the stops for totalKB of RAM: vramStopsMB up to half of
// it, or up to 512 MB when the RAM is unknown. Pure.
func vramSizesFor(totalKB uint64) []int {
	half := vramHalfRAMMB(totalKB)
	if half == 0 {
		half = 512
	}
	out := make([]int, 0, len(vramStopsMB))
	for _, mb := range vramStopsMB {
		if mb <= half {
			out = append(out, mb)
		}
	}
	return out
}

// vramParseMB reads the setting: a size in MB ("256", "256 MB"), capped at
// vramMaxMB; anything else ("auto", "", "off") is 0, Auto. Pure.
func vramParseMB(raw string) int {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimSpace(strings.TrimSuffix(s, "mb"))
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, vramMaxMB)
}

// vramAssignedMB is the assigned size in MB, 0 for Auto. Main thread (the
// config).
func vramAssignedMB(c *Config) int {
	if c == nil {
		return 0
	}
	raw, _ := customString(c.Variables.Custom, vramSetting)
	return vramParseMB(raw)
}

// vramRoomMB is the room a hand-over keeps free for a size of mb with totalKB
// of RAM: mb, at most half the RAM. Pure.
func vramRoomMB(mb int, totalKB uint64) uint64 {
	if mb <= 0 {
		return 0
	}
	if half := vramHalfRAMMB(totalKB); half > 0 && mb > half {
		mb = half
	}
	return uint64(mb)
}

// vramValue is the stored form of mb: "auto" or the number. Pure.
func vramValue(mb int) string {
	if mb <= 0 {
		return "auto"
	}
	return strconv.Itoa(mb)
}

// vramSizeLabel is a size for people: "512 MB", "1 GB", "1.5 GB". Pure.
func vramSizeLabel(mb int) string {
	switch {
	case mb >= 1024 && mb%1024 == 0:
		return strconv.Itoa(mb/1024) + " GB"
	case mb >= 1024:
		return strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64) + " GB"
	}
	return strconv.Itoa(mb) + " MB"
}

// vramLabel is the row's value: "Auto" or the size. Pure.
func vramLabel(mb int) string {
	if mb <= 0 {
		return "Auto"
	}
	return vramSizeLabel(mb)
}

// vramPublish hands the assigned size to the goroutines. Any goroutine.
func vramPublish(mb int) {
	vramPublished.Store(int32(max(0, min(mb, vramMaxMB))))
}

// vramSetStatus sets the row's second line ("" for the default). Any
// goroutine.
func vramSetStatus(s string) {
	vramState.mu.Lock()
	vramState.status = s
	vramState.mu.Unlock()
}

// vramStatus is the last hand-over's line.
func vramStatus() string {
	vramState.mu.Lock()
	defer vramState.mu.Unlock()
	return vramState.status
}

// ── The row ────────────────────────────────────────────────────────────────

// vramSettingsRow is Settings → Games & emulators → "Video memory (VRAM)",
// above Extra memory (swap). A opens the list of sizes; Left and Right step.
func vramSettingsRow() setRow {
	return setRow{
		ID: vramSetting, Label: "Video memory (VRAM)", Kind: setKindChoice, Clamp: true,
		Visible:    func(*Config) bool { return vramSupported() },
		Detail:     vramSettingDetail,
		Options:    vramOptions,
		Current:    func(c *Config) int { return ledOptionIndex(vramOptions(c), vramValue(vramAssignedMB(c))) },
		Sheet:      vramOptions,
		SheetValue: func(c *Config) string { return vramValue(vramAssignedMB(c)) },
		SheetTitle: "Video memory (VRAM)",
		Value:      func(c *Config) string { return vramLabel(vramAssignedMB(c)) },
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			mb := vramParseMB(value)
			setStoreString(c, vramSetting, vramValue(mb))
			vramPublish(mb)
			vramSetStatus("")
			setToast("Video memory (VRAM): " + vramLabel(mb))
		},
	}
}

// vramOptions are Auto and the sizes up to half the RAM; the list's notes
// say what Auto and the largest stop mean.
func vramOptions(*Config) []setOption {
	sizes := vramSizesFor(vramTotalKB())
	out := make([]setOption, 0, len(sizes)+1)
	out = append(out, setOption{Value: "auto", Label: "Auto", Note: "Games take what is free; Wine guesses from the GPU"})
	for i, mb := range sizes {
		o := setOption{Value: strconv.Itoa(mb), Label: vramSizeLabel(mb)}
		if i == len(sizes)-1 {
			o.Note = "Half the RAM: the most JukaHub can make room for"
		}
		out = append(out, o)
	}
	return out
}

// vramSettingDetail is the row's second line: with a size, the last
// hand-over's outcome, else what the size does; with Auto, what the GPU has.
// Cheap: drawn every frame.
func vramSettingDetail(c *Config) string {
	if vramAssignedMB(c) <= 0 {
		if half := vramHalfRAMMB(vramTotalKB()); half > 0 {
			return "Auto: the GPU shares the " + vramSizeLabel(half*2) + " of RAM and takes what is free"
		}
		return "Auto: the GPU shares the RAM and takes what is free"
	}
	if s := vramStatus(); s != "" {
		return s
	}
	return "Kept free when a game starts; Windows games see it as their VRAM"
}

// ── The hand-over ──────────────────────────────────────────────────────────

// vramGameTookScreen is the main loop's call when a game, emulator, port or
// app has just taken the screen (main.go, once per hand-over, at the top of
// the frame: nothing holds a texture). With a size assigned and less than
// that free, JukaHub's textures and byte caches go at once and the rest of
// what it can give back follows on a goroutine. Main thread.
func vramGameTookScreen(c *Config) {
	mb := vramAssignedMB(c)
	vramPublish(mb)
	if mb <= 0 || !vramSupported() {
		return
	}
	room := vramRoomMB(mb, vramTotalKB())
	have := memHeadroomMB(room)
	if have == ^uint64(0) {
		return // no /proc/meminfo: nothing to measure
	}
	if have >= room {
		vramSetStatus(fmt.Sprintf("The last game started with %s free", memShort(have<<20)))
		logDebug("mem", "video memory: %d MB free for the game, %d MB assigned", have, room)
		return
	}
	// The animated wallpaper is already down to one frame for the game
	// (gifWallPauseForGame, just before): the eviction leaves it animating
	// once JukaHub is back.
	evictAllCachesForPressure()
	logInfo("mem", "video memory: %d MB assigned, %d MB free as a game took the screen: JukaHub's textures and caches emptied", room, have)
	go vramMakeRoom(room)
}

// vramMakeRoom gives back the rest of what JukaHub can for a game that took
// the screen with less than room MB free, and leaves the row's line. Its own
// goroutine.
func vramMakeRoom(room uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("mem", "making room for video memory stopped: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), vramRoomWait)
	defer cancel()
	have := memFreeUp(ctx, room, memFreeGame, "video memory")
	switch {
	case have == ^uint64(0):
		// No /proc/meminfo: nothing to report.
	case have >= room:
		vramSetStatus(fmt.Sprintf("Made room for the last game: %s free", memShort(have<<20)))
	default:
		vramSetStatus(fmt.Sprintf("Made room for the last game: %s of %s free", memShort(have<<20), memShort(room<<20)))
	}
}

// ── Wine ───────────────────────────────────────────────────────────────────

// vramWineEnv gives a Wine launch's environment (planWineRuntime) the
// assigned size as the graphics card's memory: WINE_D3D_CONFIG's
// VideoMemorySize, which wined3d reads before the prefix's registry. Auto
// adds nothing. Any goroutine: it reads the published size, not the config.
func vramWineEnv(env map[string]string) {
	mb := int(vramPublished.Load())
	if mb <= 0 || env == nil {
		return
	}
	cur := wineEnvValue(env, "WINE_D3D_CONFIG")
	if next := wineD3DConfigWithVRAM(cur, mb); next != cur {
		wineSetEnv(env, "WINE_D3D_CONFIG", next)
		logInfo("windowsapps", "Windows programs see %d MB of video memory (Settings → Video memory)", mb)
	}
}

// wineD3DConfigWithVRAM adds VideoMemorySize=mb to a WINE_D3D_CONFIG value —
// key=value pairs split by commas or semicolons — unless one is already
// there (wined3d's names are case-sensitive, so only that spelling counts).
// Pure.
func wineD3DConfigWithVRAM(cfg string, mb int) string {
	if mb <= 0 {
		return cfg
	}
	for _, part := range strings.FieldsFunc(cfg, func(r rune) bool { return r == ',' || r == ';' }) {
		if name, _, _ := strings.Cut(part, "="); strings.TrimSpace(name) == "VideoMemorySize" {
			return cfg
		}
	}
	item := "VideoMemorySize=" + strconv.Itoa(mb)
	rest := strings.TrimRight(strings.TrimSpace(cfg), ",;")
	if rest == "" {
		return item
	}
	return rest + "," + item
}
