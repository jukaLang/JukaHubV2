package main

// wine_runtime.go — the runtime half of a Windows-app launch, as one call.
//
// Windows Apps (windowsapps.go) and Wine game profiles (gameprofiles.go) start
// a Windows program the same way. The runtime half finds what runs it — Box64
// with Wine on an ARM handheld, Hangover, a system Wine, or nothing at all on
// Windows, where the .exe runs directly — picks and prepares the prefix, and
// sets the variables Wine and Box64 need. The launch half puts a display around
// that command (WestonPack on a handheld without X11 or Wayland), starts it,
// holds the screen and cleans up afterwards. planWineRuntime is the interface
// between the two; currentWineRuntimeFacts and findWestonPackSource are what
// the setup page and the display code read without side effects.
//
// What runs Wine (resolveWineRT), in order: JukaHub's own runtime
// (wine_pack.go: Box64 + Wine 10.0 on ARM64, Wine 10.0 on an x86-64 PC) through
// its wrapper scripts, which are run with sh so a card that ignores the
// executable bit cannot stop them; else JUKAHUB_WINE or another Wine
// resolveWine finds (a system Wine, PortMaster's), with Box64 in front when it
// is an x86 program on an ARM device. The prefix and its first-run setup are
// wine_prefix.go's.
//
// Direct3D: Wine's own wined3d is the default everywhere. DXVK is used only
// when the installed build can run on this device's Vulkan — DXVK 2.x needs
// Vulkan 1.3, the Smart Pro's driver is 1.1-level, so there it takes the user
// installing DXVK 1.10.3 by hand (or JUKAHUB_DXVK).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// wineRuntimePlan is the runtime half of one launch.
type wineRuntimePlan struct {
	// Command and Args start the program: Args carries the exe and the
	// caller's arguments after whatever the runtime puts first.
	Command string
	Args    []string
	// Env holds every variable the launch sets: the caller's own (a game
	// profile's env, which always wins) plus the runtime's defaults
	// (WINEPREFIX, BOX64_*, WINEDEBUG, WINEDLLOVERRIDES, ...).
	Env map[string]string
	// Flavor names the runtime for logs and the setup page ("Box64 + Wine
	// 10.0", "Hangover", "Wine", "Windows").
	Flavor string
	// Prefix is the WINEPREFIX the launch uses ("" on Windows).
	Prefix string
	// After runs once the program, and the display around it, have exited:
	// it stops a lingering wineserver and releases anything the plan set up
	// (a mounted prefix image). Never nil; call it also when the start
	// fails, and off the UI thread (it can take some seconds). Calling it
	// more than once is harmless.
	After func()
}

// planWineRuntime prepares the runtime half for exe. A first launch may block
// for minutes while the prefix is created, so callers run it off the UI
// thread; progress (may be nil) receives one-line updates. env holds the
// caller's variables, which win over the runtime's defaults; it is not
// modified. On Windows the plan runs exe directly.
func planWineRuntime(ctx context.Context, exe string, args []string, env map[string]string, progress func(string)) (wineRuntimePlan, error) {
	if err := ctx.Err(); err != nil {
		return wineRuntimePlan{}, err
	}
	if progress == nil {
		progress = func(string) {}
	}
	out := make(map[string]string, len(env)+8)
	for k, v := range env {
		out[k] = v
	}
	if IsWindows() {
		return wineRuntimePlan{Command: exe, Args: append([]string{}, args...), Env: out, Flavor: "Windows", After: func() {}}, nil
	}
	rt, err := resolveWineRT(exec.LookPath)
	if err != nil {
		return wineRuntimePlan{}, err
	}
	session, err := openWineSession(ctx, rt, out, progress)
	if err != nil {
		return wineRuntimePlan{}, err
	}
	applyWineCompatibilityEnv(out)
	return wineRuntimePlan{
		Command: rt.Command,
		Args:    rt.args(append([]string{exe}, args...)...),
		Env:     out,
		Flavor:  rt.Flavor,
		Prefix:  wineEnvValue(out, "WINEPREFIX"),
		After:   session.close,
	}, nil
}

