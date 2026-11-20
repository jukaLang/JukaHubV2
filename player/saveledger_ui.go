package main

// saveledger_ui.go — the save ledger on screen (saveledger.go).
//
//   - The game page's chip: "Saved 4 min ago" (relative only between two
//     points of this boot), else the date when the clock was believable, else
//     "Saved this session", "Saved before sleep", "Saved before power off",
//     "Last save: in-game save" — in the good tone. "Not saved since it closed
//     unexpectedly", in the warning tone, when the last session ran two
//     minutes or more and ended with a crash, a power loss or JukaHub going
//     away, with no confirmed save.
//   - More → Saves: the last few saves in plain words (kind, when, what led to
//     it; never a folder), and how the last session ended when that matters.
//   - Under a RetroArch game, after a wake: "Progress safe: in-game save 3 min
//     ago" on RetroArch's own message line, when this session has a confirmed
//     save. After the power-on resume: "Back where you left off: saved before
//     power off".
//
// The chip answers from memory: the ledger is read on a goroutine, and the
// words are worked out again when it changes or every saveLedgerChipTTL.

import (
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// saveLedgerChipOrder puts the chip after Ready (10), CHIP-8 (12), the
	// network copy (15) and the Verified chip, before Manual (30).
	saveLedgerChipOrder = 22
	// saveLedgerRowOrder puts More → Saves just above "Saves on <device>" (40).
	saveLedgerRowOrder = 38
	// saveLedgerChipTTL is how long the chip's words are reused: "4 min ago"
	// must move on while the page stays open.
	saveLedgerChipTTL = 15 * time.Second
	// saveLedgerResumeTTL is how long the power-on resume's note waits for
	// its game to start (a save download may come first).
	saveLedgerResumeTTL = time.Minute
	// saveLedgerSheetPrefix starts the Saves sheet's token.
	saveLedgerSheetPrefix = "save-ledger:"
)

func init() {
	gamePageRegisterStatus(gamePageStatus{ID: "save-ledger", Order: saveLedgerChipOrder, Chip: saveLedgerChip})
	gamePageRegisterExtra(gamePageExtra{ID: "save-ledger", Order: saveLedgerRowOrder, Icon: "saves", Row: saveLedgerRow, Open: saveLedgerOpenSheet})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "save-ledger",
		IsOpen: saveLedgerSheetIsOpen,
		Key: func(e *sdl.KeyboardEvent) bool {
			if act := tbxKeyAction(e); act != tbxActNone {
				tbxSheetAction(act)
				saveLedgerSheetSync()
			}
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			if act := tbxPadAction(e); act != tbxActNone {
				tbxSheetAction(act)
				saveLedgerSheetSync()
			}
			return true
		},
		Mouse: func(mx, my int32) bool {
			tbxSheetMouse(mx, my)
			saveLedgerSheetSync()
			return true
		},
		Render: func(renderer *sdl.Renderer, config *Config) {
			if saveLedgerSheet.open && tbxSheetVisible(config) {
				// The sheet's keys in the footer, under its scrim, as the
				// network copy's sheet draws them (nas_download.go).
				_ = renderer.SetClipRect(nil)
				peerDrawFooter(renderer, config, tbxSheetHints())
				tbxRenderSheet(renderer, config)
			}
		},
		Close: func() {
			saveLedgerSheet.open = false
			if tbxSheet.open && strings.HasPrefix(tbxSheet.spec.token, saveLedgerSheetPrefix) {
				tbxCloseSheet()
			}
		},
	})
}

// ── What the ledger holds for one game ──────────────────────────────────

// saveLedgerView is one game's record, copied out of the ledger.
type saveLedgerView struct {
	saves   []saveLedgerEntry // newest first
	last    saveLedgerEntry   // the newest that read back
	hasLast bool
	end     saveLedgerEnd
	hasEnd  bool
}

