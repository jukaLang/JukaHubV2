package main

// netplay_link_ui.go — the link rows of the Netplay panel (netplay_ui.go).
//
// On a Game Boy or Game Boy Color game, when the Gambatte core is installed:
//
//   - "Link cable · host": this handheld's game waits for a partner on the
//     network; the address is on the row and the session is listed for other
//     JukaHubs (netplay_link_beacon.go). No Wi-Fi: A opens the Hotspot screen.
//   - one row per link cable heard on the network: A joins it with THIS game
//     (the panel's), whatever the host plays — Red trades with Blue;
//   - "Link cable · join by address" (X types the address).
//
// On a GBA game, when gpSP is installed, the same three rows for the wireless
// adapter, which runs over RetroArch netplay with gpSP for that session.
//
// On a console with more than two pads, Left/Right on a room or on "Join by
// address" picks the player slot asked for (Auto, 2, 3, 4).

import (
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	netplayRowLinkHost netplayRowKind = 40 + iota
	netplayRowLinkRoom
	netplayRowLinkAddress
)

// netplayLinkPanel is what the panel keeps for the link rows, resolved when
// it opens (core folders are not rescanned per frame).
var netplayLinkPanel struct {
	game  libraryGame
	mode  string // netplayLinkGB, netplayLinkGBA or ""
	core  string // the link core's file, "" when it is not installed
	usual string // the core the console's games usually start in
	slot  int    // the player a joiner asks for; 0 = first free
}

// netplayLinkPanelOpened is called by openNetplayPanel.
func netplayLinkPanelOpened(game libraryGame) {
	netplayLinkPanel.game = game
	netplayLinkPanel.slot = 0
	netplayLinkPanel.mode = netplayLinkModeFor(game.System)
	netplayLinkPanel.core, netplayLinkPanel.usual = "", ""
	if netplayLinkPanel.mode == "" {
		return
	}
	installed := netplayInstalledCores(netplayCoreDirs())
	netplayLinkPanel.core = installed[netplayLinkCoreFor(netplayLinkPanel.mode)]
	if netplayLinkPanel.core == "" {
		return
	}
	netplayLinkPanel.usual = gameCoreFromLauncher(resolveGameLauncherFor(game), game.System)
	netplayLinkStartDiscovery()
}

// netplayLinkPanelClosed is called by closeNetplayPanel.
func netplayLinkPanelClosed() {
	netplayLinkStopDiscovery()
}

// netplayLinkOffered reports whether the panel shows link rows.
func netplayLinkOffered() bool {
	return netplayLinkPanel.mode != "" && netplayLinkPanel.core != ""
}

// netplayLinkRows are the link rows for the panel's game (none when the
// console has no link or its core is not installed).
func netplayLinkRows() []netplayRow {
	if !netplayLinkOffered() {
		return nil
	}
	mode := netplayLinkPanel.mode
	what, hostLabel := "link cable", "Link cable · host"
	hostDetail := "Each handheld plays its own Game Boy game: trade or battle (Red with Blue, Tetris 2P)"
	if mode == netplayLinkGBA {
		what, hostLabel = "wireless adapter", "Wireless adapter (Pokémon FireRed/LeafGreen/Emerald)"
		hostDetail = "Union Room trades and battles, each with their own game · RetroArch 1.17 or newer"
		if core := normalizeCoreName(netplayLinkPanel.usual); core != "" && core != netplayLinkGPSP {
			hostDetail += " · uses gpSP for this session"
		}
	} else if core := normalizeCoreName(netplayLinkPanel.usual); core != "" && core != netplayLinkGambatte {
		hostDetail += " · uses Gambatte for this session"
	}
	if ip := localIPv4(); ip != "" {
		hostDetail += " · at " + ip
	} else {
		hostDetail = "No Wi-Fi: A opens the Hotspot, so the other handheld can join this one"
	}
	rows := []netplayRow{{kind: netplayRowLinkHost, key: "link-host", label: hostLabel, detail: hostDetail}}

	rooms, _ := netplayLinkLANSnapshot(mode)
	mine := netplayLinkPanel.game.Label()
	for _, room := range rooms {
		who := room.Nick
		if who == "" {
			who = "Someone"
		}
		row := netplayRow{
			kind:   netplayRowLinkRoom,
			key:    "link:" + room.key(),
			label:  who + " · " + room.Title + " · " + what,
			detail: "On your network · you join with " + mine,
			room:   netplayRoom{Source: netplayRoomLAN, Nick: room.Nick, Title: room.Title, System: room.System, Host: room.Host, Port: room.Port},
		}
		rows = append(rows, row)
	}

	addr := netplayRow{kind: netplayRowLinkAddress, key: "link-address", label: strings.ToUpper(what[:1]) + what[1:] + " · join by address"}
	if netplayPanel.address != "" {
		addr.detail = "Connect to " + netplayPanel.address + " with " + mine
	} else {
		addr.detail = "Type the host's address, e.g. 192.168.1.5"
	}
	return append(rows, addr)
}