// prepareWineRuntime gets the shared prefix ready without starting a program:
// the last step of the setup page's install, so the first launch is quick.
func prepareWineRuntime(ctx context.Context, progress func(string)) error {
	if IsWindows() {
		return nil
	}
	if progress == nil {
		progress = func(string) {}
	}
	rt, err := resolveWineRT(exec.LookPath)
	if err != nil {
		return err
	}
	session, err := openWineSession(ctx, rt, map[string]string{}, progress)
	if err != nil {
		return err
	}
	session.close()
	return nil
}

// wineRuntimeFacts is what the setup page and the launcher's status line show
// about the runtime. Reading it has no side effects and touches no network.
type wineRuntimeFacts struct {
	// Flavor is the runtime a launch would use ("" when there is none).
	Flavor string
	// Wine and Box64 are the resolved executables ("" when absent or not
	// needed).
	Wine  string
	Box64 string
	// WineVersion and PackTag describe JukaHub's own install when it is the
	// one in use (the Wine release, the JukaHub release the Box64 pack came
	// from).
	WineVersion string
	PackTag     string
	// Prefix is where the prefix lives or would live; PrefixProblem says why
	// none can (a card without symlinks and no internal storage).
	Prefix        string
	PrefixProblem string
	// PrefixImage is the ext4 image on the card the prefix lives in ("" when
	// it is plain storage); Prefix is then where it is mounted while a
	// program runs.
	PrefixImage string
	// PrefixReady reports a prefix that is already prepared, so the first
	// launch's few minutes of setup are behind.
	PrefixReady bool
	// Problem says why nothing can launch yet ("" when a launch can start).
	Problem string
}

// currentWineRuntimeFacts resolves the runtime without installing, mounting,
// downloading or starting anything.
func currentWineRuntimeFacts() wineRuntimeFacts {
	var facts wineRuntimeFacts
	if IsWindows() {
		facts.Flavor = "Windows"
		return facts
	}
	if strings.TrimSpace(os.Getenv("JUKAHUB_WINE")) == "" {
		if layout, problem := wineLayoutNow(); problem == "" {
			facts.Flavor = layout.flavor()
			facts.Wine = filepath.Join(wineWrapperDir(), "wine")
			facts.Box64 = layout.Box64
			facts.WineVersion = layout.Spec.Version
			facts.PackTag = layout.PackTag
		}
	}
	if facts.Flavor == "" {
		wine, err := resolveWine(exec.LookPath)
		switch {
		case err != nil:
			facts.Problem = "The Windows runtime is not installed"
		case needsBox64(wine):
			facts.Wine = wine
			box, boxErr := resolveBox64(exec.LookPath)
			if boxErr != nil {
				facts.Problem = "Wine is an x86 program and needs Box64: " + boxErr.Error()
			} else {
				facts.Box64, facts.Flavor = box, "Box64 + Wine"
			}
		default:
			facts.Wine, facts.Flavor = wine, "Wine"
			if wineWrapperIsHangover(wine) {
				facts.Flavor = "Hangover"
			}
		}
	}
	place := chooseWinePrefix(nil, false)
	facts.Prefix, facts.PrefixImage, facts.PrefixProblem = place.Dir, place.Image, place.Problem
	// An image prefix is ready once a session finished preparing it (the
	// marker beside the image): the image alone may hold a failed first run.
	facts.PrefixReady = wineIsPrefix(place.Dir) || (place.Image != "" && regularFile(place.Image) && regularFile(wineImageReadyPath(place.Image)))
	if facts.Problem == "" {
		facts.Problem = facts.PrefixProblem
	}
	return facts
}

// ── What starts Wine ─────────────────────────────────────────────────────

// wineRT is how this device starts a Wine program.
type wineRT struct {
	// Command is what is executed; Lead goes before Wine's own arguments
	// (a wrapper script for sh, or the Wine binary after Box64).
	Command string
	Lead    []string
	// Server is the command line that runs wineserver (nil when none was
	// found).
	Server []string
	Flavor string
	Wine   string
	Box64  string
	// Layout is JukaHub's own runtime; nil for another Wine.
	Layout *wineLayout
}

// args is Lead followed by rest.
func (r wineRT) args(rest ...string) []string {
	out := make([]string, 0, len(r.Lead)+len(rest))
	out = append(out, r.Lead...)
	return append(out, rest...)
}

