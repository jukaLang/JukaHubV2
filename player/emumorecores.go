package main

// emumorecores.go — "Get more cores": the other RetroArch cores that play the
// consoles on the card.
//
// Juka asked on 2026-11-18 for more cores, and for the question "which one?"
// when a game has more than one (that question is emupicker.go's). A TrimUI
// card ships one or two cores per console, CrossMix a few more, and the
// Emulators page could already install any core the buildbot publishes — one
// press per core, from a list of two hundred file names that says nothing
// about which ones are worth having. So one action now fetches, for every
// console that has games on the card, the cores worth having that are not
// there yet:
//
//   - for a console the ranking knows (emubest.go; docs/GAME_LIBRARY.md,
//     "Handheld, best first"), its RetroArch cores in rank order;
//   - for the rest, the short lists below (emuMoreCoreSuggestions): the cores
//     the TrimUI firmwares ship for that
//     console on this chip.
//
// A core is fetched only when the buildbot's aarch64 index lists it, it is not
// installed, and that exact build was not refused here before
// (coreBuildRefused). Each one goes through the install the Emulators page
// uses (planEmuInstall → applyEmuInstallPlan: the CRC32 the index states, the
// size cap, elfcompat's "can this firmware load it" before anything is
// written), each after the free-space floor a core update keeps
// (coreUpdateFreeFloor). A core that draws through RetroArch's GPU context
// (coreNeedsGPU: the N64, Dreamcast, PSP and Saturn 3D cores) is fetched only
// for a console with no emulator at all: a generic buildbot build of one may
// load and then show nothing under TrimUI's OpenGL-ES-only RetroArch, and a
// console that plays already should not be handed a choice that may not. Those
// stay one press each on the Emulators page, whose row says so, and the
// summary names the ones it left there.
//
// Where it is offered: Settings → Games & emulators → Get more cores, a pinned
// tile on the Emulators page, and for one console the "Which emulator?" sheet's
// last row and X on the game options' Emulator row. Progress is the install
// bar's; the result is one toast. The new cores are choices a game can start
// in (emuchoices.go), so the next start of a game with more than one asks.
//
// By itself: with Emulator updates on Install automatically, the daily pass
// fetches them once per console (an emuRuntimeUpdater, registered below). A
// console is then recorded as done (emu-morecores.json beside the patch
// state), so a core the user deletes is not fetched again, and a console whose
// first games arrive later gets its cores at a later pass. Like the other
// updaters it never runs over a game, and only at the pass's quiet moment.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// emuMoreCoresStateName is the record of the consoles done, in the patch
	// state folder.
	emuMoreCoresStateName = "emu-morecores.json"
	// emuMoreCoresEntryName is the pinned tile on the Emulators page.
	emuMoreCoresEntryName = "Get more cores"
	// emuMoreCoresPlanFresh is how long the daily pass's check stays good for
	// its install.
	emuMoreCoresPlanFresh = 30 * time.Minute
	// emuMoreCoresTimeout bounds one press: a dozen cores on handheld Wi-Fi.
	emuMoreCoresTimeout = 45 * time.Minute
)

// emuMoreCoreSuggestions are the cores worth having for the consoles the
// ranking does not rank, best first, by library name (romSystemAliases). Only
// a core emuCoreChoicePlays recognises for the console is used: one it does
// not would never be offered when a game starts.
var emuMoreCoreSuggestions = map[string][]string{
	"Master System":        {"genesis_plus_gx", "picodrive", "gearsystem", "smsplus"},
	"Game Gear":            {"genesis_plus_gx", "gearsystem"},
	"Sega CD":              {"genesis_plus_gx", "picodrive"},
	"32X":                  {"picodrive"},
	"PC Engine":            {"mednafen_pce_fast", "mednafen_pce"},
	"Arcade":               {"fbneo", "mame2003_plus"},
	"Neo Geo":              {"fbneo"},
	"Neo Geo CD":           {"neocd"},
	"Neo Geo Pocket":       {"mednafen_ngp", "race"},
	"Neo Geo Pocket Color": {"mednafen_ngp", "race"},
	"WonderSwan":           {"mednafen_wswan"},
	"WonderSwan Color":     {"mednafen_wswan"},
	"Atari 2600":           {"stella2014", "stella"},
	"Atari 5200":           {"a5200"},
	"Atari 7800":           {"prosystem"},
	"Atari Lynx":           {"handy", "mednafen_lynx"},
	"Atari Jaguar":         {"virtualjaguar"},
	"DOS":                  {"dosbox_pure"},
	"ScummVM":              {"scummvm"},
	"TIC-80":               {"tic80"},
	"EasyRPG":              {"easyrpg"},
}

