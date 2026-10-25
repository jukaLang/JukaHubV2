package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
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
}

// LinkPreview holds metadata for a URL link
type LinkPreview struct {
	URL      string
	Title    string
	Desc     string
	SiteName string
	ImageURL string
	Fetched  bool
	ThumbTex *sdl.Texture
}

// ChatAttachments stores images for rendering
var chatImages = make(map[string]*sdl.Texture)
var chatImageMutex sync.Mutex

// chatImageOrder is the insertion order of chatImages, oldest first. The
// texture cache is bounded: a long chat with image replies used to pin every
// decoded image in GPU memory for the life of the process.
var chatImageOrder []string

const chatImageCacheMax = 16

// evictChatImagesLocked destroys the oldest textures past the limit. The
// caller holds chatImageMutex.
func evictChatImagesLocked(keep string) {
	for len(chatImageOrder) > chatImageCacheMax {
		oldest := chatImageOrder[0]
		chatImageOrder = chatImageOrder[1:]
		if oldest == keep {
			continue
		}
		if tex, ok := chatImages[oldest]; ok {
			if tex != nil {
				tex.Destroy()
			}
			delete(chatImages, oldest)
		}
	}
}

var chatImageDownloads = make(map[string][]byte)
var chatImageFetching = make(map[string]bool)
var chatImageDLMutex sync.Mutex
var linkPreviews = make(map[string]*LinkPreview)
var urlRegex = regexp.MustCompile(`https?://[^\s<>"]+`)
var imageURLRegex = regexp.MustCompile(`\.(jpg|jpeg|png|gif|webp|bmp)(\?.*)?$`)

func getChatImageTexture(renderer *sdl.Renderer, url string) *sdl.Texture {
	chatImageMutex.Lock()
	if tex, ok := chatImages[url]; ok {
		chatImageMutex.Unlock()
		return tex
	}
	chatImageMutex.Unlock()

	chatImageDLMutex.Lock()
	if data, ok := chatImageDownloads[url]; ok && len(data) > 0 {
		chatImageDLMutex.Unlock()
		return uploadChatImage(renderer, url, data)
	}
	chatImageDLMutex.Unlock()

	go fetchChatImage(url)

	return nil
}

// chatAttachmentRoot is the directory local chat attachments may be read from.
// The File Explorer sets it to its own root when it attaches an image, and the
// chat renderer seeds it from the configured explorer root so an attachment
// restored from a saved history still previews after a restart. Anything else
// — a path typed into a message, or one carried in from a remote room — is
// refused rather than read and drawn.
var chatAttachmentRoot = "."

// chatLocalAttachment resolves a message's local attachment path against
// chatAttachmentRoot. It returns false unless the result is an existing image
// file inside the root and small enough to decode.
func chatLocalAttachment(path string) (string, bool) {
	if !isImageExtension(strings.ToLower(filepath.Ext(path))) {
		return "", false
	}
	resolved, err := regularFileWithin(chatAttachmentRoot, path)
	if err != nil {
		log.Printf("[SECURITY] [chat] refusing local attachment %q: %v", path, err)
		return "", false
	}
	if info, err := os.Stat(resolved); err != nil || info.Size() > 32<<20 {
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
	}
	chatImageDLMutex.Unlock()
}

func uploadChatImage(renderer *sdl.Renderer, url string, data []byte) *sdl.Texture {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Max.X, bounds.Max.Y
	if w <= 0 || h <= 0 {
		return nil
	}
	rgba := image.NewRGBA(bounds)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(w), int32(h))
	if err != nil {
		return nil
	}
	tex.Update(nil, unsafe.Pointer(&rgba.Pix[0]), rgba.Stride)
	chatImageMutex.Lock()
	if old, ok := chatImages[url]; ok {
		// Two fetch paths can race for one URL; keep one texture and one
		// order entry so eviction never frees the live copy.
		if old != nil {
			old.Destroy()
		}
	} else {
		chatImageOrder = append(chatImageOrder, url)
	}
	chatImages[url] = tex
	evictChatImagesLocked(url)
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
var (
	chatAvatarMutex   sync.Mutex
	chatAvatars       = map[string]*sdl.Texture{}
	chatAvatarOrder   []string
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
	if tex, ok := chatAvatars[url]; ok {
		chatAvatarMutex.Unlock()
		return tex
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
		log.Printf("[chat] avatar download failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[chat] avatar download failed: %s", resp.Status)
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
		log.Printf("[chat] avatar decode failed: %v", err)
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
	if old, ok := chatAvatars[url]; ok {
		if old != nil {
			old.Destroy()
		}
	} else {
		chatAvatarOrder = append(chatAvatarOrder, url)
	}
	chatAvatars[url] = tex
	evictChatAvatarsLocked(url)
	chatAvatarMutex.Unlock()
	return tex
}

// evictChatAvatarsLocked destroys the oldest avatar textures past the limit.
// The caller holds chatAvatarMutex.
func evictChatAvatarsLocked(keep string) {
	for len(chatAvatarOrder) > chatAvatarCacheMax {
		oldest := chatAvatarOrder[0]
		chatAvatarOrder = chatAvatarOrder[1:]
		if oldest == keep {
			continue
		}
		if tex, ok := chatAvatars[oldest]; ok {
			if tex != nil {
				tex.Destroy()
			}
			delete(chatAvatars, oldest)
		}
	}
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

// clearChatAvatarCache drops every cached avatar texture. The textures belong
// to one renderer, so they must go before it is destroyed.
func clearChatAvatarCache() {
	chatAvatarMutex.Lock()
	for _, tex := range chatAvatars {
		if tex != nil {
			tex.Destroy()
		}
	}
	chatAvatars = map[string]*sdl.Texture{}
	chatAvatarOrder = nil
	chatAvatarMutex.Unlock()

	chatAvatarDLMutex.Lock()
	chatAvatarData = map[string][]byte{}
	chatAvatarPending = map[string]bool{}
	chatAvatarFailed = map[string]bool{}
	chatAvatarDLMutex.Unlock()
}

// ChatSection is the chat history section persisted inside jukauser.json.
type ChatSection struct {
	Messages []ChatMessage `json:"messages"`
	// Name is the display name the local user chats under (default "You").
	Name string `json:"name,omitempty"`
}

// chatDisplayName returns the name the local user's messages are signed with.
func chatDisplayName() string {
	if userConfigCache != nil && userConfigCache.Chat != nil {
		if n := strings.TrimSpace(userConfigCache.Chat.Name); n != "" {
			return n
		}
	}
	return "You"
}

// setChatDisplayName updates and persists the local user's chat name. An empty
// (or whitespace-only) name resets to the default.
func setChatDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "You"
	}
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	if userConfigCache.Chat == nil {
		userConfigCache.Chat = &ChatSection{}
	}
	userConfigCache.Chat.Name = name
	saveUserConfig(userConfigCache)
	return name
}

