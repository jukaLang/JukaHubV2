package main

// chatsync.go — getting queued chat messages out whenever the network allows.
//
// Every message typed in Chat is stored first and sent second: it goes into
// its tab marked Pending, and the loop here delivers it — to the Discord
// channel for the community tab, to Groq for Juka AI. Offline nothing is lost
// and nothing errors: the message waits (and says so under its bubble), the
// Discord connection is retried with a backoff, and the queue drains by itself
// once the device is back online, across restarts too since Pending is saved.
//
// Only network failures keep a message queued. A refusal (a rejected token, a
// message Discord will not take, a Groq error) marks the message with one
// short sentence instead of the raw error, and Retry is in its actions.
//
// The loop never reads the live config: its variables map is written by the
// main thread every frame and a concurrent read can take the process down, so
// chatTick copies the few values the loop needs (chatRefreshCreds).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// chatNetState is what the community tab knows about its Discord connection.
type chatNetState int32

const (
	chatNetIdle chatNetState = iota
	chatNetConnecting
	chatNetLive
	chatNetOffline
	chatNetUnconfigured
	chatNetRejected
	chatNetUnreachable
)

// chatSyncInterval is how often the loop looks at the queue when nothing woke
// it. Sending wakes it at once; this only paces retries.
const chatSyncInterval = 4 * time.Second

var (
	chatSyncOnce   sync.Once
	chatSyncWakeCh = make(chan struct{}, 1)

	// chatDiscordWanted is set once Chat has been opened this session (or a
	// community message is queued): only then does the loop keep a gateway
	// connection up.
	chatDiscordWanted atomic.Bool
	chatDiscordNet    atomic.Int32
	// chatGatewayUp follows the gateway's Connect and Disconnect events:
	// discordgo reconnects by itself, so an open session is not proof of a
	// live one.
	chatGatewayUp atomic.Bool
	chatAIBusy    atomic.Bool
	// chatRetryRequested asks the loop to forget its backoff (the banner's
	// Retry button).
	chatRetryRequested atomic.Bool

	// Retry schedules. The Discord ones belong to the loop goroutine; the AI
	// one is written by the request goroutine, hence the mutex.
	chatDiscordRetryAt time.Time
	chatDiscordBackoff time.Duration
	chatSendRetryAt    time.Time
	chatAIRetryMu      sync.Mutex
	chatAIRetryAt      time.Time
	chatAIBackoff      time.Duration
)

// chatDiscordConnectFn and chatDiscordPost reach Discord. They are variables so
// tests can stand in for the network.
var (
	chatDiscordConnectFn = discordConnectWith
	chatDiscordPost      = discordPostMessage
	chatDiscordReady     = func() bool { return discordSession != nil }
)

// chatNet returns the community connection state.
func chatNet() chatNetState { return chatNetState(chatDiscordNet.Load()) }

func chatSetNet(s chatNetState) { chatDiscordNet.Store(int32(s)) }

// chatSyncEnabled lets the sender loop start. The test binary turns it off
// (testmain_test.go): the tests drive chatSyncStep themselves, and a loop left
// running in the background would reach Discord and Groq from the suite.
var chatSyncEnabled = true

// chatEnsureSync starts the sender loop once per process.
func chatEnsureSync() {
	if !chatSyncEnabled {
		return
	}
	chatSyncOnce.Do(func() { go chatSyncLoop() })
}

// chatWakeSync asks the loop to run now instead of at its next tick.
func chatWakeSync() {
	select {
	case chatSyncWakeCh <- struct{}{}:
	default:
	}
}

func chatSyncLoop() {
	ticker := time.NewTicker(chatSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-chatSyncWakeCh:
		}
		chatSyncStep(time.Now())
	}
}

// chatSyncStep is one pass over both queues. A panic inside a library call is
// logged rather than allowed to take the launcher down with the sender.
func chatSyncStep(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			logDebug("chat", "sender recovered from: %v", r)
		}
	}()
	chatSyncDiscord(now)
	chatSyncAI(now)
}

// ── Credentials snapshot ─────────────────────────────────────────────────

// chatCredSnapshot is what the loop needs from the config.
type chatCredSnapshot struct {
	groqKey    string
	groqModel  string
	discordTok string
	channel    string
	tokenType  string
	rawTok     string
	rawGroq    string
	rawModel   string
	at         time.Time
	valid      bool
}

var (
	chatCredMu sync.Mutex
	chatCreds  chatCredSnapshot
)

