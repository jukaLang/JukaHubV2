package main

// memory_growth_test.go — does navigating the shell leak textures?
//
// One frame's texture bill says nothing about a leak: the caches are supposed
// to fill. What matters is whether the *third* walk over the same scenes costs
// the same as the fourth. This walks every scene of the shipped handheld config
// several times on one renderer and samples the accounting perf.go already
// keeps (collectTextureMemoryStats, ShapeCacheStats, IconAtlasStats, the label
// caches, the fit/measure/substitution caches) plus the live Go heap after each
// full pass. A cache that grows per pass is a leak; one that stops growing has
// warmed up.
//
// Each pass also opens the Games library on a synthetic card and presses Down
// through it, so the box-art cache is measured while it is being driven the way
// the panel drives it. Its bound has a unit test of its own, but that test calls
// rememberArt directly: it cannot see whether a real walk keeps the cache at
// the bound, or whether the frame between two presses pushes it past. The
// fixture writes a cover per game, and enough games that the set is larger than
// the budget, so the walk has to evict instead of merely filling.
//
// The library phase runs on the handheld ceiling (32 MB) on every host, not on
// whichever branch the machine happens to take. That ceiling is half the
// desktop one, so it changes both the fixture and the eviction pressure: left to
// the host, a developer on Windows measured 96 MB while CI's Linux runner — and
// the device itself — measured 32 MB, and the path that ships went unverified
// on the machine the work was done on. The branch is forced here, so the same
// test measures the same 32 MB path under the -race job as on the card.
//
// The other three columns of that table are pinned the same way. Rendered text,
// home labels and shapes each have a handheld and a desktop ceiling, and left to
// the host a desktop run measured 64/48/48 MB for them where the card ships
// 24/8/12: the table said "scene navigation" while three of its numbers
// described the machine it was run on. forceHandheldCeilings() moves those live
// caches onto the handheld branch before the first pass and restores the derived
// one after, so every column of a printed table is a shipping limit.
//
//	go test -run TestSceneNavigationMemory -v

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// navMemSample is one round's accounting. Bytes unless the name says entries.
type navMemSample struct {
	round      int
	textCache  int64
	textLen    int
	homeLabels int64
	shapes     int64
	thumbs     int64
	videoOv    int64
	uiLayer    int64
	iconCopies int64
	iconBakes  int64
	iconDirect int64
	fits       int
	measures   int
	sanitize   int
	// artCount/artBytes are the Games library's box-art cache, sampled the same
	// way: entries held and the RGBA bytes they account for.
	artCount int
	artBytes int64
	heapLive uint64
	// rss is the whole process's resident set: the only place a native
	// surface or texture leak shows up, since the Go heap cannot see cgo
	// allocations. 0 when the platform has no reader.
	rss uint64
}

func (s navMemSample) textures() int64 {
	return s.textCache + s.homeLabels + s.shapes + s.thumbs + s.videoOv + s.uiLayer
}

// sceneOwnedBytes is the part of the texture bill a scene pass fully decides:
// the rendered-text cache, the home label cache and the shape cache. The
// others (thumbnails, decoded video, the compositing layer) come and go with
// work that is not scene painting.
func (s navMemSample) sceneOwnedBytes() int64 {
	return s.textCache + s.homeLabels + s.shapes
}

// sampleNavMemory reads every cache the player knows about. runtime.GC runs
// first so heapLive is live data rather than garbage nobody has swept yet.
func sampleNavMemory(round int) navMemSample {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	stats := collectTextureMemoryStats()
	_, _, shapes := ShapeCacheStats()
	copies, bakes, direct := IconAtlasStats()
	_, _, fits := textFitStats()
	_, measures := textMeasureStats()
	_, sanitize := sanitizeStats()

	textEntries := 0
	if cache := GetTextCache(); cache != nil {
		textEntries = cache.Len()
	}
	return navMemSample{
		round:      round,
		textCache:  stats.TextCache,
		textLen:    textEntries,
		homeLabels: stats.HomeText,
		shapes:     shapes,
		thumbs:     stats.Thumbnails,
		videoOv:    stats.VideoOverlay,
		uiLayer:    stats.UILayer,
		iconCopies: copies,
		iconBakes:  bakes,
		iconDirect: direct,
		fits:       fits,
		measures:   measures,
		sanitize:   sanitize,
		artCount:   len(gameLib.artCache),
		artBytes:   gameLib.artBytes,
		heapLive:   m.HeapAlloc,
		rss:        currentProcessRSSBytes(),
	}
}

