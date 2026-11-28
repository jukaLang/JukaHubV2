package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventWebhookPayload(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	body, ok := eventWebhookPayload("game_launched", "A game", now)
	if !ok {
		t.Fatal("a known event was refused")
	}
	var got webhookEvent
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "game_launched" || got.Label != "A game" || got.At != "2026-11-28T12:00:00Z" {
		t.Fatalf("payload = %+v", got)
	}
	if _, ok := eventWebhookPayload("unknown_event", "x", now); ok {
		t.Fatal("an event outside the allowlist was accepted")
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	body, _ = eventWebhookPayload("install_finished", string(long), now)
	_ = json.Unmarshal(body, &got)
	if len(got.Label) != 120 {
		t.Fatalf("label length = %d, want 120", len(got.Label))
	}
}

func TestEventWebhookOptIn(t *testing.T) {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	c.Variables.Custom[eventWebhookKey] = "https://example.test/hook"
	if eventWebhookEnabled(c) {
		t.Fatal("a URL alone enabled webhooks")
	}
	c.Variables.Custom[eventWebhookOptKey] = true
	if !eventWebhookEnabled(c) {
		t.Fatal("URL and switch did not enable webhooks")
	}
	c.Variables.Custom[eventWebhookKey] = "http://example.test/hook"
	if eventWebhookEnabled(c) {
		t.Fatal("a plain-HTTP receiver was accepted")
	}
}

func TestWebhookQueueDropsOldest(t *testing.T) {
	var q webhookQueue
	for i := 0; i < eventWebhookCap+5; i++ {
		q.push(webhookEvent{Kind: "game_launched", Label: string(rune('a' + i))})
	}
	events := q.drain()
	if len(events) != eventWebhookCap {
		t.Fatalf("queue held %d events, want %d", len(events), eventWebhookCap)
	}
	if events[0].Label != "f" {
		t.Fatalf("oldest kept event = %q, want f", events[0].Label)
	}
	if len(q.drain()) != 0 {
		t.Fatal("drain left events behind")
	}
}
