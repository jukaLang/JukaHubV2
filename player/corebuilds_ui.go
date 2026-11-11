package main

// corebuilds_ui.go — the Core builds page.
//
// Two lists behind one page, both loaded the moment it opens (no Refresh to
// press first):
//
//   - Installed: every libretro core RetroArch can load, with the system it
//     runs, its build date (the nightly it came from, when the pool knows it;
//     otherwise the day it was installed), its size and how many replaced
//     builds are kept beside it. A background check against this platform's
//     buildbot channel (emuupdatecheck.go) badges each one "Up to date" or
//     "Update · <date>"; A updates one core, X rolls it back to its newest kept
//     build, and the header says whether the handheld updates cores by itself
//     (Start switches it) and when it last looked.
//   - Kept builds: every binary an update replaced, as before — the date it was
//     replaced and its size — to restore (A) or delete (X, twice).
//
// Both lists can be narrowed to one family of systems (Nintendo, PlayStation,
// Sega, Arcade, Other) with the chips under the header, L1/R1 or Left/Right.
// Offline, the page still lists what is on the card and says plainly that the
// update check needs a connection.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// coreBuildsConfirmWindow is how long the first X press stays armed.
	// Deleting a kept build is the one action here that cannot be undone, so it
	// takes two presses — the same rule the Wi-Fi picker applies to forgetting
	// a network.
	coreBuildsConfirmWindow = 6 * time.Second
	// coreBuildsRowH is the smallest row: a name, then one line of detail.
	coreBuildsRowH = int32(66)
	// coreBuildsCheckAge is how old the update answer may be when the page
	// opens before it asks again.
	coreBuildsCheckAge = 15 * time.Minute
)

// The page's two lists.
const (
	coreBuildsTabCores = 0
	coreBuildsTabKept  = 1
)

// coreBuildsCore is one installed core on the Installed list.
type coreBuildsCore struct {
	Core   string
	Name   string
	System string
	Family string
	Path   string
	Size   int64
	// Date is the build's own date when the core pool knows it (Built), and
	// the day the file was installed otherwise.
	Date  string
	Built bool
	Kept  int
}

type coreBuildsPanelState struct {
	open   bool
	cursor int
	scroll int
	// tab is the list on screen; filter narrows it to one family ("" is all).
	tab    int
	filter string
	// rows are the kept builds, newest first, and total their size.
	rows  []coreBackup
	total int64
	// cores are the installed cores and coresTotal their size.
	cores      []coreBuildsCore
	coresTotal int64
	// confirm is the path armed by the first X press; confirmAt is when that
	// press happened, so the arm expires on its own.
	confirm   string
	confirmAt time.Time
	// returnEmu re-opens the Emulators page on close, so the panel behaves like
	// a child of the page that launched it.
	returnEmu bool
	// keptFromCores sends B on the Kept list back to the Installed list when
	// that is where it was opened from.
	keptFromCores bool
}

var kbPanel coreBuildsPanelState

type coreBuildsRowHit struct {
	index int
	rect  sdl.Rect
}

// coreBuildsChipHit is one clickable chip above the list: a tab (tab >= 0) or a
// family filter.
type coreBuildsChipHit struct {
	tab    int
	family string
	rect   sdl.Rect
}

// coreBuildsActionHit is one action button on the focused row.
type coreBuildsActionHit struct {
	button string
	rect   sdl.Rect
}

// coreBuildsRowHits and coreBuildsPanelRect are filled by
// renderCoreBuildsPanel: the visible row rects and the panel rect; the chip,
// action and hint hits likewise. Only touched on the render/event thread.
var (
	coreBuildsRowHits    []coreBuildsRowHit
	coreBuildsPanelRect  sdl.Rect
	coreBuildsChipHits   []coreBuildsChipHit
	coreBuildsActionHits []coreBuildsActionHit
	coreBuildsHintHits   []emuHintHit
)

// coreBuildsReload is set by a background install that changed a core, so the
// open page re-reads the card on its next frame.
var coreBuildsReload atomic.Bool

// coreBuildsRequestReload asks the page to re-read the card (any goroutine).
func coreBuildsRequestReload() {
	coreBuildsReload.Store(true)
}

// ── Opening and data ───────────────────────────────────────────────────────

// openCoreBuildsPanel shows the page on its Installed list — or on the Kept
// list when the card has kept builds but no core, the only thing there is to
// look at then. returnEmu puts the Emulators page back when the panel closes,
// the way the compatibility panel does.
func openCoreBuildsPanel(returnEmu bool) {
	openCoreBuildsPanelOn(-1, returnEmu)
}

// openCoreBuildsPanelOn opens the page on a given list (-1 picks as above).
func openCoreBuildsPanelOn(tab int, returnEmu bool) {
	// Never stack modals: this panel replaces the page that opened it.
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	emuCloseXboxSetup()
	cpPanel.open = false
	kbPanel = coreBuildsPanelState{open: true, returnEmu: returnEmu}
	coreBuildsPanelRect = sdl.Rect{}
	coreBuildsRowHits = nil
	coreBuildsChipHits = nil
	coreBuildsActionHits = nil
	coreBuildsHintHits = nil
	coreBuildsReload.Store(false)
	coreBuildsRefresh()
	switch {
	case tab == coreBuildsTabCores || tab == coreBuildsTabKept:
		kbPanel.tab = tab
	case len(kbPanel.cores) == 0 && len(kbPanel.rows) > 0:
		kbPanel.tab = coreBuildsTabKept
	}
	// The update badges come from the shared background check; a page whose
	// whole point is that answer asks again when it is more than a few
	// minutes old.
	emuStartUpdateCheck(coreBuildsCheckAge)
	PlayActivateSound()
}

