package main

// androidpak.go — "AndroidPak": Tier-1 Android games without Android.
//
// The 1 GB TSP cannot afford the Android system image (~60% of its RAM is
// gone before a game draws a frame). But a large class of Android games are
// NDK games: the APK's Java layer is thin glue that merely calls into a
// native lib/*.so main entry. Those games can run as ordinary native
// processes if something loads their bionic-linked .so files on glibc — the
// AndroidRT runtime in player/androidrt (its own bionic loader, a bionic
// compatibility layer and liblog/libandroid/OpenSL ES/EGL shims, in the
// spirit of the ATL project's bionic_translation). JukaHub's job here is
// not to run that runtime in-process; it is to:
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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	Abis    []string        // every lib/<abi>/ directory present, sorted
	Chosen  string          // the ABI the game will actually run (arm64 preferred)
	MainLib string          // "lib/<abi>/<name>.so" — the native entry
	Title   androidPakTitle // a title AndroidRT knows by name (androidpak_titles.go)
}

// androidPakABIOrder matches the A133P: aarch64 first, then 32-bit ARM. x86
// ABIs are never chosen (an x86-only APK gets the "no ABI this device can
// run" answer); the triage still reports what an APK ships, so a dead package
// can be named as dead instead of "not supported".
var androidPakABIOrder = []string{"arm64-v8a", "armeabi-v7a"}

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
// staging would extract (the chosen ABI's libraries + assets +
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
		total = satAddBytes(total, declaredEntryBytes(f.UncompressedSize64))
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
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		slash := filepath.ToSlash(f.Name)
		names = append(names, slash)
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
				sizes[slash] = declaredEntryBytes(f.UncompressedSize64)
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
	title, err := androidPakDetectTitle(names, chosen)
	if err != nil {
		return androidPakLibs{}, err
	}
	return androidPakLibs{Abis: abils, Chosen: chosen, MainLib: mainLib, Title: title}, nil
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

// androidPakStages serialises staging against cleanup and counts the stages
// written into each directory. A launch's cleanup runs some seconds after its
// game exits; relaunching the same game inside that window re-staged the same
// directory, and the old cleanup then deleted the files the new run was using.
// Cleanup now only removes a stage that is still the one its launch wrote.
var androidPakStages struct {
	sync.Mutex
	gen map[string]uint64
}

// stageAndroidPakGen stages an APK for a launch and returns the stage's
// generation, which androidPakStageCleanupGC needs. A failed stage is
// removed at once — a partial extraction is RAM on the device's tmpfs.
func stageAndroidPakGen(id, apk string, libs androidPakLibs) (stageDir, libDir string, gen uint64, err error) {
	androidPakStages.Lock()
	defer androidPakStages.Unlock()
	stageDir = androidPakStageDir(id)
	if androidPakStages.gen == nil {
		androidPakStages.gen = map[string]uint64{}
	}
	androidPakStages.gen[stageDir]++
	gen = androidPakStages.gen[stageDir]
	libDir, err = stageAndroidPakInto(stageDir, apk, libs)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return "", "", 0, err
	}
	return stageDir, libDir, gen, nil
}

