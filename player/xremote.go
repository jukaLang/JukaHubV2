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
//     streaming) next to Xbox Cloud Gaming. It is a PortMaster port; the
//     builds, the download and the install live in greenovercast.go, shared
//     with Cloud Gaming, and go through the one port installer
//     (portsInstallZip). Once it is in Ports, X1 launches whichever build is
//     there, warning when it is the cloud build, which has no console
//     streaming. Sign-in happens inside GreenOvercast: it shows a code to
//     enter on a phone.
//   - On a PC: Greenlight or XStreaming when installed (both stream an Xbox
//     console), otherwise xbox.com/play's console page in the browser, which
//     streams a console from the browser.
//
// The pill's dot is lit only while an Xbox answers on this network
// (xremote_discovery.go): an installed client alone lit it until 2026-11-19,
// and a green X1 with no console on and nothing to stream read as
// "connected". Nothing here claims a sign-in happened or that streaming will
// work: that is the client's to report.

import (
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
	// x1BrowserURL is xbox.com/play's console list: remote play from a
	// browser on a PC with no Xbox streaming app installed.
	x1BrowserURL = "https://www.xbox.com/play/consoles"
	// x1ScanTTL is how long a client scan answers a press.
	x1ScanTTL = 3 * time.Second
	// x1ConsoleTab is where the xHome build lists consoles.
	x1ConsoleTab = "its Consoles tab (L1/R1)"
)

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
// names only (no manifest probe, unlike scanPortLaunchers), since a press
// asks on the main loop.
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
		dirs := []string{"/var/lib/flatpak/app/io.github.unknownskl.greenlight"}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dirs = append(dirs, filepath.Join(home, ".local/share/flatpak/app/io.github.unknownskl.greenlight"))
		}
		for _, dir := range dirs {
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

// x1CachedClient is the client scan a press acts on, refreshed every
// x1ScanTTL.
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

// x1TileOn is the pill's lit dot: an Xbox is on and answering here.
func x1TileOn() bool { return x1ConsoleSeen() }

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

// x1Confirm reports whether this press is the second within the window for
// what (a launch or an install); the first press arms it.
var x1Armed struct {
	what string
	at   time.Time
}

func x1Confirm(what string) bool {
	if x1Armed.what == what && time.Since(x1Armed.at) < 3*time.Second {
		x1Armed.what = ""
		return true
	}
	x1Armed.what, x1Armed.at = what, time.Now()
	return false
}

// x1Launch starts a found client. The handheld port's build and the glibc
// check read the card, so they run on a goroutine and the decision comes
// back to the main loop.
func x1Launch(c x1Client) {
	if c.port {
		go func() {
			b, _ := goBuildIn(c.launch, goPortDir(c.launch))
			_, _ = goGlibcOld()
			tbxPost(func() { x1LaunchPort(c, b) })
		}()
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

// x1LaunchPort starts GreenOvercast once the caveats are known: the cloud
// build has no console tab (it still starts, a newer upstream may have one).
// On older TrimUI firmware goLaunch prepares the private C runtime. Main loop.
func x1LaunchPort(c x1Client, b goBuild) {
	if gameScreenSuspended() {
		return
	}
	var why []string
	if !b.hasXHome() {
		why = append(why, "this "+goClientName+" is the cloud build, without console streaming (switch in Cloud Gaming)")
	}
	if len(why) > 0 && !x1Confirm("launch") {
		PlayToggleSound()
		ShowToast(hintSentence(capitalizeFirst(strings.Join(why, "; "))+" — press %s again to start it anyway", "A", "Open"), ToastKindWarning)
		return
	}
	warn := ""
	if !b.hasXHome() {
		warn = "cloud build: your Xbox may not be listed"
	}
	goLaunch(c.name, c.launch, x1ConsoleTab, warn)
}

// x1InstallPort gets GreenOvercast's xHome build onto the handheld
// (greenovercast.go). A private C runtime is prepared on first launch when
// this TrimUI firmware is too old for the port.
// PortMaster's own app may open to finish the install only while the screen
// X1 was pressed on is still showing.
func x1InstallPort() {
	scene := currentSceneIndex
	goInstall(goBuildXHome, func() bool { return currentSceneIndex == scene }, func(portsInstallResult) { x1InvalidateScan() })
}
