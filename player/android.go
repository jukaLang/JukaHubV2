package main

// android.go — the Android-app engine for the ARM64 handheld build.
//
// Android apps cannot be started like a Linux binary. They need a full Android
// userspace: ART for the DEX bytecode, bionic for the native libraries, and
// SurfaceFlinger for the pixels. This module does not pretend to be that
// runtime. It integrates the one that exists on ARM64 Linux — Waydroid, an
// Android container that runs on the device kernel — the same way
// windowsapps.go integrates Wine:
//
//   * find the container binary and check the pieces it needs (the binder
//     device the kernel must expose);
//   * read the session state, because a stopped session cannot launch anything;
//   * list the packages installed in the container;
//   * build the launch command, bridging the display the way WestonPack
//     bridges it for Wine, because Waydroid's SurfaceFlinger draws through a
//     Wayland compositor and a stock TrimUI has none.
//
// JukaHub never needs root. The one-time root step belongs to Waydroid itself
// (installing the Android image and the binder module), and the setup page
// explains it instead of running it.
//
// What this deliberately cannot do is run an arbitrary APK. An APK is DEX plus
// bionic native code; without the Android framework there is nothing to run it
// on. The engine starts apps the container already has.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	androidBackendWaydroid = "waydroid"

	androidSessionRunning = "running"
	androidSessionStopped = "stopped"

	androidDisplayAuto    = "auto"
	androidDisplayWayland = "wayland"
	androidDisplayWeston  = "weston"
)

// androidBinderPaths are the device nodes Waydroid's kernel support shows up
// as. The plain /dev/binder is the usual one; a binderfs mount appears as a
// directory holding the same node under a name.
var androidBinderPaths = []string{
	"/dev/binder",
	"/dev/binderfs/binder",
	"/dev/binderfs/anbox-binder",
	"/dev/anbox-binder",
}

// androidBackend is one usable container: where its binary is and whether its
// session is already up.
type androidBackend struct {
	Kind    string
	Binary  string
	Session string
}

// androidApp is one package the container can start.
type androidApp struct {
	Package string
	Name    string
}

// label is the row title: the friendly name when the container knows one.
func (a androidApp) label() string {
	if strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	return a.Package
}

// androidHost abstracts the environment so detection, listing and launch are
// testable without a Waydroid install, a binder device or a compositor.
type androidHost struct {
	lookPath  func(string) (string, error)
	run       func(name string, args ...string) (string, error)
	exists    func(path string) bool
	getenv    func(string) string
	weston    string
	westonErr string
}

func currentAndroidHost() androidHost {
	weston, err := resolveWestonPack()
	host := androidHost{
		lookPath: exec.LookPath,
		run: func(name string, args ...string) (string, error) {
			out, runErr := exec.Command(name, args...).CombinedOutput()
			return string(out), runErr
		},
		exists: func(path string) bool {
			_, statErr := os.Stat(path)
			return statErr == nil
		},
		getenv: os.Getenv,
		weston: weston,
	}
	if err != nil {
		host.westonErr = err.Error()
	}
	return host
}

// androidBinderPresent reports whether the kernel exposes a binder device.
// Without it the container cannot start at all, and the panel must say so
// instead of offering a launch that fails inside Waydroid.
func androidBinderPresent(host androidHost) bool {
	for _, path := range androidBinderPaths {
		if host.exists != nil && host.exists(path) {
			return true
		}
	}
	return false
}

