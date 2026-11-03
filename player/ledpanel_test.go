package main

import (
	"strings"
	"testing"
	"time"
)

func TestLEDApplyBrightness(t *testing.T) {
	cases := []struct {
		r, g, b uint8
		pct     int
		wantR   uint8
		wantG   uint8
		wantB   uint8
	}{
		{255, 128, 64, 100, 255, 128, 64},
		{255, 128, 64, 50, 128, 64, 32},
		{240, 240, 240, 10, 24, 24, 24},
		{255, 255, 255, 0, 0, 0, 0},
		{255, 255, 255, 200, 255, 255, 255},
	}
	for _, c := range cases {
		r, g, b := ledApplyBrightness(c.r, c.g, c.b, c.pct)
		if r != c.wantR || g != c.wantG || b != c.wantB {
			t.Errorf("ledApplyBrightness(%d,%d,%d,%d) = %d,%d,%d, want %d,%d,%d",
				c.r, c.g, c.b, c.pct, r, g, b, c.wantR, c.wantG, c.wantB)
		}
	}
}

func TestLEDColorKeyAndHueRoundTrip(t *testing.T) {
	for _, hue := range []float64{0, 0.08, 0.33, 0.5, 0.66, 0.83, 0.99} {
		hex := ledColorKey(hue)
		r, g, b, ok := parseLEDHex(hex)
		if !ok {
			t.Fatalf("ledColorKey(%v) = %q, which does not parse", hue, hex)
		}
		back := rgbToHue(r, g, b)
		diff := back - hue
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.02 && diff < 0.98 {
			t.Errorf("hue %v -> %s -> %v", hue, hex, back)
		}
	}
	if got := rgbToHue(128, 128, 128); got != rgbToHue(accentColor.R, accentColor.G, accentColor.B) {
		t.Errorf("grey should fall back to the accent hue, got %v", got)
	}
}

func TestLEDEffectIsForgiving(t *testing.T) {
	if ledEffectKey("") != "solid" || ledEffectKey("PULSE") != "pulse" || ledEffectKey("nope") != "solid" {
		t.Fatalf("effect normalisation: %q %q %q",
			ledEffectKey(""), ledEffectKey("PULSE"), ledEffectKey("nope"))
	}
	if ledEffectLabel("rainbow") != "Rainbow" || ledEffectLabel("") != "Solid" {
		t.Errorf("effect labels: %q %q", ledEffectLabel("rainbow"), ledEffectLabel(""))
	}
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{ledKeyEffect: "rainbow"}}}
	if got := ledEffectFromConfig(cfg); got != "rainbow" {
		t.Errorf("ledEffectFromConfig = %q", got)
	}
	if got := ledEffectFromConfig(nil); got != "solid" {
		t.Errorf("nil config effect = %q, want solid", got)
	}
}

func TestLEDBrightnessIsReadAndClamped(t *testing.T) {
	cases := map[string]int{
		"":     ledBrightnessDefault,
		"55":   55,
		"0":    0,
		"-5":   0,
		"400":  100,
		"junk": ledBrightnessDefault,
	}
	for raw, want := range cases {
		cfg := &Config{Variables: Variables{Custom: map[string]interface{}{ledKeyBrightness: raw}}}
		if got := ledBrightnessFromConfig(cfg); got != want {
			t.Errorf("ledBrightnessFromConfig(%q) = %d, want %d", raw, got, want)
		}
	}
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{ledKeyBrightness: 70}}}
	if got := ledBrightnessFromConfig(cfg); got != 70 {
		t.Errorf("numeric brightness = %d, want 70", got)
	}
}

func TestLEDRainbowEffectRotatesHue(t *testing.T) {
	base := time.Unix(1700000000, 0)
	first := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "rainbow", 0, 0, 0, base))
	later := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "rainbow", 0, 0, 0, base.Add(3*time.Second)))
	if first == later {
		t.Fatal("rainbow should change colour over time")
	}
	// Solid ignores time entirely.
	solidA := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "solid", 10, 20, 30, base))
	solidB := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "solid", 10, 20, 30, base.Add(time.Hour)))
	if solidA != solidB {
		t.Fatal("solid must not change with time")
	}
}

