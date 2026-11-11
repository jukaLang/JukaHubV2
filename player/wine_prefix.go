package main

// wine_prefix.go — where Wine keeps its Windows (the prefix), and getting it
// ready before a program starts.
//
// A prefix is a folder holding drive_c and the registry, plus dosdevices/,
// whose entries are symbolic links (c: → ../drive_c, z: → /). The Smart Pro's
// card is exFAT or FAT32, which has no symbolic links, so the prefix cannot
// live there. The first rule that fits wins:
//
//  1. JUKAHUB_WINEPREFIX, when set;
//  2. the caller's WINEPREFIX (a game profile's own), when it already is a
//     prefix or its folder takes links. JukaHub's old default for profiles,
//     game-profiles/wineprefix, is only kept when a prefix is already there;
//  3. data/windows-runtime/prefix, when it already is a prefix or the folder
//     takes links (a PC, an ext4 card);
//  4. /mnt/UDISK/jukahub/wine-prefix: the Smart Pro's internal ext4 storage
//     (the WPE browser keeps its cache there for the same reason), when it
//     exists, takes links and has 1 GB free;
//  5. an ext4 image on the card, data/windows-runtime/prefix.img, mounted
//     under /tmp while a program runs (how PortMaster's Wine ports do it on
//     other handhelds); it needs root, loop devices and mkfs.ext4 or mke2fs;
//
// and otherwise the launch says why. The first launch creates the prefix with
// wineboot and no display (a few minutes under Box64); success is judged by
// the files it leaves, because Wine processes can end by SIGKILL even after
// they did their work. A prefix made by another Wine build is upgraded the same
// way when JukaHub's runtime changes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// winePrefixMarkerName records which Wine prepared a prefix.
	winePrefixMarkerName = ".jukahub-prefix.json"
	// wineDefaultDLLOverrides keeps Wine from offering to download Mono and
	// Gecko (a prompt nobody can answer on a handheld) and from writing
	// desktop menu entries.
	wineDefaultDLLOverrides = "mscoree,mshtml=;winemenubuilder.exe=d"
	// wineBootTimeout bounds the first-run setup, which takes about two
	// minutes under Box64 on a Cortex-A53.
	wineBootTimeout = 10 * time.Minute
	// winePrefixMinFree is the room internal storage needs for a new prefix:
	// a fresh Wine 10 prefix is a few hundred megabytes, and programs install
	// into it.
	winePrefixMinFree = int64(1 << 30)
	// winePrefixNoRoom is the problem shown when no rule fits.
	winePrefixNoRoom = "This card cannot hold Wine's links, and there is no internal storage or ext4 tool to make room for them"
)

var (
	// wineUDiskRoot is the Smart Pro's internal ext4 storage.
	wineUDiskRoot = "/mnt/UDISK"
	// wineImageMountDir is where the prefix image is mounted while a
	// program runs (tmpfs, so a power cut leaves nothing mounted).
	wineImageMountDir = "/tmp/jukahub-wine"
	// winePrefixMu serialises preparing and releasing prefixes: a mount, a
	// wineboot and an unmount never overlap.
	winePrefixMu sync.Mutex
)

// winePrefixPlace is where a launch's prefix lives.
type winePrefixPlace struct {
	Dir     string // the WINEPREFIX
	Image   string // the ext4 image holding it, "" when Dir is plain storage
	Mount   string // where Image is mounted while a program runs
	Source  string // which rule chose it, for logs
	Problem string // why no place works ("" when Dir is set)
}

// winePrefixMarker is what JukaHub records in a prefix it prepared.
type winePrefixMarker struct {
	Wine       string `json:"wine"`
	WineSHA256 string `json:"wine_sha256,omitempty"`
	Flavor     string `json:"flavor"`
	PreparedAt string `json:"prepared_at"`
}

