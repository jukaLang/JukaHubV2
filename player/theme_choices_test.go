package main

import "testing"

func TestThemeGalleryHidesRepeatedPaletteLayoutCombinations(t *testing.T) {
	if len(themeGalleryTabs) != 3 || themeGalleryTabs[1].Group != themeTabStyle {
		t.Fatalf("gallery tabs = %+v, want Colour, Styles, Experience", themeGalleryTabs)
	}
	styles := themeTabPresets(themeGalleryTabs[1])
	listed := make(map[string]bool, len(styles))
	for _, name := range styles {
		if listed[name] {
			t.Fatalf("style %q appears twice", name)
		}
		listed[name] = true
	}
	for _, spec := range themeConversionSpecs {
		base := themePresets[spec.palette]
		ownGlow := (spec.bloom != "" && spec.bloom != base.Bloom) ||
			(spec.bloom2 != "" && spec.bloom2 != base.Bloom2)
		if got, want := listed[spec.key], themePresetIsStyle(spec.key) || ownGlow; got != want {
			t.Errorf("style %q listed = %v, want %v", spec.key, got, want)
		}
		if _, ok := themePresets[spec.key]; !ok {
			t.Errorf("saved theme ID %q no longer resolves", spec.key)
		}
	}
	for _, spec := range themeRetroConversions {
		if !listed[spec.key] {
			t.Errorf("distinct console style %q is missing", spec.key)
		}
	}
	if !themePresetVisible("my-theme", ThemePreset{Kind: themeKindConversion}) {
		t.Error("user-authored conversion themes must remain selectable")
	}
}

func TestTourThemeAndLayoutChoicesStayIndependent(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range onboardingThemeChoices {
		if seen[name] {
			t.Errorf("colour %q appears twice", name)
		}
		seen[name] = true
		p := GetThemePreset(name)
		if p.Kind != themeKindColour || p.Arrangement != "" {
			t.Errorf("tour colour %q changes Home layout", name)
		}
	}
	if len(homeArrangementChoiceList) != len(onboardingLayoutNotes) {
		t.Fatalf("layout choices = %d, notes = %d", len(homeArrangementChoiceList), len(onboardingLayoutNotes))
	}
	for i, name := range homeArrangementChoiceList {
		config := &Config{Variables: Variables{Custom: map[string]interface{}{"homeArrangement": name}}}
		onboardingLayoutReset(config)
		if onbLayout.cursor != i {
			t.Errorf("saved layout %q highlighted row %d, want %d", name, onbLayout.cursor, i)
		}
		next := (i + 1) % len(homeArrangementChoiceList)
		onbLayout.cursor = next
		onboardingLayoutCommit(config)
		if got := config.Variables.Custom["homeArrangement"]; got != homeArrangementChoiceList[next] {
			t.Errorf("chosen layout = %v, want %q", got, homeArrangementChoiceList[next])
		}
	}
}