// defaultDiscordChannel is the JukaHub Discord channel the chat connects to when
// no channel id is configured. Mirrors the channel id used by the original app.
const defaultDiscordChannel = "975787212954275916"

// discordStatus holds a short human-readable Discord connection status shown in
// the chat UI.
var discordStatus string

// chatScrollOffset is the number of messages to skip from the end when scrolling.
var chatScrollOffset int

// chatAskAIBtnRect is the "Ask Juka AI" pill inside the composer (mouse hit
// target); the Y button is the controller/keyboard equivalent.
var chatAskAIBtnRect sdl.Rect
var chatSendBtnRect sdl.Rect

// chatAttachBtnRect is the "Attach image" pill inside the composer (mouse hit
// target).
var chatAttachBtnRect sdl.Rect

// chatInputRect tracks the chat text input area for mouse click detection.
var chatInputRect sdl.Rect

// chatNameRect is the display-name pill in the chat header (mouse hit target);
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

// chatLineLinkTargets keeps the spans that survived the wrap intact. A link the
// renderer clipped to fit the last line would otherwise play a malformed URL.
func chatLineLinkTargets(line string, whole map[string]bool) []chatLineLinkSpan {
	var targets []chatLineLinkSpan
	for _, span := range chatLineLinkSpans(line) {
		if whole[span.url] {
			targets = append(targets, span)
		}
	}
	return targets
}

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

// firstChatURL returns the first http(s) URL in a message, or "".
func firstChatURL(text string) string {
	m := urlRegex.FindString(text)
	if m == "" {
		return ""
	}
	return trimChatURL(m)
}

// firstChatImageURL returns the first URL in a message that points at an image,
// so pasting an image link shows a thumbnail instead of a bare address.
func firstChatImageURL(text string) string {
	for _, m := range urlRegex.FindAllString(text, -1) {
		u := trimChatURL(m)
		if imageURLRegex.MatchString(u) {
			return u
		}
	}
	return ""
}

// isYouTubeURL reports whether a URL is a YouTube watch/short link. yt-dlp
// handles both, so they can go straight to the embedded player.
func isYouTubeURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "youtube.com/") || strings.Contains(lower, "youtu.be/")
}

