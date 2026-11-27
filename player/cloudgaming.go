package main

// cloudgaming.go — Cloud Gaming (Start → Games → Cloud Gaming): Xbox Cloud
// Gaming (Game Pass) and GeForce NOW.
//
// Both services run the game on their own servers and send video, so all a
// small machine has to do is decode a picture and send buttons. That is the
// honest way Game Pass reaches a handheld: the games Game Pass downloads are
// x86-64 Windows/Xbox programs behind the Xbox app and its copy protection,
// far beyond this hardware and beyond Wine.
//
// One list, in the tool screens' frame (tbxui.go). On the handheld:
//
//	Stream from the cloud   Xbox Cloud Gaming, GeForce NOW — through
//	                        GreenOvercast (greenovercast.go)
//	Your own consoles       Xbox Remote Play (X1), PS Remote Play
//	Client                  GreenOvercast: which build, glibc, switch build
//	                        What works here? (the Game Pass answer)
//
// On a PC: Xbox Cloud Gaming through Greenlight or XStreaming when installed,
// else xbox.com/play in the browser; GeForce NOW through NVIDIA's app, else
// OpenNOW (an open-source client), else play.geforcenow.com. The card is read
// on a scene task when the screen opens, and the handheld's again when the
// screen comes back from a port or PortMaster and every minute.
//
// Nothing here reports a sign-in, a plan or a stream: the clients do.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	cgXboxCloudURL  = "https://www.xbox.com/play"
	cgGFNWebURL     = "https://play.geforcenow.com"
	cgGFNAppURL     = "https://www.nvidia.com/en-us/geforce-now/download/"
	cgOpenNOWURL    = "https://github.com/OpenCloudGaming/OpenNOW/releases"
	cgGreenlightURL = "https://github.com/unknownskl/greenlight/releases"
	// The service names GreenOvercast shows, which the toasts repeat.
	cgSvcXbox = "Xbox Cloud Gaming"
	cgSvcGFN  = "GeForce NOW"
)

// cgDesktopApp is a streaming app found on a PC.
type cgDesktopApp struct {
	name   string
	launch string
	args   []string
}

type cgScreenState struct {
	visit tbxVisit
	nav   tbxNav

	gen       int
	scanned   bool
	scanOn    int // the visit whose scan is in flight, 0 for none
	scanAt    time.Time
	lastFrame time.Time
	st        goState
	glibcHave string
	glibcOld  bool
	// pmWasInstalling notices PortMaster's own install finishing.
	pmWasInstalling bool

	// The PC's clients.
	xbox      x1Client
	xboxFound bool
	gfn       cgDesktopApp
	gfnFound  bool
}

var cgScreen cgScreenState

// cgHandheld is whether GreenOvercast is the client here.
func cgHandheld() bool { return goOnHandheld() }

// ── Scene ───────────────────────────────────────────────────────────────

// cloudGamingEnsureScene gives the screen its scene in memory; the user's
// config is never rewritten for it. Startup and every reload.
func cloudGamingEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, cloudGamingSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: cloudGamingSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == cloudGamingSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// cloudGamingOpen opens the screen (Start menu, search, Home tiles).
func cloudGamingOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	cloudGamingEnsureScene(config)
	idx := sceneIndexExact(config, cloudGamingSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

type cgSceneModule struct{ sceneBase }

func (cgSceneModule) render(r *sdl.Renderer, c *Config) bool {
	cgRenderScreen(r, c)
	return true
}

func (cgSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), cgScreenAction)
}

func (cgSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), cgScreenAction)
}

func init() { registerScene(cgSceneModule{sceneBase{name: cloudGamingSceneName}}) }

// cgOnScreen reports whether Cloud Gaming is up.
func cgOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == cloudGamingSceneName
}

