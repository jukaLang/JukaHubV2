package main

// netplay_ui.go — the Netplay panel shown over the game library.
//
// Opened with X on a highlighted game. The rows, top to bottom:
//
//   - Host this game on the home network (LAN, the old link-cable-over-Wi-Fi).
//   - Host it online: RetroArch announces the room in its public lobby and
//     carries it through a relay, so a friend on another network can join
//     without port forwarding. Left/Right picks the relay.
//   - Rooms on this network: other JukaHubs hosting, heard live
//     (netplay_rooms.go). A room joins with this device's copy of the ROM and
//     the host's core, whatever game the panel was opened on.
//   - Online rooms from RetroArch's lobby, only those whose game is in this
//     library; Y refreshes the list.
//   - Join by address, for a host that cannot be discovered (another
//     RetroArch, a VPN, a different subnet).
//
// The device's own address is printed before anything launches, because once
// RetroArch takes the screen the player can no longer read it.

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type netplayRowKind uint8

const (
	netplayRowHostLAN netplayRowKind = iota
	netplayRowHostOnline
	netplayRowRoom
	netplayRowOnlineInfo
	netplayRowAddress
)

// netplayRow is one line of the panel. Room rows carry their join plan (or
// the reason they cannot be joined), resolved when the list is rebuilt rather
// than on every frame.
type netplayRow struct {
	kind   netplayRowKind
	key    string
	label  string
	detail string
	room   netplayRoom
	plan   netplayPlan
	err    string
}

type netplayPanelState struct {
	open    bool
	game    libraryGame
	cursor  int
	scroll  int
	address string // last address typed for a join
	status  string
	warn    bool
	ready   string // "Ready · mgba" or why not
	readyOK bool
	panel   sdl.Rect
	rowHits []gameLibRowHit

	rows     []netplayRow
	rowsSig  string
	opened   time.Time
	lastRows time.Time
}

var netplayPanel netplayPanelState

// netplayLastAddress remembers the last host address so a second round trip is
// one button press instead of retyping an IP on a D-pad.
var netplayLastAddress string

// netplayOnlineRowsMax bounds the online rooms listed; the lobby can hold
// hundreds and a handheld list of more than a screenful is not browsable.
const netplayOnlineRowsMax = 30

// netplayEnabledKey is the custom-map copy of the LAN Play switch. The typed
// field is what the config file documents; the custom key is what survives in
// jukauser.json, so an upgrade from a build without the switch keeps the
// shipped default instead of reading "missing" as off.
const netplayEnabledKey = "netplay_enabled"

// netplayEnabled reports whether the Game Library should offer the Netplay
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

// netplayReadiness describes whether this console can be hosted right now:
// the emulator and the core are both resolved before the panel draws a red
// line instead of failing at launch.
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
	return "Ready to host · " + netplayCoreLabel(core), true
}

// netplayCoreLabel is a core file's short name ("mgba").
func netplayCoreLabel(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.TrimSuffix(name, "_libretro")
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
		ShowToast("Netplay is off · turn LAN play on in Quick Settings", ToastKindInfo)
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
	netplayPanel.scroll = 0
	netplayPanel.status = ""
	netplayPanel.warn = false
	netplayPanel.address = netplayLastAddress
	netplayPanel.ready, netplayPanel.readyOK = netplayReadiness(game)
	netplayPanel.rows = nil
	netplayPanel.rowsSig = ""
	netplayPanel.opened = time.Now()
	netplayStartDiscovery()
	// The online list is fetched once per minute at most: reopening the panel
	// for another game reuses it, and Y refreshes on demand.
	if _, busy, _, _ := netplayOnlineSnapshot(); !busy && netplayOnlineStale(time.Minute) {
		netplayRefreshLobby()
	}
	netplayRebuildRows(true)
	PlayNavSound()
}

// netplayOnlineStale reports whether the lobby list is older than age.
func netplayOnlineStale(age time.Duration) bool {
	netplayOnline.mu.Lock()
	defer netplayOnline.mu.Unlock()
	return netplayOnline.fetched.IsZero() || time.Since(netplayOnline.fetched) > age
}

