package main

// lookpacks_ui.go — the look packs' picture on the gallery card, and the
// staged boot logo the Boot logo row writes.
//
// A pack with a preview.png shows that picture on its card instead of the
// composed miniature (xpackDrawPreview), scaled once to the card on a worker
// through the icon packs' texture cache. A pack's boot logo is never written
// when the pack is applied: applying stages it (lookpackBootLogoKey), and the
// Boot logo row's own two-press confirm writes it through the same verified,
// backed-up install path as the JukaHub logo (bootlogo.go), with the
// picture fitted to the logo's size on black.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// lookpackDrawPreviewOr draws a pack's own preview picture in r, or the
// composed miniature while it loads, when it cannot be read, or when the pack
// has none.
func lookpackDrawPreviewOr(renderer *sdl.Renderer, c *Config, card xpackCard, r sdl.Rect) {
	if path := card.entry.Manifest.previewPath; path != "" && r.W > 0 && r.H > 0 {
		if tex, w, h := iconPackTexture(renderer, path, r.W, r.H); tex != nil {
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, glassA(8))
			_ = tex.SetAlphaMod(255)
			dst := sdl.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
			_ = renderer.Copy(tex, nil, &dst)
			return
		}
	}
	xpackDrawPreview(renderer, c, card.preview, r)
}

// lookpackStagedBootLogo is the boot logo the pack in force carries, when the
// file is still there; "" writes the JukaHub logo as before.
func lookpackStagedBootLogo() string {
	if appConfig == nil || appConfig.Variables.Custom == nil {
		return ""
	}
	path, _ := appConfig.Variables.Custom[lookpackBootLogoKey].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return path
}

// lookpackBootLogoWhat names the picture the Boot logo row would install
// (read every frame, so it only checks that one is staged).
func lookpackBootLogoWhat() string {
	if appConfig != nil && appConfig.Variables.Custom != nil {
		if path, _ := appConfig.Variables.Custom[lookpackBootLogoKey].(string); strings.TrimSpace(path) != "" {
			return "the look pack's logo"
		}
	}
	return "the JukaHub logo"
}

// lookpackBootLogoArmText is the Boot logo row's first-press toast, worded as
// the row's own was (bootlogo_test.go pins its start).
func lookpackBootLogoArmText() string {
	what := "the JukaHub boot logo"
	if lookpackStagedBootLogo() != "" {
		what = "the look pack's boot logo"
	}
	return "Press A again to install " + what + " — the current one is kept on the SD card"
}

// lookpackBootLogoPicture renders a pack's boot logo at the stock logo's size:
// the picture fitted and centred on black, opaque, in the RGBA layout
// bootLogoPicture returns. Runs on the boot logo worker.
func lookpackBootLogoPicture(info bmpInfo, path string) ([]byte, error) {
	w, h := info.Width, info.Height
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("the boot logo is %dx%d", w, h)
	}
	pic, err := lookDecodeFit(context.Background(), path, w, h, lookpackMaxBootLogoBytes)
	if err != nil {
		return nil, fmt.Errorf("the look pack's boot logo %v", err)
	}
	pw, ph := pic.Rect.Dx(), pic.Rect.Dy()
	out := make([]byte, w*h*4)
	for i := 3; i < len(out); i += 4 {
		out[i] = 255
	}
	ox, oy := (w-pw)/2, (h-ph)/2
	for y := 0; y < ph && oy+y < h; y++ {
		for x := 0; x < pw && ox+x < w; x++ {
			p := pic.Pix[y*pic.Stride+x*4:][:4]
			a := uint32(p[3])
			d := out[((oy+y)*w+ox+x)*4:][:3]
			d[0] = uint8((uint32(p[0])*a + 127) / 255)
			d[1] = uint8((uint32(p[1])*a + 127) / 255)
			d[2] = uint8((uint32(p[2])*a + 127) / 255)
		}
	}
	return out, nil
}
