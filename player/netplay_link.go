package main

// netplay_link.go — the link cable and the wireless adapter over Wi-Fi, and
// netplay for more than two players.
//
// Game Boy link cable. RetroArch netplay runs ONE game on both handhelds; a
// Pokémon trade needs two: Red on one, Blue on the other. Gambatte has its own
// network link cable for that: one side's core is a "Network Server" on a TCP
// port (56400 by default, 56400–56420 allowed), the other a "Network Client"
// given the server's IPv4 address, and each runs whatever Game Boy game it
// likes. The options (gambatte's libretro_core_options.h; libretro docs):
//
//	gambatte_gb_link_mode                 "Not Connected" | "Network Server" | "Network Client"
//	gambatte_gb_link_network_port         "56400" … "56420"
//	gambatte_gb_link_network_server_ip_1 … _12
//	                                      the server's address as 12 digits, each
//	                                      octet padded to three ("192.168.001.005")
//	gambatte_gb_link_network_server_ip_octet1 … 4
//	                                      the same as four numbers, for builds from
//	                                      before the digit options (commit 2a474b3)
//	gambatte_show_gb_link_settings        "enabled": the options show in the Quick Menu
//
// Both spellings of the address are written; a core ignores the keys it does
// not have. They are set for that one launch through raScopeApply
// (racfgscope.go) in the options file RetroArch reads for the game
// (raTuneCoreOptionFile, ratuning.go), and go back when RetroArch exits. The
// link launch is an ordinary library launch with the Gambatte core as the
// launcher (launchLibraryGame), so the save download, the save ledger, Game
// Ready and every other launch hook run as for any game. A console whose games
// usually run in another Game Boy core uses Gambatte for that session only.
// Gambatte's server looks for its partner every few seconds, so the link can
// take up to half a minute to come up once both games are in the link room.
//
// GBA wireless adapter. gpSP emulates the wireless adapter (RFU) over
// RetroArch's netplay "netpacket" interface (RetroArch 1.17 or newer), which
// passes packets instead of syncing one game, so each side again runs its own
// game: FireRed and LeafGreen trade in the Union Room. gpSP's option
// gpsp_serial ("auto", "disabled", "rfu", "mul_poke", "mul_aw1", "mul_aw2")
// is set to "rfu" for that launch, and RetroArch is started in netplay mode
// with gpSP whatever core the console usually uses (mGBA on most cards).
//
// More players. RetroArch hosts up to netplay_max_connections clients (3 =
// four players, its default) unless a user's retroarch.cfg says fewer; the
// host overlay now says 3 and that clients play rather than watch. A joiner can
// ask for a player slot (netplay_request_device_pN, Left/Right on a room or
// the address row) for a console with more than two pads; with no choice
// RetroArch gives the first free one. While a LAN host plays, the number of
// players connected is counted from the kernel's TCP table and shown on
// RetroArch's message line when it changes.
//
// Saves. A link or netplay session starts from the library, so the last game's
// saves are already written; a link launch goes through launchLibraryGame
// (cloud saves come down first, the save ledger watches it, RetroArch flushes
// battery saves every saveFlushSeconds). A RetroArch netplay launch
// (netplayLaunch) gets a filesystem sync started before RetroArch, and a GBA
// wireless session the same periodic battery-save flush, so a trade saved in
// the game is on the card even if the session ends badly.
//
// A USB-C cable between two handhelds cannot carry any of this: the stock
// TrimUI kernels have no USB networking gadget, so there is no link to run TCP
// over. Wi-Fi or the hotspot (hotspot.go) is the cable.
//
// None of this was run on two devices from this desk (backlog NETLINK-HW-1).

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// netplayLinkGB is Gambatte's network link cable; netplayLinkGBA is gpSP's
	// wireless adapter over RetroArch netplay.
	netplayLinkGB  = "gb"
	netplayLinkGBA = "gba"

	// netplayLinkGBPort is Gambatte's default link port; its option only
	// offers 56400 to 56420.
	netplayLinkGBPort    = 56400
	netplayLinkGBPortMax = 56420

	// netplayLinkPendingTTL bounds how long a link request waits for its
	// launch (a save download or Game Ready can hold it a while).
	netplayLinkPendingTTL = 3 * time.Minute

	// netplayLinkMaxPlayers is the most players the panel offers a slot for.
	netplayLinkMaxPlayers = 4

	// The cores, as netplayCoreNorm folds their file names.
	netplayLinkGambatte = "gambatte"
	netplayLinkGPSP     = "gpsp"
)