func closeCoreBuildsPanel() {
	if !kbPanel.open {
		return
	}
	returnEmu := kbPanel.returnEmu
	kbPanel = coreBuildsPanelState{}
	coreBuildsPanelRect = sdl.Rect{}
	coreBuildsRowHits = nil
	coreBuildsChipHits = nil
	coreBuildsActionHits = nil
	coreBuildsHintHits = nil
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

// coreBuildsLoadCores lists the installed cores with what the page shows for
// each: system, build date, size and kept builds.
func coreBuildsLoadCores(backups []coreBackup) ([]coreBuildsCore, int64) {
	kept := map[string]int{}
	for _, b := range backups {
		kept[b.Core]++
	}
	inv := scanCoreInventory()
	pool := compatCorePoolDir()
	out := make([]coreBuildsCore, 0, len(inv.Active))
	var total int64
	for _, build := range inv.Active {
		system, family := emuCoreInfo(build.Core)
		if family == "" {
			family = emuFamilyOther
		}
		row := coreBuildsCore{
			Core:   build.Core,
			Name:   coreConfigDisplayName(build.Core),
			System: system,
			Family: family,
			Path:   build.Path,
			Size:   build.Size,
			Kept:   kept[build.Core],
		}
		row.Date, row.Built = coreBuildsDateFor(build, pool)
		total += build.Size
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, total
}

// coreBuildsDateFor dates an active core: the newest pool build of the same
// size is the build it was activated from, and its folder is the nightly's
// date; otherwise the file's own modification day.
func coreBuildsDateFor(build coreBuild, pool string) (string, bool) {
	if pool != "" && build.Size > 0 {
		if entries, err := os.ReadDir(filepath.Join(pool, build.Core)); err == nil {
			dates := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() && validCoreIndexDate(e.Name()) {
					dates = append(dates, e.Name())
				}
			}
			sort.Sort(sort.Reverse(sort.StringSlice(dates)))
			for _, date := range dates {
				for _, name := range []string{build.Core + "_libretro.so", build.Core + "_libretro.dll"} {
					if info, err := os.Stat(filepath.Join(pool, build.Core, date, name)); err == nil && info.Size() == build.Size {
						return date, true
					}
				}
			}
		}
	}
	if info, err := os.Stat(build.Path); err == nil {
		return info.ModTime().Format("2006-01-02"), false
	}
	return "", false
}

// coreBuildsRefresh re-reads the card. It runs on open, after every action that
// changed a file, and when a background install asks for it: the page's whole
// value is that it lists what is on the card now.
func coreBuildsRefresh() {
	kbPanel.rows = scanCoreBackups()
	kbPanel.total = coreBackupsTotal(kbPanel.rows)
	kbPanel.cores, kbPanel.coresTotal = coreBuildsLoadCores(kbPanel.rows)
	kbPanel.confirm = ""
	if kbPanel.filter != "" {
		found := false
		for _, f := range coreBuildsFamilies() {
			if f == kbPanel.filter {
				found = true
			}
		}
		if !found {
			kbPanel.filter = ""
		}
	}
	coreBuildsClampCursor()
}

func coreBuildsClampCursor() {
	n := coreBuildsListLen()
	if kbPanel.cursor >= n {
		kbPanel.cursor = n - 1
	}
	if kbPanel.cursor < 0 {
		kbPanel.cursor = 0
	}
	if kbPanel.scroll > kbPanel.cursor {
		kbPanel.scroll = kbPanel.cursor
	}
}

// coreBuildsVisibleCores is the Installed list under the family filter.
func coreBuildsVisibleCores() []coreBuildsCore {
	if kbPanel.filter == "" {
		return kbPanel.cores
	}
	out := make([]coreBuildsCore, 0, len(kbPanel.cores))
	for _, c := range kbPanel.cores {
		if c.Family == kbPanel.filter {
			out = append(out, c)
		}
	}
	return out
}

// coreBuildsVisibleKept is the Kept list under the family filter.
func coreBuildsVisibleKept() []coreBackup {
	if kbPanel.filter == "" {
		return kbPanel.rows
	}
	out := make([]coreBackup, 0, len(kbPanel.rows))
	for _, b := range kbPanel.rows {
		if emuCoreFamily(b.Core) == kbPanel.filter {
			out = append(out, b)
		}
	}
	return out
}

// coreBuildsListLen is the length of the list on screen.
func coreBuildsListLen() int {
	if kbPanel.tab == coreBuildsTabKept {
		return len(coreBuildsVisibleKept())
	}
	return len(coreBuildsVisibleCores())
}

// coreBuildsFamilies lists the families the card's cores and kept builds
// belong to, in chip order.
func coreBuildsFamilies() []string {
	present := map[string]bool{}
	for _, c := range kbPanel.cores {
		present[c.Family] = true
	}
	for _, b := range kbPanel.rows {
		present[emuCoreFamily(b.Core)] = true
	}
	out := make([]string, 0, len(emuFamilies))
	for _, f := range emuFamilies {
		if present[f] {
			out = append(out, f)
		}
	}
	return out
}

func coreBuildsSelected() (coreBackup, bool) {
	if kbPanel.tab != coreBuildsTabKept {
		return coreBackup{}, false
	}
	rows := coreBuildsVisibleKept()
	if kbPanel.cursor < 0 || kbPanel.cursor >= len(rows) {
		return coreBackup{}, false
	}
	return rows[kbPanel.cursor], true
}

// coreBuildsSelectedCore is the highlighted installed core.
func coreBuildsSelectedCore() (coreBuildsCore, bool) {
	if kbPanel.tab != coreBuildsTabCores {
		return coreBuildsCore{}, false
	}
	cores := coreBuildsVisibleCores()
	if kbPanel.cursor < 0 || kbPanel.cursor >= len(cores) {
		return coreBuildsCore{}, false
	}
	return cores[kbPanel.cursor], true
}

func coreBuildsMove(delta int) {
	n := coreBuildsListLen()
	if n == 0 {
		return
	}
	next := kbPanel.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next == kbPanel.cursor {
		return
	}
	kbPanel.cursor = next
	// Moving off a row disarms it: a confirm that survived a cursor move would
	// delete a build the user is no longer looking at.
	kbPanel.confirm = ""
	PlayNavSound()
}

// coreBuildsSetTab switches lists, keeping the filter.
func coreBuildsSetTab(tab int) {
	if tab == kbPanel.tab {
		return
	}
	kbPanel.keptFromCores = tab == coreBuildsTabKept && kbPanel.tab == coreBuildsTabCores
	kbPanel.tab = tab
	kbPanel.cursor = 0
	kbPanel.scroll = 0
	kbPanel.confirm = ""
	PlayNavSound()
}

// coreBuildsShowKeptFor opens the Kept list on a core's own builds.
func coreBuildsShowKeptFor(core string) {
	coreBuildsSetTab(coreBuildsTabKept)
	for i, b := range coreBuildsVisibleKept() {
		if b.Core == core {
			kbPanel.cursor = i
			return
		}
	}
}

// coreBuildsShiftFilter steps the family filter: All, then each family the
// card has.
func coreBuildsShiftFilter(delta int) {
	options := append([]string{""}, coreBuildsFamilies()...)
	if len(options) <= 1 {
		return
	}
	at := 0
	for i, f := range options {
		if f == kbPanel.filter {
			at = i
		}
	}
	at = (at + delta + len(options)) % len(options)
	coreBuildsSetFilter(options[at])
}

func coreBuildsSetFilter(family string) {
	if family == kbPanel.filter {
		return
	}
	kbPanel.filter = family
	kbPanel.cursor = 0
	kbPanel.scroll = 0
	kbPanel.confirm = ""
	PlayNavSound()
}

// emuKeptBuildsHint is the Emulators footer's entry point (R2). It reads the
// same per-frame cache the R1 hint uses, so the page never walks the cores
// folders while drawing, and it appears once there is a core or a kept build to
// look at.
func emuKeptBuildsHint() string {
	emuCorePresence.mu.Lock()
	defer emuCorePresence.mu.Unlock()
	if !emuCorePresence.built {
		emuCorePresenceBuild()
	}
	if len(emuCorePresence.cores) == 0 && len(emuCorePresence.backups) == 0 {
		return ""
	}
	// "Cores" is short on purpose: the Emulators line already carries six groups
	// on the Brick, and a longer word pushes the B hint off its end.
	return "Cores"
}

// ── Actions ────────────────────────────────────────────────────────────────

// coreBuildsActivate is A: restore the highlighted kept build (Kept list), or
// update the highlighted core (Installed list).
func coreBuildsActivate() {
	if kbPanel.tab == coreBuildsTabCores {
		coreBuildsUpdateSelected()
		return
	}
	backup, ok := coreBuildsSelected()
	if !ok {
		ShowToast("No kept builds to restore", ToastKindInfo)
		return
	}
	build, err := rollbackCoreBuild(backup)
	if err != nil {
		ShowToast("Restore failed: "+err.Error(), ToastKindError)
		return
	}
	// The tile list, the compatibility panel, the footer's cache and this list
	// all read the cores folder; refresh them together so no screen shows the
	// picture from before the copy.
	invalidateEmulatorCache()
	invalidateCompatCache()
	invalidateEmuCorePresence()
	emuForgetUpdate(build.Core)
	coreBuildsRefresh()
	PlaySuccessSound()
	ShowToast(coreConfigDisplayName(build.Core)+" restored · "+
		backup.Stamp.Format("2006-01-02 15:04")+" build · the replaced build is kept",
		ToastKindSuccess)
}

// coreBuildsUpdateSelected updates the highlighted core through the same
// installer the Emulators page uses (download, CRC check, keep the replaced
// build); the page re-reads the card when it finishes.
func coreBuildsUpdateSelected() {
	c, ok := coreBuildsSelectedCore()
	if !ok {
		ShowToast("No core selected", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to update "+c.Name, ToastKindWarning)
		return
	}
	startEmulatorInstall(emuEntry{Name: c.Name, Core: c.Core, Status: emuAvailable})
}

// coreBuildsRollbackSelected restores the highlighted core's newest kept
// build.
func coreBuildsRollbackSelected() {
	c, ok := coreBuildsSelectedCore()
	if !ok {
		return
	}
	backup, found := emuNewestCoreBackup(c.Core)
	if !found {
		ShowToast("No previous "+c.Name+" build is kept — one is kept after the next update", ToastKindInfo)
		return
	}
	build, err := rollbackCoreBuild(backup)
	if err != nil {
		ShowToast(c.Name+": "+err.Error(), ToastKindError)
		return
	}
	invalidateEmulatorCache()
	invalidateCompatCache()
	invalidateEmuCorePresence()
	emuForgetUpdate(build.Core)
	coreBuildsRefresh()
	PlaySuccessSound()
	ShowToast(c.Name+" rolled back · "+backup.Stamp.Format("2006-01-02 15:04")+
		" build restored · the replaced build is kept", ToastKindSuccess)
}

// coreBuildsDelete is X: on the Kept list it arms the highlighted build and
// removes it on the second press inside the window (deleting is the one action
// here that cannot be undone, which is why it takes two presses); on the
// Installed list it rolls the core back.
func coreBuildsDelete() {
	if kbPanel.tab == coreBuildsTabCores {
		coreBuildsRollbackSelected()
		return
	}
	backup, ok := coreBuildsSelected()
	if !ok {
		ShowToast("No kept builds to delete", ToastKindInfo)
		return
	}
	now := time.Now()
	armed := kbPanel.confirm == backup.Path && now.Sub(kbPanel.confirmAt) <= coreBuildsConfirmWindow
	if !armed {
		kbPanel.confirm = backup.Path
		kbPanel.confirmAt = now
		ShowToast("Press X again to delete "+filepath.Base(backup.Path), ToastKindWarning)
		return
	}
	if err := deleteCoreBackup(backup); err != nil {
		ShowToast("Delete failed: "+err.Error(), ToastKindError)
		return
	}
	invalidateEmulatorCache()
	invalidateCompatCache()
	invalidateEmuCorePresence()
	coreBuildsRefresh()
	PlayBackSound()
	ShowToast("Deleted "+filepath.Base(backup.Path)+" · "+formatInstallBytes(backup.Size)+" freed", ToastKindSuccess)
}

// coreBuildsRescan re-reads the card and asks the buildbot again.
func coreBuildsRescan() {
	coreBuildsRefresh()
	emuStartUpdateCheck(0)
	PlayNavSound()
	if !IsOnline() {
		ShowToast("Offline — showing what is on the card; updates need a connection", ToastKindInfo)
		return
	}
	ShowToast("Checking for newer builds…", ToastKindInfo)
}

// coreBuildsAutoUpdateAvailable reports whether this device updates cores by
// itself (the handheld does; Windows updates on a press).
func coreBuildsAutoUpdateAvailable() bool {
	return !IsWindows()
}

// coreBuildsToggleAutoUpdate steps the Emulator updates setting (the cores
// and JukaHub's PPSSPP): Install automatically → Off → Notify me → Install
// automatically. It is the user's own choice, so emuSetCoreUpdateMode marks it
// as chosen and the start-up migration leaves it alone.
func coreBuildsToggleAutoUpdate() {
	if !coreBuildsAutoUpdateAvailable() {
		return
	}
	next := coreUpdateNotify
	switch emuCoreUpdateModeNow() {
	case coreUpdateNotify:
		next = coreUpdateInstall
	case coreUpdateInstall:
		next = coreUpdateOff
	}
	emuSetCoreUpdateMode(appConfig, next)
	PlayActivateSound()
	switch next {
	case coreUpdateInstall:
		ShowToast("Emulator updates: install automatically · daily, when idle or charging · the old build is kept", ToastKindSuccess)
	case coreUpdateOff:
		ShowToast("Emulator updates off · update from this page or the Emulators page", ToastKindInfo)
	default:
		ShowToast("Emulator updates: notify me · a daily check says what has a different build", ToastKindInfo)
	}
}

// coreBuildsBack is B: the Kept list returns to the Installed list it was
// opened from; otherwise the page closes.
func coreBuildsBack() {
	if kbPanel.confirm != "" {
		kbPanel.confirm = ""
		PlayBackSound()
		return
	}
	if kbPanel.tab == coreBuildsTabKept && kbPanel.keptFromCores {
		kbPanel.keptFromCores = false
		coreBuildsSetTab(coreBuildsTabCores)
		return
	}
	closeCoreBuildsPanel()
}

// coreBuildsAlt is Y: the other list (on the Installed list, the highlighted
// core's own kept builds).
func coreBuildsAlt() {
	if kbPanel.tab == coreBuildsTabCores {
		if c, ok := coreBuildsSelectedCore(); ok && c.Kept > 0 {
			coreBuildsShowKeptFor(c.Core)
			return
		}
		coreBuildsSetTab(coreBuildsTabKept)
		return
	}
	coreBuildsSetTab(coreBuildsTabCores)
}

// coreBuildsPress runs the action a hint badge or row button names.
func coreBuildsPress(button string) {
	switch strings.TrimSpace(button) {
	case "A":
		coreBuildsActivate()
	case "X":
		coreBuildsDelete()
	case "Y":
		coreBuildsAlt()
	case "L1":
		coreBuildsShiftFilter(-1)
	case "R1", "L1/R1":
		coreBuildsShiftFilter(1)
	case "Start":
		coreBuildsToggleAutoUpdate()
	case "B":
		coreBuildsBack()
	}
}

// ── Input ──────────────────────────────────────────────────────────────────

func ctrlCoreBuildsPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		coreBuildsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		coreBuildsMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		coreBuildsShiftFilter(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		coreBuildsShiftFilter(1)
	case sdl.CONTROLLER_BUTTON_A:
		coreBuildsActivate()
	case sdl.CONTROLLER_BUTTON_X:
		coreBuildsDelete()
	case sdl.CONTROLLER_BUTTON_Y:
		coreBuildsAlt()
	case sdl.CONTROLLER_BUTTON_START:
		coreBuildsToggleAutoUpdate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		coreBuildsBack()
	}
	return true
}

// handleCoreBuildsKey consumes every key while the panel is open, so the scene
// underneath never reacts behind the modal.
func handleCoreBuildsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !kbPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		coreBuildsMove(-1)
	case sdl.K_DOWN:
		coreBuildsMove(1)
	case sdl.K_PAGEUP:
		coreBuildsMove(-5)
	case sdl.K_PAGEDOWN:
		coreBuildsMove(5)
	case sdl.K_LEFT, sdl.K_LEFTBRACKET:
		coreBuildsShiftFilter(-1)
	case sdl.K_RIGHT, sdl.K_RIGHTBRACKET:
		coreBuildsShiftFilter(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		coreBuildsActivate()
	case sdl.K_x, sdl.K_DELETE:
		coreBuildsDelete()
	case sdl.K_y, sdl.K_TAB:
		coreBuildsAlt()
	case sdl.K_r, sdl.K_F5:
		coreBuildsRescan()
	case sdl.K_u:
		startEmulatorUpdateInstalled()
	case sdl.K_a:
		coreBuildsToggleAutoUpdate()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		coreBuildsBack()
	}
	return true
}

