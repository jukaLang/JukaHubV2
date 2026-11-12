package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- Chat System ---

type ChatMessage struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	ImageURL  string    `json:"image_url,omitempty"`
	// AvatarURL is the sender's own picture when the service behind the chat
	// provides one (Discord does). The avatar column falls back to the initial
	// circle for senders without a picture, or while it is still downloading.
	AvatarURL string `json:"avatar_url,omitempty"`
	// Own marks messages sent from this device so the user's bubbles stay
	// right-aligned (and styled as "me") even after a display-name change.
	Own bool `json:"own,omitempty"`
	// UserID and Handle are the Discord account behind the display name. They
	// let a profile card tell two people with the same name apart and ask
	// Discord for roles and a join date.
	UserID string `json:"user_id,omitempty"`
	Handle string `json:"handle,omitempty"`
	Bot    bool   `json:"bot,omitempty"`
	// Quote is the message a reply answers, kept with the reply so the history
	// still shows what was being discussed after a restart.
	Quote *ChatQuote `json:"quote,omitempty"`
	// Pending marks one of this device's messages that is not delivered yet:
	// queued for Discord, or a question still waiting for Juka's answer. It is
	// saved, so a message typed offline is sent after a restart too.
	Pending bool `json:"pending,omitempty"`
	// Error is the one sentence shown under a message that could not be
	// delivered for a reason other than the network (Retry clears it).
	Error string `json:"error,omitempty"`
	// Files are what the message carries besides its text: Discord's
	// attachments, or files attached on this device (chatfiles.go). The first
	// picture among them is the bubble's picture; the rest draw as cards.
	Files []ChatFile `json:"files,omitempty"`
}

// chatImageTexture is one decoded chat image with the footprint it occupies.
type chatImageTexture struct {
	texture *sdl.Texture
	bytes   int64
}

// ChatAttachments stores images for rendering. The cache is bounded twice:
// chatImageCacheMax entries, and chatImageTextureBudgetBytes. The count was the
// only bound for a long time, and a count is not a byte ceiling when one photo
// can be 48 MB of RGBA — sixteen of those is more than the handheld has.
var chatImages = newByteOrderedStore[string, chatImageTexture](
	chatImageCacheMax,
	chatImageTextureBudgetBytes(),
	func(t chatImageTexture) int64 { return t.bytes },
	func(t chatImageTexture) {
		if t.texture != nil {
			t.texture.Destroy()
		}
	},
)
var chatImageMutex sync.Mutex

const chatImageCacheMax = 16

// chatImageCacheStats reports the images and bytes held, for the texture
// accounting log and the tests.
func chatImageCacheStats() (entries int, bytes int64) {
	chatImageMutex.Lock()
	defer chatImageMutex.Unlock()
	return chatImages.stats()
}

// clearChatImageCache releases every decoded chat image. Used by the memory
// pressure eviction path and by shutdown.
func clearChatImageCache() {
	chatImageMutex.Lock()
	defer chatImageMutex.Unlock()
	chatImages.clear()
}

var chatImageDownloads = make(map[string][]byte)
var chatImageFetching = make(map[string]bool)
var chatImageDLMutex sync.Mutex

