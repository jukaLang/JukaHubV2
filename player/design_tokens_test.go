package main

import "testing"

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