// handleCoreBuildsMouse acts on a click: a tab or filter chip switches, a row
// button runs its action, a row on the Kept list is restored and a row on the
// Installed list is selected; a click outside the panel closes it.
func handleCoreBuildsMouse(mx, my int32) bool {
	if !kbPanel.open {
		return false
	}
	for _, hit := range coreBuildsHintHits {
		if pointInRect(mx, my, hit.rect) {
			coreBuildsPress(hit.button)
			return true
		}
	}
	for _, hit := range coreBuildsActionHits {
		if pointInRect(mx, my, hit.rect) {
			coreBuildsPress(hit.button)
			return true
		}
	}
	for _, hit := range coreBuildsChipHits {
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		if hit.tab >= 0 {
			coreBuildsSetTab(hit.tab)
		} else {
			coreBuildsSetFilter(hit.family)
		}
		return true
	}
	for _, hit := range coreBuildsRowHits {
		if pointInRect(mx, my, hit.rect) {
			if kbPanel.tab == coreBuildsTabKept {
				kbPanel.cursor = hit.index
				kbPanel.confirm = ""
				coreBuildsActivate()
				return true
			}
			if kbPanel.cursor != hit.index {
				kbPanel.cursor = hit.index
				PlayNavSound()
			}
			return true
		}
	}
	if coreBuildsPanelRect.W > 0 && pointInRect(mx, my, coreBuildsPanelRect) {
		// Clicks inside the panel but off a row only consume.
		return true
	}
	closeCoreBuildsPanel()
	return true
}

