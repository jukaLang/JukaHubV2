package main

import "testing"

func TestShortsGridLayoutAndHitboxesStayAligned(t *testing.T) {
	element := Element{X: 24, Y: 72}
	layout := newShortsGridLayout(element, 1232)
	if layout.cols != 6 {
		t.Fatalf("columns = %d, want 6 for a 1232px viewport", layout.cols)
	}

	first := shortsGridCellRect(element, layout, 0, 0)
	second := shortsGridCellRect(element, layout, 1, 0)
	lastInRow := shortsGridCellRect(element, layout, layout.cols-1, 0)
	firstNextRow := shortsGridCellRect(element, layout, layout.cols, 0)

	if first.X < element.X || lastInRow.X+lastInRow.W > element.X+1232 {
		t.Fatalf("grid cells exceed viewport: first=%+v last=%+v", first, lastInRow)
	}
	if second.X-first.X != layout.cellWidth+layout.gap {
		t.Fatalf("horizontal hitbox pitch = %d, want %d", second.X-first.X, layout.cellWidth+layout.gap)
	}
	if firstNextRow.X != first.X || firstNextRow.Y-first.Y != layout.cellHeight+layout.gap {
		t.Fatalf("next row hitbox = %+v, want same X and vertical pitch %d from %+v", firstNextRow, layout.cellHeight+layout.gap, first)
	}
}

func TestStartMenuEntriesHaveValidDestinations(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("load menu config: %v", err)
	}

	for _, entry := range getStartMenuApps(config) {
		if entry.action == nil && (entry.scene == "" || findSceneIndex(config, entry.scene) < 0) {
			t.Errorf("menu item %q has no action or valid scene destination", entry.name)
		}
	}
}

func TestTubeSceneUsesMediaDisplayName(t *testing.T) {
	if got := sceneDisplayName("Tube"); got != "Media" {
		t.Fatalf("Tube display name = %q, want Media", got)
	}
}
