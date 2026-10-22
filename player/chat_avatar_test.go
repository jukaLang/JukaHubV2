package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/veandco/go-sdl2/sdl"
)

func TestDiscordAvatarURLUsesTheAuthorsPicture(t *testing.T) {
	withPicture := &discordgo.Message{Author: &discordgo.User{
		ID: "123456789", Username: "steppa", Avatar: "abcDEF",
	}}
	want := "https://cdn.discordapp.com/avatars/123456789/abcDEF.png?size=64"
	if got := discordAvatarURL(withPicture); got != want {
		t.Fatalf("discordAvatarURL = %q, want %q", got, want)
	}

	cases := []struct {
		name string
		msg  *discordgo.Message
	}{
		{"nil message", nil},
		{"nil author", &discordgo.Message{}},
		{"author without a picture", &discordgo.Message{Author: &discordgo.User{ID: "1", Username: "plain"}}},
		{"author without an id", &discordgo.Message{Author: &discordgo.User{Username: "ghost", Avatar: "hash"}}},
		// The relay bot reposts other people's words; its picture is not
		// the speaker's, so those messages keep the initial circle.
		{"bridge relay", &discordgo.Message{Author: &discordgo.User{
			ID: "2", Username: "TrimuiSmartProJuka", Avatar: "botpic",
		}}},
	}
	for _, tc := range cases {
		if got := discordAvatarURL(tc.msg); got != "" {
			t.Errorf("%s: discordAvatarURL = %q, want no avatar", tc.name, got)
		}
	}
}

func TestCircularAvatarPixelsMasksTheRim(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	const size = 16
	pixels := circularAvatarPixels(src, size)
	if len(pixels) != size*size*4 {
		t.Fatalf("pixel buffer = %d bytes, want %d", len(pixels), size*size*4)
	}
	at := func(x, y int) color.RGBA {
		i := (y*size + x) * 4
		return color.RGBA{R: pixels[i], G: pixels[i+1], B: pixels[i+2], A: pixels[i+3]}
	}
	center := at(size/2, size/2)
	if center.A < 240 {
		t.Errorf("centre alpha = %d, want the picture to stay opaque", center.A)
	}
	if center.R != 200 || center.G != 100 || center.B != 50 {
		t.Errorf("centre colour = %+v, want the source colour untouched", center)
	}
	for _, corner := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
		if a := at(corner[0], corner[1]).A; a != 0 {
			t.Errorf("corner %v alpha = %d, want it clipped away", corner, a)
		}
	}
	if rim := at(size/2, 0).A; rim == 0 || rim == 255 {
		t.Errorf("top rim alpha = %d, want a feathered edge", rim)
	}
}

// A rectangular picture must be centre-cropped, not stretched: a wide image
// keeps its middle columns and drops the edges.
func TestCircularAvatarPixelsCentreCrops(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 10, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 10; x++ {
			c := color.RGBA{R: 255, A: 255} // left edge columns
			if x >= 2 && x < 8 {
				c = color.RGBA{G: 255, A: 255} // middle, the square that is kept
			}
			if x >= 8 {
				c = color.RGBA{B: 255, A: 255} // right edge columns
			}
			src.Set(x, y, c)
		}
	}
	const size = 8
	pixels := circularAvatarPixels(src, size)
	// side = 4 and x0 = 3, so the kept window is columns 3..6: all green.
	mid := (size/2*size + size/2) * 4
	if pixels[mid] != 0 || pixels[mid+1] != 255 || pixels[mid+2] != 0 {
		t.Fatalf("centre pixel rgb = (%d,%d,%d), want the cropped middle's green", pixels[mid], pixels[mid+1], pixels[mid+2])
	}
	if circularAvatarPixels(nil, 8) != nil {
		t.Fatal("a nil image must not produce pixels")
	}
}

// solidAvatarPNG encodes a single-colour square the way an avatar arrives.
func solidAvatarPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode avatar: %v", err)
	}
	return buf.Bytes()
}

func TestChatAvatarTextureIsDecodedOnce(t *testing.T) {
	renderer := probeRenderer(t)
	clearChatAvatarCache()
	t.Cleanup(clearChatAvatarCache)

	const url = "https://cdn.discordapp.com/avatars/1/hash.png?size=64"
	chatAvatarDLMutex.Lock()
	chatAvatarData[url] = solidAvatarPNG(t, color.RGBA{R: 1, G: 254, B: 3, A: 255})
	chatAvatarDLMutex.Unlock()

	first := getChatAvatarTexture(renderer, url)
	if first == nil {
		t.Fatal("downloaded avatar produced no texture")
	}
	if second := getChatAvatarTexture(renderer, url); second != first {
		t.Fatal("the second call should reuse the cached texture")
	}
	chatAvatarDLMutex.Lock()
	_, kept := chatAvatarData[url]
	chatAvatarDLMutex.Unlock()
	if kept {
		t.Fatal("decoded bytes should be released once the texture owns them")
	}
	if getChatAvatarTexture(renderer, "") != nil {
		t.Fatal("a sender without an avatar must not get a texture")
	}
}

