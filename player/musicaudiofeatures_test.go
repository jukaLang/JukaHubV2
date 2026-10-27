package main

// musicaudiofeatures_test.go — the equaliser, the bit-perfect path, the two Now
// Playing layouts and the Audio settings panel.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestMusicPEQFilterGraph(t *testing.T) {
	var flat [musicPEQBands]float64
	if got := musicPEQFilterGraph(flat); got != "" {
		t.Fatalf("flat graph = %q, want empty", got)
	}

	var gains [musicPEQBands]float64
	gains[0] = 3 // 20 Hz
	gains[17] = -6
	graph := musicPEQFilterGraph(gains)
	if !strings.Contains(graph, "equalizer=f=20:t=q:w=1:g=3.0") {
		t.Errorf("graph %q is missing the 20 Hz band", graph)
	}
	if !strings.Contains(graph, "equalizer=f=1000:t=q:w=1:g=-6.0") {
		t.Errorf("graph %q is missing the 1 kHz band", graph)
	}
	if n := strings.Count(graph, "equalizer="); n != 2 {
		t.Errorf("graph has %d bands, want 2: %q", n, graph)
	}

	// A near-flat band costs nothing on a handheld CPU.
	var tiny [musicPEQBands]float64
	tiny[4] = 0.1
	if got := musicPEQFilterGraph(tiny); got != "" {
		t.Errorf("near-flat graph = %q, want empty", got)
	}

	// Out-of-range gains are clamped rather than handed to ffmpeg.
	var loud [musicPEQBands]float64
	loud[1] = 40
	if got := musicPEQFilterGraph(loud); !strings.Contains(got, "g=12.0") {
		t.Errorf("clamped graph = %q, want g=12.0", got)
	}
}

func TestMusicAudioFilterChain(t *testing.T) {
	var gains [musicPEQBands]float64
	gains[10] = 4

	if got := musicAudioFilterChain(1, false, false, gains); got != "" {
		t.Errorf("EQ off, speed 1 -> %q, want empty", got)
	}
	if got := musicAudioFilterChain(1.5, false, false, gains); got != "atempo=1.50" {
		t.Errorf("speed only -> %q", got)
	}
	got := musicAudioFilterChain(1.5, true, false, gains)
	if !strings.HasPrefix(got, "equalizer=") || !strings.HasSuffix(got, ",atempo=1.50") {
		t.Errorf("EQ then tempo -> %q", got)
	}
	// Bit-perfect beats everything, including the equaliser and the tempo.
	if got := musicAudioFilterChain(2, true, true, gains); got != "" {
		t.Errorf("bit-perfect -> %q, want empty", got)
	}
}

func TestMusicFFmpegArgsUsesTheChain(t *testing.T) {
	prev := musicEQ
	defer func() { musicEQ = prev }()

	musicEQ.enabled = true
	musicEQ.bitPerfect = false
	musicEQ.gains[17] = 5
	args := strings.Join(musicFFmpegArgs("/music/a.flac", 0, 1, 48000), " ")
	if !strings.Contains(args, "-af equalizer=f=1000:t=q:w=1:g=5.0") {
		t.Fatalf("args = %q, want the PEQ band", args)
	}

	musicEQ.bitPerfect = true
	args = strings.Join(musicFFmpegArgs("/music/a.flac", 0, 2, 48000), " ")
	if strings.Contains(args, "-af") {
		t.Fatalf("bit-perfect args = %q, want no filter graph", args)
	}
}

func TestMusicPEQPresetsAndMatch(t *testing.T) {
	var flat [musicPEQBands]float64
	if got := musicPEQMatchPreset(flat); got != "Flat" {
		t.Errorf("flat curve is %q, want Flat", got)
	}
	bass := musicPEQPresetGains("Bass")
	if bass[0] <= 0 || bass[musicPEQBands-1] >= 0 {
		t.Errorf("bass curve %v should lift lows and trim highs", bass)
	}
	if got := musicPEQMatchPreset(bass); got != "Bass" {
		t.Errorf("bass curve is named %q", got)
	}
	name, gains := musicPEQCyclePreset("Night")
	if name != "Flat" || gains != flat {
		t.Errorf("Night -> %q, want the cycle to wrap to Flat", name)
	}
	name, _ = musicPEQCyclePreset("fl at")
	if name != "Flat" {
		t.Errorf("unknown preset -> %q, want Flat", name)
	}
	name, _ = musicPEQCyclePreset("Bass")
	if name != "Vocal" {
		t.Errorf("Bass -> %q, want Vocal", name)
	}
}