// wineShell is the sh that runs wrapper scripts.
func wineShell(lookPath func(string) (string, error)) string {
	if lookPath != nil {
		if sh, err := lookPath("sh"); err == nil && sh != "" {
			return sh
		}
	}
	return "/bin/sh"
}

// wineScriptNeedsShell reports a script the card will not execute directly
// (exFAT and FAT can drop the executable bit).
func wineScriptNeedsShell(p string) bool {
	return !IsWindows() && strings.HasPrefix(wineReadHead(p, 2), "#!") && !executableFile(p)
}

// box64Launch is how a program runs under Box64: JukaHub's bin/box64 wrapper
// (the pack's box64 with its x86-64 libraries on the path) through sh, else
// whatever resolveBox64 finds.
func box64Launch(lookPath func(string) (string, error)) (string, []string, error) {
	if strings.TrimSpace(os.Getenv("JUKAHUB_BOX64")) == "" {
		if wrapper := wineBox64Wrapper(); wrapper != "" {
			return wineShell(lookPath), []string{wrapper}, nil
		}
	}
	box, err := resolveBox64(lookPath)
	if err != nil {
		return "", nil, err
	}
	return box, nil, nil
}

// wineProgramCommand is how program itself is started: through Box64 when it
// is an x86 program this device cannot run, through sh when it is a script the
// card will not execute, else directly. lead ends with program whenever the
// command is something else.
func wineProgramCommand(program string, lookPath func(string) (string, error)) (string, []string, error) {
	switch {
	case needsBox64(program):
		command, lead, err := box64Launch(lookPath)
		if err != nil {
			return "", nil, err
		}
		return command, append(lead, program), nil
	case wineScriptNeedsShell(program):
		return wineShell(lookPath), []string{program}, nil
	}
	return program, nil, nil
}