// Compiled on first use, so a session that never opens Chat does not pay for
// them; the render path still compiles each one exactly once.
var urlRegex = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`https?://[^\s<>"]+`) })
var imageURLRegex = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\.(jpg|jpeg|png|gif|webp|bmp)(\?.*)?$`) })

func getChatImageTexture(renderer *sdl.Renderer, url string) *sdl.Texture {
	chatImageMutex.Lock()
	if cached, ok := chatImages.get(url); ok {
		chatImageMutex.Unlock()
		return cached.texture
	}
	chatImageMutex.Unlock()

	chatImageDLMutex.Lock()
	if data, ok := chatImageDownloads[url]; ok && len(data) > 0 {
		// Taken out here so processChatImageDownloads does not decode the
		// same bytes a second time in this frame.
		delete(chatImageDownloads, url)
		chatImageDLMutex.Unlock()
		return uploadChatImage(renderer, url, data)
	}
	// A link that failed (gone, offline, not an image) waits before the next
	// try. Nothing was remembered before, so it was fetched again on every
	// frame it stayed on screen — up to 32 MB each time, or one failing DNS
	// lookup per frame offline.
	if when, failed := chatImageFailed[url]; failed && time.Since(when) < chatImageRetryAfter {
		chatImageDLMutex.Unlock()
		return nil
	}
	fetching := chatImageFetching[url]
	chatImageDLMutex.Unlock()

	if !fetching {
		go fetchChatImage(url)
	}

	return nil
}

// chatImageFailed holds when each failing chat image URL last failed. It is
// guarded by chatImageDLMutex.
var chatImageFailed = make(map[string]time.Time)

// chatImageRetryAfter is how long a failed chat image waits for another try.
const chatImageRetryAfter = 2 * time.Minute

// markChatImageFailed records a fetch or decode failure for url.
func markChatImageFailed(url string) {
	chatImageDLMutex.Lock()
	defer chatImageDLMutex.Unlock()
	if len(chatImageFailed) > 512 {
		chatImageFailed = make(map[string]time.Time)
	}
	chatImageFailed[url] = time.Now()
}

// chatAttachmentRoot is the directory a picture saved by an older version (a
// local path in ImageURL) may be read from when it is not one the user put
// into the chat on this device (chatPathTrusted). The chat renderer seeds it
// from the configured explorer root. Anything else — a path typed into a
// message, or one carried in from a remote room — is refused rather than read
// and drawn.
var chatAttachmentRoot = "."

// chatLocalAttachment resolves a picture's local path for the bubble. A path
// the user attached or saved on this device is read where it is; any other
// must lie inside chatAttachmentRoot. Either way it has to be an existing
// image file small enough to decode.
func chatLocalAttachment(path string) (string, bool) {
	if !isImageExtension(strings.ToLower(filepath.Ext(path))) {
		return "", false
	}
	resolved := filepath.Clean(path)
	if !chatPathTrusted(path) {
		within, err := regularFileWithin(chatAttachmentRoot, path)
		if err != nil {
			logWarn("chat", "security: refusing local attachment %q: %v", path, err)
			return "", false
		}
		resolved = within
	}
	if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return "", false
	}
	return resolved, true
}

func fetchChatImage(url string) {
	chatImageDLMutex.Lock()
	if _, done := chatImageDownloads[url]; done || chatImageFetching[url] {
		chatImageDLMutex.Unlock()
		return
	}
	chatImageFetching[url] = true
	chatImageDLMutex.Unlock()

	var data []byte
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				data, _ = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			}
		}
	} else if path, ok := chatLocalAttachment(url); ok {
		// Chat attachments store a local file path; read it directly instead
		// of sending it through http.Get.
		data, _ = os.ReadFile(path)
	}

	chatImageDLMutex.Lock()
	delete(chatImageFetching, url)
	if len(data) > 0 {
		chatImageDownloads[url] = data
		delete(chatImageFailed, url)
	}
	chatImageDLMutex.Unlock()
	if len(data) == 0 {
		markChatImageFailed(url)
	}
}

func uploadChatImage(renderer *sdl.Renderer, url string, data []byte) *sdl.Texture {
	// Decode through the shared bounded decoder: it turns the bytes into a
	// dense RGBA and takes a permit, so a fan-out of decodes elsewhere cannot
	// run unbounded. This call is on the render thread, where nothing else
	// holds a permit, so the Acquire never blocks here.
	rgba, err := decodeImageRGBA(context.Background(), data)
	if err != nil {
		markChatImageFailed(url)
		return nil
	}
	bounds := rgba.Bounds()
	w, h := bounds.Max.X, bounds.Max.Y
	if w <= 0 || h <= 0 {
		markChatImageFailed(url)
		return nil
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(w), int32(h))
	if err != nil {
		markChatImageFailed(url)
		return nil
	}
	tex.Update(nil, unsafe.Pointer(&rgba.Pix[0]), rgba.Stride)
	chatImageMutex.Lock()
	// Two fetch paths can race for one URL: put replaces the value in place and
	// releases the old texture, so eviction never frees the live copy.
	chatImages.put(url, chatImageTexture{texture: tex, bytes: int64(w) * int64(h) * 4})
	chatImageMutex.Unlock()
	return tex
}

func processChatImageDownloads(renderer *sdl.Renderer) {
	chatImageDLMutex.Lock()
	var ready []struct {
		url  string
		data []byte
	}
	for url, data := range chatImageDownloads {
		ready = append(ready, struct {
			url  string
			data []byte
		}{url, data})
	}
	chatImageDLMutex.Unlock()

	for _, item := range ready {
		uploadChatImage(renderer, item.url, item.data)
		// The texture now owns the pixels. Keeping the downloaded buffer as
		// well doubled the cost of every image in the history, and nothing
		// ever freed it.
		chatImageDLMutex.Lock()
		delete(chatImageDownloads, item.url)
		chatImageDLMutex.Unlock()
	}
}

// --- Sender avatars ---

// Chat avatars are separate from message images: they are decoded, centre
// cropped and masked into a circle once, then cached by URL. The download is a
// plain goroutine that only stores bytes; decoding and texture creation happen
// on the render thread, because SDL textures belong to it.
// chatAvatarTexture is one cached avatar with the footprint it occupies. The
// size is fixed today, and recording it keeps the accounting honest if the
// decode size ever changes.
type chatAvatarTexture struct {
	texture *sdl.Texture
	bytes   int64
}

var (
	chatAvatarMutex sync.Mutex
	// Bounded by count and by bytes, like the message images: 48 fixed 64×64
	// avatars are 768 KB, which is a real byte ceiling rather than one that
	// depends on what the speakers happen to use.
	chatAvatars = newByteOrderedStore[string, chatAvatarTexture](
		chatAvatarCacheMax,
		chatAvatarTextureBudgetBytes(),
		func(t chatAvatarTexture) int64 { return t.bytes },
		func(t chatAvatarTexture) {
			if t.texture != nil {
				t.texture.Destroy()
			}
		},
	)
	chatAvatarDLMutex sync.Mutex
	chatAvatarData    = map[string][]byte{}
	chatAvatarPending = map[string]bool{}
	chatAvatarFailed  = map[string]bool{}
)

const (
	// chatAvatarCacheMax bounds the cache: a chat holds a handful of speakers
	// and each avatar is 64x64.
	chatAvatarCacheMax = 48
	// chatAvatarPixels is the decoded size. Discord serves 64 px avatars and
	// the bubble draws them at 28, so this stays sharp on a hidpi panel.
	chatAvatarPixels = 64
	// chatAvatarMaxBytes caps what a single avatar download may read.
	chatAvatarMaxBytes = 1 << 20
)

// getChatAvatarTexture returns the circular avatar for url, starting a download
// on first sight. A nil result means "no picture yet": the caller draws the
// initial circle instead, and the avatar replaces it on a later frame.
func getChatAvatarTexture(renderer *sdl.Renderer, url string) *sdl.Texture {
	if renderer == nil || url == "" {
		return nil
	}
	chatAvatarMutex.Lock()
	if cached, ok := chatAvatars.get(url); ok {
		chatAvatarMutex.Unlock()
		return cached.texture
	}
	chatAvatarMutex.Unlock()

	chatAvatarDLMutex.Lock()
	data := chatAvatarData[url]
	failed := chatAvatarFailed[url]
	chatAvatarDLMutex.Unlock()
	if len(data) > 0 {
		return uploadChatAvatar(renderer, url, data)
	}
	if !failed {
		go fetchChatAvatar(url)
	}
	return nil
}

// fetchChatAvatar downloads one avatar into its byte cache.
func fetchChatAvatar(url string) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return
	}
	chatAvatarDLMutex.Lock()
	if chatAvatarPending[url] || len(chatAvatarData[url]) > 0 || chatAvatarFailed[url] {
		chatAvatarDLMutex.Unlock()
		return
	}
	chatAvatarPending[url] = true
	chatAvatarDLMutex.Unlock()

	succeeded := false
	defer func() {
		chatAvatarDLMutex.Lock()
		delete(chatAvatarPending, url)
		if !succeeded {
			chatAvatarFailed[url] = true
		}
		chatAvatarDLMutex.Unlock()
	}()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		logWarn("chat", "avatar download failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logWarn("chat", "avatar download failed: %s", resp.Status)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, chatAvatarMaxBytes))
	if err != nil || len(data) == 0 {
		return
	}
	chatAvatarDLMutex.Lock()
	chatAvatarData[url] = data
	chatAvatarDLMutex.Unlock()
	succeeded = true
}

// uploadChatAvatar turns downloaded bytes into a cached circular texture.
func uploadChatAvatar(renderer *sdl.Renderer, url string, data []byte) *sdl.Texture {
	chatAvatarDLMutex.Lock()
	delete(chatAvatarData, url)
	chatAvatarDLMutex.Unlock()

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		chatAvatarDLMutex.Lock()
		chatAvatarFailed[url] = true
		chatAvatarDLMutex.Unlock()
		logWarn("chat", "avatar decode failed: %v", err)
		return nil
	}
	pixels := circularAvatarPixels(img, chatAvatarPixels)
	if len(pixels) == 0 {
		return nil
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, chatAvatarPixels, chatAvatarPixels)
	if err != nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pixels[0]), chatAvatarPixels*4); err != nil {
		tex.Destroy()
		return nil
	}
	tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	chatAvatarMutex.Lock()
	chatAvatars.put(url, chatAvatarTexture{
		texture: tex,
		bytes:   int64(chatAvatarPixels) * int64(chatAvatarPixels) * 4,
	})
	chatAvatarMutex.Unlock()
	return tex
}

// chatAvatarCacheStats reports the avatars and bytes held.
func chatAvatarCacheStats() (entries int, bytes int64) {
	chatAvatarMutex.Lock()
	defer chatAvatarMutex.Unlock()
	return chatAvatars.stats()
}

// circularAvatarPixels centre-crops img to a square, scales it to size and
// writes RGBA pixels that fade out along a round rim, so the avatar reads as a
// circle instead of a square tile with clipped corners.
func circularAvatarPixels(img image.Image, size int) []byte {
	if img == nil || size <= 0 {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	side := w
	if h < side {
		side = h
	}
	x0 := bounds.Min.X + (w-side)/2
	y0 := bounds.Min.Y + (h-side)/2

	pixels := make([]byte, size*size*4)
	// One-and-a-half pixels of feather keeps the rim smooth at 28 px without
	// needing a real anti-aliased mask.
	feather := 1.5 / float64(size)
	for py := 0; py < size; py++ {
		dy := (float64(py)+0.5)/float64(size)*2 - 1
		for px := 0; px < size; px++ {
			dx := (float64(px)+0.5)/float64(size)*2 - 1
			if dx*dx+dy*dy >= 1 {
				continue
			}
			dist := math.Sqrt(dx*dx + dy*dy)
			cover := 1.0
			if dist > 1-feather {
				cover = (1 - dist) / feather
			}
			src := color.NRGBAModel.Convert(img.At(x0+px*side/size, y0+py*side/size)).(color.NRGBA)
			i := (py*size + px) * 4
			pixels[i] = src.R
			pixels[i+1] = src.G
			pixels[i+2] = src.B
			pixels[i+3] = uint8(float64(src.A) * cover)
		}
	}
	return pixels
}

// clearChatAvatarCache drops the avatar textures and the download bookkeeping
// that belongs to them, and the chat view's other renderer-bound caches (the
// corner textures and the bubble layouts measured with that renderer's fonts).
// The textures belong to one renderer, so this runs before that renderer is
// destroyed.
func clearChatAvatarCache() {
	clearChatShapeCache()
	clearChatLayoutCache()
	chatAvatarMutex.Lock()
	chatAvatars.clear()
	chatAvatarMutex.Unlock()

	chatAvatarDLMutex.Lock()
	chatAvatarData = map[string][]byte{}
	chatAvatarPending = map[string]bool{}
	chatAvatarFailed = map[string]bool{}
	chatAvatarDLMutex.Unlock()
}

// ChatSection is the chat history section persisted inside jukauser.json.
type ChatSection struct {
	// Messages is the community (Discord) tab. Before the tabs existed it was
	// one mixed list; loadChatMessages splits such a history once.
	Messages []ChatMessage `json:"messages"`
	// Name is the display name the local user chats under (default "You").
	Name string `json:"name,omitempty"`
	// AI is the Juka AI conversation and Saved the notes-to-self tab; both
	// stay on this device.
	AI    []ChatMessage `json:"ai,omitempty"`
	Saved []ChatMessage `json:"saved,omitempty"`
	// Tab is the tab the chat reopens on.
	Tab string `json:"tab,omitempty"`
	// Seen holds, per tab, when it was last read (unix ms), so unread counts
	// survive a restart.
	Seen map[string]int64 `json:"seen,omitempty"`
	// Version is chatHistoryVersion once the history is split into tabs.
	Version int `json:"version,omitempty"`
}

// userName is the one name the user goes by: Settings → General → Your name,
// the chat's name chip and the Home greeting all read and write this same
// value (the "tspUsername" setting). Empty means no name is set.
//
// Before the two were tied together the chat kept its own name (Chat.Name).
// That older name still counts while the Settings name has never been
// written, so a chat name picked on an earlier build shows up in Settings and
// on Home; once the Settings name exists (even cleared to empty) it wins.
func userName(c *Config) string {
	if c != nil {
		if n := strings.TrimSpace(c.Variables.TSPUsername); n != "" {
			return n
		}
		if _, set := customValue(c.Variables.Custom, "tspUsername"); set {
			return ""
		}
	}
	if userConfigCache != nil && userConfigCache.Chat != nil {
		if n := strings.TrimSpace(userConfigCache.Chat.Name); n != "" && !strings.EqualFold(n, "You") {
			return n
		}
	}
	return ""
}

// chatNameCache is userName as last read on the main thread. chatDisplayName
// is also called off it (the sender loop's chatIsOwn, Discord handlers), where
// reading the config's variables map races the main thread's writes and a
// concurrent map read and write kills the process; it reads this copy instead.
var chatNameCache struct {
	sync.Mutex
	name  string
	valid bool
}

// chatRefreshName copies userName into chatNameCache (main thread: chatTick
// every frame, and the chat's own rename).
func chatRefreshName(c *Config) {
	n := userName(c)
	chatNameCache.Lock()
	chatNameCache.name, chatNameCache.valid = n, true
	chatNameCache.Unlock()
}

// chatDisplayName returns the name the local user's messages are signed with:
// the user's name (userName), or "You" when none is set. Safe off the main
// thread: it never reads the config itself.
func chatDisplayName() string {
	chatNameCache.Lock()
	n, valid := chatNameCache.name, chatNameCache.valid
	chatNameCache.Unlock()
	if !valid && userConfigCache != nil && userConfigCache.Chat != nil {
		// Before the first frame: the older chat name, as userName would.
		if old := strings.TrimSpace(userConfigCache.Chat.Name); !strings.EqualFold(old, "You") {
			n = old
		}
	}
	if n != "" {
		return n
	}
	return "You"
}

// setChatDisplayName updates and persists the local user's name from the chat's
// name chip. It is the same setting as Settings → Your name (and the Home
// greeting), so both change together. An empty name — or "You", the chat's
// placeholder — clears it, and the chat signs as "You" again.
func setChatDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "You") {
		name = ""
	}
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	if userConfigCache.Chat == nil {
		userConfigCache.Chat = &ChatSection{}
	}
	// Chat.Name is kept equal so an older build reading this jukauser.json
	// still signs the chat with the same name.
	userConfigCache.Chat.Name = name
	if appConfig != nil {
		// setStoreString also refreshes Variables.TSPUsername; the full save
		// carries the chat section along from the cache.
		setStoreString(appConfig, "tspUsername", name)
		saveUserConfig(userConfigFromVariables(appConfig))
	} else {
		saveUserConfig(userConfigCache)
	}
	chatRefreshName(appConfig)
	if name == "" {
		return "You"
	}
	return name
}

// defaultDiscordChannel is the JukaHub Discord channel the chat connects to when
// no channel id is configured. Mirrors the channel id used by the original app.
const defaultDiscordChannel = "975787212954275916"

// discordStatus is the technical Discord status, for the log and the keyboard's
// old chat header cache. The chat screen shows its own short states instead
// (chatNet), never this text.
var discordStatus string

// chatSendBtnRect is the round send button at the end of the composer.
var chatSendBtnRect sdl.Rect

// chatAttachBtnRect is the attach-a-file button (the paperclip) at the start
// of the composer.
var chatAttachBtnRect sdl.Rect

// chatInputRect is the composer's text field (a click starts typing).
var chatInputRect sdl.Rect

// chatNameRect is the display-name chip at the right of the tab strip;
// clicking it opens the keyboard to rename the local chat user.
var chatNameRect sdl.Rect

// chatLinkHit is a URL rendered as a preview card inside a chat bubble. Media
// links play in the media player; every other page opens in the built-in
// browser.
type chatLinkHit struct {
	rect sdl.Rect
	url  string
	play bool
}

var chatLinkRects []chatLinkHit

// chatTextLinkRects are the URL spans drawn as message text. They are kept apart
// from the card rects so a card and the text it previews never shadow each
// other in a click.
var chatTextLinkRects []chatLinkHit

// chatImageHit is an inline image preview rendered inside a chat bubble.
// Clicking it opens the image full screen (and clicking again closes it).
type chatImageHit struct {
	rect sdl.Rect
	url  string
}

var chatImageRects []chatImageHit

// handleChatImageClick opens the image preview under the cursor in the image
// viewer. It reports whether the click landed on one.
func handleChatImageClick(mx, my int32) bool {
	// An open action bar owns the clicks it covers; its own Enlarge entry
	// is how the picture is opened while a message is selected. The two
	// modal cards own their rectangle just as completely, so a click inside
	// one never reaches a preview painted underneath it.
	if chatSelectionBar.open || chatProfile.open || chatMembers.open {
		return false
	}
	for _, hit := range chatImageRects {
		if pointInRect(mx, my, hit.rect) {
			openImageViewerURL(hit.url)
			return true
		}
	}
	return false
}

// trimChatURL drops punctuation that commonly follows a pasted link in prose.
func trimChatURL(u string) string {
	return strings.TrimRight(u, ".,;:!?)]}'\"")
}

// firstChatImageURL returns the first URL in a message that points at an image,
// so pasting an image link shows a thumbnail instead of a bare address.
func firstChatImageURL(text string) string {
	for _, m := range urlRegex().FindAllString(text, -1) {
		u := trimChatURL(m)
		if imageURLRegex().MatchString(u) {
			return u
		}
	}
	return ""
}

// chatMessageImageURL is the picture a message carries: a picture saved by an
// older version first, then the first picture among its files, then the first
// image link pasted into its text. The bubble preview and the action bar both
// read it here, so they can never disagree about whether a message has a
// picture to enlarge.
func chatMessageImageURL(msg ChatMessage) string {
	if u := strings.TrimSpace(msg.ImageURL); u != "" {
		return u
	}
	if i := chatInlineImageIndex(msg); i >= 0 {
		return chatFileImageSource(msg.Files[i])
	}
	return firstChatImageURL(msg.Text)
}

// isYouTubeURL reports whether a URL is a YouTube watch/short link. yt-dlp
// handles both, so they can go straight to the embedded player.
func isYouTubeURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "youtube.com/") || strings.Contains(lower, "youtu.be/")
}

// chatLinkCardLimit bounds how many link cards one message may draw. Each card
// costs a texture and a network fetch, and a handheld must not turn a wall of
// pasted URLs into a stall.
const chatLinkCardLimit = 3

// allChatLinks returns every distinct http(s) link in a message, in order. The
// cards stop at chatLinkCardLimit; the link text in the bubble stays clickable
// for the rest, so this one is not capped.
func allChatLinks(text string) []string {
	links := make([]string, 0, chatLinkCardLimit)
	seen := make(map[string]bool, chatLinkCardLimit)
	for _, m := range urlRegex().FindAllString(text, -1) {
		u := trimChatURL(m)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		links = append(links, u)
	}
	return links
}

// chatMessageLinks returns the distinct http(s) links in a message, in order,
// limited to the preview cards one bubble may draw.
func chatMessageLinks(text string) []string {
	links := allChatLinks(text)
	if len(links) > chatLinkCardLimit {
		links = links[:chatLinkCardLimit]
	}
	return links
}

// chatLineLinkSpan is one URL inside a wrapped bubble line, as byte offsets into
// that line.
type chatLineLinkSpan struct {
	start, end int
	url        string
}

// chatLinkOpensInPlayer reports whether a link is media the player handles
// (YouTube or a direct audio/video URL) rather than a page to browse.
func chatLinkOpensInPlayer(u string) bool {
	return isYouTubeURL(u) || hasMediaExtension(u)
}

// chatYouTubeID extracts a YouTube video id from a watch or short link.
func chatYouTubeID(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	id := parsed.Query().Get("v")
	if id == "" && strings.EqualFold(strings.TrimPrefix(parsed.Hostname(), "www."), "youtu.be") {
		id = strings.Trim(parsed.Path, "/")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	if len(id) < 6 {
		return ""
	}
	return id
}

// chatLinkPreviewURL picks the image tile shown on a link card. A YouTube link
// gets its poster frame, an image link shows itself, and any other page gets
// its 64px site icon: a few KB instead of downloading a preview picture for
// every link on a 1 GB device.
func chatLinkPreviewURL(u string) string {
	if imageURLRegex().MatchString(u) {
		return u
	}
	if isYouTubeURL(u) {
		if id := chatYouTubeID(u); id != "" {
			return "https://i.ytimg.com/vi/" + id + "/default.jpg"
		}
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return "https://www.google.com/s2/favicons?sz=64&domain=" + parsed.Hostname()
}

// chatLinkHostLabel splits a link into the site name and a short path, which is
// what a card shows: the host identifies where the click goes, the path says
// which page.
func chatLinkHostLabel(u string) (string, string) {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		return truncate(u, 40), ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	path := strings.TrimSuffix(parsed.Path, "/")
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	if path == "" || path == "/" {
		path = "/"
	}
	return host, path
}

// currentSceneHasChat reports whether the visible scene draws the chat. Hit
// boxes are only meaningful there; they are not cleared when another scene
// renders, so the click handlers must not consult stale ones. The Terminal's
// config still carries a "chat" element for its prompt row, but it draws its
// own screen, so it never counts.
func currentSceneHasChat(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	if scene.Name == chatSceneName {
		return true
	}
	if scene.Name == "Terminal" {
		return false
	}
	for _, elem := range scene.Elements {
		if elem.Type == "chat" {
			return true
		}
	}
	return false
}

// handleChatLinkClick activates a link card drawn by renderChat: media plays,
// web pages open in the browser. It reports whether the click landed on one,
// so the generic composer handler does not also run.
func handleChatLinkClick(mx, my int32, config *Config) bool {
	activate := func(hit chatLinkHit) bool {
		if !pointInRect(mx, my, hit.rect) {
			return false
		}
		if hit.play {
			playSmartURL(config, hit.url)
		} else {
			openBrowserAtURL(config, hit.url)
		}
		return true
	}
	for _, hit := range chatTextLinkRects {
		if activate(hit) {
			return true
		}
	}
	for _, hit := range chatLinkRects {
		if activate(hit) {
			return true
		}
	}
	return false
}

// groqStatus holds a short human-readable Groq AI connection status shown in
// the chat UI.
var groqStatus string

// discordSession is the live Discord gateway client (discordgo) when connected.
var discordSession *discordgo.Session

// discordSessionKey names the token and channel discordSession was opened
// for, so a connect for the same pair reuses it.
var discordSessionKey string

// discordConnectMu serializes discordConnect.
var discordConnectMu sync.Mutex

// discordCreds reads the configured Discord bot token and channel id. The channel
// id comes from the explicit ChannelProfile (loaded from jukaconfig.json),
// falling back to the runtime Custom value and finally the default JukaHub
// channel. The token comes from ChannelProfile or the runtime Custom entry.
//
// This function never returns a usable token when the stored value is missing,
// not encrypted, encrypted with the wrong key, or otherwise unusable. In those
// cases it sets a clear status string and returns an empty token so the caller
// can show a specific problem instead of failing later in a less helpful way.
func discordCreds() (string, string) {
	if appConfig == nil {
		discordStatus = "Discord: config not loaded"
		return "", defaultDiscordChannel
	}

	// channel_profile is the documented location (and what -set-secret writes),
	// so it wins over a possibly stale copy in the variables map.
	tok := appConfig.ChannelProfile.Token
	if tok == "" {
		tok, _ = appConfig.Variables.Custom["discord_token"].(string)
	}
	tok = strings.TrimSpace(tok)

	if tok == "" {
		discordStatus = "Discord: token is missing"
		return "", defaultDiscordChannel
	}

	if !strings.HasPrefix(tok, "ENC:") {
		discordStatus = "Discord: token is not encrypted"
		logDebug("discord", "Stored discord_token does not have an ENC: prefix; rename it to an encrypted value before enabling Discord.")
		return "", defaultDiscordChannel
	}

	decrypted, err := DecryptAPIToken(tok, "discord_token")
	if err != nil {
		logWarn("discord", "Failed to decrypt token: %v", err)
		if strings.HasPrefix(err.Error(), "discord_token: value is not encrypted") {
			discordStatus = "Discord: token is not encrypted"
		} else if err.Error() == "discord_token: empty encrypted value" {
			discordStatus = "Discord: token is empty"
		} else if strings.HasPrefix(err.Error(), "discord_token: decryption failed") {
			discordStatus = "Discord: token decryption failed"
		} else {
			discordStatus = "Discord: token is not usable"
		}
		return "", defaultDiscordChannel
	}

	if decrypted == "" {
		discordStatus = "Discord: token decrypted to empty value"
		return "", defaultDiscordChannel
	}

	discordStatus = ""
	return decrypted, defaultDiscordChannel
}

// discordChannel returns the configured Discord channel id, falling back to
// the explicit ChannelProfile, then a runtime Custom value, then the default
// JukaHub channel.
func discordChannel() string {
	if appConfig == nil {
		return defaultDiscordChannel
	}
	ch := strings.TrimSpace(appConfig.ChannelProfile.ChannelID)
	if ch == "" {
		ch, _ = appConfig.Variables.Custom["discord_channel"].(string)
		ch = strings.TrimSpace(ch)
	}
	if ch == "" {
		ch = defaultDiscordChannel
	}
	return ch
}

// discordAPIBase is the REST root used to validate the token before opening the
// gateway. It is a variable so tests can point it at a stub server.
var discordAPIBase = "https://discord.com/api/v10"

// discordTokenType returns the configured token type. Bot is the default: a
// user (self-bot) token violates Discord's terms, so it must be opt-in.
func discordTokenType() string {
	if appConfig == nil {
		return "bot"
	}
	if t, ok := appConfig.Variables.Custom["discord_token_type"].(string); ok {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return "bot"
}

// discordAuthCandidates lists the Authorization header values to try, best
// first. Discord expects "Bot <token>" for bot tokens and a bare token for user
// tokens. A bot token stored with type "user" is a common misconfiguration and
// would otherwise fail with a bare 401, so the other form is always tried too.
func discordAuthCandidates(token, tokenType string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if strings.HasPrefix(token, "Bot ") || strings.HasPrefix(token, "Bearer ") {
		return []string{token}
	}
	bot := "Bot " + token
	if strings.EqualFold(tokenType, "user") {
		return []string{token, bot}
	}
	return []string{bot, token}
}

// discordTokenWorks performs a read-only GET /users/@me with one header value.
// ok=false means Discord rejected the credentials; an error means the check
// could not be completed (offline, HTTP 5xx, rate limit).
func discordTokenWorks(auth string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, discordAPIBase+"/users/@me", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", auth)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("cannot reach Discord: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("Discord returned HTTP %d while checking the token", resp.StatusCode)
	}
}

// discordValidateToken returns the Authorization header value Discord accepts,
// or an error whose text can be shown to the user. Without this check a bad
// token only surfaces later as an unexplained "connecting..." in the UI.
func discordValidateToken(token, tokenType string) (string, error) {
	candidates := discordAuthCandidates(token, tokenType)
	if len(candidates) == 0 {
		return "", fmt.Errorf("token is empty")
	}
	for _, auth := range candidates {
		ok, err := discordTokenWorks(auth)
		if err != nil {
			return "", err
		}
		if ok {
			if strings.HasPrefix(auth, "Bot ") && strings.EqualFold(tokenType, "user") {
				logDebug("discord", "Token is configured as a user token but Discord accepted it as a bot token")
			}
			return auth, nil
		}
	}
	return "", fmt.Errorf("token rejected by Discord (revoked, or not a valid bot token)")
}

// discordMessageParts returns the sender and text for a Discord message,
// unwrapping the bridge format the TSP bot uses ("username: message"). Live
// messages and fetched history both go through it so the same message does not
// render differently depending on how it arrived.
func discordMessageParts(m *discordgo.Message) (string, string) {
	sender := "unknown"
	if m.Author != nil {
		sender = m.Author.Username
	}
	text := m.Content
	if m.Author != nil && strings.HasPrefix(strings.ToLower(m.Author.Username), discordBridgeAuthorPrefix) {
		if idx := strings.Index(text, ":"); idx > 0 {
			sender = strings.TrimSpace(text[:idx])
			text = strings.TrimSpace(text[idx+1:])
		}
	}
	rememberDiscordUser(m.Author)
	for _, u := range m.Mentions {
		rememberDiscordUser(u)
	}
	return sender, discordResolveMentions(text, m)
}

// discordMentionPattern matches the three things Discord sends as markup: a
// user ping (<@id> or the nickname form <@!id>), a role ping (<@&id>) and a
// channel link (<#id>). The raw markup is unreadable on the handheld - a ping
// showed up as "@417829…", an id nobody can recognise.
var discordMentionPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`<(@!?|@&|#)([0-9]+)>`)
})

