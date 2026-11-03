package main

import (
	"math"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// The shipped wide and compact cards must fit the title, percentage and all
// three storage figures, including drives much larger than a handheld card.
func TestDiskPieLayoutAndLabelsFit(t *testing.T) {
	newProbeFrame(t)
	for _, tc := range []struct {
		name          string
		screen, width int32
	}{
		{"wide", 1280, 500},
		{"compact", 960, 375},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withScreen(t, tc.screen, 720)
			cfg := &Config{Variables: Variables{FontSizes: map[string]int{"big": 52}}}
			title, _ := getCachedFont(cfg, "medium")
			font, _ := getCachedFont(cfg, "small")
			if title == nil || font == nil {
				t.Fatal("disk chart fonts unavailable")
			}
			elem := Element{X: 200, Y: 72}
			layout := layoutDiskPie(elem, tc.width, 400, int32(title.Height()), int32(font.Height()))
			if layout.radius <= 0 || layout.cy-layout.radius < elem.Y+12+int32(title.Height()) ||
				layout.cy+layout.radius >= layout.usageY {
				t.Fatalf("title, ring and rows overlap: %+v", layout)
			}
			if layout.cx-layout.radius < elem.X || layout.cx+layout.radius >= elem.X+tc.width {
				t.Fatalf("ring escapes card: %+v", layout)
			}
			if end := layout.usageY + 3*layout.lineHeight + 8; end > elem.Y+400-16 {
				t.Fatalf("usage rows extend below card: %d", end)
			}
			for _, line := range diskPieUsageLines(487059*1024*1024, 410360*1024*1024) {
				w, _, _ := font.SizeUTF8(line)
				if int32(w) > layout.usageWidth {
					t.Errorf("%q needs %d pixels, only %d available", line, w, layout.usageWidth)
				}
			}
			for _, text := range []string{"84%", "100%"} {
				inner := layout.radius * 2 / 3
				pctFont := diskPiePercentFont(cfg, text, inner)
				if pctFont == nil {
					t.Fatalf("%s has no font that fits the ring", text)
				}
				w, h, _ := pctFont.SizeUTF8(text)
				if math.Hypot(float64(w)/2, float64(h)/2) > float64(inner-4) {
					t.Errorf("%s overlaps ring", text)
				}
			}
		})
	}
	if got := diskPieUsageLines(1, 2)[1]; got != "Free 0.0 B" {
		t.Errorf("used > total underflowed: %s", got)
	}
}

// Inspect the rendered arc's interior over several sweeps: every pixel well
// inside a sector must be solid and the unfilled section must remain empty.
func TestDiskPieSectorIsSolid(t *testing.T) {
	r := probeRenderer(t)
	const cx, cy, radius, inner = int32(150), int32(150), int32(90), int32(60)
	color := sdl.Color{R: 240, G: 130, B: 25, A: 255}
	for _, fraction := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.84, 1} {
		r.SetDrawColor(0, 0, 0, 255)
		r.Clear()
		drawDiskPieSector(r, cx, cy, radius, inner, fraction, color)
		buf := make([]byte, probeW*probeH*4)
		if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels: %v", err)
		}
		for dy := -radius + 3; dy <= radius-3; dy++ {
			for dx := -radius + 3; dx <= radius-3; dx++ {
				dist := math.Hypot(float64(dx), float64(dy))
				if dist <= float64(inner+3) || dist >= float64(radius-3) {
					continue
				}
				angle := math.Atan2(float64(dx), -float64(dy))
				if angle < 0 {
					angle += 2 * math.Pi
				}
				if fraction > 0 && fraction < 1 && (angle < 0.05 || math.Abs(angle-2*math.Pi*fraction) < 0.05 || angle > 2*math.Pi-0.05) {
					continue
				}
				i := ((cy+dy)*probeW + cx + dx) * 4
				painted := buf[i+2] == color.R && buf[i+1] == color.G && buf[i] == color.B
				if painted != (fraction > 0 && (fraction >= 1 || angle < 2*math.Pi*fraction)) {
					t.Fatalf("fraction %.2f pixel (%d,%d) angle %.2f: painted=%v RGB=%d,%d,%d", fraction, dx, dy, angle, painted, buf[i+2], buf[i+1], buf[i])
				}
			}
		}
	}
}

func TestDiskPieRendersThreeRows(t *testing.T) {
	r := probeRenderer(t)
	diskPieMutex.Lock()
	prev := diskPie
	diskPie = diskPieData{Total: 487059 * 1024 * 1024, Used: 410360 * 1024 * 1024, Pct: 84}
	diskPieMutex.Unlock()
	t.Cleanup(func() { diskPieMutex.Lock(); diskPie = prev; diskPieMutex.Unlock() })
	cfg := &Config{Variables: Variables{FontSizes: map[string]int{"big": 52}}}
	elem := Element{X: 390, Y: 72, Width: "500", Height: "400"}
	r.SetDrawColor(9, 14, 25, 255)
	r.Clear()
	renderDiskPieChart(r, cfg, elem)
	buf := make([]byte, probeW*probeH*4)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels: %v", err)
	}
	title, _ := getCachedFont(cfg, "medium")
	font, _ := getCachedFont(cfg, "small")
	layout := layoutDiskPie(elem, 500, 400, int32(title.Height()), int32(font.Height()))
	for row := int32(0); row < 3; row++ {
		y0 := layout.usageY + row*(layout.lineHeight+4)
		// All text is drawn at the left; the far right is a clear panel sample.
		ref := (y0*probeW + elem.X + 480) * 4
		found := false
		for y := y0; y < y0+layout.lineHeight && !found; y++ {
			for x := layout.usageX; x < layout.usageX+250; x++ {
				i := (y*probeW + x) * 4
				if buf[i] != buf[ref] || buf[i+1] != buf[ref+1] || buf[i+2] != buf[ref+2] {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("usage row %d is blank", row)
		}
	}
}
