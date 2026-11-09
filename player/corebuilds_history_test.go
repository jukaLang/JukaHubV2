package main

// corebuilds_history_test.go — LIB-16: the Core updates setting migrates the
// old switch the way the user meant it, and a game's save states are flagged
// when a build other than the installed one wrote them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoreUpdateModeMigratesTheOldSwitch(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]interface{}
		want   coreUpdateMode
	}{
		{"nothing stored: the new default", nil, coreUpdateNotify},
		{"explicit off stays off", map[string]interface{}{"emuAutoUpdate": false}, coreUpdateOff},
		{"explicit on was chosen", map[string]interface{}{"emuAutoUpdate": true}, coreUpdateInstall},
		{"off as text", map[string]interface{}{"emuAutoUpdate": "off"}, coreUpdateOff},
		{"on as text", map[string]interface{}{"emuAutoUpdate": "true"}, coreUpdateInstall},
		{"zero from JSON", map[string]interface{}{"emuAutoUpdate": 0.0}, coreUpdateOff},
		{"the mode wins", map[string]interface{}{"coreUpdateMode": "notify", "emuAutoUpdate": true}, coreUpdateNotify},
		{"install mode", map[string]interface{}{"coreUpdateMode": "install"}, coreUpdateInstall},
		{"off mode", map[string]interface{}{"coreUpdateMode": "off", "emuAutoUpdate": true}, coreUpdateOff},
		{"unknown mode, no switch", map[string]interface{}{"coreUpdateMode": "bogus"}, coreUpdateNotify},
		{"unknown mode, switch off", map[string]interface{}{"coreUpdateMode": "bogus", "emuAutoUpdate": false}, coreUpdateOff},
	}
	for _, c := range cases {
		if got := coreUpdateModeFrom(c.custom); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got.key(), c.want.key())
		}
	}
	for _, m := range coreUpdateModes {
		if back, ok := parseCoreUpdateMode(m.key()); !ok || back != m {
			t.Errorf("mode %s does not round-trip", m.key())
		}
	}
}

func TestCoreUpdateAnnouncementNamesTheCores(t *testing.T) {
	stale := map[string]coreIndexEntry{
		"snes9x": {CRC32: "22222222"},
		"mgba":   {CRC32: "11111111"},
	}
	if got := coreUpdateSignature(stale); got != "mgba@11111111,snes9x@22222222" {
		t.Errorf("signature = %q", got)
	}
	msg := coreUpdateNotifyMessage(stale)
	if !strings.HasPrefix(msg, "Different builds are available for ") || !strings.Contains(msg, "kept") || strings.Contains(msg, "ewer") {
		t.Errorf("message = %q", msg)
	}
	delete(stale, "snes9x")
	if msg := coreUpdateNotifyMessage(stale); !strings.HasPrefix(msg, "A different build is available for ") {
		t.Errorf("one core: message = %q", msg)
	}
}

func TestCoreNamesInLaunchScripts(t *testing.T) {
	cases := []struct {
		script string
		want   string
	}{
		{"cd /mnt/SDCARD/RetroArch\nHOME=/mnt/SDCARD/RetroArch ./ra64.trimui -L .retroarch/cores/mgba_libretro.so \"$1\"", "mgba"},
		{"CORE=${CORE}_libretro.so\n./retroarch -L $CORE \"$1\"", ""},
		{"if [ x ]; then C=gpsp_libretro.so; else C=MGBA_libretro.so; fi\nC2=gpsp_libretro.so", "gpsp,mgba"},
		{"./retroarch -L cores/snes9x_libretro.dll game.sfc", "snes9x"},
		{"echo nothing here_libretro.txt", ""},
	}
	for _, c := range cases {
		if got := strings.Join(coreNamesIn(c.script), ","); got != c.want {
			t.Errorf("coreNamesIn(%q) = %q, want %q", c.script, got, c.want)
		}
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("./ra64 -L cores/gpsp_libretro.so -L cores/mgba_libretro.so \"$1\""), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gameCoreFromLauncher(launcher, "Game Boy Advance"); got != "mgba" {
		t.Errorf("two cores in a script: got %q, want the system's usual core", got)
	}
	if got := gameCoreFromLauncher(filepath.Join(dir, "missing.sh"), "SNES"); got != "snes9x" {
		t.Errorf("no script: got %q, want the system's usual core", got)
	}
	if got := gameCoreFromLauncher(filepath.Join(dir, "fceumm_libretro.dll"), "SNES"); got != "fceumm" {
		t.Errorf("a core launched directly: got %q", got)
	}
}

func TestCoreHistoryJudgesStatesByTheirTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-history.json")
	const core = "jukahub_history_test"

	updated := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	coreHistoryRecord(path, core, "aaaa1111", "bbbb2222", "2026-10-31", updated)
	coreHistoryRecord(path, core, "aaaa1111", "bbbb2222", "2026-10-31", updated.Add(time.Minute))
	list := coreHistoryForAt(path, core)
	if len(list) != 2 || list[0].CRC32 != "aaaa1111" || list[0].From != "" || list[1].CRC32 != "bbbb2222" || list[1].Build != "2026-10-31" {
		t.Fatalf("history = %+v", list)
	}

	before := gmetaState{Slot: 1, Time: updated.Add(-time.Hour)}
	after := gmetaState{Slot: -1, Time: updated.Add(time.Hour)}
	if e, ok := coreBuildAt(list, before.Time); !ok || e.CRC32 != "aaaa1111" {
		t.Errorf("before the update: %+v %v", e, ok)
	}
	if e, ok := coreBuildAt(list, after.Time); !ok || e.CRC32 != "bbbb2222" {
		t.Errorf("after the update: %+v %v", e, ok)
	}

	// The installed build is the update: the older state is flagged, the newer
	// one is not.
	note := coreStatesNoteFor("mGBA", "bbbb2222", updated, list, []gmetaState{after, before})
	if note == "" || !strings.HasPrefix(note, "1 save state was made with another mGBA build") {
		t.Errorf("note = %q", note)
	}
	if got := coreStatesNoteFor("mGBA", "bbbb2222", updated, list, []gmetaState{after}); got != "" {
		t.Errorf("a state made with the installed build was flagged: %q", got)
	}

	// Rolled back to the first build: now the newer state is the odd one, and
	// its build has a name.
	rolled := updated.Add(2 * time.Hour)
	coreHistoryRecord(path, core, "bbbb2222", "aaaa1111", "", rolled)
	list = coreHistoryForAt(path, core)
	note = coreStatesNoteFor("mGBA", "aaaa1111", rolled, list, []gmetaState{after, before})
	if !strings.Contains(note, "the 2026-10-31 mGBA build") {
		t.Errorf("after the rollback, note = %q", note)
	}

	// Nothing recorded for a core: nothing is judged.
	if got := coreStatesNoteFor("Snes9x", "cccc3333", time.Time{}, nil, []gmetaState{before}); got != "" {
		t.Errorf("a core with no history was judged: %q", got)
	}
}