// handleCoreBuildsWheel moves the selection a row per notch.
func handleCoreBuildsWheel(dy int32) bool {
	if !kbPanel.open {
		return false
	}
	if dy > 0 {
		coreBuildsMove(-1)
	} else if dy < 0 {
		coreBuildsMove(1)
	}
	return true
}

// ── Text ───────────────────────────────────────────────────────────────────

// coreBuildAge turns a stamp into the phrase a person reviewing builds thinks
// in, so the list reads as "yesterday" rather than as an arithmetic problem.
func coreBuildAge(stamp, now time.Time) string {
	age := now.Sub(stamp)
	switch {
	case age < 0:
		return "stamped in the future"
	case age < 12*time.Hour:
		return "today"
	case age < 48*time.Hour:
		return "yesterday"
	case age < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	case age < 60*24*time.Hour:
		return fmt.Sprintf("%d weeks ago", int(age.Hours()/24/7))
	case age < 730*24*time.Hour:
		return fmt.Sprintf("%d months ago", int(age.Hours()/24/30))
	default:
		return fmt.Sprintf("%d years ago", int(age.Hours()/24/365))
	}
}

// coreBuildsSubtitle is the Kept list's line under the title: how many builds,
// how much card, and the rule that keeps the pile from growing.
func coreBuildsSubtitle(rows []coreBackup, total int64) string {
	if len(rows) == 0 {
		return "No kept builds · an update keeps the build it replaced"
	}
	return fmt.Sprintf("%d kept · %s · up to %d per core",
		len(rows), formatInstallBytes(total), maxCoreBackups)
}

