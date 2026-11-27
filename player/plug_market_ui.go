package main

// plug_market_ui.go — the Plugin catalog card, drawn with the shared report
// card so its list looks like the other catalogues.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityPluginMarket(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		plugMarketLines(plugMarketScan(), plugMarketUI.cursor),
		[]FooterHint{{"Up/Down", "Pick"}, {"A", "Install"}, {"B", "Back"}})
}
