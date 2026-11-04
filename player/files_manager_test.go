package main

// files_manager_test.go — the Files screen's manager and the device-to-device
// transfer address parser. Everything here works on a real temporary tree: the
// operations are filesystem work, and a mock would only test the mock.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// feTestConfig builds a Config whose Files root is dir, with the entry map the
// listing code writes into.
func feTestConfig(dir string) *Config {
	return &Config{Variables: Variables{
		FileExplorerRoot: dir,
		Custom:           map[string]interface{}{"fe_path": dir},
	}}
}

// feTestTree builds a small tree and returns (root, subfolder).
func feTestTree(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	sub := filepath.Join(root, "roms", "gba")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "saves"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sub, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-small.txt", 16)
	write("b-big.gba", 4096)
	write("c-mid.mp3", 512)
	return root, sub
}

// withFEGlobals saves and restores the screen's package state, which is shared
// by every probe in the process.
func withFEGlobals(t *testing.T) {
	t.Helper()
	prevFocus := focusedFileIndex
	t.Cleanup(func() {
		focusedFileIndex = prevFocus
		feSelectionClear()
		feClipboardClear()
		feSheet = feSheetState{}
		feRemotePanel = feRemotePanelState{}
	})
	focusedFileIndex = 0
	feSelectionClear()
	feClipboardClear()
}

