package main

// files_locations_test.go — the Files screen's way around: the ".." row on
// every folder (the Files root included), the Locations list above it, B and
// Left stepping up, the cursor coming back to the folder just left, hidden
// files, unreadable folders and the system-folder guard. All of it runs on a
// real temporary tree.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// withFilesNav gives a test a clean navigation state and puts the package's
// back afterwards: the Locations state is session-global.
func withFilesNav(t *testing.T) {
	t.Helper()
	prevNav := filesNav
	prevFocus := focusedFileIndex
	prevScene := currentSceneIndex
	prevSelected := selectedButtonIndex
	prevSheet := feSheet
	t.Cleanup(func() {
		filesNav = prevNav
		focusedFileIndex = prevFocus
		currentSceneIndex = prevScene
		selectedButtonIndex = prevSelected
		feSheet = prevSheet
		feSelectionClear()
	})
	filesNav = filesNavState{}
	focusedFileIndex = 0
	feSelectionClear()
}

// filesNavConfig is a Files scene over root.
func filesNavConfig(root string) *Config {
	config := feTestConfig(root)
	config.Scenes = []SceneConfig{{Name: "FileExplorer", Elements: []Element{
		{Type: "dynamiclist", Variable: "fe_entries", X: 24, Y: 124, Width: "1232", Height: "500"},
	}}}
	currentSceneIndex = 0
	return config
}

func filesEntryIndex(config *Config, name string) int {
	for i, entry := range feEntriesOf(config) {
		if entry.Name == name {
			return i
		}
	}
	return -1
}

func TestFilesRootHasAParentRowThatClimbsOutOfTheRoot(t *testing.T) {
	withFilesNav(t)
	root, _ := feTestTree(t)
	config := filesNavConfig(root)
	feListDirectory(config)

	entries := feEntriesOf(config)
	if len(entries) == 0 || entries[0].Name != ".." {
		t.Fatalf("the Files root must lead with a .. row, got %v", names(entries))
	}
	if !filesSamePath(entries[0].Path, filepath.Dir(root)) {
		t.Fatalf(".. points at %q, want the real parent %q", entries[0].Path, filepath.Dir(root))
	}

	// Taking it leaves the configured root: the folder above is listed, with
	// the root it came from under the cursor.
	focusedFileIndex = 0
	feEnterFocused(config)
	if got := feCurrentPath(config); !filesSamePath(got, filepath.Dir(root)) {
		t.Fatalf("after .. the folder is %q, want %q", got, filepath.Dir(root))
	}
	if i := filesEntryIndex(config, filepath.Base(root)); i < 0 || focusedFileIndex != i {
		t.Fatalf("the cursor is on row %d, want the folder just left (%d)", focusedFileIndex, i)
	}
	if !filesSamePath(feRoot(config), filesVolumeRoot(root)) {
		t.Fatalf("above the configured root the active root is %q, want the volume root", feRoot(config))
	}
}

func TestFilesBackStepsUpThenToLocations(t *testing.T) {
	withFilesNav(t)
	root, sub := feTestTree(t)
	config := filesNavConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)

	// B in a sub-folder climbs one level and lands on the folder it left.
	feBack(config)
	if got := feCurrentPath(config); !filesSamePath(got, filepath.Dir(sub)) {
		t.Fatalf("B went to %q, want %q", got, filepath.Dir(sub))
	}
	if i := filesEntryIndex(config, filepath.Base(sub)); focusedFileIndex != i {
		t.Fatalf("cursor on %d, want the gba folder (%d)", focusedFileIndex, i)
	}
	if label := filesBackLabel(config); label != "Up" {
		t.Errorf("footer B label inside a folder = %q, want Up", label)
	}
	feBack(config)
	if got := feCurrentPath(config); !filesSamePath(got, root) {
		t.Fatalf("second B went to %q, want the root", got)
	}
	if label := filesBackLabel(config); label != "Locations" {
		t.Errorf("footer B label at the root = %q, want Locations", label)
	}

	// At the root B steps up to the Locations list, with the root's row
	// under the cursor.
	feBack(config)
	if !filesNav.atLocations {
		t.Fatal("B at the Files root did not show the Locations list")
	}
	loc, ok := filesLocationAt(focusedFileIndex)
	if !ok || !filesSamePath(loc.Path, root) {
		t.Fatalf("Locations cursor on %+v, want the Files root %q", loc, root)
	}
	if label := filesBackLabel(config); label != "Back" {
		t.Errorf("footer B label on Locations = %q, want Back", label)
	}
	// Nothing on the Locations list can be marked or deleted.
	if targets := feSelectionTargets(config); len(targets) != 0 {
		t.Fatalf("a location became an action target: %v", names(targets))
	}
	for _, action := range feActions(config) {
		if action.Kind == feActDelete || action.Kind == feActRename || action.Kind == feActPaste {
			t.Errorf("%s is offered on the Locations list", action.Label)
		}
	}

	// A on the row opens it again.
	feEnterFocused(config)
	if filesNav.atLocations || !filesSamePath(feCurrentPath(config), root) {
		t.Fatalf("opening the location left atLocations=%v path=%q", filesNav.atLocations, feCurrentPath(config))
	}
}

