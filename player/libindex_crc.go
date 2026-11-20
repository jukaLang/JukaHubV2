package main

// libindex_crc.go — the library index's CRC32 pass (LIB-1).
//
// Each game of up to 64 MiB gets its CRC32 the way the game details look it
// up (gmetaROMCRCs, gmeta_dat.go: the whole file, and without an iNES or SNES
// copier header; a .zip's from its directory), kept in the index with the
// file's size and time. gmetaROMCRCs asks the index first, so a game's page
// and the cover matcher stop reading the file every time.
//
// It is cheap by design, because it is optional work on a handheld: one
// file at a time on a thread at the lowest CPU and I/O priority
// (libIndexLowPriority), resting three times as long as each file took (at
// most a quarter of the card's time), waiting while a game or a video plays,
// and stopping when the battery is under 30% and not charging (the next look
// at the card starts it again). Disc images and anything larger are matched
// by name, as before. Settings → Games & emulators → Fingerprint games in
// the background turns it off.

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// libIndexCRCKey is the Custom switch (on unless set to false).
	libIndexCRCKey = "libraryFingerprints"
	// libIndexCRCMinBattery is the charge the pass stops under, off a charger.
	libIndexCRCMinBattery = 30
	// libIndexCRCSaveEvery is how many CRCs are made between two writes.
	libIndexCRCSaveEvery = 64
	// libIndexCRCWait is how often a paused pass looks again.
	libIndexCRCWait = 5 * time.Second
)

var libIndexCRCState struct {
	sync.Mutex
	running bool
	again   bool
}

// libIndexCRCOff mirrors the switch for the pass's goroutine.
var libIndexCRCOff atomic.Bool

// libIndexCRCEnabled reads the switch (render thread).
func libIndexCRCEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, libIndexCRCKey, true)
}

// libIndexCRCKick starts the pass, or has the running one go round again
// for games found since. The switch is read on the render thread.
func libIndexCRCKick() {
	tbxPost(func() {
		on := libIndexCRCEnabled(appConfig)
		libIndexCRCOff.Store(!on)
		if !on {
			return
		}
		st := &libIndexCRCState
		st.Lock()
		if st.running {
			st.again = true
			st.Unlock()
			return
		}
		st.running = true
		st.Unlock()
		go libIndexCRCRun()
	})
}

// libIndexCRCRun is the pass's goroutine.
func libIndexCRCRun() {
	st := &libIndexCRCState
	defer func() {
		if r := recover(); r != nil {
			logWarn("library", "game fingerprints stopped: %v", r)
		}
		// Written from a thread at the usual priority: this one is in the
		// idle I/O class, and every scan's write waits for this one.
		go libIndexFlush()
		st.Lock()
		st.running, st.again = false, false
		st.Unlock()
	}()
	// This goroutine keeps its thread, at the lowest priority, to the end;
	// the thread goes with it.
	libIndexLowPriority()
	battery := ""
	if IsTrimuiDevice() {
		battery = batterySysfsDir()
	}
	made := 0
	for {
		libIdx.mu.Lock()
		paths := append([]string(nil), libIdx.games...)
		libIdx.mu.Unlock()
		for _, path := range paths {
			// A game fingerprinted already costs a stat; the battery and the
			// player are asked only before a file is read.
			if _, known := libIndexCRCsFor(path, true); known {
				continue
			}
			if !libIndexCRCMayRun(battery) {
				return
			}
			took, ok := libIndexCRCOne(path)
			if !ok {
				continue
			}
			made++
			if made%libIndexCRCSaveEvery == 0 {
				go libIndexFlush()
			}
			time.Sleep(max(100*time.Millisecond, 3*took))
		}
		st.Lock()
		again := st.again
		st.again = false
		st.Unlock()
		if !again {
			if made > 0 {
				logDebug("library", "game fingerprints: %d made", made)
			}
			return
		}
	}
}

// libIndexCRCMayRun waits while a game or a video plays, and reports false
// when the pass should stop: switched off, or a low battery off a charger.
func libIndexCRCMayRun(battery string) bool {
	for {
		if libIndexCRCOff.Load() {
			return false
		}
		// No index to keep the answer in (closed for a rebuild, or given up
		// this run): reading would be for nothing. The next look restarts it.
		libIdx.mu.Lock()
		gone := libIdx.db == nil
		libIdx.mu.Unlock()
		if gone {
			return false
		}
		if battery != "" {
			if pct := batteryPercentIn(battery); pct >= 0 && pct < libIndexCRCMinBattery && !batteryChargingIn(powerSupplyRoot) {
				logDebug("library", "game fingerprints wait for a charger (battery %d%%)", pct)
				return false
			}
		}
		if !libIndexGameRunning() && playerPhase() == "idle" {
			return true
		}
		time.Sleep(libIndexCRCWait)
	}
}

// libIndexCRCOne makes one game's CRCs (the caller found none in the index
// for the file as it is now). It reports how long the read took and whether
// it read.
func libIndexCRCOne(path string) (time.Duration, bool) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() {
		return 0, false
	}
	started := time.Now()
	crcs := gmetaROMCRCs(path)
	took := time.Since(started)
	// A file written to while it was read keeps no answer.
	after, err := os.Stat(path)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return took, true
	}
	libIdx.mu.Lock()
	if libIdx.db != nil {
		rec := libIdx.fileLocked(filepath.Clean(path))
		rec.C, rec.CS, rec.CM, rec.CD = crcs, before.Size(), before.ModTime().UnixNano(), true
	}
	libIdx.mu.Unlock()
	return took, true
}

// libIndexKnownCRCs is the CRCs the index holds for path while the file's
// size and time are the ones they were made from (nil, true: the file has
// none, a disc image or too large). It never waits: a busy index is a miss.
func libIndexKnownCRCs(path string) ([]string, bool) {
	return libIndexCRCsFor(path, false)
}

// libIndexCRCsFor is libIndexKnownCRCs; wait takes the index's lock even
// when it is busy (the pass's own goroutine).
func libIndexCRCsFor(path string, wait bool) ([]string, bool) {
	if wait {
		libIdx.mu.Lock()
	} else if !libIdx.mu.TryLock() {
		return nil, false
	}
	if !libIdx.loaded || libIdx.broken || libIdx.files == nil {
		libIdx.mu.Unlock()
		return nil, false
	}
	rec := libIdx.files[filepath.Clean(path)]
	if rec == nil || !rec.CD {
		libIdx.mu.Unlock()
		return nil, false
	}
	size, mod := rec.CS, rec.CM
	crcs := append([]string(nil), rec.C...)
	libIdx.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil || info.Size() != size || info.ModTime().UnixNano() != mod {
		return nil, false
	}
	if len(crcs) == 0 {
		return nil, true
	}
	return crcs, true
}

// libIndexCRCRow is Settings → Games & emulators → Fingerprint games in the
// background.
func libIndexCRCRow() setRow {
	return setRow{
		ID: "libraryFingerprints", Label: "Fingerprint games in the background", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if libIndexCRCEnabled(c) {
				return "CRC32 of games up to 64 MB, read slowly while nothing plays and the battery is above 30%"
			}
			return "Off: a game's CRC32 is read only when its page asks for it"
		},
		Get: libIndexCRCEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, libIndexCRCKey, on)
			libIndexCRCOff.Store(!on)
			if on {
				libIndexCRCKick()
			}
			setToast("Fingerprint games in the background: " + setOnOff(on))
		},
	}
}
