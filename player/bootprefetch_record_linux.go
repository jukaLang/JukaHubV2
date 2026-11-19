//go:build linux

package main

// bootprefetch_record_linux.go — records which parts of JukaHub and its
// libraries a cold start reads before Home, so the boot prefetch
// (internal/bootprefetch, whose init runs before this package's) can ask the
// card for them at the next cold start.
//
// Where available, /proc/self/pagemap records pages present in this process's
// page tables: pages this start touched (or the kernel mapped around one it
// touched). Stock Smart Pro kernels omit that file, so mincore records the
// executable pages in the page cache instead. The first cold start gives a
// useful list there; later lists may retain pages read by the previous
// prefetch. The mappings are looked at twice: on a goroutine from this file's
// init, once every package variable is initialised (phase 0, what the next
// start needs first; the init functions of the files after this one run beside
// it), and after the first frame (phase 1). The two
// sets become ranges of the files they belong to, merged across small gaps
// (one larger read beats two small ones on an SD card), capped, and saved in
// the data folder only when they changed: the files on disk (an update
// replaced JukaHub or a library), or the ranges by more than a little, so a
// cold start does not write the card each time.

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"jukahub/JukaHub/internal/bootprefetch"
)

// bootPrefetchMergeGap: two touched ranges of a file closer than this are read
// as one. On an SD card a request costs about as much as reading a few dozen
// KiB, and the kernel's own fault read-around is 128 KiB.
const bootPrefetchMergeGap = 64 << 10

// bootPrefetchMaxMapping: a larger mapping is not looked at (data someone
// mapped, not code a start runs).
const bootPrefetchMaxMapping = 256 << 20

// bootPrefetchRewriteMin: ranges that moved by less than this (or an eighth of
// the list, when that is more) are not worth a card write.
const bootPrefetchRewriteMin = 1 << 20

// bootPrefetchEarly carries the phase 0 snapshot from init to the recording.
var bootPrefetchEarly = make(chan *bpSnapshot, 1)

// bootPrefetchEarlyStarted is set by init when the phase 0 snapshot runs.
var bootPrefetchEarlyStarted bool

func init() {
	if !bootprefetch.Active() {
		return
	}
	bootPrefetchEarlyStarted = true
	go func() {
		var snap *bpSnapshot
		defer func() {
			_ = recover()
			bootPrefetchEarly <- snap
		}()
		snap = bootPrefetchSnapshot()
	}()
}

// bootPrefetchFirstFrame is startupFirstFrameReached's call (main thread,
// once): one log line about this start's prefetch, then on a cold start the
// recording for the next one, on a goroutine. A bench run (it exits at this
// frame) only logs.
func bootPrefetchFirstFrame() {
	st, done := bootprefetch.Result()
	if !bootprefetch.Active() && !st.Off {
		return
	}
	logInfo("startup", "%s", bootPrefetchSummary(st, done))
	if st.Off || !bootprefetch.Active() || startupBenchEnabled() {
		return
	}
	go bootPrefetchRecord()
}

// bootPrefetchSummary is the log line: "boot prefetch: 14.2 MB in 31 extents
// (6 files) read in 412 ms" (from the loader's start until the card had the
// last range), or why nothing was read.
func bootPrefetchSummary(st bootprefetch.Stats, done bool) string {
	switch {
	case st.Off:
		return "boot prefetch: " + st.Why
	case !done:
		return fmt.Sprintf("boot prefetch: %s of %s asked for so far, still reading (%d ms)",
			bootPrefetchMB(st.Bytes), bootPrefetchMB(st.Listed), st.Took.Milliseconds())
	case st.Why != "":
		return "boot prefetch: nothing read (" + st.Why + ")"
	}
	return fmt.Sprintf("boot prefetch: %s in %d extents (%d files) read in %d ms",
		bootPrefetchMB(st.Bytes), st.Extents, st.Files, st.Took.Milliseconds())
}

// bootPrefetchReportField is a short field for the startup line (startup.go
// startupReport may append it): " prefetch=14.2MB/412ms" when the list ran,
// " prefetch=9.1/14.2MB" while it still reads, " prefetch=none" or
// " prefetch=off", and nothing on a start the prefetch is not for.
func bootPrefetchReportField() string {
	st, done := bootprefetch.Result()
	mb := func(n int64) string { return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) }
	switch {
	case st.Off:
		return " prefetch=off"
	case !bootprefetch.Active():
		return ""
	case !done:
		return " prefetch=" + mb(st.Bytes) + "/" + mb(st.Listed) + "MB"
	case st.Why != "":
		return " prefetch=none"
	}
	return " prefetch=" + mb(st.Bytes) + "MB/" + strconv.FormatInt(st.Took.Milliseconds(), 10) + "ms"
}

func bootPrefetchMB(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
}

