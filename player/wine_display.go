package main

// wine_display.go — the display half of a Windows-program launch.
//
// planWineRuntime (wine_runtime.go) returns the command that runs a program:
// Box64 + Wine on the handheld, Wine on a Linux desktop, the .exe itself on
// Windows. On a stock TrimUI nothing it starts has anywhere to draw: Wine's
// winex11.drv needs an X server, and the firmware has no X11, no Wayland and no
// DRM (SDL draws straight to the framebuffer). This file puts a display around
// the command:
//
//   - a Linux desktop session (DISPLAY or WAYLAND_DISPLAY) is used as it is;
//   - on the handheld, WestonPack (PortMaster's weston_pkg runtime) starts
//     Weston with Xwayland and shows it through Crusty, its SDL2 presenter.
//     JukaHub runs westonwrap.sh the way a PortMaster port does: the SquashFS
//     image mounted read-only (an unpacked copy is used as it is), then
//     `westonwrap.sh <backend> <renderer> <shell> <gl> [NAME=VALUE...] <program>
//     [args]`, then `westonwrap.sh cleanup` and the unmount once it returned;
//   - on Windows the program opens its own windows: nothing goes around it.
//
// Two WestonPack modes are offered, both from the Westonpack wiki's examples:
//
//	weston      drm gl kiosk system                  programs that draw with
//	                                                 Xlib/GDI (the default)
//	weston-gl   headless noop kiosk crusty_glx_gl4es OpenGL 2 games (GLX on
//	                                                 GLES through gl4es)
//
// WestonPack hands the program only the NAME=VALUE words before it, so the
// program's whole environment travels that way; the wrapper's own environment
// carries what westonwrap.sh reads itself (PortMaster's device variables,
// CRUSTY_SHOW_CURSOR, the headless size).
//
// The firmware's udev has no input_id rule, and Weston's libinput ignores every
// input device udev did not tag, after which Weston does not start at all
// ("TRIMUI Player1: not tagged as supported input device", "failed to create
// compositor backend" — NX Redux #140). Before Weston starts, JukaHub adds that
// one rule to udev's runtime rules (RAM only) and re-scans the input devices
// (wine_display_linux.go), the fix NX Redux ships for PortMaster's Weston ports
// on the same firmware.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	windowsDisplayAuto     = "auto"
	windowsDisplayX11      = "x11"
	windowsDisplayWayland  = "wayland"
	windowsDisplayWeston   = "weston"
	windowsDisplayWestonGL = "weston-gl"
	windowsDisplayHeadless = "headless"
	// windowsDisplayDesktop is Windows itself: the program opens its own
	// windows. It is never asked for; resolveWineDisplay answers it there.
	windowsDisplayDesktop = "desktop"

	// westonCleanupLimit bounds `westonwrap.sh cleanup` after a session.
	westonCleanupLimit = 20 * time.Second
	// westonEnvFileName is the optional file of westonwrap.sh settings in
	// data/windows-runtime (westonEnvOverrides).
	westonEnvFileName = "weston.env"
)

var windowsAppDisplayNames = map[string]string{
	windowsDisplayX11:      "X11",
	windowsDisplayWayland:  "Wayland",
	windowsDisplayWeston:   "WestonPack",
	windowsDisplayWestonGL: "WestonPack (OpenGL)",
	windowsDisplayHeadless: "Headless",
	windowsDisplayDesktop:  "Windows desktop",
}

// westonModeWords are westonwrap.sh's first four arguments — backend,
// renderer, shell and GL library — for each WestonPack mode: Weston's own
// desktop on screen for programs that draw with Xlib (Wine's GDI windows), and
// a headless Weston with Crusty's GLX over GLES for OpenGL games.
var westonModeWords = map[string][]string{
	windowsDisplayWeston:   {"drm", "gl", "kiosk", "system"},
	windowsDisplayWestonGL: {"headless", "noop", "kiosk", "crusty_glx_gl4es"},
}

// westonModeOverrideKeys are the weston.env keys that replace a mode's four
// words, for trying another WestonPack mode on a device without a new build.
var westonModeOverrideKeys = map[string]string{
	"JUKAHUB_WESTON_MODE":    windowsDisplayWeston,
	"JUKAHUB_WESTON_GL_MODE": windowsDisplayWestonGL,
}

// wineHost is what display selection looks at, gathered once so it can be
// tested without the process environment or a real WestonPack install.
type wineHost struct {
	getenv func(string) string
	weston westonPackSource
	// westonErr says why WestonPack cannot be used although it is there.
	westonErr string
}

