package main

// xremote.go — X1: Xbox One (and Series) Remote Play from the home header.
//
// The X1 pill sits right of the PS4 and PS5 pills (pspills.go) and does the
// same one-press job for an Xbox: stream the console in the living room to
// the machine in hand. JukaHub does not speak Microsoft's xHome protocol
// itself, so the pill starts the best client this machine has:
//
//   - On the handheld (linux/arm64): GreenOvercast, a native Xbox streaming
//     client for small ARM64 handhelds, in the build that adds xHome (console
//     streaming) next to Xbox Cloud Gaming. It is a PortMaster port, so it is
//     installed the PortMaster way: JukaHub downloads greenovercast.zip into
//     PortMaster's autoinstall folder and opens PortMaster, which installs it
//     with the runtimes it declares (the same path minecraft.go takes). Once
//     it is in Ports, X1 launches it directly. Sign-in happens inside
//     GreenOvercast: it shows a code to enter on a phone.
//   - On a PC: Greenlight or XStreaming when installed (both stream an Xbox
//     console), otherwise xbox.com/play's console page in the browser, which
//     streams a console from the browser.
//
// The pill's dot is lit when a client is installed, as a PS pill's is when a
// console is paired. Nothing here claims a console was found or a sign-in
// happened: that is the client's to report.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// x1PillLabel is the pill's name in the Remote Play group.
	x1PillLabel = "X1"
	// x1ClientName is the handheld client's name in toasts and the card.
	x1ClientName = "GreenOvercast"
	// x1BrowserURL is xbox.com/play's console list: remote play from a
	// browser on a PC with no Xbox streaming app installed.
	x1BrowserURL = "https://www.xbox.com/play/consoles"
	// x1ReleasePage is where a player can fetch the port by hand when the
	// download fails.
	x1ReleasePage = "github.com/doomL/GreenOvercast/releases"
	// x1ZipName is the port zip PortMaster expects in its autoinstall folder.
	x1ZipName = "greenovercast.zip"
	// x1GlibcWant is the oldest glibc the GreenOvercast build loads on.
	x1GlibcWant = "GLIBC_2.38"
	// x1ScanTTL is how long a client scan answers the pill's dot.
	x1ScanTTL = 3 * time.Second
	// x1QueuedGrace is how long a queued zip is left for PortMaster before
	// X1 decides PortMaster passed over it and downloads it again.
	x1QueuedGrace = 10 * time.Minute
)

// x1ZipURLs are tried in order: the xHome build's newest release, then the
// release it shipped in. The upstream project streams Xbox Cloud Gaming only,
// so it is not a fallback for a console.
var x1ZipURLs = []string{
	"https://github.com/doomL/GreenOvercast/releases/latest/download/" + x1ZipName,
	"https://github.com/doomL/GreenOvercast/releases/download/v0.3.0-xhome/" + x1ZipName,
}

// x1TileRect is this frame's X1 pill; cleared when the pill is not drawn, so
// a stale rect cannot swallow clicks.
var x1TileRect sdl.Rect

// x1Client is a streaming client found on this machine.
type x1Client struct {
	name   string
	launch string // port script on the handheld, executable on a PC
	args   []string
	port   bool // launch through launchPortScript
}

var x1Scan struct {
	mu     sync.Mutex
	at     time.Time
	client x1Client
	found  bool
}

// x1Busy guards the one GreenOvercast download that may run.
var x1Busy struct {
	mu      sync.Mutex
	running bool
}

// x1PillSupported reports whether X1 can do anything here. The browser
// fallback works on any desktop, so the pill shows wherever Home does.
func x1PillSupported() bool { return true }

// x1OnHandheld reports whether X1 uses the PortMaster port.
func x1OnHandheld() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" }

// x1PortName reports whether a Ports launcher name is GreenOvercast.
func x1PortName(name string) bool {
	n := strings.ToLower(name)
	n = strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(n)
	return strings.Contains(n, "greenovercast")
}