// wineServerFor finds the wineserver that belongs to wine.
func wineServerFor(wine string, lookPath func(string) (string, error)) string {
	if s := filepath.Join(filepath.Dir(wine), "wineserver"); regularFile(s) {
		return s
	}
	if lookPath != nil {
		if s, err := lookPath("wineserver"); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// resolveWineRT finds what starts Wine programs here: JukaHub's own runtime
// first (its wrappers are rewritten when they no longer match it), unless
// JUKAHUB_WINE names another Wine; else resolveWine's Wine.
func resolveWineRT(lookPath func(string) (string, error)) (wineRT, error) {
	if strings.TrimSpace(os.Getenv("JUKAHUB_WINE")) == "" {
		if layout, problem := wineLayoutNow(); problem == "" {
			if !wineWrappersCurrent(layout) {
				if err := writeWineWrappers(layout); err != nil {
					return wineRT{}, fmt.Errorf("could not write the Wine wrappers: %w", err)
				}
			}
			sh := wineShell(lookPath)
			dir := wineWrapperDir()
			return wineRT{
				Command: sh,
				Lead:    []string{filepath.Join(dir, "wine")},
				Server:  []string{sh, filepath.Join(dir, "wineserver")},
				Flavor:  layout.flavor(),
				Wine:    layout.wineBin(),
				Box64:   layout.Box64,
				Layout:  &layout,
			}, nil
		}
	}
	wine, err := resolveWine(lookPath)
	if err != nil {
		return wineRT{}, err
	}
	command, lead, err := wineProgramCommand(wine, lookPath)
	if err != nil {
		return wineRT{}, fmt.Errorf("wine is x86 and needs box64: %w", err)
	}
	rt := wineRT{Command: command, Lead: lead, Flavor: "Wine", Wine: wine}
	switch {
	case needsBox64(wine):
		rt.Flavor = "Box64 + Wine"
		if box, err := resolveBox64(lookPath); err == nil {
			rt.Box64 = box
		}
	case wineWrapperIsHangover(wine):
		rt.Flavor = "Hangover"
	}
	if server := wineServerFor(wine, lookPath); server != "" {
		if cmd, lead, err := wineProgramCommand(server, lookPath); err == nil {
			rt.Server = append([]string{cmd}, lead...)
		}
	}
	return rt, nil
}

// windowsWineCommand builds the Wine (or Box64 + Wine) command for an exe.
func windowsWineCommand(exe string, args []string, lookPath func(string) (string, error)) (string, []string, error) {
	rt, err := resolveWineRT(lookPath)
	if err != nil {
		return "", nil, err
	}
	return rt.Command, rt.args(append([]string{exe}, args...)...), nil
}

// hostRunsX86 reports whether this device runs an x86 program of the given
// ELF machine (3 = i386, 62 = x86-64) without Box64: an x86-64 PC runs both,
// a 32-bit x86 one only its own.
func hostRunsX86(machine uint16) bool {
	switch runtime.GOARCH {
	case "amd64":
		return true
	case "386":
		return machine == 0x03
	}
	return false
}

// ── WestonPack ───────────────────────────────────────────────────────────

const (
	// westonPackImageName is the runtime's name in PortMaster-New's
	// repository and in data/windows-runtime; westonPackPMName is the name
	// ports and harbourmaster give it in PortMaster's libs folder.
	westonPackImageName = "weston_pkg_0.2.aarch64.squashfs"
	westonPackPMName    = "weston_pkg_0.2.squashfs"
)

// westonPackSource is where the WestonPack display runtime is on this device:
// a SquashFS image the launch mounts (Image), or an unpacked folder holding
// westonwrap.sh (Wrapper). Both are empty when it is not installed.
type westonPackSource struct {
	Image   string
	Wrapper string
}

// westonPortMasterDirs are the PortMaster folders whose libs/ may already
// hold WestonPack: the TrimUI layout (Apps/PortMaster/PortMaster), the one the
// installed launcher implies, and other firmwares' usual places.
func westonPortMasterDirs() []string {
	sd := portMasterSDRoot
	var dirs []string
	if launcher := portMasterLauncher(sd); launcher != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(launcher), "PortMaster"), filepath.Dir(launcher))
	}
	dirs = append(dirs,
		filepath.Join(sd, "Apps", "PortMaster", "PortMaster"),
		filepath.Join(sd, "Ports", "PortMaster"),
		filepath.Join(sd, "Roms", "Ports", "PortMaster"),
		filepath.Join(sd, "Roms", "PORTS", "PortMaster"),
		filepath.Join(sd, "PortMaster"),
		"/roms/ports/PortMaster",
		"/roms2/ports/PortMaster",
		"/storage/roms/ports/PortMaster",
		"/userdata/system/.local/share/PortMaster",
		"/mnt/mmc/MUOS/PortMaster",
		"/opt/system/Tools/PortMaster",
	)
	return dedupeCleanPaths(dirs)
}

// westonImageCandidates lists where a WestonPack image may be: JukaHub's own
// download first, then PortMaster's copies.
func westonImageCandidates() []string {
	out := []string{
		filepath.Join(windowsRuntimeDir(), westonPackImageName),
		filepath.Join(windowsRuntimeDir(), westonPackPMName),
	}
	for _, dir := range westonPortMasterDirs() {
		out = append(out, filepath.Join(dir, "libs", westonPackPMName), filepath.Join(dir, "libs", westonPackImageName))
	}
	return out
}

