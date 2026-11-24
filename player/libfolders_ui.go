package main

// libfolders_ui.go — the Library folders screen (Settings → Games &
// emulators; libfolders.go builds the map): where the games, BIOS files,
// saves and states live and who reads each folder; "Share BIOS files" when
// one BIOS folder lacks what another has; the JukaHub OS layer that makes
// one BIOS folder of them; and the computer side — a handheld's card in a PC
// (libportable.go) and Device Sync over Wi-Fi.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const libFoldersSceneName = "LibraryFolders"

type libFoldersScreenState struct {
	visit   tbxVisit
	nav     tbxNav
	folders []libFolder
	gaps    map[string]int
	loaded  bool
	loading bool
	sharing bool
	// layer is the JukaHub OS layer as it would be built now, layerOn
	// whether it is switched on; both read with the map.
	layer   jukaosLayer
	layerOn bool
}

var libFoldersScreen libFoldersScreenState

type libFoldersScene struct{ sceneBase }

func (libFoldersScene) render(r *sdl.Renderer, c *Config) bool {
	if libFoldersScreen.visit.begin() {
		libFoldersScreen.nav.scroll = 0
		libFoldersScreen.loaded = false
	}
	libFoldersRefresh()
	libFoldersRender(r, c)
	return true
}

func (libFoldersScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), libFoldersAction)
}

func (libFoldersScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), libFoldersAction)
}

func init() { registerScene(libFoldersScene{sceneBase{name: libFoldersSceneName}}) }

// libFoldersSettingsRow is Settings → Games & emulators' door to the screen.
func libFoldersSettingsRow() setRow {
	return setRow{
		ID: "libraryFolders", Label: "Library folders", Kind: setKindAction,
		Detail: setStatic("Where games, BIOS files and saves live, and one set of BIOS files for every emulator"),
		Open:   func(c *Config) { tbxOpenScene(c, libFoldersSceneName) },
	}
}

// libFoldersRefresh reads the map on a goroutine once per visit.
func libFoldersRefresh() {
	s := &libFoldersScreen
	if s.loaded || s.loading {
		return
	}
	s.loading = true
	go func() {
		folders := libFoldersMap()
		gaps := libFoldersBIOSGaps()
		appDir := jukaosAppDir()
		layer := jukaosFoldersLayer(jukaosHostFor(appDir))
		on := jukaosLoadState(appDir).Layers[jukaosFoldersID]
		tbxPost(func() {
			s := &libFoldersScreen
			s.folders, s.gaps, s.loaded, s.loading = folders, gaps, true, false
			s.layer, s.layerOn = layer, on
		})
	}()
}

