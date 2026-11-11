package main

// files_extract.go — unpacking archives from Files.
//
// A on an archive in Files opens its sheet (files_extract_ui.go), and the
// sheet's two actions end up here:
//
//   - Extract unpacks the archive into a hidden folder beside it and then moves
//     what came out into place in one step. A single top-level item (one
//     folder, one game) keeps its own name; several go into a new folder named
//     after the archive. Nothing that already exists is touched: a taken name
//     gets " (2)", the way Paste does it.
//   - Extract here unpacks the same way and then merges into the archive's own
//     folder: folders are combined and a file of the same name is replaced
//     (the sheet asks twice before that happens). Every move is decided before
//     the first one is made.
//
// ZIP, TAR (plain, .gz, .xz and .bz2) and single .gz, .xz and .bz2 files are
// read in-process. 7z and RAR — and the rare zip compressed with something
// other than Deflate — go to 7-Zip when the device has it (files_extract_tools.go).
//
// What a hostile or broken archive meets: every write goes through an os.Root
// opened on the staging folder; names are cleaned (no "..", no absolute paths,
// nothing a FAT card or Windows cannot store); links, devices and pipes are
// never created; a member must hold exactly the bytes it declares; the card's
// free space is checked before the first byte is written (a tar's member by
// member); and an .xz may not ask for a dictionary bigger than the handheld can
// spare (files_extract_xz.go). A cancel or a failure while it unpacks deletes
// the staging folder, which leaves the folder exactly as it was.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	// feExtractStagePrefix names the folder an archive is unpacked into before
	// it is moved into place. A dot name, so Files does not list it (and
	// Windows' hidden attribute for Explorer); one left behind by a crash is
	// swept the next time something is extracted there.
	feExtractStagePrefix = ".jukahub-extract-"
	// feExtractMarker is the file that makes a staging folder one: the sweep
	// removes only folders that hold it, never a folder of the user's that
	// happens to have the prefix.
	feExtractMarker = ".jukahub-staging"
	// feExtractSpaceMargin is left free on the card after an extraction, so
	// unpacking a game never leaves the system without room to write a save.
	feExtractSpaceMargin = int64(32 << 20)
	// feExtractStaleAge is how old a leftover staging folder must be before
	// it is swept: only one extraction runs at a time, so anything older than
	// this is a crash's.
	feExtractStaleAge = 10 * time.Minute
	// feExtractBufSize is the copy buffer; the SD card wants large writes.
	feExtractBufSize = 256 << 10
	// feZipMaxDirectory bounds a zip's central directory: archive/zip keeps a
	// record of every entry in memory, so a directory this large is refused
	// before it is read.
	feZipMaxDirectory = 64 << 20
	// feXzMaxDict bounds the LZMA2 dictionary an .xz may ask for. The decoder
	// allocates it up front from a number the file states, so a 100-byte file
	// could otherwise ask for 4 GiB on a 1 GB handheld; xz -9 uses 64 MiB.
	feXzMaxDict = int64(128 << 20)
)

// feExtractMaxEntries bounds the items one archive may hold. A variable so the
// tests can lower it instead of building a real bomb.
var feExtractMaxEntries = 200000

// feExtractMaxBytes bounds what one archive may unpack to, whatever a card
// reports as free (a network share can claim petabytes).
var feExtractMaxBytes = int64(1 << 40)

// feExtractFreeSpace is a test seam over filesFreeSpace.
var feExtractFreeSpace = filesFreeSpace

var (
	errFeArchiveEncrypted = errors.New("it is password-protected")
	errFeArchiveNeedsTool = errors.New("it needs 7-Zip")
	errFeArchiveEmpty     = errors.New("there was nothing in it to extract")
	errFeNotArchive       = errors.New("it is not an archive JukaHub can open")
	errFeEntryTooBig      = errors.New("an entry is larger than the space left")
	errFeToolFailed       = errors.New("the program unpacking it stopped")
	errFeCardFull         = errors.New("the card ran out of space while it unpacked")
	errFeHasLinks         = errors.New("it holds symbolic links, which JukaHub does not unpack here")
	errFeTooMany          = errors.New("it holds more items than one archive may")
)

// feNoSpaceError is a card without room for what an archive unpacks to. more
// is set when need is only a lower bound (a stream whose size is not known
// until it has been read).
type feNoSpaceError struct {
	need, free int64
	more       bool
}

func (e feNoSpaceError) Error() string {
	need := humanBytes(e.need)
	if e.more {
		need = "more than " + need
	}
	if e.free < 0 {
		return "it needs " + need + " and the card is full"
	}
	return "it needs " + need + " and " + humanBytes(e.free) + " is free"
}

// --- Kinds -------------------------------------------------------------------

// feArchiveKind is how an archive is read.
type feArchiveKind int

const (
	feArchiveNone feArchiveKind = iota
	feArchiveZip
	feArchiveTar
	feArchiveTarGz
	feArchiveTarXz
	feArchiveTarBz2
	feArchiveGz
	feArchiveXz
	feArchiveBz2
	feArchive7z
	feArchiveRar
)

// feArchiveExts maps the extensions Files treats as archives to their kind,
// the double ones first so ".tar.gz" is not read as a lone ".gz".
var feArchiveExts = []struct {
	ext  string
	kind feArchiveKind
}{
	{".tar.bz2", feArchiveTarBz2},
	{".tar.gz", feArchiveTarGz},
	{".tar.xz", feArchiveTarXz},
	{".tbz2", feArchiveTarBz2},
	{".tbz", feArchiveTarBz2},
	{".tb2", feArchiveTarBz2},
	{".tgz", feArchiveTarGz},
	{".txz", feArchiveTarXz},
	{".tar", feArchiveTar},
	{".zip", feArchiveZip},
	{".bz2", feArchiveBz2},
	{".gz", feArchiveGz},
	{".xz", feArchiveXz},
	{".7z", feArchive7z},
	{".rar", feArchiveRar},
}

// feArchiveExtOf is the archive extension name ends in ("" for none),
// compared without regard to case. It reads no disk: the Files rows ask for
// their icon on every frame.
func feArchiveExtOf(name string) (string, feArchiveKind) {
	name = strings.TrimSpace(name)
	for _, e := range feArchiveExts {
		if len(name) > len(e.ext) && strings.EqualFold(name[len(name)-len(e.ext):], e.ext) {
			return e.ext, e.kind
		}
	}
	return "", feArchiveNone
}

// feArchiveKindOf is the kind a file's name says it is.
func feArchiveKindOf(name string) feArchiveKind {
	_, kind := feArchiveExtOf(name)
	return kind
}

// feArchiveLabel is the format's name as the sheet shows it.
func feArchiveLabel(kind feArchiveKind) string {
	switch kind {
	case feArchiveZip:
		return "ZIP"
	case feArchiveTar:
		return "TAR"
	case feArchiveTarGz:
		return "TAR.GZ"
	case feArchiveTarXz:
		return "TAR.XZ"
	case feArchiveTarBz2:
		return "TAR.BZ2"
	case feArchiveGz:
		return "GZIP"
	case feArchiveXz:
		return "XZ"
	case feArchiveBz2:
		return "BZIP2"
	case feArchive7z:
		return "7Z"
	case feArchiveRar:
		return "RAR"
	}
	return "Archive"
}

