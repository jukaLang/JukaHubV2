package main

// bootlogo.go — the picture the handheld shows while it powers on.
//
// Before Linux starts, the firmware's bootloader draws a BMP kept on the
// device's internal storage. JukaHub can put its own boot screen there — the
// logo and the wordmark on the same pixels as the first frame of its boot
// animation (bootlogo_layout.go places both) — so a handheld that starts in
// JukaHub powers on as one continuous picture instead of the TrimUI logo, a
// black gap and then an app.
//
// It is the one place JukaHub writes outside the SD card, so the installer is
// narrow on purpose:
//
//   - The logo is found by partition NAME only (bootlogo_linux.go): a FAT
//     partition called boot-resource or bootloader holding bootlogo.bmp, the
//     way Allwinner's firmware keeps it, or a raw partition called bootlogo
//     that starts with a BMP. Nothing is inferred from a partition number, the
//     sources that name a partition must agree, the partition must be on the
//     internal eMMC and not on the disk JukaHub runs from, and finding two
//     stores with a logo in them is a doubt, not a choice.
//   - The stock file decides the format. Its header bytes are kept verbatim and
//     only the pixel rows are replaced, in the stock's own size, depth, channel
//     order and row order, so the file keeps its exact length and is rewritten
//     in place: nothing on the firmware's partition is created, truncated or
//     renamed.
//   - Anything the parser is unsure of — compression, a palette depth, masks
//     that are not whole bytes, a portrait or implausible size, more than
//     8 MiB — refuses the write instead of approximating it.
//   - Everything that decides a write is prepared on a read-only open: the
//     current picture is copied to the SD card (data/bootlogo/), synced and
//     read back, and recorded, before the store is opened writable — for the
//     write alone, and only while the logo is still the one that was copied. A
//     copy is never overwritten, a record that cannot be read stops the write,
//     every write is read back and compared by SHA-256, and a write that does
//     not read back puts the previous picture back.
//
// Settings → General → Boot logo installs it (A twice) and puts the previous
// picture back (A twice again). Nothing here was run on a device from this
// desk: the partition layout is Allwinner's documented one, and the installer
// says "Unavailable" with the reason rather than guess on anything else.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// bootLogoFileName is the logo's name on a FAT boot partition.
	bootLogoFileName = "bootlogo.bmp"
	// bootLogoMaxBytes caps the stock file: a full-screen 32-bit picture of the
	// largest TrimUI panel is under 4 MiB.
	bootLogoMaxBytes = 8 << 20
	// The plausible stock sizes: a full-screen boot logo, landscape.
	bootLogoMinW    = 320
	bootLogoMinH    = 240
	bootLogoMaxSide = 4096
	// bootLogoStateVersion is the state file's layout.
	bootLogoStateVersion = 1
	// bootLogoArmWindow is how long the first press arms the second.
	bootLogoArmWindow = 4 * time.Second
	// The two kinds of store.
	bootLogoKindFile = "file"
	bootLogoKindRaw  = "raw"
	// bootLogoCrossMixName is the copy CrossMix's BootLogo app can pick.
	bootLogoCrossMixName = "JukaHub.bmp"
)

// The statuses the Settings row shows.
const (
	bootLogoStatusChecking    = "Checking…"
	bootLogoStatusWriting     = "Writing…"
	bootLogoStatusOurs        = "JukaHub"
	bootLogoStatusOriginal    = "Original"
	bootLogoStatusCustom      = "Custom"
	bootLogoStatusDamaged     = "Restore needed"
	bootLogoStatusUnavailable = "Unavailable"
)

// The operations the row runs.
const (
	bootLogoOpProbe   = "probe"
	bootLogoOpInstall = "install"
	bootLogoOpRestore = "restore"
)

// The reasons a probe gives up, short enough for the row's detail line. The
// platform code wraps them with the specifics, which go to the log.
var (
	errBootLogoUnsupported = errors.New("only TrimUI handhelds have a boot logo JukaHub can change")
	errBootLogoNoStore     = errors.New("this firmware keeps its boot logo where JukaHub can't reach it")
	errBootLogoAmbiguous   = errors.New("two boot logo stores; JukaHub won't guess which one is shown")
	errBootLogoFormat      = errors.New("the firmware's logo is a format JukaHub won't rewrite")
)

// ── BMP ──────────────────────────────────────────────────────────────────

const (
	bmpBIRGB       = 0
	bmpBIBitfields = 3
)

// bmpInfo is what the installer needs to know about an uncompressed BMP to
// rewrite its pixel rows in place.
type bmpInfo struct {
	FileSize    int // the size field as stored (0 is legal for BI_RGB)
	DataOffset  int // where the pixel rows start
	HeaderSize  int // the info header's own size field
	Width       int
	Height      int  // rows; always positive
	TopDown     bool // the file's first row is the picture's top row
	BitCount    int  // 24 or 32
	Compression uint32
	RowStride   int // bytes per row, padding included
	// R, G, B and X are each channel's byte within one pixel; X is a 32-bit
	// pixel's fourth byte (alpha or unused) and -1 for 24-bit.
	R, G, B, X int
}

// pixelEnd is where the pixel rows end.
func (b bmpInfo) pixelEnd() int { return b.DataOffset + b.RowStride*b.Height }

// bmpMaskByte maps a channel mask covering one whole byte to that byte's index
// in a little-endian pixel, or -1.
func bmpMaskByte(m uint32) int {
	switch m {
	case 0x000000FF:
		return 0
	case 0x0000FF00:
		return 1
	case 0x00FF0000:
		return 2
	case 0xFF000000:
		return 3
	}
	return -1
}