func TestFEValidateName(t *testing.T) {
	good := map[string]string{
		"  notes.txt ": "notes.txt",
		"ROM folder":   "ROM folder",
		"saves":        "saves",
	}
	for in, want := range good {
		got, err := feValidateName(in)
		if err != nil {
			t.Errorf("%q was rejected: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q became %q, want %q", in, got, want)
		}
	}
	bad := []string{"", "   ", ".", "..", "a/b", `a\b`, "x\x00y"}
	for _, in := range bad {
		if got, err := feValidateName(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

func TestFESortEntriesKeepsFoldersFirst(t *testing.T) {
	now := time.Now()
	entries := []FileEntry{
		{Name: "b.txt", Size: 10, Mod: now.Add(-time.Hour)},
		{Name: "sub", IsDir: true},
		{Name: "..", IsDir: true},
		{Name: "A.txt", Size: 30, Mod: now},
		{Name: "c.txt", Size: 20, Mod: now.Add(-2 * time.Hour)},
	}

	mode := feSortName
	feSortEntries(entries, mode)
	if entries[0].Name != ".." || !entries[1].IsDir {
		t.Fatalf("parent and folders must lead the list, got %q then %q", entries[0].Name, entries[1].Name)
	}
	if entries[2].Name != "A.txt" || entries[4].Name != "c.txt" {
		t.Fatalf("names are not in case-insensitive order: %v", names(entries))
	}

	feSortEntries(entries, feSortSize)
	if entries[2].Name != "A.txt" || entries[3].Name != "c.txt" || entries[4].Name != "b.txt" {
		t.Fatalf("size order is wrong: %v", names(entries))
	}

	feSortEntries(entries, feSortDate)
	if entries[2].Name != "A.txt" || entries[3].Name != "b.txt" || entries[4].Name != "c.txt" {
		t.Fatalf("newest-first order is wrong: %v", names(entries))
	}
}

func names(entries []FileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestFEListDirectorySortsAndTotals(t *testing.T) {
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	entries := feEntriesOf(config)
	if len(entries) != 4 { // "..", three files
		t.Fatalf("got %d entries, want 4: %v", len(entries), names(entries))
	}
	if entries[0].Name != ".." {
		t.Fatalf("the parent entry must lead the list, got %q", entries[0].Name)
	}
	for _, entry := range entries[1:] {
		if entry.Size == 0 || entry.Mod.IsZero() {
			t.Errorf("%q has no size or date: %+v", entry.Name, entry)
		}
	}
	count, bytes := feDirTotals(entries)
	if count != 3 || bytes != 16+4096+512 {
		t.Fatalf("totals are %d items / %d bytes, want 3 / %d", count, bytes, 16+4096+512)
	}
}

func TestFECopyPasteLeavesTheOriginal(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	feClipboardSet([]string{filepath.Join(sub, "b-big.gba")}, false)
	dest := filepath.Join(root, "saves")
	config.Variables.Custom["fe_path"] = dest
	feListDirectory(config)

	done, err := fePaste(config)
	if err != nil || done != 1 {
		t.Fatalf("paste copied %d items, err %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b-big.gba")); err != nil {
		t.Fatalf("the copy is not in the destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "b-big.gba")); err != nil {
		t.Fatalf("a copy must leave the original: %v", err)
	}
}

func TestFECutPasteMoves(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	dest := filepath.Join(root, "saves")
	source := filepath.Join(sub, "c-mid.mp3")

	feClipboardSet([]string{source}, true)
	config.Variables.Custom["fe_path"] = dest
	feListDirectory(config)

	done, err := fePaste(config)
	if err != nil || done != 1 {
		t.Fatalf("move pasted %d items, err %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "c-mid.mp3")); err != nil {
		t.Fatalf("the moved file is not at the destination: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("a move must remove the source, stat says %v", err)
	}
	if !feClipboardEmpty() {
		t.Fatal("a cut is consumed by its paste")
	}
}

func TestFEPasteNeverOverwrites(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	dest := filepath.Join(root, "saves")
	feClipboardSet([]string{filepath.Join(sub, "b-big.gba")}, false)

	config.Variables.Custom["fe_path"] = dest
	feListDirectory(config)
	if _, err := fePaste(config); err != nil {
		t.Fatal(err)
	}
	// A second paste of the same name is a second copy, named apart, never a
	// silent replacement of the first.
	if _, err := fePaste(config); err != nil {
		t.Fatalf("the second paste failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b-big.gba")); err != nil {
		t.Fatalf("the first copy is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b-big (2).gba")); err != nil {
		t.Fatalf("the second copy did not get its own name: %v", err)
	}
}

func TestFERenameAndNewFolder(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	renamed, err := feRenameEntry(config, filepath.Join(sub, "a-small.txt"), "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(renamed) != "renamed.txt" {
		t.Fatalf("renamed to %q", renamed)
	}
	if _, err := feRenameEntry(config, renamed, "sub/child.txt"); err == nil {
		t.Fatal("a name with a separator was accepted")
	}

	dir, err := feNewFolder(config, "new folder")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("new folder is not there: %v", err)
	}
	if _, err := feNewFolder(config, "new folder"); err == nil {
		t.Fatal("an existing name was reused for a new folder")
	}
}

func TestFEDeleteRefusesTheRootAndRemovesFiles(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	if _, err := feResolveEntry(config, root); err == nil {
		t.Fatal("the browsable root resolved as a deletable entry")
	}
	target := filepath.Join(sub, "a-small.txt")
	if err := feDeleteEntry(config, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
}

func TestFESelectionDrivesTheActions(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	// Nothing marked and the cursor on "..": nothing is a target. That row's
	// path is the parent folder, and Delete on it removed the folder above.
	if got := feSelectionTargets(config); len(got) != 0 {
		t.Fatalf("the .. row became a target: %v", names(got))
	}
	for _, action := range feActions(config) {
		if (action.Kind == feActDelete || action.Kind == feActCut || action.Kind == feActCopy) && action.Enabled {
			t.Errorf("%s is offered with the cursor on ..", action.Label)
		}
	}
	if feToggleSelectionOnFocused(config); feSelectionCount() != 0 {
		t.Fatal("X marked the .. row")
	}
	if err := feDeleteEntry(config, filepath.Dir(sub)); err == nil {
		t.Fatal("deleting the folder above the current one was allowed")
	}
	if _, err := os.Stat(filepath.Join(root, "roms")); err != nil {
		t.Fatalf("the parent folder is gone: %v", err)
	}
	// Nothing marked: the actions apply to the row under the cursor.
	focusedFileIndex = 1
	if got := feSelectionTargets(config); len(got) != 1 || got[0].Name == ".." {
		t.Fatalf("cursor target is %v", names(got))
	}
	focusedFileIndex = 0
	// Marking a row makes it the target, and only it.
	entry := feEntriesOf(config)[1]
	feSelectionToggle(entry.Path)
	targets := feSelectionTargets(config)
	if len(targets) != 1 || targets[0].Path != entry.Path {
		t.Fatalf("selection target is %v", names(targets))
	}

	for _, action := range feActions(config) {
		switch action.Kind {
		case feActPaste:
			if action.Enabled {
				t.Error("paste is offered with an empty clipboard")
			}
		case feActRename:
			if !action.Enabled {
				t.Error("rename is refused for a single marked row")
			}
		case feActRemote:
			if action.Enabled {
				t.Error("remote files is offered with no device")
			}
		}
	}

	// Marking a second row disables rename, which works on one at a time.
	feSelectionToggle(feEntriesOf(config)[2].Path)
	for _, action := range feActions(config) {
		if action.Kind == feActRename && action.Enabled {
			t.Fatal("rename is offered for two marked rows")
		}
	}

	if marked := feSelectAll(config); marked != 3 {
		t.Fatalf("select all marked %d rows, want 3", marked)
	}
	if got := feSelectionCount(); got != 3 {
		t.Fatalf("selection count is %d", got)
	}
}

func TestFESheetDeleteNeedsTwoPresses(t *testing.T) {
	withFEGlobals(t)
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	target := filepath.Join(sub, "b-big.gba")
	feSelectionToggle(target)
	openFeSheet(config)

	// Point the sheet at Delete and press once: the file must survive.
	for i, action := range feActions(config) {
		if action.Kind == feActDelete {
			feSheet.cursor = i
		}
	}
	feSheetActivate(config)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("one press deleted the file: %v", err)
	}
	if !feSheetArmed("delete") {
		t.Fatal("the first press did not arm the delete")
	}
	feSheetActivate(config)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the second press did not delete the file: %v", err)
	}
	if feSheet.open {
		t.Fatal("the sheet stayed open after a delete")
	}
}

func TestFECrumbFitKeepsTheRootAndTheOpenFolder(t *testing.T) {
	font := testInterFont(t, 18)
	crumbs := []feCrumb{
		{Label: "SDCARD", Path: "/mnt/SDCARD"},
		{Label: "roms", Path: "/mnt/SDCARD/roms"},
		{Label: "gba", Path: "/mnt/SDCARD/roms/gba"},
		{Label: "Pokemon", Path: "/mnt/SDCARD/roms/gba/Pokemon"},
	}
	if got := feCrumbFit(font, crumbs, 4000); len(got) != len(crumbs) {
		t.Fatalf("a wide bar dropped %d crumbs", len(crumbs)-len(got))
	}
	fit := feCrumbFit(font, crumbs, 120)
	if len(fit) >= len(crumbs) {
		t.Fatalf("a narrow bar kept every crumb: %v", fit)
	}
	if fit[0].Label != "SDCARD" {
		t.Fatalf("the root crumb was dropped, list is %v", fit)
	}
	if fit[len(fit)-1].Label != "Pokemon" {
		t.Fatalf("the open folder was dropped, list is %v", fit)
	}
	elided := false
	for _, crumb := range fit {
		if crumb.Label == "…" && crumb.Path == "" {
			elided = true
		}
	}
	if !elided {
		t.Fatalf("the gap is not marked: %v", fit)
	}
}

func TestFECrumbsWalkFromTheRoot(t *testing.T) {
	root, sub := feTestTree(t)
	config := feTestConfig(root)
	config.Variables.Custom["fe_path"] = sub
	crumbs := feCrumbs(config)
	if len(crumbs) != 3 {
		t.Fatalf("got %d crumbs, want root, roms, gba: %v", len(crumbs), crumbs)
	}
	if crumbs[0].Path != feRoot(config) || crumbs[2].Path != sub {
		t.Fatalf("crumbs point at the wrong folders: %v", crumbs)
	}
	if !strings.EqualFold(crumbs[0].Label, filepath.Base(root)) {
		t.Fatalf("the root crumb is labelled %q, want the card's name", crumbs[0].Label)
	}
	// Parent is not part of the path bar: the root crumb is the way up.
	for _, crumb := range crumbs {
		if crumb.Label == ".." {
			t.Fatalf("the bar contains a parent crumb: %v", crumbs)
		}
	}
}

func TestFEEntryBadgesAndMeta(t *testing.T) {
	now := time.Now()
	cases := []struct {
		entry  FileEntry
		badge  string
		wantIn string
	}{
		{FileEntry{Name: "cover.png"}, "IMG", ""},
		{FileEntry{Name: "track.mp3"}, "AUD", ""},
		{FileEntry{Name: "movie.mp4"}, "VID", ""},
		{FileEntry{Name: "manual.pdf"}, "PDF", ""},
		{FileEntry{Name: "patch.zip"}, "ZIP", ""},
		{FileEntry{Name: "readme.md"}, "TXT", ""},
		{FileEntry{Name: "roms", IsDir: true}, "DIR", "Folder"},
		{FileEntry{Name: "..", IsDir: true, Path: "/mnt/SDCARD/roms"}, "UP", "back to roms"},
		{FileEntry{Name: "big.gba", Size: 4096, Mod: now}, "FILE", humanBytes(4096)},
	}
	for _, c := range cases {
		badge, _ := feEntryBadge(c.entry)
		if badge != c.badge {
			t.Errorf("%q got badge %q, want %q", c.entry.Name, badge, c.badge)
		}
		if c.wantIn != "" && !strings.Contains(feEntryMeta(c.entry), c.wantIn) {
			t.Errorf("%q meta is %q, want it to mention %q", c.entry.Name, feEntryMeta(c.entry), c.wantIn)
		}
	}
	if title := feEntryTitle(FileEntry{Name: ".."}); title != "Parent folder" {
		t.Fatalf("the parent row is titled %q", title)
	}
}

func TestParseFeRemote(t *testing.T) {
	ok := []struct {
		in   string
		want feRemote
	}{
		{"192.168.1.7", feRemote{User: sshUserName, Host: "192.168.1.7", Port: sshDefaultPort, Path: "/"}},
		{"juka@192.168.1.7:2222", feRemote{User: "juka", Host: "192.168.1.7", Port: 2222, Path: "/"}},
		{"sftp://juka@10.0.0.5:22/roms/gba", feRemote{User: "juka", Host: "10.0.0.5", Port: 22, Path: "/roms/gba"}},
		{"juka@console.local:/saves", feRemote{User: "juka", Host: "console.local", Port: sshDefaultPort, Path: "/saves"}},
	}
	for _, c := range ok {
		got, err := parseFeRemote(c.in)
		if err != nil {
			t.Errorf("%q was rejected: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q parsed to %+v, want %+v", c.in, got, c.want)
		}
		if again, err := parseFeRemote(got.address()); err != nil || again != c.want {
			t.Errorf("%q did not survive its own address form: %+v %v", c.in, again, err)
		}
	}

	bad := []string{"", "   ", "ftp://host", "juka@:22", "host:0", "host:99999", "host:abc", "ju ka@host", "@host"}
	for _, in := range bad {
		if got, err := parseFeRemote(in); err == nil {
			t.Errorf("%q was accepted as %+v", in, got)
		}
	}
}

func TestFeRemoteAddressIsClean(t *testing.T) {
	target := feRemote{User: "juka", Host: "192.168.1.7", Port: 0, Path: "roms/../../etc"}
	if got := target.key(); got != "juka@192.168.1.7:2222" {
		t.Fatalf("key with no port is %q", got)
	}
	if got := target.address(); strings.Contains(got, "..") {
		t.Fatalf("address kept a traversal: %q", got)
	}
}

func TestFeRemoteProgressLine(t *testing.T) {
	t.Cleanup(func() { feRemoteSetProgress("", 0, 0) })
	feRemoteSetProgress("", 0, 0)
	if got := feRemoteProgressLine(); got != "" {
		t.Fatalf("an idle transfer reads %q", got)
	}
	feRemoteSetProgress("Downloading roms.zip", 1024, 4096)
	got := feRemoteProgressLine()
	if !strings.Contains(got, "25%") || !strings.Contains(got, "Downloading roms.zip") {
		t.Fatalf("progress line is %q", got)
	}
	feRemoteSetProgress("Uploading saves.tar", 2048, 0)
	if got := feRemoteProgressLine(); strings.Contains(got, "%") || !strings.Contains(got, humanBytes(2048)) {
		t.Fatalf("an unknown total invented a percentage: %q", got)
	}
}

func TestFeRemotePanelNavigation(t *testing.T) {
	withFEGlobals(t)
	feRemotePanel = feRemotePanelState{
		open: true,
		tgt:  feRemote{User: "juka", Host: "192.168.1.7", Port: 2222, Path: "/"},
		path: "/",
		entries: []feRemoteEntry{
			{Name: "..", IsDir: true},
			{Name: "roms", IsDir: true},
			{Name: "a.gba", Size: 10},
		},
	}
	feRemotePanelMove(1)
	if entry, ok := feRemoteFocus(); !ok || entry.Name != "roms" {
		t.Fatalf("the cursor did not land on the second row: %+v", entry)
	}
	feRemotePanelMove(-5)
	if feRemotePanel.cursor != 2 {
		t.Fatalf("moving up wrapped to %d, want the last row", feRemotePanel.cursor)
	}
	feRemotePanelMove(1)
	if feRemotePanel.cursor != 0 {
		t.Fatalf("moving down wrapped to %d, want the first row", feRemotePanel.cursor)
	}
	if got := feRemoteShortAddress(); !strings.Contains(got, "juka@192.168.1.7:2222") {
		t.Fatalf("panel subtitle is %q", got)
	}
}

func TestFeRemoteListSortsFoldersFirst(t *testing.T) {
	entries := []feRemoteEntry{
		{Name: "b.gba", Size: 10},
		{Name: "sub", IsDir: true},
		{Name: "a.gba", Size: 20},
		{Name: "..", IsDir: true},
	}
	sortRemoteEntries(entries)
	if entries[0].Name != ".." || !entries[1].IsDir {
		t.Fatalf("remote sort put %q first and %q second", entries[0].Name, entries[1].Name)
	}
	if entries[2].Name != "a.gba" || entries[3].Name != "b.gba" {
		t.Fatalf("remote names are out of order: %v", entries)
	}
}

// Both local and remote folder rows should draw the same recognizable folder
// silhouette in the type-chip slot without replacing the parent or selection.
func TestFEFolderIconsInListings(t *testing.T) {
	withFEGlobals(t)
	config := layoutTestConfig(t)
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("no font for the Files row")
	}
	entry := FileEntry{Name: "roms", Path: "roms", IsDir: true}
	const rowH int32 = 36
	checkFolder := func(f *probeFrame, x, y int32) {
		t.Helper()
		tab := f.at(x+14, y+3)
		outsideTab := f.at(x+30, y+3)
		body := f.at(x+23, y+13)
		if tab.R < 150 || tab.G < 120 || outsideTab.R > 130 || body.R < 230 || body.G < 190 || body.B < 90 || body.B > 150 {
			t.Errorf("folder silhouette missing: tab=%+v outside=%+v body=%+v", tab, outsideTab, body)
		}
	}
	local := componentFrame(t, func(r *sdl.Renderer) {
		renderFilesRow(r, config, font, entry, 0, 0, 300, rowH, false)
	})
	checkFolder(local, 12, (rowH-20)/2)

	feSelectionToggle(entry.Path)
	selected := componentFrame(t, func(r *sdl.Renderer) {
		renderFilesRow(r, config, font, entry, 0, 0, 300, rowH, false)
	})
	if pixel := selected.at(12+23, (rowH-20)/2+11); pixel.R > 60 || pixel.G > 60 || pixel.B > 60 {
		t.Errorf("selected folder should show the dark selection tick, got %+v", pixel)
	}
	feSelectionClear()

	feRemotePanel = feRemotePanelState{open: true, entries: []feRemoteEntry{{Name: entry.Name, Path: entry.Path, IsDir: true}}}
	remote := componentFrame(t, func(r *sdl.Renderer) { renderFeRemotePanel(r, config) })
	if len(feRemoteRowHits) != 1 {
		t.Fatalf("remote listing has %d row hitboxes, want 1", len(feRemoteRowHits))
	}
	rect := feRemoteRowHits[0].rect
	checkFolder(remote, rect.X+12, rect.Y+(rect.H-20)/2)
}