// netplayLinkOpts rides on a netplayPlan (netplay.go).
type netplayLinkOpts struct {
	// Mode is netplayLinkGBA for a wireless-adapter session, "" otherwise.
	Mode string
	// Slot is the player a joiner asks for (2–4); 0 is the first free one.
	Slot int
}

// netplayLinkModeFor is the link a console has: Gambatte's cable for the Game
// Boy and Game Boy Color, gpSP's wireless adapter for the GBA, "" otherwise.
func netplayLinkModeFor(system string) string {
	set, ok := netplayCoreSetFor(system)
	if !ok || set.unsupported != "" {
		return ""
	}
	switch set.label {
	case "Game Boy":
		return netplayLinkGB
	case "Game Boy Advance":
		return netplayLinkGBA
	}
	return ""
}

// netplayLinkCoreFor is the core a link mode needs.
func netplayLinkCoreFor(mode string) string {
	switch mode {
	case netplayLinkGB:
		return netplayLinkGambatte
	case netplayLinkGBA:
		return netplayLinkGPSP
	}
	return ""
}

// netplayMaxPlayersFor is how many players a console's netplay can seat, and
// whether the games need a multitap for more than two. Pure.
func netplayMaxPlayersFor(system string) (int, bool) {
	set, ok := netplayCoreSetFor(system)
	if !ok || set.unsupported != "" {
		return 2, false
	}
	switch set.label {
	case "Nintendo 64", "Dreamcast", "Arcade":
		return netplayLinkMaxPlayers, false
	case "SNES", "NES", "Mega Drive / Genesis", "PlayStation", "PC Engine", "Saturn":
		return netplayLinkMaxPlayers, true
	}
	return 2, false
}

// netplayPlayersNote is the start of the LAN host row's line for a console
// with more than two pads ("" for the rest).
func netplayPlayersNote(system string) string {
	seats, multitap := netplayMaxPlayersFor(system)
	switch {
	case seats <= 2:
		return ""
	case multitap:
		return "Up to " + strconv.Itoa(seats) + " players in multitap games · "
	}
	return "Up to " + strconv.Itoa(seats) + " players · "
}

// ── Core options (pure) ──────────────────────────────────────────────────

// netplayLinkGBServerOptions are Gambatte's options for the side that waits.
func netplayLinkGBServerOptions(port int) []raScopeValue {
	return []raScopeValue{
		{Key: "gambatte_gb_link_mode", Value: "Network Server"},
		{Key: "gambatte_gb_link_network_port", Value: strconv.Itoa(port)},
		{Key: "gambatte_show_gb_link_settings", Value: "enabled"},
	}
}

// netplayLinkGBClientOptions are Gambatte's options for the side that
// connects to the server at ip (IPv4 only: the core takes four numbers).
func netplayLinkGBClientOptions(ip net.IP, port int) ([]raScopeValue, error) {
	v4 := ip.To4()
	if v4 == nil {
		return nil, errors.New("the link cable needs the host's IPv4 address, like 192.168.1.5")
	}
	values := []raScopeValue{
		{Key: "gambatte_gb_link_mode", Value: "Network Client"},
		{Key: "gambatte_gb_link_network_port", Value: strconv.Itoa(port)},
		{Key: "gambatte_show_gb_link_settings", Value: "enabled"},
	}
	for i, b := range v4 {
		digits := fmt.Sprintf("%03d", b)
		for j := 0; j < 3; j++ {
			values = append(values, raScopeValue{
				Key:   "gambatte_gb_link_network_server_ip_" + strconv.Itoa(i*3+j+1),
				Value: digits[j : j+1],
			})
		}
	}
	for i, b := range v4 {
		values = append(values, raScopeValue{
			Key:   "gambatte_gb_link_network_server_ip_octet" + strconv.Itoa(i+1),
			Value: strconv.Itoa(int(b)),
		})
	}
	return values, nil
}

// netplayLinkGBAOptions are gpSP's options for a wireless-adapter session.
func netplayLinkGBAOptions() []raScopeValue {
	return []raScopeValue{{Key: "gpsp_serial", Value: "rfu"}}
}

// netplayLinkSlotValues ask RetroArch for one player slot as a joiner: that
// device requested, the other three not. Empty for slot 0 (first free).
func netplayLinkSlotValues(slot int) []raScopeValue {
	if slot < 2 || slot > netplayLinkMaxPlayers {
		return nil
	}
	values := make([]raScopeValue, 0, netplayLinkMaxPlayers)
	for i := 1; i <= netplayLinkMaxPlayers; i++ {
		v := "false"
		if i == slot {
			v = "true"
		}
		values = append(values, raScopeValue{Key: "netplay_request_device_p" + strconv.Itoa(i), Value: v})
	}
	return values
}

