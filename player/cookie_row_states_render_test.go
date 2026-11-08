package main

// cookie_row_states_render_test.go — the Settings cookie row as the user meets
// it, in each state it can hold. Settings is the hub now (setui.go): the row is
// the "Keep browser sign-ins" carousel of Privacy & data, its value the window
// in force and its line under the label the day the saved session began. These
// renders pin that every window, with and without a session, draws whole on
// both canvases — the old 360px cell is what cut the date off — and, with
// JUKAHUB_COOKIE_ROW_DUMP pointing at a directory, write each frame plus one
// stacked strip of the row in every state, doubled so the text is legible
// without a zoom.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestCookieRowRendersInEveryState(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	setSkipUnlessDefaultTextSize(t)
	useTempBrowserCookiePath(t)
	config := setRenderConfig(t)
	dumpDir := os.Getenv("JUKAHUB_COOKIE_ROW_DUMP")
	if dumpDir != "" {
		if err := os.MkdirAll(dumpDir, 0o755); err != nil {
			t.Fatalf("dump dir: %v", err)
		}
	}

	p := setNewHubProbe(t)
	var strips []*image.RGBA
	for _, session := range []bool{false, true} {
		if session {
			browserCookies().SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
				{Name: "session", Value: "abc", Path: "/"},
			})
		}
		for _, days := range cookieRetentionSteps {
			config.Variables.Custom["cookieRetentionDays"] = days
			want := cookieRetentionLabel(days)
			if days <= 0 {
				want = "Until I forget them"
			}
			for _, w := range setTestCanvases {
				p.open(config, "privacy", w, 720)
				i := p.assertRowWhole("cookies")
				state := strconv.Itoa(days) + "d"
				if session {
					state += "_session"
				}
				if got := setHub.views[i].value; got != want {
					t.Errorf("%dpx %s: the carousel shows %q, want %q", w, state, got, want)
				}
				detail := setHub.views[i].detail
				if dated := strings.HasPrefix(detail, "The saved session began "); dated != session {
					t.Errorf("%dpx %s: the line under the row reads %q", w, state, detail)
				}

				if dumpDir == "" {
					continue
				}
				path := filepath.Join(dumpDir, "cookie_row_"+state+"_"+strconv.Itoa(int(w))+".png")
				if err := writeFramePNG(path, p.frame.buf); err != nil {
					t.Errorf("%s: write frame: %v", state, err)
					continue
				}
				t.Log(path)
				strips = append(strips, cookieRowStrip(p.frame, setHub.rowRects[i]))
			}
		}
	}
	if dumpDir != "" && len(strips) > 0 {
		path := filepath.Join(dumpDir, "cookie_row_states.png")
		if err := writeStackedPNG(path, strips); err != nil {
			t.Errorf("write the stacked strip: %v", err)
		} else {
			t.Log(path)
		}
	}
}

// cookieRowStrip crops the row's own band out of a rendered frame and doubles
// it, so the row's text is readable without a zoom. The margins take in the
// row's glass edge and a little of the page around it, which is what tells the
// crop apart from a screenshot of text.
func cookieRowStrip(f *probeFrame, row sdl.Rect) *image.RGBA {
	x0, y0 := int(row.X)-16, int(row.Y)-8
	x1, y1 := int(row.X+row.W)+16, int(row.Y+row.H)+8
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > int(probeW) {
		x1 = int(probeW)
	}
	if y1 > int(probeH) {
		y1 = int(probeH)
	}
	if x1 <= x0 || y1 <= y0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}

	const scale = 2
	out := image.NewRGBA(image.Rect(0, 0, (x1-x0)*scale, (y1-y0)*scale))
	for y := 0; y < (y1-y0)*scale; y++ {
		srcY := y0 + y/scale
		for x := 0; x < (x1-x0)*scale; x++ {
			srcX := x0 + x/scale
			i := (int(probeW)*srcY + srcX) * 4
			out.Set(x, y, color.RGBA{R: f.buf[i+2], G: f.buf[i+1], B: f.buf[i], A: 255})
		}
	}
	return out
}

// writeStackedPNG lays the strips one under the other with a dark rule
// between, in state order, so one image answers "what does each state look
// like" without a page per state to flip between.
func writeStackedPNG(path string, strips []*image.RGBA) error {
	const sep = 4
	width, height := 0, 0
	for _, s := range strips {
		if s.Bounds().Dx() > width {
			width = s.Bounds().Dx()
		}
		height += s.Bounds().Dy() + sep
	}
	comp := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(comp, comp.Bounds(), &image.Uniform{C: color.RGBA{R: 24, G: 26, B: 32, A: 255}}, image.Point{}, draw.Src)
	y := 0
	for _, s := range strips {
		r := image.Rect(0, y, s.Bounds().Dx(), y+s.Bounds().Dy())
		draw.Draw(comp, r, s, image.Point{}, draw.Over)
		y += s.Bounds().Dy() + sep
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, comp)
}