// resolveAndroidBackend finds the container and the two things it needs: the
// binary and kernel binder support. A stopped session is not an error — the
// caller starts it — but a missing binder device is.
func resolveAndroidBackend(host androidHost) (androidBackend, error) {
	var backend androidBackend
	kind := strings.ToLower(strings.TrimSpace(host.getenv("JUKAHUB_ANDROID_BACKEND")))
	if kind == "" {
		kind = androidBackendWaydroid
	}
	if kind != androidBackendWaydroid {
		return backend, fmt.Errorf("unknown Android backend %q (only waydroid is supported)", kind)
	}
	binary := ""
	if host.lookPath != nil {
		if found, err := host.lookPath("waydroid"); err == nil {
			binary = strings.TrimSpace(found)
		}
	}
	if binary == "" {
		return backend, errors.New("no Android container: install Waydroid (a one-time root step), then start its session")
	}
	backend = androidBackend{Kind: androidBackendWaydroid, Binary: binary}
	if !androidBinderPresent(host) {
		return backend, errors.New("the kernel exposes no binder device (/dev/binder): Waydroid needs the binder_linux module, which stock firmware does not ship — full Android lives in community firmware such as GammaOS (see docs/ANDROID-GAMMAOS.md)")
	}
	if host.run != nil {
		output, err := host.run(binary, "status")
		backend.Session = parseAndroidSession(output)
		if backend.Session == "" && err != nil {
			backend.Session = androidSessionStopped
		}
	}
	if backend.Session == "" {
		backend.Session = androidSessionStopped
	}
	return backend, nil
}

// parseAndroidSession reads the session line out of `waydroid status`.
func parseAndroidSession(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "session") {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "RUNNING":
			return androidSessionRunning
		case "STOPPED":
			return androidSessionStopped
		}
	}
	return ""
}

// parseAndroidApps reads the package list. Waydroid prints a block per app
// (`Name: …` / `Package: …`); a plain `package<TAB>label` list is accepted too
// so a debug helper or a different container build still fills the panel.
func parseAndroidApps(output string) []androidApp {
	var apps []androidApp
	index := map[string]int{}
	add := func(pkg, name string) int {
		pkg = strings.TrimSpace(pkg)
		if !androidPackageValid(pkg) {
			return -1
		}
		name = strings.TrimSpace(name)
		if at, seen := index[pkg]; seen {
			if apps[at].Name == "" && name != "" {
				apps[at].Name = name
			}
			return at
		}
		index[pkg] = len(apps)
		apps = append(apps, androidApp{Package: pkg, Name: name})
		return len(apps) - 1
	}

	// Waydroid prints `Name:` before its `Package:`, but a list that puts the
	// package first is accepted too. pendingName holds a name waiting for its
	// package, awaitingPackage the index of a package still waiting for its
	// name, so neither order loses a label.
	pendingName := ""
	awaitingPackage := -1
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.Trim(line, "\x00"))
		if trimmed == "" {
			continue
		}
		if key, value, ok := strings.Cut(trimmed, ":"); ok {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "name":
				name := strings.TrimSpace(value)
				if awaitingPackage >= 0 && apps[awaitingPackage].Name == "" {
					apps[awaitingPackage].Name = name
					awaitingPackage = -1
					continue
				}
				pendingName = name
				continue
			case "package":
				at := add(value, pendingName)
				pendingName = ""
				awaitingPackage = at
				if at >= 0 && apps[at].Name != "" {
					awaitingPackage = -1
				}
				continue
			}
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && androidPackageValid(fields[0]) {
			add(fields[0], strings.Join(fields[1:], " "))
			awaitingPackage = -1
		}
	}

	sort.SliceStable(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].label()) < strings.ToLower(apps[j].label())
	})
	return apps
}