// netplayLinkHostOverlay is added to the netplay host's --appendconfig
// (netplay_rooms.go): room for three clients (four players, RetroArch's own
// default, in case retroarch.cfg says fewer) who play rather than watch.
func netplayLinkHostOverlay() string {
	return "netplay_max_connections = \"" + strconv.Itoa(netplayLinkMaxPlayers-1) + "\"\n" +
		"netplay_start_as_spectator = \"false\"\n"
}

// ── Options files ────────────────────────────────────────────────────────

func init() {
	// The option folders (library_name) of the two link cores, for
	// raTuneCoreOptionFile.
	for core, folder := range map[string]string{netplayLinkGambatte: "Gambatte", netplayLinkGPSP: "gpSP"} {
		if len(raTuneCoreFolders[core]) == 0 {
			raTuneCoreFolders[core] = []string{folder}
		}
	}
	gameLaunchRegisterPrepare(netplayLinkPrepare)
	gameLaunchRegisterStarted(netplayLinkStarted)
	gameLaunchRegisterExited(netplayLinkExited)
}

// netplayLinkOptionFile is the options file RetroArch reads for core when it
// plays romPath with the config at cfgPath. A core that never ran here has
// none yet: an empty one is made where RetroArch would make it (the shared
// file, or the core's own per global_core_options), so the session's options
// have a file to live in; RetroArch fills in the rest when it exits.
func netplayLinkOptionFile(cfgPath, core, romPath string) string {
	if p := raTuneCoreOptionFile(cfgPath, core, romPath); p != "" {
		return p
	}
	folders := raTuneCoreFolders[core]
	if len(folders) == 0 {
		return ""
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	cfg := string(raw)
	path := ""
	if g, has := raConfigGet(cfg, "global_core_options"); !has || raValueEqual(g, "true") {
		path = filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg")
		if v, ok := raConfigGet(cfg, "core_options_path"); ok {
			if p := videoFilterExpandDir(v, cfgPath); p != "" {
				path = p
			}
		}
	} else {
		path = filepath.Join(raTuneMenuConfigDir(cfg, cfgPath), folders[0], folders[0]+".opt")
	}
	if regularFile(path) {
		return path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ""
	}
	if err := AtomicWrite(path, []byte{}, 0o644); err != nil {
		return ""
	}
	logInfo("netplay", "link: made an empty options file %s for %s's first run", path, core)
	return path
}

// netplayLinkApplyCoreOptions sets values in core's options for one launch.
func netplayLinkApplyCoreOptions(candidates []string, core, romPath string, values []raScopeValue) error {
	targets := raScopeTargets(candidates)
	if len(targets) == 0 {
		return errors.New("no retroarch.cfg found")
	}
	opt := netplayLinkOptionFile(targets[0], core, romPath)
	if opt == "" {
		return fmt.Errorf("no options file for %s", core)
	}
	if _, err := raScopeApply(opt, values); err != nil {
		return err
	}
	logInfo("netplay", "link options for this launch in %s", opt)
	return nil
}

// netplayLinkConfigCandidates are the configs of the RetroArch netplay
// starts: the ones beside its binary first (its HOME on the handheld).
func netplayLinkConfigCandidates(retroArch string) []string {
	var out []string
	if strings.TrimSpace(retroArch) != "" {
		out = raConfigsIn(filepath.Dir(retroArch))
	}
	return append(out, retroArchConfigCandidates()...)
}

// ── The link cable launch (a library launch with Gambatte) ───────────────

// netplayLinkLaunch is one requested Game Boy link session.
type netplayLinkLaunch struct {
	path   string // the ROM
	core   string // the Gambatte core file it must start with
	server bool
	host   string // client: the server's IPv4 address
	port   int
	at     time.Time
	active bool // the options are set and the game is starting or running
	beacon *netplayLinkBeacon
}

// netplayLinkRun is the current link session: written by the panel and the
// prepare/started hooks on the main thread, ended on the reaper goroutine.
var netplayLinkRun struct {
	mu  sync.Mutex
	cur netplayLinkLaunch
}

func netplayLinkSetPending(l netplayLinkLaunch) {
	netplayLinkRun.mu.Lock()
	old := netplayLinkRun.cur
	netplayLinkRun.cur = l
	netplayLinkRun.mu.Unlock()
	old.beacon.Stop()
}

func netplayLinkClear() {
	netplayLinkRun.mu.Lock()
	old := netplayLinkRun.cur
	netplayLinkRun.cur = netplayLinkLaunch{}
	netplayLinkRun.mu.Unlock()
	old.beacon.Stop()
}

// netplayLinkPrepare is the launch hook: the link session's Gambatte options
// for the launch the panel asked for, and only that one. Main thread.
func netplayLinkPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("netplay", "preparing the link cable failed: %v", r)
		}
	}()
	netplayLinkRun.mu.Lock()
	cur := netplayLinkRun.cur
	if cur.active {
		// A new launch: the earlier session is over whatever its exit said.
		cur = netplayLinkLaunch{path: cur.path, beacon: cur.beacon}
		netplayLinkRun.cur = netplayLinkLaunch{}
	}
	netplayLinkRun.mu.Unlock()
	if cur.beacon != nil {
		cur.beacon.Stop()
		cur.beacon = nil
	}
	if cur.core == "" || !sameFilePath(cur.path, game.Path) || !sameFilePath(cur.core, launcher) {
		return
	}
	if time.Since(cur.at) > netplayLinkPendingTTL {
		netplayLinkClear()
		return
	}
	var values []raScopeValue
	if cur.server {
		values = netplayLinkGBServerOptions(cur.port)
	} else {
		v, err := netplayLinkGBClientOptions(net.ParseIP(cur.host), cur.port)
		if err != nil {
			logWarn("netplay", "link: %v", err)
			netplayLinkClear()
			return
		}
		values = v
	}
	if err := netplayLinkApplyCoreOptions(raLaunchCandidates(launcher, raTuneCandidates()), netplayLinkGambatte, game.Path, values); err != nil {
		logWarn("netplay", "link: cannot set Gambatte's link options: %v; %s starts unlinked", err, filepath.Base(game.Path))
		netplayLinkClear()
		return
	}
	netplayLinkRun.mu.Lock()
	if sameFilePath(netplayLinkRun.cur.path, game.Path) {
		netplayLinkRun.cur.active = true
	}
	netplayLinkRun.mu.Unlock()
	role := "client of " + cur.host
	if cur.server {
		role = "server"
	}
	logInfo("netplay", "link cable for %s: %s on port %d", filepath.Base(game.Path), role, cur.port)
}

