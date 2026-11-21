package main

// romm_extras.go — the ROM manager's "More for your games" rows.
//
// Under the plan's summary card the ROM manager (romm_ui.go) lists the other
// things that put games, saves and BIOS where they belong, one press each:
//
//   - "Move N Color games out of Game Boy" — only when the Game Boy shelf has
//     cartridges whose header says Game Boy Color (gbcsort.go). It asks once,
//     with the games listed, then moves them with their saves, states, play
//     time, covers and options.
//   - "Combine multi-disc games" — runs the disc helpers over every disc
//     console now (disctools.go): loose "(Disc 1)", "(Disc 2)" files and
//     "Disc 1" folders become one game. The library also does this by
//     itself when it scans; this is the button for "do it now".
//   - "Make missing .cue files" — the CUE generator (cuegen.go): a
//     sheet for every .bin and split "(Track N)" rip that has none, no
//     playlists.
//   - "RomM server" — the RomM page (romm_cloud_ui.go): games, covers, saves
//     and BIOS from the user's own RomM server. Without an address and a key
//     the row says to set it up first, and the page opens on its settings.
//   - "Syncthing" — the Save Manager's Syncthing page (syncthing_ui.go),
//     which keeps the save folders the same on every paired device. Off or
//     not installed, the row says to set it up first.
//   - "Get Homebrew" — the homebrew store (homebrew_store.go).
//
// The scan that builds the plan also looks for the Color games, so the rows
// cost nothing per frame beyond the two status reads (both cached).

import (
	"errors"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// rommExtras is what the last scan found for the rows. Main thread.
var rommExtras struct {
	gbc  []gbcCandidate
	stay map[string]bool
}

// rommExtraRow is one row: its focus id and what it shows.
type rommExtraRow struct {
	id   string
	spec tbxRowSpec
}

// rommExtraRows are the rows as they stand now.
func rommExtraRows() []rommExtraRow {
	var rows []rommExtraRow
	if n := len(rommExtras.gbc); n > 0 {
		spec := tbxRowSpec{icon: "emu-gbc", title: "Move " + pluralizeCount(n, "Color game", "Color games") + " out of Game Boy",
			subtitle: "Their cartridges say Game Boy Color · saves, play time and covers go with them",
			pill:     "Move", tone: tbxToneAccent}
		if gbcMoveBusy.Load() {
			spec.pill, spec.tone = "Moving…", tbxToneNeutral
		}
		rows = append(rows, rommExtraRow{id: "x:gbc", spec: spec})
	}
	disc := tbxRowSpec{icon: "disc", title: "Combine multi-disc games",
		subtitle: "Loose (Disc 1), (Disc 2) files and Disc folders become one game", pill: "Run", tone: tbxToneNeutral}
	if discCombineBusy.Load() {
		disc.pill = "Working…"
	}
	rows = append(rows, rommExtraRow{id: "x:disc", spec: disc})
	rows = append(rows, cueGenRommRow()) // "Make missing .cue files" (cuegen.go)

	romm := tbxRowSpec{icon: "server", title: "RomM server", subtitle: "Games, saves and BIOS from your own RomM server",
		pill: "Open", tone: tbxToneNeutral}
	if s := rommServerSettingsGet(); s.URL == "" || s.Token == "" {
		romm.subtitle, romm.pill, romm.tone = "Set it up first: your server's address and an API key", "Set up", tbxToneWarn
	} else if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
		romm.pill = u.Host
	}
	rows = append(rows, rommExtraRow{id: "x:romm", spec: romm})

	st := tbxRowSpec{icon: "sync", title: "Syncthing", subtitle: "Keeps your saves the same on all your devices", tone: tbxToneNeutral}
	switch status := rommSyncthingStatus(); status {
	case "Off", "Not installed":
		st.subtitle, st.pill, st.tone = "Set it up first: switch it on there and pair your other device", "Set up", tbxToneWarn
	default:
		st.pill = status
	}
	rows = append(rows, rommExtraRow{id: "x:sync", spec: st})

	rows = append(rows, rommExtraRow{id: "x:homebrew", spec: tbxRowSpec{icon: "store", title: "Get Homebrew",
		subtitle: "Free new games made for old consoles", pill: "Open", tone: tbxToneNeutral}})
	return rows
}

