package main

// netplay.go — LAN play for emulated consoles, without a link cable.
//
// Handhelds that support a link cable (Game Boy, GBA, GBC, SNES, Mega Drive…)
// can play together, but a cable needs both consoles to be in the same room and
// the right adapter. RetroArch already carries the same link-cable protocols
// over TCP, so one device hosts and the other joins over the Wi-Fi both are
// already on. That is the whole feature: pick a game, host or join, and both
// sides run the same core and the same ROM with netplay switched on.
//
// Only RetroArch is targeted, deliberately. Standalone emulators (PPSSPP,
// melonDS, DraStic…) each ship their own incompatible link emulation, so a
// generic "link over Wi-Fi" button has no protocol to speak. A console whose
// core is missing or whose link play RetroArch cannot carry is refused with a
// reason instead of being launched into a silent single-player session.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// netplayDefaultPort is RetroArch's own default netplay TCP port. Both sides
// pass it explicitly so neither depends on whatever is saved in retroarch.cfg.
const netplayDefaultPort = 55435

type netplayRole uint8

const (
	netplayIdle netplayRole = iota
	netplayHost
	netplayJoin
)

// netplaySession is the shared description of a LAN game: who is hosting, which
// game, and the TCP endpoint both sides agree on.
type netplaySession struct {
	Role netplayRole
	Game libraryGame
	Host string
	Port int
}

// address is the "ip:port" string the other player types in.
func (s netplaySession) address() string {
	if strings.TrimSpace(s.Host) == "" {
		return ""
	}
	port := s.Port
	if port <= 0 || port > 65535 {
		port = netplayDefaultPort
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(port))
}

// netplayPlan is a session with everything the launch needs resolved up front,
// so a missing emulator or core is an error before the screen goes black.
type netplayPlan struct {
	Session   netplaySession
	RetroArch string
	Core      string
	Nick      string
}

// netplayCoreSet maps a console to the libretro cores that can carry its link
// protocol, best first. Keys are normalised names (see normaliseLauncherName),
// so a folder called "GBA", "Game Boy Advance" or "Nintendo - Game Boy Advance"
// all resolve to the same set.
type netplayCoreSet struct {
	keys        []string
	label       string
	cores       []string
	unsupported string
}

var netplayCoreTable = []netplayCoreSet{
	{
		keys:  []string{"gba", "gameboyadvance", "mgba", "vba", "visualboyadvance"},
		label: "Game Boy Advance",
		cores: []string{"mgba_libretro", "vba_next_libretro", "gpsp_libretro"},
	},
	{
		keys:  []string{"gb", "gbc", "gameboy", "gameboycolor", "gambatte", "sameboy"},
		label: "Game Boy",
		cores: []string{"gambatte_libretro", "sameboy_libretro"},
	},
	{
		keys:  []string{"snes", "sfc", "supernintendo", "superfamicom", "snes9x", "bsnes"},
		label: "SNES",
		cores: []string{"snes9x_libretro", "snes9x2005_plus_libretro", "bsnes_libretro"},
	},
	{
		keys:  []string{"nes", "fc", "famicom", "fceux", "nestopia", "mesen"},
		label: "NES",
		cores: []string{"fceumm_libretro", "nestopia_libretro", "mesen_libretro", "quicknes_libretro"},
	},
	{
		keys:  []string{"md", "megadrive", "genesis", "megadrivegenesis", "genesisplusgx", "segagenesis"},
		label: "Mega Drive / Genesis",
		cores: []string{"genesis_plus_gx_libretro", "picodrive_libretro"},
	},
	{
		keys:  []string{"gg", "gamegear"},
		label: "Game Gear",
		cores: []string{"genesis_plus_gx_libretro"},
	},
	{
		keys:  []string{"n64", "nintendo64", "mupen64plus", "mupen64"},
		label: "Nintendo 64",
		cores: []string{"mupen64plus_next_libretro", "parallel_n64_libretro"},
	},
	{
		keys:  []string{"psx", "ps", "ps1", "playstation", "pcsx", "pcsxrearmed"},
		label: "PlayStation",
		cores: []string{"pcsx_rearmed_libretro", "swanstation_libretro", "mednafen_psx_libretro", "beetle_psx_libretro"},
	},
	{
		keys:  []string{"saturn", "sat", "segasaturn", "yabause", "kronos"},
		label: "Saturn",
		cores: []string{"yabause_libretro", "kronos_libretro", "mednafen_saturn_libretro"},
	},
	{
		keys:  []string{"dreamcast", "dc", "flycast"},
		label: "Dreamcast",
		cores: []string{"flycast_libretro"},
	},
	{
		keys:  []string{"arcade", "mame", "fbneo", "fba", "neogeo"},
		label: "Arcade",
		cores: []string{"fbneo_libretro", "mame2003_plus_libretro", "mame2010_libretro"},
	},
	{
		keys:  []string{"pce", "pcengine", "turbografx", "turbografx16", "pcecd"},
		label: "PC Engine",
		cores: []string{"mednafen_pce_libretro", "mednafen_pce_fast_libretro", "beetle_pce_fast_libretro"},
	},
	{
		keys:  []string{"atari2600", "a2600", "2600", "stella"},
		label: "Atari 2600",
		cores: []string{"stella_libretro", "stella2014_libretro"},
	},
	{
		keys:        []string{"psp", "playstationportable", "ppsspp"},
		label:       "PlayStation Portable",
		unsupported: "PSP link play lives inside PPSSPP's own Ad-Hoc mode, not RetroArch netplay",
	},
	{
		keys:        []string{"nds", "nintendods", "ds", "melonds", "desmume", "drastic"},
		label:       "Nintendo DS",
		unsupported: "DS download play needs melonDS's own multiplayer; install melonDS and use its local mode",
	},
}

