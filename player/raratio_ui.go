package main

// raratio_ui.go — where the aspect ratio and hard GPU sync (raratio.go) are
// chosen.
//
// Settings → Emulation has two rows under Crop overscan that follow the
// page's "Adjust for" picker (ratuning_settings.go): Aspect ratio and Hard
// GPU sync, for All consoles or the picked console, a console's rows
// starting at "Default · …". The game options sheet has an Aspect ratio row
// for one game: Left/Right step through "Default · <the console's>" and the
// shapes, A steps and wraps; after a launch that could not apply the shape,
// the row's line says why.
//
// When the RetroArch config on the card was saved by a build older than 1.9
// (or never saved), only 4:3 and 16:9 are offered: the other shapes' places
// in RetroArch's table are not known for it (raratio.go). The config is read
// in the background, so opening the page never waits on the card.

import (
	"os"
	"sync"
	"time"
)

// raRatioProbe is what the default RetroArch config says, for the rows:
// found (a config is there) and modern (the 1.9+ table applies).
var raRatioProbe struct {
	mu      sync.Mutex
	at      time.Time
	running bool
	done    bool
	found   bool
	modern  bool
}

// raRatioLimited reports whether the rows offer only the safe shapes: a
// config is there and an older build saved it. While the first read runs,
// everything is offered.
func raRatioLimited() bool {
	p := &raRatioProbe
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running && (p.at.IsZero() || time.Since(p.at) >= 30*time.Second) {
		p.running = true
		p.at = time.Now()
		go func() {
			found, modern := false, false
			if targets := raScopeTargets(raRatioCandidates()); len(targets) > 0 {
				found = true
				if raw, err := os.ReadFile(targets[0]); err == nil {
					modern = raRatioModern(string(raw))
				}
			}
			p.mu.Lock()
			p.found, p.modern, p.done, p.running = found, modern, true, false
			p.mu.Unlock()
		}()
	}
	return p.done && p.found && !p.modern
}

// raRatioOffered reports whether a shape is offered now.
func raRatioOffered(id string) bool {
	c, ok := raRatioChoiceByID(id)
	return ok && (c.Safe || !raRatioLimited())
}

// raRatioLevelNow is the Emulation page's level (its "Adjust for" console).
func raRatioLevelNow() raLevel { return raLevel{Console: raTuneScope} }

// raRatioInherited is what "Default" means at a level.
func raRatioInherited(l raLevel, field string) string {
	var v string
	switch {
	case l.Game != "":
		v, _ = raRatioStore.resolve(l.Console, "", field)
	case l.Console != "":
		v, _ = raRatioStore.resolve("", "", field)
	}
	return v
}

// raRatioStoredAt is a level's own value as the rows show it: All
// consoles' nothing is RetroArch's own.
func raRatioStoredAt(l raLevel, field string) string {
	v := raRatioStore.field(l, field)
	if v == "" && l.Console == "" && l.Game == "" {
		return raRatioOwn
	}
	return v
}

// raRatioOptions are a level's shapes: Default below All consoles, RetroArch's
// own, then the shapes offered now.
func raRatioOptions(l raLevel) []setOption {
	out := make([]setOption, 0, len(raRatioChoices)+2)
	if l.Console != "" || l.Game != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + raRatioLabel(raRatioInherited(l, raRatioField)), Note: raTuneDefaultNote})
	}
	out = append(out, setOption{Value: raRatioOwn, Label: "RetroArch's own", Note: "Leave RetroArch's aspect ratio alone"})
	for _, c := range raRatioChoices {
		if raRatioOffered(c.ID) {
			out = append(out, setOption{Value: c.ID, Label: c.Label, Note: c.Note})
		}
	}
	return out
}

func raHardSyncOptions(l raLevel) []setOption {
	out := make([]setOption, 0, 4)
	if l.Console != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + raHardSyncLabel(raRatioInherited(l, raHardSyncField)), Note: raTuneDefaultNote})
	}
	return append(out,
		setOption{Value: raRatioOwn, Label: "RetroArch's own", Note: "Leave RetroArch's setting alone"},
		setOption{Value: "on", Label: "On", Note: "Less delay between a press and the picture; costs some CPU"},
		setOption{Value: "off", Label: "Off", Note: "RetroArch's usual pacing"})
}

// raRatioSet stores one value; All consoles' RetroArch's own is nothing
// stored.
func raRatioSet(l raLevel, field, value string) {
	if l.Console == "" && l.Game == "" && value == raRatioOwn {
		value = ""
	}
	raRatioStore.set(l, field, value)
}