// stageAndroidPakInto does the extraction; androidPakStages is held.
func stageAndroidPakInto(stageDir, apk string, libs androidPakLibs) (libDir string, err error) {
	libDir = filepath.Join(stageDir, "lib", libs.Chosen)
	if err := os.RemoveAll(stageDir); err != nil {
		return "", fmt.Errorf("clear old stage: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(apk)
	if err != nil {
		return "", fmt.Errorf("open APK: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > androidPakStageMaxEntries {
		return "", fmt.Errorf("APK has %d entries (limit %d)", len(zr.File), androidPakStageMaxEntries)
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
			return "", fmt.Errorf("APK entry: %w", err)
		}
		target := filepath.Join(stageDir, filepath.FromSlash(slash))
		if !strings.HasPrefix(target, filepath.Clean(stageDir)+string(filepath.Separator)) {
			return "", fmt.Errorf("entry escapes staging dir: %q", slash)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		// The declared size is what the stage budget is summed from, and it is
		// a uint64 from the archive: converting it blindly let a header that
		// claims more than int64 holds wrap negative and pass the cap below.
		sum, ok := addDeclaredBytes(total, declaredEntryBytes(f.UncompressedSize64), androidPakStageMaxBytes)
		if !ok {
			return "", fmt.Errorf("staged files exceed %d bytes — APK too large for RAM staging", androidPakStageMaxBytes)
		}
		total = sum
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open %q: %w", slash, err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("create %q: %w", slash, err)
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, androidPakStageMaxBytes+1))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract %q: %w", slash, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("extract %q: %w", slash, closeErr)
		}
	}
	return libDir, nil
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
func androidPakStageCleanupGC(stageDir string, gen uint64) {
	if stageDir == "" {
		return
	}
	time.Sleep(androidPakStageCleanupDelay)
	androidPakStageRelease(stageDir, gen)
}

// androidPakStageRelease removes a launch's stage now, unless a later launch
// has re-staged the directory since (see androidPakStages).
func androidPakStageRelease(stageDir string, gen uint64) {
	if stageDir == "" {
		return
	}
	androidPakStages.Lock()
	defer androidPakStages.Unlock()
	if androidPakStages.gen[stageDir] != gen {
		return
	}
	removeAndroidPakStage(stageDir)
}

// androidPakDataDir is an AndroidPak game's persistent data directory: the
// runner hands it to the game as internalDataPath/externalDataPath (files/,
// external/, obb/ and cache/ inside), so saves and settings survive the
// tmpfs stage being cleaned up after the game exits.
func androidPakDataDir(id string) string {
	if id == "" {
		id = "default"
	}
	return filepath.Join(gameProfilesDir(), "androidpak-data", id)
}

// androidPakDirExists reports whether path is a readable directory; the
// shims-dir probe in buildAndroidPakCommand uses it to stay optional (current
// runtimes are one self-contained `loader`; only older layouts ship shims/).
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
		// Saves must outlive the tmpfs stage, which is deleted after every
		// exit; a profile's own env may point somewhere else.
		if strings.TrimSpace(env["ANDROIDPAK_DATA"]) == "" {
			env["ANDROIDPAK_DATA"] = androidPakDataDir(p.ID)
		}
		// The runtime describes the device to the game (panel, DPI, the
		// firmware's EGL window contract) from JukaHub's own detection.
		if d := DetectDevice(); d.IsTrimui() && strings.TrimSpace(env["ANDROIDPAK_DEVICE"]) == "" {
			env["ANDROIDPAK_DEVICE"] = string(d)
		}
		// A recognised title's package, for a runtime that cannot read the
		// binary manifest; the manifest wins when it can.
		if libs.Title.Package != "" && strings.TrimSpace(env["ANDROIDPAK_PACKAGE"]) == "" {
			env["ANDROIDPAK_PACKAGE"] = libs.Title.Package
		}
	}
	if libs.Title.Name != "" {
		logInfo("androidpak", "%s: %s", libs.Title.Name, libs.Title.Note)
	}
	// Current runtimes load the game with their own bionic loader and need
	// no search paths. An older runtime layout with a shims/ directory gets
	// what it relied on: ANDROIDPAK_SHIMS (preloaded by that runner) and the
	// staged lib dir plus shims put first on LD_LIBRARY_PATH — the device's
	// own GL userspace stays last so the PowerVR EGL always wins.
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