// x1FindPort returns GreenOvercast's launcher script under roots. It reads
// names only (no manifest probe, unlike scanPortLaunchers), since the pill's
// dot asks from the render path.
func x1FindPort(roots []string) (portEntry, bool) {
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, item := range items {
			name := item.Name()
			if item.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".sh") {
				continue
			}
			label := strings.TrimSuffix(name, filepath.Ext(name))
			if !x1PortName(label) {
				continue
			}
			p := filepath.Join(root, name)
			if regularFile(p) {
				return portEntry{Name: label, Launch: p}, true
			}
		}
	}
	return portEntry{}, false
}

// x1FindDesktopClient returns an installed PC client: Greenlight first (the
// reference xHome client), then XStreaming.
func x1FindDesktopClient() (x1Client, bool) {
	var candidates []x1Client
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		pf := os.Getenv("ProgramFiles")
		add := func(name string, paths ...string) {
			for _, p := range paths {
				if p != "" {
					candidates = append(candidates, x1Client{name: name, launch: p})
				}
			}
		}
		join := func(root string, elem ...string) string {
			if strings.TrimSpace(root) == "" {
				return ""
			}
			return filepath.Join(append([]string{root}, elem...)...)
		}
		add("Greenlight",
			join(local, "Programs", "greenlight", "Greenlight.exe"),
			join(local, "Programs", "Greenlight", "Greenlight.exe"),
			join(pf, "Greenlight", "Greenlight.exe"))
		add("XStreaming",
			join(local, "Programs", "xstreaming", "XStreaming.exe"),
			join(local, "Programs", "XStreaming", "XStreaming.exe"),
			join(pf, "XStreaming", "XStreaming.exe"))
		for _, c := range candidates {
			if regularFile(c.launch) {
				return c, true
			}
		}
		return x1Client{}, false
	}
	for _, name := range []string{"greenlight", "Greenlight", "xstreaming", "XStreaming"} {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return x1Client{name: name, launch: p}, true
		}
	}
	if p, err := exec.LookPath("flatpak"); err == nil && p != "" {
		for _, dir := range []string{
			"/var/lib/flatpak/app/io.github.unknownskl.greenlight",
			filepath.Join(os.Getenv("HOME"), ".local/share/flatpak/app/io.github.unknownskl.greenlight"),
		} {
			if dirExists(dir) {
				return x1Client{name: "Greenlight", launch: p, args: []string{"run", "io.github.unknownskl.greenlight"}}, true
			}
		}
	}
	return x1Client{}, false
}

// x1FindClient scans for a client now.
func x1FindClient() (x1Client, bool) {
	if x1OnHandheld() {
		if e, ok := x1FindPort(portRoots(portMasterSDRoot)); ok {
			return x1Client{name: e.Name, launch: e.Launch, port: true}, true
		}
		return x1Client{}, false
	}
	return x1FindDesktopClient()
}

// x1CachedClient is the scan the pill draws from, refreshed every x1ScanTTL.
func x1CachedClient() (x1Client, bool) {
	x1Scan.mu.Lock()
	defer x1Scan.mu.Unlock()
	if x1Scan.at.IsZero() || time.Since(x1Scan.at) > x1ScanTTL {
		x1Scan.client, x1Scan.found = x1FindClient()
		x1Scan.at = time.Now()
	}
	return x1Scan.client, x1Scan.found
}

// x1InvalidateScan makes the next look rescan (an install just finished).
func x1InvalidateScan() {
	x1Scan.mu.Lock()
	x1Scan.at = time.Time{}
	x1Scan.mu.Unlock()
}

// x1TileOn is the pill's lit dot: a client is installed.
func x1TileOn() bool {
	_, ok := x1CachedClient()
	return ok
}

