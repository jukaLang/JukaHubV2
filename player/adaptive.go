package main

import (
	"bufio"
	"log"
	"os"
	"runtime"
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

// FrameRateController dynamically adjusts the render delay based on user
// activity. On TSP (ARM64, battery-powered), going from 60→20 fps when idle
// halves GPU/CPU utilisation and noticeably extends battery life.
type FrameRateController struct {
	mu              sync.Mutex
	lastInputTime   uint64
	idleThreshold   uint64 // ms before switching to Idle
	deepThreshold   uint64 // ms before switching to DeepIdle
	currentMode     FrameRateMode
	animationActive bool // transitions, hover anims, etc. keep us at 60
}

// NewFrameRateController creates a controller tuned for the given platform.
func NewFrameRateController() *FrameRateController {
	fc := &FrameRateController{
		lastInputTime: sdl.GetTicks64(),
		currentMode:   FrameRateActive,
	}
	if IsTSP() {
		fc.idleThreshold = 3000  // 3s → 30fps
		fc.deepThreshold = 10000 // 10s → 20fps
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
		log.Printf("[framerate] → Active (60 fps)")
	}
	fc.mu.Unlock()
}

// SetAnimationActive forces 60fps while transitions/animations are running.
func (fc *FrameRateController) SetAnimationActive(active bool) {
	fc.mu.Lock()
	fc.animationActive = active
	fc.mu.Unlock()
}

// Delay returns the recommended ms to sleep this frame and updates the
// internal mode based on elapsed idle time.
func (fc *FrameRateController) Delay() uint32 {
	fc.mu.Lock()
	now := sdl.GetTicks64()
	elapsed := now - fc.lastInputTime
	animation := fc.animationActive
	fc.mu.Unlock()

	var mode FrameRateMode
	switch {
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
		fps := map[FrameRateMode]int{FrameRateActive: 60, FrameRateIdle: 30, FrameRateDeepIdle: 20}
		log.Printf("[framerate] → %v (%d fps, idle %dms)", mode, fps[mode], elapsed)
	}
	fc.mu.Unlock()

	switch mode {
	case FrameRateActive:
		return 16 // ~60 fps
	case FrameRateIdle:
		return 33 // ~30 fps
	default:
		return 50 // ~20 fps
	}
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

// EvictCachesForPressure clears render caches proportional to the current
// memory pressure level. Returns true if any cache was evicted.
func EvictCachesForPressure(mm *MemoryMonitor) bool {
	if mm == nil {
		return false
	}
	level := mm.Level()
	switch level {
	case MemCritical:
		log.Printf("[mem] Critical: clearing all text caches")
		ClearTextCache()
		clearThumbnailCache()
		runtime.GC()
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
	thumbnailCacheMutex.Lock()
	defer thumbnailCacheMutex.Unlock()
	for k, tex := range textureCache {
		if tex != nil {
			tex.Destroy()
		}
		delete(textureCache, k)
	}
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

	if IsTSP() {
		log.Printf("[device] TSP mode: text cache 512 entries, adaptive FPS enabled")
	} else {
		log.Printf("[device] Desktop mode: text cache 2048 entries, standard FPS")
	}
}
