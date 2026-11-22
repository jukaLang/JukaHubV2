package main

// files_manager_test.go — the Files screen's manager, the device-to-device
// transfer address parser, the text editor's byte-exact round trip, the hex
// editor's saves and the syntax colours' lexer on arbitrary bytes.
// Everything here works on a real temporary tree: the operations are
// filesystem work, and a mock would only test the mock.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
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

// The hex editor writes only the bytes it changed — as an atomic copy that
// keeps the file's permissions, or in place for a large file — refuses a
// change past the end, and leaves no temporary file behind. Its keyboard
// field reads hex pairs and quoted text; "Go to offset" reads hex and shares.
func TestHexEditorSavesOnlyChangedBytes(t *testing.T) {
	orig := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	want := []byte{0xAA, 1, 2, 3, 0xBB, 0xCC, 6, 7, 8, 0xDD}
	patches := map[int64]byte{0: 0xAA, 4: 0xBB, 5: 0xCC, 9: 0xDD}
	for _, atomic := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "save.srm")
		if err := os.WriteFile(path, orig, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := hexWritePatches(path, patches, atomic); err != nil {
			t.Fatalf("atomic=%v: %v", atomic, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("atomic=%v: file = % X, want % X (%v)", atomic, got, want, err)
		}
		if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
			t.Errorf("atomic=%v: mode %v, want -rw-r-----", atomic, info.Mode().Perm())
		}
		if err := hexWritePatches(path, map[int64]byte{10: 1}, atomic); err == nil {
			t.Errorf("atomic=%v: a change past the end was written", atomic)
		}
		if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".jukahub-hex-*")); len(left) > 0 {
			t.Errorf("atomic=%v: temporary files left: %v", atomic, left)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"4A 75 6b 61", "Juka"},
		{"0x4A,0x75", "Ju"},
		{`"Juka"`, "Juka"},
		{`'J`, "J"},
	} {
		if got, err := hexParseBytes(c.in); err != nil || string(got) != c.want {
			t.Errorf("hexParseBytes(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "4", "4G", `""`} {
		if _, err := hexParseBytes(bad); err == nil {
			t.Errorf("hexParseBytes(%q) took it", bad)
		}
	}
	if off, err := hexParseOffset("0x1f", 100); err != nil || off != 31 {
		t.Errorf("hexParseOffset(0x1f) = %d, %v", off, err)
	}
	if off, err := hexParseOffset("50%", 101); err != nil || off != 50 {
		t.Errorf("hexParseOffset(50%%) = %d, %v", off, err)
	}
	if off, err := hexParseOffset("FFFF", 100); err != nil || off != 99 {
		t.Errorf("an offset past the end should land on the last byte, got %d, %v", off, err)
	}
}

// The syntax colours run on whatever a file holds. Every row, cut anywhere,
// gives spans that are in order, inside the row, on rune boundaries and never
// plain, and the state-only pass ends where the colouring one does. A few
// exact spans pin the rules that keep the rest of a file right.
func TestSyntaxScanStaysInBounds(t *testing.T) {
	rows := []string{
		"", " ", "#", "\"", "'", "`", "/", "//", "/*", "*/", "<<", "<<<", "<<-", "$", "${", "$(", "@", "%",
		`echo "$HOME/${x}" # c`, "cat <<-EOF", "s{a}{b}g;", "my %h = (a => 1); $x % 2", "=pod", "=cut",
		`print <<"END";`, "a = b / c / d", `x = /re[/]x/g.test(s)`, "`tmpl ${a}", `#include <stdio.h>`,
		`auto s = R"x(raw)x";`, `R"(open`, `{"k": "v", "n": -1.5e3}`, `key = "v" ; c`, "[section]", "- a: 1",
		`x = f"{a}" # c`, `'''doc`, `@app.route("/")`, "qw(a b", "tr/a-z//", "$#", "${#x}", "0x1Fu 1'000 2.5e-3f",
		"日本語 'строка' \"ü\"", "\xff\xfe\x00\x80", strings.Repeat("(", 300) + strings.Repeat("\"", 301),
	}
	// A fixed byte soup from a tiny LCG: the same rows on every run.
	seed := uint32(1)
	for i := 0; i < 64; i++ {
		b := make([]byte, 1+i*7)
		for j := range b {
			seed = seed*1664525 + 1013904223
			b[j] = "\"'`/*#$@%<>{}()[];=\\ \tqsmyx0aZ\x80\xff"[seed>>27]
		}
		rows = append(rows, string(b))
	}
	for lang := synNone; lang < synLangCount; lang++ {
		for _, ends := range []bool{true, false} {
			var st synState
			for _, row := range rows {
				var spans []synSpan
				next := synScan(lang, st, row, ends, &spans)
				if bare := synScan(lang, st, row, ends, nil); bare != next {
					t.Errorf("%s: %q ends in %+v without spans, %+v with", synLangName(lang), row, bare, next)
				}
				end := 0
				for _, sp := range spans {
					if sp.start < end || sp.end <= sp.start || sp.end > len(row) || sp.kind == synPlain || sp.kind >= synKindCount ||
						!utf8.RuneStart(row[sp.start]) || (sp.end < len(row) && !utf8.RuneStart(row[sp.end])) {
						t.Fatalf("%s: bad span %+v in %q (%+v)", synLangName(lang), sp, row, spans)
					}
					end = sp.end
				}
				st = next
			}
		}
	}

	type want struct {
		text string
		kind synKind
	}
	check := func(lang synLangID, row string, wants ...want) {
		t.Helper()
		var spans []synSpan
		synScan(lang, synState{}, row, true, &spans)
		got := map[string]synKind{}
		for _, sp := range spans {
			got[row[sp.start:sp.end]] = sp.kind
		}
		for _, w := range wants {
			if k, ok := got[w.text]; !ok || k != w.kind {
				t.Errorf("%s %q: %q is %v (coloured: %v), want %v", synLangName(lang), row, w.text, k, ok, w.kind)
			}
		}
	}
	check(synPython, `def f(self): return "x" # c`,
		want{"def", synKeyword}, want{"f", synFunc}, want{"self", synVar}, want{`"x"`, synString}, want{"# c", synComment})
	check(synBash, `echo "$HOME" # c`, want{"echo", synType}, want{"$HOME", synVar}, want{"# c", synComment})
	check(synPerl, `my %h = (a => 1); $x % 2`, want{"my", synKeyword}, want{"%h", synVar}, want{"a", synKey}, want{"$x", synVar})
	check(synJS, `x = /re[/]x/g.test(s) / 2`, want{"/re[/]x/g", synString}, want{"test", synFunc}, want{"2", synNumber})
	check(synJSON, `{"k": "v", "n": -1.5e3, "b": true}`,
		want{`"k"`, synKey}, want{`"v"`, synString}, want{"-1.5e3", synNumber}, want{"true", synConst})
	check(synCpp, `#include <stdio.h>`, want{"#include", synMeta}, want{"<stdio.h>", synString})
	check(synCpp, `int main() { return nullptr; } // c`,
		want{"int", synType}, want{"main", synFunc}, want{"return", synKeyword}, want{"nullptr", synConst}, want{"// c", synComment})

	// What carries over from one line to the next.
	st := synScan(synPython, synState{}, `s = '''doc`, true, nil)
	var spans []synSpan
	synScan(synPython, st, `still''' + x`, true, &spans)
	if len(spans) == 0 || spans[0].start != 0 || spans[0].end != len(`still'''`) || spans[0].kind != synString {
		t.Errorf("a Python docstring did not carry over: %+v", spans)
	}
	st = synScan(synBash, synState{}, "cat <<-EOF", true, nil)
	st = synScan(synBash, st, "  $not_a_var", true, nil)
	spans = spans[:0]
	if synScan(synBash, st, "  EOF", true, &spans) != (synState{}) || len(spans) != 1 {
		t.Errorf("a shell here-doc did not end at its terminator: %+v", spans)
	}
}
