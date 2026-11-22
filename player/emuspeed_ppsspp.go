package main

// emuspeed_ppsspp.go — the PSP's Speed level (emuspeed.go) for JukaHub's own
// PPSSPP (ppsspp_managed.go), which plays PSP games on the handheld outside
// RetroArch and reads its settings from the ppsspp.ini in its own HOME.
//
// The same levels as the PPSSPP core's, in the ini's own keys ([Graphics]):
//
//	Fast     FrameSkip 1 with AutoFrameSkip on: a frame is skipped only when
//	         the game falls behind, and never two in a row.
//	Fastest  Fast, and lazy texture caching (TextureBackoffCache), curves
//	         and surfaces at low detail (SplineBezierQuality 0), GPU
//	         read-backs skipped (SkipGPUReadbackMode 1, or SkipGPUReadbacks in
//	         builds before 1.17) and no anisotropic filtering.
//
// For one launch, like RetroArch's settings: a value is written only where the
// file holds PPSSPP's default for it, and a key the file lacks is not added
// (PPSSPP writes every key it knows when it exits, so only a build without the
// setting lacks one). The change is journalled first in JukaHub's data folder
// (emu-speed-ppsspp.json) and put back once the game's process group is gone,
// only where the file still holds what JukaHub wrote: a change made in
// PPSSPP's own menu during the game wins. A journal left by a JukaHub that
// stopped mid-game is put back before the next PSP start. A game with settings
// of its own in PPSSPP (its <ID>_ppsspp.ini) plays with those, as PPSSPP reads
// them instead of these.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// emuSpeedPPSSPPJournalName is the journal in JukaHub's data folder.
const emuSpeedPPSSPPJournalName = "emu-speed-ppsspp.json"

// emuSpeedINIOpt is one ppsspp.ini value a level sets.
type emuSpeedINIOpt struct {
	Section  string
	Key      string
	Value    string
	Defaults []string // PPSSPP's own values; any other is someone's choice
	Fastest  bool     // only Fastest sets it
}

// emuSpeedPPSSPPOpts are the levels' values (see the file comment).
var emuSpeedPPSSPPOpts = []emuSpeedINIOpt{
	{Section: "Graphics", Key: "FrameSkip", Value: "1", Defaults: []string{"0"}},
	{Section: "Graphics", Key: "AutoFrameSkip", Value: "True", Defaults: []string{"False"}},
	{Section: "Graphics", Key: "TextureBackoffCache", Value: "True", Defaults: []string{"False"}, Fastest: true},
	{Section: "Graphics", Key: "SplineBezierQuality", Value: "0", Defaults: []string{"2"}, Fastest: true},
	{Section: "Graphics", Key: "SkipGPUReadbackMode", Value: "1", Defaults: []string{"0"}, Fastest: true},
	{Section: "Graphics", Key: "SkipGPUReadbacks", Value: "True", Defaults: []string{"False"}, Fastest: true},
	{Section: "Graphics", Key: "AnisotropyLevel", Value: "0", Defaults: []string{"4"}, Fastest: true},
}

// emuSpeedINIEdit is one value written for a launch: what the file held and
// what was written.
type emuSpeedINIEdit struct {
	Path    string `json:"path"`
	Section string `json:"section"`
	Key     string `json:"key"`
	Before  string `json:"before"`
	Wrote   string `json:"wrote"`
}

type emuSpeedINIJournal struct {
	Edits []emuSpeedINIEdit `json:"edits"`
}

// emuSpeedPPSSPPMu serialises the journal and the ini edits: the launch runs
// on the main thread, the restore on the reaper goroutine.
var emuSpeedPPSSPPMu sync.Mutex

func init() {
	gameLaunchRegisterExited(emuSpeedPPSSPPExited)
}

func emuSpeedPPSSPPJournalPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, emuSpeedPPSSPPJournalName)
}

// emuSpeedPPSSPPIni is the ini JukaHub's PPSSPP reads, "" when there is none
// to edit.
func emuSpeedPPSSPPIni() string {
	memstick := ppssppManagedMemstick()
	if memstick == "" {
		return ""
	}
	return ppssppMemstickIni(memstick)
}

// emuSpeedPPSSPPPlan lists the edits a level makes to an ini's lines. Pure.
func emuSpeedPPSSPPPlan(path string, lines []string, level string) []emuSpeedINIEdit {
	if level != emuSpeedFast && level != emuSpeedFastest {
		return nil
	}
	var edits []emuSpeedINIEdit
	for _, o := range emuSpeedPPSSPPOpts {
		if o.Fastest && level != emuSpeedFastest {
			continue
		}
		cur, had := ppssppINIValue(lines, o.Section, o.Key)
		cur = strings.TrimSpace(cur)
		if !had || strings.EqualFold(cur, o.Value) || !emuSpeedIsDefault(cur, o.Defaults) {
			continue
		}
		edits = append(edits, emuSpeedINIEdit{Path: path, Section: o.Section, Key: o.Key, Before: cur, Wrote: o.Value})
	}
	return edits
}