func (s navMemSample) log(t *testing.T) {
	t.Helper()
	t.Logf("round %d: textures=%dKB (text=%dKB/%d entries, labels=%dKB, shapes=%dKB, thumbs=%dKB, video=%dKB, ui=%dKB) "+
		"+ boxart=%dKB/%d covers | icons copies=%d bakes=%d direct=%d | fits=%d measures=%d sanitize=%d | heap=%dKB rss=%dKB",
		s.round, s.textures()>>10, s.textCache>>10, s.textLen, s.homeLabels>>10,
		s.shapes>>10, s.thumbs>>10, s.videoOv>>10, s.uiLayer>>10,
		s.artBytes>>10, s.artCount,
		s.iconCopies, s.iconBakes, s.iconDirect, s.fits, s.measures, s.sanitize,
		s.heapLive>>10, s.rss>>10)
}

// TestSceneNavigationSamplerSeesGrowth is the harness checking itself. A
// stability assertion that can never fail is worse than none: this puts a
// known number of texture bytes into the cache the sampler reads and requires
// it to report them, so a refactor that stops sampling the text cache (or
// counts the wrong field) fails here rather than silently passing forever.
func TestSceneNavigationSamplerSeesGrowth(t *testing.T) {
	cache := GetTextCache()
	cache.Clear()
	t.Cleanup(cache.Clear)

	before := sampleNavMemory(0)

	const entries = 64
	for i := 0; i < entries; i++ {
		// 100x20 RGBA = 8000 bytes each, nil texture: the cache accounts by
		// size, so no renderer is needed to check the bookkeeping.
		cache.Put(nil, fmt.Sprintf("growth-check-%d", i), textCacheTestColor, nil, 100, 20)
	}

	after := sampleNavMemory(0)
	if grew := after.textCache - before.textCache; grew < entries*8000 {
		t.Fatalf("sampler reported %d bytes after %d known labels; the harness is not measuring the text cache",
			grew, entries)
	}
	if after.textLen != before.textLen+entries {
		t.Fatalf("sampler entry count went %d→%d, want +%d", before.textLen, after.textLen, entries)
	}
	if after.textures() <= before.textures() {
		t.Fatal("the texture total did not move with the cache it is built from")
	}
}

// walkScenePass paints every scene of the handheld config once on the given
// renderer, entering each one the way the launcher does.
func walkScenePass(t *testing.T, renderer *sdl.Renderer, config *Config) int {
	t.Helper()
	rendered := 0
	prevScene := currentSceneIndex
	defer func() { currentSceneIndex = prevScene }()
	for i := range config.Scenes {
		currentSceneIndex = i
		menuTilesApply(config, i)
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, config.Scenes[i])
		rendered++
	}
	return rendered
}

// navMemCoverW/H is a box-art file larger than the preview draws it. The point
// is not to model a real cover's pixels but to make a set of them bigger than
// the cache budget with a number of games a test can walk in a few frames; a
// thumbnail-sized cover would need hundreds of files and hundreds of frames to
// reach the same pressure.
const (
	navMemCoverW = 800
	navMemCoverH = 600
)

// forceHandheldCeilings pins every ceiling the walk's table reports to the
// handheld branch. The box-art budget was the first one, but it was not the only
// one: the rendered-text, home-label and shape caches branch on the device too,
// so on a desktop three of the table's columns were measured against 64, 48 and
// 48 MB while the card ships 24, 8 and 12. Pinning only box art made the table
// read as the device's numbers with most of it describing the host.
//
// Each seam is asserted after it is set. A budget function that stopped honouring
// the override would otherwise put the harness quietly back on the host branch
// and the table would go on looking plausible.
func forceHandheldCeilings(t *testing.T) {
	t.Helper()
	prevArt := libraryArtBudgetForce.Swap(1)
	prevScene := sceneBudgetForce.Swap(1)

	// The two singletons are built on first use and are package-wide, so the
	// branch has to be applied to the live caches as well as to the functions
	// that report it.
	cache := GetTextCache()
	cache.SetBudget(textCacheBudgetBytes())
	homeTextCacheApplyBudget()

	t.Cleanup(func() {
		sceneBudgetForce.Store(prevScene)
		libraryArtBudgetForce.Store(prevArt)
		// Restore from the un-pinned branch rather than from what the cache
		// held before: if the singleton was created inside the forced window,
		// putting back its old number would leave it under a ceiling that no
		// longer matches textCacheBudgetBytes().
		cache.SetBudget(textCacheBudgetBytes())
		homeTextCacheApplyBudget()
	})

	checks := []struct {
		name string
		got  int64
		want int64
	}{
		{"box art", libraryArtBudgetBytes(), libraryArtHandheldBudget},
		{"rendered text", cache.Limit(), textBudgetHandheldBytes},
		{"home labels", homeTextCache.maxBytes, homeTextBudgetHandheldBytes},
		{"shapes", shapeCacheBudgetBytes(), shapeBudgetHandheldBytes},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("forcing the handheld branch left the %s budget at %d bytes, want the handheld %d",
				check.name, check.got, check.want)
		}
	}
	if got := homeTextCache.maxItems; got != homeTextHandheldItems {
		t.Fatalf("forcing the handheld branch left the label cache at %d entries, want the handheld %d",
			got, homeTextHandheldItems)
	}
	// The shape cache reads its budget at eviction time, so the pin has to reach
	// the live instance too, not just the free function.
	if got := shapeTextures.budget(); got != shapeBudgetHandheldBytes {
		t.Fatalf("the live shape cache is enforcing %d bytes, want the handheld %d",
			got, shapeBudgetHandheldBytes)
	}
}

