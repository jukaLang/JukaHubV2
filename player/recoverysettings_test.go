package main

// Tests for the settings half of recovery: where the last good settings are
// found, how the card dates them, and the restart contract with launch.sh.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryFindSettingsSourcesNewestFirst(t *testing.T) {
	appDir, exportDir := t.TempDir(), t.TempDir()
	touch := func(path string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)

	lastGood := t.TempDir()
	if got := recoveryFindSettingsSources(appDir, exportDir, lastGood); len(got) != 0 {
		t.Fatalf("nothing saved yet, got %+v", got)
	}

	touch(filepath.Join(appDir, "jukauser.json.bak"), day.Add(48*time.Hour))
	got := recoveryFindSettingsSources(appDir, exportDir, "")
	if len(got) != 1 || got[0].Kind != recoverySourceBak || got[0].Path != appDir {
		t.Fatalf("the .bak alone: %+v", got)
	}

	// Two weekly backups and a manual one: only the newest weekly counts, and
	// it is newer than the .bak.
	touch(filepath.Join(exportDir, "JukaHub-backup-brick-20261101-1200-auto"+bkupExt), day)
	newest := filepath.Join(exportDir, "JukaHub-backup-brick-20261108-1200-auto"+bkupExt)
	touch(newest, day.Add(7*24*time.Hour))
	touch(filepath.Join(exportDir, "JukaHub-backup-brick-20261109-1200"+bkupExt), day.Add(8*24*time.Hour))
	got = recoveryFindSettingsSources(appDir, exportDir, lastGood)
	if len(got) != 2 {
		t.Fatalf("want the .bak and the newest weekly backup, got %+v", got)
	}
	if got[0].Kind != recoverySourceWeekly || got[0].Path != newest {
		t.Fatalf("newest first: %+v", got)
	}
	if got[1].Kind != recoverySourceBak {
		t.Fatalf("the .bak comes second: %+v", got)
	}

	// What the last healthy start read leads, even when it is the oldest: the
	// others were written by saves, and a save may be what broke the start.
	touch(filepath.Join(lastGood, "jukaconfig.json"), day)
	got = recoveryFindSettingsSources(appDir, exportDir, lastGood)
	if len(got) != 3 || got[0].Kind != recoverySourceLastGood || got[0].Path != lastGood ||
		got[1].Kind != recoverySourceWeekly || got[2].Kind != recoverySourceBak {
		t.Fatalf("the last good start first, then newest first: %+v", got)
	}
}

func TestSafetyBackupPruneKeepsTheNewOne(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mod time.Time) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	for i := 0; i < bkupSafetyKeep; i++ {
		write("JukaHub-safety-2026110"+string(rune('1'+i))+"-1000"+bkupExt, good.Add(time.Duration(i)*time.Hour))
	}
	// Written after a cold boot without an RTC: the oldest by its time.
	fresh := write("JukaHub-safety-20200101-0000"+bkupExt, time.Date(2020, 1, 1, 0, 0, 30, 0, time.Local))
	bkupPruneKeep(dir, "JukaHub-safety-", "", bkupSafetyKeep, fresh)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the backup just written must survive the prune: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != bkupSafetyKeep {
		t.Fatalf("kept %d, want %d", len(entries), bkupSafetyKeep)
	}
	if _, err := os.Stat(filepath.Join(dir, "JukaHub-safety-20261101-1000"+bkupExt)); !os.IsNotExist(err) {
		t.Fatalf("the oldest dated backup should have gone: %v", err)
	}
}

func TestRecoveryWhenHidesAnUnsetClock(t *testing.T) {
	if got := recoveryWhen(time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC)); got != "" {
		t.Fatalf("a factory-epoch time must not be shown, got %q", got)
	}
	if got := recoveryWhen(time.Time{}); got != "" {
		t.Fatalf("no time, got %q", got)
	}
	if got := recoveryWhen(time.Date(2026, 11, 9, 14, 3, 0, 0, time.Local)); got != " · 9 Nov 14:03" {
		t.Fatalf("got %q", got)
	}
}

func TestLaunchScriptHonoursTheRestartRequest(t *testing.T) {
	data, err := os.ReadFile("launch.sh")
	if err != nil {
		t.Skipf("launch.sh not next to the package: %v", err)
	}
	script := string(data)
	for _, want := range []string{recoveryRestartToken, recoveryRestartMarker, "CRASHED"} {
		if !strings.Contains(script, want) {
			t.Errorf("launch.sh does not carry %q; the app would never use it", want)
		}
	}
}

