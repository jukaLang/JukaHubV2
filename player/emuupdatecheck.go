package main

// emuupdatecheck.go — knowing, before a press, which emulators have a newer
// build.
//
// The Emulators page and the Core builds page both want an "Update available"
// badge, and neither may wait on the network or read every core off the card
// while drawing. One background check answers for both: it reads the
// buildbot index for this platform's channel and compares it with the CRC32 of
// every installed core (cached by size and modification time, so a second
// check reads nothing it already read), and asks each project JukaHub installed
// for its newest release: the managed emulators on Windows, hakuX on the
// handheld. The answer is kept here, guarded by a mutex, and the pages only
// ever read it.
//
// The check is throttled — a page opening does not hit GitHub's rate limit —
// and skipped outright when the device is offline.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// emuUpdateCheckInterval is how long one answer is trusted before a page
	// opening asks again.
	emuUpdateCheckInterval = 6 * time.Hour
	// emuCRCCacheMax bounds the checksum cache; a card holds far fewer cores.
	emuCRCCacheMax = 512
)

// emuUpdateChecksEnabled lets the test binary keep the pages from starting a
// network check behind every render test (TestMain turns it off; a test that
// wants the check runs emuRunUpdateCheck itself).
var emuUpdateChecksEnabled = true

// emuUpdateState is the last answer to "is there something newer".
var emuUpdateState struct {
	mu        sync.Mutex
	checking  bool
	checkedAt time.Time
	// cores maps an installed core to the newer index build; current lists
	// the cores the index has and that match it.
	cores   map[string]coreIndexEntry
	current map[string]bool
	// indexed lists every core the channel publishes, so a page can tell "not
	// published" from "up to date".
	indexed map[string]coreIndexEntry
	// win maps a managed emulator — a Windows one, or the handheld's Xbox tile
	// for hakuX — to its newer release.
	win map[string]emuWinRelease
	// offline records that the last check could not run for lack of a
	// connection.
	offline bool
	err     string
}

// emuCoreUpdateKnown reports a newer index build for core, from the last
// check.
func emuCoreUpdateKnown(core string) (coreIndexEntry, bool) {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	entry, ok := emuUpdateState.cores[normalizeCoreName(core)]
	return entry, ok
}

// emuCoreIndexEntry is the index line for core from the last check.
func emuCoreIndexEntry(core string) (coreIndexEntry, bool) {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	entry, ok := emuUpdateState.indexed[normalizeCoreName(core)]
	return entry, ok
}

// emuCoreKnownCurrent reports that the last check found core matching the
// index.
func emuCoreKnownCurrent(core string) bool {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	return emuUpdateState.current[normalizeCoreName(core)]
}

// emuWinUpdateKnown reports a newer release for a managed Windows emulator.
func emuWinUpdateKnown(name string) (emuWinRelease, bool) {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	rel, ok := emuUpdateState.win[strings.ToLower(strings.TrimSpace(name))]
	return rel, ok
}

// emuUpdateCount is how many updates the last check found.
func emuUpdateCount() int {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	return len(emuUpdateState.cores) + len(emuUpdateState.win)
}

// emuUpdateCheckSnapshot is the check's state for a page header.
func emuUpdateCheckSnapshot() (checking bool, checkedAt time.Time, offline bool, errText string) {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	return emuUpdateState.checking, emuUpdateState.checkedAt, emuUpdateState.offline, emuUpdateState.err
}

// emuForgetUpdate drops a badge once its update is installed (or rolled
// back): the next check would say so anyway, but the tile should not claim an
// update for a minute after the install finished.
func emuForgetUpdate(name string) {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	delete(emuUpdateState.win, key)
	core := normalizeCoreName(name)
	delete(emuUpdateState.cores, core)
	delete(emuUpdateState.current, core)
}

// emuForgetAllUpdates is emuForgetUpdate for a pass that touched many cores.
func emuForgetAllUpdates() {
	emuUpdateState.mu.Lock()
	defer emuUpdateState.mu.Unlock()
	emuUpdateState.cores = nil
	emuUpdateState.current = nil
	emuUpdateState.checkedAt = time.Time{}
}

// emuCRCCache remembers core checksums by path, size and modification time.
var emuCRCCache struct {
	mu      sync.Mutex
	entries map[string]emuCRCEntry
}

type emuCRCEntry struct {
	size int64
	mod  time.Time
	crc  string
}

// emuCachedCRC32 is coreCRC32File without re-reading a file that has not
// changed since it was last read.
func emuCachedCRC32(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	emuCRCCache.mu.Lock()
	if hit, ok := emuCRCCache.entries[path]; ok && hit.size == info.Size() && hit.mod.Equal(info.ModTime()) {
		emuCRCCache.mu.Unlock()
		return hit.crc, nil
	}
	emuCRCCache.mu.Unlock()
	crc, err := coreCRC32File(path)
	if err != nil {
		return "", err
	}
	emuCRCCache.mu.Lock()
	if emuCRCCache.entries == nil || len(emuCRCCache.entries) >= emuCRCCacheMax {
		emuCRCCache.entries = make(map[string]emuCRCEntry, 64)
	}
	emuCRCCache.entries[path] = emuCRCEntry{size: info.Size(), mod: info.ModTime(), crc: crc}
	emuCRCCache.mu.Unlock()
	return crc, nil
}