// wineEnvValue reads key from env, ignoring case like hasEnvKey.
func wineEnvValue(env map[string]string, key string) string {
	for k, v := range env {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// wineSetEnv sets key in env, dropping any spelling of it in another case.
func wineSetEnv(env map[string]string, key, value string) {
	for k := range env {
		if strings.EqualFold(k, key) && k != key {
			delete(env, k)
		}
	}
	env[key] = value
}

// wineIsPrefix reports a folder that already holds a Wine prefix.
func wineIsPrefix(dir string) bool {
	return dir != "" && regularFile(filepath.Join(dir, "system.reg")) && regularFile(filepath.Join(dir, "user.reg"))
}

// winePrefixProblem says what a prepared prefix lacks, "" when it is
// complete: the three registry files, system32 and the drive links.
func winePrefixProblem(dir string) string {
	for _, name := range []string{"system.reg", "user.reg", "userdef.reg"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "its registry (" + name + ") is missing"
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "drive_c", "windows", "system32")); err != nil || !info.IsDir() {
		return "drive_c/windows/system32 is missing"
	}
	if _, err := os.Lstat(filepath.Join(dir, "dosdevices", "c:")); err != nil {
		return "its drive links (dosdevices) are missing"
	}
	return ""
}

// wineLegacyProfilePrefix reports JukaHub's old default prefix for game
// profiles, which sat on the card.
func wineLegacyProfilePrefix(dir string) bool {
	return filepath.Clean(dir) == filepath.Clean(filepath.Join(gameProfilesDir(), "wineprefix"))
}

// wineExistingAncestor is dir or the nearest folder above it that exists.
func wineExistingAncestor(dir string) string {
	dir = filepath.Clean(dir)
	for i := 0; i < 64; i++ {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// wineLinkProbes remembers which folders took a test link: a card does not
// change its file system while JukaHub runs.
var wineLinkProbes struct {
	mu      sync.Mutex
	results map[string]bool
}

// wineSymlinkProbe creates and removes a test link in dir.
func wineSymlinkProbe(dir string) bool {
	wineLinkProbes.mu.Lock()
	defer wineLinkProbes.mu.Unlock()
	if ok, seen := wineLinkProbes.results[dir]; seen {
		return ok
	}
	name := filepath.Join(dir, ".jukahub-link-test-"+strconv.Itoa(os.Getpid()))
	_ = os.Remove(name)
	ok := os.Symlink("jukahub-link-target", name) == nil
	if ok {
		target, err := os.Readlink(name)
		ok = err == nil && target == "jukahub-link-target"
	}
	_ = os.Remove(name)
	if wineLinkProbes.results == nil {
		wineLinkProbes.results = map[string]bool{}
	}
	wineLinkProbes.results[dir] = ok
	return ok
}

// winePrefixLinksOK reports whether a prefix could be made at dir: its file
// system takes symbolic links. The file-system type answers without writing
// anything; when it is unknown, probe makes a test link (and without probe the
// answer is a hopeful yes, for status lines).
func winePrefixLinksOK(dir string, probe bool) bool {
	existing := wineExistingAncestor(dir)
	if existing == "" {
		return false
	}
	if links, known := wineFSTakesLinks(existing); known {
		return links
	}
	if !probe {
		wineLinkProbes.mu.Lock()
		ok, seen := wineLinkProbes.results[existing]
		wineLinkProbes.mu.Unlock()
		return ok || !seen
	}
	return wineSymlinkProbe(existing)
}

// wineUDiskPrefix is the prefix on the Smart Pro's internal storage.
func wineUDiskPrefix() string { return filepath.Join(wineUDiskRoot, "jukahub", "wine-prefix") }

// wineUDiskUsable reports whether internal storage can take a new prefix.
func wineUDiskUsable(probe bool) bool {
	if info, err := os.Stat(wineUDiskRoot); err != nil || !info.IsDir() {
		return false
	}
	if free, err := freeBytes(wineUDiskRoot); err == nil && free < winePrefixMinFree {
		return false
	}
	if !probe {
		links, known := wineFSTakesLinks(wineUDiskRoot)
		return links || !known
	}
	parent := filepath.Dir(wineUDiskPrefix())
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return false
	}
	return winePrefixLinksOK(parent, true)
}

// wineFindTool finds a system tool on PATH or in the sbin folders a
// handheld's PATH may leave out.
func wineFindTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// wineMkfsTool is the formatter for the prefix image and the argument sets to
// try in order (the image path goes last). BusyBox's mke2fs makes ext2 and
// knows neither -t nor -q; ext2 holds links as well.
func wineMkfsTool() (string, [][]string) {
	if p := wineFindTool("mkfs.ext4"); p != "" {
		return p, [][]string{{"-F", "-q", "-m", "0", "-L", "JukaHubWine"}, {"-F", "-m", "0"}}
	}
	if p := wineFindTool("mke2fs"); p != "" {
		return p, [][]string{{"-t", "ext4", "-F", "-q", "-m", "0", "-L", "JukaHubWine"}, {"-F", "-m", "0", "-L", "JukaHubWine"}, {"-F"}}
	}
	if p := wineFindTool("mkfs.ext2"); p != "" {
		return p, [][]string{{"-F", "-m", "0"}, {"-F"}}
	}
	return "", nil
}

// wineCanMakeImage reports whether a prefix image could be made and mounted
// here: root, loop devices, a formatter and mount.
func wineCanMakeImage() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	loop := false
	for _, p := range []string{"/dev/loop-control", "/dev/loop0", "/dev/block/loop0"} {
		if _, err := os.Stat(p); err == nil {
			loop = true
			break
		}
	}
	if !loop || wineFindTool("mount") == "" {
		return false
	}
	tool, _ := wineMkfsTool()
	return tool != ""
}

// chooseWinePrefix applies the rules above to env (the caller's variables).
// probe allows test links and creating the internal-storage folder; without
// it the answer touches nothing, for status lines.
func chooseWinePrefix(env map[string]string, probe bool) winePrefixPlace {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WINEPREFIX")); override != "" {
		return winePrefixPlace{Dir: filepath.Clean(override), Source: "JUKAHUB_WINEPREFIX"}
	}
	if caller := strings.TrimSpace(wineEnvValue(env, "WINEPREFIX")); caller != "" {
		dir := filepath.Clean(caller)
		legacy := wineLegacyProfilePrefix(dir)
		switch {
		case wineIsPrefix(dir) && (!legacy || winePrefixLinksOK(dir, probe)):
			return winePrefixPlace{Dir: dir, Source: "WINEPREFIX"}
		case !legacy:
			if winePrefixLinksOK(dir, probe) {
				return winePrefixPlace{Dir: dir, Source: "WINEPREFIX"}
			}
			return winePrefixPlace{Problem: "WINEPREFIX " + dir + " is on a card that cannot hold Wine's links · leave WINEPREFIX out to use JukaHub's shared Windows folder"}
		case wineIsPrefix(dir) && probe:
			// JukaHub's old default, half made on a card without links: its
			// drive links can never be made there, so every launch would fail.
			// The shared prefix below is used instead; the folder is left alone.
			logInfo("windowsapps", "the old profile prefix %s is on a card that cannot hold Wine's links; using the shared Windows folder", dir)
		}
	}
	runtimeDir := windowsRuntimeDir()
	runtimePrefix := filepath.Join(runtimeDir, "prefix")
	if wineIsPrefix(runtimePrefix) || winePrefixLinksOK(runtimeDir, probe) {
		return winePrefixPlace{Dir: runtimePrefix, Source: "runtime folder"}
	}
	if udisk := wineUDiskPrefix(); wineIsPrefix(udisk) || wineUDiskUsable(probe) {
		return winePrefixPlace{Dir: udisk, Source: "internal storage"}
	}
	image := filepath.Join(runtimeDir, "prefix.img")
	if regularFile(image) || wineCanMakeImage() {
		return winePrefixPlace{Dir: filepath.Join(wineImageMountDir, "prefix"), Image: image, Mount: wineImageMountDir, Source: "image on the card"}
	}
	return winePrefixPlace{Problem: winePrefixNoRoom}
}

