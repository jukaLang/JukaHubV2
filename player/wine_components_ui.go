package main

// wine_components_ui.go — the WINE components card, drawn with the shared
// report card.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityWineComponents(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		wineComponentsLines(wineComponentsUI.cursor),
		[]FooterHint{{"Up/Down", "Pick"}, {"A", "Install"}, {"X", "Verify"}, {"Y", "Example"}, {"Del", "Remove"}, {"B", "Back"}})
}
