package main

// minecraft_ui.go — the Minecraft screen (Start → Games → Minecraft).
//
// One list, in the tool screens' frame (tbxui.go). On the handheld:
//
//	Java Edition       the PortMaster port (Install / Needs game files / Play)
//	                   and, per paired PC, the versions it can copy over
//	Bedrock Edition    the player's own APK through AndroidRT (APK Store)
//	Free and open      ClassiCube and Luanti · Mineclonia (Install / Play)
//
// On a PC: the Minecraft launchers installed there, and what a paired
// handheld can copy from its .minecraft. The card is read on a scene task when
// the screen opens and the ports again every few seconds, so a port PortMaster
// just installed turns into Play when the player comes back.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mcCopyRow is one "Copy <version> from <PC>" row.
type mcCopyRow struct {
	id      string
	pc      int
	version mcPeerVersion
}

type mcScreenState struct {
	visit tbxVisit
	nav   tbxNav

	gen        int
	scanned    bool
	portScanAt time.Time
	portScanOn int // the visit whose port scan is in flight, 0 for none
	lastFrame  time.Time
	ports      [mcPortCount]mcPortState
	// pmLauncher is PortMaster's launcher as of the last scan ("" when it
	// is not installed); pmWasInstalling notices its install finishing.
	pmLauncher      string
	pmWasInstalling bool

	bedrock         mcBedrockState
	bedrockScanning bool

	pcs        []mcPCVersions
	pcsLoading int // the visit whose PC fetch is in flight, 0 for none
	copyRows   []mcCopyRow

	desktop  []mcDesktopApp
	javaHome string
	javaVers []mcPeerVersion
}

var mcScreen mcScreenState

// mcHandheld is whether the screen offers the handheld's ways in (PortMaster
// and AndroidRT) rather than a PC's launchers.
func mcHandheld() bool { return IsTrimuiDevice() }

// ── Scene ───────────────────────────────────────────────────────────────

// mcEnsureScene gives the screen its scene in memory, like the APK Store;
// the user's config is never rewritten for it. Startup and every reload.
func mcEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, minecraftSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: minecraftSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == minecraftSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// minecraftOpen opens the screen (Start menu, search, voice).
func minecraftOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	mcEnsureScene(config)
	idx := sceneIndexExact(config, minecraftSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

type mcSceneModule struct{ sceneBase }

func (mcSceneModule) render(r *sdl.Renderer, c *Config) bool {
	mcRenderScreen(r, c)
	return true
}

func (mcSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), mcScreenAction)
}

func (mcSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), mcScreenAction)
}

func init() { registerScene(mcSceneModule{sceneBase{name: minecraftSceneName}}) }

// mcOnScreen reports whether the Minecraft screen is up.
func mcOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == minecraftSceneName
}

// ── Work ────────────────────────────────────────────────────────────────

// mcScreenEnter runs once per visit: everything is read again.
func mcScreenEnter() {
	mcScreen.gen++
	mcScreen.nav.scroll = 0
	mcScreen.nav.focus = ""
	mcScreen.scanned = false
	mcScreen.portScanOn, mcScreen.pcsLoading = 0, 0
	mcScreen.portScanAt = time.Time{}
	mcScreen.pcs, mcScreen.copyRows = nil, nil
	gen := mcScreen.gen
	if !mcHandheld() {
		runSceneTask(func(ctx context.Context) {
			apps := mcDesktopApps()
			home := mcJavaHome()
			var vers []mcPeerVersion
			if home != "" {
				vers = mcListVersions(home)
			}
			tbxPost(func() {
				if gen == mcScreen.gen {
					mcScreen.desktop, mcScreen.javaHome, mcScreen.javaVers = apps, home, vers
					mcScreen.scanned = true
				}
			})
		})
		return
	}
	mcScanPortsAsync()
	mcScreen.bedrockScanning = true
	started := runSceneTask(func(ctx context.Context) {
		st := mcScanBedrock(ctx)
		tbxPost(func() {
			if gen == mcScreen.gen {
				mcScreen.bedrock, mcScreen.bedrockScanning = st, false
			}
		})
	})
	if !started {
		mcScreen.bedrockScanning = false
	}
	mcRefreshPCs()
}