func TestFilesLocationsListTheRootAndTheSystem(t *testing.T) {
	withFilesNav(t)
	root, _ := feTestTree(t)
	config := filesNavConfig(root)
	locs := filesBuildLocations(config)
	if len(locs) == 0 || !filesSamePath(locs[0].Path, root) {
		t.Fatalf("the configured root must be the first location: %+v", locs)
	}
	seen := map[string]bool{}
	for _, loc := range locs {
		key := filepath.Clean(loc.Path)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			t.Errorf("%q is listed twice", loc.Path)
		}
		seen[key] = true
		if strings.TrimSpace(loc.Label) == "" {
			t.Errorf("location %q has no name", loc.Path)
		}
	}
	if runtime.GOOS != "windows" && !seen["/"] {
		t.Error("the filesystem root is not a location on Linux")
	}
	if runtime.GOOS == "windows" && len(filesDrives()) == 0 {
		t.Error("no drive letters were listed on Windows")
	}
}

func TestFilesFocusIsRememberedPerFolder(t *testing.T) {
	withFilesNav(t)
	root, sub := feTestTree(t)
	config := filesNavConfig(root)
	config.Variables.Custom["fe_path"] = sub
	feListDirectory(config)
	focusedFileIndex = filesEntryIndex(config, "c-mid.mp3")
	filesNoteFocus(config)

	// Leave for the parent and come back down: the row is where it was.
	filesGoParent(config)
	if !filesSamePath(feCurrentPath(config), filepath.Dir(sub)) {
		t.Fatalf("not in the parent: %q", feCurrentPath(config))
	}
	filesEnterDir(config, FileEntry{Name: filepath.Base(sub), Path: sub, IsDir: true})
	if got := filesEntryIndex(config, "c-mid.mp3"); focusedFileIndex != got {
		t.Fatalf("back in the folder the cursor is on %d, want c-mid.mp3 (%d)", focusedFileIndex, got)
	}

	// Re-entering the Files scene (which resets every list's cursor) finds it
	// too.
	focusedFileIndex = 0
	filesSceneEntered(config)
	if got := filesEntryIndex(config, "c-mid.mp3"); focusedFileIndex != got {
		t.Fatalf("after re-entering Files the cursor is on %d, want %d", focusedFileIndex, got)
	}
}

func TestFilesHiddenEntriesFollowTheSwitch(t *testing.T) {
	withFilesNav(t)
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = false
	t.Cleanup(func() { userConfigWritesEnabled = prevWrites })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filesNavConfig(root)
	feListDirectory(config)
	if filesEntryIndex(config, ".secret") >= 0 {
		t.Fatal("a dotfile is listed while hidden files are off")
	}
	if filesNav.hiddenCount != 1 {
		t.Fatalf("hidden count = %d, want 1", filesNav.hiddenCount)
	}
	if !strings.Contains(feFolderTotalsLabel(config), "1 hidden") {
		t.Errorf("totals %q do not say an item is hidden", feFolderTotalsLabel(config))
	}
	filesSetShowHidden(config, true)
	if filesEntryIndex(config, ".secret") < 0 {
		t.Fatal("the dotfile is still hidden with the switch on")
	}
	for _, action := range feActions(config) {
		if action.Kind == feActHidden && action.Detail != "On" {
			t.Errorf("the sheet's hidden-files row reads %q, want On", action.Detail)
		}
	}
}

func TestFilesUnreadableFolderExplainsItself(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not lock out this user here")
	}
	withFilesNav(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	config := filesNavConfig(root)
	feListDirectory(config)
	filesEnterDir(config, FileEntry{Name: "locked", Path: locked, IsDir: true})
	if !filesSamePath(feCurrentPath(config), locked) {
		t.Fatalf("the locked folder was not opened: %q", feCurrentPath(config))
	}
	entries := feEntriesOf(config)
	if len(entries) != 1 || entries[0].Name != ".." {
		t.Fatalf("an unreadable folder must still offer the way back, got %v", names(entries))
	}
	if filesNav.listErr == nil {
		t.Fatal("the read error was not kept for the screen to explain")
	}
	title, _ := filesListErrorText(filesNav.listErr)
	if !strings.Contains(strings.ToLower(title), "permission") {
		t.Errorf("the explanation is %q, want it to name the permission", title)
	}
}