// bmpParse reads the layout of an uncompressed 24- or 32-bit BMP and checks
// that its pixel rows fit inside data. Every variant the installer would have
// to approximate is refused: the OS/2 headers, palettes, run-length or JPEG
// compression, 16-bit and masks that are not whole bytes.
func bmpParse(data []byte) (bmpInfo, error) {
	var info bmpInfo
	if len(data) < 14+40 {
		return info, errors.New("too short to be a BMP")
	}
	if data[0] != 'B' || data[1] != 'M' {
		return info, errors.New("not a BMP")
	}
	le := binary.LittleEndian
	info.FileSize = int(le.Uint32(data[2:6]))
	info.DataOffset = int(le.Uint32(data[10:14]))
	info.HeaderSize = int(le.Uint32(data[14:18]))
	switch info.HeaderSize {
	case 40, 52, 56, 108, 124:
	default:
		return info, fmt.Errorf("unsupported BMP header of %d bytes", info.HeaderSize)
	}
	if 14+info.HeaderSize > len(data) {
		return info, errors.New("the BMP header is cut short")
	}
	w := int32(le.Uint32(data[18:22]))
	h := int32(le.Uint32(data[22:26]))
	if planes := le.Uint16(data[26:28]); planes != 1 {
		return info, fmt.Errorf("a BMP with %d planes", planes)
	}
	info.BitCount = int(le.Uint16(data[28:30]))
	info.Compression = le.Uint32(data[30:34])
	if w <= 0 || h == 0 {
		return info, fmt.Errorf("a %dx%d BMP", w, h)
	}
	info.Width = int(w)
	if h < 0 {
		info.TopDown = true
		info.Height = -int(h)
	} else {
		info.Height = int(h)
	}
	if info.Width > bootLogoMaxSide || info.Height > bootLogoMaxSide {
		return info, fmt.Errorf("a %dx%d BMP is no screen", info.Width, info.Height)
	}
	minOffset := 14 + info.HeaderSize
	switch info.BitCount {
	case 24:
		if info.Compression != bmpBIRGB {
			return info, fmt.Errorf("a 24-bit BMP with compression %d", info.Compression)
		}
		info.B, info.G, info.R, info.X = 0, 1, 2, -1
	case 32:
		switch info.Compression {
		case bmpBIRGB:
			info.B, info.G, info.R, info.X = 0, 1, 2, 3
		case bmpBIBitfields:
			// The masks sit in a V2+ header, or right after a 40-byte one.
			at := 14 + 40
			if info.HeaderSize == 40 {
				minOffset += 12
			}
			if at+12 > len(data) {
				return info, errors.New("the BMP's colour masks are cut short")
			}
			r := bmpMaskByte(le.Uint32(data[at:]))
			g := bmpMaskByte(le.Uint32(data[at+4:]))
			b := bmpMaskByte(le.Uint32(data[at+8:]))
			if r < 0 || g < 0 || b < 0 || r == g || g == b || r == b {
				return info, errors.New("BMP colour masks that are not whole bytes")
			}
			x := 6 - r - g - b // the byte left over (0+1+2+3 = 6)
			if info.HeaderSize >= 56 {
				if a := le.Uint32(data[at+12:]); a != 0 && bmpMaskByte(a) != x {
					return info, errors.New("a BMP alpha mask over a colour byte")
				}
			}
			info.R, info.G, info.B, info.X = r, g, b, x
		default:
			return info, fmt.Errorf("a 32-bit BMP with compression %d", info.Compression)
		}
	default:
		return info, fmt.Errorf("a %d-bit BMP", info.BitCount)
	}
	info.RowStride = (info.Width*info.BitCount + 31) / 32 * 4
	if info.DataOffset < minOffset {
		return info, errors.New("the BMP's pixel rows overlap its header")
	}
	if info.pixelEnd() > len(data) {
		return info, errors.New("the BMP's pixel rows are cut short")
	}
	return info, nil
}

// bootLogoAcceptable applies the installer's own limits to a parsed stock
// logo of total bytes: a landscape picture of a plausible screen size, no more
// than 8 MiB, whose size field agrees with its pixel rows. A portrait logo
// means the firmware stores it rotated, and which way is not something to
// guess.
func bootLogoAcceptable(info bmpInfo, total int) error {
	if total > bootLogoMaxBytes {
		return fmt.Errorf("%w (%d bytes is over 8 MiB)", errBootLogoFormat, total)
	}
	if info.Width < bootLogoMinW || info.Height < bootLogoMinH || info.Width > bootLogoMaxSide || info.Height > bootLogoMaxSide {
		return fmt.Errorf("%w (%dx%d is no screen size)", errBootLogoFormat, info.Width, info.Height)
	}
	if info.Height > info.Width {
		return fmt.Errorf("%w (%dx%d is stored rotated)", errBootLogoFormat, info.Width, info.Height)
	}
	if info.FileSize != 0 && (info.FileSize < info.pixelEnd() || info.FileSize > total) {
		return fmt.Errorf("%w (its size field says %d bytes, the file has %d)", errBootLogoFormat, info.FileSize, total)
	}
	return nil
}

// bootLogoValidate parses a stock logo and applies the installer's limits.
func bootLogoValidate(data []byte) (bmpInfo, error) {
	info, err := bmpParse(data)
	if err != nil {
		return info, fmt.Errorf("%w (%v)", errBootLogoFormat, err)
	}
	return info, bootLogoAcceptable(info, len(data))
}

// bmpPatch returns a copy of stock with its pixels replaced by rgba (straight
// RGBA rows, top row first, info.Width×info.Height). Everything outside the
// pixel rows — the headers, a palette, trailing bytes — is kept byte for byte,
// so the result is exactly as long as the stock file. A 32-bit pixel's fourth
// byte is written opaque, and row padding is zeroed.
func bmpPatch(stock []byte, info bmpInfo, rgba []byte) ([]byte, error) {
	if info.Width <= 0 || info.Height <= 0 || len(rgba) != info.Width*info.Height*4 {
		return nil, fmt.Errorf("a %d-byte picture for a %dx%d logo", len(rgba), info.Width, info.Height)
	}
	if info.BitCount != 24 && info.BitCount != 32 {
		return nil, fmt.Errorf("a %d-bit logo", info.BitCount)
	}
	if info.pixelEnd() > len(stock) || info.DataOffset < 14 {
		return nil, errors.New("the pixel rows do not fit the stock file")
	}
	out := append([]byte(nil), stock...)
	bpp := info.BitCount / 8
	rowBytes := info.Width * bpp
	for y := 0; y < info.Height; y++ {
		fileRow := info.Height - 1 - y
		if info.TopDown {
			fileRow = y
		}
		row := out[info.DataOffset+fileRow*info.RowStride : info.DataOffset+(fileRow+1)*info.RowStride]
		src := rgba[y*info.Width*4 : (y+1)*info.Width*4]
		for x := 0; x < info.Width; x++ {
			p := row[x*bpp : x*bpp+bpp]
			p[info.R] = src[x*4]
			p[info.G] = src[x*4+1]
			p[info.B] = src[x*4+2]
			if info.X >= 0 {
				p[info.X] = 0xFF
			}
		}
		for i := rowBytes; i < len(row); i++ {
			row[i] = 0
		}
	}
	return out, nil
}

