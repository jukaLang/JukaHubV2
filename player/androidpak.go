package main

// androidpak.go — "AndroidPak": Tier-1 Android games without Android.
//
// The 1 GB TSP cannot afford the Android system image (~60% of its RAM is
// gone before a game draws a frame). But a large class of Android games are
// NDK games: the APK's Java layer is thin glue that merely calls into a
// native lib/*.so main entry. Those games can run as ordinary native
// processes if something loads their bionic-linked .so files on glibc — the
// bionic translation runtime (the ATL project's bionic_translation loader)
// plus small shims for liblog/libandroid/OpenSL ES. JukaHub's job here is
// not to reimplement that runtime; it is to:
//
//   1. TRIAGE — inspect an APK read-only and report which ABIs it ships
//      (arm64-v8a first, matching the A133P), which .so is the native entry,
//      and refuse up front the things that will never work this way (no
//      native libs at all → a real Java app, out of scope).
//   2. STAGE — extract the parts a native process reads from disk
//      (lib/<abi>, assets/, res/, manifest, resources.arsc — never
//      classes.dex or META-INF) into a bounded tmpfs staging dir, so a play
//      session does not re-read the APK from the SD card and cannot fill RAM
//      with the whole package.
//   3. PLAN/LAUNCH — build the command for the installed bionic runtime (a
//      `loader` executable or a runtime-provided launch.sh) and hand it
//      ANDROIDPAK_* environment variables pointing at the staged tree and
//      the chosen main library.
//
// The runtime itself is user-installed (JUKAHUB_ANDROIDRT override or the
// usual SD-card layouts). Without it a profile lists as Unavailable with the
// exact reason — the same posture as Wine/Box64 profiles.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	gameRuntimeAndroidPak = "androidpak"

	// Staging bounds. Cherry-picked extraction (see androidPakStagePrefixes)
	// keeps real games inside these; the caps exist so a padded APK cannot
	// claim all of the device's tmpfs.
	androidPakStageMaxEntries = 32768
	androidPakStageMaxBytes   = 512 << 20 // 512 MiB

	// Stage GC delay after the game exits, so the runtime can flush.
	androidPakStageCleanupDelay = 10 * time.Second
)

// androidPakLibs is the triage result for one APK.
type androidPakLibs struct {
	Abis    []string // every lib/<abi>/ directory present, sorted
	Chosen  string   // the ABI the game will actually run (arm64 preferred)
	MainLib string   // "lib/<abi>/<name>.so" — the native entry
}

// androidPakABIOrder matches the A133P: aarch64 first, then 32-bit ARM.
// x86 ABIs are never chosen; the triage reports what an APK ships so a dead
// package can be named as dead instead of "not supported".
var androidPakABIOrder = []string{"arm64-v8a", "armeabi-v7a", "x86_64", "x86"}

// androidPakStagePrefixes selects what a native process needs on disk. DEX
// and signatures are deliberately skipped: this Tier-1 path never runs Java.
var androidPakStagePrefixes = []string{"lib/", "assets/", "res/"}