// mcScanPortsAsync reads the card for the ports on a scene task.
func mcScanPortsAsync() {
	if mcScreen.portScanOn == mcScreen.gen {
		return
	}
	gen := mcScreen.gen
	mcScreen.portScanOn = gen
	mcScreen.portScanAt = time.Now()
	started := runSceneTask(func(ctx context.Context) {
		launcher := portMasterLauncher(portMasterSDRoot)
		ports := mcScanPorts(portMasterSDRoot, launcher)
		tbxPost(func() {
			if gen != mcScreen.gen {
				return
			}
			mcScreen.portScanOn = 0
			mcScreen.ports, mcScreen.pmLauncher, mcScreen.scanned = ports, launcher, true
			mcScreen.portScanAt = time.Now()
			mcScreen.copyRows = mcBuildCopyRows(mcScreen.pcs, ports[mcPortJava].Starts)
		})
	})
	if !started {
		mcScreen.portScanOn = 0
	}
}

// mcRefreshPCs asks the paired PCs for their versions.
func mcRefreshPCs() {
	if mcScreen.pcsLoading == mcScreen.gen {
		return
	}
	gen := mcScreen.gen
	mcScreen.pcsLoading = gen
	started := runSceneTask(func(ctx context.Context) {
		pcs := mcFetchPCVersions(ctx)
		tbxPost(func() {
			if gen != mcScreen.gen {
				return
			}
			mcScreen.pcsLoading = 0
			mcScreen.pcs = pcs
			mcScreen.copyRows = mcBuildCopyRows(pcs, mcScreen.ports[mcPortJava].Starts)
		})
	})
	if !started {
		mcScreen.pcsLoading = 0
	}
}

// mcVersionRank orders a PC's versions: ones the port can start first, then
// releases, then the newest-looking id.
func mcVersionRank(v mcPeerVersion, starts []string) int {
	r := 0
	if mcStartForVersion(starts, v) != "" {
		r += 4
	}
	if v.Type == "release" || v.Inherits != "" {
		r += 2
	}
	if v.Jar || v.Inherits != "" {
		r++
	}
	return r
}

// mcBuildCopyRows lists at most eight versions per PC.
func mcBuildCopyRows(pcs []mcPCVersions, starts []string) []mcCopyRow {
	var rows []mcCopyRow
	for i, pc := range pcs {
		vers := append([]mcPeerVersion(nil), pc.versions...)
		sort.SliceStable(vers, func(a, b int) bool {
			ra, rb := mcVersionRank(vers[a], starts), mcVersionRank(vers[b], starts)
			if ra != rb {
				return ra > rb
			}
			return vers[a].ID > vers[b].ID
		})
		if len(vers) > 8 {
			vers = vers[:8]
		}
		for _, v := range vers {
			rows = append(rows, mcCopyRow{id: "copy:" + strconv.Itoa(i) + ":" + v.ID, pc: i, version: v})
		}
	}
	return rows
}

// ── Rows ────────────────────────────────────────────────────────────────

// mcBrickJava reports a Java port row on the Brick, which has no sticks for
// it (PortMaster lists the port on the Smart Pro only).
func mcBrickJava(kind mcPortKind) bool {
	return kind == mcPortJava && !mcScreen.ports[kind].Installed && DetectDevice() == DeviceTrimuiBrick
}

// mcPortRow is a port's row.
func mcPortRow(kind mcPortKind) tbxRowSpec {
	spec := mcPortSpecs[kind]
	st := mcScreen.ports[kind]
	row := tbxRowSpec{icon: minecraftGlyph, title: spec.title}
	if kind != mcPortJava {
		row.icon = "gamepad"
	}
	busyKind, busy := mcPortInstallBusy()
	switch {
	case !mcScreen.scanned:
		row.subtitle, row.pill, row.tone = "Reading the card…", "…", tbxToneNeutral
	case busy && busyKind == kind:
		row.subtitle, row.pill, row.tone = "Downloading from PortMaster's catalogue", "Downloading…", tbxToneAccent
	case st.Installed && kind == mcPortJava && st.DataDir == "":
		row.subtitle = "Installed, but JukaHub cannot find its folder — Y shows the launcher; copy files by hand"
		row.pill, row.tone = "Folder not found", tbxToneWarn
	case st.Installed && kind == mcPortJava && len(st.Versions) == 0:
		row.subtitle = "Installed — now it needs your game files (copy them from a PC below)"
		row.pill, row.tone = "Needs game files", tbxToneWarn
	case st.Installed:
		row.subtitle = "Installed through PortMaster"
		if kind == mcPortJava {
			row.subtitle = "Your versions here: " + strings.Join(mcShortList(st.Versions, 4), " · ")
		}
		row.pill, row.tone = "Play", tbxToneGood
		if st.Compat.State == portCompat32BitOnly {
			row.subtitle = "Its manifest lists no 64-bit ARM build: " + setOr(st.Compat.Note, "32-bit only")
			row.pill, row.tone = "Wrong CPU", tbxToneBad
		}
	case st.Queued != "" && mcScreen.pmLauncher != "":
		row.subtitle = "Downloaded — PortMaster installs it as soon as it opens"
		row.pill, row.tone = "Open PortMaster", tbxToneAccent
	case portMasterInstalling():
		row.subtitle, row.pill, row.tone = "Installing PortMaster first…", "Installing…", tbxToneAccent
	case mcBrickJava(kind):
		row.subtitle = "Needs two analog sticks — PortMaster lists it on the Smart Pro, not the Brick"
		row.pill, row.tone, row.dim = "Not for Brick", tbxToneNeutral, true
	case mcScreen.pmLauncher == "":
		row.subtitle = "Comes through PortMaster — A installs PortMaster (" + portMasterInstallSize + ")"
		row.pill, row.tone = "Get PortMaster", tbxToneNeutral
	default:
		row.subtitle = mcPortTagline(kind)
		row.pill, row.tone = "Install", tbxToneAccent
	}
	return row
}