// netplayLinkActivate runs a link row; false for any other row.
func netplayLinkActivate(row netplayRow, config *Config) bool {
	switch row.kind {
	case netplayRowLinkHost:
		netplayLinkHost(config)
	case netplayRowLinkRoom:
		netplayLinkJoin(row.room.Host, row.room.Port, row.room.Nick, config)
	case netplayRowLinkAddress:
		if strings.TrimSpace(netplayPanel.address) == "" {
			vkOpenNetplay()
			return true
		}
		netplayLinkJoinAddress(netplayPanel.address, config)
	default:
		return false
	}
	return true
}

// netplayLinkJoinSubmit takes the on-screen keyboard's address when it was
// typed on the link address row (the cursor stays on it while typing).
func netplayLinkJoinSubmit(text string, config *Config) bool {
	row, ok := netplayCurrentRow()
	if !ok || row.kind != netplayRowLinkAddress {
		return false
	}
	netplayLinkJoinAddress(text, config)
	return true
}

// netplayLinkNoWiFi sends a host without a network to the Hotspot screen.
func netplayLinkNoWiFi(config *Config) {
	closeNetplayPanel()
	hotspotOpenScreen(config)
	ShowToast("No Wi-Fi: turn the hotspot on, join it from the other handheld, then host the link again", ToastKindInfo)
}

// netplayLinkHost starts this game as the link's host.
func netplayLinkHost(config *Config) {
	game, core := netplayPanel.game, netplayLinkPanel.core
	ip := localIPv4()
	if ip == "" {
		netplayLinkNoWiFi(config)
		return
	}
	if netplayLinkPanel.mode == netplayLinkGBA {
		plan, err := netplayPlanFor(netplayHost, game, "")
		if err != nil {
			netplaySetStatus(err.Error(), true)
			return
		}
		plan.Core = core
		plan.Link = netplayLinkOpts{Mode: netplayLinkGBA}
		if err := netplayLaunch(plan, config); err != nil {
			netplaySetStatus(err.Error(), true)
			return
		}
		if beacon, err := startNetplayLinkBeacon(netplayLinkBeaconInfo{
			Mode:   netplayLinkGBA,
			Nick:   plan.Nick,
			Title:  netplayCleanText(game.Label(), 80),
			System: netplayCleanText(game.System, 40),
			Port:   plan.Session.Port,
		}); err == nil {
			netplayLinkBeaconUntilExit(beacon)
		}
		closeNetplayPanel()
		ShowToast("Wireless adapter open at "+plan.Session.address()+" — the other player picks your room under Wireless adapter, then both enter the Union Room", ToastKindInfo)
		return
	}
	netplayLinkSetPending(netplayLinkLaunch{path: game.Path, core: core, server: true, port: netplayLinkGBPort, at: time.Now()})
	if !netplayLinkStartGame(game, core) {
		return
	}
	ShowToast("Link cable open at "+ip+" — the other handheld picks it under Link cable; it connects within 30 s of both games reaching the link room", ToastKindInfo)
}

// netplayLinkJoinAddress joins the link at a typed address.
func netplayLinkJoinAddress(raw string, config *Config) {
	def := netplayPort()
	if netplayLinkPanel.mode == netplayLinkGB {
		def = netplayLinkGBPort
	}
	host, port, err := parseNetplayAddress(raw, def)
	if err != nil {
		netplaySetStatus(err.Error(), true)
		return
	}
	if netplayLinkPanel.mode == netplayLinkGB && !netplayLinkPortOK(netplayLinkGB, port) {
		// A netplay port typed from habit: Gambatte's link only uses
		// 56400–56420, and a host made here uses the first.
		port = netplayLinkGBPort
	}
	netplayLastAddress = host
	netplayLinkJoin(host, port, "", config)
}

