package main

// chip8_shelf_ui.go — "Get CHIP-8 games": the chip8Archive as a list to
// install from (Start → Games, and More on a CHIP-8 game's page).
//
// The screen is a tool screen in Home's glass (tbxui.go's frame, focus list
// and sheet): machine chips (All, CHIP-8, SUPER-CHIP, XO-CHIP) above one row
// per game — its title, authors, machine and event, and "Installed" once it
// is on the shelf. A opens the game's sheet (its description, release, the
// speed the archive asks for) with Install or Play; X installs the focused
// game straight away; Y reads the list from GitHub again. The list and the
// installs come from chip8_archive.go on goroutines and land here through
// tbxPost; the footer is drawn here with the shared hint layer, since the
// tool screens' footer table is not this file's.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	chip8ShelfSceneName = "CHIP8Games"
	chip8ShelfEntryName = "Get CHIP-8 games"
)

// chip8Shelf is the screen's state. Main thread only.
var chip8Shelf struct {
	visit   tbxVisit
	nav     tbxNav
	progs   []chip8Program
	loading bool
	// fetched: the list was read from GitHub this run; savedOnly: the list
	// on show is the saved copy because GitHub could not be reached.
	fetched   bool
	savedOnly bool
	err       string
	installed map[string]string // archive key → its file on the card
	busy      map[string]bool   // keys being installed
	filter    int               // 0 all, else chip8Platform+1
	visBuf    []*chip8Program   // chip8ShelfVisible's, reused every frame
}

// chip8ShelfFilters are the chips.
var chip8ShelfFilters = [...]string{"All", "CHIP-8", "SUPER-CHIP", "XO-CHIP"}

type chip8ShelfModule struct{ sceneBase }

func init() {
	registerScene(chip8ShelfModule{sceneBase{name: chip8ShelfSceneName}})
}

// chip8ShelfOpen opens the screen.
func chip8ShelfOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	chip8EnsureScene(config)
	idx := sceneIndexExact(config, chip8ShelfSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if gameLib.open {
		closeGameLibraryPanel()
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

func (chip8ShelfModule) render(r *sdl.Renderer, c *Config) bool {
	chip8ShelfRender(r, c)
	return true
}

func (chip8ShelfModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e != nil && e.Type == sdl.KEYDOWN && e.Keysym.Sym == sdl.K_F5 && !tbxSheetVisible(c) {
		chip8ShelfLoad(true)
		return true
	}
	return tbxDispatch(c, tbxKeyAction(e), chip8ShelfAction)
}

func (chip8ShelfModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), chip8ShelfAction)
}

// chip8ShelfEnter runs once per visit: the saved list at once, the network's
// once per run, and which games are on the card.
func chip8ShelfEnter() {
	chip8ShelfRefreshInstalled()
	chip8ShelfLoad(false)
}

// chip8ShelfLoad loads the list: the saved copy first when nothing is shown,
// then GitHub's when it was not read this run (or force).
func chip8ShelfLoad(force bool) {
	sh := &chip8Shelf
	if sh.loading {
		return
	}
	needSaved := len(sh.progs) == 0
	needNet := force || !sh.fetched
	if !needSaved && !needNet {
		return
	}
	sh.loading = true
	sh.err = ""
	if force {
		PlayActivateSound()
	}
	go func() {
		var saved []chip8Program
		if needSaved {
			saved, _ = chip8LoadSavedCatalog()
		}
		if len(saved) > 0 {
			tbxPost(func() {
				if len(chip8Shelf.progs) == 0 {
					chip8Shelf.progs = saved
					chip8Shelf.savedOnly = true
				}
			})
		}
		var progs []chip8Program
		var err error
		if needNet {
			if IsOnline() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				progs, err = chip8FetchCatalog(ctx)
				cancel()
			} else {
				err = chip8ErrOffline
			}
		}
		tbxPost(func() {
			sh := &chip8Shelf
			sh.loading = false
			if !needNet {
				return
			}
			if err != nil {
				logWarn("chip8", "reading the chip8Archive list: %v", err)
				sh.err = firstLine(err.Error())
				if errors.Is(err, chip8ErrOffline) {
					sh.err = offlinePlainNotice
				}
				if force {
					ShowToast("Could not read the game list: "+sh.err, ToastKindWarning)
				}
				return
			}
			sh.progs = progs
			sh.fetched = true
			sh.savedOnly = false
		})
	}()
}

// chip8ErrOffline is the list's answer with no network.
var chip8ErrOffline = errors.New("offline")

// chip8ShelfRefreshInstalled reads which archive games are on the card.
func chip8ShelfRefreshInstalled() {
	go func() {
		got := map[string]string{}
		for _, rec := range chip8InstalledAll() {
			if rec.Key != "" && regularFile(rec.Path) {
				got[rec.Key] = rec.Path
			}
		}
		tbxPost(func() { chip8Shelf.installed = got })
	}()
}

