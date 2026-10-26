package main

// netplay_ui.go — the LAN play panel shown over the game library.
//
// Opened with X on a highlighted game. Two rows: host it here, or join someone
// else's session with the same game. The device's own address is printed in
// full before anything launches, because once RetroArch takes the screen the
// player can no longer read it.

import (
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type netplayPanelState struct {
	open    bool
	game    libraryGame
	cursor  int
	address string // last address typed for a join
	status  string
	warn    bool
	ready   string // "Ready · mgba_libretro" or why not
	readyOK bool
	panel   sdl.Rect
	rowHits []gameLibRowHit
}

var netplayPanel netplayPanelState

// netplayLastAddress remembers the last host address so a second round trip is
// one button press instead of retyping an IP on a D-pad.
var netplayLastAddress string

var netplayMenuRows = []string{"Host this game", "Join a game"}

// netplayEnabledKey is the custom-map copy of the LAN Play switch. The typed
// field is what the config file documents; the custom key is what survives in
// jukauser.json, so an upgrade from a build without the switch keeps the
// shipped default instead of reading "missing" as off.
const netplayEnabledKey = "netplay_enabled"

// netplayEnabled reports whether the Game Library should offer the LAN Play
// panel. A nil config (early startup, tests) behaves as enabled.
func netplayEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	if raw, ok := customString(config.Variables.Custom, netplayEnabledKey); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return config.Variables.NetPlayEnabled
}

func setNetplayEnabled(config *Config, on bool) {
	if config == nil {
		return
	}
	config.Variables.NetPlayEnabled = on
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[netplayEnabledKey] = on
}

// netplayReadiness describes whether this console can be played over the LAN
// right now: the emulator and the core are both resolved before the panel draws
// a red row instead of failing at launch.
func netplayReadiness(game libraryGame) (string, bool) {
	if note := netplaySupportNote(game.System); note != "" {
		return note, false
	}
	if netplayRetroArchPath() == "" {
		return "RetroArch not found — install it from Emulators", false
	}
	core := netplayCorePath(game.System, netplayCoreDirs())
	if core == "" {
		return "The RetroArch core for this console is not installed", false
	}
	name := strings.TrimSuffix(filepath.Base(core), filepath.Ext(core))
	name = strings.TrimSuffix(name, "_libretro")
	return "Ready · " + name, true
}

// netplayLocalAddress is the string the other player types in.
func netplayLocalAddress() string {
	ip := localIPv4()
	if ip == "" {
		return ""
	}
	session := netplaySession{Role: netplayHost, Host: ip, Port: netplayPort()}
	return session.address()
}

// openNetplayForSelectedGame opens the panel for the highlighted library game.
func openNetplayForSelectedGame() bool {
	game, ok := selectedLibraryGame()
	if !ok {
		return false
	}
	// The switch lives in Quick Settings, not here: the panel is a host/join
	// dialog, and a disabled feature that opens to an empty panel would look
	// broken. Saying where to turn it on costs one toast.
	if !netplayEnabled(appConfig) {
		ShowToast("LAN play is off · turn it on in Quick Settings", ToastKindInfo)
		return false
	}
	openNetplayPanel(game)
	return true
}

func openNetplayPanel(game libraryGame) {
	if strings.TrimSpace(game.Title) == "" {
		game.Title = gameDisplayTitle(game.Path)
	}
	netplayPanel.open = true
	netplayPanel.game = game
	netplayPanel.cursor = 0
	netplayPanel.status = ""
	netplayPanel.warn = false
	netplayPanel.address = netplayLastAddress
	netplayPanel.ready, netplayPanel.readyOK = netplayReadiness(game)
	PlayNavSound()
}

func closeNetplayPanel() {
	netplayPanel.open = false
	netplayPanel.rowHits = netplayPanel.rowHits[:0]
	netplayPanel.panel = sdl.Rect{}
	if virtualKeyboardActive && vkTarget == vkTargetNetplay {
		vkCloseKeyboard()
	}
}

func netplaySetStatus(message string, warn bool) {
	netplayPanel.status = message
	netplayPanel.warn = warn
}

// handleNetplayKey consumes every key while the panel is open, so the library
// underneath never reacts behind it. The on-screen keyboard is handled earlier
// in the event loop, so typing an address never reaches this.
func handleNetplayKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !netplayPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		netplayMove(-1)
	case sdl.K_DOWN:
		netplayMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		netplayActivate(config)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeNetplayPanel()
	}
	return true
}

func ctrlNetplay(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !netplayPanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		netplayMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		netplayMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		netplayActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeNetplayPanel()
	default:
		return false
	}
	return true
}

func handleNetplayMouse(mx, my int32) bool {
	if !netplayPanel.open {
		return false
	}
	for _, hit := range netplayPanel.rowHits {
		if pointInRect(mx, my, hit.rect) {
			netplayPanel.cursor = hit.index
			PlayNavSound()
			return true
		}
	}
	if netplayPanel.panel.W > 0 && pointInRect(mx, my, netplayPanel.panel) {
		return true
	}
	closeNetplayPanel()
	return true
}

func netplayMove(delta int) {
	if len(netplayMenuRows) == 0 {
		return
	}
	netplayPanel.cursor += delta
	if netplayPanel.cursor < 0 {
		netplayPanel.cursor = len(netplayMenuRows) - 1
	}
	if netplayPanel.cursor >= len(netplayMenuRows) {
		netplayPanel.cursor = 0
	}
	PlayNavSound()
}

