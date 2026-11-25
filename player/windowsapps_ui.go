package main

// windowsapps_ui.go — the "Windows Apps" overlay.
//
// The panel shows every discovered .exe, says why one cannot launch, follows a
// starting app on its row, switches an app's display on Y (Windows/GDI or
// OpenGL), and on X offers the runtime install (Wine, WestonPack) plus the
// detected Wine/Box64/display status. On Windows it only lists and starts.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type windowsAppsPanelState struct {
	open      bool
	cursor    int
	scroll    int
	help      bool
	returnEmu bool
	// notice explains what the setup page is about to install when the user
	// pressed A on an app the runtime cannot start yet. pending is that app:
	// it starts once the install finishes, unless setup or the panel was
	// closed first (windowsAppsStartReady).
	notice  string
	pending *windowsAppEntry
	// sampleWait is set while the sample program this visit asked for
	// downloads: it starts once it is in (windowsSampleFinished), unless the
	// panel or setup was left, or another app was chosen, first.
	sampleWait bool
}

var winAppsPanel windowsAppsPanelState

type windowsAppRowHit struct {
	index int
	rect  sdl.Rect
}

var windowsAppRowHits []windowsAppRowHit
var windowsAppPanelRect sdl.Rect

// windowsSetupButtonRect is the tappable "Setup" pill in the panel header, so
// touch-only handheld users can reach the runtime page without a keyboard.
var windowsSetupButtonRect sdl.Rect

// windowsSampleLineRect is the empty list's sample line, which a tap
// activates like A.
var windowsSampleLineRect sdl.Rect

func openWindowsAppsPanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	portsPanel = portsPanelState{}
	winAppsPanel = windowsAppsPanelState{open: true, returnEmu: returnEmu}
	windowsAppPanelRect = sdl.Rect{}
	windowsAppRowHits = nil
	windowsSetupButtonRect = sdl.Rect{}
	windowsSampleLineRect = sdl.Rect{}
	windowsSetupRowHit = [windowsSetupRowCount]sdl.Rect{}
	setupCursor = windowsSetupRowWine
	// The built-in Wine test goes into the list the first time, or a newer
	// build over an older one (windowsapps_winetest.go).
	if err := wineTestEnsure(false); err != nil {
		logWarn("windowsapps", "built-in Wine test not placed: %v", err)
	}
	invalidateWindowsAppsCache()
	// A launch from here runs on a goroutine: the settings it needs are read
	// now, on the main thread. A failure from an earlier visit is old news,
	// and so is an app an earlier visit's setup was waiting for (another
	// screen can close this panel without closeWindowsAppsPanel).
	wineLaunchSettingsTake(appConfig)
	windowsAppsTakeFailure()
	windowsDropPendingLaunch()
	PlayActivateSound()
}

func closeWindowsAppsPanel() {
	if !winAppsPanel.open {
		return
	}
	returnEmu := winAppsPanel.returnEmu
	// Leaving the panel abandons the pending launch: the runtime install keeps
	// running, but the app must not start behind the user's back later.
	windowsDropPendingLaunch()
	winAppsPanel = windowsAppsPanelState{}
	windowsAppPanelRect = sdl.Rect{}
	windowsAppRowHits = nil
	windowsSetupButtonRect = sdl.Rect{}
	windowsSampleLineRect = sdl.Rect{}
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

func windowsAppsPanelMove(delta int) {
	entries := windowsAppEntries()
	if len(entries) == 0 {
		return
	}
	winAppsPanel.cursor += delta
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = 0
	}
	PlayNavSound()
}

func setWindowsSetupCursor(row int) {
	if row < 0 {
		row = windowsSetupRowCount - 1
	}
	if row >= windowsSetupRowCount {
		row = 0
	}
	if row != setupCursor {
		PlayNavSound()
	}
	setupCursor = row
}

func setupMove(delta int) {
	setWindowsSetupCursor(setupCursor + delta)
}

// setupActivate runs the install action for the focused setup row. When a
// launch is waiting for its runtime, the whole missing chain runs instead: the
// user pressed A once to install, and the app starts when the last piece is in.
func setupActivate() {
	if IsWindows() {
		ShowToast("Windows runs .exe files directly; nothing needs installing", ToastKindInfo)
		return
	}
	if pending := winAppsPanel.pending; pending != nil {
		windowsAppsPanelProvision(*pending)
		return
	}
	switch setupCursor {
	case windowsSetupRowDisplay:
		windowsAppsPanelInstallDisplay()
	case windowsSetupRowDXVK:
		windowsAppsPanelInstallDXVK()
	case windowsSetupRowSample:
		windowsAppsPanelSample()
	case windowsSetupRowWineTest:
		windowsAppsPanelWineTest()
	default:
		if tag := windowsRuntimeUpdateTag(); tag != "" {
			windowsAppsPanelUpdateRuntime(tag)
			return
		}
		windowsAppsPanelInstall()
	}
}

