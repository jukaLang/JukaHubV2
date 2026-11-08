package main

// files_textview.go — the paging behind the text viewer (fileeditor.go).
//
// The editor used to read the whole file and refuse anything over 256 KB, which
// put every log on the device out of reach. A large file is now read one window
// at a time: about 256 KB of text cut at line breaks, so a 200 MB log costs a
// quarter of a megabyte of RAM however far into it the user scrolls. Lines
// longer than a screenful of text are shown as continuation rows, which keeps
// one minified JSON line from becoming a multi-megabyte texture. Binary files
// get the same windows as a hex dump (16 bytes a row) instead of being decoded
// as text.
//
// Line numbers of a large file need the newlines before the window. A
// background scan counts them once per opened file, leaving a checkpoint every
// 64 KB; a window then needs at most one 64 KB read to know its first line's
// number. Until the scan reaches it the gutter shows offsets instead.
//
// Nothing here touches SDL, so all of it is testable on a temporary file.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// filesViewMode is how the text viewer holds the file.
type filesViewMode int

const (
	// filesViewEdit: the whole file is in memory as editable lines.
	filesViewEdit filesViewMode = iota
	// filesViewWindow: a large text file, read-only, one window at a time.
	filesViewWindow
	// filesViewHex: binary data as a read-only hex dump, one window at a time.
	filesViewHex
)

const (
	// filesWindowBytes is one window of a large text file.
	filesWindowBytes = int64(256 << 10)
	// filesHexWindowBytes is one window of a hex dump: 4096 rows.
	filesHexWindowBytes = int64(64 << 10)
	// filesMaxChunkBytes is the longest run of a line shown as one row; a
	// longer line continues on the next rows. It also bounds how far window
	// boundaries look for a line break.
	filesMaxChunkBytes = 2000
	// filesHexRowBytes is the bytes of one hex row.
	filesHexRowBytes = 16
	// filesLineIndexStep is the distance between line-count checkpoints.
	filesLineIndexStep = int64(64 << 10)
)

// filesHexRow is one pre-formatted row of the hex view: the offset, four
// groups of four bytes, and the printable characters.
type filesHexRow struct {
	offset string
	groups [4]string
	ascii  string
}

// filesTextWindow is one window of a large file.
type filesTextWindow struct {
	start, end int64
	// lines are the rows of a text window; lineNo is each row's line number
	// relative to the window's first line start (0-based), or -1 for the
	// continuation of a long line.
	lines  []string
	lineNo []int32
	// hex holds the rows of a hex window.
	hex []filesHexRow
	// startsMidLine is true when the window begins inside a line: its first
	// row continues the previous window's last line.
	startsMidLine bool
}

// filesWindowEnd is where a window that starts at start should end: the line
// break nearest after start+size, so a window never cuts a line in two unless
// the line itself is longer than filesMaxChunkBytes.
func filesWindowEnd(f io.ReaderAt, start, size, total int64) int64 {
	if start+size >= total {
		return total
	}
	probe := start + size
	buf := make([]byte, filesMaxChunkBytes)
	n, _ := f.ReadAt(buf, probe)
	if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
		return probe + int64(i) + 1
	}
	return probe
}

// filesWindowStart is where the window that ends at end should start: the
// first line start after end-size.
func filesWindowStart(f io.ReaderAt, end, size int64) int64 {
	probe := end - size
	if probe <= 0 {
		return 0
	}
	buf := make([]byte, filesMaxChunkBytes)
	n, _ := f.ReadAt(buf, probe)
	if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 && probe+int64(i)+1 < end {
		return probe + int64(i) + 1
	}
	return probe
}

// filesSplitRows turns window bytes into rows: one per line, lines longer than
// filesMaxChunkBytes split at rune boundaries into continuation rows, a final
// "\r" dropped. A trailing newline does not add an empty last row.
func filesSplitRows(data []byte, startsMidLine bool) ([]string, []int32) {
	text := string(data)
	if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	var rows []string
	var nums []int32
	line := int32(0)
	for i, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		first := true
		if i == 0 && startsMidLine {
			// The window opens inside the previous window's last line.
			first = false
		}
		for {
			piece := raw
			if len(piece) > filesMaxChunkBytes {
				cut := filesMaxChunkBytes
				for cut > 0 && !utf8.RuneStart(raw[cut]) {
					cut--
				}
				if cut == 0 {
					cut = filesMaxChunkBytes
				}
				piece = raw[:cut]
			}
			rows = append(rows, piece)
			if first {
				nums = append(nums, line)
				line++
				first = false
			} else {
				nums = append(nums, -1)
			}
			raw = raw[len(piece):]
			if raw == "" {
				break
			}
		}
	}
	return rows, nums
}