// feArchiveSniffBytes names the container a file's first bytes say it is, or
// feArchiveNone when they do not say (a zip may carry a stub in front of its
// first entry, and a plain tar keeps its mark at offset 257).
func feArchiveSniffBytes(head []byte) feArchiveKind {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")),
		bytes.HasPrefix(head, []byte("PK\x07\x08")):
		return feArchiveZip
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return feArchiveGz
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return feArchiveXz
	case bytes.HasPrefix(head, []byte("BZh")):
		return feArchiveBz2
	case bytes.HasPrefix(head, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}):
		return feArchive7z
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return feArchiveRar
	case feLooksLikeTar(head):
		return feArchiveTar
	}
	return feArchiveNone
}

// feLooksLikeTar reports a POSIX or GNU tar header ("ustar" at offset 257).
func feLooksLikeTar(head []byte) bool {
	return len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar"))
}

// feArchiveReconcile is the kind an archive is read as: what its name says,
// unless its first bytes say it is another container (a 7z saved as .zip, a
// download whose name lost its real extension).
func feArchiveReconcile(named, sniffed feArchiveKind) feArchiveKind {
	if sniffed == feArchiveNone || sniffed == named {
		return named
	}
	switch {
	case sniffed == feArchiveGz && named == feArchiveTarGz,
		sniffed == feArchiveXz && named == feArchiveTarXz,
		sniffed == feArchiveBz2 && named == feArchiveTarBz2:
		return named
	}
	return sniffed
}

// feArchiveDetect reads the first bytes of path and reconciles them with its
// name.
func feArchiveDetect(path string) feArchiveKind {
	named := feArchiveKindOf(path)
	f, err := os.Open(path)
	if err != nil {
		return named
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return feArchiveReconcile(named, feArchiveSniffBytes(head[:n]))
}

// feArchiveStem is an archive's name without its archive extension, made safe
// to use as a folder or file name: "Game (USA).zip" → "Game (USA)",
// "backup.tar.gz" → "backup", "disc.iso.gz" → "disc.iso".
func feArchiveStem(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if ext, _ := feArchiveExtOf(base); ext != "" {
		stem = base[:len(base)-len(ext)]
	}
	stem = strings.TrimSpace(stem)
	if stem == "" || stem == "." || stem == ".." {
		return "archive"
	}
	return feExtractTopName(feExtractComponent(stem))
}

// --- Names ---------------------------------------------------------------------

// feExtractComponent makes one path component storable on a FAT or exFAT card
// and on Windows: the characters those refuse become "_", trailing dots and
// spaces go (Windows drops them, and the name would then differ from the one
// written), and a reserved device name gets a "_" in front. Invalid UTF-8
// becomes U+FFFD through the range loop.
func feExtractComponent(part string) string {
	var b strings.Builder
	b.Grow(len(part))
	for _, r := range part {
		switch {
		case r < 0x20, r == 0x7f:
			b.WriteByte('_')
		case strings.ContainsRune(`<>:"|?*\/`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), " .")
	if out == "" {
		return "_"
	}
	if feReservedWindowsName(out) {
		return "_" + out
	}
	return out
}

// feExtractTopName keeps a name from looking like a staging folder, which the
// sweep would take for a crash's leftover.
func feExtractTopName(name string) string {
	if strings.HasPrefix(strings.ToLower(name), feExtractStagePrefix) {
		return "_" + name
	}
	return name
}

// feReservedWindowsName reports a name Windows maps onto a device (CON,
// NUL.txt, COM1, LPT³…), which a file on a card shared with a PC must not be
// called.
func feReservedWindowsName(name string) bool {
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(stem) > 3 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		switch stem[3:] {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}

// Why an entry was left out (feExtractRel's second answer).
const (
	feSkipUnsafe = "unsafe"
	feSkipJunk   = "junk"
)

// feExtractMaxDepth and feExtractMaxPath bound one entry's path: deeper or
// longer than any real archive goes, and a hostile one could otherwise make the
// bookkeeping per entry grow without end.
const (
	feExtractMaxDepth = 64
	feExtractMaxPath  = 4096
)

// feExtractRel is the native relative path an entry is written at inside the
// staging folder. An empty path with no reason is the archive's own top (a
// "./" entry), which has nothing to write. Leading slashes and a drive prefix
// are dropped, as 7-Zip and GNU tar do; a ".." anywhere, or a path deeper or
// longer than the bounds, makes the entry unsafe; macOS metadata (__MACOSX,
// .DS_Store) is junk. No component may look like a staging folder, which the
// sweep of a later extraction in that folder would take for a leftover.
func feExtractRel(name string) (string, string) {
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.ContainsRune(n, 0) || len(n) > feExtractMaxPath {
		return "", feSkipUnsafe
	}
	// "C:/x" and "C:" are a drive; "a:b.txt" is a name with a colon in it,
	// which the component rule below turns into "a_b.txt".
	if len(n) >= 2 && n[1] == ':' && (len(n) == 2 || n[2] == '/') &&
		((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= 'A' && n[0] <= 'Z')) {
		n = n[2:]
	}
	var parts []string
	for _, part := range strings.Split(n, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", feSkipUnsafe
		}
		parts = append(parts, feExtractTopName(feExtractComponent(part)))
		if len(parts) > feExtractMaxDepth {
			return "", feSkipUnsafe
		}
	}
	if len(parts) == 0 {
		return "", ""
	}
	if parts[0] == "__MACOSX" || parts[len(parts)-1] == ".DS_Store" {
		return "", feSkipJunk
	}
	rel := filepath.Join(parts...)
	if !filepath.IsLocal(rel) {
		return "", feSkipUnsafe
	}
	return rel, ""
}

// feCP437High is code page 437 from 0x80 up: what a zip's names are written in
// when the archive does not mark them as UTF-8 and they are not valid UTF-8
// (DOS and older Windows zippers).
var feCP437High = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00a0")

// feDecodeCP437 reads s as code page 437.
func feDecodeCP437(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 || len(feCP437High) != 128 {
			b.WriteByte(c)
			continue
		}
		b.WriteRune(feCP437High[c-0x80])
	}
	return b.String()
}

// feZipEntryName is a zip entry's name as text: code page 437 when the archive
// did not say UTF-8 and the bytes are not UTF-8 either.
func feZipEntryName(f *zip.File) string {
	if f.NonUTF8 && !utf8.ValidString(f.Name) {
		return feDecodeCP437(f.Name)
	}
	return f.Name
}

// feZipModTime is when an entry was last written. A zip with only the MS-DOS
// date (archive/zip then answers in UTC) recorded the local wall clock of the
// machine that made it, so that clock is read as local time here.
func feZipModTime(f *zip.File) time.Time {
	m := f.Modified
	if m.IsZero() || m.Location() != time.UTC {
		return m
	}
	return time.Date(m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute(), m.Second(), 0, time.Local)
}

// feZipMethodName names a zip compression method the standard library cannot
// read, for the sheet and the error.
func feZipMethodName(method uint16) string {
	switch method {
	case 9:
		return "Deflate64"
	case 12:
		return "BZIP2"
	case 14:
		return "LZMA"
	case 93:
		return "Zstandard"
	case 95:
		return "XZ"
	case 98:
		return "PPMd"
	case 99:
		return "AES"
	}
	return fmt.Sprintf("method %d", method)
}

// --- What an extraction wrote -----------------------------------------------

// feExtractSkips counts what an archive held that was not written.
type feExtractSkips struct {
	Unsafe    int // a name that climbs out, is absolute or cannot be stored
	Links     int // symbolic links (a hard link is copied when its file came first)
	Special   int // devices, pipes and sockets
	Junk      int // macOS metadata: __MACOSX folders and .DS_Store files
	Duplicate int // a second entry for a name already written
	Failed    int // entries the card refused (a name it cannot hold)
}

func (s *feExtractSkips) add(reason string) {
	switch reason {
	case feSkipUnsafe:
		s.Unsafe++
	case feSkipJunk:
		s.Junk++
	}
}

// feExtractStats is what one extraction wrote.
type feExtractStats struct {
	Files    int
	Dirs     int
	Bytes    int64
	Skips    feExtractSkips
	Replaced int    // Extract here: files of the same name that were replaced
	Kept     int    // Extract here: names taken by something else (a folder for a file)
	NotMoved int    // Extract here: items that could not be moved in (open, read-only)
	Warning  string // a complaint 7-Zip made without failing
}

// --- Progress --------------------------------------------------------------

// feExtractMeter is one extraction's progress: written by the goroutine that
// unpacks, read by the renderer every frame. A nil meter takes every call and
// records nothing, which is what the tests pass.
type feExtractMeter struct {
	mu      sync.Mutex
	done    int64  // progress so far, in the unit total is in
	total   int64  // 0 while unknown
	written int64  // bytes written to the card
	output  bool   // total counts the bytes that will be written (a zip)
	percent bool   // done and total are 7-Zip's percent
	entry   string // the item being written
	phase   string // "Preparing", "Getting 7-Zip", "Unpacking", "Finishing"
}

// feExtractReading is a copy of a meter for the renderer.
type feExtractReading struct {
	Done, Total, Written int64
	Output, Percent      bool
	Entry, Phase         string
}

func (m *feExtractMeter) setTotal(total int64, output, percent bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.total, m.output, m.percent = total, output, percent
	m.mu.Unlock()
}

// setDone records how far the reading side has come; its signature fits
// progressReader's callback.
func (m *feExtractMeter) setDone(done, _ int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.done = done
	m.mu.Unlock()
}

func (m *feExtractMeter) addWritten(n int64, alsoDone bool) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.written += n
	if alsoDone {
		m.done += n
	}
	m.mu.Unlock()
}