// openNavMemLibraryArt puts a synthetic card under the library panel: one
// console, one ROM per game, and a cover file for every one of them. The cover
// count is derived from the art budget so the set is always larger than the
// cache can hold — that is what makes the walk evict instead of merely filling
// once, and eviction is the half of the bound a unit call to rememberArt cannot
// show happening under navigation.
//
// The panel globals are left open on the card; the caller runs
// closeNavMemLibraryArt when it is done.
func openNavMemLibraryArt(t *testing.T) (covers int, coverBytes int64) {
	t.Helper()
	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	artRoot := filepath.Join(root, "art")

	perCover := int64(navMemCoverW) * navMemCoverH * 4
	covers = int(libraryArtBudgetBytes()/perCover) + 10
	if covers > libraryArtMaxTextures {
		t.Fatalf("a %d-byte budget at %d bytes a cover needs %d covers, past the %d-entry bound; "+
			"the fixture and the cache no longer describe the same limit",
			libraryArtBudgetBytes(), perCover, covers, libraryArtMaxTextures)
	}

	for i := 0; i < covers; i++ {
		rom := filepath.Join(roms, "GBA", fmt.Sprintf("Covered Game %02d.gba", i))
		if err := os.MkdirAll(filepath.Dir(rom), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := buildGameLibrary(scanROMLibrary([]string{roms}), artRoot)
	if lib.Total != covers {
		t.Fatalf("the fixture wrote %d ROMs but the library scanned %d", covers, lib.Total)
	}

	// The cover goes where the scanner will look for it, which is the path the
	// report already decided for this game — not a folder name guessed here.
	for _, system := range lib.Systems {
		for _, game := range system.Games {
			if strings.TrimSpace(game.Art) == "" {
				t.Fatalf("%s has no artwork path, so a cover cannot be placed for it", game.Path)
			}
			writeSolidPNG(t, game.Art, navMemCoverW, navMemCoverH, color.RGBA{R: 198, G: 122, B: 44, A: 255})
		}
	}

	// Re-scan so the report counts the covers that now exist: the header's
	// "N cover(s)" has to describe the card the walk is about to read.
	lib = buildGameLibrary(scanROMLibrary([]string{roms}), artRoot)
	if lib.WithArt != covers {
		t.Fatalf("library counts %d covers for %d games; the fixture's covers are not where the panel looks",
			lib.WithArt, covers)
	}

	gameLib = gameLibraryPanel{}
	gameLib.open = true
	gameLib.lib = lib
	gameLib.recents = nil
	gameLib.query = ""
	gameLib.focus = 1
	gameLib.system = 0
	gameLib.cursor = 0
	gameLib.scroll = 0
	// Without this the first frame sees a generation mismatch and drops the
	// cache wholesale, so the walk would measure a cache that never keeps
	// anything.
	gameLib.artSeen = artworkFetchGeneration()
	return covers, perCover
}

// closeNavMemLibraryArt frees the panel's textures and its globals. It must run
// before the renderer is destroyed, and it must not leave the panel open for the
// next test in the package.
func closeNavMemLibraryArt() {
	closeGameLibraryArt()
	gameLib = gameLibraryPanel{}
}

// walkLibraryArtPresses draws the panel on the first game of the console and
// then presses Down through the whole list, one frame per press. The covers
// therefore arrive through the same path a D-pad press takes — the render
// between two presses is what puts the newly focused cover in the cache — and
// every frame in between is a frame the screen actually costs.
func walkLibraryArtPresses(t *testing.T, renderer *sdl.Renderer, config *Config, covers int) {
	t.Helper()
	gameLib.cursor, gameLib.scroll = 0, 0
	for i := 0; i < covers; i++ {
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderGameLibraryPanel(renderer, config)
		if i+1 == covers {
			break
		}
		before := gameLib.cursor
		gameLibraryMove(0, 1)
		if gameLib.cursor != before+1 {
			t.Fatalf("press %d moved the cursor %d→%d; the walk is not stepping the list", i, before, gameLib.cursor)
		}
	}
}

// TestSceneNavigationMemory walks the whole shell four times and reports what
// each pass cost. The first pass fills the caches; from there a pass that costs
// more than the one before it is holding on to something the previous identical
// pass did not.
func TestSceneNavigationMemory(t *testing.T) {
	configs := loadAuditConfigs(t)
	config := configs["trimui/jukaconfig.device.json"]
	if config == nil {
		t.Fatalf("the handheld config ships in player/trimui; it could not be loaded")
	}
	withScreen(t, int32(config.Width), int32(config.Height))
	renderer := newKeyboardTestRenderer(t)

	// The handheld ceilings are the ones that ship, so they are the ones this
	// harness measures, on a Windows desktop as much as on a Linux runner. The
	// library fixture below sizes itself from the same budget, so the art
	// eviction the walk has to perform is the handheld one too.
	forceHandheldCeilings(t)
	t.Logf("ceilings forced to the handheld branch: box art %d MB (desktop %d), text %d MB (%d), "+
		"labels %d MB / %d entries (%d / %d), shapes %d MB (%d)",
		libraryArtBudgetBytes()>>20, libraryArtDesktopBudget>>20,
		GetTextCache().Limit()>>20, textBudgetDesktopBytes>>20,
		homeTextCache.maxBytes>>20, homeTextCache.maxItems,
		homeTextBudgetDesktopBytes>>20, homeTextDesktopItems,
		shapeCacheBudgetBytes()>>20, shapeBudgetDesktopBytes>>20)

	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	// The library phase's card. Prepared once: the walk below is what puts the
	// covers in the cache, and it starts from the top every pass, so a pass that
	// ends with more art than the one before it is holding on to a texture the
	// identical previous pass released.
	covers, coverBytes := openNavMemLibraryArt(t)
	t.Cleanup(closeNavMemLibraryArt)

	const rounds = 4
	samples := make([]navMemSample, 0, rounds)
	for round := 1; round <= rounds; round++ {
		if n := walkScenePass(t, renderer, config); n < 20 {
			t.Fatalf("pass %d rendered only %d scenes", round, n)
		}
		walkLibraryArtPresses(t, renderer, config, covers)
		s := sampleNavMemory(round)
		s.log(t)
		samples = append(samples, s)
	}

	// The library numbers only mean something if the walk actually loaded
	// covers and actually crossed the budget. An empty or never-evicting cache
	// is trivially stable, so both have to be ruled out before stability is
	// read.
	last := samples[len(samples)-1]
	if last.artCount == 0 || last.artBytes == 0 {
		t.Fatalf("walking %d games left %d box-art entries (%d bytes): no cover was loaded, "+
			"so the panel's art path never ran", covers, last.artCount, last.artBytes)
	}
	if last.artCount >= covers {
		t.Fatalf("%d covers walked and %d still cached: the set never crossed the %d-byte budget, "+
			"so eviction went unmeasured", covers, last.artCount, libraryArtBudgetBytes())
	}
	if budget := libraryArtBudgetBytes(); last.artBytes > budget {
		t.Fatalf("box-art cache holds %d bytes after the walk, past its %d-byte budget", last.artBytes, budget)
	}
	if last.artCount > libraryArtMaxTextures {
		t.Fatalf("box-art cache holds %d entries, past its %d-entry bound", last.artCount, libraryArtMaxTextures)
	}

	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		t.Logf("delta round %d→%d: textures=%+dKB text=%+dKB shapes=%+dKB labels=%+dKB boxart=%+dKB/%+d covers heap=%+dKB rss=%+dKB",
			prev.round, cur.round,
			(cur.textures()-prev.textures())>>10,
			(cur.textCache-prev.textCache)>>10,
			(cur.shapes-prev.shapes)>>10,
			(cur.homeLabels-prev.homeLabels)>>10,
			(cur.artBytes-prev.artBytes)>>10, cur.artCount-prev.artCount,
			int64(cur.heapLive-prev.heapLive)>>10,
			int64(cur.rss-prev.rss)>>10)
	}

	// The first pass is the warm-up: it fills the caches, so it is allowed to
	// cost whatever it costs. Every pass after it paints the same scenes with
	// everything already cached, so it must cost the same — no new texture, no
	// new label, no new entry in the fit/measure/substitution tables, and no
	// growth in the resident set (where a native surface leaked by a draw path
	// would show up, since the Go heap cannot see it).
	const (
		heapSlack = int64(4 << 20)  // collector noise, observed at <100 KB
		rssSlack  = int64(16 << 20) // allocator/page-cache noise, observed at ~1.4 MB
		// A background goroutine left by another test can draw one label or
		// measure one string while the next pass renders (a clock tick, a
		// toast, a thumbnail note). That is real work, not a leak, so the
		// scene caches get a small per-pass allowance: a screenful of new
		// labels is tens of KB and dozens of entries, far above this.
		sceneSlack  = int64(64 << 10)
		entrySlack  = 8
		stringSlack = 8
		otherSlack  = int64(4 << 20)
		// The box-art cache is walked from the top every pass, so it sees the
		// same covers in the same order and ends on the same entries. One cover
		// of churn is allowed for a fetch landing mid-pass; anything past that is
		// the walk itself holding what the previous identical walk released.
		artEntrySlack = 1
	)
	for i := 2; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		if d := cur.sceneOwnedBytes() - prev.sceneOwnedBytes(); d > sceneSlack {
			t.Errorf("pass %d→%d grew the scene caches by %+d bytes with every cache already warm"+
				" (text %d→%d, labels %d→%d, shapes %d→%d)",
				prev.round, cur.round, d,
				prev.textCache, cur.textCache, prev.homeLabels, cur.homeLabels,
				prev.shapes, cur.shapes)
		}
		if d := cur.textLen - prev.textLen; d > entrySlack {
			t.Errorf("pass %d→%d added %d entries to the text cache", prev.round, cur.round, d)
		}
		if d := int64(cur.fits - prev.fits); d > stringSlack {
			t.Errorf("pass %d→%d added %d fitted strings", prev.round, cur.round, d)
		}
		if d := int64(cur.measures - prev.measures); d > stringSlack {
			t.Errorf("pass %d→%d added %d measured strings", prev.round, cur.round, d)
		}
		if d := int64(cur.textures()) - int64(prev.textures()); d > otherSlack {
			t.Errorf("pass %d→%d grew total texture memory by %+d bytes", prev.round, cur.round, d)
		}
		if d := cur.artCount - prev.artCount; d > artEntrySlack {
			t.Errorf("pass %d→%d ended the same library walk with %+d box-art entries (%d→%d) and %+d bytes",
				prev.round, cur.round, d, prev.artCount, cur.artCount, cur.artBytes-prev.artBytes)
		}
		if d := cur.artBytes - prev.artBytes; d > coverBytes {
			t.Errorf("pass %d→%d grew the box-art cache by %+d bytes with the same %d covers walked",
				prev.round, cur.round, d, covers)
		}
		if d := int64(cur.heapLive) - int64(prev.heapLive); d > heapSlack {
			t.Errorf("pass %d→%d grew the live Go heap by %d bytes", prev.round, cur.round, d)
		}
		if prev.rss > 0 && cur.rss > 0 {
			if d := int64(cur.rss) - int64(prev.rss); d > rssSlack {
				t.Errorf("pass %d→%d grew the process working set by %d bytes (a native surface/texture may not be freed)",
					prev.round, cur.round, d)
			}
		}
	}

	// The substitution cache is the one cache whose entry count is *not* a leak
	// signal. It drops half its entries when it fills, so a library screen of
	// distinct titles pushes it over the cap at the end of a pass and the next
	// pass starts over on what was dropped: the sample lands on whichever side
	// of that sawtooth the last frame left it on, and demanding it never rise
	// would fail on a cache that is behaving exactly as designed. The property
	// that has to hold — and the one a leak would break — is the cap itself.
	for _, s := range samples {
		if s.sanitize > maxSanitizeEntries {
			t.Errorf("pass %d left %d substituted strings in the cache, past its %d-entry cap",
				s.round, s.sanitize, maxSanitizeEntries)
		}
	}
}
