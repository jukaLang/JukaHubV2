package main

// UI access audit — the checks the older menu and D-pad audits do not make.
//
// menu_audit_test.go proves every control is wired, in bounds and drawn;
// dpad_audit_test.go proves every control can be reached. Neither says whether
// a control is still *readable and hittable* once the user turns on the
// accessibility text size, or on the compact 4:3 canvas where the 1280-wide
// design is squeezed into 960. Those are the two ways a working button becomes
// an unusable one, so they are checked here.
//
// The canvases are the two the app actually renders into: 1280x720 for the
// 16:9 handhelds and 960x720 for the Brick family (uiprofile.go).

import (
	"testing"
)

// uiAuditCanvas is one logical canvas the app draws into.
type uiAuditCanvas struct {
	name   string
	device DeviceModel
	w, h   int32
}

var uiAuditCanvases = []uiAuditCanvas{
	{"wide", DeviceTrimuiSmartPro, uiCanvasWideWidth, uiCanvasWideHeight},
	{"compact", DeviceTrimuiBrick, uiCanvasCompactW, uiCanvasCompactH},
}

// uiAuditConfig loads both shipped configs onto one canvas. The profile is
// applied the same way startup applies it, so element geometry is what the
// device actually renders.
func uiAuditConfig(t *testing.T, canvas uiAuditCanvas) map[string]*Config {
	t.Helper()
	withScreen(t, canvas.w, canvas.h)
	configs := loadAuditConfigs(t)
	for _, config := range configs {
		applyUIProfileForDevice(config, canvas.device)
	}
	return configs
}

// setA11yScale changes the accessibility text size and rebuilds every font so
// measurements use the size the user would see.
func setA11yScale(t *testing.T, scale float64) {
	t.Helper()
	previous := a11y.fontScale
	a11y.fontScale = scale
	a11yRefreshFonts()
	t.Cleanup(func() {
		a11y.fontScale = previous
		a11yRefreshFonts()
	})
}

// uiAuditButtonSize is the size a pressable element really occupies: a button
// sizes itself from its label unless the config fixes a width, a toggle is
// resolved from its own element.
func uiAuditButtonSize(config *Config, el Element) (int32, int32) {
	font, _ := getCachedFont(config, el.Font)
	if el.Type == "button" {
		return buttonHitSize(el, font)
	}
	return resolveElementDimensions(el, 320, 48)
}

// TestButtonLabelsStayWholeAtTheLargestTextScale is the accessibility
// counterpart of TestInteractiveElementsStayInBounds: a button inside the panel
// can still be unreadable when the only thing it shows is "Play…". At 145% the
// label must still be drawn in full, which means the button shrinks its text
// (buttonLabelFont) until it fits instead of chopping it.
func TestButtonLabelsStayWholeAtTheLargestTextScale(t *testing.T) {
	newProbeFrame(t)
	setA11yScale(t, a11yMaxScale)

	neededFitting := 0
	checked := 0
	for _, canvas := range uiAuditCanvases {
		for name, config := range uiAuditConfig(t, canvas) {
			for _, scene := range config.Scenes {
				if scene.Name == "Main" && scene.Layout == "home" {
					continue
				}
				for _, el := range scene.Elements {
					if el.Type != "button" || el.Text == "" {
						continue
					}
					width, _ := uiAuditButtonSize(config, el)
					inner := width - 2*SpaceXS
					if inner <= 0 {
						continue
					}
					checked++
					base, _ := getCachedFont(config, el.Font)
					if base != nil && ellipsize(base, el.Text, inner) != el.Text {
						neededFitting++
					}
					fitted := buttonLabelFont(config, el.Font, el.Text, width)
					if fitted == nil {
						t.Fatalf("%s / %s: no font for %q", canvas.name, scene.Name, el.Text)
					}
					if cut := ellipsize(fitted, el.Text, inner); cut != el.Text {
						t.Errorf("%s / %s / %s: label %q is cut to %q in a %dpx button at %d%% text",
							canvas.name, name, scene.Name, el.Text, cut, width, int(a11yMaxScale*100))
					}
				}
			}
		}
	}
	// If the requested size were not applied, the fitted font would equal the
	// unfitted one everywhere and this audit would pass without testing
	// anything. Some shipped labels must need fitting at the largest size.
	if neededFitting == 0 {
		t.Fatalf("audit has no teeth: none of %d labels needed fitting at %d%% text",
			checked, int(a11yMaxScale*100))
	}
}