func TestFilesSystemFoldersAskTwice(t *testing.T) {
	areas := []string{"/mnt/SDCARD", "/home/juka"}
	systemDir := "/etc"
	if runtime.GOOS == "windows" {
		areas = []string{`D:\`, `C:\Users\juka`}
		systemDir = `C:\Windows\System32`
	}
	cases := []struct {
		path   string
		system bool
	}{
		{areas[0], false},
		{filepath.Join(areas[1], "Downloads"), false},
		{systemDir, true},
	}
	for _, c := range cases {
		if got := filesIsSystemPath(c.path, areas); got != c.system {
			t.Errorf("filesIsSystemPath(%q) = %v, want %v", c.path, got, c.system)
		}
	}

	withFilesNav(t)
	filesNav.systemFolder = true
	feSheet = feSheetState{open: true}
	if filesSystemConfirm("Rename") {
		t.Fatal("a rename in a system folder ran on the first press")
	}
	if !filesSystemConfirm("Rename") {
		t.Fatal("the second press did not confirm")
	}
	filesNav.systemFolder = false
	if !filesSystemConfirm("Rename") {
		t.Fatal("a user folder asked for confirmation")
	}
}

func TestFilesROMsAndIcons(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		path   string
		system string
	}{
		{filepath.Join(sep+"roms", "GBA", "game.gba"), "Game Boy Advance"},
		{filepath.Join(sep+"roms", "PS", "Game", "disc.cue"), "PlayStation"},
		{filepath.Join(sep+"downloads", "game.gba"), "Game Boy Advance"},
		{filepath.Join(sep+"downloads", "archive.zip"), ""},
		{filepath.Join(sep+"roms", "MD", "Sonic.md"), "Mega Drive"},
		{filepath.Join(sep+"notes", "README.md"), ""},
		{filepath.Join(sep+"roms", "GBA", "README.md"), ""},
		{filepath.Join(sep+"docs", "manual.pdf"), ""},
	}
	for _, c := range cases {
		if got := filesROMSystem(c.path); got != c.system {
			t.Errorf("filesROMSystem(%q) = %q, want %q", c.path, got, c.system)
		}
	}
	icons := map[string]string{
		"photo.png": "image", "song.mp3": "music", "clip.mp4": "video", "manual.pdf": "pdf",
		"setup.sh": "terminal", "index.html": "code", "notes.txt": "text", "data.bin": "text",
	}
	for name, want := range icons {
		if got := filesEntryIcon(FileEntry{Name: name, Path: name}); got != want {
			t.Errorf("icon for %s = %q, want %q", name, got, want)
		}
	}
	if got := filesEntryIcon(FileEntry{Name: "..", IsDir: true}); got != "folder-up" {
		t.Errorf("the parent row's icon is %q", got)
	}
}

func TestFilesDefaultOpenUsesTheEditor(t *testing.T) {
	resetFileEditorForTest(t)
	withFilesNav(t)
	root := t.TempDir()
	odd := filepath.Join(root, "settings.conf2")
	if err := os.WriteFile(odd, []byte("key=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filesNavConfig(root)
	feListDirectory(config)
	focusedFileIndex = filesEntryIndex(config, "settings.conf2")
	feEnterFocused(config)
	if !fileEditor.open || fileEditor.mode != filesViewEdit {
		t.Fatalf("a file with no viewer of its own did not open in the editor (open=%v mode=%v)", fileEditor.open, fileEditor.mode)
	}
	fileEditorForget()

	blob := filepath.Join(root, "firmware.img")
	if err := os.WriteFile(blob, []byte{0x7f, 'E', 'L', 'F', 0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	feListDirectory(config)
	focusedFileIndex = filesEntryIndex(config, "firmware.img")
	feEnterFocused(config)
	if !fileEditor.open || fileEditor.mode != filesViewHex || !fileEditor.readOnly {
		t.Fatalf("binary data did not open read-only as hex (open=%v mode=%v ro=%v)", fileEditor.open, fileEditor.mode, fileEditor.readOnly)
	}
}

func TestFilesLeftAndRightWalkTheTree(t *testing.T) {
	withFilesNav(t)
	root, sub := feTestTree(t)
	config := filesNavConfig(root)
	selectedButtonIndex = 0
	feListDirectory(config)
	focusedFileIndex = filesEntryIndex(config, "roms")
	if !feListAction(config, ActionNavigateRight) || !filesSamePath(feCurrentPath(config), filepath.Dir(sub)) {
		t.Fatalf("Right on a folder did not open it: %q", feCurrentPath(config))
	}
	if !feListAction(config, ActionNavigateLeft) || !filesSamePath(feCurrentPath(config), root) {
		t.Fatalf("Left did not climb back: %q", feCurrentPath(config))
	}
	if action, ok := feKeyAction(sdl.K_BACKSPACE); !ok || action != ActionBack {
		t.Error("Backspace is not Back in Files")
	}
}
