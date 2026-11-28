package main

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetplayInviteRoundTrip(t *testing.T) {
	plan := netplayPlan{
		Session: netplaySession{
			Role: netplayHost,
			Game: libraryGame{Path: filepath.Join("roms", "GBA", "Pokemon Emerald.gba"), Title: "Pokemon Emerald"},
			Host: "192.168.1.5",
			Port: 55435,
		},
		Core: "/cores/mgba_libretro.so",
		Nick: "Ann",
	}
	link := netplayInviteLink(plan)
	if !strings.HasPrefix(link, "jukahub://netplay?") {
		t.Fatalf("invite = %q", link)
	}
	room, err := parseNetplayInvite(link)
	if err != nil {
		t.Fatal(err)
	}
	if room.Host != "192.168.1.5" || room.Port != 55435 || room.File != "Pokemon Emerald.gba" ||
		room.Core != "mgba_libretro" || room.Nick != "Ann" || room.Title != "Pokemon Emerald" {
		t.Fatalf("room = %+v", room)
	}
	if _, err := parseNetplayInvite("jukahub://netplay?host=192.168.1.5&port=55435&game=../escape.gba&core=mgba_libretro"); err == nil {
		t.Fatal("an invite with a path as its game was accepted")
	}
	if _, err := parseNetplayInvite("jukahub://netplay?host=192.168.1.5&port=99999&game=a.gba&core=mgba_libretro"); err == nil {
		t.Fatal("an invite with an out-of-range port was accepted")
	}
	if netplayInviteIsLink("https://example.com/game") {
		t.Fatal("an http link was taken for a netplay invite")
	}
}

func TestRetroArchAnnouncementIsParsed(t *testing.T) {
	packet := make([]byte, 688)
	binary.BigEndian.PutUint32(packet[0:], netplayRetroArchResponseMagic)
	binary.BigEndian.PutUint32(packet[4:], 0x1234)
	binary.BigEndian.PutUint32(packet[8:], uint32(55435))
	binary.BigEndian.PutUint32(packet[12:], 1)
	copy(packet[16:], "Ann")
	copy(packet[80:], "mgba_libretro")
	copy(packet[176:], "/home/ann/roms/GBA/Pokemon Emerald.gba")
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.9")}
	room, ok := parseRetroArchAnnouncement(packet, from)
	if !ok {
		t.Fatal("a valid RetroArch announcement was rejected")
	}
	if room.Host != "192.168.1.9" || room.Port != 55435 || room.File != "Pokemon Emerald.gba" ||
		room.Core != "mgba_libretro" || room.Nick != "Ann" || !room.Locked {
		t.Fatalf("room = %+v", room)
	}
	packet[0] = 0
	if _, ok := parseRetroArchAnnouncement(packet, from); ok {
		t.Fatal("a packet with the wrong magic was accepted")
	}
	packet[0] = 0x52
	packet[1] = 0x41
	packet[2] = 0x4E
	packet[3] = 0x53
	copy(packet[80:], "../evil")
	if _, ok := parseRetroArchAnnouncement(packet, from); ok {
		t.Fatal("an announcement with an unsafe core name was accepted")
	}
}

func TestLockedRoomNeedsItsPassword(t *testing.T) {
	dir := t.TempDir()
	rom := filepath.Join(dir, "Pokemon Emerald.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	game := libraryGame{Path: rom, System: "GBA", Title: "Pokemon Emerald"}
	index := map[string][]libraryGame{netplayNormName(rom): {game}}
	installed := map[string]string{netplayCoreNorm("mgba_libretro"): filepath.Join(dir, "mgba_libretro.so")}
	room := netplayRoom{
		Source: netplayRoomLAN, Title: game.Title, File: filepath.Base(rom),
		Core: "mgba_libretro", Host: "192.168.1.5", Port: 55435, Locked: true,
	}
	if _, err := netplayPlanForRoom(room, index, installed, "/usr/bin/retroarch", ""); !errors.Is(err, errNetplayLocked) {
		t.Fatalf("without a password: err = %v, want errNetplayLocked", err)
	}
	plan, err := netplayPlanForRoom(room, index, installed, "/usr/bin/retroarch", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Session.Password != "s3cret" {
		t.Fatalf("password = %q", plan.Session.Password)
	}
}

func TestNetplayPasswordConfigIsSealed(t *testing.T) {
	text, err := netplayPasswordConfigText("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if text != "netplay_password = \"s3cret\"\n" {
		t.Fatalf("config = %q", text)
	}
	if _, err := netplayPasswordConfigText("bad\"password"); err == nil {
		t.Fatal("a password with a quote was accepted")
	}
	netplayAppendConfigDirOverride = t.TempDir()
	defer func() { netplayAppendConfigDirOverride = "" }()
	path, err := netplayWritePasswordConfig("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != text {
		t.Fatalf("written config = %q", raw)
	}
}
