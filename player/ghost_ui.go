package main

// ghost_ui.go — the Ghost Challenges screen (Games → Ghost Challenges) and
// the "send this run" sheet (ghost.go has the runs).
//
// The screen is a tool screen in Home's glass (tbxui.go): "From friends", one
// row per run a friend sent (who, which game, the score to beat, your best
// against it and "Beaten" once you have), then "Your best runs", one row per
// game you have played. A races the row's run (yours too: race your own
// best), X deletes a friend's run, Y sends one of your runs. Opening the
// screen takes in any .jghost file left in the Ghosts folder.
//
// The send sheet comes up here and on an arcade game's game-over card (Y):
// a button per paired JukaHub (Settings → Network → Nearby devices, the two
// seen most recently), Save as a file, Close. Sending runs on a goroutine and
// answers with a toast.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const ghostSceneName = "GhostChallenges"

// ghostUI is the screen's state. Main thread only.
var ghostUI struct {
	visit   tbxVisit
	nav     tbxNav
	inbox   []ghostRun
	best    []ghostRun
	loading bool
}

type ghostScreen struct{ sceneBase }

func init() { registerScene(ghostScreen{sceneBase{name: ghostSceneName}}) }

// ghostEnsureScene adds the screen to a config that lacks it.
func ghostEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, ghostSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: ghostSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == ghostSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

func (ghostScreen) render(r *sdl.Renderer, c *Config) bool {
	ghostScreenRender(r, c)
	return true
}

func (ghostScreen) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), ghostScreenAction)
}

func (ghostScreen) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), ghostScreenAction)
}

// ghostScreenLoad reads the runs (and takes in the Ghosts folder's files) off
// the main thread.
func ghostScreenLoad() {
	ghostUI.loading = true
	go func() {
		n := ghostImportShared()
		inbox := ghostInboxRuns()
		best := ghostBestRuns()
		tbxPost(func() {
			ghostUI.inbox, ghostUI.best, ghostUI.loading = inbox, best, false
			if n > 0 {
				ShowToast(pluralizeCount(n, "challenge", "challenges")+" taken in from the Ghosts folder", ToastKindSuccess)
			}
		})
	}()
}

