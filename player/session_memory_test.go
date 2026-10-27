package main

// session_memory_test.go — what a long chat and a long reading session hold.
//
// The scene-navigation harness paints each screen once and moves on. The caches
// a bug report is actually about ("it gets slower after an hour in the browser")
// only fill with use, and their budgets — chat pictures 24 MB handheld, avatars
// 4 MB, page images 64 MB, rendered text 24 MB, the browser's page cache 12 MB —
// were sized from the same 1 GB reasoning as everything else. This drives the
// two panels a session is spent in and prints what they really hold against
// those budgets, through the same accounting the device logs behind
// JUKAHUB_TEXSTATS=1.
//
//	go test -run TestLongSessionTextureMemory -v
//
// The session is synthetic but not mocked: messages, pictures and pages go
// through the decode-and-cache paths the panels use, and every ceiling is pinned
// to the handheld branch first, so the figures describe the card rather than the
// desktop running the probe.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	sessionChatMessages = 300
	sessionChatSenders  = 24
	// Pictures are the size a Discord screenshot arrives at, which is what makes
	// them the largest thing the chat cache ever holds: one is 3 MB as RGBA, and
	// the cache's 24 MB ceiling is eight of them.
	sessionChatPictureW = 1024
	sessionChatPictureH = 768
	sessionBrowserPages = 24
	sessionPageImages   = 3
	sessionPageImageW   = 640
	sessionPageImageH   = 480
)

// sessionChatLine builds the nth message body: 60–400 characters, so the text
// cache sees the mix of short replies and paragraphs a real conversation has.
func sessionChatLine(i int) string {
	body := "The build I tried last night stutters on the scrolling shelves, " +
		"and the save state came back with the wrong slot name. "
	switch i % 4 {
	case 1:
		body += "It only happens after a long session, never on a fresh boot, " +
			"which is why I think the cache is involved rather than the emulator. "
	case 2:
		body += "Same here on the Brick. "
	case 3:
		body += "Try rolling the core back one build; that fixed it for me on the Smart Pro. "
	}
	repeat := i%5 + 1
	return strings.TrimSpace(strings.Repeat(body, repeat))
}

// runChatSession fills the chat caches the way a long conversation does: every
// sender has a picture, every so often a message carries one, and the panel is
// drawn while scrolling from the newest message to the oldest. Pictures are
// seeded as already-downloaded bytes, so the decode and the cache insert are the
// real ones and no request leaves the machine.
func runChatSession(t *testing.T, renderer *sdl.Renderer, config *Config) {
	t.Helper()
	prevMessages, prevOffset := chatMessages, chatScrollOffset
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages = prevMessages
		chatMutex.Unlock()
		chatScrollOffset = prevOffset
		chatImageDLMutex.Lock()
		for url := range chatImageDownloads {
			delete(chatImageDownloads, url)
		}
		for url := range chatImageFetching {
			delete(chatImageFetching, url)
		}
		chatImageDLMutex.Unlock()
		chatAvatarDLMutex.Lock()
		for url := range chatAvatarData {
			delete(chatAvatarData, url)
		}
		chatAvatarDLMutex.Unlock()
		clearChatImageCache()
		clearChatAvatarCache()
	})

	avatarPNG := tinyTestPNG(t, 128, 128)
	picturePNG := tinyTestPNG(t, sessionChatPictureW, sessionChatPictureH)

	chatAvatarDLMutex.Lock()
	for i := 0; i < sessionChatSenders; i++ {
		chatAvatarData[fmt.Sprintf("https://avatars.example/%d.png", i)] = avatarPNG
	}
	chatAvatarDLMutex.Unlock()

	// A message picture whose download has landed but whose upload has not: the
	// render's own processChatImageDownloads does the decode and the insert.
	chatImageDLMutex.Lock()
	for i := 0; i < sessionChatMessages/21; i++ {
		url := fmt.Sprintf("https://images.example/%d.png", i)
		chatImageDownloads[url] = picturePNG
		// Keep the fetch path out of the probe; the bytes are already here.
		chatImageFetching[url] = true
	}
	chatImageDLMutex.Unlock()

	now := time.Now()
	messages := make([]ChatMessage, 0, sessionChatMessages)
	for i := 0; i < sessionChatMessages; i++ {
		sender := i % sessionChatSenders
		msg := ChatMessage{
			ID:        fmt.Sprintf("%d", i),
			Sender:    fmt.Sprintf("Reader %02d", sender),
			UserID:    fmt.Sprintf("1000000000000000%02d", sender),
			AvatarURL: fmt.Sprintf("https://avatars.example/%d.png", sender),
			Text:      sessionChatLine(i),
			Timestamp: now.Add(time.Duration(i) * time.Minute),
		}
		if i%21 == 0 {
			msg.ImageURL = fmt.Sprintf("https://images.example/%d.png", i/21)
		}
		messages = append(messages, msg)
	}
	chatMutex.Lock()
	chatMessages = messages
	chatMutex.Unlock()

	// A long conversation is read by scrolling a few messages at a time. The
	// step is small because a screenful holds two or three bubbles once the
	// replies carry a paragraph: stepping by a screen would skip the senders in
	// between, and their avatars would never be asked for.
	element := Element{Type: "chat", X: 0, Y: 0, Width: "1240", Height: "640", Font: "small"}
	for offset := 0; offset < sessionChatMessages; offset += 3 {
		chatScrollOffset = offset
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderChat(renderer, config, element)
	}
	chatScrollOffset = 0
}