// mcPortTagline is a port's one-line pitch.
func mcPortTagline(kind mcPortKind) string {
	switch kind {
	case mcPortJava:
		return "The real Java Edition (1.7.10, 1.12.2 and more) with your own game files"
	case mcPortClassiCube:
		return "Free Minecraft Classic client — build alone or on ClassiCube servers"
	case mcPortLuanti:
		return "Free survival game like Minecraft: mine, craft, mobs, redstone"
	}
	return ""
}

// mcShortList is up to n names, then "+k more".
func mcShortList(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	out := append([]string(nil), names[:n]...)
	return append(out, "+"+strconv.Itoa(len(names)-n)+" more")
}

// mcHasLocalVersion reports whether the port already holds a version.
func mcHasLocalVersion(id string) bool {
	for _, v := range mcScreen.ports[mcPortJava].Versions {
		if v == id {
			return true
		}
	}
	return false
}

// mcCopyRowSpec is a "Copy <version> from <PC>" row.
func mcCopyRowSpec(r mcCopyRow) tbxRowSpec {
	pc := mcScreen.pcs[r.pc]
	row := tbxRowSpec{icon: "download", title: "Copy " + r.version.ID + " from " + pc.peerName}
	var bits []string
	switch {
	case r.version.Inherits != "":
		bits = append(bits, "built on "+r.version.Inherits)
	case r.version.Type != "":
		bits = append(bits, r.version.Type)
	}
	java := mcScreen.ports[mcPortJava]
	switch {
	case len(java.Starts) == 0:
	case mcStartForVersion(java.Starts, r.version) != "":
		bits = append(bits, "the port has a start file for it")
	default:
		bits = append(bits, "the port lists no start file for it")
	}
	row.subtitle = strings.Join(bits, " · ")
	copying, busy := mcCopyBusy()
	switch {
	case busy && copying == r.version.ID:
		row.pill, row.tone = "Copying…", tbxToneAccent
	case !java.Installed:
		row.pill, row.tone, row.dim = "Port first", tbxToneNeutral, true
	case java.GameDir == "":
		row.pill, row.tone, row.dim = "No folder", tbxToneNeutral, true
	case mcHasLocalVersion(r.version.ID):
		row.pill, row.tone = "On handheld", tbxToneGood
	default:
		row.pill, row.tone = "Copy", tbxToneAccent
	}
	return row
}

// mcFilesHelpRow is the row shown when no PC offers versions.
func mcFilesHelpRow() tbxRowSpec {
	row := tbxRowSpec{icon: "devices", title: "Your game files", pill: "How", tone: tbxToneNeutral}
	switch {
	case mcScreen.pcsLoading != 0:
		row.subtitle, row.pill = "Asking your paired PCs…", "…"
	case peerPairedCount() == 0:
		row.subtitle = "Pair a PC running JukaHub (Devices) and copy them in one press — or copy them by hand"
	case !peerEnabledFlag.Load():
		row.subtitle = "Turn on Nearby devices to copy them from your paired PC — A for the ways in"
	default:
		row.subtitle = "No paired PC answered with a .minecraft folder — A for the ways in"
	}
	return row
}

