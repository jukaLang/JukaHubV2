package main

// chatvoiceclip.go — record a short voice message with the same microphone
// used by voice typing, then put the WAV in Chat's attachment tray. The user
// can add a caption and send it through the tab's usual delivery path.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	chatVoiceClipMax = 120 * time.Second
	chatVoiceClipMin = 300 * time.Millisecond
)

var chatVoiceClip struct {
	mic     *micStream
	pcm     []byte
	started time.Time
	tab     chatTab
	scratch [micRate]byte
}

func chatVoiceRecording() bool { return chatVoiceClip.mic != nil }

// chatVoiceToggle starts a new recording or finishes the current one. A
// finished clip is attached, never posted without the user's Send press.
func chatVoiceToggle() {
	if chatVoiceRecording() {
		chatVoiceFinish()
		return
	}
	if virtualKeyboardActive || chatSelectionBar.open || chatOverlayOpen() {
		return
	}
	tab := chatActiveTabID()
	if chatStagedCount(tab) >= chatStageLimit(tab) {
		showToast("This message already has the maximum number of files", ToastWarn())
		return
	}
	// A clip is not voice typing. Keeping the two microphone owners separate
	// prevents one feature from silently stealing samples from the other.
	if vkVoiceActive() {
		showToast("Finish voice typing before recording a clip", ToastInfo())
		return
	}
	chatStopInlineTyping()
	jukaSpeakStop()
	mic, err := micOpen()
	if err != nil {
		showToast("The microphone could not be opened", ToastWarn())
		return
	}
	chatVoiceClip.mic = mic
	chatVoiceClip.pcm = make([]byte, 0, micRate*2*8)
	chatVoiceClip.started = time.Now()
	chatVoiceClip.tab = tab
	PlayActivateSound()
	showToast("Recording voice clip — press the mic again to attach, B to cancel", ToastInfo())
}

// chatVoiceTick drains capture on the UI thread, as micStream.Read requires.
// The two-minute cap also keeps a paused or forgotten recording bounded.
func chatVoiceTick(config *Config) {
	if !chatVoiceRecording() {
		return
	}
	if !chatSceneActive(config) {
		chatVoiceCancel()
		return
	}
	chatVoiceDrain()
	if time.Since(chatVoiceClip.started) >= chatVoiceClipMax ||
		len(chatVoiceClip.pcm) >= micRate*2*int(chatVoiceClipMax/time.Second) {
		chatVoiceFinish()
	}
}

func chatVoiceDrain() {
	if !chatVoiceRecording() {
		return
	}
	maxBytes := micRate * 2 * int(chatVoiceClipMax/time.Second)
	room := maxBytes - len(chatVoiceClip.pcm)
	if room <= 0 {
		return
	}
	chunk := chatVoiceClip.scratch[:]
	if room < len(chunk) {
		chunk = chunk[:room]
	}
	if n := chatVoiceClip.mic.Read(chunk); n > 0 {
		chatVoiceClip.pcm = append(chatVoiceClip.pcm, chunk[:n]...)
	}
}

func chatVoiceFinish() {
	if !chatVoiceRecording() {
		return
	}
	chatVoiceDrain()
	chatVoiceClip.mic.Close()
	pcm, tab := chatVoiceClip.pcm, chatVoiceClip.tab
	chatVoiceClip.mic = nil
	chatVoiceClip.pcm = nil
	if len(pcm) < micRate*2*int(chatVoiceClipMin/time.Millisecond)/1000 {
		showToast("Clip was too short — hold the recording a little longer", ToastInfo())
		return
	}
	dir := filepath.Join(chatSaveDir(), "Voice Clips")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		showToast("Could not save the voice clip", ToastWarn())
		return
	}
	pattern := fmt.Sprintf("voice-%s-*.wav", time.Now().Format("20060102-150405"))
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		showToast("Could not save the voice clip", ToastWarn())
		return
	}
	path := f.Name()
	_, writeErr := f.Write(vkEncodeWAV(pcm, micRate))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		showToast("Could not save the voice clip", ToastWarn())
		return
	}
	if err := chatStageFile(tab, path); err != nil {
		_ = os.Remove(path)
		showToast("Could not attach the voice clip: "+err.Error(), ToastWarn())
		return
	}
	PlayActivateSound()
	showToast("Voice clip attached — press Send when ready", ToastInfo())
}

func chatVoiceCancel() {
	if !chatVoiceRecording() {
		return
	}
	chatVoiceClip.mic.Close()
	chatVoiceClip.mic = nil
	chatVoiceClip.pcm = nil
	showToast("Voice clip discarded", ToastInfo())
}

func chatVoiceClock() string {
	if !chatVoiceRecording() {
		return ""
	}
	seconds := int(time.Since(chatVoiceClip.started) / time.Second)
	return fmt.Sprintf("Recording %d:%02d · tap mic to attach", seconds/60, seconds%60)
}