// rommSyncthingStatus is the Syncthing row's state without starting
// Syncthing's worker: it runs from startup when Syncthing is on, and when it
// is off there is nothing to ask it.
func rommSyncthingStatus() string {
	if !syncthingEnabled(appConfig) {
		return "Off"
	}
	return syncthingStatusText(syncthingSnapshotNow(), true)
}

// rommDrawExtras draws the rows under a caption at content y, adding them to
// the focus list from row on. It returns the y and row after them.
func rommDrawExtras(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32, row int) (int32, int) {
	rows := rommExtraRows()
	if len(rows) == 0 {
		return y, row
	}
	nav := &rommUI.nav
	y += 18
	if rommOnScreen(top+y, fonts.smallH, body) {
		renderText(renderer, config, fonts.small, "More for your games", ColorTextTertiary(), frame.x, top+y)
	}
	y += fonts.smallH + 8
	h := tbxRowHeight(fonts)
	for _, r := range rows {
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: h}
		if rommOnScreen(top+y, h, body) {
			screen := rect
			screen.Y += top
			tbxDrawRow(renderer, config, fonts, screen, r.spec, nav.focused(r.id))
		}
		nav.add(r.id, rect, row, false)
		row++
		y += h + 8
	}
	return y, row
}

// rommExtrasLabel is what A does on a row, "" when id is none of them.
func rommExtrasLabel(id string) string {
	switch id {
	case "x:gbc":
		return "Move"
	case "x:disc":
		return "Combine"
	case "x:cue":
		return "Make"
	case "x:romm", "x:sync", "x:homebrew":
		return "Open"
	}
	return ""
}

// rommExtrasActivate presses a row. It reports whether id was one.
func rommExtrasActivate(config *Config, id string) bool {
	switch id {
	case "x:gbc":
		rommConfirmGBC(config)
	case "x:cue":
		cueGenRommRun() // cuegen.go
	case "x:disc":
		err := discCombineNow(func(r discHelperReport) {
			if r.Created() > 0 && tbxCurrentScene(appConfig) == rommSceneName && rommUI.phase == rommPhaseReady {
				rommStartScan()
			}
		})
		if errors.Is(err, errDiscCombineBusy) {
			ShowToast("Still combining disc games…", ToastKindInfo)
			return true
		}
		PlayActivateSound()
	case "x:romm":
		PlayActivateSound()
		rommCloudOpen(config)
	case "x:sync":
		// The Save Manager's Syncthing page: switching it on, pairing and
		// the folders are all there (syncthing_ui.go).
		openSaveManagerPanel()
		syncthingPanelEnter()
	case "x:homebrew":
		PlayActivateSound()
		homebrewStoreOpen(config)
	default:
		return false
	}
	return true
}

// rommConfirmGBC asks before the Color games move, listing them.
func rommConfirmGBC(config *Config) {
	cands := rommExtras.gbc
	if len(cands) == 0 {
		return
	}
	if gbcMoveBusy.Load() {
		ShowToast("Still moving the Color games…", ToastKindInfo)
		return
	}
	targets := rommUI.plan.Targets
	body := "Their cartridges say they are made for the Game Boy Color. Each one moves to " +
		rommShortPath(filepath.Dir(cands[0].Target), targets) + " with its saves, save states, play time, covers and options. " +
		"Nothing is overwritten: a game whose name is already there stays where it is.\n\n## To move\n"
	for i, c := range cands {
		if i == 6 {
			body += "• … and " + strconv.Itoa(len(cands)-i) + " more\n"
			break
		}
		body += "• " + filepath.Base(c.Path) + "\n"
	}
	n := len(cands)
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Move " + pluralizeCount(n, "game", "games") + " to Game Boy Color?",
		subtitle: "They already play in color from Game Boy",
		icon:     "emu-gbc",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "Move " + strconv.Itoa(n), primary: true, run: rommMoveGBCNow},
		},
		focus: 1,
	})
}

// rommMoveGBCNow moves them, and scans the page again when it is done.
func rommMoveGBCNow() {
	err := gbcMoveAll(rommExtras.gbc, rommExtras.stay, func(int) {
		if tbxCurrentScene(appConfig) == rommSceneName && rommUI.phase != rommPhaseApplying {
			rommStartScan()
		}
	})
	if err != nil {
		ShowToast("Not now · "+err.Error()+" — try again in a moment", ToastKindInfo)
		return
	}
	PlayActivateSound()
}
