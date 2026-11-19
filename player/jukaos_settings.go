package main

// jukaos_settings.go — JukaHub OS in the interface: the Settings section, the
// power card's "Return to <firmware>", the escape hatch at boot, the health
// clock of the OS guard and the teardown step. The session itself is
// jukaos_linux.go; the design is in jukaos.go.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// jukaosSettingsSection is the Settings section's id.
const jukaosSettingsSection = "jukaos"

// jukaosEscapeSamples × jukaosEscapeEvery is how long Select or B must still
// be down when the boot animation ends: a hold, not a stray press.
const (
	jukaosEscapeSamples = 6
	jukaosEscapeEvery   = 50 * time.Millisecond
	// jukaosReturnArm is how long the "Return to …" row waits for its second
	// press.
	jukaosReturnArm = 4 * time.Second
)

// jukaosUI is the Settings section's copy of os.json, read when the section
// is built: the rows' Get functions run every frame and must not read the
// card. Main thread.
var jukaosUI struct {
	state     jukaosState
	layers    []jukaosLayer
	kernel    string
	stockOnce bool
}

// jukaosFirmwareName is the firmware's own menu as the user knows it.
var jukaosFirmwareName = sync.OnceValue(func() string {
	root := jukaosSDRootOf(jukaosAppDir())
	if root == "" {
		root = "/mnt/SDCARD"
	}
	switch {
	case dirExists(filepath.Join(filepath.FromSlash(root), ".system")):
		return "NextUI"
	case dirExists(filepath.Join(filepath.FromSlash(root), "System", "usr", "trimui")):
		return "CrossMix"
	case dirExists(filepath.Join(filepath.FromSlash(root), "spruce")):
		// spruceOS (Smart Pro, Smart Pro S, Brick) keeps its scripts there.
		return "spruceOS"
	}
	return "TrimUI"
})

// jukaosFilterSections drops the JukaHub OS section where it cannot run (a
// desktop, a Linux that is not a TrimUI). setSections calls it once.
func jukaosFilterSections(sections []setSection) []setSection {
	if jukaosSupported() {
		return sections
	}
	out := make([]setSection, 0, len(sections))
	for _, s := range sections {
		if s.ID != jukaosSettingsSection {
			out = append(out, s)
		}
	}
	return out
}

// jukaosExitLabel is the power card's Exit button: in an OS session leaving
// JukaHub is going back to the firmware's menu, and says so.
func jukaosExitLabel(fallback string) string {
	if jukaosNativeActive() {
		return "Power off"
	}
	if jukaosSessionActive() {
		return "Return to " + jukaosFirmwareName()
	}
	return fallback
}

// jukaosEscapeHeld samples Select and B on the built-in pad.
func jukaosEscapeHeld() bool {
	sdl.PumpEvents()
	if sdl.NumJoysticks() <= 0 {
		return false
	}
	pad := sdl.GameControllerOpen(0)
	if pad == nil {
		return false
	}
	defer pad.Close()
	return pad.Button(sdl.CONTROLLER_BUTTON_BACK) != 0 || pad.Button(sdl.CONTROLLER_BUTTON_B) != 0
}

// jukaosEscapeCheck is the escape hatch, run as the boot animation ends
// (main.go): at a power-on into JukaHub OS, Select or B still held means the
// stock menu for this boot. The rest of the boot is the stock system's (a
// JukaHub opened from the menu runs as the app), the session's changes are
// undone by the teardown, and JukaHub quits to the firmware. Main thread.
func jukaosEscapeCheck() {
	if !jukaosSessionActive() || !bootOSPowerOnActive() {
		return
	}
	// With the startup lock up, a held button must not leave for the stock
	// menu (which has no lock): unlock first, then "Return to …" (bootlock.go).
	if startupLocked() {
		if jukaosEscapeHeld() {
			logInfo("jukaos", "Select/B held through the boot, but the startup lock is up: staying")
		}
		return
	}
	for i := 0; i < jukaosEscapeSamples; i++ {
		if !jukaosEscapeHeld() {
			return
		}
		time.Sleep(jukaosEscapeEvery)
	}
	logWarn("jukaos", "Select/B held through the boot: the stock system for this boot")
	jukaosMarkStockOnce()
	ShowToast("Starting "+jukaosFirmwareName()+" for this boot", ToastKindInfo)
	requestAppExit()
}