// bootPrefetchRecord makes the list for the next cold start and saves it when
// it changed. It waits for this start's loader first: the list it read is
// what the new one is compared with.
func bootPrefetchRecord() {
	defer func() { _ = recover() }()
	late := bootPrefetchSnapshot()
	st, done := bootprefetch.Wait(10 * time.Second)
	if late == nil || !done || st.Off {
		return
	}
	var early *bpSnapshot
	if bootPrefetchEarlyStarted {
		select {
		case early = <-bootPrefetchEarly:
		case <-time.After(2 * time.Second):
		}
	}
	list := bootPrefetchBuild(early, late, bootPrefetchWholeFiles(late.exe))
	if list == nil {
		return
	}
	data := bootprefetch.Encode(list)
	if _, err := bootprefetch.Parse(data); err != nil {
		logWarn("startup", "boot prefetch: list not saved: %v", err)
		return
	}
	if !bootPrefetchChanged(bootprefetch.Previous(), list) {
		return
	}
	if err := bootPrefetchSave(bootprefetch.ListPath(late.exe), data); err != nil {
		logWarn("startup", "boot prefetch: list not saved: %v", err)
		return
	}
	logInfo("startup", "boot prefetch: saved %s in %d extents of %d files for the next start",
		bootPrefetchMB(list.Bytes()), list.ExtentCount(), len(list.Files))
}

// ── Snapshots ─────────────────────────────────────────────────────────────

// bpRange is the byte range [off, end) of a file.
type bpRange struct{ off, end int64 }

// bpFile is one mapped file of a snapshot: its inode, to tell it from a file
// put at the same path since, whether any of it is mapped executable (code:
// the executable and the libraries, not a database or cache file something
// mapped, which changes on disk all the time), and the ranges of it this
// process has touched.
type bpFile struct {
	ino    uint64
	code   bool
	ranges []bpRange
}

// bpSnapshot is what this process has touched of its mapped files at one
// moment. order is the executable first, then the libraries as mapped.
type bpSnapshot struct {
	exe   string
	order []string
	files map[string]*bpFile
}

func (f *bpFile) add(off, end int64) {
	if n := len(f.ranges); n > 0 && f.ranges[n-1].end == off {
		f.ranges[n-1].end = end
		return
	}
	f.ranges = append(f.ranges, bpRange{off: off, end: end})
}

// bpMapping is one line of /proc/self/maps.
type bpMapping struct {
	start, end uint64
	off        int64
	ino        uint64
	path       string
	exec       bool
}

// parseBootPrefetchMapsLine reads a /proc/self/maps line ("start-end perms
// offset dev inode path") and keeps readable mappings of files on disk: not
// anonymous memory, not a deleted file, not a device.
func parseBootPrefetchMapsLine(line string) (bpMapping, bool) {
	var f [5]string
	rest := line
	for i := range f {
		f[i], rest, _ = strings.Cut(strings.TrimLeft(rest, " "), " ")
		if f[i] == "" {
			return bpMapping{}, false
		}
	}
	path := strings.TrimLeft(rest, " ")
	if len(f[1]) < 4 || f[1][0] != 'r' || strings.HasSuffix(path, " (deleted)") || !bootprefetch.ValidPath(path) {
		return bpMapping{}, false
	}
	lo, hi, ok := strings.Cut(f[0], "-")
	start, err1 := strconv.ParseUint(lo, 16, 64)
	end, err2 := strconv.ParseUint(hi, 16, 64)
	off, err3 := strconv.ParseInt(f[2], 16, 64)
	ino, err4 := strconv.ParseUint(f[4], 10, 64)
	if !ok || err1 != nil || err2 != nil || err3 != nil || err4 != nil || ino == 0 || off < 0 || end <= start {
		return bpMapping{}, false
	}
	return bpMapping{start: start, end: end, off: off, ino: ino, path: path, exec: f[1][2] == 'x'}, true
}

// bootPrefetchSnapshot reads which pages of its mapped files this process has
// in its page tables: bit 63 (present) or 62 (swapped) of each page's
// /proc/self/pagemap entry. nil when that cannot be read, or says nothing of
// the executable (a kernel that hides it).
func bootPrefetchSnapshot() *bpSnapshot {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return nil
	}
	pm, _ := os.Open("/proc/self/pagemap")
	if pm != nil {
		defer pm.Close()
	}
	page := int64(os.Getpagesize())
	snap := &bpSnapshot{exe: exe, order: []string{exe}, files: make(map[string]*bpFile)}
	buf := make([]byte, 8*4096)
	for _, line := range strings.Split(string(maps), "\n") {
		m, ok := parseBootPrefetchMapsLine(line)
		if !ok || m.end-m.start > bootPrefetchMaxMapping || m.end > 1<<56 {
			continue
		}
		f := snap.files[m.path]
		if f == nil {
			f = &bpFile{ino: m.ino}
			snap.files[m.path] = f
			if m.path != exe {
				snap.order = append(snap.order, m.path)
			}
		} else if f.ino != m.ino {
			continue
		}
		f.code = f.code || m.exec
		if pm != nil {
			bootPrefetchReadPagemap(pm, buf, m, page, f)
		} else {
			bootPrefetchReadMincore(m, page, f)
		}
	}
	if f := snap.files[exe]; f == nil || len(f.ranges) == 0 {
		return nil
	}
	return snap
}