// androidPakRunReport is what the AndroidRT runner printed about the run that
// just ended. The runner's own bionic loader lists every undefined symbol
// before any game code runs and binds missing functions to traps:
//
//	[androidrt] undefined symbols: 2 (1 functions abort when called, 1 data zero-filled)
//	[androidrt] undefined symbol: AChoreographer_getInstance (func, needed by libgame.so, from libandroid.so@LIBANDROID)
//	[androidrt] trap: libgame.so called unimplemented AChoreographer_getInstance — aborting (...)
//	[androidrt] refusing to start: 2 undefined symbols and ANDROIDPAK_MISSING=strict
//	[androidrt] could not load /stage/lib/armeabi-v7a/libgame.so: 32-bit ELF (ELFCLASS32): ...
//
// Older runtimes (glibc dlopen) printed "could not load <lib>: ...: undefined
// symbol: NAME" instead; that line still parses into Symbol.
//
// The runner's JNI layer (androidrt/src/jnienv.c) answers what the game asks
// Java for. What it cannot provide is logged once per name as it happens and
// listed again in the exit report, both in the order the game asked:
//
//	[androidrt][jni] FindClass ("com/foo/Bar") -> null (no implementation, not in the APK)
//	[androidrt][jni] GetMethodID com/foo/Bar.baz(I)V -> not implemented; calls return 0
//	[androidrt][jni] GetFieldID com/foo/Bar.count I -> not implemented; reads return 0 until the game stores a value
//	[androidrt][jni] called com/foo/Bar.baz(I)V — not implemented, returned 0
//	[androidrt][jni] report: 1 unknown classes, 0 APK classes without implementation, 2 unimplemented members (1 called)
//	[androidrt][jni] report: FindClass returned null for (1):
//	[androidrt][jni] report:   com/foo/Bar
//	[androidrt][jni] report: unimplemented members ("call ..." = the game called it) (3):
//	[androidrt][jni] report:   com/foo/Bar.baz(I)V
//	[androidrt][jni] report:   call com/foo/Bar.baz(I)V
//	[androidrt][jni] report:   com/foo/Bar.count:I
//
// The report is printed only when the run reaches its end; a game that
// crashed leaves just the first-seen lines, which say the same in the same
// order, so they are the fallback.
type androidPakRunReport struct {
	Trap        string   // unimplemented function the game called; the run aborted there
	Undefined   []string // every undefined symbol the loader listed, in log order
	Refused     bool     // ANDROIDPAK_MISSING=strict refused to start
	LoadFailure string   // why the game library never loaded, when it did not
	Symbol      string   // the undefined symbol of a legacy dlopen failure
	JavaClasses []string // Java classes FindClass could not provide, in the order the game asked
	JavaMembers []string // methods ("C.m(sig)") and fields ("C.f:sig") nobody implements, same order
	JavaCalled  []string // the unimplemented methods the game actually called, same order
	JavaTotal   int      // distinct gaps the exit report counted: classes + members (0 without a report)
}

// androidPakJavaGaps collects one source of Java gaps (the exit report or
// the first-seen lines), each name once.
type androidPakJavaGaps struct {
	classes, members, called []string
	total                    int
	names                    map[string]bool
}

func (g *androidPakJavaGaps) add(list *[]string, kind, name string) {
	name = strings.TrimSpace(name)
	if name == "" || g.names[kind+" "+name] {
		return
	}
	if g.names == nil {
		g.names = map[string]bool{}
	}
	g.names[kind+" "+name] = true
	*list = append(*list, name)
}

func (g *androidPakJavaGaps) empty() bool {
	return len(g.classes)+len(g.members)+len(g.called) == 0
}