// jukaosFirstFrame runs with the first frame (main.go): the OS guard's
// health clock starts (a start with layers that keeps running for
// bootGuardHealthyAfter clears the count), a start the guard ran without
// layers says so once, and the page cache is freed for the games.
func jukaosFirstFrame() {
	// With the boot animation off (the default) the startup chime rises with
	// Home instead (bootchime.go); every platform, not only JukaHub OS.
	bootChimeOverHome()
	if jukaosNativeActive() {
		logInfo("jukaos", "standalone first frame at %s seconds since boot", jukaosNativeBootSeconds())
		// Home's picture is kept for the next power-on (bootframe.go).
		bootFrameStart()
	}
	jukaosLive.mu.Lock()
	session, guarded := jukaosLive.session, jukaosLive.guarded
	if session && len(jukaosLive.layers) > 0 && !guarded && jukaosLive.healthy == nil {
		jukaosLive.healthy = time.AfterFunc(bootGuardHealthyAfter, jukaosMarkHealthy)
	}
	jukaosLive.mu.Unlock()
	if !session {
		return
	}
	appDir := jukaosAppDir()
	if guarded {
		if g := jukaosLoadGuard(appDir); !g.Told {
			ShowToast("JukaHub OS started without its layers: the last two starts with them failed. Turn the faulty layer off in Settings → JukaHub OS.", ToastKindWarning)
			g.Told = true
			go func() { _ = jukaosSaveGuard(appDir, g) }()
		}
	}
	go func() {
		if jukaosLoadState(appDir).jukaosDropCachesOn() {
			jukaosDropCaches()
		}
	}()
}

// jukaosMarkHealthy clears the OS guard's count: the layers work.
func jukaosMarkHealthy() {
	appDir := jukaosAppDir()
	if g := jukaosLoadGuard(appDir); g.Failed != 0 || g.Told {
		if err := jukaosSaveGuard(appDir, jukaosGuardState{}); err != nil {
			logWarn("jukaos", "OS guard not cleared: %v", err)
		}
	}
}

// jukaosResetGuard gives the layers a fresh start after the user changed
// them.
func jukaosResetGuard(appDir string) {
	if err := jukaosSaveGuard(appDir, jukaosGuardState{}); err != nil {
		logWarn("jukaos", "OS guard not reset: %v", err)
	}
}

// jukaosShutdownStepAdd is the teardown's row for an OS session, and the
// standalone image's boot-frame row (shutdown.go appends them to the "Closing
// caches" group, before the sync); steps unchanged otherwise.
func jukaosShutdownStepAdd(steps []shutdownStep) []shutdownStep {
	// The standalone image keeps the last settled Home for the next power-on,
	// taken before the power card and this card were drawn (bootframe.go).
	if jukaosNativeActive() && bootFrameDirty() && !bootFrameLockOn(appConfig) {
		// Longer on the Smart Pro image, which writes the bootloader's
		// picture after it (bootframe_ubootlogo.go).
		steps = append(steps, shutdownStep{label: "Keeping Home for the next start", group: 2, timeout: bootFrameShutdownWait(appConfig), fn: bootFrameShutdownStep})
	}
	if step, ok := bootFrameUbootShutdownStep(appConfig); ok {
		steps = append(steps, step)
	}
	if !jukaosSessionActive() {
		return steps
	}
	return append(steps, shutdownStep{label: "Undoing JukaHub OS changes", group: 2, timeout: 1500 * time.Millisecond, fn: jukaosShutdownStep})
}

// jukaosShutdownStep undoes the session's drivers and values. A start that
// reached a deliberate quit was a healthy one, as for the boot guard.
func jukaosShutdownStep() (string, error) {
	report := jukaosRevert("quit")
	jukaosLive.mu.Lock()
	layered := len(jukaosLive.layers) > 0 && !jukaosLive.guarded
	if jukaosLive.healthy != nil {
		jukaosLive.healthy.Stop()
	}
	jukaosLive.mu.Unlock()
	if layered {
		jukaosMarkHealthy()
	}
	if len(report) == 0 {
		return "nothing to undo", nil
	}
	return fmt.Sprintf("%d change(s) undone", len(report)), nil
}

// ── Settings ───────────────────────────────────────────────────────────────