// findWestonPackSource looks for WestonPack without mounting or downloading
// anything (it reads the first bytes of an image to see that it is SquashFS,
// and a westonwrap.sh's text). It must never call resolveWestonPack (the
// display half calls this).
//
// The order is: an explicit override; JukaHub's own copy (unpacked, then the
// image its setup row downloaded); PortMaster's images; and only then a
// westonwrap.sh someone unpacked elsewhere. Unpacked copies in other folders
// come last because one of them can be the CrossMix preview build whose bare
// `udevadm trigger` unmounts the SD card — the launch refuses that script
// (westonTextResetsDevices), and an earlier, official copy must not be
// shadowed by it. Such a script, or an image a launch found it in
// (westonImageRefused), does not count as WestonPack at all: the setup page
// then offers JukaHub's own download instead of calling it installed. Only
// JUKAHUB_WESTON is taken as it is, and the launch says why it refuses it.
func findWestonPackSource() westonPackSource {
	var src westonPackSource
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WESTON")); override != "" && executableFile(override) {
		src.Wrapper = override
		return src
	}
	usableImage := func(image string) bool {
		return westonImageProblem(image, false) == "" && !westonImageRefused(image)
	}
	usableWrapper := func(wrapper string) bool {
		return executableFile(wrapper) && !westonWrapperResetsDevices(wrapper)
	}
	if image := strings.TrimSpace(os.Getenv("JUKAHUB_WESTON_IMAGE")); image != "" && usableImage(image) {
		src.Image = image
		return src
	}
	for _, candidate := range []string{
		filepath.Join(windowsRuntimeDir(), "westonwrap.sh"),
		filepath.Join(windowsRuntimeDir(), "weston", "westonwrap.sh"),
	} {
		if usableWrapper(candidate) {
			src.Wrapper = candidate
			return src
		}
	}
	for _, image := range westonImageCandidates() {
		if usableImage(image) {
			src.Image = image
			return src
		}
	}
	for _, candidate := range []string{
		"/mnt/SDCARD/PortMaster/runtimes/westonpack/westonwrap.sh",
		"/mnt/SDCARD/PortMaster/runtimes/weston/westonwrap.sh",
		"/mnt/SDCARD/PortMaster/weston/westonwrap.sh",
		"/mnt/SDCARD/Apps/WestonPack/westonwrap.sh",
		"/tmp/weston/westonwrap.sh",
	} {
		if usableWrapper(candidate) {
			src.Wrapper = candidate
			return src
		}
	}
	return src
}

// ── DXVK / Vulkan ────────────────────────────────────────────────────────

// dxvkMarkerName records, in a DXVK JukaHub installed, which release it is.
const dxvkMarkerName = ".jukahub-dxvk.json"

// dxvkDLLNames are the DLLs a DXVK release may carry (1.x has d3d10 and
// d3d10_1; 2.x adds d3d8).
var dxvkDLLNames = []string{"d3d8", "d3d9", "d3d10", "d3d10_1", "d3d10core", "d3d11", "dxgi"}

