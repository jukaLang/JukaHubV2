package main

// Notifications during videos (Settings → Media). A full-screen video that is
// playing is left alone by default: a toast nobody asked for (a download that
// finished, an update, a bug report sent) waits its time out unseen, still
// filed in the bell, and when the video stops or leaves full screen one line
// says how many came in. With the switch on, those toasts show over the video
// as they always did, and so do chat messages (Discord, Juka's answers),
// which otherwise only count on the Home Chat tile.
//
// What the user just did is never held: a toast raised within
// videoNotifInputWindowMS of a button press ("Subtitles on", "Quality: 720p")
// answers that press. Errors are not held either: the ones that come while a
// video plays are usually about the video.
//
// Everything here runs on the render thread (videoNotifTick and
// videoNotifHoldsToast are called from renderToast); the chat side only reads
// the screensaver's chat feed under its own lock.

import (
	"fmt"
	"strings"
	"time"
)

// videoNotifKey is the Settings switch (Custom, "true"/"false"; off by default).
const videoNotifKey = "notifyDuringVideo"

// videoNotifInputWindowMS is how soon after a button press a toast still
// counts as the answer to it.
const videoNotifInputWindowMS = 1500

// videoNotifChatMaxRunes caps the message text a chat toast shows.
const videoNotifChatMaxRunes = 140

var videoNotif struct {
	// watching is last frame's videoNotifWatching.
	watching bool
	// held counts toasts kept off the video since watching began.
	held int
	// decidedAt / decidedHold remember the answer for one toast (its
	// StartedAt), so a press during a held toast does not reveal it halfway.
	decidedAt   uint64
	decidedHold bool
	// chatSeen is the arrival time of the newest chat note already handled.
	chatSeen time.Time
}

// videoNotifEnabled reports the Settings switch.
func videoNotifEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, videoNotifKey, false)
}

// videoNotifWatching reports a full-screen video that is playing (not paused,
// not loading): the only time toasts are held.
func videoNotifWatching() bool {
	return videoFullscreen && playerPhase() == "playing"
}

// videoNotifByUser reports whether a toast that started at startedAt answers
// a button press: the press came first and at most the window before it.
func videoNotifByUser(startedAt, lastInput uint64) bool {
	return lastInput > 0 && startedAt >= lastInput && startedAt-lastInput <= videoNotifInputWindowMS
}

// videoNotifShouldHold is the pure decision for one toast.
func videoNotifShouldHold(watching, enabled bool, kind ToastKind, startedAt, lastInput uint64) bool {
	if !watching || enabled {
		return false
	}
	if kind == ToastKindError || kind == ToastKindSwitch {
		return false
	}
	return !videoNotifByUser(startedAt, lastInput)
}

// videoNotifHoldsToast reports whether renderToast keeps t off the screen this
// frame. The answer is fixed per toast while watching lasts.
func videoNotifHoldsToast(config *Config, t ToastState) bool {
	if !videoNotif.watching {
		return false
	}
	if t.StartedAt == videoNotif.decidedAt {
		return videoNotif.decidedHold
	}
	var last uint64
	if frameRateCtrl != nil {
		last = frameRateCtrl.LastInput()
	}
	hold := videoNotifShouldHold(true, videoNotifEnabled(config), t.Kind, t.StartedAt, last)
	videoNotif.decidedAt = t.StartedAt
	videoNotif.decidedHold = hold
	if hold {
		videoNotif.held++
	}
	return hold
}

// videoNotifSummary is the line shown when watching ends with n held toasts.
func videoNotifSummary(n int) string {
	if n == 1 {
		return "1 notification came in during the video — it is in the bell"
	}
	return fmt.Sprintf("%d notifications came in during the video — they are in the bell", n)
}

// videoNotifChatLine is the toast for a chat message: "Sender: text", with
// "(+N more)" when several came in the same moment.
func videoNotifChatLine(sender, text string, more int) string {
	text = ellipsizeRunes(strings.Join(strings.Fields(text), " "), videoNotifChatMaxRunes)
	line := text
	if sender = strings.TrimSpace(sender); sender != "" {
		line = sender + ": " + text
	}
	if more > 0 {
		line += fmt.Sprintf(" (+%d more)", more)
	}
	return line
}

// videoNotifTick runs once a frame before the toast is drawn: it notices
// watching start and end (the summary) and raises chat toasts while watching
// with the switch on.
func videoNotifTick(config *Config) {
	watching := videoNotifWatching()
	if watching && !videoNotif.watching {
		videoNotif.held = 0
		videoNotif.decidedAt = 0
	}
	if !watching && videoNotif.watching {
		n := videoNotif.held
		videoNotif.held = 0
		videoNotif.decidedAt = 0
		if n > 0 {
			// Not filed: the toasts it counts are in the bell already.
			showToastPrimitive(videoNotifSummary(n), ToastKindInfo)
		}
	}
	videoNotif.watching = watching
	videoNotifChatTick(watching && videoNotifEnabled(config))
}

// videoNotifChatTick shows the newest chat note that arrived since the last
// frame, when show is true. Otherwise it only moves the mark, so turning the
// switch on or starting a video does not replay old messages. Screenshot mode
// shows none (other people's names and words).
func videoNotifChatTick(show bool) {
	now := time.Now()
	if !show || shotModeOn() {
		videoNotif.chatSeen = now
		return
	}
	ssChatFeed.mu.Lock()
	var newest ssChatNote
	fresh := 0
	for _, note := range ssChatFeed.notes {
		if note.at.After(videoNotif.chatSeen) {
			fresh++
			newest = note
		}
	}
	ssChatFeed.mu.Unlock()
	if fresh == 0 {
		return
	}
	videoNotif.chatSeen = newest.at
	// Not filed in the bell: the Home Chat tile already counts the message.
	showToastPrimitive(videoNotifChatLine(newest.sender, newest.text, fresh-1), ToastKindInfo)
}