// The handheld has no RTC battery: before NTP its clock can read 1970. A
// replacement made then is not recorded, a state stamped then is not judged,
// and a binary stamped then makes no state judgeable.
func TestCoreHistoryIgnoresAClockThatWasWrong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-history.json")
	const core = "jukahub_clock_test"
	updated := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	boot := time.Unix(3600, 0)
	coreHistoryRecord(path, core, "aaaa1111", "bbbb2222", "2026-10-31", updated)
	coreHistoryRecord(path, core, "bbbb2222", "cccc3333", "", boot)
	list := coreHistoryForAt(path, core)
	if len(list) != 2 || list[1].CRC32 != "bbbb2222" {
		t.Fatalf("a replacement stamped %s was recorded: %+v", boot.UTC(), list)
	}
	old := gmetaState{Slot: 1, Time: updated.Add(-time.Hour)}
	if coreStatesNoteFor("mGBA", "bbbb2222", updated, list, []gmetaState{old}) == "" {
		t.Fatal("the control case was not flagged")
	}
	if got := coreStatesNoteFor("mGBA", "bbbb2222", updated, list, []gmetaState{{Slot: 2, Time: boot}}); got != "" {
		t.Errorf("a state stamped before 2020 was judged: %q", got)
	}
	if got := coreStatesNoteFor("mGBA", "bbbb2222", boot, list, []gmetaState{old}); got != "" {
		t.Errorf("states were judged against a binary stamped before 2020: %q", got)
	}
}

// Replacements are written in the order they happened, with the checksums the
// copy computed, even when they follow each other at once (a rollback and its
// undo); the writes are queued, and the flush waits for them.
func TestCoreHistoryWritesReplacementsInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-history.json")
	prev := coreHistoryPathFn
	coreHistoryPathFn = func() string { return path }
	t.Cleanup(func() { coreHistoryPathFn = prev })
	target := filepath.Join(dir, "jukahub_fifo_test_libretro.so")
	coreHistoryNoteReplaced("", target, "aaaa1111", "bbbb2222")
	coreHistoryNoteReplaced("", target, "bbbb2222", "aaaa1111")
	coreHistoryNoteReplaced("", target, "aaaa1111", "bbbb2222")
	coreHistoryFlush()
	var got []string
	for _, e := range coreHistoryForAt(path, "jukahub_fifo_test") {
		got = append(got, e.CRC32)
	}
	if strings.Join(got, ",") != "aaaa1111,bbbb2222,aaaa1111,bbbb2222" {
		t.Fatalf("history = %v", got)
	}
}

// The buildbot index is "<date> <crc32> <file>"; a fourth column (a size) is
// accepted on either side of the file name, and only when it is a number.
func TestParseCoreIndexAcceptsASizeColumn(t *testing.T) {
	text := strings.Join([]string{
		"2026-10-23 16647efd mgba_libretro.so.zip 1234567",
		"2026-10-23 9e7737e6 7654321 snes9x_libretro.so.zip",
		"2026-10-23 aabbccdd gpsp_libretro.so.zip 12ab",
		"2026-10-23 aabbccdd fceumm_libretro.so.zip",
	}, "\n")
	got := map[string]string{}
	for _, e := range parseCoreIndex(text) {
		got[e.Core] = e.CRC32 + " " + e.File
	}
	want := map[string]string{
		"mgba":   "16647efd mgba_libretro.so.zip",
		"snes9x": "9e7737e6 snes9x_libretro.so.zip",
		"fceumm": "aabbccdd fceumm_libretro.so.zip",
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for core, w := range want {
		if got[core] != w {
			t.Errorf("%s = %q, want %q", core, got[core], w)
		}
	}
}
