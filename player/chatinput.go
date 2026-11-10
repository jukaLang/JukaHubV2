package main

// chatinput.go — the Chat scene's controls: pad, keyboard, mouse and wheel.
//
// Chat is a registered scene, so it owns its keys and buttons before the
// generic element dispatch sees them. That dispatch is what used to break Back
// on Windows: entering Chat switched the composer on, the chat element kept it
// on every frame while it had focus, and its key handler treated Escape as
// "clear the draft" — so on a keyboard "b" was typed into the message and Esc
// only emptied it; nothing ever left the screen.
//
// Now the screen starts in browse mode:
//
//	Up / Down            walk the history (a tall message scrolls first);
//	                     Down from the newest message reaches the files
//	                     waiting to be sent, then the composer
//	L1 / R1, Left/Right  previous / next tab (Tab / Shift+Tab on a keyboard);
//	                     on the waiting files Left/Right picks one
//	L2 / R2              oldest / newest message
//	right stick, wheel   scroll freely
//	A / Enter            type (on-screen keyboard; Enter types inline on a
//	                     real keyboard), open a focused message's actions, or
//	                     take a waiting file off the message
//	Start                send what is typed or attached (an empty composer
//	                     leaves Start to the Start menu, as everywhere)
//	Y                    the tab's own action: People / New chat / Add key
//	X                    attach files (Files opens to pick them)
//	R (keyboard)         rename yourself (also the name chip, and Rename on
//	                     your own card in People)
//	B / Esc / Backspace  leave Chat (an open card or action bar closes first,
//	                     and the on-screen keyboard closes itself first)
//
// While typing inline every printable key is text — "b" and "q" included — and
// Esc stops typing; Enter sends, Shift+Enter starts a new line.

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// chatComposerInline is set while a physical keyboard types straight into the
// composer, without the on-screen keyboard.
var chatComposerInline bool

// chatSkipSpace drops the text event of the Space press that started typing:
// SDL delivers the key and then its text.
var chatSkipSpace bool

// chatKeyboardSeenAt is when a physical key last reached the chat. A desktop
// that is being typed on gets inline typing when the composer is clicked; a
// mouse- or touch-only setup gets the on-screen keyboard.
var chatKeyboardSeenAt time.Time

// chatDraftMax caps a draft, like the on-screen keyboard's own limit.
const chatDraftMax = 2000

// chatScene registers Chat with the scene registry.
type chatScene struct{ sceneBase }

func (chatScene) render(r *sdl.Renderer, c *Config) bool {
	chatRenderScene(r, c)
	return true
}

func (chatScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return chatSceneKey(e, c) }

func (chatScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return chatSceneController(e, c)
}

func init() { registerScene(chatScene{sceneBase{name: chatSceneName}}) }

// ── Pad ──────────────────────────────────────────────────────────────────