// westonPath is where WestonPack is: the wrapper, or the image to mount.
func (h wineHost) westonPath() string {
	if h.weston.Wrapper != "" {
		return h.weston.Wrapper
	}
	return h.weston.Image
}

func currentWineHost() wineHost {
	host := wineHost{getenv: os.Getenv, weston: findWestonPackSource()}
	if host.weston.Wrapper == "" && host.weston.Image != "" && !IsWindows() && os.Geteuid() != 0 {
		host.westonErr = "WestonPack is a SquashFS image, and mounting it needs root (JukaHub is not running as root)"
	}
	return host
}

// resolveWestonPack is where WestonPack is on this device, without mounting
// anything: an unpacked westonwrap.sh, else the image a launch mounts. It is
// kept for callers that only show the path.
func resolveWestonPack() (string, error) {
	host := currentWineHost()
	if host.westonErr != "" {
		return host.westonPath(), errors.New(host.westonErr)
	}
	return host.westonPath(), nil
}

// resolveWineDisplay picks a display. "auto" prefers a real desktop session,
// then WestonPack. It refuses to pretend that a bare framebuffer is enough for
// a Win32 window. On Windows the answer is always the desktop.
func resolveWineDisplay(requested string, host wineHost) (string, error) {
	if IsWindows() {
		return windowsDisplayDesktop, nil
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" && host.getenv != nil {
		requested = strings.ToLower(strings.TrimSpace(host.getenv("JUKAHUB_WINE_DISPLAY")))
	}
	if requested == "" {
		requested = windowsDisplayAuto
	}

	hasWayland := host.getenv != nil && strings.TrimSpace(host.getenv("WAYLAND_DISPLAY")) != ""
	hasX11 := host.getenv != nil && strings.TrimSpace(host.getenv("DISPLAY")) != ""
	westonMissing := withLaunchFailureKind(launchFailureRuntimeMissing,
		errors.New("no display for Windows programs: install WestonPack from Windows Apps → Setup (or set JUKAHUB_WESTON)"))

	switch requested {
	case windowsDisplayAuto:
		// Prefer X11 when both are present: Wine and Hangover default to
		// winex11, and XWayland usually exports DISPLAY alongside Wayland.
		switch {
		case hasX11:
			return windowsDisplayX11, nil
		case hasWayland:
			return windowsDisplayWayland, nil
		case host.westonErr != "":
			return "", errors.New(host.westonErr)
		case host.westonPath() != "":
			return windowsDisplayWeston, nil
		}
		return "", westonMissing
	case windowsDisplayX11:
		if !hasX11 {
			return "", errors.New("display=x11, but DISPLAY is not set")
		}
		return windowsDisplayX11, nil
	case windowsDisplayWayland:
		if !hasWayland {
			return "", errors.New("display=wayland, but WAYLAND_DISPLAY is not set")
		}
		return windowsDisplayWayland, nil
	case windowsDisplayWeston, windowsDisplayWestonGL:
		if host.westonErr != "" {
			return "", errors.New(host.westonErr)
		}
		if host.westonPath() == "" {
			return "", westonMissing
		}
		return requested, nil
	case windowsDisplayHeadless:
		return windowsDisplayHeadless, nil
	}
	return "", fmt.Errorf("unknown display %q (use auto, x11, wayland, weston, weston-gl or headless)", requested)
}

// ── The display for one launch ───────────────────────────────────────────

// wineDisplayPlan is the display half of one launch: the command to start
// (the program itself, or westonwrap.sh around it), its environment, and what
// to put away once it has exited.
type wineDisplayPlan struct {
	// Mode is a windowsDisplay* value.
	Mode    string
	Command string
	Args    []string
	// Env is the started process's whole environment; nil inherits JukaHub's
	// (only the Windows desktop leaves it nil, and only with nothing to add).
	Env []string
	// Detail is one line for the log: which WestonPack, and where it is.
	Detail string

	wrapper  string // westonwrap.sh, for the clean-up
	mountDir string // where an image is mounted ("" when nothing is)
	unmount  bool   // this launch mounted it, so this launch unmounts it
}

// weston reports a WestonPack session.
func (d wineDisplayPlan) weston() bool {
	return d.Mode == windowsDisplayWeston || d.Mode == windowsDisplayWestonGL
}

