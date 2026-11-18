package main

// hotspot_ui.go — the Hotspot screen (Tools → Hotspot, Settings → Network →
// Hotspot), drawn like the other tool screens (usbotg_ui.go).
//
// Top to bottom: the switch, the network name and password (A edits either on
// the keyboard; X on the password makes a new random one), a code a phone's
// camera joins with, who has joined and the Companion's address on the
// hotspot, and two plain lines on what the hotspot is for. The screen only
// reads hotspotCurrent(); starting and stopping run on goroutines
// (hotspot.go).

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// hotspotSceneName is the scene the Tools hub and Settings open.
const hotspotSceneName = "Hotspot"

type hotspotScreenState struct {
	visit tbxVisit
	nav   tbxNav
}

var hotspotScreen hotspotScreenState

// hotspotShare is what Screen Share must paint over (castShareRedactions): the
// password row as last drawn, and the join code's card while it is up. Main
// thread (the capture reads it on the render thread too).
var hotspotShare struct {
	pass    sdl.Rect
	qrTitle string
}

// hotspotShareRedactions are the Hotspot screen's secrets on screen now, in
// logical pixels: the password row while the screen is up, the join code's
// card while it is open. Nothing elsewhere.
func hotspotShareRedactions() []sdl.Rect {
	var out []sdl.Rect
	c := appConfig
	if c != nil && currentSceneIndex >= 0 && currentSceneIndex < len(c.Scenes) &&
		c.Scenes[currentSceneIndex].Name == hotspotSceneName && hotspotShare.pass.W > 0 {
		out = append(out, hotspotShare.pass)
	}
	if qrZoom.open && hotspotShare.qrTitle != "" && qrZoom.title == hotspotShare.qrTitle {
		card, _ := qrZoomLayout(screenWidth, screenHeight, len(qrZoom.modules))
		out = append(out, card)
	}
	return out
}

// hotspotOpenScreen opens the Hotspot screen. Cross-area contract: the Tools
// hub, Settings → Network and Home search call it.
func hotspotOpenScreen(config *Config) {
	tbxOpenScene(config, hotspotSceneName)
}

type hotspotScene struct{ sceneBase }

func (hotspotScene) render(r *sdl.Renderer, c *Config) bool {
	if hotspotScreen.visit.begin() {
		hotspotScreen.nav.scroll = 0
	}
	hotspotRenderScreen(r, c)
	return true
}

func (hotspotScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), hotspotSceneAction)
}

func (hotspotScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), hotspotSceneAction)
}

func init() { registerScene(hotspotScene{sceneBase{name: hotspotSceneName}}) }

// hotspotSettingsRow is Settings → Network's door to the screen.
func hotspotSettingsRow() setRow {
	return setRow{
		ID: "hotspot", Label: "Hotspot", Kind: setKindAction,
		Detail: func(*Config) string {
			if _, ok := hotspotToolsLabel(); !ok {
				return "This firmware has no hotspot tools"
			}
			return "Share a Wi-Fi network from this handheld: netplay and the phone companion, no router"
		},
		Visible: func(*Config) bool { return hotspotSupported() },
		Value:   func(*Config) string { return hotspotTileValue() },
		Open:    hotspotOpenScreen,
	}
}

// hotspotToggleSubtitle explains the switch for a snapshot.
func hotspotToggleSubtitle(s hotspotSnapshot, tools bool) string {
	switch s.phase {
	case hotspotStarting:
		return "Starting… your Wi-Fi steps aside"
	case hotspotStopping:
		return "Turning off… your Wi-Fi comes back"
	case hotspotOn:
		return "On at " + s.run.ip + " · " + strings.TrimPrefix(hotspotTileValue(), "On · ")
	}
	switch {
	case !tools:
		return "This firmware has no hotspot tools (no softap_up or hostapd)"
	case s.err != "":
		return "Could not start: " + s.err
	case s.note != "":
		return s.note
	}
	return "Off · turn it on and other devices can join this handheld"
}

