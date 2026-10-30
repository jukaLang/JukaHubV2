package main

// The About screen shows the identification evidence DetectDevice used, so a
// handheld that is named — or is not — can be checked without a shell and
// without the log. These tests pin the wording of those rows and prove the four
// cards still fit the content band on both shipped canvases.

import (
	"strings"
	"testing"
)

// rowValue returns the value of the row with the given label.
func rowValue(rows []aboutRow, label string) (string, bool) {
	for _, row := range rows {
		if row.label == label {
			return row.value, true
		}
	}
	return "", false
}

// TestAboutDeviceRowsShowTheEvidence: a Brick identified from its device tree
// must report the model, the source that named it, the panel, the firmware's own
// model strings and the kernel's board description.
func TestAboutDeviceRowsShowTheEvidence(t *testing.T) {
	id := deviceIdentity{
		Model:      "Trimui Brick",
		Compatible: "allwinner,sun50i-a100 tg3040",
		Names:      []string{"Trimui Brick\n", "NAME=TrimUI"},
		PanelW:     1024,
		PanelH:     768,
		TrimUI:     true,
	}
	rows := aboutDeviceRows(DeviceTrimuiBrick, "device tree", id)

	cases := map[string]string{
		"Model":         "TrimUI Brick (TG3040)",
		"Identified by": "device tree",
		"Panel":         "1024x768",
		"Firmware":      "Trimui Brick",
		"Device tree":   "Trimui Brick",
		"Compatible":    "allwinner,sun50i-a100 tg3040",
	}
	for label, want := range cases {
		got, ok := rowValue(rows, label)
		if !ok {
			t.Errorf("About has no %q row; rows=%v", label, rows)
			continue
		}
		if got != want {
			t.Errorf("About %q row = %q, want %q", label, got, want)
		}
	}
}

// TestAboutDeviceRowsNameTheProSOnce: the longer firmware name contains the
// shorter one, so a Smart Pro S must not be reported as both.
func TestAboutDeviceRowsNameTheProSOnce(t *testing.T) {
	rows := aboutDeviceRows(DeviceTrimuiSmartProS, "launcher env", deviceIdentity{
		EnvDevice: "smart pro s",
		Names:     []string{"Trimui Smart Pro S"},
		PanelW:    1280,
		PanelH:    720,
	})
	if got, _ := rowValue(rows, "Firmware"); got != "Trimui Smart Pro S" {
		t.Fatalf("Firmware = %q, want only the Pro S name", got)
	}
}

// TestAboutDeviceRowsSayWhatIsMissing: on hardware nothing named — a desktop
// build, or a handheld the image does not describe — the card says so instead of
// printing blank values, and omits the device-tree rows a platform never has.
func TestAboutDeviceRowsSayWhatIsMissing(t *testing.T) {
	rows := aboutDeviceRows(DeviceUnknown, "", deviceIdentity{})

	if got, _ := rowValue(rows, "Model"); got != "Unknown" {
		t.Errorf("Model = %q, want Unknown", got)
	}
	if got, _ := rowValue(rows, "Identified by"); got != "not identified" {
		t.Errorf("Identified by = %q, want an honest \"not identified\"", got)
	}
	if got, _ := rowValue(rows, "Panel"); got != "not detected" {
		t.Errorf("Panel = %q, want not detected", got)
	}
	if got, _ := rowValue(rows, "Firmware"); got != "no model name" {
		t.Errorf("Firmware = %q, want no model name", got)
	}
	if _, ok := rowValue(rows, "Device tree"); ok {
		t.Error("an empty device tree should not get a row")
	}
	if _, ok := rowValue(rows, "Compatible"); ok {
		t.Error("an empty compatible list should not get a row")
	}
}

// TestAboutCardsFitBothCanvases renders the real About scene at the wide and the
// compact canvas and checks the measured content against the card it lives in.
// The four cards and the licence column were the overflow risk here: the card
// heights are measured from the panel now, so neither canvas may clip a line
// over its own border.
func TestAboutCardsFitBothCanvases(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := sceneIndexExact(config, "About")
	if idx < 0 {
		t.Fatal("config has no About scene")
	}

	prevScene, prevConfig := currentSceneIndex, appConfig
	currentSceneIndex, appConfig = idx, config
	t.Cleanup(func() { currentSceneIndex, appConfig = prevScene, prevConfig })

	for _, canvas := range []struct {
		name string
		w, h int32
	}{
		{"wide", uiCanvasWideWidth, uiCanvasWideHeight},
		{"compact", uiCanvasCompactW, uiCanvasCompactH},
	} {
		t.Run(canvas.name, func(t *testing.T) {
			withScreen(t, canvas.w, canvas.h)
			renderer := newKeyboardTestRenderer(t)
			ClearTextCache()
			t.Cleanup(ClearTextCache)

			renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
			renderer.Clear()
			renderScene(renderer, config, config.Scenes[idx])

			if aboutCardBottom == 0 || aboutContentBottom == 0 {
				t.Fatalf("About reported no measurements on the %s canvas (card=%d content=%d)",
					canvas.name, aboutCardBottom, aboutContentBottom)
			}
			if aboutContentBottom > aboutCardBottom {
				t.Fatalf("About content reaches y=%d but its card ends at y=%d on the %s canvas",
					aboutContentBottom, aboutCardBottom, canvas.name)
			}
			if limit := canvas.h - HomeFooterH; aboutContentBottom > limit {
				t.Fatalf("About content reaches y=%d, past the footer at y=%d on the %s canvas",
					aboutContentBottom, limit, canvas.name)
			}
		})
	}
}

// TestAboutLicenceRowsTrimVisibly: a card too short for the whole list keeps the
// rows it can show and says how many are missing, instead of clipping them into
// the card's border.
func TestAboutLicenceRowsTrimVisibly(t *testing.T) {
	licenses := []string{"A", "B", "C", "D", "E", "F", "G", "H"}

	// 54px title + 3 lines of 20px fits a 128px card.
	shown := aboutLicenceRows(licenses, 128, 20)
	if len(shown) != 3 {
		t.Fatalf("trimmed list has %d rows, want 3 fits: %v", len(shown), shown)
	}
	if last := shown[len(shown)-1]; !strings.Contains(last, "6 more") || !strings.Contains(last, "NOTICE") {
		t.Fatalf("last row = %q, want a count and where the rest went", last)
	}

	// A card with room keeps every licence untouched.
	if full := aboutLicenceRows(licenses, 226, 20); len(full) != len(licenses) {
		t.Fatalf("a roomy card dropped rows: %v", full)
	}
}