// emuMoreCoreCandidates is the cores worth having for system, best first: the
// ranking's RetroArch cores, then the suggestions.
func emuMoreCoreCandidates(system string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(core string) {
		core = normalizeCoreName(core)
		if core == "" || seen[core] || !emuCoreChoicePlays(core, system) {
			return
		}
		seen[core] = true
		out = append(out, core)
	}
	for _, c := range emuBestCandidatesFor(system) {
		if c.Kind == emuBestCore {
			add(c.Core)
		}
	}
	for _, core := range emuMoreCoreSuggestions[strings.TrimSpace(system)] {
		add(core)
	}
	return out
}

// emuMoreCoresSupported reports whether this device fetches more cores: the
// handheld, where an installed core is a choice a game can start in.
func emuMoreCoresSupported() bool {
	return IsTrimuiDevice() && emuCoreInstallSupported()
}

// emuMoreCoresSystemsWithGames lists, sorted, the consoles that have a game on
// the card and cores worth having. It reads the Roms folders (the first
// entries of each, emuBestFolderHasFile): off the main thread.
func emuMoreCoresSystemsWithGames() []string {
	found := map[string]bool{}
	for _, root := range romLibraryRoots() {
		dirs, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sys, ok := romSystemAliases[strings.ToLower(d.Name())]
			if !ok || found[sys.Name] {
				continue
			}
			if _, desktopOnly := emuDesktopSystemFor(sys.Name); desktopOnly {
				continue
			}
			if len(emuMoreCoreCandidates(sys.Name)) == 0 {
				continue
			}
			if emuBestFolderHasFile(filepath.Join(root, d.Name())) {
				found[sys.Name] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for s := range found {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ── The plan ─────────────────────────────────────────────────────────────

// emuMoreCorePick is one core to fetch, for the console it was picked for.
type emuMoreCorePick struct {
	System string
	Core   string
	// GPU: the core draws with the GPU (fetched only because the console has
	// no emulator at all), which the summary says.
	GPU bool
}

// emuMoreCorePlan is what one run fetches.
type emuMoreCorePlan struct {
	// Systems are the consoles the plan looked at.
	Systems []string
	Picks   []emuMoreCorePick
	// Held names the GPU-drawn cores left for a press on the Emulators page.
	Held []string
}

// emuMoreCoresPlanFor works out the cores to fetch for systems. index nil
// means the buildbot's catalog is not known yet and every candidate counts (a
// number for a row, never a download). installed is the set of cores the card
// has; hasEmulator reports whether a console plays already without them.
func emuMoreCoresPlanFor(systems []string, index []coreIndexEntry, installed map[string]bool, hasEmulator func(system string) bool) emuMoreCorePlan {
	plan := emuMoreCorePlan{Systems: systems}
	published := make(map[string]coreIndexEntry, len(index))
	for _, e := range index {
		published[e.Core] = e
	}
	taken := map[string]bool{}
	held := map[string]bool{}
	for _, system := range systems {
		plays := hasEmulator != nil && hasEmulator(system)
		for _, core := range emuMoreCoreCandidates(system) {
			if installed[core] || taken[core] {
				continue
			}
			if index != nil {
				entry, ok := published[core]
				if !ok {
					continue
				}
				if _, refused := coreBuildRefused(core, entry.CRC32); refused {
					continue
				}
			}
			gpu := coreNeedsGPU(core)
			if gpu && plays {
				if !held[core] {
					held[core] = true
					plan.Held = append(plan.Held, coreConfigDisplayName(core))
				}
				continue
			}
			taken[core] = true
			plan.Picks = append(plan.Picks, emuMoreCorePick{System: system, Core: core, GPU: gpu})
		}
	}
	return plan
}

// emuMoreCoresActiveSet is the cores RetroArch has, from an inventory.
func emuMoreCoresActiveSet(inv coreInventory) map[string]bool {
	out := make(map[string]bool, len(inv.Active))
	for core := range inv.Active {
		out[normalizeCoreName(core)] = true
	}
	return out
}

// emuMoreCoresHasEmulator is hasEmulator for a catalog: a console plays when
// any emulator starts its games — its own launchers, their alternatives, an
// installed core (findGameLaunchers). Safe off the main thread.
func emuMoreCoresHasEmulator(entries []emuEntry) func(string) bool {
	return func(system string) bool { return len(findGameLaunchers(system, entries)) > 0 }
}

// emuMoreCoresAvailableFor counts the cores a press would fetch for system,
// from what is known without the network: the core rows of the emulator
// catalog (built already wherever this is asked: the picker and the game
// options list the console's emulators from it) and the buildbot's catalog
// when a check has read it (emuPublishedCores; before that every candidate
// counts, and the press says when the buildbot has none). plays: the console
// has an emulator already. Main thread.
func emuMoreCoresAvailableFor(system string, plays bool) int {
	if !emuMoreCoresSupported() || strings.TrimSpace(system) == "" {
		return 0
	}
	installed := map[string]bool{}
	for _, e := range emulatorEntries() {
		if e.CorePath != "" {
			installed[normalizeCoreName(e.Core)] = true
		}
	}
	index := emuPublishedCores()
	if len(index) == 0 {
		index = nil
	}
	plan := emuMoreCoresPlanFor([]string{system}, index, installed, func(string) bool { return plays })
	return len(plan.Picks)
}

// ── What was done ────────────────────────────────────────────────────────

// emuMoreCoresDone is one console's record: when its cores were fetched, and
// which ones a run installed.
type emuMoreCoresDone struct {
	At    string   `json:"at,omitempty"`
	Cores []string `json:"cores,omitempty"`
}

// emuMoreCoresFile is emu-morecores.json.
type emuMoreCoresFile struct {
	Schema   int                         `json:"schema"`
	Consoles map[string]emuMoreCoresDone `json:"consoles,omitempty"`
}

// emuMoreCoresState is the record and the session's bookkeeping.
var emuMoreCoresState struct {
	mu     sync.Mutex
	loaded bool
	path   string
	file   emuMoreCoresFile
	// plan and index are the daily pass's check, kept for its install.
	plan   emuMoreCorePlan
	index  []coreIndexEntry
	planAt time.Time
	// running: a run is fetching (a press or the pass); a second waits.
	running bool
}

// emuMoreCoresStatePath is where the record lives; a variable so a test can
// use a temporary folder. "" keeps it in memory only.
var emuMoreCoresStatePath = func() string {
	dir, err := PatchStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, emuMoreCoresStateName)
}

// emuMoreCoresLoadLocked reads the record the first time it is needed. A
// missing or unreadable file is an empty record: every console is then due,
// and a console whose cores are all here is recorded again without a download.
func emuMoreCoresLoadLocked() {
	if emuMoreCoresState.loaded {
		return
	}
	emuMoreCoresState.loaded = true
	emuMoreCoresState.path = emuMoreCoresStatePath()
	emuMoreCoresState.file = emuMoreCoresFile{Schema: 1, Consoles: map[string]emuMoreCoresDone{}}
	if emuMoreCoresState.path == "" {
		return
	}
	data, err := os.ReadFile(emuMoreCoresState.path)
	if err != nil || len(data) > 1<<20 {
		return
	}
	var f emuMoreCoresFile
	if json.Unmarshal(data, &f) != nil || f.Schema != 1 {
		return
	}
	if f.Consoles == nil {
		f.Consoles = map[string]emuMoreCoresDone{}
	}
	emuMoreCoresState.file = f
}

// emuMoreCoresSaveLocked writes the record.
func emuMoreCoresSaveLocked() {
	if emuMoreCoresState.path == "" {
		return
	}
	data, err := json.MarshalIndent(emuMoreCoresState.file, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(emuMoreCoresState.path, append(data, '\n'), 0o644); err != nil {
		logWarn("emulators", "could not save %s: %v", emuMoreCoresStateName, err)
	}
}

// emuMoreCoresPending is systems less the consoles done before.
func emuMoreCoresPending(systems []string) []string {
	emuMoreCoresState.mu.Lock()
	defer emuMoreCoresState.mu.Unlock()
	emuMoreCoresLoadLocked()
	var out []string
	for _, s := range systems {
		if _, done := emuMoreCoresState.file.Consoles[s]; !done {
			out = append(out, s)
		}
	}
	return out
}

// emuMoreCoresMarkDone records systems as done, with the cores each got. The
// stamp is for a reader of the file only: nothing compares it (the handheld's
// clock can read 1970).
func emuMoreCoresMarkDone(systems []string, got map[string][]string) {
	if len(systems) == 0 {
		return
	}
	emuMoreCoresState.mu.Lock()
	defer emuMoreCoresState.mu.Unlock()
	emuMoreCoresLoadLocked()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range systems {
		rec := emuMoreCoresState.file.Consoles[s]
		rec.At = now
		for _, core := range got[s] {
			known := false
			for _, c := range rec.Cores {
				if c == core {
					known = true
					break
				}
			}
			if !known {
				rec.Cores = append(rec.Cores, core)
			}
		}
		emuMoreCoresState.file.Consoles[s] = rec
	}
	emuMoreCoresSaveLocked()
}

// emuMoreCoresBegin claims the one run; false when one is going.
func emuMoreCoresBegin() bool {
	emuMoreCoresState.mu.Lock()
	defer emuMoreCoresState.mu.Unlock()
	if emuMoreCoresState.running {
		return false
	}
	emuMoreCoresState.running = true
	return true
}

// emuMoreCoresEnd releases it.
func emuMoreCoresEnd() {
	emuMoreCoresState.mu.Lock()
	emuMoreCoresState.running = false
	emuMoreCoresState.mu.Unlock()
}

// emuMoreCoresBusy reports a run going.
func emuMoreCoresBusy() bool {
	emuMoreCoresState.mu.Lock()
	defer emuMoreCoresState.mu.Unlock()
	return emuMoreCoresState.running
}

// ── Running a plan ───────────────────────────────────────────────────────

// emuMoreCoresReport is what one run did.
type emuMoreCoresReport struct {
	// Installed names the cores installed ("ParaLLEl N64"); Got lists them by
	// console, by core name, for the record.
	Installed []string
	Got       map[string][]string
	// Settled are the consoles whose every pick was installed, refused or
	// found installed already: the ones the record may call done.
	Settled []string
	// Refused names builds this device will not take (too large, or unable
	// to load here); RefusedNote is the first one's reason.
	Refused     []string
	RefusedNote string
	Errors      []string
	// LowSpace is the free space a run stopped at ("" when it did not).
	LowSpace    string
	Interrupted bool
	// Held: GPU-drawn cores left for the Emulators page; GPUAdded: ones
	// fetched for a console with no emulator, which may not run.
	Held     []string
	GPUAdded []string
}

// emuMoreCoresRun installs plan's picks one after another, through the
// Emulators page's install. progress (may be nil) gets one line per core. A
// stop (ctx) ends the run between cores and is reported as Interrupted.
func emuMoreCoresRun(ctx context.Context, client *http.Client, plan emuMoreCorePlan, index []coreIndexEntry, progress func(line string)) emuMoreCoresReport {
	report := emuMoreCoresReport{Got: map[string][]string{}, Held: plan.Held}
	settled := make([]bool, len(plan.Picks))
	inv := scanCoreInventory()
	pool := compatCorePoolDir()
	// Where a new core is written: the pool, and the cores folder RetroArch
	// loads it from — coreActivationTarget's, the first one not on the
	// firmware's read-only partition (deviceRetroArchCoresDir may be that
	// partition, or a folder that is not there).
	spaceDirs := []string{pool}
	for _, dir := range retroArchCoreDirs() {
		if !coreDirIsSystem(dir) {
			spaceDirs = append(spaceDirs, dir)
			break
		}
	}
	for i, pick := range plan.Picks {
		if ctx.Err() != nil {
			report.Interrupted = true
			break
		}
		// A new core is written twice (the pool copy and the cores folder):
		// the card keeps the floor a core update keeps, so the next save can
		// still be written.
		if free, ok := emuMoreCoresLeastFree(spaceDirs); ok && free < coreUpdateFreeFloor("") {
			report.LowSpace = bootFormatBytes(free)
			logWarn("emulators", "more cores: stopped before %s, only %s free on the card", pick.Core, report.LowSpace)
			break
		}
		name := coreConfigDisplayName(pick.Core)
		if progress != nil {
			progress(fmt.Sprintf("%s for %s (%d/%d)", name, pick.System, i+1, len(plan.Picks)))
		}
		p := planEmuInstall(emuEntry{Name: name, Core: pick.Core}, inv, index)
		if p.Action != emuInstallNew {
			// Installed in the meantime (a press on the Emulators page), or
			// gone from the index: nothing to fetch.
			settled[i] = true
			continue
		}
		build, _, err := applyEmuInstallPlan(ctx, client, p, pool)
		if err != nil {
			var refused *coreRefusedError
			switch {
			case ctx.Err() != nil:
				report.Interrupted = true
			case errors.As(err, &refused):
				settled[i] = true
				report.Refused = append(report.Refused, name)
				if report.RefusedNote == "" {
					report.RefusedNote = err.Error()
				}
			default:
				report.Errors = append(report.Errors, name+": "+emuFriendlyError(err))
				logWarn("emulators", "more cores: %s for %s failed: %v", pick.Core, pick.System, err)
			}
			if report.Interrupted {
				break
			}
			continue
		}
		settled[i] = true
		if !coreKeepActivatedInPool() {
			// The build is the active file now: on the handheld a pool copy
			// would be a second copy of every core fetched.
			if err := coreDropPoolBuild(pool, build); err != nil {
				logDebug("emulators", "kept the pool copy of %s %s: %v", pick.Core, build.Version, err)
			}
		}
		report.Installed = append(report.Installed, name)
		report.Got[pick.System] = append(report.Got[pick.System], pick.Core)
		if pick.GPU {
			report.GPUAdded = append(report.GPUAdded, name)
		}
		logInfo("emulators", "more cores: installed %s (%s) for %s", pick.Core, build.Version, pick.System)
	}
	open := map[string]bool{}
	for i, pick := range plan.Picks {
		if !settled[i] {
			open[pick.System] = true
		}
	}
	for _, s := range plan.Systems {
		if !open[s] {
			report.Settled = append(report.Settled, s)
		}
	}
	if len(report.Installed) > 0 {
		// The catalog (and with it the games' choices), the compatibility
		// panel and the Core builds page all read the cores folder.
		invalidateEmulatorCache()
		invalidateCompatCache()
		coreBuildsRequestReload()
	}
	emuMoreCoresMarkDone(report.Settled, report.Got)
	return report
}

// emuMoreCoresLeastFree is the least free space of dirs, the ones that can be
// read; ok is false when none can.
func emuMoreCoresLeastFree(dirs []string) (least int64, ok bool) {
	for _, dir := range dirs {
		free, err := freeBytes(dir)
		if err != nil {
			continue
		}
		if !ok || free < least {
			least, ok = free, true
		}
	}
	return least, ok
}

// summary is the run's one toast.
func (r emuMoreCoresReport) summary() string {
	var parts []string
	switch n := len(r.Installed); {
	case n == 1:
		parts = append(parts, "Installed the "+r.Installed[0]+" core")
	case n > 1:
		parts = append(parts, fmt.Sprintf("Installed %d cores (%s)", n, emuRuntimeList(r.Installed)))
	}
	if note := r.notes(); note != "" {
		parts = append(parts, note)
	}
	if len(r.Installed) > 0 {
		// True whatever "Ask which emulator" says (Never asks nothing), and
		// read off the main thread, where that setting is not.
		parts = append(parts, "Game options → Emulator lists them")
	}
	if len(parts) == 0 {
		return "Every core worth having for your consoles is here already"
	}
	return capitalizeFirst(strings.Join(parts, " · "))
}

// notes is what the run has to say beyond what it installed: refusals,
// failures, a full card, and the GPU-drawn cores.
func (r emuMoreCoresReport) notes() string {
	var parts []string
	switch {
	case len(r.Refused) == 1 && r.RefusedNote != "":
		parts = append(parts, r.Refused[0]+": "+r.RefusedNote)
	case len(r.Refused) > 1:
		parts = append(parts, fmt.Sprintf("%d cannot be used here (%s)", len(r.Refused), emuRuntimeList(r.Refused)))
	}
	switch len(r.Errors) {
	case 0:
	case 1:
		parts = append(parts, r.Errors[0])
	default:
		parts = append(parts, fmt.Sprintf("%d failed · tried again on the next press", len(r.Errors)))
	}
	if r.LowSpace != "" {
		parts = append(parts, coreLowSpaceNote(r.LowSpace))
	} else if r.Interrupted {
		parts = append(parts, "stopped early")
	}
	if len(r.GPUAdded) > 0 {
		parts = append(parts, emuRuntimeList(r.GPUAdded)+" draw with the GPU and may not run here")
	}
	if len(r.Held) > 0 {
		parts = append(parts, emuRuntimeList(r.Held)+" draw with the GPU: one press each on the Emulators page")
	}
	return strings.Join(parts, " · ")
}

// ── The press ────────────────────────────────────────────────────────────

// emuMoreCoresStart is the press — Settings, the Emulators page's tile, the
// picker's last row, X on the game options' Emulator row: system "" fetches
// for every console with games, a console's name for that one. It runs off
// the event thread and reports through the install bar and one toast, as the
// Emulators page's installs do. Main thread.
func emuMoreCoresStart(system string) {
	system = strings.TrimSpace(system)
	if !emuMoreCoresSupported() {
		ShowToast("More cores are fetched on the handheld", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to get more cores", ToastKindWarning)
		return
	}
	if !emuMoreCoresBegin() {
		ShowToast("More cores are being fetched already", ToastKindInfo)
		return
	}
	if !tryBeginPatchOp() {
		emuMoreCoresEnd()
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	label := "Getting more emulator cores"
	if system != "" {
		label = "Getting more cores for " + system
	}
	installBarBeginLabelled("Emulators", label)
	installBarPhase(installPhaseResolving)
	PlayActivateSound()
	logInfo("emulators", "more cores: fetching for %s", setOr(system, "every console with games"))
	go func() {
		defer endPatchOp()
		defer emuMoreCoresEnd()
		// The deadline derives from the card's run token, so a cancel from the
		// card stops the transfer mid-flight.
		ctx, cancel := context.WithTimeout(installBarRunContext(), emuMoreCoresTimeout)
		defer cancel()
		client := newEmulatorUpdateClient()
		index, err := fetchCoreIndex(ctx, client)
		if err != nil {
			installBarFail(installCardDetail(emuFriendlyError(err)))
			ShowToast("Core index: "+emuFriendlyError(err), ToastKindError)
			return
		}
		// The catalog learns what the buildbot publishes: without a check of
		// the Emulators page this session, the picker's row and the game
		// options' X count every candidate (emuMoreCoresAvailableFor), and a
		// core the buildbot does not publish would keep offering a press that
		// fetches nothing.
		emuNoteCoreCheck(index, nil, nil, nil)
		systems := []string{system}
		if system == "" {
			systems = emuMoreCoresSystemsWithGames()
		}
		plan := emuMoreCoresPlanFor(systems, index, emuMoreCoresActiveSet(scanCoreInventory()), emuMoreCoresHasEmulator(emulatorEntries()))
		if len(plan.Picks) == 0 {
			installBarPhase(installPhaseDone)
			installBarDone()
			emuMoreCoresMarkDone(plan.Systems, nil)
			msg := "Every core worth having for your consoles is here already"
			switch {
			case len(systems) == 0:
				msg = "No games on the card yet · add some and press again"
			case system != "":
				msg = "No other cores for " + system + " are published for this device"
			}
			if len(plan.Held) > 0 {
				msg += " · " + emuRuntimeList(plan.Held) + " draw with the GPU: one press each on the Emulators page"
			}
			ShowToast(msg, ToastKindInfo)
			return
		}
		installBarPhase(installPhaseDownload)
		report := emuMoreCoresRun(ctx, client, plan, index, func(line string) { installBarDetail(line) })
		msg := report.summary()
		if len(report.Installed) == 0 && (len(report.Errors) > 0 || report.LowSpace != "") {
			installBarFail(installCardDetail(msg))
			ShowToast(msg, ToastKindWarning)
			return
		}
		installBarPhase(installPhaseDone)
		installBarDone()
		kind := ToastKindSuccess
		if len(report.Installed) == 0 || len(report.Errors) > 0 || report.LowSpace != "" || report.Interrupted {
			kind = ToastKindWarning
		}
		if len(report.Installed) > 0 {
			PlaySuccessSound()
		}
		ShowToast(msg, kind)
	}()
}

// emuMoreCoresEmuEntry is the Emulators page's pinned tile.
func emuMoreCoresEmuEntry() emuEntry {
	return emuEntry{
		Name:   emuMoreCoresEntryName,
		Detail: "The other RetroArch cores for every console with games · A downloads them",
		Status: emuAvailable,
		Pinned: true,
	}
}

// emuMoreCoresSettingsRow is the action in Settings → Games & emulators.
func emuMoreCoresSettingsRow() setRow {
	return setRow{
		ID: "moreCores", Label: "Get more cores", Kind: setKindAction,
		Detail: func(*Config) string {
			if emuCoreUpdateModeNow() == coreUpdateInstall {
				return "Other RetroArch cores for your consoles · Install automatically fetches them once"
			}
			return "Other RetroArch cores for every console with games"
		},
		Visible: func(*Config) bool { return emuMoreCoresSupported() },
		Open:    func(*Config) { emuMoreCoresStart("") },
	}
}

// ── The daily pass ───────────────────────────────────────────────────────

// emuMoreCoresAutoPlan is the daily pass's plan: the consoles with games that
// were not done before, and the cores to fetch for them. A console that needs
// nothing is recorded as done here.
func emuMoreCoresAutoPlan(ctx context.Context) (emuMoreCorePlan, []coreIndexEntry, error) {
	pending := emuMoreCoresPending(emuMoreCoresSystemsWithGames())
	if len(pending) == 0 {
		return emuMoreCorePlan{}, nil, nil
	}
	installed := emuMoreCoresActiveSet(scanCoreInventory())
	plays := emuMoreCoresHasEmulator(emulatorEntries())
	// Before the network: consoles whose cores are all here are done.
	if local := emuMoreCoresPlanFor(pending, nil, installed, plays); len(local.Picks) == 0 {
		emuMoreCoresMarkDone(pending, nil)
		return emuMoreCorePlan{}, nil, nil
	}
	// The buildbot is not GitHub: the pass's client changes only how GitHub is
	// asked, and core downloads keep their own redirect rule.
	index, err := fetchCoreIndex(ctx, newEmulatorUpdateClient())
	if err != nil {
		return emuMoreCorePlan{}, nil, err
	}
	emuNoteCoreCheck(index, nil, nil, nil) // as the press does (emuMoreCoresStart)
	plan := emuMoreCoresPlanFor(pending, index, installed, plays)
	if len(plan.Picks) == 0 {
		emuMoreCoresMarkDone(pending, nil)
		logPatch("more cores: nothing to fetch for %s", strings.Join(pending, ", "))
	}
	return plan, index, nil
}

// emuMoreCoresLabel names a plan in the pass's toast: "(4 for Saturn, SNES)".
func emuMoreCoresLabel(plan emuMoreCorePlan) string {
	var systems []string
	seen := map[string]bool{}
	for _, p := range plan.Picks {
		if !seen[p.System] {
			seen[p.System] = true
			systems = append(systems, p.System)
		}
	}
	return fmt.Sprintf("(%d for %s)", len(plan.Picks), emuRuntimeList(systems))
}

// emuMoreCoresCheck is the updater's Check: is there anything to fetch.
func emuMoreCoresCheck(ctx context.Context, _ *http.Client) (bool, string, error) {
	plan, index, err := emuMoreCoresAutoPlan(ctx)
	if err != nil || len(plan.Picks) == 0 {
		return false, "", err
	}
	emuMoreCoresState.mu.Lock()
	emuMoreCoresState.plan, emuMoreCoresState.index, emuMoreCoresState.planAt = plan, index, time.Now()
	emuMoreCoresState.mu.Unlock()
	return true, emuMoreCoresLabel(plan), nil
}

// emuMoreCoresInstall is the updater's Install: the check's plan when it is
// fresh, else a new one.
func emuMoreCoresInstall(ctx context.Context, _ *http.Client, progress func(line string)) error {
	emuMoreCoresState.mu.Lock()
	plan, index, at := emuMoreCoresState.plan, emuMoreCoresState.index, emuMoreCoresState.planAt
	emuMoreCoresState.plan, emuMoreCoresState.index, emuMoreCoresState.planAt = emuMoreCorePlan{}, nil, time.Time{}
	emuMoreCoresState.mu.Unlock()
	if at.IsZero() || time.Since(at) > emuMoreCoresPlanFresh || len(plan.Picks) == 0 {
		var err error
		if plan, index, err = emuMoreCoresAutoPlan(ctx); err != nil {
			return err
		}
	}
	if len(plan.Picks) == 0 {
		return errEmuUpdaterCurrent
	}
	if !emuMoreCoresBegin() {
		// A press is fetching them right now: nothing failed.
		return errRelPackBusy
	}
	defer emuMoreCoresEnd()
	emuRuntimeCardFrom(ctx).phase(installPhaseDownload)
	report := emuMoreCoresRun(ctx, newEmulatorUpdateClient(), plan, index, progress)
	if note := report.notes(); note != "" {
		emuRuntimeLeaveNote(note)
	}
	switch {
	case len(report.Installed) > 0:
		return nil
	case report.Interrupted && ctx.Err() != nil:
		return ctx.Err()
	case len(report.Errors) > 0:
		return errors.New(report.Errors[0])
	case report.LowSpace != "":
		return errors.New(coreLowSpaceNote(report.LowSpace))
	}
	return errEmuUpdaterCurrent
}

// The daily pass fetches the recommended missing cores once per console, only
// in Install automatically (Notify me would announce downloads that are not
// updates of anything), and not while a press is fetching them.
func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:   "morecores",
		Title: "extra cores",
		Supported: func() bool {
			return emuMoreCoresSupported() && emuCoreUpdateModeNow() == coreUpdateInstall
		},
		Installed: func() bool { return true },
		InUse:     emuMoreCoresBusy,
		Check:     emuMoreCoresCheck,
		Install:   emuMoreCoresInstall,
	})
}
