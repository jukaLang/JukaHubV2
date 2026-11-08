package main

import "testing"

// TestChatSelectionActionsLeadWithEnlarge checks the action bar's order: a
// message that carries a picture offers Enlarge as its first entry, a link
// comes next, and the rest is the community bar every message has.
func TestChatSelectionActionsLeadWithEnlarge(t *testing.T) {
	resetChatTestState(t)
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Ada", Text: "look at this", ImageURL: "https://example.com/cat.png"},
		{ID: "2", Sender: "Ada", Text: "see https://example.com/dog.png"},
		{ID: "3", Sender: "Ada", Text: "no picture here"},
	}

	common := []string{"reply", "copy", "ask", "save", "profile", "close"}
	cases := []struct {
		index int
		want  []string
	}{
		{0, append([]string{"image"}, common...)},
		{1, append([]string{"image", "open"}, common...)},
		{2, common},
	}
	for _, tc := range cases {
		actions := chatSelectionActions(tc.index)
		got := make([]string, 0, len(actions))
		for _, a := range actions {
			got = append(got, a.kind)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("message %d: actions = %v, want %v", tc.index, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("message %d: actions = %v, want %v", tc.index, got, tc.want)
			}
		}
		if tc.want[0] == "image" && actions[0].label != "Enlarge" {
			t.Fatalf("message %d: picture action is labelled %q, want %q", tc.index, actions[0].label, "Enlarge")
		}
	}
}

// The tabs that belong to this device offer Delete on every message, and the
// community channel only on this device's own.
func TestChatSelectionActionsFollowTheTab(t *testing.T) {
	resetChatTestState(t)
	chatMessages = []ChatMessage{{ID: "1", Sender: "Ada", Text: "hi"}}
	chatAIMessages = []ChatMessage{{ID: "2", Sender: chatAISender, Text: "Hello!"}}
	chatSavedMessages = []ChatMessage{{ID: "3", Sender: "You", Text: "note", Own: true}}

	has := func(kind string) bool {
		for _, a := range chatSelectionActions(0) {
			if a.kind == kind {
				return true
			}
		}
		return false
	}
	if has("delete") {
		t.Error("someone else's community message offers Delete")
	}
	if !has("profile") || !has("reply") {
		t.Error("a community message lost Reply or Profile")
	}
	chatActiveTab.Store(int32(chatTabAI))
	if !has("delete") || has("ask") || has("reply") {
		t.Error("the Juka AI tab offers the wrong actions")
	}
	chatActiveTab.Store(int32(chatTabSaved))
	if !has("delete") || has("save") || has("profile") {
		t.Error("the Saved tab offers the wrong actions")
	}
}

// TestChatMessageImageURLPrefersAttachment pins the source the bubble and the
// bar share: the attachment wins, then the first image link in the text.
func TestChatMessageImageURLPrefersAttachment(t *testing.T) {
	msg := ChatMessage{Text: "see https://example.com/dog.png", ImageURL: "https://example.com/cat.png"}
	if got := chatMessageImageURL(msg); got != "https://example.com/cat.png" {
		t.Fatalf("chatMessageImageURL with attachment = %q, want the attachment", got)
	}
	msg.ImageURL = "  "
	if got := chatMessageImageURL(msg); got != "https://example.com/dog.png" {
		t.Fatalf("chatMessageImageURL without attachment = %q, want the pasted link", got)
	}
	msg.Text = "no picture here"
	if got := chatMessageImageURL(msg); got != "" {
		t.Fatalf("chatMessageImageURL on a text message = %q, want empty", got)
	}
}
