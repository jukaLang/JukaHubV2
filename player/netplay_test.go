package main

// netplay_test.go — the pure half of LAN play: address parsing, console/core
// mapping, the exact RetroArch argv, and the preflight that stops a launch from
// becoming a black screen.

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withNetplayFixture(t *testing.T, systems ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	coresDir := filepath.Join(dir, "cores")
	if err := os.MkdirAll(coresDir, 0o755); err != nil {
		t.Fatalf("mkdir cores: %v", err)
	}
	ra := filepath.Join(dir, "retroarch")
	if err := os.WriteFile(ra, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write retroarch: %v", err)
	}
	oldRA := netplayRetroArchOverride
	oldCores := netplayCoreDirsOverride
	netplayRetroArchOverride = ra
	netplayCoreDirsOverride = []string{coresDir}
	t.Cleanup(func() {
		netplayRetroArchOverride = oldRA
		netplayCoreDirsOverride = oldCores
	})
	return ra, coresDir
}

func TestParseNetplayAddress(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		host    string
		port    int
		wantErr bool
	}{
		{name: "bare ipv4", raw: "192.168.1.5", host: "192.168.1.5", port: netplayDefaultPort},
		{name: "ipv4 and port", raw: "192.168.1.5:56000", host: "192.168.1.5", port: 56000},
		{name: "padded", raw: "  192.168.1.5:56000  ", host: "192.168.1.5", port: 56000},
		{name: "hostname", raw: "jukahub.local", host: "jukahub.local", port: netplayDefaultPort},
		{name: "hostname and port", raw: "jukahub.local:55435", host: "jukahub.local", port: 55435},
		{name: "trailing slash", raw: "10.0.0.7/", host: "10.0.0.7", port: netplayDefaultPort},
		{name: "ipv6 with port", raw: "[fe80::1]:5555", host: "fe80::1", port: 5555},
		{name: "bare ipv6", raw: "fe80::1", host: "fe80::1", port: netplayDefaultPort},
		{name: "empty", raw: "   ", wantErr: true},
		{name: "spaces", raw: "192.168.1.5 55435", wantErr: true},
		{name: "not a port", raw: "192.168.1.5:abc", wantErr: true},
		{name: "port zero", raw: "192.168.1.5:0", wantErr: true},
		{name: "port too big", raw: "192.168.1.5:70000", wantErr: true},
		{name: "colon only", raw: ":", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := parseNetplayAddress(tc.raw, netplayDefaultPort)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got host=%q port=%d", tc.raw, host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.host || port != tc.port {
				t.Fatalf("got %q:%d, want %q:%d", host, port, tc.host, tc.port)
			}
		})
	}
}

func TestNetplaySessionAddress(t *testing.T) {
	if got := (netplaySession{Host: "192.168.1.5", Port: 55435}).address(); got != "192.168.1.5:55435" {
		t.Fatalf("ipv4 address = %q", got)
	}
	if got := (netplaySession{Host: "fe80::1", Port: 55435}).address(); got != "[fe80::1]:55435" {
		t.Fatalf("ipv6 address = %q", got)
	}
	if got := (netplaySession{Host: "10.0.0.2"}).address(); got != "10.0.0.2:55435" {
		t.Fatalf("default port address = %q", got)
	}
	if got := (netplaySession{}).address(); got != "" {
		t.Fatalf("empty session address = %q", got)
	}
}

