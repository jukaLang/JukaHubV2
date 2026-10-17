package main

// Tests for Discord token presentation. Discord needs "Bot " in front of a bot
// token and a bare token for a user token; the stored token type is not always
// right, so the app verifies the token against the REST API and uses whichever
// form Discord accepts.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func withDiscordStub(t *testing.T, accept func(auth string) (status int, body string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/@me" {
			http.NotFound(w, r)
			return
		}
		status, body := accept(r.Header.Get("Authorization"))
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	prev := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() {
		discordAPIBase = prev
		srv.Close()
	})
}

func TestDiscordValidateUsesBotPrefixForBotToken(t *testing.T) {
	withDiscordStub(t, func(auth string) (int, string) {
		if auth == "Bot good-token" {
			return http.StatusOK, `{"id":"1","username":"jukahub"}`
		}
		return http.StatusUnauthorized, `{"message":"401: Unauthorized"}`
	})

	auth, err := discordValidateToken("good-token", "bot")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if auth != "Bot good-token" {
		t.Fatalf("auth header = %q", auth)
	}
}

// A bot token stored with type "user" is the case that used to fail silently.
func TestDiscordValidateFallsBackToBotWhenTypeIsUser(t *testing.T) {
	withDiscordStub(t, func(auth string) (int, string) {
		if auth == "Bot good-token" {
			return http.StatusOK, `{}`
		}
		return http.StatusUnauthorized, `{}`
	})

	auth, err := discordValidateToken("good-token", "user")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if auth != "Bot good-token" {
		t.Fatalf("auth header = %q", auth)
	}
}

func TestDiscordValidateKeepsUserTokenBare(t *testing.T) {
	withDiscordStub(t, func(auth string) (int, string) {
		if auth == "user-token" {
			return http.StatusOK, `{}`
		}
		return http.StatusUnauthorized, `{}`
	})

	auth, err := discordValidateToken("user-token", "user")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if auth != "user-token" {
		t.Fatalf("auth header = %q", auth)
	}
}

func TestDiscordValidatePrefixedTokenIsNotDoubled(t *testing.T) {
	withDiscordStub(t, func(auth string) (int, string) {
		if auth == "Bot already-prefixed" {
			return http.StatusOK, `{}`
		}
		return http.StatusUnauthorized, `{}`
	})

	auth, err := discordValidateToken("Bot already-prefixed", "bot")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if auth != "Bot already-prefixed" {
		t.Fatalf("auth header = %q", auth)
	}
}

func TestDiscordValidateExplainsRejection(t *testing.T) {
	withDiscordStub(t, func(string) (int, string) {
		return http.StatusUnauthorized, `{}`
	})

	_, err := discordValidateToken("revoked", "bot")
	if err == nil {
		t.Fatal("revoked token accepted")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestDiscordValidateReportsServerFailure(t *testing.T) {
	withDiscordStub(t, func(string) (int, string) {
		return http.StatusServiceUnavailable, `{}`
	})

	_, err := discordValidateToken("token", "bot")
	if err == nil {
		t.Fatal("503 treated as success")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error does not mention the status: %v", err)
	}
}

func TestDiscordAuthCandidatesOrder(t *testing.T) {
	cases := []struct {
		token string
		typ   string
		want  string
	}{
		{"abc", "user", "abc,Bot abc"},
		{"abc", "bot", "Bot abc,abc"},
		{"abc", "", "Bot abc,abc"},
		{"Bot abc", "user", "Bot abc"},
		{"Bearer abc", "bot", "Bearer abc"},
	}
	for _, c := range cases {
		got := strings.Join(discordAuthCandidates(c.token, c.typ), ",")
		if got != c.want {
			t.Errorf("candidates(%q,%q) = %q, want %q", c.token, c.typ, got, c.want)
		}
	}
	if got := discordAuthCandidates("  ", "bot"); got != nil {
		t.Errorf("empty token candidates = %v", got)
	}
}

// The encrypted channel_profile token is the one -set-secret writes; a stale
// copy in the variables map must not shadow it.
func TestDiscordCredsPrefersChannelProfile(t *testing.T) {
	good, err := EncryptAPIToken("profile-token", "discord_token")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := EncryptAPIToken("stale-token", "discord_token")
	if err != nil {
		t.Fatal(err)
	}
	prevCfg, prevStatus := appConfig, discordStatus
	t.Cleanup(func() { appConfig, discordStatus = prevCfg, prevStatus })

	appConfig = &Config{
		ChannelProfile: ChannelProfileConfig{ChannelID: "42", Token: good},
		Variables:      Variables{Custom: map[string]interface{}{"discord_token": stale}},
	}
	tok, _ := discordCreds()
	if tok != "profile-token" {
		t.Fatalf("token came from the wrong place: %d chars", len(tok))
	}
	if ch := discordChannel(); ch != "42" {
		t.Fatalf("channel = %q", ch)
	}
}

// The connect path must stop before opening a gateway when Discord rejects the
// token, and leave a status the chat UI can show.
func TestDiscordConnectReportsRejectedToken(t *testing.T) {
	withDiscordStub(t, func(string) (int, string) {
		return http.StatusUnauthorized, `{}`
	})

	enc, err := EncryptAPIToken("revoked-token", "discord_token")
	if err != nil {
		t.Fatal(err)
	}
	prevCfg, prevStatus, prevSession := appConfig, discordStatus, discordSession
	t.Cleanup(func() {
		appConfig, discordStatus, discordSession = prevCfg, prevStatus, prevSession
	})
	discordSession = nil
	discordStatus = ""
	appConfig = &Config{ChannelProfile: ChannelProfileConfig{ChannelID: "123", Token: enc}}

	if err := discordConnect(); err == nil {
		t.Fatal("connect succeeded with a rejected token")
	}
	if !strings.Contains(discordStatus, "rejected") {
		t.Fatalf("status does not explain the failure: %q", discordStatus)
	}
	if discordSession != nil {
		t.Fatal("a session was left behind")
	}
}

func TestDiscordMessagePartsUnwrapsBridge(t *testing.T) {
	bridge := &discordgo.Message{
		Content: "alice: hello there",
		Author:  &discordgo.User{Username: "TrimuiSmartProJuka"},
	}
	sender, text := discordMessageParts(bridge)
	if sender != "alice" || text != "hello there" {
		t.Fatalf("bridge message became (%q, %q)", sender, text)
	}

	plain := &discordgo.Message{Content: "hi", Author: &discordgo.User{Username: "bob"}}
	sender, text = discordMessageParts(plain)
	if sender != "bob" || text != "hi" {
		t.Fatalf("plain message became (%q, %q)", sender, text)
	}

	anon := &discordgo.Message{Content: "orphan"}
	if sender, text := discordMessageParts(anon); sender != "unknown" || text != "orphan" {
		t.Fatalf("anonymous message became (%q, %q)", sender, text)
	}
}

func TestDiscordTokenTypeDefaultsToBot(t *testing.T) {
	prev := appConfig
	t.Cleanup(func() { appConfig = prev })

	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if got := discordTokenType(); got != "bot" {
		t.Fatalf("default token type = %q", got)
	}
	appConfig.Variables.Custom["discord_token_type"] = "user"
	if got := discordTokenType(); got != "user" {
		t.Fatalf("configured token type = %q", got)
	}
	appConfig.Variables.Custom["discord_token_type"] = "  "
	if got := discordTokenType(); got != "bot" {
		t.Fatalf("blank token type = %q", got)
	}
}
