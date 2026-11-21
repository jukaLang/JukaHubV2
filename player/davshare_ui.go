package main

// davshare_ui.go — the WebDAV share screen (Settings → Network → WebDAV
// share, Tools → Connectivity, Network tools), drawn like the other tool
// screens (hotspot_ui.go). Named apart from "Network drives" (the NAS client,
// nas_ui.go) and "SMB share" (smbserve_ui.go).
//
// Top to bottom: the switch; while it is on, the address with its key (A
// shows it as a code), the Mac and Linux sign-in, and who is connected; then
// how to connect from Windows, a Mac and Linux, each a sheet of steps; and
// three plain lines on what the drive shares and when it stops. The server is
// davshare.go's; starting it is quick (a listener and the shares' roots), so
// it starts on the press, as FTP does.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// davShareSceneName is the scene Settings and the Tools hub open.
const davShareSceneName = "NetworkDrive"

type davShareScreenState struct {
	visit tbxVisit
	nav   tbxNav
}

var davShareScreen davShareScreenState

// davShareOpenScreen opens the screen. Cross-area contract: Settings, the
// Tools hub and Network tools call it.
func davShareOpenScreen(config *Config) {
	tbxOpenScene(config, davShareSceneName)
}

type davShareScene struct{ sceneBase }

func (davShareScene) render(r *sdl.Renderer, c *Config) bool {
	if davShareScreen.visit.begin() {
		davShareScreen.nav.scroll = 0
	}
	davShareRenderScreen(r, c)
	return true
}

func (davShareScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), davShareSceneAction)
}

func (davShareScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), davShareSceneAction)
}

func init() { registerScene(davShareScene{sceneBase{name: davShareSceneName}}) }

// davShareSettingsRow is Settings → Network's door to the screen.
func davShareSettingsRow() setRow {
	return setRow{
		ID: "networkShare", Label: "WebDAV share", Kind: setKindAction,
		Detail: setStatic("This handheld's games, saves and media as a drive on Windows, a Mac or Linux — no app needed"),
		Value:  func(*Config) string { return davShareTileValue() },
		Open:   davShareOpenScreen,
	}
}

// davShareHubTile is the Tools hub tile.
func davShareHubTile() toolsHubTile {
	t := toolsHubTile{id: "netdrive", label: "WebDAV share", icon: "server", need: toolsHubNeedLAN, value: davShareTileValue()}
	if davSrv.Running() {
		t.tone = toolsHubToneGood
	}
	return t
}

// davShareTileValue is the one-liner Settings and the tiles show.
func davShareTileValue() string {
	if !davSrv.Running() {
		return "Off — map the card as a drive on a computer"
	}
	v := "On"
	if addr := davSrv.BaseAddress(); addr != "" {
		v += " · " + strings.TrimSuffix(strings.TrimPrefix(addr, "http://"), "/")
	}
	return v + netToolsConnected(davSrv.clients.count())
}

// davShareToggleSubtitle explains the switch.
func davShareToggleSubtitle() string {
	if davSrv.Running() {
		n := davSrv.clients.count()
		if n == 0 {
			return "On · stops by itself after " + fsIdleLimitText() + " unused"
		}
		return "On · " + netToolsCount(n, "connection", "connections")
	}
	if err := davSrv.LastError(); err != "" {
		return "Could not start: " + err
	}
	if fileServerOnTrimui() {
		return "Off · share games, saves and media with a computer"
	}
	return "Off · share the folder next to JukaHub with another computer"
}

// ── Drawing ──────────────────────────────────────────────────────────────