// cloudGamingSceneClick routes a click or tap: the open sheet first, then
// the rows.
func cloudGamingSceneClick(mx, my int32, config *Config) bool {
	if !cgOnScreen(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := cgScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	cgScreen.nav.focus = it.id
	cgActivate(config, it.id)
	return true
}

// cloudGamingSceneWheel scrolls the sheet or the list.
func cloudGamingSceneWheel(config *Config, y int32) bool {
	if !cgOnScreen(config) || y == 0 {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	cgScreen.nav.scrollBy(-y * 48)
	return true
}

// ── Work ────────────────────────────────────────────────────────────────

// cgScreenEnter runs once per visit.
func cgScreenEnter() {
	cgScreen.gen++
	cgScreen.nav.scroll = 0
	cgScreen.nav.focus = ""
	cgScreen.scanned = false
	cgScreen.scanOn = 0
	cgScreen.scanAt = time.Time{}
	cgScan()
}

// cgScan reads the card (handheld) or the PC's apps on a scene task.
func cgScan() {
	if cgScreen.scanOn == cgScreen.gen {
		return
	}
	gen := cgScreen.gen
	cgScreen.scanOn = gen
	cgScreen.scanAt = time.Now()
	handheld := cgHandheld()
	started := runSceneTask(func(ctx context.Context) {
		var st goState
		var have string
		var old bool
		var xbox x1Client
		var xboxOK bool
		var gfn cgDesktopApp
		var gfnOK bool
		if handheld {
			st = goScanState()
			have, old = goGlibcOld()
		} else {
			xbox, xboxOK = x1FindDesktopClient()
			gfn, gfnOK = cgFindGFNApp()
		}
		tbxPost(func() {
			if gen != cgScreen.gen {
				return
			}
			cgScreen.scanOn = 0
			cgScreen.scanned = true
			cgScreen.scanAt = time.Now()
			cgScreen.st, cgScreen.glibcHave, cgScreen.glibcOld = st, have, old
			cgScreen.xbox, cgScreen.xboxFound = xbox, xboxOK
			cgScreen.gfn, cgScreen.gfnFound = gfn, gfnOK
		})
	})
	if !started {
		cgScreen.scanOn = 0
	}
}

// cgFindGFNApp finds a GeForce NOW client on a PC: NVIDIA's app first, then
// OpenNOW. The install paths are the usual per-user ones (not verified on
// every release).
func cgFindGFNApp() (cgDesktopApp, bool) {
	if runtime.GOOS == "windows" {
		local, pf := os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles")
		var candidates []cgDesktopApp
		add := func(name string, root string, elem ...string) {
			if strings.TrimSpace(root) != "" {
				candidates = append(candidates, cgDesktopApp{name: name, launch: filepath.Join(append([]string{root}, elem...)...)})
			}
		}
		add("GeForce NOW", local, "NVIDIA Corporation", "GeForceNOW", "CEF", "GeForceNOW.exe")
		add("GeForce NOW", pf, "NVIDIA Corporation", "GeForceNOW", "CEF", "GeForceNOW.exe")
		add("OpenNOW", local, "Programs", "OpenNOW", "OpenNOW.exe")
		add("OpenNOW", local, "Programs", "opennow", "OpenNOW.exe")
		add("OpenNOW", pf, "OpenNOW", "OpenNOW.exe")
		for _, c := range candidates {
			if regularFile(c.launch) {
				return c, true
			}
		}
		return cgDesktopApp{}, false
	}
	if p, err := exec.LookPath("flatpak"); err == nil && p != "" {
		dirs := []string{"/var/lib/flatpak/app/com.nvidia.geforcenow"}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dirs = append(dirs, filepath.Join(home, ".local/share/flatpak/app/com.nvidia.geforcenow"))
		}
		for _, dir := range dirs {
			if dirExists(dir) {
				return cgDesktopApp{name: "GeForce NOW", launch: p, args: []string{"run", "com.nvidia.geforcenow"}}, true
			}
		}
	}
	for _, name := range []string{"opennow", "OpenNOW"} {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return cgDesktopApp{name: "OpenNOW", launch: p}, true
		}
	}
	return cgDesktopApp{}, false
}

// cgStartDesktop starts a PC client.
func cgStartDesktop(name, launch string, args []string, service string) {
	logDebug("cloudgaming", "launch %s (%s %v)", name, launch, args)
	cmd := hiddenCommand(launch, args...)
	cmd.Dir = filepath.Dir(launch)
	if err := cmd.Start(); err != nil {
		logWarn("cloudgaming", "launch %s failed: %v", launch, err)
		PlayErrorSound()
		ShowToast("Could not start "+name+" — "+firstLine(err.Error()), ToastKindError)
		return
	}
	go func() { _ = cmd.Wait() }()
	PlayActivateSound()
	trophyEvent(tkGameLaunch)
	ShowToast("Starting "+name+" for "+service, ToastKindSuccess)
}