func TestSettingsRestoreAllDefaultsRequiresConfirmation(t *testing.T) {
	section := setSections()[setSectionIndex("about")]
	var restore *setRow
	for i := range section.Rows {
		if section.Rows[i].ID == "restoreDefaults" {
			restore = &section.Rows[i]
			break
		}
	}
	if restore == nil || restore.Label != "Reset" || restore.Kind != setKindAction || restore.Open == nil {
		t.Fatal("Settings → About must offer Reset as an action")
	}

	before := recoveryCard
	defer func() { recoveryCard = before }()
	restore.Open(&Config{})
	if !recoveryCard.open || recoveryCard.title != "Reset" || len(recoveryCard.options) != 3 {
		t.Fatalf("the Settings action must open the Reset choice: %+v", recoveryCard.options)
	}
	// Reset settings asks twice; both questions open on Cancel.
	recoveryCard.options[0].run(&Config{})
	for _, title := range []string{"Reset settings?", "Reset settings now?"} {
		if !recoveryCard.open || recoveryCard.title != title || recoveryCard.focus != 0 ||
			len(recoveryCard.options) != 2 || recoveryCard.options[0].title != "Cancel" {
			t.Fatalf("%q must be asked with Cancel focused: %q %+v", title, recoveryCard.title, recoveryCard.options)
		}
		if title == "Reset settings?" {
			recoveryCard.options[1].run(&Config{})
		}
	}
	recoveryCard.options[0].run(&Config{})
	if !recoveryCard.open || recoveryCard.title != "Reset" {
		t.Fatal("Cancel must go back to the Reset choice without resetting settings")
	}
}