// emuSpeedPPSSPPKnown reports whether a journal entry is one this file could
// have written: the journal sits in the data folder, and a restore must never
// edit any other file or key.
func emuSpeedPPSSPPKnown(e emuSpeedINIEdit, ini string) bool {
	if ini == "" || filepath.Clean(e.Path) != filepath.Clean(ini) {
		return false
	}
	for _, o := range emuSpeedPPSSPPOpts {
		if o.Section == e.Section && o.Key == e.Key && o.Value == e.Wrote {
			return true
		}
	}
	return false
}

// emuSpeedPPSSPPPrepare gives JukaHub's PPSSPP the PSP's level for this launch
// (emuSpeedPrepare). Main thread.
func emuSpeedPPSSPPPrepare(game libraryGame) {
	// What an interrupted launch left goes back first, so its values are not
	// taken for PPSSPP's own.
	emuSpeedPPSSPPRestore()
	level := emuSpeedLevelFor(appConfig, game.System)
	if level != emuSpeedFast && level != emuSpeedFastest {
		return
	}
	ini, journal := emuSpeedPPSSPPIni(), emuSpeedPPSSPPJournalPath()
	if ini == "" || journal == "" {
		return
	}
	if !regularFile(ini) {
		logInfo("emulation", "emulator speed: JukaHub's PPSSPP has no ppsspp.ini yet (it writes one when it exits); its own settings this launch")
		return
	}
	emuSpeedPPSSPPMu.Lock()
	defer emuSpeedPPSSPPMu.Unlock()
	raw, err := os.ReadFile(ini)
	if err != nil {
		logWarn("emulation", "emulator speed: cannot read %s: %v", ini, err)
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	edits := emuSpeedPPSSPPPlan(ini, lines, level)
	if len(edits) == 0 {
		return
	}
	// Write-ahead: what the file held is on the card before it changes.
	data, err := json.Marshal(emuSpeedINIJournal{Edits: edits})
	if err == nil {
		err = AtomicWrite(journal, data, 0o644)
	}
	if err != nil {
		logWarn("emulation", "emulator speed: cannot record PPSSPP's settings to put back, so none change: %v", err)
		return
	}
	if err := writePPSSPPControls(ini, func(lines []string) []string {
		for _, e := range edits {
			lines = ppssppSetSectionValue(lines, e.Section, e.Key, e.Wrote)
		}
		return lines
	}); err != nil {
		// The journal stays: the restore only puts back what holds its value.
		logWarn("emulation", "emulator speed: cannot set PPSSPP's settings in %s: %v", ini, err)
		return
	}
	set := make([]string, 0, len(edits))
	for _, e := range edits {
		set = append(set, e.Key+" = "+e.Wrote)
	}
	Log().Info("emulator speed for this launch", "game", game.Title, "emulator", "JukaHub's PPSSPP", "level", emuSpeedLabel(level), "settings", strings.Join(set, ", "))
}

// emuSpeedPPSSPPRestore puts back what the journal records, where the ini
// still holds what was written, and removes the journal. Any goroutine.
func emuSpeedPPSSPPRestore() {
	journal := emuSpeedPPSSPPJournalPath()
	if journal == "" {
		return
	}
	emuSpeedPPSSPPMu.Lock()
	defer emuSpeedPPSSPPMu.Unlock()
	raw, err := os.ReadFile(journal)
	if err != nil {
		return
	}
	var j emuSpeedINIJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		logWarn("emulation", "emulator speed: %s is unreadable and goes: %v", journal, err)
		_ = os.Remove(journal)
		return
	}
	ini := emuSpeedPPSSPPIni()
	var edits []emuSpeedINIEdit
	for _, e := range j.Edits {
		if emuSpeedPPSSPPKnown(e, ini) {
			edits = append(edits, e)
		}
	}
	if len(edits) > 0 && regularFile(ini) {
		text, err := os.ReadFile(ini)
		if err != nil {
			logWarn("emulation", "emulator speed: cannot read %s to put PPSSPP's settings back: %v", ini, err)
			return // the journal stays for the next try
		}
		lines := strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n")
		var back []emuSpeedINIEdit
		for _, e := range edits {
			if cur, had := ppssppINIValue(lines, e.Section, e.Key); had && strings.EqualFold(strings.TrimSpace(cur), e.Wrote) {
				back = append(back, e)
			}
		}
		if len(back) > 0 {
			if err := writePPSSPPControls(ini, func(lines []string) []string {
				for _, e := range back {
					lines = ppssppSetSectionValue(lines, e.Section, e.Key, e.Before)
				}
				return lines
			}); err != nil {
				logWarn("emulation", "emulator speed: cannot put PPSSPP's settings back in %s: %v", ini, err)
				return
			}
			logInfo("emulation", "emulator speed: put back %d of JukaHub's PPSSPP settings", len(back))
		}
	}
	if err := os.Remove(journal); err != nil && !os.IsNotExist(err) {
		logWarn("emulation", "emulator speed: cannot remove %s: %v", journal, err)
	}
}

// emuSpeedPPSSPPExited is the Exited hook (the reaper goroutine): a game that
// played in JukaHub's PPSSPP has its settings put back.
func emuSpeedPPSSPPExited(_ libraryGame, launcher string, _ time.Duration, _ error) {
	if ppssppIsManagedLauncher(launcher) {
		emuSpeedPPSSPPRestore()
	}
}