// chatRawGroqKey is the stored Groq key value, encrypted or not, without
// decrypting it: enough to notice a change.
func chatRawGroqKey(config *Config) string {
	if config == nil || config.Variables.Custom == nil {
		return ""
	}
	if v, ok := config.Variables.Custom["groq_api_key"].(string); ok && v != "" {
		return v
	}
	v, _ := config.Variables.Custom["GroqApiKey"].(string)
	return v
}

// chatRawDiscordToken is the stored Discord token value, as discordCreds reads
// it.
func chatRawDiscordToken(config *Config) string {
	if config == nil {
		return ""
	}
	tok := strings.TrimSpace(config.ChannelProfile.Token)
	if tok == "" && config.Variables.Custom != nil {
		tok, _ = config.Variables.Custom["discord_token"].(string)
		tok = strings.TrimSpace(tok)
	}
	return tok
}

// chatRefreshCreds copies the chat credentials out of the config on the main
// thread, at most once a second unless forced, and only decrypts a token again
// when its stored value changed.
func chatRefreshCreds(config *Config, now time.Time, force bool) {
	if config == nil || appConfig == nil {
		return
	}
	chatCredMu.Lock()
	snap := chatCreds
	chatCredMu.Unlock()
	if !force && snap.valid && now.Sub(snap.at) < time.Second {
		return
	}
	rawTok := chatRawDiscordToken(appConfig)
	rawGroq := chatRawGroqKey(appConfig)
	if !snap.valid || rawTok != snap.rawTok {
		snap.discordTok, _ = discordCreds()
		snap.rawTok = rawTok
	}
	if !snap.valid || rawGroq != snap.rawGroq {
		snap.groqKey = groqAPIKey()
		snap.rawGroq = rawGroq
	}
	// The model is normalised only when it changes: a retired id is logged
	// each time it is mapped, and this runs every second.
	rawModel, _ := appConfig.Variables.Custom["groq_model"].(string)
	if !snap.valid || rawModel != snap.rawModel {
		snap.groqModel = groqModel()
		snap.rawModel = rawModel
	}
	snap.channel = discordChannel()
	snap.tokenType = discordTokenType()
	snap.at = now
	snap.valid = true
	chatCredMu.Lock()
	chatCreds = snap
	chatCredMu.Unlock()
}

// chatCredsNow returns the last snapshot.
func chatCredsNow() chatCredSnapshot {
	chatCredMu.Lock()
	defer chatCredMu.Unlock()
	return chatCreds
}

// ── Community (Discord) ──────────────────────────────────────────────────

// chatPendingMessages returns the queued messages of a tab, oldest first.
func chatPendingMessages(tab chatTab) []ChatMessage {
	var out []ChatMessage
	for _, msg := range chatTabMessages(tab) {
		if msg.Pending && msg.Error == "" && chatIsOwn(msg) {
			out = append(out, msg)
		}
	}
	return out
}

// chatFailPending marks every queued message of a tab as not sent.
func chatFailPending(tab chatTab, reason string) {
	for _, msg := range chatPendingMessages(tab) {
		chatUpdateMessage(tab, msg.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = reason
		})
	}
}