// jukaosSettingsRows builds the section from the card (the hub calls it when
// the section opens): status, the mode, booting into it, starting it now,
// one switch per layer, where layers go, and the way back.
func jukaosSettingsRows(*Config) []setRow {
	if jukaosNativeActive() {
		rows := []setRow{{
			ID: "jukaosStatus", Label: "Status", Kind: setKindInfo,
			Value:  setStatic("Standalone OS"),
			Detail: setStatic(jukaosNativeBaseLabel() + " · kernel " + setOr(jukaosKernel(), "unknown")),
		}, jukaosBootTimeRow(startupBootSummary())}
		if bootFrameUbootSupported() {
			// Smart Pro image: Home as the bootloader's picture.
			rows = append(rows, bootFrameUbootSettingsRow())
		}
		return rows
	}
	appDir := jukaosAppDir()
	jukaosUI.state = jukaosLoadState(appDir)
	jukaosUI.layers = append(jukaosLoadLayers(jukaosHostFor(appDir), jukaosLayersDir(appDir)), jukaosBuiltinLayer(appDir))
	jukaosUI.kernel = setOr(jukaosKernel(), "unknown")
	jukaosUI.stockOnce = jukaosStockOnce()
	session := jukaosSessionActive()
	fw := jukaosFirmwareName()

	rows := []setRow{
		{
			ID: "jukaosStatus", Label: "Status", Kind: setKindInfo,
			Value:  func(*Config) string { return jukaosStatusValue() },
			Detail: func(*Config) string { return jukaosStatusDetail() },
		},
		{
			ID: "jukaosMode", Label: "JukaHub OS mode", Kind: setKindToggle,
			Detail: setStatic("JukaHub runs as the handheld's OS, with the layers below. Nothing on the system changes for good; a reboot is always the stock system. Hold Select or B through the boot animation to start " + fw + " instead, once."),
			Get:    func(*Config) bool { return jukaosUI.state.Enabled },
			Set:    func(_ *Config, on bool) { jukaosSetMode(on) },
		},
		{
			ID: "jukaosBoot", Label: "Boot into JukaHub OS", Kind: setKindToggle,
			Detail: setStatic("Turn the handheld on straight into JukaHub OS (also switches on Start JukaHub at login)"),
			Get:    func(c *Config) bool { return jukaosUI.state.Enabled && autoStartEnabled(c) },
			Set:    jukaosSetBoot,
		},
		{
			ID: "jukaosFastBoot", Label: "Skip the stock menu at boot", Kind: setKindToggle,
			Detail: func(c *Config) string {
				// Settings → General's switch of the same name holds the
				// menu for every boot, JukaHub OS's too (autostart.go).
				if !autoStartSkipMenuOn(c) {
					return "Off in Settings → General → Skip the stock menu at boot, which keeps " + fw + "'s menu at every boot"
				}
				return "At power-on the firmware starts JukaHub OS in place of its menu, so " + fw + "'s menu never shows. Off: the menu opens for a moment and is closed (try this if Wi-Fi, brightness or volume are not set up after a boot)"
			},
			Disabled: func(c *Config) bool { return !jukaosUI.state.Enabled || !autoStartEnabled(c) || !autoStartSkipMenuOn(c) },
			Get:      func(*Config) bool { return jukaosUI.state.jukaosFastBootOn() },
			Set:      func(_ *Config, on bool) { jukaosSetFastBoot(on) },
		},
	}
	if summary := startupBootSummary(); summary != "" {
		rows = append(rows, jukaosBootTimeRow(summary))
	}
	start := setRow{
		ID: "jukaosStart", Label: "Start OS session now", Kind: setKindAction,
		Detail: setStatic("Restart JukaHub as JukaHub OS with the layers switched on"),
		Open:   jukaosStartNow,
	}
	if session {
		start.Label = "Restart JukaHub OS"
		start.Detail = setStatic("Apply layer changes: JukaHub OS starts again")
	}
	rows = append(rows, start)
	if session {
		rows = append(rows, setRow{
			ID: "jukaosApplied", Label: "Applied at this start", Kind: setKindInfo,
			Value:  func(*Config) string { return jukaosAppliedValue() },
			Detail: func(*Config) string { return jukaosAppliedDetail() },
		})
	}
	for i := range jukaosUI.layers {
		rows = append(rows, jukaosLayerRow(jukaosUI.layers[i]))
	}
	rows = append(rows, setRow{
		ID: "jukaosAddLayer", Label: "Add a layer", Kind: setKindInfo,
		Detail: setStatic("Copy a layer folder (with its layer.json) into os/layers in JukaHub's folder, then open this page again"),
		Value:  setStatic("Apps/JukaHub/os/layers"),
	})
	if session {
		rows = append(rows, setRow{
			ID: "jukaosReturn", Label: "Return to " + fw, Kind: setKindAction,
			Detail: setStatic("Undo the session's changes and go back to " + fw + " (press A twice)"),
			Open:   jukaosReturnPress,
		})
	}
	return rows
}

