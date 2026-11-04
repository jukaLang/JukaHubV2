package main

// Tests for the built-in text editor: byte-exact round trips (a file that is
// opened and closed must not change), line replacement through the on-screen
// keyboard, atomic saves that keep permissions, the JSON guard, and the two
// places that open it (Patch's packages.json and Files).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func resetFileEditorForTest(t *testing.T) {
	t.Helper()
	prev := fileEditor
	prevBuffer := fileEditorLineBuffer
	t.Cleanup(func() {
		fileEditor = prev
		fileEditorLineBuffer = prevBuffer
	})
	fileEditorForget()
}

func writeEditorFixture(t *testing.T, name, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileEditorRoundTripsFilesExactly(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		lines   int
		newline bool
	}{
		{"empty", "", 1, false},
		{"one line, no newline", "hello", 1, false},
		{"one line, newline", "hello\n", 1, true},
		{"two lines", "a\nb", 2, false},
		{"two lines, newline", "a\nb\n", 2, true},
		{"blank line between", "a\n\nb\n", 3, true},
	}
	for _, c := range cases {
		lines := fileEditorSplit(c.text)
		if len(lines) != c.lines {
			t.Errorf("%s: split into %d lines, want %d (%q)", c.name, len(lines), c.lines, lines)
		}
		if got := fileEditorJoin(lines, c.newline); got != c.text {
			t.Errorf("%s: join = %q, want %q", c.name, got, c.text)
		}
	}
}