// chatSyncDiscord keeps the gateway up while Chat wants it and delivers the
// community queue.
func chatSyncDiscord(now time.Time) {
	if chatRetryRequested.Swap(false) {
		chatDiscordRetryAt, chatSendRetryAt = time.Time{}, time.Time{}
		chatDiscordBackoff = 0
		chatUploadRetryReset()
	}
	pending := chatPendingMessages(chatTabCommunity)
	if !chatDiscordWanted.Load() && len(pending) == 0 {
		return
	}
	if !IsOnline() {
		chatSetNet(chatNetOffline)
		return
	}
	if !chatDiscordReady() {
		if now.Before(chatDiscordRetryAt) {
			return
		}
		creds := chatCredsNow()
		if !creds.valid {
			return
		}
		chatSetNet(chatNetConnecting)
		if err := chatDiscordConnectFn(creds.discordTok, creds.channel, creds.tokenType); err != nil {
			state, wait := chatClassifyConnectError(err)
			chatSetNet(state)
			chatDiscordRetryAt = now.Add(wait)
			if state == chatNetUnconfigured || state == chatNetRejected {
				chatFailPending(chatTabCommunity, chatCommunityProblem(state))
			}
			logWarn("chat", "discord connect: %v", err)
			return
		}
		chatDiscordBackoff = 0
		chatGatewayUp.Store(true)
		chatSetNet(chatNetLive)
	} else if chatGatewayUp.Load() {
		if chatNet() != chatNetLive {
			chatSetNet(chatNetLive)
		}
	} else if chatNet() != chatNetConnecting {
		// The session is open but its gateway dropped; discordgo is
		// reconnecting, and its Connect event brings the tab back to Live.
		chatSetNet(chatNetConnecting)
	}
	// While a message's files are going up nothing else is sent, so the
	// channel shows the messages in the order they were written.
	if len(pending) == 0 || now.Before(chatSendRetryAt) || chatUploadBusy.Load() {
		return
	}
	for _, msg := range pending {
		if len(msg.Files) > 0 {
			// Files go up on their own goroutine (chatupload.go), one message
			// at a time; the ones after it wait their turn.
			if chatUploadDue(now) {
				chatStartUpload(msg)
			}
			return
		}
		// The list was read before the first post went out: a message deleted
		// (or delivered by its echo) since then is not posted.
		if cur, ok := chatFindMessage(chatTabCommunity, msg.ID); !ok || !cur.Pending || cur.Error != "" {
			continue
		}
		replyID := ""
		if msg.Quote != nil {
			replyID = msg.Quote.ID
		}
		// While the post is on its way the message is in flight, so an echo
		// that beats Discord's answer finds it (chatEchoCandidate).
		chatPostingBegin(msg.ID)
		discordID, err := chatDiscordPost(msg.Sender, msg.Text, replyID)
		if err != nil {
			chatPostingEnd()
			if chatIsNetworkError(err) {
				chatSendRetryAt = now.Add(15 * time.Second)
				return
			}
			logWarn("chat", "discord send refused: %v", err)
			chatUpdateMessage(chatTabCommunity, msg.ID, func(m *ChatMessage) {
				m.Pending = false
				m.Error = "Not sent — Discord refused the message"
			})
			continue
		}
		posted := chatUpdateMessage(chatTabCommunity, msg.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = ""
			if discordID != "" {
				m.ID = discordID
			}
		})
		chatPostingEnd()
		if !posted && discordID != "" && chatTakeUnsend(msg.ID, nil) {
			// Deleted here while the post was on its way: taken back.
			chatDropRemote(discordID)
		}
	}
}

// chatClassifyConnectError turns a connect failure into a state and the time
// to wait before trying again.
func chatClassifyConnectError(err error) (chatNetState, time.Duration) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.HasPrefix(msg, "discord not configured"):
		return chatNetUnconfigured, 10 * time.Minute
	case strings.Contains(msg, "rejected"):
		return chatNetRejected, 10 * time.Minute
	}
	switch {
	case chatDiscordBackoff < 10*time.Second:
		chatDiscordBackoff = 10 * time.Second
	case chatDiscordBackoff < 2*time.Minute:
		chatDiscordBackoff *= 2
	}
	if chatDiscordBackoff > 2*time.Minute {
		chatDiscordBackoff = 2 * time.Minute
	}
	return chatNetUnreachable, chatDiscordBackoff
}

// chatCommunityProblem is the one sentence a community message shows when it
// cannot be sent at all.
func chatCommunityProblem(state chatNetState) string {
	if state == chatNetUnconfigured {
		return "Not sent — community chat isn't set up on this device"
	}
	return "Not sent — community chat is unavailable right now"
}

