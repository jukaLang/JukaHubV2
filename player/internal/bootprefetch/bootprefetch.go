// Package bootprefetch asks the card for the parts of JukaHub and its
// libraries that a cold start is about to read, before the start reads them.
//
// JukaHub's executable is large and the start touches scattered pages of it,
// and of the shared libraries (SDL, the image and font codecs, the GPU
// driver). Each page the start touches first is a page fault on the thread
// that needs it: a small random read from the SD card, or a squashfs block
// decompressed on that thread. A list recorded at an earlier cold start
// (player/bootprefetch_record_linux.go) names those ranges; at the next cold
// start this package's init starts a goroutine that asks the kernel for them,
// as few large ordered reads, while the main package initialises and SDL and
// the GPU driver start. Like systemd-readahead or Android's iorap, but for one
// program.
//
// The list lives in the data folder beside the executable
// (<exe dir>/data/boot-prefetch.list), so it is found without the main package.
// It is text, one entry per line:
//
//	jukahub-boot-prefetch 1           header and format version
//	f <size> <mtime ns> <path>        a file; the first one is the executable
//	e <phase> <offset> <length>       a byte range of the file above
//	w <path>                          a start-up file read whole, after the rest
//	end <crc32>                       IEEE CRC-32 of every byte before this line
//
// Phase 0 ranges were in use by the end of package initialisation, phase 1
// ones only by the first frame; every phase 0 range is asked for first. A file
// is only read when it is still the file the list was made from (regular, same
// size and modification time); when the executable is not, the whole list is
// passed over: it belongs to another build. Inode and device numbers are not
// compared: FAT and exFAT number inodes afresh at every mount.
//
// Nothing here can make a start fail: every error skips silently, all work is
// on the goroutine, nothing runs under `go test`, and the reads are hints the
// kernel may drop. JUKAHUB_BOOT_PREFETCH=0 (or a file named boot-prefetch.off
// in the data folder) switches it off; =1 makes any start, not only one in
// the first minute after the kernel booted, use the list.
package bootprefetch

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ListName is the list's file name in the data folder.
	ListName = "boot-prefetch.list"
	// OffName in the data folder switches the prefetch off.
	OffName = "boot-prefetch.off"
	// EnvName is the environment switch: 0 off, 1 on at every start.
	EnvName = "JUKAHUB_BOOT_PREFETCH"

	// MaxBytes caps the ranges of one list: more than a start reads is only
	// card time taken from the reads the start waits for.
	MaxBytes = 32 << 20
	// MaxExtents, MaxFiles and MaxWhole bound the list's entries.
	MaxExtents = 4096
	MaxFiles   = 256
	MaxWhole   = 16
	// MaxWholeBytes caps the start-up files read whole.
	MaxWholeBytes = 8 << 20

	// Chunk is the largest single request: the kernel caps one read-ahead
	// hint at its read-ahead window (read_ahead_kb, 128 KiB by default), and
	// drops the rest of a larger one.
	Chunk = 128 << 10
	// Window is how much the loader keeps asked for and not yet arrived. A
	// page fault of the start that lands behind the queue waits for it, so it
	// stays short (tens of milliseconds of card time) while the card is
	// still never idle.
	Window = 1 << 20

	header      = "jukahub-boot-prefetch 1"
	maxListSize = 512 << 10
	maxPathLen  = 4096
)

// Extent is one byte range of a listed file.
type Extent struct {
	Phase uint8
	Off   int64
	Len   int64
}

// File is a listed file: what it was when the list was made, and its ranges.
type File struct {
	Path    string
	Size    int64
	MtimeNs int64
	Extents []Extent
}

// List is a parsed boot prefetch list. Files[0] is the executable.
type List struct {
	Files []File
	Whole []string
}

// Info is what an opened file is now.
type Info struct {
	Regular bool
	Size    int64
	MtimeNs int64
}

// Stats is one start's prefetch.
type Stats struct {
	// Listed is the bytes of ranges the list holds, Bytes the bytes asked
	// for (so far, while it runs), in Extents ranges of Files files.
	Listed  int64
	Bytes   int64
	Extents int
	Files   int
	// Took is from the loader's start to its last request answered.
	Took time.Duration
	// Why says why nothing was asked for; empty when the list ran.
	Why string
	// Off is true when a switch turned the prefetch off.
	Off bool
}