// filesHexRows formats binary data starting at offset base.
func filesHexRows(data []byte, base int64) []filesHexRow {
	rows := make([]filesHexRow, 0, (len(data)+filesHexRowBytes-1)/filesHexRowBytes)
	const hexDigits = "0123456789ABCDEF"
	var ascii [filesHexRowBytes]byte
	for at := 0; at < len(data); at += filesHexRowBytes {
		end := at + filesHexRowBytes
		if end > len(data) {
			end = len(data)
		}
		chunk := data[at:end]
		row := filesHexRow{offset: fmt.Sprintf("%08X", base+int64(at))}
		for g := 0; g < 4; g++ {
			var sb strings.Builder
			for b := g * 4; b < g*4+4 && b < len(chunk); b++ {
				if b > g*4 {
					sb.WriteByte(' ')
				}
				sb.WriteByte(hexDigits[chunk[b]>>4])
				sb.WriteByte(hexDigits[chunk[b]&0x0F])
			}
			row.groups[g] = sb.String()
		}
		for i, c := range chunk {
			if c >= 0x20 && c < 0x7F {
				ascii[i] = c
			} else {
				ascii[i] = '.'
			}
		}
		row.ascii = string(ascii[:len(chunk)])
		rows = append(rows, row)
	}
	return rows
}

// filesLoadWindow reads the window [start, end) of path.
func filesLoadWindow(path string, start, end int64, hex bool) (filesTextWindow, error) {
	w := filesTextWindow{start: start, end: end}
	if end < start {
		return w, fmt.Errorf("empty window")
	}
	f, err := os.Open(path)
	if err != nil {
		return w, err
	}
	defer f.Close()
	data := make([]byte, end-start)
	n, err := f.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return w, err
	}
	data = data[:n]
	w.end = start + int64(n)
	if hex {
		w.hex = filesHexRows(data, start)
		return w, nil
	}
	if start > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], start-1); err == nil && prev[0] != '\n' {
			w.startsMidLine = true
		}
	}
	w.lines, w.lineNo = filesSplitRows(data, w.startsMidLine)
	return w, nil
}

