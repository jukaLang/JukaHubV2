package main

// aiagent.go — Juka AI as an agent: the model may call the device tools
// (aitools*.go) to answer, in a loop of at most aiMaxSteps rounds.
//
// Cloud (Groq): the same model chain as the chat (gpt-oss-120b, then
// gpt-oss-20b, then qwen3.8-27b — all take OpenAI-style `tools`), one
// non-streamed request per round, so a round's tool_calls arrive whole. Each
// tool call becomes a short status line in the typing bubble ("Checked
// storage", "Read 40 log lines"); the answer is filed under the question with
// those lines on top. A change pauses the run on a confirmation card in the
// chat (aiagent_view.go): A Allow, B Deny, Y Allow for this conversation. The
// decision goes back to the model as the tool's result. Nothing that changes
// the device runs without that card — a destructive step (a permanent
// delete, stopping a process, a terminal command) asks even after Y.
//
// Offline (no key, or no internet): the handheld's own model is SmolLM2-135M
// with a 384-token context (offline_ai.go) — it cannot call tools or read a
// 4 KB result. So offline Juka is "lite": a keyword router picks read-only
// tools for the question, runs them, and the answer is their results,
// labelled "Offline — Juka can look but not change things".
//
// The model is asked to end with "FOLLOWUPS: a | b | c"; that line is cut
// from the answer and becomes up to three chips under it (aiagent_view.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	aiMaxSteps      = 8
	aiRunTimeout    = 4 * time.Minute
	aiToolTimeout   = 45 * time.Second
	aiConfirmWait   = 3 * time.Minute
	aiFollowupLimit = 3
)

// aiAgentPrompt is what the model is told about its tools.
const aiAgentPrompt = " You run on the user's handheld and have tools to look at it and, with the user's OK, change it. " +
	"For questions about this device (games not starting, storage, battery, Wi-Fi, settings, files, errors) call tools first; never guess what a tool can tell you. " +
	"Tools that change something show the user an Allow/Deny card themselves: when the user asked for the change, call the tool directly instead of asking in text. " +
	"If a change was declined, accept it. Never say something was changed unless the tool result says so. " +
	"Prefer the safe fix: settings and moving files to the trash before permanent deletes. When a fix is a screen the user should see, call open_screen. " +
	"Answer in short plain text (no tables, no markdown headings). " +
	"Finally, on its own last line, write FOLLOWUPS: then up to 3 short next requests the user might tap, separated by |, each under 40 characters, written as the user speaking."

// aiAgentVoicePrompt replaces the follow-ups for the voice orb.
const aiAgentVoicePrompt = " You may only look (read-only tools). Answer in one to three short spoken sentences, no lists, no file paths unless essential. Do not write FOLLOWUPS."

// ── One run ──────────────────────────────────────────────────────────────

// aiRun is one question being answered.
type aiRun struct {
	promptID string
	conv     string // the conversation the question belongs to (allow-all scope)
	readOnly bool
	voice    bool

	mu        sync.Mutex
	statuses  []string
	openLabel string
	open      func(c *Config)
}

type aiRunKey struct{}

// aiRunFrom is the run a tool was called in (nil outside one).
func aiRunFrom(ctx context.Context) *aiRun {
	r, _ := ctx.Value(aiRunKey{}).(*aiRun)
	return r
}

func (r *aiRun) setOpen(label string, open func(c *Config)) {
	r.mu.Lock()
	r.openLabel, r.open = label, open
	r.mu.Unlock()
}

// note adds a status line and shows the lines so far in the typing bubble.
func (r *aiRun) note(line string) {
	r.mu.Lock()
	if line != "" && (len(r.statuses) == 0 || r.statuses[len(r.statuses)-1] != line) {
		r.statuses = append(r.statuses, line)
	}
	text := strings.Join(r.statuses, " · ") + " …"
	r.mu.Unlock()
	if r.promptID != "" {
		chatStreamUpdate(r.promptID, text)
		chatDirty.Store(true)
	}
}

func (r *aiRun) statusLine() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.statuses, " · ")
}

// ── What the chips and the card show ─────────────────────────────────────

// aiChipState is the last answer's follow-ups and its open-a-screen button.
var aiChipState struct {
	mu        sync.Mutex
	promptID  string
	followups []string
	openLabel string
	open      func(c *Config)
}

