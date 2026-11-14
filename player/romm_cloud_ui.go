package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const rommCloudSceneName = "RomMServer"

const (
	rommCloudGames = iota
	rommCloudFirmware
	rommCloudSettings
)

var rommCloud struct {
	visit     tbxVisit
	nav       tbxNav
	mode      int
	editToken bool
	settings  rommServerSettings
	games     []rommServerGame
	firmware  []rommServerFirmware
	total     int
	busy      bool
	note      string
	gen       uint64
}

func rommCloudEnsureScene(config *Config) {
	if config == nil || sceneIndexExact(config, rommCloudSceneName) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{Name: rommCloudSceneName})
}

func rommCloudOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	rommCloudEnsureScene(config)
	qs.open = false
	changeSceneTo(config, sceneIndexExact(config, rommCloudSceneName))
}

type rommCloudScene struct{ sceneBase }

func (rommCloudScene) render(r *sdl.Renderer, c *Config) bool { rommCloudRender(r, c); return true }
func (rommCloudScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), rommCloudAction)
}
func (rommCloudScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), rommCloudAction)
}
func init() { registerScene(rommCloudScene{sceneBase{name: rommCloudSceneName}}) }

func rommCloudEnter() {
	rommCloud.settings = rommServerSettingsGet()
	rommCloud.mode = rommCloudGames
	rommCloud.nav.focus = "mode0"
	rommCloud.nav.scroll = 0
	rommCloud.games = nil
	rommCloud.firmware = nil
	rommCloud.total = 0
	rommCloud.note = ""
	if rommCloud.settings.URL == "" || rommCloud.settings.Token == "" {
		rommCloud.mode = rommCloudSettings
		rommCloud.nav.focus = "address"
		return
	}
	rommCloudRefresh()
}

func rommCloudRefresh() {
	client, err := newRommServerClient(rommCloud.settings)
	if err != nil {
		rommCloud.note = err.Error()
		rommCloud.mode = rommCloudSettings
		return
	}
	rommCloud.gen++
	gen := rommCloud.gen
	rommCloud.busy = true
	rommCloud.note = "Connecting to your RomM server…"
	mode := rommCloud.mode
	offset := 0
	if mode == rommCloudGames && len(rommCloud.games) > 0 {
		offset = len(rommCloud.games)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		var page rommServerPage
		var bios []rommServerFirmware
		var err error
		if mode == rommCloudFirmware {
			bios, err = client.firmware(ctx)
		} else {
			page, err = client.games(ctx, offset)
		}
		tbxPost(func() {
			if gen != rommCloud.gen {
				return
			}
			rommCloud.busy = false
			if err != nil {
				rommCloud.note = firstLine(err.Error())
				return
			}
			rommCloud.note = "Connected to your RomM server"
			if mode == rommCloudFirmware {
				rommCloud.firmware = bios
			} else {
				if offset == 0 {
					rommCloud.games = page.Items
				} else {
					rommCloud.games = append(rommCloud.games, page.Items...)
				}
				if page.Total != nil {
					rommCloud.total = *page.Total
				}
			}
		})
	}()
}