func (m *feExtractMeter) setEntry(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.entry = name
	m.mu.Unlock()
}

func (m *feExtractMeter) setPhase(phase string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.phase = phase
	m.mu.Unlock()
}

func (m *feExtractMeter) reading() feExtractReading {
	if m == nil {
		return feExtractReading{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return feExtractReading{
		Done:    m.done,
		Total:   m.total,
		Written: m.written,
		Output:  m.output,
		Percent: m.percent,
		Entry:   m.entry,
		Phase:   m.phase,
	}
}

// feMeterWriter counts what a copy writes.
type feMeterWriter struct {
	w     io.Writer
	meter *feExtractMeter
	done  bool // the written bytes are the progress too (a zip)
}

func (w *feMeterWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.meter.addWritten(int64(n), w.done)
	return n, err
}

// --- Writing -------------------------------------------------------------------

// feExtractBudget is how many bytes may be written into dir — its free space
// less the margin — and the free space itself (-1 when it cannot be read, in
// which case only feExtractMaxBytes bounds the write and a full card stops it).
func feExtractBudget(dir string) (int64, int64) {
	free, err := feExtractFreeSpace(dir)
	if err != nil || free < 0 {
		return feExtractMaxBytes, -1
	}
	budget := free - feExtractSpaceMargin
	if budget < 0 {
		budget = 0
	}
	if budget > feExtractMaxBytes {
		budget = feExtractMaxBytes
	}
	return budget, free
}

// feExtractPerm is the mode a file is created with: executable when the
// archive says so (a script, a port's binary), otherwise plain.
func feExtractPerm(mode fs.FileMode) fs.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// feExtractStamp gives a written file the date the archive records, so
// "Sort: Newest" orders what came out of an archive by when it was made.
func feExtractStamp(root *os.Root, rel string, mod time.Time) {
	if mod.IsZero() || mod.Year() < 1980 {
		return
	}
	_ = root.Chtimes(rel, mod, mod)
}

// feExtractParent creates the folders above rel.
func feExtractParent(root *os.Root, rel string) error {
	if parent := filepath.Dir(rel); parent != "." && parent != "" {
		return root.MkdirAll(parent, 0o755)
	}
	return nil
}

// feExtractMkdir creates a folder entry. A name already taken by a file (an
// archive that lists "a" as a file and then as a folder) is a duplicate.
func feExtractMkdir(root *os.Root, rel string, st *feExtractStats) error {
	err := root.MkdirAll(rel, 0o755)
	switch {
	case err == nil:
		st.Dirs++
		return nil
	case errors.Is(err, fs.ErrExist) || feExtractSkippable(err):
		st.Skips.Duplicate++
		return nil
	}
	return err
}

// feExtractSkippable reports an error about one entry's name rather than the
// card — a name the filesystem refuses, a path through a file — so the entry
// is skipped and the rest still unpack. A full or read-only card is not one.
func feExtractSkippable(err error) bool {
	// os.Root answers with these on Windows too, where they are Go's own
	// stand-ins rather than system error numbers.
	if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.EINVAL) {
		return true
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	if runtime.GOOS == "windows" {
		switch uintptr(errno) {
		case 3, 123, 206, 267: // ERROR_PATH_NOT_FOUND, _INVALID_NAME, _FILENAME_EXCED_RANGE, _DIRECTORY
			return true
		}
		return false
	}
	switch uintptr(errno) {
	case 20, 22, 36, 84: // ENOTDIR, EINVAL, ENAMETOOLONG, EILSEQ (Linux)
		return true
	}
	return false
}

// feFileTooLarge reports a write a FAT32 card refused because one file passed
// 4 GiB (EFBIG on Linux, ERROR_FILE_TOO_LARGE on Windows).
func feFileTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if runtime.GOOS == "windows" {
			return uintptr(errno) == 223
		}
		return uintptr(errno) == 27
	}
	return strings.Contains(strings.ToLower(err.Error()), "file too large")
}

// feExtractWrite writes one file of the archive at rel inside root. size is
// what the archive declares (-1 when the format does not say up front) and
// limit what may be written at most; a member that ends early or runs past its
// size is a damaged archive, one that passes limit is errFeEntryTooBig, and a
// half-written file is removed. O_EXCL makes a second entry for one name an
// error the caller decides about.
func feExtractWrite(ctx context.Context, root *os.Root, rel string, src io.Reader, size, limit int64, perm fs.FileMode, meter *feExtractMeter, buf []byte, outputProgress bool) (int64, error) {
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return 0, err
	}
	if limit < 0 || limit > feExtractMaxBytes {
		limit = feExtractMaxBytes
	}
	w := &feMeterWriter{w: out, meter: meter, done: outputProgress}
	n, copyErr := io.CopyBuffer(w, &archiveReader{ctx: ctx, r: io.LimitReader(src, limit+1)}, buf)
	closeErr := out.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		// A declared size is checked first: a zip member that runs past what
		// its header says is damaged, not too big for the card.
		switch {
		case size >= 0 && n != size:
			copyErr = fmt.Errorf("%w (%s holds %d bytes, not the %d it declares)", errBadToolArchive, filepath.Base(rel), n, size)
		case n > limit:
			copyErr = errFeEntryTooBig
		}
	}
	if copyErr != nil {
		_ = root.Remove(rel)
		return n, copyErr
	}
	return n, nil
}

// --- ZIP -------------------------------------------------------------------------