// hotspotCompanionURL is the Companion's address on the hotspot, or "" when
// the Companion is not serving. Main thread.
func hotspotCompanionURL(s hotspotSnapshot) string {
	if s.phase != hotspotOn || s.run.ip == "" || companion.srv == nil || !companion.srv.Running() {
		return ""
	}
	return companionBaseURL(s.run.ip, companion.srv.Port())
}

// ── Drawing ──────────────────────────────────────────────────────────────

func hotspotRenderScreen(renderer *sdl.Renderer, config *Config) {
	s := hotspotCurrent()
	status, statusColor := hotspotTileValue(), ColorTextMuted()
	if s.phase == hotspotOn {
		statusColor = ToastSuccessInk()
	}
	header := tbxHeader("Hotspot", "Let phones and other handhelds join this one", "radar", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &hotspotScreen.nav
	nav.begin(body.Y, body.H)

	if !hotspotSupported() {
		tbxDrawEmpty(renderer, config, body, "radar", "For the handheld",
			"The hotspot turns the handheld into a Wi-Fi network of its own, for netplay and the phone companion without a router. A computer shares its connection from the system's own settings.")
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}
	method, tools := hotspotToolsLabel()
	ssid, pass := hotspotCredentials(config)

	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	hotspotShare.pass = sdl.Rect{}
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		if id == "pass" {
			hotspotShare.pass = screen
		}
		tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}

	// The switch and the network.
	tbxDrawSection(renderer, config, "Hotspot", "radar", x, top+y, w)
	y += tbxSectionHeight
	toggle := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	toggleScreen := toggle
	toggleScreen.Y += top
	sub := hotspotToggleSubtitle(s, tools)
	switch {
	case s.phase == hotspotStarting || s.phase == hotspotStopping:
		pill := "Starting…"
		if s.phase == hotspotStopping {
			pill = "Turning off…"
		}
		tbxDrawRow(renderer, config, fonts, toggleScreen, tbxRowSpec{icon: "radar", title: "Hotspot", subtitle: sub, pill: pill, tone: tbxToneAccent}, nav.focused("toggle"))
	case !tools:
		tbxDrawRow(renderer, config, fonts, toggleScreen, tbxRowSpec{icon: "radar", title: "Hotspot", subtitle: sub, pill: "Not available", tone: tbxToneWarn}, nav.focused("toggle"))
	default:
		hotspotDrawToggleRow(renderer, config, fonts, toggleScreen, "Hotspot", sub, s.phase == hotspotOn, nav.focused("toggle"))
	}
	nav.add("toggle", toggle, row, false)
	row++
	y += rowH + 8
	drawRow("ssid", tbxRowSpec{icon: "wifi", title: "Network name", subtitle: ssid, pill: "Edit", tone: tbxToneNeutral})
	drawRow("pass", tbxRowSpec{icon: "key", title: "Password", subtitle: pass, pill: "Edit", tone: tbxToneNeutral, note: "X: new"})
	drawRow("qr", tbxRowSpec{icon: "qr", title: "Join with a phone", subtitle: "A code the phone's camera reads: no typing", pill: "Show", tone: tbxToneAccent})
	y += 14

	// Who has joined, while it is on.
	if s.phase == hotspotOn {
		tbxDrawSection(renderer, config, "Joined", "devices", x, top+y, w)
		y += tbxSectionHeight
		for _, c := range s.clients {
			title := c.Name
			if title == "" {
				title = "Device"
			}
			bits := []string{}
			if c.IP != "" {
				bits = append(bits, c.IP)
			} else {
				bits = append(bits, "getting an address…")
			}
			if c.MAC != "" {
				bits = append(bits, c.MAC)
			}
			drawRow("", tbxRowSpec{icon: "devices", title: title, subtitle: strings.Join(bits, " · "), pill: "Joined", tone: tbxToneGood})
		}
		if len(s.clients) == 0 {
			drawRow("", tbxRowSpec{icon: "devices", title: "No one has joined yet", subtitle: "Join " + ssid + " from a phone or another handheld", dim: true})
		}
		if url := hotspotCompanionURL(s); url != "" {
			drawRow("companion", tbxRowSpec{icon: "phone", title: "Phone companion", subtitle: url, pill: "Code", tone: tbxToneAccent})
		}
		y += 14
	}

	// What it is for.
	tbxDrawSection(renderer, config, "Good to know", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("", tbxRowSpec{icon: "wifi-off", title: "Your Wi-Fi is off while the hotspot is on",
		subtitle: "The radio does one or the other; turning the hotspot off rejoins your network", dim: true})
	drawRow("", tbxRowSpec{icon: "party", title: "Netplay rooms and the Companion work over it",
		subtitle: "No internet through it: phones keep their mobile data for that", dim: true})
	if method != "" {
		drawRow("", tbxRowSpec{icon: "info", title: "How", subtitle: method, dim: true})
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// hotspotDrawToggleRow draws the switch row: icon-less glass row, title over
// its line, the switch at the right edge.
func hotspotDrawToggleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, title, sub string, on, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-18, r.Y+r.H/2, on)
	textW := toggle.X - r.X - 34
	block := fonts.titleH + 3 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), r.X+18, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), r.X+18, ty+fonts.titleH+3)
}

