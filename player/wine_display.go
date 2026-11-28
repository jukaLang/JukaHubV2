package main

// wine_display.go — the display half of a Windows-program launch.
//
// planWineRuntime (wine_runtime.go) returns the command that runs a program:
// Box64 + Wine on the handheld, Wine on a Linux desktop, the .exe itself on
// Windows. On a stock TrimUI nothing it starts has anywhere to draw: Wine's
// winex11.drv needs an X server, and the firmware has no X11 or Wayland
// desktop (its SDL apps draw directly to the panel). This file puts a display
// around the command:
//
//   - a Linux desktop session (DISPLAY or WAYLAND_DISPLAY) is used as it is;
//   - on the handheld, WestonPack (PortMaster's weston_pkg runtime) starts
//     Weston with Xwayland and shows it through Crusty, its SDL2 presenter.
//     JukaHub runs westonwrap.sh the way a PortMaster port does: the SquashFS
//     image mounted read-only (an unpacked copy is used as it is), then
//     `westonwrap.sh <backend> <renderer> <shell> <gl> /bin/sh <private script>`,
//     then `westonwrap.sh cleanup` and the unmount once it returned. The script
//     quotes the program, arguments, environment and working directory: the
//     upstream wrapper joins its arguments into an eval string, so passing a
//     Wine value such as `WINEDLLOVERRIDES=...;...` directly runs the second
//     half as a shell command. It also always exits 0, so the script records
//     the program's real exit status before the wrapper cleans up;
//   - on Windows the program opens its own windows: nothing goes around it.
//
// Two WestonPack modes are offered, both from the Westonpack wiki's examples:
//
//	weston      drm gl kiosk system                  programs that draw with
//	                                                 Xlib/GDI (the default)
//	weston-gl   headless noop kiosk crusty_glx_gl4es OpenGL 2 games (GLX on
//	                                                 GLES through gl4es)
//
// The private script exports the program's environment after WestonPack sets
// its display and graphics variables. The wrapper's own environment carries
// what westonwrap.sh reads itself (PortMaster's device variables,
// CRUSTY_SHOW_CURSOR, the headless size).
//
// westonwrap.sh is a bash script; a stock TrimUI has only BusyBox's sh, which
// stops it at the first file it cannot source. It runs under a real bash —
// the firmware's when it has one, else JukaHub's on the private glibc,
// fetched at the first launch — and, where /bin/bash is not bash, in a mount
// namespace of its own where it is (westonPickShell, westonBinBashPlan).
//
// The firmware's udev has no input_id rule, and Weston's libinput ignores every
// input device udev did not tag, after which Weston does not start at all
// ("TRIMUI Player1: not tagged as supported input device", "failed to create
// compositor backend"). Before Weston starts, JukaHub adds that one rule to
// udev's runtime rules (RAM only) and re-scans the input devices
// (wine_display_linux.go).

import (
	"context"
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
	"sync"
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
	// westonLaunchScriptName and westonLaunchStatusName live in a private /tmp
	// directory for one launch. WestonPack's eval sees only their safe paths.
	westonLaunchScriptName = "launch.sh"
	westonLaunchStatusName = "exit-status"
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
	shell    string // what runs westonwrap.sh (westonPickShell), the clean-up too
	mountDir string // where an image is mounted ("" when nothing is)
	unmount  bool   // this launch mounted it, so this launch unmounts it
	// binbash gives the session's own `#!/bin/bash` scripts a real bash in a
	// mount namespace of its own (portmaster_binbash.go); nil when the
	// firmware's /bin/bash is one, or when none can be given.
	binbash *pmBinBashPlan
	// The upstream wrapper does `cd $game_dir` without quotes and returns 0
	// even when its app fails. It starts in launchDir (a safe /tmp path), while
	// the private script changes to the app's real folder and writes appStatus.
	launchDir string
	appStatus string
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
		cmd = hiddenCommand(d.Command, d.Args...)
	}
	cmd.Dir = dir
	if d.launchDir != "" {
		cmd.Dir = d.launchDir
	}
	cmd.Env = d.Env
	return cmd, nil
}