func TestMusicPEQGainsRoundTrip(t *testing.T) {
	var gains [musicPEQBands]float64
	for i := range gains {
		gains[i] = float64(i%7) - 3
	}
	raw := formatMusicPEQGains(gains)
	parsed, ok := parseMusicPEQGains(raw)
	if !ok {
		t.Fatalf("parse(%q) failed", raw)
	}
	if parsed != gains {
		t.Errorf("round trip changed the curve: %v -> %v", gains, parsed)
	}

	for _, bad := range []string{"", "1,2,3", "0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,x,0,0,0,0,0,0,0,0,0,0,0,0,0"} {
		if _, ok := parseMusicPEQGains(bad); ok {
			t.Errorf("parse(%q) accepted a malformed curve", bad)
		}
	}
	// Values outside the sliders are clamped, not rejected: a hand-edited
	// config should still make a sound.
	parts := make([]string, musicPEQBands)
	for i := range parts {
		parts[i] = "99"
	}
	parsed, ok = parseMusicPEQGains(strings.Join(parts, ","))
	if !ok || parsed[0] != musicPEQMaxGain {
		t.Errorf("clamped parse = %v, %v", parsed[0], ok)
	}
}

func TestMusicPEQLoadAndSave(t *testing.T) {
	prev := musicEQ
	prevLoaded := musicPEQLoaded
	defer func() {
		musicEQ = prev
		musicPEQLoaded = prevLoaded
	}()

	var gains [musicPEQBands]float64
	gains[2] = 6
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		musicPEQOnKey:      "true",
		musicPEQGainsKey:   formatMusicPEQGains(gains),
		musicBitPerfectKey: "false",
	}
	musicPEQLoad(config)
	if !musicEQ.enabled || musicEQ.gains[2] != 6 {
		t.Fatalf("loaded state = %+v", musicEQ)
	}

	musicEQ.bitPerfect = true
	musicPEQSave(config)
	if config.Variables.Custom[musicBitPerfectKey] != true {
		t.Errorf("bit-perfect was not saved: %v", config.Variables.Custom[musicBitPerfectKey])
	}
	if _, ok := parseMusicPEQGains(customStringValue(config.Variables.Custom, musicPEQGainsKey)); !ok {
		t.Error("saved curve does not parse back")
	}

	// Bit-perfect wins over a stale "EQ on" flag: the two cannot both be on.
	config.Variables.Custom[musicPEQOnKey] = "true"
	config.Variables.Custom[musicBitPerfectKey] = "true"
	musicPEQLoad(config)
	if musicEQ.enabled && musicEQ.bitPerfect {
		t.Error("EQ and bit-perfect are both on")
	}
}

func TestMusicPEQStatusLine(t *testing.T) {
	prev := musicEQ
	defer func() { musicEQ = prev }()

	musicEQ = musicPEQState{preset: "Bass"}
	if got := musicPEQStatus(); got != "EQ off" {
		t.Errorf("off status = %q", got)
	}
	musicEQ.enabled = true
	if got := musicPEQStatus(); got != "PEQ Bass" {
		t.Errorf("on status = %q", got)
	}
	musicEQ.bitPerfect = true
	if got := musicPEQStatus(); got != "Bit-perfect" {
		t.Errorf("bit-perfect status = %q", got)
	}
}

func TestMusicLayoutConfigRoundTrip(t *testing.T) {
	config := &Config{}
	if got := musicLayoutFromConfig(config); got != musicLayoutAuralis {
		t.Errorf("default layout = %v, want Auralis", got)
	}
	musicSetLayout(config, musicLayoutStockOS)
	if got := musicLayoutFromConfig(config); got != musicLayoutStockOS {
		t.Errorf("saved layout = %v, want StockOS", got)
	}
	if musicLayout(musicLayoutStockOS).label() != "StockOS" {
		t.Error("StockOS label is wrong")
	}
	config.Variables.Custom[musicLayoutKey] = "nonsense"
	if got := musicLayoutFromConfig(config); got != musicLayoutAuralis {
		t.Errorf("unknown layout = %v, want the Auralis fallback", got)
	}
	if musicLayoutFromConfig(nil) != musicLayoutAuralis {
		t.Error("nil config should fall back to Auralis")
	}
}