// chatPlayableURL returns the first link in a message that the media player can
// open: a YouTube link, a direct audio/video file, or a live stream.
func chatPlayableURL(text string) string {
	for _, m := range urlRegex.FindAllString(text, -1) {
		u := trimChatURL(m)
		if isYouTubeURL(u) || hasMediaExtension(u) {
			return u
		}
	}
	return ""
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
	for _, m := range urlRegex.FindAllString(text, -1) {
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

// chatLineLinkSpans finds the URLs drawn on one wrapped line, so a tap on the
// link text itself can play or open it. The rects are measured from the same
// line and font the renderer uses, which keeps the target exactly on the text.
func chatLineLinkSpans(line string) []chatLineLinkSpan {
	var spans []chatLineLinkSpan
	for _, m := range urlRegex.FindAllStringIndex(line, -1) {
		if m[0] >= m[1] {
			continue
		}
		u := trimChatURL(line[m[0]:m[1]])
		if u == "" {
			continue
		}
		spans = append(spans, chatLineLinkSpan{start: m[0], end: m[0] + len(u), url: u})
	}
	return spans
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
	if imageURLRegex.MatchString(u) {
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

// currentSceneHasChat reports whether the visible scene owns a chat panel. Link
// hitboxes are only meaningful there; they are not cleared when another scene
// renders, so the click handler must not consult stale ones.
func currentSceneHasChat(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
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
		log.Printf("[DISCORD] Stored discord_token does not have an ENC: prefix; rename it to an encrypted value before enabling Discord.")
		return "", defaultDiscordChannel
	}

	decrypted, err := DecryptAPIToken(tok, "discord_token")
	if err != nil {
		log.Printf("[DISCORD] Failed to decrypt token: %v", err)
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
				log.Printf("[DISCORD] Token is configured as a user token but Discord accepted it as a bot token")
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
var discordMentionPattern = regexp.MustCompile(`<(@!?|@&|#)([0-9]+)>`)

// discordResolveMentions rewrites those tokens into names. User names come from
// the message itself (Discord lists every pinged user in the payload); roles and
// channels come from the small cache filled when the chat connects.
func discordResolveMentions(text string, m *discordgo.Message) string {
	if text == "" || !strings.Contains(text, "<") {
		return text
	}
	return discordMentionPattern.ReplaceAllStringFunc(text, func(token string) string {
		parts := discordMentionPattern.FindStringSubmatch(token)
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
		ImageURL:  extractImageURL(m),
		AvatarURL: discordAvatarURL(m),
		UserID:    userID,
		Handle:    handle,
		Bot:       bot,
	}
}

func extractImageURL(msg *discordgo.Message) string {
	for _, att := range msg.Attachments {
		if strings.HasPrefix(att.ContentType, "image/") {
			return att.URL
		}
	}
	return ""
}

// discordConnect opens a live Discord gateway session using the configured bot
// token and listens for new messages in the configured channel. It returns
// immediately; the connection is established in the background.
func discordConnect() error {
	tok, ch := discordCreds()
	if tok == "" {
		return fmt.Errorf("discord not configured: %s", discordStatus)
	}
	if ch == "" {
		discordStatus = "Discord: channel not set"
		return fmt.Errorf("discord not configured: channel not set")
	}
	if discordSession != nil {
		discordSession.Close()
		discordSession = nil
	}
	auth, authErr := discordValidateToken(tok, discordTokenType())
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
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.ChannelID != ch {
			return
		}
		// Skip our own bot's messages to avoid duplicating the locally shown send.
		if m.Author != nil && m.Author.Bot && s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID {
			return
		}
		sender, text := discordMessageParts(m.Message)
		entry := chatMessageFromDiscord(m.Message)
		chatMutex.Lock()
		chatMessages = append(chatMessages, entry)
		if len(chatMessages) > 500 {
			chatMessages = chatMessages[len(chatMessages)-500:]
		}
		chatMutex.Unlock()
		saveChatMessages()
		// A message that arrives while the panel is idle should be readable on
		// the screensaver without waking the device.
		noteScreensaverChat(sender, text)
	})
	if err := dg.Open(); err != nil {
		discordStatus = "Discord: " + err.Error()
		return err
	}
	discordSession = dg
	discordStatus = "Discord: connecting..."
	// Fetch recent history in the background.
	go func() {
		// Role and channel names first: message text is rewritten when it is
		// stored, so the cache has to be ready before the history is read or a
		// role ping would keep its numeric id forever.
		if info, cerr := dg.Channel(ch); cerr == nil && info != nil && info.GuildID != "" {
			// The guild id is what a profile needs to ask Discord for roles
			// and a join date, so it is remembered for the session.
			discordGuildID = info.GuildID
			resetDiscordMemberDetails()
			if guild, gerr := dg.Guild(info.GuildID); gerr == nil && guild != nil {
				cacheDiscordNames(guild.Roles, guild.Channels)
			}
		}
		msgs, ferr := dg.ChannelMessages(ch, 10, "", "", "")
		if ferr != nil {
			discordStatus = "Discord: " + ferr.Error()
			return
		}
		chatMutex.Lock()
		chatMessages = chatMessages[:0]
		for _, m := range msgs {
			chatMessages = append(chatMessages, chatMessageFromDiscord(m))
		}
		// Discord returns newest-first; reverse to chronological.
		for i, j := 0, len(chatMessages)-1; i < j; i, j = i+1, j-1 {
			chatMessages[i], chatMessages[j] = chatMessages[j], chatMessages[i]
		}
		chatMutex.Unlock()
		saveChatMessages()
		discordStatus = "Discord: connected"
	}()
	return nil
}

// discordSendMessage posts a message to the configured Discord channel using the
// live gateway session. The sender is prefixed ("username: message") so the
// message interoperates with the TSP bot bridge used elsewhere in the community.
func discordSendMessage(sender, text string) error {
	return discordSendMessageReply(sender, text, "")
}

// discordSendMessageReply posts a message and, when replyToID names a Discord
// message, sends it as a real reply so the quoted context is visible on both
// sides. A deleted original is not an error (FailIfNotExists is false).
func discordSendMessageReply(sender, text, replyToID string) error {
	if discordSession == nil {
		return fmt.Errorf("discord not connected")
	}
	ch := discordChannel()
	if ch == "" {
		return fmt.Errorf("discord not configured: channel not set")
	}
	payload := text
	if sender != "" {
		payload = sender + ": " + text
	}
	data := &discordgo.MessageSend{Content: payload}
	if replyToID != "" {
		failIfMissing := false
		data.Reference = &discordgo.MessageReference{
			MessageID:       replyToID,
			ChannelID:       ch,
			FailIfNotExists: &failIfMissing,
		}
	}
	if _, err := discordSession.ChannelMessageSendComplex(ch, data); err != nil {
		discordStatus = "Discord send: " + err.Error()
		return err
	}
	discordStatus = "Discord: connected"
	return nil
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
		log.Printf("[groq] model %q was retired; using %q", model, replacement)
		return replacement
	}
	return model
}

// groqCandidates returns the configured model followed by the current
// production lineup, so a single unavailable model does not break the
// assistant.
func groqCandidates() []string {
	first := groqModel()
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

// buildGroqMessages assembles a short conversation so the assistant keeps
// context instead of answering every message from scratch.
func buildGroqMessages(userText string) []map[string]string {
	messages := []map[string]string{{"role": "system", "content": groqSystemPrompt}}

	chatMutex.Lock()
	history := append([]ChatMessage(nil), chatMessages...)
	chatMutex.Unlock()

	// sendGroqChatMessage stores the prompt before requesting, so drop the
	// trailing copy to avoid sending it twice.
	if n := len(history); n > 0 && strings.EqualFold(history[n-1].Sender, "You") &&
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
		case strings.EqualFold(msg.Sender, "You"), strings.EqualFold(msg.Sender, "User"):
			role = "user"
		case strings.EqualFold(msg.Sender, "Juka AI"):
			role = "assistant"
		default:
			continue // Discord chatter and system notices are not Juka's words
		}
		text := strings.TrimSpace(msg.Text)
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

// groqRequestModel performs one chat completion against a single model.
func groqRequestModel(client *http.Client, key, model string, messages []map[string]string) (string, error) {
	payload := map[string]interface{}{
		"model":                 model,
		"messages":              messages,
		"temperature":           0.6,
		"max_completion_tokens": 1024,
	}
	// gpt-oss models are reasoning models; low effort keeps replies snappy on
	// handheld hardware without truncating the visible answer.
	if strings.HasPrefix(model, "openai/gpt-oss") {
		payload["reasoning_effort"] = "low"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", groqEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Groq request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", groqErrorFromResponse(resp.StatusCode, raw)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("Groq returned an unreadable response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", errors.New("Groq returned no response.")
	}
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

// groqComplete asks each candidate model until one answers.
func groqComplete(key, userText string) (string, error) {
	client := &http.Client{Timeout: 45 * time.Second}
	messages := buildGroqMessages(userText)
	var lastErr error
	for _, model := range groqCandidates() {
		reply, err := groqRequestModel(client, key, model, messages)
		if err == nil {
			if reply == "" {
				return "", errors.New("Groq returned an empty reply.")
			}
			return reply, nil
		}
		lastErr = err
		var apiErr *groqError
		if errors.As(err, &apiErr) && apiErr.model {
			log.Printf("[groq] model %s unavailable: %s", model, apiErr.msg)
			continue
		}
		return "", err
	}
	return "", lastErr
}

// groqPending counts in-flight assistant requests (rendered as a typing row).
var groqPending int32

func groqIsPending() bool { return atomic.LoadInt32(&groqPending) == 1 }

func sendGroqChatMessage(userText string) {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return
	}
	key := groqAPIKey()
	if key == "" {
		groqStatus = "Groq: no API key"
		sendChatMessage("System", "Groq API key not configured. Set groq_api_key in config.")
		return
	}
	sendChatMessage(chatDisplayName(), userText)
	groqStatus = "Groq: thinking..."
	atomic.StoreInt32(&groqPending, 1)
	go func() {
		defer atomic.StoreInt32(&groqPending, 0)
		reply, err := groqComplete(key, userText)
		if err != nil {
			groqStatus = "Groq: error"
			log.Printf("[groq] request failed: %v", err)
			sendChatMessage("System", err.Error())
			return
		}
		groqStatus = "Groq: connected"
		sendChatMessage("Juka AI", reply)
	}()
}

// groqStatusColor tints the status label according to its state.
func groqStatusColor() sdl.Color {
	switch {
	case strings.Contains(groqStatus, "error"), strings.Contains(groqStatus, "no API key"):
		return ColorDanger
	case strings.Contains(groqStatus, "thinking"):
		return ColorWarning
	}
	return ColorSuccess
}

func loadChatMessages() {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	chatMessages = []ChatMessage{}
	if userConfigCache != nil && userConfigCache.Chat != nil {
		chatMessages = userConfigCache.Chat.Messages
	}
	if chatMessages == nil {
		chatMessages = []ChatMessage{}
	}
}

func saveChatMessages() {
	chatMutex.Lock()
	msgs := append([]ChatMessage(nil), chatMessages...)
	chatMutex.Unlock()
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	// Preserve the display name: rebuilding the section must not drop it.
	name := ""
	if userConfigCache.Chat != nil {
		name = userConfigCache.Chat.Name
	}
	userConfigCache.Chat = &ChatSection{Messages: msgs, Name: name}
	saveUserConfig(userConfigCache)
}

// newChatMessageID gives a local message a stable id, so the selection and the
// quote target follow it even when the history scrolls or is trimmed.
func newChatMessageID() string {
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	return fmt.Sprintf("%x", idBytes)
}

func sendChatMessage(sender, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	chatMutex.Lock()
	msg := ChatMessage{
		ID:        newChatMessageID(),
		Sender:    sender,
		Text:      strings.TrimSpace(text),
		Timestamp: time.Now(),
		Own:       isLocalChatSender(sender),
		// A reply picked from the action bar travels with the message, so
		// the quote survives a restart and becomes a Discord reply.
		Quote: takeChatReply(sender),
	}
	chatMessages = append(chatMessages, msg)
	if len(chatMessages) > 500 {
		chatMessages = chatMessages[len(chatMessages)-500:]
	}
	chatMutex.Unlock()
	saveChatMessages()
	if !msg.Own {
		// Incoming replies (Juka AI, a relayed message) go to the idle feed too.
		noteScreensaverChat(msg.Sender, msg.Text)
	}

	// Mirror the message to Discord when configured, quoting the original when
	// the reply points at a Discord message.
	if appConfig != nil {
		replyID := ""
		if msg.Quote != nil {
			replyID = msg.Quote.ID
		}
		go func() {
			if err := discordSendMessageReply(sender, text, replyID); err != nil {
				log.Printf("[discord] send failed: %v", err)
			}
		}()
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
// one, and the colored initial circle when it does not (or while the picture is
// still being fetched).
func renderChatAvatar(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, size int32, name, avatarURL string, col sdl.Color) {
	if tex := getChatAvatarTexture(renderer, avatarURL); tex != nil {
		renderer.Copy(tex, nil, &sdl.Rect{X: x, Y: y, W: size, H: size})
		return
	}
	fillCircle(renderer, x+size/2, y+size/2, size/2, col)
	initial := "?"
	runes := []rune(name)
	if len(runes) > 0 {
		initial = string(runes[0])
	}
	fw, _, _ := font.SizeUTF8(initial)
	textX := x + (size-int32(fw))/2
	textY := y + (size-int32(font.Height()))/2
	renderText(renderer, config, font, initial, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textX, textY)
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
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusSM, WithAlpha(ColorSurface, 235))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusSM, 1, WithAlpha(ColorInfo, 110))

	pad := int32(5)
	icon := rect.H - pad*2
	iconX, iconY := rect.X+pad, rect.Y+pad
	host, path := chatLinkHostLabel(linkURL)
	if tex := getChatImageTexture(renderer, chatLinkPreviewURL(linkURL)); tex != nil {
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
		renderer.Copy(tex, nil, &sdl.Rect{X: iconX + (icon-dw)/2, Y: iconY + (icon-dh)/2, W: dw, H: dh})
	} else {
		fillRoundedRect(renderer, iconX, iconY, icon, icon, 4, WithAlpha(ColorInfo, 45))
		if host != "" {
			initial := strings.ToUpper(string([]rune(host)[0]))
			if iw, ih, err := small.SizeUTF8(initial); err == nil {
				renderText(renderer, config, small, initial, ColorTextPrimary(), iconX+(icon-int32(iw))/2, iconY+(icon-int32(ih))/2)
			}
		}
	}

	action := "OPEN"
	actionCol := WithAlpha(ColorInfo, 235)
	if chatLinkOpensInPlayer(linkURL) {
		action = "PLAY"
		actionCol = ColorTextAccent()
	}
	aw, _, _ := small.SizeUTF8(action)
	actionX := rect.X + rect.W - int32(aw) - 8
	renderText(renderer, config, small, action, actionCol, actionX, rect.Y+(rect.H-int32(small.Height()))/2)

	textX := iconX + icon + 7
	textW := actionX - textX - 8
	if textW <= 0 {
		return
	}
	renderText(renderer, config, font, ellipsize(font, host, textW), ColorTextPrimary(), textX, rect.Y+5)
	renderText(renderer, config, small, ellipsize(small, path, textW), ColorTextSecondary(), textX, rect.Y+5+int32(font.Height()))
}

// drawPencilGlyph draws the small edit mark used by the chat name pill. It is
// a filled 45° pencil (tip bottom-left) rather than a character, because the
// bundled fonts have no pencil glyph and a substituted one read as a slash.
func drawPencilGlyph(renderer *sdl.Renderer, x, y, size int32, col sdl.Color) {
	if size < 8 {
		size = 8
	}
	fs := float64(size)
	dirX, dirY := 0.7071, -0.7071
	perpX, perpY := 0.7071, 0.7071
	bodyW := fs * 0.34
	bodyLen := fs * 0.86
	round := func(v float64) int32 { return int32(math.Round(v)) }
	at := func(px, py float64) pt { return pt{round(px), round(py)} }

	tip := pt{x, y + size/2}
	baseX := float64(x) + dirX*bodyW*0.8
	baseY := float64(y+size/2) + dirY*bodyW*0.8
	endX := baseX + dirX*bodyLen
	endY := baseY + dirY*bodyLen
	c1 := at(baseX+perpX*bodyW/2, baseY+perpY*bodyW/2)
	c2 := at(baseX-perpX*bodyW/2, baseY-perpY*bodyW/2)
	c3 := at(endX-perpX*bodyW/2, endY-perpY*bodyW/2)
	c4 := at(endX+perpX*bodyW/2, endY+perpY*bodyW/2)

	fillTriangleFilled(renderer, tip, c1, c2, col)
	fillQuad(renderer, c1, c2, c3, c4, col)
}

func renderChat(renderer *sdl.Renderer, config *Config, element Element) {
	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}

	// Reset the hit targets this frame. chatNameRect in particular used to be
	// cleared after the header was drawn, which left the rename pill visible
	// but unclickable.
	chatNameRect = sdl.Rect{}
	chatAskAIBtnRect = sdl.Rect{}
	chatSendBtnRect = sdl.Rect{}
	chatAttachBtnRect = sdl.Rect{}
	chatInputRect = sdl.Rect{}
	chatLinkRects = chatLinkRects[:0]
	chatTextLinkRects = chatTextLinkRects[:0]
	chatImageRects = chatImageRects[:0]
	chatMessageHits = chatMessageHits[:0]
	chatHeaderMembersRect = sdl.Rect{}
	chatJumpRect = sdl.Rect{}
	chatReplyCancelRect = sdl.Rect{}
	chatReplyStrip = sdl.Rect{}
	// One stat per session: the explorer root only changes with the config, and
	// the attach path updates this as soon as the user picks a new folder.
	if chatAttachmentRoot == "." && config != nil {
		chatAttachmentRoot = feRoot(config)
	}

	listX := int32(element.X) + SpaceMD
	listY := int32(element.Y) + SpaceMD
	listW := getElementWidth(element, 1160) - 2*SpaceMD
	listH := getElementHeight(element, 480) - 2*SpaceMD

	const headerH = int32(44)
	const inputH = int32(50)
	msgAreaTop := listY + headerH + SpaceSM

	// Panel background with subtle border.
	fillRoundedRect(renderer, listX, listY, listW, listH, RadiusMD, ColorSurfaceRaised)
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 120)
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: listW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: 1, H: listH - 2})
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 60)
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + listH - 2, W: listW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: listX + listW - 2, Y: listY + 1, W: 1, H: listH - 2})

	// Status indicators row.
	statusY := listY + SpaceSM
	statusFont, _ := getCachedFont(config, "small")
	if statusFont == nil {
		statusFont = font
	}
	statusRight := listX + listW - SpaceMD
	statusLeft := statusRight

	// The reply strip sits above the composer and takes its room from the
	// message area, so the newest bubble is never covered by it.
	replyH := int32(0)
	if !chatSelectionBar.open {
		replyH = chatReplyStripHeight(statusFont)
	}
	msgAreaBottom := listY + listH - inputH - SpaceSM - replyH
	if replyH > 0 {
		msgAreaBottom -= SpaceXS
	}

	// Discord status (right-aligned).
	if discordStatus != "" {
		dsw, _, _ := statusFont.SizeUTF8(discordStatus)
		statusRight -= int32(dsw) + SpaceSM
		renderText(renderer, config, statusFont, discordStatus, ColorTextTertiary(), statusRight, statusY)
		statusLeft = statusRight
		statusRight -= SpaceSM
	}

	// Groq status (left of Discord).
	if groqStatus != "" {
		gw, _, _ := statusFont.SizeUTF8(groqStatus)
		groqX := statusRight - int32(gw) - SpaceSM
		if groqX < listX+SpaceMD {
			groqX = listX + SpaceMD
		}
		renderText(renderer, config, statusFont, groqStatus, groqStatusColor(), groqX, statusY)
		statusLeft = groqX
	}

	// Display-name pill (left of the status labels): tap to rename the local
	// chat user. The label shows the active name so the change is discoverable.
	// The edit mark is drawn, not typed: Inter has no pencil codepoint and the
	// font substitution replaced the old "✎" with an ellipsis, so the pill read
	// "You …" on every device.
	nameLabel := chatDisplayName()
	nw, _, _ := statusFont.SizeUTF8(nameLabel)
	npad := int32(10)
	pencil := int32(12)
	nph := int32(statusFont.Height()) + SpaceXS*2
	chatNameRect = sdl.Rect{X: listX + SpaceMD, Y: statusY - SpaceXS, W: int32(nw) + npad*2 + pencil + SpaceXS, H: nph}
	fillRoundedRect(renderer, chatNameRect.X, chatNameRect.Y, chatNameRect.W, chatNameRect.H, nph/2, WithAlpha(ColorInfo, 45))
	strokeRoundedRect(renderer, chatNameRect.X, chatNameRect.Y, chatNameRect.W, chatNameRect.H, nph/2, 1, WithAlpha(ColorInfo, 120))
	renderText(renderer, config, statusFont, nameLabel, ColorTextSecondary(), chatNameRect.X+npad, statusY)
	drawPencilGlyph(renderer, chatNameRect.X+npad+int32(nw)+SpaceXS, chatNameRect.Y+nph/2, pencil, WithAlpha(ColorInfo, 220))

	// Members chip: the controller path to a speaker's profile. It is dropped
	// rather than crowded when the status labels already reach the name pill.
	membersCount := len(chatMembersList())
	membersLabel := fmt.Sprintf("Members %d", membersCount)
	if mw, _, _ := statusFont.SizeUTF8(membersLabel); int32(mw)+18+SpaceSM <= statusLeft-(chatNameRect.X+chatNameRect.W) {
		chipX := chatNameRect.X + chatNameRect.W + SpaceSM
		chatHeaderMembersRect = drawChatMembersChip(renderer, config, statusFont, chipX, chatNameRect.Y, membersCount)
	}

	// Divider under status.
	dividerY := statusY + int32(statusFont.Height()) + SpaceXS
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
	renderer.FillRect(&sdl.Rect{X: listX + SpaceMD, Y: dividerY, W: listW - 2*SpaceMD, H: 1})

	// Messages area.
	chatMutex.Lock()
	messages := chatMessages
	chatMutex.Unlock()

	bubblePad := int32(12)
	avatarSize := int32(28)
	maxBubbleW := listW * 3 / 4
	lineH := int32(font.Height())
	bubbleGap := int32(10)

	// Clamp scroll offset to a valid range.
	if chatScrollOffset < 0 {
		chatScrollOffset = 0
	}
	if chatScrollOffset > len(messages)-1 {
		chatScrollOffset = len(messages) - 1
	}
	if chatScrollOffset < 0 {
		chatScrollOffset = 0
	}

	// Reading the newest message marks the history seen; a reader scrolled back
	// keeps the count so the jump pill can say how many messages arrived.
	chatMutex.Lock()
	total := len(chatMessages)
	chatMutex.Unlock()
	if chatScrollOffset == 0 {
		chatSeenCount = total
	}
	if chatSeenCount > total {
		chatSeenCount = total
	}
	if chatSeenCount < 0 {
		chatSeenCount = 0
	}
	chatUnread = total - chatSeenCount
	chatClampSelection()

	// An empty room draws just the rename pill over bare background, which
	// read as a stray "You ✏" chip from the audit. Give the empty history the
	// shared state card instead, so the pill sits next to a real explanation.
	if len(messages) == 0 && replyH == 0 {
		UIEmptyState(renderer, config, sdl.Rect{
			X: listX + SpaceMD, Y: msgAreaTop, W: listW - 2*SpaceMD, H: msgAreaBottom - msgAreaTop,
		}, "", "No messages yet", "Say hello below, or press Y / Ask AI for a solo chat.")
	}

	// Render messages from the bottom up so the latest message sits at the bottom.
	startY := msgAreaBottom
	endIdx := len(messages) - 1 - chatScrollOffset
	chatNewestShown = endIdx
	chatOldestShown = endIdx
	for i := endIdx; i >= 0 && startY > msgAreaTop; i-- {
		msg := messages[i]
		isUser := msg.Own || isLocalChatSender(msg.Sender)
		isAI := strings.EqualFold(msg.Sender, "Juka AI")
		isSystem := strings.EqualFold(msg.Sender, "System") && !isAI

		// A follow-up from the same speaker is a continuation: the name and
		// avatar are already on screen above it. A reply or a new day always
		// starts a fresh group.
		grouped := !isSystem && chatContinuesGroup(messages, i)
		quoteH := int32(0)
		if msg.Quote != nil {
			quoteH = int32(statusFont.Height())*2 + 6 + SpaceXS
		}

		text := msg.Text
		lines := wrapChatLines(font, text, maxBubbleW-bubblePad*2, 8)
		// The links as written, for validating the wrapped ones below.
		wholeLinks := make(map[string]bool, chatLinkCardLimit)
		for _, l := range allChatLinks(text) {
			wholeLinks[l] = true
		}
		bw := int32(0)
		for _, line := range lines {
			if w, _, _ := font.SizeUTF8(line); int32(w) > bw {
				bw = int32(w)
			}
		}
		bw += bubblePad * 2
		if bw < 120 {
			bw = 120
		}
		if msg.Quote != nil && bw < 240 && maxBubbleW >= 240 {
			// Leave the quote header room for a readable author and preview.
			bw = 240
		}
		if bw > maxBubbleW {
			bw = maxBubbleW
		}
		bh := int32(len(lines))*lineH + bubblePad*2

		// A pasted image URL gets the same inline preview as an attachment;
		// a YouTube or media URL gets a play pill that opens the media player.
		thumbURL := msg.ImageURL
		if thumbURL == "" {
			thumbURL = firstChatImageURL(text)
		}
		var imageH int32
		var thumbTex *sdl.Texture
		if thumbURL != "" {
			thumbTex = getChatImageTexture(renderer, thumbURL)
			if thumbTex != nil {
				imageH = 80 + SpaceXS
			}
		}
		// Every link becomes a preview card. The bubble grows to a card width so
		// a short message with one URL still shows a readable preview.
		links := chatMessageLinks(text)
		if thumbURL != "" {
			// The image already has its own preview tile; a card for the same
			// URL would draw it twice.
			kept := links[:0]
			for _, l := range links {
				if l != thumbURL {
					kept = append(kept, l)
				}
			}
			links = kept
		}
		linkCardH := int32(44)
		linkCardGap := int32(SpaceXS)
		linkH := int32(0)
		if len(links) > 0 {
			linkH = int32(len(links))*linkCardH + int32(len(links)-1)*linkCardGap + SpaceXS
			minBubble := int32(280)
			if minBubble > maxBubbleW {
				minBubble = maxBubbleW
			}
			if bw < minBubble {
				bw = minBubble
			}
		}
		bh += imageH + linkH + quoteH

		by := startY - bh
		if by < msgAreaTop {
			break
		}

		var bx int32
		var senderCol sdl.Color
		var avatarHit, senderHit sdl.Rect
		if isUser {
			bx = listX + listW - bw - SpaceSM
			senderCol = ColorInfo
		} else if isAI {
			bx = listX + avatarSize + SpaceSM*2
			senderCol = ColorTextAccent()
			if !grouped {
				avatarHit = sdl.Rect{X: listX + SpaceSM, Y: by + (bh-avatarSize)/2, W: avatarSize, H: avatarSize}
				renderChatAvatar(renderer, config, font, avatarHit.X, avatarHit.Y, avatarSize, "J", "", uiHighlightColor())
			}
		} else if isSystem {
			bx = listX + (listW-bw)/2
			senderCol = ColorWarning
		} else {
			bx = listX + avatarSize + SpaceSM*2
			senderCol = senderColorForName(msg.Sender)
			if !grouped {
				// Avatar: the speaker's own picture when it exists. Clicking it
				// opens their profile, exactly like the name does.
				avatarHit = sdl.Rect{X: listX + SpaceSM, Y: by + (bh-avatarSize)/2, W: avatarSize, H: avatarSize}
				renderChatAvatar(renderer, config, font, avatarHit.X, avatarHit.Y, avatarSize, msg.Sender, msg.AvatarURL, senderCol)
			}
		}

		// Bubble background.
		bubbleCol := ColorSurface
		if isUser {
			bubbleCol = WithAlpha(ColorInfo, 60)
		} else if isAI {
			bubbleCol = WithAlpha(ColorSurfaceAlt, 240)
		} else if isSystem {
			bubbleCol = WithAlpha(ColorSurfaceAlt, 200)
		}
		fillRoundedRect(renderer, bx, by, bw, bh, RadiusMD, bubbleCol)
		if isUser {
			strokeRoundedRect(renderer, bx-1, by-1, bw+2, bh+2, RadiusMD+1, 1, WithAlpha(ColorInfo, 120))
		} else if isAI {
			// Accent spine marks assistant replies without shouting.
			fillRoundedRect(renderer, bx, by+8, 3, bh-16, 2, uiHighlightColor())
		}
		// The selected message wears the accent ring the whole UI uses for
		// focus, so it is obvious which message the action bar acts on.
		if i == chatSelected {
			strokeRoundedRect(renderer, bx-2, by-2, bw+4, bh+4, RadiusMD+2, 2, WithAlpha(accentColor, 210))
		}

		// Sender name + timestamp above the bubble, once per group. Clicking
		// either opens the sender's profile card.
		if !isSystem && !grouped {
			nameText := fmt.Sprintf("%s • %s", msg.Sender, msg.Timestamp.Format("15:04"))
			nameX := bx
			nameY := by - int32(statusFont.Height()) - 2
			nameW, _, _ := statusFont.SizeUTF8(msg.Sender)
			senderHit = sdl.Rect{X: nameX, Y: nameY, W: int32(nameW), H: int32(statusFont.Height())}
			renderText(renderer, config, statusFont, nameText, senderCol, nameX, nameY)
		}

		// Quote header for a reply.
		if msg.Quote != nil {
			drawChatQuoteBlock(renderer, config, statusFont, bx+bubblePad, by+bubblePad, bw-bubblePad*2, msg.Quote)
		}

		// Message text (wrapped so long AI answers stay readable).
		for li, line := range lines {
			lineY := by + bubblePad + quoteH + int32(li)*lineH
			renderText(renderer, config, font, line, ColorTextPrimary(), bx+bubblePad, lineY)
			// A tap on the link text itself plays or opens it, not only the
			// preview card below. Only a link that survived the wrap intact is
			// a target: a clipped tail (the ellipsised last line) would play a
			// malformed URL.
			for _, span := range chatLineLinkTargets(line, wholeLinks) {
				prefixW, _, _ := font.SizeUTF8(line[:span.start])
				urlW, _, _ := font.SizeUTF8(line[span.start:span.end])
				chatTextLinkRects = append(chatTextLinkRects, chatLinkHit{
					rect: sdl.Rect{X: bx + bubblePad + int32(prefixW), Y: lineY, W: int32(urlW), H: lineH},
					url:  span.url,
					play: chatLinkOpensInPlayer(span.url),
				})
			}
		}

		// Inline image thumbnail. The rect is recorded so a click can open the
		// same image full screen.
		if thumbTex != nil {
			iw := int32(120)
			ih := int32(80)
			ix := bx + bubblePad
			iy2 := by + bubblePad + quoteH + int32(len(lines))*lineH + SpaceXS
			fillRoundedRect(renderer, ix, iy2, iw, ih, RadiusSM, ColorSurface)
			renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
			renderer.DrawRect(&sdl.Rect{X: ix, Y: iy2, W: iw, H: ih})
			renderer.Copy(thumbTex, nil, &sdl.Rect{X: ix + 1, Y: iy2 + 1, W: iw - 2, H: ih - 2})
			chatImageRects = append(chatImageRects, chatImageHit{
				rect: sdl.Rect{X: ix, Y: iy2, W: iw, H: ih},
				url:  thumbURL,
			})
		}

		// One card per link: thumbnail, site, path and the action it performs.
		// The rects are recorded for the click handler in main.go.
		if len(links) > 0 {
			cardW := bw - bubblePad*2
			if cardW > 360 {
				cardW = 360
			}
			cy := by + bubblePad + quoteH + int32(len(lines))*lineH + imageH
			for i, link := range links {
				r := sdl.Rect{X: bx + bubblePad, Y: cy + int32(i)*(linkCardH+linkCardGap), W: cardW, H: linkCardH}
				drawChatLinkCard(renderer, config, font, statusFont, r, link)
				chatLinkRects = append(chatLinkRects, chatLinkHit{rect: r, url: link, play: chatLinkOpensInPlayer(link)})
			}
		}

		// Record the targets this message owns: the body selects it, the avatar
		// and the name open the speaker's profile.
		chatMessageHits = append(chatMessageHits, chatMessageHit{
			index:      i,
			body:       sdl.Rect{X: bx, Y: by, W: bw, H: bh},
			avatar:     avatarHit,
			name:       senderHit,
			sender:     msg.Sender,
			userID:     msg.UserID,
			ownMessage: msg.Own,
		})
		chatOldestShown = i

		aboveY := by - bubbleGap
		if !isSystem && !grouped {
			aboveY -= int32(statusFont.Height()) + 2
		}
		// A divider closes a group: a new day, or the boundary between what the
		// reader had already seen and what arrived since.
		label := chatDayLabel(msg.Timestamp)
		unreadBoundary := chatUnread > 0 && i == len(messages)-chatUnread
		if i == 0 || !chatSameDay(messages[i-1].Timestamp, msg.Timestamp) || unreadBoundary {
			dividerH := int32(statusFont.Height()) + 10
			if aboveY-dividerH > msgAreaTop {
				if unreadBoundary {
					label = "NEW MESSAGES"
				}
				drawChatDayDivider(renderer, config, statusFont, listX+SpaceSM, listW-SpaceSM*2, aboveY-dividerH, label, unreadBoundary)
				aboveY -= dividerH
			}
		}
		startY = aboveY
	}

	// Assistant typing indicator.
	if groqIsPending() && startY-lineH-bubblePad*2 > msgAreaTop {
		tipW := int32(200)
		tipH := lineH + bubblePad*2
		tipY := startY - tipH
		tipX := listX + avatarSize + SpaceSM*2
		fillRoundedRect(renderer, tipX, tipY, tipW, tipH, RadiusMD, WithAlpha(ColorSurfaceAlt, 220))
		fillRoundedRect(renderer, tipX, tipY+8, 3, tipH-16, 2, WithAlpha(uiHighlightColor(), 170))
		dots := strings.Repeat("•", 1+int((sdl.GetTicks64()/350)%3))
		renderText(renderer, config, font, "Juka is typing "+dots, ColorTextSecondary(), tipX+bubblePad, tipY+bubblePad)
	}

	processChatImageDownloads(renderer)

	// Single composer: click the input area to type, Send to send, Ask AI for Groq.
	inputY := listY + listH - inputH - SpaceSM
	if replyH > 0 {
		drawChatReplyStrip(renderer, config, statusFont, sdl.Rect{
			X: listX + SpaceSM,
			Y: inputY - replyH - SpaceXS,
			W: listW - 2*SpaceSM,
			H: replyH,
		})
	}
	// Unread pill: how many messages arrived while the reader is scrolled back.
	drawChatJumpPill(renderer, config, statusFont, listX+SpaceSM, inputY-replyH-SpaceXS, listW-2*SpaceSM)

	// A selected message turns the composer into its action bar, so a
	// controller never needs a second navigation mode to act on a message.
	if chatSelectionBar.open {
		drawChatSelectionBar(renderer, config, statusFont, sdl.Rect{
			X: listX + SpaceSM, Y: inputY, W: listW - 2*SpaceSM, H: inputH,
		})
		chatInputRect = sdl.Rect{}
		chatSendBtnRect = sdl.Rect{}
		chatAskAIBtnRect = sdl.Rect{}
		chatAttachBtnRect = sdl.Rect{}
		return
	}
	inputX := listX + SpaceSM
	inputW := listW - 2*SpaceSM
	inputBorderCol := ColorBorder
	if chatInputActive {
		inputBorderCol = WithAlpha(ColorInfo, 160)
	}
	chatInputRect = sdl.Rect{X: inputX, Y: inputY, W: inputW, H: inputH}
	fillRoundedRect(renderer, inputX, inputY, inputW, inputH, RadiusMD, ColorSurfacePanel)
	strokeRoundedRect(renderer, inputX, inputY, inputW, inputH, RadiusMD, 1, inputBorderCol)

	if chatInputText == "" {
		renderText(renderer, config, font, "Type a message...", ColorTextTertiary(), listX+SpaceMD, inputY+(inputH-lineH)/2)
	} else {
		renderText(renderer, config, font, chatInputText, ColorTextPrimary(), listX+SpaceMD, inputY+(inputH-lineH)/2)
	}

	// Send button + Ask Juka AI pill on the right of the composer.
	if statusFont != nil {
		pad := int32(10)
		ah := int32(32)
		// Send button - always visible (grayed when empty).
		sendLabel := "Send"
		sw, _, _ := statusFont.SizeUTF8(sendLabel)
		saw := int32(sw) + pad*2
		sax := listX + listW - SpaceSM - saw
		say := inputY + (inputH-ah)/2
		sendFill, sendLabelCol := ColorIconSurface, ColorTextTertiary()
		if chatInputText != "" {
			sendFill, sendLabelCol = WithAlpha(ColorInfo, 200), sdl.Color{R: 255, G: 255, B: 255, A: 255}
		}
		chatSendBtnRect = UIButton(renderer, config, sdl.Rect{X: sax, Y: say, W: saw, H: ah}, sendLabel, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       sendFill,
			HoverFill:  sendFill,
			FocusFill:  sendFill,
			Border:     WithAlpha(sendFill, 160),
			LabelColor: sendLabelCol,
			Font:       statusFont,
			Pill:       true,
			NoShadow:   true,
		}, UIButtonState{})
		// Ask Juka AI pill.
		aiLabel := "Ask AI"
		aw, _, _ := statusFont.SizeUTF8(aiLabel)
		aaw := int32(aw) + pad*2
		aax := chatSendBtnRect.X - aaw - SpaceSM
		aay := inputY + (inputH-ah)/2
		chatAskAIBtnRect = UIButton(renderer, config, sdl.Rect{X: aax, Y: aay, W: aaw, H: ah}, aiLabel, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       ColorIconSurface,
			Border:     WithAlpha(ColorInfo, 120),
			LabelColor: ColorTextPrimary(),
			Font:       statusFont,
			Pill:       true,
			NoShadow:   true,
		}, UIButtonState{})
		// Attach image pill (left of Ask AI); the composer hit-test in main.go
		// reads chatAttachBtnRect to trigger the image picker.
		attachLabel := "Attach"
		attw, _, _ := statusFont.SizeUTF8(attachLabel)
		attaw := int32(attw) + pad*2
		attax := aax - attaw - SpaceSM
		attay := inputY + (inputH-ah)/2
		chatAttachBtnRect = UIButton(renderer, config, sdl.Rect{X: attax, Y: attay, W: attaw, H: ah}, attachLabel, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       ColorIconSurface,
			Border:     WithAlpha(ColorInfo, 120),
			LabelColor: ColorTextPrimary(),
			Font:       statusFont,
			Pill:       true,
			NoShadow:   true,
		}, UIButtonState{})
	}

	// The composer's controls are communicated once, by the shared footer
	// (D-Pad Navigate / A Send / Y Ask AI / B Back); no second hint line here.
}