func isAndroidPakStageEntry(name string) bool {
	if name == "AndroidManifest.xml" || name == "resources.arsc" {
		return true
	}
	for _, prefix := range androidPakStagePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ── launch-time memory pre-flight ──────────────────────────────────────
//
// Staging caps what an APK can *put* in tmpfs, but says nothing about
// whether the device has room for it. This is a 1 GB handheld: a gaming
// launch that leaves the hub itself eligible for the OOM killer is a
// launch that fails in the worst visible way — mid-game, with the session
// gone. Full-Android firmwares survive this by trimming the system image
// (the GammaOS study in docs/ANDROID-GAMMAOS.md §4); the hub's side of the
// same discipline is refusing a launch whose pieces demonstrably do not
// fit, with the numbers in the panel instead of a mystery crash.
var androidPakMemInfoPath = "/proc/meminfo"

const (
	// bionic loader + shim libraries + a typical NDK game's heap/GL command
	// stream. Deliberately generous — the point is catching the launches
	// that cannot fit, not estimating precisely.
	androidPakRuntimeOverhead = 192 << 20

	// Room the hub itself needs to keep running while the game plays.
	androidPakHubReserve = 96 << 20
)

// androidPakStageFootprint sums the uncompressed sizes of the entries
// stageAndroidPak would extract (the chosen ABI's libraries + assets +
// res + manifest), without touching the disk. Rejected ABIs and DEX are
// skipped, matching the real staging work.
func androidPakStageFootprint(apk string, libs androidPakLibs) (int64, error) {
	zr, err := zip.OpenReader(apk)
	if err != nil {
		return 0, fmt.Errorf("open APK: %w", err)
	}
	defer zr.Close()
	chosenPrefix := filepath.ToSlash(filepath.Join("lib", libs.Chosen)) + "/"
	var total int64
	for _, f := range zr.File {
		slash := filepath.ToSlash(f.Name)
		if !isAndroidPakStageEntry(slash) {
			continue
		}
		if strings.HasPrefix(slash, "lib/") && !strings.HasPrefix(slash, chosenPrefix) {
			continue
		}
		total += int64(f.UncompressedSize64)
	}
	return total, nil
}

// androidPakMemAvailable reads MemAvailable out of a meminfo file, in
// bytes. Zero means the file could not be read or parsed — callers treat
// that as "unknown", never as an automatic refusal.
func androidPakMemAvailable(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "MemAvailable") {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "kB")), 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// androidPakPreflightFloor returns the minimum MemAvailable the launch
// needs: staged bytes + runtime overhead + the hub's own reserve.
// JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB (in MiB) replaces the hub reserve —
// set it to 0 to disable the check entirely on a device where it misfires.
func androidPakPreflightFloor(stageFootprint int64) int64 {
	reserve := int64(androidPakHubReserve)
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB")); override != "" {
		if mb, err := strconv.ParseInt(override, 10, 64); err == nil && mb >= 0 {
			reserve = mb << 20
		}
	}
	return stageFootprint + androidPakRuntimeOverhead + reserve
}

// androidPakPreflight checks, before anything is staged, that the device
// plausibly has room for the launch. A readable but too-small MemAvailable
// is a refusal with the exact numbers; an unreadable or unparseable meminfo
// lets the launch proceed — this check must never block a launch it cannot
// actually know about.
func androidPakPreflight(apk string, libs androidPakLibs) error {
	footprint, err := androidPakStageFootprint(apk, libs)
	if err != nil {
		return err
	}
	available := androidPakMemAvailable(androidPakMemInfoPath)
	if available <= 0 {
		return nil
	}
	floor := androidPakPreflightFloor(footprint)
	if available >= floor {
		return nil
	}
	mib := func(n int64) string { return fmt.Sprintf("%d MiB", n>>20) }
	return fmt.Errorf("not enough free memory for this launch: %s available, launch needs ~%s (staged %s + runtime %s, keeping %s for the hub itself) — free memory or set JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB=0 to launch anyway",
		mib(available), mib(floor), mib(footprint), mib(androidPakRuntimeOverhead), mib(androidPakPreflightFloor(footprint)-footprint))
}

