package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lookTestPNG writes a w×h PNG to path.
func lookTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A community pack.json is outside data: every theme-pack field that walks out
// of the pack, names something unknown or is too big is dropped and named,
// and the good ones are normalised.
func TestLookpackManifestHostile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evil")
	lookTestPNG(t, filepath.Join(dir, "logo.png"), 4, 4)
	big := filepath.Join(dir, "huge.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(lookpackMaxBootLogoBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	manifest := `{"id":"evil","name":"Evil","bootLogo":"../../etc/passwd","iconPack":"../Icons/x",
		"consoleNames":"klingon","menuColour":"#ff0000","preview":"huge.png"}`
	if err := os.WriteFile(filepath.Join(dir, "pack.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := xpackLoadEntry(dir, xpackSourceUser)
	if err != nil {
		t.Fatal(err)
	}
	l := e.Manifest.lookpackFields
	if l.BootLogo != "" || l.bootLogoPath != "" || l.IconPack != "" || l.ConsoleNames != "" || l.MenuColour != "" || l.previewPath != "" {
		t.Fatalf("hostile fields kept: %+v", l)
	}
	if len(e.Problems) != 5 {
		t.Fatalf("want 5 problems, got %q", e.Problems)
	}

	good := `{"id":"good","bootLogo":"logo.png","iconPack":"Burst!","consoleNames":"JP","menuColor":"Coral","startupSound":false}`
	if err := os.WriteFile(filepath.Join(dir, "pack.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if e, err = xpackLoadEntry(dir, xpackSourceUser); err != nil {
		t.Fatal(err)
	}
	l = e.Manifest.lookpackFields
	if l.bootLogoPath == "" || l.IconPack != "Burst!" || l.ConsoleNames != consoleNamesJapan || l.MenuColour != "coral" ||
		l.StartupSound == nil || *l.StartupSound || len(e.Problems) != 0 {
		t.Fatalf("good fields: %+v problems %q", l, e.Problems)
	}

	// A manifest past the size cap is refused before it is parsed.
	huge := `{"id":"x","name":"` + strings.Repeat("a", xpackMaxManifestBytes) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "pack.json"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := xpackLoadEntry(dir, xpackSourceUser); err == nil {
		t.Fatal("oversized pack.json was read")
	}
}

// A picture that declares a giant size is refused from its header, before a
// pixel is allocated.
func TestLookDecodeFitRefusesGiantHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "giant.png")
	lookTestPNG(t, path, 1, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// IHDR: length(4) "IHDR"(4) width(4) height(4) …, CRC over type+data.
	binary.BigEndian.PutUint32(data[16:20], 20000)
	binary.BigEndian.PutUint32(data[20:24], 20000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lookDecodeFit(context.Background(), path, 64, 64, 0); err == nil {
		t.Fatal("a 20000x20000 header was decoded")
	}
	lookTestPNG(t, path, 200, 100)
	pic, err := lookDecodeFit(context.Background(), path, 64, 64, 0)
	if err != nil || pic.Rect.Dx() != 64 || pic.Rect.Dy() != 32 {
		t.Fatalf("fit: %v %v", err, pic)
	}
}

// An icon pack in CrossMix's layout (Emus/ inside) maps file names to glyphs
// case-insensitively, prefers the main console of a shared glyph and skips
// what is not a console picture.
func TestIconPackReadCrossMixLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Burst!")
	for _, name := range []string{"SEGACD.png", "MD.png", "fc.PNG", "FDS.png", "Game Boy Advance.png", "preview.png"} {
		lookTestPNG(t, filepath.Join(dir, "Emus", name), 2, 2)
	}
	if err := os.WriteFile(filepath.Join(dir, "Emus", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.json"), []byte(strings.Repeat(" ", iconPackMaxManifest+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	info, files := iconPackRead(dir, "Burst!")
	if info.Name != "Burst!" || info.Count != 3 {
		t.Fatalf("info %+v files %v", info, files)
	}
	if filepath.Base(files["emu-genesis"]) != "MD.png" || filepath.Base(files["emu-nes"]) != "fc.PNG" ||
		filepath.Base(files["emu-gba"]) != "Game Boy Advance.png" {
		t.Fatalf("files %v", files)
	}
	if iconPackCleanName("../Icons") != "" || iconPackCleanName(".hidden") != "" || iconPackCleanName("Burst!") != "Burst!" {
		t.Fatal("pack names that leave Icons/ must be refused")
	}
}
