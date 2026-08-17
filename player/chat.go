package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/veandco/go-sdl2/sdl"
)

// --- Chat System ---

type ChatMessage struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	ImageURL  string    `json:"image_url,omitempty"`
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
var chatImageDownloads = make(map[string][]byte)
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

func fetchChatImage(url string) {
	chatImageDLMutex.Lock()
	if _, downloading := chatImageDownloads[url]; downloading {
		chatImageDLMutex.Unlock()
		return
	}
	chatImageDLMutex.Unlock()

	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	chatImageDLMutex.Lock()
	chatImageDownloads[url] = data
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
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_STATIC, int32(w), int32(h))
	if err != nil {
		return nil
	}
	tex.Update(nil, unsafe.Pointer(&rgba.Pix[0]), rgba.Stride)
	chatImageMutex.Lock()
	chatImages[url] = tex
	chatImageMutex.Unlock()
	return tex
}

func processChatImageDownloads(renderer *sdl.Renderer) {
	chatImageDLMutex.Lock()
	var toProcess []struct {
		url  string
		data []byte
	}
	for url, data := range chatImageDownloads {
		chatImageDLMutex.Unlock()
		uploadChatImage(renderer, url, data)
		toProcess = append(toProcess, struct {
			url  string
			data []byte
		}{url, data})
		chatImageDLMutex.Lock()
	}
	chatImageDLMutex.Unlock()
}

const chatJSON = "chat.json"

// defaultDiscordChannel is the JukaHub Discord channel the chat connects to when
// no channel id is configured. Mirrors the channel id used by the original app.
const defaultDiscordChannel = "975787212954275916"

// discordStatus holds a short human-readable Discord connection status shown in
// the chat UI.
var discordStatus string

// groqStatus holds a short human-readable Groq AI connection status shown in
// the chat UI.
var groqStatus string

// discordSession is the live Discord gateway client (discordgo) when connected.
var discordSession *discordgo.Session