// Matches reports whether info is still the file f was listed as.
func (f *File) Matches(info Info) bool {
	return info.Regular && info.Size == f.Size && info.MtimeNs == f.MtimeNs
}

// Bytes is the sum of the list's ranges.
func (l *List) Bytes() int64 {
	var n int64
	for _, f := range l.Files {
		for _, e := range f.Extents {
			n += e.Len
		}
	}
	return n
}

// ExtentCount is the number of the list's ranges.
func (l *List) ExtentCount() int {
	n := 0
	for _, f := range l.Files {
		n += len(f.Extents)
	}
	return n
}

// ListPath is where the list for the executable at exe lives.
func ListPath(exe string) string {
	return filepath.Join(filepath.Dir(exe), "data", ListName)
}

// ValidPath reports whether p may be listed: absolute, clean, one line, and
// not a device or kernel file (opening one can do something).
func ValidPath(p string) bool {
	if p == "" || len(p) > maxPathLen || p[0] != '/' || path.Clean(p) != p || strings.ContainsAny(p, "\x00\r\n") {
		return false
	}
	for _, dir := range []string{"/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(p, dir) {
			return false
		}
	}
	return true
}

// Encode writes l in the list format. The recorder parses the result again
// before saving it, so a list the loader would refuse is never written.
func Encode(l *List) []byte {
	var b bytes.Buffer
	b.WriteString(header + "\n")
	for _, f := range l.Files {
		fmt.Fprintf(&b, "f %d %d %s\n", f.Size, f.MtimeNs, f.Path)
		for _, e := range f.Extents {
			fmt.Fprintf(&b, "e %d %d %d\n", e.Phase, e.Off, e.Len)
		}
	}
	for _, p := range l.Whole {
		fmt.Fprintf(&b, "w %s\n", p)
	}
	fmt.Fprintf(&b, "end %08x\n", crc32.ChecksumIEEE(b.Bytes()))
	return b.Bytes()
}

// Parse reads a list and checks all of it: the checksum (a list cut short
// or zero-filled by a power cut is refused whole), the header, every number,
// that each range lies inside its file and the ranges of one file and phase
// are in order without overlap, and the caps.
func Parse(data []byte) (*List, error) {
	if len(data) == 0 || len(data) > maxListSize || data[len(data)-1] != '\n' {
		return nil, errors.New("empty, too large or cut short")
	}
	cut := bytes.LastIndexByte(data[:len(data)-1], '\n')
	if cut < 0 {
		return nil, errors.New("cut short")
	}
	body, last := data[:cut+1], string(data[cut+1:len(data)-1])
	sum, ok := strings.CutPrefix(last, "end ")
	if !ok || len(sum) != 8 {
		return nil, errors.New("no end line")
	}
	want, err := strconv.ParseUint(sum, 16, 32)
	if err != nil || uint32(want) != crc32.ChecksumIEEE(body) {
		return nil, errors.New("checksum does not match")
	}
	lines := strings.Split(string(body[:len(body)-1]), "\n")
	if lines[0] != header {
		return nil, errors.New("not a list of this version")
	}
	l := &List{}
	var cur *File
	var prevEnd [2]int64
	var total int64
	extents := 0
	files := make(map[string]bool)
	for i, line := range lines[1:] {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "f":
			sizeText, more, ok1 := strings.Cut(rest, " ")
			mtimeText, p, ok2 := strings.Cut(more, " ")
			size, err1 := strconv.ParseInt(sizeText, 10, 64)
			mtime, err2 := strconv.ParseInt(mtimeText, 10, 64)
			if !ok1 || !ok2 || err1 != nil || err2 != nil || size < 0 || !ValidPath(p) || files[p] ||
				len(l.Files) == MaxFiles || len(l.Whole) > 0 {
				return nil, badLine(i+2, line)
			}
			files[p] = true
			l.Files = append(l.Files, File{Path: p, Size: size, MtimeNs: mtime})
			cur = &l.Files[len(l.Files)-1]
			prevEnd = [2]int64{}
		case "e":
			fields := strings.Split(rest, " ")
			if cur == nil || len(l.Whole) > 0 || len(fields) != 3 {
				return nil, badLine(i+2, line)
			}
			phase, err1 := strconv.ParseUint(fields[0], 10, 8)
			off, err2 := strconv.ParseInt(fields[1], 10, 64)
			n, err3 := strconv.ParseInt(fields[2], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil || phase > 1 || off < 0 || n <= 0 ||
				off > cur.Size-n || off < prevEnd[phase] || extents == MaxExtents || n > MaxBytes-total {
				return nil, badLine(i+2, line)
			}
			prevEnd[phase] = off + n
			total += n
			extents++
			cur.Extents = append(cur.Extents, Extent{Phase: uint8(phase), Off: off, Len: n})
		case "w":
			if !ValidPath(rest) || len(l.Whole) == MaxWhole || slices.Contains(l.Whole, rest) {
				return nil, badLine(i+2, line)
			}
			l.Whole = append(l.Whole, rest)
		default:
			return nil, badLine(i+2, line)
		}
	}
	if len(l.Files) == 0 {
		return nil, errors.New("no executable listed")
	}
	return l, nil
}