// ── Running Wine's own tools ─────────────────────────────────────────────

// wineCmdEnv is the environment for a Wine helper: JukaHub's own with env on
// top; headless also drops DISPLAY and WAYLAND_DISPLAY, so wineboot never
// tries to open a window.
func wineCmdEnv(env map[string]string, headless bool) []string {
	list := mergeGameProfileEnv(os.Environ(), env)
	if !headless {
		return list
	}
	out := make([]string, 0, len(list))
	for _, kv := range list {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.EqualFold(key, "DISPLAY") || strings.EqualFold(key, "WAYLAND_DISPLAY") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// errWineTimedOut is a helper that did not finish in its time.
var errWineTimedOut = errors.New("did not finish in time")

// wineRunBounded runs argv in its own process group with env, writing its
// output to out (nil discards it), and stops the group when ctx ends or limit
// passes. Output goes to a file, never a pipe: a wineserver that outlives the
// command would hold a pipe open and the wait with it.
func wineRunBounded(ctx context.Context, argv []string, env []string, limit time.Duration, out *os.File) error {
	if len(argv) == 0 {
		return errors.New("nothing to run")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	cmd.WaitDelay = 5 * time.Second
	processGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = screenChildKill(cmd)
		<-done
		return ctx.Err()
	case <-timer.C:
		_ = screenChildKill(cmd)
		<-done
		return errWineTimedOut
	}
}

// wineRunOutput runs a short system tool (mkfs, mount) with a time limit and
// returns what it printed.
func wineRunOutput(ctx context.Context, limit time.Duration, name string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := exec.CommandContext(runCtx, name, args...).CombinedOutput()
	return string(out), err
}

// wineLogFile opens data/windows-runtime/logs/<name> for appending, starting
// over once it passes the game-log cap.
func wineLogFile(name string) (*os.File, string) {
	dir := filepath.Join(windowsRuntimeDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, ""
	}
	p := filepath.Join(dir, name)
	rotateGameProfileLog(p)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, p
	}
	return f, p
}

// wineLogTail is the last non-empty line of a log, for an error message.
func wineLogTail(p string) string {
	if p == "" {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 4096
	if info.Size() > window {
		if _, err := f.Seek(info.Size()-window, 0); err != nil {
			return ""
		}
	}
	buf := make([]byte, window)
	n, _ := f.Read(buf)
	lines := strings.Split(strings.TrimSpace(string(buf[:n])), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" && !strings.HasPrefix(line, "===") {
			return firstLine(line)
		}
	}
	return ""
}

// ── The prefix image ─────────────────────────────────────────────────────

// wineIsMounted reports whether something is mounted at dir.
func wineIsMounted(dir string) bool {
	data, err := wineReadSmall("/proc/mounts", 4<<20)
	if err != nil {
		return false
	}
	want := filepath.Clean(dir)
	alt := want
	if real, err := filepath.EvalSymlinks(want); err == nil {
		alt = real
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if target := strings.ReplaceAll(fields[1], `\040`, " "); target == want || target == alt {
			return true
		}
	}
	return false
}

// wineImageSize picks the image size for free bytes on the card (free < 0
// when unknown): 2 GB when there is room for it, else 1 GB, or
// JUKAHUB_WINE_PREFIX_IMAGE_MB (512 to 3500, under FAT32's 4 GB file limit).
func wineImageSize(free int64) (int64, error) {
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_WINE_PREFIX_IMAGE_MB")); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb >= 512 && mb <= 3500 {
			size := mb << 20
			if free >= 0 && free < size+relPackSpare {
				return 0, fmt.Errorf("%w · Windows' space on the card needs %s, the card has %s", errRelPackNoSpace, formatInstallBytes(size), formatInstallBytes(free))
			}
			return size, nil
		}
	}
	for _, mb := range []int64{2048, 1024} {
		size := mb << 20
		if free < 0 || free >= size+size/4 {
			return size, nil
		}
	}
	return 0, fmt.Errorf("%w · Windows needs a 1 GB space on the card, which has %s free", errRelPackNoSpace, formatInstallBytes(max64(free, 0)))
}