func rommCloudApplyEdit(value string) {
	next := rommCloud.settings
	if rommCloud.editToken {
		next.Token = strings.TrimSpace(value)
	} else {
		next.URL = strings.TrimSpace(value)
	}
	if next.URL != rommCloud.settings.URL || next.Token != rommCloud.settings.Token {
		next.DeviceID = ""
	}
	if err := rommServerSettingsSave(next); err != nil {
		ShowToast("RomM: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	rommCloud.settings = next
	rommCloud.gen++ // a response from the previous connection cannot repopulate this screen
	rommCloud.busy = false
	rommCloud.games = nil
	rommCloud.firmware = nil
	rommCloud.total = 0
	ShowToast("RomM connection saved", ToastKindSuccess)
}

func rommCloudSettingsRow(id string) tbxRowSpec {
	switch id {
	case "address":
		sub := rommCloud.settings.URL
		if sub == "" {
			sub = "Enter the address of your own RomM server"
		}
		return tbxRowSpec{icon: "network", title: "Server address", subtitle: sub}
	case "token":
		sub := "Paste a read/write client API token from RomM"
		if rommCloud.settings.Token != "" {
			sub = "Saved · token hidden"
		}
		return tbxRowSpec{icon: "key", title: "Client API token", subtitle: sub}
	case "connect":
		return tbxRowSpec{icon: "refresh", title: "Connect and load games", subtitle: "Check your server and show its catalogue"}
	}
	return tbxRowSpec{}
}

func rommCloudRender(renderer *sdl.Renderer, config *Config) {
	if rommCloud.visit.begin() {
		rommCloudEnter()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status := rommCloud.note
	if rommCloud.busy {
		status = "Working…"
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader("RomM", "Games, covers, save sync and BIOS from your server", "rom", status, ColorTextTertiary()))
	body := frame.bodyRect()
	nav := &rommCloud.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	y := int32(0)
	rowNum := 0
	rowH := tbxRowHeight(fonts)
	_, targets := rommDefaultRoots()
	modeNames := []string{"Games", "BIOS files", "Connection"}
	for mode, title := range modeNames {
		id := "mode" + strconv.Itoa(mode)
		pill := ""
		if mode == rommCloud.mode {
			pill = "Open"
		}
		rommCloudDrawRow(renderer, config, fonts, body, y, rowH, rowNum, id,
			tbxRowSpec{icon: []string{"game", "key", "network"}[mode], title: title, subtitle: []string{"Browse and download games with their covers", "Install firmware into the BIOS folder", "Server address and API token"}[mode], pill: pill, tone: tbxToneAccent})
		y += rowH + 5
		rowNum++
	}
	y += 10
	if rommCloud.mode == rommCloudSettings {
		for _, id := range []string{"address", "token", "connect"} {
			rommCloudDrawRow(renderer, config, fonts, body, y, rowH, rowNum, id, rommCloudSettingsRow(id))
			y += rowH + 5
			rowNum++
		}
	} else if rommCloud.mode == rommCloudFirmware {
		for i, f := range rommCloud.firmware {
			if f.Missing {
				continue
			}
			name := rommServerSafeName(f.FileName)
			installed := name != "" && y+rowH >= nav.scroll && y <= nav.scroll+body.H && regularFile(filepath.Join(targets.BIOS, name))
			pill := "Get"
			tone := tbxToneAccent
			if installed {
				pill, tone = "On card", tbxToneGood
			}
			rommCloudDrawRow(renderer, config, fonts, body, y, rowH, rowNum, "bios"+strconv.Itoa(i), tbxRowSpec{icon: "key", title: f.FileName, subtitle: "Download from your RomM server to BIOS", pill: pill, tone: tone})
			y += rowH + 5
			rowNum++
		}
	} else {
		for i, g := range rommCloud.games {
			if g.Missing || g.Physical {
				continue
			}
			installed := y+rowH >= nav.scroll && y <= nav.scroll+body.H && regularFile(rommCloudLocalGamePathAt(g, targets.Roms))
			pill, tone := "Get", tbxToneAccent
			if installed {
				pill, tone = "On card", tbxToneGood
			}
			rommCloudDrawRow(renderer, config, fonts, body, y, rowH, rowNum, "game"+strconv.Itoa(i), tbxRowSpec{icon: "game", title: g.title(), subtitle: g.PlatformName + " · " + g.FSName, pill: pill, tone: tone})
			y += rowH + 5
			rowNum++
		}
		if rommCloud.total > len(rommCloud.games) {
			rommCloudDrawRow(renderer, config, fonts, body, y, rowH, rowNum, "more", tbxRowSpec{icon: "more", title: "Load more games", subtitle: fmt.Sprintf("%d of %d shown", len(rommCloud.games), rommCloud.total)})
			y += rowH + 5
		}
	}
	if y < body.H {
		nav.extend(y)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func rommCloudDrawRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, y, h int32, row int, id string, spec tbxRowSpec) {
	nav := &rommCloud.nav
	rect := sdl.Rect{X: body.X, Y: body.Y + y - nav.scroll, W: body.W, H: h}
	nav.add(id, sdl.Rect{X: body.X, Y: y, W: body.W, H: h}, row, false)
	if rect.Y+rect.H >= body.Y && rect.Y <= body.Y+body.H {
		tbxDrawRow(renderer, config, fonts, rect, spec, nav.focused(id))
	}
}

func rommCloudLocalGamePath(g rommServerGame) string {
	_, targets := rommDefaultRoots()
	return rommCloudLocalGamePathAt(g, targets.Roms)
}

func rommCloudLocalGamePathAt(g rommServerGame, romsRoot string) string {
	system, ok := rommServerSystem(g)
	name := rommServerSafeName(g.FSName)
	if !ok || name == "" {
		return ""
	}
	if g.HasMultipleFiles {
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		return rommServerPrimaryGame(filepath.Join(romsRoot, system.Folder, stem))
	}
	return filepath.Join(romsRoot, system.Folder, name)
}

func rommCloudAction(config *Config, act tbxAction) bool {
	if act == tbxActX {
		rommCloud.mode = (rommCloud.mode + 1) % 3
		rommCloud.nav.setFocus("mode" + strconv.Itoa(rommCloud.mode))
		if (rommCloud.mode == rommCloudFirmware && rommCloud.firmware == nil) || (rommCloud.mode == rommCloudGames && rommCloud.games == nil) {
			rommCloudRefresh()
		}
		return true
	}
	if act == tbxActY {
		rommCloud.games, rommCloud.firmware = nil, nil
		rommCloud.total = 0
		rommCloudRefresh()
		return true
	}
	if act == tbxActConfirm {
		rommCloudActivate(config, rommCloud.nav.focus)
		return true
	}
	return tbxNavAction(&rommCloud.nav, act)
}

func rommCloudActivate(config *Config, id string) {
	if rommCloud.busy {
		ShowToast("RomM is still working", ToastKindInfo)
		return
	}
	if strings.HasPrefix(id, "mode") {
		mode, err := strconv.Atoi(strings.TrimPrefix(id, "mode"))
		if err == nil && mode >= 0 && mode < 3 {
			rommCloud.mode = mode
			if (mode == rommCloudGames && rommCloud.games == nil) || (mode == rommCloudFirmware && rommCloud.firmware == nil) {
				rommCloudRefresh()
			}
		}
		return
	}
	switch id {
	case "address":
		rommCloud.editToken = false
		vkOpenRommServer(rommCloud.settings.URL)
		return
	case "token":
		rommCloud.editToken = true
		vkOpenRommServer("")
		return
	case "connect":
		rommCloud.mode = rommCloudGames
		rommCloud.games, rommCloud.firmware = nil, nil
		rommCloudRefresh()
		return
	case "more":
		rommCloudRefresh()
		return
	}
	if strings.HasPrefix(id, "bios") {
		i, err := strconv.Atoi(strings.TrimPrefix(id, "bios"))
		if err == nil && i >= 0 && i < len(rommCloud.firmware) {
			rommCloudDownloadFirmware(rommCloud.firmware[i])
		}
		return
	}
	if strings.HasPrefix(id, "game") {
		i, err := strconv.Atoi(strings.TrimPrefix(id, "game"))
		if err == nil && i >= 0 && i < len(rommCloud.games) {
			rommCloudGameSheet(config, rommCloud.games[i])
		}
	}
}

func rommCloudGameSheet(config *Config, game rommServerGame) {
	if _, ok := rommServerSystem(game); !ok {
		ShowToast("This console has no matching Brick games folder", ToastKindWarning)
		return
	}
	path := rommCloudLocalGamePath(game)
	buttons := []tbxSheetButton{{label: "Cancel"}}
	if path == "" || !regularFile(path) {
		buttons = append(buttons, tbxSheetButton{label: "Get game + cover", primary: true, run: func() { rommCloudDownloadGame(game) }})
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Get cover", run: func() { rommCloudDownloadCover(game, path) }})
		buttons = append(buttons, tbxSheetButton{label: "Sync saves", primary: true, run: func() { rommCloudSyncGame(game, path) }})
	}
	text := game.PlatformName + " · " + game.FSName + "\n\n"
	text += "Download the game and its cover to the card, or sync its RetroArch saves with RomM. Multi-file games are imported together."
	tbxOpenSheet(config, tbxSheetSpec{title: game.title(), subtitle: "Your RomM library", icon: "game", body: text, buttons: buttons, focus: len(buttons) - 1})
}

func rommCloudRun(label string, work func(context.Context, *rommServerClient) error) {
	client, err := newRommServerClient(rommCloud.settings)
	if err != nil {
		ShowToast("RomM: "+err.Error(), ToastKindWarning)
		return
	}
	rommCloud.busy = true
	rommCloud.note = label + "…"
	rommCloud.gen++
	gen := rommCloud.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := work(ctx, client)
		tbxPost(func() {
			if gen != rommCloud.gen {
				return
			}
			rommCloud.busy = false
			rommCloud.settings = rommServerSettingsGet()
			if err != nil {
				rommCloud.note = firstLine(err.Error())
				ShowToast("RomM: "+rommCloud.note, ToastKindWarning)
				return
			}
			rommCloud.note = label + " done"
			ShowToast(rommCloud.note, ToastKindSuccess)
			if gameLib.open {
				refreshGameLibrary()
			}
		})
	}()
}

func rommCloudDownloadGame(g rommServerGame) {
	rommCloudRun("Game download", func(ctx context.Context, client *rommServerClient) error {
		_, targets := rommDefaultRoots()
		path, err := client.downloadGame(ctx, g, targets.Roms)
		if err != nil {
			return err
		}
		if g.CoverSmall != "" || g.CoverLarge != "" {
			_, err = client.downloadCover(ctx, g, path, libraryArtworkRoot())
		}
		return err
	})
}

func rommCloudDownloadCover(g rommServerGame, path string) {
	rommCloudRun("Cover download", func(ctx context.Context, client *rommServerClient) error {
		_, err := client.downloadCover(ctx, g, path, libraryArtworkRoot())
		return err
	})
}

func rommCloudDownloadFirmware(f rommServerFirmware) {
	rommCloudRun("BIOS download", func(ctx context.Context, client *rommServerClient) error {
		_, targets := rommDefaultRoots()
		_, err := client.downloadFirmware(ctx, f, targets.BIOS)
		return err
	})
}

func rommCloudSaveRoots() []saveRoot {
	_, targets := rommDefaultRoots()
	card := filepath.Dir(targets.Roms)
	var roots []saveRoot
	haveSave, haveState := false, false
	cfgText, cfgPath, _ := retroArchConfig()
	add := func(root saveRoot) {
		if root.Path == "" {
			return
		}
		for _, old := range roots {
			if old.Name == root.Name && strings.EqualFold(filepath.Clean(old.Path), filepath.Clean(root.Path)) {
				return
			}
		}
		roots = append(roots, root)
		if root.Name == "RetroArch saves" {
			haveSave = true
		} else if root.Name == "RetroArch states" {
			haveState = true
		}
	}
	for _, setting := range []struct{ key, name string }{{"savefile_directory", "RetroArch saves"}, {"savestate_directory", "RetroArch states"}} {
		if value, ok := retroArchConfigValue(cfgText, setting.key); ok {
			add(saveRoot{Name: setting.name, Path: resolveRetroArchSaveDir(value, cfgPath)})
		}
	}
	for _, root := range saveRootsFor(card, cfgText, cfgPath) {
		switch root.Name {
		case "RetroArch saves":
		case "RetroArch states":
		default:
			continue
		}
		add(root)
	}
	if !haveSave {
		add(saveRoot{Name: "RetroArch saves", Path: filepath.Join(card, "RetroArch", "saves")})
	}
	if !haveState {
		add(saveRoot{Name: "RetroArch states", Path: filepath.Join(card, "RetroArch", "states")})
	}
	return roots
}

func rommCloudSyncGame(g rommServerGame, romPath string) {
	settings := rommCloud.settings
	rommCloudRun("Save sync", func(ctx context.Context, client *rommServerClient) error {
		result, err := client.syncSavesFromRoots(ctx, settings, g, romPath, rommCloudSaveRoots())
		if err != nil {
			return err
		}
		tbxPost(func() {
			ShowToast(fmt.Sprintf("Saves: %d sent, %d received, %d conflicts", result.Uploaded, result.Downloaded, result.Conflicts), ToastKindSuccess)
		})
		return nil
	})
}

func rommCloudClick(mx, my int32, config *Config) bool {
	if item, ok := rommCloud.nav.hit(mx, my); ok {
		rommCloud.nav.setFocus(item.id)
		rommCloudActivate(config, item.id)
	}
	return true
}

func rommCloudHints() []FooterHint {
	return []FooterHint{{Button: "A", Label: "Open"}, {Button: "X", Label: "Next tab"}, {Button: "Y", Label: "Refresh"}, {Button: "B", Label: "Back"}}
}