// discordResolveMentions rewrites those tokens into names. User names come from
// the message itself (Discord lists every pinged user in the payload); roles and
// channels come from the small cache filled when the chat connects.
func discordResolveMentions(text string, m *discordgo.Message) string {
	if text == "" || !strings.Contains(text, "<") {
		return text
	}
	return discordMentionPattern().ReplaceAllStringFunc(text, func(token string) string {
		parts := discordMentionPattern().FindStringSubmatch(token)
		if len(parts) != 3 {
			return token
		}
		kind, id := parts[1], parts[2]
		switch kind {
		case "@", "@!":
			if name := discordMentionedUserName(id, m); name != "" {
				return "@" + name
			}
			// The id is missing from the payload (an old or edited message):
			// still say someone was pinged instead of printing the number.
			return "@user"
		case "@&":
			if name := discordCachedName(discordRoleNames, id); name != "" {
				return "@" + name
			}
		case "#":
			if name := discordCachedName(discordChannelNames, id); name != "" {
				return "#" + name
			}
		}
		return token
	})
} // Names seen in the chat are cached: the message payload only carries the ids
// for roles and channels, and an edited message can arrive without its mention
// list. The caches are tiny (one name per id) and refreshed per connection.
var (
	discordNameMu       sync.Mutex
	discordUserNames    = map[string]string{}
	discordRoleNames    = map[string]string{}
	discordChannelNames = map[string]string{}
)

