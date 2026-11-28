package main

// tailscaled_ui.go — the Tailscale screen (Tools → Network tools →
// Tailscale, Settings → Network → Tailscale), drawn like the other tool
// screens (hotspot_ui.go).
//
// Top to bottom: the switch (Install the first time), signing in (a QR code
// for the browser sign-in, or an auth key), this device's tailnet addresses,
// the other devices in the tailnet (A copies an address), what reaches the
// handheld over it, and the program (version, Update, Log out, Remove). On a
// PC it shows Tailscale for Windows' state instead. The screen only reads
// tailscaledSnapshotNow(); everything that waits runs on the worker
// (tailscaled.go).

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// tailscaledSceneName is the scene Network tools and Settings open.
const tailscaledSceneName = "Tailscale"

// tailscaleKeyVKTarget aims the keyboard at an auth key (masked, submit
// only, never stored: it is handed to one sign-in).
const tailscaleKeyVKTarget vkTargetKind = 2963

// tailscaleKeyBuffer is the key being typed; cleared when the keyboard closes.
var tailscaleKeyBuffer string

// tailscaledLoginToken names the sign-in code's sheet.
const tailscaledLoginToken = "tailscale-login"

type tailscaledScreenState struct {
	visit tbxVisit
	nav   tbxNav
	// shownURL is the sign-in address whose code opened by itself: once per
	// address.
	shownURL string
	// confirm is the row waiting for a second A ("logout", "remove").
	confirm string
}

var tailscaledScreen tailscaledScreenState

// tailscaledOpenScreen opens the screen. Cross-area contract: Network tools
// and Settings → Network call it.
func tailscaledOpenScreen(config *Config) {
	tbxOpenScene(config, tailscaledSceneName)
}

type tailscaledScene struct{ sceneBase }

func (tailscaledScene) render(r *sdl.Renderer, c *Config) bool {
	if tailscaledScreen.visit.begin() {
		tailscaledScreen.nav.scroll = 0
		tailscaledScreen.confirm = ""
		tailscaledNoteViewer()
		tailscaledStartWorker()
		tailscaledKick()
	}
	tailscaledNoteViewer()
	tailscaledRenderScreen(r, c)
	return true
}

func (tailscaledScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), tailscaledSceneAction)
}

func (tailscaledScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), tailscaledSceneAction)
}

func init() { registerScene(tailscaledScene{sceneBase{name: tailscaledSceneName}}) }

// tailscaledSettingsRow is Settings → Network's door to the screen.
func tailscaledSettingsRow() setRow {
	return setRow{
		ID: "tailscale", Label: "Tailscale", Kind: setKindAction,
		Detail: func(*Config) string {
			if !tailscaledCanInstall() {
				return "Shows Tailscale for Windows: this computer's tailnet address and your devices"
			}
			return "Your own private network: reach this handheld's SSH, files and phone companion from anywhere"
		},
		Value: tailscaledRowValue,
		Open:  tailscaledOpenScreen,
	}
}

func tailscaledRowValue(*Config) string {
	v, _ := tailscaledTileValue()
	return v
}

// ── Drawing ──────────────────────────────────────────────────────────────

