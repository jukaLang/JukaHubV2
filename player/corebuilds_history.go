package main

// corebuilds_history.go — which core build made a game's save states.
//
// A save state is a snapshot of an emulator core's memory in that core's own
// format, and a newer build of the same core may refuse to load it. RetroArch
// does not record which build wrote a state, so JukaHub keeps the history
// itself: every time it replaces a core binary (installCoreFile — an update,
// an automatic update, a rollback, an activation from the compatibility panel)
// it notes the new build's CRC32, the identity the buildbot index uses, and
// when it became active, in core-history.json in the data folder. The first
// replacement it sees also records the build it replaced as "the build before".
//
// A state's modification time then says which build was active when it was
// written, and the game's page (gmeta_ui.go) shows one line when some states
// were written by a build other than the one installed now, naming it and
// pointing at Core builds, where the old build is kept for a rollback.
//
// It only judges what it saw: a state written after the binary on the card
// was put there belongs to that binary; a state older than everything
// recorded is not judged at all. A core replaced outside JukaHub (RetroArch's
// own updater, a copy from a PC) is only noticed through its file time.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// coreHistoryMaxEntries bounds one core's history; a state older than the
// oldest entry kept is not judged.
const coreHistoryMaxEntries = 12

// coreHistoryEntry is one build of a core that was active from From on. From
// is "" for the build that was active before the first recorded replacement.
// Build is the build's label (the buildbot date) when it is known.
type coreHistoryEntry struct {
	CRC32 string `json:"crc32"`
	Build string `json:"build,omitempty"`
	From  string `json:"from,omitempty"`
}

// coreHistoryFile is core-history.json.
type coreHistoryFile struct {
	Schema int                           `json:"schema"`
	Cores  map[string][]coreHistoryEntry `json:"cores"`
}

var coreHistoryMu sync.Mutex

// coreHistoryPathFn is where the history lives; tests point it at a
// temporary folder.
var coreHistoryPathFn = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "core-history.json")
}

