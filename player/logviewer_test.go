package main

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestTailLinesKeepsNewestEntries(t *testing.T) {
	lines := []string{"oldest", "old", "new", "newest"}

	got := tailLines(lines, 2)
	if len(got) != 2 || got[0] != "new" || got[1] != "newest" {
		t.Errorf("tailLines(4,2) = %v, want the two newest lines", got)
	}

	if got := tailLines(lines, 10); len(got) != 4 {
		t.Errorf("a panel taller than the log should show every line, got %v", got)
	}
	if got := tailLines(lines, 0); len(got) != 4 {
		t.Errorf("max<=0 should leave the lines untouched, got %v", got)
	}
	if got := tailLines(nil, 3); len(got) != 0 {
		t.Errorf("nil input = %v, want empty", got)
	}
}

// The rendered panel must show the newest line, not the first one captured at
// startup.
func TestLogPanelShowsNewestLine(t *testing.T) {
	t.Cleanup(func() { appLog.Clear() })
	appLog.Clear()
	for _, line := range []string{"boot", "middle", "latest"} {
		appLog.Write([]byte(line + "\n"))
	}

	lines := strings.Split(appLog.String(), "\n")
	visible := 1
	got := strings.Join(tailLines(lines, visible), "\n")
	if !strings.Contains(got, "latest") {
		t.Errorf("visible log tail = %q, want the newest line", got)
	}
}

// Ctrl+L must reach the viewer from any scene, except in the terminal (where
// the key clears the output) and where the config has no viewer scene to show,
// in which case it has to stay unhandled instead of being swallowed.
func TestCtrlLOpensTheLogViewer(t *testing.T) {
	viewer := SceneConfig{Name: "LogExporter", Elements: []Element{
		{Type: "textlog", X: 24, Y: 72, Width: "1232", Height: "600"},
	}}
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}, viewer}}

	prevScene, prevPhase, prevPending := currentSceneIndex, transitionPhase, pendingSceneIndex
	t.Cleanup(func() {
		currentSceneIndex, transitionPhase, pendingSceneIndex = prevScene, prevPhase, prevPending
	})
	currentSceneIndex, transitionPhase, pendingSceneIndex = 0, "none", -1

	event := func(sym sdl.Keycode, mod uint16) *sdl.KeyboardEvent {
		return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym, Mod: mod}}
	}
	ctrl := uint16(sdl.KMOD_CTRL)

	if handleLogShortcut(event(sdl.K_l, ctrl), config, true) {
		t.Fatal("Ctrl+L must stay with the terminal, which clears its output with it")
	}
	if handleLogShortcut(event(sdl.K_l, 0), config, false) {
		t.Fatal("a plain L is typing, not the log viewer")
	}
	if !handleLogShortcut(event(sdl.K_l, ctrl), config, false) {
		t.Fatal("Ctrl+L did nothing outside the terminal")
	}
	if pendingSceneIndex != 1 || transitionPhase != "fade-out" {
		t.Fatalf("Ctrl+L queued scene %d (phase %q), want a fade to LogExporter",
			pendingSceneIndex, transitionPhase)
	}

	currentSceneIndex, transitionPhase, pendingSceneIndex = 0, "none", -1
	withoutViewer := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	if handleLogShortcut(event(sdl.K_l, ctrl), withoutViewer, false) {
		t.Fatal("Ctrl+L was consumed although there is no viewer scene to open")
	}
}
