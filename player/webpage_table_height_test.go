package main

// webpage_table_height_test.go — a table's last row is as tall as it paints.
//
// A cell paints one padding row below its content rows (webTagCellSurface), and
// the page used to stop at the content rows' end: with 12px padding the last
// row's bottom band fell outside the scrollable area, so the table looked cut
// at max scroll (the reader's table fixture measured page height 360px against
// a final cell surface ending at 396px).

import "testing"

func TestWebTablePageHeightCoversLastRowPadding(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>
		body{margin:0;font-size:17px}
		table{width:600px;border-collapse:collapse}
		td{padding:12px;border:1px solid #000;background:#eee}
	</style>
	<table>
		<tr><td>Alpha</td><td>Bravo</td></tr>
		<tr><td>Delta</td><td>Tail</td></tr>
	</table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)

	alpha := webSpanCellBox(t, layout, "Alpha")
	delta := webSpanCellBox(t, layout, "Delta")
	tail := webSpanCellBox(t, layout, "Tail")

	// Rows stay flush: the second row starts where the first ends.
	if alpha.Y+alpha.H > delta.Y {
		t.Fatalf("table rows overlap: first=%+v second=%+v", alpha, delta)
	}

	// The page is tall enough to scroll to the last cell's bottom padding,
	// not just to its text rows.
	if bottom := tail.Y + tail.H; layout.Height < bottom {
		t.Fatalf("page height %dpx does not cover the last cell's surface, which ends at %dpx (last cell %+v)",
			layout.Height, bottom, tail)
	}
}