// raRatioSettingsRows are the Emulation page's rows.
func raRatioSettingsRows() []setRow {
	return []setRow{
		{
			ID: "raRatio", Label: "Aspect ratio", Kind: setKindChoice,
			Detail: func(*Config) string {
				what := "The picture's shape in RetroArch games · a game can choose its own (Start in the library)"
				if raTuneScope != "" {
					what = raTuneScope + " games only"
				}
				if raRatioLimited() {
					what += " · this RetroArch takes only 4:3 and 16:9"
				}
				return what
			},
			Options:    func(*Config) []setOption { return raRatioOptions(raRatioLevelNow()) },
			Sheet:      func(*Config) []setOption { return raRatioOptions(raRatioLevelNow()) },
			SheetValue: func(*Config) string { return raRatioStoredAt(raRatioLevelNow(), raRatioField) },
			SheetTitle: "Aspect ratio",
			Current: func(*Config) int {
				return raTuneOptionIndex(raRatioOptions(raRatioLevelNow()), raRatioStoredAt(raRatioLevelNow(), raRatioField))
			},
			Value: func(*Config) string {
				l := raRatioLevelNow()
				v := raRatioStoredAt(l, raRatioField)
				if v == "" {
					return "Default · " + raRatioLabel(raRatioInherited(l, raRatioField))
				}
				if v != raRatioOwn && !raRatioOffered(v) {
					return raRatioLabel(v) + " · needs RetroArch 1.9"
				}
				return raRatioLabel(v)
			},
			Choose: func(_ *Config, value string) {
				value = normaliseRARatio(raRatioField, value)
				raRatioSet(raRatioLevelNow(), raRatioField, value)
				label := raRatioLabel(value)
				if value == "" {
					label = "Default"
				}
				raTuneScopedToast("Aspect ratio", label)
			},
		},
		{
			ID: "raHardSync", Label: "Hard GPU sync", Kind: setKindChoice,
			Detail: func(*Config) string {
				if raTuneScope != "" {
					return raTuneScope + " games only"
				}
				return "Less input lag with RetroArch's gl video driver, for some CPU · V-sync is Smooth motion's"
			},
			Options: func(*Config) []setOption { return raHardSyncOptions(raRatioLevelNow()) },
			Current: func(*Config) int {
				return raTuneOptionIndex(raHardSyncOptions(raRatioLevelNow()), raRatioStoredAt(raRatioLevelNow(), raHardSyncField))
			},
			Choose: func(_ *Config, value string) {
				value = normaliseRARatio(raHardSyncField, value)
				raRatioSet(raRatioLevelNow(), raHardSyncField, value)
				label := raHardSyncLabel(value)
				if value == "" {
					label = "Default"
				}
				raTuneScopedToast("Hard GPU sync", label)
			},
		},
	}
}

// ── Game options sheet ───────────────────────────────────────────────────

func raRatioGameLevel(game libraryGame) raLevel {
	return raLevel{Console: gameControlsConsole(game.System), Game: game.Path}
}

// raRatioSheetStep moves the game's shape; it reports whether it changed.
func raRatioSheetStep(game libraryGame, delta int, wrap bool) bool {
	l := raRatioGameLevel(game)
	opts := raRatioOptions(l)
	ladder := make([]string, 0, len(opts))
	for _, o := range opts {
		ladder = append(ladder, o.Value)
	}
	cur := raRatioStore.field(l, raRatioField)
	next := gameControlsStep(ladder, cur, delta, wrap)
	if next == cur {
		return false
	}
	raRatioSet(l, raRatioField, next)
	return true
}

// raRatioSheetValue is the sheet row's value.
func raRatioSheetValue(game libraryGame) string {
	l := raRatioGameLevel(game)
	if v := raRatioStore.field(l, raRatioField); v != "" {
		return raRatioLabel(v)
	}
	return "Default · " + raRatioLabel(raRatioInherited(l, raRatioField))
}

// raRatioSheetOwn reports the game's own choice.
func raRatioSheetOwn(game libraryGame) bool {
	return raRatioStore.field(raRatioGameLevel(game), raRatioField) != ""
}

// raRatioSheetHint is the row's second line.
func raRatioSheetHint(game libraryGame) string {
	if note := raRatioStore.note(game.Path); note != "" {
		return note
	}
	return "The picture's shape: 4:3, 16:9, full screen… · RetroArch games, this game only"
}