// rememberDiscordUser caches a name so later mentions of the same id resolve
// even when the payload names only one of them.
func rememberDiscordUser(u *discordgo.User) {
	if u == nil || u.ID == "" || u.Username == "" {
		return
	}
	discordNameMu.Lock()
	discordUserNames[u.ID] = u.Username
	discordNameMu.Unlock()
}

// discordMentionedUserName resolves a pinged user id, first from the message's
// own mention list (which Discord always fills for a ping) and then from names
// seen earlier in the chat.
func discordMentionedUserName(id string, m *discordgo.Message) string {
	discordNameMu.Lock()
	defer discordNameMu.Unlock()
	if m != nil {
		for _, u := range m.Mentions {
			if u != nil && u.ID == id && u.Username != "" {
				discordUserNames[id] = u.Username
				return u.Username
			}
		}
	}
	return discordUserNames[id]
}

func discordCachedName(names map[string]string, id string) string {
	discordNameMu.Lock()
	defer discordNameMu.Unlock()
	return names[id]
}

// cacheDiscordNames stores the role and channel names used by mention markup.
func cacheDiscordNames(roles []*discordgo.Role, channels []*discordgo.Channel) {
	discordNameMu.Lock()
	defer discordNameMu.Unlock()
	discordRoleNames = make(map[string]string, len(roles))
	for _, r := range roles {
		if r != nil && r.ID != "" && r.Name != "" {
			discordRoleNames[r.ID] = r.Name
		}
	}
	discordChannelNames = make(map[string]string, len(channels))
	for _, c := range channels {
		if c != nil && c.ID != "" && c.Name != "" {
			discordChannelNames[c.ID] = c.Name
		}
	}
}