// bmpEncode24 writes rgba (straight RGBA, top row first) as a 24-bit,
// bottom-up BI_RGB BMP with a 40-byte header: the form CrossMix's BootLogo app
// and the other flashing tools ask for. It returns nil for an empty or short
// picture.
func bmpEncode24(rgba []byte, w, h int) []byte {
	if w <= 0 || h <= 0 || len(rgba) < w*h*4 {
		return nil
	}
	stride := (w*3 + 3) &^ 3
	size := 54 + stride*h
	out := make([]byte, size)
	le := binary.LittleEndian
	out[0], out[1] = 'B', 'M'
	le.PutUint32(out[2:], uint32(size))
	le.PutUint32(out[10:], 54)
	le.PutUint32(out[14:], 40)
	le.PutUint32(out[18:], uint32(w))
	le.PutUint32(out[22:], uint32(h))
	le.PutUint16(out[26:], 1)
	le.PutUint16(out[28:], 24)
	le.PutUint32(out[34:], uint32(stride*h))
	le.PutUint32(out[38:], 2835) // 72 dpi
	le.PutUint32(out[42:], 2835)
	for y := 0; y < h; y++ {
		row := out[54+(h-1-y)*stride:]
		src := rgba[y*w*4:]
		for x := 0; x < w; x++ {
			row[x*3] = src[x*4+2]
			row[x*3+1] = src[x*4+1]
			row[x*3+2] = src[x*4]
		}
	}
	return out
}

// ── The picture ──────────────────────────────────────────────────────────

// bootLogoCompose paints the boot screen at w×h as opaque RGBA: black, the
// logo artwork (already resampled to the layout's sprite side, straight
// alpha) and the wordmark's coverage (ww×wh, one byte per pixel) in
// bootLogoInk, each where bootLogoPlace puts it for a line box as tall as the
// wordmark's own surface. The wordmark's left edge is (w-ww)/2 in whole
// pixels, the same rounding the boot animation draws it with.
func bootLogoCompose(w, h int, logo *image.NRGBA, word []uint8, ww, wh int) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	out := make([]byte, w*h*4)
	for i := 3; i < len(out); i += 4 {
		out[i] = 255
	}
	over := func(x, y int, r, g, b, a uint32) {
		if a == 0 || x < 0 || y < 0 || x >= w || y >= h {
			return
		}
		d := out[(y*w+x)*4 : (y*w+x)*4+3]
		d[0] = uint8((r*a + uint32(d[0])*(255-a) + 127) / 255)
		d[1] = uint8((g*a + uint32(d[1])*(255-a) + 127) / 255)
		d[2] = uint8((b*a + uint32(d[2])*(255-a) + 127) / 255)
	}
	l := bootLogoPlace(int32(w), int32(h), int32(wh))
	if logo != nil {
		b := logo.Rect
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				i := logo.PixOffset(b.Min.X+x, b.Min.Y+y)
				p := logo.Pix[i : i+4]
				over(int(l.Sprite.X)+x, int(l.Sprite.Y)+y, uint32(p[0]), uint32(p[1]), uint32(p[2]), uint32(p[3]))
			}
		}
	}
	if ww > 0 && wh > 0 && len(word) >= ww*wh {
		ox, oy := (w-ww)/2, int(l.WordTop)
		ink := bootLogoInk
		for y := 0; y < wh; y++ {
			for x := 0; x < ww; x++ {
				over(ox+x, oy+y, uint32(ink.R), uint32(ink.G), uint32(ink.B), uint32(word[y*ww+x]))
			}
		}
	}
	return out
}

// bootLogoArtwork is what the render thread hands the worker: the screen the
// wordmark was rasterised for, and its coverage. The artwork itself is decoded
// and resampled on the worker (pure Go).
type bootLogoArtwork struct {
	ScreenW int
	ScreenH int
	Word    []uint8
	WordW   int
	WordH   int
}

// bootLogoWordmark rasterises the wordmark for a screen h pixels tall, with the
// face and size the boot animation draws it with (Inter at bootLogoWordPx, no
// accessibility scaling), so both pictures match. It calls SDL_ttf and must run
// on the render thread.
func bootLogoWordmark(h int) ([]uint8, int, int, bool) {
	font := loadCachedFont(homeUIFontFile, bootLogoWordPx(int32(h)))
	return bootLiquidTextAlpha(font, bootLogoWordText)
}

// bootLogoPicture renders the boot screen at the stock logo's size.
func bootLogoPicture(info bmpInfo, art bootLogoArtwork) ([]byte, error) {
	if art.ScreenW != info.Width || art.ScreenH != info.Height {
		return nil, fmt.Errorf("the boot logo is %dx%d now, not the %dx%d it was checked at; try again", info.Width, info.Height, art.ScreenW, art.ScreenH)
	}
	src := brandLogoSource()
	if src == nil {
		return nil, errors.New("the JukaHub logo artwork could not be decoded")
	}
	l := bootLogoPlace(int32(info.Width), int32(info.Height), int32(art.WordH))
	logo := brandResample(src, int(l.Sprite.W))
	return bootLogoCompose(info.Width, info.Height, logo, art.Word, art.WordW, art.WordH), nil
}

// ── Where the logo lives ─────────────────────────────────────────────────

// bootLogoWhere names the store a target opened.
type bootLogoWhere struct {
	Kind   string // bootLogoKindFile or bootLogoKindRaw
	Device string // the block device, e.g. /dev/mmcblk0p1
	Part   string // the partition's name, e.g. boot-resource
	File   string // the logo's name on a FAT partition
}

// String is the store in one phrase, for the log.
func (w bootLogoWhere) String() string {
	if w.Kind == bootLogoKindFile {
		return w.Part + " (" + w.Device + ") /" + w.File
	}
	return w.Part + " partition (" + w.Device + ")"
}