// wineImageReadyPath marks, beside the prefix image on the card, that the
// prefix inside it was prepared: the image cannot be looked into without
// mounting it, and a fresh image whose first wineboot failed is not ready.
func wineImageReadyPath(image string) string { return image + ".ready" }

// wineMarkImageReady records that the prefix in image is prepared.
func wineMarkImageReady(image string) {
	marker := wineImageReadyPath(image)
	if regularFile(marker) {
		return
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		logDebug("windowsapps", "could not mark %s as prepared: %v", image, err)
	}
}

// wineCreateImage makes the ext4 image the prefix lives in when the card
// cannot hold links itself.
func wineCreateImage(ctx context.Context, image string, progress func(string)) error {
	// A new image holds no prefix yet, whatever an older one held.
	_ = os.Remove(wineImageReadyPath(image))
	free, err := freeBytes(filepath.Dir(image))
	if err != nil {
		free = -1
	}
	size, err := wineImageSize(free)
	if err != nil {
		return err
	}
	tool, argSets := wineMkfsTool()
	if tool == "" {
		return errors.New(winePrefixNoRoom)
	}
	progress("Making a " + formatInstallBytes(size) + " space for Windows on the card (once)…")
	tmp := image + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	formatted := false
	var lastErr error
	for _, args := range argSets {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		out, err := wineRunOutput(ctx, 3*time.Minute, tool, append(append([]string{}, args...), tmp)...)
		if err == nil {
			formatted = true
			break
		}
		lastErr = fmt.Errorf("%s: %v %s", filepath.Base(tool), err, firstLine(strings.TrimSpace(out)))
	}
	if !formatted {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not format Windows' space on the card: %w", lastErr)
	}
	if err := os.Rename(tmp, image); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	logInfo("windowsapps", "made the Wine prefix image %s (%s, %s)", image, formatInstallBytes(size), filepath.Base(tool))
	return nil
}

