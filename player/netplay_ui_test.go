package main

// netplay_ui_test.go — the panel behaviour that decides what a button press
// does: which game the library hands over, when the address field opens, and
// that a bad address stays on screen instead of launching anything.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func netplayTestPanel(t *testing.T, system string) libraryGame {
	t.Helper()
	_, coresDir := withNetplayFixture(t)
	core := filepath.Join(coresDir, "mgba_libretro.so")
	if err := os.WriteFile(core, []byte("core"), 0o644); err != nil {
		t.Fatalf("write core: %v", err)
	}
	rom := filepath.Join(t.TempDir(), "Mario Kart.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatalf("write rom: %v", err)
	}
	game := libraryGame{Path: rom, System: system, Title: "Mario Kart: Super Circuit"}

	prevOpen := netplayPanel.open
	prevVirtual := virtualKeyboardActive
	prevTarget := vkTarget
	prevAddress := netplayLastAddress
	netplayLastAddress = ""
	t.Cleanup(func() {
		netplayPanel.open = prevOpen
		netplayPanel.status = ""
		netplayPanel.cursor = 0
		virtualKeyboardActive = prevVirtual
		vkTarget = prevTarget
		netplayLastAddress = prevAddress
	})
	openNetplayPanel(game)
	return game
}

func TestLibraryXOpensNetplayForTheSelectedGame(t *testing.T) {
	game := netplayTestPanel(t, "GBA")
	prevOpen, prevLib, prevSystem, prevCursor, prevFocus := gameLib.open, gameLib.lib, gameLib.system, gameLib.cursor, gameLib.focus
	t.Cleanup(func() {
		gameLib.open, gameLib.lib, gameLib.system, gameLib.cursor, gameLib.focus = prevOpen, prevLib, prevSystem, prevCursor, prevFocus
	})

	gameLib.open = true
	gameLib.lib = gameLibraryReport{Systems: []librarySystem{{
		Name:  "GBA",
		Games: []libraryGame{{Path: game.Path, System: "GBA", Title: "Mario Kart: Super Circuit"}},
	}}}
	gameLib.system = 0
	gameLib.focus = 1
	gameLib.cursor = 0
	netplayPanel.open = false

	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN}
	key.Keysym.Sym = sdl.K_x
	if !handleGameLibraryKey(key, nil) {
		t.Fatal("the library swallowed X without opening LAN play")
	}
	if !netplayPanel.open {
		t.Fatal("X did not open the LAN play panel")
	}
	if netplayPanel.game.Path != game.Path {
		t.Fatalf("panel game = %q, want %q", netplayPanel.game.Path, game.Path)
	}
	if !strings.Contains(netplayPanel.ready, "mgba") {
		t.Fatalf("readiness not resolved on open: %q", netplayPanel.ready)
	}
}

func TestNetplayPanelChooseAndMove(t *testing.T) {
	netplayTestPanel(t, "GBA")
	netplayPanel.cursor = 0
	down := &sdl.KeyboardEvent{Type: sdl.KEYDOWN}
	down.Keysym.Sym = sdl.K_DOWN
	if !handleNetplayKey(down, nil) {
		t.Fatal("panel did not consume the key")
	}
	if netplayPanel.cursor != 1 {
		t.Fatalf("cursor = %d after one down press", netplayPanel.cursor)
	}
	handleNetplayKey(down, nil)
	if netplayPanel.cursor != 0 {
		t.Fatalf("cursor = %d after wrapping", netplayPanel.cursor)
	}
	esc := &sdl.KeyboardEvent{Type: sdl.KEYDOWN}
	esc.Keysym.Sym = sdl.K_ESCAPE
	if !handleNetplayKey(esc, nil) || netplayPanel.open {
		t.Fatal("Esc did not close the panel")
	}
}

func TestNetplayJoinWithoutAddressOpensTheKeyboard(t *testing.T) {
	netplayTestPanel(t, "GBA")
	if virtualKeyboardActive {
		t.Fatal("keyboard was already open")
	}
	netplayPanel.cursor = 1
	netplayActivate(nil)
	if !virtualKeyboardActive || vkTarget != vkTargetNetplay {
		t.Fatalf("join with no address should open the address field (active=%v target=%v)",
			virtualKeyboardActive, vkTarget)
	}
	if vkTitle() != "HOST ADDRESS" || vkPlaceholder() != "192.168.1.5" {
		t.Fatalf("address field labels = %q / %q", vkTitle(), vkPlaceholder())
	}
	// Committing the field hands the text straight to the join path, which is
	// what the panel shows while the address is being typed.
	vkSetText("192.168.1.9:55435", nil)
	if netplayPanel.address != "192.168.1.9:55435" {
		t.Fatalf("typed address = %q", netplayPanel.address)
	}
}

func TestNetplayBadAddressStaysOnThePanel(t *testing.T) {
	netplayTestPanel(t, "GBA")
	netplayJoinSubmit("192.168.1.9:99999")
	if !netplayPanel.open {
		t.Fatal("panel closed after a bad address")
	}
	if !netplayPanel.warn || !strings.Contains(netplayPanel.status, "port") {
		t.Fatalf("status = %q warn=%v", netplayPanel.status, netplayPanel.warn)
	}
}

func TestNetplayUnsupportedConsoleExplainsItself(t *testing.T) {
	netplayTestPanel(t, "PSP")
	if netplayPanel.readyOK {
		t.Fatalf("PSP reported ready: %q", netplayPanel.ready)
	}
	if !strings.Contains(strings.ToLower(netplayPanel.ready), "ppsspp") {
		t.Fatalf("PSP readiness = %q", netplayPanel.ready)
	}
}

func TestNetplayRowDetailShowsWhereToConnect(t *testing.T) {
	netplayTestPanel(t, "GBA")
	netplayPanel.address = "192.168.1.9:55435"
	detail := netplayRowDetail(1)
	if !strings.Contains(detail, "192.168.1.9:55435") {
		t.Fatalf("join row detail = %q", detail)
	}
	if got := netplayRowDetail(9); got != "" {
		t.Fatalf("unknown row detail = %q", got)
	}
}
