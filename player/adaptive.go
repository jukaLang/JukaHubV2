package main

import (
	"bufio"
	"fmt"
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
	// performancePinned is the Performance power mode: never idle-throttle,
	// even when nothing has been touched for a while. It is a user choice
	// from quicksettings, not an animation trigger like animationActive.
	performancePinned bool
	profile           frameProfile
	// videoActive is a video on screen that is playing: it gets the active
	// cadence whatever the power setting, or a 30 fps clip judders at 24.
	videoActive bool
	// backdropMS is what the moving pictures drawn in the last frame asked
	// for (requestFrameEvery): a new frame at least every backdropMS ms, 0
	// when nothing on screen moves by itself.
	backdropMS uint32
	// backdropPastLowPower is a request the Low Power setting does not cap
	// at its idle cadence (requestFrameEveryPastLowPower): the liquid
	// background's moving water.
	backdropPastLowPower bool
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
		logDebug("framerate", "→ Active (%d fps)", fc.profile.fps(FrameRateActive))
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
} // SetPerformance pins the cadence at the active profile: the Performance
// power mode keeps the full frame rate even when the app sits idle.
func (fc *FrameRateController) SetPerformance(on bool) {
	fc.mu.Lock()
	fc.performancePinned = on
	fc.mu.Unlock()
}

// SetVideoActive reports whether a playing video is on screen. The decoder
// hands the loop its newest frame, so the loop's cadence is the frame rate the
// viewer sees: idling down to 15 fps ten seconds into a film made it stutter.
func (fc *FrameRateController) SetVideoActive(on bool) {
	fc.mu.Lock()
	fc.videoActive = on
	fc.mu.Unlock()
}

// SetBackdropFrame reports what the moving pictures drawn in the frame just
// finished need: a new frame at least every ms milliseconds, or 0 when
// nothing moves by itself (adaptiveTick feeds it from requestFrameEvery). A
// moving backdrop is not someone using the screen, so it never shortens the
// cadence below the active one, and under Low Power it keeps the idle cadence
// — the cap that setting promises — but gets it whole (Pace): the liquid
// background's water stepped at 30 Hz but drawn at 15–24 fps moved in jerks,
// and an animated wallpaper of 25 frames a second played at 15. pastLowPower
// lifts that cap for the frame (requestFrameEveryPastLowPower): the moving
// water's request, which Low Power — the handheld's default — does not cap.
func (fc *FrameRateController) SetBackdropFrame(ms uint32, pastLowPower bool) {
	fc.mu.Lock()
	fc.backdropMS = ms
	fc.backdropPastLowPower = pastLowPower
	fc.mu.Unlock()
}

// backdropDelay folds a moving backdrop's request into the delay a mode gave:
// never faster than the active cadence, never past Low Power's idle cap (the
// caller passes lowPower false for a request that may pass it), and never
// slower than the mode already was.
func (p frameProfile) backdropDelay(delay, want uint32, lowPower bool) uint32 {
	if want == 0 {
		return delay
	}
	want = max(want, p.Active)
	if lowPower {
		want = max(want, p.Idle)
	}
	return min(delay, want)
}

// Delay returns the recommended ms to sleep this frame and updates the
// internal mode based on elapsed idle time.
func (fc *FrameRateController) Delay() uint32 {
	fc.mu.Lock()
	now := sdl.GetTicks64()
	elapsed := now - fc.lastInputTime
	animation := fc.animationActive
	lowPower := fc.lowPower
	pinned := fc.performancePinned
	video := fc.videoActive
	backdrop := fc.backdropMS
	backdropPast := fc.backdropPastLowPower

	fc.mu.Unlock()

	var mode FrameRateMode
	switch {
	case pinned || video:
		// Performance, or a playing video: answer an idle query with the
		// fastest cadence.
		mode = FrameRateActive
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
		logDebug("framerate", "→ %v (%d fps, idle %dms)", mode, fc.profile.fps(mode), elapsed)
	}
	fc.mu.Unlock()

	if video {
		// Low Power's slower cadence is for menus; a clip shown at 24 of its
		// 30 frames a second judders, and the decoder is the real cost anyway.
		return fc.profile.Active
	}
	return fc.profile.backdropDelay(fc.profile.delayFor(mode, lowPower), backdrop, lowPower && !backdropPast)
}

// CurrentMode returns the current frame rate mode (for diagnostics).
func (fc *FrameRateController) CurrentMode() FrameRateMode {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.currentMode
}