func davShareRenderScreen(renderer *sdl.Renderer, config *Config) {
	running := davSrv.Running()
	status, statusColor := "Off", ColorTextMuted()
	if running {
		status, statusColor = "On", ToastSuccessInk()
	}
	header := tbxHeader("WebDAV share", "This handheld's card as a drive on Windows, a Mac or Linux", "server", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &davShareScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}

	tbxDrawSection(renderer, config, "WebDAV share", "server", x, top+y, w)
	y += tbxSectionHeight
	toggle := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	toggleScreen := toggle
	toggleScreen.Y += top
	davShareDrawToggleRow(renderer, config, fonts, toggleScreen, "WebDAV share", davShareToggleSubtitle(), running, nav.focused("toggle"))
	nav.add("toggle", toggle, row, false)
	row++
	y += rowH + 8
	if running {
		addr := davSrv.Address()
		if addr == "" {
			drawRow("", tbxRowSpec{icon: "wifi-off", title: "No network address", subtitle: "Join Wi-Fi: the address appears here", dim: true})
		} else {
			drawRow("address", tbxRowSpec{icon: "link", title: "Address", subtitle: addr, pill: "Code", tone: tbxToneAccent})
		}
		if base := davSrv.BaseAddress(); base != "" {
			drawRow("password", tbxRowSpec{icon: "key", title: "Or sign in (Mac, Linux)",
				subtitle: base + " · user " + davSrv.User() + " · password " + davSrv.Password(), pill: "Details", tone: tbxToneNeutral})
		}
		y += 14
		tbxDrawSection(renderer, config, "Connected", "devices", x, top+y, w)
		y += tbxSectionHeight
		clients := davSrv.Clients()
		if len(clients) == 0 {
			drawRow("", tbxRowSpec{icon: "devices", title: "Nobody yet", subtitle: "Computers that open the drive show up here", dim: true})
		}
		for i, c := range clients {
			if i == 6 {
				drawRow("", tbxRowSpec{icon: "devices", title: netToolsCount(len(clients)-6, "more connection", "more connections"), dim: true})
				break
			}
			sub := "connected " + netToolsAge(c.Since)
			if c.Transfer != "" {
				sub += " · " + netToolsClip(c.Transfer, 48)
			}
			drawRow("", tbxRowSpec{icon: "devices", title: fsClientHost(c.Remote), subtitle: sub, dim: true})
		}
	}
	y += 14

	tbxDrawSection(renderer, config, "How to connect", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("win", tbxRowSpec{icon: "devices", title: "Windows", subtitle: "File Explorer → This PC → Map network drive → the address", pill: "Steps", tone: tbxToneNeutral})
	drawRow("mac", tbxRowSpec{icon: "devices", title: "Mac", subtitle: "Finder → Go → Connect to Server → the address", pill: "Steps", tone: tbxToneNeutral})
	drawRow("linux", tbxRowSpec{icon: "devices", title: "Linux", subtitle: "Files → Other Locations → dav:// and the address", pill: "Steps", tone: tbxToneNeutral})
	y += 14

	tbxDrawSection(renderer, config, "Good to know", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("", tbxRowSpec{icon: "folder", title: "The same folders as FTP",
		subtitle: "Games, BIOS, saves, screenshots and media; folders of programs are read-only", dim: true})
	drawRow("", tbxRowSpec{icon: "shield", title: "For your home network",
		subtitle: "Plain HTTP; a new address and password at every start; off after " + fsIdleLimitText() + " unused or on another network", dim: true})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// davShareDrawToggleRow draws the switch row: the title over its line, the
// switch at the right edge.
func davShareDrawToggleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, title, sub string, on, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-18, r.Y+r.H/2, on)
	textW := toggle.X - r.X - 34
	block := fonts.titleH + 3 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), r.X+18, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), r.X+18, ty+fonts.titleH+3)
}

// ── Input ────────────────────────────────────────────────────────────────