var (
	// Test seams, matching the *_override variables used elsewhere.
	netplayRetroArchOverride string
	netplayCoreDirsOverride  []string
	netplayPortOverride      int
	netplayListenProbe       = netplayCanListen
	netplayReachProbe        = netplayReachable
)

// netplayCoreSetFor finds the console table entry for a library system name.
// Exact matches win; a longer key inside the name is the fallback so vendor
// prefixed folder names still resolve.
func netplayCoreSetFor(system string) (netplayCoreSet, bool) {
	name := normaliseLauncherName(system)
	if name == "" {
		return netplayCoreSet{}, false
	}
	for _, set := range netplayCoreTable {
		for _, key := range set.keys {
			if key == name {
				return set, true
			}
		}
	}
	best := -1
	bestLen := 0
	for i, set := range netplayCoreTable {
		for _, key := range set.keys {
			if len(key) < 4 || !strings.Contains(name, key) {
				continue
			}
			if len(key) > bestLen {
				best, bestLen = i, len(key)
			}
		}
	}
	if best >= 0 {
		return netplayCoreTable[best], true
	}
	return netplayCoreSet{}, false
}

// netplayCoreCandidates lists the core base names to look for, preferred first.
// Empty when the console has no RetroArch link path.
func netplayCoreCandidates(system string) []string {
	set, ok := netplayCoreSetFor(system)
	if !ok || set.unsupported != "" {
		return nil
	}
	return append([]string(nil), set.cores...)
}

// netplaySupportNote explains why a console cannot use LAN play. Empty means it
// can (the core still has to be installed).
func netplaySupportNote(system string) string {
	set, ok := netplayCoreSetFor(system)
	if !ok {
		label := strings.TrimSpace(system)
		if label == "" {
			label = "this console"
		}
		return "LAN play needs a RetroArch core; none is mapped for " + label
	}
	return set.unsupported
}

