package main

import (
	"bufio"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Adaptive frame rate controller
// ──────────────────────────────────────────────────────────────────────

// FrameRateMode represents the current rendering cadence.
type FrameRateMode int

const (
	FrameRateActive   FrameRateMode = iota // 60 fps – user is interacting
	FrameRateIdle                          // 30 fps – no input for a while
	FrameRateDeepIdle                      // 20 fps – no input for a long time
)

// frameProfile holds the render delay (ms) for each cadence mode.
type frameProfile struct {
	Active uint32
	Idle   uint32
	Deep   uint32
}

// frameProfileFor returns the cadence for the target platform. The handheld
// caps at 30fps: the TSP cannot sustain 60fps while drawing menus, the
// on-screen keyboard or decoded video, so the extra frames only cost battery
// and heat and make input feel later than it is.
func frameProfileFor(handheld bool) frameProfile {
	if handheld {
		return frameProfile{Active: 33, Idle: 42, Deep: 66} // ~30 / 24 / 15 fps
	}
	return frameProfile{Active: 16, Idle: 33, Deep: 50} // ~60 / 30 / 20 fps
}

// delayFor returns the sleep for a mode. LowPower caps the cadence at the idle
// profile instead of the fastest one; it must never be passed to
// SetAnimationActive, which would turn the battery setting into 60fps.
func (p frameProfile) delayFor(mode FrameRateMode, lowPower bool) uint32 {
	if lowPower {
		return p.Idle
	}
	switch mode {
	case FrameRateActive:
		return p.Active
	case FrameRateIdle:
		return p.Idle
	default:
		return p.Deep
	}
}

// fps reports the approximate frames per second of a mode (for diagnostics).
func (p frameProfile) fps(mode FrameRateMode) int {
	d := p.delayFor(mode, false)
	if d == 0 {
		return 0
	}
	return int(1000 / d)
}

// FrameRateController dynamically adjusts the render delay based on user
// activity. On TSP (ARM64, battery-powered), slowing down when idle halves
// GPU/CPU utilisation and noticeably extends battery life.
type FrameRateController struct {
	mu              sync.Mutex
	lastInputTime   uint64
	idleThreshold   uint64 // ms before switching to Idle
	deepThreshold   uint64 // ms before switching to DeepIdle
	currentMode     FrameRateMode
	animationActive bool // transitions, hover anims, etc. keep the fastest mode
	lowPower        bool // user setting: always use the slowest cadence
	profile         frameProfile
}

// NewFrameRateController creates a controller tuned for the given platform.
func NewFrameRateController() *FrameRateController {
	handheld := IsTrimuiDevice()
	fc := &FrameRateController{
		lastInputTime: sdl.GetTicks64(),
		currentMode:   FrameRateActive,
		profile:       frameProfileFor(handheld),
	}
	if handheld {
		fc.idleThreshold = 3000  // 3s → 24fps
		fc.deepThreshold = 10000 // 10s → 15fps
	} else {
		fc.idleThreshold = 5000
		fc.deepThreshold = 20000
	}
	return fc
}

// NotifyInput records that user input occurred, resetting the idle timer.
func (fc *FrameRateController) NotifyInput() {
	fc.mu.Lock()
	fc.lastInputTime = sdl.GetTicks64()
	if fc.currentMode != FrameRateActive {
		fc.currentMode = FrameRateActive
		log.Printf("[framerate] → Active (%d fps)", fc.profile.fps(FrameRateActive))
	}
	fc.mu.Unlock()
}

// SetAnimationActive selects the fastest cadence while transitions or
// animations are running.
func (fc *FrameRateController) SetAnimationActive(active bool) {
	fc.mu.Lock()
	fc.animationActive = active
	fc.mu.Unlock()
}

// SetLowPower caps the cadence at the idle profile regardless of activity.
// This is what the LowPower setting means; it is not an animation trigger.
func (fc *FrameRateController) SetLowPower(low bool) {
	fc.mu.Lock()
	fc.lowPower = low
	fc.mu.Unlock()
}

// Delay returns the recommended ms to sleep this frame and updates the
// internal mode based on elapsed idle time.
func (fc *FrameRateController) Delay() uint32 {
	fc.mu.Lock()
	now := sdl.GetTicks64()
	elapsed := now - fc.lastInputTime
	animation := fc.animationActive
	lowPower := fc.lowPower
	fc.mu.Unlock()

	var mode FrameRateMode
	switch {
	case lowPower && elapsed < fc.deepThreshold:
		mode = FrameRateIdle
	case animation || elapsed < fc.idleThreshold:
		mode = FrameRateActive
	case elapsed < fc.deepThreshold:
		mode = FrameRateIdle
	default:
		mode = FrameRateDeepIdle
	}

	fc.mu.Lock()
	if mode != fc.currentMode {
		fc.currentMode = mode
		log.Printf("[framerate] → %v (%d fps, idle %dms)", mode, fc.profile.fps(mode), elapsed)
	}
	fc.mu.Unlock()

	return fc.profile.delayFor(mode, lowPower)
}

// CurrentMode returns the current frame rate mode (for diagnostics).
func (fc *FrameRateController) CurrentMode() FrameRateMode {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.currentMode
}

// ──────────────────────────────────────────────────────────────────────
// Memory pressure monitor (Linux /proc/meminfo)
// ──────────────────────────────────────────────────────────────────────

// MemoryPressureLevel indicates how much free RAM is left.
type MemoryPressureLevel int

const (
	MemNormal   MemoryPressureLevel = iota // >150 MB free
	MemWarning                             // 80–150 MB free
	MemCritical                            // <80 MB free
)

// memInfo caches parsed /proc/meminfo values.
type memInfo struct {
	MemTotal  uint64 // kB
	MemFree   uint64 // kB
	MemAvail  uint64 // kB
	Buffers   uint64 // kB
	Cached    uint64 // kB
	Timestamp time.Time
}

// MemoryMonitor periodically reads /proc/meminfo on Linux and exposes the
// current pressure level. On Windows (no /proc), it falls back to Go's
// runtime heap stats.
type MemoryMonitor struct {
	mu     sync.Mutex
	info   memInfo
	level  MemoryPressureLevel
	stopCh chan struct{}
}

// NewMemoryMonitor starts a background goroutine that polls memory every 5s.
func NewMemoryMonitor() *MemoryMonitor {
	mm := &MemoryMonitor{
		level:  MemNormal,
		stopCh: make(chan struct{}),
	}
	go mm.poll()
	return mm
}

func (mm *MemoryMonitor) poll() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-mm.stopCh:
			return
		case <-ticker.C:
			mm.refresh()
		}
	}
}