// sessionImagePNG is one page picture as a data URI. Each one is its own image
// on purpose: the texture cache is keyed by source, so pages sharing a picture
// would pay for one texture between them and the budget would never be reached.
func sessionImagePNG(t *testing.T, page, slot int) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, sessionPageImageW, sessionPageImageH))
	fill := color.RGBA{R: uint8(40 + page), G: uint8(90 + slot*20), B: 160, A: 255}
	for y := 0; y < sessionPageImageH; y++ {
		for x := 0; x < sessionPageImageW; x++ {
			im.SetRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// sessionPageHTML is one article: a heading, eight paragraphs of prose and three
// embedded pictures. The pictures are data URIs, so fetchWebImages decodes them
// inline and the probe never touches the network while still paying the same
// per-image byte limits and the same decode into the shared texture cache.
func sessionPageHTML(t *testing.T, index int, pictureURIs []string) string {
	t.Helper()
	var body strings.Builder
	fmt.Fprintf(&body, "<h1>Field notes %d</h1>", index)
	slot := 0
	// An article of this length scrolls several screens, and the pictures are
	// spread through it: the first one is above the fold, the rest are reached
	// by scrolling, which is what the probe does before it measures.
	for p := 0; p < 8; p++ {
		if p%3 == 0 && slot < len(pictureURIs) {
			fmt.Fprintf(&body, "<img src=\"%s\" width=\"%d\" height=\"%d\">",
				pictureURIs[slot], sessionPageImageW, sessionPageImageH)
			slot++
		}
		body.WriteString("<p>")
		body.WriteString(strings.Repeat(
			fmt.Sprintf("Paragraph %d of page %d walks through the build, the settings it shipped with and what the card did afterwards. ", p, index), 4))
		body.WriteString("</p>")
	}
	return body.String()
}

// runBrowserSession reads a set of pages the way the browser does: load, wait
// for the fetch, draw, then the next one. Pages are drawn one after another so
// the page cache crosses its entry cap and the image cache crosses its byte
// budget, which is what a long read looks like from inside the caches.
func runBrowserSession(t *testing.T, renderer *sdl.Renderer) (*Config, Element) {
	t.Helper()
	// SDL_image has to be up for a decoded image to become a texture: the app
	// calls img.Init at startup, and the probe renderer does not.
	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		t.Logf("SDL_image init reported %v; continuing (it is already up if this is not the first caller)", err)
	}

	html := make(map[string]string, sessionBrowserPages)
	urls := make([]string, 0, sessionBrowserPages)

	for i := 0; i < sessionBrowserPages; i++ {
		url := fmt.Sprintf("https://notes.example/%d", i)
		urls = append(urls, url)
		slots := make([]string, 0, sessionPageImages)
		for slot := 0; slot < sessionPageImages; slot++ {
			slots = append(slots, sessionImagePNG(t, i, slot))
		}
		html[url] = sessionPageHTML(t, i, slots)
	}

	config, element, _ := browserCacheFixture(t, func(rawURL string) browserPage {
		pageHTML, ok := html[rawURL]
		if !ok {
			// A failed load is never cached, so an unknown address must not
			// silently stand in for a page.
			return browserPage{}
		}
		doc := parseWebPage(pageHTML, "", rawURL)
		// The real loader fetches the page's images before publishing it; with
		// data URIs that is a decode and an entry in doc.Images per <img>.
		fetchWebImages(doc)
		return browserPage{
			Text:     fmt.Sprintf("Title: Field notes\nURL: %s\n\nField notes, read in the browser.", rawURL),
			FinalURL: rawURL,
			Doc:      doc,
		}
	})

	// The panel a reader actually gets on the Smart Pro, not the fixture's
	// default 480px stub: the viewport decides how much of a page (and how many
	// of its pictures) is drawn per frame.
	element.Width = "1240"
	element.Height = "640"
	// The Browser card renders a web element; without the source the panel takes
	// the plain-text reader, which draws no images at all.
	element.Source = textBrowserSourceWeb

	// A page is read top to bottom, so each one is drawn at the top and then
	// scrolled: an image below the fold costs nothing until it is reached, and
	// the probe is measuring what reading costs, not what opening costs.
	prevScroll := textBrowserScrollY
	t.Cleanup(func() { textBrowserScrollY = prevScroll })
	for _, url := range urls {
		loadTextBrowserURLAt(config, element, url, true)
		waitForBrowserFetches()
		drainCustom(config)
		for _, scroll := range []int32{0, 14, 34, 60} {
			textBrowserScrollY = scroll
			renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
			renderer.Clear()
			renderTextBrowser(renderer, config, element)
		}
	}
	textBrowserScrollY = 0
	return config, element
}

// TestLongSessionTextureMemory is the measurement, and then the guard: it prints
// what a long session holds per cache and fails if any of them ended past the
// ceiling the card ships with. The printed line is the device's own accounting
// (`[tex] …`), so the figures in a bug report and the figures here can be
// compared directly.
func TestLongSessionTextureMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("long-session probe skipped in -short mode")
	}
	// Every ceiling the session touches, on the branch the card ships.
	forceHandheldCeilings(t)
	prevChatBudget := chatImages.maxBytes
	chatImageMutex.Lock()
	chatImages.setBudget(chatImageTextureHandheldBudget)
	chatImageMutex.Unlock()
	t.Cleanup(func() {
		chatImageMutex.Lock()
		chatImages.setBudget(prevChatBudget)
		chatImageMutex.Unlock()
	})
	thumbs := getThumbnailTextures()
	thumbs.setBudget(thumbnailTextureHandheldBudget)
	t.Cleanup(func() { thumbs.setBudget(thumbnailTextureBudgetBytes()) })
	if got := thumbs.budget(); got != thumbnailTextureHandheldBudget {
		t.Fatalf("page-image ceiling stayed at %d bytes, want the handheld %d", got, thumbnailTextureHandheldBudget)
	}

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	newProbeFrame(t)
	renderer := probeRenderer(t)

	runChatSession(t, renderer, config)
	runBrowserSession(t, renderer)
	// The page images are renderer-owned textures in a cache that outlives the
	// renderer, so drop them while it is still alive instead of leaving a later
	// test drawing a handle SDL already freed.
	t.Cleanup(func() { thumbs.Clear() })

	stats := collectTextureMemoryStats()
	_, _, shapeBytes := ShapeCacheStats()
	chatEntries, chatBytes := chatImageCacheStats()
	avatarEntries, avatarBytes := chatAvatarCacheStats()
	pages, pageBytes := browserCacheSize()
	textCap := GetTextCache().Limit()
	thumbBytes := thumbs.Bytes()

	// The device's own line, unchanged: this is the probe a bug report quotes.
	logTextureMemoryStats("long session")
	t.Logf("session: %d chat pictures / %d KB of %d KB · %d avatars / %d KB of %d KB · "+
		"%d of %d pages / %d KB of %d KB · page images %d KB of %d KB · "+
		"text %d KB of %d KB · labels %d KB of %d KB · shapes %d KB of %d KB",
		chatEntries, chatBytes>>10, chatImageTextureHandheldBudget>>10,
		avatarEntries, avatarBytes>>10, chatAvatarTextureBudget>>10,
		pages, browserCacheMaxEntries, pageBytes>>10, int64(browserCacheMaxBytes)>>10,
		thumbBytes>>10, thumbnailTextureHandheldBudget>>10,
		stats.TextCache>>10, textCap>>10,
		stats.HomeText>>10, homeTextCacheBudgetBytes()>>10,
		shapeBytes>>10, shapeCacheBudgetBytes()>>10)

	// A session that loaded nothing would pass every budget below, so the
	// measurement has to prove it happened first.
	if chatBytes == 0 || chatEntries == 0 {
		t.Fatalf("the chat session cached no picture (%d entries, %d bytes)", chatEntries, chatBytes)
	}
	if avatarEntries == 0 || avatarBytes == 0 {
		t.Fatalf("the chat session cached no avatar (%d entries, %d bytes)", avatarEntries, avatarBytes)
	}
	if pages == 0 || pageBytes == 0 {
		t.Fatalf("the browser session cached no page (%d entries, %d bytes)", pages, pageBytes)
	}
	if thumbBytes == 0 {
		t.Fatal("the browser session decoded no page image")
	}
	if stats.TextCache == 0 {
		t.Fatal("neither session rendered any text")
	}

	// The pictures are the largest objects in play, so eviction has to have
	// happened for the ceiling to mean anything: 14 pictures of 3 MB cannot fit
	// in 24 MB.
	if chatEntries >= sessionChatMessages/21 {
		t.Fatalf("all %d chat pictures are still cached in %d bytes; the session never crossed the budget",
			chatEntries, chatImageTextureHandheldBudget)
	}
	if pages >= sessionBrowserPages {
		t.Fatalf("all %d pages are still cached; the browser's entry cap never binded", sessionBrowserPages)
	}

	checks := []struct {
		name string
		got  int64
		cap  int64
	}{
		{"chat pictures", chatBytes, chatImageTextureHandheldBudget},
		{"chat avatars", avatarBytes, chatAvatarTextureBudget},
		{"browser pages", int64(pageBytes), int64(browserCacheMaxBytes)},
		{"page images", thumbBytes, thumbnailTextureHandheldBudget},
		{"rendered text", stats.TextCache, textCap},
		{"home labels", stats.HomeText, homeTextCacheBudgetBytes()},
		{"shapes", shapeBytes, shapeCacheBudgetBytes()},
	}
	for _, check := range checks {
		if check.cap <= 0 {
			t.Errorf("%s has no ceiling to check against", check.name)
			continue
		}
		if check.got > check.cap {
			t.Errorf("%s ended the session at %d bytes, past its %d-byte ceiling", check.name, check.got, check.cap)
		}
	}
}