// discordBridgeAuthorPrefix marks the relay bot that reposts messages as
// "username: message". Its own picture is not the speaker's, so those messages
// keep the initial circle instead of borrowing the bot's avatar.
const discordBridgeAuthorPrefix = "trimuismartprojuka"

// discordAvatarURL returns the CDN URL of a message author's avatar, or "" when
// the author has none (Discord accounts can be pictureless) or when the
// message was relayed by the bridge bot on someone else's behalf.
func discordAvatarURL(m *discordgo.Message) string {
	if m == nil || m.Author == nil {
		return ""
	}
	author := m.Author
	if author.ID == "" || author.Avatar == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(author.Username), discordBridgeAuthorPrefix) {
		return ""
	}
	// Animated avatar hashes start with "a_"; the PNG endpoint still answers
	// with a static frame, which is all a 28 px bubble can show.
	return fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png?size=%d", author.ID, author.Avatar, chatAvatarPixels)
}

// discordIdentity returns the Discord account behind a message. A message the
// bridge bot relayed on someone's behalf is the bot's own message, so it has no
// account of its own and must not show the bot's profile.
func discordIdentity(m *discordgo.Message) (userID, handle string, bot bool) {
	if m == nil || m.Author == nil {
		return "", "", false
	}
	author := m.Author
	if strings.HasPrefix(strings.ToLower(author.Username), discordBridgeAuthorPrefix) {
		return "", "", false
	}
	return author.ID, author.Username, author.Bot
}

// chatMessageFromDiscord builds one history entry from a Discord message. Live
// messages and fetched history both come through here, so a message renders the
// same way no matter how it arrived.
func chatMessageFromDiscord(m *discordgo.Message) ChatMessage {
	sender, text := discordMessageParts(m)
	userID, handle, bot := discordIdentity(m)
	return ChatMessage{
		ID:        m.ID,
		Sender:    sender,
		Text:      text,
		Timestamp: time.Time(m.Timestamp),
		// Every attachment is a file now, the picture included: the first
		// picture among them is drawn in the bubble (chatInlineImageIndex).
		Files:     chatFilesFromDiscord(m),
		AvatarURL: discordAvatarURL(m),
		UserID:    userID,
		Handle:    handle,
		Bot:       bot,
	}
}

// discordSessionChannel is the channel discordSession listens to and posts in.
var discordSessionChannel string

// discordSelfID is the bot account the session is logged in as. Every JukaHub
// device posts through one shared bot, so a message from that account is a
// device's relayed "name: text" — another device's, or this one's own echo.
var (
	discordSelfMu sync.Mutex
	discordSelfID string
)

func discordSetSelf(id string) {
	discordSelfMu.Lock()
	discordSelfID = id
	discordSelfMu.Unlock()
}

func discordSelf() string {
	discordSelfMu.Lock()
	defer discordSelfMu.Unlock()
	return discordSelfID
}

// chatChannelTitle is the community tab's label ("#general") once the channel
// name is known. chatMutex guards it.
var chatChannelTitle string

// chatSetChannelTitle records the channel's name for the community tab.
func chatSetChannelTitle(title string) {
	chatMutex.Lock()
	chatChannelTitle = title
	chatMutex.Unlock()
}

// chatCommunityTitle is the community tab's label.
func chatCommunityTitle() string {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	if chatChannelTitle != "" {
		return chatChannelTitle
	}
	return "Community"
}

