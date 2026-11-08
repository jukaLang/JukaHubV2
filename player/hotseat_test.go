package main

// hotseat_test.go — Local play: joining and port order, the announcement of a
// second controller, the RetroArch player order on launch, and the
// pass-and-play turns with a fake clock.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// hotseatTestReset empties the session and puts it, and the seams, back after
// the test.
func hotseatTestReset(t *testing.T) {
	t.Helper()
	prev := hotseat
	prevCount, prevClock, prevDir := hotseatCountHook, hotseatClockHook, hotseatAppendDir
	t.Cleanup(func() {
		hotseat = prev
		hotseatCountHook, hotseatClockHook, hotseatAppendDir = prevCount, prevClock, prevDir
	})
	hotseat.extras, hotseat.players, hotseat.count = nil, nil, 0
	hotseat.note, hotseat.turns = "", hotseatTurns{}
}

func TestHotseatJoinLeaveAndOrder(t *testing.T) {
	hotseatTestReset(t)
	clearToastAfterTest(t)
	if !hotseatJoin(10, "Pad A") || !hotseatJoin(11, "Pad B") {
		t.Fatal("two pads could not join")
	}
	if hotseatJoin(10, "Pad A again") {
		t.Fatal("a pad joined twice")
	}
	hotseatJoin(12, "Pad C")
	hotseatJoin(13, "Pad D")
	if hotseatJoin(14, "Pad E") || len(hotseat.players) != hotseatMaxPlayers {
		t.Fatalf("%d players after a fifth tried to join", len(hotseat.players))
	}
	if !hotseatMove(0, 1) || hotseat.players[0].id != 11 || hotseat.players[1].id != 10 {
		t.Fatalf("moving P1 right gave %+v", hotseat.players)
	}
	if hotseatMove(3, 1) || hotseatMove(0, -1) {
		t.Fatal("a player moved past the ends")
	}
	if !hotseatLeave(10) || hotseatPlayerIndex(10) != -1 || len(hotseat.players) != 3 || hotseatPlayerIndex(12) != 1 {
		t.Fatalf("after Pad A left: %+v", hotseat.players)
	}
	if hotseatLeave(99) {
		t.Fatal("a pad that never joined left")
	}
	if hotseatColor(0) == hotseatColor(1) || hotseatColor(4) != hotseatColor(0) {
		t.Fatal("the player colours do not tell P1 and P2 apart, or do not cycle")
	}
}

func TestHotseatAnnouncesASecondController(t *testing.T) {
	hotseatTestReset(t)
	clearToastAfterTest(t)
	hotseatCountChanged(1)
	if currentToast.Message != "" {
		t.Fatalf("one controller was announced: %q", currentToast.Message)
	}
	hotseatCountChanged(2)
	if !strings.Contains(currentToast.Message, "2 controllers connected") || !strings.Contains(currentToast.Message, "local multiplayer ready") {
		t.Fatalf("toast = %q", currentToast.Message)
	}
	currentToast = ToastState{}
	hotseatCountChanged(2)
	if currentToast.Message != "" {
		t.Fatal("the same count was announced twice")
	}
	hotseatCountChanged(3)
	if !strings.Contains(currentToast.Message, "3 controllers") {
		t.Fatalf("a third pad was not announced: %q", currentToast.Message)
	}
	if hotseatCountText() != "3 controllers" {
		t.Fatalf("header read-out = %q", hotseatCountText())
	}
}