func davShareSceneAction(config *Config, act tbxAction) bool {
	nav := &davShareScreen.nav
	switch act {
	case tbxActConfirm:
		davShareActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func davShareSceneClick(mx, my int32, config *Config) bool {
	it, ok := davShareScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	davShareScreen.nav.focus = it.id
	davShareActivate(config, it.id)
	return true
}

func davShareSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch davShareScreen.nav.focus {
	case "toggle":
		if davSrv.Running() {
			hints = append(hints, FooterHint{Button: "A", Label: "Turn off"})
		} else {
			hints = append(hints, FooterHint{Button: "A", Label: "Turn on"})
		}
	case "address":
		hints = append(hints, FooterHint{Button: "A", Label: "Show code"})
	case "password", "win", "mac", "linux":
		hints = append(hints, FooterHint{Button: "A", Label: "Details"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func davShareActivate(config *Config, id string) {
	switch id {
	case "toggle":
		davShareToggle()
	case "address":
		if addr := davSrv.Address(); addr != "" {
			PlayActivateSound()
			qrZoomOpenCardDetail(tbxQRModules(addr), "WebDAV share", addr)
		}
	case "password":
		if base := davSrv.BaseAddress(); base != "" {
			PlayActivateSound()
			tbxOpenSheet(config, tbxSheetSpec{
				title: "Sign in without the key", subtitle: base, icon: "key",
				body: "For a Mac or Linux file manager that asks for a name and password:\n\n" +
					"Address: " + base + "\nUser: " + davSrv.User() + "\nPassword: " + davSrv.Password() + "\n\n" +
					"Windows cannot sign in this way over plain HTTP; use the address with the key instead. Both change at every start.",
				buttons: []tbxSheetButton{{label: "OK", primary: true}},
			})
		}
	case "win", "mac", "linux":
		PlayActivateSound()
		title, body := davShareSteps(id)
		tbxOpenSheet(config, tbxSheetSpec{title: title, icon: "devices", body: body, tall: true,
			buttons: []tbxSheetButton{{label: "OK", primary: true}}})
	}
}

// davShareToggle starts or stops the drive.
func davShareToggle() {
	PlayActivateSound()
	if davSrv.Running() {
		davSrv.Stop()
		ShowToast("WebDAV share off", ToastKindInfo)
		return
	}
	if err := davSrv.Start(); err != nil {
		ShowToast("WebDAV share: "+err.Error(), ToastKindError)
		return
	}
	davShareScreen.nav.setFocus("address")
	ShowToast("WebDAV share on — type the address on your computer", ToastKindSuccess)
}

// davShareSteps is one system's steps, with this run's address in them.
func davShareSteps(id string) (string, string) {
	addr := davSrv.Address()
	if addr == "" {
		addr = "http://IP:PORT/KEY/ (turn the share on to see it)"
	}
	switch id {
	case "mac":
		return "Connect from a Mac",
			"1. In Finder, choose Go → Connect to Server (⌘K).\n" +
				"2. Type " + addr + " and click Connect.\n" +
				"3. The drive appears under Locations in Finder's sidebar. Eject it there when you are done.\n\n" +
				"## Without the key\nType the address without the key and sign in as a Registered User with the name and password on this screen."
	case "linux":
		dav := strings.Replace(addr, "http://", "dav://", 1)
		web := strings.Replace(addr, "http://", "webdav://", 1)
		return "Connect from Linux",
			"• GNOME Files: Other Locations → Connect to Server → " + dav + "\n" +
				"• KDE Dolphin: type " + web + " in the address bar.\n" +
				"• From a terminal: sudo mount -t davfs " + addr + " /mnt/jukahub (the davfs2 package).\n\n" +
				"## Without the key\nUse the address without the key and the name and password on this screen."
	}
	return "Connect from Windows",
		"1. Open File Explorer, right-click This PC and choose Map network drive.\n" +
			"2. In Folder, type " + addr + "\n" +
			"3. Untick Reconnect at sign-in (the address changes every time the share starts) and choose Finish.\n" +
			"The drive appears under This PC. Pasting the address into Explorer's address bar works too.\n\n" +
			"## Files over 50 MB\nWindows copies at most 50 MB per file over WebDAV until its limit is raised. In PowerShell as administrator:\n" +
			"Set-ItemProperty HKLM:\\SYSTEM\\CurrentControlSet\\Services\\WebClient\\Parameters FileSizeLimitInBytes 4294967295\n" +
			"Restart-Service WebClient\n" +
			"The SMB share, FTP and SFTP (Network tools) have no such limit.\n\n" +
			"## \"The folder you entered does not appear to be valid\"\nWindows' WebClient service must run: open Services, set WebClient to Automatic and start it."
}