func badLine(n int, line string) error {
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return fmt.Errorf("line %d: %q", n, line)
}

// ── Running a list ────────────────────────────────────────────────────────

// fileIO is how run reaches the files: the kernel's read-ahead in the loader
// (load_linux.go), a fake in the test.
type fileIO interface {
	// open opens path for reading and says what it is; ok is false when it
	// cannot be opened. An opened file is closed by close.
	open(path string) (fd int, info Info, ok bool)
	// queue asks for one range of at most Chunk bytes.
	queue(fd int, off, n int64)
	// finish waits for what is still asked for.
	finish()
	close(fd int)
}

// run asks for the list's ranges in the order a start needs them: every
// phase 0 range (the executable's first, then each library's in list order),
// then every phase 1 range, then the start-up files read whole. A file is
// opened when its first range comes up, so a library the start loads late
// costs nothing before the executable's ranges are on their way. exe is the
// running executable's path: a list made for another one, or for this one
// before it changed, asks for nothing.
func run(l *List, exe string, fio fileIO) Stats {
	st := Stats{Listed: l.Bytes()}
	if len(l.Files) == 0 || l.Files[0].Path != exe {
		st.Why = "the list is for another copy of JukaHub"
		return st
	}
	fds := make([]int, len(l.Files))
	state := make([]int8, len(l.Files)) // 0 not opened yet, 1 open and current, -1 skipped
	var opened []int
	defer func() {
		fio.finish()
		for _, fd := range opened {
			fio.close(fd)
		}
	}()
	use := func(i int) bool {
		if state[i] == 0 {
			state[i] = -1
			fd, info, ok := fio.open(l.Files[i].Path)
			if ok {
				opened = append(opened, fd)
			}
			if ok && l.Files[i].Matches(info) {
				state[i], fds[i] = 1, fd
				st.Files++
			}
		}
		return state[i] == 1
	}
	if !use(0) {
		st.Why = "JukaHub changed since the list was made"
		return st
	}
	for phase := range uint8(2) {
		for i := range l.Files {
			for _, e := range l.Files[i].Extents {
				if e.Phase != phase {
					continue
				}
				if !use(i) {
					break
				}
				queueRange(fio, fds[i], e.Off, e.Len)
				st.Extents++
				st.Bytes += e.Len
			}
		}
	}
	// The start-up files read whole are JukaHub's own, in its folder: they
	// are read with no identity to match, so nothing else is.
	dir := strings.TrimSuffix(path.Dir(exe), "/") + "/"
	budget := int64(MaxWholeBytes)
	for _, p := range l.Whole {
		if !strings.HasPrefix(p, dir) {
			continue
		}
		fd, info, ok := fio.open(p)
		if !ok {
			continue
		}
		opened = append(opened, fd)
		if !info.Regular || info.Size <= 0 || info.Size > budget {
			continue
		}
		queueRange(fio, fd, 0, info.Size)
		budget -= info.Size
		st.Files++
		st.Extents++
		st.Bytes += info.Size
	}
	return st
}

// queueRange asks for [off, off+n) in requests of at most Chunk bytes.
func queueRange(fio fileIO, fd int, off, n int64) {
	for n > 0 {
		c := min(n, Chunk)
		fio.queue(fd, off, c)
		off += c
		n -= c
	}
}