// cgOpenWeb opens a page in the PC's browser.
func cgOpenWeb(link, why string) {
	if err := openExternalURL(link); err != nil {
		PlayErrorSound()
		ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
	if why != "" {
		ShowToast(why, ToastKindInfo)
	}
}

// ── Rows ────────────────────────────────────────────────────────────────

// cgServiceOK reports whether the installed build can stream svc.
func cgServiceOK(svc string, b goBuild) bool {
	if svc == "gfn" {
		return b.hasGFN()
	}
	return true
}

// cgServiceRow is the Xbox Cloud Gaming or GeForce NOW row on the handheld.
func cgServiceRow(svc string) tbxRowSpec {
	st := cgScreen.st
	row := tbxRowSpec{icon: "xbox", title: cgSvcXbox}
	tagline := "Game Pass games streamed from Microsoft's servers — plan hours apply"
	if svc == "gfn" {
		row.icon, row.title = cloudGamingGlyph, cgSvcGFN
		tagline = "PC games you own, streamed from NVIDIA's servers — free tier with ads and queues"
	}
	busyBuild, phase, busy := goInstallBusy()
	switch {
	case !cgScreen.scanned:
		row.subtitle, row.pill, row.tone = "Reading the card…", "…", tbxToneNeutral
	case busy:
		row.subtitle = "Getting " + goClientName + " (" + busyBuild.short() + ")"
		row.pill, row.tone = "Downloading…", tbxToneAccent
		if phase == installPhaseExtract {
			row.pill = "Installing…"
		}
	case st.Installed && !cgServiceOK(svc, st.Build):
		row.subtitle = hintSentence("Your "+goClientName+" is the xHome build, made before GeForce NOW — %s offers the cloud build", "A", "Switch")
		row.pill, row.tone = "Switch build", tbxToneWarn
	case st.Installed && cgScreen.glibcOld:
		if IsTrimuiDevice() {
			row.subtitle = tagline + " · JukaHub uses a private compatibility runtime"
			row.pill, row.tone = "Play", tbxToneGood
		} else {
			row.subtitle = "Needs glibc 2.38; this firmware has " + cgScreen.glibcHave
			row.pill, row.tone = "Try", tbxToneWarn
		}
	case st.Installed:
		row.subtitle = tagline
		row.pill, row.tone = "Play", tbxToneGood
	case st.Queued != "" && st.PMLauncher != "" && !cgServiceOK(svc, st.QueuedBuild):
		row.subtitle = "The " + goClientName + " waiting for PortMaster is the xHome build, without GeForce NOW"
		row.pill, row.tone = "Install", tbxToneAccent
	case st.Queued != "" && st.PMLauncher != "":
		row.subtitle = "Downloaded — PortMaster installs " + goClientName + " as soon as it opens"
		row.pill, row.tone = "Open PortMaster", tbxToneAccent
	case portMasterInstalling():
		row.subtitle, row.pill, row.tone = "Installing PortMaster first…", "Installing…", tbxToneAccent
	case cgScreen.glibcOld:
		if IsTrimuiDevice() {
			row.subtitle = "JukaHub prepares a compatibility runtime when you first play"
			row.pill, row.tone = "Install", tbxToneAccent
		} else {
			row.subtitle = "Needs glibc 2.38; this firmware has " + cgScreen.glibcHave
			row.pill, row.tone = "Needs newer system", tbxToneWarn
		}
	case st.PMLauncher == "":
		row.subtitle = hintSentence("Streams through "+goClientName+", a PortMaster port — %s installs PortMaster first", "A", "Install")
		row.pill, row.tone = "Get PortMaster", tbxToneNeutral
	default:
		row.subtitle = tagline + " · " + goClientName + " installs in one press"
		row.pill, row.tone = "Install", tbxToneAccent
	}
	return row
}

// cgDesktopRow is a service row on a PC.
func cgDesktopRow(svc string) tbxRowSpec {
	if svc == "gfn" {
		row := tbxRowSpec{icon: cloudGamingGlyph, title: cgSvcGFN}
		switch {
		case !cgScreen.scanned:
			row.subtitle, row.pill, row.tone = "Looking for a GeForce NOW app…", "…", tbxToneNeutral
		case cgScreen.gfnFound:
			row.subtitle = "Opens " + cgScreen.gfn.name + " — PC games you own, streamed from NVIDIA's servers"
			row.pill, row.tone = "Play", tbxToneGood
		default:
			row.subtitle = hintSentence("Plays in your browser at play.geforcenow.com — %s for the apps", "Y", "About")
			row.pill, row.tone = "Open", tbxToneAccent
		}
		return row
	}
	row := tbxRowSpec{icon: "xbox", title: cgSvcXbox}
	switch {
	case !cgScreen.scanned:
		row.subtitle, row.pill, row.tone = "Looking for an Xbox streaming app…", "…", tbxToneNeutral
	case cgScreen.xboxFound:
		row.subtitle = "Opens " + cgScreen.xbox.name + " — Game Pass games streamed from Microsoft's servers"
		row.pill, row.tone = "Play", tbxToneGood
	default:
		row.subtitle = "Plays in your browser at xbox.com/play — plan hours apply"
		row.pill, row.tone = "Open", tbxToneAccent
	}
	return row
}

// cgClientRow is GreenOvercast's row.
func cgClientRow() tbxRowSpec {
	st := cgScreen.st
	row := tbxRowSpec{icon: "download", title: goClientName}
	busyBuild, phase, busy := goInstallBusy()
	switch {
	case !cgScreen.scanned:
		row.subtitle, row.pill, row.tone = "Reading the card…", "…", tbxToneNeutral
	case busy:
		row.subtitle = "Getting the " + busyBuild.short()
		row.pill, row.tone = "Downloading…", tbxToneAccent
		if phase == installPhaseExtract {
			row.pill = "Installing…"
		}
	case st.Installed:
		row.subtitle = strings.ToUpper(st.Build.short()[:1]) + st.Build.short()[1:] + ": " + st.Build.streams()
		row.pill, row.tone = "Installed", tbxToneGood
		if cgScreen.glibcOld {
			if IsTrimuiDevice() {
				row.subtitle += " · compatibility runtime available"
			} else {
				row.pill, row.tone = "Needs newer system", tbxToneWarn
			}
		}
	case st.Queued != "":
		row.subtitle = "Waiting in PortMaster's autoinstall folder"
		if st.QueuedBuild != goBuildUnknown {
			row.subtitle = "The " + st.QueuedBuild.short() + " waits in PortMaster's autoinstall folder"
		}
		row.pill, row.tone = "Queued", tbxToneAccent
	default:
		row.subtitle = "The native ARM64 streaming client for both services"
		row.pill, row.tone = "Not installed", tbxToneNeutral
		if cgScreen.glibcOld {
			if IsTrimuiDevice() {
				row.subtitle = "JukaHub prepares a compatibility runtime when you first play"
			} else {
				row.subtitle = "Needs glibc 2.38; this firmware has " + cgScreen.glibcHave
			}
		}
	}
	return row
}

// ── Drawing ─────────────────────────────────────────────────────────────

func cgHeader() UIPageHeader {
	if !cgHandheld() {
		return tbxHeader("Cloud Gaming", "Xbox Cloud Gaming (Game Pass) and GeForce NOW on this PC", cloudGamingGlyph, "", ColorTextTertiary())
	}
	st := cgScreen.st
	status, ink := "Set up "+goClientName, ToastWarnInk()
	switch {
	case !cgScreen.scanned:
		status, ink = "Reading…", ColorTextTertiary()
	case st.Installed && cgScreen.glibcOld:
		if IsTrimuiDevice() {
			status, ink = "Ready to play", ToastSuccessInk()
		} else {
			status, ink = "Needs newer system", ToastWarnInk()
		}
	case st.Installed:
		status, ink = "Installed", ToastSuccessInk()
	}
	return tbxHeader("Cloud Gaming", "Xbox Cloud Gaming (Game Pass) and GeForce NOW through "+goClientName, cloudGamingGlyph, status, ink)
}

// cgRenderScreen draws the screen.
func cgRenderScreen(renderer *sdl.Renderer, config *Config) {
	if cgScreen.visit.begin() {
		cgScreenEnter()
	}
	// Read the card again after the screen was away (a port or PortMaster
	// had it: frames stop meanwhile), when PortMaster's install ends, when an
	// install of ours ends, and every minute.
	now := time.Now()
	gap := now.Sub(cgScreen.lastFrame)
	cgScreen.lastFrame = now
	installing := portMasterInstalling()
	pmDone := cgScreen.pmWasInstalling && !installing
	cgScreen.pmWasInstalling = installing
	if cgHandheld() && cgScreen.scanOn == 0 && !gameScreenSuspended() &&
		(gap > 2*time.Second || pmDone || now.Sub(cgScreen.scanAt) > time.Minute) {
		cgScan()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, cgHeader())
	body := frame.bodyRect()
	nav := &cgScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	cgRenderBody(renderer, config, fonts, frame)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, cgScreenHints())
}

func cgRenderBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame) {
	nav := &cgScreen.nav
	top := frame.bodyTop - nav.scroll
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	y := int32(0)
	row := 0
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	section := func(title, icon string) {
		if y > 0 {
			y += 14
		}
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}

	section("Stream from the cloud", cloudGamingGlyph)
	if cgHandheld() {
		addRow("xcloud", cgServiceRow("xcloud"))
		addRow("gfn", cgServiceRow("gfn"))
	} else {
		addRow("xcloud", cgDesktopRow("xcloud"))
		addRow("gfn", cgDesktopRow("gfn"))
	}
	section("Your own consoles", "gamepad")
	addRow("x1", tbxRowSpec{icon: "xbox", title: "Xbox Remote Play",
		subtitle: "The Xbox in your living room, over your network (X1 in the top bar)", pill: "Open", tone: tbxToneNeutral})
	addRow("ps", tbxRowSpec{icon: "playstation", title: "PS Remote Play",
		subtitle: "Your PS4 or PS5, over your network", pill: "Open", tone: tbxToneNeutral})
	if cgHandheld() {
		section("Client", "download")
		addRow("client", cgClientRow())
	} else {
		section("Help", "info")
	}
	addRow("help", tbxRowSpec{icon: "info", title: "What works here?",
		subtitle: "Game Pass, GeForce NOW and this machine — the honest answer", pill: "Read", tone: tbxToneNeutral})
	nav.extend(y)
}