// mcBedrockRow is the Bedrock (Android) row.
func mcBedrockRow() tbxRowSpec {
	b := mcScreen.bedrock
	row := tbxRowSpec{icon: "phone", title: "Minecraft: Bedrock Edition"}
	switch {
	case mcScreen.bedrockScanning:
		row.subtitle, row.pill, row.tone = "Looking for a Minecraft APK on the card…", "…", tbxToneNeutral
	case len(b.APKs) == 0:
		row.subtitle = "Your own 64-bit APK through AndroidRT — experimental"
		row.pill, row.tone = "How", tbxToneNeutral
	case b.Playable > 0:
		a := b.APKs[0]
		for _, x := range b.APKs {
			if x.Tier == apkTierMinecraft {
				a = x
				break
			}
		}
		name := setOr(a.Label, "Minecraft")
		if v := a.Manifest.VersionName; v != "" {
			name += " " + v
		}
		row.subtitle = name + " · experimental, the APK Store adds and plays it"
		row.pill, row.tone = "Try it", tbxToneAccent
		if b.Runtime.dir == "" {
			row.subtitle = name + " · AndroidRT is missing — the APK Store installs it"
			row.pill, row.tone = "Get AndroidRT", tbxToneWarn
		}
	default:
		a := b.APKs[0]
		row.subtitle = setOr(a.Verdict, "This APK cannot run here") + " — " + setOr(firstLine(a.Why), apkStoreFileLabel(a.Path))
		row.pill, row.tone = "Cannot run", tbxToneWarn
	}
	return row
}

// mcDesktopRow is a launcher on this PC.
func mcDesktopRow(a mcDesktopApp) tbxRowSpec {
	return tbxRowSpec{icon: minecraftGlyph, title: a.title, subtitle: a.subtitle, pill: "Play", tone: tbxToneGood}
}

// ── Drawing ─────────────────────────────────────────────────────────────

func mcHeader() UIPageHeader {
	if !mcHandheld() {
		status, ink := "", ColorTextTertiary()
		if n := len(mcScreen.desktop); n > 0 {
			status, ink = pluralizeCount(n, "launcher", "launchers")+" here", ToastSuccessInk()
		}
		return tbxHeader("Minecraft", "The launchers on this PC — and your versions for a paired handheld", minecraftGlyph, status, ink)
	}
	ready := 0
	for k := mcPortKind(0); k < mcPortCount; k++ {
		st := mcScreen.ports[k]
		if st.Installed && (k != mcPortJava || len(st.Versions) > 0) {
			ready++
		}
	}
	status, ink := "Set up a way to play", ToastWarnInk()
	if ready > 0 {
		status, ink = pluralizeCount(ready, "way", "ways")+" to play", ToastSuccessInk()
	}
	if !mcScreen.scanned {
		status, ink = "Reading…", ColorTextTertiary()
	}
	return tbxHeader("Minecraft", "Java Edition, Bedrock and free block games on this handheld", minecraftGlyph, status, ink)
}

// mcRenderScreen draws the screen.
func mcRenderScreen(renderer *sdl.Renderer, config *Config) {
	if mcScreen.visit.begin() {
		mcScreenEnter()
	}
	// The ports are read again after the screen was away (PortMaster or a
	// port ran: frames stop while a child has the screen) and every so often.
	now := time.Now()
	gap := now.Sub(mcScreen.lastFrame)
	mcScreen.lastFrame = now
	installing := portMasterInstalling()
	pmDone := mcScreen.pmWasInstalling && !installing
	mcScreen.pmWasInstalling = installing
	if mcHandheld() && mcScreen.portScanOn == 0 && !gameScreenSuspended() &&
		(gap > 2*time.Second || pmDone || now.Sub(mcScreen.portScanAt) > time.Minute) {
		mcScanPortsAsync()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, mcHeader())
	body := frame.bodyRect()
	nav := &mcScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	mcRenderBody(renderer, config, fonts, frame)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, mcScreenHints())
}

func mcRenderBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame) {
	nav := &mcScreen.nav
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

	if !mcHandheld() {
		section("On this PC", minecraftGlyph)
		for _, a := range mcScreen.desktop {
			addRow("app:"+a.id, mcDesktopRow(a))
		}
		if len(mcScreen.desktop) == 0 {
			sub := "Buy and download it from minecraft.net"
			if !mcScreen.scanned {
				sub = "Looking for Minecraft on this PC…"
			}
			addRow("get", tbxRowSpec{icon: "download", title: "Get Minecraft", subtitle: sub, pill: "Open", tone: tbxToneAccent})
		}
		section("For your handheld", "devices")
		home := tbxRowSpec{icon: "folder", title: "Java Edition files"}
		switch {
		case mcScreen.javaHome == "":
			home.subtitle, home.pill, home.tone = "No .minecraft folder here yet — start a version once in the launcher", "None", tbxToneNeutral
		default:
			home.subtitle = "A paired handheld can copy " + strings.Join(mcShortList(mcPeerVersionIDs(mcScreen.javaVers), 4), " · ")
			home.pill, home.tone = pluralizeCount(len(mcScreen.javaVers), "version", "versions"), tbxToneGood
			if !peerEnabledFlag.Load() {
				home.subtitle = "Turn on Nearby devices so a paired handheld can copy your versions"
				home.pill, home.tone = "Nearby is off", tbxToneWarn
			}
		}
		addRow("home", home)
		section("Free and open", "sparkles")
		addRow("web:classicube", tbxRowSpec{icon: "globe", title: "ClassiCube", subtitle: mcPortTagline(mcPortClassiCube), pill: "Website", tone: tbxToneNeutral})
		addRow("web:luanti", tbxRowSpec{icon: "globe", title: "Luanti · Mineclonia", subtitle: mcPortTagline(mcPortLuanti), pill: "Website", tone: tbxToneNeutral})
		nav.extend(y)
		return
	}

	section("Java Edition", minecraftGlyph)
	addRow("port:java", mcPortRow(mcPortJava))
	for _, r := range mcScreen.copyRows {
		if r.pc < len(mcScreen.pcs) {
			addRow(r.id, mcCopyRowSpec(r))
		}
	}
	if len(mcScreen.copyRows) == 0 {
		addRow("files", mcFilesHelpRow())
	}
	section("Bedrock Edition (Android)", "phone")
	addRow("bedrock", mcBedrockRow())
	section("Free and open", "sparkles")
	addRow("port:classicube", mcPortRow(mcPortClassiCube))
	addRow("port:luanti", mcPortRow(mcPortLuanti))
	nav.extend(y)
}

// mcPeerVersionIDs is the ids of a version list.
func mcPeerVersionIDs(vs []mcPeerVersion) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

// ── Input ───────────────────────────────────────────────────────────────