// parseNetplayAddress accepts what a person actually types: "192.168.1.5",
// "192.168.1.5:55435", "hostname", "[fe80::1]:55435". A bare address gets the
// default port.
func parseNetplayAddress(raw string, defPort int) (string, int, error) {
	if defPort <= 0 || defPort > 65535 {
		defPort = netplayDefaultPort
	}
	text := strings.TrimSpace(raw)
	text = strings.TrimSpace(strings.TrimSuffix(text, "/"))
	if text == "" {
		return "", 0, errors.New("enter the other device's address")
	}
	if strings.ContainsAny(text, " \t\n") {
		return "", 0, fmt.Errorf("%q is not an address", raw)
	}

	host, portText := text, ""
	switch {
	case strings.HasPrefix(text, "["):
		// Bracketed IPv6, with or without a port.
		if h, p, err := net.SplitHostPort(text); err == nil {
			host, portText = h, p
		} else {
			host = strings.Trim(text, "[]")
		}
	case strings.Count(text, ":") == 1:
		i := strings.LastIndexByte(text, ':')
		host, portText = text[:i], text[i+1:]
	case strings.Count(text, ":") > 1:
		// Bare IPv6: every colon belongs to the address.
		host = text
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", 0, errors.New("enter the other device's address")
	}

	port := defPort
	if portText != "" {
		p, err := strconv.Atoi(portText)
		if err != nil || p <= 0 || p > 65535 {
			return "", 0, fmt.Errorf("port %q is not a number between 1 and 65535", portText)
		}
		port = p
	}
	return host, port, nil
}

// netplayRetroArchPath finds the RetroArch binary. JUKAHUB_RETROARCH wins so a
// non-standard install can be pointed at without a code change.
func netplayRetroArchPath() string {
	// An explicit override is only honoured when it points at a real file, so a
	// stale path still reports "RetroArch not found" instead of failing later.
	for _, override := range []string{netplayRetroArchOverride, os.Getenv("JUKAHUB_RETROARCH")} {
		if p := strings.TrimSpace(override); p != "" && regularFile(p) {
			return p
		}
	}
	candidates := []string{
		"/mnt/SDCARD/RetroArch/retroarch",
		"/mnt/SDCARD/Apps/RetroArch/retroarch",
		"/mnt/sdcard/RetroArch/retroarch",
		"/mnt/sdcard/Apps/RetroArch/retroarch",
		"/usr/trimui/RetroArch/retroarch",
		"/usr/bin/retroarch",
		"/usr/local/bin/retroarch",
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, "RetroArch", "retroarch"),
			filepath.Join(dir, "retroarch", "retroarch"),
		)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "RetroArch", "retroarch"),
			filepath.Join(filepath.Dir(exe), "retroarch", "retroarch"),
		)
	}
	for _, candidate := range candidates {
		if regularFile(candidate) {
			return candidate
		}
	}
	for _, name := range []string{"retroarch", "retroarch.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// netplayCoreDirs lists the folders a core may live in. The RetroArch cores
// folder is the canonical one; a portable build keeps its cores next to the
// binary, which is the common case on a desktop copy.
func netplayCoreDirs() []string {
	if len(netplayCoreDirsOverride) > 0 {
		return append([]string{}, netplayCoreDirsOverride...)
	}
	var dirs []string
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_RETROARCH_CORES")); override != "" {
		dirs = append(dirs, override)
	}
	if exe := netplayRetroArchPath(); exe != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "cores"))
	}
	dirs = append(dirs, retroArchCoreDirs()...)
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// netplayCorePath resolves the installed core file for a console, preferring the
// earlier (better supported) core in the table over a later one in another dir.
func netplayCorePath(system string, dirs []string) string {
	for _, name := range netplayCoreCandidates(system) {
		for _, dir := range dirs {
			for _, ext := range []string{".so", ".dll", ".dylib"} {
				p := filepath.Join(dir, name+ext)
				if regularFile(p) {
					return p
				}
			}
		}
	}
	return ""
}

// netplayPort returns the port both sides use for a session.
func netplayPort() int {
	if netplayPortOverride > 0 && netplayPortOverride <= 65535 {
		return netplayPortOverride
	}
	return netplayDefaultPort
}

// netplayNickname is the name the other player sees. It reuses the display name
// already configured for chat, trimmed to what a RetroArch picker shows.
func netplayNickname() string {
	name := strings.TrimSpace(chatDisplayName())
	if name == "" {
		name = "JukaHub"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case r == ' ':
			return '_'
		}
		return r
	}, name)
	runes := []rune(name)
	if len(runes) > 20 {
		name = string(runes[:20])
	}
	return name
}