// netplayLinkStarted is the launch hook after the start: a link server
// answers the link query while its game runs. A beacon that cannot bind only
// costs the listing: the address the panel showed still works. Main thread.
func netplayLinkStarted(game libraryGame, _ string, _ *exec.Cmd) {
	netplayLinkRun.mu.Lock()
	cur := netplayLinkRun.cur
	netplayLinkRun.mu.Unlock()
	if !cur.active || !cur.server || cur.beacon != nil || !sameFilePath(cur.path, game.Path) {
		return
	}
	beacon, err := startNetplayLinkBeacon(netplayLinkBeaconInfo{
		Mode:   netplayLinkGB,
		Nick:   netplayNickname(),
		Title:  netplayCleanText(game.Label(), 80),
		System: netplayCleanText(game.System, 40),
		Port:   cur.port,
	})
	if err != nil {
		logInfo("netplay", "link: not listed on the network (%v); join by address still works", err)
		return
	}
	netplayLinkRun.mu.Lock()
	if netplayLinkRun.cur.active && sameFilePath(netplayLinkRun.cur.path, game.Path) && netplayLinkRun.cur.beacon == nil {
		netplayLinkRun.cur.beacon = beacon
		beacon = nil
	}
	netplayLinkRun.mu.Unlock()
	beacon.Stop() // nil unless the session ended meanwhile
}

// netplayLinkExited ends the session: the beacon stops and the request goes.
// The options go back by themselves (racfgscope.go). Reaper goroutine.
func netplayLinkExited(game libraryGame, _ string, _ time.Duration, _ error) {
	netplayLinkRun.mu.Lock()
	cur := netplayLinkRun.cur
	if !cur.active || !sameFilePath(cur.path, game.Path) {
		netplayLinkRun.mu.Unlock()
		return
	}
	netplayLinkRun.cur = netplayLinkLaunch{}
	netplayLinkRun.mu.Unlock()
	cur.beacon.Stop()
}

// ── RetroArch netplay sessions (netplayLaunch) ───────────────────────────