// netplayActivate runs the highlighted row. Join asks for an address first when
// one is not already remembered.
func netplayActivate(config *Config) {
	switch netplayPanel.cursor {
	case 0:
		netplayStartHost(config)
	case 1:
		if strings.TrimSpace(netplayPanel.address) == "" {
			vkOpenNetplay()
			return
		}
		netplayStartJoin(netplayPanel.address, config)
	}
}

func netplayStartHost(config *Config) {
	plan, err := netplayPlanFor(netplayHost, netplayPanel.game, "")
	if err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	address := plan.Session.address()
	if err := netplayLaunch(plan, config); err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	closeNetplayPanel()
	ShowToast("Hosting "+plan.Session.Game.Title+" — join at "+address, ToastKindInfo)
}

func netplayStartJoin(raw string, config *Config) {
	plan, err := netplayPlanFor(netplayJoin, netplayPanel.game, raw)
	if err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	netplayPanel.address = raw
	netplayLastAddress = plan.Session.address()
	if err := netplayLaunch(plan, config); err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	closeNetplayPanel()
	ShowToast("Joining "+plan.Session.Game.Title+" at "+plan.Session.address(), ToastKindInfo)
}

// netplayJoinSubmit is called by the on-screen keyboard when the address is
// committed. The keyboard has already closed, so failures land in the panel.
func netplayJoinSubmit(raw string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		netplaySetStatus("Enter the other device's address", true)
		return
	}
	netplayPanel.address = text
	netplayStartJoin(text, appConfig)
}

// netplayRowDetail is the second line of each row.
func netplayRowDetail(index int) string {
	switch index {
	case 0:
		if address := netplayLocalAddress(); address != "" {
			return "Start, then join from the other device at " + address
		}
		return "Share this console's Wi-Fi address with the other player"
	case 1:
		if netplayPanel.address != "" {
			return "Connect to " + netplayPanel.address + "  ·  A to start"
		}
		return "Type the host's address, e.g. 192.168.1.5"
	}
	return ""
}

// renderNetplayPanel draws the panel over the blurred library behind it.
func renderNetplayPanel(renderer *sdl.Renderer, config *Config) {
	if !netplayPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = int32(24)
	// The rows carry a title and a detail line; 76px leaves the detail clear of
	// the rounded border at both the 34px and the scaled-up medium font.
	rowH := int32(76)
	rowGap := int32(10)
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height.
	subtitle := netplayPanel.game.Title
	if system := strings.TrimSpace(netplayPanel.game.System); system != "" {
		subtitle += " · " + system
	}
	header := UIPanelHeader("LAN Play", subtitle, "tools")
	headerH := UIPanelHeaderBand(config, header, 14)
	// Address line, readiness line and the same-core/ROM reminder.
	infoH := int32(18) + medH + int32(8) + 2*smallH + int32(26)
	footerH := int32(14) + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + int32(len(netplayMenuRows))*(rowH+rowGap) + infoH + footerH + int32(24)
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	netplayPanel.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 200)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header.X = panelX + pad
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*pad
	UIPageHeaderDraw(renderer, config, header)

	focusColor := installHighlight()
	rowY := panelY + headerH
	netplayPanel.rowHits = netplayPanel.rowHits[:0]
	for i, label := range netplayMenuRows {
		rect := sdl.Rect{X: panelX + pad, Y: rowY + int32(i)*(rowH+rowGap), W: panelW - 2*pad, H: rowH}
		netplayPanel.rowHits = append(netplayPanel.rowHits, gameLibRowHit{index: i, rect: rect})
		selected := i == netplayPanel.cursor
		bg := WithAlpha(ColorSurfaceRow, 160)
		if selected {
			bg = WithAlpha(focusColor, 48)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 12, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 12, 2, focusColor)
		}
		renderText(renderer, config, medium, label, ColorTextPrimary(), rect.X+16, rect.Y+8)
		renderText(renderer, config, font, ellipsize(font, netplayRowDetail(i), rect.W-32),
			ColorTextTertiary(), rect.X+16, rect.Y+8+medH+2)
	}

	infoY := rowY + int32(len(netplayMenuRows))*(rowH+rowGap) + int32(8)
	if address := netplayLocalAddress(); address != "" {
		renderText(renderer, config, medium, "This device: "+address, ColorTextAccent(), panelX+pad, infoY)
	} else {
		renderText(renderer, config, medium, "No Wi-Fi address — connect to a network first",
			ColorWarning, panelX+pad, infoY)
	}
	readyColor := ColorTextTertiary()
	if !netplayPanel.readyOK {
		readyColor = ColorWarning
	}
	readyY := infoY + medH + 8
	renderText(renderer, config, font, ellipsize(font, netplayPanel.ready, panelW-2*pad),
		readyColor, panelX+pad, readyY)
	renderText(renderer, config, font, "Both devices need the same core and the same ROM",
		ColorTextTertiary(), panelX+pad, readyY+smallH+6)

	hint := hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Start"}, FooterHint{"B", "Close"})
	hintColor := ColorTextSecondary()
	if netplayPanel.status != "" {
		hint = netplayPanel.status
		if netplayPanel.warn {
			hintColor = ColorWarning
		}
	}
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*pad),
		hintColor, panelX+pad, panelY+panelH-footerH)
}