// Pace returns how long the loop should sleep after a frame that took spent.
//
// While the user is pressing buttons, something animates, a video plays or
// the backdrop moves, the cadence is a frame budget: the frame's own cost
// comes out of it, so the handheld's 33 ms profile really is 30 fps. A plain
// sleep after the frame made it 33 ms plus the 10–20 ms the A53 spends
// drawing — about 20 fps while navigating. Once nothing is touched and
// nothing moves the sleep stays whole, which is the cheaper cadence idle
// screens are meant to have.
func (fc *FrameRateController) Pace(spent time.Duration) uint32 {
	delay := fc.Delay()
	fc.mu.Lock()
	busy := fc.animationActive || fc.performancePinned || fc.videoActive || fc.backdropMS > 0 ||
		sdl.GetTicks64()-fc.lastInputTime < fc.idleThreshold
	fc.mu.Unlock()
	if !busy || spent <= 0 {
		return delay
	}
	ms := uint32(spent / time.Millisecond)
	if ms >= delay {
		// The frame used the whole budget: yield for a moment so input and
		// the decoder goroutines still get the CPU.
		return 1
	}
	return delay - ms
}

// ── Frame requests from moving pictures ─────────────────────────────────

// frameWant is the shortest frame interval anything drawn in the current
// frame asked for, and the frame (frameCount) it was asked in.
var frameWant struct {
	ms    uint32
	frame uint64
	// pastLowPower: a request of this frame may pass Low Power's cap
	// (requestFrameEveryPastLowPower).
	pastLowPower bool
}

// requestFrameEvery is how a picture that moves by itself — the liquid
// background's water, an animated wallpaper, the living background — asks
// the frame-rate governor for a new frame at least every ms milliseconds. It
// is asked from the draw, every frame the picture is drawn moving, so it only
// counts while the picture is on screen: a screen that stops drawing the
// backdrop (a video, a game, a full-screen page) stops asking, and so does a
// picture that has come to rest. 1 means as fast as the active cadence goes.
// The shortest request of the frame wins. Main thread only.
func requestFrameEvery(ms uint32) {
	if ms == 0 {
		ms = 1
	}
	if frameWant.frame != frameCount {
		// The frame's first request: what an earlier frame asked is over.
		frameWant.ms, frameWant.frame, frameWant.pastLowPower = ms, frameCount, false
	} else if frameWant.ms == 0 || ms < frameWant.ms {
		frameWant.ms = ms
	}
}

// requestFrameEveryPastLowPower is requestFrameEvery for the one picture the
// Low Power setting does not cap at its idle cadence: the liquid background's
// moving water (liquidbg.go). Low Power is the handheld's default, and the
// water at its 24 fps was the "slow framerate" a tester reported. The active
// cadence still bounds it, the idle screens' own budget (saver_power.go)
// still stretches the sleep, and under Battery Saver the water asks with
// requestFrameEvery and keeps the cap.
func requestFrameEveryPastLowPower(ms uint32) {
	requestFrameEvery(ms)
	frameWant.pastLowPower = true
}

// adaptiveTick is this file's per-frame work on the main loop, once the
// frame is drawn and before its sleep is worked out: it hands the governor
// what the frame's moving pictures asked for, and now and then logs how much
// memory JukaHub uses. Modal loops (the on-screen keyboard, the shutdown
// card) present without coming here; what they ask for lands in the frame
// they return to.
func adaptiveTick() {
	want, past := uint32(0), false
	if frameWant.frame == frameCount {
		want, past = frameWant.ms, frameWant.pastLowPower
	}
	frameWant.ms, frameWant.pastLowPower = 0, false
	if frameRateCtrl != nil {
		frameRateCtrl.SetBackdropFrame(want, past)
	}
	memoryLogTick(time.Now())
}

// The memory line (memstat.go) is logged a minute in, once startup has
// settled, and every five minutes after: often enough that a bug report's
// log shows what the app held, rare enough to cost nothing.
const (
	memLogFirst = time.Minute
	memLogEvery = 5 * time.Minute
)

var memLogNext time.Time