// appExit returns the program's status, which WestonPack's cleanup hides by
// always exiting 0. A missing marker means its wrapper stopped before the
// private command could finish, even when the wrapper claimed success.
func (d wineDisplayPlan) appExit(waitErr error) error {
	if d.appStatus == "" {
		return waitErr
	}
	data, err := os.ReadFile(d.appStatus)
	if err != nil {
		if waitErr != nil {
			return waitErr
		}
		return errors.New("WestonPack ended before the Windows program reported an exit status")
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || code < 0 || code > 255 {
		return errors.New("WestonPack recorded an invalid Windows program exit status")
	}
	if code != 0 {
		return fmt.Errorf("Windows program exited with status %d", code)
	}
	return waitErr
}

// prepareWineDisplay puts the display around plan's command. openGL picks
// WestonPack's OpenGL mode where WestonPack is the display. A WestonPack image
// is mounted here; whatever this sets up is put away by stop and release.
// phase shows a step that takes a while (a bash download, westonPickShell);
// binBashOff is the portBinBash setting, read on the main thread.
func prepareWineDisplay(request string, openGL bool, plan wineRuntimePlan, dir string, log wineLog, phase func(string), binBashOff bool) (wineDisplayPlan, error) {
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
		return prepareWestonDisplay(d, host.weston, plan, dir, log, phase, binBashOff)
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

// prepareWestonDisplay wraps the command in westonwrap.sh: a real bash found
// (or fetched) to run it, the image mounted when WestonPack is one, the
// wrapper checked, the PortMaster-style values set, the input devices labelled
// for Weston's libinput.
func prepareWestonDisplay(d wineDisplayPlan, src westonPackSource, plan wineRuntimePlan, dir string, log wineLog, phase func(string), binBashOff bool) (wineDisplayPlan, error) {
	// The shell comes first: getting JukaHub's bash is a download, and no
	// image stays mounted meanwhile.
	base := screenChildEnv(os.Environ())
	shell, shellDir, shellNote := westonPickShell(base, phase, log)
	d.shell = shell
	log.printf("display: %s\n", shellNote)
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
		if src.Wrapper != "" {
			// Only JUKAHUB_WESTON can name such a script: findWestonPackSource
			// passes over it everywhere else.
			return fail(errors.New("WestonPack at " + wrapper + " is the preview build that resets the SD card on TrimUI; point JUKAHUB_WESTON at the official WestonPack, or unset it"))
		}
		// An image only shows what it holds once mounted. From now on it does
		// not count as WestonPack, so Setup offers JukaHub's own copy, and the
		// panel opens Setup for this app (a missing runtime).
		westonRefuseImage(src.Image)
		invalidateWindowsAppsCache()
		invalidateWindowsSetupSnapshot()
		return fail(withLaunchFailureKind(launchFailureRuntimeMissing, errors.New("this WestonPack is the preview build that resets the SD card on TrimUI; install WestonPack from Windows Apps → Setup")))
	}
	d.wrapper = wrapper

	w, h := wineScreenSize()
	overrides, modes := westonEnvOverrides(filepath.Join(windowsRuntimeDir(), westonEnvFileName), log)
	words := westonModeWords[d.Mode]
	if custom := modes[d.Mode]; len(custom) == 4 {
		words = custom
	}
	home, err := westonControlShim(w, h)
	if err != nil {
		log.printf("display: no PortMaster stand-in (%v); westonwrap.sh reads PortMaster's own control.txt, if it finds one\n", err)
		home = ""
	} else {
		log.printf("display: westonwrap.sh's HOME is %s, where it finds JukaHub's PortMaster stand-in\n", home)
	}
	d.Env = westonWrapperEnv(base, w, h, home, overrides)
	if shellDir != "" {
		// `bash` by name is the same bash as the one running westonwrap.sh.
		path, _ := childEnvLookup(d.Env, "PATH")
		d.Env = mergeGameProfileEnv(d.Env, map[string]string{"PATH": strings.TrimSuffix(shellDir+":"+path, ":")})
	}

	// WestonPack's own modules link libjpeg.so.8, the one library its image
	// leaves to the firmware and a stock TrimUI does not have: Weston then stops
	// at the first module it cannot load, before the program's script runs, and
	// every launch ends as "Weston stopped while it was starting"
	// (wine_weston_libs.go). The stand-in goes last on the session's library
	// path, so a firmware that carries the real library keeps it.
	if libDir, unavailable, libErr := westonEnsureWestonLibs(filepath.Dir(wrapper), words, d.Env, log); libErr != nil {
		log.printf("display: WestonPack's libraries: %v\n", libErr)
	} else {
		if libDir != "" {
			path, _ := childEnvLookup(d.Env, "LD_LIBRARY_PATH")
			if path == "" {
				path = libDir
			} else {
				path += ":" + libDir
			}
			d.Env = mergeGameProfileEnv(d.Env, map[string]string{"LD_LIBRARY_PATH": path})
		}
		if len(unavailable) > 0 {
			log.printf("display: WestonPack's Weston links %s and this firmware has no copy of it\n", strings.Join(unavailable, ", "))
		}
	}

	// Crusty draws the pointer itself (CRUSTY_SHOW_CURSOR); the program gets the
	// switch as well, for the mode where Crusty runs inside it. WestonPack 0.2.7
	// concatenates its app words and evals them. Give it only /bin/sh and a
	// metacharacter-free /tmp path; the private script quotes every real word.
	appEnv := cloneGameProfileEnv(plan.Env)
	if !hasEnvKey(appEnv, "CRUSTY_SHOW_CURSOR") {
		appEnv["CRUSTY_SHOW_CURSOR"] = "1"
	}
	// westonwrap.sh runs with the stand-in's HOME; the program gets its own.
	westonAppHome(appEnv, base, home)
	// Box64's native FreeType wrapper opens the unversioned libfreetype.so,
	// while both WestonPack and the firmware provide only libfreetype.so.6.
	// Without an alias, Wine cannot draw TrueType text in ordinary apps.
	fontLib := wineFreeTypeSource()
	if fontLib == "" {
		return fail(errors.New("Wine cannot draw text: no compatible ARM64 FreeType library was found in the firmware"))
	}
	log.printf("display: Wine fonts use %s through a private libfreetype.so alias\n", fontLib)
	launchDir, appStatus, script, err := westonLaunchScript(dir, plan, appEnv, fontLib)
	if err != nil {
		return fail(err)
	}
	d.launchDir, d.appStatus = launchDir, appStatus
	d.Command = shell
	d.Args = append([]string{wrapper}, westonWrapArgs(words, "/bin/sh", []string{script})...)
	binbash, binbashNote := westonBinBashPlan(shell, d.Env, binBashOff)
	d.binbash = binbash
	if binbashNote != "" {
		log.printf("display: %s\n", binbashNote)
	}
	wineLabelInputDevices(log)
	return d, nil
}

// westonLaunchScript writes one private command for WestonPack. The script is
// needed because upstream westonwrap.sh evals a space-joined argument string:
// directly passing Wine's semicolon-bearing overrides, a folder with spaces,
// or a caller's arbitrary arguments is unsafe and fails on the Smart Pro.
func westonLaunchScript(dir string, plan wineRuntimePlan, env map[string]string, fontLib string) (launchDir, status, script string, err error) {
	launchDir, err = os.MkdirTemp("/tmp", "jukahub-wine-")
	if err != nil {
		return "", "", "", fmt.Errorf("make a temporary WestonPack command: %w", err)
	}
	status = filepath.Join(launchDir, westonLaunchStatusName)
	script = filepath.Join(launchDir, westonLaunchScriptName)
	fontAlias := filepath.Join(launchDir, wineFreeTypeAlias)
	err = os.Symlink(fontLib, fontAlias)
	if err != nil {
		_ = os.Remove(launchDir)
		return "", "", "", fmt.Errorf("link Wine's native FreeType library: %w", err)
	}
	command, err := westonLaunchScriptText(dir, plan.Command, plan.Args, env, status, launchDir)
	if err == nil {
		err = os.WriteFile(script, []byte(command), 0o600)
	}
	if err != nil {
		_ = os.Remove(script)
		_ = os.Remove(fontAlias)
		_ = os.Remove(launchDir)
		return "", "", "", fmt.Errorf("write the WestonPack command: %w", err)
	}
	return launchDir, status, script, nil
}

// westonLaunchScriptText is the actual /bin/sh command. It inherits DISPLAY,
// Wayland and the GPU libraries WestonPack sets, adds the app's own variables,
// runs in the app's folder, and records the exit that WestonPack discards.
func westonLaunchScriptText(dir, command string, args []string, env map[string]string, status, nativeLibDir string) (string, error) {
	if command == "" || status == "" || strings.ContainsRune(command, 0) || strings.ContainsRune(dir, 0) || strings.ContainsRune(status, 0) || strings.ContainsRune(nativeLibDir, 0) {
		return "", errors.New("invalid WestonPack command")
	}
	keys := make([]string, 0, len(env))
	for key, value := range env {
		if !gameProfileEnvKeyPattern().MatchString(key) || strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("invalid Windows program environment variable %q", key)
		}
		keys = append(keys, key)
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return "", errors.New("invalid Windows program argument")
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if dir != "" {
		fmt.Fprintf(&b, "cd %s || { printf '125\\n' > %s; exit 125; }\n", shellQuote(dir), shellQuote(status))
	}
	for _, key := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", key, shellQuote(env[key]))
	}
	if nativeLibDir != "" {
		// Preserve WestonPack's GL/X11 chain (or a profile's chosen path)
		// while letting Box64 find the private unversioned FreeType alias.
		fmt.Fprintf(&b, "export LD_LIBRARY_PATH=%s\"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\"\n", shellQuote(nativeLibDir))
	}
	b.WriteString(shellQuote(command))
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	fmt.Fprintf(&b, "\njh_status=$?\nprintf '%%s\\n' \"$jh_status\" > %s\nexit \"$jh_status\"\n", shellQuote(status))
	return b.String(), nil
}