// wineMountImage mounts the prefix image at mount.
func wineMountImage(ctx context.Context, image, mount string) error {
	tool := wineFindTool("mount")
	if tool == "" {
		return errors.New("this device has no mount command")
	}
	var lastErr error
	for _, args := range [][]string{{"-o", "loop,noatime"}, {"-t", "ext4", "-o", "loop"}, {"-t", "ext2", "-o", "loop"}} {
		out, err := wineRunOutput(ctx, 30*time.Second, tool, append(append([]string{}, args...), image, mount)...)
		if err == nil && wineIsMounted(mount) {
			return nil
		}
		if err == nil {
			err = errors.New("nothing was mounted")
		}
		lastErr = fmt.Errorf("%v %s", err, firstLine(strings.TrimSpace(out)))
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("could not open Windows' space on the card (%s): %w", filepath.Base(image), lastErr)
}

// wineUnmount releases the prefix image and the loop device under it: -d asks
// umount to free the device too (BusyBox and util-linux both know it), since
// BusyBox's mount does not always set autoclear. A umount that refuses -d is
// run plain, and a device it left bound to the image is detached here. A
// program still holding a file there gets a lazy unmount: the kernel lets go
// once it exits.
func wineUnmount(mount string) {
	tool := wineFindTool("umount")
	if tool == "" || !wineIsMounted(mount) {
		return
	}
	loop := wineMountSource(mount)
	for try := 0; try < 3; try++ {
		for _, args := range [][]string{{"-d", mount}, {mount}} {
			_, _ = wineRunOutput(context.Background(), 15*time.Second, tool, args...)
			if wineIsMounted(mount) {
				continue
			}
			if len(args) == 1 {
				wineReleaseLoop(loop, wineLog{})
			}
			_ = os.Remove(mount)
			return
		}
		time.Sleep(time.Second)
	}
	if out, err := wineRunOutput(context.Background(), 15*time.Second, tool, "-l", mount); err != nil {
		logWarn("windowsapps", "could not unmount %s: %v %s", mount, err, firstLine(strings.TrimSpace(out)))
	}
}

// wineMountSource is the device mounted at dir ("" when nothing is): for the
// prefix image, the loop device to free after the unmount.
func wineMountSource(dir string) string {
	data, err := wineReadSmall("/proc/mounts", 4<<20)
	if err != nil {
		return ""
	}
	want := filepath.Clean(dir)
	alt := want
	if real, err := filepath.EvalSymlinks(want); err == nil {
		alt = real
	}
	source := ""
	for _, m := range parseProcMounts(string(data)) {
		if m.dir == want || m.dir == alt {
			// The last mount there is the one in use.
			source = m.source
		}
	}
	return source
}

// wineEnsureImage makes, mounts and opens the image a prefix lives in.
func wineEnsureImage(ctx context.Context, place winePrefixPlace, progress func(string)) error {
	if !wineIsMounted(place.Mount) {
		if !regularFile(place.Image) {
			if err := wineCreateImage(ctx, place.Image, progress); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(place.Mount, 0o755); err != nil {
			return err
		}
		if err := wineMountImage(ctx, place.Image, place.Mount); err != nil {
			return err
		}
	}
	return os.MkdirAll(place.Dir, 0o755)
}

// ── Preparing the prefix ─────────────────────────────────────────────────

// wineHomeCheck remembers whether JukaHub's HOME is a folder Wine can write
// its settings and caches to.
var wineHomeCheck struct {
	once sync.Once
	ok   bool
}

func wineHomeUsable() bool {
	wineHomeCheck.once.Do(func() {
		home := strings.TrimSpace(os.Getenv("HOME"))
		if home == "" || home == "/" {
			return
		}
		f, err := os.CreateTemp(home, ".jukahub-home-test-*")
		if err != nil {
			return
		}
		name := f.Name()
		f.Close()
		_ = os.Remove(name)
		wineHomeCheck.ok = true
	})
	return wineHomeCheck.ok
}

// wineApplyDefaultEnv sets what every launch gets unless the caller set it:
// WINEDEBUG=-all, and a writable HOME beside the prefix when JukaHub's own is
// missing or read-only. (WINEDLLOVERRIDES is applyWineCompatibilityEnv's.)
func wineApplyDefaultEnv(env map[string]string, prefix string) {
	if !hasEnvKey(env, "WINEDEBUG") {
		env["WINEDEBUG"] = "-all"
	}
	if !hasEnvKey(env, "HOME") && !wineHomeUsable() {
		// Named apart from the wine-<version> folders that old-runtime
		// cleanup removes.
		home := filepath.Join(filepath.Dir(prefix), "home")
		if err := os.MkdirAll(home, 0o755); err == nil {
			env["HOME"] = home
		}
	}
}

// winePrefixNeed says what a prefix needs before a launch: "init" when it is
// not a prefix yet, "update" when JukaHub's runtime changed since it was
// prepared, "" when it is ready.
func winePrefixNeed(dir string, rt wineRT) string {
	if !wineIsPrefix(dir) {
		return "init"
	}
	if rt.Layout == nil {
		return ""
	}
	var marker winePrefixMarker
	if !wineReadJSON(filepath.Join(dir, winePrefixMarkerName), &marker) || !strings.EqualFold(marker.WineSHA256, rt.Layout.Spec.SHA256) {
		return "update"
	}
	return ""
}

// wineBootPrefix creates (or, with update, upgrades) the prefix env names,
// with no display and the Mono/Gecko prompts off, then waits for wineserver
// to write the registry and stops anything left. The prefix's files decide
// whether it worked.
func wineBootPrefix(ctx context.Context, rt wineRT, env map[string]string, update bool) error {
	prefix := wineEnvValue(env, "WINEPREFIX")
	bootEnv := cloneGameProfileEnv(env)
	wineSetEnv(bootEnv, "WINEDLLOVERRIDES", wineDefaultDLLOverrides)
	if !hasEnvKey(bootEnv, "WINEDEBUG") {
		bootEnv["WINEDEBUG"] = "-all"
	}
	list := wineCmdEnv(bootEnv, true)
	logFile, logPath := wineLogFile("wineboot.log")
	if logFile != nil {
		defer logFile.Close()
	}
	flag := "--init"
	if update {
		flag = "--update"
	}
	if logFile != nil {
		fmt.Fprintf(logFile, "\n=== wineboot %s %s · %s · %s ===\n", flag, time.Now().Format(time.RFC3339), rt.Flavor, prefix)
	}
	started := time.Now()
	err := wineRunBounded(ctx, append([]string{rt.Command}, rt.args("wineboot", flag)...), list, wineBootTimeout, logFile)
	if len(rt.Server) > 0 {
		// wineserver writes the registry when it exits: wait for that, then
		// make sure nothing is left running.
		if ctx.Err() == nil {
			_ = wineRunBounded(ctx, append(append([]string{}, rt.Server...), "-w"), list, 90*time.Second, logFile)
		}
		_ = wineRunBounded(context.Background(), append(append([]string{}, rt.Server...), "-k"), list, 15*time.Second, logFile)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if problem := winePrefixProblem(prefix); problem != "" {
		msg := "Windows could not be prepared: " + problem
		if tail := wineLogTail(logPath); tail != "" {
			msg += " (" + tail + ")"
		} else if err != nil {
			msg += " (" + firstLine(err.Error()) + ")"
		}
		return errors.New(msg)
	}
	if err != nil {
		// Wine can be killed on its way out after it finished; the files say
		// the prefix is complete.
		logInfo("windowsapps", "wineboot ended with %v, but the prefix in %s is complete", err, prefix)
	}
	marker := winePrefixMarker{Flavor: rt.Flavor, PreparedAt: time.Now().UTC().Format(time.RFC3339)}
	if rt.Layout != nil {
		marker.Wine, marker.WineSHA256 = rt.Layout.Spec.Version, rt.Layout.Spec.SHA256
	}
	if err := wineWriteJSON(filepath.Join(prefix, winePrefixMarkerName), marker); err != nil {
		logWarn("windowsapps", "could not record the prefix's Wine: %v", err)
	}
	logInfo("windowsapps", "Wine prefix %s ready in %s (%s)", prefix, time.Since(started).Round(time.Second), flag)
	return nil
}

// ── Sessions ─────────────────────────────────────────────────────────────

// wineSession is one planned launch's hold on its prefix: the last one to
// close stops the prefix's wineserver and unmounts its image.
type wineSession struct {
	prefix string
	server []string
	env    map[string]string
	once   sync.Once
}

// wineSessions counts the open sessions per prefix, and which mounts the last
// one releases.
var wineSessions struct {
	mu     sync.Mutex
	refs   map[string]int
	mounts map[string]string
}

// wineSessionsOpen is how many Wine programs JukaHub planned and has not seen
// exit.
func wineSessionsOpen() int {
	wineSessions.mu.Lock()
	defer wineSessions.mu.Unlock()
	n := 0
	for _, refs := range wineSessions.refs {
		n += refs
	}
	return n
}

// wineReleaseIdleMount unmounts a prefix image no session holds, after a
// launch that failed before it took one. winePrefixMu is held.
func wineReleaseIdleMount(place winePrefixPlace) {
	if place.Image == "" {
		return
	}
	wineSessions.mu.Lock()
	held := wineSessions.refs[place.Dir] > 0
	wineSessions.mu.Unlock()
	if !held {
		wineUnmount(place.Mount)
	}
}

// openWineSession chooses the prefix for env, sets WINEPREFIX and the other
// defaults in env, mounts the prefix's image when it lives in one, creates or
// upgrades the prefix when needed (progress gets one line), and holds it until
// the session closes.
func openWineSession(ctx context.Context, rt wineRT, env map[string]string, progress func(string)) (*wineSession, error) {
	place := chooseWinePrefix(env, true)
	if place.Problem != "" {
		return nil, withLaunchFailureKind(launchFailureRuntimeMissing, errors.New(place.Problem))
	}
	winePrefixMu.Lock()
	defer winePrefixMu.Unlock()
	if place.Image != "" {
		if err := wineEnsureImage(ctx, place, progress); err != nil {
			wineReleaseIdleMount(place)
			return nil, err
		}
	} else if err := os.MkdirAll(place.Dir, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return nil, disk
		}
		return nil, fmt.Errorf("could not create the Windows folder %s: %w", place.Dir, err)
	}
	wineSetEnv(env, "WINEPREFIX", place.Dir)
	wineApplyDefaultEnv(env, place.Dir)
	if need := winePrefixNeed(place.Dir, rt); need != "" {
		if need == "update" {
			progress("Updating Windows for " + rt.Flavor + "…")
		} else {
			progress("Preparing Windows (first run, a few minutes)…")
		}
		if err := wineBootPrefix(ctx, rt, env, need == "update"); err != nil {
			wineReleaseIdleMount(place)
			return nil, err
		}
	}
	if place.Image != "" {
		wineMarkImageReady(place.Image)
	}
	wineSessions.mu.Lock()
	if wineSessions.refs == nil {
		wineSessions.refs = map[string]int{}
		wineSessions.mounts = map[string]string{}
	}
	wineSessions.refs[place.Dir]++
	if place.Image != "" {
		wineSessions.mounts[place.Dir] = place.Mount
	}
	wineSessions.mu.Unlock()
	logDebug("windowsapps", "Wine prefix %s (%s)", place.Dir, place.Source)
	return &wineSession{prefix: place.Dir, server: rt.Server, env: cloneGameProfileEnv(env)}, nil
}

// close releases the session once. The last session of a prefix stops its
// wineserver (bounded) and unmounts its image; it may take some seconds, so
// callers run it off the UI thread.
func (s *wineSession) close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		wineSessions.mu.Lock()
		if wineSessions.refs[s.prefix] > 0 {
			wineSessions.refs[s.prefix]--
		}
		wineSessions.mu.Unlock()
		winePrefixMu.Lock()
		defer winePrefixMu.Unlock()
		wineSessions.mu.Lock()
		last := wineSessions.refs[s.prefix] <= 0
		mount := ""
		if last {
			delete(wineSessions.refs, s.prefix)
			mount = wineSessions.mounts[s.prefix]
			delete(wineSessions.mounts, s.prefix)
		}
		wineSessions.mu.Unlock()
		if !last {
			return
		}
		if len(s.server) > 0 {
			if err := wineRunBounded(context.Background(), append(append([]string{}, s.server...), "-k"), wineCmdEnv(s.env, false), 10*time.Second, nil); err != nil {
				logDebug("windowsapps", "wineserver -k for %s: %v", s.prefix, err)
			}
		}
		if mount != "" {
			wineUnmount(mount)
		}
	})
}
