package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The saved game is the player's to resume: it has to come back exactly, undo
// history included, and a damaged game must not cost the statistics kept
// beside it.
func TestSolitaireSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), solSaveFileName)
	if f, g, err := solLoadFile(path); err != nil || g != nil || f.Stats.Played != 0 {
		t.Fatalf("no file yet: %+v, %v, %v", f, g, err)
	}

	g := solDeal(42, 3)
	g.drawStock()
	g.counted = true
	stats := solStats{Played: 3, Won: 1, Streak: 1, BestStreak: 1, BestTimeMs: 90000, FewestMoves: 80}
	data, err := solEncodeFile(g, stats, solOptions{Draw: 3, AutoMoveOff: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, back, err := solLoadFile(path)
	if err != nil || back == nil {
		t.Fatalf("saved game did not load: %v", err)
	}
	if back.encodePiles() != g.encodePiles() || back.moves != 1 || back.draw != 3 || back.seed != 42 || !back.counted {
		t.Errorf("game came back as %+v", back)
	}
	if f.Stats != stats || f.Options.Draw != 3 || !f.Options.AutoMoveOff {
		t.Errorf("statistics and options came back as %+v, %+v", f.Stats, f.Options)
	}
	if !back.undoStep() || back.encodePiles() != solDeal(42, 3).encodePiles() {
		t.Error("the saved undo step does not lead back to the deal")
	}

	bad := `{"version":1,"stats":{"played":3,"won":1},"game":{"variant":"klondike","piles":"ASAS"}}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, back, err := solLoadFile(path); back != nil || err == nil || f.Stats.Played != 3 || f.Stats.Won != 1 {
		t.Errorf("damaged game: %+v, %v, %v; want no game and the statistics kept", f.Stats, back, err)
	}
}
