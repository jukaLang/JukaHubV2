package main

// offline_ref_ui.go — the Offline reference card, drawn with the shared
// report card.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityOfflineRef(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		offlineRefLines(),
		[]FooterHint{{"Up/Down", "Pick or scroll"}, {"A", "Read or close"}, {"B", "Back"}})
}
