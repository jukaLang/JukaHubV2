package main

// The disk chart used to stack "used" and "total" under the big percentage
// inside the donut, where the three lines overprinted each other on the
// handheld. These are pure-geometry checks for the new placement: the numbers
// live in the bottom-left corner, clear of the ring and of the percentage.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestDiskPieUsageSitsBelowThePercentage(t *testing.T) {
	element := Element{X: 390, Y: 72}
	elemW := getElementWidth(element, 500)
	elemH := getElementHeight(element, 400)
	lineH := int32(24)

	usageX, usageY := diskPieUsageLayout(element, elemH, lineH)
	if usageX != element.X+24 {
		t.Errorf("usage figures x=%d, want the bottom-left inset (%d)", usageX, element.X+24)
	}

	// Percentage: centred in the card, roughly the height of the big font.
	cx, cy := element.X+elemW/2, element.Y+elemH/2
	const bigHalfH = int32(28)
	if usageX >= cx-bigHalfH {
		t.Errorf("usage figures at x=%d intrude into the percentage column (centre %d)", usageX, cx)
	}
	if usageY <= cy+bigHalfH+8 {
		t.Errorf("usage figures y=%d overlap the centred percentage (bottom %d)", usageY, cy+bigHalfH)
	}

	// The ring must stop above the figures.
	ringBottom := cy + diskPieRadius(elemW, elemH)
	if ringBottom > usageY-8 {
		t.Errorf("ring bottom %d reaches the usage figures at %d", ringBottom, usageY)
	}

	// Both lines (and their gap) stay inside the card.
	if bottom := usageY + lineH + 6 + lineH; bottom > element.Y+elemH-4 {
		t.Errorf("usage block ends at %d, outside the card (%d)", bottom, element.Y+elemH)
	}
}

// TestDiskPieRendersFiguresBottomLeft checks the real pixels: the usage line
// must be painted in the bottom-left corner, below the donut, and nothing may
// be painted over the centred percentage.
func TestDiskPieRendersFiguresBottomLeft(t *testing.T) {
	_ = newProbeFrame(t)
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer r.Destroy()
	withScreen(t, probeW, probeH)

	diskPieMutex.Lock()
	prevPie := diskPie
	diskPie = diskPieData{Total: 16 << 30, Used: 10 << 30, Pct: 62}
	diskPieMutex.Unlock()
	t.Cleanup(func() {
		diskPieMutex.Lock()
		diskPie = prevPie
		diskPieMutex.Unlock()
		ClearTextCache()
	})

	element := Element{X: 390, Y: 72, Width: "500", Height: "400"}
	r.SetDrawColor(9, 14, 25, 255)
	r.Clear()
	renderDiskPieChart(r, &Config{}, element)

	buf := make([]byte, probeW*probeH*4)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	at := func(x, y int32) sdl.Color {
		i := (y*probeW + x) * 4
		return sdl.Color{B: buf[i], G: buf[i+1], R: buf[i+2], A: buf[i+3]}
	}

	// Use the real font metrics: the render call places the figures with the
	// height of the theme's "small" font.
	font, _ := getCachedFont(&Config{}, "small")
	lineH := int32(24)
	if font != nil {
		lineH = int32(font.Height())
	}
	usageX, usageY := diskPieUsageLayout(element, 400, lineH)
	// Flat card fill sampled clear of the ring and of any text.
	ref := at(element.X+480, element.Y+200)
	different := func(x0, y0, x1, y1 int32) int {
		n := 0
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				c := at(x, y)
				if c.R != ref.R || c.G != ref.G || c.B != ref.B {
					n++
				}
			}
		}
		return n
	}

	if n := different(usageX, usageY, usageX+260, usageY+lineH); n == 0 {
		t.Fatal("no usage figures painted in the bottom-left corner")
	}
	// The percentage must still be centred in the ring.
	cx, cy := element.X+250, element.Y+200
	if n := different(cx-40, cy-20, cx+40, cy+20); n == 0 {
		t.Fatal("percentage not painted in the centre of the ring")
	}
	// And the ring has to stop above the figures.
	if ringBottom := cy + diskPieRadius(500, 400); ringBottom > usageY-2 {
		t.Fatalf("ring bottom %d reaches the usage figures at %d", ringBottom, usageY)
	}
}

func TestDiskPieRadiusShrinksForNarrowCards(t *testing.T) {
	if r := diskPieRadius(200, 400); r > 200/2-20 {
		t.Errorf("radius %d ignores the card width", r)
	}
	if r := diskPieRadius(500, 140); r != 20 {
		t.Errorf("radius on a short card = %d, want the 20px floor", r)
	}
	if r := diskPieRadius(500, 400); r != 112 {
		t.Errorf("radius on the standard card = %d, want 112 (112 leaves room for the figures)", r)
	}
}
