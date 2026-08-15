package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Chat System ---

type ChatMessage struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

const chatJSON = "chat.json"

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
	defer chatMutex.Unlock()
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
	saveChatMessages()
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

	drawPanel(renderer, listX, listY, listW, listH, sdl.Color{R: 16, G: 19, B: 26, A: 220}, accentColor)

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
		timeStr := msg.Timestamp.Format("15:04")
		line := fmt.Sprintf("[%s] %s: %s", timeStr, msg.Sender, msg.Text)
		if len(line) > 90 {
			line = line[:87] + "..."
		}
		renderText(renderer, config, font, line, sdl.Color{R: 200, G: 210, B: 230, A: 255}, listX+12, iy)
	}

	inputY := listY + listH + 10
	inputW := listW - 120
	fillRoundedRect(renderer, listX, inputY, inputW, 36, 8, sdl.Color{R: 30, G: 35, B: 48, A: 255})
	display := chatInputText
	if display == "" {
		display = "Type a message..."
	}
	renderText(renderer, config, font, display, sdl.Color{R: 200, G: 210, B: 230, A: 255}, listX+12, inputY+8)

	sendX := listX + inputW + 10
	fillRoundedRect(renderer, sendX, inputY, 100, 36, 8, sdl.Color{R: 44, G: 49, B: 62, A: 255})
	sw, _, _ := font.SizeUTF8("Send")
	renderText(renderer, config, font, "Send", sdl.Color{R: 235, G: 238, B: 245, A: 255}, sendX+(100-int32(sw))/2, inputY+8)
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