// chatIsNetworkError reports whether err is the network failing (worth a
// quiet retry) rather than a server refusing (worth telling the user).
func chatIsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || plainNetworkError(err) == offlinePlainNotice {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection refused", "connection reset", "broken pipe", "unexpected eof",
		"client.timeout", "stream interrupted", "network is down",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// ── Juka AI (Groq) ───────────────────────────────────────────────────────

// chatGroqEndpoint is the chat completions URL; tests point it at a stub.
var chatGroqEndpoint = groqEndpoint

// chatStreamState is the answer Juka is writing right now. chatMutex guards it.
type chatStreamState struct {
	active   bool
	promptID string
	text     string
}

var chatStream chatStreamState

func chatStreamBegin(promptID string) {
	chatMutex.Lock()
	chatStream = chatStreamState{active: true, promptID: promptID}
	chatMutex.Unlock()
}

func chatStreamUpdate(promptID, text string) {
	chatMutex.Lock()
	if chatStream.active && chatStream.promptID == promptID {
		chatStream.text = text
	}
	chatMutex.Unlock()
}

func chatStreamEnd() {
	chatMutex.Lock()
	chatStream = chatStreamState{}
	chatMutex.Unlock()
}

// chatStreamSnapshot returns the answer in progress.
func chatStreamSnapshot() chatStreamState {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	return chatStream
}

// chatNextPrompt is the oldest question still waiting for an answer.
func chatNextPrompt() (ChatMessage, bool) {
	for _, msg := range chatTabMessages(chatTabAI) {
		if msg.Pending && msg.Error == "" && chatIsOwn(msg) {
			return msg, true
		}
	}
	return ChatMessage{}, false
}

// chatAIRetryLater backs the next attempt off after a network failure.
func chatAIRetryLater(now time.Time) {
	chatAIRetryMu.Lock()
	defer chatAIRetryMu.Unlock()
	switch {
	case chatAIBackoff < 5*time.Second:
		chatAIBackoff = 5 * time.Second
	case chatAIBackoff < time.Minute:
		chatAIBackoff *= 2
	}
	if chatAIBackoff > time.Minute {
		chatAIBackoff = time.Minute
	}
	chatAIRetryAt = now.Add(chatAIBackoff)
}

func chatAIRetryReset() {
	chatAIRetryMu.Lock()
	chatAIBackoff = 0
	chatAIRetryAt = time.Time{}
	chatAIRetryMu.Unlock()
}

func chatAIRetryDue(now time.Time) bool {
	chatAIRetryMu.Lock()
	defer chatAIRetryMu.Unlock()
	return !now.Before(chatAIRetryAt)
}

// chatSyncAI starts an answer for the oldest queued question.
func chatSyncAI(now time.Time) {
	if chatAIBusy.Load() {
		return
	}
	prompt, ok := chatNextPrompt()
	if !ok {
		return
	}
	creds := chatCredsNow()
	if !creds.valid {
		return
	}
	if creds.groqKey == "" {
		chatUpdateMessage(chatTabAI, prompt.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = "Add a Groq API key in Settings to ask Juka"
		})
		return
	}
	if !IsOnline() || !chatAIRetryDue(now) {
		return
	}
	chatAIBusy.Store(true)
	go chatRunAI(creds.groqKey, groqCandidatesFor(creds.groqModel), prompt)
}

// chatAIHistoryBefore is the AI conversation that precedes a question.
func chatAIHistoryBefore(promptID string) []ChatMessage {
	list := chatTabMessages(chatTabAI)
	for i, msg := range list {
		if msg.ID == promptID {
			return list[:i]
		}
	}
	return list
}

// chatRunAI asks Groq one question, streaming the answer into the typing
// bubble, and files the answer under the question.
func chatRunAI(key string, models []string, prompt ChatMessage) {
	defer chatAIBusy.Store(false)
	defer func() {
		if r := recover(); r != nil {
			chatStreamEnd()
			logDebug("groq", "request recovered from: %v", r)
		}
	}()
	history := chatAIHistoryBefore(prompt.ID)
	question := strings.TrimSpace(prompt.Text)
	// A text file attached to the question (or, for a follow-up, earlier in
	// the conversation) goes with it as an excerpt (chatfiles.go).
	extra, ferr := chatAIFileContext(prompt, history)
	if ferr != nil {
		logWarn("groq", "attached file: %v", ferr)
		chatUpdateMessage(chatTabAI, prompt.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = "Couldn't read the attached file — Retry reads it again"
		})
		return
	}
	if extra != "" {
		if question == "" {
			question = chatAIFileQuestion
		}
		question += "\n\n" + extra
	}
	groqStatus = "Groq: thinking..."
	chatStreamBegin(prompt.ID)
	messages := buildGroqMessagesFrom(history, question)
	reply, err := chatGroqAsk(key, models, messages, func(partial string) {
		chatStreamUpdate(prompt.ID, partial)
	})
	chatStreamEnd()
	now := time.Now()
	if err != nil {
		logWarn("groq", "request failed: %v", err)
		if chatIsNetworkError(err) {
			groqStatus = "Groq: offline"
			chatAIRetryLater(now)
			return
		}
		groqStatus = "Groq: error"
		chatUpdateMessage(chatTabAI, prompt.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = chatFriendlyAIError(err)
		})
		return
	}
	reply = chatStripThink(reply)
	if reply == "" {
		chatUpdateMessage(chatTabAI, prompt.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = "Juka had no answer — try again"
		})
		return
	}
	chatAIRetryReset()
	groqStatus = "Groq: connected"
	answer := ChatMessage{ID: newChatMessageID(), Sender: chatAISender, Text: reply, Timestamp: now}
	chatMutex.Lock()
	chatUpdateLocked(chatTabAI, prompt.ID, func(m *ChatMessage) {
		m.Pending = false
		m.Error = ""
	})
	chatInsertAfterLocked(chatTabAI, prompt.ID, answer)
	if chatLiveTab.Load() == int32(chatTabAI) {
		chatMarkSeenLocked(chatTabAI)
	} else {
		chatTabs[chatTabAI].unread++
	}
	chatMutex.Unlock()
	chatDirty.Store(true)
	noteScreensaverChat(chatAISender, reply)
	chatWakeSync()
}

