package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestPkgCatalogIconReachesCards(t *testing.T) {
	entries, err := pkgParseCatalog([]byte(`[
		{"name":"FTP","icon":"https://example.com/ftp.png"},
		{"name":"No icon"},
		{"name":"Invalid icon","icon":12}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	wanted := pkgArtWanted(entries)
	key := packageDirName("FTP")
	if len(wanted) != 1 || wanted[key] != "https://example.com/ftp.png" {
		t.Fatalf("wanted artwork = %#v", wanted)
	}
	remote := pkgArtPath(wanted[key])
	model := pkgBuildModel(pkgModelInput{Entries: entries, Art: map[string]string{key: remote}})
	if got := model.ByKey[key].Icon; got != remote {
		t.Errorf("registry icon on card = %q, want %q", got, remote)
	}
	model = pkgBuildModel(pkgModelInput{
		Entries: entries, Art: map[string]string{key: remote},
		Installed: map[string]bool{key: true}, Icons: map[string]string{key: "installed.png"},
	})
	if got := model.ByKey[key].Icon; got != "installed.png" {
		t.Errorf("installed icon = %q, want installed.png", got)
	}
	for _, name := range []string{"No icon", "Invalid icon"} {
		if got := model.ByKey[packageDirName(name)].Icon; got != "" {
			t.Errorf("%s should keep its monogram, got icon %q", name, got)
		}
	}
}

func TestPkgArtLoadsAllCurrentCatalogIcons(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(picture.Bytes())
	}))
	defer srv.Close()

	previousDir, previousEvents := pkgInstallDirOverride, pkgEvents
	pkgInstallDirOverride = t.TempDir()
	pkgEvents = make(chan pkgEvent, 64)
	netMon.mu.Lock()
	previousChecked, previousOnline := netMon.checked, netMon.online
	netMon.checked, netMon.online = true, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		pkgInstallDirOverride, pkgEvents = previousDir, previousEvents
		netMon.mu.Lock()
		netMon.checked, netMon.online = previousChecked, previousOnline
		netMon.mu.Unlock()
	})

	const count = 33 // the updated registry has more icons than the old 24-image cap
	wanted := make(map[string]string, count)
	for i := range count {
		wanted[fmt.Sprintf("Package_%d", i)] = fmt.Sprintf("%s/%d.png", srv.URL, i)
	}
	pkgLoadArt(context.Background(), wanted)
	got := make(map[string]string, count)
	batches := 0
	for len(pkgEvents) > 0 {
		ev := <-pkgEvents
		if ev.kind != pkgEvArt {
			t.Fatalf("unexpected event: %v", ev.kind)
		}
		batches++
		for key, path := range ev.icons {
			got[key] = path
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s icon not cached: %v", key, err)
			}
		}
	}
	if len(got) != count || batches < 2 {
		t.Fatalf("loaded %d/%d icons in %d batches", len(got), count, batches)
	}
}

// TestWebPDecoderChoice: SDL_image first, then the card's ffmpeg, else a .webp
// stays hidden (MED-37); ffmpeg is asked for exactly the size the caller's
// buffer has, and the header the pixel cap reads gives the picture's size.
func TestWebPDecoderChoice(t *testing.T) {
	for _, c := range []struct {
		sdl, ffmpeg bool
		want        webpDecoder
	}{
		{true, true, webpDecoderSDL},
		{true, false, webpDecoderSDL},
		{false, true, webpDecoderFFmpeg},
		{false, false, webpDecoderNone},
	} {
		if got := webpDecoderChoice(c.sdl, c.ffmpeg); got != c.want {
			t.Errorf("sdl=%v ffmpeg=%v: decoder %d, want %d", c.sdl, c.ffmpeg, got, c.want)
		}
	}
	path := t.TempDir() + string(os.PathSeparator) + "cover.webp"
	want := fmt.Sprint([]string{"-nostdin", "-v", "error", "-i", path, "-frames:v", "1",
		"-vf", "scale=512:384:flags=area", "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"})
	if got := fmt.Sprint(webpFFmpegArgs(path, 512, 384)); got != want {
		t.Errorf("args = %s\nwant   %s", got, want)
	}
	if got := webpFFmpegArgs("-x.webp", 1, 1); got[4] != "file:-x.webp" {
		t.Errorf("a relative name went to ffmpeg as %q", got[4])
	}
	if w, h, ok := webpImageSize(webpProbe); !ok || w != 1 || h != 1 {
		t.Errorf("probe header = %dx%d (%v), want 1x1", w, h, ok)
	}
	if _, _, ok := webpImageSize([]byte("RIFF\x00\x00\x00\x00WEBPVP8L")); ok {
		t.Error("a truncated header gave a size")
	}
}
