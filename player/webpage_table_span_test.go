package main

// webpage_table_span_test.go — a cell that spans columns takes the width of
// every column it crosses, whether it ends its row or not.
//
// A one-cell table row used to be laid out flat instead of through the row
// machinery, so its cell's surface started a row early and painted over the
// row above. The overlap made the reader's own table fixture read the
// previous row's half-width cell as the spanning cell's box, which is what
// looked like a colspan that did not span.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// webSpanRunRect is the box of one laid-out word.
func webSpanRunRect(t *testing.T, layout *webLayout, text string) sdl.Rect {
	t.Helper()
	if layout == nil {
		t.Fatal("page layout is nil")
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text != text {
				continue
			}
			w, h := int32(0), int32(0)
			if run.Font != nil {
				if fw, fh, err := run.Font.SizeUTF8(run.Text); err == nil {
					w, h = int32(fw), int32(fh)
				}
			}
			return sdl.Rect{X: run.X, Y: row.Y, W: w, H: h}
		}
	}
	t.Fatalf("run %q is missing from the layout", text)
	return sdl.Rect{}
}

// webSpanCellBox is the smallest painted surface holding a word: the cell the
// word sits in.
func webSpanCellBox(t *testing.T, layout *webLayout, text string) sdl.Rect {
	t.Helper()
	run := webSpanRunRect(t, layout, text)
	best, bestArea := sdl.Rect{}, int32(1<<30-1)
	for _, r := range layout.Rects {
		if !r.BackgroundFill || r.Rect.W <= 0 || r.Rect.H <= 0 {
			continue
		}
		if run.X < r.Rect.X || run.Y < r.Rect.Y ||
			run.X+run.W > r.Rect.X+r.Rect.W || run.Y+run.H > r.Rect.Y+r.Rect.H {
			continue
		}
		if area := r.Rect.W * r.Rect.H; area < bestArea {
			best, bestArea = r.Rect, area
		}
	}
	if bestArea == 1<<30-1 {
		t.Fatalf("no painted cell surface contains %q at %+v", text, run)
	}
	return best
}

func TestWebTableColspanCoversItsColumns(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>
		table{width:600px;border-collapse:collapse;border-spacing:0}
		td{padding:12px;border:1px solid #000;background:#eee}
	</style>
	<table>
		<tr><td>Alpha</td><td>Bravo</td><td>Charlie</td></tr>
		<tr><td colspan="2">Wide</td><td>Echo</td></tr>
		<tr><td>Foxtrot</td><td colspan="2">Tail</td></tr>
		<tr><td colspan="3">Footer</td></tr>
	</table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)

	alpha := webSpanCellBox(t, layout, "Alpha")
	bravo := webSpanCellBox(t, layout, "Bravo")
	charlie := webSpanCellBox(t, layout, "Charlie")
	wide := webSpanCellBox(t, layout, "Wide")
	echo := webSpanCellBox(t, layout, "Echo")
	foxtrot := webSpanCellBox(t, layout, "Foxtrot")
	tail := webSpanCellBox(t, layout, "Tail")
	footer := webSpanCellBox(t, layout, "Footer")

	column := echo.W
	if column <= 0 {
		t.Fatalf("the third column has no width: %+v", echo)
	}
	near := func(got, want int32, what string) {
		t.Helper()
		if d := got - want; d > 8 || d < -8 {
			t.Fatalf("%s = %+v, want %+v (tolerance 8px)", what, got, want)
		}
	}

	// A span in the middle of a row: two columns wide, from column 0 to the
	// third column's left edge.
	near(wide.X, alpha.X, "the mid-row span's left edge")
	near(wide.X+wide.W, echo.X, "the mid-row span's right edge")
	if wide.W < column*3/2 {
		t.Fatalf("the mid-row span is %d px wide, want about two %d px columns", wide.W, column)
	}

	// A span that ends its row: two columns wide, from column 1 to the
	// table's right edge.
	near(tail.X, bravo.X, "the trailing span's left edge")
	near(tail.X+tail.W, charlie.X+charlie.W, "the trailing span's right edge")
	if tail.W < column*3/2 {
		t.Fatalf("the trailing span is %d px wide, want about two %d px columns", tail.W, column)
	}
	// Where a span sits in its row must not change its width.
	near(tail.W, wide.W, "the trailing span's width against the mid-row span's")

	// A span alone in its row covers every column, and the row after it starts
	// below it: this is the case that painted over the row above when a
	// one-cell row skipped the row machinery.
	near(footer.X, alpha.X, "the row-wide span's left edge")
	near(footer.X+footer.W, charlie.X+charlie.W, "the row-wide span's right edge")
	if footer.W < column*5/2 {
		t.Fatalf("the row-wide span is %d px wide, want about three %d px columns", footer.W, column)
	}

	// Rows are stacked, never overlapping: a cell's surface ends where the
	// next row begins.
	rowBottom := max32(foxtrot.Y+foxtrot.H, tail.Y+tail.H)
	if alpha.Y+alpha.H > wide.Y || wide.Y+wide.H > foxtrot.Y || rowBottom > footer.Y {
		t.Fatalf("table rows overlap: first=%+v span=%+v last=%+v trailing=%+v footer=%+v",
			alpha, wide, foxtrot, tail, footer)
	}
}