// memoryLogTick logs the memory line when it is due. Main thread: the
// texture caches it adds up are the render thread's.
func memoryLogTick(now time.Time) {
	if memLogNext.IsZero() {
		memLogNext = now.Add(memLogFirst)
		return
	}
	if now.Before(memLogNext) {
		return
	}
	memLogNext = now.Add(memLogEvery)
	logInfo("mem", "%s", memoryReportLine())
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
		logInfo("mem", "Pressure: %s (avail %d MB)", names[level], availMB)
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
		logInfo("mem", "Critical: clearing every texture and byte cache")
		ClearTextCache()
		// Shape textures hold the palette in their pixels just like text does.
		ClearShapeCache()
		ClearIconAtlas()
		// The gradient textures are the largest cached surfaces — free first.
		ClearBgGradientCache()
		// An animated wallpaper keeps only its first frame: its frames are up
		// to 40 MB of textures (gifwall.go).
		gifWallShrinkForPressure()
		clearHomeTextCache()
		clearThumbnailCache()
		// Launcher icons, chat images and avatars are textures too. Each was
		// bounded by a count alone until the cache-budget pass, so a launcher of
		// large icons or a chat of photos could hold megabytes under a cap that
		// only counted entries.
		clearAppIconCache()
		clearChatImageCache()
		clearChatAvatarCache()
		// The bytes side: downloaded thumbnail payloads waiting for the renderer,
		// and the validated feed bodies kept for conditional requests. Both are
		// refetchable, so dropping them costs a round trip, not state.
		clearThumbnailDataCache()
		conditionalCache.clear()
		// The page cache holds whole articles and their images. Dropping it
		// costs the user one refetch on Back, which beats being killed for RAM.
		browserClearPageCache()
		// Reclaim the freed heap off the render thread. runtime.GC() here used
		// to block the frame that was drawing a transition.
		go debug.FreeOSMemory()
		return true
	case MemWarning:
		logInfo("mem", "Warning: trimming text cache (50%%)")
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

	logInfo("device", "Platform: %s | Arch: %s | CPUs: %d",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	logInfo("device", "Heap: %d MB | Sys: %d MB | NumGC: %d",
		m.HeapAlloc/1024/1024, m.Sys/1024/1024, m.NumGC)

	if runtime.GOOS == "linux" {
		info := readProcMeminfo()
		logInfo("device", "RAM: %d MB total, %d MB available",
			info.MemTotal/1024, info.MemAvail/1024)
	}

	if IsTrimuiDevice() {
		logInfo("device", "TrimUI mode: text cache 512 entries, adaptive FPS enabled")
	} else {
		logInfo("device", "Non-TrimUI mode: text cache 2048 entries, standard FPS")
	}

	// Texture accounting starts near zero and grows as the user browses; the
	// baseline still proves the budgets are what this build expects.
	s := collectTextureMemoryStats()
	logInfo("tex", "startup: total=%d MB (text=%d, labels=%d, thumbs=%d, video-overlay=%d, frame=%d, ui-layer=%d)",
		s.Total()>>20, s.TextCache>>20, s.HomeText>>20, s.Thumbnails>>20, s.VideoOverlay>>20, s.VideoFrame>>20, s.UILayer>>20)
	logInfo("tex", "thumbnail texture budget: %d MB", thumbnailTextureBudgetBytes()>>20)
}

// renderPipeline is the one startup snapshot of what SDL actually handed us:
// the video backend in use, the 2D renderer and where its cost lands, and the
// largest texture the device accepts. On the A133P these four fields are the
// whole story of whether the shell runs on the GE8300 or silently fell back
// to software, which is why they are captured in one place and logged verbatim
// rather than inferred elsewhere.
type renderPipeline struct {
	VideoDriver  string
	Renderer     string
	Acceleration string // "gpu" or "software"
	MaxTexture   string // "WxH", or "unknown" when the query fails
}

// renderPipelineInfo reads the facts out of SDL. Extracted so the device
// build's values (and the software fallback path) can be verified against a
// real renderer in tests instead of only being eyeballed in a boot log.
func renderPipelineInfo(renderer *sdl.Renderer) renderPipeline {
	p := renderPipeline{Acceleration: "unknown", MaxTexture: "unknown"}
	p.VideoDriver = "unknown"
	if drv, err := sdl.GetCurrentVideoDriver(); err == nil {
		p.VideoDriver = drv
	}
	if renderer == nil {
		return p
	}
	if info, err := renderer.GetInfo(); err == nil {
		p.Renderer = info.Name
		p.MaxTexture = fmt.Sprintf("%dx%d", info.MaxTextureWidth, info.MaxTextureHeight)
		if info.Flags&sdl.RENDERER_SOFTWARE != 0 {
			p.Acceleration = "software"
		} else if info.Flags&sdl.RENDERER_ACCELERATED != 0 {
			p.Acceleration = "gpu"
		}
	}
	return p
}

// logRenderPipeline fires the structured "render pipeline" log line from the
// fact gathering above.
func logRenderPipeline(renderer *sdl.Renderer) {
	p := renderPipelineInfo(renderer)
	Log().Info("render pipeline",
		"video_driver", p.VideoDriver,
		"renderer", p.Renderer,
		"acceleration", p.Acceleration,
		"max_texture", p.MaxTexture,
	)
}
