package main

// files_extract_test.go — the Files archive extraction (files_extract.go and
// files_extract_ui.go) on real temporary trees: names, formats, where the
// result lands, what Extract here replaces, cancelling, the queue and the
// sheet's rows. unzip_bounds_test.go holds the refusals.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

// feZipTestEntry is one entry of a test zip; a name ending in "/" is a folder.
type feZipTestEntry struct {
	name string
	body string
}

func feZipTestFile(t *testing.T, path string, entries ...feZipTestEntry) {
	t.Helper()
	writeUnzipTestArchive(t, path, func(zw *zip.Writer) {
		for _, e := range entries {
			w, err := zw.Create(e.name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(e.name, "/") {
				continue
			}
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// feTarTestEntry is one member of a test tar.
type feTarTestEntry struct {
	hdr  tar.Header
	body string
}

func feTarTestBytes(t *testing.T, entries ...feTarTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		data := hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeCont
		if data {
			hdr.Size = int64(len(e.body))
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if data {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func feGzipTestBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func feWriteTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func feReadTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestFEExtractRel(t *testing.T) {
	j := filepath.Join
	cases := []struct {
		name, want, reason string
	}{
		{"a.txt", "a.txt", ""},
		{"dir/a.txt", j("dir", "a.txt"), ""},
		{"./dir/./a.txt", j("dir", "a.txt"), ""},
		{"/abs/a.txt", j("abs", "a.txt"), ""},
		{`C:\win\a.txt`, j("win", "a.txt"), ""},
		{"dir/", "dir", ""},
		{"./", "", ""},
		{"", "", ""},
		{"../x", "", feSkipUnsafe},
		{"a/../b", "", feSkipUnsafe},
		{`a\..\..\b`, "", feSkipUnsafe},
		{"x\x00y", "", feSkipUnsafe},
		{"__MACOSX/._a.txt", "", feSkipJunk},
		{"dir/.DS_Store", "", feSkipJunk},
		{`ab:c?"d".txt`, "ab_c__d_.txt", ""},
		{"a:b.txt", "a_b.txt", ""},
		{"C:/x/a.txt", j("x", "a.txt"), ""},
		{"C:", "", ""},
		{"CON", "_CON", ""},
		{"nul.txt", "_nul.txt", ""},
		{"COM1.log", "_COM1.log", ""},
		{"COMX", "COMX", ""},
		{"trail. ", "trail", ""},
		{"...", "_", ""},
		{"COM¹.txt", "_COM¹.txt", ""},
		{".jukahub-extract-x/a.txt", j("_.jukahub-extract-x", "a.txt"), ""},
		{"keep/.jukahub-extract-x", j("keep", "_.jukahub-extract-x"), ""},
	}
	for _, c := range cases {
		got, reason := feExtractRel(c.name)
		if got != c.want || reason != c.reason {
			t.Errorf("feExtractRel(%q) = %q, %q; want %q, %q", c.name, got, reason, c.want, c.reason)
		}
	}
}

func TestFEArchiveKindOf(t *testing.T) {
	cases := map[string]feArchiveKind{
		"game.zip":         feArchiveZip,
		"GAME.ZIP":         feArchiveZip,
		"src.tar.gz":       feArchiveTarGz,
		"src.TGZ":          feArchiveTarGz,
		"src.tar.xz":       feArchiveTarXz,
		"src.txz":          feArchiveTarXz,
		"src.tar.bz2":      feArchiveTarBz2,
		"src.tbz2":         feArchiveTarBz2,
		"src.tar":          feArchiveTar,
		"log.gz":           feArchiveGz,
		"disc.iso.xz":      feArchiveXz,
		"dump.bz2":         feArchiveBz2,
		"set.7z":           feArchive7z,
		"pack.rar":         feArchiveRar,
		"notes.txt":        feArchiveNone,
		".zip":             feArchiveNone,
		"zip":              feArchiveNone,
		"Roms/GBA/x.gba":   feArchiveNone,
		"/card/a b/c.zip ": feArchiveZip,
	}
	for name, want := range cases {
		if got := feArchiveKindOf(name); got != want {
			t.Errorf("feArchiveKindOf(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFEArchiveStem(t *testing.T) {
	cases := map[string]string{
		"Game (USA).zip":   "Game (USA)",
		"backup.tar.gz":    "backup",
		"disc.iso.gz":      "disc.iso",
		"x.ZIP":            "x",
		".zip":             "archive",
		"ab: c.zip":        "ab_ c",
		"notes":            "notes",
		"/card/Pack.7z":    "Pack",
		"weird name .rar":  "weird name",
		"dots....tar.bz2":  "dots",
		"CON.zip":          "_CON",
		"set.part1.rar":    "set.part1",
		"Folder.v1.2.zip":  "Folder.v1.2",
		"spaced  .tar.xz ": "spaced",
	}
	for name, want := range cases {
		if got := feArchiveStem(name); got != want {
			t.Errorf("feArchiveStem(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFEArchiveSniffAndReconcile(t *testing.T) {
	tarHead := make([]byte, 512)
	copy(tarHead[257:], "ustar")
	cases := []struct {
		head []byte
		want feArchiveKind
	}{
		{[]byte("PK\x03\x04rest"), feArchiveZip},
		{[]byte{0x1f, 0x8b, 8, 0}, feArchiveGz},
		{[]byte{0xfd, '7', 'z', 'X', 'Z', 0, 0}, feArchiveXz},
		{[]byte("BZh91AY"), feArchiveBz2},
		{[]byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c, 0}, feArchive7z},
		{[]byte("Rar!\x1a\x07\x01\x00"), feArchiveRar},
		{tarHead, feArchiveTar},
		{[]byte("plain text"), feArchiveNone},
		{nil, feArchiveNone},
	}
	for _, c := range cases {
		if got := feArchiveSniffBytes(c.head); got != c.want {
			t.Errorf("feArchiveSniffBytes(%q) = %v, want %v", c.head, got, c.want)
		}
	}
	reconcile := []struct {
		named, sniffed, want feArchiveKind
	}{
		{feArchiveTarGz, feArchiveGz, feArchiveTarGz},
		{feArchiveTarXz, feArchiveXz, feArchiveTarXz},
		{feArchiveTarBz2, feArchiveBz2, feArchiveTarBz2},
		{feArchiveZip, feArchive7z, feArchive7z},
		{feArchiveZip, feArchiveNone, feArchiveZip},
		{feArchiveGz, feArchiveTar, feArchiveTar},
		{feArchiveRar, feArchiveZip, feArchiveZip},
	}
	for _, c := range reconcile {
		if got := feArchiveReconcile(c.named, c.sniffed); got != c.want {
			t.Errorf("feArchiveReconcile(%v, %v) = %v, want %v", c.named, c.sniffed, got, c.want)
		}
	}
}

func TestFEDecodeCP437(t *testing.T) {
	if len(feCP437High) != 128 {
		t.Fatalf("the code page 437 table has %d runes, want 128", len(feCP437High))
	}
	cases := map[string]string{
		"caf\x82.txt":  "café.txt",
		"\x81ber":      "über",
		"plain.txt":    "plain.txt",
		"\x8e\x99\x9a": "ÄÖÜ",
		"\xe1":         "ß",
	}
	for in, want := range cases {
		if got := feDecodeCP437(in); got != want {
			t.Errorf("feDecodeCP437(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFEExtractFreeName(t *testing.T) {
	dir := t.TempDir()
	feWriteTestFile(t, filepath.Join(dir, "game.gba"), "x")
	feWriteTestFile(t, filepath.Join(dir, "game (2).gba"), "x")
	feWriteTestFile(t, filepath.Join(dir, ".bashrc"), "x")
	if err := os.Mkdir(filepath.Join(dir, "Game v1.2"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		isDir bool
		want  string
	}{
		{"new.txt", false, "new.txt"},
		{"game.gba", false, "game (3).gba"},
		{"Game v1.2", true, "Game v1.2 (2)"},
		{".bashrc", false, ".bashrc (2)"},
	}
	for _, c := range cases {
		got, err := feExtractFreeName(dir, c.name, c.isDir)
		if err != nil {
			t.Fatalf("feExtractFreeName(%q): %v", c.name, err)
		}
		if filepath.Base(got) != c.want {
			t.Errorf("feExtractFreeName(%q) = %q, want %q", c.name, filepath.Base(got), c.want)
		}
	}
}

func TestFEExtractLoneFolderKeepsItsName(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.zip")
	feZipTestFile(t, archive,
		feZipTestEntry{name: "Game/"},
		feZipTestEntry{name: "Game/a.txt", body: "alpha"},
		feZipTestEntry{name: "Game/sub/b.txt", body: "beta"},
	)
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if want := filepath.Join(dir, "Game"); out.Target != want || !out.IsDir {
		t.Fatalf("target = %q (dir %v), want the folder %q", out.Target, out.IsDir, want)
	}
	if out.Stats.Files != 2 {
		t.Fatalf("files = %d, want 2", out.Stats.Files)
	}
	unzipTestOnly(t, dir, "Game", "pack.zip")
	if got := feReadTestFile(t, filepath.Join(dir, "Game", "sub", "b.txt")); got != "beta" {
		t.Fatalf("Game/sub/b.txt = %q", got)
	}
}

func TestFEExtractSeveralItemsGetAFolder(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "Pack (USA).zip")
	feZipTestFile(t, archive,
		feZipTestEntry{name: "a.txt", body: "a"},
		feZipTestEntry{name: "b/c.txt", body: "c"},
	)
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if want := filepath.Join(dir, "Pack (USA)"); out.Target != want {
		t.Fatalf("target = %q, want %q", out.Target, want)
	}
	unzipTestOnly(t, dir, "Pack (USA)", "Pack (USA).zip")
	unzipTestOnly(t, filepath.Join(dir, "Pack (USA)"), "a.txt", "b")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(out.Target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o055 == 0 {
			t.Fatalf("the new folder kept the staging folder's private mode %v", info.Mode().Perm())
		}
	}
}

func TestFEExtractNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	feWriteTestFile(t, filepath.Join(dir, "game.gba"), "the user's own")
	feWriteTestFile(t, filepath.Join(dir, "Game", "save.srm"), "progress")
	single := filepath.Join(dir, "game.zip")
	feZipTestFile(t, single, feZipTestEntry{name: "game.gba", body: "from the zip"})
	folder := filepath.Join(dir, "folder.zip")
	feZipTestFile(t, folder, feZipTestEntry{name: "Game/rom.gba", body: "rom"})

	out := unzipTestRun(single, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if filepath.Base(out.Target) != "game (2).gba" {
		t.Fatalf("target = %q, want game (2).gba", out.Target)
	}
	out = unzipTestRun(folder, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if filepath.Base(out.Target) != "Game (2)" {
		t.Fatalf("target = %q, want Game (2)", out.Target)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "game.gba")); got != "the user's own" {
		t.Fatalf("an existing file was overwritten: %q", got)
	}
	unzipTestOnly(t, filepath.Join(dir, "Game"), "save.srm")
	if got := feReadTestFile(t, filepath.Join(dir, "game (2).gba")); got != "from the zip" {
		t.Fatalf("game (2).gba = %q", got)
	}
}

func TestFEExtractHereMergesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	feWriteTestFile(t, filepath.Join(dir, "a.txt"), "old")
	feWriteTestFile(t, filepath.Join(dir, "keep.txt"), "mine")
	feWriteTestFile(t, filepath.Join(dir, "sub", "old.txt"), "old sub")
	if err := os.Mkdir(filepath.Join(dir, "thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "update.zip")
	feZipTestFile(t, archive,
		feZipTestEntry{name: "a.txt", body: "new"},
		feZipTestEntry{name: "sub/new.txt", body: "new sub"},
		feZipTestEntry{name: "fresh.txt", body: "fresh"},
		feZipTestEntry{name: "thing", body: "a file where a folder is"},
	)
	out := unzipTestRun(archive, feExtractMerge)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.Target != dir {
		t.Fatalf("target = %q, want the folder itself", out.Target)
	}
	if out.Stats.Replaced != 1 || out.Stats.Kept != 1 {
		t.Fatalf("replaced %d, kept %d; want 1 and 1", out.Stats.Replaced, out.Stats.Kept)
	}
	checks := map[string]string{
		"a.txt":       "new",
		"keep.txt":    "mine",
		"fresh.txt":   "fresh",
		"sub/old.txt": "old sub",
		"sub/new.txt": "new sub",
	}
	for rel, want := range checks {
		if got := feReadTestFile(t, filepath.Join(dir, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "thing")); err != nil || !info.IsDir() {
		t.Fatalf("the folder named like a file in the archive was replaced: %v", err)
	}
	unzipTestOnly(t, dir, "a.txt", "fresh.txt", "keep.txt", "sub", "thing", "update.zip")
}

func TestFEExtractTarGzRules(t *testing.T) {
	dir := t.TempDir()
	data := feTarTestBytes(t,
		feTarTestEntry{hdr: tar.Header{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755}},
		feTarTestEntry{hdr: tar.Header{Name: "top/a.txt", Typeflag: tar.TypeReg, ModTime: time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)}, body: "alpha"},
		feTarTestEntry{hdr: tar.Header{Name: "top/run.sh", Typeflag: tar.TypeReg, Mode: 0o755}, body: "#!/bin/sh\n"},
		feTarTestEntry{hdr: tar.Header{Name: "top/hard", Typeflag: tar.TypeLink, Linkname: "top/a.txt"}},
		feTarTestEntry{hdr: tar.Header{Name: "top/soft", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		feTarTestEntry{hdr: tar.Header{Name: "top/pipe", Typeflag: tar.TypeFifo}},
		feTarTestEntry{hdr: tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg}, body: "boom"},
	)
	archive := filepath.Join(dir, "src.tar.gz")
	if err := os.WriteFile(archive, feGzipTestBytes(t, data), 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.Target != filepath.Join(dir, "top") {
		t.Fatalf("target = %q", out.Target)
	}
	unzipTestOnly(t, filepath.Join(dir, "top"), "a.txt", "hard", "run.sh")
	if got := feReadTestFile(t, filepath.Join(dir, "top", "hard")); got != "alpha" {
		t.Fatalf("the hard link was not copied from its file: %q", got)
	}
	if s := out.Stats.Skips; s.Links != 1 || s.Special != 1 || s.Unsafe != 1 {
		t.Fatalf("skips = %+v, want one link, one special file and one unsafe name", s)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Fatal("a traversal member was written outside the folder")
	}
	info, err := os.Stat(filepath.Join(dir, "top", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Year() != 2020 {
		t.Errorf("a.txt is dated %v, want the archive's 2020 date", info.ModTime())
	}
	if runtime.GOOS != "windows" {
		if run, err := os.Stat(filepath.Join(dir, "top", "run.sh")); err != nil || run.Mode().Perm()&0o100 == 0 {
			t.Errorf("run.sh lost its executable bit: %v %v", run, err)
		}
	}
}

func TestFEExtractTarXz(t *testing.T) {
	dir := t.TempDir()
	data := feTarTestBytes(t,
		feTarTestEntry{hdr: tar.Header{Name: "one.txt", Typeflag: tar.TypeReg}, body: "1"},
		feTarTestEntry{hdr: tar.Header{Name: "two.txt", Typeflag: tar.TypeReg}, body: "22"},
	)
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "pair.tar.xz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	unzipTestOnly(t, dir, "pair", "pair.tar.xz")
	if got := feReadTestFile(t, filepath.Join(dir, "pair", "two.txt")); got != "22" {
		t.Fatalf("two.txt = %q", got)
	}
}

// feTarBz2Fixture is a tar.bz2 (made with Python's tarfile, since the
// standard library has no bzip2 writer) holding bz/hello.txt = "hi from
// bzip2\n".
const feTarBz2Fixture = "425a683931415926535901396e84000073fb80ca1004004001ff8004007366de500808200075114da6a190340c207a9a" +
	"6d41254d0c800000068fee618840d8908453d5720eb606a0440ac61c6fcfd6b84d9980115803e24bdbd9139a22c7108b" +
	"1a420f8626ae117a1cf754a073a227d4d5e60920fc5dc914e1424004e5ba10"

func TestFEExtractTarBz2(t *testing.T) {
	data, err := hex.DecodeString(feTarBz2Fixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "hello.tar.bz2")
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "bz", "hello.txt")); got != "hi from bzip2\n" {
		t.Fatalf("bz/hello.txt = %q", got)
	}
}

func TestFEExtractSingleGzFileAndHiddenTar(t *testing.T) {
	dir := t.TempDir()
	disc := filepath.Join(dir, "disc.iso.gz")
	if err := os.WriteFile(disc, feGzipTestBytes(t, []byte("ISO DATA")), 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(disc, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "disc.iso")); got != "ISO DATA" {
		t.Fatalf("disc.iso = %q", got)
	}

	bundle := filepath.Join(dir, "bundle.gz")
	tarData := feTarTestBytes(t, feTarTestEntry{hdr: tar.Header{Name: "inside/x.txt", Typeflag: tar.TypeReg}, body: "x"})
	if err := os.WriteFile(bundle, feGzipTestBytes(t, tarData), 0o644); err != nil {
		t.Fatal(err)
	}
	out = unzipTestRun(bundle, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "inside", "x.txt")); got != "x" {
		t.Fatalf("a .gz holding a tar was not unpacked as one: %q", got)
	}
}

func TestFEExtractDamagedGzipLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	good := feGzipTestBytes(t, bytes.Repeat([]byte("data "), 2000))
	bad := append([]byte(nil), good[:len(good)/2]...)
	archive := filepath.Join(dir, "cut.gz")
	if err := os.WriteFile(archive, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err == nil {
		t.Fatal("a truncated gzip must fail")
	}
	unzipTestOnly(t, dir, "cut.gz")
}

func TestFEExtractCancelledLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.zip")
	feZipTestFile(t, archive, feZipTestEntry{name: "a.txt", body: "a"}, feZipTestEntry{name: "b.txt", body: "b"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := feExtractRun(ctx, feExtractJob{Archive: archive, Dest: dir, Mode: feExtractSmart}, nil)
	if !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", out.Err)
	}
	unzipTestOnly(t, dir, "pack.zip")
}

func TestFEExtractRefusesEncryptedZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "secret.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.txt", Method: zip.Store, Flags: 0x1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("not really encrypted")); err != nil {
			t.Fatal(err)
		}
	})
	out := unzipTestRun(archive, feExtractSmart)
	if !errors.Is(out.Err, errFeArchiveEncrypted) {
		t.Fatalf("err = %v, want errFeArchiveEncrypted", out.Err)
	}
	unzipTestOnly(t, dir, "secret.zip")
}

// feNopWriteCloser stores a test zip entry under a method the standard
// library cannot read.
type feNopWriteCloser struct{ io.Writer }

func (feNopWriteCloser) Close() error { return nil }

func TestFEExtractZipWithOtherMethodNeedsSevenZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "lzma.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		zw.RegisterCompressor(14, func(w io.Writer) (io.WriteCloser, error) { return feNopWriteCloser{w}, nil })
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.txt", Method: 14})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("raw")); err != nil {
			t.Fatal(err)
		}
	})
	staging := t.TempDir()
	var st feExtractStats
	err := feExtractZip(context.Background(), archive, staging, &st, nil)
	if !errors.Is(err, errFeArchiveNeedsTool) {
		t.Fatalf("err = %v, want errFeArchiveNeedsTool", err)
	}
	if !strings.Contains(err.Error(), "LZMA") {
		t.Errorf("the error does not name the method: %v", err)
	}
	unzipTestOnly(t, staging)
}

func TestFEExtractSweepsLeftoverStaging(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, feExtractStagePrefix+"crashed")
	fresh := filepath.Join(dir, feExtractStagePrefix+"running")
	mine := filepath.Join(dir, feExtractStagePrefix+"photos")
	feWriteTestFile(t, filepath.Join(old, "out", "half.bin"), "x")
	feWriteTestFile(t, filepath.Join(old, feExtractMarker), "")
	feWriteTestFile(t, filepath.Join(fresh, "out", "part.bin"), "x")
	feWriteTestFile(t, filepath.Join(fresh, feExtractMarker), "")
	feWriteTestFile(t, filepath.Join(mine, "photo.jpg"), "x")
	past := time.Now().Add(-2 * feExtractStaleAge)
	for _, d := range []string{old, mine} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(dir, "a.zip")
	feZipTestFile(t, archive, feZipTestEntry{name: "a.txt", body: "a"})
	if out := unzipTestRun(archive, feExtractSmart); out.Err != nil {
		t.Fatal(out.Err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("a staging folder left by a crash was not swept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a recent staging folder was swept")
	}
	if _, err := os.Stat(filepath.Join(mine, "photo.jpg")); err != nil {
		t.Fatal("a folder of the user's with the prefix but no marker was swept")
	}
}

func TestFEExtractTidyHoldsToolOutputToTheRules(t *testing.T) {
	staging := t.TempDir()
	feWriteTestFile(t, filepath.Join(staging, "keep", "a.txt"), "aaaa")
	feWriteTestFile(t, filepath.Join(staging, "keep", ".DS_Store"), "x")
	feWriteTestFile(t, filepath.Join(staging, "__MACOSX", "._a.txt"), "x")
	linked := os.Symlink("/etc/passwd", filepath.Join(staging, "keep", "link")) == nil
	var st feExtractStats
	if err := feExtractTidy(staging, &st); err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 || st.Bytes != 4 {
		t.Fatalf("files %d, bytes %d; want 1 and 4", st.Files, st.Bytes)
	}
	unzipTestOnly(t, staging, "keep")
	unzipTestOnly(t, filepath.Join(staging, "keep"), "a.txt")
	if linked && st.Skips.Links != 1 {
		t.Fatalf("links removed = %d, want 1", st.Skips.Links)
	}
}

func TestFE7ZipFollowProgress(t *testing.T) {
	meter := &feExtractMeter{}
	meter.setTotal(100, false, true)
	fe7ZipFollowProgress(strings.NewReader("  0%\b\b\b\b    \b\b\b\b  5% 3 - a.bin\b\b\b\b\b\b\b\b\b\b\b 42% 9 - b 1000%"), meter)
	if r := meter.reading(); r.Done != 42 || feExtractPercent(r) != 42 {
		t.Fatalf("progress = %d (%d%%), want 42", r.Done, feExtractPercent(r))
	}
	named := &feExtractMeter{}
	fe7ZipFollowProgress(strings.NewReader("  7% 1 - 50% off.txt\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b 12% 2 - 99%.bin"), named)
	if r := named.reading(); r.Done != 12 {
		t.Fatalf("a percentage in a file name moved the meter: %d, want 12", r.Done)
	}
	silent := &feExtractMeter{}
	fe7ZipFollowProgress(strings.NewReader("no meter here"), silent)
	if r := silent.reading(); feExtractPercent(r) != -1 {
		t.Fatalf("a 7-Zip that prints no percentage reads as %d%%, want unknown", feExtractPercent(r))
	}
}

func TestFEArchiveInspectZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.zip")
	feZipTestFile(t, archive,
		feZipTestEntry{name: "Game/a.txt", body: strings.Repeat("a", 10)},
		feZipTestEntry{name: "Game/b.txt", body: strings.Repeat("b", 20)},
		feZipTestEntry{name: "readme.txt", body: "hello"},
	)
	feWriteTestFile(t, filepath.Join(dir, "readme.txt"), "older")
	prev := feExtractFreeSpace
	feExtractFreeSpace = func(string) (int64, error) { return 1 << 40, nil }
	t.Cleanup(func() { feExtractFreeSpace = prev })
	info, rels := feArchiveInspect(context.Background(), archive, dir)
	if info.Err != "" || !info.Listed || info.Kind != feArchiveZip {
		t.Fatalf("inspect: err %q, listed %v, kind %v", info.Err, info.Listed, info.Kind)
	}
	if info.Files != 3 || info.Bytes != 35 || info.TopCount != 2 {
		t.Fatalf("files %d, bytes %d, top %d; want 3, 35, 2", info.Files, info.Bytes, info.TopCount)
	}
	if top := info.Top[0]; top.Name != "Game" || !top.IsDir || top.Files != 2 || top.Bytes != 30 {
		t.Fatalf("first top-level item = %+v", top)
	}
	if len(rels) != 3 {
		t.Fatalf("files to check for conflicts = %d, want 3", len(rels))
	}
	if n := feArchiveCountConflicts(dir, archive, rels, func() bool { return true }); n != 1 {
		t.Fatalf("conflicts = %d, want 1 (readme.txt)", n)
	}
	if n := feArchiveCountConflicts(dir, archive, rels, func() bool { return false }); n != -1 {
		t.Fatalf("a superseded count answered %d, want -1", n)
	}
	if info.SmartName != "pack" || !info.SmartDir {
		t.Fatalf("Extract would land as %q (dir %v), want the folder pack", info.SmartName, info.SmartDir)
	}
	if why := feArchiveBlocker(info); why != "" {
		t.Fatalf("a plain zip is blocked: %s", why)
	}
	if got := feArchiveSmartDetail(info); got != "→ new folder pack/" {
		t.Errorf("Extract detail = %q", got)
	}
	if got := feArchiveMergeDetail(1); got != "replaces 1 file" {
		t.Errorf("Extract here detail = %q", got)
	}
	if got := feArchiveMergeDetail(-1); !strings.Contains(got, "may replace") {
		t.Errorf("Extract here detail with an unknown count = %q", got)
	}
}

func TestFEArchiveRowsAndSheetTargets(t *testing.T) {
	withFEGlobals(t)
	one := []FileEntry{{Name: "a.zip", Path: "/card/Downloads/a.zip"}}
	two := append(one, FileEntry{Name: "b.tar.gz", Path: "/card/Downloads/b.tar.gz"})
	mixed := append(two, FileEntry{Name: "notes.txt", Path: "/card/Downloads/notes.txt"}, FileEntry{Name: "dir.zip", Path: "/card/dir.zip", IsDir: true})
	if got := feArchiveTargets(mixed); len(got) != 2 {
		t.Fatalf("archive targets = %d, want 2 (no text file, no folder)", len(got))
	}
	if a := feArchiveMainAction(one); a.Kind != feActArchive || !a.Enabled || a.Label != "Extract…" {
		t.Fatalf("one archive: %+v", a)
	}
	if a := feArchiveMainAction(two); !a.Enabled || a.Detail != "2 archives" {
		t.Fatalf("two archives: %+v", a)
	}
	if !feExtractActive() {
		if a := feArchiveMainAction(nil); a.Enabled {
			t.Fatalf("no archive: %+v", a)
		}
	}
	rom := FileEntry{Name: "game.zip", Path: filepath.Join("Roms", "GBA", "game.zip")}
	if feArchiveOpensSheet(rom) {
		t.Fatal("a zipped game in a console folder must start, not open the sheet")
	}
	if !feArchiveOpensSheet(FileEntry{Name: "pack.zip", Path: filepath.Join("Downloads", "pack.zip")}) {
		t.Fatal("an archive in Downloads must open the sheet")
	}
	if feArchiveOpensSheet(FileEntry{Name: "x.zip", Path: "x.zip", IsDir: true}) {
		t.Fatal("a folder is never an archive")
	}
}

func TestFEExtractQueueRunsAndReports(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "queued.zip")
	feZipTestFile(t, archive, feZipTestEntry{name: "only.txt", body: "q"})
	_ = feExtractTakeOutcomes()
	place, err := feExtractEnqueue(feExtractJob{Archive: archive, Dest: dir, Mode: feExtractSmart})
	if err != nil || place < 1 {
		t.Fatalf("enqueue: place %d, err %v", place, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var outcomes []feExtractOutcome
	for len(outcomes) == 0 && time.Now().Before(deadline) {
		outcomes = append(outcomes, feExtractTakeOutcomes()...)
		time.Sleep(10 * time.Millisecond)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Err != nil || o.Target != filepath.Join(dir, "only.txt") {
		t.Fatalf("outcome: target %q, err %v", o.Target, o.Err)
	}
	msg, _ := feExtractOutcomeMessage(o)
	if !strings.Contains(msg, "queued.zip") || !strings.Contains(msg, "only.txt") {
		t.Errorf("toast = %q", msg)
	}
	// The outcome and the end of the job are one step in the worker.
	if feExtractPlace(archive) != 0 {
		t.Error("a reported job is still in the queue")
	}
}

func TestFEExtractOutcomeMessages(t *testing.T) {
	job := feExtractJob{Archive: filepath.Join("card", "pack.zip"), Dest: "card", Mode: feExtractSmart}
	cases := []struct {
		o    feExtractOutcome
		want string
	}{
		{feExtractOutcome{Job: job, Target: filepath.Join("card", "Game"), IsDir: true, Stats: feExtractStats{Files: 3}}, "→ Game/ · 3 files"},
		{feExtractOutcome{Job: job, Err: context.Canceled}, "Stopped extracting pack.zip"},
		{feExtractOutcome{Job: job, Err: errFeArchiveEncrypted}, "password-protected"},
		{feExtractOutcome{Job: job, Err: feNoSpaceError{need: 3 << 30, free: 1 << 30}}, "needs 3.0 GiB and 1.0 GiB is free"},
		{feExtractOutcome{Job: job, Err: errBadToolArchive}, "damaged or incomplete"},
		{feExtractOutcome{Job: job, Target: "x", Stats: feExtractStats{Files: 1, Skips: feExtractSkips{Links: 2, Junk: 5}}}, "2 links skipped"},
	}
	for _, c := range cases {
		if got, _ := feExtractOutcomeMessage(c.o); !strings.Contains(got, c.want) {
			t.Errorf("message %q does not say %q", got, c.want)
		}
	}
	if got, _ := feExtractOutcomeMessage(feExtractOutcome{Job: job, Target: "x", Stats: feExtractStats{Skips: feExtractSkips{Junk: 3}}}); strings.Contains(got, "skipped") {
		t.Errorf("macOS metadata should be dropped quietly: %q", got)
	}
}

func TestFEZipPrecheckRefusesTooManyEntriesUpFront(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "five.zip")
	feZipTestFile(t, archive,
		feZipTestEntry{name: "1", body: "1"}, feZipTestEntry{name: "2", body: "2"}, feZipTestEntry{name: "3", body: "3"},
		feZipTestEntry{name: "4", body: "4"}, feZipTestEntry{name: "5", body: "5"},
	)
	if err := feZipPrecheck(archive); err != nil {
		t.Fatalf("a small zip was refused: %v", err)
	}
	prev := feExtractMaxEntries
	feExtractMaxEntries = 3
	t.Cleanup(func() { feExtractMaxEntries = prev })
	if err := feZipPrecheck(archive); !errors.Is(err, errFeTooMany) {
		t.Fatalf("err = %v, want errFeTooMany before the directory is read", err)
	}
	// The end record may understate the count: archive/zip reads headers
	// until one is not a header, and so does the precheck.
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	eocd := len(data) - 22
	if !bytes.Equal(data[eocd:eocd+4], []byte("PK\x05\x06")) {
		t.Fatal("the test zip does not end in its end record")
	}
	data[eocd+8], data[eocd+9], data[eocd+10], data[eocd+11] = 1, 0, 1, 0
	lying := filepath.Join(dir, "lying.zip")
	if err := os.WriteFile(lying, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := feZipPrecheck(lying); !errors.Is(err, errFeTooMany) {
		t.Fatalf("a zip that says it holds one entry but holds five: err = %v, want errFeTooMany", err)
	}
	notZip := filepath.Join(dir, "text.zip")
	feWriteTestFile(t, notZip, "plain text, no zip records at all")
	if err := feZipPrecheck(notZip); err != nil {
		t.Fatalf("the precheck must leave a non-zip to archive/zip: %v", err)
	}
}

// feXzTestBytes compresses data as .xz with an LZMA2 dictionary of dictCap
// bytes (0: the writer's 8 MiB default).
func feXzTestBytes(t *testing.T, data []byte, dictCap int) []byte {
	t.Helper()
	var buf bytes.Buffer
	xw, err := xz.WriterConfig{DictCap: dictCap}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFEXzDictionaryLimit(t *testing.T) {
	data := feXzTestBytes(t, bytes.Repeat([]byte("dictionary "), 1000), 0)
	dict, big := feXzDictTooBig(bytes.NewReader(data), int64(len(data)), 1<<20)
	if !big || dict != 8<<20 {
		t.Fatalf("the default 8 MiB dictionary under a 1 MiB limit: %d, %v; want it refused", dict, big)
	}
	if _, big := feXzDictTooBig(bytes.NewReader(data), int64(len(data)), 1<<30); big {
		t.Fatal("the writer's default dictionary was refused under a 1 GiB limit")
	}
	if _, big := feXzDictTooBig(bytes.NewReader(data[:len(data)/2]), int64(len(data)/2), 1); big {
		t.Fatal("a cut file must be left to the decoder, not called too big")
	}
	// Two streams, the big dictionary in the first: the walk from the end has
	// to reach it through the second.
	small := feXzTestBytes(t, []byte("small"), 1<<20)
	both := append(append([]byte(nil), data...), small...)
	if dict, big := feXzDictTooBig(bytes.NewReader(both), int64(len(both)), 4<<20); !big || dict != 8<<20 {
		t.Fatalf("the first stream's dictionary was not read: %d, %v", dict, big)
	}
	if _, big := feXzDictTooBig(bytes.NewReader(small), int64(len(small)), 4<<20); big {
		t.Fatal("a 1 MiB dictionary was refused under a 4 MiB limit")
	}
	// Stream padding after the last stream is allowed.
	padded := append(append([]byte(nil), small...), 0, 0, 0, 0)
	if dict, big := feXzDictTooBig(bytes.NewReader(padded), int64(len(padded)), 1<<19); !big || dict != 1<<20 {
		t.Fatalf("a padded stream's dictionary: %d, %v; want 1 MiB refused under 512 KiB", dict, big)
	}
}

func TestFE7ZipParseListing(t *testing.T) {
	listing := strings.Join([]string{
		"7-Zip (z) 24.09 (x64) : Copyright (c) 1999-2024 Igor Pavlov : 2024-11-29",
		"",
		"Scanning the drive for archives:",
		"1 file, 4321 bytes (5 KiB)",
		"",
		"Listing archive: pack.7z",
		"",
		"--",
		"Path = pack.7z",
		"Type = 7z",
		"Physical Size = 4321",
		"",
		"----------",
		"Path = Game",
		"Size = 0",
		"Attributes = D drwxr-xr-x",
		"",
		"Path = Game/disc.bin",
		"Size = 734003200",
		"Attributes = A -rw-r--r--",
		"Encrypted = -",
		"",
		"Path = Game/link",
		"Size = 9",
		"Attributes = A lrwxrwxrwx",
		"",
		"Path = Extras",
		"Folder = +",
		"Size = 0",
		"",
		"Path = secret.txt",
		"Size = 5",
		"Encrypted = +",
		"Symbolic Link =",
		"",
		"Path = winlink",
		"Size = 0",
		"Attributes = AL",
		"",
		"Path = plain",
		"Size = 3",
		"Attributes = drwxrwxrwx",
		"",
	}, "\r\n")
	entries, err := fe7ZipParseListing(strings.NewReader(listing), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("entries = %d (%+v), want 7", len(entries), entries)
	}
	want := []fe7ZipEntry{
		{Path: "Game", Dir: true},
		{Path: "Game/disc.bin", Size: 734003200},
		{Path: "Game/link", Size: 9, Link: true},
		{Path: "Extras", Dir: true},
		{Path: "secret.txt", Size: 5, Encrypted: true},
		{Path: "winlink", Link: true},
		{Path: "plain", Size: 3, Dir: true},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], w)
		}
	}
	if _, err := fe7ZipParseListing(strings.NewReader(listing), 3); !errors.Is(err, errFe7ZipTooMany) {
		t.Fatalf("a listing past the cap: err %v, want errFe7ZipTooMany", err)
	}
	if _, err := fe7ZipParseListing(strings.NewReader("ERROR: not an archive"), 100); err == nil {
		t.Fatal("output without a listing must be an error")
	}
}

func TestFEExtractHereNeverReplacesTheArchiveItself(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "update.zip")
	feZipTestFile(t, archive, feZipTestEntry{name: "update.zip", body: "an inner zip"}, feZipTestEntry{name: "new.txt", body: "new"})
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractMerge)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.Stats.Kept != 1 || out.Stats.Replaced != 0 {
		t.Fatalf("kept %d, replaced %d; want the archive kept", out.Stats.Kept, out.Stats.Replaced)
	}
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Extract here replaced the archive it was unpacking (%v)", err)
	}
	unzipTestOnly(t, dir, "new.txt", "update.zip")
}

// feTarTestRename rewrites the name of the tar header at off (a writer refuses
// some old forms, such as a regular entry whose name ends in a slash) and
// recomputes the header's checksum.
func feTarTestRename(t *testing.T, data []byte, off int, name string) {
	t.Helper()
	hdr := data[off : off+512]
	for i := 0; i < 100; i++ {
		hdr[i] = 0
	}
	copy(hdr, name)
	for i := 148; i < 156; i++ {
		hdr[i] = ' '
	}
	sum := 0
	for _, c := range hdr {
		sum += int(c)
	}
	copy(hdr[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

func TestFEExtractTarLaterCopyWinsAndOldForms(t *testing.T) {
	dir := t.TempDir()
	data := feTarTestBytes(t,
		feTarTestEntry{hdr: tar.Header{Name: "pack", Typeflag: tar.TypeReg}},
		feTarTestEntry{hdr: tar.Header{Name: "pack/save.txt", Typeflag: tar.TypeReg}, body: "old"},
		feTarTestEntry{hdr: tar.Header{Name: "pack/save.txt", Typeflag: tar.TypeReg}, body: "newer"},
		feTarTestEntry{hdr: tar.Header{Name: "pack/cont.bin", Typeflag: tar.TypeCont}, body: "data"},
	)
	// An old tar's folder: a regular, empty entry named "pack/".
	feTarTestRename(t, data, 0, "pack/")
	archive := filepath.Join(dir, "pack.tar")
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "pack", "save.txt")); got != "newer" {
		t.Fatalf("save.txt = %q, want the later copy", got)
	}
	if out.Stats.Files != 2 || out.Stats.Skips.Duplicate != 0 {
		t.Fatalf("files %d, duplicates %d; want 2 and 0", out.Stats.Files, out.Stats.Skips.Duplicate)
	}
}

// feSheetTestState saves the sheet, the Files navigation and the scene index
// a test changes, and an empty queue around it.
func feSheetTestState(t *testing.T) {
	t.Helper()
	withFEGlobals(t)
	prevNav, prevScene := filesNav, currentSceneIndex
	t.Cleanup(func() {
		feArchiveInfoForget(feSheet.archive)
		filesNav, currentSceneIndex = prevNav, prevScene
	})
	filesNav = filesNavState{}
}

// feSheetTestWait waits for the queue to report one outcome.
func feSheetTestWait(t *testing.T) feExtractOutcome {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if outs := feExtractTakeOutcomes(); len(outs) > 0 {
			return outs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the job did not finish")
	return feExtractOutcome{}
}

func TestFEArchiveStartArmsOnceInASystemFolder(t *testing.T) {
	feSheetTestState(t)
	dir := t.TempDir()
	archive := filepath.Join(dir, "port.tar.gz")
	tarData := feTarTestBytes(t, feTarTestEntry{hdr: tar.Header{Name: "port/run.sh", Typeflag: tar.TypeReg}, body: "#!/bin/sh\n"})
	if err := os.WriteFile(archive, feGzipTestBytes(t, tarData), 0o644); err != nil {
		t.Fatal(err)
	}
	config := feTestConfig(dir)
	// No user area holds dir: it is a system folder, where changes ask twice.
	filesNav.userAreas = []string{filepath.Join(dir, "elsewhere")}
	_ = feExtractTakeOutcomes()
	feSheet = feSheetState{open: true, archive: archive, cursor: 1}
	feArchiveInspectAsync(archive, dir, 0)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ready := feArchiveInfoPeek(archive); ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	feArchiveStart(config, feExtractMerge, "Extract here")
	if feExtractPlace(archive) != 0 || feSheet.armed != "extract-here" {
		t.Fatalf("the first press: place %d, armed %q; want armed and nothing queued", feExtractPlace(archive), feSheet.armed)
	}
	feArchiveStart(config, feExtractMerge, "Extract here")
	if feSheet.armed != "" || feSheet.cursor != 0 {
		t.Fatalf("after the second press: armed %q, cursor %d; want nothing armed, cursor on the first row", feSheet.armed, feSheet.cursor)
	}
	if o := feSheetTestWait(t); o.Err != nil || o.Job.Mode != feExtractMerge {
		t.Fatalf("the second press must run Extract here: %+v", o)
	}
	if got := feReadTestFile(t, filepath.Join(dir, "port", "run.sh")); got != "#!/bin/sh\n" {
		t.Fatalf("port/run.sh = %q", got)
	}
}

func TestFEExtractReportKeepsTheCursorOnItsFile(t *testing.T) {
	feSheetTestState(t)
	dir := t.TempDir()
	feWriteTestFile(t, filepath.Join(dir, "b-notes.txt"), "keep me")
	archive := filepath.Join(dir, "c-pack.zip")
	feZipTestFile(t, archive, feZipTestEntry{name: "one.txt", body: "1"}, feZipTestEntry{name: "two.txt", body: "2"})
	config := feTestConfig(dir)
	config.Scenes = []SceneConfig{{Name: "FileExplorer"}}
	currentSceneIndex = 0
	filesNav.atLocations = false
	feListDirectory(config)
	feFocusPath(config, filepath.Join(dir, "b-notes.txt"))

	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	// The Y menu is open with Delete armed on b-notes.txt when the job ends.
	feSheet = feSheetState{open: true, armed: "delete", armedAt: time.Now()}
	feExtractReport(config, out)
	if feSheet.armed != "" {
		t.Fatalf("armed = %q after the relist; a second press must not delete whatever moved under the cursor", feSheet.armed)
	}
	if entry, ok := feFocusEntry(config); !ok || entry.Name != "b-notes.txt" {
		t.Fatalf("the cursor moved to %+v, want it kept on b-notes.txt (a new folder sorts in above it)", entry)
	}

	// With no sheet open and the cursor on the archive, it lands on the result.
	feSheet = feSheetState{}
	feFocusPath(config, archive)
	second := unzipTestRun(archive, feExtractSmart)
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	feExtractReport(config, second)
	if entry, ok := feFocusEntry(config); !ok || !filesSamePath(entry.Path, second.Target) {
		t.Fatalf("the cursor is on %+v, want the result %s", entry, second.Target)
	}
}

// feXzTestPropsAt is where the first filter's properties byte of the first
// block header of an .xz the test wrote sits.
func feXzTestPropsAt(t *testing.T, data []byte) int {
	t.Helper()
	flags := data[13]
	p := 14
	for _, present := range []bool{flags&0x40 != 0, flags&0x80 != 0} {
		if present {
			_, k := feXzVarint(data[p:])
			p += k
		}
	}
	if data[p] != 0x21 || data[p+1] != 1 {
		t.Fatalf("the test .xz does not start with an LZMA2 filter (% x)", data[12:p+3])
	}
	return p + 2
}

// feXzTestSetDict rewrites the dictionary byte of the first block header and
// its CRC, so the header is well-formed and asks for code d.
func feXzTestSetDict(t *testing.T, data []byte, d byte) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	hdrLen := (int(out[12]) + 1) * 4
	out[feXzTestPropsAt(t, out)] = d
	hdr := out[12 : 12+hdrLen]
	binary.LittleEndian.PutUint32(hdr[hdrLen-4:], crc32.ChecksumIEEE(hdr[:hdrLen-4]))
	return out
}

func TestFEXzReaderDecodesWhatTheLibraryWrites(t *testing.T) {
	payload := bytes.Repeat([]byte("xz container "), 5000)
	var all, want []byte
	for i, cfg := range []xz.WriterConfig{{}, {CheckSum: xz.CRC32}, {CheckSum: xz.SHA256}, {NoCheckSum: true}, {DictCap: 1 << 20}} {
		var buf bytes.Buffer
		w, err := cfg.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		part := append([]byte(fmt.Sprintf("stream %d: ", i)), payload[:len(payload)/(i+1)]...)
		if _, err := w.Write(part); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(buf.Bytes()), feXzMaxDict))
		if err != nil || !bytes.Equal(got, part) {
			t.Fatalf("stream %d (%+v): %d bytes, err %v; want %d bytes", i, cfg, len(got), err, len(part))
		}
		all = append(append(all, buf.Bytes()...), 0, 0, 0, 0)
		want = append(want, part...)
	}
	got, err := io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(all), feXzMaxDict))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("five streams with padding: %d bytes, err %v; want %d bytes", len(got), err, len(want))
	}
}

func TestFEXzReaderRefusesABigDictionary(t *testing.T) {
	data := feXzTestBytes(t, bytes.Repeat([]byte("x"), 10000), 0)
	_, err := io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(data), 1<<20))
	var memory feXzMemoryErr
	if !errors.As(err, &memory) || memory.dict != 8<<20 {
		t.Fatalf("an 8 MiB dictionary under a 1 MiB limit: err = %v, want feXzMemoryErr for 8 MiB", err)
	}
	// A well-formed header asking for 4 GiB is refused before any decoder
	// exists for it.
	_, err = io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(feXzTestSetDict(t, data, 40)), feXzMaxDict))
	if !errors.As(err, &memory) || memory.dict != 0xffffffff {
		t.Fatalf("a 4 GiB dictionary: err = %v, want feXzMemoryErr", err)
	}
	if !errors.Is(err, errBadToolArchive) {
		t.Fatal("the refusal must read as a damaged archive to the readers above it")
	}
	if msg := feExtractFriendlyError(err, feExtractStats{}); !strings.Contains(msg, "memory") {
		t.Fatalf("the toast does not say why: %q", msg)
	}
}

