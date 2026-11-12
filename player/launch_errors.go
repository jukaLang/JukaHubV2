package main

// launch_errors.go — the one card that says why something did not start.
//
// Two shapes share it. A launch that failed before its child ran (a missing
// profile executable, a runtime that is not installed) shows a reason and a
// suggested fix (showLaunchFailureDialog). A game that started and quit within
// seconds gets the game diagnosis's findings (gamediag.go,
// showLaunchDiagnosis): a list of issues, each with the file names that are
// missing and the full folder to copy them into, plus the last relevant lines
// of the emulator's log. Folder paths on the handheld are long
// ("/mnt/SDCARD/RetroArch/.retroarch/system/dc"), so every line is wrapped —
// at spaces first, then after a path separator — instead of being ellipsized
// into "…/system/d", and a card taller than the screen scrolls with Up/Down.

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// launchIssue is one finding on the card: what is wrong, the files that are
// missing, the folder they go in and a sentence of explanation. Level orders
// the list (gameDiagLevel: 0 certain, 1 likely, 2 maybe).
type launchIssue struct {
	Level  int
	Title  string
	Files  []string
	Where  string
	Detail string
}

type launchFailureState struct {
	open   bool
	target string
	reason string
	fix    string
	log    string
	// The diagnosis form (showLaunchDiagnosis): a title of its own, one line
	// under it, the findings and a log excerpt.
	title    string
	summary  string
	issues   []launchIssue
	logLines []string
	// scroll is the first body line drawn; maxScroll is set by the renderer.
	scroll    int
	maxScroll int
	// lines is the wrapped body, built once per card width.
	lines  []launchFailureLine
	linesW int32
}

var launchFailure launchFailureState

// launchFailureLineKind picks a body line's font and colour.
type launchFailureLineKind uint8

const (
	launchLineBody launchFailureLineKind = iota
	launchLineHeading
	launchLinePath
	launchLineDim
	launchLineGap
)

type launchFailureLine struct {
	kind launchFailureLineKind
	text string
}

type launchFailureKind uint8

const (
	launchFailureGameFolderMissing launchFailureKind = iota + 1
	launchFailureProfileExecutableMissing
	launchFailureUnsupportedLauncherType
	launchFailureRuntimeMissing
)

// launchFailureCause adds a stable category while preserving the underlying
// error for errors.Is/As and for the reason shown in the dialog.
type launchFailureCause struct {
	kind launchFailureKind
	err  error
}

func (e *launchFailureCause) Error() string { return e.err.Error() }
func (e *launchFailureCause) Unwrap() error { return e.err }

func withLaunchFailureKind(kind launchFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &launchFailureCause{kind: kind, err: err}
}

func launchFailureSuggestion(err error) string {
	var categorized *launchFailureCause
	if errors.As(err, &categorized) {
		switch categorized.kind {
		case launchFailureGameFolderMissing:
			return "Put the game folder under one of the configured game roots, or correct the profile’s folder path."
		case launchFailureProfileExecutableMissing:
			return "Check the profile’s executable path and make sure the game files are installed."
		case launchFailureUnsupportedLauncherType:
			return "Use a supported launcher file (.exe, .bat, .cmd, .lnk, .url or .sh), then rescan."
		case launchFailureRuntimeMissing:
			return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
		}
	}
	// An unclassified exec.ErrNotFound comes from the command or runtime. A
	// missing profile executable has its own category above.
	if errors.Is(err, exec.ErrNotFound) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrNotExist) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrPermission) {
		return "The launcher is not executable. Check file permissions or reinstall the emulator."
	}
	return "Check the emulator's required BIOS, runtime, and launcher configuration. The game log may contain more detail."
}

func showLaunchFailureDialog(target string, err error, logPath string) {
	if err == nil {
		return
	}
	launchFailure = launchFailureState{
		open:   true,
		target: strings.TrimSpace(target),
		reason: strings.TrimSpace(err.Error()),
		fix:    launchFailureSuggestion(err),
		log:    strings.TrimSpace(logPath),
	}
}

// showLaunchDiagnosis opens the card with a diagnosis: title ("Sonic CD
// didn't start"), one summary line, the issues most certain first, the log
// lines worth reading and the log's path. Main thread only.
func showLaunchDiagnosis(title, summary string, issues []launchIssue, logLines []string, logPath string) {
	launchFailure = launchFailureState{
		open:     true,
		title:    strings.TrimSpace(title),
		summary:  strings.TrimSpace(summary),
		issues:   append([]launchIssue(nil), issues...),
		logLines: append([]string(nil), logLines...),
		log:      strings.TrimSpace(logPath),
	}
}