func TestMusicArtOpacity(t *testing.T) {
	config := &Config{}
	if got := musicArtOpacityPercent(config); got != 100 {
		t.Errorf("default opacity = %d, want 100", got)
	}
	musicSetArtOpacity(config, 40)
	if got := musicArtOpacityPercent(config); got != 40 {
		t.Errorf("saved opacity = %d", got)
	}
	musicSetArtOpacity(config, 5)
	if got := musicArtOpacityPercent(config); got != 10 {
		t.Errorf("low opacity = %d, want 10", got)
	}
	musicSetArtOpacity(config, 500)
	if got := musicArtOpacityPercent(config); got != 100 {
		t.Errorf("high opacity = %d, want 100", got)
	}
	if musicArtAlpha(100) != 255 {
		t.Error("100% should be fully opaque")
	}
	if musicArtAlpha(10) >= musicArtAlpha(50) {
		t.Error("opacity does not scale alpha")
	}
}

func TestMusicBackgroundSetting(t *testing.T) {
	config := &Config{}
	if !musicBackgroundEnabled(config) {
		t.Error("background playback should default to on")
	}
	musicSetBackgroundEnabled(config, false)
	if musicBackgroundEnabled(config) {
		t.Error("background playback did not turn off")
	}
	if musicSettingsBackgroundNote(config) != "Background playback: OFF" {
		t.Errorf("note = %q", musicSettingsBackgroundNote(config))
	}
}

func TestMusicScreenModeSettingRoundTrip(t *testing.T) {
	config := &Config{}
	for _, mode := range []musicScreenMode{musicScreenAny, musicScreenControls, musicScreenSelect} {
		musicSetScreenMode(config, mode)
		if got := musicScreenModeFromConfig(config); got != mode {
			t.Errorf("%v saved as %v", mode, got)
		}
	}
}

// The settings rows must all report a value: an empty right-hand column reads as
// a broken row.
func TestMusicSettingsRowsHaveValues(t *testing.T) {
	prev := musicEQ
	defer func() { musicEQ = prev }()
	config := &Config{}
	for _, row := range musicSettingsRows() {
		if row.label == "" || row.hint == "" {
			t.Errorf("row %v is missing text", row.kind)
		}
		if row.kind != msRowShortcuts && musicSettingsValue(row, config) == "" {
			t.Errorf("row %q has no value", row.label)
		}
	}
	if len(musicSettingsRows()) != 7 {
		t.Errorf("got %d rows, want 7", len(musicSettingsRows()))
	}
}

// Changing a row from the panel persists immediately, so a power cut does not
// lose the choice.
func TestMusicSettingsAdjustPersists(t *testing.T) {
	prev := musicEQ
	prevLoaded := musicPEQLoaded
	defer func() {
		musicEQ = prev
		musicPEQLoaded = prevLoaded
	}()
	config := &Config{}

	rows := musicSettingsRows()
	musicSetLayout(config, musicLayoutAuralis)
	musicSettingsAdjust(rows[0], 1, config)
	if musicLayoutFromConfig(config) != musicLayoutStockOS {
		t.Error("layout row did not switch to StockOS")
	}
	musicSettingsAdjust(rows[0], -1, config)
	if musicLayoutFromConfig(config) != musicLayoutAuralis {
		t.Error("layout row did not switch back")
	}

	musicSetArtOpacity(config, 50)
	musicSettingsAdjust(rows[1], 1, config)
	if got := musicArtOpacityPercent(config); got != 60 {
		t.Errorf("opacity after one step = %d, want 60", got)
	}

	musicSettingsAdjust(rows[2], 1, config)
	if got := musicScreenModeFromConfig(config); got != musicScreenControls {
		t.Errorf("screen mode after one step = %v", got)
	}
}

