package main

// ──────────────────────────────────────────────────────────────────────
// Low-memory device tuning
//
// JukaHub has to stay responsive on a 1 GB TrimUI Smart Pro while video,
// thumbnails and a software-drawn UI compete for the same RAM. This file holds
// the levers that made the biggest difference, kept pure where possible so
// they can be tested without a device:
//
//   * Go garbage-collector tuning (so the heap does not balloon between
//     collections while a video is decoding).
//   * A debounced cache-pressure evictor (the old loop cleared every cache and
//     ran a full GC every ~10 frames while memory was low, which showed up as
//     stutter during transitions).
//   * A decode-size cap (4K sources used to be decoded to 33 MB RGBA frames
//     even though the panel is 1280×720).
// ──────────────────────────────────────────────────────────────────────

import (
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
)

// gcSettings is one device's Go runtime tuning.
type gcSettings struct {
	Percent     int   // GOGC
	MemoryLimit int64 // soft heap limit in bytes
}

// gcSettingsFor picks the collector settings for a device. ok is false when
// the device has enough memory that stock settings are the better choice.
//
// The handheld target gets a lower GOGC (collect a little earlier, keep the
// heap small) plus a soft memory limit: the limit is what actually prevents
// the "video decode + thumbnail grid + transition layer" spike from pushing
// the device into swap. 384 MB leaves room for SDL textures and ffmpeg, which
// live outside the Go heap, under the 1 GB total.
func gcSettingsFor(ramMB uint64, handheld bool) (gcSettings, bool) {
	if !handheld && ramMB == 0 {
		// Desktop with unknown RAM: leave the collector at its defaults
		// rather than guessing a limit that could throttle a big machine.
		return gcSettings{}, false
	}
	if ramMB > 2048 {
		// Even a handheld with this much RAM does not need the limit; a too
		// tight soft limit would only burn CPU on unnecessary collections.
		return gcSettings{}, false
	}
	if handheld || ramMB <= 1024 {
		return gcSettings{Percent: 60, MemoryLimit: 384 << 20}, true
	}
	return gcSettings{Percent: 80, MemoryLimit: 768 << 20}, true
}

// deviceRAMMB reports total physical RAM, or 0 when it cannot be determined.
func deviceRAMMB() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	return readProcMeminfo().MemTotal / 1024
}

// applyGCForDevice installs the collector settings for this device. Explicit
// GOGC/GOMEMLIMIT environment variables always win: an operator measuring the
// app should never have their experiment silently overridden.
func applyGCForDevice() {
	if v := os.Getenv("GOGC"); v != "" {
		log.Printf("[perf] GOGC=%s set by environment; leaving the collector alone", v)
		return
	}
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		log.Printf("[perf] GOMEMLIMIT=%s set by environment; leaving the collector alone", v)
		return
	}
	handheld := IsTrimuiDevice() || P().IsHandheld()
	settings, ok := gcSettingsFor(deviceRAMMB(), handheld)
	if !ok {
		log.Printf("[perf] GC: stock settings (no memory constraint detected)")
		return
	}
	debug.SetGCPercent(settings.Percent)
	debug.SetMemoryLimit(settings.MemoryLimit)
	log.Printf("[perf] GC: GOGC=%d memlimit=%d MB (handheld=%v)", settings.Percent, settings.MemoryLimit>>20, handheld)
}

// ──────────────────────────────────────────────────────────────────────
// Debounced cache eviction
// ──────────────────────────────────────────────────────────────────────

const (
	evictCriticalCooldownMS = 15_000
	evictWarningCooldownMS  = 60_000
)

// cachePressureEvictor decides when memory-pressure cleanup may run. The main
// loop calls it every few frames; acting only on a new level or after a
// cooldown keeps a low-memory device from re-clearing caches (and running a
// full GC) several times a second.
type cachePressureEvictor struct {
	lastLevel MemoryPressureLevel
	lastAct   uint64
	seen      bool
}