// netplayLinkJoin joins the link host at host:port with the panel's game.
func netplayLinkJoin(host string, port int, who string, config *Config) {
	game, core := netplayPanel.game, netplayLinkPanel.core
	if who == "" {
		who = host
	}
	if netplayLinkPanel.mode == netplayLinkGBA {
		plan, err := netplayPlanFor(netplayJoin, game, net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			netplaySetStatus(err.Error(), true)
			return
		}
		plan.Core = core
		plan.Link = netplayLinkOpts{Mode: netplayLinkGBA}
		if err := netplayLaunch(plan, config); err != nil {
			netplaySetStatus(err.Error(), true)
			return
		}
		closeNetplayPanel()
		ShowToast("Wireless adapter: joining "+who+" with "+game.Label()+" — enter the Union Room in both games", ToastKindInfo)
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		netplaySetStatus("The link cable needs the host's IPv4 address, like 192.168.1.5", true)
		return
	}
	netplayLinkSetPending(netplayLinkLaunch{path: game.Path, core: core, host: ip.To4().String(), port: port, at: time.Now()})
	if !netplayLinkStartGame(game, core) {
		return
	}
	ShowToast("Link cable to "+who+" — enter the trade or battle room in both games; the link can take 30 s", ToastKindInfo)
}

// netplayLinkStartGame starts game in core through the library's own launch,
// so its save download, Game Ready and the save ledger run as for any game.
// false when it did not start (the reason is on the panel or on a card).
func netplayLinkStartGame(game libraryGame, core string) bool {
	go bootSyncFilesystems()
	if err := launchLibraryGame(game, core); err != nil {
		netplayLinkClear()
		netplaySetStatus(firstLine(err.Error()), true)
		return false
	}
	closeNetplayPanel()
	if gameLaunchWasHeld(game.Path) {
		ShowToast(game.Label()+" is not ready yet: its card says why", ToastKindWarning)
		return false
	}
	return true
}

// ── Player slot (more than two pads) ─────────────────────────────────────

// netplayLinkSlotRow reports whether row joins someone and the panel's
// console seats more than two players.
func netplayLinkSlotRow(row netplayRow) bool {
	if row.kind != netplayRowRoom && row.kind != netplayRowAddress {
		return false
	}
	seats, _ := netplayMaxPlayersFor(netplayPanel.game.System)
	if row.kind == netplayRowRoom && row.plan.Session.Game.System != "" {
		seats, _ = netplayMaxPlayersFor(row.plan.Session.Game.System)
	}
	return seats > 2
}

// netplayLinkSide is Left/Right on a join row: the player slot to ask for.
// false when the row has none (the panel's own Left/Right runs).
func netplayLinkSide(delta int) bool {
	row, ok := netplayCurrentRow()
	if !ok || !netplayLinkSlotRow(row) {
		return false
	}
	steps := []int{0, 2, 3, 4}
	i := 0
	for k, s := range steps {
		if s == netplayLinkPanel.slot {
			i = k
		}
	}
	netplayLinkPanel.slot = steps[((i+delta)%len(steps)+len(steps))%len(steps)]
	if netplayLinkPanel.slot == 0 {
		netplaySetStatus("Join in the first free player slot", false)
	} else {
		netplaySetStatus("Join as player "+strconv.Itoa(netplayLinkPanel.slot)+" — if it is taken, RetroArch says so", false)
	}
	PlayNavSound()
	return true
}

// netplayLinkRowSuffix is added to a join row's label when a slot is chosen.
func netplayLinkRowSuffix(row netplayRow) string {
	if netplayLinkPanel.slot == 0 || !netplayLinkSlotRow(row) {
		return ""
	}
	return " · as player " + strconv.Itoa(netplayLinkPanel.slot)
}

// netplayLinkHint is the footer for a link row or a join row with slots; ""
// leaves the panel's own.
func netplayLinkHint(row netplayRow) string {
	switch row.kind {
	case netplayRowLinkHost:
		return hintLine(FooterHint{"A", "Host"}, FooterHint{"B", "Close"})
	case netplayRowLinkRoom:
		return hintLine(FooterHint{"A", "Join with your game"}, FooterHint{"B", "Close"})
	case netplayRowLinkAddress:
		return hintLine(FooterHint{"A", "Join"}, FooterHint{"X", "Edit address"}, FooterHint{"B", "Close"})
	}
	if !netplayLinkSlotRow(row) {
		return ""
	}
	if row.kind == netplayRowAddress {
		return hintLine(FooterHint{"Left/Right", "Player"}, FooterHint{"A", "Join"}, FooterHint{"X", "Edit address"}, FooterHint{"B", "Close"})
	}
	return hintLine(FooterHint{"Left/Right", "Player"}, FooterHint{"A", "Join"}, FooterHint{"B", "Close"})
}