// stop ends what the display left running once the program has exited:
// `westonwrap.sh cleanup`, as every WestonPack port runs it, bounded so a
// clean-up that hangs cannot keep the screen parked.
func (d wineDisplayPlan) stop(log wineLog) {
	if d.wrapper == "" {
		return
	}
	shell := d.shell
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := hiddenCommand(shell, d.wrapper, "cleanup")
	cmd.Dir = filepath.Dir(d.wrapper)
	if d.launchDir != "" {
		cmd.Dir = d.launchDir
	}
	cmd.Env = d.Env
	if log.f != nil {
		cmd.Stdout, cmd.Stderr = log.f, log.f
	}
	processGroupConfigure(cmd)
	d.binbash.apply(cmd)
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
	// Whatever the clean-up started in the background and left behind — a
	// control.txt's splash (sdl2imgshow) — must not stay over the screen JukaHub
	// takes back: nothing in this group outlives it on purpose.
	if screenChildGroupAlive(cmd) {
		log.printf("display: westonwrap.sh cleanup left %s running; stopping it\n", screenChildGroupNames(cmd))
		_ = screenChildKill(cmd)
	}
}

// release unmounts the image this launch mounted (lazily when something still
// holds it). A mount someone else made is left alone.
func (d wineDisplayPlan) release(log wineLog) {
	if d.mountDir != "" && d.unmount {
		wineUnmountWeston(d.mountDir, log)
	}
	if d.launchDir != "" {
		_ = os.Remove(filepath.Join(d.launchDir, westonLaunchScriptName))
		_ = os.Remove(filepath.Join(d.launchDir, wineFreeTypeAlias))
		_ = os.Remove(d.appStatus)
		if err := os.Remove(d.launchDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.printf("display: temporary command folder could not be removed: %v\n", err)
		}
	}
}