// bootLogoTarget is the firmware's logo, opened for one operation.
type bootLogoTarget interface {
	// Where names the store.
	Where() bootLogoWhere
	// Read returns the logo's current bytes: the whole BMP.
	Read() ([]byte, error)
	// WriteInPlace overwrites the logo with data, which must be exactly as
	// long as what Read returned, and syncs it to the device.
	WriteInPlace(data []byte) error
	// Close unmounts what the open mounted and puts back what it remounted.
	// It is safe to call twice.
	Close() error
}

// Overridable for tests: whether this device can have its logo changed, how
// the store is opened, where the backups go, the SD card's root (for the
// CrossMix copy), the wordmark raster, how work is started, the clock and the
// toasts.
var (
	bootLogoSupportedFn = func() bool { return bootLogoPlatformSupported() && IsTrimuiDevice() }
	bootLogoOpenFn      = bootLogoOpenPlatform
	bootLogoDataDirFn   = defaultBootLogoDataDir
	bootLogoSDRootFn    = defaultBootLogoSDRoot
	bootLogoWordmarkFn  = bootLogoWordmark
	bootLogoGo          = func(fn func()) { go fn() }
	bootLogoNow         = time.Now
	bootLogoToast       = ShowToast
)

// defaultBootLogoDataDir is data/bootlogo beside the app.
func defaultBootLogoDataDir() (string, error) {
	dir, err := P().DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bootlogo"), nil
}

