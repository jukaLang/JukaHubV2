package main

import (
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

func TestFooterPlanCacheFollowsHintsAndCanvas(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	oldKeyboard := hintKeyboardNow.Load()
	t.Cleanup(func() {
		screenWidth, screenHeight = oldW, oldH
		hintKeyboardNow.Store(oldKeyboard)
		footerLayoutCache.valid = false
		footerLayoutCache.hints = nil
	})
	screenWidth, screenHeight = 1280, 720
	footerLayoutCache.valid = false
	hintKeyboardNow.Store(false)

	config := &Config{}
	hints := []FooterHint{{Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
	first := cachedFooterPlan(config, hints, 400)
	if first.font == nil || len(first.badgeW) != 2 {
		t.Fatal("footer did not produce a measured plan")
	}
	again := cachedFooterPlan(config, hints, 400)
	if &again.badgeW[0] != &first.badgeW[0] {
		t.Fatal("unchanged hints were measured again")
	}

	hints[0].Label = "Choose a different action"
	changed := cachedFooterPlan(config, hints, 400)
	if changed.hints[0].Label != hints[0].Label || &changed.badgeW[0] == &first.badgeW[0] {
		t.Fatal("changed hint reused the old layout")
	}

	_ = cachedFooterPlan(config, hints, 200)
	if footerLayoutCache.avail != 200 {
		t.Fatal("canvas width change kept the previous layout")
	}
	hintKeyboardNow.Store(true)
	_ = cachedFooterPlan(config, hints, 200)
	if footerLayoutCache.vocab != hintVocabKeyboard {
		t.Fatal("keyboard hints kept the controller layout")
	}
}

func TestHomeFooterWidthsFollowHintsAndInput(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	oldKeyboard := hintKeyboardNow.Load()
	t.Cleanup(func() {
		hintKeyboardNow.Store(oldKeyboard)
		homeFooterWidthCache.valid = false
	})
	hintKeyboardNow.Store(false)
	homeFooterWidthCache.valid = false
	font := getFontSized(&Config{}, "small", footerHintSize)
	if font == nil {
		t.Fatal("home footer font was unavailable")
	}
	hints := []footerHint{{button: "A", label: "Open"}, {button: "B", label: "Back"}}
	_, _, first := homeFooterWidths(font, hints, 1280, 720)
	_, _, again := homeFooterWidths(font, hints, 1280, 720)
	if first != again {
		t.Fatal("same home hints changed width")
	}
	hints[0].label = "Open a different view"
	_, _, changed := homeFooterWidths(font, hints, 1280, 720)
	if changed <= first {
		t.Fatal("changed home hint kept the old width")
	}
	hintKeyboardNow.Store(true)
	_, _, _ = homeFooterWidths(font, hints, 1280, 720)
	if homeFooterWidthCache.vocab != hintVocabKeyboard {
		t.Fatal("keyboard hints kept the controller widths")
	}
	_, _, _ = homeFooterWidths(font, hints, 960, 720)
	if homeFooterWidthCache.width != 960 {
		t.Fatal("canvas width change kept the old widths")
	}
}

func BenchmarkFooterPlan(b *testing.B) {
	if err := ttf.Init(); err != nil {
		b.Fatalf("initialize SDL_ttf: %v", err)
	}
	config := &Config{}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: "Back"},
		{Button: "L1/R1", Label: "Page"},
	}
	const avail = int32(700)
	b.Run("recompute", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = planFooterHints(config, hints, avail)
		}
	})
	b.Run("cached", func(b *testing.B) {
		_ = cachedFooterPlan(config, hints, avail)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = cachedFooterPlan(config, hints, avail)
		}
	})
}

func BenchmarkHomeFooterWidths(b *testing.B) {
	if err := ttf.Init(); err != nil {
		b.Fatalf("initialize SDL_ttf: %v", err)
	}
	font := getFontSized(&Config{}, "small", footerHintSize)
	if font == nil {
		b.Fatal("home footer font was unavailable")
	}
	hints := homeFooterHintsStart[:6]
	b.Run("recompute", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			homeFooterWidthCache.valid = false
			_, _, _ = homeFooterWidths(font, hints, 1280, 720)
		}
	})
	b.Run("cached", func(b *testing.B) {
		_, _, _ = homeFooterWidths(font, hints, 1280, 720)
		for i := 0; i < b.N; i++ {
			_, _, _ = homeFooterWidths(font, hints, 1280, 720)
		}
	})
}