func TestNetplayCoreMapping(t *testing.T) {
	cases := []struct {
		system string
		want   string
	}{
		{system: "GBA", want: "mgba_libretro"},
		{system: "Game Boy Advance", want: "mgba_libretro"},
		{system: "Nintendo - Game Boy Advance", want: "mgba_libretro"},
		{system: "GB", want: "gambatte_libretro"},
		{system: "GBC", want: "gambatte_libretro"},
		{system: "SNES", want: "snes9x_libretro"},
		{system: "NES", want: "fceumm_libretro"},
		{system: "MD", want: "genesis_plus_gx_libretro"},
		{system: "N64", want: "mupen64plus_next_libretro"},
	}
	for _, tc := range cases {
		t.Run(tc.system, func(t *testing.T) {
			cores := netplayCoreCandidates(tc.system)
			if len(cores) == 0 {
				t.Fatalf("%q has no cores", tc.system)
			}
			if cores[0] != tc.want {
				t.Fatalf("%q preferred core = %q, want %q", tc.system, cores[0], tc.want)
			}
			if note := netplaySupportNote(tc.system); note != "" {
				t.Fatalf("%q should be supported, note = %q", tc.system, note)
			}
		})
	}
}

func TestNetplayUnsupportedSystemsExplainThemselves(t *testing.T) {
	for _, system := range []string{"PSP", "PlayStation Portable", "NDS", "Nintendo DS"} {
		if cores := netplayCoreCandidates(system); len(cores) != 0 {
			t.Fatalf("%q should not offer cores, got %v", system, cores)
		}
		note := netplaySupportNote(system)
		if !strings.Contains(strings.ToLower(note), "netplay") && !strings.Contains(strings.ToLower(note), "multiplayer") {
			t.Fatalf("%q note does not say why: %q", system, note)
		}
	}
	if note := netplaySupportNote("Virtual Boy"); !strings.Contains(note, "Virtual Boy") {
		t.Fatalf("unknown system note = %q", note)
	}
}

func TestNetplayPlanRefusesWithoutCoreOrEmulator(t *testing.T) {
	_, coresDir := withNetplayFixture(t)
	rom := filepath.Join(t.TempDir(), "Mario Kart.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatalf("write rom: %v", err)
	}
	game := libraryGame{Path: rom, System: "GBA", Title: "Mario Kart"}

	if _, err := netplayPlanFor(netplayJoin, game, "192.168.1.9"); err == nil ||
		!strings.Contains(err.Error(), "core") {
		t.Fatalf("missing core should be refused, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so"), []byte("core"), 0o644); err != nil {
		t.Fatalf("write core: %v", err)
	}
	plan, err := netplayPlanFor(netplayJoin, game, "192.168.1.9")
	if err != nil {
		t.Fatalf("join plan failed: %v", err)
	}
	if plan.Core != filepath.Join(coresDir, "mgba_libretro.so") {
		t.Fatalf("core = %q", plan.Core)
	}
	if plan.Session.Host != "192.168.1.9" || plan.Session.Port != netplayDefaultPort {
		t.Fatalf("session = %q:%d", plan.Session.Host, plan.Session.Port)
	}
	if plan.Session.Game.Title != "Mario Kart" {
		t.Fatalf("game title = %q", plan.Session.Game.Title)
	}
	if strings.TrimSpace(plan.Nick) == "" || strings.Contains(plan.Nick, " ") {
		t.Fatalf("nickname = %q", plan.Nick)
	}

	oldRA := netplayRetroArchOverride
	netplayRetroArchOverride = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { netplayRetroArchOverride = oldRA })
	if _, err := netplayPlanFor(netplayJoin, game, "192.168.1.9"); err == nil ||
		!strings.Contains(err.Error(), "RetroArch") {
		t.Fatalf("missing RetroArch should be refused, got %v", err)
	}
}

