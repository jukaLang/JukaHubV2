package main

import "testing"

// Settings search opens one row, not just its section: the focus has to land on
// the row with the requested id, and rows that predate ids still resolve by
// label. Hidden rows are skipped.
func TestSetHubFocusRowFindsTheRequestedRow(t *testing.T) {
	prev := setHub
	t.Cleanup(func() { setHub = prev })

	setHub.rows = []setRow{
		{ID: "vibration", Label: "Vibration"},
		{Label: "Language"},
	}
	setHub.views = []setRowView{{visible: true}, {visible: true}}
	setHub.row, setHub.inPane = 0, false

	if !setHubFocusRow("vibration", "Vibration") {
		t.Fatal("the row with the requested id was not found")
	}
	if setHub.row != 0 || !setHub.inPane || !setHub.follow {
		t.Errorf("focus = row %d, pane %v, follow %v", setHub.row, setHub.inPane, setHub.follow)
	}

	setHub.row, setHub.inPane = 0, false
	if !setHubFocusRow("", "Language") {
		t.Fatal("a row without an id did not resolve by label")
	}
	if setHub.row != 1 || !setHub.inPane {
		t.Errorf("label focus = row %d, pane %v", setHub.row, setHub.inPane)
	}

	if setHubFocusRow("missing", "Vibration") {
		t.Error("a wrong id matched a label instead")
	}

	setHub.views[1].visible = false
	if setHubFocusRow("", "Language") {
		t.Error("a hidden row was focused")
	}
}