// discordConnectWith opens a live Discord gateway session for tok on channel
// ch and listens for new messages there; the recent history is fetched in the
// background once the session is open.
func discordConnectWith(tok, ch, tokenType string) error {
	// One connect at a time: two overlapping connects used to each open a
	// gateway session, and the overwritten one went on appending every message
	// a second time.
	discordConnectMu.Lock()
	defer discordConnectMu.Unlock()
	if tok == "" {
		if discordStatus == "" {
			discordStatus = "Discord: token is missing"
		}
		return fmt.Errorf("discord not configured: %s", discordStatus)
	}
	if ch == "" {
		discordStatus = "Discord: channel not set"
		return fmt.Errorf("discord not configured: channel not set")
	}
	if discordSession != nil {
		// An open session for the same token and channel is kept: each new
		// one is a fresh gateway IDENTIFY, which Discord limits per token.
		if discordSessionKey == tok+"\x00"+ch {
			return nil
		}
		discordSession.Close()
		discordSession = nil
		chatPublishSession(nil, "")
	}
	auth, authErr := discordValidateToken(tok, tokenType)
	if authErr != nil {
		discordStatus = "Discord: " + authErr.Error()
		return authErr
	}
	dg, err := discordgo.New(auth)
	if err != nil {
		discordStatus = "Discord: " + err.Error()
		return err
	}
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	dg.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		if r != nil && r.User != nil {
			discordSetSelf(r.User.ID)
		}
	})
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Connect) {
		chatGatewayUp.Store(true)
		chatSetNet(chatNetLive)
	})
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		// discordgo reconnects by itself; the tab says so meanwhile.
		chatGatewayUp.Store(false)
		if chatNet() == chatNetLive {
			chatSetNet(chatNetConnecting)
		}
	})
	dg.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if m == nil || m.Message == nil || m.ChannelID != ch {
			return
		}
		entry, echo := chatEntryFromDiscord(m.Message)
		if chatAppendIncoming(entry, echo) {
			// A message that arrives while the panel is idle should be
			// readable on the screensaver without waking the device.
			noteScreensaverChat(entry.Sender, chatMessagePreview(entry))
		}
	})
	if err := dg.Open(); err != nil {
		discordStatus = "Discord: " + err.Error()
		return err
	}
	discordSession = dg
	discordSessionKey = tok + "\x00" + ch
	discordSessionChannel = ch
	// Other goroutines read the pair from here (chatupload.go).
	chatPublishSession(dg, ch)
	discordStatus = "Discord: connecting..."
	go discordLoadHistory(dg, ch)
	return nil
}

// chatEntryFromDiscord builds a history entry from a Discord message and
// reports whether it came from the bot account this device posts through. Such
// a message is "name: text" relayed for a JukaHub device; the bot's own
// picture and profile are not the speaker's, so they are dropped.
func chatEntryFromDiscord(m *discordgo.Message) (ChatMessage, bool) {
	entry := chatMessageFromDiscord(m)
	self := discordSelf()
	if self == "" || m.Author == nil || m.Author.ID != self {
		return entry, false
	}
	if !strings.HasPrefix(strings.ToLower(m.Author.Username), discordBridgeAuthorPrefix) {
		if idx := strings.Index(entry.Text, ":"); idx > 0 {
			entry.Sender = strings.TrimSpace(entry.Text[:idx])
			entry.Text = strings.TrimSpace(entry.Text[idx+1:])
		}
		entry.UserID, entry.Handle, entry.AvatarURL, entry.Bot = "", "", "", false
	}
	return entry, true
}

// discordLoadHistory fetches the channel's name and recent history for a new
// session and merges the history into the community tab.
func discordLoadHistory(dg *discordgo.Session, ch string) {
	defer func() {
		if r := recover(); r != nil {
			logDebug("discord", "history fetch recovered from: %v", r)
		}
	}()
	// Role and channel names first: message text is rewritten when it is
	// stored, so the cache has to be ready before the history is read or a
	// role ping would keep its numeric id forever.
	if info, cerr := dg.Channel(ch); cerr == nil && info != nil {
		if info.Name != "" {
			chatSetChannelTitle("#" + info.Name)
		}
		if info.GuildID != "" {
			// The guild id is what a profile needs to ask Discord for roles
			// and a join date, so it is remembered for the session.
			discordGuildID = info.GuildID
			resetDiscordMemberDetails()
			if guild, gerr := dg.Guild(info.GuildID); gerr == nil && guild != nil {
				cacheDiscordNames(guild.Roles, guild.Channels)
				// The server's boost tier sets how big a file it takes.
				chatSetUploadTier(int(guild.PremiumTier))
			}
		}
	}
	msgs, ferr := dg.ChannelMessages(ch, 50, "", "", "")
	if ferr != nil {
		discordStatus = "Discord: " + ferr.Error()
		logWarn("discord", "history fetch failed: %v", ferr)
		return
	}
	// Discord answers newest first.
	fresh := make([]ChatMessage, 0, len(msgs))
	echoes := make([]bool, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] == nil {
			continue
		}
		entry, echo := chatEntryFromDiscord(msgs[i])
		fresh = append(fresh, entry)
		echoes = append(echoes, echo)
	}
	chatMergeCommunity(fresh, echoes)
	discordStatus = "Discord: connected"
}

// chatMergeCommunity merges a page of channel history into the community tab.
// What is already there (the same id, or this device's own message coming
// back through the shared bot) is kept as it is, except for its file links:
// Discord's expire after about a day and the fresh page has working ones. The
// rest is added in time order. A new slice is swapped in, so a frame reading
// the old one is safe.
func chatMergeCommunity(fresh []ChatMessage, echoes []bool) {
	chatMutex.Lock()
	cur := chatMessages
	have := make(map[string]int, len(cur))
	for i, m := range cur {
		if m.ID != "" {
			have[m.ID] = i
		}
	}
	merged := make([]ChatMessage, 0, len(cur)+len(fresh))
	merged = append(merged, cur...)
	added, unseen := 0, 0
	relinked := false
	for i, m := range fresh {
		if m.ID != "" {
			if at, ok := have[m.ID]; ok {
				if at < len(cur) {
					if next, changed := chatRefreshLinks(merged[at], m); changed {
						merged[at] = next
						relinked = true
					}
				}
				continue
			}
		}
		if i < len(echoes) && echoes[i] {
			if at := chatOwnTwin(merged, m); at >= 0 {
				// This device's own message, already in the channel: it is
				// delivered, even when a restart lost Discord's answer, and
				// must not go out a second time.
				next := merged[at]
				chatAdoptEcho(&next, m)
				merged[at] = next
				if m.ID != "" {
					have[m.ID] = at
				}
				relinked = true
				continue
			}
			if chatTakeUnsend("", &m) {
				// Deleted here while it went out: taken back, not shown.
				chatDeleteRemote(m.ID)
				continue
			}
		}
		if chatWasDropped(m.ID) {
			continue // taken back off the channel already
		}
		merged = append(merged, m)
		added++
		if m.ID != "" {
			have[m.ID] = len(merged) - 1
		}
		if !chatIsOwn(m) && m.Timestamp.UnixMilli() > chatTabs[chatTabCommunity].seenAt {
			unseen++
		}
	}
	if added == 0 {
		if relinked {
			chatMessages = merged
		}
		chatMutex.Unlock()
		if relinked {
			chatDirty.Store(true)
		}
		return
	}
	sort.SliceStable(merged, func(a, b int) bool {
		return merged[a].Timestamp.Before(merged[b].Timestamp)
	})
	if limit := chatTabCap(chatTabCommunity); len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	chatMessages = merged
	switch {
	case chatLiveTab.Load() == int32(chatTabCommunity):
		chatMarkSeenLocked(chatTabCommunity)
	case chatTabs[chatTabCommunity].seenAt > 0:
		// With no reading on record (the first connect ever) nothing counts
		// as unread: fifty "new" messages on a first visit would say nothing.
		chatTabs[chatTabCommunity].unread += unseen
	}
	chatMutex.Unlock()
	chatDirty.Store(true)
}

// chatOwnTwin returns the index in list of this device's own copy of m (the
// same sender, text and files, not yet holding a Discord id), or -1.
func chatOwnTwin(list []ChatMessage, m ChatMessage) int {
	for i := len(list) - 1; i >= 0; i-- {
		have := list[i]
		if chatSameContent(have, m) && chatEchoCandidate(have, m.Timestamp, true) {
			return i
		}
	}
	return -1
}