// chatFriendlyAIError turns a Groq failure into one sentence for the bubble.
func chatFriendlyAIError(err error) string {
	var apiErr *groqError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.status == http.StatusUnauthorized || apiErr.status == http.StatusForbidden:
			return "Groq rejected the API key — check it in Settings"
		case apiErr.status == http.StatusTooManyRequests:
			return "Juka is busy right now — try again in a moment"
		case apiErr.model:
			return "No Groq model is available right now"
		case apiErr.status >= 500:
			return "Groq is down right now — try again later"
		}
	}
	return "Juka couldn't answer — try again"
}

// chatStripThink removes the <think>…</think> blocks some models put before
// the answer. An unterminated block (still streaming) hides everything after
// it until it closes.
func chatStripThink(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// chatGroqAsk asks each candidate model until one answers, streaming the
// answer through onDelta as it arrives.
func chatGroqAsk(key string, models []string, messages []map[string]string, onDelta func(string)) (string, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	var lastErr error
	for _, model := range models {
		reply, err := chatGroqStreamModel(client, key, model, messages, onDelta)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		var apiErr *groqError
		if errors.As(err, &apiErr) && apiErr.model {
			logWarn("groq", "model %s unavailable: %s", model, apiErr.msg)
			continue
		}
		return "", err
	}
	if lastErr == nil {
		lastErr = errors.New("no Groq model to ask")
	}
	return "", lastErr
}

// chatGroqStreamModel performs one streamed chat completion.
func chatGroqStreamModel(client *http.Client, key, model string, messages []map[string]string, onDelta func(string)) (string, error) {
	payload := map[string]interface{}{
		"model":                 model,
		"messages":              messages,
		"temperature":           0.6,
		"max_completion_tokens": 1024,
		"stream":                true,
	}
	// gpt-oss models are reasoning models; low effort keeps answers quick on
	// handheld hardware without cutting the visible answer short.
	if strings.HasPrefix(model, "openai/gpt-oss") {
		payload["reasoning_effort"] = "low"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, chatGroqEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Groq request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", groqErrorFromResponse(resp.StatusCode, raw)
	}
	return chatReadGroqStream(resp.Body, onDelta)
}

// chatGroqChunk is one event of the streamed answer. A server that ignored
// "stream" answers with one whole completion instead, which has message
// rather than delta.
type chatGroqChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chatReadGroqStream reads an OpenAI-style event stream ("data: {...}" lines
// ending with "data: [DONE]") and returns the whole answer. An interrupted
// stream keeps what already arrived.
func chatReadGroqStream(r io.Reader, onDelta func(string)) (string, error) {
	reader := bufio.NewReaderSize(r, 16<<10)
	var answer strings.Builder
	var plain bytes.Buffer
	sawEvent := false
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			switch {
			case strings.HasPrefix(trimmed, "data:"):
				sawEvent = true
				data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if data == "[DONE]" {
					return answer.String(), nil
				}
				var chunk chatGroqChunk
				if json.Unmarshal([]byte(data), &chunk) != nil {
					break
				}
				if chunk.Error != nil && chunk.Error.Message != "" {
					return answer.String(), errors.New("Groq error: " + chunk.Error.Message)
				}
				grew := false
				for _, c := range chunk.Choices {
					piece := c.Delta.Content
					if piece == "" {
						piece = c.Message.Content
					}
					if piece != "" {
						answer.WriteString(piece)
						grew = true
					}
				}
				if grew && onDelta != nil {
					onDelta(answer.String())
				}
			case !sawEvent && !strings.HasPrefix(trimmed, ":"):
				plain.WriteString(line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if answer.Len() > 0 {
				return answer.String(), nil
			}
			return "", fmt.Errorf("Groq stream interrupted: %w", err)
		}
	}
	if !sawEvent && plain.Len() > 0 {
		var chunk chatGroqChunk
		if err := json.Unmarshal(plain.Bytes(), &chunk); err != nil {
			return "", fmt.Errorf("Groq returned an unreadable response: %w", err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return "", errors.New("Groq error: " + chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			return "", errors.New("Groq returned no response")
		}
		text := strings.TrimSpace(chunk.Choices[0].Message.Content)
		if onDelta != nil && text != "" {
			onDelta(text)
		}
		return text, nil
	}
	return answer.String(), nil
}