// ── westonwrap.sh's arguments and environment ────────────────────────────

// westonWrapArgs gives upstream westonwrap.sh only plain shell words. It evals
// everything after the four mode words, so app environment and arguments live
// in the private script instead of this list.
func westonWrapArgs(words []string, command string, args []string) []string {
	out := append([]string{}, words...)
	out = append(out, command)
	return append(out, args...)
}

// westonWrapperEnv is westonwrap.sh's own environment: base (the firmware's,
// screenChildEnv) plus what a PortMaster port would have set — the device's
// screen and name, ESUDO empty because JukaHub runs as root, PortMaster's
// control folder (under home, the stand-in HOME of westonControlShim, which
// becomes HOME and XDG_DATA_HOME's base) — and Crusty's software pointer, the
// only pointer a Windows program gets on this screen. overrides (weston.env)
// win. A desktop session's DISPLAY or WAYLAND_DISPLAY is dropped: WestonPack
// starts its own.
func westonWrapperEnv(base []string, w, h int, home string, overrides map[string]string) []string {
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
		// westonwrap.sh 0.2.7.1 asks `[ "$WP_32BIT" -eq 1 ]` before it says
		// which of its two copies it runs. PortMaster's control.txt sets it,
		// and the stand-in JukaHub gives it does not, so every session put
		// "westonwrap.sh: line 95: [: : integer expression expected" in the
		// app's log. JukaHub's Wine is 64-bit only.
		"WP_32BIT": "0",
	}
	if home != "" {
		share := filepath.Join(home, ".local", "share")
		set["HOME"] = home
		set["XDG_DATA_HOME"] = share
		set["controlfolder"] = filepath.Join(share, "PortMaster")
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

// westonPickShell is what runs westonwrap.sh, with a line for the launch log;
// dir, when not "", goes first on the session's PATH so that `bash` by name is
// the same bash. westonwrap.sh is a bash script, and BusyBox's sh — the only
// shell of a stock TrimUI — stops it at the first file it cannot source, where
// bash reports the file and goes on: a TrimUI PortMaster's control.txt sources
// TRIMUI_EX's ex_config, which a card without TRIMUI_EX does not have
// ("source: line 12: can't open '/mnt/SDCARD/System/etc/ex_config'", exit 2
// before Weston started). So: a bash on env's PATH that runs a bash array;
// else JukaHub's — Ubuntu's bash on the private glibc, the one PortMaster's
// ports get (glibc_runtime.go), fetched here on an ARM64 TrimUI when the
// handheld is online (a few MB, once); else the firmware's sh. Off the main
// thread: it starts programs and may download.
func westonPickShell(env []string, phase func(string), log wineLog) (shell, dir, note string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	all := pmLookPathAll(env, "bash")
	for i, p := range all {
		if i >= 3 {
			break
		}
		if pmBashWorks(ctx, p, env) {
			cancel()
			if i > 0 {
				// A BusyBox bash comes first on PATH: a shim names this one.
				dir = pmBashShimDir(p)
			}
			return p, dir, "shell: " + p
		}
	}
	cancel()
	own := func(how string) (string, string, string, bool) {
		d := privateGlibcBashWrapperDir()
		if d == "" {
			return "", "", "", false
		}
		return filepath.Join(d, "bash"), d, "shell: JukaHub's bash (Ubuntu's, on the private glibc " + privateGlibcVersion + ")" + how, true
	}
	if shell, dir, note, ok := own(""); ok {
		return shell, dir, note
	}
	why := "the firmware has no bash"
	if len(all) > 0 {
		why = all[0] + " is not a full bash"
	}
	switch {
	case !pmRuntimeApplies():
	case !IsOnline():
		why += "; connect to Wi-Fi once so JukaHub can add one"
	default:
		if phase != nil {
			phase("Adding bash for WestonPack…")
		}
		log.printf("display: %s; adding JukaHub's bash (Ubuntu's, on the private glibc)\n", why)
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Minute)
		err := privateGlibcEnsureBash(dctx, nil)
		dcancel()
		if err == nil {
			if shell, dir, note, ok := own(", added now: " + why); ok {
				return shell, dir, note
			}
			err = errors.New("the private runtime has no usable bash")
		}
		why += "; adding JukaHub's failed: " + firstLine(err.Error())
	}
	sh := "/bin/sh"
	if p, err := exec.LookPath("sh"); err == nil && p != "" {
		sh = p
	}
	return sh, "", "shell: " + sh + " — " + why + ". westonwrap.sh is a bash script: under this sh it stops at the first file it cannot source"
}