// discordPostMessage posts one message to the session's channel and returns
// the id Discord gave it. The sender is prefixed ("name: message") so the
// message reads right on Discord and on the other JukaHub devices sharing the
// bot. When replyToID names a Discord message it is sent as a real reply, so
// the quoted context is visible on both sides; a deleted original is not an
// error (FailIfNotExists is false).
func discordPostMessage(sender, text, replyToID string) (string, error) {
	dg := discordSession
	ch := discordSessionChannel
	if dg == nil || ch == "" {
		return "", fmt.Errorf("discord not connected")
	}
	payload := text
	if sender != "" {
		payload = sender + ": " + text
	}
	data := &discordgo.MessageSend{Content: payload}
	if replyToID != "" && isDiscordSnowflake(replyToID) {
		failIfMissing := false
		data.Reference = &discordgo.MessageReference{
			MessageID:       replyToID,
			ChannelID:       ch,
			FailIfNotExists: &failIfMissing,
		}
	}
	sent, err := dg.ChannelMessageSendComplex(ch, data)
	if err != nil {
		discordStatus = "Discord send: " + err.Error()
		return "", err
	}
	discordStatus = "Discord: connected"
	if sent == nil {
		return "", nil
	}
	return sent.ID, nil
}

// Groq Cloud models. Groq retired llama-3.3-70b-versatile and
// llama-3.1-8b-instant on 2026-08-16; gpt-oss-120b is the recommended
// replacement, with gpt-oss-20b and qwen3.8-27b as fallbacks.
const (
	groqEndpoint      = "https://api.groq.com/openai/v1/chat/completions"
	groqDefaultModel  = "openai/gpt-oss-120b"
	groqFallbackModel = "openai/gpt-oss-20b"
	groqThirdModel    = "qwen/qwen3.8-27b"

	groqSystemPrompt = "You are Juka, the built-in assistant on JukaHub, a handheld media launcher. " +
		"Answer briefly and concretely in a few short sentences of plain text. " +
		"Help with device features, media and search ideas, settings and general questions."
)

// groqRetiredModels maps ids that no longer answer on Groq Cloud to the
// replacement Groq recommends.
var groqRetiredModels = map[string]string{
	"llama-3.3-70b-versatile":                       groqDefaultModel,
	"llama-3.1-8b-instant":                          "openai/gpt-oss-20b",
	"qwen/qwen3-32b":                                "qwen/qwen3.8-27b",
	"qwen/qwen3.6-27b":                              "qwen/qwen3.8-27b",
	"meta-llama/llama-4-scout-17b-16e-instruct":     "qwen/qwen3.8-27b",
	"meta-llama/llama-4-maverick-17b-128e-instruct": "qwen/qwen3.8-27b",
	"groq/compound":                                 groqDefaultModel,
	"groq/compound-mini":                            "openai/gpt-oss-20b",
}

func groqAPIKey() string {
	if appConfig == nil {
		return ""
	}
	if v, ok := appConfig.Variables.Custom["groq_api_key"].(string); ok && v != "" {
		// Attempt to decrypt if the value is encrypted
		if strings.HasPrefix(v, "ENC:") {
			decrypted, err := DecryptAPIToken(v, "groq_api_key")
			if err == nil {
				return decrypted
			}
			return ""
		}
		return v
	}
	if v, ok := appConfig.Variables.Custom["GroqApiKey"].(string); ok {
		// Attempt to decrypt if the value is encrypted
		if strings.HasPrefix(v, "ENC:") {
			decrypted, err := DecryptAPIToken(v, "GroqApiKey")
			if err == nil {
				return decrypted
			}
			return ""
		}
		return v
	}
	return ""
}

func groqModel() string {
	configured := ""
	if appConfig != nil {
		if v, ok := appConfig.Variables.Custom["groq_model"].(string); ok {
			configured = strings.TrimSpace(v)
		}
	}
	return normalizeGroqModel(configured)
}

// normalizeGroqModel maps empty or retired model ids onto a model that Groq
// currently serves. Groq shut down llama-3.3-70b-versatile and
// llama-3.1-8b-instant on 2026-08-16, so saved configs keep working without
// asking the user to edit JSON.
func normalizeGroqModel(model string) string {
	if model == "" {
		return groqDefaultModel
	}
	if replacement, ok := groqRetiredModels[strings.ToLower(model)]; ok {
		logDebug("groq", "model %q was retired; using %q", model, replacement)
		return replacement
	}
	return model
}

// groqCandidatesFor is groqCandidates for an already-resolved first choice
// (the chat's sender loop has a snapshot, not the live config).
func groqCandidatesFor(first string) []string {
	if first == "" {
		first = groqDefaultModel
	}
	chain := []string{first}
	for _, candidate := range []string{groqDefaultModel, groqFallbackModel, groqThirdModel} {
		if !strings.EqualFold(candidate, first) {
			chain = append(chain, candidate)
		}
	}
	return chain
}

// groqError carries the HTTP status so callers can decide whether another
// model is worth trying.
type groqError struct {
	status int
	msg    string
	model  bool // model-specific failure (retired / not enabled)
}

func (e *groqError) Error() string { return e.msg }

func groqErrorFromResponse(status int, body []byte) *groqError {
	apiMsg := ""
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		apiMsg = strings.TrimSpace(payload.Error.Message)
	}
	modelErr := status == http.StatusNotFound ||
		(status == http.StatusBadRequest &&
			(strings.Contains(strings.ToLower(apiMsg), "model") ||
				strings.Contains(strings.ToLower(apiMsg), "decommission") ||
				strings.Contains(strings.ToLower(apiMsg), "not found")))

	var msg string
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		msg = "Groq rejected the API key. Check groq_api_key in Settings."
	case status == http.StatusTooManyRequests:
		msg = "Groq rate limit reached. Wait a moment and try again."
	case modelErr:
		msg = "Groq model unavailable: " + apiMsg
	case status >= 500:
		msg = fmt.Sprintf("Groq is unavailable right now (HTTP %d).", status)
	case apiMsg != "":
		msg = "Groq error: " + apiMsg
	default:
		msg = fmt.Sprintf("Groq error: HTTP %d", status)
	}
	return &groqError{status: status, msg: msg, model: modelErr}
}

// buildGroqMessagesFrom builds the request from a history: the system prompt,
// the exchanges since the last "New conversation" divider, and the question.
func buildGroqMessagesFrom(history []ChatMessage, userText string) []map[string]string {
	messages := []map[string]string{{"role": "system", "content": jukaSystemPrompt()}}

	for i := len(history) - 1; i >= 0; i-- {
		if chatIsSystem(history[i]) {
			history = history[i+1:]
			break
		}
	}
	// The question is stored before it is asked; a trailing copy is dropped so
	// it is not sent twice.
	if n := len(history); n > 0 && chatIsOwn(history[n-1]) &&
		strings.TrimSpace(history[n-1].Text) == userText {
		history = history[:n-1]
	}
	if len(history) > 12 {
		history = history[len(history)-12:]
	}

	total := 0
	for _, msg := range history {
		role := ""
		switch {
		case chatIsAI(msg):
			role = "assistant"
		case chatIsOwn(msg), strings.EqualFold(msg.Sender, "User"):
			role = "user"
		default:
			continue // Discord chatter and system notices are not Juka's words
		}
		if msg.Pending || msg.Error != "" {
			continue // an unanswered question is not part of the conversation
		}
		// A question that carried files names them; their contents went with
		// that question (chatAIFileContext).
		text := chatAIMessageText(msg)
		if text == "" {
			continue
		}
		if len(text) > 1200 {
			text = text[:1200]
		}
		if total+len(text) > 6000 {
			break
		}
		total += len(text)
		messages = append(messages, map[string]string{"role": role, "content": text})
	}
	return append(messages, map[string]string{"role": "user", "content": userText})
}

// sendGroqChatMessage asks Juka a question in the Juka AI tab. The question is
// queued and answered by the chat's sender loop (chatsync.go), so offline it
// simply waits for the connection. Without a key nothing is posted: the tab's
// setup hint says where the key goes.
func sendGroqChatMessage(userText string) {
	chatAskAI(userText)
}

