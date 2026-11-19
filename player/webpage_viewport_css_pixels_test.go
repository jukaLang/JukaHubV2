package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestWebResponsiveBreakpointUsesCSSPixelViewport(t *testing.T) {
	oldW, oldH, oldZoom := screenWidth, screenHeight, browserZoomIndex
	screenWidth, screenHeight, browserZoomIndex = 1024, 768, browserZoomDefault
	t.Cleanup(func() { screenWidth, screenHeight, browserZoomIndex = oldW, oldH, oldZoom })

	w, _ := webMediaViewport()
	if math.Abs(w-1024/(24.0/16.0)) > 0.01 {
		t.Fatalf("CSS viewport width = %.2f, want %.2f", w, 1024/(24.0/16.0))
	}
	if webMediaBlockApplies("@media (min-width:875px)") || !webMediaBlockApplies("@media (max-width:875px)") {
		t.Fatal("a two-column breakpoint used physical pixels instead of CSS pixels")
	}
	var cfg struct {
		Width int `json:"width"`
	}
	if err := json.Unmarshal([]byte((&webJSSession{}).configJSON()), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Width != int(math.Round(w)) {
		t.Fatalf("script viewport width = %d, media queries use %.2f", cfg.Width, w)
	}
}