// feZipPrecheck refuses a zip with more entries than one may hold, or a central
// directory too big to read, before archive/zip builds a record of every entry
// in memory. archive/zip does not trust the counts the end record declares —
// it reads headers from the directory's start until one is not a header — so
// neither does this: it walks the headers the same way, from the same start
// (with or without the bytes a self-extractor puts in front), counting without
// keeping anything. A file it cannot make sense of is left for archive/zip.
func feZipPrecheck(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	const eocdLen = 22
	size := info.Size()
	if size < eocdLen {
		return nil
	}
	tail := int64(66560)
	if tail > size {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, size-tail); err != nil {
		return nil
	}
	at := -1
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if buf[i] == 'P' && buf[i+1] == 'K' && buf[i+2] == 5 && buf[i+3] == 6 &&
			i+eocdLen+int(binary.LittleEndian.Uint16(buf[i+20:i+22])) <= len(buf) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	dirEnd := size - tail + int64(at)
	records := uint64(binary.LittleEndian.Uint16(buf[at+10 : at+12]))
	dirSize := uint64(binary.LittleEndian.Uint32(buf[at+12 : at+16]))
	dirOffset := uint64(binary.LittleEndian.Uint32(buf[at+16 : at+20]))
	// archive/zip turns to the zip64 record on these values, when the locator
	// before the end record names disk 0 of one disk; the record's offset then
	// takes the end record's place in the arithmetic below.
	if (records == 0xffff || dirSize == 0xffff || dirOffset == 0xffffffff) && dirEnd >= 20 {
		var loc [20]byte
		if _, err := f.ReadAt(loc[:], dirEnd-20); err == nil && bytes.Equal(loc[:4], []byte("PK\x06\x07")) &&
			binary.LittleEndian.Uint32(loc[4:8]) == 0 && binary.LittleEndian.Uint32(loc[16:20]) == 1 {
			off := binary.LittleEndian.Uint64(loc[8:16])
			var rec [56]byte
			if off < uint64(size) && uint64(size)-off >= 56 {
				if _, err := f.ReadAt(rec[:], int64(off)); err == nil && bytes.Equal(rec[:4], []byte("PK\x06\x06")) {
					records = binary.LittleEndian.Uint64(rec[32:40])
					dirSize = binary.LittleEndian.Uint64(rec[40:48])
					dirOffset = binary.LittleEndian.Uint64(rec[48:56])
					dirEnd = int64(off)
				}
			}
		}
	}
	if records > uint64(feExtractMaxEntries) {
		return fmt.Errorf("%w (%d; the limit is %d)", errFeTooMany, records, feExtractMaxEntries)
	}
	if dirSize > feZipMaxDirectory {
		return fmt.Errorf("its list of contents is larger than the %s JukaHub reads", humanBytes(feZipMaxDirectory))
	}
	// archive/zip starts reading at the end record less the directory's size
	// (which allows for bytes in front of the archive), or at the declared
	// offset when a header is found there: both are walked.
	var starts []int64
	if dirOffset < uint64(size) {
		starts = append(starts, int64(dirOffset))
	}
	if start := dirEnd - int64(dirSize); start >= 0 && start != int64(dirOffset) {
		starts = append(starts, start)
	}
	for _, start := range starts {
		count, walked := feZipCountHeaders(f, start, size)
		if count > feExtractMaxEntries {
			return fmt.Errorf("%w (more than %d)", errFeTooMany, feExtractMaxEntries)
		}
		if walked > feZipMaxDirectory {
			return fmt.Errorf("its list of contents is larger than the %s JukaHub reads", humanBytes(feZipMaxDirectory))
		}
	}
	return nil
}

// feZipCountHeaders counts the central-directory headers from start, as far as
// they go, and the bytes they take; it stops just past either bound.
func feZipCountHeaders(f *os.File, start, size int64) (int, int64) {
	if start < 0 || start >= size {
		return 0, 0
	}
	r := bufio.NewReaderSize(io.NewSectionReader(f, start, size-start), 64<<10)
	var hdr [46]byte
	count := 0
	var walked int64
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil || !bytes.Equal(hdr[:4], []byte("PK\x01\x02")) {
			return count, walked
		}
		count++
		skip := int(binary.LittleEndian.Uint16(hdr[28:30])) + int(binary.LittleEndian.Uint16(hdr[30:32])) +
			int(binary.LittleEndian.Uint16(hdr[32:34]))
		walked += int64(46 + skip)
		if count > feExtractMaxEntries || walked > feZipMaxDirectory {
			return count, walked
		}
		if _, err := r.Discard(skip); err != nil {
			return count, walked
		}
	}
}

// feOpenZip opens a zip after feZipPrecheck. A reader that comes back with
// ErrInsecurePath (a GODEBUG setting can turn that on) is still used:
// feExtractRel applies its own, stricter rule to every name.
func feOpenZip(path string) (*zip.ReadCloser, error) {
	if err := feZipPrecheck(path); err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		if errors.Is(err, zip.ErrInsecurePath) && zr != nil {
			return zr, nil
		}
		if zr != nil {
			_ = zr.Close()
		}
		return nil, fmt.Errorf("%w (%v)", errBadToolArchive, err)
	}
	return zr, nil
}

// feZipPlan checks a zip's directory before anything is written: an
// encrypted entry or too many entries stop it, and it returns the bytes the
// entries that will be written declare. A compression method only 7-Zip reads
// is reported as errFeArchiveNeedsTool after the total, so the space can still
// be checked before the archive is handed over.
func feZipPlan(zr *zip.ReadCloser) (int64, error) {
	if len(zr.File) > feExtractMaxEntries {
		return 0, fmt.Errorf("%w (%d; the limit is %d)", errFeTooMany, len(zr.File), feExtractMaxEntries)
	}
	var total int64
	method := ""
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			return 0, errFeArchiveEncrypted
		}
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
			continue
		}
		// What will be skipped neither counts nor needs a method.
		if rel, why := feExtractRel(feZipEntryName(f)); why != "" || rel == "" {
			continue
		}
		if method == "" && f.Method != zip.Store && f.Method != zip.Deflate {
			method = feZipMethodName(f.Method)
		}
		sum, ok := addDeclaredBytes(total, declaredEntryBytes(f.UncompressedSize64), feExtractMaxBytes)
		if !ok {
			return 0, fmt.Errorf("it unpacks to more than %s", humanBytes(feExtractMaxBytes))
		}
		total = sum
	}
	if method != "" {
		return total, fmt.Errorf("%w (for its %s compression)", errFeArchiveNeedsTool, method)
	}
	return total, nil
}

// feExtractZip unpacks a zip into out.
func feExtractZip(ctx context.Context, archive, out string, st *feExtractStats, meter *feExtractMeter) error {
	zr, err := feOpenZip(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	total, planErr := feZipPlan(zr)
	if planErr != nil && !errors.Is(planErr, errFeArchiveNeedsTool) {
		return planErr
	}
	budget, free := feExtractBudget(out)
	if total > budget {
		return feNoSpaceError{need: total + feExtractSpaceMargin, free: free}
	}
	if planErr != nil {
		return planErr
	}
	meter.setTotal(total, true, false)
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	buf := make([]byte, feExtractBufSize)
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, reason := feExtractRel(feZipEntryName(f))
		if reason != "" {
			st.Skips.add(reason)
			continue
		}
		if rel == "" {
			continue
		}
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			st.Skips.Links++
			continue
		case f.FileInfo().IsDir():
			if err := feExtractMkdir(root, rel, st); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			st.Skips.Special++
			continue
		}
		if err := feExtractParent(root, rel); err != nil {
			if errors.Is(err, fs.ErrExist) || feExtractSkippable(err) {
				st.Skips.Failed++
				continue
			}
			return err
		}
		meter.setEntry(rel)
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w (%s: %v)", errBadToolArchive, f.Name, err)
		}
		size := declaredEntryBytes(f.UncompressedSize64)
		n, err := feExtractWrite(ctx, root, rel, rc, size, size, feExtractPerm(mode), meter, buf, true)
		_ = rc.Close()
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrExist):
				st.Skips.Duplicate++
				continue
			case feExtractSkippable(err):
				st.Skips.Failed++
				continue
			}
			return err
		}
		st.Files++
		st.Bytes += n
		feExtractStamp(root, rel, feZipModTime(f))
	}
	return nil
}

