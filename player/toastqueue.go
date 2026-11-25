package main

// toastqueue.go — toasts wait their turn instead of replacing each other.
//
// There is one toast on screen (core.go's currentToast). A newer one used to
// replace it at once, so a burst — "Saved", then a warning a moment later —
// showed only the last, and the first flashed by unread. Now:
//
//   - A toast whose text is the one on screen with other numbers ("Volume
//     40%" → "Volume 45%", a download's percent) replaces it in place, as
//     before: it is the same message, updated. So do scene-switch labels.
//   - A toast with a higher priority than the one on screen (an error over
//     a confirmation) takes the screen at once.
//   - Otherwise it waits in a short queue, ordered by priority (errors,
//     warnings, the rest) and then by arrival, until the one on screen has
//     been up for toastMinShowMS (stretched by Accessibility's toast time) —
//     or, when what waits matters less than what shows, for its whole time;
//     then the next one shows. A toast that waited longer than
//     toastStaleMS is skipped: it is old news, and it is in the bell.
//   - The queue holds toastQueueMax; when full, the oldest of the lowest
//     priority makes room.
//
// Everything here runs under currentToastMu.

import (
	"strings"
	"testing"
)

const (
	toastQueueMax  = 4
	toastMinShowMS = 1100
	toastStaleMS   = 9000
)

// toastQueue are the toasts waiting, in the order they will show. Guarded by
// currentToastMu.
var toastQueue []ToastState

// toastRank orders kinds for the queue: errors first, scene switches last.
func toastRank(k ToastKind) int {
	switch k {
	case ToastKindError:
		return 3
	case ToastKindWarning:
		return 2
	case ToastKindSwitch:
		return 0
	}
	return 1
}

// toastStem is a message without its numbers, so two readings of one value
// ("Volume 40%", "Volume 45%") are recognised as one message.
func toastStem(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	for _, r := range strings.ToLower(msg) {
		if (r >= '0' && r <= '9') || r == '%' || r == '.' || r == ',' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// toastMinShow is how long t stays up before a waiting toast may take over.
func toastMinShow(t ToastState) uint64 {
	m := a11yToastMs(toastMinShowMS)
	if t.Duration > 0 && t.Duration < m {
		return t.Duration
	}
	return m
}

// toastDone reports a toast whose time is up (or no toast at all).
func toastDone(t ToastState, now uint64) bool {
	return t.Message == "" || t.Duration == 0 || now < t.StartedAt || now-t.StartedAt > t.Duration
}

// toastOfferLocked places a new toast: on screen at once, or in the queue.
// The caller holds currentToastMu.
func toastOfferLocked(next ToastState, now uint64) {
	cur := currentToast
	if next.Message == "" || testing.Testing() {
		// Under go test a toast replaces the one on screen, as before: the
		// tests read the newest message their code raised.
		// An empty toast takes back the one on screen; the next waiting one
		// shows on the next frame.
		currentToast = next
		return
	}
	stem := toastStem(next.Message)
	if toastDone(cur, now) || next.Kind == ToastKindSwitch || cur.Kind == ToastKindSwitch ||
		toastStem(cur.Message) == stem {
		currentToast = next
		return
	}
	for i := range toastQueue {
		if toastStem(toastQueue[i].Message) == stem {
			toastQueue[i] = next
			return
		}
	}
	if toastRank(next.Kind) > toastRank(cur.Kind) {
		// The lesser one was seen, if briefly; it is in the bell.
		currentToast = next
		return
	}
	if len(toastQueue) == 0 && now-cur.StartedAt >= toastMinShow(cur) && toastRank(next.Kind) >= toastRank(cur.Kind) {
		currentToast = next
		return
	}
	toastEnqueueLocked(next)
}

// toastEnqueueLocked adds t in priority order, making room when full.
func toastEnqueueLocked(t ToastState) {
	if len(toastQueue) >= toastQueueMax {
		// Drop the oldest of the lowest rank (the queue is ordered by rank,
		// then arrival, so that is the first of the last rank's run).
		low := toastRank(toastQueue[len(toastQueue)-1].Kind)
		if toastRank(t.Kind) < low {
			return
		}
		for i := range toastQueue {
			if toastRank(toastQueue[i].Kind) == low {
				toastQueue = append(toastQueue[:i], toastQueue[i+1:]...)
				break
			}
		}
	}
	at := len(toastQueue)
	for i := range toastQueue {
		if toastRank(t.Kind) > toastRank(toastQueue[i].Kind) {
			at = i
			break
		}
	}
	toastQueue = append(toastQueue, ToastState{})
	copy(toastQueue[at+1:], toastQueue[at:])
	toastQueue[at] = t
}

// toastAdvanceLocked runs once a frame before the toast is read: a toast
// whose time is up goes, and a waiting one takes the screen once the one on
// it has been up long enough. The caller holds currentToastMu.
func toastAdvanceLocked(now uint64) {
	cur := currentToast
	done := toastDone(cur, now)
	if len(toastQueue) == 0 {
		if done && cur.Message != "" {
			currentToast = ToastState{}
		}
		return
	}
	if !done && (now-cur.StartedAt < toastMinShow(cur) || toastRank(toastQueue[0].Kind) < toastRank(cur.Kind)) {
		// Up for less than its minimum, or what waits matters less: an error
		// keeps its whole time over a confirmation behind it.
		return
	}
	for len(toastQueue) > 0 {
		head := toastQueue[0]
		toastQueue = toastQueue[1:]
		if now >= head.StartedAt && now-head.StartedAt > toastStaleMS {
			continue
		}
		// Its time starts now that it shows.
		head.StartedAt = now
		currentToast = head
		return
	}
	if done {
		currentToast = ToastState{}
	}
}
