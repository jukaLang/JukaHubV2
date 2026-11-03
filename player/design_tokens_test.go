package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// The semantic type scale must stay ordered and resolve to real font names.
func TestTypeRoleScale(t *testing.T) {
	prev := int32(1 << 30)
	for _, role := range []TypeRole{TypeTitle, TypeHeading, TypeBody, TypeLabel, TypeCaption} {
		name := role.FontName()
		if name == "" {
			t.Errorf("role %d has no font name", role)
		}
		size := role.Size()
		if size <= 0 {
			t.Errorf("role %s has size %d", name, size)
		}
		if size > prev {
			t.Errorf("type scale is not descending at %s: %d after %d", name, size, prev)
		}
		prev = size
	}
	if FontForRole(nil, TypeBody) != nil {
		t.Error("a nil config should resolve to no font")
	}
}

// Comfortable density must never be tighter than compact, and low power must
// choose the compact rhythm.
func TestDensityTokens(t *testing.T) {
	for _, step := range []string{"xs", "sm", "md", "lg", "xl", "2xl"} {
		compact := DensitySpace(DensityCompact, step)
		comfortable := DensitySpace(DensityComfortable, step)
		if compact <= 0 {
			t.Errorf("%s compact spacing = %d", step, compact)
		}
		if comfortable < compact {
			t.Errorf("%s: comfortable (%d) is tighter than compact (%d)", step, comfortable, compact)
		}
	}
	if got := DensitySpace(DensityCompact, "unknown"); got != SpaceMD {
		t.Errorf("unknown step = %d, want the medium fallback", got)
	}

	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	screenHeight = 720
	if got := DensityFor(&Config{}); got != DensityComfortable {
		t.Errorf("720p density = %v, want comfortable", got)
	}
	screenHeight = 480
	if got := DensityFor(&Config{}); got != DensityCompact {
		t.Errorf("small screen density = %v, want compact", got)
	}
	screenHeight = 720
	saver := &Config{}
	saver.Variables.LowPower = true
	if got := DensityFor(saver); got != DensityCompact {
		t.Errorf("low power density = %v, want compact", got)
	}
}

// Status colours are authored as fills, but the app also draws labels in them
// ("Failed", "Offline", the Groq status). Sending the fill straight to text put
// those labels under WCAG AA on several of the 25 presets, and nothing checked
// it: TestThemePresetsAreReadable only holds body text, plus a 2:1 visibility
// floor for the accent that is the graphics ratio, not the text one. Every
// preset must now yield ink that clears 4.5:1 on all three text surfaces
// without flattening the hue into grey.
func TestStatusInkIsReadableInEveryTheme(t *testing.T) {
	// ApplyThemeColors rewrites every colour token in the package, so the
	// palette goes back afterwards: a test that leaves the last preset applied
	// turns this fixed-order suite into an order-dependent one, and the render
	// tests that follow would quietly assert against the wrong theme.
	snap := capturePalette()
	t.Cleanup(func() { restorePalette(snap) })

	spread := func(c sdl.Color) int {
		hi, lo := c.R, c.R
		for _, v := range []uint8{c.G, c.B} {
			if v > hi {
				hi = v
			}
			if v < lo {
				lo = v
			}
		}
		return int(hi - lo)
	}

	adjusted := 0
	for _, name := range ListThemePresets() {
		preset := themePresets[name]
		ApplyThemeColors(preset)
		surfaces := map[string]sdl.Color{
			"background": ColorBackground,
			"surface":    ColorSurface,
			"surfaceAlt": ColorSurfaceAlt,
		}
		for label, pair := range map[string][2]sdl.Color{
			"success": {ColorSuccess, SuccessInk()},
			"warning": {ColorWarning, WarningInk()},
			"danger":  {ColorDanger, DangerInk()},
			"info":    {ColorInfo, InfoInk()},
			"accent":  {hexRGBA(preset.Info), ColorTextAccent()},
			// The toast palette is theme-invariant, so it has to be checked
			// against every preset: the light one is where it fails hardest.
			"toastSuccess": {ColorToastSuccess, ToastSuccessInk()},
			"toastWarning": {ColorToastWarn, ToastWarnInk()},
			"toastDanger":  {ColorToastError, ToastErrorInk()},
			"toastInfo":    {ColorToastInfo, ToastInfoInk()},
		} {
			fill, ink := pair[0], pair[1]
			if ink != fill {
				adjusted++
			}
			for where, bg := range surfaces {
				if r := contrastRatio(sdlRGB(ink), sdlRGB(bg)); r < 4.5 {
					t.Errorf("%s: %s ink %.2f:1 on the %s, want >= 4.5", name, label, r, where)
				}
			}
			// A white-on-everything "fix" would satisfy the ratio and lose the
			// meaning, so the ink has to keep most of the authored hue.
			if spread(fill) >= 12 && spread(ink)*3 < spread(fill) {
				t.Errorf("%s: %s ink %v flattens the authored hue %v", name, label, ink, fill)
			}
		}
	}
	t.Logf("%d of %d status inks were adjusted to reach 4.5:1", adjusted, 9*len(ListThemePresets()))
	if adjusted == 0 {
		t.Error("no preset needed adjustment: the derivation is never exercised")
	}

	// Where the palette was already correct the derivation must be a no-op, or
	// every theme drifts slightly on upgrade.
	ApplyThemeColors(themePresets["dark"])
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	if got := TextInk(white); got != white {
		t.Errorf("TextInk changed a colour that already passes: %v -> %v", white, got)
	}

	// StatusInk is the single boundary where a status tone becomes text, so
	// naming a token and naming its ink have to agree, or a panel subtitle
	// silently keeps the unreadable value.
	for _, tc := range []struct {
		name string
		src  sdl.Color
		want sdl.Color
	}{
		{"success", ColorSuccess, SuccessInk()},
		{"warning", ColorWarning, WarningInk()},
		{"danger", ColorDanger, DangerInk()},
		{"info", ColorInfo, InfoInk()},
		{"toastSuccess", ColorToastSuccess, ToastSuccessInk()},
		{"toastWarn", ColorToastWarn, ToastWarnInk()},
		{"toastError", ColorToastError, ToastErrorInk()},
		{"toastInfo", ColorToastInfo, ToastInfoInk()},
	} {
		if got := StatusInk(tc.src); got != tc.want {
			t.Errorf("StatusInk(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A neutral label colour must survive: wrapping is not a licence to retint
	// every subtitle that happens to pass through this function.
	neutral := ColorTextSecondary()
	if got := StatusInk(neutral); got != neutral {
		t.Errorf("StatusInk altered a neutral colour: %v -> %v", neutral, got)
	}
}