// TestFactoryErasePlanNeverReachesGamesSavesOrMedia: "Erase everything JukaHub
// created" lists JukaHub's own stores and caches, never a ROM, BIOS, save or
// media folder, nothing outside JukaHub's folders, and leaves them in place.
func TestFactoryErasePlanNeverReachesGamesSavesOrMedia(t *testing.T) {
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(appDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	erased := []string{
		"jukauser.json", "jukauser.json.bak", "jukaconfig.json.bak", "jukaalarm.json", "notes.json", "jukahub.cache",
		"data/game-recents.json", "data/vk-words.json.bak", "data/retroachievements.json", "data/gdrive.json",
		"data/input-profiles/pad.json", "data/game-meta/x.json", "data/voice-cache/a.wav", "data/peer/identity.json",
		"data/steam/account.json", "data/cheats/cheats.zip", "data/vpn.json",
	}
	kept := []string{
		"JukaHub", "launch.sh", "jukaconfig.json", "jukatimezone", "config.json", "themes/t.json", "plugins/p/plugin.json",
		"Roms/GBA/game.gba", "BIOS/scph1001.bin", "saves/game.srm",
		"data/Roms/a.zip", "data/BIOS/b.bin", "data/RetroArch/saves/x.srm", "data/retroarch/states/x.state",
		"data/save-manager/x.srm", "data/.minecraft/saves/world/level.dat", "data/HTML5/game/save.json",
		"data/PICO-8/cdata/c.p8d.txt", "data/downloads/song.mp3", "data/wallpapers/w.png", "data/timelapses/t.mp4",
		"data/video/v.json", "data/game-art/a.png", "data/backups/JukaHub-safety-x.jkbackup", "data/versions/v1/JukaHub",
		"data/patch/state.json", "data/logs/l.txt", "data/emulators/ppsspp/memstick/SAVEDATA/s.bin",
		"data/windows-apps/prefix/user.reg", "data/androidrt/data/app.db", "data/ssync/exports/e.zip",
		"data/reset-trash/x.state", "data/bootguard.json", "data/release-info.json", "data/packs/p.json",
		"data/steam/library.json", "data/notes.txt", "data/cheats/SNES/mine.cht", "data/battery_cal.json",
		"data/vpn-resolv.json",
	}
	for _, rel := range append(append([]string(nil), erased...), kept...) {
		write(rel)
	}

	plan := factoryErasePlan(appDir, dataDir)
	protected := []string{"rom", "bios", "save", "state", "minecraft", "html5", "pico", "retroarch", "download",
		"wallpaper", "timelapse", "video", "music", "photo", "screenshot", "media", "art", "backup", "version",
		"patch", "log", "emulator", "windows", "android", "theme", "plugin", "pack"}
	for _, p := range plan {
		if !isPathWithin(p, appDir) || filepath.Clean(p) == filepath.Clean(appDir) || filepath.Clean(p) == filepath.Clean(dataDir) {
			t.Errorf("plan reaches outside JukaHub's folders or a whole folder: %s", p)
		}
		rel, _ := filepath.Rel(appDir, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for _, part := range parts {
			lower := strings.ToLower(part)
			for _, word := range protected {
				// The media index (posters, not media) is the one exception.
				if strings.Contains(lower, word) && lower != "medialibrary" {
					t.Errorf("plan names a protected folder or file (%q): %s", word, rel)
				}
			}
		}
	}

	// The undo file holds every file the erase takes away, and the design file.
	outDir := t.TempDir()
	archive, err := factoryEraseBackup(context.Background(), outDir, "JukaHub-erased-", appDir, dataDir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	m, err := bkupVerify(archive)
	if err != nil || m.Label != factoryEraseLabel || len(m.Files) != len(erased)+1 {
		t.Fatalf("undo file: %d files, label %q, err %v; want %d", len(m.Files), m.Label, err, len(erased)+1)
	}

	// The start erases only when the marker names this device's undo file of
	// an erase: not without one, not when it is gone, not another backup.
	marker := func(path string) []byte { return []byte(factoryEraseToken + "\narchive: " + path + "\n") }
	if err := factoryEraseArchiveReady(marker(archive)); err != nil {
		t.Fatalf("a marker naming its undo file must erase: %v", err)
	}
	other := filepath.Join(outDir, "JukaHub-backup-x"+bkupExt)
	if _, err := bkupWrite(context.Background(), other, nil, nil,
		bkupManifest{Format: bkupFormat, Version: bkupVersion, Platform: P().Name(), Label: "weekly"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(factoryEraseToken + "\n"), marker(filepath.Join(outDir, "gone"+bkupExt)), marker(other)} {
		if factoryEraseArchiveReady(bad) == nil {
			t.Errorf("this marker must not erase: %q", bad)
		}
	}

	removed, failed := factoryEraseMove(plan, appDir, dataDir)
	if len(failed) > 0 || removed != len(plan) {
		t.Fatalf("removed %d of %d, failed %v", removed, len(plan), failed)
	}
	for _, rel := range erased {
		if _, err := os.Stat(filepath.Join(appDir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s should be erased (stat err %v)", rel, err)
		}
	}
	for _, rel := range kept {
		if _, err := os.Stat(filepath.Join(appDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s must stay: %v", rel, err)
		}
	}
	// A second run (a power cut before the marker went) finds nothing to do.
	if again := factoryErasePlan(appDir, dataDir); len(again) != 0 {
		t.Errorf("a second erase would take %v", again)
	}

	// The undo puts every file back byte for byte, and writes nothing an
	// erase does not take away.
	written, skipped, err := factoryUndoWriteArchive(archive, appDir, dataDir)
	if err != nil || skipped != 0 || written != len(erased)+1 {
		t.Fatalf("undo wrote %d, left out %d, err %v; want %d", written, skipped, err, len(erased)+1)
	}
	for _, rel := range erased {
		if data, err := os.ReadFile(filepath.Join(appDir, filepath.FromSlash(rel))); err != nil || string(data) != rel {
			t.Errorf("%s came back as %q (%v)", rel, data, err)
		}
	}
	for _, target := range []string{"app:launch.sh", "app:JukaHub", "app:" + factoryEraseMarker, "app:" + factoryUndoMarker,
		"data:bootguard.json", "data:vpn-resolv.json", "data:emulators/run.sh", "data:cheats/SNES/mine.cht", "data:game-meta"} {
		token, rel, _ := strings.Cut(target, ":")
		if factoryUndoAllowed(token, rel) {
			t.Errorf("an undo may write %s", target)
		}
	}

	// A data folder that is a drive or file-system root gets nothing.
	if p := factoryErasePlan("", string(filepath.Separator)); len(p) != 0 {
		t.Errorf("data folder = root: plan %v", p)
	}

	// A data folder that is JukaHub's own folder gets only the named folders:
	// the *.json pattern would reach the program's files (config.json).
	for _, p := range factoryErasePlan(appDir, appDir) {
		if filepath.Base(p) == "config.json" {
			t.Errorf("data folder = app folder: plan reaches the program's own %s", p)
		}
	}
}

// TestFactoryEraseTrashNameIsBounded: the erase runs before anything is shown,
// so finding a free name in the trash must end — numbered on a clash, and
// giving up after its tries (the item is then removed in place).
func TestFactoryEraseTrashNameIsBounded(t *testing.T) {
	trash := t.TempDir()
	for _, name := range []string{"notes.json", "notes.json-2"} {
		if err := os.WriteFile(filepath.Join(trash, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := factoryEraseFreeName(trash, "notes.json", 3); !ok || got != filepath.Join(trash, "notes.json-3") {
		t.Fatalf("got %q %v, want notes.json-3", got, ok)
	}
	if got, ok := factoryEraseFreeName(trash, "notes.json", 2); ok {
		t.Fatalf("two names taken, two tries: got %q, want none", got)
	}
	if got, ok := factoryEraseFreeName(trash, "game-meta", factoryEraseTrashTries); !ok || got != filepath.Join(trash, "game-meta") {
		t.Fatalf("a free name: got %q %v", got, ok)
	}
}
