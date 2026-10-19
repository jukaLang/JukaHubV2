package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestUIGridColumnsFitsCells(t *testing.T) {
	cases := []struct {
		areaW, minW, gap int32
		want             int
	}{
		{1160, 230, 16, 4},
		{940, 230, 16, 3},
		{700, 230, 16, 2},
		{300, 230, 16, 1},
		{0, 230, 16, 1},
		{-20, 230, 16, 1},
		{500, 0, 16, 1},
	}
	for _, tc := range cases {
		if got := UIGridColumns(tc.areaW, tc.minW, tc.gap); got != tc.want {
			t.Errorf("UIGridColumns(%d,%d,%d) = %d, want %d",
				tc.areaW, tc.minW, tc.gap, got, tc.want)
		}
	}
}

func TestUIGridCellWidthUsesAllSpace(t *testing.T) {
	// 1160 wide, 4 columns, 16px gaps, exactly as wide as the theme gallery.
	if got, want := UIGridCellWidth(1160, 4, 16), int32(278); got != want {
		t.Errorf("width = %d, want %d", got, want)
	}
	last := UIGridCellRect(3, 4, 0, 0, 278, 168, 16)
	if last.X+last.W > 1160 {
		t.Errorf("last cell %+v overflows 1160", last)
	}
	if got := UIGridCellWidth(10, 4, 16); got != 0 {
		t.Errorf("impossible grid width = %d, want 0", got)
	}
}

func TestUIGridVisibleRows(t *testing.T) {
	if got := UIGridVisibleRows(560, 168, 16); got != 3 {
		t.Errorf("rows = %d, want 3", got)
	}
	if got := UIGridVisibleRows(100, 168, 16); got != 1 {
		t.Errorf("rows = %d, want at least 1", got)
	}
	if got := UIGridVisibleRows(0, 168, 16); got != 1 {
		t.Errorf("zero area rows = %d, want 1", got)
	}
}

func TestUIGridCellRect(t *testing.T) {
	got := UIGridCellRect(5, 3, 100, 50, 200, 120, 10)
	want := sdl.Rect{X: 100 + 2*(200+10), Y: 50 + (120 + 10), W: 200, H: 120}
	if got != want {
		t.Errorf("cell 5 = %+v, want %+v", got, want)
	}
	if got := UIGridCellRect(-1, 3, 0, 0, 10, 10, 0); got.X != 0 || got.Y != 0 {
		t.Errorf("negative index = %+v, want the first cell", got)
	}
	if got := UIGridCellRect(2, 0, 0, 0, 10, 10, 5); got.X != 0 || got.Y != 30 {
		t.Errorf("zero columns = %+v, want a one-column grid (third row)", got)
	}
}

// The gallery's geometry must come from the shared grid helpers so its
// rendering, hit-testing and scroll maths cannot drift apart.
func TestThemeGalleryUsesGridGeometry(t *testing.T) {
	element := Element{X: 0, Y: 0, Width: StringOrInt("1280"), Height: StringOrInt("560")}
	cols, cardW, rows := themeGalleryMetrics(element)
	if want := UIGridColumns(1280, themeGalleryMinW, themeGalleryGap); cols != want {
		t.Errorf("cols = %d, want %d", cols, want)
	}
	if want := UIGridCellWidth(1280, cols, themeGalleryGap); cardW != want {
		t.Errorf("cardW = %d, want %d", cardW, want)
	}
	if want := UIGridVisibleRows(560, themeGalleryH, themeGalleryGap); rows != want {
		t.Errorf("rows = %d, want %d", rows, want)
	}
}