// westonBinBashPlan is the /bin/bash cover for a WestonPack session on an
// ARM64 TrimUI whose /bin/bash is BusyBox or missing (portmaster_binbash.go,
// as PortMaster's ports get it): whatever westonwrap.sh starts by its own
// `#!/bin/bash` line then gets shell too. nil when shell is not a bash, when
// /bin/bash is one, when portBinBash is off, or when no namespace can be made;
// the note (for the log) says which. Off the main thread.
func westonBinBashPlan(shell string, env []string, off bool) (*pmBinBashPlan, string) {
	if !pmRuntimeApplies() || filepath.Base(shell) != "bash" || !filepath.IsAbs(shell) {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := pmRuntimeCheck{Done: true, BashOK: true, BashPath: shell, BashGood: shell}
	ok, why := pmBinBashProbe(ctx, env, c)
	if ok {
		return nil, ""
	}
	why = setOr(why, pmBinBashSystem+" is not bash")
	if off {
		return nil, why + "; portBinBash is off, so a script's own #!/bin/bash line gets the firmware's"
	}
	plan, target, err := pmBinBashMake(c)
	if err != nil {
		return nil, why + "; the session keeps it: " + err.Error()
	}
	return plan, why + ", so the session runs in its own mount namespace where /bin/bash is " + target
}

// westonHomeBase is the HOME westonwrap.sh runs with: JukaHub's PortMaster
// stand-in is its .local/share/PortMaster (westonControlShim). In RAM, and a
// path without spaces: westonwrap.sh sources $controlfolder/control.txt
// without quotes.
var westonHomeBase = "/tmp/jukahub-weston-home"

// westonControlShim writes the PortMaster control.txt stand-in westonwrap.sh
// sources and returns the folder westonwrap.sh gets as HOME (westonHomeBase,
// else data/windows-runtime/weston-env). westonwrap.sh 0.2.7.1 looks for
// PortMaster in /opt/system/Tools/PortMaster, /opt/tools/PortMaster,
// $HOME/.local/share/PortMaster and /roms/ports/PortMaster — it sets
// XDG_DATA_HOME to "$HOME/.local/share" first, so the XDG_DATA_HOME JukaHub
// used to point at the stand-in was never read. With the firmware's HOME it
// found the real PortMaster through the link JukaHub's port launches make
// there (ports_env.go), and every session ran PortMaster's TrimUI control.txt,
// device_info.txt and get_controls on its way to Weston and again in
// `westonwrap.sh cleanup`: a sdl2imgshow splash started in the background,
// setterm, HOME moved to the card (device log, 2026-11-25). JukaHub is not
// PortMaster: the stand-in sets the values a port launch would have — each one
// only when the environment (weston.env) has not — and makes PortMaster's
// helper functions do nothing. westonAppHome gives the program its own HOME
// back.
func westonControlShim(w, h int) (string, error) {
	var lastErr error
	for _, home := range []string{westonHomeBase, filepath.Join(windowsRuntimeDir(), "weston-env")} {
		if !westonPlainPath(home) {
			lastErr = errors.New(home + " has spaces or wildcards, which westonwrap.sh cannot source through")
			continue
		}
		if err := westonWriteControl(home, w, h); err != nil {
			lastErr = err
			continue
		}
		return home, nil
	}
	return "", lastErr
}

// westonPlainPath reports an absolute path westonwrap.sh can use unquoted:
// letters, digits and / . _ - + , : @ only.
func westonPlainPath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._-+,:@", r):
		default:
			return false
		}
	}
	return true
}

