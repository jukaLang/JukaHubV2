package main

// whileaway.go — the "While you were playing" return banner.
//
// The player is already a multitasker that the firmware never shows. While a
// game, port or Wine app owns the framebuffer, the render loop parks (see
// gamesuspend.go) — but the worker goroutines keep going: the download queue
// saves tracks, helper tools and PortMaster install in the background,
// achievements unlock from their polls. Each of those raises its own toast,
// which nobody sees: no frame is presented under a live child, so the badges
// pile up in the queue and the last one briefly flashes, unreadable, when the
// game hands the screen back.
//
// The visible half lives here. Completion sites call a note-callback; the
// first presented frame after the last child exits drains the ledger into one
// summary toast and drops it for longer than an ordinary banner, because the
// person has just come back and is re-orienting.
//
// Everything a test needs is a plain function of plain values: the exact
// wording lives in awaySummaryText, and the ledger's only behaviour of its own
// is the gate — an event counts as "while you were playing" only when a
// screen-taking child is actually alive. Events raised while the UI is on
// screen keep their existing individual toasts and are ignored here.

import (
	"strconv"
	"strings"
	"sync"
)

// awayEvent is one background completion that happened under a live child.
// kind is one of the awayKind* constants; title is optional detail.
type awayEvent struct {
	kind  string
	title string
}

const (
	awayKindDownload    = "download" // a saved track or video
	awayKindTool        = "tool"     // a helper tool or PortMaster finished installing
	awayKindAchievement = "achievement"
)

// awayEventCap bounds the ledger. A hundred-track queue finishing under one
// child is plausible for music; hundreds is a bug elsewhere, so keep the memory
// flat and aggregate honestly up to the cap.
const awayEventCap = 64

// awayLedger collects completions while the screen is away. Events arrive from
// worker goroutines and are read (and cleared) on the render loop, so every
// access is under mu.
type awayLedger struct {
	mu     sync.Mutex
	events []awayEvent
}

var whileAway awayLedger

// noteAwayEvent records one background completion if — and only if — a
// screen-taking child owns the framebuffer right now. Safe to call from any
// goroutine; the gate is a single atomic load under the ledger lock.
func noteAwayEvent(kind, title string) {
	if strings.TrimSpace(kind) == "" {
		return
	}
	whileAway.mu.Lock()
	defer whileAway.mu.Unlock()
	if !gameScreenSuspended() {
		return
	}
	if len(whileAway.events) < awayEventCap {
		whileAway.events = append(whileAway.events, awayEvent{kind: kind, title: strings.TrimSpace(title)})
	}
}

// noteAwayDownloadSaved is called by the download worker when a track or video
// lands on the card.
func noteAwayDownloadSaved(title string) { noteAwayEvent(awayKindDownload, title) }

// noteAwayToolInstalled is called when a helper tool or PortMaster finished
// installing in the background.
func noteAwayToolInstalled(name string) { noteAwayEvent(awayKindTool, name) }

// noteAwayAchievement is called by the RetroAchievements poll when a game
// unlocks one.
func noteAwayAchievement(title string) { noteAwayEvent(awayKindAchievement, title) }

// awayEventCounts is what awaySummaryText consumes: each kind with the number
// of events recorded (titles are not needed for the aggregate wording).
type awayEventCounts map[string]int

// countAwayEvents folds a ledger into kind counts. Pure values in, pure
// values out — whileaway_test.go exercises it exactly like downloads_test.go
// exercises its queue.
func countAwayEvents(events []awayEvent) awayEventCounts {
	counts := make(awayEventCounts, 3)
	for _, ev := range events {
		counts[ev.kind]++
	}
	return counts
}

// awayLabelFor is the summary wording for one kind. Singular and plural are
// written out rather than counted in the message so a German or French
// translation later only has to touch this one switch.
func awayLabelFor(kind string, n int) (string, bool) {
	switch kind {
	case awayKindDownload:
		if n == 1 {
			return "1 track saved", true
		}
		return strconv.Itoa(n) + " tracks saved", true
	case awayKindTool:
		if n == 1 {
			return "1 tool installed", true
		}
		return strconv.Itoa(n) + " tools installed", true
	case awayKindAchievement:
		if n == 1 {
			return "1 achievement unlocked", true
		}
		return strconv.Itoa(n) + " achievements unlocked", true
	}
	return "", false
}

// awaySummaryText renders the whole ledger into one toast line, or "" when
// there is nothing to say. The prefix names the situation; the clauses are
// joined with the separator the rest of the UI already uses.
func awaySummaryText(events []awayEvent) string {
	if len(events) == 0 {
		return ""
	}
	counts := countAwayEvents(events)
	parts := make([]string, 0, 3)
	// Fixed order so two runs over the same work produce the same sentence;
	// map iteration would shuffle the clauses between returns.
	for _, kind := range []string{awayKindDownload, awayKindTool, awayKindAchievement} {
		if n := counts[kind]; n > 0 {
			if label, ok := awayLabelFor(kind, n); ok {
				parts = append(parts, label)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "While you were playing: " + strings.Join(parts, " · ")
}

// whileAwayDrain takes every recorded event and empties the ledger, so each
// return from a game speaks for exactly what happened under it.
func whileAwayDrain() []awayEvent {
	whileAway.mu.Lock()
	defer whileAway.mu.Unlock()
	events := whileAway.events
	whileAway.events = nil
	return events
}

// whileAwayToastDurationMS is longer than toastDurationFor's success slot: the
// reader has just been handed back the screen and needs a beat to place it.
const whileAwayToastDurationMS = 8000

// whileAwayNotify raises the summary. Called on the render loop, right after
// the parked section of the main loop returns to presenting frames.
func whileAwayNotify() {
	events := whileAwayDrain()
	text := awaySummaryText(events)
	if text == "" {
		return
	}
	ShowToast(text, ToastKindSuccess)
	currentToast.Duration = whileAwayToastDurationMS
}
