package main

// files_textview_test.go — the text viewer's paging: a large file is read one
// window at a time and the windows join up without losing or repeating a line;
// a long line is split into continuation rows; binary data becomes hex rows;
// the background line count numbers a window it has not read; and the viewer
// opens big files read-only and binary files as hex instead of refusing them.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeNumberedLog writes n lines "line 000001 …" and returns the path.
func writeNumberedLog(t *testing.T, name string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %06d the quick brown fox jumps over the lazy dog\n", i)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFilesSplitRowsChunksLongLines(t *testing.T) {
	long := strings.Repeat("é", filesMaxChunkBytes) // two bytes each
	rows, nums := filesSplitRows([]byte("short\r\n"+long+"\nlast"), false)
	if rows[0] != "short" {
		t.Fatalf("the carriage return was kept: %q", rows[0])
	}
	if len(rows) < 4 {
		t.Fatalf("a %d-byte line was not split: %d rows", len(long), len(rows))
	}
	for _, row := range rows {
		if len(row) > filesMaxChunkBytes {
			t.Errorf("a row is %d bytes, over the chunk limit", len(row))
		}
		if !strings.HasPrefix(row, "é") && row != "short" && row != "last" {
			t.Errorf("a chunk was cut inside a character: %q…", row[:4])
		}
	}
	if nums[0] != 0 || nums[1] != 1 || nums[2] != -1 {
		t.Fatalf("line numbers = %v, want 0, 1 and a continuation", nums[:3])
	}
	if nums[len(nums)-1] != 2 {
		t.Errorf("the last line is numbered %d, want 2", nums[len(nums)-1])
	}
	// A window that opens inside a line starts with a continuation row.
	_, nums = filesSplitRows([]byte("tail of a line\nnext\n"), true)
	if nums[0] != -1 || nums[1] != 0 {
		t.Fatalf("mid-line window numbers = %v", nums)
	}
}