// mcScreenAction handles one action on the screen.
func mcScreenAction(config *Config, act tbxAction) bool {
	nav := &mcScreen.nav
	switch act {
	case tbxActConfirm:
		mcActivate(config, nav.focus)
		return true
	case tbxActY:
		if k, ok := mcPortKindFromID(nav.focus); ok {
			PlayActivateSound()
			mcOpenPortSheet(config, k)
		}
		return true
	case tbxActX:
		if mcHandheld() {
			PlayToggleSound()
			mcScanPortsAsync()
			mcRefreshPCs()
			ShowToast("Looking again", ToastKindInfo)
		}
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// mcSceneClick routes a click or tap: the open sheet first, then the rows.
func mcSceneClick(mx, my int32, config *Config) bool {
	if !mcOnScreen(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := mcScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	mcScreen.nav.focus = it.id
	mcActivate(config, it.id)
	return true
}

func mcPortKindFromID(id string) (mcPortKind, bool) {
	if !strings.HasPrefix(id, "port:") {
		return 0, false
	}
	name := strings.TrimPrefix(id, "port:")
	for k := mcPortKind(0); k < mcPortCount; k++ {
		if mcPortSpecs[k].id == name {
			return k, true
		}
	}
	return 0, false
}

// mcScreenHints is the footer.
func mcScreenHints() []FooterHint {
	id := mcScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	label := ""
	switch {
	case strings.HasPrefix(id, "port:"):
		if k, ok := mcPortKindFromID(id); ok {
			label = strings.TrimSuffix(mcPortRow(k).pill, "…")
			switch label {
			case "Needs game files", "Folder not found":
				label = "How"
			case "Not for Brick":
				label = "Why"
			case "Wrong CPU":
				label = ""
				if portCompatIgnored(appConfig) {
					label = "Play anyway"
				}
			case "Downloading", "Installing":
				label = ""
			}
		}
	case strings.HasPrefix(id, "copy:"):
		for _, r := range mcScreen.copyRows {
			if r.id != id || r.pc >= len(mcScreen.pcs) {
				continue
			}
			switch mcCopyRowSpec(r).pill {
			case "Copy":
				label = "Copy"
			case "On handheld":
				label = "Copy again"
			case "Port first", "No folder":
				label = "Why"
			}
		}
	case strings.HasPrefix(id, "app:"):
		label = "Play"
	case id == "bedrock":
		label = "Open"
	case id == "files", id == "home":
		label = "How"
	case id == "get", strings.HasPrefix(id, "web:"):
		label = "Open"
	}
	if label != "" {
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	if strings.HasPrefix(id, "port:") {
		hints = append(hints, FooterHint{Button: "Y", Label: "About"})
	}
	if mcHandheld() {
		hints = append(hints, FooterHint{Button: "X", Label: "Refresh"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// mcActivate runs the focused row's action.
func mcActivate(config *Config, id string) {
	switch {
	case strings.HasPrefix(id, "port:"):
		if k, ok := mcPortKindFromID(id); ok {
			mcActivatePort(config, k)
		}
	case strings.HasPrefix(id, "copy:"):
		for _, r := range mcScreen.copyRows {
			if r.id == id {
				mcActivateCopy(config, r)
				return
			}
		}
	case id == "files":
		PlayActivateSound()
		mcOpenFilesSheet(config)
	case id == "bedrock":
		PlayActivateSound()
		mcActivateBedrock(config)
	case strings.HasPrefix(id, "app:"):
		for _, a := range mcScreen.desktop {
			if "app:"+a.id == id {
				if err := a.launch(); err != nil {
					PlayErrorSound()
					ShowToast("Could not start "+a.title+" — "+firstLine(err.Error()), ToastKindError)
					return
				}
				PlayActivateSound()
				trophyEvent(tkGameLaunch)
				ShowToast("Starting "+a.title, ToastKindSuccess)
				return
			}
		}
	case id == "get":
		mcOpenWeb("https://www.minecraft.net/download")
	case id == "web:classicube":
		mcOpenWeb("https://www.classicube.net/")
	case id == "web:luanti":
		mcOpenWeb("https://www.luanti.org/")
	case id == "home":
		PlayActivateSound()
		mcOpenPCHomeSheet(config)
	}
}

func mcOpenWeb(link string) {
	if err := openExternalURL(link); err != nil {
		PlayErrorSound()
		ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
}

// mcActivatePort does what a port row's pill says.
func mcActivatePort(config *Config, kind mcPortKind) {
	st := mcScreen.ports[kind]
	spec := mcPortSpecs[kind]
	busyKind, busy := mcPortInstallBusy()
	switch {
	case !mcScreen.scanned:
		return
	case busy && busyKind == kind:
		ShowToast(spec.title+" is downloading — the status strip shows how far", ToastKindInfo)
	case st.Installed && kind == mcPortJava && (st.DataDir == "" || len(st.Versions) == 0):
		PlayActivateSound()
		mcOpenFilesSheet(config)
	case st.Installed:
		mcPlayPort(kind, st)
	case st.Queued != "" && mcScreen.pmLauncher != "":
		PlayActivateSound()
		mcOpenPortMaster(mcScreen.pmLauncher, "PortMaster is installing "+spec.title+" — close it when it is done to come back")
	case portMasterInstalling():
		ShowToast("PortMaster is installing — "+spec.title+" comes right after", ToastKindInfo)
	case mcBrickJava(kind):
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title:    spec.title,
			subtitle: "Not listed for the Brick",
			icon:     minecraftGlyph,
			body: "PortMaster offers its Minecraft port on devices with two analog sticks — the Smart Pro, not the Brick — " +
				"and Java Edition is tight in 1 GB of memory. You can still try: PortMaster may refuse it here.\n\n" +
				"ClassiCube and Luanti · Mineclonia below run on the Brick.",
			buttons: []tbxSheetButton{{label: "Close", primary: true}, {label: "Install anyway", run: func() { mcInstallPort(kind) }}},
		})
	default:
		PlayActivateSound()
		mcInstallPort(kind)
	}
}

// mcInstallPort installs PortMaster first if needed, then the port. UI thread.
func mcInstallPort(kind mcPortKind) {
	spec := mcPortSpecs[kind]
	if mcScreen.pmLauncher == "" && portMasterLauncher(portMasterSDRoot) == "" {
		if err := startPortMasterInstall(); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		ShowToast("Installing PortMaster — then press A on "+spec.title+" again", ToastKindInfo)
		return
	}
	err := mcStartPortInstall(kind, func() { mcScreen.portScanAt = time.Time{} })
	if err != nil {
		PlayErrorSound()
		ShowToast(firstLine(err.Error()), ToastKindWarning)
	}
}

// mcActivateCopy asks before copying a version from a PC.
func mcActivateCopy(config *Config, r mcCopyRow) {
	java := mcScreen.ports[mcPortJava]
	switch {
	case !java.Installed:
		PlayErrorSound()
		ShowToast("Install Minecraft: Java Edition (the port) first — its folder is where the files go", ToastKindWarning)
		return
	case java.GameDir == "":
		PlayErrorSound()
		ShowToast("JukaHub cannot find the port's folder on the card — copy the files by hand (Y on the port shows its launcher)", ToastKindWarning)
		return
	}
	if v, busy := mcCopyBusy(); busy {
		ShowToast("Already copying "+v, ToastKindInfo)
		return
	}
	if r.pc >= len(mcScreen.pcs) {
		return
	}
	pc := mcScreen.pcs[r.pc]
	PlayActivateSound()
	var body strings.Builder
	body.WriteString("JukaHub copies everything " + r.version.ID + " needs from " + pc.peerName + "'s .minecraft — the version, its libraries and its sounds and textures — and checks every file the launcher lists a checksum for.\n\n")
	body.WriteString("## Goes to\n" + java.GameDir + "\n\n")
	if mcHasLocalVersion(r.version.ID) {
		body.WriteString("It is already here: complete files are kept, the rest is added.\n\n")
	}
	if len(java.Starts) > 0 && mcStartForVersion(java.Starts, r.version) == "" {
		body.WriteString("The port lists start files for " + strings.Join(java.Starts, ", ") + " only — this version may not start.\n\n")
	}
	body.WriteString("Your account, saves and settings on the PC are never copied. Keep both on the same Wi-Fi until it finishes; the progress shows in the status strip.")
	peerID, peerName, version, gameDir := pc.peerID, pc.peerName, r.version.ID, java.GameDir
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Copy " + version,
		subtitle: "From " + peerName,
		icon:     "download",
		body:     body.String(),
		buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Copy", primary: true, run: func() {
			err := mcStartCopy(peerID, peerName, version, gameDir, func(missing int, err error) {
				mcScreen.portScanAt = time.Time{}
				switch {
				case errors.Is(err, context.Canceled):
					ShowToast("Copy of "+version+" stopped — press Copy again to finish it", ToastKindInfo)
				case err != nil:
					ShowToast("Copy stopped — "+firstLine(peerErrorText(err, peerName)), ToastKindError)
				case missing > 0:
					ShowToast(version+" copied, but "+peerName+" lacks "+pluralizeCount(missing, "file", "files")+
						" it needs — start "+version+" there once, then copy again", ToastKindWarning)
				default:
					PlaySuccessSound()
					ShowToast(version+" is on your handheld — Minecraft: Java Edition is ready", ToastKindSuccess)
				}
			})
			if err != nil {
				ShowToast(firstLine(err.Error()), ToastKindWarning)
			}
		}}},
		focus: 1,
	})
}

// mcActivateBedrock opens the APK Store or explains how to get the APK.
func mcActivateBedrock(config *Config) {
	if len(mcScreen.bedrock.APKs) > 0 {
		apkStoreOpen(config)
		return
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Minecraft: Bedrock Edition",
		subtitle: "Experimental · your own APK",
		icon:     "phone",
		body: "Bedrock's Android build runs through AndroidRT, JukaHub's Android game runtime. Nobody has run it on a handheld yet, so expect to be the first.\n\n" +
			"## What you need\n" +
			"• Minecraft bought on Google Play, and its 64-bit (arm64-v8a) APK copied off your phone — an APK backup app or adb does it\n" +
			"• The APK in the APKs folder on the card, or sent with Phone Drop\n\n" +
			"## Honest limits\n" +
			"• The store, the licence check and Xbox Live answer \"unavailable\", never \"granted\" — a Google Play build may stop at its licence screen\n" +
			"• It needs OpenGL ES 3.0 and most of the 1 GB of memory\n\n" +
			"The APK Store reads the APK, says whether it can run and adds it to your games.",
		buttons: []tbxSheetButton{{label: "Close"}, {label: "Open APK Store", primary: true, run: func() { apkStoreOpen(config) }}},
		focus:   1,
	})
}

// mcOpenFilesSheet explains how the Java game files get onto the handheld.
func mcOpenFilesSheet(config *Config) {
	java := mcScreen.ports[mcPortJava]
	var body strings.Builder
	body.WriteString("The port is the engine; the game is your own copy. It needs the versions/, libraries/ and assets/ that the Minecraft Launcher downloads on a PC where you own the game.\n\n")
	body.WriteString("## In one press\n")
	body.WriteString("• Install JukaHub on that PC and start the version once in the Minecraft Launcher\n")
	body.WriteString("• Pair the PC with this handheld (Settings → Network → Paired devices)\n")
	body.WriteString("• Come back here: each version on the PC shows as \"Copy <version> from <PC>\"\n\n")
	body.WriteString("## By hand\n")
	dest := java.GameDir
	if dest == "" {
		dest = "the port's .minecraft folder (Data/ports/minecraft/.minecraft once the port is installed)"
	}
	body.WriteString("Copy versions/<version>, libraries/ and assets/ from %APPDATA%\\.minecraft on Windows (~/.minecraft on Linux) into " + dest + " with the Files screen, FTP or the SD card.\n\n")
	if len(java.Starts) > 0 {
		body.WriteString("The port has start files for: " + strings.Join(java.Starts, ", ") + ".\n\n")
	}
	body.WriteString("Your Microsoft account never leaves the PC: JukaHub copies game files only.")
	buttons := []tbxSheetButton{{label: "Close", primary: true}}
	if java.Installed && len(java.Versions) == 0 {
		buttons = append(buttons, tbxSheetButton{label: "Play anyway", run: func() { mcPlayPort(mcPortJava, java) }})
	}
	if peerPairedCount() == 0 {
		buttons = append(buttons, tbxSheetButton{label: "Paired devices", run: func() { peerDevicesOpen(config) }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Your Java Edition files",
		subtitle: "Bring them from a PC",
		icon:     "folder",
		body:     body.String(),
		buttons:  buttons,
	})
}

// mcOpenPortSheet is a port's About sheet: what it is, where it lives and its
// own README.
func mcOpenPortSheet(config *Config, kind mcPortKind) {
	spec := mcPortSpecs[kind]
	st := mcScreen.ports[kind]
	var body strings.Builder
	body.WriteString(spec.blurb + "\n\n")
	if st.Installed {
		body.WriteString("## On this card\n• Launcher: " + st.Launch + "\n")
		if st.DataDir != "" {
			body.WriteString("• Folder: " + st.DataDir + "\n")
		}
		if kind == mcPortJava && len(st.Versions) > 0 {
			body.WriteString("• Versions: " + strings.Join(st.Versions, ", ") + "\n")
		}
		if readme := mcReadme(st.DataDir); readme != "" {
			body.WriteString("\n## The port's README\n" + readme + "\n")
		}
	} else {
		body.WriteString("JukaHub fetches it from PortMaster's catalogue, checks the download and lets PortMaster install it with the runtimes it needs. You can also install it from PortMaster yourself: search for \"" + spec.search + "\".")
	}
	buttons := []tbxSheetButton{{label: "Close", primary: true}}
	if launcher := portMasterLauncher(portMasterSDRoot); launcher != "" {
		buttons = append(buttons, tbxSheetButton{label: "Open PortMaster", run: func() { mcOpenPortMaster(launcher, "") }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    spec.title,
		subtitle: "PortMaster port",
		icon:     minecraftGlyph,
		body:     body.String(),
		buttons:  buttons,
		tall:     st.Installed,
	})
}

// mcOpenPCHomeSheet explains the PC side of the copy.
func mcOpenPCHomeSheet(config *Config) {
	var body strings.Builder
	if mcScreen.javaHome == "" {
		body.WriteString("This PC has no .minecraft folder yet. Install the Minecraft Launcher, sign in and start the version you want once — it downloads the files a handheld copies.\n\n")
	} else {
		body.WriteString("## Found\n" + mcScreen.javaHome + "\n\n")
		body.WriteString("Versions: " + strings.Join(mcPeerVersionIDs(mcScreen.javaVers), ", ") + "\n\n")
	}
	body.WriteString("## On the handheld\n• Pair it with this PC (Settings → Network → Paired devices) and keep Nearby devices on here\n• Open Minecraft there and press A on \"Copy <version> from this PC\"\n\n")
	body.WriteString("Only versions/, libraries/ and assets/ are shared, and only with paired devices — never your account, saves or settings. PortMaster's port runs 1.7.10 and 1.12.2 best.")
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Java Edition for your handheld",
		subtitle: "What a paired handheld can copy",
		icon:     "devices",
		body:     body.String(),
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}