// windowsRuntimeUpdateTag is the newer Windows-app runtime the daily pass's
// check found (wineUpdateCheck, LIB-25) when the runtime row can offer it as
// Update: the Box64 pack's runtime is installed and no runtime install runs.
// "" otherwise. Main thread.
func windowsRuntimeUpdateTag() string {
	newer, tag := wineUpdateKnown()
	if !newer || tag == "" || IsWindows() || !winePackSupported() || windowsRuntimeInstalling() {
		return ""
	}
	if base := windowsSetupSnapshot(); len(base.rows) == 0 || base.rows[0].Status != "installed" {
		return ""
	}
	return tag
}

// windowsAppsPanelUpdateRuntime is the runtime row's Update: the install the
// daily pass runs (wineUpdateInstall — the newer Box64 pack and the Wine it
// names), started from a press, on the install bar with its phase and bytes,
// and on the page's installer line.
func windowsAppsPanelUpdateRuntime(tag string) {
	switch {
	case wineProgramsRunning():
		ShowToast("Close the running Windows app first, then update the runtime", ToastKindInfo)
		return
	case !IsOnline():
		PlayErrorSound()
		ShowToast("Updating the Windows app runtime needs internet — connect to Wi-Fi, then try again", ToastKindWarning)
		return
	case installBarActive() || !tryBeginPatchOp():
		// A new install-bar run would cancel the one on the bar now.
		ShowToast("Another install is running — try again when it finishes", ToastKindInfo)
		return
	}
	gen, runCtx := installBarBeginRun("Windows runtime")
	installBarPhase(installPhaseResolving)
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(emuRuntimeWithRun(runCtx, gen), emuWinInstallTimeout)
		defer cancel()
		err := wineUpdateInstall(ctx, nil, func(line string) {
			if installBarGeneration() == gen {
				installBarDetail(line)
			}
		})
		invalidateWindowsAppsCache()
		invalidateWindowsSetupSnapshot()
		windowsRuntimeStatusForget()
		tbxPost(func() {
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(gen)
				ShowToast("Stopped updating the Windows app runtime", ToastKindInfo)
			case errors.Is(err, errRelPackBusy):
				installBarAbortRun(gen)
				ShowToast("The runtime is busy (an install or a Windows program) — try again when it is free", ToastKindInfo)
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				PlayErrorSound()
				ShowToast("Could not update the Windows app runtime: "+msg, ToastKindError)
			default:
				installBarDoneRun(gen)
				PlaySuccessSound()
				ShowToast("The Windows app runtime is updated ("+tag+")", ToastKindSuccess)
			}
		})
	}()
	PlayActivateSound()
}