// chip8ShelfVisible is the list under the chip that is picked.
func chip8ShelfVisible() []*chip8Program {
	sh := &chip8Shelf
	out := sh.visBuf[:0]
	for i := range sh.progs {
		p := &sh.progs[i]
		if sh.filter > 0 && int(p.platform())+1 != sh.filter {
			continue
		}
		out = append(out, p)
	}
	sh.visBuf = out
	return out
}

// chip8ShelfFind is the program with key.
func chip8ShelfFind(key string) (*chip8Program, bool) {
	for i := range chip8Shelf.progs {
		if chip8Shelf.progs[i].Key == key {
			return &chip8Shelf.progs[i], true
		}
	}
	return nil, false
}

// ── Drawing ─────────────────────────────────────────────────────────────────

func chip8ShelfRender(r *sdl.Renderer, c *Config) {
	sh := &chip8Shelf
	if sh.visit.begin() {
		chip8ShelfEnter()
	}
	fonts, ok := tbxLoadFonts(c)
	if !ok {
		return
	}
	status, tone := "", ColorTextMuted()
	switch {
	case sh.loading && len(sh.progs) == 0:
		status = "Loading…"
	case sh.savedOnly && sh.err != "":
		status, tone = "Saved list · offline", ColorToastWarn
	case len(sh.progs) > 0:
		status = pluralizeCount(len(sh.progs), "game", "games")
	}
	header := tbxHeader(chip8ShelfEntryName, "Free homebrew from the chip8Archive · public domain", "rom", status, tone)
	frame := tbxBeginFrame(r, c, header)
	body := frame.bodyRect()
	nav := &sh.nav
	nav.begin(body.Y, body.H)
	tbxClip(r, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	// The machine chips.
	chipH := fonts.smallH + 18
	cx := x
	for i, label := range chip8ShelfFilters {
		bw := tbxButtonWidth(fonts.small, label, 96)
		rect := sdl.Rect{X: cx, Y: y, W: bw, H: chipH}
		screen := rect
		screen.Y += top
		id := "f:" + strconv.Itoa(i)
		tbxDrawButton(r, c, fonts.small, screen, label, sh.filter == i, nav.focused(id), false)
		nav.add(id, rect, 0, false)
		cx += bw + 10
	}
	y += chipH + 16
	list := chip8ShelfVisible()
	if len(list) == 0 {
		title, msg := "Loading the game list…", "It comes from github.com/JohnEarnest/chip8Archive."
		if !sh.loading {
			title, msg = "No games to show", sh.err
			if msg == "" {
				msg = "No game in the list is for this machine."
			}
		}
		tbxDrawEmpty(r, c, sdl.Rect{X: x, Y: top + y, W: w, H: max(160, body.H-y-20)}, "rom", title, msg)
		nav.extend(y + 160)
	}
	rowH := tbxRowHeight(fonts)
	for i, p := range list {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		id := "p:" + p.Key
		nav.add(id, rect, i+1, false)
		screen := rect
		screen.Y += top
		if screen.Y+screen.H >= body.Y && screen.Y <= body.Y+body.H {
			spec := tbxRowSpec{icon: "rom", title: p.Title, subtitle: p.rowLine()}
			switch {
			case sh.busy[p.Key]:
				spec.pill, spec.tone = "Installing…", tbxToneAccent
			case sh.installed[p.Key] != "":
				spec.pill, spec.tone = "Installed", tbxToneGood
			}
			tbxDrawRow(r, c, fonts, screen, spec, nav.focused(id))
		}
		y += rowH + 8
	}
	nav.extend(y)
	tbxUnclip(r)
	nav.end()
	tbxDrawScrollbar(r, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	r.SetClipRect(nil)
	renderStatusBar(r, c)
	chip8DrawFooter(r, c, chip8ShelfHints(c))
	tbxRenderSheet(r, c)
	_ = r.SetDrawColor(0, 0, 0, 255)
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	r.SetClipRect(nil)
}

// chip8ShelfHints are the screen's keys (the sheet's while one is up).
func chip8ShelfHints(c *Config) []FooterHint {
	if tbxSheetVisible(c) {
		return tbxSheetHints()
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Move"}}
	if strings.HasPrefix(chip8Shelf.nav.focus, "f:") {
		hints = append(hints, FooterHint{Button: "A", Label: "Show"})
	} else {
		hints = append(hints, FooterHint{Button: "A", Label: "Details"}, FooterHint{Button: "X", Label: "Install"})
	}
	return append(hints, FooterHint{Button: "Y / F5", Label: "Refresh"}, FooterHint{Button: "B", Label: "Back"})
}

// chip8DrawFooter is renderFooter with the player's own hints: the same band,
// badges and fitting (planFooterHints), right-aligned.
func chip8DrawFooter(r *sdl.Renderer, c *Config, hints []FooterHint) {
	band := sceneFooterBand()
	drawGlassFooterBand(r, band)
	pad := scaleX(16, screenWidth)
	plan := cachedFooterPlan(c, hints, band.W-2*pad)
	if plan.font == nil {
		return
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(r, c, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(r, c, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

// ── Input ───────────────────────────────────────────────────────────────────

func chip8ShelfAction(c *Config, act tbxAction) bool {
	sh := &chip8Shelf
	focus := sh.nav.focus
	switch act {
	case tbxActConfirm:
		if strings.HasPrefix(focus, "f:") {
			if n, err := strconv.Atoi(focus[2:]); err == nil && n >= 0 && n < len(chip8ShelfFilters) && n != sh.filter {
				sh.filter = n
				sh.nav.scroll = 0
				PlayActivateSound()
			}
			return true
		}
		if p, ok := chip8ShelfFind(strings.TrimPrefix(focus, "p:")); ok {
			PlayActivateSound()
			chip8ShelfSheet(c, p)
		}
		return true
	case tbxActX:
		if p, ok := chip8ShelfFind(strings.TrimPrefix(focus, "p:")); ok {
			chip8ShelfInstall(c, p.Key)
		}
		return true
	case tbxActY:
		chip8ShelfLoad(true)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(&sh.nav, act)
}

// chip8ShelfSheet opens a game's sheet.
func chip8ShelfSheet(c *Config, p *chip8Program) {
	o := p.options()
	var b strings.Builder
	if p.Desc != "" {
		b.WriteString(p.Desc)
		b.WriteString("\n\n")
	}
	b.WriteString("• Machine: " + p.platform().label() + "\n")
	b.WriteString("• Speed: " + strconv.Itoa(o.Tickrate) + " instructions per frame\n")
	if p.Release != "" {
		b.WriteString("• Released: " + p.Release + "\n")
	}
	if p.Event != "" {
		b.WriteString("• Made for: " + p.Event + "\n")
	}
	b.WriteString("\nFrom the chip8Archive, released into the public domain by its authors.")
	sub := p.Authors
	if sub == "" {
		sub = p.platform().label()
	}
	tbxOpenSheet(c, tbxSheetSpec{
		token:    "chip8:" + p.Key,
		title:    p.Title,
		subtitle: sub,
		icon:     "rom",
		body:     b.String(),
		buttons:  chip8SheetButtons(c, p.Key),
	})
}

// chip8SheetButtons are a game's sheet buttons as things stand.
func chip8SheetButtons(c *Config, key string) []tbxSheetButton {
	sh := &chip8Shelf
	closeBtn := tbxSheetButton{label: "Close"}
	switch {
	case sh.busy[key]:
		return []tbxSheetButton{closeBtn}
	case sh.installed[key] != "":
		path := sh.installed[key]
		return []tbxSheetButton{{label: "Play", primary: true, run: func() { chip8ShelfPlay(c, path) }}, closeBtn}
	}
	return []tbxSheetButton{{label: "Install", primary: true, keepOpen: true, run: func() { chip8ShelfInstall(c, key) }}, closeBtn}
}

// chip8ShelfInstall installs one game on a goroutine.
func chip8ShelfInstall(c *Config, key string) {
	sh := &chip8Shelf
	p, ok := chip8ShelfFind(key)
	if !ok || sh.busy[key] {
		return
	}
	if path := sh.installed[key]; path != "" {
		PlayNavSound()
		ShowToast(p.Title+" is already on the CHIP-8 shelf", ToastKindInfo)
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if sh.busy == nil {
		sh.busy = map[string]bool{}
	}
	sh.busy[key] = true
	prog := *p
	PlayActivateSound()
	tbxUpdateSheet("chip8:"+key, tbxSheet.spec.body, chip8SheetButtons(c, key))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		path, err := chip8Install(ctx, prog)
		cancel()
		tbxPost(func() {
			delete(chip8Shelf.busy, key)
			if err != nil {
				logWarn("chip8", "installing %s: %v", key, err)
				PlayErrorSound()
				ShowToast("Could not install "+prog.Title+": "+firstLine(err.Error()), ToastKindError)
			} else {
				if chip8Shelf.installed == nil {
					chip8Shelf.installed = map[string]string{}
				}
				chip8Shelf.installed[key] = path
				// The library reads the card again, so the game is on the
				// CHIP-8 shelf now, not after the next scan.
				refreshGameLibrary()
				invalidateGameLibrarySystemsCache()
				PlaySuccessSound()
				ShowToast(prog.Title+" is on the CHIP-8 shelf", ToastKindSuccess)
			}
			tbxUpdateSheet("chip8:"+key, tbxSheet.spec.body, chip8SheetButtons(c, key))
		})
	}()
}

// chip8ShelfPlay starts an installed game the library's way (so its
// recents, play time and pins apply).
func chip8ShelfPlay(c *Config, path string) {
	if !regularFile(path) {
		PlayErrorSound()
		ShowToast("The game is not on the card any more", ToastKindWarning)
		chip8ShelfRefreshInstalled()
		return
	}
	game := libraryGame{Path: path, System: chip8System, Title: gameDisplayTitle(path)}
	chooseGameLauncher(game, func(launcher string) {
		if err := launchLibraryGame(game, launcher); err != nil {
			gameLaunchErrorToast(game.Title, err)
		}
	})
}
