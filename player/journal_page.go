package main

// journal_page.go — the Play Journal page, drawn with the shared report card.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityJournal(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		journalLines(false),
		[]FooterHint{{"Up/Down", "Scroll"}, {"X", "Save recap"}, {"B", "Back"}})
}