// inspectAndroidPak triages an APK read-only. If the profile asks for a
// specific main library (by base name), it must exist inside one of the
// shipped ABIs; otherwise the largest .so of the preferred ABI is the entry.
func inspectAndroidPak(apk string, requestedMain string) (androidPakLibs, error) {
	zr, err := zip.OpenReader(apk)
	if err != nil {
		return androidPakLibs{}, fmt.Errorf("open APK: %w", err)
	}
	defer zr.Close()

	var manifest bool
	abils := []string{}
	seen := map[string]bool{}
	sizes := map[string]int64{} // "lib/<abi>/<name>.so" → uncompressed size
	for _, f := range zr.File {
		slash := filepath.ToSlash(f.Name)
		if slash == "AndroidManifest.xml" || slash == "resources.arsc" {
			manifest = true
		}
		if rest, ok := strings.CutPrefix(slash, "lib/"); ok {
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) == 2 && parts[1] != "" && strings.HasSuffix(parts[1], ".so") && !strings.Contains(parts[1], "/") {
				abi := parts[0]
				if !seen[abi] {
					seen[abi] = true
					abils = append(abils, abi)
				}
				sizes[slash] = int64(f.UncompressedSize64)
			}
		}
	}
	if !manifest {
		return androidPakLibs{}, errors.New("not an APK (no AndroidManifest.xml)")
	}
	if len(sizes) == 0 {
		return androidPakLibs{}, errors.New("no native libraries — this is a Java/Dalvik app, not a Tier-1 NDK game")
	}

	abiPrefix := func(abi string) string { return "lib/" + abi + "/" }
	pickMain := func(abi string) string {
		main, size := "", int64(-1)
		for name, sz := range sizes {
			if !strings.HasPrefix(name, abiPrefix(abi)) {
				continue
			}
			if requestedMain != "" && filepath.ToSlash(filepath.Base(name)) != requestedMain {
				continue
			}
			if sz > size {
				main, size = name, sz
			}
		}
		return main
	}
	var chosen, mainLib string
	for _, abi := range androidPakABIOrder {
		if main := pickMain(abi); main != "" {
			chosen, mainLib = abi, main
			break
		}
	}
	if chosen == "" {
		if requestedMain != "" {
			return androidPakLibs{}, fmt.Errorf("main library %q is in none of the shipped ABIs (%s)", requestedMain, strings.Join(abils, ", "))
		}
		return androidPakLibs{}, errors.New("no ABI this device can run (A133P wants arm64-v8a or armeabi-v7a)")
	}
	sort.Strings(abils)
	return androidPakLibs{Abis: abils, Chosen: chosen, MainLib: mainLib}, nil
}

// resolveAndroidRuntime finds the installed bionic translation runtime. The
// runtime contract is its own directory, either with a `loader` executable
// (bionic loader) or a runtime-provided `launch.sh` that owns the details.
func resolveAndroidRuntime(lookPath func(string) (string, error)) (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_ANDROIDRT")); override != "" {
		info, err := os.Stat(override)
		if err != nil {
			return "", fmt.Errorf("JUKAHUB_ANDROIDRT %q does not exist", override)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("JUKAHUB_ANDROIDRT %q is not a directory", override)
		}
		abs, err := filepath.Abs(filepath.Clean(override))
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	candidates := []string{
		"/mnt/SDCARD/Apps/AndroidRT",
		"/mnt/SDCARD/Apps/androidrt",
		"/mnt/SDCARD/Apps/bionic-translation",
		"/mnt/SDCARD/PortMaster/AndroidRT",
		"/mnt/SDCARD/PortMaster/runtimes/androidrt",
		"/mnt/SDCARD/Emus/PortMaster/AndroidRT",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
	}
	if lookPath != nil {
		if path, err := lookPath("androidrt"); err == nil && path != "" {
			return filepath.Dir(path), nil
		}
	}
	return "", errors.New("android runtime not found (install the bionic translation runtime or set JUKAHUB_ANDROIDRT)")
}

// androidRuntimeCommand picks how the runtime is started. The loader runs the
// game APK directly (argv: loader <apk> ...); a launch.sh wrapper form takes
// the APK as its first argument, like the WestonPack wrapper.
func androidRuntimeCommand(rt string, lookPath func(string) (string, error)) (string, []string, error) {
	if loader := filepath.Join(rt, "loader"); regularFile(loader) {
		return loader, nil, nil
	}
	if script := filepath.Join(rt, "launch.sh"); regularFile(script) {
		sh, err := lookPath("sh")
		if err != nil || sh == "" {
			return "", nil, errors.New("android runtime launch.sh needs sh, but it was not found")
		}
		return sh, []string{script}, nil
	}
	return "", nil, fmt.Errorf("android runtime at %s has no loader or launch.sh", rt)
}