func TestNetplayPlanHostNeedsAnAddress(t *testing.T) {
	_, coresDir := withNetplayFixture(t)
	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so"), []byte("core"), 0o644); err != nil {
		t.Fatalf("write core: %v", err)
	}
	rom := filepath.Join(t.TempDir(), "Zelda.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatalf("write rom: %v", err)
	}
	game := libraryGame{Path: rom, System: "GBA", Title: "Zelda Four Swords"}
	plan, err := netplayPlanFor(netplayHost, game, "")
	if localIPv4() == "" {
		if err == nil || !strings.Contains(err.Error(), "Wi-Fi") {
			t.Fatalf("offline host should explain itself, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("host plan failed: %v", err)
	}
	if plan.Session.Host != localIPv4() || plan.Session.Port != netplayDefaultPort {
		t.Fatalf("host session = %q:%d", plan.Session.Host, plan.Session.Port)
	}
}

func TestNetplayCommandUsesSameCoreAndContent(t *testing.T) {
	plan := netplayPlan{
		RetroArch: `/mnt/SDCARD/RetroArch/retroarch`,
		Core:      `/mnt/SDCARD/RetroArch/.retroarch/cores/mgba_libretro.so`,
		Nick:      "Stepan",
		Session: netplaySession{
			Role: netplayHost,
			Game: libraryGame{Path: `/mnt/SDCARD/Roms/GBA/Mario Kart.gba`},
			Host: "192.168.1.5",
			Port: 55435,
		},
	}
	name, args := netplayCommand(plan)
	if name != plan.RetroArch {
		t.Fatalf("binary = %q", name)
	}
	want := []string{
		"-L", plan.Core, `/mnt/SDCARD/Roms/GBA/Mario Kart.gba`,
		"--host", "--port", "55435", "--nick", "Stepan",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("host argv = %v, want %v", args, want)
	}

	plan.Session.Role = netplayJoin
	plan.Session.Host = "192.168.1.9"
	_, args = netplayCommand(plan)
	want = []string{
		"-L", plan.Core, `/mnt/SDCARD/Roms/GBA/Mario Kart.gba`,
		"--connect", "192.168.1.9", "--port", "55435", "--nick", "Stepan",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("join argv = %v, want %v", args, want)
	}

	// A blank nickname is dropped rather than passed as an empty flag value.
	plan.Nick = "  "
	_, args = netplayCommand(plan)
	for _, arg := range args {
		if arg == "--nick" {
			t.Fatalf("empty nickname still passed: %v", args)
		}
	}
}

func TestNetplayLaunchPreflight(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := busy.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { busy.Close() })

	if err := netplayCanListen(port); err == nil {
		t.Fatalf("port %d is bound by the test but looked free", port)
	}

	// A failed preflight must not leak the probe listener: the same port has to
	// stay usable afterwards.
	free, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	if err := netplayCanListen(freePort); err != nil {
		t.Fatalf("free port %d refused: %v", freePort, err)
	}
	if err := netplayCanListen(freePort); err != nil {
		t.Fatalf("probe leaked the listener on %d: %v", freePort, err)
	}

	plan := netplayPlan{
		RetroArch: "retroarch",
		Core:      "core.so",
		Session: netplaySession{
			Role: netplayHost,
			Game: libraryGame{Path: "game.gba"},
			Host: "127.0.0.1",
			Port: port,
		},
	}
	oldListen := netplayListenProbe
	netplayListenProbe = netplayCanListen
	t.Cleanup(func() { netplayListenProbe = oldListen })
	if err := netplayLaunch(plan, nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("busy host port should be refused, got %v", err)
	}

	// Nothing is listening on the free port, so a join has to fail before
	// RetroArch is started.
	plan.Session.Role = netplayJoin
	plan.Session.Port = freePort
	if err := netplayLaunch(plan, nil); err == nil || !strings.Contains(err.Error(), "no host answered") {
		t.Fatalf("unreachable host should be refused, got %v", err)
	}
}

func TestNetplayReadinessReportsCore(t *testing.T) {
	_, coresDir := withNetplayFixture(t)
	game := libraryGame{Path: "Mario Kart.gba", System: "GBA", Title: "Mario Kart"}
	message, ok := netplayReadiness(game)
	if ok {
		t.Fatalf("no core installed yet, readiness said %q", message)
	}
	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so"), []byte("core"), 0o644); err != nil {
		t.Fatalf("write core: %v", err)
	}
	message, ok = netplayReadiness(game)
	if !ok || !strings.Contains(message, "mgba") {
		t.Fatalf("readiness = %q ok=%v", message, ok)
	}
}