// saveLedgerViewFor copies path's record. Any goroutine; never the disk.
func saveLedgerViewFor(path string) saveLedgerView {
	var v saveLedgerView
	saveLedger.mu.Lock()
	defer saveLedger.mu.Unlock()
	g := saveLedger.file.Games[path]
	if g == nil {
		return v
	}
	for i := len(g.Saves) - 1; i >= 0; i-- {
		e := g.Saves[i]
		v.saves = append(v.saves, e)
		if !v.hasLast && e.Bad == "" {
			v.last, v.hasLast = e, true
		}
	}
	if g.End != nil {
		v.end, v.hasEnd = *g.End, true
	}
	return v
}

// closedUnsaved reports a last session that ran saveLedgerLongRun or more and
// ended by a crash, a power loss or JukaHub going away, with no confirmed
// save since.
func (v saveLedgerView) closedUnsaved() bool {
	if !v.hasEnd || !v.end.Long || v.end.Saved {
		return false
	}
	switch v.end.How {
	case saveLedgerEndCrash, saveLedgerEndLost, saveLedgerEndGone:
	default:
		return false
	}
	return !v.hasLast || v.end.Seq > v.last.Seq
}

// ── Words ────────────────────────────────────────────────────────────────

// saveLedgerAgoWords is "just now", "4 min ago" or "3 h ago" for a stamp of
// this boot, "" for anything else (or older than two days).
func saveLedgerAgoWords(st saveLedgerStamp) string {
	d, ok := saveLedgerElapsed(st, saveLedgerStampNow(0))
	if !ok || d >= 48*time.Hour {
		return ""
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	}
	return strconv.Itoa(int(d/time.Hour)) + " h ago"
}

// saveLedgerDateWords is "today" or the short date of a stamp whose clock was
// believable, "" otherwise.
func saveLedgerDateWords(config *Config, st saveLedgerStamp) string {
	if st.Date == "" {
		return ""
	}
	if st.Date == vfDateNow() {
		return "today"
	}
	t, err := time.Parse("2006-01-02", st.Date)
	if err != nil {
		return ""
	}
	return formatDateShort(config, t)
}

// saveLedgerWhenWords is when a save was, as well as it can be said: "4 min
// ago", "today", "18 Nov", "this session", or "".
func saveLedgerWhenWords(config *Config, st saveLedgerStamp) string {
	if ago := saveLedgerAgoWords(st); ago != "" {
		return ago
	}
	if d := saveLedgerDateWords(config, st); d != "" {
		return d
	}
	if st.Boot != "" && st.Boot == saveLedgerBootID() {
		return "this session"
	}
	return ""
}

// saveLedgerTriggerWords says what led to a save.
func saveLedgerTriggerWords(trigger string) string {
	switch trigger {
	case saveLedgerTrigSleep:
		return "before sleep"
	case saveLedgerTrigBattery:
		return "when the battery ran low"
	case saveLedgerTrigPowerOff:
		return "before power off"
	case saveLedgerTrigQuit:
		return "when the game closed"
	}
	return "while playing"
}

// saveLedgerSentence capitalises the first letter.
func saveLedgerSentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// saveLedgerChipFor is the page's chip for a record.
func saveLedgerChipFor(config *Config, v saveLedgerView) (string, int, bool) {
	if v.closedUnsaved() {
		return "Not saved since it closed unexpectedly", gamePageToneWarn, true
	}
	if !v.hasLast {
		return "", 0, false
	}
	e := v.last
	switch when := saveLedgerWhenWords(config, e.saveLedgerStamp); {
	case when == "today":
		return "Saved today", gamePageToneGood, true
	case when != "":
		return "Saved " + when, gamePageToneGood, true
	}
	switch e.Trigger {
	case saveLedgerTrigSleep, saveLedgerTrigBattery, saveLedgerTrigPowerOff:
		return "Saved " + saveLedgerTriggerWords(e.Trigger), gamePageToneGood, true
	}
	return "Last save: " + saveLedgerKindWord(e.Kind), gamePageToneGood, true
}