// --- TAR and single compressed files --------------------------------------------

// feArchiveDecompress wraps src in the decompressor kind needs. The returned
// func closes it.
func feArchiveDecompress(ctx context.Context, src io.Reader, kind feArchiveKind) (io.Reader, func(), error) {
	switch kind {
	case feArchiveTarGz, feArchiveGz:
		gr, err := gzip.NewReader(src)
		if err != nil {
			return nil, nil, fmt.Errorf("%w (gzip: %v)", errBadToolArchive, err)
		}
		if kind == feArchiveTarGz {
			// One member: some tools pad a .tar.gz with zeros after it, which
			// a multistream reader takes for a damaged second member.
			gr.Multistream(false)
		}
		return gr, func() { _ = gr.Close() }, nil
	case feArchiveTarXz, feArchiveXz:
		// The container is read here, so no block's dictionary is allocated
		// before it was checked (files_extract_xz.go).
		return newFeXzReader(ctx, src, feXzMaxDict), func() {}, nil
	case feArchiveTarBz2, feArchiveBz2:
		return bzip2.NewReader(src), func() {}, nil
	}
	return src, func() {}, nil
}

// feExtractTar unpacks a tar (plain or compressed) or a single compressed file
// into out. A lone .gz, .xz or .bz2 is looked into first: one that holds a tar
// is unpacked as one, anything else is written as the one file it is, named
// after the archive without its extension.
func feExtractTar(ctx context.Context, archive, out string, kind feArchiveKind, st *feExtractStats, meter *feExtractMeter) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var size int64
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	// Progress is the share of the archive read, the one figure known before
	// the end; the buffer matters because the xz decoder reads a byte at a
	// time.
	meter.setTotal(size, false, false)
	src := bufio.NewReaderSize(&progressReader{r: f, total: size, fn: meter.setDone}, 1<<20)
	stream, closeStream, err := feArchiveDecompress(ctx, src, kind)
	if err != nil {
		return err
	}
	defer closeStream()
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	budget, free := feExtractBudget(out)
	switch kind {
	case feArchiveGz, feArchiveXz, feArchiveBz2:
		peek := bufio.NewReaderSize(stream, 64<<10)
		head, _ := peek.Peek(512)
		if feLooksLikeTar(head) {
			return feExtractTarStream(ctx, peek, root, budget, free, st, meter)
		}
		return feExtractSingle(ctx, peek, root, feArchiveStem(archive), budget, free, st, meter)
	}
	return feExtractTarStream(ctx, stream, root, budget, free, st, meter)
}

// feExtractSingle writes a decompressed stream as one file.
func feExtractSingle(ctx context.Context, src io.Reader, root *os.Root, name string, budget, free int64, st *feExtractStats, meter *feExtractMeter) error {
	meter.setEntry(name)
	n, err := feExtractWrite(ctx, root, name, src, -1, budget, 0o644, meter, make([]byte, feExtractBufSize), false)
	if errors.Is(err, errFeEntryTooBig) {
		return feNoSpaceError{need: n + feExtractSpaceMargin, free: free, more: true}
	}
	if err != nil {
		return err
	}
	st.Files++
	st.Bytes += n
	return nil
}

// feExtractTarStream unpacks a tar stream into root. Folders and regular files
// are written, and a later member of a name replaces an earlier one, as tar
// does with an archive that was appended to; a hard link becomes a copy of its
// file when that file came first (a card may not do links at all); symbolic
// links, devices and pipes are counted and left out. The stream is read to its
// very end afterwards, because a gzip or xz checksum is only checked there.
func feExtractTarStream(ctx context.Context, stream io.Reader, root *os.Root, budget, free int64, st *feExtractStats, meter *feExtractMeter) error {
	tr := tar.NewReader(&archiveReader{ctx: ctx, r: stream})
	buf := make([]byte, feExtractBufSize)
	written := map[string]int64{}
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, errBadToolArchive) {
				return err
			}
			return fmt.Errorf("%w (tar: %v)", errBadToolArchive, err)
		}
		entries++
		if entries > feExtractMaxEntries {
			return fmt.Errorf("%w (the limit is %d)", errFeTooMany, feExtractMaxEntries)
		}
		rel, reason := feExtractRel(hdr.Name)
		if reason != "" {
			st.Skips.add(reason)
			continue
		}
		if rel == "" {
			continue
		}
		typ := hdr.Typeflag
		if typ == tar.TypeReg && hdr.Size == 0 && strings.HasSuffix(hdr.Name, "/") {
			// An old tar's folder: an empty "file" whose name ends in a slash.
			typ = tar.TypeDir
		}
		switch typ {
		case tar.TypeDir:
			if err := feExtractMkdir(root, rel, st); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeGNUSparse, tar.TypeCont:
			if hdr.Size < 0 {
				return fmt.Errorf("%w (%s declares a negative size)", errBadToolArchive, hdr.Name)
			}
			if old, again := written[rel]; again {
				// The later copy wins; the earlier one stops counting.
				if err := root.Remove(rel); err == nil {
					st.Files--
					st.Bytes -= old
					delete(written, rel)
				}
			}
			if hdr.Size > budget-st.Bytes {
				return feNoSpaceError{need: st.Bytes + hdr.Size + feExtractSpaceMargin, free: free}
			}
			n, err := feExtractTarFile(ctx, root, rel, tr, hdr.Size, feExtractPerm(fs.FileMode(hdr.Mode)), meter, buf, st)
			if err != nil {
				return err
			}
			if n >= 0 {
				written[rel] = n
				feExtractStamp(root, rel, hdr.ModTime)
			}
		case tar.TypeLink:
			target, why := feExtractRel(hdr.Linkname)
			if _, ok := written[target]; why != "" || target == "" || !ok {
				st.Skips.Links++
				continue
			}
			n, err := feExtractCopyWithin(ctx, root, target, rel, budget-st.Bytes, meter, buf, st)
			if errors.Is(err, errFeEntryTooBig) {
				return feNoSpaceError{need: st.Bytes + n + feExtractSpaceMargin, free: free}
			}
			if err != nil {
				return err
			}
			if n >= 0 {
				written[rel] = n
			}
		case tar.TypeSymlink:
			st.Skips.Links++
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			st.Skips.Special++
		}
	}
	if _, err := io.CopyBuffer(io.Discard, &archiveReader{ctx: ctx, r: stream}, buf); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

// feExtractTarFile writes one regular tar member and counts it. It answers -1
// (and no error) for an entry that was skipped as a duplicate or a name the
// card refused.
func feExtractTarFile(ctx context.Context, root *os.Root, rel string, src io.Reader, size int64, perm fs.FileMode, meter *feExtractMeter, buf []byte, st *feExtractStats) (int64, error) {
	if err := feExtractParent(root, rel); err != nil {
		if errors.Is(err, fs.ErrExist) || feExtractSkippable(err) {
			st.Skips.Failed++
			return -1, nil
		}
		return 0, err
	}
	meter.setEntry(rel)
	n, err := feExtractWrite(ctx, root, rel, src, size, size, perm, meter, buf, false)
	switch {
	case err == nil:
		st.Files++
		st.Bytes += n
		return n, nil
	case errors.Is(err, fs.ErrExist):
		st.Skips.Duplicate++
		return -1, nil
	case feExtractSkippable(err):
		st.Skips.Failed++
		return -1, nil
	}
	return n, err
}