// androidPakStageRoot is where staged packages live (tmpfs on the device).
func androidPakStageRoot() string {
	if stage := strings.TrimSpace(os.Getenv("JUKAHUB_ANDROID_STAGE")); stage != "" {
		return stage
	}
	return filepath.Join(os.TempDir(), "jukahub-android")
}

func androidPakStageDir(id string) string {
	return filepath.Join(androidPakStageRoot(), "pak-"+id)
}

// stageAndroidPak extracts the runtime-relevant subset of the APK (bounded)
// into a fresh staging dir and returns it with the chosen ABI's library
// directory. A previous stage for the same profile is removed first, so a
// rebuilt APK is never masked by stale files.
func stageAndroidPak(id, apk string, libs androidPakLibs) (stageDir, libDir string, err error) {
	stageDir = androidPakStageDir(id)
	libDir = filepath.Join(stageDir, "lib", libs.Chosen)
	if err := os.RemoveAll(stageDir); err != nil {
		return "", "", fmt.Errorf("clear old stage: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", "", err
	}
	zr, err := zip.OpenReader(apk)
	if err != nil {
		return "", "", fmt.Errorf("open APK: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > androidPakStageMaxEntries {
		return "", "", fmt.Errorf("APK has %d entries (limit %d)", len(zr.File), androidPakStageMaxEntries)
	}
	chosenPrefix := filepath.ToSlash(filepath.Join("lib", libs.Chosen)) + "/"
	var total int64
	for _, f := range zr.File {
		slash := filepath.ToSlash(f.Name)
		if !isAndroidPakStageEntry(slash) {
			continue
		}
		// Only the chosen ABI's libraries go to RAM; other ABIs (if any)
		// stay in the APK on the card.
		if strings.HasPrefix(slash, "lib/") && !strings.HasPrefix(slash, chosenPrefix) {
			continue
		}
		if err := sanitizeArchiveEntry(slash, f.Mode(), f.FileInfo().IsDir()); err != nil {
			return "", "", fmt.Errorf("APK entry: %w", err)
		}
		target := filepath.Join(stageDir, filepath.FromSlash(slash))
		if !strings.HasPrefix(target, filepath.Clean(stageDir)+string(filepath.Separator)) {
			return "", "", fmt.Errorf("entry escapes staging dir: %q", slash)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", "", err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > androidPakStageMaxBytes {
			return "", "", fmt.Errorf("staged files exceed %d bytes — APK too large for RAM staging", androidPakStageMaxBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", "", fmt.Errorf("open %q: %w", slash, err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return "", "", fmt.Errorf("create %q: %w", slash, err)
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, androidPakStageMaxBytes+1))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return "", "", fmt.Errorf("extract %q: %w", slash, copyErr)
		}
		if closeErr != nil {
			return "", "", fmt.Errorf("extract %q: %w", slash, closeErr)
		}
	}
	return stageDir, libDir, nil
}

// removeAndroidPakStage removes a staging dir, tolerating a missing one.
func removeAndroidPakStage(stageDir string) {
	if stageDir == "" {
		return
	}
	_ = os.RemoveAll(stageDir)
}

// androidPakStageCleanupGC runs in the launch reaper goroutine after the game
// exits: give the runtime a moment to flush, then free the tmpfs stage.
func androidPakStageCleanupGC(stageDir string) {
	if stageDir == "" {
		return
	}
	time.Sleep(androidPakStageCleanupDelay)
	removeAndroidPakStage(stageDir)
}

// androidPakPlanCheck is the androidpak-specific readiness check used by
// resolveGameProfileEntry: the APK must be a real package with an ABI this
// device runs, and the runtime must be installed with a usable entry point.
func androidPakPlanCheck(p GameProfile, apkPath string, lookPath func(string) (string, error)) error {
	if _, err := inspectAndroidPak(apkPath, p.MainLib); err != nil {
		return err
	}
	rt, err := resolveAndroidRuntime(lookPath)
	if err != nil {
		return err
	}
	if _, _, err := androidRuntimeCommand(rt, lookPath); err != nil {
		return err
	}
	return nil
}

// androidPakDirExists reports whether path is a readable directory; the
// shims-dir probe in buildAndroidPakCommand uses it to stay optional (a
// runtime without shims still launches — launch.sh prints the same warning).
func androidPakDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// buildAndroidPakCommand builds the launcher argv for an AndroidPak profile.
// The staged tree is handed over through ANDROIDPAK_* environment variables,
// so the runtime decides what to do with it — env survives both the loader
// form and a launch.sh form.
func buildAndroidPakCommand(p GameProfile, apkPath, stageDir string, libs androidPakLibs, env map[string]string) (string, []string, error) {
	rt, err := resolveAndroidRuntime(exec.LookPath)
	if err != nil {
		return "", nil, err
	}
	command, prefix, err := androidRuntimeCommand(rt, exec.LookPath)
	if err != nil {
		return "", nil, err
	}
	if env != nil {
		env["ANDROIDPAK_APK"] = apkPath
		env["ANDROIDPAK_STAGED"] = stageDir
		env["ANDROIDPAK_MAINLIB"] = libs.MainLib
		env["ANDROIDPAK_ABIS"] = strings.Join(libs.Abis, ",")
		env["ANDROIDPAK_RT"] = rt
	}
	// The launch.sh form arranges its own library paths and shims dir, but
	// a runtime that ships only the `loader` binary gets it from here: the
	// runner preloads <rt>/shims (ANDROIDPAK_SHIMS) and glibc resolves the
	// game's DT_NEEDED entries (liblog.so, libandroid.so, ...) through the
	// staged lib dir and the shims dir put first on LD_LIBRARY_PATH — the
	// device's own GL userspace stays last so the PowerVR EGL always wins.
	if env != nil {
		if shims := filepath.Join(rt, "shims"); androidPakDirExists(shims) {
			env["ANDROIDPAK_SHIMS"] = shims
			libDir := filepath.Join(stageDir, "lib", libs.Chosen)
			libsFirst := libDir + string(filepath.ListSeparator) + shims
			if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
				env["LD_LIBRARY_PATH"] = libsFirst + string(filepath.ListSeparator) + existing
			} else {
				env["LD_LIBRARY_PATH"] = libsFirst
			}
		}
	}
	args := append(append([]string{}, prefix...), apkPath)
	args = append(args, p.Args...)
	return command, args, nil
}