// saveLedgerKindLabel names one entry's save, with its slot for a state.
func saveLedgerKindLabel(e saveLedgerEntry, stem string) string {
	word := saveLedgerSentence(saveLedgerKindWord(e.Kind))
	if e.Kind == saveLedgerKindState && stem != "" {
		if slot, ok := gmetaStateSlot(e.File, stem); ok && slot >= 0 {
			word += " (slot " + strconv.Itoa(slot) + ")"
		}
	}
	return word
}

// saveLedgerEntryLine is one line of the Saves sheet.
func saveLedgerEntryLine(config *Config, e saveLedgerEntry, stem string) string {
	parts := []string{saveLedgerKindLabel(e, stem)}
	if when := saveLedgerWhenWords(config, e.saveLedgerStamp); when != "" {
		parts = append(parts, when)
	}
	if e.Bad != "" {
		parts = append(parts, "did not read back ("+e.Bad+")")
	} else {
		parts = append(parts, saveLedgerTriggerWords(e.Trigger))
	}
	return strings.Join(parts, " · ")
}

// saveLedgerEndLine says how the last session ended without a save.
func saveLedgerEndLine(end saveLedgerEnd) string {
	switch end.How {
	case saveLedgerEndCrash:
		ran := ""
		if end.RanS >= 60 {
			ran = " after " + strconv.FormatInt(end.RanS/60, 10) + " min"
		}
		return "Last time the game crashed" + ran + ", with no new save."
	case saveLedgerEndLost:
		return "Last time the power went while the game was running, with no new save."
	}
	return "Last time JukaHub closed while the game was running, with no new save."
}

// saveLedgerSheetBody is the Saves sheet's text. Main thread (the flush
// setting is read from appConfig).
func saveLedgerSheetBody(config *Config, v saveLedgerView, stem string) string {
	var b strings.Builder
	if v.closedUnsaved() {
		b.WriteString(saveLedgerEndLine(v.end))
		b.WriteString("\n\n")
	}
	b.WriteString("## Last saves\n")
	if len(v.saves) == 0 {
		b.WriteString("None seen yet: each save is noted while you play.\n")
	}
	for _, e := range v.saves {
		b.WriteString("• ")
		b.WriteString(saveLedgerEntryLine(config, e, stem))
		b.WriteString("\n")
	}
	b.WriteString("\nEach save is read back after the emulator writes it: on the card, complete and readable.")
	if secs := saveFlushSeconds(); secs > 0 {
		b.WriteString(" RetroArch games write a changed in-game save to the card every " + strconv.Itoa(secs) + " seconds while you play.")
	}
	return b.String()
}

// ── The page ─────────────────────────────────────────────────────────────

// saveLedgerPage is the chip's last answer. Main thread.
var saveLedgerPage struct {
	path string
	gen  uint64
	at   time.Time
	text string
	tone int
	ok   bool
}

// saveLedgerChip is the page's chip (gamePageRegisterStatus), from memory.
func saveLedgerChip(game libraryGame) (string, int, bool) {
	p := &saveLedgerPage
	gen := saveLedgerGen.Load()
	if p.path != game.Path || p.gen != gen || time.Since(p.at) >= saveLedgerChipTTL {
		p.path, p.gen, p.at = game.Path, gen, time.Now()
		p.text, p.tone, p.ok = "", 0, false
		if shoApplies(game) {
			saveLedgerKickLoad()
			p.text, p.tone, p.ok = saveLedgerChipFor(appConfig, saveLedgerViewFor(game.Path))
		}
	}
	return p.text, p.tone, p.ok
}

