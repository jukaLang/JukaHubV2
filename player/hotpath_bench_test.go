package main

// Benchmarks for the per-frame text hot path. They run headless: no renderer
// is involved, only the work every drawn string does before it reaches SDL.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

var benchAccent = sdl.Color{R: 120, G: 130, B: 255, A: 255}

// BenchmarkSubstituteVars measures the variable pass every renderText call
// makes, for the common case (plain text without any $variable).
func BenchmarkSubstituteVarsPlain(b *testing.B) {
	vars := map[string]interface{}{"wifi": "on", "battery": "80"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := substituteVars("Settings", vars); got != "Settings" {
			b.Fatalf("got %q", got)
		}
	}
}

func BenchmarkSubstituteVarsWithVar(b *testing.B) {
	vars := map[string]interface{}{"wifi": "on"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := substituteVars("Wi-Fi: $wifi", vars); got != "Wi-Fi: on" {
			b.Fatalf("got %q", got)
		}
	}
}

// BenchmarkTextCacheHit measures the cache lookup done for every visible
// string on every frame.
func BenchmarkTextCacheHit(b *testing.B) {
	cache := NewTextCache(512)
	for i := 0; i < 512; i++ {
		cache.Put(nil, string(rune('a'+i%26))+string(rune('0'+i%10)), benchAccent, nil, 40, 20)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cache.Get(nil, "a0", benchAccent) == nil {
			b.Fatal("miss")
		}
	}
}

// BenchmarkTextCacheChurn measures a miss-heavy frame: a long list where every
// row's text is new, which is what a scroll through IPTV channels looks like.
func BenchmarkTextCacheChurn(b *testing.B) {
	cache := NewTextCache(512)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		text := "Channel " + string(rune('A'+i%26))
		cache.Put(nil, text, benchAccent, nil, 120, 20)
		cache.Get(nil, text, benchAccent)
	}
}

// benchTVChannels fills the Live TV store with a realistic public-playlist
// sized library so the per-frame filter bar cost can be measured.
func benchTVChannels(b *testing.B, n int) {
	b.Helper()
	channels := make([]TVChannel, 0, n)
	index := make(map[string]TVChannel, n)
	groups := []string{"News", "Sports", "Movies", "Kids"}
	for i := 0; i < n; i++ {
		ch := TVChannel{
			Name:  "Channel " + string(rune('A'+i%26)) + " " + string(rune('a'+i%20)),
			URL:   "http://example.com/stream/" + string(rune('0'+i%10)) + ".m3u8",
			Group: groups[i%len(groups)],
		}
		channels = append(channels, ch)
		index[ch.URL+string(rune(i))] = ch
	}

	tvLib.mu.Lock()
	prevChannels, prevIndex := tvLib.channels, tvLib.index
	prevCat, prevQuery := tvLib.category, tvLib.query
	prevVisible, prevValid := tvLib.visible, tvLib.countsValid
	tvLib.channels, tvLib.index = channels, index
	tvLib.category, tvLib.query = tvAllCategory, ""
	tvLib.visible, tvLib.countsValid = 0, false
	tvLib.mu.Unlock()
	b.Cleanup(func() {
		tvLib.mu.Lock()
		tvLib.channels, tvLib.index = prevChannels, prevIndex
		tvLib.category, tvLib.query = prevCat, prevQuery
		tvLib.visible, tvLib.countsValid = prevVisible, prevValid
		tvLib.mu.Unlock()
	})
}

// BenchmarkTVFilterBarCounts is what the filter bar now costs per frame.
func BenchmarkTVFilterBarCounts(b *testing.B) {
	benchTVChannels(b, 8000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if visible, total := tvCounts(); visible != total {
			b.Fatalf("counts = %d/%d", visible, total)
		}
	}
}

// BenchmarkTVFilterAll is the old per-frame cost: copy and lower-case the
// whole playlist to learn a number.
func BenchmarkTVFilterAll(b *testing.B) {
	benchTVChannels(b, 8000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := len(tvFilteredChannels()); got != 8000 {
			b.Fatalf("filtered = %d", got)
		}
	}
}

// BenchmarkFrameTextPass simulates one frame's worth of UI labels: 40 strings
// go through variable substitution and the texture-cache lookup, which is what
// every drawn label does before it reaches SDL. It is the accounting the two
// fixes below are measured against.
func BenchmarkFrameTextPass(b *testing.B) {
	labels := make([]string, 40)
	for i := range labels {
		labels[i] = "Label " + string(rune('A'+i%26)) + " " + string(rune('a'+i%20))
	}
	cache := NewTextCache(512)
	for _, s := range labels {
		cache.Put(nil, s, benchAccent, nil, 120, 20)
	}
	vars := map[string]interface{}{"wifi": "on", "battery": "80"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, s := range labels {
			text := substituteVars(s, vars)
			if cache.Get(nil, text, benchAccent) == nil {
				b.Fatal("miss")
			}
		}
	}
}