func TestFilesWindowsCoverTheFileExactly(t *testing.T) {
	path := writeNumberedLog(t, "big.log", 20000) // about 1.2 MB
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	total := info.Size()
	start, end, err := filesEdgeWindowRange(path, false, total, false)
	if err != nil || start != 0 {
		t.Fatalf("first window = %d..%d, %v", start, end, err)
	}
	var lines []string
	windows := 0
	for {
		w, err := filesLoadWindow(path, start, end, false)
		if err != nil {
			t.Fatal(err)
		}
		if w.startsMidLine {
			t.Fatalf("window %d starts inside a line at %d", windows, start)
		}
		lines = append(lines, w.lines...)
		windows++
		if end >= total {
			break
		}
		start, end, err = filesNextWindowRange(path, end, total, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	if windows < 4 {
		t.Fatalf("a 1.2 MB file came in %d windows, want it paged", windows)
	}
	if len(lines) != 20000 {
		t.Fatalf("the windows hold %d lines, want 20000", len(lines))
	}
	for i, line := range lines {
		if want := fmt.Sprintf("line %06d", i+1); !strings.HasPrefix(line, want) {
			t.Fatalf("line %d = %q, want it to start %q", i, line, want)
		}
	}

	// Walking back from the end lands on line starts too.
	start, end, err = filesEdgeWindowRange(path, true, total, false)
	if err != nil || end != total {
		t.Fatalf("last window = %d..%d, %v", start, end, err)
	}
	prevStart, prevEnd, err := filesPrevWindowRange(path, start, false)
	if err != nil || prevEnd != start {
		t.Fatalf("previous window = %d..%d, %v", prevStart, prevEnd, err)
	}
	w, err := filesLoadWindow(path, prevStart, prevEnd, false)
	if err != nil || w.startsMidLine || !strings.HasPrefix(w.lines[0], "line ") {
		t.Fatalf("previous window starts %q (mid-line %v, err %v)", w.lines[0], w.startsMidLine, err)
	}
}

func TestFilesHexRows(t *testing.T) {
	data := []byte("Hello\x00\x01World!!\xff\xfeAB")
	rows := filesHexRows(data, 0x100)
	if len(rows) != 2 {
		t.Fatalf("%d bytes made %d rows, want 2", len(data), len(rows))
	}
	if rows[0].offset != "00000100" || rows[1].offset != "00000110" {
		t.Fatalf("offsets = %q, %q", rows[0].offset, rows[1].offset)
	}
	if rows[0].groups[0] != "48 65 6C 6C" {
		t.Fatalf("first group = %q", rows[0].groups[0])
	}
	if rows[0].ascii != "Hello..World!!.." {
		t.Fatalf("ascii = %q", rows[0].ascii)
	}
	if rows[1].groups[0] != "41 42" || rows[1].groups[1] != "" || rows[1].ascii != "AB" {
		t.Fatalf("short last row = %+v", rows[1])
	}
}

func TestFilesLineIndexNumbersAWindow(t *testing.T) {
	path := writeNumberedLog(t, "count.log", 5000)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	filesLineIndexStart(path, info.Size())
	t.Cleanup(filesLineIndexStop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, total, done := filesLineIndexProgress(path); done {
			if total != 5000 {
				t.Fatalf("counted %d lines, want 5000", total)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the line count did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The line starting at byte offset of line 3001 is line 3001.
	lineLen := int64(len(fmt.Sprintf("line %06d the quick brown fox jumps over the lazy dog\n", 1)))
	if n, ok := filesLineNumberAt(path, 3000*lineLen); !ok || n != 3001 {
		t.Fatalf("line at offset = %d (%v), want 3001", n, ok)
	}
}

func TestFileViewerOpensLargeFilesPaged(t *testing.T) {
	resetFileEditorForTest(t)
	path := writeNumberedLog(t, "huge.txt", 25000) // over the 1 MiB edit limit
	if !fileEditorOpen(path) {
		t.Fatal("a large text file was refused instead of opened read-only")
	}
	t.Cleanup(filesLineIndexStop)
	if fileEditor.mode != filesViewWindow || !fileEditor.readOnly {
		t.Fatalf("mode = %v read-only = %v, want a read-only window", fileEditor.mode, fileEditor.readOnly)
	}
	if !strings.Contains(fileEditor.notice, "editor stops") {
		t.Errorf("the notice %q does not say why it is read-only", fileEditor.notice)
	}
	if got := fileEditorRowCount(); got == 0 || int64(got) > filesWindowBytes/40 {
		t.Fatalf("the first window holds %d rows", got)
	}
	// Walking past the end of the window loads the next one.
	first := fileEditor.window.start
	fileEditor.cursor = fileEditorRowCount() - 1
	fileEditorMove(1)
	if fileEditor.window.start == first {
		t.Fatal("moving past the window's last line did not load the next window")
	}
	if row := fileEditorRowText(fileEditor.cursor); !strings.HasPrefix(row, "line ") {
		t.Fatalf("the next window opened on %q", row)
	}
	// Editing keys refuse instead of pretending.
	fileEditorStartLine()
	if virtualKeyboardActive {
		t.Fatal("the keyboard opened on a read-only window")
	}
	if strings.Contains(fileEditorFooter(), "Save") {
		t.Errorf("footer %q offers Save on a read-only file", fileEditorFooter())
	}
}

func TestFileViewerShowsBinaryAsHex(t *testing.T) {
	resetFileEditorForTest(t)
	path := filepath.Join(t.TempDir(), "blob.dat")
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if fileEditorOpen(path) {
		t.Fatal("Edit text opened binary data as lines")
	}
	if !filesViewerOpen(path) {
		t.Fatal("the viewer refused binary data")
	}
	if fileEditor.mode != filesViewHex || !fileEditor.readOnly || fileEditorRowCount() != 7 {
		t.Fatalf("mode %v ro %v rows %d, want 7 read-only hex rows", fileEditor.mode, fileEditor.readOnly, fileEditorRowCount())
	}
	if !strings.Contains(strings.ToLower(fileEditor.notice), "binary") {
		t.Errorf("the hex view does not warn: %q", fileEditor.notice)
	}
	// X shows the same bytes as text and back.
	filesViewerToggleHexText()
	if !fileEditor.forcedText || fileEditorRowCount() == 0 {
		t.Fatal("X did not show the binary file as text")
	}
	filesViewerToggleHexText()
	if fileEditor.forcedText || fileEditorRowCount() != 7 {
		t.Fatal("X did not return to hex")
	}
}

func TestFilesWrapAndSliceHelpers(t *testing.T) {
	if got := filesDisplayText("a\tb\rc\x01"); got != "a    bc·" {
		t.Fatalf("display text = %q", got)
	}
	if got := filesSliceRunes("héllo world", 2, 3); got != "llo" {
		t.Fatalf("slice = %q", got)
	}
	if got := filesSliceRunes("abc", 10, 3); got != "" {
		t.Fatalf("slice past the end = %q", got)
	}
	font := testInterFont(t, 20)
	rows := filesWrapLine(font, strings.Repeat("word ", 60), 200, 64)
	if len(rows) < 5 {
		t.Fatalf("a long line wrapped into %d rows", len(rows))
	}
	for _, row := range rows {
		if w, _, _ := font.SizeUTF8(row); int32(w) > 200 {
			t.Errorf("row %q is %d px, wider than 200", row, w)
		}
	}
}
