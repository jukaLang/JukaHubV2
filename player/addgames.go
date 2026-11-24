package main

// addgames.go — "Add games": every way to put games on the handheld, one
// press each, on one screen. It is where a new handheld's first five
// minutes go (the first-run tour's quick start, Home's Recent tab while the
// library is empty, the Games tile), so each row opens the real feature
// rather than explaining it:
//
//   - From your phone: the Phone Companion's code; the page it opens drops
//     each file into its console's folder (companion_drop_route.go);
//   - From a computer: the WebDAV share (davshare_ui.go), or USB storage
//     mode where the firmware allows it (usbstorage.go);
//   - From a USB stick: USB & OTG's import of a stick's Roms and BIOS
//     folders (usbotg_ui.go);
//   - Free games: Get Homebrew's itch.io shelves (homebrew_store.go);
//   - From your RomM server (romm_cloud_ui.go);
//   - and a rescan, for games copied by any other means.
//
// Rows that need a network say so and open Wi-Fi instead.

import (
	"github.com/veandco/go-sdl2/sdl"
)

const addGamesSceneName = "AddGames"

type addGamesScreenState struct {
	visit tbxVisit
	nav   tbxNav
}

var addGamesScreen addGamesScreenState

type addGamesScene struct{ sceneBase }

func (addGamesScene) render(r *sdl.Renderer, c *Config) bool {
	if addGamesScreen.visit.begin() {
		addGamesScreen.nav.scroll = 0
	}
	addGamesRender(r, c)
	return true
}

func (addGamesScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), addGamesAction)
}

func (addGamesScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), addGamesAction)
}

func init() { registerScene(addGamesScene{sceneBase{name: addGamesSceneName}}) }

// addGamesOpen opens the screen. Cross-area contract: the tour's quick
// start, Home's Recent tab and the empty Games tile call it.
func addGamesOpen(config *Config) {
	tbxOpenScene(config, addGamesSceneName)
}

// addGamesOnLAN reports a network address other devices can reach.
func addGamesOnLAN() bool { return localIPv4() != "" }

func addGamesRender(renderer *sdl.Renderer, config *Config) {
	total := homeTabsLibrary().Total
	status, statusColor := "No games yet", ColorTextMuted()
	if total > 0 {
		status, statusColor = pluralizeCount(total, "game", "games"), ToastSuccessInk()
	}
	header := tbxHeader("Add games", "Put games on this handheld — each lands in its console's folder", "add", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &addGamesScreen.nav
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
	lan, online := addGamesOnLAN(), IsOnline()
	needLAN := func(spec tbxRowSpec) tbxRowSpec {
		if !lan {
			spec.pill, spec.tone = "Wi-Fi first", tbxToneWarn
		}
		return spec
	}

	tbxDrawSection(renderer, config, "Over Wi-Fi", "wifi", x, top+y, w)
	y += tbxSectionHeight
	drawRow("phone", needLAN(tbxRowSpec{icon: "phone", title: "From your phone",
		subtitle: "Scan the code, then drop games on the page: each goes to its console's folder", pill: "Code", tone: tbxToneAccent}))
	drawRow("computer", needLAN(tbxRowSpec{icon: "devices", title: "From a computer",
		subtitle: "The card as a network drive on Windows, a Mac or Linux: copy games into Roms", pill: "Open", tone: tbxToneAccent}))
	homebrew := tbxRowSpec{icon: "store", title: "Free games",
		subtitle: "Homebrew made for these consoles, from itch.io", pill: "Browse", tone: tbxToneAccent}
	if !online {
		homebrew.pill, homebrew.tone = "Wi-Fi first", tbxToneWarn
	}
	drawRow("homebrew", homebrew)
	drawRow("romm", tbxRowSpec{icon: "server", title: "From a RomM server",
		subtitle: "Your own game library server: sign in once, then download games", pill: "Open", tone: tbxToneNeutral})
	y += 6
	tbxDrawSection(renderer, config, "With a cable or a stick", "folder", x, top+y, w)
	y += tbxSectionHeight
	if usbStorageSupported() {
		spec := tbxRowSpec{icon: "devices", title: "As a USB drive", subtitle: usbStorageDetail(config), pill: "Start", tone: tbxToneNeutral}
		if usbStorageUnavailable() != "" {
			spec.dim, spec.pill = true, ""
		}
		drawRow("usbdrive", spec)
	}
	drawRow("stick", tbxRowSpec{icon: "folder", title: "From a USB stick",
		subtitle: "Plug a stick with a Roms folder into the USB-C port (an OTG adapter); BIOS files come too", pill: "Open", tone: tbxToneNeutral})
	y += 6
	tbxDrawSection(renderer, config, "Already copied?", "roms", x, top+y, w)
	y += tbxSectionHeight
	drawRow("rescan", tbxRowSpec{icon: "refresh", title: "Look for new games",
		subtitle: "Games go in " + addGamesWhere() + ", one folder per console (GBA, SFC, PS…)", pill: "Scan", tone: tbxToneNeutral})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// addGamesWhere names the card's games folder for the rescan row.
func addGamesWhere() string {
	if IsWindows() {
		return "the Roms folder beside JukaHub"
	}
	return "Roms on the SD card"
}

func addGamesAction(config *Config, act tbxAction) bool {
	nav := &addGamesScreen.nav
	switch act {
	case tbxActConfirm:
		addGamesActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func addGamesSceneClick(mx, my int32, config *Config) bool {
	it, ok := addGamesScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	addGamesScreen.nav.focus = it.id
	addGamesActivate(config, it.id)
	return true
}

func addGamesSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if addGamesScreen.nav.focus != "" {
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func addGamesActivate(config *Config, id string) {
	lan := addGamesOnLAN()
	switch id {
	case "phone", "computer":
		if !lan {
			PlayActivateSound()
			wifiScrOpen(config)
			return
		}
		PlayActivateSound()
		if id == "computer" {
			davShareOpenScreen(config)
			return
		}
		companionEnsureStarted()
		if len(companion.qr) == 0 {
			ShowToast("The phone code is not ready yet — try again in a moment", ToastKindInfo)
			return
		}
		homeShowPairingQR()
	case "homebrew":
		PlayActivateSound()
		if !IsOnline() {
			wifiScrOpen(config)
			return
		}
		homebrewStoreOpen(config)
	case "romm":
		PlayActivateSound()
		rommCloudOpen(config)
	case "usbdrive":
		usbStorageOpen(config)
	case "stick":
		PlayActivateSound()
		usbOtgOpenScreen(config)
	case "rescan":
		PlayActivateSound()
		if !homeTabsLib.scanning && !gameLib.scanning {
			homeTabsStartScan()
		}
		ShowToast("Looking for new games…", ToastKindInfo)
	}
}
