package main

// ledpolicy_test.go — the non-audio LED policies. They are pure colour maths
// over device state (battery, charging, temperature) plus a custom colour, so
// they can be pinned without a device or a running loop.

import (
	"testing"
	"time"
)

func dominant(c sdlColorLike) string {
	switch {
	case c.r > c.g && c.r > c.b:
		return "red"
	case c.g >= c.r && c.g > c.b:
		return "green"
	case c.b >= c.r && c.b > c.g:
		return "blue"
	}
	return "even"
}

// sdlColorLike keeps the assertions readable across the three uint8 returns.
type sdlColorLike struct{ r, g, b uint8 }

func asColorLike(r, g, b uint8) sdlColorLike { return sdlColorLike{r, g, b} }

func TestLEDBatteryPolicyRunsRedToGreen(t *testing.T) {
	now := time.Unix(0, 0)
	low := asColorLike(ledPolicyColor(ledPolicyBattery, 5, false, 0, "", 0, 0, 0, now))
	high := asColorLike(ledPolicyColor(ledPolicyBattery, 95, false, 0, "", 0, 0, 0, now))

	if dominant(low) != "red" {
		t.Fatalf("5%% battery is %+v, want red", low)
	}
	if dominant(high) != "green" {
		t.Fatalf("95%% battery is %+v, want green", high)
	}
	// Out-of-range readings clamp instead of wrapping the hue.
	if got := asColorLike(ledPolicyColor(ledPolicyBattery, -20, false, 0, "", 0, 0, 0, now)); dominant(got) != "red" {
		t.Fatalf("negative battery is %+v, want red", got)
	}
}

// Charging is dark when the cable is out and breathes when it is in.
func TestLEDChargingPolicyBreathesOnlyWhileCharging(t *testing.T) {
	if r, g, b := ledPolicyColor(ledPolicyCharging, 50, false, 0, "", 0, 0, 0, time.Now()); r|g|b != 0 {
		t.Fatalf("unplugged charging policy lit %d,%d,%d", r, g, b)
	}
	base := time.Unix(0, 0)
	dark := asColorLike(ledPolicyColor(ledPolicyCharging, 50, true, 0, "", 0, 0, 0, base))
	bright := asColorLike(ledPolicyColor(ledPolicyCharging, 50, true, 0, "", 0, 0, 0, base.Add(500*time.Millisecond)))
	if dominant(dark) != "green" {
		t.Fatalf("charging colour is %+v, want green", dark)
	}
	if bright.g == dark.g {
		t.Fatalf("charging pulse did not move: %d then %d", dark.g, bright.g)
	}
}

func TestLEDTemperaturePolicyRunsGreenToRed(t *testing.T) {
	now := time.Unix(0, 0)
	cool := asColorLike(ledPolicyColor(ledPolicyTemperature, 50, false, 42, "", 0, 0, 0, now))
	hot := asColorLike(ledPolicyColor(ledPolicyTemperature, 50, false, 90, "", 0, 0, 0, now))
	if dominant(cool) != "green" {
		t.Fatalf("42 C is %+v, want green", cool)
	}
	if dominant(hot) != "red" {
		t.Fatalf("90 C is %+v, want red", hot)
	}
}

func TestLEDCustomPolicyUsesColourAndEffect(t *testing.T) {
	r, g, b, ok := parseLEDHex("#3366FF")
	if !ok || r != 0x33 || g != 0x66 || b != 0xFF {
		t.Fatalf("parseLEDHex(#3366FF) = %d,%d,%d,%v", r, g, b, ok)
	}
	if _, _, _, ok := parseLEDHex("oops"); ok {
		t.Fatal("a non-hex colour was accepted")
	}

	base := time.Unix(0, 0)
	solid := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "solid", r, g, b, base))
	if solid != (sdlColorLike{0x33, 0x66, 0xFF}) {
		t.Fatalf("solid custom colour = %+v, want the stored RGB", solid)
	}
	dark := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "pulse", r, g, b, base))
	bright := asColorLike(ledPolicyColor(ledPolicyCustom, 0, false, 0, "pulse", r, g, b, base.Add(500*time.Millisecond)))
	if bright.b <= dark.b {
		t.Fatalf("pulse did not breathe: %d then %d", dark.b, bright.b)
	}
}

func TestLEDPolicyStorageIsForgiving(t *testing.T) {
	if ledPolicyKey("nonsense") != ledPolicyAudio {
		t.Fatal("an unknown policy must fall back to audio")
	}
	if ledPolicyLabel("battery") != "Battery" || ledPolicyLabel("") != "Audio" {
		t.Fatal("policy labels are wrong")
	}
	seen := map[string]bool{}
	key := ledPolicyAudio
	for i := 0; i < len(ledPolicyOrder); i++ {
		seen[key] = true
		key = nextLEDPolicy(key, 1)
	}
	if key != ledPolicyAudio || len(seen) != len(ledPolicyOrder) {
		t.Fatalf("cycling visited %v and ended at %q", seen, key)
	}

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{ledKeyPolicy: "temperature"}}}
	if got := ledPolicyFromConfig(cfg); got != ledPolicyTemperature {
		t.Fatalf("stored policy = %q", got)
	}
}
