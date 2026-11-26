package main

import (
	"fmt"
	"testing"
)

// A catalog refresh can add many downloadable cores while the Emulators page
// remains open. The selection and the last drawn tile still mean GameCube.
func TestEmulatorSelectionSurvivesCatalogGrowth(t *testing.T) {
	old := make([]emuEntry, 100)
	for i := range old {
		old[i] = emuEntry{Name: fmt.Sprintf("Old %03d", i), Status: emuAvailable}
	}
	old[97] = emuEntry{Name: "GameCube", Status: emuMissing, Core: "dolphin"}

	newEntries := make([]emuEntry, 0, 262)
	newEntries = append(newEntries, old[:97]...)
	for i := 0; i < 162; i++ {
		newEntries = append(newEntries, emuEntry{Name: fmt.Sprintf("New %03d", i), Status: emuAvailable})
	}
	newEntries = append(newEntries, old[97:]...)
	const gameCubeIndex = 259
	newEntries[gameCubeIndex].Status = emuAvailable // installation also changes status
	if len(newEntries) != 262 || newEntries[97].Name == "GameCube" {
		t.Fatal("fixture does not reorder the 100-entry catalog into 262 entries")
	}

	previousPanel := emuPanel
	emuCache.mu.Lock()
	previousEntries, previousBuilt := emuCache.entries, emuCache.built
	emuCache.entries, emuCache.built = newEntries, true
	emuCache.mu.Unlock()
	t.Cleanup(func() {
		emuPanel = previousPanel
		emuCache.mu.Lock()
		emuCache.entries, emuCache.built = previousEntries, previousBuilt
		emuCache.mu.Unlock()
	})

	emuPanel = emuPanelState{open: true}
	if !emuPanelSelect(old, 97) {
		t.Fatal("could not select GameCube in the old catalog")
	}
	selected, ok := emuSelectedEntry() // A, X and Y resolve through this helper
	if !ok || selected.Name != "GameCube" || emuPanel.cursor != gameCubeIndex {
		t.Fatalf("action target after refresh = %q at %d, want GameCube at %d", selected.Name, emuPanel.cursor, gameCubeIndex)
	}

	staleHit := emuRowHit{key: emuEntryIdentity(old[97])}
	if index := emuEntryIndex(newEntries, staleHit.key); index != gameCubeIndex {
		t.Fatalf("stale GameCube click maps to %d, want %d", index, gameCubeIndex)
	}
	withoutGameCube := append([]emuEntry(nil), newEntries[:gameCubeIndex]...)
	withoutGameCube = append(withoutGameCube, newEntries[gameCubeIndex+1:]...)
	if index := emuEntryIndex(withoutGameCube, staleHit.key); index != -1 {
		t.Fatalf("click on removed GameCube maps to a different tile at %d", index)
	}
	emuCache.mu.Lock()
	emuCache.entries = withoutGameCube
	emuCache.mu.Unlock()
	if selected, ok := emuSelectedEntry(); ok {
		t.Fatalf("action after GameCube disappeared targets %q", selected.Name)
	}
}

func TestEmulatorIdentitySeparatesStandaloneAndCore(t *testing.T) {
	entries := []emuEntry{
		{Name: "PPSSPP", Status: emuMissing},
		{Name: "ppsspp", Status: emuAvailable, Core: "ppsspp", CorePath: "/cores/ppsspp_libretro.dll"},
	}
	if index := emuEntryIndex(entries, emuEntryIdentity(entries[0])); index != 0 {
		t.Fatalf("standalone PPSSPP resolves to tile %d, want 0", index)
	}
	if index := emuEntryIndex(entries, emuEntryIdentity(entries[1])); index != 1 {
		t.Fatalf("PPSSPP core resolves to tile %d, want 1", index)
	}
	if emuEntryIdentity(entries[0]) == emuEntryIdentity(entries[1]) {
		t.Fatal("standalone emulator and core share one selection identity")
	}
}
