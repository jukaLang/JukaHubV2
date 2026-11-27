package main

// steam_achievements_ui.go — the achievements card, drawn with the shared
// report card like the Journal and ROM Audit pages.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunitySteamAch(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	title, list, loading, errText := steamAchSnapshot()
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		steamAchLines(title, list, loading, errText),
		[]FooterHint{{"Up/Down", "Scroll"}, {"B", "Back"}})
}
