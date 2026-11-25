package main

// webpage_table_follow_test.go — after a table, the page keeps going below it.
//
// A cell paints one padding row below the row grid its content sits on
// (webTagCellSurface): rows end at 72 in a cell's sub layout while the surface
// runs to 108. The row pass used to count only the row grid, so the table's
// flow height stopped at the content rows and a block placed straight after the
// table began over the last row's bottom padding band — text on top of the
// table's border and background.
//
// The last row now keeps that band (CellLastRow → one row in layoutFlexRowItems),
// so whatever follows the table starts at the last cell's painted bottom.

import "testing"

func TestWebTableFollowingBlockStartsBelowLastCell(t *testing.T) {
	legibleTestInitFonts(t)
	pad12 := "td{padding:12px;background:#eee}"
	edge := "td{border:1px solid #000}"
	three := `<tr><td>Alpha</td><td>Bravo</td><td>Charlie</td></tr>`
	cases := []struct {
		name string
		css  string
		rows string
	}{
		// Two cells in the last row, no border: only the padding band.
		{"no-border", pad12 + "table{width:600px;border-collapse:collapse}",
			`<tr><td>Delta</td><td>Tail</td></tr>`},
		// The same with collapsed borders, which put an edge on the band.
		{"bordered", pad12 + edge + "table{width:600px;border-collapse:collapse}",
			`<tr><td>Delta</td><td>Tail</td></tr>`},
		// The last cell ends its row with a colspan over the last two columns.
		{"colspan", pad12 + edge + "table{width:600px;border-collapse:collapse}",
			three + `<tr><td>Delta</td><td colspan="2">Tail</td></tr>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			html := `<style>body{margin:0;font-size:17px}` + c.css +
				`#after{padding:6px;background:#dde}</style>` +
				`<table>` + c.rows + `</table><div id="after">After</div>`
			layout := parseWebPage(html, "", "https://example.com/").
				computeLayout(&Config{}, Element{Font: "body"}, 720)
			last := webSpanCellBox(t, layout, "Tail")
			after := webSpanCellBox(t, layout, "After")
			bottom := last.Y + last.H
			if after.Y < bottom {
				t.Fatalf("the block after the table starts at y=%d, on top of the last cell's surface, which ends at y=%d (last cell %+v, following block %+v)",
					after.Y, bottom, last, after)
			}
			// The band itself is the only space between them: no blank row.
			if gap := after.Y - bottom; gap > layout.RowH {
				t.Fatalf("the block after the table starts %dpx below the last cell (band ends at y=%d, block at y=%d), more than the %dpx paint band allows",
					gap, bottom, after.Y, layout.RowH)
			}
		})
	}
}
