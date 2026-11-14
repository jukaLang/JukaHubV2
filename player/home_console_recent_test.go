package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestHomeConsoleRecentOrder(t *testing.T) {
	oldFile, oldLook, oldEdit, oldSwitch := homeConsoleRecentsFile, homeConsoleLook, homeEdit.open, homeSwitch
	homeConsoleRecents.Lock()
	oldItems, oldLoaded, oldGen := homeConsoleRecents.items, homeConsoleRecents.loaded, homeConsoleRecents.gen
	homeConsoleRecents.items, homeConsoleRecents.loaded, homeConsoleRecents.gen = nil, false, 0
	homeConsoleRecents.Unlock()
	t.Cleanup(func() {
		homeConsoleRecentsFile, homeConsoleLook, homeEdit.open, homeSwitch = oldFile, oldLook, oldEdit, oldSwitch
		homeConsoleRecents.Lock()
		homeConsoleRecents.items, homeConsoleRecents.loaded, homeConsoleRecents.gen = oldItems, oldLoaded, oldGen
		homeConsoleRecents.Unlock()
	})
	dir := t.TempDir()
	homeConsoleRecentsFile = func() string { return filepath.Join(dir, "home-console-recents.json") }
	homeConsoleLook.carousel, homeConsoleLook.switchHome = homeCarouselHub, false
	homeEdit.open = false
	app := HomeFocusID("app:do:Emulators")
	tiles := []homeDynTile{
		{ID: HomeFocusEmulator, Ref: "emulator", Kind: homeTileCard},
		{ID: app, Ref: string(app), Kind: homeTileApp},
		{ID: HomeFocusChat, Ref: "chat", Kind: homeTileCard},
		{ID: HomeFocusID("w:clock"), Ref: "w:clock", Kind: homeTileWidget},
	}
	homeConsoleRecordApp(string(app))
	homeConsoleRecordApp("chat")
	ordered := homeConsoleOrderTiles(tiles)
	if ordered[0].ID != HomeFocusChat || ordered[1].ID != app || ordered[2].ID != HomeFocusEmulator {
		t.Fatalf("PS5 recent app order: %v", []HomeFocusID{ordered[0].ID, ordered[1].ID, ordered[2].ID})
	}
	if tiles[0].ID != HomeFocusEmulator {
		t.Fatal("sorting changed the saved Home tile order")
	}
	homeEdit.open = true
	if got := homeConsoleOrderTiles(tiles); got[0].ID != HomeFocusEmulator {
		t.Fatal("Edit Home did not keep the saved order")
	}
	homeEdit.open = false
	homeConsoleRecents.Lock()
	homeConsoleRecents.items, homeConsoleRecents.loaded = nil, false
	homeConsoleRecents.Unlock()
	ordered = homeConsoleOrderTiles(tiles)
	if ordered[0].ID != HomeFocusChat || ordered[1].ID != app {
		t.Fatal("recent app order did not survive reload")
	}
	homeConsoleLook.carousel, homeConsoleLook.switchHome = homeCarouselStrip, true
	gamePath := "/roms/PSP/game.iso"
	homeSwitch.recents = []homeSwRecent{{item: FavoriteItem{Data: recentGame{Path: gamePath, PlayedAt: time.Now().Add(-time.Hour)}}}}
	game := homeDynTile{ID: homeSwRecentID(0), Ref: string(homeSwRecentID(0)), Kind: homeTileRecent}
	ordered = homeConsoleOrderTiles(append([]homeDynTile{game}, tiles...))
	if ordered[0].ID != HomeFocusChat || ordered[1].ID != app || ordered[2].ID != game.ID {
		t.Fatalf("Switch app and game recency: %v", []HomeFocusID{ordered[0].ID, ordered[1].ID, ordered[2].ID})
	}
	homeConsoleRecordGame(gamePath)
	ordered = homeConsoleOrderTiles(append([]homeDynTile{game}, tiles...))
	if ordered[0].ID != game.ID {
		t.Fatal("a game opened after an app did not move to the front")
	}
	homeConsoleRecordApp(string(app))
	ordered = homeConsoleOrderTiles(append([]homeDynTile{game}, tiles...))
	if ordered[0].ID != app || ordered[1].ID != game.ID {
		t.Fatal("an app opened after a game did not move to the front")
	}
	if got := homeConsoleMenuRef(startMenuEntry{name: "Emulators", action: func(*Config) {}}); got != string(app) {
		t.Fatalf("Emulators opened from Start would use the wrong tile: %q", got)
	}
	unpinned := homeConsoleVisibleTiles(&Config{}, []homeTile{{Ref: "chat"}})
	if len(unpinned) != 2 || unpinned[1].Ref != string(app) {
		t.Fatalf("a Start-menu app absent from Home did not appear: %v", unpinned)
	}
	for i := 0; i <= maxRecentGames; i++ {
		homeConsoleRecordGame(fmt.Sprintf("/roms/PSP/game-%d.iso", i))
	}
	homeConsoleRecents.Lock()
	appKept := false
	for _, item := range homeConsoleRecents.items {
		appKept = appKept || item.Ref == string(app)
	}
	homeConsoleRecents.Unlock()
	if !appKept {
		t.Fatal("game history pushed app history out of the console order")
	}
	homeConsoleRecordApp(string(HomeFocusPackages))
	saved := []homeTile{{Ref: "emulator"}, {Ref: string(app)}, {Ref: "chat"}, {Ref: "w:clock"}}
	visible := homeConsoleVisibleTiles(nil, saved)
	if len(visible) != len(saved)+1 || visible[len(visible)-1].Ref != string(HomeFocusPackages) {
		t.Fatal("a recently opened app absent from the saved row did not appear")
	}
	homeEdit.open = true
	if edited := homeConsoleVisibleTiles(nil, saved); len(edited) != len(saved) {
		t.Fatal("Edit Home included a temporary recent app tile")
	}
}