func (mm *MemoryMonitor) refresh() {
	var info memInfo
	if runtime.GOOS == "linux" {
		info = readProcMeminfo()
	} else {
		// Fallback: use Go runtime stats as a rough proxy.
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		info.MemTotal = 1 << 30 // assume ~1 GB
		info.MemAvail = info.MemTotal - m.Sys/1024
		info.MemFree = info.MemAvail
	}
	info.Timestamp = time.Now()

	var level MemoryPressureLevel
	availMB := info.MemAvail / 1024
	switch {
	case availMB < 80:
		level = MemCritical
	case availMB < 150:
		level = MemWarning
	default:
		level = MemNormal
	}

	mm.mu.Lock()
	mm.info = info
	previousLevel := mm.level
	mm.level = level
	mm.mu.Unlock()

	if level != previousLevel {
		names := map[MemoryPressureLevel]string{MemNormal: "Normal", MemWarning: "Warning", MemCritical: "Critical"}
		log.Printf("[mem] Pressure: %s (avail %d MB)", names[level], availMB)
	}
}

// Level returns the current memory pressure level.
func (mm *MemoryMonitor) Level() MemoryPressureLevel {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.level
}

// AvailableMB returns the estimated available memory in MB.
func (mm *MemoryMonitor) AvailableMB() uint64 {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.info.MemAvail / 1024
}

// Stop terminates the background polling goroutine.
func (mm *MemoryMonitor) Stop() {
	close(mm.stopCh)
}

