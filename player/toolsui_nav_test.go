package main

// Grid navigation on the Tools panel. The rows are ragged and separated by
// section headings, so flat index arithmetic moved the cursor diagonally: Right
// at the end of a row jumped to the first tile of the row below, which after the
// section split crossed a heading. These cases pin the eye-level rule instead.

import "testing"

func navRows() []toolsRow {
	rows := make([]toolsRow, 14)
	rows[0].section = "A"
	rows[4].section = "B"
	rows[11].section = "C"
	for i := range rows {
		rows[i].label = "row"
	}
	return rows
}

func navGrid() toolsGrid {
	return toolsGridBuild(navRows(), toolsGridMetrics{
		cols: 4, tileW: 100, tileH: 90, gap: 10, padX: 10, sectionH: 20,
	})
}

func TestToolsGridTargetStaysInItsRow(t *testing.T) {
	g := navGrid()
	cases := []struct {
		cursor, dx, dy, want int
		why                  string
	}{
		{0, 1, 0, 1, "right inside the row"},
		{3, 1, 0, 3, "right at the row's end stays put"},
		{0, -1, 0, 0, "left at the row's start stays put"},
		{3, -1, 0, 2, "left inside the row"},
		{0, 0, 1, 4, "down keeps the column"},
		{3, 0, 1, 7, "down from a full row keeps the last column"},
		{6, 0, 1, 10, "down from the probes' first row"},
		{9, 0, 1, 12, "down keeps the middle column in the next row"},
		{10, 0, 1, 13, "down into a shorter row keeps the last column"},
		{11, 0, -1, 8, "up into a longer row keeps column zero"},
		{13, 0, 1, 13, "down at the last row stays put"},
		{13, 1, 0, 13, "right at the last tile stays put"},
	}
	for _, c := range cases {
		if got := toolsGridTarget(g, c.cursor, c.dx, c.dy); got != c.want {
			t.Errorf("toolsGridTarget(cursor=%d, dx=%d, dy=%d) = %d, want %d (%s)",
				c.cursor, c.dx, c.dy, got, c.want, c.why)
		}
	}
}

func TestToolsGridTargetIgnoresAnOffGridCursor(t *testing.T) {
	g := navGrid()
	if got := toolsGridTarget(g, -1, 1, 0); got != -1 {
		t.Fatalf("cursor -1 moved to %d", got)
	}
	if got := toolsGridTarget(g, 99, 0, 1); got != 99 {
		t.Fatalf("out-of-range cursor moved to %d", got)
	}
}

// The panel's own move must not leave the row it started in: Right on the last
// server tile used to land on the first probe, one row down and past a heading.
func TestToolsPanelMoveStaysInsideTheRow(t *testing.T) {
	withScreen(t, 1280, 720)
	prevCursor, prevOpen := toolsPanel.cursor, toolsPanel.open
	t.Cleanup(func() { toolsPanel.cursor, toolsPanel.open = prevCursor, prevOpen })
	toolsPanel.open = true

	rows := toolsRows(&Config{})
	if len(rows) < 5 {
		t.Fatalf("rows = %d, too few for this check", len(rows))
	}
	toolsPanel.cursor = 3 // SSH / SFTP, last tile of SERVERS & SHARING
	toolsPanelMove(&Config{}, 1, 0)
	if toolsPanel.cursor != 3 {
		t.Fatalf("right at the end of the server row moved to %d (%q), want to stay",
			toolsPanel.cursor, rows[toolsPanel.cursor].label)
	}
	toolsPanelMove(&Config{}, 0, 1)
	if want := 3 + 4; toolsPanel.cursor != want {
		t.Fatalf("down from the server row moved to %d, want %d (same column, next row)", toolsPanel.cursor, want)
	}
}