// parseAndroidPakJavaLine reads one "[androidrt][jni] " line (prefix
// already cut) into the report or first-seen collector. section carries
// which report list the item lines belong to.
func parseAndroidPakJavaLine(rest string, report, seen *androidPakJavaGaps, section *string) {
	if after, ok := strings.CutPrefix(rest, "report:"); ok {
		if item, isItem := strings.CutPrefix(after, "   "); isItem { // "report:   <entry>"
			item = strings.TrimSpace(item)
			switch {
			case strings.HasPrefix(item, "... and "):
			case *section == "classes":
				report.add(&report.classes, "class", item)
			case *section == "members":
				if name, called := strings.CutPrefix(item, "call "); called {
					report.add(&report.called, "call", name)
				} else {
					report.add(&report.members, "member", item)
				}
			}
			return
		}
		header := strings.TrimSpace(after)
		switch {
		case strings.HasPrefix(header, "FindClass returned null for"):
			*section = "classes"
		case strings.HasPrefix(header, "unimplemented members"):
			*section = "members"
		default:
			var classes, apk, members, called int
			if n, _ := fmt.Sscanf(header, "%d unknown classes, %d APK classes without implementation, %d unimplemented members (%d called)",
				&classes, &apk, &members, &called); n == 4 {
				report.total = classes + members // a called method was looked up first: one gap, not two
			}
			*section = ""
		}
		return
	}
	switch {
	case strings.HasPrefix(rest, "FindClass"): // FindClass ("C") -> null; the old stub printed FindClass("C")
		after := strings.TrimSpace(strings.TrimPrefix(rest, "FindClass"))
		if name, ok := strings.CutPrefix(after, "(\""); ok {
			if i := strings.Index(name, "\")"); i >= 0 && strings.Contains(name[i:], "-> null") {
				seen.add(&seen.classes, "class", name[:i])
			}
		}
	case strings.HasPrefix(rest, "GetMethodID "), strings.HasPrefix(rest, "GetStaticMethodID "):
		_, after, _ := strings.Cut(rest, " ")
		if name, _, found := strings.Cut(after, " -> not implemented"); found {
			seen.add(&seen.members, "member", name)
		}
	case strings.HasPrefix(rest, "GetFieldID "), strings.HasPrefix(rest, "GetStaticFieldID "):
		_, after, _ := strings.Cut(rest, " ")
		if name, _, found := strings.Cut(after, " -> not implemented"); found {
			if i := strings.LastIndex(name, " "); i > 0 { // "C.f sig" -> the report's "C.f:sig"
				name = name[:i] + ":" + name[i+1:]
			}
			seen.add(&seen.members, "member", name)
		}
	case strings.HasPrefix(rest, "called "):
		if name, _, found := strings.Cut(strings.TrimPrefix(rest, "called "), " — not implemented"); found {
			seen.add(&seen.called, "call", name)
		}
	}
}

// androidPakLastRunLog returns the part of a profile's launch log the most
// recent run wrote ("" without a log). The log is appended to on every
// launch; reading from the top named the oldest failure in the file, so a
// symbol already shimmed kept being reported after the rebuild.
func androidPakLastRunLog(id string) string {
	data, err := os.ReadFile(filepath.Join(gameProfilesLogsDir(), id+".log"))
	if err != nil {
		return ""
	}
	text := string(data)
	if i := strings.LastIndex(text, "=== "+id+" start "); i >= 0 {
		text = text[i:]
	}
	return text
}

// androidPakLeadingToken is s up to its first space, comma or parenthesis
// ("memcpy, version LIBC" → "memcpy").
func androidPakLeadingToken(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " ,("); i >= 0 {
		s = s[:i]
	}
	return s
}

// parseAndroidPakRunReport reads the runner's report lines out of one run's
// launch log.
func parseAndroidPakRunReport(text string) androidPakRunReport {
	var r androidPakRunReport
	var report, seen androidPakJavaGaps
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if jni, ok := strings.CutPrefix(line, "[androidrt][jni] "); ok {
			parseAndroidPakJavaLine(jni, &report, &seen, &section)
			continue
		}
		rest, ok := strings.CutPrefix(line, "[androidrt] ")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "trap: "):
			if _, name, found := strings.Cut(rest, " called unimplemented "); found && r.Trap == "" {
				r.Trap = androidPakLeadingToken(name)
			}
		case strings.HasPrefix(rest, "undefined symbol: "):
			if name := androidPakLeadingToken(strings.TrimPrefix(rest, "undefined symbol: ")); name != "" {
				r.Undefined = append(r.Undefined, name)
			}
		case strings.HasPrefix(rest, "refusing to start: "):
			r.Refused = true
		case strings.HasPrefix(rest, "could not load "):
			if _, name, found := strings.Cut(rest, " undefined symbol: "); found {
				if r.Symbol == "" {
					r.Symbol = androidPakLeadingToken(name)
				}
			} else if _, reason, found := strings.Cut(rest, ": "); found && r.LoadFailure == "" {
				r.LoadFailure = strings.TrimSpace(reason)
			}
		}
	}
	gaps := seen
	if !report.empty() {
		gaps = report
	}
	r.JavaClasses, r.JavaMembers, r.JavaCalled, r.JavaTotal = gaps.classes, gaps.members, gaps.called, report.total
	return r
}