// readProcMeminfo parses /proc/meminfo for key values.
func readProcMeminfo() memInfo {
	var info memInfo
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return info
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, _ := strconv.ParseUint(parts[1], 10, 64)
		switch parts[0] {
		case "MemTotal:":
			info.MemTotal = val
		case "MemFree:":
			info.MemFree = val
		case "MemAvailable:":
			info.MemAvail = val
		case "Buffers:":
			info.Buffers = val
		case "Cached:":
			info.Cached = val
		}
	}
	return info
}

// ──────────────────────────────────────────────────────────────────────
// Adaptive eviction — clear caches under memory pressure
// ──────────────────────────────────────────────────────────────────────

// cacheEvictor rate-limits pressure cleanup. The main loop polls every ten
// frames, so without this a device sitting under MemCritical re-cleared every
// texture cache and ran a full GC several times a second: that was the hitch
// users saw when a transition happened while RAM was tight.
var cacheEvictor = &cachePressureEvictor{}

// EvictCachesForPressure clears render caches proportional to the current
// memory pressure level. Returns true if any cache was evicted.
func EvictCachesForPressure(mm *MemoryMonitor) bool {
	if mm == nil {
		return false
	}
	level := mm.Level()
	if !cacheEvictor.shouldAct(level, sdl.GetTicks64()) {
		return false
	}
	switch level {
	case MemCritical:
		logTextureMemoryStats("critical-evict")
		log.Printf("[mem] Critical: clearing text and thumbnail texture caches")
		ClearTextCache()
		clearHomeTextCache()
		clearThumbnailCache()
		// Reclaim the freed heap off the render thread. runtime.GC() here used
		// to block the frame that was drawing a transition.
		go debug.FreeOSMemory()
		return true
	case MemWarning:
		log.Printf("[mem] Warning: trimming text cache (50%%)")
		TrimTextCache(512 / 2) // keep only half
		return true
	}
	return false
}

// clearThumbnailCache destroys all cached thumbnail textures.
func clearThumbnailCache() {
	getThumbnailTextures().Clear()
}

// TrimTextCache reduces the text cache to at most keepEntries entries,
// evicting the oldest LRU entries first.
func TrimTextCache(keepEntries int) {
	if globalTextCache != nil {
		globalTextCache.TrimTo(keepEntries)
	}
}

// ──────────────────────────────────────────────────────────────────────
// Startup diagnostics — log device capabilities once
// ──────────────────────────────────────────────────────────────────────

// LogDeviceDiagnostics prints a one-time summary of the device's capabilities
// so TSP-specific tuning decisions are visible in logs.
func LogDeviceDiagnostics() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	log.Printf("[device] Platform: %s | Arch: %s | CPUs: %d",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	log.Printf("[device] Heap: %d MB | Sys: %d MB | NumGC: %d",
		m.HeapAlloc/1024/1024, m.Sys/1024/1024, m.NumGC)

	if runtime.GOOS == "linux" {
		info := readProcMeminfo()
		log.Printf("[device] RAM: %d MB total, %d MB available",
			info.MemTotal/1024, info.MemAvail/1024)
	}

	if IsTrimuiDevice() {
		log.Printf("[device] TrimUI mode: text cache 512 entries, adaptive FPS enabled")
	} else {
		log.Printf("[device] Non-TrimUI mode: text cache 2048 entries, standard FPS")
	}

	// Texture accounting starts near zero and grows as the user browses; the
	// baseline still proves the budgets are what this build expects.
	s := collectTextureMemoryStats()
	log.Printf("[tex] startup: total=%d MB (text=%d, labels=%d, thumbs=%d, video-overlay=%d, frame=%d, ui-layer=%d)",
		s.Total()>>20, s.TextCache>>20, s.HomeText>>20, s.Thumbnails>>20, s.VideoOverlay>>20, s.VideoFrame>>20, s.UILayer>>20)
	log.Printf("[tex] thumbnail texture budget: %d MB", thumbnailTextureBudgetBytes()>>20)
}