// coreBuildsCoresSubtitle is the Installed list's line under the title.
func coreBuildsCoresSubtitle(cores []coreBuildsCore, total int64, updates int) string {
	if len(cores) == 0 {
		return "No cores installed yet · install one from the Emulators page"
	}
	noun := "cores"
	if len(cores) == 1 {
		noun = "core"
	}
	line := fmt.Sprintf("%d %s · %s", len(cores), noun, formatInstallBytes(total))
	switch {
	case updates == 1:
		line += " · 1 update available"
	case updates > 1:
		line += fmt.Sprintf(" · %d updates available", updates)
	}
	return line
}

// coreBuildsFileLabel is a row's title: the core file being kept, without the
// backup stamp, because the date has its own line.
func coreBuildsFileLabel(b coreBackup) string {
	name := filepath.Base(b.Target)
	if name == "" || name == "." {
		name = filepath.Base(b.Path)
	}
	return name
}

// coreBuildsMeta is a row's second line: when the build was replaced, how long
// ago that was, and the folder it will be restored into.
func coreBuildsMeta(b coreBackup, now time.Time) string {
	parts := []string{b.Stamp.Format("2006-01-02 15:04"), coreBuildAge(b.Stamp, now)}
	if dir := filepath.Dir(b.Path); dir != "" && dir != "." {
		parts = append(parts, dir)
	}
	return strings.Join(parts, " · ")
}

// coreBuildsCoreMeta is an installed core's detail line.
func coreBuildsCoreMeta(c coreBuildsCore) string {
	parts := make([]string, 0, 4)
	if c.System != "" {
		parts = append(parts, c.System)
	}
	switch {
	case c.Date != "" && c.Built:
		parts = append(parts, "build "+c.Date)
	case c.Date != "":
		parts = append(parts, "installed "+c.Date)
	}
	parts = append(parts, formatInstallBytes(c.Size))
	if c.Kept == 1 {
		parts = append(parts, "1 kept build")
	} else if c.Kept > 1 {
		parts = append(parts, fmt.Sprintf("%d kept builds", c.Kept))
	}
	return strings.Join(parts, " · ")
}