// chatSceneController handles one pad press on the Chat scene. The member
// list, the profile card and the action bar take the pad earlier (in
// handleControllerEvent), and the on-screen keyboard before that.
func chatSceneController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !chatSceneActive(config) || virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if !chatFocusUp() {
			// The top of the history: Up steps onto the bar, as on every screen.
			focusChromeBar()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		chatFocusDown()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if !chatTrayMove(-1) {
			chatCycleTab(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if !chatTrayMove(1) {
			chatCycleTab(1)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		chatCycleTab(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		chatCycleTab(1)
	case sdl.CONTROLLER_BUTTON_A:
		chatActivate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if chatTrayLeave() {
			break
		}
		chatLeave(config)
	case sdl.CONTROLLER_BUTTON_X:
		chatStartAttach(config)
	case sdl.CONTROLLER_BUTTON_Y:
		chatTabAction(config)
	case sdl.CONTROLLER_BUTTON_START:
		return chatSendDraft()
	default:
		return false
	}
	return true
}

// chatStartSendsDraft lets Start send a typed draft. It runs before the Start
// menu opens (handleControllerEvent); with nothing typed it declines and Start
// opens the menu as on every other screen.
func chatStartSendsDraft(config *Config) bool {
	if !chatSceneActive(config) || virtualKeyboardActive || chatOverlayOpen() || chatSelectionBar.open {
		return false
	}
	return chatSendDraft()
}

// chatSceneShoulder maps the shoulders and triggers on the Chat scene: tabs on
// L1/R1, oldest/newest message on L2/R2.
func chatSceneShoulder(s padShoulder, config *Config) bool {
	if !chatSceneActive(config) || virtualKeyboardActive {
		return false
	}
	if chatOverlayOpen() {
		return true // the profile or member card on top owns the pad
	}
	switch s {
	case padL1:
		chatCycleTab(-1)
	case padR1:
		chatCycleTab(1)
	case padL2:
		clearChatSelection()
		chatScrollToTop()
		PlayNavSound()
	case padR2:
		clearChatSelection()
		chatJumpToLatest()
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Keyboard ─────────────────────────────────────────────────────────────

// chatSceneKey handles one key on the Chat scene. Keys it leaves alone (the
// function keys, the global chords) fall through to the app's shortcuts.
func chatSceneKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !chatSceneActive(config) || virtualKeyboardActive {
		return false
	}
	chatKeyboardSeenAt = time.Now()
	sym, mod := e.Keysym.Sym, e.Keysym.Mod
	ctrl := mod&sdl.KMOD_CTRL != 0
	alt := mod&sdl.KMOD_ALT != 0
	shift := mod&sdl.KMOD_SHIFT != 0
	if chatComposerInline {
		return chatInlineKey(sym, ctrl, alt, shift)
	}
	if alt {
		return false
	}
	if ctrl {
		if sym == sdl.K_TAB {
			chatCycleTab(chatTabStep(shift))
			return true
		}
		// Ctrl+Enter is the pad's Start here: send the typed draft (the
		// footer says "Start / Ctrl+Enter"). With nothing typed it falls
		// through like any other chord.
		if (sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER) && chatSendDraft() {
			return true
		}
		return false
	}
	switch sym {
	case sdl.K_UP:
		if !chatFocusUp() {
			focusChromeBar()
		}
	case sdl.K_DOWN:
		chatFocusDown()
	case sdl.K_LEFT:
		if !chatTrayMove(-1) {
			chatCycleTab(-1)
		}
	case sdl.K_RIGHT:
		if !chatTrayMove(1) {
			chatCycleTab(1)
		}
	case sdl.K_DELETE:
		// Del takes the waiting file under the cursor off the message.
		if !chatTrayRemoveFocused() {
			return false
		}
	case sdl.K_TAB:
		chatCycleTab(chatTabStep(shift))
	case sdl.K_1, sdl.K_2, sdl.K_3:
		if chatSelectTab(chatTab(sym - sdl.K_1)) {
			PlayNavSound()
		}
	case sdl.K_PAGEUP:
		chatScrollPage(1)
	case sdl.K_PAGEDOWN:
		chatScrollPage(-1)
	case sdl.K_HOME:
		clearChatSelection()
		chatScrollToTop()
	case sdl.K_END:
		clearChatSelection()
		chatJumpToLatest()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if chatSuggestFocus >= 0 || chatTrayFocus >= 0 {
			chatActivate()
			break
		}
		if _, _, ok := selectedChatMessage(); ok {
			openChatActionsForSelected()
			PlayActivateSound()
			break
		}
		// On the handheld the pad's A can arrive as Enter, and there is no
		// keyboard to type inline with: it opens the on-screen keyboard.
		if P().IsHandheld() {
			chatOpenKeyboard()
			break
		}
		chatSkipSpace = sym == sdl.K_SPACE
		chatStartInlineTyping()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		if chatTrayLeave() {
			break
		}
		chatLeave(config)
	case sdl.K_y:
		chatTabAction(config)
	case sdl.K_x:
		chatStartAttach(config)
	case sdl.K_r:
		PlayActivateSound()
		vkOpenName()
	default:
		return false
	}
	return true
}

// chatTabStep is the tab direction a Tab press means.
func chatTabStep(shift bool) int {
	if shift {
		return -1
	}
	return 1
}

// chatInlineKey handles a key while typing inline. Every printable key is
// consumed — its character arrives as a text event — so letter shortcuts can
// never fire in the middle of a word.
func chatInlineKey(sym sdl.Keycode, ctrl, alt, shift bool) bool {
	switch sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if alt {
			return false // Alt+Enter is the fullscreen toggle
		}
		if shift {
			chatComposerInsert("\n")
			return true
		}
		chatSendDraft()
		return true
	case sdl.K_ESCAPE:
		chatStopInlineTyping()
		PlayBackSound()
		return true
	case sdl.K_BACKSPACE:
		chatInputText = chatDeleteBack(chatInputText, ctrl)
		return true
	case sdl.K_UP:
		chatScrollBy(float64(chatScrollStep()))
		return true
	case sdl.K_DOWN:
		chatScrollBy(-float64(chatScrollStep()))
		return true
	case sdl.K_PAGEUP:
		chatScrollPage(1)
		return true
	case sdl.K_PAGEDOWN:
		chatScrollPage(-1)
		return true
	case sdl.K_TAB:
		chatCycleTab(chatTabStep(shift))
		return true
	}
	if ctrl || alt {
		return false
	}
	// Printable keycodes are their character; keys without one carry the
	// scancode bit (1<<30) and fall through to the global shortcuts.
	return sym >= 32 && sym != 127 && sym < 1<<30
}

// chatDeleteBack removes the last character of s, or the last word when word
// is set (Ctrl+Backspace).
func chatDeleteBack(s string, word bool) string {
	if s == "" {
		return s
	}
	if !word {
		return trimLastRune(s)
	}
	trimmed := strings.TrimRight(s, " \n")
	if i := strings.LastIndexAny(trimmed, " \n"); i >= 0 {
		return trimmed[:i+1]
	}
	return ""
}

// chatStartInlineTyping puts a physical keyboard's typing into the composer.
func chatStartInlineTyping() {
	if virtualKeyboardActive {
		return
	}
	clearChatSelection()
	chatSuggestFocus = -1
	chatComposerInline = true
	chatInputActive = true
	// Pastes and the shared text funnel (vkInsertText) follow the keyboard
	// target, which may still point at whatever was edited last.
	vkTarget = vkTargetChat
	sdl.StartTextInput()
	PlayActivateSound()
}

// chatStopInlineTyping ends inline typing (the draft is kept).
func chatStopInlineTyping() {
	if !chatComposerInline {
		return
	}
	chatComposerInline = false
	chatInputActive = false
	chatSkipSpace = false
}

// chatComposerTakesText reports whether typed text belongs to the composer:
// inline typing on the Chat scene with nothing floating above it.
func chatComposerTakesText(config *Config) bool {
	return chatComposerInline && chatSceneActive(config) && !virtualKeyboardActive &&
		!chatOverlayOpen() && !chatSelectionBar.open
}

// chatComposerInsert adds typed text to the draft.
func chatComposerInsert(text string) {
	if chatSkipSpace {
		chatSkipSpace = false
		if text == " " {
			return
		}
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' {
			return -1
		}
		return r
	}, text)
	if text == "" || utf8.RuneCountInString(chatInputText)+utf8.RuneCountInString(text) > chatDraftMax {
		return
	}
	chatInputText += text
}

// ── Actions ──────────────────────────────────────────────────────────────

// chatLeave goes back from the Chat scene.
func chatLeave(config *Config) {
	chatStopInlineTyping()
	goBackScene(config)
}

// chatSendDraft sends what the composer holds: the typed text and the files
// waiting in the tray. It reports false when there is nothing to send.
func chatSendDraft() bool {
	text := strings.TrimSpace(chatInputText)
	if text == "" && chatStagedCount(chatActiveTabID()) == 0 {
		return false
	}
	chatInputText = ""
	PlayActivateSound()
	chatSubmitComposer(text, false)
	return true
}

// chatHasDraft reports whether the composer has something to send.
func chatHasDraft() bool {
	return strings.TrimSpace(chatInputText) != "" || chatStagedCount(chatActiveTabID()) > 0
}

// chatActivate is A on the Chat scene.
func chatActivate() {
	if chatTrayFocus >= 0 {
		chatTrayRemoveFocused()
		return
	}
	if chatSuggestFocus >= 0 && chatSuggestFocus < chatSuggestionCount {
		text := chatSuggestions[chatSuggestFocus]
		chatSuggestFocus = -1
		PlayActivateSound()
		chatSubmitComposer(text, false)
		return
	}
	if _, _, ok := selectedChatMessage(); ok {
		openChatActionsForSelected()
		PlayActivateSound()
		return
	}
	chatOpenKeyboard()
}

// chatOpenKeyboard opens the on-screen keyboard on the composer.
func chatOpenKeyboard() {
	chatStopInlineTyping()
	clearChatSelection()
	chatSuggestFocus = -1
	PlayActivateSound()
	vkOpenChat()
}

// chatComposerClicked starts typing after a click or tap on the composer:
// inline on a desktop that is being typed on, the on-screen keyboard otherwise.
func chatComposerClicked() {
	if IsWindows() && !chatKeyboardSeenAt.IsZero() && time.Since(chatKeyboardSeenAt) < 10*time.Minute {
		chatStartInlineTyping()
		return
	}
	chatOpenKeyboard()
}

// chatTabAction is Y on the Chat scene: the tab's own action.
func chatTabAction(config *Config) {
	switch chatActiveTabID() {
	case chatTabCommunity:
		if openChatMembers() {
			return
		}
		showToast("No one has written here yet", ToastInfo())
	case chatTabAI:
		if !chatHasGroqKey() {
			chatOpenGroqKeySetting(config)
			return
		}
		if chatAddMarker() {
			PlayActivateSound()
			clearChatSelection()
			chatJumpToLatest()
		}
	}
}

// chatCycleTab moves to the previous or next tab, wrapping around.
func chatCycleTab(delta int) {
	n := int(chatTabCount)
	next := (int(chatActiveTabID()) + delta%n + n) % n
	if chatSelectTab(chatTab(next)) {
		PlayNavSound()
	}
}

// chatStartAttach opens Files to pick files for the tab on screen: A on a
// file (or on the rows X marked) puts it in the tray and comes back here
// (chatfiles.go).
func chatStartAttach(config *Config) {
	if config == nil {
		return
	}
	if chatPickBegin(config, chatActiveTabID()) {
		showToast(hintSentence2("Pick a file to attach — %s marks several, %s cancels", "X", "Mark", "B", "Cancel"), ToastInfo())
	}
}

// chatOpenGroqKeySetting takes the user to the Groq key field — the input
// bound to groq_api_key in whichever scene has it — with its keyboard open.
func chatOpenGroqKeySetting(config *Config) {
	if config == nil {
		return
	}
	for si, scene := range config.Scenes {
		for ei, elem := range scene.Elements {
			if elem.Type != "input" || !strings.EqualFold(elem.Variable, "groq_api_key") {
				continue
			}
			PlayActivateSound()
			if si != currentSceneIndex {
				changeSceneTo(config, si)
			}
			vkOpenSceneInput(config, si, ei)
			return
		}
	}
	for _, name := range []string{"SettingsGeneral", "Settings"} {
		if idx := sceneIndexExact(config, name); idx >= 0 {
			PlayActivateSound()
			changeSceneTo(config, idx)
			return
		}
	}
	showToast("Set groq_api_key in jukaconfig.json to talk to Juka", ToastInfo())
}

// chatBannerActivate runs the banner's button.
func chatBannerActivate(config *Config, action chatBannerAction) {
	switch action {
	case chatBannerWiFi:
		PlayActivateSound()
		wifiScrOpen(config)
	case chatBannerAddKey:
		chatOpenGroqKeySetting(config)
	case chatBannerRetry:
		PlayActivateSound()
		chatDiscordRetryNow()
	}
}

// chatDiscordRetryNow retries the community connection right away.
func chatDiscordRetryNow() {
	chatRetryRequested.Store(true)
	chatDiscordWanted.Store(true)
	chatEnsureSync()
	chatWakeSync()
}

// ── Focus and scrolling ──────────────────────────────────────────────────

// chatPrevFocusable is the nearest message before index from that a person
// wrote (dividers are skipped), or -1.
func chatPrevFocusable(msgs []ChatMessage, from int) int {
	for i := from - 1; i >= 0; i-- {
		if i < len(msgs) && !chatIsSystem(msgs[i]) {
			return i
		}
	}
	return -1
}

// chatNextFocusable is the nearest message after from, or -1.
func chatNextFocusable(msgs []ChatMessage, from int) int {
	for i := from + 1; i < len(msgs); i++ {
		if !chatIsSystem(msgs[i]) {
			return i
		}
	}
	return -1
}

// chatFocusMessage puts the pad on message idx and asks the view to show it.
func chatFocusMessage(msgs []ChatMessage, idx int, mode chatAnchorMode) {
	if idx < 0 || idx >= len(msgs) {
		return
	}
	chatSelected, chatSelectedID = idx, msgs[idx].ID
	chatSelectionBar = chatSelection{index: idx, buttons: chatSelectionActions(idx)}
	chatTrayFocus = -1
	view := &chatViews[chatActiveTabID()]
	view.anchorID = msgs[idx].ID
	view.anchorMode = mode
	PlayNavSound()
}

// chatFocusComposer hands the pad back to the composer at the newest message.
func chatFocusComposer() {
	clearChatSelection()
	chatSuggestFocus = -1
	chatJumpToLatest()
	PlayNavSound()
}

// chatFocusUp moves the pad from the composer onto the history and up through
// it. It returns false at the very top, where Up belongs to the top bar.
func chatFocusUp() bool {
	tab := chatActiveTabID()
	msgs := chatTabMessages(tab)
	view := &chatViews[tab]
	if chatSuggestFocus >= 0 {
		if chatSuggestFocus > 0 {
			chatSuggestFocus--
			PlayNavSound()
			return true
		}
		return false
	}
	if chatSelected < 0 || chatSelected >= len(msgs) {
		// Above the composer come the files waiting to be sent, then the
		// history.
		if chatTrayFocus < 0 && chatStagedCount(tab) > 0 {
			chatTrayFocus = chatStagedCount(tab) - 1
			PlayNavSound()
			return true
		}
		chatTrayFocus = -1
		if len(msgs) == 0 {
			if tab == chatTabAI && chatHasGroqKey() {
				chatSuggestFocus = chatSuggestionCount - 1
				PlayNavSound()
				return true
			}
			return false
		}
		if idx := chatPrevFocusable(msgs, len(msgs)); idx >= 0 {
			chatFocusMessage(msgs, idx, chatAnchorBottom)
			return true
		}
		return false
	}
	// A message taller than the list is read before the focus moves on.
	if chatView.focusValid && chatView.focusTop < chatView.list.Y-2 {
		chatScrollBy(float64(min32(chatScrollStep(), chatView.list.Y-chatView.focusTop)))
		return true
	}
	if idx := chatPrevFocusable(msgs, chatSelected); idx >= 0 {
		chatFocusMessage(msgs, idx, chatAnchorBottom)
		return true
	}
	if !view.topReached {
		chatScrollBy(float64(chatScrollStep()))
		return true
	}
	return false
}

// chatFocusDown moves the pad down the history and back to the composer.
func chatFocusDown() bool {
	tab := chatActiveTabID()
	msgs := chatTabMessages(tab)
	if chatSuggestFocus >= 0 {
		if chatSuggestFocus < chatSuggestionCount-1 {
			chatSuggestFocus++
		} else {
			// Below the questions: the files waiting to go, then the composer.
			chatSuggestFocus = -1
			if n := chatStagedCount(tab); n > 0 {
				chatTrayFocus = n - 1
			}
		}
		PlayNavSound()
		return true
	}
	if chatTrayFocus >= 0 {
		// From the waiting files down to the composer.
		chatTrayFocus = -1
		PlayNavSound()
		return true
	}
	if chatSelected < 0 || chatSelected >= len(msgs) {
		if !chatView.atBottom {
			chatJumpToLatest()
			PlayNavSound()
		}
		return true
	}
	listBottom := chatView.list.Y + chatView.list.H
	if chatView.focusValid && chatView.focusBottom > listBottom+2 {
		chatScrollBy(-float64(min32(chatScrollStep(), chatView.focusBottom-listBottom)))
		return true
	}
	if idx := chatNextFocusable(msgs, chatSelected); idx >= 0 {
		chatFocusMessage(msgs, idx, chatAnchorTop)
		return true
	}
	if n := chatStagedCount(tab); n > 0 {
		// Past the newest message: the files waiting to be sent come first.
		chatFocusComposer()
		chatTrayFocus = n - 1
		return true
	}
	chatFocusComposer()
	return true
}

// chatScrollStep is one D-pad or arrow step through a long message.
func chatScrollStep() int32 {
	return max32(chatView.list.H/3, chatPx(80))
}

// chatScrollBy moves the active tab's scroll target; positive shows older
// messages. The top end is clamped by the next frame, which knows where the
// history starts.
func chatScrollBy(delta float64) {
	view := &chatViews[chatActiveTabID()]
	view.target += delta
	if view.target < 0 {
		view.target = 0
	}
}

// chatScrollPage scrolls by most of a screen (dir 1 = older).
func chatScrollPage(dir int) {
	h := chatView.list.H
	if h <= 0 {
		h = chatPx(400)
	}
	chatScrollBy(float64(dir) * float64(h) * 0.85)
}

// chatJumpToLatest scrolls the active tab to its newest message.
func chatJumpToLatest() {
	view := &chatViews[chatActiveTabID()]
	view.target = 0
	view.anchorID = ""
}

// chatScrollToTop scrolls the active tab to its oldest message.
func chatScrollToTop() {
	view := &chatViews[chatActiveTabID()]
	view.anchorID = ""
	view.target = 1 << 30
}

// chatSceneWheel scrolls the history with the mouse wheel.
func chatSceneWheel(config *Config, dy int32) bool {
	if !chatSceneActive(config) || dy == 0 {
		return false
	}
	if chatFileSheet.open {
		setSheetWheel(&chatFileSheet, dy)
		return true
	}
	if chatOverlayOpen() {
		return true
	}
	chatScrollBy(float64(dy) * float64(chatPx(64)))
	return true
}

// chatStickSpeed is the right stick's scroll speed in pixels per second
// (positive shows older messages). The render applies it every frame, so a
// held stick scrolls smoothly at any frame rate.
var chatStickSpeed float64

// chatSceneStick takes the right stick's vertical axis on the Chat scene: the
// history scrolls freely with it, faster the further it is pushed. The left
// stick stays a D-pad (message by message, tabs sideways).
func chatSceneStick(config *Config, value int16) bool {
	if !chatSceneActive(config) || virtualKeyboardActive || chatOverlayOpen() {
		chatStickSpeed = 0
		return false
	}
	mag := float64(value)
	if mag < 0 {
		mag = -mag
	}
	if mag < stickDeadzone {
		chatStickSpeed = 0
		return true
	}
	mag = (mag - stickDeadzone) / (32767 - stickDeadzone)
	if mag > 1 {
		mag = 1
	}
	speed := mag * mag * float64(chatPx(1800))
	if value > 0 {
		speed = -speed // pushed down: towards the newest message
	}
	chatStickSpeed = speed
	return true
}

// ── Mouse ────────────────────────────────────────────────────────────────

// chatSceneClick routes a click on the Chat scene. Every click inside the chat
// is consumed, so the element behind it never opens a keyboard by accident.
func chatSceneClick(mx, my int32, config *Config) bool {
	if !chatSceneActive(config) || chatOverlayOpen() {
		return false
	}
	// The "Jump to latest" pill floats over the bottom of the history: it
	// takes its clicks before the picture, card or link drawn under it.
	if chatJumpRect.W > 0 && pointInRect(mx, my, chatJumpRect) {
		clearChatSelection()
		chatJumpToLatest()
		PlayNavSound()
		return true
	}
	if handleChatImageClick(mx, my) {
		return true
	}
	// A file card opens (or saves) its file, like A on it in the file list.
	if handleChatFileClick(mx, my, config) {
		return true
	}
	// Link text sits inside a bubble, so it is checked before the bubble,
	// whose click selects the message.
	if handleChatLinkClick(mx, my, config) {
		return true
	}
	if chatMessageClick(mx, my, config) {
		return true
	}
	if handleChatComposerMouse(mx, my, config) {
		return true
	}
	if pointInRect(mx, my, chatView.area) {
		chatStopInlineTyping()
		return true
	}
	return false
}

// handleChatComposerMouse handles a click on the chat's own controls: tabs,
// the banner button, the starter questions, the rename chip, the composer and
// its buttons. Stale hit boxes from an earlier chat frame are ignored on any
// other scene.
func handleChatComposerMouse(mx, my int32, config *Config) bool {
	if !currentSceneHasChat(config) {
		return false
	}
	for t := chatTabCommunity; t < chatTabCount; t++ {
		if r := chatView.tabs[t]; r.W > 0 && pointInRect(mx, my, r) {
			if chatSelectTab(t) {
				PlayNavSound()
			}
			return true
		}
	}
	if r := chatView.bannerBtn; r.W > 0 && pointInRect(mx, my, r) {
		chatBannerActivate(config, chatView.bannerAction)
		return true
	}
	for k := 0; k < chatSuggestionCount; k++ {
		if r := chatView.suggestions[k]; r.W > 0 && pointInRect(mx, my, r) {
			chatSuggestFocus = -1
			PlayActivateSound()
			chatSubmitComposer(chatSuggestions[k], false)
			return true
		}
	}
	if chatNameRect.W > 0 && pointInRect(mx, my, chatNameRect) {
		PlayActivateSound()
		vkOpenName()
		return true
	}
	if chatHeaderMembersRect.W > 0 && pointInRect(mx, my, chatHeaderMembersRect) {
		openChatMembers()
		return true
	}
	if handleChatTrayClick(mx, my) {
		return true
	}
	if chatInputRect.W > 0 && pointInRect(mx, my, chatInputRect) {
		chatComposerClicked()
		return true
	}
	if chatSendBtnRect.W > 0 && pointInRect(mx, my, chatSendBtnRect) {
		if !chatSendDraft() {
			chatComposerClicked()
		}
		return true
	}
	if chatAttachBtnRect.W > 0 && pointInRect(mx, my, chatAttachBtnRect) {
		chatStartAttach(config)
		return true
	}
	return false
}

// ── Footer ───────────────────────────────────────────────────────────────

// chatFooterHints is the Chat scene's hint row. It follows the state, so it
// never names a binding the screen would not answer to.
func chatFooterHints() []FooterHint {
	back := FooterHint{Button: "B", Label: "Back"}
	switch {
	case chatFileSheet.open:
		// A message's list of files is up (chatOpenFileSheet).
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Open"},
			{Button: "B", Label: "Close"},
		}
	case chatSelectionBar.open:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Select"},
			{Button: "B", Label: "Close"},
		}
	case chatComposerInline:
		return []FooterHint{
			{Button: "Enter", Label: "Send"},
			back,
			{Button: "Esc", Label: "Done"},
		}
	case chatTrayFocus >= 0:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Remove"},
			{Button: "Start / Ctrl+Enter", Label: "Send"},
			{Button: "B", Label: "Done"},
			{Button: "X", Label: "Attach"},
		}
	case chatSuggestFocus >= 0:
		return []FooterHint{
			{Button: "D-Pad", Label: "Move"},
			{Button: "A", Label: "Ask"},
			back,
			{Button: "L1/R1", Label: "Tabs"},
		}
	case chatSelected >= 0:
		return []FooterHint{
			{Button: "D-Pad", Label: "Move"},
			{Button: "A", Label: "Actions"},
			back,
			{Button: "L1/R1", Label: "Tabs"},
		}
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Browse"},
		{Button: "A", Label: "Type"},
	}
	// Something to send leads; the planner drops hints from the end when the
	// footer is narrow.
	if chatHasDraft() {
		hints = append(hints, FooterHint{Button: "Start / Ctrl+Enter", Label: "Send"})
	}
	hints = append(hints, back,
		FooterHint{Button: "X", Label: "Attach"},
		FooterHint{Button: "L1/R1", Label: "Tabs"},
	)
	switch chatActiveTabID() {
	case chatTabCommunity:
		hints = append(hints, FooterHint{Button: "Y", Label: "People"})
	case chatTabAI:
		if chatHasGroqKey() {
			hints = append(hints, FooterHint{Button: "Y", Label: "New chat"})
		} else {
			hints = append(hints, FooterHint{Button: "Y", Label: "Add key"})
		}
	}
	return hints
}