// androidPackageValid checks a package name before it reaches argv. It is also
// a security boundary: the name goes to `waydroid app launch`, so anything that
// could be read as an option (`-x`), a shell word or a path is refused.
func androidPackageValid(pkg string) bool {
	if pkg == "" || len(pkg) > 200 || strings.HasPrefix(pkg, "-") {
		return false
	}
	parts := strings.Split(pkg, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			case r >= '0' && r <= '9':
				if i == 0 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// listAndroidApps asks the container for its packages. A stopped session can
// still be listed from, so this does not start anything.
func listAndroidApps(backend androidBackend, host androidHost) ([]androidApp, error) {
	if host.run == nil {
		return nil, errors.New("cannot run the container on this build")
	}
	output, err := host.run(backend.Binary, "app", "list")
	apps := parseAndroidApps(output)
	if err != nil && len(apps) == 0 {
		return nil, fmt.Errorf("list Android apps: %v", err)
	}
	return apps, nil
}

// resolveAndroidDisplay picks the render surface. Waydroid's SurfaceFlinger
// draws through a Wayland compositor; X11 alone does not help it, which is why
// this does not offer an X11 mode the way the Wine launcher does.
func resolveAndroidDisplay(requested string, host androidHost) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" && host.getenv != nil {
		requested = strings.ToLower(strings.TrimSpace(host.getenv("JUKAHUB_ANDROID_DISPLAY")))
	}
	if requested == "" {
		requested = androidDisplayAuto
	}
	hasWayland := host.getenv != nil && strings.TrimSpace(host.getenv("WAYLAND_DISPLAY")) != ""

	switch requested {
	case androidDisplayAuto:
		switch {
		case hasWayland:
			return androidDisplayWayland, nil
		case host.weston != "":
			return androidDisplayWeston, nil
		default:
			if host.westonErr != "" {
				return "", errors.New(host.westonErr)
			}
			return "", errors.New("Android apps need a Wayland compositor: run Waydroid in an existing session, or install WestonPack for DRM/KMS")
		}
	case androidDisplayWayland:
		if !hasWayland {
			return "", errors.New("display=wayland, but WAYLAND_DISPLAY is not set")
		}
		return androidDisplayWayland, nil
	case androidDisplayWeston:
		if host.weston == "" {
			if host.westonErr != "" {
				return "", errors.New(host.westonErr)
			}
			return "", errors.New("display=weston, but westonwrap.sh was not found (install WestonPack or set JUKAHUB_WESTON)")
		}
		return androidDisplayWeston, nil
	default:
		return "", fmt.Errorf("unknown Android display %q (use auto, wayland or weston)", requested)
	}
}

// wrapAndroidCommand adapts a container command for the selected display. On
// WestonPack the wrapper owns the compositor and takes the command as its
// final arguments; the session directory is passed explicitly because the
// wrapped app has to find Waydroid's socket under the same XDG_RUNTIME_DIR.
func wrapAndroidCommand(displayRequest, command string, args []string, host androidHost) (string, []string, error) {
	mode, err := resolveAndroidDisplay(displayRequest, host)
	if err != nil {
		return "", nil, err
	}
	if mode != androidDisplayWeston {
		return command, args, nil
	}
	wrapped := []string{"drm", "gl", "kiosk", "system"}
	if host.getenv != nil {
		if dir := strings.TrimSpace(host.getenv("XDG_RUNTIME_DIR")); dir != "" {
			wrapped = append(wrapped, "XDG_RUNTIME_DIR="+dir)
		}
	}
	wrapped = append(wrapped, command)
	wrapped = append(wrapped, args...)
	return host.weston, wrapped, nil
}

// androidLaunchCommand is the whole launch path: validate the package, make
// sure the container can run, start its session when needed, and hand the app
// to it through the display bridge.
func androidLaunchCommand(pkg, displayRequest string, host androidHost) (string, []string, error) {
	if !androidPackageValid(pkg) {
		return "", nil, fmt.Errorf("%q is not an Android package name", pkg)
	}
	backend, err := resolveAndroidBackend(host)
	if err != nil {
		return "", nil, err
	}
	return wrapAndroidCommand(displayRequest, backend.Binary, []string{"app", "launch", pkg}, host)
}

// androidSessionStartCommand starts a stopped container session. It is exposed
// separately because the session outlives one app: the panel starts it once and
// then launches as many apps as the user picks.
func androidSessionStartCommand(backend androidBackend) (string, []string) {
	return backend.Binary, []string{"session", "start"}
}