// shouldAct reports whether cleanup should run now.
func (e *cachePressureEvictor) shouldAct(level MemoryPressureLevel, nowMS uint64) bool {
	if level == MemNormal {
		e.seen = false
		e.lastLevel = MemNormal
		return false
	}
	cooldown := uint64(evictCriticalCooldownMS)
	if level == MemWarning {
		cooldown = evictWarningCooldownMS
	}
	elapsed := nowMS - e.lastAct
	if nowMS < e.lastAct {
		elapsed = cooldown // clock reset (tests, suspend/resume)
	}
	if !e.seen || e.lastLevel != level || elapsed >= cooldown {
		e.seen = true
		e.lastLevel = level
		e.lastAct = nowMS
		return true
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────
// Video decode sizing
// ──────────────────────────────────────────────────────────────────────

// videoDecodeMaxSize is the largest frame worth decoding for the display.
// Decoding above the panel size costs memory bandwidth (RGBA frames are
// width×height×4 bytes) and buys nothing on a 1280×720 screen.
func videoDecodeMaxSize(handheld bool) (int32, int32) {
	if handheld {
		return 1280, 720
	}
	return 1920, 1080
}

// videoDecodeSize fits w×h inside maxW×maxH preserving the aspect ratio and
// rounds to even dimensions, which rawvideo output requires. Sources already
// within the box are returned unchanged so no scaling filter is added.
func videoDecodeSize(w, h, maxW, maxH int32) (int32, int32) {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return w, h
	}
	if w <= maxW && h <= maxH {
		return w, h
	}
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	nw := int32(math.Floor(float64(w) * scale))
	nh := int32(math.Floor(float64(h) * scale))
	nw &^= 1
	nh &^= 1
	if nw < 2 {
		nw = 2
	}
	if nh < 2 {
		nh = 2
	}
	return nw, nh
}

// ──────────────────────────────────────────────────────────────────────
// SDL texture memory accounting
// ──────────────────────────────────────────────────────────────────────

// textureMemoryStats is a snapshot of the large SDL texture allocations the
// UI owns. It is logged at startup and (when JUKAHUB_TEXSTATS is set) while
// the app runs, so a memory regression is visible instead of guessed at.
type textureMemoryStats struct {
	TextCache    int64 // renderText texture cache
	HomeText     int64 // drawText label cache
	Thumbnails   int64 // thumbnail textures
	AppIcons     int64 // launcher icon textures
	ChatImages   int64 // decoded chat images
	ChatAvatars  int64 // chat avatar textures
	VideoOverlay int64 // cached static video overlay layer
	VideoFrame   int64 // current decoded RGBA frame buffer
	UILayer      int64 // offscreen compositing layer
}

func (s textureMemoryStats) Total() int64 {
	return s.TextCache + s.HomeText + s.Thumbnails + s.AppIcons + s.ChatImages + s.ChatAvatars +
		s.VideoOverlay + s.VideoFrame + s.UILayer
}

// collectTextureMemoryStats reads the live caches. Safe to call from the main
// loop; each cache reports its own size under its own lock.
func collectTextureMemoryStats() textureMemoryStats {
	var stats textureMemoryStats
	if globalTextCache != nil {
		stats.TextCache = globalTextCache.Bytes()
	}
	stats.HomeText = homeTextCacheBytes()
	stats.Thumbnails = getThumbnailTextures().Bytes()
	_, stats.AppIcons = appIconTextureStats()
	_, stats.ChatImages = chatImageCacheStats()
	_, stats.ChatAvatars = chatAvatarCacheStats()

	embeddedPlayer.mu.Lock()
	stats.VideoFrame = int64(len(embeddedPlayer.currentRaw))
	embeddedPlayer.mu.Unlock()

	stats.VideoOverlay = cachedVideoOverlayBytes()
	if uiLayerTex != nil {
		stats.UILayer = int64(uiLayerW) * int64(uiLayerH) * 4
	}
	return stats
}

// textureStatsLoggingEnabled reports whether JUKAHUB_TEXSTATS=1 asked for
// periodic texture accounting. It is read once; the value never changes.
var textureStatsLoggingEnabled = os.Getenv("JUKAHUB_TEXSTATS") == "1"

// textureStatsInterval is how often the periodic log fires (in frames); at
// the handheld cadence that is roughly every five seconds.
const textureStatsInterval = 150

// logTextureMemoryStats writes one accounting line. Called on the main loop;
// formatting is cheap and only happens when explicitly enabled.
func logTextureMemoryStats(reason string) {
	s := collectTextureMemoryStats()
	// The text cache is the one cache whose ceiling is a byte budget rather
	// than a fixed allocation, so print the budget beside its usage: a report
	// that reads "text=23/24 MB" says the cache is at its cap, not leaking.
	textCap := int64(0)
	if globalTextCache != nil {
		textCap = globalTextCache.Limit()
	}
	// The three small caches are printed in KB: an avatar cache of 768 KB would
	// read as "0 MB" and look like it was not being accounted for at all.
	log.Printf("[tex] %s: total=%d MB (text=%d/%d, labels=%d, thumbs=%d, icons=%dK, chat=%dK, avatars=%dK, video-overlay=%d, frame=%d, ui-layer=%d)",
		reason, s.Total()>>20, s.TextCache>>20, textCap>>20, s.HomeText>>20, s.Thumbnails>>20,
		s.AppIcons>>10, s.ChatImages>>10, s.ChatAvatars>>10,
		s.VideoOverlay>>20, s.VideoFrame>>20, s.UILayer>>20)
}