// defaultBootLogoSDRoot is the card JukaHub runs from: it lives in
// <SD>/Apps/JukaHub, so the executable's path names the root.
func defaultBootLogoSDRoot() string {
	if root := strings.TrimSpace(os.Getenv("JUKAHUB_SD_ROOT")); root != "" {
		return root
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	apps := filepath.Dir(filepath.Dir(exe))
	if !strings.EqualFold(filepath.Base(apps), "Apps") {
		return ""
	}
	return filepath.Dir(apps)
}

// ── State and backups ────────────────────────────────────────────────────

// bootLogoState is data/bootlogo/state.json: what was installed where, and
// where the picture it replaced is kept. Interrupted is raised before every
// write to the partition and lowered once the write has read back, so a write
// cut short by a power loss is recognised and offered a restore. Original is
// the very first copy ever made on this card, kept for the docs' recovery
// steps even after a later install replaced a custom logo.
type bootLogoState struct {
	Version     int       `json:"version"`
	Installed   bool      `json:"installed"`
	Interrupted bool      `json:"interrupted,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Device      string    `json:"device,omitempty"`
	Part        string    `json:"part,omitempty"`
	File        string    `json:"file,omitempty"`
	Model       string    `json:"model,omitempty"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	Size        int       `json:"size,omitempty"`
	BackupPath  string    `json:"backupPath,omitempty"`
	BackupSHA   string    `json:"backupSha256,omitempty"`
	OursSHA     string    `json:"oursSha256,omitempty"`
	Original    string    `json:"originalBackup,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	RestoredAt  time.Time `json:"restoredAt"`
}

// bootLogoSHA is the hex SHA-256 of data.
func bootLogoSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// bootLogoStatePath is the state file's path.
func bootLogoStatePath() (string, error) {
	dir, err := bootLogoDataDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// bootLogoLoadState reads the state; a missing or unreadable one is empty.
func bootLogoLoadState() bootLogoState {
	path, err := bootLogoStatePath()
	if err != nil {
		return bootLogoState{}
	}
	var st bootLogoState
	if _, err := readJSONWithBackup(path, &st); err != nil || st.Version != bootLogoStateVersion {
		return bootLogoState{}
	}
	return st
}

// bootLogoLoadStateStrict reads the state for an operation that writes. A
// missing record is a first install, but one that exists and cannot be read —
// damaged past its .bak, or written by another version — stops the write:
// where the previous picture is kept, and whether a write was cut short, are
// exactly what a write relies on.
func bootLogoLoadStateStrict() (bootLogoState, error) {
	path, err := bootLogoStatePath()
	if err != nil {
		return bootLogoState{}, err
	}
	// Absent is a first install, as readJSONWithBackup treats it: a .bak left
	// beside a record someone removed is not brought back. (A data folder that
	// cannot exist is the backup's failure to report, not the record's.)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return bootLogoState{}, nil
	}
	var st bootLogoState
	if _, err := readJSONWithBackup(path, &st); err != nil {
		return bootLogoState{}, fmt.Errorf("the boot logo record (%s) cannot be read, so nothing was written; remove it only if the device shows its original logo: %w", path, err)
	}
	if st.Version != bootLogoStateVersion {
		return bootLogoState{}, fmt.Errorf("the boot logo record (%s) was written by another JukaHub version, so nothing was written", path)
	}
	return st, nil
}

// bootLogoSaveState writes the state through the crash-safe save path.
func bootLogoSaveState(st bootLogoState) error {
	path, err := bootLogoStatePath()
	if err != nil {
		return err
	}
	st.Version = bootLogoStateVersion
	payload, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, payload, 0o644)
}

// bootLogoStateSaysOurs reports whether this card installed the JukaHub boot
// logo on this kind of device and has not put the previous one back. It reads
// only the state file — no mount — so the boot animation can ask at startup
// whether the firmware has just shown the same picture its first frame draws.
func bootLogoStateSaysOurs() bool {
	if !bootLogoSupportedFn() {
		return false
	}
	st := bootLogoLoadState()
	if !st.Installed || st.Interrupted || st.OursSHA == "" {
		return false
	}
	return st.Model == "" || st.Model == string(DetectDevice())
}

// bootLogoWriteBackup copies data into dir as before-<sha>.bmp, synced and read
// back, and returns its path and hash. An identical copy already there is
// reused; a different file under that name is never touched — this copy gets
// a name of its own instead.
func bootLogoWriteBackup(dir string, data []byte) (string, string, error) {
	sum := bootLogoSHA(data)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "before-"+sum[:12]+".bmp")
	if existing, err := os.ReadFile(path); err == nil {
		if bootLogoSHA(existing) == sum {
			return path, sum, nil
		}
		path = filepath.Join(dir, "before-"+sum[:12]+"-"+bootLogoNow().Format("20060102-150405")+".bmp")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", "", fmt.Errorf("%s already exists", filepath.Base(path))
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	syncDirectory(dir)
	back, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if bootLogoSHA(back) != sum {
		return "", "", errors.New("the copy did not read back intact")
	}
	return path, sum, nil
}

// bootLogoWriteVerified writes data in place and reads it back.
func bootLogoWriteVerified(t bootLogoTarget, data []byte, sum string) error {
	if err := t.WriteInPlace(data); err != nil {
		return err
	}
	back, err := t.Read()
	if err != nil {
		return fmt.Errorf("could not read it back: %w", err)
	}
	if bootLogoSHA(back) != sum {
		return errors.New("it did not read back as written")
	}
	return nil
}

// bootLogoVerifyFresh opens the store again — a new mount, so the bytes come
// from the device and not from the page cache of the one just closed — and
// checks the logo hashes to sum.
func bootLogoVerifyFresh(sum string) error {
	t, err := bootLogoOpenFn(false)
	if err != nil {
		return err
	}
	defer t.Close()
	data, err := t.Read()
	if err != nil {
		return err
	}
	if bootLogoSHA(data) != sum {
		return errors.New("the device returned different bytes after the write")
	}
	return nil
}

// ── Install and restore ──────────────────────────────────────────────────

// errBootLogoChanged is a logo that changed between the read-only look an
// operation was prepared from and its writable open.
var errBootLogoChanged = errors.New("the boot logo changed while JukaHub was preparing; nothing was written — try again")

// bootLogoWarning is an operation that did what it was asked and has something
// to add (a partition it could not make read-only again): the row shows it as
// a warning instead of a success.
type bootLogoWarning struct{ msg string }

func (w *bootLogoWarning) Error() string { return w.msg }

// bootLogoReadOnce opens the store read-only, reads the logo and lets go of it.
func bootLogoReadOnce() (bootLogoWhere, []byte, error) {
	t, err := bootLogoOpenFn(false)
	if err != nil {
		return bootLogoWhere{}, nil, err
	}
	where := t.Where()
	defer func() {
		if cerr := t.Close(); cerr != nil {
			logWarn("bootlogo", "letting go of %s after reading it: %v", where, cerr)
		}
	}()
	data, err := t.Read()
	if err != nil {
		return where, nil, fmt.Errorf("could not read the boot logo: %w", err)
	}
	return where, data, nil
}

// bootLogoSwap is how a writable open went that did not place the new bytes:
// untouched when the device still holds what was there (nothing written, or
// rolled back) — or, after a good write, closeErr: a close that failed (the
// write stands, but the user is told what was left).
type bootLogoSwap struct {
	untouched bool
	closeErr  error
}

// bootLogoWriteSwap is the only time the store is open for writing: it checks
// that the logo is still prev (the bytes the operation was prepared from),
// writes data over it and reads it back. A write that does not read back puts
// prev back.
func bootLogoWriteSwap(where bootLogoWhere, prev []byte, prevSHA string, data []byte, dataSHA string) (bootLogoSwap, error) {
	t, err := bootLogoOpenFn(true)
	if err != nil {
		return bootLogoSwap{untouched: true}, err
	}
	open := true
	defer func() {
		if open {
			_ = t.Close()
		}
	}()
	if now := t.Where(); now != where {
		return bootLogoSwap{untouched: true}, fmt.Errorf("the boot logo moved from %s to %s while JukaHub was preparing; nothing was written", where, now)
	}
	cur, err := t.Read()
	if err != nil {
		return bootLogoSwap{untouched: true}, fmt.Errorf("could not read the boot logo again, so nothing was written: %w", err)
	}
	if bootLogoSHA(cur) != prevSHA {
		return bootLogoSwap{untouched: true}, errBootLogoChanged
	}
	if werr := bootLogoWriteVerified(t, data, dataSHA); werr != nil {
		logWarn("bootlogo", "write on %s failed: %v; putting back what was there", where, werr)
		if rerr := bootLogoWriteVerified(t, prev, prevSHA); rerr != nil {
			logError("bootlogo", "putting back what was there failed too: %v", rerr)
			return bootLogoSwap{}, fmt.Errorf("the write did not read back (%v), and putting back the logo that was there failed too", werr)
		}
		return bootLogoSwap{untouched: true}, fmt.Errorf("the write did not read back (%v); the logo that was there was put back", werr)
	}
	open = false
	var sw bootLogoSwap
	if cerr := t.Close(); cerr != nil {
		logWarn("bootlogo", "closing %s after the write: %v", where, cerr)
		sw.closeErr = cerr
	}
	return sw, nil
}

// bootLogoInstall writes the JukaHub boot screen over the firmware's logo and
// returns the message for the toast. Everything that decides the write — the
// stock file, the picture, the copy on the SD card and the record of it —
// happens on a read-only open; the store is opened writable only for the
// write itself, and only while the logo is still the one that was copied. A
// write that does not read back puts the previous picture straight back.
func bootLogoInstall(art bootLogoArtwork) (string, error) {
	if !bootLogoSupportedFn() {
		return "", errBootLogoUnsupported
	}
	st, err := bootLogoLoadStateStrict()
	if err != nil {
		return "", err
	}
	where, stock, err := bootLogoReadOnce()
	if err != nil {
		return "", err
	}
	info, err := bootLogoValidate(stock)
	if err != nil {
		return "", err
	}
	stockSHA := bootLogoSHA(stock)
	if st.OursSHA != "" && st.OursSHA == stockSHA {
		// Ours is on the partition already (a write that finished but was
		// never recorded as finished included): only the record changes.
		if st.Interrupted || !st.Installed {
			st.Installed, st.Interrupted = true, false
			if err := bootLogoSaveState(st); err != nil {
				logWarn("bootlogo", "could not record the install: %v", err)
			}
		}
		return "The JukaHub boot logo is already installed", nil
	}
	if st.Interrupted && stockSHA != st.BackupSHA {
		// A cut-short write left neither picture there. Backing this up would
		// replace the record of the real previous logo with the damaged one.
		return "", errors.New("the last boot logo write was cut short; restore the previous logo first")
	}
	pic, err := bootLogoPicture(info, art)
	if err != nil {
		return "", err
	}
	ours, err := bmpPatch(stock, info, pic)
	if err != nil {
		return "", err
	}
	oursSHA := bootLogoSHA(ours)
	dir, err := bootLogoDataDirFn()
	if err != nil {
		return "", fmt.Errorf("no folder on the SD card for the backup, so nothing was written: %w", err)
	}
	backupPath, backupSHA, err := bootLogoWriteBackup(dir, stock)
	if err != nil {
		return "", fmt.Errorf("could not save the current logo to the SD card, so nothing was written: %w", err)
	}
	next := bootLogoState{
		Installed:   true,
		Interrupted: true,
		Kind:        where.Kind,
		Device:      where.Device,
		Part:        where.Part,
		File:        where.File,
		Model:       string(DetectDevice()),
		Width:       info.Width,
		Height:      info.Height,
		Size:        len(stock),
		BackupPath:  backupPath,
		BackupSHA:   backupSHA,
		OursSHA:     oursSHA,
		Original:    st.Original,
		InstalledAt: bootLogoNow(),
	}
	if next.Original == "" {
		next.Original = backupPath
	}
	// The record goes first: if the write is cut short, Restore still knows
	// where the previous picture is.
	if err := bootLogoSaveState(next); err != nil {
		return "", fmt.Errorf("could not record the backup, so nothing was written: %w", err)
	}
	logInfo("bootlogo", "installing on %s: %dx%d %d-bit, previous picture kept at %s", where, info.Width, info.Height, info.BitCount, backupPath)
	var closeErr error
	if oursSHA != stockSHA {
		sw, err := bootLogoWriteSwap(where, stock, stockSHA, ours, oursSHA)
		if err != nil {
			if sw.untouched {
				// The previous picture is in place: nothing is installed and
				// nothing was cut short.
				next.Installed, next.Interrupted = false, false
				if serr := bootLogoSaveState(next); serr != nil {
					logWarn("bootlogo", "could not record that nothing was installed: %v", serr)
				}
				return "", err
			}
			// Neither picture is known to be there: the record stays
			// "interrupted", which is what offers Restore.
			return "", fmt.Errorf("%v; use Restore", err)
		}
		closeErr = sw.closeErr
	}
	if verr := bootLogoVerifyFresh(oursSHA); verr != nil {
		logError("bootlogo", "the logo on %s did not verify after the write: %v", where, verr)
		return "", fmt.Errorf("the boot logo did not verify after the write (%v); use Restore", verr)
	}
	next.Interrupted = false
	if err := bootLogoSaveState(next); err != nil {
		logWarn("bootlogo", "could not record the install: %v", err)
	}
	bootLogoCrossMixCopy(pic, info.Width, info.Height)
	logInfo("bootlogo", "installed on %s (sha %s)", where, oursSHA[:12])
	msg := "Boot logo installed — you'll see it the next time the device powers on"
	if closeErr != nil {
		return msg, &bootLogoWarning{msg: "Boot logo installed, but " + closeErr.Error()}
	}
	return msg, nil
}

// bootLogoRestore puts the picture the last install replaced back, on the same
// store of the same kind of device, prepared on a read-only open like an
// install.
func bootLogoRestore() (string, error) {
	if !bootLogoSupportedFn() {
		return "", errBootLogoUnsupported
	}
	st, err := bootLogoLoadStateStrict()
	if err != nil {
		return "", err
	}
	if st.BackupPath == "" || st.BackupSHA == "" {
		return "", errors.New("there is no saved boot logo to put back")
	}
	if model := string(DetectDevice()); st.Model != "" && st.Model != model {
		return "", fmt.Errorf("the saved boot logo is from a %s, not this %s; put it back with another tool", st.Model, model)
	}
	backup, err := os.ReadFile(st.BackupPath)
	if err != nil {
		return "", fmt.Errorf("the saved boot logo is missing from the SD card (%s)", st.BackupPath)
	}
	if bootLogoSHA(backup) != st.BackupSHA {
		return "", fmt.Errorf("the saved boot logo on the SD card is damaged (%s)", st.BackupPath)
	}
	binfo, err := bmpParse(backup)
	if err != nil {
		return "", fmt.Errorf("the saved boot logo is not a BMP JukaHub can write back: %w", err)
	}
	where, cur, err := bootLogoReadOnce()
	if err != nil {
		return "", err
	}
	if st.Kind != "" && (where.Kind != st.Kind || where.Part != st.Part) {
		return "", fmt.Errorf("the saved boot logo came from the %s partition, but the device's logo is on %s now; put it back with another tool", st.Part, where)
	}
	curSHA := bootLogoSHA(cur)
	if curSHA == st.BackupSHA {
		st.Installed, st.Interrupted = false, false
		if err := bootLogoSaveState(st); err != nil {
			logWarn("bootlogo", "could not record the restore: %v", err)
		}
		return "The previous boot logo is already in place", nil
	}
	// The same slot or nothing: the stock header bytes are identical in every
	// picture JukaHub wrote, so a different length or header means another
	// tool has changed the logo since, and it should put its own back.
	if len(cur) != len(backup) || !bytes.Equal(cur[:binfo.DataOffset], backup[:binfo.DataOffset]) {
		return "", errors.New("the boot logo was changed by another tool since; put it back with that tool")
	}
	wasInterrupted := st.Interrupted
	st.Interrupted = true
	if err := bootLogoSaveState(st); err != nil {
		return "", fmt.Errorf("could not record the restore, so nothing was written: %w", err)
	}
	logInfo("bootlogo", "restoring %s from %s", where, st.BackupPath)
	sw, err := bootLogoWriteSwap(where, cur, curSHA, backup, st.BackupSHA)
	if err != nil {
		if sw.untouched {
			st.Interrupted = wasInterrupted
			if serr := bootLogoSaveState(st); serr != nil {
				logWarn("bootlogo", "could not record that nothing was restored: %v", serr)
			}
		}
		logError("bootlogo", "restore on %s failed: %v", where, err)
		if errors.Is(err, errBootLogoChanged) {
			return "", err
		}
		return "", fmt.Errorf("putting the boot logo back failed (%v); try again", err)
	}
	if verr := bootLogoVerifyFresh(st.BackupSHA); verr != nil {
		logError("bootlogo", "the restored logo on %s did not verify: %v", where, verr)
		return "", fmt.Errorf("the restored boot logo did not verify (%v); try again", verr)
	}
	st.Installed, st.Interrupted = false, false
	st.RestoredAt = bootLogoNow()
	if err := bootLogoSaveState(st); err != nil {
		logWarn("bootlogo", "could not record the restore: %v", err)
	}
	msg := "The previous boot logo is back"
	if sw.closeErr != nil {
		return msg, &bootLogoWarning{msg: "The previous boot logo is back, but " + sw.closeErr.Error()}
	}
	return msg, nil
}

// bootLogoCrossMixCopy leaves the boot screen where CrossMix's BootLogo app
// lists its images, when that app is on the card, so it can be picked there
// too. Best-effort: the install has already succeeded.
func bootLogoCrossMixCopy(pic []byte, w, h int) {
	root := strings.TrimSpace(bootLogoSDRootFn())
	if root == "" {
		return
	}
	dir := filepath.Join(root, "Apps", "BootLogo", "Images")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	data := bmpEncode24(pic, w, h)
	if data == nil {
		return
	}
	path := filepath.Join(dir, bootLogoCrossMixName)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("bootlogo", "CrossMix copy: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("bootlogo", "CrossMix copy: %v", err)
		return
	}
	logInfo("bootlogo", "left a copy for CrossMix's BootLogo app at %s", path)
}

// ── Probe ────────────────────────────────────────────────────────────────

// bootLogoProbe is what the Settings row knows about the device's logo.
type bootLogoProbe struct {
	Status string
	Reason string // why it is unavailable, for the detail line
	Where  bootLogoWhere
	Info   bmpInfo
}

// bootLogoReason turns a probe failure into the row's short reason; the full
// error goes to the log.
func bootLogoReason(err error) string {
	for _, known := range []error{errBootLogoUnsupported, errBootLogoNoStore, errBootLogoAmbiguous, errBootLogoFormat} {
		if errors.Is(err, known) {
			return bootLogoSentence(known.Error())
		}
	}
	return "Could not read the boot logo (details in the log)"
}

// bootLogoSentence capitalises a reason for display.
func bootLogoSentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// bootLogoClassify names the logo whose hash is sum against the state.
func bootLogoClassify(st bootLogoState, sum string) string {
	switch {
	case st.OursSHA != "" && sum == st.OursSHA:
		return bootLogoStatusOurs
	case st.Interrupted && sum != st.BackupSHA:
		return bootLogoStatusDamaged
	case st.BackupSHA == "" || sum == st.BackupSHA:
		return bootLogoStatusOriginal
	}
	return bootLogoStatusCustom
}

// bootLogoRunProbe opens the store read-only and classifies what is there.
func bootLogoRunProbe() bootLogoProbe {
	if !bootLogoSupportedFn() {
		return bootLogoProbe{Status: bootLogoStatusUnavailable, Reason: bootLogoReason(errBootLogoUnsupported)}
	}
	t, err := bootLogoOpenFn(false)
	if err != nil {
		logInfo("bootlogo", "unavailable: %v", err)
		return bootLogoProbe{Status: bootLogoStatusUnavailable, Reason: bootLogoReason(err)}
	}
	defer t.Close()
	where := t.Where()
	data, err := t.Read()
	if err != nil {
		logWarn("bootlogo", "reading %s: %v", where, err)
		return bootLogoProbe{Status: bootLogoStatusUnavailable, Reason: bootLogoReason(err), Where: where}
	}
	info, err := bootLogoValidate(data)
	if err != nil {
		logInfo("bootlogo", "%s: %v", where, err)
		return bootLogoProbe{Status: bootLogoStatusUnavailable, Reason: bootLogoReason(err), Where: where}
	}
	st := bootLogoLoadState()
	status := bootLogoClassify(st, bootLogoSHA(data))
	if status != bootLogoStatusOurs && st.Installed && !st.Interrupted {
		// Another tool (or a firmware update) replaced JukaHub's logo: the
		// record stops saying ours, or the next power-on would open the boot
		// animation on a picture the firmware no longer shows.
		st.Installed = false
		if err := bootLogoSaveState(st); err != nil {
			logWarn("bootlogo", "could not record that the logo is no longer JukaHub's: %v", err)
		}
	}
	logInfo("bootlogo", "%s: %dx%d %d-bit, %s", where, info.Width, info.Height, info.BitCount, status)
	return bootLogoProbe{Status: status, Where: where, Info: info}
}

// bootLogoSafeProbe is bootLogoRunProbe with a panic turned into "Unavailable"
// (the probe's deferred close has let go of the store on the way out).
func bootLogoSafeProbe() (p bootLogoProbe) {
	defer func() {
		if r := recover(); r != nil {
			logError("bootlogo", "the boot logo check panicked: %v", r)
			p = bootLogoProbe{Status: bootLogoStatusUnavailable, Reason: "Could not read the boot logo (details in the log)"}
		}
	}()
	return bootLogoRunProbe()
}

// bootLogoRunOp runs one install or restore, with a panic turned into an error
// (its deferred closes have let go of the store on the way out).
func bootLogoRunOp(op string, art bootLogoArtwork) (msg string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logError("bootlogo", "%s panicked: %v", op, r)
			msg, err = "", fmt.Errorf("it stopped unexpectedly (%v); details are in the log", r)
		}
	}()
	if op == bootLogoOpInstall {
		return bootLogoInstall(art)
	}
	return bootLogoRestore()
}

// bootLogoCleanupOnce registers the exit's wait for a write once.
var bootLogoCleanupOnce sync.Once

// bootLogoRegisterCleanup makes quitting wait, briefly, for a write in
// progress: quitting in the middle of one would leave the partition mounted
// writable and the record saying the write was cut short.
func bootLogoRegisterCleanup() {
	bootLogoCleanupOnce.Do(func() {
		Cleanup = append(Cleanup, func() { bootLogoWaitIdle(3 * time.Second) })
	})
}

// bootLogoWaitIdle waits at most limit for an install or restore (and the check
// after it) to finish, and reports whether it did.
func bootLogoWaitIdle(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		_, busy, _, _ := bootLogoSnapshot()
		if busy != bootLogoOpInstall && busy != bootLogoOpRestore {
			return true
		}
		if !time.Now().Before(deadline) {
			logWarn("bootlogo", "quitting while the boot logo is still being written")
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ── Settings row ─────────────────────────────────────────────────────────

// bootLogoUI is the row's live state: the last probe, the operation running
// (one at a time) and the armed first press.
var bootLogoUI struct {
	mu      sync.Mutex
	started bool
	busy    string
	have    bool
	probe   bootLogoProbe
	armOp   string
	armAt   time.Time
}

// bootLogoBegin claims the row for op if nothing else is running.
func bootLogoBegin(op string) bool {
	u := &bootLogoUI
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy != "" {
		return false
	}
	u.busy = op
	u.started = true
	return true
}

// bootLogoFinish records a probe and frees the row.
func bootLogoFinish(p bootLogoProbe) {
	u := &bootLogoUI
	u.mu.Lock()
	u.probe, u.have, u.busy = p, true, ""
	u.mu.Unlock()
}

// bootLogoStartProbe checks the device's logo off the render thread.
func bootLogoStartProbe() {
	if !bootLogoBegin(bootLogoOpProbe) {
		return
	}
	bootLogoGo(func() { bootLogoFinish(bootLogoSafeProbe()) })
}

// bootLogoSnapshot is the row's state under the lock.
func bootLogoSnapshot() (started bool, busy string, have bool, p bootLogoProbe) {
	u := &bootLogoUI
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.started, u.busy, u.have, u.probe
}

// bootLogoRowVisible shows the row only where a boot logo can be changed.
func bootLogoRowVisible(*Config) bool { return bootLogoSupportedFn() }

// bootLogoRowValue is the row's read-out. It is called every frame, so it only
// reads the cached state; the first call starts the probe.
func bootLogoRowValue(*Config) string {
	started, busy, have, p := bootLogoSnapshot()
	if !started {
		bootLogoStartProbe()
		return bootLogoStatusChecking
	}
	switch {
	case busy == bootLogoOpInstall || busy == bootLogoOpRestore:
		return bootLogoStatusWriting
	case !have:
		return bootLogoStatusChecking
	}
	return p.Status
}

// bootLogoRowDetail is the line under the row's label.
func bootLogoRowDetail(*Config) string {
	_, busy, have, p := bootLogoSnapshot()
	switch {
	case busy == bootLogoOpInstall:
		return "Writing the JukaHub logo to the device…"
	case busy == bootLogoOpRestore:
		return "Putting the previous logo back…"
	case !have:
		return "The picture the device shows while it powers on"
	}
	switch p.Status {
	case bootLogoStatusOurs:
		return "Shown at power-on · A twice restores the previous logo"
	case bootLogoStatusOriginal:
		return "Shown at power-on · A twice installs the JukaHub logo"
	case bootLogoStatusCustom:
		return "Set by another tool · A twice installs the JukaHub logo"
	case bootLogoStatusDamaged:
		return "The last write was cut short · A twice restores the logo"
	}
	if p.Reason != "" {
		return p.Reason
	}
	return "The picture the device shows while it powers on"
}

// bootLogoRowOpen is the row's press: the first arms, a second within four
// seconds installs the JukaHub logo (or, when it is installed, puts the
// previous one back) on a worker, and a toast reports the result.
func bootLogoRowOpen(*Config) {
	_, busy, have, p := bootLogoSnapshot()
	switch {
	case busy == bootLogoOpProbe:
		bootLogoToast("Still checking the boot logo…", ToastKindInfo)
		return
	case busy != "":
		bootLogoToast("The boot logo is still being written…", ToastKindInfo)
		return
	case !have:
		bootLogoStartProbe()
		bootLogoToast("Checking the boot logo…", ToastKindInfo)
		return
	case p.Status == bootLogoStatusUnavailable:
		reason := p.Reason
		if reason == "" {
			reason = "This device's boot logo can't be changed"
		}
		bootLogoToast(reason, ToastKindWarning)
		// A transient failure (a busy partition) may pass: check again.
		bootLogoStartProbe()
		return
	}
	op := bootLogoOpInstall
	if p.Status == bootLogoStatusOurs || p.Status == bootLogoStatusDamaged {
		op = bootLogoOpRestore
	}
	now := bootLogoNow()
	u := &bootLogoUI
	u.mu.Lock()
	armed := u.armOp == op && !now.Before(u.armAt) && now.Sub(u.armAt) <= bootLogoArmWindow
	if armed {
		u.armOp = ""
	} else {
		u.armOp, u.armAt = op, now
	}
	u.mu.Unlock()
	if !armed {
		if op == bootLogoOpInstall {
			bootLogoToast("Press A again to install the JukaHub boot logo — the current one is kept on the SD card", ToastKindInfo)
		} else {
			bootLogoToast("Press A again to put the previous boot logo back", ToastKindInfo)
		}
		return
	}
	var art bootLogoArtwork
	if op == bootLogoOpInstall {
		// The wordmark is an SDL_ttf raster: it is drawn here, on the render
		// thread, at the size the probe found, and the worker gets bytes.
		word, ww, wh, ok := bootLogoWordmarkFn(p.Info.Height)
		if !ok {
			bootLogoToast("Could not draw the wordmark for the boot logo", ToastKindError)
			return
		}
		art = bootLogoArtwork{ScreenW: p.Info.Width, ScreenH: p.Info.Height, Word: word, WordW: ww, WordH: wh}
	}
	if !bootLogoBegin(op) {
		bootLogoToast("The boot logo is busy; try again in a moment", ToastKindInfo)
		return
	}
	if op == bootLogoOpInstall {
		bootLogoToast("Writing the JukaHub boot logo…", ToastKindInfo)
	} else {
		bootLogoToast("Putting the previous boot logo back…", ToastKindInfo)
	}
	bootLogoRegisterCleanup()
	bootLogoGo(func() {
		msg, err := bootLogoRunOp(op, art)
		bootLogoFinish(bootLogoSafeProbe())
		var warn *bootLogoWarning
		switch {
		case errors.As(err, &warn):
			logWarn("bootlogo", "%s: %v", op, err)
			bootLogoToast(warn.msg, ToastKindWarning)
		case err != nil:
			logWarn("bootlogo", "%s: %v", op, err)
			bootLogoToast("Boot logo: "+err.Error(), ToastKindError)
		default:
			bootLogoToast(msg, ToastKindSuccess)
		}
	})
}