// saveLedgerRow is More → Saves (gamePageRegisterExtra): only for a game the
// ledger knows something about.
func saveLedgerRow(game libraryGame) (label, detail string, ok bool) {
	if !shoApplies(game) {
		return "", "", false
	}
	v := saveLedgerViewFor(game.Path)
	if len(v.saves) == 0 && !v.closedUnsaved() {
		return "", "", false
	}
	switch {
	case v.closedUnsaved():
		detail = "Not saved since it closed unexpectedly"
	case v.hasLast:
		detail = saveLedgerSentence(saveLedgerKindWord(v.last.Kind))
		if when := saveLedgerWhenWords(appConfig, v.last.saveLedgerStamp); when != "" {
			detail += " · " + when
		}
	default:
		detail = "The last save did not read back"
	}
	return "Saves", detail, true
}

// saveLedgerSheet is the Saves sheet over the page. Main thread.
var saveLedgerSheet struct {
	open  bool
	token string
}

// saveLedgerOpenSheet opens More → Saves.
func saveLedgerOpenSheet(game libraryGame) {
	token := saveLedgerSheetPrefix + game.Path
	saveLedgerSheet.open, saveLedgerSheet.token = true, token
	tbxOpenSheet(appConfig, tbxSheetSpec{
		token:    token,
		title:    "Saves",
		subtitle: game.Label(),
		icon:     "saves",
		body:     saveLedgerSheetBody(appConfig, saveLedgerViewFor(game.Path), shoGameOf(game).stem),
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// saveLedgerSheetIsOpen is the overlay's IsOpen.
func saveLedgerSheetIsOpen() bool {
	saveLedgerSheetSync()
	return saveLedgerSheet.open
}

// saveLedgerSheetSync notices the sheet closed by its own button or Back.
func saveLedgerSheetSync() {
	if saveLedgerSheet.open && !(tbxSheet.open && tbxSheet.spec.token == saveLedgerSheet.token) {
		saveLedgerSheet.open = false
	}
}

// ── Wake and power-on resume ─────────────────────────────────────────────

// saveLedgerWakeWords is "in-game save 3 min ago" for the running session's
// newest confirmed save; ok false when this session has none. Any goroutine.
func saveLedgerWakeWords(s *saveLedgerSession) (string, bool) {
	saveLedger.mu.Lock()
	ready, since := s.ready, s.startSeq
	saveLedger.mu.Unlock()
	if !ready {
		return "", false
	}
	v := saveLedgerViewFor(s.game.Path)
	if !v.hasLast || v.last.Seq <= since {
		return "", false
	}
	when := saveLedgerAgoWords(v.last.saveLedgerStamp)
	if when == "" {
		when = "this session"
	}
	return saveLedgerKindWord(v.last.Kind) + " " + when, true
}

// saveLedgerResumeWords is the line after the power-on resume: what saved
// the game, from its newest confirmed save. Any goroutine.
func saveLedgerResumeWords(path string) string {
	const back = "Back where you left off"
	v := saveLedgerViewFor(path)
	if !v.hasLast {
		return back
	}
	switch v.last.Trigger {
	case saveLedgerTrigBattery, saveLedgerTrigPowerOff, saveLedgerTrigSleep:
		return back + ": saved " + saveLedgerTriggerWords(v.last.Trigger)
	}
	return back
}

// saveLedgerResume is the game the power-on resume is starting, until its
// launch does. Main thread.
var saveLedgerResume struct {
	path string
	at   time.Time
}

// saveLedgerNoteResume is quickBootTick's note that path is being resumed at
// power-on. Main thread.
func saveLedgerNoteResume(path string) {
	saveLedgerResume.path, saveLedgerResume.at = path, time.Now()
}

// saveLedgerTakeResume reports, once, whether a game starting now is the
// power-on resume's. Main thread.
func saveLedgerTakeResume(path string) bool {
	ok := saveLedgerResume.path != "" && saveLedgerResume.path == path && time.Since(saveLedgerResume.at) < saveLedgerResumeTTL
	saveLedgerResume.path = ""
	return ok
}