func closeLaunchFailureDialog() {
	launchFailure = launchFailureState{}
	PlayBackSound()
}

// launchFailureScroll moves a tall card by delta lines.
func launchFailureScroll(delta int) {
	next := launchFailure.scroll + delta
	if next > launchFailure.maxScroll {
		next = launchFailure.maxScroll
	}
	if next < 0 {
		next = 0
	}
	if next != launchFailure.scroll {
		launchFailure.scroll = next
		PlayNavSound()
	}
}

// Up and Down scroll the card (and never dismiss it, so reading a long
// diagnosis cannot close it by accident); every other key dismisses.
func handleLaunchFailureKey(e *sdl.KeyboardEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		launchFailureScroll(-2)
		return true
	case sdl.K_DOWN:
		launchFailureScroll(2)
		return true
	}
	closeLaunchFailureDialog()
	return true
}

func handleLaunchFailureController(e *sdl.ControllerButtonEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		launchFailureScroll(-2)
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		launchFailureScroll(2)
		return true
	}
	closeLaunchFailureDialog()
	return true
}

func handleLaunchFailureMouse() bool {
	if !launchFailure.open {
		return false
	}
	closeLaunchFailureDialog()
	return true
}

// launchSplitLong cuts one word wider than maxW into pieces that fit, each
// ending after a path separator when there is one, so a folder path breaks
// between its folders.
func launchSplitLong(measure func(string) int32, word string, maxW int32) []string {
	if measure(word) <= maxW {
		return []string{word}
	}
	var out []string
	runes := []rune(word)
	for len(runes) > 0 {
		n, cut := 0, 0
		for n < len(runes) && measure(string(runes[:n+1])) <= maxW {
			if runes[n] == '/' || runes[n] == '\\' {
				cut = n + 1
			}
			n++
		}
		if n >= len(runes) {
			out = append(out, string(runes))
			break
		}
		if cut == 0 {
			cut = n
		}
		if cut == 0 {
			cut = 1 // a single rune wider than the card
		}
		out = append(out, string(runes[:cut]))
		runes = runes[cut:]
	}
	return out
}

// launchWrapText word-wraps text to maxW, keeping explicit line breaks and
// splitting long words (paths) with launchSplitLong. Nothing is cut off.
func launchWrapText(measure func(string) int32, text string, maxW int32) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			pieces := launchSplitLong(measure, word, maxW)
			if len(pieces) > 1 {
				// A path wider than the card starts on its own line.
				if cur != "" {
					out = append(out, cur)
				}
				out = append(out, pieces[:len(pieces)-1]...)
				cur = pieces[len(pieces)-1]
				continue
			}
			cand := word
			if cur != "" {
				cand = cur + " " + word
			}
			if cur == "" || measure(cand) <= maxW {
				cur = cand
				continue
			}
			out = append(out, cur)
			cur = word
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// launchFailureParagraphs is the card's body before wrapping.
func launchFailureParagraphs(st *launchFailureState) []launchFailureLine {
	var p []launchFailureLine
	add := func(kind launchFailureLineKind, text string) {
		if kind == launchLineGap || strings.TrimSpace(text) != "" {
			p = append(p, launchFailureLine{kind: kind, text: text})
		}
	}
	if st.title == "" {
		// The classic form: why it failed and what to do.
		add(launchLineHeading, "Reason")
		add(launchLineBody, st.reason)
		add(launchLineGap, "")
		add(launchLineHeading, "Suggested fix")
		add(launchLineBody, st.fix)
		if st.log != "" {
			add(launchLineGap, "")
			add(launchLineDim, "Log: "+st.log)
		}
		return p
	}
	if st.summary != "" {
		add(launchLineBody, st.summary)
		add(launchLineGap, "")
	}
	for i, issue := range st.issues {
		heading := issue.Title
		if len(st.issues) > 1 {
			heading = strconv.Itoa(i+1) + ". " + heading
		}
		add(launchLineHeading, heading)
		if len(issue.Files) > 0 {
			add(launchLineDim, "Missing:")
			for _, f := range issue.Files {
				add(launchLinePath, "• "+f)
			}
		}
		if issue.Where != "" {
			add(launchLineDim, "Copy to:")
			add(launchLinePath, issue.Where)
		}
		add(launchLineBody, issue.Detail)
		add(launchLineGap, "")
	}
	if len(st.logLines) > 0 {
		add(launchLineHeading, "From the log")
		for _, line := range st.logLines {
			add(launchLineDim, line)
		}
		add(launchLineGap, "")
	}
	if st.log != "" {
		add(launchLineDim, "Full log: "+st.log)
	}
	return p
}