// windowsAppsPanelProvision installs every missing runtime piece for entry and
// launches it when the last one succeeds. The setup page keeps the progress on
// its installer line while it runs.
func windowsAppsPanelProvision(entry windowsAppEntry) {
	if IsWindows() {
		ShowToast("Windows runs .exe files directly; the Wine runtime is only needed on the handheld", ToastKindInfo)
		return
	}
	if err := startWindowsProvisioning(&entry); err != nil {
		ShowToast(capitalizeFirst(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
}

// windowsAppsPanelInstallDXVK installs DXVK on its own: it is never part of
// the automatic chain on a handheld. Where the Vulkan driver is older than the
// 1.3 DXVK 2 needs, only DXVK 1.10.3 can try, and the first A asks for a
// second one before installing that experiment.
func windowsAppsPanelInstallDXVK() {
	if dxvk, _ := resolveDXVK(); dxvk == "" && dxvkLegacyOnly() && !windowsDXVKConfirmed() {
		ShowToast("DXVK "+windowsDXVKLegacyVersion+" is an experiment on this Vulkan · press A again to install it", ToastKindInfo)
		return
	}
	if !beginWindowsRuntimeInstall("dxvk", "checking for a Vulkan loader") {
		ShowToast("The runtime installer is already running", ToastKindInfo)
		return
	}
	go func() {
		err := installDXVKRuntime()
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
		invalidateWindowsSetupSnapshot()
		if err != nil {
			windowsStepFailed("dxvk", "DXVK install", err)
			return
		}
		dxvk, _ := resolveDXVK()
		if ok, why := dxvkUsableHere(dxvk); !ok {
			setWindowsRuntimeStatus("DXVK installed, but off", why, false)
			ShowToast("DXVK installed, but it stays off: "+why, ToastKindWarning)
			return
		}
		setWindowsRuntimeStatus("DXVK installed", "", false)
		ShowToast("DXVK installed — Direct3D now runs through Vulkan", ToastKindSuccess)
	}()
	PlayActivateSound()
}

// windowsAppsPanelInstall runs the whole setup chain from the runtime row:
// the runtime, the display when this device needs one, and the prepared
// Windows folder. The setup page keeps rendering while it runs, with the
// current step on its installer line.
func windowsAppsPanelInstall() {
	if windowsRuntimeInstalling() {
		ShowToast("The runtime installer is already running", ToastKindInfo)
		return
	}
	if err := startWindowsRuntimeInstall(); err != nil {
		ShowToast(capitalizeFirst(err.Error()), ToastKindInfo)
		return
	}
	invalidateWindowsSetupSnapshot()
	PlaySuccessSound()
	setWindowsSetupCursor(windowsSetupRowWine)
}

// windowsAppsPanelInstallDisplay gets WestonPack, the display bridge Wine
// needs on a handheld with no X11 or Wayland session: PortMaster's copy when
// it is there, else a checked download.
func windowsAppsPanelInstallDisplay() {
	if windowsDesktopSession() {
		ShowToast("Not needed: Wine uses this computer's desktop", ToastKindInfo)
		return
	}
	if src := findWestonPackSource(); src.Image != "" || src.Wrapper != "" {
		ShowToast("WestonPack is already here", ToastKindInfo)
		return
	}
	if !beginWindowsRuntimeInstall("weston", "Looking for WestonPack…") {
		ShowToast("The runtime installer is already running", ToastKindInfo)
		return
	}
	go func() {
		err := installWestonRuntime()
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
		invalidateWindowsSetupSnapshot()
		if err != nil {
			windowsStepFailed("weston", "WestonPack install", err)
			return
		}
		setWindowsRuntimeStatus("WestonPack installed", "", false)
		ShowToast("WestonPack installed", ToastKindSuccess)
	}()
	PlayActivateSound()
}

func windowsAppsPanelRescan() {
	invalidateWindowsAppsCache()
	entries := windowsAppEntries()
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = 0
	}
	runtime := windowsRuntimeStatusCached()
	detail := "Found " + pluralizeCount(len(entries), "app", "apps")
	if !runtime.ready() {
		detail += " · runtime setup needed"
	}
	ShowToast(detail, ToastKindInfo)
	PlayNavSound()
}

// windowsAppsPanelActivate starts the focused app. The panel stays open: the
// row shows the launch's progress until the program takes the screen, and the
// list is back when it ends. A missing runtime opens the setup page instead.
// On an empty list, A gets the sample program (windowsapps_sample.go).
func windowsAppsPanelActivate() {
	entries := windowsAppEntries()
	if len(entries) == 0 {
		if IsWindows() {
			ShowToast("No programs found — copy them into "+windowsAppsDir(), ToastKindInfo)
		} else {
			windowsAppsPanelSample()
		}
		return
	}
	if winAppsPanel.cursor < 0 || winAppsPanel.cursor >= len(entries) {
		return
	}
	windowsAppsActivateEntry(entries[winAppsPanel.cursor])
}

// windowsAppsActivateEntry is A on entry's row: nothing while it is starting
// or the runtime is being installed, the setup page when the runtime misses a
// piece, else the launch. Choosing an app ends the wait for a sample
// download (sampleWait).
func windowsAppsActivateEntry(entry windowsAppEntry) {
	winAppsPanel.sampleWait = false
	if wineLaunchPhase(entry.Path) != "" {
		// Already starting: the row says how far it got.
		return
	}
	if windowsAppsRuntimeBusy() {
		return
	}
	wineLaunchSettingsTake(appConfig)
	runtime := windowsRuntimeStatusCached()
	if windowsAppsOfferSetup(entry, runtime) {
		return
	}
	if !runtime.ready() {
		showLaunchFailureDialog(entry.Title, errors.New(runtime.Problem), "")
		return
	}
	windowsAppsLaunch(entry)
}

// windowsAppsLaunch starts entry from the panel, on the main thread; a launch
// that cannot start shows the launch-failure dialog with the app's log.
func windowsAppsLaunch(entry windowsAppEntry) {
	if err := launchWindowsApp(entry); err != nil {
		showLaunchFailureDialog(entry.Title, err, windowsAppLogIfAny(entry))
		return
	}
	PlaySuccessSound()
	ShowToast("Starting "+entry.Title, ToastKindSuccess)
}

// windowsAppsRuntimeBusy refuses, politely, to start an app while the Windows
// runtime is being installed or updated (the setup chain, a setup row, the
// daily Emulator updates): the launch would run on files being replaced.
func windowsAppsRuntimeBusy() bool {
	if IsWindows() || !windowsRuntimeInstalling() {
		return false
	}
	ShowToast("The Windows runtime is being installed or updated · start the app when it is done", ToastKindInfo)
	return true
}

// windowsAppsStartReady starts, on the main thread, the app a finished setup
// chain was asked to start (Install & Run) — only while the setup page still
// waits for that app; leaving setup or the panel dropped it already. The page
// gives way to the list, where the app's row shows how far its start got.
func windowsAppsStartReady() {
	entry, ok := windowsTakeReadyLaunch()
	if !ok {
		return
	}
	if pending := winAppsPanel.pending; !winAppsPanel.help || pending == nil || pending.Path != entry.Path {
		return
	}
	winAppsPanel.help, winAppsPanel.pending, winAppsPanel.notice = false, nil, ""
	for i, e := range windowsAppEntries() {
		if e.Path == entry.Path {
			winAppsPanel.cursor = i
			break
		}
	}
	if windowsAppsRuntimeBusy() {
		return
	}
	wineLaunchSettingsTake(appConfig)
	windowsAppsLaunch(entry)
}

// windowsAppsOfferSetup opens the setup page on what entry still needs, when
// the runtime is missing pieces, and reports whether it did. One press is the
// confirmation: the setup page names what will be downloaded and its size,
// and the next A installs the whole chain and starts the app. A 300 MB
// download never begins silently. Windows needs nothing.
func windowsAppsOfferSetup(entry windowsAppEntry, runtime windowsRuntimeStatus) bool {
	if IsWindows() {
		return false
	}
	plan := windowsRuntimePlan(runtime)
	if len(plan) == 0 {
		return false
	}
	pending := entry
	winAppsPanel.help = true
	winAppsPanel.pending = &pending
	winAppsPanel.notice = fmt.Sprintf("To run %s, install %s first. Press A to download and install it; %s starts when it is ready.",
		entry.Title, windowsPlanLabel(plan), entry.Title)
	setWindowsSetupCursor(windowsRuntimeStepRow(plan[0]))
	PlayActivateSound()
	ShowToast("Runtime needed — press A to install", ToastKindInfo)
	return true
}

// windowsAppLogIfAny is the app's log when one exists, for the failure dialog.
func windowsAppLogIfAny(entry windowsAppEntry) string {
	if path := windowsAppLogPath(entry); regularFile(path) {
		return path
	}
	return ""
}

// windowsAppsShowFailure shows, on the main thread, a launch that failed on its
// goroutine (windowsAppsLaunchFailed): the setup page when the runtime turned
// out to be missing a piece it can install, else the launch-failure dialog
// with the app's log.
func windowsAppsShowFailure() {
	f, ok := windowsAppsTakeFailure()
	if !ok {
		return
	}
	var cause *launchFailureCause
	if errors.As(f.err, &cause) && cause.kind == launchFailureRuntimeMissing {
		invalidateWindowsAppsCache()
		if windowsAppsOfferSetup(f.entry, windowsRuntimeStatusCached()) {
			return
		}
	}
	showLaunchFailureDialog(f.entry.Title, f.err, f.log)
}

// windowsAppsPanelToggleDisplay switches the focused app between WestonPack's
// Windows (GDI) mode, for programs that draw their own windows, and its OpenGL
// mode, for OpenGL games. The choice is kept per app (display.json). Windows
// shows programs itself, so there is nothing to choose there.
func windowsAppsPanelToggleDisplay() {
	if IsWindows() {
		return
	}
	entries := windowsAppEntries()
	if winAppsPanel.cursor < 0 || winAppsPanel.cursor >= len(entries) {
		return
	}
	entry := entries[winAppsPanel.cursor]
	on := !windowsAppOpenGL(entry.Path)
	if err := windowsAppSetOpenGL(entry.Path, on); err != nil {
		ShowToast("Could not save the display choice: "+err.Error(), ToastKindError)
		return
	}
	label := "Windows (GDI)"
	if on {
		label = "OpenGL"
	}
	PlayToggleSound()
	ShowToast(entry.Title+" · Display: "+label, ToastKindInfo)
}

func windowsAppsPanelToggleHelp() {
	if IsWindows() && !winAppsPanel.help {
		// Windows runs the programs itself: there is nothing to set up.
		return
	}
	winAppsPanel.help = !winAppsPanel.help
	if winAppsPanel.help {
		PlayActivateSound()
		return
	}
	// Closing setup drops a launch that was waiting for the runtime, or for
	// the sample download; the user asked for the launcher, not for the app
	// to open later by itself.
	windowsDropPendingLaunch()
	winAppsPanel.pending = nil
	winAppsPanel.notice = ""
	winAppsPanel.sampleWait = false
	PlayBackSound()
}

func ctrlWindowsApps(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if winAppsPanel.help {
			setupMove(-1)
		} else {
			windowsAppsPanelMove(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if winAppsPanel.help {
			setupMove(1)
		} else {
			windowsAppsPanelMove(1)
		}
	case sdl.CONTROLLER_BUTTON_A:
		if winAppsPanel.help {
			setupActivate()
		} else {
			windowsAppsPanelActivate()
		}
	case sdl.CONTROLLER_BUTTON_X:
		windowsAppsPanelToggleHelp()
	case sdl.CONTROLLER_BUTTON_Y:
		if !winAppsPanel.help {
			windowsAppsPanelY()
		}
	case sdl.CONTROLLER_BUTTON_START:
		if !winAppsPanel.help {
			windowsAppsPanelRescan()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if winAppsPanel.help {
			windowsAppsPanelToggleHelp()
		} else {
			closeWindowsAppsPanel()
		}
	}
	return true
}

func handleWindowsAppsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !winAppsPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if winAppsPanel.help {
			setupMove(-1)
		} else {
			windowsAppsPanelMove(-1)
		}
	case sdl.K_DOWN:
		if winAppsPanel.help {
			setupMove(1)
		} else {
			windowsAppsPanelMove(1)
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if winAppsPanel.help {
			setupActivate()
		} else {
			windowsAppsPanelActivate()
		}
	case sdl.K_x, sdl.K_h:
		windowsAppsPanelToggleHelp()
	case sdl.K_y:
		if !winAppsPanel.help {
			windowsAppsPanelY()
		}
	case sdl.K_d:
		if !winAppsPanel.help {
			windowsAppsPanelToggleDisplay()
		}
	case sdl.K_r, sdl.K_F5:
		if !winAppsPanel.help {
			windowsAppsPanelRescan()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		if winAppsPanel.help {
			windowsAppsPanelToggleHelp()
		} else {
			closeWindowsAppsPanel()
		}
	}
	return true
}

func handleWindowsAppsMouse(mx, my int32, config *Config) bool {
	if !winAppsPanel.open {
		return false
	}
	if !winAppsPanel.help && windowsSetupButtonRect.W > 0 && pointInRect(mx, my, windowsSetupButtonRect) {
		windowsAppsPanelToggleHelp()
		return true
	}
	if winAppsPanel.help {
		for i, r := range windowsSetupRowHit {
			if r.W > 0 && pointInRect(mx, my, r) {
				setupCursor = i
				setupActivate()
				return true
			}
		}
		if windowsAppPanelRect.W > 0 && pointInRect(mx, my, windowsAppPanelRect) {
			return true
		}
		windowsAppsPanelToggleHelp()
		return true
	}
	for _, hit := range windowsAppRowHits {
		if pointInRect(mx, my, hit.rect) {
			winAppsPanel.cursor = hit.index
			windowsAppsPanelActivate()
			return true
		}
	}
	if windowsSampleLineRect.W > 0 && pointInRect(mx, my, windowsSampleLineRect) {
		windowsAppsPanelSample()
		return true
	}
	if windowsAppPanelRect.W > 0 && pointInRect(mx, my, windowsAppPanelRect) {
		return true
	}
	closeWindowsAppsPanel()
	return true
}

// drawWindowsSetupButton paints the header "Setup" pill and records its hit
// rect. It is omitted once the setup page itself is showing.
func drawWindowsSetupButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, panelY, panelW int32) {
	if renderer == nil || font == nil {
		windowsSetupButtonRect = sdl.Rect{}
		return
	}
	label := "Setup"
	w, _, _ := font.SizeUTF8(label)
	pad := int32(12)
	r := sdl.Rect{
		X: panelX + panelW - 24 - int32(w) - pad*2,
		Y: panelY + 14,
		W: int32(w) + pad*2,
		H: int32(font.Height()) + 10,
	}
	windowsSetupButtonRect = r
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(ColorSurfaceRow, 200))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, ColorBorderDefault)
	renderText(renderer, config, font, label, ColorTextSecondary(), r.X+pad, r.Y+5)
}

// windowsAppsSetupPill draws the header's Setup pill on the list, except on
// Windows, where there is nothing to set up.
func windowsAppsSetupPill(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, panelY, panelW int32) {
	if IsWindows() {
		windowsSetupButtonRect = sdl.Rect{}
		return
	}
	drawWindowsSetupButton(renderer, config, font, panelX, panelY, panelW)
}

// windowsRuntimeStatusCache keeps the panel's runtime status for a second:
// resolving it reads files, and the panel is drawn every frame. Installs and
// rescans drop it (invalidateWindowsAppsCache).
var windowsRuntimeStatusCache struct {
	mu     sync.Mutex
	status windowsRuntimeStatus
	at     time.Time
}

// windowsRuntimeStatusCached is windowsRuntimeStatusFor, at most a second old.
func windowsRuntimeStatusCached() windowsRuntimeStatus {
	windowsRuntimeStatusCache.mu.Lock()
	defer windowsRuntimeStatusCache.mu.Unlock()
	if windowsRuntimeStatusCache.at.IsZero() || time.Since(windowsRuntimeStatusCache.at) > time.Second {
		windowsRuntimeStatusCache.status = windowsRuntimeStatusFor()
		windowsRuntimeStatusCache.at = time.Now()
	}
	return windowsRuntimeStatusCache.status
}

// windowsRuntimeStatusForget makes the next windowsRuntimeStatusCached read
// the status again. Any goroutine.
func windowsRuntimeStatusForget() {
	windowsRuntimeStatusCache.mu.Lock()
	windowsRuntimeStatusCache.at = time.Time{}
	windowsRuntimeStatusCache.mu.Unlock()
}

func windowsAppsSummary(entries []windowsAppEntry, runtime windowsRuntimeStatus) string {
	parts := []string{pluralizeCount(len(entries), "app", "apps")}
	if len(entries) == 1 {
		parts[0] = "1 app"
	}
	if IsWindows() {
		return strings.Join(append(parts, "run directly by Windows"), " · ")
	}
	if runtime.ready() {
		flavor := runtime.Flavor
		if flavor == "" {
			flavor = "Wine"
		}
		parts = append(parts, flavor+" ready")
		if runtime.Display != "" {
			parts = append(parts, runtime.displayLabel())
		}
		if runtime.DXVK != "" {
			if runtime.Vulkan {
				parts = append(parts, "DXVK")
			} else {
				parts = append(parts, "DXVK needs Vulkan")
			}
		}
	} else {
		parts = append(parts, "runtime setup needed")
	}
	return strings.Join(parts, " · ")
}

// Windows Apps setup actions. They are the only focusable rows on the setup
// page; everything below them is status text. The last two download the
// sample program (windowsapps_sample.go) and start the built-in Wine test
// (windowsapps_winetest.go).
const (
	windowsSetupRowWine     = 0
	windowsSetupRowDisplay  = 1
	windowsSetupRowDXVK     = 2
	windowsSetupRowSample   = 3
	windowsSetupRowWineTest = 4
	windowsSetupRowCount    = 5
)

// windowsRuntimeStepRow maps a provisioning step to the setup row it belongs
// to, so the page opens on the action the launch is waiting for.
func windowsRuntimeStepRow(step windowsRuntimeStep) int {
	switch step {
	case windowsStepWine:
		return windowsSetupRowWine
	case windowsStepDisplay:
		return windowsSetupRowDisplay
	default:
		return windowsSetupRowDXVK
	}
}

var (
	setupCursor        int
	windowsSetupRowHit [windowsSetupRowCount]sdl.Rect
)

type windowsSetupRow struct {
	Title  string
	Detail string
	Status string
	Bad    bool
}

// windowsAppsSetupRows builds the install actions: the runtime (which runs
// the whole chain), the display, DXVK and the sample program. The actions
// come first so the panel answers "what do I press" before it explains the
// environment. The facts behind them are at most a second old
// (wine_setup.go); whether a row is installing or failed is read fresh.
func windowsAppsSetupRows(runtime windowsRuntimeStatus) []windowsSetupRow {
	base := windowsSetupSnapshot()
	rows := append([]windowsSetupRow(nil), base.rows...)
	target := windowsRuntimeInstallTarget()
	for i, key := range []string{"wine", "weston", "dxvk"} {
		if i >= len(rows) {
			break
		}
		switch {
		case target == key:
			rows[i].Status, rows[i].Bad = "installing", false
		case windowsRuntimeFailed(key) != "" && (rows[i].Bad || rows[i].Status == "optional"):
			rows[i].Status, rows[i].Bad = "failed", true
		}
	}
	if !IsWindows() && len(rows) > windowsSetupRowSample {
		windowsSampleSetupOverlay(&rows[windowsSetupRowSample])
	}
	// The daily pass found a newer runtime (Emulator updates on Notify me, or
	// an install that failed): the runtime row says so and A updates it.
	if tag := windowsRuntimeUpdateTag(); tag != "" && len(rows) > 0 {
		rows[0].Status = "update"
		rows[0].Detail = tag + " is out · " + hintSentence("%s updates it", "A", "Update")
		if why := windowsRuntimeFailed("wine"); why != "" {
			rows[0].Detail = tag + " is out · the last update failed (" + why + ") · " + hintSentence("%s tries again", "A", "Update")
		}
	}
	return rows
}

func windowsAppsSetupLines(runtime windowsRuntimeStatus) []string {
	base := windowsSetupSnapshot()
	lines := append([]string(nil), base.lines...)
	if line := windowsRuntimeInstallLine(); line != "" {
		lines = append(lines, "", line)
	}
	if runtime.Problem != "" && !IsWindows() {
		lines = append([]string{"Missing: " + runtime.Problem, ""}, lines...)
	}
	return lines
}

// renderWindowsAppsPanel draws the launcher or its setup page. Row hits are
// rebuilt every frame so mouse and touch cannot drift from the visible list.
func renderWindowsAppsPanel(renderer *sdl.Renderer, config *Config) {
	if !winAppsPanel.open || renderer == nil {
		return
	}
	// A launch that failed on its goroutine is shown here, on the main thread,
	// and the app a finished setup chain was asked for starts here.
	windowsAppsShowFailure()
	windowsAppsStartReady()
	entries := windowsAppEntries()
	runtime := windowsRuntimeStatusCached()
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = OverlayPadX
	rowH := 8 + medH + 4 + smallH + 8

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height. The Setup pill
	// sits on the title line opposite it, so the subtitle no longer has to reserve
	// room for the pill by hand.
	title := "Windows Apps"
	if winAppsPanel.help {
		title = "Windows Apps · Setup"
	}
	header := UIPanelHeader(title, windowsAppsSummary(entries, runtime), "apps")
	if !runtime.ready() {
		header.SubtitleColor = WarningInk()
	}
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := 20 + smallH

	panelW := int32(900)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}

	var bodyH int32
	var visible int32
	// Only the built-in Wine test in the list: the lines an empty list has
	// (how to add programs, the sample download) follow its row.
	starter := !IsWindows() && windowsAppsOnlyBuiltIn(entries)
	starterH := 10 + 4*(smallH+4)
	setupLines := windowsAppsSetupLines(runtime)
	if winAppsPanel.help && winAppsPanel.notice != "" {
		// The notice answers "why did pressing A open this page"; it sits above
		// the detected runtime so it is read before the diagnostics.
		setupLines = append([]string{winAppsPanel.notice, ""}, setupLines...)
	}
	if winAppsPanel.help {
		bodyH = rowH*int32(windowsSetupRowCount) + 12 + int32(len(setupLines))*(smallH+4) + 12
	} else if len(entries) == 0 {
		bodyH = 18 + medH + 4 + smallH + 4 + smallH + 4 + smallH + 18
		if !IsWindows() {
			// The sample program's line.
			bodyH += smallH + 4
		}
	} else {
		maxRows := (screenHeight - 40 - headerH - footerH) / rowH
		if maxRows < 3 {
			maxRows = 3
		}
		visible = int32(len(entries))
		if visible > maxRows {
			visible = maxRows
		}
		bodyH = visible * rowH
		if starter {
			bodyH += starterH
		}
	}
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	windowsAppPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if !winAppsPanel.help && len(entries) > 0 {
		if winAppsPanel.cursor < winAppsPanel.scroll {
			winAppsPanel.scroll = winAppsPanel.cursor
		}
		if winAppsPanel.cursor >= winAppsPanel.scroll+int(visible) {
			winAppsPanel.scroll = winAppsPanel.cursor - int(visible) + 1
		}
		if winAppsPanel.scroll > len(entries)-int(visible) {
			winAppsPanel.scroll = len(entries) - int(visible)
		}
		if winAppsPanel.scroll < 0 {
			winAppsPanel.scroll = 0
		}
	} else {
		winAppsPanel.scroll = 0
	}

	drawOverlayScrim(renderer)

	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)
	// Only the empty list has the sample line; the other views drop its tap
	// target, so a tap where it was does not start a download.
	windowsSampleLineRect = sdl.Rect{}

	if winAppsPanel.help {
		windowsAppRowHits = nil
		windowsSetupButtonRect = sdl.Rect{}
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		if setupCursor < 0 || setupCursor >= windowsSetupRowCount {
			setupCursor = windowsSetupRowWine
		}
		rows := windowsAppsSetupRows(runtime)
		y := panelY + headerH
		for i, row := range rows {
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			windowsSetupRowHit[i] = r
			focused := i == setupCursor
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			if focused {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}
			renderText(renderer, config, medium, row.Title, ColorTextPrimary(), r.X+16, r.Y+8)
			statusColor := ToastSuccessInk()
			switch {
			case row.Bad:
				statusColor = ToastErrorInk()
			case row.Status == "installing" || row.Status == "update" || row.Status == "downloading":
				statusColor = WarningInk()
			case row.Status == "optional" || row.Status == "not needed" || row.Status == "off":
				statusColor = ColorTextTertiary()
			}
			sw, _, _ := font.SizeUTF8(row.Status)
			renderText(renderer, config, font, row.Status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)
			renderText(renderer, config, font, ellipsize(font, row.Detail, r.W-32),
				ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
			y += rowH
		}
		y += 12
		for _, line := range setupLines {
			if y+smallH > panelY+panelH-footerH {
				break
			}
			color := ColorTextSecondary()
			switch {
			case line != "" && line == winAppsPanel.notice:
				color = WarningInk()
			case strings.HasPrefix(line, "Missing:"):
				color = ToastErrorInk()
			case strings.HasPrefix(line, "Installer:") && strings.Contains(line, "failed"):
				color = ToastErrorInk()
			case strings.HasPrefix(line, "Installer:"):
				color = WarningInk()
			case strings.HasPrefix(line, "Runtime:") || strings.HasPrefix(line, "Windows folder:") ||
				strings.HasPrefix(line, "Display:") || strings.HasPrefix(line, "DXVK:") ||
				strings.HasPrefix(line, "Vulkan:"):
				color = ColorTextTertiary()
			}
			if line != "" {
				renderText(renderer, config, font, ellipsize(font, line, panelW-2*padX),
					color, panelX+padX, y)
			}
			y += smallH + 4
		}
	} else if len(entries) == 0 {
		windowsAppRowHits = nil
		windowsAppsSetupPill(renderer, config, font, panelX, panelY, panelW)
		y := panelY + headerH
		renderText(renderer, config, medium, "No Windows apps yet", ColorTextPrimary(), panelX+padX, y)
		y += medH + 4
		copyLine := "Copy Windows apps into:"
		if !IsWindows() {
			// A downloads the sample program; the line says so, or how its
			// download goes, and a tap on it is A.
			text, busy, failed := windowsSampleEmptyLine("A")
			color := accentColor
			switch {
			case busy:
				color = WarningInk()
			case failed:
				color = ToastErrorInk()
			}
			text = ellipsize(font, text, panelW-2*padX)
			w, _, _ := font.SizeUTF8(text)
			windowsSampleLineRect = sdl.Rect{X: panelX + padX, Y: y - 2, W: int32(w), H: smallH + 4}
			renderText(renderer, config, font, text, color, panelX+padX, y)
			y += smallH + 4
			copyLine = "or copy Windows apps into:"
		}
		renderText(renderer, config, font, copyLine, ColorTextSecondary(), panelX+padX, y)
		y += smallH + 4
		for i, root := range windowsAppRootsCached() {
			if i >= 2 {
				break
			}
			renderText(renderer, config, font, ellipsize(font, root, panelW-2*padX),
				accentColor, panelX+padX, y)
			y += smallH + 4
		}
		then := "then press " + hintKeyFor("Start", "Rescan") + " to rescan, or " + hintKeyFor("X", "Setup") + " for runtime setup."
		if IsWindows() {
			then = "then press " + hintKeyFor("Start", "Rescan") + " to rescan."
		}
		renderText(renderer, config, font, then, ColorTextTertiary(), panelX+padX, y)
	} else {
		windowsAppsSetupPill(renderer, config, font, panelX, panelY, panelW)
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		windowsAppRowHits = make([]windowsAppRowHit, 0, visible)
		for i := int32(0); i < visible; i++ {
			idx := winAppsPanel.scroll + int(i)
			entry := entries[idx]
			y := panelY + headerH + i*rowH
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			windowsAppRowHits = append(windowsAppRowHits, windowsAppRowHit{index: idx, rect: r})

			focused := idx == winAppsPanel.cursor
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			if focused {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}

			renderText(renderer, config, medium, entry.Title, ColorTextPrimary(), r.X+16, r.Y+8)
			status := windowsAppStatus(entry, runtime)
			statusColor := ToastSuccessInk()
			switch status {
			case "Unavailable":
				statusColor = ToastErrorInk()
			case "Running":
				statusColor = accentColor
			case "Starting…":
				statusColor = WarningInk()
			}
			sw, _, _ := font.SizeUTF8(status)
			renderText(renderer, config, font, status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)

			// The second line: what a starting app is doing (the first launch
			// prepares Windows for minutes), else where it is and, for an
			// OpenGL game, its display.
			detail := windowsAppDetail(entry)
			detailColor := ColorTextTertiary()
			if phase := wineLaunchPhase(entry.Path); phase != "" {
				detail, detailColor = phase, WarningInk()
			} else {
				if !IsWindows() && windowsAppOpenGL(entry.Path) {
					detail += " · OpenGL"
				}
				if !runtime.ready() {
					detailColor = ToastErrorInk()
				}
			}
			renderText(renderer, config, font, ellipsize(font, detail, r.W-32),
				detailColor, r.X+16, r.Y+8+medH+4)
		}
		if starter {
			windowsAppsDrawStarterLines(renderer, config, font, panelX+padX, panelY+headerH+visible*rowH+2, panelW-2*padX, smallH)
		}
	}

	backLabel := "Back"
	if winAppsPanel.returnEmu {
		backLabel = "Emulators"
	}
	var hint string
	switch {
	case winAppsPanel.help && winAppsPanel.pending != nil:
		hint = hintLine(FooterHint{"A", "Install & Run"}, FooterHint{"B", "Cancel"})
	case winAppsPanel.help:
		hint = hintLine(FooterHint{"A", "Install"}, FooterHint{"B", "Back"})
	case IsWindows() && len(entries) == 0:
		hint = hintLine(FooterHint{"Start", "Rescan"}, FooterHint{"B", backLabel})
	case IsWindows():
		hint = hintLine(FooterHint{"A", "Launch"}, FooterHint{"Start", "Rescan"}, FooterHint{"B", backLabel})
	case len(entries) == 0:
		hint = hintLine(FooterHint{"A", "Get Notepad++"}, FooterHint{"X", "Setup"}, FooterHint{"Start", "Rescan"}, FooterHint{"B", backLabel})
	case starter:
		hint = hintLine(FooterHint{"A", "Launch"}, FooterHint{"Y", "Get Notepad++"}, FooterHint{"X", "Setup"},
			FooterHint{"Start", "Rescan"}, FooterHint{"B", backLabel})
	default:
		hint = hintLine(FooterHint{"A", "Launch"}, FooterHint{"Y", "Display"}, FooterHint{"X", "Setup"},
			FooterHint{"Start", "Rescan"}, FooterHint{"B", backLabel})
	}
	if pkgHubEnabled() && tbxCurrentScene(config) == "Packages" && !winAppsPanel.returnEmu && !winAppsPanel.help {
		hint = "L2/R2 Stores   " + hint
	}
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*padX), ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