// coresRootLabel is the cores folder a user would look at in a file manager:
// the first one the card has, so an empty list still says where it looked.
func coresRootLabel() string {
	dirs := retroArchCoreDirs()
	if len(dirs) == 0 {
		return "not found"
	}
	return dirs[0]
}

// coreBuildsCoreStatus is an installed core's pill: what the last check said
// about it, or why there is no answer yet.
func coreBuildsCoreStatus(c coreBuildsCore, checking, offline bool) (string, sdl.Color) {
	if name := emuInstallingName(); name != "" && strings.EqualFold(name, c.Name) {
		return "Updating…", glassAccent()
	}
	if entry, ok := emuCoreUpdateKnown(c.Core); ok {
		return "Update · " + entry.Date, glassAccent()
	}
	if emuCoreKnownCurrent(c.Core) {
		return "Up to date", ToastSuccessInk()
	}
	if _, ok := emuCoreIndexEntry(c.Core); !ok {
		switch {
		case checking:
			return "Checking…", ColorTextSecondary()
		case offline:
			return "Offline", ColorTextTertiary()
		}
		checked := false
		if _, at, _, _ := emuUpdateCheckSnapshot(); !at.IsZero() {
			checked = true
		}
		if checked {
			return "Not published", ColorTextTertiary()
		}
		return "Not checked", ColorTextTertiary()
	}
	if checking {
		return "Checking…", ColorTextSecondary()
	}
	return "Not checked", ColorTextTertiary()
}

// coreBuildsHeaderStatus is the read-out opposite the title: the connection,
// the automatic updates and when the cores were last checked.
func coreBuildsHeaderStatus(now time.Time) (string, sdl.Color) {
	checking, checkedAt, _, _ := emuUpdateCheckSnapshot()
	if !IsOnline() {
		return "Offline · updates need Wi-Fi", ToastWarnInk()
	}
	if checking {
		return "Checking for updates…", ColorTextSecondary()
	}
	last := checkedAt
	if t, ok := parseReleaseTimestamp(coreAutoUpdateLastCheck()); ok && t.After(last) && !t.After(now) {
		last = t
	}
	when := "not checked yet"
	if !last.IsZero() {
		when = "checked " + timeAgo(last)
	}
	if !coreBuildsAutoUpdateAvailable() {
		return "Updates on demand · " + when, ColorTextSecondary()
	}
	switch emuCoreUpdateModeNow() {
	case coreUpdateInstall:
		return "Auto-update on · " + when, ToastSuccessInk()
	case coreUpdateNotify:
		return "Updates: notify · " + when, ColorTextSecondary()
	}
	return "Auto-update off · " + when, ColorTextTertiary()
}