// lendsPad reports whether the session turns the pad into a mouse with keys:
// a WestonPack session on the handheld, whose program has no other pointer.
func (d wineDisplayPlan) lendsPad() bool {
	return d.weston() && vcursorBridgeSupported()
}

// command builds the process to start in dir. A batch file on Windows goes
// through cmd.exe the safe way (winbatch.go).
func (d wineDisplayPlan) command(dir string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if d.Mode == windowsDisplayDesktop && windowsBatchFile(d.Command) {
		batch, err := winBatchCommand(d.Command, d.Args...)
		if err != nil {
			return nil, err
		}
		cmd = batch
	} else {
		cmd = exec.Command(d.Command, d.Args...)
	}
	cmd.Dir = dir
	cmd.Env = d.Env
	return cmd, nil
}

// prepareWineDisplay puts the display around plan's command. openGL picks
// WestonPack's OpenGL mode where WestonPack is the display. A WestonPack image
// is mounted here; whatever this sets up is put away by stop and release.
func prepareWineDisplay(request string, openGL bool, plan wineRuntimePlan, log wineLog) (wineDisplayPlan, error) {
	host := currentWineHost()
	mode, err := resolveWineDisplay(request, host)
	if err != nil {
		return wineDisplayPlan{}, err
	}
	if mode == windowsDisplayWeston && openGL {
		mode = windowsDisplayWestonGL
	}
	d := wineDisplayPlan{Mode: mode, Command: plan.Command, Args: append([]string{}, plan.Args...)}
	switch mode {
	case windowsDisplayDesktop:
		if len(plan.Env) > 0 {
			d.Env = mergeGameProfileEnv(os.Environ(), plan.Env)
		}
		return d, nil
	case windowsDisplayWeston, windowsDisplayWestonGL:
		return prepareWestonDisplay(d, host.weston, plan, log)
	case windowsDisplayHeadless:
		// An explicitly headless launch must not pick up a desktop session's
		// display connection.
		env := cloneGameProfileEnv(plan.Env)
		env["DISPLAY"] = ""
		env["WAYLAND_DISPLAY"] = ""
		d.Env = mergeGameProfileEnv(screenChildEnv(os.Environ()), env)
		return d, nil
	}
	d.Env = mergeGameProfileEnv(os.Environ(), plan.Env)
	return d, nil
}

// prepareWestonDisplay wraps the command in westonwrap.sh: the image mounted
// when WestonPack is one, the wrapper checked, the PortMaster-style values set,
// the input devices labelled for Weston's libinput.
func prepareWestonDisplay(d wineDisplayPlan, src westonPackSource, plan wineRuntimePlan, log wineLog) (wineDisplayPlan, error) {
	wrapper := src.Wrapper
	if wrapper == "" {
		dir, mounted, err := wineMountWestonImage(src.Image, log)
		if err != nil {
			return d, err
		}
		d.mountDir, d.unmount = dir, mounted
		wrapper = filepath.Join(dir, "westonwrap.sh")
		d.Detail = src.Image + " mounted at " + dir
	} else {
		d.Detail = wrapper
	}
	fail := func(err error) (wineDisplayPlan, error) {
		d.release(log)
		return d, err
	}
	if !regularFile(wrapper) {
		return fail(errors.New("WestonPack has no westonwrap.sh at " + wrapper))
	}
	if westonWrapperResetsDevices(wrapper) {
		return fail(errors.New("this WestonPack is the preview build that resets the SD card on TrimUI; install WestonPack again from Windows Apps → Setup"))
	}
	d.wrapper = wrapper

	w, h := wineScreenSize()
	overrides, modes := westonEnvOverrides(filepath.Join(windowsRuntimeDir(), westonEnvFileName), log)
	words := westonModeWords[d.Mode]
	if custom := modes[d.Mode]; len(custom) == 4 {
		words = custom
	}
	control, err := westonControlShim(w, h)
	if err != nil {
		log.printf("display: no PortMaster stand-in (%v); westonwrap.sh runs without one\n", err)
		control = ""
	}
	d.Env = westonWrapperEnv(screenChildEnv(os.Environ()), w, h, control, overrides)

	// Crusty draws the pointer itself (CRUSTY_SHOW_CURSOR); the program gets the
	// switch as well, for the mode where Crusty runs inside it.
	appEnv := cloneGameProfileEnv(plan.Env)
	if !hasEnvKey(appEnv, "CRUSTY_SHOW_CURSOR") {
		appEnv["CRUSTY_SHOW_CURSOR"] = "1"
	}
	d.Command = westonShell()
	d.Args = append([]string{wrapper}, westonWrapArgs(words, plan.Command, plan.Args, appEnv)...)
	wineLabelInputDevices(log)
	return d, nil
}