// newChatMessageID gives a local message a stable id, so the selection and the
// quote target follow it even when the history scrolls or is trimmed. The
// letter in front keeps it from ever reading as a Discord id: sixteen hex
// digits are all decimal about once in 1,850 ids, and isDiscordSnowflake would
// take such a local message for one Discord already has.
func newChatMessageID() string {
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	return fmt.Sprintf("l%x", idBytes)
}

func sendChatMessage(sender, text string) {
	// "System" lines are the app talking to this user (a missing key, a file
	// that could not be read); they are never posted to the channel.
	postChatMessage(sender, text, sender != "System")
}

// postChatMessage adds a message to the community tab. With mirror set, a
// message of this device's own is queued for the Discord channel: the sender
// loop delivers it now, or as soon as the device is back online.
func postChatMessage(sender, text string, mirror bool) {
	postChatMessageFiles(sender, text, nil, mirror)
}

// postChatMessageFiles is postChatMessage for a message that carries files
// (attached on this device, so each has a Path); they go up with it.
func postChatMessageFiles(sender, text string, files []ChatFile, mirror bool) {
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return
	}
	msg := newChatMessage(sender, text)
	msg.Files = files
	// A reply picked from the action bar travels with the message, so the
	// quote survives a restart and becomes a Discord reply.
	msg.Quote = takeChatReply(sender)
	queued := mirror && msg.Own
	msg.Pending = queued
	chatMutex.Lock()
	chatAppendLocked(chatTabCommunity, msg)
	chatMutex.Unlock()
	chatDirty.Store(true)
	if !msg.Own {
		// Incoming messages (a relayed one, an app notice) go to the idle feed.
		noteScreensaverChat(msg.Sender, chatMessagePreview(msg))
	}
	if queued {
		chatDiscordWanted.Store(true)
		chatOutboxPoke()
	}
}

// isLocalChatSender reports whether a sender name is this device's user (the
// current display name or one of the legacy defaults).
func isLocalChatSender(sender string) bool {
	return strings.EqualFold(sender, chatDisplayName()) ||
		strings.EqualFold(sender, "You") ||
		strings.EqualFold(sender, "User")
}

// senderColorForName returns a stable, readable accent color for a sender name.
func senderColorForName(name string) sdl.Color {
	colors := []sdl.Color{
		{R: 85, G: 216, B: 255, A: 255},
		{R: 139, G: 124, B: 255, A: 255},
		{R: 72, G: 213, B: 151, A: 255},
		{R: 255, G: 191, B: 105, A: 255},
		{R: 226, G: 104, B: 120, A: 255},
		{R: 255, G: 122, B: 122, A: 255},
		{R: 173, G: 122, B: 255, A: 255},
	}
	h := 0
	for _, r := range name {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	return colors[h%len(colors)]
}

// renderChatAvatar draws the sender's picture when the chat behind it supplies
// one, and a disc in the sender's colour with their initials when it does not
// (or while the picture is still being fetched).
func renderChatAvatar(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, size int32, name, avatarURL string, col sdl.Color) {
	if tex := getChatAvatarTexture(renderer, avatarURL); tex != nil {
		chatDstRect = sdl.Rect{X: x, Y: y, W: size, H: size}
		_ = renderer.Copy(tex, nil, &chatDstRect)
		return
	}
	col.A = 255
	chatFillDisc(renderer, x, y, size, col)
	if font == nil {
		return
	}
	initials := sanitizeCached(font, chatInitials(name))
	fw, fh := measureText(font, initials)
	drawTextRun(renderer, config, font, initials, contrastText(col), x+(size-fw)/2, y+(size-fh)/2)
}

// chatLinkLabel is what a link card shows, worked out once per link: parsing
// a URL on every frame is a handful of allocations per card.
type chatLinkLabel struct {
	host    string
	path    string
	initial string
	preview string
	play    bool
}

// chatLinkLabels caches chatLinkLabel by link; it is dropped whole when full.
var chatLinkLabels = make(map[string]chatLinkLabel, 32)

// chatLinkLabelFor returns the card text for a link.
func chatLinkLabelFor(link string) chatLinkLabel {
	if l, ok := chatLinkLabels[link]; ok {
		return l
	}
	if len(chatLinkLabels) >= 256 {
		chatLinkLabels = make(map[string]chatLinkLabel, 32)
	}
	host, path := chatLinkHostLabel(link)
	l := chatLinkLabel{host: host, path: path, preview: chatLinkPreviewURL(link), play: chatLinkOpensInPlayer(link)}
	if r := []rune(host); len(r) > 0 {
		l.initial = strings.ToUpper(string(r[0]))
	}
	chatLinkLabels[link] = l
	return l
}

// drawChatLinkCard draws one link preview: a thumbnail, the site and path, and
// the action the click performs. Every link in a message is one card, so a
// reader can see where it goes before opening it.
func drawChatLinkCard(renderer *sdl.Renderer, config *Config, font, small *ttf.Font, rect sdl.Rect, linkURL string) {
	if font == nil {
		return
	}
	if small == nil {
		small = font
	}
	radius := min32(chatPx(12), rect.H/2)
	fill := glassA(12)
	if chatHover(rect) {
		fill = glassA(20)
	}
	chatFillRound(renderer, rect, radius, fill)
	chatStrokeRound(renderer, rect, radius, glassA(28))

	pad := chatPx(6)
	icon := rect.H - pad*2
	iconX, iconY := rect.X+pad, rect.Y+pad
	label := chatLinkLabelFor(linkURL)
	host, path := label.host, label.path
	if tex := getChatImageTexture(renderer, label.preview); tex != nil {
		// Keep the preview's aspect inside the square tile instead of
		// stretching a 4:3 poster frame into it.
		dw, dh := icon, icon
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			dh = icon * th / tw
			if dh > icon {
				dh = icon
				dw = icon * tw / th
			}
		}
		chatDstRect = sdl.Rect{X: iconX + (icon-dw)/2, Y: iconY + (icon-dh)/2, W: dw, H: dh}
		_ = renderer.Copy(tex, nil, &chatDstRect)
	} else {
		chatFillRound(renderer, sdl.Rect{X: iconX, Y: iconY, W: icon, H: icon}, chatPx(8), WithAlpha(glassAccent(), 40))
		if label.initial != "" {
			initial := sanitizeCached(small, label.initial)
			iw, ih := measureText(small, initial)
			drawTextRun(renderer, config, small, initial, ColorTextPrimary(), iconX+(icon-iw)/2, iconY+(icon-ih)/2)
		}
	}

	action := "OPEN"
	actionCol := InfoInk()
	if label.play {
		action = "PLAY"
		actionCol = glassAccent()
	}
	aw, _ := measureText(small, action)
	actionX := rect.X + rect.W - aw - chatPx(12)
	drawTextRun(renderer, config, small, action, actionCol, actionX, rect.Y+(rect.H-int32(small.Height()))/2)

	textX := iconX + icon + chatPx(10)
	textW := actionX - textX - chatPx(10)
	if textW <= 0 {
		return
	}
	lines := int32(small.Height()) * 2
	top := rect.Y + (rect.H-lines)/2
	drawTextRun(renderer, config, small, ellipsize(small, sanitizeCached(small, host), textW), ColorTextPrimary(), textX, top)
	drawTextRun(renderer, config, small, ellipsize(small, sanitizeCached(small, path), textW), ColorTextTertiary(), textX, top+int32(small.Height()))
}

// renderChat draws the chat into a config element's rect: the generic element
// renderer's entry point for a scene that places a "chat" element. The Chat
// scene itself is drawn by its module (chatRenderScene) into the same rect.
func renderChat(renderer *sdl.Renderer, config *Config, element Element) {
	area := sdl.Rect{
		X: element.X,
		Y: element.Y,
		W: getElementWidth(element, 1160),
		H: getElementHeight(element, 480),
	}
	chatRenderArea(renderer, config, area)
}

// ellipsizeLine trims a line until it fits with a trailing ellipsis.
func ellipsizeLine(font *ttf.Font, line string, maxW int32) string {
	measure := func(v string) int32 {
		w, _, _ := font.SizeUTF8(v)
		return int32(w)
	}
	runes := []rune(line)
	for len(runes) > 0 && measure(string(runes)+"…") > maxW {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