// bootPrefetchReadMincore records cached file pages on kernels without
// /proc/self/pagemap (including stock Smart Pro firmware). The mapping was
// already present in this process; mincore only inspects its page-cache state
// and does not touch the file's contents or pull new pages into memory.
func bootPrefetchReadMincore(m bpMapping, page int64, f *bpFile) {
	pages := int64(m.end-m.start) / page
	if pages <= 0 {
		return
	}
	vec := make([]byte, pages)
	_, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(m.start), uintptr(m.end-m.start), uintptr(unsafe.Pointer(&vec[0])))
	if errno != 0 {
		return // the mapping may have gone away since /proc/self/maps was read
	}
	for i, resident := range vec {
		if resident&1 != 0 {
			off := m.off + int64(i)*page
			f.add(off, off+page)
		}
	}
}

// bootPrefetchReadPagemap adds the touched pages of one mapping to f, as
// ranges of the file.
func bootPrefetchReadPagemap(pm *os.File, buf []byte, m bpMapping, page int64, f *bpFile) {
	first := int64(m.start) / page
	pages := int64(m.end-m.start) / page
	per := int64(len(buf) / 8)
	for i := int64(0); i < pages; {
		n := min(pages-i, per)
		got, _ := pm.ReadAt(buf[:n*8], (first+i)*8)
		read := int64(got / 8)
		for k := int64(0); k < read; k++ {
			if binary.NativeEndian.Uint64(buf[k*8:])>>62 == 0 {
				continue
			}
			off := m.off + (i+k)*page
			f.add(off, off+page)
		}
		if read < n {
			return
		}
		i += n
	}
}

// ── The list ──────────────────────────────────────────────────────────────

// bootPrefetchBuild makes the list from the two snapshots: for the executable
// and each library (code) still on disk as it was mapped, the ranges touched
// by the end of package initialisation (phase 0) and those touched after it
// until the first frame (phase 1), each merged across small gaps; phase 0
// everywhere is kept before phase 1 anywhere when the caps cut. Then the
// start-up files read whole. nil when the executable is not among them.
func bootPrefetchBuild(early, late *bpSnapshot, whole []string) *bootprefetch.List {
	page := int64(os.Getpagesize())
	type candidate struct {
		file   bootprefetch.File
		phases [2][]bpRange
	}
	var cands []candidate
	for _, path := range late.order {
		lf := late.files[path]
		if lf == nil || !lf.code || len(cands) == bootprefetch.MaxFiles {
			continue
		}
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Ino != lf.ino || st.Size <= 0 {
			continue
		}
		all := bpNormalize(lf.ranges, st.Size)
		var first []bpRange
		if early != nil {
			if ef := early.files[path]; ef != nil && ef.ino == lf.ino {
				first = bpNormalize(ef.ranges, st.Size)
			}
		}
		p0 := bpMergeGaps(first, bootPrefetchMergeGap)
		p1 := bpSubtract(bpMergeGaps(bpSubtract(all, first), bootPrefetchMergeGap), p0)
		cands = append(cands, candidate{
			file:   bootprefetch.File{Path: path, Size: st.Size, MtimeNs: st.Mtim.Nano()},
			phases: [2][]bpRange{p0, p1},
		})
	}
	if len(cands) == 0 || cands[0].file.Path != late.exe {
		return nil
	}
	budget := int64(bootprefetch.MaxBytes)
	extents := 0
	for phase := range 2 {
		for i := range cands {
			for _, r := range cands[i].phases[phase] {
				n := min(r.end-r.off, budget)
				if n < r.end-r.off {
					n -= n % page
				}
				if n <= 0 || extents == bootprefetch.MaxExtents {
					break
				}
				cands[i].file.Extents = append(cands[i].file.Extents, bootprefetch.Extent{Phase: uint8(phase), Off: r.off, Len: n})
				budget -= n
				extents++
			}
		}
	}
	if len(cands[0].file.Extents) == 0 {
		return nil
	}
	list := &bootprefetch.List{Whole: whole}
	for _, c := range cands {
		if len(c.file.Extents) > 0 {
			list.Files = append(list.Files, c.file)
		}
	}
	return list
}