// stop ends what the display left running once the program has exited:
// `westonwrap.sh cleanup`, as every WestonPack port runs it, bounded so a
// clean-up that hangs cannot keep the screen parked.
func (d wineDisplayPlan) stop(log wineLog) {
	if d.wrapper == "" {
		return
	}
	cmd := exec.Command(westonShell(), d.wrapper, "cleanup")
	cmd.Dir = filepath.Dir(d.wrapper)
	cmd.Env = d.Env
	if log.f != nil {
		cmd.Stdout, cmd.Stderr = log.f, log.f
	}
	processGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		log.printf("display: westonwrap.sh cleanup did not start: %v\n", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			log.printf("display: westonwrap.sh cleanup: %v\n", err)
		}
	case <-time.After(westonCleanupLimit):
		log.printf("display: westonwrap.sh cleanup took longer than %s; stopped it\n", westonCleanupLimit)
		_ = screenChildKill(cmd)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// release unmounts the image this launch mounted (lazily when something still
// holds it). A mount someone else made is left alone.
func (d wineDisplayPlan) release(log wineLog) {
	if d.mountDir == "" || !d.unmount {
		return
	}
	wineUnmountWeston(d.mountDir, log)
}

// ── westonwrap.sh's arguments and environment ────────────────────────────

// westonWrapArgs is westonwrap.sh's command line after the script: the four
// mode words, the program's environment as NAME=VALUE words (sorted, so the
// log reads the same every time), then the program and its arguments.
func westonWrapArgs(words []string, command string, args []string, env map[string]string) []string {
	out := append([]string{}, words...)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	out = append(out, command)
	return append(out, args...)
}

// westonWrapperEnv is westonwrap.sh's own environment: base (the firmware's,
// screenChildEnv) plus what a PortMaster port would have set — the device's
// screen and name, ESUDO empty because JukaHub runs as root, PortMaster's
// control folder (control, the stand-in) — and Crusty's software pointer, the
// only pointer a Windows program gets on this screen. overrides (weston.env)
// win. A desktop session's DISPLAY or WAYLAND_DISPLAY is dropped: WestonPack
// starts its own.
func westonWrapperEnv(base []string, w, h int, control string, overrides map[string]string) []string {
	set := map[string]string{
		"CRUSTY_SHOW_CURSOR":     "1",
		"WESTON_HEADLESS_WIDTH":  strconv.Itoa(w),
		"WESTON_HEADLESS_HEIGHT": strconv.Itoa(h),
		"DISPLAY_WIDTH":          strconv.Itoa(w),
		"DISPLAY_HEIGHT":         strconv.Itoa(h),
		// Not "TrimUI": a WestonPack build with TrimUI-only steps (the preview
		// that re-triggers every device) must not take them for JukaHub.
		"CFW_NAME":    "JukaHub",
		"DEVICE_NAME": westonDeviceName(),
		"DEVICE_ARCH": westonArch(),
		"ESUDO":       "",
	}
	if control != "" {
		set["XDG_DATA_HOME"] = control
		set["controlfolder"] = filepath.Join(control, "PortMaster")
	}
	for key, value := range overrides {
		set[key] = value
	}
	kept := make([]string, 0, len(base))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if key == "DISPLAY" || key == "WAYLAND_DISPLAY" {
			continue
		}
		kept = append(kept, kv)
	}
	return mergeGameProfileEnv(kept, set)
}

// westonDeviceName is PortMaster's DEVICE_NAME for this handheld: the model's
// name without its board code ("TrimUI Smart Pro").
func westonDeviceName() string {
	name, _, _ := strings.Cut(deviceDisplayName(DetectDevice()), " (")
	return name
}

// westonArch is PortMaster's DEVICE_ARCH.
func westonArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	}
	return runtime.GOARCH
}

// wineScreenSize is the panel in landscape: the size the firmware reports,
// else the model family's (1280×720 on the Smart Pro, 1024×768 on the Brick).
func wineScreenSize() (int, int) {
	_, _, id := deviceIdentityReport()
	w, h := int(id.PanelW), int(id.PanelH)
	if w > 0 && h > 0 {
		if h > w {
			w, h = h, w
		}
		return w, h
	}
	if deviceFamily(DetectDevice()) == DeviceTrimuiBrick {
		return 1024, 768
	}
	return 1280, 720
}

