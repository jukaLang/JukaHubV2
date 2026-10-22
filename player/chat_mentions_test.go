package main

// Discord sends pings as markup ("<@417829...>"), which the chat used to print
// verbatim. These tests pin the rewrite to names.

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func resetDiscordNameCaches(t *testing.T) {
	t.Helper()
	discordNameMu.Lock()
	prevUsers, prevRoles, prevChannels := discordUserNames, discordRoleNames, discordChannelNames
	discordUserNames = map[string]string{}
	discordRoleNames = map[string]string{}
	discordChannelNames = map[string]string{}
	discordNameMu.Unlock()
	t.Cleanup(func() {
		discordNameMu.Lock()
		discordUserNames, discordRoleNames, discordChannelNames = prevUsers, prevRoles, prevChannels
		discordNameMu.Unlock()
	})
}

func TestDiscordUserMentionsBecomeNames(t *testing.T) {
	resetDiscordNameCaches(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"plain", "<@112233> hi", "@alice hi"},
		{"nickname form", "<@!112233> hi", "@alice hi"},
		{"mid sentence", "hey <@112233>, look", "hey @alice, look"},
		{"twice", "<@112233> and <@112233>", "@alice and @alice"},
		{"no markup", "hello everyone", "hello everyone"},
		{"plain at-sign untouched", "@everyone ping", "@everyone ping"},
		// A custom emoji is <:name:id>; it must survive the rewrite.
		{"custom emoji", "<:blob:445566>", "<:blob:445566>"},
	}
	for _, tc := range cases {
		msg := &discordgo.Message{
			Content:  tc.text,
			Mentions: []*discordgo.User{{ID: "112233", Username: "alice"}},
		}
		if _, got := discordMessageParts(msg); got != tc.want {
			t.Errorf("%s: text = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An edited or restored message can arrive without its mention list; the id
// must not leak into the bubble.
func TestDiscordUnknownMentionDoesNotPrintTheID(t *testing.T) {
	resetDiscordNameCaches(t)
	_, got := discordMessageParts(&discordgo.Message{Content: "ping <@999888777>"})
	if strings.Contains(got, "999888777") {
		t.Fatalf("text = %q, still shows the raw id", got)
	}
	if got != "ping @user" {
		t.Fatalf("text = %q, want the mention labelled as a user", got)
	}
}

// A name seen once is remembered, so a later message that references the same
// id without listing the user still resolves.
func TestDiscordMentionNamesAreRemembered(t *testing.T) {
	resetDiscordNameCaches(t)
	first := &discordgo.Message{
		Content:  "hi <@445566>",
		Mentions: []*discordgo.User{{ID: "445566", Username: "bob"}},
	}
	if _, got := discordMessageParts(first); got != "hi @bob" {
		t.Fatalf("first message = %q", got)
	}
	if _, got := discordMessageParts(&discordgo.Message{Content: "and <@445566> again"}); got != "and @bob again" {
		t.Fatalf("second message = %q, want the remembered name", got)
	}
}

func TestDiscordRoleAndChannelMentionsBecomeNames(t *testing.T) {
	resetDiscordNameCaches(t)
	cacheDiscordNames(
		[]*discordgo.Role{{ID: "778899", Name: "Mods"}},
		[]*discordgo.Channel{{ID: "556677", Name: "general"}},
	)

	msg := &discordgo.Message{Content: "ask <@&778899> in <#556677>"}
	if _, got := discordMessageParts(msg); got != "ask @Mods in #general" {
		t.Fatalf("text = %q, want the role and channel names", got)
	}
}

// The bridge bot reposts text on someone else's behalf, so its messages run
// through the same rewrite.
func TestDiscordBridgeMentionsAreResolvedToo(t *testing.T) {
	resetDiscordNameCaches(t)
	msg := &discordgo.Message{
		Content:  "steppa: hi <@112233>",
		Author:   &discordgo.User{ID: "1", Username: "TrimuiSmartProJuka"},
		Mentions: []*discordgo.User{{ID: "112233", Username: "alice"}},
	}
	sender, got := discordMessageParts(msg)
	if sender != "steppa" {
		t.Fatalf("sender = %q, want the relayed name", sender)
	}
	if got != "hi @alice" {
		t.Fatalf("text = %q, want the mention resolved", got)
	}
}
