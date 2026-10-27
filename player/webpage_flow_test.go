package main

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// webTestRowOf finds the grid row whose runs contain word, or -1.
func webTestRowOf(layout *webLayout, word string) int {
	for i, row := range layout.Rows {
		for _, run := range row.Runs {
			if strings.Contains(run.Text, word) {
				return i
			}
		}
	}
	return -1
}

// webTestRunX returns the x of the run containing word on a row, or -1.
func webTestRunX(layout *webLayout, row int, word string) int32 {
	if row < 0 || row >= len(layout.Rows) {
		return -1
	}
	for _, run := range layout.Rows[row].Runs {
		if strings.Contains(run.Text, word) {
			return run.X
		}
	}
	return -1
}

// A flex row puts its items side by side instead of stacking them, which is
// what makes a card grid or a icon+label row look like the page.
func TestWebLayoutFlexRowPlacesItemsSideBySide(t *testing.T) {
	doc := parseWebPage(`<body>
		<div style="display:flex; gap:20px">
			<div style="flex:1"><h3>Left card</h3><p>left body</p></div>
			<div style="flex:1"><h3>Right card</h3><p>right body</p></div>
		</div>
		<p>after rows</p>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 800)
	if layout == nil {
		t.Fatal("no layout")
	}

	// Runs are measured word by word, so the items are found by one word each.
	leftRow := webTestRowOf(layout, "Left")
	rightRow := webTestRowOf(layout, "Right")
	if leftRow < 0 || rightRow < 0 {
		t.Fatalf("missing items: left=%d right=%d", leftRow, rightRow)
	}
	if leftRow != rightRow {
		t.Fatalf("flex items landed on different rows (%d and %d); they were stacked", leftRow, rightRow)
	}
	leftX := webTestRunX(layout, leftRow, "Left")
	rightX := webTestRunX(layout, rightRow, "Right")
	if rightX <= leftX {
		t.Fatalf("right item x=%d is not right of left item x=%d", rightX, leftX)
	}
	if leftX < 0 || rightX > 800 {
		t.Fatalf("items escape the page: left=%d right=%d", leftX, rightX)
	}
	after := webTestRowOf(layout, "after")
	if after <= leftRow {
		t.Fatalf("content after the flex row landed on row %d, inside it (starts at %d)", after, leftRow)
	}
}

// flex-direction: column keeps the items stacked but honours the gap, so the
// container still reads as a group.
func TestWebLayoutFlexColumnKeepsItemsStacked(t *testing.T) {
	doc := parseWebPage(`<body>
		<div style="display:flex; flex-direction:column; gap:40px">
			<div>First</div>
			<div>Second</div>
		</div>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	first := webTestRowOf(layout, "First")
	second := webTestRowOf(layout, "Second")
	if first < 0 || second < 0 {
		t.Fatalf("missing items: first=%d second=%d", first, second)
	}
	if second <= first {
		t.Fatalf("column items are not stacked: first=%d second=%d", first, second)
	}
	if gap := second - first - 1; gap < 1 {
		t.Errorf("column gap produced %d blank rows", gap)
	}
}

// position:absolute takes a block out of the flow and into the overlay layer,
// and z-index orders the layers.
func TestWebLayoutAbsoluteBlocksBecomeZOrderedLayers(t *testing.T) {
	doc := parseWebPage(`<body>
		<p>flow text</p>
		<div style="position:absolute; top:24px; left:16px; z-index:9; width:200px">Top layer</div>
		<div style="position:absolute; top:8px; left:8px; z-index:1; width:200px">Under layer</div>
		<p>more flow</p>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	if got := webTestRowOf(layout, "Top layer"); got >= 0 {
		t.Error("an absolutely positioned block stayed in the text flow")
	}
	if len(layout.Floats) != 2 {
		t.Fatalf("%d overlay layers measured, want 2", len(layout.Floats))
	}
	if layout.Floats[0].Z > layout.Floats[1].Z {
		t.Errorf("layers are not in z order: %d then %d", layout.Floats[0].Z, layout.Floats[1].Z)
	}
	if layout.Floats[0].Z != 1 || layout.Floats[1].Z != 9 {
		t.Errorf("layer z-index went missing: %d and %d", layout.Floats[0].Z, layout.Floats[1].Z)
	}
	for _, layer := range layout.Floats {
		if len(layer.Rows) == 0 {
			t.Fatal("overlay layer has no text rows")
		}
		if layer.Y < 0 {
			t.Errorf("layer y=%d is above the page", layer.Y)
		}
	}
	if layout.Floats[1].Y <= layout.Floats[0].Y {
		t.Errorf("top offset ignored: under y=%d top y=%d", layout.Floats[0].Y, layout.Floats[1].Y)
	}
}

// z-index on ordinary blocks orders their backgrounds, so a card can paint
// over the one before it.
func TestWebLayoutBackgroundsCarryZIndex(t *testing.T) {
	doc := parseWebPage(`<body>
		<div style="background:#cccccc; z-index:7">card</div>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	found := false
	for _, r := range layout.Rects {
		if r.Rect.H > 4 && r.Color.R > 150 && r.Z == 7 {
			found = true
		}
	}
	if !found {
		t.Error("the card background did not carry its z-index")
	}
	for i := 1; i < len(layout.Rects); i++ {
		if layout.Rects[i-1].Z > layout.Rects[i].Z {
			t.Fatalf("rects are not painted in z order at %d: %d then %d", i, layout.Rects[i-1].Z, layout.Rects[i].Z)
		}
	}
}

// The overlay layer has to reach the screen: measuring it is not enough.
func TestDrawWebDocumentPaintsOverlayLayers(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	doc := parseWebPage(`<body>
		<p>page text under the layer</p>
		<div style="position:absolute; top:0; left:0; width:220px; z-index:3; background:#ff0000">badge</div>
	</body>`, "", "")
	config := layoutTestConfig(t)
	withScreen(t, probeW, probeH)
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 700)
	if layout == nil || len(layout.Floats) == 0 {
		t.Fatal("no overlay layer was laid out")
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	rect := sdl.Rect{X: 10, Y: 10, W: 700, H: 480}
	drawWebDocument(renderer, config, doc, layout, rect, 0)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	red, outside := 0, 0
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			i := (int(y)*int(probeW) + int(x)) * 4
			b, g, r := frame.buf[i], frame.buf[i+1], frame.buf[i+2]
			if x < rect.X || x >= rect.X+rect.W || y < rect.Y || y >= rect.Y+rect.H {
				if r|g|b != 0 {
					outside++
				}
				continue
			}
			if r > 200 && g < 60 && b < 60 {
				red++
			}
		}
	}
	if red < 2000 {
		t.Errorf("the overlay layer painted only %d pixels", red)
	}
	if outside != 0 {
		t.Errorf("%d pixels were painted outside the page rect", outside)
	}
}
