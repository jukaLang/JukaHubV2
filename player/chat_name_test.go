package main

// Tests for the changeable chat display name: default, set/reset/persist,
// own-message marking, and the name surviving a history save. They run from a
// temporary directory so the developer's real jukauser.json is never touched.

import (
	"os"
	"testing"
)

// withChatNameEnv isolates the user-config globals and working directory for
// one test, restoring both afterwards.
func withChatNameEnv(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	prevCache := userConfigCache
	prevMessages := chatMessages
	userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	chatMessages = nil
	t.Cleanup(func() {
		userConfigCache = prevCache
		chatMessages = prevMessages
	})
}

func TestChatDisplayNameDefault(t *testing.T) {
	withChatNameEnv(t)
	if got := chatDisplayName(); got != "You" {
		t.Fatalf("chatDisplayName() = %q, want %q", got, "You")
	}
}

func TestSetChatDisplayNamePersists(t *testing.T) {
	withChatNameEnv(t)
	if got := setChatDisplayName("  Steppa  "); got != "Steppa" {
		t.Fatalf("setChatDisplayName returned %q, want trimmed %q", got, "Steppa")
	}
	if got := chatDisplayName(); got != "Steppa" {
		t.Fatalf("chatDisplayName() = %q after set, want %q", got, "Steppa")
	}
	if _, err := os.Stat("jukauser.json"); err != nil {
		t.Fatalf("name was not persisted to jukauser.json: %v", err)
	}
}

func TestSetChatDisplayNameEmptyResets(t *testing.T) {
	withChatNameEnv(t)
	setChatDisplayName("Steppa")
	if got := setChatDisplayName("   "); got != "You" {
		t.Fatalf("blank name reset to %q, want %q", got, "You")
	}
	if got := chatDisplayName(); got != "You" {
		t.Fatalf("chatDisplayName() = %q after reset, want %q", got, "You")
	}
}

func TestIsLocalChatSender(t *testing.T) {
	withChatNameEnv(t)
	setChatDisplayName("Steppa")
	for _, name := range []string{"Steppa", "steppa", "You", "User"} {
		if !isLocalChatSender(name) {
			t.Errorf("isLocalChatSender(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Juka AI", "System", "bob"} {
		if isLocalChatSender(name) {
			t.Errorf("isLocalChatSender(%q) = true, want false", name)
		}
	}
}

func TestSendChatMessageMarksOwn(t *testing.T) {
	withChatNameEnv(t)
	setChatDisplayName("Steppa")
	sendChatMessage("Steppa", "hello")
	sendChatMessage("Juka AI", "hi there")
	if len(chatMessages) != 2 {
		t.Fatalf("got %d messages, want 2", len(chatMessages))
	}
	if !chatMessages[0].Own {
		t.Error("message sent under the local display name is not marked Own")
	}
	if chatMessages[1].Own {
		t.Error("assistant message must not be marked Own")
	}
}

func TestSaveChatMessagesPreservesName(t *testing.T) {
	withChatNameEnv(t)
	setChatDisplayName("Steppa")
	chatMessages = []ChatMessage{{ID: "1", Sender: "Steppa", Text: "hi"}}
	saveChatMessages()
	if userConfigCache.Chat == nil || userConfigCache.Chat.Name != "Steppa" {
		t.Fatalf("saveChatMessages dropped the display name: %+v", userConfigCache.Chat)
	}
}