// coreBuildsHints is the footer for the list on screen.
func coreBuildsHints() []FooterHint {
	if kbPanel.confirm != "" {
		return []FooterHint{{Button: "X", Label: "Delete build"}, {Button: "B", Label: "Cancel"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Select"}}
	if kbPanel.tab == coreBuildsTabKept {
		if len(coreBuildsVisibleKept()) > 0 {
			hints = append(hints, FooterHint{Button: "A", Label: "Restore"}, FooterHint{Button: "X", Label: "Delete"})
		}
		hints = append(hints, FooterHint{Button: "Y", Label: "Installed"})
	} else {
		if c, ok := coreBuildsSelectedCore(); ok {
			if IsOnline() {
				hints = append(hints, FooterHint{Button: "A", Label: "Update"})
			}
			if c.Kept > 0 {
				hints = append(hints, FooterHint{Button: "X", Label: "Roll back"})
			}
		}
		hints = append(hints, FooterHint{Button: "Y", Label: "Kept builds"})
		if coreBuildsAutoUpdateAvailable() {
			// Start steps the setting; the hint names the mode it steps to.
			label := "Auto-install"
			switch emuCoreUpdateModeNow() {
			case coreUpdateInstall:
				label = "Updates off"
			case coreUpdateOff:
				label = "Notify updates"
			}
			hints = append(hints, FooterHint{Button: "Start", Label: label})
		}
	}
	if len(coreBuildsFamilies()) > 1 {
		hints = append(hints, FooterHint{Button: "L1/R1", Label: "Filter"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Rendering ──────────────────────────────────────────────────────────────

func renderCoreBuildsPanel(renderer *sdl.Renderer, config *Config) {
	if !kbPanel.open || renderer == nil {
		return
	}
	if coreBuildsReload.Swap(false) {
		coreBuildsRefresh()
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	title := getFontSized(config, "small", 19)
	if title == nil {
		title = font
	}
	meta := getFontSized(config, "small", 15)
	if meta == nil {
		meta = font
	}
	now := time.Now()

	panelW := int32(880)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(560)
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	panel := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	coreBuildsPanelRect = panel

	const padX = OverlayPadX
	checking, _, _, _ := emuUpdateCheckSnapshot()
	offline := !IsOnline()
	subtitle := coreBuildsSubtitle(kbPanel.rows, kbPanel.total)
	if kbPanel.tab == coreBuildsTabCores {
		updates := 0
		for _, c := range kbPanel.cores {
			if _, ok := emuCoreUpdateKnown(c.Core); ok {
				updates++
			}
		}
		subtitle = coreBuildsCoresSubtitle(kbPanel.cores, kbPanel.coresTotal, updates)
	}
	header := UIPanelHeader("Core builds", subtitle, "emu-retroarch")
	header.Status, header.StatusColor = coreBuildsHeaderStatus(now)
	headerH := UIPanelHeaderBand(config, header, 8)

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, panel)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, panel))

	// ── Tabs and family chips ─────────────────────────────────────────────
	rowW := panelW - 2*padX
	chipFont := meta
	chipH := int32(chipFont.Height()) + 14
	chipY := panelY + headerH
	x := panelX + padX
	coreBuildsChipHits = coreBuildsChipHits[:0]
	drawChipButton := func(label string, active bool, rect sdl.Rect) {
		tint := sdl.Color{}
		if active {
			tint = glassButtonTint(glassAccent())
		}
		drawGlassButton(renderer, config, rect, label, chipFont, tint, glassState{
			Hovered: pointInRect(mouseX, mouseY, rect),
		})
		if active {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 2, glassAccent())
		}
	}
	for _, tab := range []struct {
		id    int
		label string
	}{
		{coreBuildsTabCores, fmt.Sprintf("Installed %d", len(kbPanel.cores))},
		{coreBuildsTabKept, fmt.Sprintf("Kept builds %d", len(kbPanel.rows))},
	} {
		w, _ := measureText(chipFont, tab.label)
		rect := sdl.Rect{X: x, Y: chipY, W: w + 28, H: chipH}
		drawChipButton(tab.label, kbPanel.tab == tab.id, rect)
		coreBuildsChipHits = append(coreBuildsChipHits, coreBuildsChipHit{tab: tab.id, rect: rect})
		x += rect.W + 8
	}
	if families := coreBuildsFamilies(); len(families) > 1 {
		x += 16
		for _, family := range append([]string{""}, families...) {
			label := family
			if label == "" {
				label = "All"
			}
			w, _ := measureText(chipFont, label)
			rect := sdl.Rect{X: x, Y: chipY, W: w + 24, H: chipH}
			if rect.X+rect.W > panelX+panelW-padX {
				break
			}
			drawChipButton(label, kbPanel.filter == family, rect)
			coreBuildsChipHits = append(coreBuildsChipHits, coreBuildsChipHit{tab: -1, family: family, rect: rect})
			x += rect.W + 6
		}
	}

	// ── Rows ──────────────────────────────────────────────────────────────
	footerH := footerBadgeHeight(font) + 26
	bodyY := chipY + chipH + 12
	bodyH := panelY + panelH - footerH - bodyY
	rowH := int32(title.Height()) + int32(meta.Height()) + 26
	if rowH < coreBuildsRowH {
		rowH = coreBuildsRowH
	}
	coreBuildsRowHits = coreBuildsRowHits[:0]
	coreBuildsActionHits = coreBuildsActionHits[:0]
	n := coreBuildsListLen()

	if n == 0 {
		lineY := bodyY + 10
		heading := "No kept builds on this card"
		body := "An update keeps the build it replaced beside the core it came from, so a nightly that behaves worse can be undone without a card reader. This card has none right now; the Emulators page keeps up to five per core once it updates one."
		if kbPanel.tab == coreBuildsTabCores {
			heading = "No cores installed"
			body = "RetroArch's cores folder has no libretro cores yet. Pick a system on the Emulators page and press Install: the core comes from the libretro buildbot, checked against the CRC it publishes."
		}
		if kbPanel.filter != "" {
			heading = "Nothing in " + kbPanel.filter
			body = "No " + kbPanel.filter + " cores here. L1/R1 or the chips above switch the filter."
		}
		renderText(renderer, config, title, heading, ColorTextPrimary(), panelX+padX, lineY)
		lineY += int32(title.Height()) + 10
		for _, line := range compatTextLines(meta, body, rowW) {
			renderText(renderer, config, meta, line, ColorTextTertiary(), panelX+padX, lineY)
			lineY += int32(meta.Height()) + 2
		}
		lineY += 8
		for _, line := range compatTextLines(meta, "Cores folder: "+coresRootLabel(), rowW) {
			renderText(renderer, config, meta, line, ColorTextSecondary(), panelX+padX, lineY)
			lineY += int32(meta.Height()) + 2
		}
	} else {
		maxRows := bodyH / (rowH + 6)
		if maxRows < 1 {
			maxRows = 1
		}
		visible := int32(n)
		if visible > maxRows {
			visible = maxRows
		}
		// Keep the cursor inside the window, then clamp the window itself so
		// the last page is full instead of half empty.
		if kbPanel.cursor < kbPanel.scroll {
			kbPanel.scroll = kbPanel.cursor
		}
		if kbPanel.cursor >= kbPanel.scroll+int(visible) {
			kbPanel.scroll = kbPanel.cursor - int(visible) + 1
		}
		if kbPanel.scroll > n-int(visible) {
			kbPanel.scroll = n - int(visible)
		}
		if kbPanel.scroll < 0 {
			kbPanel.scroll = 0
		}
		kept := coreBuildsVisibleKept()
		cores := coreBuildsVisibleCores()
		for i := int32(0); i < visible; i++ {
			idx := kbPanel.scroll + int(i)
			if idx >= n {
				break
			}
			r := sdl.Rect{X: panelX + padX, Y: bodyY + i*(rowH+6), W: rowW, H: rowH}
			coreBuildsRowHits = append(coreBuildsRowHits, coreBuildsRowHit{index: idx, rect: r})
			focused := idx == kbPanel.cursor
			hovered := pointInRect(mouseX, mouseY, r)
			if kbPanel.tab == coreBuildsTabKept {
				coreBuildsDrawKeptRow(renderer, config, title, meta, r, kept[idx], focused, hovered, now)
			} else {
				coreBuildsDrawCoreRow(renderer, config, title, meta, r, cores[idx], focused, hovered, checking, offline)
			}
		}
		if int(visible) < n {
			drawScrollbar(renderer, panelX+panelW-14, bodyY, 3, bodyH-14,
				float64(visible)/float64(n),
				float64(kbPanel.scroll)/float64(n-int(visible)))
		}
	}

	// ── Footer ────────────────────────────────────────────────────────────
	footerY := panelY + panelH - footerH
	setDrawColor(renderer, glassHairline())
	renderer.DrawLine(panelX+padX, footerY, panelX+panelW-padX, footerY)
	coreBuildsHintHits = emuDrawHintRow(renderer, config, coreBuildsHints(), panelX+padX,
		footerY+(footerH-footerBadgeHeight(font))/2, rowW, coreBuildsHintHits[:0])
}

// coreBuildsDrawKeptRow draws one kept build: the core file, its size at the
// right edge (measured, so the name is ellipsized into exactly the room that is
// left), and when it was replaced — or the delete warning while it is armed.
func coreBuildsDrawKeptRow(renderer *sdl.Renderer, config *Config, title, meta *ttf.Font, r sdl.Rect, backup coreBackup, focused, hovered bool, now time.Time) {
	armed := kbPanel.confirm != "" && kbPanel.confirm == backup.Path
	drawGlassSurface(renderer, r, glassRowRadius, glassState{Focused: focused, Hovered: hovered && !focused, NoLift: true})
	if armed {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, 2, ToastErrorInk())
	}
	badge := sdl.Rect{X: r.X + 12, Y: r.Y + (r.H-38)/2, W: 38, H: 38}
	glyph := drawGlassBadge(renderer, badge)
	DrawHomeIcon(renderer, "backup", glyph, false)
	textX := badge.X + badge.W + 14

	size := formatInstallBytes(backup.Size)
	sizeW, _ := measureText(meta, size)
	renderText(renderer, config, title,
		ellipsize(title, coreBuildsFileLabel(backup), r.X+r.W-textX-sizeW-28),
		ColorTextPrimary(), textX, r.Y+9)
	renderText(renderer, config, meta, size, ColorTextSecondary(),
		r.X+r.W-14-sizeW, r.Y+9+int32(title.Height()-meta.Height())/2)

	line := coreBuildsMeta(backup, now)
	lineColor := ColorTextTertiary()
	if armed {
		line = "Press X again to delete this build"
		lineColor = ToastErrorInk()
	}
	renderText(renderer, config, meta, ellipsize(meta, line, r.X+r.W-14-textX), lineColor,
		textX, r.Y+11+int32(title.Height()))
}

// coreBuildsDrawCoreRow draws one installed core: its system icon, name and
// detail, the update pill, and on the focused row the Update / Roll back
// buttons a mouse or a tap can press.
func coreBuildsDrawCoreRow(renderer *sdl.Renderer, config *Config, title, meta *ttf.Font, r sdl.Rect, c coreBuildsCore, focused, hovered, checking, offline bool) {
	drawGlassSurface(renderer, r, glassRowRadius, glassState{Focused: focused, Hovered: hovered && !focused, NoLift: true})
	badge := sdl.Rect{X: r.X + 12, Y: r.Y + (r.H-38)/2, W: 38, H: 38}
	glyph := drawGlassBadge(renderer, badge)
	DrawHomeIcon(renderer, emuGlyphFor(c.Core, c.Core), glyph, false)
	textX := badge.X + badge.W + 14

	label, ink := coreBuildsCoreStatus(c, checking, offline)
	pillW, _ := measureText(meta, label)
	pill := sdl.Rect{W: pillW + 22, H: int32(meta.Height()) + 8}
	pill.X = r.X + r.W - 14 - pill.W
	pill.Y = r.Y + 8
	drawChip(renderer, config, pill, label, WithAlpha(ink, 44), ink, meta)
	strokeRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, 1, WithAlpha(ink, 110))

	renderText(renderer, config, title, ellipsize(title, c.Name, pill.X-12-textX), ColorTextPrimary(), textX, r.Y+9)
	detailRight := r.X + r.W - 14
	if focused {
		// The row's own buttons, right-aligned under the pill.
		bx := r.X + r.W - 14
		by := pill.Y + pill.H + 4
		bh := r.Y + r.H - 6 - by
		if bh > int32(meta.Height())+10 {
			bh = int32(meta.Height()) + 10
		}
		var buttons []FooterHint
		if c.Kept > 0 {
			buttons = append(buttons, FooterHint{Button: "X", Label: "Roll back"})
		}
		if !offline {
			buttons = append(buttons, FooterHint{Button: "A", Label: "Update"})
		}
		for _, b := range buttons {
			w, _ := measureText(meta, b.Label)
			rect := sdl.Rect{X: bx - (w + 24), Y: by, W: w + 24, H: bh}
			drawGlassButton(renderer, config, rect, b.Label, meta, sdl.Color{}, glassState{Hovered: pointInRect(mouseX, mouseY, rect)})
			coreBuildsActionHits = append(coreBuildsActionHits, coreBuildsActionHit{button: b.Button, rect: rect})
			bx = rect.X - 8
		}
		detailRight = bx - 6
	}
	renderText(renderer, config, meta, ellipsize(meta, coreBuildsCoreMeta(c), detailRight-textX), ColorTextTertiary(),
		textX, r.Y+11+int32(title.Height()))
}
