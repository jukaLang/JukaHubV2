package bootprefetch

// bootprefetch_test.go pins the list an early loader reads from the card
// before anything else: a damaged, cut or out-of-bounds list is refused
// whole, and a listed file that is no longer what the list says is not read.

import (
	"fmt"
	"hash/crc32"
	"path"
	"slices"
	"strings"
	"testing"
)

// fakeIO answers run's opens from a table and records what it asks for.
type fakeIO struct {
	files  map[string]Info
	paths  []string
	queued []string
	closed int
}

func (f *fakeIO) open(p string) (int, Info, bool) {
	info, ok := f.files[p]
	if !ok {
		return -1, Info{}, false
	}
	f.paths = append(f.paths, p)
	return len(f.paths) - 1, info, true
}

func (f *fakeIO) queue(fd int, off, n int64) {
	f.queued = append(f.queued, fmt.Sprintf("%s@%d+%d", path.Base(f.paths[fd]), off, n))
}

func (f *fakeIO) finish() {}

func (f *fakeIO) close(int) { f.closed++ }

func withEnd(body string) string {
	return body + fmt.Sprintf("end %08x\n", crc32.ChecksumIEEE([]byte(body)))
}

func TestBootPrefetchList(t *testing.T) {
	const (
		exe  = "/mnt/SDCARD/Apps/JukaHub/JukaHub"
		lib  = "/usr/lib/libSDL2-2.0.so.0"
		font = "/mnt/SDCARD/Apps/JukaHub/Inter-Regular.ttf"
	)
	good := header + "\n" +
		"f 1048576 1700000000000000000 " + exe + "\n" +
		"e 0 0 8192\n" +
		"e 0 65536 4096\n" +
		"e 1 8192 300000\n" +
		"f 5000 42 " + lib + "\n" +
		"e 1 0 5000\n" +
		"w " + font + "\n"
	exeInfo := Info{Regular: true, Size: 1048576, MtimeNs: 1700000000000000000}
	libInfo := Info{Regular: true, Size: 5000, MtimeNs: 42}
	fontInfo := Info{Regular: true, Size: 1000, MtimeNs: 7}
	all := map[string]Info{exe: exeInfo, lib: libInfo, font: fontInfo}
	// Phase 0 of every file, then phase 1 (in requests of at most Chunk),
	// then the whole files.
	ordered := []string{
		"JukaHub@0+8192", "JukaHub@65536+4096",
		"JukaHub@8192+131072", "JukaHub@139264+131072", "JukaHub@270336+37856",
		"libSDL2-2.0.so.0@0+5000",
		"Inter-Regular.ttf@0+1000",
	}
	withoutLib := slices.DeleteFunc(slices.Clone(ordered), func(s string) bool { return strings.HasPrefix(s, "libSDL2") })
	withoutFont := ordered[:len(ordered)-1]
	changedLib := libInfo
	changedLib.MtimeNs++
	grownExe := exeInfo
	grownExe.Size++
	hugeFont := fontInfo
	hugeFont.Size = MaxWholeBytes + 1
	head := header + "\n"
	exeLine := "f 1048576 1 " + exe + "\n"

	cases := []struct {
		name    string
		data    string
		wantErr bool
		// files, when set, runs the parsed list against them.
		files      map[string]Info
		wantQueued []string
		wantBytes  int64
		wantWhy    string
	}{
		{name: "valid list: phase 0, phase 1, whole files", data: withEnd(good), files: all,
			wantQueued: ordered, wantBytes: 318288},
		{name: "library changed since: skipped", data: withEnd(good),
			files:      map[string]Info{exe: exeInfo, lib: changedLib, font: fontInfo},
			wantQueued: withoutLib, wantBytes: 313288},
		{name: "library gone: skipped", data: withEnd(good),
			files:      map[string]Info{exe: exeInfo, font: fontInfo},
			wantQueued: withoutLib, wantBytes: 313288},
		{name: "library not a regular file: skipped", data: withEnd(good),
			files:      map[string]Info{exe: exeInfo, lib: {Size: 5000, MtimeNs: 42}, font: fontInfo},
			wantQueued: withoutLib, wantBytes: 313288},
		{name: "executable changed: nothing read", data: withEnd(good),
			files:   map[string]Info{exe: grownExe, lib: libInfo, font: fontInfo},
			wantWhy: "JukaHub changed since the list was made"},
		{name: "list for another executable: nothing read",
			data: withEnd(strings.Replace(good, exe, "/mnt/SDCARD/Apps/Other/JukaHub", 1)), files: all,
			wantWhy: "the list is for another copy of JukaHub"},
		{name: "whole file over its budget: skipped", data: withEnd(good),
			files:      map[string]Info{exe: exeInfo, lib: libInfo, font: hugeFont},
			wantQueued: withoutFont, wantBytes: 317288},

		{name: "empty", data: "", wantErr: true},
		{name: "zero-filled by a power cut", data: strings.Repeat("\x00", 4096), wantErr: true},
		{name: "checksum wrong", data: good + "end 00000000\n", wantErr: true},
		{name: "cut short", data: withEnd(good)[:len(withEnd(good))-5], wantErr: true},
		{name: "line after the end line", data: withEnd(good) + "w /x\n", wantErr: true},
		{name: "other format version", data: withEnd(strings.Replace(good, "prefetch 1\n", "prefetch 2\n", 1)), wantErr: true},
		{name: "Windows line endings", data: withEnd(strings.ReplaceAll(good, "\n", "\r\n")), wantErr: true},
		{name: "no files", data: withEnd(head), wantErr: true},
		{name: "range before any file", data: withEnd(head + "e 0 0 1\n"), wantErr: true},
		{name: "range past the end of its file", data: withEnd(head + "f 5000 1 " + lib + "\ne 0 0 5001\n"), wantErr: true},
		{name: "ranges out of order", data: withEnd(head + exeLine + "e 0 8192 4096\ne 0 0 4096\n"), wantErr: true},
		{name: "ranges overlap", data: withEnd(head + exeLine + "e 1 0 8192\ne 1 4096 4096\n"), wantErr: true},
		{name: "unknown phase", data: withEnd(head + exeLine + "e 2 0 4096\n"), wantErr: true},
		{name: "empty range", data: withEnd(head + exeLine + "e 0 0 0\n"), wantErr: true},
		{name: "negative offset", data: withEnd(head + exeLine + "e 0 -4096 4096\n"), wantErr: true},
		{name: "over the byte cap", data: withEnd(head + "f 67108864 1 " + exe + "\ne 0 0 33554433\n"), wantErr: true},
		{name: "relative path", data: withEnd(head + "f 1 1 JukaHub\n"), wantErr: true},
		{name: "path not clean", data: withEnd(head + "f 1 1 /mnt/SDCARD/../JukaHub\n"), wantErr: true},
		{name: "device file", data: withEnd(head + exeLine + "f 1 1 /dev/watchdog\n"), wantErr: true},
		{name: "same file twice", data: withEnd(head + exeLine + exeLine), wantErr: true},
		{name: "file after a whole file", data: withEnd(head + exeLine + "w " + font + "\nf 1 1 " + lib + "\n"), wantErr: true},
		{name: "unknown entry", data: withEnd(head + exeLine + "x 0 0 1\n"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := Parse([]byte(tc.data))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse accepted %q", tc.data)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := string(Encode(l)); got != tc.data {
				t.Fatalf("Encode(Parse(x)) = %q, want %q", got, tc.data)
			}
			if tc.files == nil {
				return
			}
			fio := &fakeIO{files: tc.files}
			st := run(l, exe, fio)
			if st.Why != tc.wantWhy || st.Bytes != tc.wantBytes || !slices.Equal(fio.queued, tc.wantQueued) {
				t.Fatalf("run: why %q bytes %d queued %q; want why %q bytes %d queued %q",
					st.Why, st.Bytes, fio.queued, tc.wantWhy, tc.wantBytes, tc.wantQueued)
			}
			if fio.closed != len(fio.paths) {
				t.Fatalf("run closed %d of %d opened files", fio.closed, len(fio.paths))
			}
		})
	}
}