func TestFEXzReaderCatchesDamage(t *testing.T) {
	data := feXzTestBytes(t, bytes.Repeat([]byte("checked "), 3000), 1<<20)
	cases := map[string][]byte{
		"cut short":        data[:len(data)-20],
		"junk after it":    append(append([]byte(nil), data...), 0, 0, 0, 1),
		"bad stream magic": append([]byte{0}, data[1:]...),
		"bad header CRC":   append(append([]byte(nil), data[:14]...), append([]byte{data[14] ^ 1}, data[15:]...)...),
	}
	// The check sits just before the index: 8 bytes of CRC64, then the
	// index (indicator 0x00) — flip the first byte of the check.
	checked := append([]byte(nil), data...)
	idx := len(checked) - 12 - (int(binary.LittleEndian.Uint32(checked[len(checked)-8:len(checked)-4]))+1)*4
	checked[idx-8] ^= 0xff
	cases["bad check"] = checked
	for name, bad := range cases {
		_, err := io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(bad), feXzMaxDict))
		if !errors.Is(err, errBadToolArchive) {
			t.Errorf("%s: err = %v, want a damaged archive", name, err)
		}
	}
	if _, err := io.ReadAll(newFeXzReader(context.Background(), bytes.NewReader(nil), feXzMaxDict)); !errors.Is(err, errBadToolArchive) {
		t.Errorf("an empty file: err = %v, want a damaged archive", err)
	}
}