// x1TileActivate is A on the pill (or a click): play, or get a client.
func x1TileActivate() {
	if gameScreenSuspended() {
		return
	}
	x1InvalidateScan()
	client, ok := x1CachedClient()
	if ok {
		x1Launch(client)
		return
	}
	if x1OnHandheld() {
		x1InstallPort()
		return
	}
	if err := openExternalURL(x1BrowserURL); err != nil {
		PlayErrorSound()
		ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
	ShowToast("Xbox Remote Play opens in the browser — install Greenlight for an app", ToastKindInfo)
}

// x1Launch starts a found client.
func x1Launch(c x1Client) {
	if c.port {
		if have, old := hostGlibcBelow(x1GlibcWant); old {
			// Say it before the port fails to start, then let it try: a CFW
			// may ship its own newer runtime beside the port.
			ShowToast(x1ClientName+" needs glibc 2.38 and this firmware has "+
				strings.TrimPrefix(have, "GLIBC_")+" — it may not start", ToastKindWarning)
		}
		logDebug("xremote", "launch %s (%s)", c.name, c.launch)
		launchPortScript(c.name, c.launch)
		return
	}
	logDebug("xremote", "launch %s (%s %v)", c.name, c.launch, c.args)
	cmd := exec.Command(c.launch, c.args...)
	cmd.Dir = filepath.Dir(c.launch)
	if err := cmd.Start(); err != nil {
		logWarn("xremote", "launch %s failed: %v", c.launch, err)
		PlayErrorSound()
		ShowToast("Could not start "+c.name+" — "+firstLine(err.Error()), ToastKindError)
		return
	}
	go func() { _ = cmd.Wait() }()
	PlayActivateSound()
	ShowToast("Starting "+c.name+" for Xbox Remote Play", ToastKindSuccess)
}

// x1InstallPort gets GreenOvercast onto the handheld through PortMaster.
func x1InstallPort() {
	launcher := portMasterLauncher(portMasterSDRoot)
	if launcher == "" {
		if portMasterInstalling() {
			ShowToast("PortMaster is installing — press X1 again when it is done", ToastKindInfo)
			return
		}
		if installBarActive() {
			ShowToast("Another download is running — try again when it finishes", ToastKindInfo)
			return
		}
		if err := startPortMasterInstall(); err != nil {
			PlayErrorSound()
			ShowToast("X1 needs PortMaster — "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
		ShowToast("Installing PortMaster first — press X1 again when it is done", ToastKindInfo)
		return
	}
	inbox := mcAutoinstallDir(launcher)
	if inbox == "" {
		mcOpenPortMaster(launcher, "Copy "+x1ZipName+" from "+x1ReleasePage+" into PortMaster's autoinstall folder")
		return
	}
	if q := filepath.Join(inbox, x1ZipName); regularFile(q) {
		// Downloaded, not installed yet. A good zip queued a moment ago is
		// PortMaster's to finish; a broken one, or one PortMaster has already
		// passed over (it is still here after x1QueuedGrace and no port came
		// of it), is fetched again rather than reopening PortMaster forever.
		st, err := os.Stat(q)
		if err == nil && x1CheckPortZip(q) == nil && time.Since(st.ModTime()) < x1QueuedGrace {
			mcOpenPortMaster(launcher, "PortMaster is installing "+x1ClientName+" — close it when it is done, then press X1")
			return
		}
		logDebug("xremote", "dropping stale queued %s", q)
		_ = os.Remove(q)
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast("No internet — connect Wi-Fi, then press X1 again", ToastKindError)
		return
	}
	x1Busy.mu.Lock()
	if x1Busy.running {
		x1Busy.mu.Unlock()
		ShowToast(x1ClientName+" is already downloading", ToastKindInfo)
		return
	}
	if installBarActive() {
		x1Busy.mu.Unlock()
		ShowToast("Another download is running — try again when it finishes", ToastKindInfo)
		return
	}
	x1Busy.running = true
	x1Busy.mu.Unlock()

	PlayActivateSound()
	ShowToast("Downloading "+x1ClientName+" for Xbox Remote Play", ToastKindInfo)
	gen, card := installBarBeginRun(x1ClientName)
	ctx, cancel := mcRunContext(gen, card)
	go func() {
		defer cancel()
		defer func() {
			x1Busy.mu.Lock()
			x1Busy.running = false
			x1Busy.mu.Unlock()
		}()
		zipPath, err := x1DownloadZip(ctx, gen, inbox, x1ZipURLs)
		switch {
		case err == nil:
			installBarDoneRun(gen)
			logDebug("xremote", "%s queued for PortMaster: %s", x1ClientName, zipPath)
			tbxPost(func() {
				x1InvalidateScan()
				if gameScreenSuspended() || !x1HomeOnScreen(appConfig) {
					ShowToast(x1ClientName+" is downloaded — press X1 to let PortMaster install it", ToastKindSuccess)
					return
				}
				mcOpenPortMaster(launcher, "PortMaster is installing "+x1ClientName+" — close it when it is done, then press X1")
			})
		case ctx.Err() != nil:
			logDebug("xremote", "download cancelled")
			installBarAbortRun(gen)
		default:
			logWarn("xremote", "download failed: %v", err)
			installBarFailRun(gen, firstLine(err.Error()))
			tbxPost(func() {
				ShowToast("Could not download "+x1ClientName+" — get "+x1ZipName+" from "+x1ReleasePage, ToastKindError)
			})
		}
	}()
}

// x1DownloadZip fetches the port zip into inbox under a hidden name, checks
// it is the GreenOvercast port, and gives it its real name only then, so
// PortMaster never sees half a file or the wrong one.
func x1DownloadZip(ctx context.Context, gen uint64, inbox string, urls []string) (string, error) {
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", fmt.Errorf("PortMaster's autoinstall folder: %w", err)
	}
	final := filepath.Join(inbox, x1ZipName)
	part := filepath.Join(inbox, "."+x1ZipName+".part")
	progress := func(done, total int64) { mcCardUpdate(gen, installPhaseDownload, done, total) }
	var lastErr error
	for _, u := range urls {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		mcCardUpdate(gen, installPhaseDownload, 0, 0)
		if err := downloadFileWithProgressCtx(ctx, u, part, progress); err != nil {
			_ = os.Remove(part)
			lastErr = err
			logDebug("xremote", "download %s: %v", u, err)
			continue
		}
		mcCardUpdate(gen, installPhaseVerify, 0, 0)
		if err := x1CheckPortZip(part); err != nil {
			_ = os.Remove(part)
			lastErr = err
			continue
		}
		if err := mcSyncFile(part); err != nil {
			_ = os.Remove(part)
			return "", err
		}
		if err := os.Rename(part, final); err != nil {
			_ = os.Remove(part)
			return "", err
		}
		return final, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no download address")
	}
	return "", lastErr
}

// x1CheckPortZip accepts a readable zip shaped like a PortMaster port that
// carries GreenOvercast: a launcher script or port.json, and an entry named
// for it.
func x1CheckPortZip(p string) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("the download is not a zip: %w", err)
	}
	defer zr.Close()
	named, port := false, false
	for _, f := range zr.File {
		name := strings.TrimSuffix(f.Name, "/")
		base := filepath.Base(name)
		if x1PortName(base) || x1PortName(strings.SplitN(name, "/", 2)[0]) {
			named = true
		}
		if strings.EqualFold(filepath.Ext(base), ".sh") || strings.EqualFold(base, "port.json") {
			port = true
		}
	}
	if !named || !port {
		return errors.New("the download is not the " + x1ClientName + " port")
	}
	return nil
}

// x1HomeOnScreen reports whether Home is the scene showing, where the pill
// was pressed: PortMaster opens itself only there.
func x1HomeOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex == homeSceneIndex(config)
}
