package main

import "testing"

// The games column on a TrimUI: every shipped config leaves the "small" and
// "medium" roles at the 24px default (29px lines), and the old fixed 58px row
// put the detail line ("PlayStation Portable · game.iso") 16px into the next
// row, under its background and against its title.
func TestGameLibraryRowsDoNotOverlap(t *testing.T) {
	cases := []struct {
		name                           string
		bodyH, titleH, detailH, rangeH int32
		total                          int
	}{
		// The bundled Inter at the shipped roles: 24px titles have a 30px line,
		// the 18px detail line 22px; the Smart Pro's column is 515px tall.
		{"Smart Pro, long list", 515, 30, 22, 30, 40},
		{"Smart Pro, short list", 515, 30, 22, 30, 3},
		{"Brick", 539, 22, 17, 22, 40},
		{"largest text size", 515, 43, 33, 43, 40},
		{"taller body", 610, 24, 19, 24, 12},
		{"tiny body", 70, 29, 22, 29, 5},
	}
	for _, c := range cases {
		l := gameLibraryRowLayout(c.bodyH, c.titleH, c.detailH, c.rangeH, c.total)
		if l.visible < 1 {
			t.Fatalf("%s: %d visible rows, want at least 1", c.name, l.visible)
		}
		box := l.rowH - gameLibraryRowGap
		if l.titleY < 0 || l.detailY < l.titleY+c.titleH {
			t.Errorf("%s: the detail line (y %d) starts inside the title (y %d, %dpx)", c.name, l.detailY, l.titleY, c.titleH)
		}
		if l.detailY+c.detailH > box {
			t.Errorf("%s: the detail line ends at %d, below its %dpx row box — it draws into the next row",
				c.name, l.detailY+c.detailH, box)
		}
		shown := l.visible
		if shown > c.total {
			shown = c.total
		}
		lastBottom := int32(shown-1)*l.rowH + box
		if c.total > l.visible {
			// The "1-6 of 40" line sits under the last row, not on it.
			if !l.showRange {
				t.Errorf("%s: a list longer than the column has no range line", c.name)
			}
			if l.visible > 1 && l.rangeY < lastBottom {
				t.Errorf("%s: the range line (y %d) overlaps the last row (bottom %d)", c.name, l.rangeY, lastBottom)
			}
			if l.visible > 1 && l.rangeY+c.rangeH > c.bodyH {
				t.Errorf("%s: the range line ends at %d, past the column (%d)", c.name, l.rangeY+c.rangeH, c.bodyH)
			}
		} else if l.showRange {
			t.Errorf("%s: a list that fits shows a range line", c.name)
		}
		if l.visible > 1 && lastBottom > c.bodyH {
			t.Errorf("%s: %d rows end at %d, past the column (%d)", c.name, l.visible, lastBottom, c.bodyH)
		}
	}
}

// The design pitch stays the floor: small type does not pack rows tighter
// than the 58px the panel was drawn at.
func TestGameLibraryRowPitchFloor(t *testing.T) {
	if l := gameLibraryRowLayout(538, 14, 12, 14, 40); l.rowH != 58 {
		t.Fatalf("row pitch with small type = %d, want the 58px floor", l.rowH)
	}
}

// A console row grows with its type instead of letting the name run into the
// cover meter drawn along its bottom edge.
func TestGameLibrarySystemRowFitsItsName(t *testing.T) {
	for _, nameH := range []int32{24, 29, 36, 43} {
		rowH := gameLibrarySystemRowHeight(nameH)
		box := rowH - 6
		textTop := (box - nameH) / 2
		meterTop := box - 5
		if textTop+nameH > meterTop {
			t.Errorf("name %dpx: text ends at %d, on the meter at %d (row %d)", nameH, textTop+nameH, meterTop, rowH)
		}
		if rowH < 46 {
			t.Errorf("name %dpx: row %d is under the 46px design height", nameH, rowH)
		}
	}
}