// westonWriteControl writes the stand-in control.txt for HOME home (in
// .local/share/PortMaster), unless it already holds exactly that.
func westonWriteControl(home string, w, h int) error {
	control := filepath.Join(home, ".local", "share", "PortMaster")
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
		return nil
	}
	if err := os.MkdirAll(control, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// westonAppHome gives the program the HOME (and XDG_DATA_HOME) it would have
// had without westonwrap.sh's stand-in HOME home (westonControlShim): what its
// own env names, else the firmware's from base. Nothing changes when there is
// no stand-in, or base has no value either.
func westonAppHome(app map[string]string, base []string, home string) {
	if home == "" {
		return
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME"} {
		if hasEnvKey(app, key) {
			continue
		}
		if v, ok := childEnvLookup(base, key); ok {
			app[key] = v
		}
	}
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
		if !ok || !gameProfileEnvKeyPattern().MatchString(key) || strings.ContainsRune(value, '\x00') {
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

// westonRefusedImages holds the WestonPack images whose westonwrap.sh turned out
// to be the preview build when a launch mounted one: cleaned path → the file's
// size and time then, so a new file at the same path counts again.
// findWestonPackSource passes over them, so the setup page does not call
// WestonPack installed. Kept until JukaHub restarts; by then JukaHub's own
// image, which is looked for first, is normally there.
var westonRefusedImages sync.Map

// westonImageStamp is a file's size and modification time, "" when it is not
// there.
func westonImageStamp(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(info.Size(), 10) + "@" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

func westonRefuseImage(image string) {
	if stamp := westonImageStamp(image); stamp != "" {
		westonRefusedImages.Store(filepath.Clean(image), stamp)
	}
}

// westonImageRefused reports an image westonRefuseImage turned down, while it
// is still the same file.
func westonImageRefused(image string) bool {
	stamp, ok := westonRefusedImages.Load(filepath.Clean(image))
	if !ok {
		return false
	}
	s, _ := stamp.(string)
	return s != "" && s == westonImageStamp(image)
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
// JukaHub's own folder included. The official 0.2.7.1 has no
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
	if name, _, ok := strings.Cut(word, "="); ok && gameProfileEnvKeyPattern().MatchString(name) && (wineSecretName(name) || hide[name]) {
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