// resolveDXVK finds a DXVK build installed beside Wine, by JukaHub's own
// installer or by PortMaster. The DLLs must match the Wine prefix's
// architecture, which the x64/x32 layout of a release provides. A detected
// build is only used when a Vulkan loader is also present, because DXVK cannot
// start without one.
func resolveDXVK() (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_DXVK")); override != "" {
		if !dxvkBuild(override) {
			return "", fmt.Errorf("JUKAHUB_DXVK does not point at a DXVK directory or its d3d11.dll: %s", override)
		}
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	candidates := []string{
		filepath.Join(windowsRuntimeDir(), "dxvk"),
		filepath.Join(windowsRuntimeDir(), "bin", "dxvk"),
		"/mnt/SDCARD/Apps/DXVK",
		"/mnt/SDCARD/PortMaster/runtimes/dxvk",
		"/mnt/SDCARD/PortMaster/dxvk",
		"/mnt/SDCARD/Apps/Wine/dxvk",
		"/mnt/SDCARD/PortMaster/wine/dxvk",
	}
	for _, candidate := range candidates {
		if dxvkBuild(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

func dxvkBuild(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return strings.EqualFold(filepath.Base(path), "d3d11.dll") || strings.EqualFold(filepath.Base(path), "d3d9.dll")
	}
	for _, rel := range []string{"x64/d3d11.dll", "d3d11.dll", "x64/d3d9.dll", "d3d9.dll"} {
		if info, err := os.Stat(filepath.Join(path, rel)); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

// resolveVulkan returns the first Vulkan loader found. DXVK is a Vulkan
// consumer; Wine's built-in D3D path does not require this. The lookup is
// intentionally filesystem-only so a Linux device without vulkaninfo still
// gets a useful diagnostic.
func resolveVulkan() string {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_VULKAN")); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override
		}
		return ""
	}
	candidates := []string{
		"/usr/lib/libvulkan.so.1",
		"/usr/lib64/libvulkan.so.1",
		"/lib/libvulkan.so.1",
		"/usr/trimui/lib/libvulkan.so.1",
		"/usr/lib/aarch64-linux-gnu/libvulkan.so.1",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	for _, pattern := range []string{"/usr/lib/*/libvulkan.so.1", "/lib/*/libvulkan.so.1"} {
		if matches, err := filepath.Glob(pattern); err == nil {
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					return match
				}
			}
		}
	}
	return ""
}

// vulkanICD remembers the newest API version the installed Vulkan drivers
// declare: drivers do not change while JukaHub runs.
var vulkanICD struct {
	once         sync.Once
	major, minor int
	ok           bool
}

// vulkanParseVersion reads "1.3.250" as 1, 3.
func vulkanParseVersion(v string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || major <= 0 || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

// vulkanICDVersion reads the api_version of the Vulkan driver manifests
// (icd.d/*.json); ok is false when none says.
func vulkanICDVersion() (int, int, bool) {
	vulkanICD.once.Do(func() {
		for _, dir := range []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d", "/usr/local/share/vulkan/icd.d", "/usr/trimui/share/vulkan/icd.d"} {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
			for i, m := range matches {
				if i >= 32 {
					break
				}
				var manifest struct {
					ICD struct {
						APIVersion string `json:"api_version"`
					} `json:"ICD"`
				}
				if !wineReadJSON(m, &manifest) {
					continue
				}
				major, minor, ok := vulkanParseVersion(manifest.ICD.APIVersion)
				if ok && (!vulkanICD.ok || major > vulkanICD.major || (major == vulkanICD.major && minor > vulkanICD.minor)) {
					vulkanICD.major, vulkanICD.minor, vulkanICD.ok = major, minor, true
				}
			}
		}
	})
	return vulkanICD.major, vulkanICD.minor, vulkanICD.ok
}

// wineVulkanLevel is this device's Vulkan API level: what its driver
// manifests declare, else 1.1 on a TrimUI (the Smart Pro's PowerVR driver);
// known is false elsewhere.
func wineVulkanLevel() (int, int, bool) {
	if major, minor, ok := vulkanICDVersion(); ok {
		return major, minor, true
	}
	if IsTrimuiDevice() {
		return 1, 1, true
	}
	return 0, 0, false
}

// dxvkVersionAt is the DXVK release in dir: the one JukaHub recorded when it
// installed it, else one a folder name like dxvk-1.10.3 gives; "" when
// unknown.
func dxvkVersionAt(dir string) string {
	var marker struct {
		Version string `json:"version"`
	}
	if wineReadJSON(filepath.Join(dir, dxvkMarkerName), &marker) && wineVersionMajor(marker.Version) > 0 {
		return marker.Version
	}
	base := strings.ToLower(filepath.Base(dir))
	if v := strings.TrimPrefix(base, "dxvk-"); v != base && wineVersionMajor(v) > 0 {
		return v
	}
	return ""
}

// dxvkNeedsVulkan is the Vulkan level a DXVK release needs: 1.1 for the
// legacy 1.x line, 1.3 for 2.x and for a build whose version is unknown.
func dxvkNeedsVulkan(version string) (int, int) {
	if wineVersionMajor(version) == 1 {
		return 1, 1
	}
	return 1, 3
}

// dxvkUsableHere reports whether the DXVK in dir can run on this device's
// Vulkan, and why not. JUKAHUB_DXVK is the user's own choice and always is.
func dxvkUsableHere(dir string) (bool, string) {
	if strings.TrimSpace(os.Getenv("JUKAHUB_DXVK")) != "" {
		return true, ""
	}
	have, haveMinor, known := wineVulkanLevel()
	if !known {
		return true, ""
	}
	need, needMinor := dxvkNeedsVulkan(dxvkVersionAt(dir))
	if have > need || (have == need && haveMinor >= needMinor) {
		return true, ""
	}
	return false, fmt.Sprintf("this DXVK needs Vulkan %d.%d, and this device has %d.%d", need, needMinor, have, haveMinor)
}

// dxvkArchDir is the folder of one architecture's DLLs in a DXVK build ("" for
// x32 in a flat build, which has 64-bit DLLs only).
func dxvkArchDir(dir, arch string) string {
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	if info, err := os.Stat(filepath.Join(dir, arch)); err == nil && info.IsDir() {
		return filepath.Join(dir, arch)
	}
	if arch == "x64" {
		return dir
	}
	return ""
}

// dxvkOverrideList is the WINEDLLOVERRIDES entry for the DLLs the build has
// ("d3d9,d3d10core,d3d11,dxgi=n,b"); "" when it has none.
func dxvkOverrideList(dir string) string {
	src := dxvkArchDir(dir, "x64")
	var names []string
	for _, name := range dxvkDLLNames {
		if regularFile(filepath.Join(src, name+".dll")) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ",") + "=n,b"
}

// dxvkCopyIntoPrefix puts the build's DLLs where Wine loads native DLLs from:
// the 64-bit ones in system32, the 32-bit ones in syswow64. A DLL already
// there with the same size is left alone. Without the override Wine keeps
// loading its own builtin versions, so the copies change nothing until DXVK
// is enabled.
func dxvkCopyIntoPrefix(dir, prefix string) error {
	for _, pair := range [][2]string{{"x64", "system32"}, {"x32", "syswow64"}} {
		src := dxvkArchDir(dir, pair[0])
		dst := filepath.Join(prefix, "drive_c", "windows", pair[1])
		if src == "" {
			continue
		}
		if info, err := os.Stat(dst); err != nil || !info.IsDir() {
			continue
		}
		for _, name := range dxvkDLLNames {
			from := filepath.Join(src, name+".dll")
			fromInfo, err := os.Stat(from)
			if err != nil || !fromInfo.Mode().IsRegular() {
				continue
			}
			to := filepath.Join(dst, name+".dll")
			if toInfo, err := os.Stat(to); err == nil && toInfo.Size() == fromInfo.Size() {
				continue
			}
			if err := copyArchiveFile(from, to, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyWineCompatibilityEnv sets the default DLL overrides (no Mono or Gecko
// prompt, no menu entries) when the caller set none, and enables DXVK on top
// of them only when its DLLs, a Vulkan loader and a Vulkan level the build can
// run on are all there (the DLLs are copied into the prefix first). Overrides
// the caller set win; silently replacing them would make a working setup stop
// working.
func applyWineCompatibilityEnv(env map[string]string) (dxvk string, vulkan bool) {
	if env == nil {
		return "", false
	}
	if !hasEnvKey(env, "WINEDLLOVERRIDES") {
		env["WINEDLLOVERRIDES"] = wineDefaultDLLOverrides
	}
	dxvk, _ = resolveDXVK()
	vulkan = resolveVulkan() != ""
	if dxvk == "" || !vulkan {
		return dxvk, vulkan
	}
	if ok, why := dxvkUsableHere(dxvk); !ok {
		logDebug("windowsapps", "DXVK in %s stays off: %s", dxvk, why)
		return dxvk, vulkan
	}
	list := dxvkOverrideList(dxvk)
	if list == "" || wineEnvValue(env, "WINEDLLOVERRIDES") != wineDefaultDLLOverrides {
		return dxvk, vulkan
	}
	if prefix := wineEnvValue(env, "WINEPREFIX"); wineIsPrefix(prefix) {
		if err := dxvkCopyIntoPrefix(dxvk, prefix); err != nil {
			logWarn("windowsapps", "DXVK stays off: could not copy its DLLs into %s: %v", prefix, err)
			return dxvk, vulkan
		}
	}
	wineSetEnv(env, "WINEDLLOVERRIDES", wineDefaultDLLOverrides+";"+list)
	return dxvk, vulkan
}

// ensureWinePrefix sets WINEPREFIX to the shared prefix unless the caller
// supplied one. It only chooses the place; planWineRuntime also creates the
// prefix, and is the only way to use one that lives in a mounted image.
func ensureWinePrefix(env map[string]string) error {
	if hasEnvKey(env, "WINEPREFIX") {
		return nil
	}
	place := chooseWinePrefix(env, true)
	switch {
	case place.Problem != "":
		return withLaunchFailureKind(launchFailureRuntimeMissing, fmt.Errorf("%s", place.Problem))
	case place.Image != "":
		return fmt.Errorf("the Windows folder lives in %s, which only a planned launch mounts", place.Image)
	}
	if err := os.MkdirAll(place.Dir, 0o755); err != nil {
		return fmt.Errorf("create wine prefix: %w", err)
	}
	env["WINEPREFIX"] = place.Dir
	return nil
}