func libFoldersRender(renderer *sdl.Renderer, config *Config) {
	s := &libFoldersScreen
	header := tbxHeader("Library folders", "Where your games, BIOS files and saves live, whichever emulator reads them", "folder", "", ColorTextMuted())
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &s.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}
	section := func(title, icon string) {
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}
	if !s.loaded {
		drawRow("", tbxRowSpec{icon: "folder", title: "Reading the card…", dim: true})
	}
	kinds := []struct{ kind, title, icon, count string }{
		{"games", "Games", "roms", "console folders"},
		{"bios", "BIOS files", "chip", "files"},
		{"saves", "Saves", "saves", "files"},
		{"states", "Save states", "savestate", "files"},
	}
	totalGaps := 0
	for _, n := range s.gaps {
		totalGaps += n
	}
	for _, k := range kinds {
		first := true
		for i, f := range s.folders {
			if f.Kind != k.kind {
				continue
			}
			if first {
				if y > 0 {
					y += 6
				}
				section(k.title, k.icon)
				first = false
			}
			sub := f.Path + " · " + strconv.Itoa(f.Files) + " " + k.count
			if gap := s.gaps[f.Path]; k.kind == "bios" && gap > 0 {
				sub += " · lacks " + pluralizeCount(gap, "file", "files") + " another BIOS folder has"
			}
			note := ""
			if f.Main {
				note = "Library's"
			}
			drawRow("f:"+strconv.Itoa(i), tbxRowSpec{icon: "folder", title: f.Name, subtitle: sub, note: note, dim: !f.Main})
		}
		if k.kind == "bios" && !first {
			spec := tbxRowSpec{icon: "copy", title: "Share BIOS files",
				subtitle: "Copy each BIOS file one folder has into the others: nothing replaced or deleted", pill: "Share", tone: tbxToneAccent}
			switch {
			case s.sharing:
				spec.pill = "Copying…"
			case totalGaps == 0:
				spec.subtitle, spec.pill, spec.tone = "Every BIOS folder already has the same BIOS files", "Done", tbxToneNeutral
			}
			drawRow("share", spec)
		}
	}
	if s.loaded {
		y += 6
		section("One folder in JukaHub OS", "jukahub")
		layer, on := s.layer, s.layerOn
		spec := tbxRowSpec{icon: "chip", title: "One BIOS folder for every emulator", pill: "Switch on", tone: tbxToneAccent,
			subtitle: "In JukaHub OS, the firmware's emulators read the library's BIOS folder too"}
		if on {
			spec.pill, spec.tone = "On", tbxToneGood
		}
		if layer.Problem != "" {
			spec.subtitle = "Not yet: " + layer.Problem
		} else if len(layer.Notes) > 0 {
			spec.subtitle = pluralizeCount(len(layer.Binds), "folder joins", "folders join") + "; " + layer.Notes[0]
		}
		drawRow("oslayer", spec)
		y += 6
		section("On a computer", "devices")
		if P().IsHandheld() {
			drawRow("pc", tbxRowSpec{icon: "devices", title: "Play from this card on a PC",
				subtitle: "Put this SD card in a computer running JukaHub: its games play there with these saves", dim: true})
		} else {
			pc := portableCardSettingsRow()
			pill := "Off"
			if portableCardSetting(config) {
				pill = "On"
			}
			drawRow("pc", tbxRowSpec{icon: "devices", title: pc.Label, subtitle: pc.Detail(config), pill: pill, tone: tbxToneNeutral})
		}
		drawRow("sync", tbxRowSpec{icon: "sync", title: "Device Sync over Wi-Fi",
			subtitle: "Saves, BIOS files, games, collections and play time with a paired JukaHub", pill: "Open", tone: tbxToneNeutral})
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func libFoldersAction(config *Config, act tbxAction) bool {
	nav := &libFoldersScreen.nav
	switch act {
	case tbxActConfirm:
		libFoldersActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func libFoldersSceneClick(mx, my int32, config *Config) bool {
	it, ok := libFoldersScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	libFoldersScreen.nav.focus = it.id
	libFoldersActivate(config, it.id)
	return true
}

func libFoldersSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch f := libFoldersScreen.nav.focus; {
	case f == "share", f == "oslayer", f == "pc", f == "sync":
		hints = append(hints, FooterHint{Button: "A", Label: "Choose"})
	case strings.HasPrefix(f, "f:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open in Files"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func libFoldersActivate(config *Config, id string) {
	s := &libFoldersScreen
	switch {
	case id == "share":
		if s.sharing {
			return
		}
		s.sharing = true
		PlayActivateSound()
		go func() {
			res := libFoldersShareBIOS()
			tbxPost(func() {
				s := &libFoldersScreen
				s.sharing, s.loaded = false, false
				switch {
				case res.Failed > 0:
					ShowToast("Shared "+pluralizeCount(res.Copied, "BIOS file", "BIOS files")+"; "+strconv.Itoa(res.Failed)+" failed: "+res.First, ToastKindWarning)
				case res.Copied == 0:
					ShowToast("Every BIOS folder already had them", ToastKindInfo)
				default:
					PlaySuccessSound()
					ShowToast("Shared "+pluralizeCount(res.Copied, "BIOS file", "BIOS files")+" ("+humanBytes(res.Bytes)+")", ToastKindSuccess)
				}
			})
		}()
	case id == "oslayer":
		jukaosUI.state = jukaosLoadState(jukaosAppDir())
		on := !jukaosUI.state.Layers[jukaosFoldersID]
		PlayActivateSound()
		jukaosSetLayer(jukaosFoldersID, "One BIOS folder for every emulator", on)
		s.loaded = false
	case id == "pc":
		if P().IsHandheld() {
			return
		}
		PlayToggleSound()
		portableCardSettingsRow().Set(config, !portableCardSetting(config))
		s.loaded = false
	case id == "sync":
		PlayActivateSound()
		ssyncOpenScreen(config)
	case strings.HasPrefix(id, "f:"):
		i, err := strconv.Atoi(id[2:])
		if err != nil || i < 0 || i >= len(s.folders) {
			return
		}
		PlayActivateSound()
		usbOtgOpenInFiles(config, s.folders[i].Path)
	}
}
