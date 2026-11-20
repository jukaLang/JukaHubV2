package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The save ledger is what the game page's "Saved 4 min ago" stands on: it
// must survive a reload, stay bounded however long the handheld is used, and
// a damaged file must fall back to its backup or start empty, never fail.
func TestSaveLedgerFilePersistsBoundedAndSurvivesDamage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, saveLedgerFileName)
	if f := saveLedgerLoadAt(path); f.V != saveLedgerVersion || len(f.Games) != 0 {
		t.Fatalf("a missing ledger reads as %+v, want an empty one", f)
	}

	f := saveLedgerFile{V: saveLedgerVersion}
	add := func(rom string, e saveLedgerEntry) {
		f.Seq++
		e.Seq = f.Seq
		saveLedgerAdd(&f, rom, "", "", e)
	}
	rom := "/mnt/SDCARD/Roms/GBA/Zelda.gba"
	for i := 1; i <= saveLedgerKeep+3; i++ {
		add(rom, saveLedgerEntry{Kind: saveLedgerKindState, File: "Zelda.state" + strconv.Itoa(i), Size: 10, Trigger: saveLedgerTrigQuit})
	}
	// A battery save written again and again in one session keeps one line.
	for i := 0; i < 3; i++ {
		add(rom, saveLedgerEntry{Kind: saveLedgerKindSave, File: "Zelda.srm", Size: 8, Trigger: saveLedgerTrigPlay, Sess: 7})
	}
	g := f.Games[rom]
	if g == nil || len(g.Saves) != saveLedgerKeep {
		t.Fatalf("game record = %+v, want %d entries", g, saveLedgerKeep)
	}
	srm := 0
	for _, e := range g.Saves {
		if e.File == "Zelda.srm" {
			srm++
		}
	}
	if last := g.Saves[len(g.Saves)-1]; srm != 1 || last.File != "Zelda.srm" || last.Seq != f.Seq {
		t.Errorf("battery save lines = %d, newest = %+v; want one, the newest", srm, last)
	}

	// The games touched longest ago go first.
	for i := 0; i < saveLedgerMaxGames+5; i++ {
		add("/roms/"+strconv.Itoa(i)+".nes", saveLedgerEntry{Kind: saveLedgerKindSave, File: strconv.Itoa(i) + ".srm", Trigger: saveLedgerTrigQuit})
	}
	if len(f.Games) != saveLedgerMaxGames || f.Games[rom] != nil || f.Games["/roms/0.nes"] != nil || f.Games["/roms/5.nes"] == nil {
		t.Fatalf("after %d more games: %d kept, oldest kept=%t", saveLedgerMaxGames+5, len(f.Games), f.Games[rom] != nil)
	}

	write := func() {
		t.Helper()
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := AtomicWrite(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write()
	firstSeq := f.Seq
	if back := saveLedgerLoadAt(path); len(back.Games) != saveLedgerMaxGames || back.Seq != firstSeq {
		t.Fatalf("reloaded %d games at seq %d, want %d at %d", len(back.Games), back.Seq, saveLedgerMaxGames, firstSeq)
	}

	// A second write keeps the first as the backup; a live file cut short
	// (a power cut mid-write on an old card) falls back to it.
	add("/roms/new.nes", saveLedgerEntry{Kind: saveLedgerKindSave, File: "new.srm", Trigger: saveLedgerTrigQuit})
	write()
	if err := os.WriteFile(path, []byte(`{"v":1,"seq":9,"games":{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if back := saveLedgerLoadAt(path); back.Seq != firstSeq || len(back.Games) != saveLedgerMaxGames {
		t.Errorf("damaged ledger read seq %d (%d games), want the backup's %d", back.Seq, len(back.Games), firstSeq)
	}
	// Kept aside, so the next write cannot copy it over the good backup.
	if _, err := os.Stat(path + ".damaged"); err != nil {
		t.Errorf("the damaged ledger was not kept aside: %v", err)
	}
	// Damaged with no backup, or a newer version's file: empty, never an error.
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"v":1,"seq":9,"games":{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if back := saveLedgerLoadAt(path); len(back.Games) != 0 || back.V != saveLedgerVersion {
		t.Errorf("damaged ledger without a backup = %d games, want none", len(back.Games))
	}
	if err := os.WriteFile(path, []byte(`{"v":99,"seq":3,"games":{"/x.gba":{"seq":3}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if back := saveLedgerLoadAt(path); len(back.Games) != 0 {
		t.Errorf("a newer version's ledger was read as this one: %+v", back.Games)
	}
}