// The real path is: download the picture once, then decode it on the render
// thread. A local server keeps the test off the network while exercising the
// same fetch a Discord avatar URL goes through.
func TestChatAvatarDownloadsThenUploads(t *testing.T) {
	renderer := probeRenderer(t)
	clearChatAvatarCache()
	t.Cleanup(clearChatAvatarCache)

	payload := solidAvatarPNG(t, chatAvatarTestColor)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/avatar.png"
	if tex := getChatAvatarTexture(renderer, url); tex != nil {
		t.Fatal("the first call cannot have the picture yet")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		chatAvatarDLMutex.Lock()
		downloaded := len(chatAvatarData[url]) > 0
		chatAvatarDLMutex.Unlock()
		if downloaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the avatar was never downloaded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tex := getChatAvatarTexture(renderer, url); tex == nil {
		t.Fatal("the downloaded avatar was not decoded into a texture")
	}
}

// chatAvatarTestColor is a colour nothing else on the chat panel draws, so a
// hit in the framebuffer proves the avatar picture was blitted.
var chatAvatarTestColor = color.RGBA{R: 1, G: 254, B: 3, A: 255}

// countAvatarColor counts pixels of one colour. The channel order in the
// framebuffer depends on the pixel format, so both sides are compared as an
// unordered triplet.
func countAvatarColor(buf []byte, want color.RGBA) int {
	target := []int{int(want.R), int(want.G), int(want.B)}
	sort.Ints(target)
	hits := 0
	for i := 0; i+3 < len(buf); i += 4 {
		rgb := []int{int(buf[i]), int(buf[i+1]), int(buf[i+2])}
		sort.Ints(rgb)
		if rgb[0] == target[0] && rgb[1] == target[1] && rgb[2] == target[2] {
			hits++
		}
	}
	return hits
}

// renderChatWithAvatar renders one message from a sender with avatarURL and
// returns how many pixels show the seeded picture. pngBytes seeds the download
// cache; nil leaves the sender without a picture.
func renderChatWithAvatar(t *testing.T, avatarURL string, pngBytes []byte) int {
	t.Helper()
	renderer := probeRenderer(t)
	clearChatAvatarCache()
	t.Cleanup(clearChatAvatarCache)
	if len(pngBytes) > 0 {
		chatAvatarDLMutex.Lock()
		chatAvatarData[avatarURL] = pngBytes
		chatAvatarDLMutex.Unlock()
	}

	prevMessages, prevOffset := chatMessages, chatScrollOffset
	chatMutex.Lock()
	chatMessages = []ChatMessage{{
		Sender:    "Steppa",
		Text:      "hello from discord",
		Timestamp: time.Now(),
		AvatarURL: avatarURL,
	}}
	chatMutex.Unlock()
	chatScrollOffset = 0
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages = prevMessages
		chatMutex.Unlock()
		chatScrollOffset = prevOffset
	})

	if err := renderer.SetDrawColor(0, 0, 0, 255); err != nil {
		t.Skipf("renderer unavailable: %v", err)
	}
	renderer.Clear()
	renderChat(renderer, &Config{}, Element{Type: "chat", X: 0, Y: 0, Width: "1280", Height: "720"})

	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return countAvatarColor(buf, chatAvatarTestColor)
}

func TestChatDrawsTheSendersAvatarWhenItExists(t *testing.T) {
	const url = "https://cdn.discordapp.com/avatars/2/hash.png?size=64"
	// A 28 px circle covers roughly 600 pixels.
	if hits := renderChatWithAvatar(t, url, solidAvatarPNG(t, chatAvatarTestColor)); hits < 200 {
		t.Fatalf("avatar colour covered %d pixels, want the sender's picture", hits)
	}
}

func TestChatFallsBackToTheInitialCircleWithoutAPicture(t *testing.T) {
	// A URL the avatar fetcher never touches (no http scheme), so the sender
	// stays pictureless for the whole frame, like a Discord user who never
	// uploaded one.
	if hits := renderChatWithAvatar(t, "avatar:none", nil); hits != 0 {
		t.Fatalf("avatar colour covered %d pixels with no picture to draw", hits)
	}
}