// TestFittedLabelNeverShrinksBelowNormalTextSize pins the floor in
// buttonLabelFont: the user's larger text may give way to the layout, but never
// to a size smaller than the same label gets at 100%.
func TestFittedLabelNeverShrinksBelowNormalTextSize(t *testing.T) {
	newProbeFrame(t)
	setA11yScale(t, a11yMaxScale)
	for _, canvas := range uiAuditCanvases {
		for name, config := range uiAuditConfig(t, canvas) {
			for _, scene := range config.Scenes {
				if scene.Name == "Main" && scene.Layout == "home" {
					continue
				}
				for _, el := range scene.Elements {
					if el.Type != "button" || el.Text == "" {
						continue
					}
					width, _ := uiAuditButtonSize(config, el)
					if width-2*SpaceXS <= 0 {
						continue
					}
					fitted := buttonLabelFont(config, el.Font, el.Text, width)
					normal := loadRoleFont(config, el.Font, fontRoleSize(config, el.Font))
					if fitted == nil || normal == nil {
						continue
					}
					fittedWidth, _, err := fitted.SizeUTF8(el.Text)
					if err != nil {
						continue
					}
					normalWidth, _, err := normal.SizeUTF8(el.Text)
					if err != nil {
						continue
					}
					if fittedWidth+1 < normalWidth {
						t.Errorf("%s / %s / %s: %q was shrunk to %dpx of text, below its %dpx normal size",
							canvas.name, name, scene.Name, el.Text, fittedWidth, normalWidth)
					}
				}
			}
		}
	}
}

// TestButtonTargetsStayHittable guards the size floor every pressable control
// keeps on both canvases: a control smaller than this is a miss waiting to
// happen with a thumb or a stylus.
func TestButtonTargetsStayHittable(t *testing.T) {
	const minW, minH int32 = 40, 32
	newProbeFrame(t)
	for _, canvas := range uiAuditCanvases {
		for name, config := range uiAuditConfig(t, canvas) {
			for _, scene := range config.Scenes {
				for _, el := range scene.Elements {
					if el.Type != "button" && el.Type != "toggle" {
						continue
					}
					w, h := uiAuditButtonSize(config, el)
					if w < minW || h < minH {
						t.Errorf("%s / %s / %s: %q is %dx%d, below the %dx%d tap floor",
							canvas.name, name, scene.Name, el.Text, w, h, minW, minH)
					}
				}
			}
		}
	}
}

// TestCompactCanvasKeepsControlsInThePanel checks the 960-wide canvas with its
// own margins (the design margin scales with the canvas): the compact profile is
// what the 4:3 handhelds render, and nothing may slide under the footer or off
// the edges there.
func TestCompactCanvasKeepsControlsInThePanel(t *testing.T) {
	canvas := uiAuditCanvases[1]
	newProbeFrame(t)
	margin := int32(float64(24) * float64(canvas.w) / float64(uiCanvasWideWidth))
	for name, config := range uiAuditConfig(t, canvas) {
		for _, scene := range config.Scenes {
			if scene.Name == "Main" && scene.Layout == "home" {
				continue
			}
			footerTop := canvas.h - bottomBarHeight(scene.Name) - 16
			for _, el := range scene.Elements {
				if el.Type != "button" && el.Type != "toggle" {
					continue
				}
				w, h := uiAuditButtonSize(config, el)
				if el.X < margin || el.X+w > canvas.w-margin {
					t.Errorf("%s / %s: %q spans x=%d..%d, outside the %dpx margins",
						name, scene.Name, el.Text, el.X, el.X+w, margin)
				}
				if el.Y+h > footerTop {
					t.Errorf("%s / %s: %q ends at y=%d, below the %dpx content panel",
						name, scene.Name, el.Text, el.Y+h, footerTop)
				}
			}
		}
	}
}
