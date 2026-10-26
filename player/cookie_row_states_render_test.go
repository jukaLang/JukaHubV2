package main

// cookie_row_states_render_test.go — the Settings → General cookie row as the
// user meets it, in each state it can hold. The value is substituted by the
// page itself; these renders pin that a state's date label survives the draw
// (the scene-entry re-seed must not clobber it), that the row paints its
// label rather than an empty cell, and — with JUKAHUB_COOKIE_ROW_DUMP
// pointing at a directory — write each frame plus one stacked strip of the
// five labels, doubled so a 360px cell's text is legible without a zoom.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cookieRowStates are the states the row's value can read, in the order the
// visual pass wants them: the shipped default with no session, the default
// with one, a window on its own (what the row reads right after Forget), the
// documented value, and the widest stop with a single-digit day.
var cookieRowStates = []struct {
	slug  string
	value string
}{
	{"keep", "Keep"},
	{"keep_since", "Keep · since Oct 12"},
	{"30d", "30d"},
	{"30d_since", "30d · since Oct 12"},
	{"90d_since", "90d · since Sep 5"},
}

func TestCookieRowRendersInEveryState(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := widgetRenderConfig(t)
	row := cookieAgeRow(t, config)
	dumpDir := os.Getenv("JUKAHUB_COOKIE_ROW_DUMP")
	if dumpDir != "" {
		if err := os.MkdirAll(dumpDir, 0o755); err != nil {
			t.Fatalf("dump dir: %v", err)
		}
	}

	strips := make([]*image.RGBA, 0, len(cookieRowStates))
	for _, state := range cookieRowStates {
		config.Variables.Custom[cookieRowLabelKey] = state.value
		f, _ := renderNamedScene(t, config, "SettingsGeneral")

		// The value the page draws is the state's own: nothing between the
		// set and the screen may have re-seeded the row from the jar.
		if got := substituteVars(row.Text, config.Variables.Custom); !strings.Contains(got, state.value) {
			t.Errorf("%s: row reads %q, want it to carry %q", state.slug, got, state.value)
		}
		// The row painted a label rather than an empty cell.
		if bright := brightPixelsIn(f, *row, config); bright < 40 {
			t.Errorf("%s: the row drew %d bright pixels, want its label on screen", state.slug, bright)
		}

		if dumpDir == "" {
			continue
		}
		path := filepath.Join(dumpDir, "cookie_row_"+state.slug+".png")
		if err := writeFramePNG(path, f.buf); err != nil {
			t.Errorf("%s: write frame: %v", state.slug, err)
			continue
		}
		t.Log(path)
		strips = append(strips, cookieRowStrip(t, f, row))
	}
	if dumpDir != "" && len(strips) == len(cookieRowStates) {
		path := filepath.Join(dumpDir, "cookie_row_states.png")
		if err := writeStackedPNG(path, strips); err != nil {
			t.Errorf("write the stacked strip: %v", err)
		} else {
			t.Log(path)
		}
	}

	// The helper line under the Screensaver button is ~616px wide — wider
	// than the space beside this row — so it used to run under the row at
	// y576 and was cut mid-word by the button. It now sits below the row's
	// band; pin that clearance geometrically, on every canvas, rather than
	// trusting two coordinates to travel together when either slot moves.
	var helper *Element
	for i := range settingsGeneralScene(config).Elements {
		if strings.Contains(settingsGeneralScene(config).Elements[i].Text, "Idle time before") {
			helper = &settingsGeneralScene(config).Elements[i]
			break
		}
	}
	if helper == nil {
		t.Fatal("the page has no screensaver helper line to keep clear of the row")
	}
	font, _ := getCachedFont(config, helper.Font)
	if font == nil {
		t.Skip("no font available for the helper line's geometry")
	}
	textW, _, _ := font.SizeUTF8(substituteVars(helper.Text, config.Variables.Custom))
	rowW, rowH := resolveElementDimensions(*row, 320, 48)
	helperRight, helperBottom := helper.X+int32(textW), helper.Y+int32(font.Height())
	if helper.X < row.X+rowW && row.X < helperRight &&
		helper.Y < row.Y+rowH && row.Y < helperBottom {
		t.Errorf("the screensaver helper (%d..%d x %d..%d) runs under the cookie row (%d..%d x %d..%d)",
			helper.X, helperRight, helper.Y, helperBottom,
			row.X, row.X+rowW, row.Y, row.Y+rowH)
	}
}

// cookieRowStrip crops the row's own band out of a rendered frame and doubles
// it, so the label a 360px cell draws is readable without a zoom. The margins
// take in the cell's border and a little of the page around it, which is what
// tells the crop apart from a screenshot of text.
func cookieRowStrip(t *testing.T, f *probeFrame, row *Element) *image.RGBA {
	t.Helper()
	w := getElementWidth(*row, 0)
	if w <= 0 {
		t.Fatalf("the row reports a %dpx box to crop", w)
	}
	h := getElementHeight(*row, probeH)
	if h <= 0 {
		h = 48
	}
	x0, y0 := int(row.X)-16, int(row.Y)-8
	x1, y1 := int(row.X+w)+16, int(row.Y+h)+8
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
// like" without five near-identical pages to flip between.
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