// filesNextWindowRange is the window after [start, end).
func filesNextWindowRange(path string, end, total int64, hex bool) (int64, int64, error) {
	if end >= total {
		return 0, 0, io.EOF
	}
	if hex {
		next := end + filesHexWindowBytes
		if next > total {
			next = total
		}
		return end, next, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return end, filesWindowEnd(f, end, filesWindowBytes, total), nil
}

// filesPrevWindowRange is the window before the one starting at start.
func filesPrevWindowRange(path string, start int64, hex bool) (int64, int64, error) {
	if start <= 0 {
		return 0, 0, io.EOF
	}
	if hex {
		prev := start - filesHexWindowBytes
		if prev < 0 {
			prev = 0
		}
		return prev, start, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return filesWindowStart(f, start, filesWindowBytes), start, nil
}

// filesEdgeWindowRange is the first window of the file, or its last one when
// atEnd is set (the end of a log is usually what it is opened for).
func filesEdgeWindowRange(path string, atEnd bool, total int64, hex bool) (int64, int64, error) {
	if hex {
		start := int64(0)
		if atEnd && total > filesHexWindowBytes {
			start = total - filesHexWindowBytes
			start -= start % filesHexRowBytes
		}
		end := start + filesHexWindowBytes
		if end > total {
			end = total
		}
		return start, end, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if atEnd {
		return filesWindowStart(f, total, filesWindowBytes), total, nil
	}
	return 0, filesWindowEnd(f, 0, filesWindowBytes, total), nil
}

// filesLooksBinary sniffs the start of a file: a NUL byte, or bytes that are
// not UTF-8, mark data the editor must not turn into lines.
func filesLooksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	return fileEditorLooksBinary(buf[:n]), nil
}

// --- Line index -------------------------------------------------------------

// filesLineIndex counts the newlines of one large file in the background.
// counts[i] is the number of newlines before offset i*filesLineIndexStep.
type filesLineIndex struct {
	mu     sync.Mutex
	path   string
	size   int64
	counts []int64
	total  int64
	done   bool
	cancel context.CancelFunc
}

var filesLines filesLineIndex

// filesLineIndexStart begins counting path's lines, replacing any scan of a
// file opened before it.
func filesLineIndexStart(path string, size int64) {
	ctx, cancel := context.WithCancel(context.Background())
	filesLines.mu.Lock()
	if filesLines.cancel != nil {
		filesLines.cancel()
	}
	filesLines.path, filesLines.size = path, size
	filesLines.counts = []int64{0}
	filesLines.total, filesLines.done = 0, false
	filesLines.cancel = cancel
	filesLines.mu.Unlock()
	go filesLineIndexRun(ctx, path)
}

// filesLineIndexStop cancels the scan (the viewer was closed).
func filesLineIndexStop() {
	filesLines.mu.Lock()
	if filesLines.cancel != nil {
		filesLines.cancel()
		filesLines.cancel = nil
	}
	filesLines.path = ""
	filesLines.counts = nil
	filesLines.done = false
	filesLines.mu.Unlock()
}

func filesLineIndexRun(ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var newlines, pos int64
	next := filesLineIndexStep
	var last byte = '\n'
	for {
		if ctx.Err() != nil {
			return
		}
		n, rerr := f.Read(buf)
		data := buf[:n]
		if n > 0 {
			last = data[n-1]
		}
		for len(data) > 0 {
			take := int64(len(data))
			if pos+take > next {
				take = next - pos
			}
			newlines += int64(bytes.Count(data[:take], []byte{'\n'}))
			pos += take
			data = data[take:]
			if pos == next {
				filesLines.mu.Lock()
				if filesLines.path != path {
					filesLines.mu.Unlock()
					return
				}
				filesLines.counts = append(filesLines.counts, newlines)
				filesLines.mu.Unlock()
				next += filesLineIndexStep
			}
		}
		if rerr != nil {
			break
		}
	}
	filesLines.mu.Lock()
	if filesLines.path == path {
		filesLines.total = newlines
		if last != '\n' {
			// A last line without a newline is still a line.
			filesLines.total++
		}
		filesLines.done = true
	}
	filesLines.mu.Unlock()
}

// filesLineIndexProgress reports how far the scan of path has come (0..1) and
// the total line count once it is done.
func filesLineIndexProgress(path string) (float64, int64, bool) {
	filesLines.mu.Lock()
	defer filesLines.mu.Unlock()
	if filesLines.path != path || filesLines.size <= 0 {
		return 0, 0, false
	}
	if filesLines.done {
		return 1, filesLines.total, true
	}
	scanned := int64(len(filesLines.counts)-1) * filesLineIndexStep
	return float64(scanned) / float64(filesLines.size), 0, false
}

// filesLineNumberAt returns the 1-based number of the line that starts at
// offset, once the scan has passed the checkpoint before it. The remainder is
// counted with one read of at most filesLineIndexStep bytes.
func filesLineNumberAt(path string, offset int64) (int64, bool) {
	filesLines.mu.Lock()
	if filesLines.path != path {
		filesLines.mu.Unlock()
		return 0, false
	}
	idx := offset / filesLineIndexStep
	if idx >= int64(len(filesLines.counts)) {
		filesLines.mu.Unlock()
		return 0, false
	}
	base := filesLines.counts[idx]
	filesLines.mu.Unlock()
	from := idx * filesLineIndexStep
	if offset > from {
		f, err := os.Open(path)
		if err != nil {
			return 0, false
		}
		defer f.Close()
		buf := make([]byte, offset-from)
		n, err := f.ReadAt(buf, from)
		if err != nil && err != io.EOF {
			return 0, false
		}
		base += int64(bytes.Count(buf[:n], []byte{'\n'}))
	}
	return base + 1, true
}
