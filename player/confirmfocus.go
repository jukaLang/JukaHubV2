package main

// confirmfocus.go — the one focus rule every confirmation follows (backlog
// UX-14, docs/UX_REVIEW.md principle 8): a card that asks before deleting,
// forgetting, resetting, uninstalling or removing something opens on its safe
// answer and remembers nothing, so an A pressed by habit (or the repeat of the
// press that opened the card) changes nothing.
//
// The shared cards apply it when they open — tbxOpenSheet and tbxUpdateSheet
// (tbxui.go) and recoveryCardShow (recoverycard.go) — so a new confirmation is
// safe whichever order it lists its buttons in. A card that would open on a
// destructive button opens on its Cancel/Keep/Not now/Close button instead; a
// card with no such button, or one opening on a harmless action, is left as
// it was.
//
// The Home power card is the one exception: Exit stays focused there
// (home_power.go), because quitting is a clean shutdown and the owner asked
// for the two-press quit; Sleep, Restart and Power off are never focused first.

import "strings"

// confirmDestructiveVerbs start the label of a button that throws something
// away. Lower case; the label is matched as a whole word.
var confirmDestructiveVerbs = [...]string{
	"delete", "forget", "reset", "uninstall", "remove", "erase", "wipe", "clear", "discard", "format",
}

// confirmSafeVerbs start the label of an answer that leaves everything as it
// is.
var confirmSafeVerbs = [...]string{"cancel", "keep", "not now", "no", "back", "close"}

// confirmLabelHas reports whether label starts with one of verbs as a whole
// word ("Remove", "Remove 3 files", "Forget cookies"; not "Removed" or
// "Nothing"). Called when a card opens, never per frame.
func confirmLabelHas(label string, verbs []string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, v := range verbs {
		if !strings.HasPrefix(l, v) {
			continue
		}
		rest := l[len(v):]
		if rest == "" || rest[0] == ' ' || strings.HasPrefix(rest, "…") || rest[0] == '.' {
			return true
		}
	}
	return false
}

// confirmIsDestructive reports whether a button label names an action that
// throws something away.
func confirmIsDestructive(label string) bool {
	return confirmLabelHas(label, confirmDestructiveVerbs[:])
}

// confirmIsSafe reports whether a button label is the way out that changes
// nothing.
func confirmIsSafe(label string) bool {
	return confirmLabelHas(label, confirmSafeVerbs[:])
}

// confirmSafeFocus is the button a card with n buttons opens focused on, given
// the one its caller asked for: when that one is destructive and the card has
// a safe answer, the first safe answer; otherwise focus unchanged. label
// returns button i's label.
func confirmSafeFocus(n, focus int, label func(i int) string) int {
	if focus < 0 || focus >= n || !confirmIsDestructive(label(focus)) {
		return focus
	}
	for i := 0; i < n; i++ {
		if i != focus && confirmIsSafe(label(i)) {
			return i
		}
	}
	return focus
}