// jukaosBootTimeRow shows how long the last boot took to reach Home, as the
// start-up measured it (startupBootSummary, e.g. "Power to Home 1.84 s ·
// system 1.21 s · JukaHub 0.63 s"): its first part as the value, all of it
// underneath. Read once, when the section opens.
func jukaosBootTimeRow(summary string) setRow {
	summary = strings.TrimSpace(summary)
	value, detail := "Not measured yet", "Measured at the next start"
	if summary != "" {
		value, _, _ = strings.Cut(summary, " · ")
		detail = summary
	}
	if jukaosNativeActive() {
		detail += " · details in .userdata/jukahub/boot.log"
	}
	return setRow{
		ID: "jukaosBootTime", Label: "Last boot", Kind: setKindInfo,
		Value:  setStatic(value),
		Detail: setStatic(detail),
	}
}

// jukaosLayerRow is one layer's switch. A layer that cannot be used here is
// greyed out with the reason, unless it is on (so it can still be turned off).
func jukaosLayerRow(l jukaosLayer) setRow {
	id := l.ID
	name := setOr(l.Spec.Name, id)
	detail := l.Spec.Description
	if l.Spec.Version != "" {
		detail = strings.TrimSpace("v" + strings.TrimPrefix(l.Spec.Version, "v") + " · " + detail)
	}
	switch {
	case l.Elsewhere:
		detail = "Not for this handheld: " + l.Problem
	case l.Problem != "":
		detail = "Can't use: " + l.Problem
	case len(l.Notes) == 1:
		detail = setOr(detail, name) + " (skipped: " + l.Notes[0] + ")"
	case len(l.Notes) > 1:
		detail = setOr(detail, name) + fmt.Sprintf(" (%d parts skipped, first: %s)", len(l.Notes), l.Notes[0])
	}
	if l.Problem == "" && l.Absent > 0 {
		detail = setOr(detail, name) + fmt.Sprintf(" · %d of its values are not on this handheld", l.Absent)
	}
	problem := l.Problem != ""
	return setRow{
		ID: "jukaosLayer:" + id, Label: name, Kind: setKindToggle,
		Detail:   setStatic(setOr(detail, "No description")),
		Disabled: func(*Config) bool { return problem && !jukaosUI.state.Layers[id] },
		Get:      func(*Config) bool { return jukaosUI.state.Layers[id] },
		Set:      func(_ *Config, on bool) { jukaosSetLayer(id, name, on) },
	}
}

// jukaosStatusValue is the status row's read-out.
func jukaosStatusValue() string {
	switch {
	case jukaosSessionActive():
		return "OS session"
	case jukaosUI.stockOnce:
		return "Stock for this boot"
	case jukaosUI.state.Enabled:
		return "OS from next start"
	}
	return "App mode"
}