// StockOS replaces the whole body: it has to publish its own hitboxes and draw
// real pixels, otherwise clicks and the list go dead in that layout.
func TestMusicStockOSLayoutRenders(t *testing.T) {
	resetMusicState(t)
	prev := musicEQ.panel
	musicEQ.panel = false
	t.Cleanup(func() { musicEQ.panel = prev })
	musicLoadPlaylist(musicTestFiles(t), "")

	config := &Config{}
	musicSetLayout(config, musicLayoutStockOS)

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMusicPlayer(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	if musicRects.playPause.W == 0 || musicRects.seek.W == 0 {
		t.Fatalf("StockOS did not publish its transport rects: %+v", musicRects)
	}
	if len(musicRects.rows) == 0 {
		t.Fatal("StockOS drew no playlist rows")
	}
	row := musicRects.rows[0]
	if n := overlayCountNear(f, ColorTextPrimary(), row.X+40, row.Y, row.X+row.W-20, row.Y+row.H, 70); n == 0 {
		t.Error("StockOS row has no track name")
	}
}

// The PEQ panel and the settings panel both draw over a live frame without
// touching a nil font or a zero-sized card.
func TestMusicPanelsRender(t *testing.T) {
	resetMusicState(t)
	musicLoadPlaylist(musicTestFiles(t), "")
	prevEQ := musicEQ
	t.Cleanup(func() { musicEQ = prevEQ })
	musicEQ.panel = true

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	config := &Config{}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMusicPlayer(renderer, config)
	if !musicEQ.panel {
		t.Fatal("the PEQ panel closed itself")
	}
	if !HandleMusicPEQClick(musicPEQBandRect(musicPEQPanelRect(), 3).X+2, musicPEQBandRect(musicPEQPanelRect(), 3).Y+2) {
		t.Error("a tap on a band was not consumed by the panel")
	}
	if musicEQ.sel != 3 {
		t.Errorf("tap selected band %d, want 3", musicEQ.sel)
	}

	musicEQ.panel = false
	openMusicSettingsPanel(config)
	if !musicSettings.open {
		t.Fatal("the Audio settings panel did not open")
	}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMusicSettingsPanel(renderer, config)
	if len(musicSettings.rowHits) == 0 {
		t.Error("the Audio settings panel published no rows")
	}
	closeMusicSettingsPanel()
}

// saveProbePNG writes one rendered probe frame to tmp/ so the layout can be
// reviewed without a window.
func saveProbePNG(t *testing.T, f *probeFrame, name string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, int(probeW), int(probeH)))
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			i := (y*probeW + x) * 4
			c := f.buf[i : i+4]
			img.SetRGBA(int(x), int(y), color.RGBA{R: c[2], G: c[1], B: c[0], A: 255})
		}
	}
	out := filepath.Join("..", "tmp", name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
}

// TestMusicLayoutPreviews writes one PNG per new screen for design review. It
// only runs with JUKA_PREVIEW=1 so the normal suite stays headless.
func TestMusicLayoutPreviews(t *testing.T) {
	if os.Getenv("JUKA_PREVIEW") == "" {
		t.Skip("set JUKA_PREVIEW=1 to write the audio layout previews")
	}
	resetMusicState(t)
	prev := musicEQ
	t.Cleanup(func() { musicEQ = prev })
	musicLoadPlaylist(musicTestFiles(t), "")
	musicEQ.panel = false

	render := func(config *Config) *probeFrame {
		f := newProbeFrame(t)
		renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
		}
		defer destroyTestRenderer(renderer)
		ClearTextCache()
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		renderMusicPlayer(renderer, config)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
		return f
	}

	auralis := &Config{}
	saveProbePNG(t, render(auralis), "audioplayer-auralis.png")

	musicSetArtOpacity(auralis, 40)
	saveProbePNG(t, render(auralis), "audioplayer-auralis-opacity40.png")

	stock := &Config{}
	musicSetLayout(stock, musicLayoutStockOS)
	saveProbePNG(t, render(stock), "audioplayer-stockos.png")

	musicEQ.enabled = true
	musicEQ.preset = "Rock"
	musicEQ.gains = musicPEQPresetGains("Rock")
	musicEQ.panel = true
	saveProbePNG(t, render(auralis), "audioplayer-peq.png")

	musicEQ.panel = false
	openMusicSettingsPanel(auralis)
	saveProbePNG(t, render(auralis), "audioplayer-settings.png")
	closeMusicSettingsPanel()
}