// feExtractCopyWithin writes a second copy of a file already unpacked: what a
// tar hard link stands for.
func feExtractCopyWithin(ctx context.Context, root *os.Root, from, to string, limit int64, meter *feExtractMeter, buf []byte, st *feExtractStats) (int64, error) {
	in, err := root.Open(from)
	if err != nil {
		st.Skips.Links++
		return -1, nil
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		st.Skips.Links++
		return -1, nil
	}
	if info.Size() > limit {
		return info.Size(), errFeEntryTooBig
	}
	return feExtractTarFile(ctx, root, to, in, info.Size(), info.Mode().Perm()|0o600, meter, buf, st)
}

// --- XZ dictionary -------------------------------------------------------------

// feXzDictTooBig walks an .xz file's streams from the end — footer, index, then
// each block's header — and reports the first LZMA2 dictionary larger than
// limit. It reads only headers, so the sheet can say so before anything is
// unpacked; it trusts the index, and a file it cannot follow is not called too
// big. The extraction itself does not rely on it: feXzReader checks every block
// header before it starts that block's decoder (files_extract_xz.go).
func feXzDictTooBig(r io.ReaderAt, size, limit int64) (int64, bool) {
	pos := size
	for streams := 0; pos > 0 && streams < 1024; streams++ {
		// Stream padding: zeros in fours, followed no further than 64 KiB.
		for steps := 0; pos >= 4; steps++ {
			if steps > 16<<10 {
				return 0, false
			}
			var pad [4]byte
			if _, err := r.ReadAt(pad[:], pos-4); err != nil || pad != [4]byte{} {
				break
			}
			pos -= 4
		}
		if pos < 24 {
			return 0, false
		}
		var footer [12]byte
		if _, err := r.ReadAt(footer[:], pos-12); err != nil || footer[10] != 'Y' || footer[11] != 'Z' {
			return 0, false
		}
		backward := (int64(binary.LittleEndian.Uint32(footer[4:8])) + 1) * 4
		indexAt := pos - 12 - backward
		if backward > 16<<20 || indexAt < 12 {
			return 0, false
		}
		index := make([]byte, backward)
		if _, err := r.ReadAt(index, indexAt); err != nil || index[0] != 0 {
			return 0, false
		}
		p := 1
		count, k := feXzVarint(index[p:])
		if k == 0 || count > 1<<20 {
			return 0, false
		}
		p += k
		unpadded := make([]int64, 0, count)
		var blocks int64
		for i := int64(0); i < count; i++ {
			u, k := feXzVarint(index[p:])
			if k == 0 || u <= 0 || u > size {
				return 0, false
			}
			p += k
			if _, k = feXzVarint(index[p:]); k == 0 {
				return 0, false
			}
			p += k
			unpadded = append(unpadded, u)
			blocks += (u + 3) &^ 3
		}
		streamAt := indexAt - blocks - 12
		if streamAt < 0 {
			return 0, false
		}
		var header [6]byte
		if _, err := r.ReadAt(header[:], streamAt); err != nil || !bytes.Equal(header[:], []byte{0xfd, '7', 'z', 'X', 'Z', 0}) {
			return 0, false
		}
		at := streamAt + 12
		for _, u := range unpadded {
			if dict, big := feXzBlockDictTooBig(r, at, limit); big {
				return dict, true
			}
			at += (u + 3) &^ 3
		}
		pos = streamAt
	}
	return 0, false
}

// feXzBlockDictTooBig reads one block header at off and reports an LZMA2
// dictionary larger than limit.
func feXzBlockDictTooBig(r io.ReaderAt, off, limit int64) (int64, bool) {
	var first [1]byte
	if _, err := r.ReadAt(first[:], off); err != nil || first[0] == 0 {
		return 0, false
	}
	hdr := make([]byte, (int(first[0])+1)*4)
	if _, err := r.ReadAt(hdr, off); err != nil {
		return 0, false
	}
	flags := hdr[1]
	p := 2
	for _, present := range []bool{flags&0x40 != 0, flags&0x80 != 0} {
		if !present {
			continue
		}
		_, k := feXzVarint(hdr[p:])
		if k == 0 {
			return 0, false
		}
		p += k
	}
	for i := 0; i <= int(flags&0x03); i++ {
		id, k := feXzVarint(hdr[p:])
		if k == 0 {
			return 0, false
		}
		p += k
		propLen, k := feXzVarint(hdr[p:])
		if k == 0 || propLen < 0 || int64(len(hdr)-p-k) < propLen {
			return 0, false
		}
		p += k
		props := hdr[p : p+int(propLen)]
		p += int(propLen)
		if id != 0x21 || len(props) != 1 {
			continue
		}
		d := props[0] & 0x3f
		if d > 40 {
			return 0, false
		}
		dict := int64(0xffffffff)
		if d < 40 {
			dict = int64(2|(d&1)) << (d/2 + 11)
		}
		if dict > limit {
			return dict, true
		}
	}
	return 0, false
}

// feXzMemoryErr is an .xz whose LZMA2 dictionary needs more memory than
// feXzMaxDict. It counts as a damaged archive to the readers above it, which
// pass such errors on untouched, and keeps its own sentence for the toast.
type feXzMemoryErr struct {
	dict int64
}

func (e feXzMemoryErr) Error() string {
	return fmt.Sprintf("its xz compression needs %s of memory, more than the %s JukaHub allows", humanBytes(e.dict), humanBytes(feXzMaxDict))
}

func (e feXzMemoryErr) Is(target error) bool { return target == errBadToolArchive }

// feXzMemoryError is the error for an .xz whose dictionary is dict bytes.
func feXzMemoryError(dict int64) error {
	return feXzMemoryErr{dict: dict}
}

// feXzVarint reads an xz multibyte integer; the second answer is the bytes it
// took, 0 when b does not hold one.
func feXzVarint(b []byte) (int64, int) {
	var v int64
	for i := 0; i < len(b) && i < 9; i++ {
		v |= int64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			if i > 0 && b[i] == 0 {
				// Not the shortest encoding, which the format requires.
				return 0, 0
			}
			return v, i + 1
		}
	}
	return 0, 0
}

// --- Into place ---------------------------------------------------------------

// feExtractMode is what an extraction does with what came out.
type feExtractMode int

const (
	// feExtractSmart puts a lone top-level item beside the archive and
	// several into a new folder; it never replaces anything.
	feExtractSmart feExtractMode = iota
	// feExtractMerge merges everything into the archive's folder, replacing
	// files of the same name.
	feExtractMerge
)

// feExtractStage makes one job's staging folder in dest: a hidden folder that
// holds the marker the sweep looks for and "out", the folder the archive is
// unpacked into.
func feExtractStage(dest string) (string, string, error) {
	stage, err := os.MkdirTemp(dest, feExtractStagePrefix+"*")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(stage, feExtractMarker), nil, 0o644); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", err
	}
	out := filepath.Join(stage, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", err
	}
	filesSetHidden(stage)
	return stage, out, nil
}