// discordCreds reads the configured Discord bot token and channel id. The channel
// id comes from the explicit ChannelProfile (loaded from jukaconfig.json),
// falling back to the runtime Custom value and finally the default JukaHub
// channel. The token comes from ChannelProfile or the runtime Custom entry.
func discordCreds() (string, string) {
	if appConfig == nil {
		return "", defaultDiscordChannel
	}
	tok, _ := appConfig.Variables.Custom["discord_token"].(string)
	if tok == "" {
		tok = appConfig.ChannelProfile.Token
	}
	if strings.HasPrefix(tok, "ENC:") {
		decrypted, err := DecryptToken(tok)
		if err != nil {
			log.Printf("[DISCORD] Failed to decrypt token: %v", err)
			discordStatus = "Discord: failed to decrypt token"
			return "", defaultDiscordChannel
		}
		tok = decrypted
	}
	ch := strings.TrimSpace(appConfig.ChannelProfile.ChannelID)
	if ch == "" {
		ch, _ = appConfig.Variables.Custom["discord_channel"].(string)
		ch = strings.TrimSpace(ch)
	}
	if ch == "" {
		ch = defaultDiscordChannel
	}
	return strings.TrimSpace(tok), ch
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
	if tok == "" || ch == "" {
		discordStatus = "Discord: token / channel not set"
		return fmt.Errorf("discord not configured")
	}
	if discordSession != nil {
		discordSession.Close()
		discordSession = nil
	}
	dg, err := discordgo.New("Bot " + tok)
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
		sender := "unknown"
		if m.Author != nil {
			sender = m.Author.Username
		}
		text := m.Content
		// Bridge: messages from the TSP bot are formatted "username: message".
		if m.Author != nil && strings.HasPrefix(strings.ToLower(m.Author.Username), "trimuismartprojuka") {
			if idx := strings.Index(text, ":"); idx > 0 {
				sender = strings.TrimSpace(text[:idx])
				text = strings.TrimSpace(text[idx+1:])
			}
		}
		chatMutex.Lock()
		chatMessages = append(chatMessages, ChatMessage{
			ID:        m.ID,
			Sender:    sender,
			Text:      text,
			Timestamp: time.Time(m.Timestamp),
			ImageURL:  extractImageURL(m.Message),
		})
		if len(chatMessages) > 500 {
			chatMessages = chatMessages[len(chatMessages)-500:]
		}
		chatMutex.Unlock()
		saveChatMessages()
	})
	if err := dg.Open(); err != nil {
		discordStatus = "Discord: " + err.Error()
		return err
	}
	discordSession = dg
	discordStatus = "Discord: connecting..."
	// Fetch recent history in the background.
	go func() {
		msgs, ferr := dg.ChannelMessages(ch, 10, "", "", "")
		if ferr != nil {
			discordStatus = "Discord: " + ferr.Error()
			return
		}
		chatMutex.Lock()
		chatMessages = chatMessages[:0]
		for _, m := range msgs {
			sender := "unknown"
			if m.Author != nil {
				sender = m.Author.Username
			}
			chatMessages = append(chatMessages, ChatMessage{
				ID:        m.ID,
				Sender:    sender,
				Text:      m.Content,
				Timestamp: time.Time(m.Timestamp),
				ImageURL:  extractImageURL(m),
			})
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
	_, ch := discordCreds()
	if discordSession == nil {
		return fmt.Errorf("discord not connected")
	}
	payload := text
	if sender != "" {
		payload = sender + ": " + text
	}
	if _, err := discordSession.ChannelMessageSend(ch, payload); err != nil {
		discordStatus = "Discord send: " + err.Error()
		return err
	}
	discordStatus = "Discord: connected"
	return nil
}

func groqAPIKey() string {
	if appConfig == nil {
		return ""
	}
	if v, ok := appConfig.Variables.Custom["groq_api_key"].(string); ok && v != "" {
		return v
	}
	if v, ok := appConfig.Variables.Custom["GroqApiKey"].(string); ok {
		return v
	}
	return ""
}

func groqModel() string {
	if appConfig == nil {
		return "llama-3.3-70b-versatile"
	}
	if v, ok := appConfig.Variables.Custom["groq_model"].(string); ok && v != "" {
		return v
	}
	return "llama-3.3-70b-versatile"
}

func sendGroqChatMessage(userText string) {
	key := groqAPIKey()
	if key == "" {
		groqStatus = "Groq: no API key"
		sendChatMessage("System", "Groq API key not configured. Set groq_api_key in config.")
		return
	}
	sendChatMessage("You", userText)
	groqStatus = "Groq: thinking..."
	go func() {
		client := &http.Client{Timeout: 30 * time.Second}
		messages := []map[string]string{
			{"role": "system", "content": "You are Juka, helpful assistant for JukaHub handheld device"},
			{"role": "user", "content": userText},
		}
		body, err := json.Marshal(map[string]interface{}{
			"model":       groqModel(),
			"messages":    messages,
			"temperature": 0.7,
		})
		if err != nil {
			groqStatus = "Groq: error"
			sendChatMessage("System", "Groq request error: "+err.Error())
			return
		}
		req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			groqStatus = "Groq: error"
			sendChatMessage("System", "Groq request error: "+err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			groqStatus = "Groq: error"
			sendChatMessage("System", "Groq request error: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			groqStatus = "Groq: error"
			sendChatMessage("System", fmt.Sprintf("Groq error: HTTP %d", resp.StatusCode))
			return
		}
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			groqStatus = "Groq: error"
			sendChatMessage("System", "Groq parse error: "+err.Error())
			return
		}
		if len(result.Choices) > 0 {
			aiText := result.Choices[0].Message.Content
			groqStatus = "Groq: connected"
			sendChatMessage("Juka AI", aiText)
		} else {
			groqStatus = "Groq: empty"
			sendChatMessage("System", "Groq returned no response.")
		}
	}()
}

func loadChatMessages() {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	data, err := os.ReadFile(chatJSON)
	if err != nil {
		return
	}
	var wrapper struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil {
		chatMessages = wrapper.Messages
	}
	if chatMessages == nil {
		chatMessages = []ChatMessage{}
	}
}

func saveChatMessages() {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	wrapper := struct {
		Messages []ChatMessage `json:"messages"`
	}{Messages: chatMessages}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(chatJSON, data, 0644)
}

func sendChatMessage(sender, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	chatMutex.Lock()
	idBytes := make([]byte, 8)
	rand.Read(idBytes)
	msg := ChatMessage{
		ID:        fmt.Sprintf("%x", idBytes),
		Sender:    sender,
		Text:      strings.TrimSpace(text),
		Timestamp: time.Now(),
	}
	chatMessages = append(chatMessages, msg)
	if len(chatMessages) > 500 {
		chatMessages = chatMessages[len(chatMessages)-500:]
	}
	chatMutex.Unlock()
	saveChatMessages()

	// Mirror the message to Discord when configured.
	if appConfig != nil {
		go func() {
			if err := discordSendMessage(sender, text); err != nil {
				log.Printf("[discord] send failed: %v", err)
			}
		}()
	}
}

func renderChat(renderer *sdl.Renderer, config *Config, element Element) {
	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		return
	}

	listX := element.X + 10
	listY := element.Y + 10
	listW := getElementWidth(element, 1160) - 20
	listH := getElementHeight(element, 480) - 60

	drawPanel(renderer, listX, listY, listW, listH, PanelFill(220), accentColor)

	var discordStatusW int32
	// Discord connection status (top-right of the chat panel)
	if discordStatus != "" {
		statFont, _ := getCachedFont(config, "small")
		if statFont == nil {
			statFont = font
		}
		dsw, _, _ := statFont.SizeUTF8(discordStatus)
		discordStatusW = int32(dsw)
		renderText(renderer, config, statFont, discordStatus, ColorTextTertiary(), listX+listW-discordStatusW-12, listY+8)
	}

	// Groq AI connection status (left of Discord status)
	if groqStatus != "" {
		statFont, _ := getCachedFont(config, "small")
		if statFont == nil {
			statFont = font
		}
		gw, _, _ := statFont.SizeUTF8(groqStatus)
		startX := listX + listW - discordStatusW - 12 - int32(gw) - 16
		if discordStatus != "" && startX < listX+listW-discordStatusW-12-int32(gw)-16 {
			startX = listX + 8
		}
		if startX < listX+8 {
			startX = listX + 8
		}
		renderText(renderer, config, statFont, groqStatus, sdl.Color{R: 100, G: 200, B: 120, A: 255}, startX, listY+8)
	}

	chatMutex.Lock()
	messages := chatMessages
	chatMutex.Unlock()

	lineH := int32(28)
	maxVisible := int(listH / lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := 0
	if len(messages) > maxVisible {
		start = len(messages) - maxVisible
	}
	end := len(messages)
	if start < 0 {
		start = 0
	}

	for i := start; i < end; i++ {
		msg := messages[i]
		iy := listY + 10 + int32(i-start)*lineH
		// subtle alternating row stripe
		if (i-start)%2 == 0 {
			fillRoundedRect(renderer, listX+4, iy, listW-8, lineH-2, 5, GlossFill(4))
		}
		timeStr := msg.Timestamp.Format("15:04")
		prefix := fmt.Sprintf("[%s] %s:", timeStr, msg.Sender)
		line := msg.Text
		if len(line) > 70 {
			line = line[:67] + "..."
		}
		if font != nil {
			senderCol := ColorTextAccent()
			if msg.Sender == "You" || strings.EqualFold(msg.Sender, "user") {
				senderCol = ColorInfo
			}
			renderText(renderer, config, font, prefix, senderCol, listX+12, iy)
			pw, _, _ := font.SizeUTF8(prefix)
			textX := listX + 12 + int32(pw) + 1

			remaining := line
			curX := textX
			for {
				match := urlRegex.FindStringIndex(remaining)
				if match == nil {
					break
				}
				startIdx := match[0]
				endIdx := match[1]
				if startIdx > 0 {
					before := remaining[:startIdx]
					renderText(renderer, config, font, before, ColorTextSecondary(), curX, iy)
					bw, _, _ := font.SizeUTF8(before)
					curX += int32(bw)
				}
				urlStr := remaining[startIdx:endIdx]
				renderText(renderer, config, font, urlStr, sdl.Color{R: 100, G: 180, B: 255, A: 255}, curX, iy)
				uw, _, _ := font.SizeUTF8(urlStr)
				curX += int32(uw)
				remaining = remaining[endIdx:]
			}
			if remaining != "" {
				renderText(renderer, config, font, remaining, ColorTextSecondary(), curX, iy)
			}
		}

		// Image thumbnail below the message
		if msg.ImageURL != "" {
			tex := getChatImageTexture(renderer, msg.ImageURL)
			if tex != nil {
				iw, ih := int32(160), int32(120)
				ix := listX + 12
				iy2 := iy + lineH + 4
				fillRoundedRect(renderer, ix, iy2, iw, ih, 6, sdl.Color{R: 20, G: 24, B: 32, A: 255})
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 100)
				renderer.DrawRect(&sdl.Rect{X: ix, Y: iy2, W: iw, H: ih})
				renderer.Copy(tex, nil, &sdl.Rect{X: ix + 1, Y: iy2 + 1, W: iw - 2, H: ih - 2})
			}
		}
	}

	processChatImageDownloads(renderer)
}

func handleChatInput(e *sdl.KeyboardEvent, config *Config) {
	if e.Type == sdl.KEYDOWN {
		switch e.Keysym.Sym {
		case sdl.K_RETURN:
			if chatInputText != "" {
				sendChatMessage("User", chatInputText)
				chatInputText = ""
			}
		case sdl.K_BACKSPACE:
			if len(chatInputText) > 0 {
				chatInputText = chatInputText[:len(chatInputText)-1]
			}
		default:
			if e.Keysym.Sym != 0 {
				chatInputText += string(rune(e.Keysym.Sym))
			}
		}
	}
}