func tailscaledRenderScreen(renderer *sdl.Renderer, config *Config) {
	s := tailscaledSnapshotNow()
	want := tailscaledEnabled(config)
	status := tailscaledStatusText(s, want)
	statusColor := ColorTextMuted()
	if tailscaledConnected(s) {
		statusColor = ToastSuccessInk()
	}
	tailscaledLoginSheetFollow(config, s)
	header := tbxHeader("Tailscale", "Your own private network: reach this handheld from anywhere", "network", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &tailscaledScreen.nav
	nav.begin(body.Y, body.H)

	if !tailscaledCanInstall() && !s.System {
		tbxDrawEmpty(renderer, config, body, "network", "Tailscale for Windows",
			"Install Tailscale for Windows from tailscale.com and sign in there: this page then shows this computer's tailnet address and your devices. On the handheld, JukaHub installs and runs its own Tailscale.")
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}

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
	section := func(title, icon string) {
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}
	running := s.Phase == tailscaledPhaseRunning
	connected := tailscaledConnected(s)

	section("Tailscale", "network")
	if s.System {
		drawRow("", tbxRowSpec{icon: "network", title: "Tailscale for Windows", subtitle: status + " · sign in and out from its icon in the taskbar", dim: true})
	} else {
		toggle := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		toggleScreen := toggle
		toggleScreen.Y += top
		tailscaledDrawSwitch(renderer, config, fonts, toggleScreen, s, want, nav.focused("toggle"))
		nav.add("toggle", toggle, row, false)
		row++
		y += rowH + 8
		signIn := s.LoggingIn || s.Backend == "NeedsLogin" || s.Backend == "Stopped" || s.Backend == "NeedsMachineAuth"
		if running && !connected && signIn {
			login := tbxRowSpec{icon: "qr", title: "Log in", subtitle: "Sign in from your phone: scan a code, no typing", pill: "Log in", tone: tbxToneAccent}
			switch {
			case s.AuthURL != "":
				login.subtitle, login.pill = "The sign-in code is ready", "Show code"
			case s.LoggingIn:
				login.subtitle, login.pill = "Asking Tailscale for a sign-in code…", "Wait"
			case s.Backend == "Stopped":
				login.title, login.subtitle, login.pill = "Connect", "Signed in, but disconnected", "Connect"
			case s.Backend == "NeedsMachineAuth":
				login.subtitle = "An admin of your tailnet approves this device in the admin console"
			}
			drawRow("login", login)
			drawRow("authkey", tbxRowSpec{icon: "key", title: "Log in with an auth key", subtitle: "A key from the admin console's Keys page (tskey-auth-…)", pill: "Key", tone: tbxToneNeutral})
		}
		if s.Detail != "" {
			drawRow("", tbxRowSpec{icon: "warning", title: "Last problem", subtitle: s.Detail, dim: true})
		}
	}

	if connected {
		sub := strings.Join(s.IPs, " · ")
		if s.DNSName != "" {
			sub += " · " + s.DNSName
		}
		title := "This device"
		if s.Tailnet != "" {
			title += " · " + s.Tailnet
		}
		drawRow("device", tbxRowSpec{icon: "devices", title: title, subtitle: sub, pill: "Copy", tone: tbxToneNeutral})
		y += 14
		section("Your devices", "devices")
		for i, p := range s.Peers {
			if i == tailscaledPeersMax {
				drawRow("", tbxRowSpec{icon: "devices", title: pluralizeCount(len(s.Peers)-i, "more device", "more devices"), dim: true})
				break
			}
			pill, tone := "Offline", tbxToneNeutral
			if p.Online {
				pill, tone = "Online", tbxToneGood
			}
			name, _, _ := strings.Cut(p.Name, ".")
			drawRow("peer:"+strconv.Itoa(i), tbxRowSpec{icon: "devices", title: name, subtitle: strings.Join(p.IPs, " · "), pill: pill, tone: tone})
		}
		if len(s.Peers) == 0 {
			drawRow("", tbxRowSpec{icon: "devices", title: "No other devices yet",
				subtitle: "Install Tailscale on your phone or PC and sign in with the same account", dim: true})
		}
	}

	if !s.System {
		y += 14
		section("Good to know", "info")
		drawRow("", tbxRowSpec{icon: "server", title: "SSH, FTP, phone companion, VNC and network drive",
			subtitle: "Turn one on in Network tools, then use this handheld's tailnet address from anywhere", dim: true})
		drawRow("", tbxRowSpec{icon: "shield", title: "Only connections to this handheld use it",
			subtitle: "No system changes: games, stores and the browser keep the normal internet", dim: true})
		drawRow("", tbxRowSpec{icon: "battery", title: "Paused below 15% battery and without Wi-Fi",
			subtitle: "Back by itself at 20% or on the charger, and when Wi-Fi returns", dim: true})
		if s.Installed {
			y += 14
			section("Program", "update")
			ver := tbxRowSpec{icon: "update", title: "Tailscale " + s.Version, subtitle: "A checks pkgs.tailscale.com for a newer version", pill: "Check", tone: tbxToneNeutral}
			if s.Version == "" {
				ver.title = "Tailscale"
			}
			if tailscaledUpdateReady(s) {
				ver.subtitle, ver.pill, ver.tone = "Version "+s.Latest+" is out: A installs it", "Update", tbxToneAccent
			} else if s.Latest != "" {
				ver.subtitle = "The newest version · Emulator updates keep it current"
			}
			drawRow("version", ver)
			if running && s.Backend != "NeedsLogin" && s.Backend != "" {
				out := tbxRowSpec{icon: "logout", title: "Log out", subtitle: "This handheld leaves your tailnet until you log in again", pill: "Log out", tone: tbxToneNeutral}
				if tailscaledScreen.confirm == "logout" {
					out.pill, out.tone = "A again", tbxToneWarn
				}
				drawRow("logout", out)
			}
			rm := tbxRowSpec{icon: "trash", title: "Remove Tailscale", subtitle: "Deletes the program and this device's tailnet key", pill: "Remove", tone: tbxToneNeutral}
			if tailscaledScreen.confirm == "remove" {
				rm.pill, rm.tone = "A again", tbxToneWarn
			}
			drawRow("remove", rm)
		}
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// tailscaledUpdateReady reports whether the last check found a newer build.
func tailscaledUpdateReady(s tailscaledSnapshot) bool {
	return s.Installed && s.Latest != "" && s.Version != "" && portVersionNewer(s.Latest, s.Version)
}

// tailscaledDrawSwitch draws the first row: Install, Installing… or the
// switch with what Tailscale is doing.
func tailscaledDrawSwitch(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, s tailscaledSnapshot, want, focused bool) {
	switch {
	case s.Phase == tailscaledPhaseInstalling:
		tbxDrawRow(renderer, config, fonts, r, tbxRowSpec{icon: "download", title: "Tailscale",
			subtitle: "Downloading the official build and checking its SHA-256", pill: "Installing…", tone: tbxToneAccent}, focused)
	case !s.Installed && s.Phase != tailscaledPhaseChecking:
		tbxDrawRow(renderer, config, fonts, r, tbxRowSpec{icon: "download", title: "Tailscale",
			subtitle: "A downloads the official build from pkgs.tailscale.com and checks its SHA-256", pill: "Install", tone: tbxToneAccent}, focused)
	default:
		sub := tailscaledStatusText(s, want)
		if !want {
			sub = "Off · on, this handheld joins your tailnet whenever Wi-Fi is up"
		}
		hotspotDrawToggleRow(renderer, config, fonts, r, "Tailscale", sub, want, focused)
	}
}

// tailscaledLoginSheetFollow opens the sign-in code by itself once per new
// address, and closes it once the handheld is in. Main thread.
func tailscaledLoginSheetFollow(config *Config, s tailscaledSnapshot) {
	if tbxSheet.open && tbxSheet.spec.token == tailscaledLoginToken && tailscaledConnected(s) {
		tbxCloseSheet()
		return
	}
	link := s.AuthURL
	if !s.LoggingIn || !strings.HasPrefix(link, "https://") || link == tailscaledScreen.shownURL || tbxSheetVisible(config) {
		return
	}
	tailscaledScreen.shownURL = link
	tailscaledShowCode(config, link)
}

// tailscaledShowCode shows the sign-in address as a code a phone scans.
func tailscaledShowCode(config *Config, link string) {
	tbxOpenSheet(config, tbxSheetSpec{
		token: tailscaledLoginToken,
		title: "Log in to Tailscale",
		icon:  "qr",
		body: "Scan the code with your phone's camera and sign in, or open this address on a computer:\n\n" + link +
			"\n\nThe handheld joins your tailnet the moment you finish; this closes by itself.",
		qr:      tbxQRModules(link),
		qrLabel: "Scan to sign in",
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// ── Input ────────────────────────────────────────────────────────────────

func tailscaledSceneAction(config *Config, act tbxAction) bool {
	nav := &tailscaledScreen.nav
	switch act {
	case tbxActConfirm:
		tailscaledActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	before := nav.focus
	handled := tbxNavAction(nav, act)
	if nav.focus != before {
		tailscaledScreen.confirm = ""
	}
	return handled
}

func tailscaledSceneClick(mx, my int32, config *Config) bool {
	it, ok := tailscaledScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	tailscaledScreen.nav.focus = it.id
	tailscaledActivate(config, it.id)
	return true
}

func tailscaledSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	s := tailscaledSnapshotNow()
	if !tailscaledCanInstall() && !s.System {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	focus := tailscaledScreen.nav.focus
	label := ""
	switch {
	case focus == "toggle":
		switch {
		case s.Phase == tailscaledPhaseInstalling, s.Phase == tailscaledPhaseChecking:
		case !s.Installed:
			label = "Install"
		case tailscaledEnabled(appConfig):
			label = "Turn off"
		default:
			label = "Turn on"
		}
	case focus == "login":
		label = "Log in"
		if s.AuthURL != "" {
			label = "Show code"
		}
	case focus == "authkey":
		label = "Enter key"
	case focus == "device", strings.HasPrefix(focus, "peer:"):
		label = "Copy address"
	case focus == "version":
		label = "Check"
		if tailscaledUpdateReady(s) {
			label = "Update"
		}
	case focus == "logout":
		label = "Log out"
	case focus == "remove":
		label = "Remove"
	}
	if label != "" {
		if focus != "" && tailscaledScreen.confirm == focus {
			label = "Confirm"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func tailscaledActivate(config *Config, id string) {
	s := tailscaledSnapshotNow()
	switch {
	case id == "toggle":
		tailscaledTogglePress(config, s)
	case id == "login":
		switch {
		case strings.HasPrefix(s.AuthURL, "https://"):
			PlayActivateSound()
			tailscaledScreen.shownURL = s.AuthURL
			tailscaledShowCode(config, s.AuthURL)
		case s.LoggingIn:
			ShowToast("One moment — Tailscale is preparing the sign-in code", ToastKindInfo)
		default:
			PlayActivateSound()
			tailscaledPost(func() { tailscaledLoginStart("") })
		}
	case id == "authkey":
		PlayActivateSound()
		tailscaledOpenKeyKeyboard()
	case id == "device":
		netToolsCopy(tailscaledIPv4(s.IPs), "Tailnet address copied")
	case strings.HasPrefix(id, "peer:"):
		i, err := strconv.Atoi(strings.TrimPrefix(id, "peer:"))
		if err != nil || i < 0 || i >= len(s.Peers) {
			return
		}
		p := s.Peers[i]
		addr := tailscaledIPv4(p.IPs)
		if addr == "" && len(p.IPs) > 0 {
			addr = p.IPs[0]
		}
		netToolsCopy(addr, "Address of "+p.Name+" copied")
	case id == "version":
		PlayActivateSound()
		if tailscaledUpdateReady(s) {
			tailscaledInstallStart()
			return
		}
		tailscaledCheckNow()
	case id == "logout" || id == "remove":
		if tailscaledScreen.confirm != id {
			tailscaledScreen.confirm = id
			msg := hintSentence("Press %s again to log out of Tailscale", "A", "Log out")
			if id == "remove" {
				msg = hintSentence("Press %s again to remove Tailscale", "A", "Remove")
			}
			ShowToast(msg, ToastKindWarning)
			return
		}
		tailscaledScreen.confirm = ""
		PlayActivateSound()
		if id == "logout" {
			tailscaledPost(tailscaledLogout)
			return
		}
		// Removed first, switched off after: the daemon must still run for
		// the log-out that expires this device's key in the tailnet.
		tailscaledPost(func() {
			err := tailscaledRemoveAll()
			tbxPost(func() { tailscaledSetEnabled(appConfig, false) })
			if err != nil {
				ShowToast("Could not remove Tailscale: "+firstLine(err.Error()), ToastKindError)
				return
			}
			ShowToast("Tailscale removed", ToastKindSuccess)
		})
	}
}

// tailscaledCheckNow asks pkgs.tailscale.com for a newer build (a press).
func tailscaledCheckNow() {
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi to check for a newer Tailscale", ToastKindInfo)
		return
	}
	ShowToast("Checking for a newer Tailscale…", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		newer, ver, err := tailscaledUpdateCheck(ctx, nil)
		switch {
		case err != nil:
			ShowToast("Tailscale: "+firstLine(err.Error()), ToastKindError)
		case newer:
			ShowToast("Tailscale "+ver+" is out — "+hintSentence("press %s to update", "A", "Update"), ToastKindInfo)
		default:
			ShowToast("Tailscale "+ver+" is the newest version", ToastKindSuccess)
		}
	}()
}

// tailscaledTogglePress is A on the first row: Install, or the switch.
func tailscaledTogglePress(config *Config, s tailscaledSnapshot) {
	switch {
	case s.Phase == tailscaledPhaseChecking:
		tailscaledStartWorker()
		tailscaledKick()
		ShowToast("One moment — checking for Tailscale", ToastKindInfo)
		return
	case s.Phase == tailscaledPhaseInstalling:
		ShowToast("Tailscale is installing — one moment", ToastKindInfo)
		return
	}
	PlayActivateSound()
	if !s.Installed {
		tailscaledSetEnabled(config, true)
		tailscaledInstallStart()
		return
	}
	on := !tailscaledEnabled(config)
	tailscaledSetEnabled(config, on)
	if on {
		ShowToast("Tailscale on — joining your tailnet", ToastKindSuccess)
	} else {
		ShowToast("Tailscale off", ToastKindInfo)
	}
}

// ── The auth-key keyboard ────────────────────────────────────────────────

// tailscaledOpenKeyKeyboard opens the masked keyboard for an auth key. It
// starts empty.
func tailscaledOpenKeyKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = tailscaleKeyVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	tailscaleKeyBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// tailscaledKeySubmit takes a typed key (vkSubmit, after the keyboard
// closed). An empty submit does nothing.
func tailscaledKeySubmit(text string) {
	key := strings.TrimSpace(text)
	if key == "" {
		return
	}
	if !strings.HasPrefix(key, "tskey-") || len(key) > 256 || strings.ContainsAny(key, " \t\r\n") {
		PlayErrorSound()
		ShowToast("That is not a Tailscale auth key: they start with tskey-", ToastKindWarning)
		return
	}
	ShowToast("Logging in to Tailscale with the key…", ToastKindInfo)
	tailscaledPost(func() { tailscaledLoginWithKey(key) })
}