// feExtractFreeName is dir/name, or "name (2)", "name (3)"… when that is
// taken. A file keeps its extension at the end ("game (2).gba"); a folder has
// none ("Game v1.2 (2)").
func feExtractFreeName(dir, name string, isDir bool) (string, error) {
	candidate, err := safeJoinFile(dir, name)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
		return candidate, nil
	}
	base, ext := name, ""
	if !isDir {
		ext = filepath.Ext(name)
		base = strings.TrimSuffix(name, ext)
		if base == "" {
			base, ext = name, ""
		}
	}
	for i := 2; i <= 999; i++ {
		next, err := safeJoinFile(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(next); errors.Is(err, fs.ErrNotExist) {
			return next, nil
		}
	}
	return "", fmt.Errorf("%q and 998 numbered copies of it already exist here", name)
}

// feExtractFinish moves what was unpacked (out) into dest and returns where it
// landed: the one item or the new folder (Extract), or dest (Extract here).
// archive is the job's own archive, which Extract here never replaces.
func feExtractFinish(out, dest, stem, archive string, mode feExtractMode, st *feExtractStats) (string, error) {
	items, err := os.ReadDir(out)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", errFeArchiveEmpty
	}
	if mode == feExtractMerge {
		var steps []feMergeStep
		if err := feExtractMergePlan(out, dest, archive, st, 0, &steps); err != nil {
			return "", err
		}
		for _, step := range steps {
			var err error
			if step.replace {
				err = pkgRename(step.from, step.to)
			} else {
				// Planned as a new name: something that took the name since
				// is left alone.
				err = feRenameNoReplace(step.from, step.to)
			}
			switch {
			case err == nil:
				if step.replace {
					st.Replaced++
				}
			case errors.Is(err, fs.ErrExist):
				st.Kept++
			default:
				logWarn("files", "extract here: could not move %s: %v", step.to, err)
				st.NotMoved++
			}
		}
		return dest, nil
	}
	from, name, isDir := out, stem, true
	if len(items) == 1 {
		from, name, isDir = filepath.Join(out, items[0].Name()), items[0].Name(), items[0].IsDir()
	}
	for tries := 0; tries < 8; tries++ {
		target, err := feExtractFreeName(dest, name, isDir)
		if err != nil {
			return "", err
		}
		err = feRenameNoReplace(from, target)
		if err == nil {
			return target, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		// Something took the name after it was found free: the next one.
	}
	return "", fmt.Errorf("no free name for %q could be kept", name)
}

// feRenameByReserving moves from to to by first taking the name with an empty
// placeholder of the same kind (a folder, or a file made with O_EXCL) and then
// renaming over it, so a name another program takes in between makes the
// placeholder fail instead of being replaced. It is the way without
// renameat2's RENAME_NOREPLACE.
func feRenameByReserving(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if info.IsDir() {
		err = os.Mkdir(to, 0o700)
	} else {
		var f *os.File
		if f, err = os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			err = f.Close()
		}
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if err := os.Rename(from, to); err != nil {
		// The placeholder is the empty one made above.
		_ = os.Remove(to)
		return err
	}
	return nil
}

// feMergeStep is one move Extract here makes.
type feMergeStep struct {
	from, to string
	replace  bool
}

// feExtractMergePlan decides every move of Extract here before any is made, so
// a folder it cannot read or nest stops the job while nothing has changed: a
// new name moves in, a folder joins the folder of the same name, and a file
// replaces the regular file of the same name. A name the folder does not list
// under that name (an 8.3 alias on a FAT card), a name taken by another kind of
// thing (a folder for a file, a link), and the archive itself are left alone
// and counted.
func feExtractMergePlan(src, dst, archive string, st *feExtractStats, depth int, steps *[]feMergeStep) error {
	if depth > 64 {
		return errors.New("its folders nest too deeply")
	}
	items, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	if have, err := os.ReadDir(dst); err == nil {
		for _, e := range have {
			listed[strings.ToLower(e.Name())] = true
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, item := range items {
		from := filepath.Join(src, item.Name())
		to := filepath.Join(dst, item.Name())
		info, err := os.Lstat(to)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			*steps = append(*steps, feMergeStep{from: from, to: to})
			continue
		case err != nil:
			return err
		}
		switch {
		case !listed[strings.ToLower(item.Name())]:
			st.Kept++
		case depth == 0 && strings.EqualFold(item.Name(), filepath.Base(archive)):
			st.Kept++
		case item.IsDir() && info.IsDir():
			if err := feExtractMergePlan(from, to, archive, st, depth+1, steps); err != nil {
				return err
			}
		case !item.IsDir() && feMergeReplaceable(info):
			*steps = append(*steps, feMergeStep{from: from, to: to, replace: true})
		default:
			st.Kept++
		}
	}
	return nil
}

// feMergeReplaceable reports a file Extract here may replace: a regular file.
// Anything else of the same name — a folder, a link, a Windows reparse point
// (a junction, a cloud placeholder) — is left alone.
func feMergeReplaceable(info fs.FileInfo) bool {
	return info.Mode().IsRegular()
}

// feExtractSweep removes staging folders an earlier, interrupted extraction
// left in dest: real folders (never a link) with the prefix and the marker,
// old enough not to be a running job's.
func feExtractSweep(dest string) {
	items, err := os.ReadDir(dest)
	if err != nil {
		return
	}
	for _, item := range items {
		if !item.IsDir() || !strings.HasPrefix(item.Name(), feExtractStagePrefix) {
			continue
		}
		dir := filepath.Join(dest, item.Name())
		if marker, err := os.Lstat(filepath.Join(dir, feExtractMarker)); err != nil || !marker.Mode().IsRegular() {
			continue
		}
		info, err := item.Info()
		if err != nil || time.Since(info.ModTime()) < feExtractStaleAge {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			logWarn("files", "extract: could not remove the leftover %s: %v", item.Name(), err)
		}
	}
}

// --- One job ------------------------------------------------------------------

// feExtractJob is one archive to unpack.
type feExtractJob struct {
	ID      int64
	Archive string // the archive, an absolute path already checked against the Files root
	Dest    string // the folder it is unpacked into (the archive's own)
	Mode    feExtractMode
	Kind    feArchiveKind // feArchiveNone: detect when the job runs
}

// feExtractOutcome is how a job ended.
type feExtractOutcome struct {
	Job      feExtractJob
	Target   string // where the result landed (Extract) or the folder (Extract here)
	IsDir    bool   // Target is a folder
	Stats    feExtractStats
	Err      error
	NeverRan bool // cancelled while it waited its turn
}

// feExtractRun unpacks one job. Whatever happens while it unpacks, the staging
// folder is gone when it returns, and dest holds either the finished result or
// nothing new.
func feExtractRun(ctx context.Context, job feExtractJob, meter *feExtractMeter) (out feExtractOutcome) {
	out.Job = job
	defer func() {
		if r := recover(); r != nil {
			logWarn("files", "extract %s: %v", job.Archive, r)
			out.Target = ""
			out.Err = fmt.Errorf("an internal error stopped it (%v)", r)
		}
	}()
	meter.setPhase("Preparing")
	info, err := os.Stat(job.Archive)
	if err != nil {
		out.Err = err
		return out
	}
	if !info.Mode().IsRegular() {
		out.Err = errFeNotArchive
		return out
	}
	feExtractSweep(job.Dest)
	stage, unpack, err := feExtractStage(job.Dest)
	if err != nil {
		out.Err = err
		return out
	}
	defer feExtractRemoveStage(stage)
	kind := job.Kind
	if kind == feArchiveNone {
		kind = feArchiveDetect(job.Archive)
	}
	meter.setPhase("Unpacking")
	switch kind {
	case feArchiveZip:
		err = feExtractZip(ctx, job.Archive, unpack, &out.Stats, meter)
		if errors.Is(err, errFeArchiveNeedsTool) && feArchiveToolFor(feArchiveZip).Path != "" {
			// Nothing was written: the plan refuses before the first entry.
			out.Stats = feExtractStats{}
			err = feExtractWithTool(ctx, feArchiveZip, job.Archive, unpack, &out.Stats, meter)
		}
	case feArchiveTar, feArchiveTarGz, feArchiveTarXz, feArchiveTarBz2, feArchiveGz, feArchiveXz, feArchiveBz2:
		err = feExtractTar(ctx, job.Archive, unpack, kind, &out.Stats, meter)
	case feArchive7z, feArchiveRar:
		err = feExtractWithTool(ctx, kind, job.Archive, unpack, &out.Stats, meter)
	default:
		err = errFeNotArchive
	}
	// "Finishing" is set before the last look at the cancel: feExtractCancel
	// reads the phase, so a cancel either sees it and is refused, or lands
	// before this check and is honoured.
	meter.setPhase("Finishing")
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		out.Err = err
		return out
	}
	target, err := feExtractFinish(unpack, job.Dest, feArchiveStem(job.Archive), job.Archive, job.Mode, &out.Stats)
	if err != nil {
		out.Err = err
		return out
	}
	out.Target = target
	if fi, err := os.Stat(target); err == nil {
		out.IsDir = fi.IsDir()
	}
	return out
}