// netplayPlanFor validates everything a launch needs and builds the session.
// A failure here is a message the user can act on, never a black screen.
func netplayPlanFor(role netplayRole, game libraryGame, rawAddress string) (netplayPlan, error) {
	if role != netplayHost && role != netplayJoin {
		return netplayPlan{}, errors.New("choose host or join")
	}
	if strings.TrimSpace(game.Path) == "" {
		return netplayPlan{}, errors.New("no game selected")
	}
	if !regularFile(game.Path) {
		return netplayPlan{}, fmt.Errorf("ROM is missing: %s", filepath.Base(game.Path))
	}
	if note := netplaySupportNote(game.System); note != "" {
		return netplayPlan{}, errors.New(note)
	}
	if strings.TrimSpace(game.Title) == "" {
		game.Title = gameDisplayTitle(game.Path)
	}

	plan := netplayPlan{Nick: netplayNickname()}
	plan.RetroArch = netplayRetroArchPath()
	if plan.RetroArch == "" {
		return netplayPlan{}, errors.New("RetroArch not found — install it from Emulators, or set JUKAHUB_RETROARCH")
	}
	plan.Core = netplayCorePath(game.System, netplayCoreDirs())
	if plan.Core == "" {
		label := strings.TrimSpace(game.System)
		if set, ok := netplayCoreSetFor(game.System); ok && set.label != "" {
			label = set.label
		}
		return netplayPlan{}, fmt.Errorf("no RetroArch core for %s — install one from Emulators", label)
	}

	plan.Session = netplaySession{Role: role, Game: game, Port: netplayPort()}
	switch role {
	case netplayHost:
		ip := localIPv4()
		if ip == "" {
			return netplayPlan{}, errors.New("no Wi-Fi address yet — connect to a network, then host again")
		}
		plan.Session.Host = ip
	case netplayJoin:
		host, port, err := parseNetplayAddress(rawAddress, netplayPort())
		if err != nil {
			return netplayPlan{}, err
		}
		plan.Session.Host, plan.Session.Port = host, port
	}
	return plan, nil
}

// netplayCommand builds the exact argv: the same core and the same ROM on both
// sides, with netplay switched on and the port passed explicitly.
func netplayCommand(plan netplayPlan) (string, []string) {
	session := plan.Session
	port := session.Port
	if port <= 0 || port > 65535 {
		port = netplayDefaultPort
	}
	args := []string{"-L", plan.Core, session.Game.Path}
	if session.Role == netplayHost {
		args = append(args, "--host")
	} else {
		args = append(args, "--connect", session.Host)
	}
	args = append(args, "--port", strconv.Itoa(port))
	if nick := strings.TrimSpace(plan.Nick); nick != "" {
		args = append(args, "--nick", nick)
	}
	return plan.RetroArch, args
}

// netplayCanListen reports whether this device can bind the netplay port. The
// listener is closed immediately: RetroArch needs to own the port itself.
func netplayCanListen(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	return ln.Close()
}

// netplayReachable checks that something is listening at the other end before
// RetroArch is started, so a typo is a message instead of a hang.
func netplayReachable(host string, port int, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// netplayLaunch starts RetroArch in netplay mode. Errors are returned rather
// than toasted so the panel can keep the exact reason on screen.
func netplayLaunch(plan netplayPlan, config *Config) error {
	switch plan.Session.Role {
	case netplayHost:
		if err := netplayListenProbe(plan.Session.Port); err != nil {
			return fmt.Errorf("port %d is already in use — close the other session (%v)", plan.Session.Port, err)
		}
	case netplayJoin:
		if err := netplayReachProbe(plan.Session.Host, plan.Session.Port, 1500*time.Millisecond); err != nil {
			return fmt.Errorf("no host answered at %s — check the address and that the other device is hosting", plan.Session.address())
		}
	}

	name, args := netplayCommand(plan)
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Dir(plan.RetroArch)
	restoreProfile := beginGamePowerProfile(config)
	if err := cmd.Start(); err != nil {
		restoreProfile()
		return fmt.Errorf("could not start RetroArch: %v", err)
	}
	noteGameChildStarted()
	recorder := startTimelapse(config, plan.Session.Game.Title)
	go func() {
		_ = cmd.Wait()
		noteGameChildExited()
		restoreProfile()
		recorder.Stop()
	}()
	recordGameLaunch(plan.Session.Game)
	return nil
}