// jukaosStatusDetail is the status row's second line: what the session
// applied, and the kernel the drivers have to match. Outside a session with
// OS mode on it says why this start is the app.
func jukaosStatusDetail() string {
	kernel := jukaosUI.kernel
	jukaosLive.mu.Lock()
	session, guarded := jukaosLive.session, jukaosLive.guarded
	layers, modules, drivers := len(jukaosLive.layers), jukaosLive.modules, jukaosLive.drivers
	values, applied, why := jukaosLive.values, jukaosLive.applied, jukaosLive.why
	info := jukaosLive.info
	driverLayers, driverValues := jukaosLive.driverLayers, jukaosLive.driverValues
	jukaosLive.mu.Unlock()
	if !session {
		prefix := "JukaHub runs as an app"
		if jukaosUI.state.Edition == jukaosEditionOS {
			prefix = "JukaHub OS edition, running as an app this start"
		}
		if why != "" {
			prefix += " (" + why + ")"
		}
		return prefix + " · kernel " + kernel
	}
	driverPart := fmt.Sprintf("%d of %d driver(s) loaded", modules, drivers)
	switch {
	case !applied:
		driverPart = "drivers and values being applied…"
	case len(driverLayers) > 0:
		// A driver layer for this handheld: the firmware's drivers, set up
		// by the layer (and any modules it brings for this kernel).
		driverPart = "Drivers: " + jukaosDriverSummary(driverLayers, driverValues)
		if drivers > 0 {
			driverPart += fmt.Sprintf(", %d of %d module(s) loaded", modules, drivers)
		}
	case drivers == 0:
		// No layer brings a driver: nothing newer than the firmware's is
		// running, and the line must not suggest otherwise.
		driverPart = "no driver layer (the firmware's own drivers)"
	}
	parts := []string{
		fmt.Sprintf("%d layer(s)", layers),
		fmt.Sprintf("%d file(s) replaced", info.Binds),
		fmt.Sprintf("%d value(s) set", values),
		driverPart,
		"kernel " + kernel,
	}
	line := strings.Join(parts, " · ")
	switch {
	case guarded:
		line = "Layers paused after two failed starts · " + line
	case info.NamespaceErr != "":
		line += " · file layers off: " + info.NamespaceErr
	case len(info.Skipped) > 0:
		line += fmt.Sprintf(" · %d part(s) skipped", len(info.Skipped))
	}
	return line
}

// jukaosDriverSummary is "A523 driver layer (3 values)": the applied driver
// layers by name and the values they have in force.
func jukaosDriverSummary(layers []string, values int) string {
	return fmt.Sprintf("%s (%d value(s))", strings.Join(layers, ", "), values)
}

// jukaosDriverStatus is the session's driver line for other screens (the
// device's driver list): "" outside a session or before the apply finished,
// else what the status row says after "Drivers:".
func jukaosDriverStatus() string {
	jukaosLive.mu.Lock()
	defer jukaosLive.mu.Unlock()
	switch {
	case !jukaosLive.session || !jukaosLive.applied:
		return ""
	case len(jukaosLive.driverLayers) > 0:
		return jukaosDriverSummary(jukaosLive.driverLayers, jukaosLive.driverValues)
	}
	return "no driver layer (the firmware's own drivers)"
}

// The Drivers & hardware page's JukaHub OS row: what the session's driver
// layers applied, or that this start is the app on the firmware's drivers.
func init() {
	registerDriverStatus("JukaHub OS", func() driverStatus {
		if !jukaosSupported() {
			// Windows, a desktop Linux: no JukaHub OS and no firmware.
			return driverStatus{State: driverNotUsed}
		}
		if s := jukaosDriverStatus(); s != "" {
			return driverStatus{State: driverOK, Driver: "JukaHub OS session", Detail: s}
		}
		return driverStatus{State: driverNotUsed, Detail: "app mode: the firmware's own drivers"}
	})
}