// ── Input ────────────────────────────────────────────────────────────────

func hotspotSceneAction(config *Config, act tbxAction) bool {
	nav := &hotspotScreen.nav
	switch act {
	case tbxActConfirm:
		hotspotActivate(config, nav.focus)
		return true
	case tbxActX:
		if nav.focus == "pass" && hotspotSupported() {
			hotspotNewPasswordAction(config)
		}
		return true
	case tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func hotspotSceneClick(mx, my int32, config *Config) bool {
	it, ok := hotspotScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	hotspotScreen.nav.focus = it.id
	hotspotActivate(config, it.id)
	return true
}

func hotspotSceneHints() []FooterHint {
	if !hotspotSupported() {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch hotspotScreen.nav.focus {
	case "toggle":
		s := hotspotCurrent()
		_, tools := hotspotToolsLabel()
		switch {
		case s.busy || s.phase == hotspotStarting || s.phase == hotspotStopping:
			// A press now only says "One moment…": no A hint.
		case !tools:
			hints = append(hints, FooterHint{Button: "A", Label: "Why"})
		case s.phase == hotspotOn:
			hints = append(hints, FooterHint{Button: "A", Label: "Turn off"})
		default:
			hints = append(hints, FooterHint{Button: "A", Label: "Turn on"})
		}
	case "ssid":
		hints = append(hints, FooterHint{Button: "A", Label: "Edit"})
	case "pass":
		hints = append(hints, FooterHint{Button: "A", Label: "Edit"}, FooterHint{Button: "X", Label: "New password"})
	case "qr", "companion":
		hints = append(hints, FooterHint{Button: "A", Label: "Show code"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func hotspotActivate(config *Config, id string) {
	if !hotspotSupported() {
		return
	}
	switch id {
	case "toggle":
		hotspotToggle(config)
	case "ssid":
		if !hotspotEditable() {
			return
		}
		PlayActivateSound()
		ssid, _ := hotspotCredentials(config)
		tbxOpenPrompt("Hotspot name", "JukaHub-1234", ssid, func(text string) {
			hotspotSetName(config, text)
		})
	case "pass":
		if !hotspotEditable() {
			return
		}
		PlayActivateSound()
		_, pass := hotspotCredentials(config)
		tbxOpenPrompt("Hotspot password", "8 to 63 characters", pass, func(text string) {
			hotspotSetPassword(config, text)
		})
	case "qr":
		ssid, pass := hotspotCredentials(config)
		hotspotShare.qrTitle = "Join " + ssid
		qrZoomOpenCardDetail(tbxQRModules(hotspotWiFiQR(ssid, pass)), hotspotShare.qrTitle, "Password: "+pass)
	case "companion":
		if url := hotspotCompanionURL(hotspotCurrent()); url != "" {
			qrZoomOpenCardDetail(tbxQRModules(url), "Phone companion", "Join "+hotspotSSIDNow(config)+", then open "+url)
		}
	}
}

func hotspotSSIDNow(config *Config) string {
	ssid, _ := hotspotCredentials(config)
	return ssid
}

// hotspotToggle turns the hotspot on or off, or explains why it cannot.
func hotspotToggle(config *Config) {
	s := hotspotCurrent()
	if s.busy || s.phase == hotspotStarting || s.phase == hotspotStopping {
		ShowToast("One moment…", ToastKindInfo)
		return
	}
	if _, ok := hotspotToolsLabel(); !ok {
		tbxOpenSheet(config, tbxSheetSpec{
			title: "No hotspot on this firmware",
			icon:  "radar",
			body: "JukaHub makes its hotspot with the tools the firmware ships: Allwinner's softap_up, or hostapd. This firmware has neither, so there is nothing to turn on.\n\n" +
				"## What still works\n• Netplay and the phone Companion over a shared Wi-Fi network.\n• A phone's own hotspot: join it from Wi-Fi.",
			buttons: []tbxSheetButton{{label: "OK", primary: true}},
		})
		return
	}
	PlayActivateSound()
	if s.phase == hotspotOn {
		hotspotStopAsync("")
		return
	}
	hotspotStartAsync(config)
}

// hotspotEditable reports whether the name and password can change now: not
// while a start or a stop runs, which would bring the network up with the old
// ones while the screen showed the new. It says so when not.
func hotspotEditable() bool {
	s := hotspotCurrent()
	if s.busy || s.phase == hotspotStarting || s.phase == hotspotStopping {
		ShowToast("One moment…", ToastKindInfo)
		return false
	}
	return true
}

// hotspotSetName saves a new network name; a running hotspot restarts with it.
func hotspotSetName(config *Config, text string) {
	text = strings.TrimSpace(text)
	if text == "" || !hotspotEditable() {
		return
	}
	if err := validateWiFiSSID(text); err != nil {
		ShowToast("Hotspot name: "+err.Error(), ToastKindWarning)
		return
	}
	_, pass := hotspotCredentials(config)
	hotspotSaveCredentials(config, text, pass)
	ShowToast("Hotspot name: "+text, ToastKindSuccess)
	hotspotRestartAsync(config)
}

// hotspotSetPassword saves a new password; a running hotspot restarts with it.
func hotspotSetPassword(config *Config, text string) {
	if text == "" || !hotspotEditable() {
		return
	}
	if err := validateWiFiPassphrase(text); err != nil {
		ShowToast("Hotspot password: "+err.Error(), ToastKindWarning)
		return
	}
	ssid, _ := hotspotCredentials(config)
	hotspotSaveCredentials(config, ssid, text)
	ShowToast("Hotspot password saved", ToastKindSuccess)
	hotspotRestartAsync(config)
}

// hotspotNewPasswordAction makes and saves a new random password.
func hotspotNewPasswordAction(config *Config) {
	if !hotspotEditable() {
		return
	}
	PlayActivateSound()
	ssid, _ := hotspotCredentials(config)
	pass := hotspotNewPassword()
	hotspotSaveCredentials(config, ssid, pass)
	ShowToast("New hotspot password: "+pass, ToastKindSuccess)
	hotspotRestartAsync(config)
}

// hotspotRestartAsync restarts a running hotspot so a new name or password
// takes effect; devices joined before have to join again. Main thread.
func hotspotRestartAsync(config *Config) {
	if hotspotPhase(hotspotPhaseNow.Load()) != hotspotOn {
		return
	}
	prior := hotspotCurrent().run
	go func() {
		if hotspotStopSync("", hotspotGiveBackNone) {
			hotspotState.mu.Lock()
			hotspotState.carry = hotspotRun{iface: prior.iface, hadStation: prior.hadStation, radioWasOff: prior.radioWasOff}
			hotspotState.carrySet = true
			hotspotState.mu.Unlock()
			tbxPost(func() {
				if hotspotStartAsync(config) {
					return
				}
				// The second start did not begin: the radio goes back to
				// what the first start found, or Wi-Fi would stay off.
				hotspotState.mu.Lock()
				carry, ok := hotspotState.carry, hotspotState.carrySet
				hotspotState.carry, hotspotState.carrySet = hotspotRun{}, false
				hotspotState.mu.Unlock()
				if ok {
					go hotspotGiveBackRadio(carry, hotspotGiveBackPrior)
				}
			})
		}
	}()
}