func aiSetChips(promptID string, followups []string, openLabel string, open func(c *Config)) {
	aiChipState.mu.Lock()
	aiChipState.promptID, aiChipState.followups = promptID, followups
	aiChipState.openLabel, aiChipState.open = openLabel, open
	aiChipGen++
	aiChipState.mu.Unlock()
}

// aiAllowConv is the conversation "Allow for this conversation" was given in.
var aiAllowConv struct {
	mu  sync.Mutex
	key string
	set bool
}

func aiAllowedFor(conv string) bool {
	aiAllowConv.mu.Lock()
	defer aiAllowConv.mu.Unlock()
	return aiAllowConv.set && aiAllowConv.key == conv
}

func aiAllowAllFor(conv string) {
	aiAllowConv.mu.Lock()
	aiAllowConv.key, aiAllowConv.set = conv, true
	aiAllowConv.mu.Unlock()
}

// aiConversationEnded is "New chat" or a cleared Juka AI history: the card
// still up is answered no, and "Allow for this conversation" is forgotten
// (a cleared history starts again at conversation "", the first one's id).
// Any thread.
func aiConversationEnded() {
	aiConfirmAnswer(aiDeny)
	aiAllowConv.mu.Lock()
	aiAllowConv.key, aiAllowConv.set = "", false
	aiAllowConv.mu.Unlock()
}

// aiConversationOf is the id of the "New conversation" divider a question
// follows ("" for the first conversation).
func aiConversationOf(history []ChatMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if chatIsSystem(history[i]) {
			return history[i].ID
		}
	}
	return ""
}

// ── Confirmations ────────────────────────────────────────────────────────

type aiDecision int

const (
	aiDeny aiDecision = iota
	aiAllow
	aiAllowConversation
)

// aiConfirm is the card waiting for the user. The agent's goroutine waits on
// reply; the main thread answers (aiConfirmAnswer).
var aiConfirm struct {
	mu          sync.Mutex
	pending     bool
	text        string
	destructive bool
	reply       chan aiDecision
}

// aiConfirmAsk shows the card and waits for the answer. A card nobody
// answers in aiConfirmWait counts as no.
func aiConfirmAsk(ctx context.Context, text string, destructive bool) aiDecision {
	reply := make(chan aiDecision, 1)
	aiConfirm.mu.Lock()
	aiConfirm.pending, aiConfirm.text, aiConfirm.destructive, aiConfirm.reply = true, text, destructive, reply
	aiConfirm.mu.Unlock()
	chatDirty.Store(true)
	tbxPost(func() {
		if !chatSceneActive(appConfig) || chatActiveTabID() != chatTabAI {
			showToast("Juka is waiting for your OK in Chat → Juka AI", ToastInfo())
		}
		PlayNavSound()
	})
	t := time.NewTimer(aiConfirmWait)
	defer t.Stop()
	d := aiDeny
	select {
	case d = <-reply:
	case <-ctx.Done():
	case <-t.C:
	}
	aiConfirm.mu.Lock()
	if aiConfirm.reply == reply {
		aiConfirm.pending, aiConfirm.reply = false, nil
	}
	aiConfirm.mu.Unlock()
	chatDirty.Store(true)
	return d
}

// aiConfirmPending is the card on screen, if any. Any thread.
func aiConfirmPending() (text string, destructive, ok bool) {
	aiConfirm.mu.Lock()
	defer aiConfirm.mu.Unlock()
	return aiConfirm.text, aiConfirm.destructive, aiConfirm.pending
}

// aiConfirmAnswer answers the card. Main thread.
func aiConfirmAnswer(d aiDecision) bool {
	aiConfirm.mu.Lock()
	reply := aiConfirm.reply
	if !aiConfirm.pending || reply == nil {
		aiConfirm.mu.Unlock()
		return false
	}
	if d == aiAllowConversation && aiConfirm.destructive {
		// The card offers no Y for a destructive step: Y (also "New chat"
		// on this tab) must not allow a permanent delete. Only A does.
		aiConfirm.mu.Unlock()
		return false
	}
	aiConfirm.pending, aiConfirm.reply = false, nil
	aiConfirm.mu.Unlock()
	reply <- d
	chatDirty.Store(true)
	return true
}

// ── Groq with tools ──────────────────────────────────────────────────────

type aiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type aiChatResp struct {
	Choices []struct {
		Message struct {
			Content   string       `json:"content"`
			ToolCalls []aiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// aiToolSpecs is the `tools` array for the tools allowed in this run.
func aiToolSpecs(readOnly bool) []map[string]any {
	var out []map[string]any
	for _, t := range aiToolRegistry() {
		if !aiToolAllowed(t, readOnly) {
			continue
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Desc,
				"parameters":  t.Params,
			},
		})
	}
	return out
}

// aiGroqRound makes one request. choice is the tool_choice sent with tools:
// "auto" while Juka may still look things up, "none" for the final answer
// (the tool list goes along so a history full of tool calls stays valid).
func aiGroqRound(ctx context.Context, key, model string, messages []map[string]any, tools []map[string]any, choice string) (aiChatResp, error) {
	payload := map[string]any{
		"model":                 model,
		"messages":              messages,
		"temperature":           0.3,
		"max_completion_tokens": 1200,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = choice
	}
	if strings.HasPrefix(model, "openai/gpt-oss") {
		payload["reasoning_effort"] = "low"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return aiChatResp{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGroqEndpoint, bytes.NewReader(body))
	if err != nil {
		return aiChatResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return aiChatResp{}, fmt.Errorf("Groq request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return aiChatResp{}, fmt.Errorf("Groq stream interrupted: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		gerr := groqErrorFromResponse(resp.StatusCode, raw)
		// A malformed tool call ("tool_use_failed") is the model's fault: the
		// next model in the chain may do better.
		if resp.StatusCode == http.StatusBadRequest && bytes.Contains(raw, []byte("tool_use_failed")) {
			gerr.model = true
		}
		return aiChatResp{}, gerr
	}
	var out aiChatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return aiChatResp{}, fmt.Errorf("Groq answer unreadable: %w", err)
	}
	if len(out.Choices) == 0 {
		return aiChatResp{}, errors.New("Groq sent no answer")
	}
	return out, nil
}

// aiSystemFor is the system prompt of a run.
func aiSystemFor(run *aiRun) string {
	s := jukaSystemPrompt() + aiAgentPrompt
	if run.voice {
		s = jukaSystemPrompt() + aiAgentVoicePrompt
	}
	s += fmt.Sprintf(" Device: %s (%s). Time: %s.", deviceDisplayName(DetectDevice()), runtime.GOOS, time.Now().Format("Mon 2 Jan 15:04"))
	if run.readOnly {
		s += " In this conversation you can only look, not change anything: say what the user can do instead."
	} else if !aiChangesOn() {
		s += " The user has turned off changes: you can only look; tell them what to change themselves."
	}
	return s
}

// aiAgentLoop runs the model with tools until it answers. It returns the
// answer text (follow-ups not yet cut).
func aiAgentLoop(ctx context.Context, run *aiRun, key string, models []string, messages []map[string]any) (string, error) {
	tools := aiToolSpecs(run.readOnly)
	modelIdx := 0
	for step := 0; step < aiMaxSteps; step++ {
		if modelIdx >= len(models) {
			return "", errors.New("no Groq model could use the tools")
		}
		resp, err := aiGroqRound(ctx, key, models[modelIdx], messages, tools, "auto")
		if err != nil {
			var apiErr *groqError
			if errors.As(err, &apiErr) && apiErr.model {
				logWarn("aiagent", "model %s: %s", models[modelIdx], apiErr.msg)
				modelIdx++
				step--
				continue
			}
			return "", err
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		calls := msg.ToolCalls
		if len(calls) > 6 {
			calls = calls[:6]
		}
		for i := range calls {
			// Echoed back as sent; the API wants type "function" on each.
			if calls[i].Type == "" {
				calls[i].Type = "function"
			}
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": msg.Content, "tool_calls": calls})
		for _, call := range calls {
			result := aiExecCall(ctx, run, call)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	// Out of rounds: one last request that may not call tools.
	run.note("Writing the answer")
	for ; modelIdx < len(models); modelIdx++ {
		resp, err := aiGroqRound(ctx, key, models[modelIdx], messages, tools, "none")
		if err == nil {
			return resp.Choices[0].Message.Content, nil
		}
		var apiErr *groqError
		if !errors.As(err, &apiErr) || !apiErr.model {
			return "", err
		}
	}
	return "", errors.New("no Groq model could finish the answer")
}

// aiExecCall runs one tool call, asking the user first when it changes
// something, and returns the result for the model.
func aiExecCall(ctx context.Context, run *aiRun, call aiToolCall) (result string) {
	tool, ok := aiToolByName(call.Function.Name)
	if !ok || !aiToolAllowed(tool, run.readOnly) {
		return "Error: no tool named " + call.Function.Name + " is available here."
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("aiagent", "tool %s recovered: %v", tool.Name, r)
			result = "Error: the tool stopped unexpectedly."
		}
	}()
	args := aiArgs{}
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" && raw != "null" {
		if len(raw) > 16<<10 || json.Unmarshal([]byte(raw), &args) != nil {
			return "Error: the arguments were not valid JSON."
		}
	}
	tctx, cancel := context.WithTimeout(ctx, aiToolTimeout)
	defer cancel()
	tctx = context.WithValue(tctx, aiRunKey{}, run)
	logDebug("aiagent", "tool %s", tool.Name)
	step := tool.Run(tctx, args)
	if step.Confirm == "" || step.Apply == nil {
		if step.Status != "" {
			run.note(step.Status)
		}
		return aiFinish(step.Result)
	}
	if run.readOnly {
		return "Error: this conversation can only look; tell the user how to do it."
	}
	destructive := tool.Risk == aiRiskDestructive || step.Destructive
	allowed := !destructive && aiAllowedFor(run.conv)
	if !allowed {
		run.note("Waiting for your OK")
		d := aiConfirmAsk(ctx, step.Confirm, destructive)
		if d == aiAllowConversation {
			aiAllowAllFor(run.conv)
		}
		allowed = d != aiDeny
	}
	if !allowed {
		run.note("You said no")
		logInfo("aiagent", "%s declined by the user", tool.Name)
		return "The user said no. Nothing was changed. Do not try again unless they ask."
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), aiToolTimeout)
	defer acancel()
	out := step.Apply(actx)
	if strings.HasPrefix(out, "Error") {
		run.note("A change failed")
	} else {
		run.note(aiFirstLine(step.Confirm, "Changed"))
	}
	return aiFinish(out)
}

// aiFirstLine turns "Change X from a to b?" into the past-tense status line
// "Done: Change X from a to b".
func aiFirstLine(confirm, fallback string) string {
	line := strings.TrimSpace(strings.SplitN(confirm, "\n", 2)[0])
	line = strings.TrimSuffix(line, "?")
	if utf8.RuneCountInString(line) > 70 {
		line = string([]rune(line)[:70]) + "…"
	}
	if line == "" {
		return fallback
	}
	return "Done: " + line
}

// ── The answer ───────────────────────────────────────────────────────────

// aiSplitFollowups cuts the "FOLLOWUPS:" line off an answer. Pure.
func aiSplitFollowups(text string) (string, []string) {
	idx := strings.LastIndex(strings.ToUpper(text), "FOLLOWUPS:")
	if idx < 0 {
		return strings.TrimSpace(text), nil
	}
	tail := text[idx+len("FOLLOWUPS:"):]
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 {
		tail = tail[:nl]
	}
	var out []string
	for _, p := range strings.Split(tail, "|") {
		p = strings.Trim(strings.TrimSpace(p), `"'*-•`)
		if p == "" || utf8.RuneCountInString(p) > 60 {
			continue
		}
		out = append(out, p)
		if len(out) == aiFollowupLimit {
			break
		}
	}
	return strings.TrimSpace(text[:idx]), out
}

// aiDecorate puts the run's status lines on top of the answer.
func aiDecorate(run *aiRun, answer string) string {
	if s := run.statusLine(); s != "" {
		return s + "\n\n" + answer
	}
	return answer
}

// aiAgentWanted reports whether chat questions go through the agent.
func aiAgentWanted() bool { return aiToolsOn() }

// aiAgentAnswer answers a Juka AI question with tools (chatRunAI calls it on
// the sender's goroutine). The answer comes back for chatRunAI to file; the
// follow-ups and the open-a-screen button go to the chip strip.
func aiAgentAnswer(key string, models []string, promptID string, history []ChatMessage, question string) (string, error) {
	ctx, cancel := context.WithTimeout(chatAIRunContext(), aiRunTimeout) // a wake ends it (chatsync.go)
	defer cancel()
	run := &aiRun{promptID: promptID, conv: aiConversationOf(history)}
	messages := []map[string]any{{"role": "system", "content": aiSystemFor(run)}}
	for _, m := range buildGroqMessagesFrom(history, question)[1:] {
		messages = append(messages, map[string]any{"role": m["role"], "content": m["content"]})
	}
	aiSetChips("", nil, "", nil)
	reply, err := aiAgentLoop(ctx, run, key, models, messages)
	if err != nil {
		var apiErr *groqError
		if errors.As(err, &apiErr) && apiErr.model || strings.Contains(err.Error(), "use the tools") {
			// No model would take tools today: answer without them.
			logWarn("aiagent", "tools unavailable, plain answer: %v", err)
			return chatGroqAsk(key, models, buildGroqMessagesFrom(history, question), nil)
		}
		return "", err
	}
	answer, followups := aiSplitFollowups(chatStripThink(reply))
	run.mu.Lock()
	openLabel, open := run.openLabel, run.open
	run.mu.Unlock()
	aiSetChips(promptID, followups, openLabel, open)
	if answer == "" {
		return "", nil
	}
	return aiDecorate(run, answer), nil
}

// ── Offline: lite mode ───────────────────────────────────────────────────

// aiLiteRoute picks the read-only tools a question is about, with their
// arguments. Pure.
func aiLiteRoute(question string) []struct {
	name string
	args aiArgs
} {
	q := " " + strings.ToLower(question) + " "
	type pick = struct {
		name string
		args aiArgs
	}
	var out []pick
	add := func(name string, args aiArgs) {
		for _, p := range out {
			if p.name == name {
				return
			}
		}
		out = append(out, pick{name, args})
	}
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(q, w) {
				return true
			}
		}
		return false
	}
	if has("duplicate", "copies", "twice") {
		add("find_files", aiArgs{"duplicates": true})
	}
	if has("won't start", "wont start", "doesn't start", "does not start", "won't launch", "black screen", "crash", "not starting", "game won", "won't load", "bios") {
		add("diagnose_game", aiArgs{})
	}
	if has("space", "storage", "full", "filling", "free up", "disk", "card") {
		add("disk_usage", aiArgs{})
	}
	if has("wifi", "wi-fi", "internet", "network", "online", "connect", "bluetooth") {
		add("network_check", aiArgs{})
	}
	if has("battery", "drain", "hot ", "heat", "temperature", "slow", "lag", "smooth", "stutter", "performance", " cpu", "memory", " ram") {
		add("device_status", aiArgs{})
		add("performance_get", aiArgs{})
		if has("drain", "slow", "memory", " ram", "lag") {
			add("list_processes", aiArgs{"limit": float64(8)})
		}
	}
	if has(" log", "error", "warning", "what went wrong") {
		add("read_logs", aiArgs{"source": "errors", "lines": float64(25)})
	}
	if has("game", "rom") && len(out) == 0 {
		add("list_games", aiArgs{})
	}
	if has("setting", "turn on", "turn off", "enable", "disable") {
		add("search_settings", aiArgs{"query": aiLiteSettingWords(q)})
	}
	if has("status", "how is my", "device", "version") && len(out) == 0 {
		add("device_status", aiArgs{})
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// aiLiteSettingWords keeps the words of a question that may name a setting.
func aiLiteSettingWords(q string) string {
	stop := map[string]bool{"turn": true, "on": true, "off": true, "the": true, "a": true, "my": true, "please": true,
		"enable": true, "disable": true, "setting": true, "settings": true, "how": true, "do": true, "i": true,
		"to": true, "can": true, "you": true, "where": true, "is": true, "change": true, "set": true, "juka": true}
	var kept []string
	for _, w := range strings.Fields(strings.Trim(q, " ?!.")) {
		w = strings.Trim(w, "?!.,")
		if !stop[w] && w != "" {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// aiLiteAnswer answers offline with read-only tools. ok is false when no
// tool fits the question.
func aiLiteAnswer(ctx context.Context, run *aiRun, question string, perTool int) (string, bool) {
	picks := aiLiteRoute(question)
	if len(picks) == 0 || !aiToolsOn() {
		return "", false
	}
	var b strings.Builder
	for _, p := range picks {
		tool, ok := aiToolByName(p.name)
		if !ok || tool.Risk != aiRiskRead {
			continue // lite mode only looks
		}
		tctx, cancel := context.WithTimeout(ctx, aiToolTimeout)
		step := tool.Run(context.WithValue(tctx, aiRunKey{}, run), p.args)
		cancel()
		if step.Status != "" {
			run.note(step.Status)
		}
		res := aiFinish(step.Result)
		if len(res) > perTool {
			cut := perTool
			for cut > 0 && res[cut]&0xC0 == 0x80 {
				cut--
			}
			res = res[:cut] + "\n…"
		}
		title := step.Status
		if title == "" {
			title = tool.Name
		}
		b.WriteString(title + ":\n" + res + "\n\n")
	}
	return strings.TrimSpace(b.String()), true
}

// aiLiteWanted reports whether a question goes to lite mode now: tools on
// and no way to ask Groq (no key, or no internet and the question is one
// the tools can answer — anything else waits for the connection as before).
func aiLiteWanted(key, question string) bool {
	if !aiToolsOn() {
		return false
	}
	if strings.TrimSpace(key) == "" {
		return true
	}
	return !IsOnline() && !netReconnecting() && len(aiLiteRoute(question)) > 0
}

// aiLiteRunChat answers a queued question offline and files the answer
// under it, like chatRunAI does (chatSyncAI starts it on a goroutine and set
// chatAIBusy).
func aiLiteRunChat(prompt ChatMessage) {
	defer chatAIBusy.Store(false)
	defer func() {
		if r := recover(); r != nil {
			chatStreamEnd()
			logWarn("aiagent", "offline answer recovered: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := &aiRun{promptID: prompt.ID, readOnly: true}
	chatStreamBegin(prompt.ID)
	question := strings.TrimSpace(prompt.Text)
	body, ok := aiLiteAnswer(ctx, run, question, 1400)
	if !ok {
		body = ""
		if offlineAIReady() {
			run.note("Thinking offline")
			if reply, err := offlineAIAnswer(ctx, question, false, nil); err == nil {
				body = reply
			}
		}
		if body == "" {
			body = "Offline, Juka can check storage, logs, a game that won't start, Wi-Fi, battery and settings. Try one of the buttons below"
			if chatRawGroqKey(appConfig) == "" {
				body += ", or add a Groq API key for full answers"
			}
			body += "."
		}
	}
	chatStreamEnd()
	reply := "Offline — Juka can look but not change things.\n\n" + body
	aiSetChips(prompt.ID, nil, "", nil)
	now := time.Now()
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
	chatWakeSync()
}

// ── Other ways in ────────────────────────────────────────────────────────

// aiAgentAskFromDeck opens Chat on the Juka AI tab and asks prompt through
// the agent (the command deck's entry point). Main thread.
func aiAgentAskFromDeck(prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	if c := appConfig; c != nil {
		if idx := sceneIndexExact(c, chatSceneName); idx >= 0 && idx != currentSceneIndex {
			changeSceneTo(c, idx)
		}
	}
	chatEnsureLoaded()
	chatSelectTab(chatTabAI)
	if chatAskAI(prompt) {
		chatJumpToLatest()
	}
}

// aiVoiceWants reports a spoken question the read-only agent should take:
// one about this device that a tool can answer. Pure apart from the setting.
func aiVoiceWants(text string) bool {
	if !aiToolsOn() {
		return false
	}
	q := strings.ToLower(text)
	for _, w := range []string{"why won't", "why wont", "why doesn't", "why does my", "won't start", "wont start", "what's filling", "whats filling",
		"filling my", "free up", "space left", "battery drain", "draining", "what's wrong", "whats wrong", "check my wi", "is my wi",
		"internet working", "duplicate", "running hot", "why is it slow", "how much space"} {
		if strings.Contains(q, w) {
			return true
		}
	}
	return false
}

// aiAgentVoiceAnswer answers a spoken diagnostic question with read-only
// tools: through Groq when it can, else lite mode's first findings. Off the
// main thread.
func aiAgentVoiceAnswer(ctx context.Context, question string) (string, bool, error) {
	run := &aiRun{readOnly: true, voice: true}
	key := strings.TrimSpace(groqAPIKey())
	if key != "" && (IsOnline() || netReconnecting()) {
		messages := []map[string]any{
			{"role": "system", "content": aiSystemFor(run)},
			{"role": "user", "content": question},
		}
		reply, err := aiAgentLoop(ctx, run, key, groqCandidatesFor(groqModel()), messages)
		if err == nil {
			answer, _ := aiSplitFollowups(chatStripThink(reply))
			return answer, false, nil
		}
		if ctx.Err() != nil {
			return "", false, err
		}
		logWarn("aiagent", "voice agent failed, looking offline: %v", err)
	}
	body, ok := aiLiteAnswer(ctx, run, question, 600)
	if !ok {
		return "", true, errors.New("offline Juka has no check for that")
	}
	// Spoken: the first lines of what was found.
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
		if len(lines) == 5 {
			break
		}
	}
	return strings.Join(lines, ". "), true, nil
}