func coreHistoryLoadLocked(path string) coreHistoryFile {
	h := coreHistoryFile{Schema: 1, Cores: map[string][]coreHistoryEntry{}}
	if path == "" {
		return h
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	var f coreHistoryFile
	if json.Unmarshal(data, &f) != nil || f.Cores == nil {
		return h
	}
	h.Cores = f.Cores
	return h
}

// coreHistoryFor is one core's recorded builds, oldest first.
func coreHistoryFor(core string) []coreHistoryEntry {
	return coreHistoryForAt(coreHistoryPathFn(), core)
}

func coreHistoryForAt(path, core string) []coreHistoryEntry {
	coreHistoryMu.Lock()
	defer coreHistoryMu.Unlock()
	return coreHistoryLoadLocked(path).Cores[core]
}

// coreHistoryNoteReplaced is installCoreFile's note that target, an active
// core binary, was just replaced by source. oldCRC is the CRC32 of the build
// it replaced ("" when there was none) and newCRC that of the new one, both
// from the bytes installCoreFile copied, so nothing is read again and a quick
// rollback-and-undo cannot hash a file that has already changed again. The
// write is queued for one worker (coreHistoryEnqueue): in order, and off the
// event thread a rollback runs on.
func coreHistoryNoteReplaced(source, target, oldCRC, newCRC string) {
	core, _ := parseCoreFile(filepath.Base(target))
	if core == "" || newCRC == "" {
		return
	}
	// A same-size build written inside one file-time tick would otherwise keep
	// the replaced build's cached checksum.
	emuCRCForget(target)
	coreHistoryEnqueue(coreHistoryJob{
		path:   coreHistoryPathFn(),
		core:   core,
		oldCRC: oldCRC,
		newCRC: newCRC,
		label:  coreHistoryLabelFor(source),
		at:     time.Now(),
	})
}

// coreHistoryJob is one replacement waiting to be written.
type coreHistoryJob struct {
	path   string
	core   string
	oldCRC string
	newCRC string
	label  string
	at     time.Time
}

// coreHistoryQueue holds the replacements not written yet. One worker at a
// time drains it in order; pending counts what is queued or being written.
var coreHistoryQueue struct {
	mu      sync.Mutex
	jobs    []coreHistoryJob
	running bool
	pending sync.WaitGroup
}

// coreHistoryEnqueue queues one replacement and starts the worker when it is
// idle. It never blocks on the card.
func coreHistoryEnqueue(job coreHistoryJob) {
	coreHistoryQueue.pending.Add(1)
	coreHistoryQueue.mu.Lock()
	coreHistoryQueue.jobs = append(coreHistoryQueue.jobs, job)
	start := !coreHistoryQueue.running
	coreHistoryQueue.running = true
	coreHistoryQueue.mu.Unlock()
	if start {
		go coreHistoryWorker()
	}
}

// coreHistoryWorker writes the queued replacements, oldest first, and exits
// when the queue is empty.
func coreHistoryWorker() {
	for {
		coreHistoryQueue.mu.Lock()
		if len(coreHistoryQueue.jobs) == 0 {
			coreHistoryQueue.running = false
			coreHistoryQueue.mu.Unlock()
			return
		}
		job := coreHistoryQueue.jobs[0]
		coreHistoryQueue.jobs = coreHistoryQueue.jobs[1:]
		coreHistoryQueue.mu.Unlock()
		coreHistoryRecord(job.path, job.core, job.oldCRC, job.newCRC, job.label, job.at)
		coreHistoryQueue.pending.Done()
	}
}

// coreHistoryFlush waits until every queued replacement is written. The test
// suite calls it before deleting the folder the history lives in.
func coreHistoryFlush() { coreHistoryQueue.pending.Wait() }

// coreHistoryLabelFor is the build label of a pool build (its buildbot date
// folder), "" for anything else (a backup being restored).
func coreHistoryLabelFor(source string) string {
	ext := strings.ToLower(filepath.Ext(source))
	if ext != ".so" && ext != ".dll" {
		return ""
	}
	if date := filepath.Base(filepath.Dir(source)); validCoreIndexDate(date) {
		return date
	}
	return ""
}

// coreHistoryRecord appends one replacement to a core's history in the file
// at path. A replacement made while the clock was wrong (the handheld has no
// RTC battery: before NTP it can boot into 1970) is not recorded: its time
// would place every later state before it.
func coreHistoryRecord(path, core, oldCRC, newCRC, label string, at time.Time) {
	coreHistoryMu.Lock()
	defer coreHistoryMu.Unlock()
	if path == "" {
		return
	}
	if at.Before(minSaneClock) {
		logDebug("emulators", "core history: %s replaced while the clock reads %s; not recorded", core, at.UTC().Format(time.RFC3339))
		return
	}
	h := coreHistoryLoadLocked(path)
	list := h.Cores[core]
	if len(list) == 0 && oldCRC != "" && oldCRC != newCRC {
		list = append(list, coreHistoryEntry{CRC32: oldCRC})
	}
	if n := len(list); n == 0 || list[n-1].CRC32 != newCRC {
		list = append(list, coreHistoryEntry{CRC32: newCRC, Build: label, From: at.UTC().Format(time.RFC3339)})
	}
	if len(list) > coreHistoryMaxEntries {
		list = list[len(list)-coreHistoryMaxEntries:]
	}
	h.Cores[core] = list
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		logWarn("emulators", "core history: %v", err)
	}
}

// coreBuildAt is the history entry that was active at t: the last one that
// started at or before it. ok is false when t is older than everything
// recorded.
func coreBuildAt(list []coreHistoryEntry, t time.Time) (coreHistoryEntry, bool) {
	var best coreHistoryEntry
	found := false
	for _, e := range list {
		if e.From == "" {
			if !found {
				best, found = e, true
			}
			continue
		}
		from, err := time.Parse(time.RFC3339, e.From)
		if err != nil {
			continue
		}
		if !from.After(t) {
			best, found = e, true
		}
	}
	return best, found
}

