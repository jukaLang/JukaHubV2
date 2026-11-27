package main

// wine_app_catalog_ui.go — the suggested Windows apps card, drawn with the
// shared report card.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityWineApps(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		wineAppCatalogLines(wineAppCatalogUI.cursor),
		[]FooterHint{{"Up/Down", "Pick"}, {"A", "Project site"}, {"B", "Back"}})
}