// netplayLinkBeforeLaunch is netplayLaunch's last step before RetroArch
// starts: a disk sync, the joiner's player slot, gpSP's wireless adapter and
// the periodic battery-save flush for it, and the player count for a LAN host.
// Every value is for this launch only (racfgscope.go). Never blocks or fails
// the launch. Main thread.
func netplayLinkBeforeLaunch(plan *netplayPlan) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("netplay", "preparing netplay failed: %v", r)
		}
	}()
	if plan == nil {
		return
	}
	// Whatever the last game wrote is on the card before a session that
	// may end in a crash or a dead battery (off this thread: sync can take
	// a moment).
	go bootSyncFilesystems()

	session := plan.Session
	seats, _ := netplayMaxPlayersFor(session.Game.System)
	if session.Role == netplayJoin && plan.Link.Slot == 0 && netplayPanel.open && seats > 2 {
		plan.Link.Slot = netplayLinkPanel.slot
	}
	var values []raScopeValue
	if session.Role == netplayJoin {
		values = append(values, raScopeValue{Key: "netplay_start_as_spectator", Value: "false"})
		values = append(values, netplayLinkSlotValues(plan.Link.Slot)...)
	}
	if plan.Link.Mode == netplayLinkGBA {
		if v, ok := savestateSRAMFlushValue(); ok {
			values = append(values, v)
		}
	}
	countPlayers := session.Role == netplayHost && !session.Online && seats > 2
	if countPlayers {
		values = append(values, raScopeValue{Key: "network_cmd_enable", Value: "true"})
	}
	candidates := netplayLinkConfigCandidates(plan.RetroArch)
	targets := raScopeTargets(candidates)
	cmdPort := 0
	for _, path := range targets {
		if len(values) > 0 {
			if _, err := raScopeApply(path, values); err != nil {
				logWarn("netplay", "cannot set this session's netplay settings in %s: %v", path, err)
				continue
			}
		}
		if cmdPort == 0 {
			if raw, err := os.ReadFile(path); err == nil {
				cmdPort = networkCmdPort(string(raw))
			}
		}
	}
	if plan.Link.Mode == netplayLinkGBA {
		if err := netplayLinkApplyCoreOptions(candidates, netplayLinkGPSP, session.Game.Path, netplayLinkGBAOptions()); err != nil {
			logWarn("netplay", "wireless adapter: cannot set gpSP's link option: %v", err)
		}
	}
	if countPlayers && cmdPort > 0 {
		go netplayLinkWatchPlayers(cmdPort, session.Port, seats)
	}
}

// netplayLinkWatchPlayers says on RetroArch's message line how many players a
// LAN host has whenever that changes, until the game is gone.
func netplayLinkWatchPlayers(cmdPort, netPort, seats int) {
	time.Sleep(6 * time.Second)
	last := 0
	for gameScreenSuspended() {
		if n := netplayLinkPeers(netPort); n >= 0 && n != last {
			msg := "Netplay: " + strconv.Itoa(n+1) + " of " + strconv.Itoa(seats) + " players here"
			if n+1 > seats {
				msg = "Netplay: " + strconv.Itoa(n+1) + " connected (" + strconv.Itoa(seats) + " can play, the rest watch)"
			}
			_ = sendRetroArchNetCmd(cmdPort, "SHOW_MSG "+msg)
			last = n
		}
		time.Sleep(3 * time.Second)
	}
}

// netplayLinkPeers counts the clients connected to this device's netplay
// port, -1 where the kernel's TCP table cannot be read (Windows).
func netplayLinkPeers(port int) int {
	total, read := 0, false
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		read = true
		total += netplayLinkCountPeers(string(raw), port)
	}
	if !read {
		return -1
	}
	return total
}

// netplayLinkCountPeers counts the established connections whose local port
// is port in a /proc/net/tcp table ("sl local rem st …", addresses as
// HEXIP:HEXPORT, state 01 = established). Pure.
func netplayLinkCountPeers(table string, port int) int {
	seen := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "01" {
			continue
		}
		local := fields[1]
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		p, err := strconv.ParseUint(local[i+1:], 16, 32)
		if err != nil || int(p) != port {
			continue
		}
		seen[fields[2]] = true
	}
	return len(seen)
}

// netplayLinkBeaconUntilExit keeps a wireless host's link beacon up while its
// RetroArch runs (netplayLaunch has no exit hook).
func netplayLinkBeaconUntilExit(b *netplayLinkBeacon) {
	if b == nil {
		return
	}
	go func() {
		time.Sleep(5 * time.Second)
		deadline := time.Now().Add(raScopeWaitMax)
		for gameScreenSuspended() && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
		}
		b.Stop()
	}()
}