// Opening a file and closing it again must leave it untouched and must not
// report edits the user did not make.
func TestFileEditorOpensCleanAndSavesWhatItShows(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	path := writeEditorFixture(t, "packages.json", "{\n  \"schema\": 1\n}\n", 0o640)

	if !fileEditorOpen(path) {
		t.Fatal("a JSON file was refused")
	}
	if fileEditorDirty() {
		t.Fatalf("a freshly opened file reported unsaved changes: %q", fileEditorText())
	}
	if fileEditor.title != "packages.json" || len(fileEditor.lines) != 3 {
		t.Fatalf("state = title %q, %d lines", fileEditor.title, len(fileEditor.lines))
	}
	if got := fileEditorSubtitle(nil, 0); !strings.Contains(got, "no changes") {
		t.Errorf("subtitle = %q, want it to say the file is unedited", got)
	}

	// Replace the first line through the real keyboard path.
	fileEditor.cursor = 0
	fileEditorStartLine()
	if !virtualKeyboardActive || vkTarget != vkTargetFileLine {
		t.Fatal("editing a line did not open its keyboard")
	}
	if vkCurrentText() != "{" {
		t.Fatalf("keyboard seeded with %q, want the focused line", vkCurrentText())
	}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkSetText("{\"schema\": 1,", config)
	vkSubmit(config, false)
	if virtualKeyboardActive {
		t.Fatal("submitting the line left the keyboard open")
	}
	if !fileEditorDirty() {
		t.Fatal("the edited line did not mark the file as changed")
	}
	if got := fileEditorSave(); got != nil {
		t.Fatalf("save failed: %v", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{\"schema\": 1,\n  \"schema\": 1\n}\n" {
		t.Fatalf("saved body = %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no permission bits to preserve: os.Chmod there only toggles
	// the read-only attribute, so the round trip cannot be compared.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Errorf("saved mode = %v, want the original 0640", info.Mode().Perm())
	}
	if fileEditorDirty() {
		t.Error("a saved file is still reported as changed")
	}
}

func TestFileEditorRefusesBrokenJSON(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	const original = "{\n  \"schema\": 1\n}\n"
	path := writeEditorFixture(t, "packages.json", original, 0o644)

	if !fileEditorOpen(path) {
		t.Fatal("open failed")
	}
	fileEditor.lines[1] = "\"schema\": "
	if err := fileEditorSave(); err == nil {
		t.Fatal("a package index with a syntax error was written")
	} else if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error = %v, want it to name JSON", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != original {
		t.Fatalf("the file changed despite the refusal: %q", body)
	}
	if !fileEditorDirty() {
		t.Error("the refused buffer was discarded")
	}
	// A non-JSON file has no such guard: a shell script is not parsed.
	script := writeEditorFixture(t, "run.sh", "echo one\n", 0o755)
	fileEditorForget()
	if !fileEditorOpen(script) {
		t.Fatal("a script could not be opened")
	}
	fileEditor.lines[0] = "echo two"
	if err := fileEditorSave(); err != nil {
		t.Fatalf("saving a script failed: %v", err)
	}
	body, _ = os.ReadFile(script)
	if string(body) != "echo two\n" {
		t.Fatalf("script body = %q", body)
	}
}

func TestFileEditorInsertsAndDeletesLines(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	path := writeEditorFixture(t, "notes.txt", "one\ntwo\nthree\n", 0o644)
	if !fileEditorOpen(path) {
		t.Fatal("open failed")
	}
	fileEditor.cursor = 1
	fileEditorStartInsert()
	if fileEditor.pending != fileEditorInsertLine {
		t.Fatal("X did not arm an insert")
	}
	fileEditorApplyLine("inserted")
	if got := strings.Join(fileEditor.lines, "|"); got != "one|two|inserted|three" {
		t.Fatalf("after insert: %q", got)
	}
	if fileEditor.cursor != 2 {
		t.Errorf("cursor = %d, want the inserted line", fileEditor.cursor)
	}
	fileEditorDeleteLine()
	if got := strings.Join(fileEditor.lines, "|"); got != "one|two|three" {
		t.Fatalf("after delete: %q", got)
	}
	// The first line can be deleted too, and the last line cannot make the
	// cursor point past the buffer.
	fileEditor.cursor = 0
	fileEditorDeleteLine()
	if got := strings.Join(fileEditor.lines, "|"); got != "two|three" {
		t.Fatalf("after deleting the first line: %q", got)
	}
	fileEditor.cursor = 99
	fileEditorClamp()
	if fileEditor.cursor != 1 {
		t.Errorf("cursor clamped to %d, want the last line", fileEditor.cursor)
	}
	// Deleting everything leaves one empty line rather than an empty list.
	for range fileEditor.lines {
		fileEditorDeleteLine()
	}
	if len(fileEditor.lines) != 1 || fileEditor.lines[0] != "" {
		t.Fatalf("empty buffer = %q, want one empty line", fileEditor.lines)
	}
	if got := fileEditorText(); got != "" {
		t.Errorf("empty buffer writes %q, want nothing", got)
	}
}

func TestFileEditorRefusesBinaryAndHugeFiles(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)

	binary := writeEditorFixture(t, "image.txt", "abc\x00\x01", 0o644)
	if fileEditorOpen(binary) {
		t.Fatal("a binary file was opened as text")
	}
	if fileEditor.open {
		t.Fatal("the refused file stayed open")
	}

	big := filepath.Join(t.TempDir(), "huge.log")
	// A real write, not a sparse truncate: the check is about the file's size,
	// and zero bytes are text until the size rule refuses them.
	if err := os.WriteFile(big, make([]byte, fileEditorMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if fileEditorOpen(big) {
		t.Fatal("an oversized file was opened")
	}
	if !strings.Contains(fileEditorTooLargeText(fileEditorMaxBytes+1), "editor stops") {
		t.Errorf("oversize message = %q", fileEditorTooLargeText(fileEditorMaxBytes+1))
	}
}

func TestFileEditorAsksBeforeDiscarding(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	path := writeEditorFixture(t, "a.cfg", "key=1\n", 0o644)
	if !fileEditorOpen(path) {
		t.Fatal("open failed")
	}
	fileEditor.lines[0] = "key=2"
	fileEditorRequestClose()
	if !fileEditor.open || !fileEditor.confirm {
		t.Fatal("closing a changed file did not ask first")
	}
	if got := fileEditorTitle(); !strings.Contains(got, "Discard") {
		t.Errorf("title = %q, want the discard question", got)
	}
	fileEditorRequestClose()
	if fileEditor.open {
		t.Fatal("the second close did not discard the edits")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "key=1\n" {
		t.Fatalf("discarding wrote the buffer: %q", body)
	}
}

// The keyboard's title and hints have to describe the line being edited, not a
// generic search field.
func TestFileEditorKeyboardDescribesTheLine(t *testing.T) {
	resetFileEditorForTest(t)
	path := writeEditorFixture(t, "b.json", "{\n\"x\": 1\n}\n", 0o644)
	if !fileEditorOpen(path) {
		t.Fatal("open failed")
	}
	fileEditor.cursor = 1
	fileEditorStartLine()
	if got := vkTitle(); got != "LINE 2" {
		t.Errorf("title = %q, want the line number", got)
	}
	vkCloseKeyboard()
	fileEditorStartInsert()
	if got := vkTitle(); got != "INSERT LINE" {
		t.Errorf("title = %q, want the insert heading", got)
	}
	vkCloseKeyboard()
	// A cancelled keyboard leaves the document alone.
	if fileEditorDirty() {
		t.Error("cancelling the line editor changed the file")
	}
}

// Files ignored every text file that was not an image, a video or a script.
// The routing table is what decides that now.
func TestFileEditorExtensionRouting(t *testing.T) {
	editable := []string{"packages.json", "jukaconfig.json", "notes.txt", "README.md", "app.cfg", "servers.ini", "config.yaml", "settings.toml", "a.csv", "run.sh"}
	for _, name := range editable {
		if !fileEditorCanEdit(name) {
			t.Errorf("%s: the action sheet refuses to edit it", name)
		}
	}
	for _, name := range []string{"packages.json", "notes.txt", "config.yaml"} {
		if !fileEditorAutoOpens(name) {
			t.Errorf("%s: A on the row does not open the editor", name)
		}
	}
	// Formats with a better handler keep it: A runs a script and opens a page
	// in the sandbox, so only the sheet's Edit reaches the editor.
	for _, name := range []string{"run.sh", "index.html", "app.js"} {
		if fileEditorAutoOpens(name) {
			t.Errorf("%s: A should keep its existing handler", name)
		}
	}
	for _, name := range []string{"movie.mp4", "photo.png", "game.zip", ""} {
		if fileEditorCanEdit(name) {
			t.Errorf("%s: offered for editing", name)
		}
	}
}

// The Files action sheet offers Edit for a single text file, and says why when
// it cannot.
func TestFileEditorSheetRowFollowsTheSelection(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	// The explorer refuses a path outside its configured root, so the fixture
	// folder is made the root: that is also what the handheld does on its SD
	// card.
	config.Variables.FileExplorerRoot = dir
	config.Variables.Custom["fe_path"] = dir
	feListDirectory(config)
	if len(feEntriesOf(config)) == 0 {
		t.Fatal("the fixture folder was not listed")
	}

	editRow := func() feAction {
		t.Helper()
		for _, action := range feActions(config) {
			if action.Kind == feActEdit {
				return action
			}
		}
		t.Fatal("the action sheet has no Edit row")
		return feAction{}
	}

	prevIndex := focusedFileIndex
	prevSelection := feSelectedPaths
	t.Cleanup(func() {
		focusedFileIndex = prevIndex
		feSelectedPaths = prevSelection
		feSelectionClear()
	})
	feSelectionClear()
	for i, entry := range feEntriesOf(config) {
		if entry.Name == "notes.txt" {
			focusedFileIndex = i
			break
		}
	}
	row := editRow()
	if !row.Enabled || !strings.Contains(row.Detail, "notes.txt") {
		t.Fatalf("text row = %+v, want Edit enabled for notes.txt", row)
	}

	// A folder cannot be edited, and the row says so.
	for i, entry := range feEntriesOf(config) {
		if entry.Name == "sub" {
			focusedFileIndex = i
			break
		}
	}
	if row := editRow(); row.Enabled {
		t.Errorf("a folder was offered for editing: %+v", row)
	}
}

// Patch's "Edit packages.json" must open the editor on that file, not move the
// user to the Files screen.
func TestPatchEditRepoOpensTheEditor(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	origStateDir, origFn := patchStateDir, patchStateDirFn
	dir := t.TempDir()
	patchStateDir = dir
	patchStateDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		patchStateDir, patchStateDirFn = origStateDir, origFn
	})

	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	currentSceneIndex = 0

	openPatchRepoEditor(config)
	if !fileEditor.open {
		t.Fatal("the row did not open the editor")
	}
	if filepath.Base(fileEditor.path) != "packages.json" {
		t.Fatalf("editor opened %q, want packages.json", fileEditor.path)
	}
	if !strings.Contains(fileEditorText(), "\"schema\"") {
		t.Errorf("the starter index was not loaded: %q", fileEditorText())
	}
	if currentSceneIndex != 0 {
		t.Error("the row changed the scene instead of opening the editor")
	}
	// The editor is modal: its keys are consumed and the scene never sees them.
	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}
	if !handleFileEditorKey(key, config) {
		t.Fatal("the editor did not consume a key")
	}
	if fileEditor.cursor != 1 {
		t.Errorf("cursor = %d after Down", fileEditor.cursor)
	}
	if handleFileEditorKey(nil, config) {
		t.Error("a nil event was consumed")
	}
}

// A file the app cannot write must open read-only and refuse the edit keys.
// Accepting the keystrokes and rejecting the save later throws the work away.
func TestFileEditorReadOnlyRefusesEdits(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	path := writeEditorFixture(t, "locked.cfg", "key=1\n", 0o644)
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("could not make the file read-only: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	if !fileEditorOpen(path) {
		t.Fatal("a read-only text file was not opened")
	}
	if !fileEditor.readOnly {
		t.Fatal("the editor opened a read-only file as writable, so Save would fail after the edit")
	}
	if got := fileEditorSubtitle(nil, 0); !strings.Contains(got, "read-only") {
		t.Fatalf("subtitle = %q, want it to say read-only", got)
	}
	if got := fileEditorFooter(); strings.Contains(got, "Save") || strings.Contains(got, "Edit") {
		t.Fatalf("footer = %q, want no edit or save key on a read-only file", got)
	}

	before := fileEditor.lines[0]
	fileEditorStartLine()
	if virtualKeyboardActive {
		t.Fatal("the keyboard opened for a read-only file")
	}
	fileEditorApplyLine("changed")
	if fileEditor.lines[0] != before {
		t.Fatalf("line became %q after an edit to a read-only file", fileEditor.lines[0])
	}
	count := len(fileEditor.lines)
	fileEditorDeleteLine()
	if len(fileEditor.lines) != count {
		t.Fatal("a read-only file lost a line")
	}
	if fileEditorDirty() {
		t.Fatal("a read-only file was marked dirty")
	}
	if fileEditorSaveAndReport() {
		t.Fatal("saving a read-only file reported success")
	}
}

// A handheld has no Delete key and A/X/Y/B are already editing keys, so the
// line removal sits on R2 — advertised in the footer — and on D for a keyboard.
// Without it a line could be added on the device but never removed.
func TestFileEditorDeletesALineFromThePadAndKeyboard(t *testing.T) {
	resetFileEditorForTest(t)
	newProbeFrame(t)
	path := writeEditorFixture(t, "keys.txt", "one\ntwo\nthree\n", 0o644)
	if !fileEditorOpen(path) {
		t.Fatal("open failed")
	}
	config := &Config{}

	// R2 removes the focused line.
	fileEditor.cursor = 1
	if !handleShoulderPress(padR2, config) {
		t.Fatal("R2 was not consumed by the editor")
	}
	if got := strings.Join(fileEditor.lines, "|"); got != "one|three" {
		t.Fatalf("after R2: %q, want one|three", got)
	}

	// D does the same on a keyboard.
	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_d}}
	if !handleFileEditorKey(key, config) {
		t.Fatal("D was not consumed by the editor")
	}
	if got := strings.Join(fileEditor.lines, "|"); got != "one" {
		t.Fatalf("after D: %q, want one", got)
	}

	// The footer has to say so, or the capability is hidden.
	if footer := fileEditorFooter(); !strings.Contains(footer, "R2 Delete") {
		t.Fatalf("footer = %q, want it to advertise R2 Delete", footer)
	}

	// A read-only file must not lose a line to either key.
	fileEditor.readOnly = true
	before := strings.Join(fileEditor.lines, "|")
	handleShoulderPress(padR2, config)
	handleFileEditorKey(key, config)
	if got := strings.Join(fileEditor.lines, "|"); got != before {
		t.Fatalf("read-only file lost a line: %q → %q", before, got)
	}

	// Nothing is written until Y: the file on disk is untouched.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "one\ntwo\nthree\n" {
		t.Fatalf("file on disk = %q, want the original (deletes are buffer-only until save)", body)
	}
}

// A text file whose first 8 KiB ends inside a multi-byte character is still
// text. The cut used to make about half of all Cyrillic or CJK files over
// 8 KiB "not a text file".
func TestFileEditorTextCutMidCharacterIsText(t *testing.T) {
	// One ASCII byte, then two-byte characters: byte 8192 splits one.
	body := append([]byte("x"), []byte(strings.Repeat("а", 4096))...)
	if fileEditorLooksBinary(body) {
		t.Fatal("UTF-8 text cut mid-character at 8 KiB was called binary")
	}
	if !fileEditorLooksBinary([]byte("abc\x00d")) {
		t.Fatal("a NUL byte must still mark binary data")
	}
}