// westonShell runs the WestonPack scripts. PortMaster's scripts are written for
// bash; the firmware's sh is the fallback, as for PortMaster ports (ports.go).
func westonShell() string {
	for _, name := range []string{"bash", "sh"} {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path
		}
	}
	return "/bin/sh"
}

// westonControlShim writes the PortMaster control.txt stand-in westonwrap.sh
// sources (it looks under $XDG_DATA_HOME/PortMaster, like every port) and
// returns the folder to use as XDG_DATA_HOME. JukaHub is not PortMaster: the
// stand-in sets the values a port launch would have — each one only when the
// environment (weston.env) has not — and makes PortMaster's helper functions
// do nothing, so no real control.txt with other ideas about the device is read.
func westonControlShim(w, h int) (string, error) {
	base := filepath.Join(windowsRuntimeDir(), "weston-env")
	control := filepath.Join(base, "PortMaster")
	sticks := "2"
	if !stickHardware() {
		sticks = "0"
	}
	var b strings.Builder
	b.WriteString("# Written by JukaHub for WestonPack's westonwrap.sh, which sources\n")
	b.WriteString("# PortMaster's control.txt. JukaHub is not PortMaster: these are the values\n")
	b.WriteString("# a port launch would have, and PortMaster's helpers do nothing here.\n")
	b.WriteString("# Rewritten on every launch; put your own values in weston.env instead.\n")
	fmt.Fprintf(&b, "controlfolder=%s\n", shellQuote(control))
	b.WriteString("ESUDO=\"${ESUDO:-}\"\n")
	fmt.Fprintf(&b, "CFW_NAME=\"${CFW_NAME:-JukaHub}\"\n")
	fmt.Fprintf(&b, "DEVICE_NAME=\"${DEVICE_NAME:-%s}\"\n", shellSafeWord(westonDeviceName()))
	fmt.Fprintf(&b, "DEVICE_ARCH=\"${DEVICE_ARCH:-%s}\"\n", shellSafeWord(westonArch()))
	fmt.Fprintf(&b, "DISPLAY_WIDTH=\"${DISPLAY_WIDTH:-%d}\"\n", w)
	fmt.Fprintf(&b, "DISPLAY_HEIGHT=\"${DISPLAY_HEIGHT:-%d}\"\n", h)
	fmt.Fprintf(&b, "ANALOG_STICKS=\"${ANALOG_STICKS:-%s}\"\n", sticks)
	b.WriteString("get_controls() { :; }\n")
	b.WriteString("pm_platform_helper() { :; }\n")
	b.WriteString("pm_finish() { :; }\n")
	b.WriteString("pm_message() { echo \"$*\"; }\n")
	path := filepath.Join(control, "control.txt")
	if old, err := os.ReadFile(path); err == nil && string(old) == b.String() {
		return base, nil
	}
	if err := os.MkdirAll(control, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return base, nil
}

// shellSafeWord keeps the characters that are plain text inside a
// double-quoted shell word (letters, digits, space and a few marks).
func shellSafeWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// westonEnvOverrides reads data/windows-runtime/weston.env (westonEnvFileName):
// NAME=VALUE lines for westonwrap.sh's environment, so a setting can be tried
// on a device without a new build. JUKAHUB_WESTON_MODE and
// JUKAHUB_WESTON_GL_MODE replace the four mode words of the two modes instead
// (westonModeOverrideKeys). A missing file is normal; bad lines are logged.
func westonEnvOverrides(path string, log wineLog) (map[string]string, map[string][]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	env, modes, bad := parseWestonEnv(string(data))
	for _, line := range bad {
		log.printf("display: %s: ignored %q\n", westonEnvFileName, line)
	}
	if len(env) > 0 || len(modes) > 0 {
		log.printf("display: %s sets %d value(s) and %d mode(s)\n", westonEnvFileName, len(env), len(modes))
	}
	return env, modes
}

// parseWestonEnv parses weston.env's text: NAME=VALUE per line (an optional
// "export " in front, the value optionally in matching quotes), blank lines
// and # comments skipped. NAME must be a plain variable name and VALUE a
// single line without NUL. The two mode keys must hold exactly four words of
// letters, digits and underscores. Lines that break a rule come back in bad.
func parseWestonEnv(text string) (env map[string]string, modes map[string][]string, bad []string) {
	env = map[string]string{}
	modes = map[string][]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !gameProfileEnvKeyPattern.MatchString(key) || strings.ContainsRune(value, '\x00') {
			bad = append(bad, line)
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if mode, isMode := westonModeOverrideKeys[key]; isMode {
			words := strings.Fields(value)
			if len(words) != 4 || !westonModeWordsOK(words) {
				bad = append(bad, line)
				continue
			}
			modes[mode] = words
			continue
		}
		env[key] = value
	}
	return env, modes, bad
}

// westonModeWordsOK accepts mode words made of letters, digits and
// underscores, which is every word westonwrap.sh documents.
func westonModeWordsOK(words []string) bool {
	for _, w := range words {
		if w == "" {
			return false
		}
		for _, r := range w {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

// westonWrapperResetsDevices reads westonwrap.sh and reports the build that
// must not run (westonTextResetsDevices). An unreadable script is let through:
// starting it fails on its own, with the reason in the log.
func westonWrapperResetsDevices(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return false
	}
	return westonTextResetsDevices(string(data))
}

// westonTextResetsDevices reports a westonwrap.sh with a bare `udevadm
// trigger`: one without a subsystem filter, which sends a change event to every
// device. CrossMix's preview of WestonPack ("Westonwrap 0.2.8 (Preview 1)")
// runs one on TrimUI, and the firmware's mdev then unmounts the SD card —
// JukaHub's own folder included (NX Redux #207). The official 0.2.7.1 has no
// such line.
func westonTextResetsDevices(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		i := strings.Index(line, "udevadm trigger")
		if i < 0 {
			continue
		}
		rest := line[i+len("udevadm trigger"):]
		if !strings.Contains(rest, "subsystem") && !strings.Contains(rest, "sysname") && !strings.Contains(rest, "-s ") {
			return true
		}
	}
	return false
}

// ── /proc/mounts ─────────────────────────────────────────────────────────

// procMount is one line of /proc/mounts.
type procMount struct {
	source, dir, fstype string
}

// parseProcMounts reads /proc/mounts' text. Spaces and other special
// characters in a field arrive as octal escapes (\040), which are decoded.
func parseProcMounts(text string) []procMount {
	var out []procMount
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		out = append(out, procMount{source: unescapeMountField(f[0]), dir: unescapeMountField(f[1]), fstype: f[2]})
	}
	return out
}

// unescapeMountField decodes the kernel's \ooo escapes in a mounts field.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ── Logging ──────────────────────────────────────────────────────────────

// wineLog is one launch's log file; a nil file writes nowhere. The program's
// own output goes to the same file.
type wineLog struct {
	f    *os.File
	path string
}

func (l wineLog) printf(format string, args ...any) {
	if l.f != nil {
		fmt.Fprintf(l.f, format, args...)
	}
}

func (l wineLog) close() {
	if l.f != nil {
		_ = l.f.Close()
	}
}

// wineSecretName reports an environment name whose value must not reach a log:
// tokens, keys, passwords, cookies and the like.
func wineSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "COOKIE", "CREDENTIAL", "AUTH", "API_KEY", "APIKEY", "PRIVATE"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return strings.HasSuffix(upper, "_KEY") || upper == "KEY"
}