func TestHotseatAppendConfig(t *testing.T) {
	got := hotseatAppendConfig([]int{1, 0, -1})
	if !strings.Contains(got, `input_player1_joypad_index = "1"`) || !strings.Contains(got, `input_player2_joypad_index = "0"`) ||
		strings.Contains(got, "input_player3") {
		t.Fatalf("config =\n%s", got)
	}
	const path = "/tmp/lp.cfg"
	for _, c := range []struct {
		in, want []string
	}{
		{nil, []string{"--appendconfig=" + path}},
		{[]string{"--fullscreen"}, []string{"--fullscreen", "--appendconfig=" + path}},
		{[]string{"--appendconfig=a.cfg", "-v"}, []string{"--appendconfig=a.cfg|" + path, "-v"}},
		{[]string{"--appendconfig", "a.cfg"}, []string{"--appendconfig", "a.cfg|" + path}},
	} {
		if got := hotseatMergeAppendConfig(c.in, path); strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("merge %v = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		head  string
		batch bool
		want  bool
	}{
		{"#!/bin/sh\nretroarch -L core \"$1\"\n", false, false},
		{"#!/bin/sh\nretroarch -L core \"$@\"\n", false, true},
		{"exec ra $*", false, true},
		{"ra ${@}", false, true},
		{"retroarch.exe %*", true, true},
		{"retroarch.exe %1", true, false},
	} {
		if got := hotseatScriptForwards(c.head, c.batch); got != c.want {
			t.Errorf("%q forwards = %v, want %v", c.head, got, c.want)
		}
	}
}

func TestHotseatLaunchArgs(t *testing.T) {
	hotseatTestReset(t)
	dir := t.TempDir()
	hotseatAppendDir = func() string { return dir }
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	forwarding := write("launch.sh", "#!/bin/sh\nexec /mnt/SDCARD/RetroArch/retroarch -L core \"$@\"\n")
	stock := write("stock.sh", "#!/bin/sh\n/mnt/SDCARD/RetroArch/retroarch -L core \"$1\"\n")
	other := write("ppsspp.bin", "standalone emulator")
	extra := []string{"--verbose"}

	hotseat.players = []hotseatPlayer{{id: 1, name: "One"}}
	if got := hotseatLaunchArgs(forwarding, extra); len(got) != 1 {
		t.Fatalf("one player changed the launch: %v", got)
	}

	hotseat.players = append(hotseat.players, hotseatPlayer{id: 2, name: "Two"})
	got := hotseatLaunchArgs(forwarding, extra)
	cfg := filepath.Join(dir, hotseatAppendName)
	if len(got) != 2 || got[0] != "--verbose" || got[1] != "--appendconfig="+cfg {
		t.Fatalf("two players launched with %v", got)
	}
	if body, err := os.ReadFile(cfg); err != nil || !strings.Contains(string(body), "Local play") {
		t.Fatalf("the one-launch config = %q (%v)", body, err)
	}

	if got := hotseatLaunchArgs(stock, extra); len(got) != 1 || !strings.Contains(hotseat.note, "does not pass options") {
		t.Fatalf("a launcher that passes only the ROM got %v, note %q", got, hotseat.note)
	}
	if got := hotseatLaunchArgs(other, extra); len(got) != 1 || !strings.Contains(hotseat.note, "only reaches RetroArch") {
		t.Fatalf("a standalone emulator got %v, note %q", got, hotseat.note)
	}
}

func TestHotseatTurns(t *testing.T) {
	var turns hotseatTurns
	turns.start(1, 60)
	if turns.seats != 2 || !turns.passing || turns.turn != 1 || turns.current != 0 {
		t.Fatalf("start = %+v", turns)
	}
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if turns.remaining(t0) != -1 {
		t.Fatal("the timer runs while the pass card is up")
	}
	turns.begin(t0)
	if left := turns.remaining(t0.Add(15 * time.Second)); left != 45 {
		t.Fatalf("15 s into a minute: %d left", left)
	}
	if turns.tick(t0.Add(59 * time.Second)) {
		t.Fatal("the turn ended a second early")
	}
	if !turns.tick(t0.Add(60*time.Second)) || !turns.passing || !turns.expired || turns.current != 1 || turns.turn != 2 {
		t.Fatalf("at the limit: %+v", turns)
	}
	turns.begin(t0.Add(70 * time.Second))
	turns.pass(false)
	if turns.current != 0 || turns.turn != 3 || turns.round() != 2 || turns.expired {
		t.Fatalf("after Player 2's turn: %+v (round %d)", turns, turns.round())
	}
	turns.pass(false)
	if turns.turn != 3 {
		t.Fatal("a second pass while the card was up counted a turn")
	}

	var free hotseatTurns
	free.start(3, 0)
	free.begin(t0)
	if free.remaining(t0.Add(time.Hour)) != -1 || free.tick(t0.Add(time.Hour)) {
		t.Fatal("a session without a timer ran out")
	}
	if hotseatTimerLabel(0) != "Off" || hotseatTimerLabel(30) != "30 seconds" || hotseatTimerLabel(120) != "2 minutes" {
		t.Fatal("timer labels")
	}
	if hotseatClock(65) != "1:05" || hotseatClock(600) != "10:00" {
		t.Fatalf("clock = %q, %q", hotseatClock(65), hotseatClock(600))
	}
}

func TestHotseatPassTurnAndTimerFromAnywhere(t *testing.T) {
	hotseatTestReset(t)
	clearToastAfterTest(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	hotseatClockHook = func() time.Time { return now }
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}}}
	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	currentSceneIndex = 0

	if hotseatPassTurn(config) {
		t.Fatal("a turn passed with no session")
	}
	hotseat.turns.start(2, 30)
	if hotseatPassTurn(config) {
		t.Fatal("a turn passed while the card was up")
	}
	hotseat.turns.begin(now)
	if !hotseatPassTurn(config) || !hotseat.turns.passing || hotseat.turns.current != 1 {
		t.Fatalf("pass_turn: %+v", hotseat.turns)
	}
	if !strings.Contains(currentToast.Message, "Pass to Player 2") {
		t.Fatalf("toast = %q", currentToast.Message)
	}

	// The timer runs out on another screen: a toast says so, once.
	hotseat.turns.begin(now)
	now = now.Add(31 * time.Second)
	currentToast = ToastState{}
	hotseatTick(config)
	if !hotseat.turns.passing || !strings.Contains(currentToast.Message, "Time's up") {
		t.Fatalf("after the limit: %+v, toast %q", hotseat.turns, currentToast.Message)
	}
	currentToast = ToastState{}
	hotseatTick(config)
	if currentToast.Message != "" {
		t.Fatalf("the time-up toast came twice: %q", currentToast.Message)
	}
}

func TestHotseatToolsTile(t *testing.T) {
	hotseatTestReset(t)
	tile := hotseatToolsTile()
	if tile.id != "localplay" || tile.icon != "users" || tile.tone != toolsHubToneNeutral || tile.value == "" {
		t.Fatalf("idle tile = %+v", tile)
	}
	hotseat.count = 2
	if tile = hotseatToolsTile(); tile.value != "2 controllers connected" || tile.tone != toolsHubToneGood {
		t.Fatalf("two pads: %+v", tile)
	}
	hotseat.players = []hotseatPlayer{{id: sdl.JoystickID(1)}, {id: sdl.JoystickID(2)}}
	if tile = hotseatToolsTile(); tile.value != "2 players ready" {
		t.Fatalf("two players: %+v", tile)
	}
}