func handleChatInput(e *sdl.KeyboardEvent, config *Config) {
	if e.Type == sdl.KEYDOWN {
		switch e.Keysym.Sym {
		case sdl.K_RETURN:
			if chatInputText != "" {
				sendChatMessage(chatDisplayName(), chatInputText)
				chatInputText = ""
			}
		case sdl.K_BACKSPACE:
			if len(chatInputText) > 0 {
				// Remove the last complete rune, not just the last byte.
				_, size := utf8.DecodeLastRuneInString(chatInputText)
				if size > 0 {
					chatInputText = chatInputText[:len(chatInputText)-size]
				}
			}
		case sdl.K_ESCAPE:
			chatInputText = ""
			clearChatSelection()
		case sdl.K_LEFT:
			moveChatSelection(-1)
		case sdl.K_RIGHT:
			moveChatSelection(1)
		case sdl.K_TAB:
			// Tab opens the member list, the keyboard way to reach someone who
			// has not written recently enough for their name to be on screen.
			openChatMembers()
		case sdl.K_y:
			if strings.TrimSpace(chatInputText) != "" {
				sendGroqChatMessage(chatInputText)
				chatInputText = ""
			}
		case sdl.K_UP:
			chatScrollOffset++
		case sdl.K_DOWN:
			if chatScrollOffset > 0 {
				chatScrollOffset--
			}
		}
	}
}