func renderLaunchFailure(renderer *sdl.Renderer, config *Config) {
	if !launchFailure.open || renderer == nil || config == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	large, _ := getCachedFont(config, "large")
	if font == nil || medium == nil || large == nil {
		return
	}
	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	textW := panelW - 48
	if launchFailure.lines == nil || launchFailure.linesW != textW {
		launchFailure.lines = launchFailure.lines[:0]
		measure := func(f func(string) (int, int, error)) func(string) int32 {
			return func(s string) int32 {
				w, _, _ := f(s)
				return int32(w)
			}
		}
		smallW, mediumW := measure(font.SizeUTF8), measure(medium.SizeUTF8)
		for _, para := range launchFailureParagraphs(&launchFailure) {
			if para.kind == launchLineGap {
				launchFailure.lines = append(launchFailure.lines, para)
				continue
			}
			m := smallW
			if para.kind == launchLineHeading {
				m = mediumW
			}
			for _, line := range launchWrapText(m, para.text, textW) {
				launchFailure.lines = append(launchFailure.lines, launchFailureLine{kind: para.kind, text: line})
			}
		}
		if launchFailure.lines == nil {
			launchFailure.lines = []launchFailureLine{}
		}
		launchFailure.linesW = textW
	}
	lineH := func(kind launchFailureLineKind) int32 {
		switch kind {
		case launchLineHeading:
			return int32(medium.Height()) + 6
		case launchLineGap:
			return 12
		}
		return int32(font.Height()) + 4
	}
	contentH := int32(0)
	for _, line := range launchFailure.lines {
		contentH += lineH(line.kind)
	}
	const headTop, footH = 92, 56
	panelH := headTop + contentH + footH
	if panelH < 300 {
		panelH = 300
	}
	if panelH > screenHeight-48 {
		panelH = screenHeight - 48
	}
	bodyH := panelH - headTop - footH
	// The furthest the card scrolls: the first line from which the rest fits.
	launchFailure.maxScroll = 0
	rest := int32(0)
	for i := len(launchFailure.lines) - 1; i >= 0; i-- {
		rest += lineH(launchFailure.lines[i].kind)
		if rest > bodyH {
			launchFailure.maxScroll = i + 1
			break
		}
	}
	if launchFailure.scroll > launchFailure.maxScroll {
		launchFailure.scroll = launchFailure.maxScroll
	}

	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	title := launchFailure.title
	if title == "" {
		title = "Couldn't launch"
		if launchFailure.target != "" {
			title += " " + launchFailure.target
		}
	}
	renderText(renderer, config, large, ellipsize(large, title, textW), ColorTextPrimary(), panelX+24, panelY+24)

	y := panelY + headTop
	bottom := y + bodyH
	for i := launchFailure.scroll; i < len(launchFailure.lines); i++ {
		line := launchFailure.lines[i]
		h := lineH(line.kind)
		if y+h > bottom {
			break
		}
		switch line.kind {
		case launchLineHeading:
			renderText(renderer, config, medium, line.text, ColorTextPrimary(), panelX+24, y)
		case launchLinePath:
			renderText(renderer, config, font, line.text, ColorTextAccent(), panelX+24, y)
		case launchLineDim:
			renderText(renderer, config, font, line.text, ColorTextTertiary(), panelX+24, y)
		case launchLineBody:
			renderText(renderer, config, font, line.text, ColorTextSecondary(), panelX+24, y)
		}
		y += h
	}
	hints := []FooterHint{{"A", "Dismiss"}}
	if launchFailure.maxScroll > 0 {
		hints = append([]FooterHint{{"Up/Down", "Scroll"}}, hints...)
	}
	footer := ellipsize(font, hintLine(hints...), textW)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(), panelX+(panelW-int32(fw))/2, panelY+panelH-34)
}