// emuCRCForget drops a file's cached checksum. Every path that replaces a core
// calls it: a same-size build written inside one timestamp tick would otherwise
// keep the old answer.
func emuCRCForget(path string) {
	emuCRCCache.mu.Lock()
	delete(emuCRCCache.entries, path)
	emuCRCCache.mu.Unlock()
}

// emuCompareCores splits the installed cores into those the index has a
// different build of, and those that match it. A core the index does not carry
// is in neither; nor is one on the firmware's read-only partition, or one whose
// listed build this device already refused (coreBuildRefused) — offering those
// as updates would only lead to a press that cannot install them.
func emuCompareCores(index []coreIndexEntry, active map[string]coreBuild) (stale map[string]coreIndexEntry, current map[string]bool) {
	stale = map[string]coreIndexEntry{}
	current = map[string]bool{}
	for _, entry := range index {
		build, ok := active[entry.Core]
		if !ok || build.Path == "" || coreDirIsSystem(filepath.Dir(build.Path)) {
			continue
		}
		if _, refused := coreBuildRefused(entry.Core, entry.CRC32); refused {
			continue
		}
		sum, err := emuCachedCRC32(build.Path)
		if err != nil {
			continue
		}
		if sum == entry.CRC32 {
			current[entry.Core] = true
			continue
		}
		stale[entry.Core] = entry
	}
	return stale, current
}

// emuStartUpdateCheck runs the background check unless one is running, the
// device is offline, or the last answer is younger than maxAge (zero always
// checks). The Emulators page passes emuUpdateCheckInterval; the Core builds
// page, whose whole point is the answer, a few minutes.
func emuStartUpdateCheck(maxAge time.Duration) {
	if !emuUpdateChecksEnabled {
		return
	}
	emuUpdateState.mu.Lock()
	if emuUpdateState.checking {
		emuUpdateState.mu.Unlock()
		return
	}
	if !IsOnline() {
		emuUpdateState.offline = true
		emuUpdateState.mu.Unlock()
		return
	}
	if maxAge > 0 && !emuUpdateState.checkedAt.IsZero() && time.Since(emuUpdateState.checkedAt) < maxAge {
		emuUpdateState.mu.Unlock()
		return
	}
	emuUpdateState.checking = true
	emuUpdateState.offline = false
	emuUpdateState.mu.Unlock()
	go emuRunUpdateCheck()
}

// emuRunUpdateCheck is the check itself. It never installs anything.
func emuRunUpdateCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := newEmulatorUpdateClient()

	var stale map[string]coreIndexEntry
	var current map[string]bool
	var indexed map[string]coreIndexEntry
	errText := ""
	inv := scanCoreInventory()
	if len(inv.Active) > 0 || IsWindows() {
		if index, err := fetchCoreIndex(ctx, client); err == nil {
			indexed = make(map[string]coreIndexEntry, len(index))
			for _, entry := range index {
				indexed[entry.Core] = entry
			}
			stale, current = emuCompareCores(index, inv.Active)
		} else {
			errText = emuFriendlyError(err)
		}
	}

	win := map[string]emuWinRelease{}
	if IsWindows() {
		for _, spec := range emuWinSpecs {
			if !spec.installable() {
				continue
			}
			_, manifest, ok := emuWinManaged(spec.Name)
			if !ok {
				continue
			}
			release, err := emuWinResolve(ctx, client, spec)
			if err != nil {
				if errText == "" {
					errText = emuFriendlyError(err)
				}
				continue
			}
			if emuWinUpdateAvailable(manifest, release) {
				win[strings.ToLower(spec.Name)] = release
			}
		}
	} else {
		// The handheld's one standalone install: hakuX, when JukaHub put it
		// there. Its badge sits on the Xbox tile, keyed by the tile's name.
		release, newer, err := hakuxUpdateFor(ctx, client)
		if err != nil && errText == "" {
			errText = emuFriendlyError(err)
		}
		if newer {
			win[strings.ToLower(emuXboxEntryName)] = release
		}
	}

	emuUpdateState.mu.Lock()
	emuUpdateState.checking = false
	emuUpdateState.checkedAt = time.Now()
	if indexed != nil {
		emuUpdateState.cores = stale
		emuUpdateState.current = current
		emuUpdateState.indexed = indexed
	}
	emuUpdateState.win = win
	emuUpdateState.err = errText
	emuUpdateState.mu.Unlock()
}
