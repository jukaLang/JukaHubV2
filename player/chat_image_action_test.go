package main

import "testing"

// TestChatSelectionActionsLeadWithEnlarge checks the action bar's order: a
// message that carries a picture offers Enlarge as its first entry, and a
// text-only message keeps the bar it always had.
func TestChatSelectionActionsLeadWithEnlarge(t *testing.T) {
	prev := chatMessages
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Ada", Text: "look at this", ImageURL: "https://example.com/cat.png"},
		{ID: "2", Sender: "Ada", Text: "see https://example.com/dog.png"},
		{ID: "3", Sender: "Ada", Text: "no picture here"},
	}
	t.Cleanup(func() { chatMessages = prev })

	cases := []struct {
		index int
		pic   bool
	}{
		{0, true},
		{1, true},
		{2, false},
	}
	for _, tc := range cases {
		actions := chatSelectionActions(tc.index)
		if len(actions) == 0 {
			t.Fatalf("message %d: no actions", tc.index)
		}
		hasEnlarge := actions[0].kind == "image"
		if hasEnlarge != tc.pic {
			t.Fatalf("message %d: first action = %q, want picture action = %v", tc.index, actions[0].kind, tc.pic)
		}
		if hasEnlarge && actions[0].label != "Enlarge" {
			t.Fatalf("message %d: picture action is labelled %q, want %q", tc.index, actions[0].label, "Enlarge")
		}
		want := []string{"reply", "copy", "ask", "profile", "close"}
		got := make([]string, 0, len(actions))
		for i, a := range actions {
			if i == 0 && hasEnlarge {
				continue
			}
			got = append(got, a.kind)
		}
		if len(got) != len(want) {
			t.Fatalf("message %d: actions after the picture = %v, want %v", tc.index, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("message %d: actions after the picture = %v, want %v", tc.index, got, want)
			}
		}
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