// ── Input ───────────────────────────────────────────────────────────────

// cgScreenAction handles one action on the screen.
func cgScreenAction(config *Config, act tbxAction) bool {
	nav := &cgScreen.nav
	switch act {
	case tbxActConfirm:
		cgActivate(config, nav.focus)
		return true
	case tbxActY:
		switch nav.focus {
		case "xcloud", "help":
			PlayActivateSound()
			cgOpenGamePassSheet(config)
		case "gfn":
			PlayActivateSound()
			cgOpenGFNSheet(config)
		case "client":
			PlayActivateSound()
			cgOpenClientSheet(config)
		}
		return true
	case tbxActX:
		PlayToggleSound()
		cgScreen.scanOn = 0
		cgScan()
		ShowToast("Looking again", ToastKindInfo)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// cgScreenHints is the footer.
func cgScreenHints() []FooterHint {
	id := cgScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	label := ""
	switch id {
	case "xcloud", "gfn":
		if cgHandheld() {
			label = strings.TrimSuffix(cgServiceRow(id).pill, "…")
		} else {
			label = strings.TrimSuffix(cgDesktopRow(id).pill, "…")
		}
		switch label {
		case "Downloading", "Installing", "":
			label = ""
		case "Get PortMaster":
			label = "Install"
		case "Switch build":
			label = "Switch"
		}
	case "x1", "ps":
		label = "Open"
	case "client":
		label = "Details"
	case "help":
		label = "Read"
	}
	if label != "" {
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	switch id {
	case "xcloud", "gfn", "client":
		hints = append(hints, FooterHint{Button: "Y", Label: "About"})
	}
	hints = append(hints, FooterHint{Button: "X", Label: "Refresh"})
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// cgActivate runs the focused row's action.
func cgActivate(config *Config, id string) {
	switch id {
	case "xcloud", "gfn":
		if cgHandheld() {
			cgActivateService(config, id)
		} else {
			cgActivateDesktop(config, id)
		}
	case "x1":
		PlayActivateSound()
		x1TileActivate()
	case "ps":
		PlayActivateSound()
		openChiakiPanel()
	case "client":
		PlayActivateSound()
		cgOpenClientSheet(config)
	case "help":
		PlayActivateSound()
		cgOpenGamePassSheet(config)
	}
}

// cgScreenUp is goInstall's question: is this screen still the one showing?
func cgScreenUp() bool { return cgOnScreen(appConfig) }

// cgAfterInstall reads the card again once an install reports back.
func cgAfterInstall(portsInstallResult) {
	cgScreen.scanOn = 0
	cgScan()
}

// cgActivateService does what a handheld service row's pill says.
func cgActivateService(config *Config, svc string) {
	st := cgScreen.st
	service := cgSvcXbox
	if svc == "gfn" {
		service = cgSvcGFN
	}
	_, _, busy := goInstallBusy()
	switch {
	case !cgScreen.scanned:
		return
	case busy:
		ShowToast(goClientName+" is on its way — the status strip shows how far", ToastKindInfo)
	case st.Installed && !cgServiceOK(svc, st.Build):
		PlayActivateSound()
		cgOpenSwitchSheet(config, goBuildCloud)
	case st.Installed && cgScreen.glibcOld:
		goLaunch(st.Name, st.Launch, service, "")
	case st.Installed:
		goLaunch(st.Name, st.Launch, service, "")
	case st.Queued != "" && st.PMLauncher != "" && !cgServiceOK(svc, st.QueuedBuild):
		// The zip waiting for PortMaster is the xHome build: GeForce NOW needs
		// the cloud build, which replaces it once downloaded.
		cgInstallChecked(config, goBuildCloud)
	case st.Queued != "" && st.PMLauncher != "":
		PlayActivateSound()
		mcOpenPortMaster(st.PMLauncher, "PortMaster is installing "+goClientName+" — close it when it is done to come back")
	case portMasterInstalling():
		ShowToast("PortMaster is installing — "+goClientName+" comes right after", ToastKindInfo)
	case cgScreen.glibcOld:
		goInstall(goBuildCloud, cgScreenUp, cgAfterInstall)
	default:
		goInstall(goBuildCloud, cgScreenUp, cgAfterInstall)
	}
}

// cgActivateDesktop opens a service on a PC.
func cgActivateDesktop(config *Config, svc string) {
	if !cgScreen.scanned {
		return
	}
	if svc == "gfn" {
		if cgScreen.gfnFound {
			cgStartDesktop(cgScreen.gfn.name, cgScreen.gfn.launch, cgScreen.gfn.args, cgSvcGFN)
			return
		}
		cgOpenWeb(cgGFNWebURL, "GeForce NOW opens in the browser — sign in there")
		return
	}
	if cgScreen.xboxFound {
		cgStartDesktop(cgScreen.xbox.name, cgScreen.xbox.launch, cgScreen.xbox.args, cgSvcXbox)
		return
	}
	cgOpenWeb(cgXboxCloudURL, "Xbox Cloud Gaming opens in the browser — sign in with your Game Pass account")
}

// ── Sheets ──────────────────────────────────────────────────────────────

// cgOpenGamePassSheet is the honest answer to "does Game Pass work here?".
func cgOpenGamePassSheet(config *Config) {
	var b strings.Builder
	b.WriteString("Game Pass has two halves, and only one of them reaches a small machine.\n\n")
	b.WriteString("## Downloaded games — no\n")
	b.WriteString("The games Game Pass downloads to a PC or an Xbox are x86-64 Windows and Xbox programs that need the Xbox app, " +
		"Microsoft's copy protection, DirectX 12 and a desktop graphics card with gigabytes of its own memory. ")
	if cgHandheld() {
		b.WriteString("This handheld is an ARM computer with a phone-class graphics chip; Wine here runs small, older Windows games, not these.\n\n")
	} else {
		b.WriteString("A Windows PC that meets their requirements plays them in the Xbox app.\n\n")
	}
	b.WriteString("## Cloud gaming — yes\n")
	b.WriteString("Xbox Cloud Gaming runs the game on Microsoft's servers and sends video: the device only decodes the picture and sends your buttons. " +
		"It comes with Game Pass Essential, Premium and Ultimate, and since November 2026 each includes a set number of hours a month — " +
		"Essential 5, Premium 10, Ultimate 15; extra hours can be bought. Not every Game Pass game streams; the cloud ones are marked in the Xbox app.\n\n")
	if cgHandheld() {
		b.WriteString("## On this handheld\n")
		b.WriteString("JukaHub streams through " + goClientName + ", an open-source client for ARM handhelds that installs as a PortMaster port. " +
			"It is experimental and has not been tried on a TrimUI yet.\n")
		b.WriteString("• " + goGlibcNote() + "\n")
		if note := goDecodeNote(); note != "" {
			b.WriteString("• " + note + "\n")
		}
		b.WriteString("• Signing in shows a code: enter it at the address it shows, on your phone.\n\n")
	}
	b.WriteString("## GeForce NOW\n")
	b.WriteString("NVIDIA's service streams PC games you own on Steam, Epic, Ubisoft and others — many PC Game Pass games too, through your Xbox account. " +
		"A free membership plays with ads, queues and short sessions; paid memberships get priority access and longer sessions.\n\n")
	b.WriteString("## Your own Xbox\n")
	b.WriteString("Xbox Remote Play (X1) streams the console in your living room instead of Microsoft's servers.")
	buttons := []tbxSheetButton{{label: "Close", primary: true}}
	if !cgHandheld() {
		buttons = append(buttons, tbxSheetButton{label: "Get Greenlight", run: func() { cgOpenWeb(cgGreenlightURL, "") }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Game Pass here",
		subtitle: "What works and what cannot",
		icon:     "xbox",
		body:     b.String(),
		buttons:  buttons,
		tall:     true,
	})
}

// cgOpenGFNSheet explains GeForce NOW here, with the PC's apps.
func cgOpenGFNSheet(config *Config) {
	var b strings.Builder
	b.WriteString("GeForce NOW runs PC games you own — on Steam, Epic Games Store, Ubisoft Connect, Xbox and more — on NVIDIA's servers and streams them. " +
		"Link your stores in its library first. A free membership plays with ads, queues and short sessions; Performance and Ultimate memberships get priority access and play longer at higher quality.\n\n")
	buttons := []tbxSheetButton{{label: "Close", primary: true}}
	if cgHandheld() {
		b.WriteString("## On this handheld\n")
		b.WriteString("NVIDIA's own app is for x86-64 PCs. " + goClientName + "'s cloud build (v0.3.0 and later) added experimental GeForce NOW: sign in with the code it shows, browse, queue and play.\n")
		if cgScreen.st.Installed {
			b.WriteString("• On this card: the " + cgScreen.st.Build.short() + " — " + cgScreen.st.Build.streams() + "\n")
		}
		b.WriteString("• " + goGlibcNote() + "\n")
		if note := goDecodeNote(); note != "" {
			b.WriteString("• " + note + "\n")
		}
		if cgScreen.st.Installed && !cgScreen.st.Build.hasGFN() {
			buttons = append(buttons, tbxSheetButton{label: "Switch to cloud build", run: func() { cgOpenSwitchSheet(config, goBuildCloud) }})
		}
	} else {
		b.WriteString("## On this PC\n")
		switch {
		case cgScreen.gfnFound:
			b.WriteString(hintSentence("Found: "+cgScreen.gfn.name+" — %s on the row opens it.", "A", "Play") + "\n")
		default:
			b.WriteString(hintSentence("No GeForce NOW app was found, so %s opens play.geforcenow.com in the browser.", "A", "Open") + " NVIDIA's app (Windows, macOS, and Linux on x86-64) " +
				"and OpenNOW, an open-source client, both stream more smoothly than a browser.\n")
		}
		buttons = append(buttons,
			tbxSheetButton{label: "NVIDIA's app", run: func() { cgOpenWeb(cgGFNAppURL, "") }},
			tbxSheetButton{label: "OpenNOW", run: func() { cgOpenWeb(cgOpenNOWURL, "") }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    cgSvcGFN,
		subtitle: "NVIDIA's cloud gaming",
		icon:     cloudGamingGlyph,
		body:     b.String(),
		buttons:  buttons,
		tall:     true,
	})
}

// cgOpenClientSheet is GreenOvercast's details: the build on the card, what
// the other build would change, and where it lives.
func cgOpenClientSheet(config *Config) {
	if !cgHandheld() {
		cgOpenGFNSheet(config)
		return
	}
	st := cgScreen.st
	var b strings.Builder
	b.WriteString(goClientName + " is an open-source streaming client for small ARM64 Linux handhelds. It comes in two builds that install as the same port:\n")
	b.WriteString("• Cloud build (Producdevity): Xbox Cloud Gaming and GeForce NOW\n")
	b.WriteString("• xHome build (doomL's fork of an older release): Xbox Cloud Gaming and your own Xbox, no GeForce NOW\n\n")
	b.WriteString("Cloud Gaming installs the cloud build and X1 the xHome build; either plays what is installed.\n\n")
	if st.Installed {
		b.WriteString("## On this card\n• The " + st.Build.short() + ": " + st.Build.streams() + "\n")
		if !st.FromMarker && st.Build != goBuildUnknown {
			b.WriteString("• Recognised from its files, not installed by JukaHub\n")
		}
		b.WriteString("• Launcher: " + st.Launch + "\n")
		if st.Dir != "" {
			b.WriteString("• Folder: " + st.Dir + "\n")
		}
		b.WriteString("\n")
	} else if st.Queued != "" {
		b.WriteString("## Waiting\nA downloaded " + goZipName + " waits for PortMaster in " + filepath.Dir(st.Queued) + ".\n\n")
	}
	b.WriteString("## Will it run?\n• " + goGlibcNote() + "\n")
	if note := goDecodeNote(); note != "" {
		b.WriteString("• " + note + "\n")
	}
	buttons := []tbxSheetButton{{label: "Close", primary: true}}
	if st.Installed {
		other := st.Build.other()
		if st.Build == goBuildUnknown {
			buttons = append(buttons,
				tbxSheetButton{label: "Install cloud build", run: func() { cgOpenSwitchSheet(config, goBuildCloud) }},
				tbxSheetButton{label: "Install xHome build", run: func() { cgOpenSwitchSheet(config, goBuildXHome) }})
		} else {
			buttons = append(buttons,
				tbxSheetButton{label: "Switch to " + other.short(), run: func() { cgOpenSwitchSheet(config, other) }},
				tbxSheetButton{label: "Reinstall", run: func() { goInstall(st.Build, cgScreenUp, cgAfterInstall) }})
		}
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Install cloud build", run: func() { cgInstallChecked(config, goBuildCloud) }})
	}
	if st.PMLauncher != "" {
		launcher := st.PMLauncher
		buttons = append(buttons, tbxSheetButton{label: "Open PortMaster", run: func() { mcOpenPortMaster(launcher, "") }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    goClientName,
		subtitle: "Streaming client · PortMaster port",
		icon:     "download",
		body:     b.String(),
		buttons:  buttons,
		tall:     true,
	})
}

// cgOpenSwitchSheet confirms replacing the installed build with target,
// saying what is lost.
func cgOpenSwitchSheet(config *Config, target goBuild) {
	var b strings.Builder
	b.WriteString("JukaHub downloads the " + target.short() + " and installs it over the " + goClientName + " on the card (both are the same port). ")
	b.WriteString("It streams " + target.streams() + ".\n\n")
	b.WriteString("## You lose\n")
	if target == goBuildCloud {
		b.WriteString("Streaming your own Xbox (X1) until the cloud build gains it — doomL has offered that work upstream. Switch back any time from here.\n\n")
	} else {
		b.WriteString("GeForce NOW, which the xHome build predates. Switch back any time from here.\n\n")
	}
	b.WriteString("Your sign-in may need doing again afterwards.")
	if _, old := goGlibcOld(); old {
		b.WriteString("\n\n" + goGlibcNote())
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Switch to the " + target.short(),
		subtitle: goClientName,
		icon:     "download",
		body:     b.String(),
		buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Switch", primary: true, run: func() {
			goInstall(target, cgScreenUp, cgAfterInstall)
		}}},
		focus: 1,
	})
}

// cgInstallChecked installs a build. An older TrimUI gets its compatibility
// runtime automatically on the first launch after the port is installed.
func cgInstallChecked(_ *Config, build goBuild) {
	goInstall(build, cgScreenUp, cgAfterInstall)
}