// jukaosAppliedValue is the "Applied at this start" read-out: the layers in
// force, by name.
func jukaosAppliedValue() string {
	jukaosLive.mu.Lock()
	ids := append([]string(nil), jukaosLive.layers...)
	guarded := jukaosLive.guarded
	jukaosLive.mu.Unlock()
	if guarded {
		return "No layers (OS guard)"
	}
	if len(ids) == 0 {
		return "No layers"
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		for _, l := range jukaosUI.layers {
			if l.ID == id {
				name = setOr(l.Spec.Name, id)
				break
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// jukaosAppliedDetail is what the session start and the background apply
// left out, and why (errors.txt has every line as "jukaos:").
func jukaosAppliedDetail() string {
	jukaosLive.mu.Lock()
	skipped := append(append([]string(nil), jukaosLive.info.Skipped...), jukaosLive.notes...)
	nsErr := jukaosLive.info.NamespaceErr
	jukaosLive.mu.Unlock()
	if nsErr != "" {
		skipped = append([]string{"file layers off: " + nsErr}, skipped...)
	}
	if len(skipped) == 0 {
		return "Everything the layers switched on asked for is in force"
	}
	line := "Left out: " + strings.Join(skipped[:min(len(skipped), 3)], "; ")
	if len(skipped) > 3 {
		line += fmt.Sprintf(" (+%d more in errors.txt)", len(skipped)-3)
	}
	return line
}

// jukaosSetFastBoot switches skipping the stock menu at power-on (the boot
// hook reads os.json at every boot).
func jukaosSetFastBoot(on bool) {
	s := jukaosUI.state
	if on {
		s.FastBoot = nil
	} else {
		off := false
		s.FastBoot = &off
	}
	if !jukaosSaveUI(s) {
		return
	}
	if on {
		setToast("From the next power-on JukaHub OS starts without the stock menu")
	} else {
		setToast("From the next power-on the stock menu opens first and is then closed")
	}
}

// jukaosSaveUI writes the section's state; false (with a toast) on failure.
func jukaosSaveUI(s jukaosState) bool {
	if err := jukaosSaveState(jukaosAppDir(), s); err != nil {
		ShowToast("JukaHub OS: could not save ("+err.Error()+")", ToastKindError)
		return false
	}
	jukaosUI.state = s
	return true
}

// jukaosSetMode switches OS mode on or off for the next start.
func jukaosSetMode(on bool) {
	s := jukaosUI.state
	s.Enabled = on
	if !jukaosSaveUI(s) {
		return
	}
	if on {
		setToast("JukaHub OS is on from the next start (or Start OS session now)")
	} else {
		setToast("JukaHub OS is off: JukaHub starts as an app from the next start")
	}
}

// jukaosSetBoot drives the login setting: on turns OS mode and "Start
// JukaHub at login" on together; off only stops the start at login (OS mode
// stays for starts from the menu).
func jukaosSetBoot(c *Config, on bool) {
	if on && !jukaosUI.state.Enabled {
		s := jukaosUI.state
		s.Enabled = true
		if !jukaosSaveUI(s) {
			return
		}
	}
	setStoreBool(c, "autoStart", on)
	widgetToggleSideEffect(c, "autoStart", on)
	if on {
		setToast("The handheld will start into JukaHub OS")
	}
}

// jukaosSetLayer switches one layer for the next OS start. Any change gives
// the layers a fresh start with the OS guard.
func jukaosSetLayer(id, name string, on bool) {
	s := jukaosUI.state
	layers := map[string]bool{}
	for k, v := range s.Layers {
		if v {
			layers[k] = true
		}
	}
	if on {
		layers[id] = true
	} else {
		delete(layers, id)
	}
	s.Layers = layers
	if !jukaosSaveUI(s) {
		return
	}
	jukaosResetGuard(jukaosAppDir())
	when := "at the next JukaHub OS start"
	if jukaosSessionActive() {
		when = "after Restart JukaHub OS"
	}
	setToast(name + ": " + strings.ToLower(setOnOff(on)) + " " + when)
}

// jukaosStartNow turns OS mode on and restarts into it through launch.sh;
// where launch.sh does not run JukaHub, it takes effect at the next start.
func jukaosStartNow(*Config) {
	s := jukaosUI.state
	s.Enabled = true
	if !jukaosSaveUI(s) {
		return
	}
	jukaosClearStockOnce()
	jukaosResetGuard(jukaosAppDir())
	if recoveryRestartKind() == "launcher" && recoveryRestartApp() {
		setToast("Starting JukaHub OS…")
		return
	}
	setToast("JukaHub OS starts the next time JukaHub is opened")
}

// jukaosReturnPress is the "Return to …" row: the first press arms it, a
// second within jukaosReturnArm quits to the firmware (the teardown undoes
// the session's changes).
func jukaosReturnPress(*Config) {
	fw := jukaosFirmwareName()
	jukaosLive.mu.Lock()
	armed := !jukaosLive.escapeArm.IsZero() && time.Since(jukaosLive.escapeArm) < jukaosReturnArm
	if armed {
		jukaosLive.escapeArm = time.Time{}
	} else {
		jukaosLive.escapeArm = time.Now()
	}
	jukaosLive.mu.Unlock()
	if !armed {
		setToast("Press A again to return to " + fw)
		return
	}
	setToast("Returning to " + fw + "…")
	requestAppExit()
}
