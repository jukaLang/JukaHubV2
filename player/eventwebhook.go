package main

// eventwebhook.go — opt-in POSTs for a few events, so a phone or a home
// automation box can react without JukaHub opening a connection by default.
// The payload carries only the event and a label: no config, keys, ROM paths
// beyond the short title, or device identifiers. The queue is bounded and an
// unavailable receiver never blocks play.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	eventWebhookKey    = "eventWebhookURL"
	eventWebhookOptKey = "eventWebhooks"
	eventWebhookCap    = 16
	eventWebhookWait   = 5 * time.Second
)

// eventWebhookKinds are the events offered. Anything else is dropped before it
// reaches the queue.
var eventWebhookKinds = map[string]bool{
	"game_launched":    true,
	"trophy_unlocked":  true,
	"install_finished": true,
}

type webhookEvent struct {
	Kind  string `json:"event"`
	Label string `json:"name"`
	At    string `json:"at"`
}

// eventWebhookEnabled reports whether the user set a URL and turned the
// switch on. Both are needed: a stale URL from a hand-edited file is inert.
func eventWebhookEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if !customBool(config.Variables.Custom, eventWebhookOptKey, false) {
		return false
	}
	u, _ := customString(config.Variables.Custom, eventWebhookKey)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "https://")
}

// eventWebhookPayload renders one event. The label is bounded so a hostile ROM
// name cannot turn a notification into a large upload.
func eventWebhookPayload(kind, label string, now time.Time) ([]byte, bool) {
	if !eventWebhookKinds[kind] {
		return nil, false
	}
	if len(label) > 120 {
		label = label[:120]
	}
	b, err := json.Marshal(webhookEvent{Kind: kind, Label: label, At: now.UTC().Format(time.RFC3339)})
	return b, err == nil
}

// webhookQueue is a bounded FIFO: the newest event wins when the receiver is
// down, and play never waits for a slow POST.
type webhookQueue struct {
	mu     sync.Mutex
	events []webhookEvent
}

var eventWebhooks webhookQueue

func (q *webhookQueue) push(e webhookEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.events) >= eventWebhookCap {
		q.events = q.events[1:]
	}
	q.events = append(q.events, e)
}

func (q *webhookQueue) drain() []webhookEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.events
	q.events = nil
	return out
}

// eventWebhookNotify queues and sends one event. The send gets its own short
// context and a plain client: no app credentials, cookies or config travel.
func eventWebhookNotify(config *Config, kind, label string) {
	if !eventWebhookEnabled(config) {
		return
	}
	u, _ := customString(config.Variables.Custom, eventWebhookKey)
	u = strings.TrimSpace(u)
	now := time.Now()
	if _, ok := eventWebhookPayload(kind, label, now); !ok {
		return
	}
	eventWebhooks.push(webhookEvent{Kind: kind, Label: label, At: now.UTC().Format(time.RFC3339)})
	go func() {
		for _, e := range eventWebhooks.drain() {
			payload, ok := eventWebhookPayload(e.Kind, e.Label, time.Now())
			if !ok {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), eventWebhookWait)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
			if err != nil {
				cancel()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{}).Do(req)
			cancel()
			if err != nil {
				return // the receiver is offline; the next event tries again
			}
			_ = resp.Body.Close()
		}
	}()
}