// bootPrefetchWholeFiles are the start-up files startup_linux.go reads ahead
// (the config, the fonts, the update state), where they exist.
func bootPrefetchWholeFiles(exe string) []string {
	dir := filepath.Dir(exe)
	var out []string
	for _, name := range startupPrefetchFiles {
		p := filepath.Join(dir, name)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || !bootprefetch.ValidPath(p) {
			continue
		}
		out = append(out, p)
		if len(out) == bootprefetch.MaxWhole {
			break
		}
	}
	return out
}

// bootPrefetchChanged reports whether cur is worth saving over old: there is
// no old list, it is for another executable, a file in both changed on disk,
// the start-up files differ, or the ranges moved by more than
// bootPrefetchRewriteMin and an eighth of cur. Two cold starts rarely touch
// exactly the same pages.
func bootPrefetchChanged(old, cur *bootprefetch.List) bool {
	if old == nil || len(old.Files) == 0 || old.Files[0].Path != cur.Files[0].Path || !slices.Equal(old.Whole, cur.Whole) {
		return true
	}
	prev := make(map[string]*bootprefetch.File, len(old.Files))
	for i := range old.Files {
		prev[old.Files[i].Path] = &old.Files[i]
	}
	var moved int64
	for i := range cur.Files {
		c := &cur.Files[i]
		now := bpExtentRanges(c)
		o := prev[c.Path]
		if o == nil {
			moved += bpSize(now)
			continue
		}
		delete(prev, c.Path)
		if o.Size != c.Size || o.MtimeNs != c.MtimeNs {
			return true
		}
		before := bpExtentRanges(o)
		moved += bpSize(before) + bpSize(now) - 2*bpOverlap(before, now)
	}
	for _, o := range prev {
		moved += bpSize(bpExtentRanges(o))
	}
	return moved > max(bootPrefetchRewriteMin, cur.Bytes()/8)
}

// bootPrefetchSave replaces the list: written under a fresh name in the same
// folder (os.CreateTemp, never through a link left there) and renamed over
// it. No sync: a list a power cut damaged fails its checksum and is recorded
// again at the next cold start.
func bootPrefetchSave(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pattern := bootprefetch.ListName + ".*.tmp"
	// Left by a start stopped between the two steps.
	stale, _ := filepath.Glob(filepath.Join(dir, pattern))
	for _, p := range stale {
		_ = os.Remove(p)
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// ── Ranges ────────────────────────────────────────────────────────────────

// bpNormalize sorts ranges, joins overlapping and touching ones and clips
// them to size.
func bpNormalize(rs []bpRange, size int64) []bpRange {
	s := slices.Clone(rs)
	slices.SortFunc(s, func(a, b bpRange) int { return cmp.Compare(a.off, b.off) })
	out := s[:0]
	for _, r := range s {
		r.end = min(r.end, size)
		if r.off < 0 || r.off >= r.end {
			continue
		}
		if n := len(out); n > 0 && r.off <= out[n-1].end {
			out[n-1].end = max(out[n-1].end, r.end)
			continue
		}
		out = append(out, r)
	}
	return out
}

// bpMergeGaps joins sorted ranges less than gap apart.
func bpMergeGaps(rs []bpRange, gap int64) []bpRange {
	var out []bpRange
	for _, r := range rs {
		if n := len(out); n > 0 && r.off-out[n-1].end < gap {
			out[n-1].end = max(out[n-1].end, r.end)
			continue
		}
		out = append(out, r)
	}
	return out
}

// bpSubtract is a without b, both sorted and disjoint.
func bpSubtract(a, b []bpRange) []bpRange {
	var out []bpRange
	j := 0
	for _, r := range a {
		for j < len(b) && b[j].end <= r.off {
			j++
		}
		cur := r.off
		for k := j; k < len(b) && b[k].off < r.end; k++ {
			if b[k].off > cur {
				out = append(out, bpRange{off: cur, end: b[k].off})
			}
			cur = max(cur, b[k].end)
		}
		if cur < r.end {
			out = append(out, bpRange{off: cur, end: r.end})
		}
	}
	return out
}

// bpOverlap is the bytes a and b (both sorted and disjoint) share.
func bpOverlap(a, b []bpRange) int64 {
	var n int64
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if lo, hi := max(a[i].off, b[j].off), min(a[i].end, b[j].end); lo < hi {
			n += hi - lo
		}
		if a[i].end < b[j].end {
			i++
		} else {
			j++
		}
	}
	return n
}

func bpSize(rs []bpRange) int64 {
	var n int64
	for _, r := range rs {
		n += r.end - r.off
	}
	return n
}

// bpExtentRanges is a listed file's ranges, all phases, as one sorted set.
func bpExtentRanges(f *bootprefetch.File) []bpRange {
	rs := make([]bpRange, 0, len(f.Extents))
	for _, e := range f.Extents {
		rs = append(rs, bpRange{off: e.Off, end: e.Off + e.Len})
	}
	return bpNormalize(rs, f.Size)
}
