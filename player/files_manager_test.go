package main

// files_manager_test.go — the Files screen's manager, the device-to-device
// transfer address parser and the text editor's byte-exact round trip.
// Everything here works on a real temporary tree: the operations are
// filesystem work, and a mock would only test the mock.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// The built-in text editor splits a file into lines and joins it back: a file
// that is opened and saved unchanged must come back byte for byte, final
// newline or not.
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
