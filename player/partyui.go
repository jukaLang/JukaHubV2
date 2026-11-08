package main

// partyui.go — Watch Party's keyboard fields and the entry points other
// screens call.
//
// Watch Party used to be a modal panel drawn over whatever screen opened it.
// It is a full-screen scene now (partyscene.go). What stays here is the glue to
// the on-screen keyboard: the keyboard's "party" target edits partyUI.input,
// and submitting it lands in partySubmitInput. The same target also serves the
// other tool screens' text fields (a bug report's title and description)
// through tbxOpenPrompt, so those fields need no keyboard target of their own.

import (
	"fmt"
	"strings"
)

type partyInputMode int

const (
	partyInputNone partyInputMode = iota
	partyInputJoin
	partyInputQueue
	// partyInputPrompt is a field described by tbxOpenPrompt.
	partyInputPrompt
)

// partyUIState is what the keyboard edits for the party target.
type partyUIState struct {
	inputMode partyInputMode
	input     string
}

var partyUI partyUIState

// tbxPrompt describes the field the keyboard edits in prompt mode.
var tbxPrompt struct {
	title       string
	placeholder string
	submit      func(string)
}

// tbxOpenPrompt opens the on-screen keyboard for one text field of a tool
// screen. seed fills the field, so an edit is a correction instead of a
// retype; submit receives the trimmed text (possibly empty, which clears the
// field) on the main loop.
func tbxOpenPrompt(title, placeholder, seed string, submit func(string)) {
	tbxPrompt.title = title
	tbxPrompt.placeholder = placeholder
	tbxPrompt.submit = submit
	partyUI.inputMode = partyInputPrompt
	partyUI.input = seed
	vkOpenPartyInput()
}

// partyVKTitle is the keyboard's field title for the party target.
func partyVKTitle() string {
	switch partyUI.inputMode {
	case partyInputPrompt:
		return strings.ToUpper(tbxPrompt.title)
	case partyInputQueue:
		return "SHARE A LINK"
	}
	return "ROOM CODE OR INVITE"
}

// partyVKPlaceholder is the hint shown in the empty field.
func partyVKPlaceholder() string {
	switch partyUI.inputMode {
	case partyInputPrompt:
		return tbxPrompt.placeholder
	case partyInputQueue:
		return "https://…"
	}
	return "ABCD2  or  192.168.1.5:8318 ABCD2"
}

// partyOpenJoinPrompt asks for a room code (or a full invite).
func partyOpenJoinPrompt() {
	partyUI.inputMode = partyInputJoin
	partyUI.input = ""
	vkOpenPartyInput()
}

// partyOpenLinkPrompt asks for a link to add to the shared queue.
func partyOpenLinkPrompt() {
	partyUI.inputMode = partyInputQueue
	partyUI.input = ""
	vkOpenPartyInput()
}

// partySubmitInput receives the keyboard's submitted text.
func partySubmitInput(text string) {
	mode := partyUI.inputMode
	partyUI.inputMode = partyInputNone
	partyUI.input = ""
	text = strings.TrimSpace(text)
	switch mode {
	case partyInputPrompt:
		fn := tbxPrompt.submit
		tbxPrompt.submit = nil
		if fn != nil {
			fn(text)
		}
	case partyInputJoin:
		if text != "" {
			partyJoinFlow(text)
		}
	case partyInputQueue:
		if text != "" {
			partyQueueFlow(text, "")
		}
	}
}

// partyDeviceName is the name this device shows in a room: the chat name.
func partyDeviceName() string {
	name := strings.TrimSpace(chatDisplayName())
	if name == "" {
		return "Handheld"
	}
	return name
}

// partyOpenPanel is the old overlay's entry point, kept for callers that have
// not moved to partyOpenScreen: it opens the Watch Party screen.
func partyOpenPanel() {
	partyOpenScreen(appConfig)
}

// partyTogglePanel is F8: it opens Watch Party, or leaves it when it is the
// screen on display.
func partyTogglePanel() {
	if tbxCurrentScene(appConfig) == partySceneName {
		goBackScene(appConfig)
		return
	}
	partyOpenScreen(appConfig)
}

// partyFormatWatch is a watch time for a row: "42s", "12m 05s", "3h 20m".
func partyFormatWatch(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", int(seconds+0.5))
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%dh %02dm", int(seconds)/3600, (int(seconds)%3600)/60)
	}
	return fmt.Sprintf("%dm %02ds", int(seconds)/60, int(seconds)%60)
}

// partyHistoryText is the history sheet's body: the all-time leaderboard and
// the rooms behind it, grouped by host.
func partyHistoryText(report partyHistoryView) string {
	var sb strings.Builder
	sb.WriteString("## All-time watch leaderboard\n")
	if len(report.People) == 0 {
		sb.WriteString("No watch time recorded yet. Join or host a party to start.\n")
	} else {
		shown := len(report.People)
		if shown > partyLeaderboardLimit {
			shown = partyLeaderboardLimit
		}
		for i := 0; i < shown; i++ {
			p := report.People[i]
			rooms := "rooms"
			if p.Rooms == 1 {
				rooms = "room"
			}
			sb.WriteString(fmt.Sprintf("• %d. %s — %s · %d %s\n", i+1, p.Name, partyFormatWatch(p.WatchSecs), p.Rooms, rooms))
		}
	}
	sb.WriteString("\n## Friend history\n")
	if len(report.Friends) == 0 {
		sb.WriteString("No parties yet. Hosted and joined rooms appear here.\n")
		return sb.String()
	}
	for _, friend := range report.Friends {
		count := 0
		for _, room := range report.Rooms {
			if room.Host == friend.Host {
				count++
			}
		}
		word := "rooms"
		if count == 1 {
			word = "room"
		}
		name := friend.Name
		if name == "" {
			name = friend.Host
		}
		sb.WriteString(fmt.Sprintf("• %s — %d %s · %s · %s\n", name, count, word, partyFormatWatch(friend.WatchSecs), partyHistoryWhen(friend.LastSeen)))
		shown := 0
		for _, room := range report.Rooms {
			if room.Host != friend.Host || shown >= partyHistoryFriendRooms {
				continue
			}
			label := room.Name
			if label == "" {
				label = room.Code
			}
			line := "  " + label + " (" + room.Code + ") · " + partyFormatWatch(room.WatchSecs)
			if room.Title != "" {
				line += " · " + room.Title
			}
			sb.WriteString(line + "\n")
			shown++
		}
	}
	return sb.String()
}
