package main

// romaudit_ui.go — the ROM Audit page, drawn with the shared report card.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func renderCommunityROMAudit(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32, report romAuditReport,
	scanning bool, checked, total int) {
	renderReportCard(renderer, config, font, panelX, panelY, panelW, panelH, padX, headerH,
		romAuditLines(report, scanning, checked, total, false),
		[]FooterHint{{"Up/Down", "Scroll"}, {"Y", "Rescan"}, {"X", "Save report"}, {"B", "Back"}})
}