// coreStatesNote is the line a game's page shows when some of its save
// states were written by another build of the core that runs it than the one
// installed now, and "" otherwise. It reads the card (the launcher, the
// history, the core's checksum — cached by size and time): background only.
func coreStatesNote(launcher, system string, states []gmetaState) string {
	if len(states) == 0 {
		return ""
	}
	core := gameCoreFromLauncher(launcher, system)
	if core == "" {
		return ""
	}
	list := coreHistoryFor(core)
	if len(list) == 0 {
		return ""
	}
	active := coreActivePath(core)
	if active == "" {
		return ""
	}
	info, err := os.Stat(active)
	if err != nil {
		return ""
	}
	cur, err := emuCachedCRC32(active)
	if err != nil {
		return ""
	}
	return coreStatesNoteFor(coreConfigDisplayName(core), cur, info.ModTime(), list, states)
}

// coreStatesNoteFor is coreStatesNote's decision, without the card: cur is the
// installed build's CRC32 and since the time its binary was written. Times
// from a clock that was wrong (before minSaneClock) are never judged: a state
// with one is skipped, and a binary written with one makes every state's
// place relative to it unknown, so nothing is said at all.
func coreStatesNoteFor(name, cur string, since time.Time, list []coreHistoryEntry, states []gmetaState) string {
	if !since.IsZero() && since.Before(minSaneClock) {
		return ""
	}
	n := 0
	label := ""
	for _, st := range states {
		if st.Time.Before(minSaneClock) {
			continue
		}
		if !since.IsZero() && !st.Time.Before(since) {
			// Written with the binary that is on the card now.
			continue
		}
		e, ok := coreBuildAt(list, st.Time)
		if !ok || e.CRC32 == cur {
			continue
		}
		n++
		if label == "" {
			label = e.Build
		}
	}
	if n == 0 {
		return ""
	}
	which := "another " + name + " build"
	if label != "" {
		which = "the " + label + " " + name + " build"
	}
	what := "1 save state was"
	if n > 1 {
		what = fmt.Sprintf("%d save states were", n)
	}
	return what + " made with " + which + " and may not load · Core builds keeps the old build"
}

// gameCoreFromLauncher names the libretro core a launcher starts: the core
// file itself (Windows launches a .dll), the one core a launch script names,
// or the system's usual core when the script names several or none.
func gameCoreFromLauncher(launcher, system string) string {
	fallback := normalizeCoreName(defaultCoreForSystem(system))
	ext := strings.ToLower(filepath.Ext(launcher))
	if ext == ".so" || ext == ".dll" {
		if core, _ := parseCoreFile(filepath.Base(launcher)); core != "" {
			return core
		}
		return fallback
	}
	f, err := os.Open(launcher)
	if err != nil {
		return fallback
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return fallback
	}
	found := coreNamesIn(string(head))
	switch {
	case len(found) == 1:
		return found[0]
	case len(found) > 1:
		for _, c := range found {
			if c == fallback {
				return c
			}
		}
		return found[0]
	}
	return fallback
}

// coreNamesIn lists the distinct "<core>_libretro.so" / ".dll" names a script
// spells out, in order. A name built from a variable ("${CORE}_libretro.so")
// is not one.
func coreNamesIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	lower := strings.ToLower(text)
	const mark = "_libretro."
	for i := 0; i < len(lower); {
		j := strings.Index(lower[i:], mark)
		if j < 0 {
			break
		}
		end := i + j
		start := end
		for start > 0 && coreNameByte(lower[start-1]) {
			start--
		}
		rest := lower[end+len(mark):]
		if name := lower[start:end]; name != "" && !seen[name] &&
			(strings.HasPrefix(rest, "so") || strings.HasPrefix(rest, "dll")) {
			seen[name] = true
			out = append(out, name)
		}
		i = end + len(mark)
	}
	return out
}

func coreNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// coreActivePath is the binary RetroArch loads for core, "" when none is
// installed.
func coreActivePath(core string) string {
	for _, dir := range retroArchCoreDirs() {
		for _, ext := range []string{".so", ".dll"} {
			p := filepath.Join(dir, core+"_libretro"+ext)
			if regularFile(p) {
				return p
			}
		}
	}
	return ""
}