func closeNetplayPanel() {
	netplayPanel.open = false
	netplayPanel.rowHits = netplayPanel.rowHits[:0]
	netplayPanel.panel = sdl.Rect{}
	netplayPanel.rows = nil
	netplayStopDiscovery()
	if virtualKeyboardActive && vkTarget == vkTargetNetplay {
		vkCloseKeyboard()
	}
}

func netplaySetStatus(message string, warn bool) {
	netplayPanel.status = message
	netplayPanel.warn = warn
	if warn {
		PlayErrorSound()
	}
}

// netplayRebuildRows refreshes the row list when a scan or the lobby fetch
// landed (or force). The highlighted row keeps its place by key.
func netplayRebuildRows(force bool) {
	lan, lanRev := netplayLANSnapshot()
	online, busy, onlineErr, onlineRev := netplayOnlineSnapshot()
	sig := strings.Join([]string{
		strconv.FormatUint(lanRev, 10), strconv.FormatUint(onlineRev, 10), strconv.FormatUint(gameLib.scanGen, 10),
		strconv.Itoa(gameLib.lib.Total),
		netplayPanel.address, netplayRelayFor(appConfig).ID,
	}, "|")
	if !force && sig == netplayPanel.rowsSig {
		return
	}
	netplayPanel.rowsSig = sig
	selected := ""
	if netplayPanel.cursor >= 0 && netplayPanel.cursor < len(netplayPanel.rows) {
		selected = netplayPanel.rows[netplayPanel.cursor].key
	}

	game := netplayPanel.game
	rows := []netplayRow{
		{kind: netplayRowHostLAN, key: "host-lan", label: "Host on this network"},
		{kind: netplayRowHostOnline, key: "host-online", label: "Host online"},
	}
	rows[0].detail = netplayHostLANDetail()
	relay := netplayRelayFor(appConfig)
	rows[1].detail = "Friends anywhere join through the " + relay.Label + " relay — no router setup"

	index := netplayLibraryIndex()
	// One RetroArch lookup and one core-folder listing per rebuild, shared by
	// every room row (each lookup is a handful of stats, a PATH search on a
	// desktop without RetroArch).
	retroArch := netplayRetroArchPath()
	installed := netplayInstalledCores(netplayCoreDirs())
	for _, room := range lan {
		rows = append(rows, netplayRoomRow(room, index, installed, retroArch))
	}

	// Online: only rooms whose game is here; the panel's own game first. The
	// list is cut before rooms are resolved into rows.
	wantName := netplayNormName(game.Path)
	var mine, others []netplayRoom
	for _, room := range online {
		if _, ok := netplayMatchGame(room, index); !ok {
			continue
		}
		if netplayNormName(room.File) == wantName {
			mine = append(mine, room)
		} else {
			others = append(others, room)
		}
	}
	total := len(mine) + len(others)
	listed := append(mine, others...)
	if len(listed) > netplayOnlineRowsMax {
		listed = listed[:netplayOnlineRowsMax]
	}

	// The info row heads the online rooms (and is the refresh button).
	info := netplayRow{kind: netplayRowOnlineInfo, key: "online-info", label: "Online rooms"}
	switch {
	case busy:
		info.detail = "Looking for online rooms…"
	case onlineErr != "" && len(online) > 0:
		info.detail = onlineErr + " — showing the last list"
		info.err = onlineErr
	case onlineErr != "":
		info.detail = onlineErr
		info.err = onlineErr
	case total == 0 && len(online) > 0:
		info.detail = itoa(len(online)) + " rooms online, none for games you have"
	case total == 0:
		info.detail = "No online rooms right now"
	default:
		info.detail = itoa(total) + " rooms for games you have"
	}
	rows = append(rows, info)
	for _, room := range listed {
		rows = append(rows, netplayRoomRow(room, index, installed, retroArch))
	}

	addr := netplayRow{kind: netplayRowAddress, key: "address", label: "Join by address"}
	if netplayPanel.address != "" {
		addr.detail = "Connect to " + netplayPanel.address
	} else {
		addr.detail = "Type the host's address, e.g. 192.168.1.5"
	}
	rows = append(rows, addr)

	// The highlighted row keeps its place by key; when it is gone (a room
	// closed) the cursor stays at the same height instead of jumping to the top.
	prev := netplayPanel.cursor
	netplayPanel.rows = rows
	netplayPanel.cursor = -1
	for i, r := range rows {
		if r.key == selected {
			netplayPanel.cursor = i
			break
		}
	}
	if netplayPanel.cursor < 0 {
		netplayPanel.cursor = prev
		if netplayPanel.cursor >= len(rows) {
			netplayPanel.cursor = len(rows) - 1
		}
		if netplayPanel.cursor < 0 {
			netplayPanel.cursor = 0
		}
	}
}