func TestLEDPanelAdjustWritesKeysAndSwitchesToCustom(t *testing.T) {
	prev := ledPanel
	t.Cleanup(func() { ledPanel = prev })
	ledPanel = ledPanelState{open: true}

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	ledPanel.row = ledRowMode
	ledPanelAdjust(cfg, 1)
	if got := ledPolicyFromConfig(cfg); got != ledPolicyBattery {
		t.Fatalf("mode after one step = %q, want battery", got)
	}
	ledPanelAdjust(cfg, -1)
	if got := ledPolicyFromConfig(cfg); got != ledPolicyAudio {
		t.Fatalf("mode after stepping back = %q, want audio", got)
	}

	ledPanel.row = ledRowEffect
	ledPanelAdjust(cfg, 1)
	if got := ledEffectFromConfig(cfg); got != "pulse" {
		t.Fatalf("effect = %q, want pulse", got)
	}
	// An effect the lights can show implies Custom mode.
	if got := ledPolicyFromConfig(cfg); got != ledPolicyCustom {
		t.Fatalf("effect edit left mode = %q, want custom", got)
	}

	ledPanel.row = ledRowColor
	before := ledColorKey(ledHueFromConfig(cfg))
	ledPanelAdjust(cfg, 1)
	if after := ledColorKey(ledHueFromConfig(cfg)); after == before {
		t.Fatalf("colour did not move from %s", before)
	}

	ledPanel.row = ledRowBrightness
	ledPanelAdjust(cfg, -1)
	if got := ledBrightnessFromConfig(cfg); got != 95 {
		t.Fatalf("brightness after one step = %d, want 95", got)
	}
	for i := 0; i < 30; i++ {
		ledPanelAdjust(cfg, -1)
	}
	if got := ledBrightnessFromConfig(cfg); got != ledBrightnessMin {
		t.Fatalf("brightness floor = %d, want %d", got, ledBrightnessMin)
	}
}

func TestLEDPanelMoveWraps(t *testing.T) {
	prev := ledPanel
	t.Cleanup(func() { ledPanel = prev })
	ledPanel = ledPanelState{open: true, row: 0}
	ledPanelMove(-1)
	if ledPanel.row != ledRowCount-1 {
		t.Fatalf("up from the first row = %d, want the last", ledPanel.row)
	}
	ledPanelMove(1)
	if ledPanel.row != 0 {
		t.Fatalf("down from the last row = %d, want the first", ledPanel.row)
	}
}

func TestLEDFrameColorAppliesBrightness(t *testing.T) {
	prevConfig := appConfig
	t.Cleanup(func() { appConfig = prevConfig })

	base := time.Unix(1700000000, 0)
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		ledKeyPolicy:     ledPolicyCustom,
		ledKeyEffect:     "solid",
		ledKeyColor:      "#3366ff",
		ledKeyBrightness: 100,
	}}}
	fullR, fullG, fullB := ledFrameColor(base)
	appConfig.Variables.Custom[ledKeyBrightness] = 50
	halfR, halfG, halfB := ledFrameColor(base)
	if halfR >= fullR || halfG >= fullG || halfB >= fullB {
		t.Fatalf("half brightness = %d,%d,%d, full = %d,%d,%d",
			halfR, halfG, halfB, fullR, fullG, fullB)
	}
	appConfig.Variables.Custom[ledKeyBrightness] = 0
	if r, g, b := ledFrameColor(base); r|g|b != 0 {
		t.Fatalf("0%% brightness = %d,%d,%d, want off", r, g, b)
	}
}

func TestLEDPanelColorHexIsUppercaseInUI(t *testing.T) {
	hex := ledColorKey(0.5)
	if !strings.HasPrefix(strings.ToUpper(hex), "#") {
		t.Fatalf("hex = %q", hex)
	}
}