// androidPakFirstRunSymbol scans a profile's launch log for the AndroidRT
// loader's first-run failure line — the run exits 2 before any onCreate
// because glibc could not resolve a bionic symbol in the staged main lib.
// The line looks like:
//
//	[androidrt] could not load /stage/lib/arm64-v8a/libgame.so: \
//	...: undefined symbol: ATrace_beginSection
//
// and returns only the undefined symbol's name ("" when the run failed for
// a different reason: exit status, no ANativeActivity_onCreate, wall time).
func androidPakFirstRunSymbol(id string) string {
	data, err := os.ReadFile(filepath.Join(gameProfilesLogsDir(), id+".log"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "[androidrt] could not load") {
			continue
		}
		if _, symbol, found := strings.Cut(line, " undefined symbol: "); found {
			return strings.TrimSpace(symbol)
		}
		return ""
	}
	return ""
}

// androidPakFirstRunReport runs in the launch reaper goroutine after an
// AndroidPak profile exits: when the run failed, the first-run undefined
// symbol is read back out of the launch log and named — the toast is the
// "next shim" answer, the JukaHub log line is where file paths go.
func androidPakFirstRunReport(id string, exitErr error) {
	if exitErr == nil {
		return
	}
	symbol := androidPakFirstRunSymbol(id)
	logPath := filepath.Join(gameProfilesLogsDir(), id+".log")
	if symbol == "" {
		log.Printf("[androidpak] %s first run failed; loader output is in %s", id, logPath)
		return
	}
	message := fmt.Sprintf("AndroidPak stopped on undefined symbol: %s — add it to an AndroidRT shim, rebuild, retry (log: %s)", symbol, logPath)
	log.Printf("[androidpak] %s", message)
	ShowToast(message, ToastKindError)
}