// wrapChatLines word-wraps chat text into at most maxLines lines, adding an
// ellipsis when the message is longer. Long AI answers used to be cut off
// after a single line by fitTextWidth.
func wrapChatLines(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	if font == nil || maxW <= 0 {
		return []string{text}
	}
	joined := strings.Join(strings.Fields(strings.ReplaceAll(text, string(rune(10)), " ")), " ")
	if joined == "" {
		return []string{""}
	}
	measure := func(v string) int32 {
		w, _, _ := font.SizeUTF8(v)
		return int32(w)
	}
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	overflow := false
	for _, word := range strings.Split(joined, " ") {
		if word == "" {
			continue
		}
		for measure(word) > maxW && len([]rune(word)) > 1 {
			flush()
			if len(lines) >= maxLines {
				overflow = true
				break
			}
			runes := []rune(word)
			i := len(runes)
			for i > 1 && measure(string(runes[:i])) > maxW {
				i--
			}
			lines = append(lines, string(runes[:i]))
			word = strings.TrimLeft(string(runes[i:]), " ")
		}
		if overflow {
			break
		}
		candidate := word
		if cur != "" {
			candidate = cur + " " + word
		}
		if measure(candidate) <= maxW {
			cur = candidate
			continue
		}
		flush()
		if len(lines) >= maxLines {
			overflow = true
			break
		}
		cur = word
	}
	if !overflow {
		flush()
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		overflow = true
	}
	if overflow && len(lines) > 0 {
		lines[len(lines)-1] = ellipsizeLine(font, lines[len(lines)-1], maxW)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
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