// feExtractRemoveStage deletes a staging folder. A folder 7-Zip restored as
// read-only would stop the removal on Linux, so the folders are made writable
// and the removal tried again.
func feExtractRemoveStage(stage string) {
	err := os.RemoveAll(stage)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
		err = os.RemoveAll(stage)
	}
	if err != nil {
		logWarn("files", "extract: could not remove %s: %v", stage, err)
	}
}

// --- The queue -------------------------------------------------------------------

// feExtractQueue runs one extraction at a time — two at once on one SD card
// would only make both slower — and keeps the rest waiting. The worker
// goroutine records outcomes; feExtractDrain reports them on the SDL thread.
var feExtractQueue struct {
	mu       sync.Mutex
	pending  []feExtractJob
	running  bool
	current  feExtractJob
	cancel   context.CancelFunc
	meter    *feExtractMeter
	outcomes []feExtractOutcome
	nextID   int64
}

// feExtractSameJob reports two requests for the same archive.
func feExtractSameJob(a, b feExtractJob) bool {
	return a.Archive != "" && filesSamePath(a.Archive, b.Archive)
}

// feExtractEnqueue adds a job and starts the worker when it is idle. It
// answers the job's place in line (1 = unpacking now) or an error when that
// archive is already being extracted or waiting.
func feExtractEnqueue(job feExtractJob) (int, error) {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running && feExtractSameJob(q.current, job) {
		return 0, fmt.Errorf("%s is being extracted already", filepath.Base(job.Archive))
	}
	for _, waiting := range q.pending {
		if feExtractSameJob(waiting, job) {
			return 0, fmt.Errorf("%s is waiting to be extracted already", filepath.Base(job.Archive))
		}
	}
	q.nextID++
	job.ID = q.nextID
	q.pending = append(q.pending, job)
	place := len(q.pending)
	if q.running && q.current.Archive != "" {
		place++
	}
	if !q.running {
		q.running = true
		go feExtractWorker()
	}
	return place, nil
}

// feExtractWorker unpacks the queue until it is empty.
func feExtractWorker() {
	q := &feExtractQueue
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.running = false
			q.current = feExtractJob{}
			q.cancel = nil
			q.meter = nil
			q.mu.Unlock()
			return
		}
		job := q.pending[0]
		q.pending = q.pending[1:]
		ctx, cancel := context.WithCancel(context.Background())
		meter := &feExtractMeter{}
		q.current, q.cancel, q.meter = job, cancel, meter
		q.mu.Unlock()

		outcome := feExtractRun(ctx, job, meter)
		cancel()
		if outcome.Err != nil && !errors.Is(outcome.Err, context.Canceled) {
			logWarn("files", "extract %s: %v", job.Archive, outcome.Err)
		}

		// The outcome and the end of "running this job" are one step, so the
		// screen never shows a finished job as still unpacking.
		q.mu.Lock()
		q.outcomes = append(q.outcomes, outcome)
		q.current, q.cancel, q.meter = feExtractJob{}, nil, nil
		q.mu.Unlock()
	}
}

// feExtractCancel stops the job for archive (running or waiting). A running
// one reports its cancel through the drain like any other ending; a waiting
// one is dropped and reported at once. A job moving its result into place is
// too far along to stop, and answers false.
func feExtractCancel(archive string) bool {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	probe := feExtractJob{Archive: archive}
	if q.running && feExtractSameJob(q.current, probe) && q.cancel != nil {
		if q.meter.reading().Phase == "Finishing" {
			return false
		}
		q.cancel()
		return true
	}
	for i, waiting := range q.pending {
		if feExtractSameJob(waiting, probe) {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			q.outcomes = append(q.outcomes, feExtractOutcome{Job: waiting, Err: context.Canceled, NeverRan: true})
			return true
		}
	}
	return false
}

// feExtractShutdown stops the queue when JukaHub quits: the waiting jobs are
// dropped, and the running one is cancelled and given a moment to delete its
// staging folder.
func feExtractShutdown() {
	q := &feExtractQueue
	q.mu.Lock()
	q.pending = nil
	running, cancel := q.running, q.cancel
	q.mu.Unlock()
	if !running {
		return
	}
	if cancel != nil {
		cancel()
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		q.mu.Lock()
		stillRunning := q.running
		q.mu.Unlock()
		if !stillRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// feExtractTakeOutcomes hands the finished jobs to the drain.
func feExtractTakeOutcomes() []feExtractOutcome {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.outcomes
	q.outcomes = nil
	return out
}

// feExtractStatus is the queue as the screen shows it.
type feExtractStatus struct {
	Active  bool
	Job     feExtractJob
	Waiting int
	Reading feExtractReading
}

// feExtractSnapshot reads the queue for one frame.
func feExtractSnapshot() feExtractStatus {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.running || q.current.Archive == "" {
		return feExtractStatus{Waiting: len(q.pending)}
	}
	return feExtractStatus{Active: true, Job: q.current, Waiting: len(q.pending), Reading: q.meter.reading()}
}

// feExtractPlace is where archive stands: 1 while it unpacks, 2 and up while
// it waits, 0 when it is not in the queue.
func feExtractPlace(archive string) int {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	probe := feExtractJob{Archive: archive}
	busy := q.running && q.current.Archive != ""
	if busy && feExtractSameJob(q.current, probe) {
		return 1
	}
	for i, waiting := range q.pending {
		if feExtractSameJob(waiting, probe) {
			if busy {
				return i + 2
			}
			return i + 1
		}
	}
	return 0
}

// feExtractDestBusy reports a job unpacking or waiting to unpack into dir:
// what such a job writes is not in a conflict count taken before it ran.
func feExtractDestBusy(dir string) bool {
	q := &feExtractQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running && q.current.Archive != "" && filesSamePath(q.current.Dest, dir) {
		return true
	}
	for _, waiting := range q.pending {
		if filesSamePath(waiting.Dest, dir) {
			return true
		}
	}
	return false
}

// feExtractPercent is a reading's progress in percent, or -1 while unknown.
func feExtractPercent(r feExtractReading) int {
	if r.Total <= 0 {
		return -1
	}
	p := int(r.Done * 100 / r.Total)
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