// wineLogWord is one word of a logged command line: NAME=VALUE has its value
// hidden when the name looks secret or is one of hide (a game profile's own
// variables, whose values stay out of logs), and a word with spaces or quotes
// is quoted.
func wineLogWord(word string, hide map[string]bool) string {
	if name, _, ok := strings.Cut(word, "="); ok && gameProfileEnvKeyPattern.MatchString(name) && (wineSecretName(name) || hide[name]) {
		word = name + "=<hidden>"
	}
	if word == "" || strings.ContainsAny(word, " \t\"'\\$`") {
		return strconv.Quote(word)
	}
	return word
}

// wineLogCommand is the command line for the log, values hidden as
// wineLogWord says.
func wineLogCommand(command string, args []string, hide map[string]bool) string {
	words := make([]string, 0, len(args)+1)
	words = append(words, wineLogWord(command, hide))
	for _, arg := range args {
		words = append(words, wineLogWord(arg, hide))
	}
	return strings.Join(words, " ")
}

// wineLogEnv lists an environment map for the log, sorted, values hidden as
// wineLogWord says.
func wineLogEnv(env map[string]string, hide map[string]bool) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	words := make([]string, 0, len(keys))
	for _, key := range keys {
		words = append(words, wineLogWord(key+"="+env[key], hide))
	}
	return strings.Join(words, " ")
}