func netplayHostLANDetail() string {
	if address := netplayLocalAddress(); address != "" {
		return "Others on your Wi-Fi see it here, or join at " + address
	}
	return "Connect to Wi-Fi first"
}

// netplayRoomRow resolves one room into a row.
func netplayRoomRow(room netplayRoom, index map[string][]libraryGame, installed map[string]string, retroArch string) netplayRow {
	row := netplayRow{kind: netplayRowRoom, key: "room:" + room.key(), room: room}
	who := room.Nick
	if who == "" {
		who = "Someone"
	}
	row.label = who + " · " + room.Title
	plan, err := netplayPlanForRoom(room, index, installed, retroArch)
	where := "On your network"
	if room.Source == netplayRoomOnline {
		where = "Online"
		if room.Country != "" {
			where += " (" + strings.ToUpper(room.Country) + ")"
		}
		if room.Relay != "" {
			where += " · relay"
		}
	}
	if err != nil {
		row.err = err.Error()
		row.detail = where + " · " + row.err
		return row
	}
	row.plan = plan
	row.detail = where + " · " + netplayCoreLabel(plan.Core) + " · ready"
	return row
}

// handleNetplayKey consumes every key while the panel is open, so the library
// underneath never reacts behind it. The on-screen keyboard is handled earlier
// in the event loop, so typing an address never reaches this.
func handleNetplayKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !netplayPanel.open {
		return false
	}
	// Space on the Join row opens the address keyboard: its space must not
	// be typed into the address (vkopenertext.go).
	wasOpen := virtualKeyboardActive
	defer vkDropOpenerText(e, wasOpen)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		netplayMove(-1)
	case sdl.K_DOWN:
		netplayMove(1)
	case sdl.K_LEFT:
		netplaySide(config, -1)
	case sdl.K_RIGHT:
		netplaySide(config, 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		netplayActivate(config)
	case sdl.K_x, sdl.K_e:
		netplayEditAddress()
	case sdl.K_y, sdl.K_r:
		netplayRefreshOnline()
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
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		netplaySide(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		netplaySide(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		netplayActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		netplayEditAddress()
	case sdl.CONTROLLER_BUTTON_Y:
		netplayRefreshOnline()
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
			// A click on the highlighted row runs it; on another row it
			// highlights first, so a stray click never starts a game.
			if hit.index == netplayPanel.cursor {
				netplayActivate(appConfig)
				return true
			}
			netplayPanel.cursor = hit.index
			netplayPanel.status = ""
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
	n := len(netplayPanel.rows)
	if n == 0 {
		return
	}
	netplayPanel.cursor = ((netplayPanel.cursor+delta)%n + n) % n
	netplayPanel.status = ""
	PlayNavSound()
}

// netplaySide is Left/Right: on the online-host row it picks the relay.
func netplaySide(config *Config, delta int) {
	row, ok := netplayCurrentRow()
	if !ok || row.kind != netplayRowHostOnline {
		return
	}
	relay := netplayCycleRelay(config, delta)
	netplaySetStatus("Relay: "+relay.Label+" — pick the one nearest to both players", false)
	netplayRebuildRows(true)
	PlayNavSound()
}

func netplayCurrentRow() (netplayRow, bool) {
	if netplayPanel.cursor < 0 || netplayPanel.cursor >= len(netplayPanel.rows) {
		return netplayRow{}, false
	}
	return netplayPanel.rows[netplayPanel.cursor], true
}

func netplayEditAddress() {
	if row, ok := netplayCurrentRow(); ok && row.kind == netplayRowAddress {
		vkOpenNetplay()
	}
}

func netplayRefreshOnline() {
	netplayRefreshLobby()
	netplayPanel.status = ""
	netplayRebuildRows(true)
	PlayNavSound()
}

// netplayActivate runs the highlighted row.
func netplayActivate(config *Config) {
	row, ok := netplayCurrentRow()
	if !ok {
		return
	}
	switch row.kind {
	case netplayRowHostLAN:
		netplayStartHost(config, false)
	case netplayRowHostOnline:
		netplayStartHost(config, true)
	case netplayRowRoom:
		netplayStartRoom(row, config)
	case netplayRowOnlineInfo:
		netplayRefreshOnline()
	case netplayRowAddress:
		if strings.TrimSpace(netplayPanel.address) == "" {
			vkOpenNetplay()
			return
		}
		netplayStartJoin(netplayPanel.address, config)
	}
}

func netplayStartHost(config *Config, online bool) {
	var plan netplayPlan
	var err error
	if online {
		// The relay needs internet, not a LAN address to show.
		plan, err = netplayPlanForOnlineHost(netplayPanel.game)
	} else {
		plan, err = netplayPlanFor(netplayHost, netplayPanel.game, "")
	}
	if err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	relay := netplayRelayFor(config)
	plan.Session.Online = online
	plan.Session.RelayServer = relay.ID
	address := plan.Session.address()
	if err := netplayLaunch(plan, config); err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	title := plan.Session.Game.Label()
	closeNetplayPanel()
	if online {
		ShowToast("Hosting "+title+" online as "+plan.Nick+" — friends find it under Online rooms or in RetroArch's Netplay lobby", ToastKindInfo)
		return
	}
	ShowToast("Hosting "+title+" — others on your Wi-Fi see it in Netplay, or join at "+address, ToastKindInfo)
}

// netplayPlanForOnlineHost is netplayPlanFor without the LAN address check.
func netplayPlanForOnlineHost(game libraryGame) (netplayPlan, error) {
	plan, err := netplayPlanFor(netplayJoin, game, "127.0.0.1")
	if err != nil {
		return netplayPlan{}, err
	}
	plan.Session.Role = netplayHost
	plan.Session.Host = ""
	plan.Session.Port = netplayPort()
	return plan, nil
}

func netplayStartRoom(row netplayRow, config *Config) {
	if row.err != "" {
		netplaySetStatus(row.err, true)
		return
	}
	plan := row.plan
	if err := netplayLaunch(plan, config); err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	closeNetplayPanel()
	who := row.room.Nick
	if who == "" {
		who = "the host"
	}
	ShowToast("Joining "+who+" in "+plan.Session.Game.Label(), ToastKindInfo)
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
	ShowToast("Joining "+plan.Session.Game.Label()+" at "+plan.Session.address(), ToastKindInfo)
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

// renderNetplayPanel draws the panel over the blurred library behind it.
func renderNetplayPanel(renderer *sdl.Renderer, config *Config) {
	if !netplayPanel.open || renderer == nil {
		return
	}
	// Scans land every few seconds; the row list follows at most twice a
	// second so a busy LAN cannot make the panel rebuild every frame.
	if now := time.Now(); now.Sub(netplayPanel.lastRows) > 500*time.Millisecond {
		netplayPanel.lastRows = now
		netplayRebuildRows(false)
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

	const pad = OverlayPadX
	// The rows carry a title and a detail line; 76px leaves the detail clear of
	// the rounded border at both the 34px and the scaled-up medium font.
	rowH := int32(76)
	if minH := medH + smallH + 22; rowH < minH {
		rowH = minH
	}
	rowGap := int32(10)
	subtitle := netplayPanel.game.Label()
	if system := strings.TrimSpace(netplayPanel.game.System); system != "" {
		subtitle += " · " + system
	}
	header := UIPanelHeader("Netplay", subtitle, "tools")
	headerH := UIPanelHeaderBand(config, header, 14)
	// Address line and the readiness line.
	infoH := int32(14) + medH + int32(6) + smallH + int32(14)
	footerH := int32(14) + smallH

	panelW := int32(860)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	rows := netplayPanel.rows
	fixedH := headerH + infoH + footerH + int32(24)
	maxH := screenHeight - 24
	visible := int((maxH - fixedH) / (rowH + rowGap))
	if visible < 1 {
		visible = 1
	}
	if visible > len(rows) {
		visible = len(rows)
	}
	panelH := fixedH + int32(visible)*(rowH+rowGap)
	if panelH > maxH {
		panelH = maxH
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	netplayPanel.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// Keep the highlighted row inside the visible window.
	if netplayPanel.cursor < netplayPanel.scroll {
		netplayPanel.scroll = netplayPanel.cursor
	}
	if netplayPanel.cursor >= netplayPanel.scroll+visible {
		netplayPanel.scroll = netplayPanel.cursor - visible + 1
	}
	if maxScroll := len(rows) - visible; netplayPanel.scroll > maxScroll {
		netplayPanel.scroll = maxScroll
	}
	if netplayPanel.scroll < 0 {
		netplayPanel.scroll = 0
	}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

	focusColor := installHighlight()
	rowY := panelY + headerH
	listW := panelW - 2*pad - 14
	netplayPanel.rowHits = netplayPanel.rowHits[:0]
	for vi := 0; vi < visible; vi++ {
		i := netplayPanel.scroll + vi
		if i >= len(rows) {
			break
		}
		row := rows[i]
		rect := sdl.Rect{X: panelX + pad, Y: rowY + int32(vi)*(rowH+rowGap), W: listW, H: rowH}
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
		labelColor := ColorTextPrimary()
		detailColor := ColorTextTertiary()
		if row.kind == netplayRowRoom && row.err != "" {
			labelColor = ColorTextSecondary()
			detailColor = WarningInk()
		}
		label := row.label
		if row.kind == netplayRowHostOnline {
			label += " · " + netplayRelayFor(config).Label
		}
		renderText(renderer, config, medium, ellipsize(medium, label, rect.W-32), labelColor, rect.X+16, rect.Y+8)
		renderText(renderer, config, font, ellipsize(font, row.detail, rect.W-32),
			detailColor, rect.X+16, rect.Y+8+medH+2)
	}
	if len(rows) > visible && visible > 0 {
		trackH := int32(visible)*(rowH+rowGap) - rowGap
		drawScrollbar(renderer, panelX+panelW-pad-8, rowY, 6, trackH,
			float64(visible)/float64(len(rows)),
			float64(netplayPanel.scroll)/float64(len(rows)-visible))
	}

	infoY := rowY + int32(visible)*(rowH+rowGap) + int32(4)
	if address := netplayLocalAddress(); address != "" {
		renderText(renderer, config, medium, "This device: "+address, ColorTextAccent(), panelX+pad, infoY)
	} else {
		renderText(renderer, config, medium, "No Wi-Fi address — connect to a network first",
			WarningInk(), panelX+pad, infoY)
	}
	readyColor := ColorTextTertiary()
	readyText := netplayPanel.ready + "  ·  both players need the same ROM"
	if !netplayPanel.readyOK {
		readyColor = WarningInk()
		readyText = netplayPanel.ready
	}
	renderText(renderer, config, font, ellipsize(font, readyText, panelW-2*pad),
		readyColor, panelX+pad, infoY+medH+6)

	hint := hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Start"},
		FooterHint{"Y", "Refresh online"}, FooterHint{"B", "Close"})
	if row, ok := netplayCurrentRow(); ok {
		switch row.kind {
		case netplayRowHostOnline:
			hint = hintLine(FooterHint{"←/→", "Relay"}, FooterHint{"A", "Host"}, FooterHint{"B", "Close"})
		case netplayRowAddress:
			hint = hintLine(FooterHint{"A", "Join"}, FooterHint{"X", "Edit address"}, FooterHint{"B", "Close"})
		case netplayRowOnlineInfo:
			hint = hintLine(FooterHint{"A", "Refresh"}, FooterHint{"B", "Close"})
		case netplayRowRoom:
			hint = hintLine(FooterHint{"A", "Join"}, FooterHint{"Y", "Refresh online"}, FooterHint{"B", "Close"})
		}
	}
	hintColor := ColorTextSecondary()
	if netplayPanel.status != "" {
		hint = netplayPanel.status
		if netplayPanel.warn {
			hintColor = WarningInk()
		}
	}
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*pad),
		hintColor, panelX+pad, panelY+panelH-footerH)
}