// androidPakFirstJavaGap picks the Java gap a toast names: the first
// unimplemented method the game actually called (it got a default back and
// went on with it), else the first class it could not get, else the first
// member it looked up. total counts every gap the log lists once: a called
// method is also a member the game looked up.
func androidPakFirstJavaGap(r androidPakRunReport) (kind, name string, total int) {
	total = r.JavaTotal
	if total == 0 {
		total = len(r.JavaClasses) + len(r.JavaMembers)
	}
	switch {
	case len(r.JavaCalled) > 0:
		return "called", r.JavaCalled[0], total
	case len(r.JavaClasses) > 0:
		return "class", r.JavaClasses[0], total
	case len(r.JavaMembers) > 0:
		return "member", r.JavaMembers[0], total
	}
	return "", "", 0
}

// androidPakClip shortens s to at most n runes with an ellipsis.
func androidPakClip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// androidPakRunMessage words a failed run's report for the toast and the
// JukaHub log. toast is false when the launch log has nothing more specific
// to say than "it failed".
func androidPakRunMessage(id string, r androidPakRunReport, logPath string) (string, bool) {
	more := ""
	if n := len(r.Undefined); n > 1 {
		more = fmt.Sprintf(" (%d undefined symbols listed in the log)", n)
	}
	switch {
	case r.Trap != "":
		return fmt.Sprintf("AndroidPak: the game called %s, which AndroidRT does not implement yet%s — log: %s", r.Trap, more, logPath), true
	case r.Symbol != "":
		return fmt.Sprintf("AndroidPak stopped on undefined symbol: %s — add it to an AndroidRT shim, rebuild, retry (log: %s)", r.Symbol, logPath), true
	case r.Refused && len(r.Undefined) > 0:
		return fmt.Sprintf("AndroidPak refused to start: %d undefined symbols (first: %s) with ANDROIDPAK_MISSING=strict — log: %s", len(r.Undefined), r.Undefined[0], logPath), true
	case r.LoadFailure != "":
		return fmt.Sprintf("AndroidPak could not load the game: %s (log: %s)", androidPakClip(r.LoadFailure, 160), logPath), true
	}
	// The game ran: what it asked Java for and did not get is the best lead.
	if kind, name, total := androidPakFirstJavaGap(r); name != "" {
		javaMore := ""
		if total > 1 {
			javaMore = fmt.Sprintf(" (%d more Java gaps in the log)", total-1)
		}
		name = androidPakClip(name, 120)
		switch kind {
		case "called":
			return fmt.Sprintf("AndroidPak: the game called the Java method %s, which AndroidRT does not implement yet%s — log: %s", name, javaMore, logPath), true
		case "class":
			return fmt.Sprintf("AndroidPak: the game asked for the Java class %s, which AndroidRT does not provide yet%s — log: %s", name, javaMore, logPath), true
		default:
			return fmt.Sprintf("AndroidPak: the game looked up the Java member %s, which AndroidRT does not implement yet%s — log: %s", name, javaMore, logPath), true
		}
	}
	if n := len(r.Undefined); n > 0 {
		return fmt.Sprintf("%s first run failed (%d undefined symbols listed); loader output is in %s", id, n, logPath), false
	}
	return fmt.Sprintf("%s first run failed; loader output is in %s", id, logPath), false
}

// androidPakFirstRunReport runs in the launch reaper goroutine after an
// AndroidPak profile exits: when the run failed, the runner's report is read
// back out of the launch log — the toast names what to implement next, the
// JukaHub log line is where file paths go.
func androidPakFirstRunReport(id string, exitErr error) {
	if exitErr == nil {
		return
	}
	logPath := filepath.Join(gameProfilesLogsDir(), id+".log")
	message, toast := androidPakRunMessage(id, parseAndroidPakRunReport(androidPakLastRunLog(id)), logPath)
	logDebug("androidpak", "%s", message)
	if toast {
		ShowToast(message, ToastKindError)
	}
}