// ghostScreenRender draws the screen.
func ghostScreenRender(r *sdl.Renderer, c *Config) {
	if ghostUI.visit.begin() {
		ghostScreenLoad()
	}
	fonts, ok := tbxLoadFonts(c)
	if !ok {
		return
	}
	status := ""
	if n := len(ghostUI.inbox); n > 0 {
		status = pluralizeCount(n, "challenge", "challenges")
	}
	header := tbxHeader("Ghost Challenges", "Race a friend's run: same seed, their score beside yours", "trophy", status, ColorTextMuted())
	frame := tbxBeginFrame(r, c, header)
	body := frame.bodyRect()
	nav := &ghostUI.nav
	nav.begin(body.Y, body.H)
	tbxClip(r, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	rowH := tbxRowHeight(fonts)
	row := 0
	add := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		nav.add(id, rect, row, false)
		row++
		screen := rect
		screen.Y += top
		if screen.Y+screen.H >= body.Y && screen.Y <= body.Y+body.H {
			tbxDrawRow(r, c, fonts, screen, spec, nav.focused(id))
		}
		y += rowH + 8
	}

	tbxDrawSection(r, c, "From friends", "users", x, top+y, w)
	y += tbxSectionHeight
	if len(ghostUI.inbox) == 0 {
		msg := "A friend's run arrives from a paired JukaHub nearby, or as a .jghost file in " + ghostSharedDir()
		if ghostUI.loading {
			msg = "Looking…"
		}
		tbxDrawEmpty(r, c, sdl.Rect{X: x, Y: top + y, W: w, H: 150}, "trophy", "No challenges yet", msg)
		y += 160
	}
	for _, g := range ghostUI.inbox {
		spec := tbxRowSpec{icon: "trophy", title: fmt.Sprintf("%s · %s: %d", setOr(g.Player, "A friend"), ghostGameTitle(g.Game), g.Score)}
		switch {
		case g.Beat:
			spec.pill, spec.tone = "Beaten", tbxToneGood
			spec.subtitle = fmt.Sprintf("Your best against it: %d", g.Tried)
		case g.Tries > 0:
			spec.subtitle = fmt.Sprintf("Your best against it: %d · %s", g.Tried, pluralizeCount(g.Tries, "try", "tries"))
		default:
			spec.subtitle = "Not raced yet"
		}
		add("in:"+g.ID, spec)
	}

	y += 8
	tbxDrawSection(r, c, "Your best runs", "star", x, top+y, w)
	y += tbxSectionHeight
	if len(ghostUI.best) == 0 && !ghostUI.loading {
		tbxDrawEmpty(r, c, sdl.Rect{X: x, Y: top + y, W: w, H: 150}, "games", "No runs yet",
			"Play Flappy, Snake, Tetris, 2048 or Brick Breaker: your best run of each is kept here to send")
		y += 160
	}
	for _, g := range ghostUI.best {
		add("best:"+g.Game, tbxRowSpec{icon: "star", title: fmt.Sprintf("%s: %d", ghostGameTitle(g.Game), g.Score),
			subtitle: "Race yourself, or send it to a friend"})
	}
	nav.extend(y)
	tbxUnclip(r)
	nav.end()
	tbxDrawScrollbar(r, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	r.SetClipRect(nil)
	renderStatusBar(r, c)
	chip8DrawFooter(r, c, ghostScreenHints(c))
	tbxRenderSheet(r, c)
}

// ghostScreenHints are the screen's keys (the sheet's while one is up).
func ghostScreenHints(c *Config) []FooterHint {
	if tbxSheetVisible(c) {
		return tbxSheetHints()
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Move"}, {Button: "A", Label: "Race"}}
	switch focus := ghostUI.nav.focus; {
	case strings.HasPrefix(focus, "in:"):
		hints = append(hints, FooterHint{Button: "X", Label: "Delete"})
	case strings.HasPrefix(focus, "best:"):
		hints = append(hints, FooterHint{Button: "Y", Label: "Send"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ghostFocused is the run on the focused row.
func ghostFocused() (ghostRun, bool, bool) {
	focus := ghostUI.nav.focus
	if id, ok := strings.CutPrefix(focus, "in:"); ok {
		for _, g := range ghostUI.inbox {
			if g.ID == id {
				return g, true, true
			}
		}
	}
	if game, ok := strings.CutPrefix(focus, "best:"); ok {
		for _, g := range ghostUI.best {
			if g.Game == game {
				return g, false, true
			}
		}
	}
	return ghostRun{}, false, false
}

// ghostScreenAction handles the screen's keys.
func ghostScreenAction(c *Config, act tbxAction) bool {
	run, friend, ok := ghostFocused()
	switch act {
	case tbxActConfirm:
		if !ok {
			return true
		}
		PlayActivateSound()
		label := "Your best"
		if friend {
			label = ghostRunLabel(run)
		}
		ghostRace(c, run, label)
		return true
	case tbxActX:
		if ok && friend {
			ghostInboxDelete(run.ID)
			PlayNavSound()
			ShowToast("Deleted "+setOr(run.Player, "the")+"'s "+ghostGameTitle(run.Game)+" run", ToastKindInfo)
			ghostScreenLoad()
		}
		return true
	case tbxActY:
		if ok && !friend {
			ghostSendSheet(c, run)
		}
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(&ghostUI.nav, act)
}

// ghostSendSheet offers a run to a paired JukaHub or as a file.
func ghostSendSheet(c *Config, run ghostRun) {
	if c == nil {
		c = appConfig
	}
	title := fmt.Sprintf("%s: %d", ghostGameTitle(run.Game), run.Score)
	var b strings.Builder
	b.WriteString("Their JukaHub races it: the same seed — the same pipes, pieces and tiles — with your score counting up beside theirs")
	if run.Game == "flappy" {
		b.WriteString(", and your bird flying beside theirs")
	}
	b.WriteString(".\n\n")
	var buttons []tbxSheetButton
	peers := ghostSendPeers()
	if len(peers) == 0 {
		b.WriteString("• No JukaHub is paired: pair one in Settings → Network → Nearby devices to send it straight there.\n")
	}
	for _, p := range peers {
		buttons = append(buttons, tbxSheetButton{label: "Send to " + ellipsizeRunes(setOr(p.Name, "device"), 14), primary: true, run: func() {
			ShowToast("Sending your "+ghostGameTitle(run.Game)+" run to "+setOr(p.Name, "the device")+"…", ToastKindInfo)
			go func() {
				err := ghostSendToPeer(p.ID, run)
				tbxPost(func() {
					if err != nil {
						ShowToast("Could not send it: "+firstLine(err.Error()), ToastKindWarning)
						return
					}
					ShowToast("Sent to "+setOr(p.Name, "the device")+": they race your "+fmt.Sprint(run.Score), ToastKindSuccess)
				})
			}()
		}})
	}
	b.WriteString("• Save as a file puts it in " + ghostSharedDir() + " as a .jghost file: send it any way you like — Chat, a card, a cable.")
	buttons = append(buttons, tbxSheetButton{label: "Save as a file", run: func() {
		go func() {
			path, err := ghostExport(run)
			tbxPost(func() {
				if err != nil {
					ShowToast("Could not save it: "+firstLine(err.Error()), ToastKindWarning)
					return
				}
				ShowToast("Saved "+filepath.Base(path)+" in the Ghosts folder", ToastKindSuccess)
			})
		}()
	}}, tbxSheetButton{label: "Close"})
	tbxOpenSheet(c, tbxSheetSpec{title: "Send this run", subtitle: title, icon: "trophy", body: b.String(), buttons: buttons})
}

// ghostSendPeers are the paired JukaHubs to offer, the two seen most
// recently, while Nearby devices is on.
func ghostSendPeers() []peerRecord {
	if !peerEnabledFlag.Load() {
		return nil
	}
	peers := peerPairedList()
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].LastSeen.After(peers[j].LastSeen) })
	if len(peers) > 2 {
		peers = peers[:2]
	}
	return peers
}
