package main

// ctrllayout.go — named control layouts a game can be played with: "Arcade
// 6-button", "DS stylus", "DOS keyboard", "ScummVM point and click".
//
// A layout is a whole way of holding the pad for one kind of game: which
// RetroPad button each pad button sends, which keyboard key it types for a
// core that reads a keyboard (DOSBox Pure, ScummVM), the core options that
// go with it (a DS core's touch mode), and the chart the controls card shows.
// Layouts come from three places: the built-in ones, the features that know a
// console best (the DOS and ScummVM shelves register theirs from init), and
// the player's own files.
//
// This file is the registry every one of those shares. The per-game choice,
// the launch-time application and the picker live beside it (ctrllayout_*.go):
// ctrllayout_builtin.go (the arcade and DS layouts), ctrllayout_store.go (the
// choices and the player's own layout files), ctrllayout_apply.go (what a
// launch does) and ctrllayout_ui.go (the picker, the More row and the chip).

import (
	"sort"
	"strings"
	"sync"
)

// ctrlLayoutLine is one line of a layout's chart: "A — Left click".
type ctrlLayoutLine struct {
	Button string // "A", "Left stick", "L1 + R1"
	Action string // "Left click", "Jump"
}

// ctrlLayout is one named layout.
type ctrlLayout struct {
	// ID is the stable key stored per game ("arcade-6", "dos-keyboard").
	ID          string
	Name        string
	Description string
	// Systems are the library systems the layout is offered for (library
	// system names: "Arcade", "DOS", "Nintendo DS"); empty offers it for every
	// system.
	Systems []string
	// DefaultFor are the systems whose games use this layout until the player
	// picks another for the game.
	DefaultFor []string
	// Buttons remaps RetroPad buttons for the launch: pad button → the RetroPad
	// button the core is handed, both named as raRemapButtonNames names them
	// ("a", "b", "x", "y", "l", "r", "l2", "r2", "l3", "r3", "start",
	// "select", "up", "down", "left", "right"); "none" sends nothing, and a
	// stick direction ("lstick-up" … "rstick-right") pushes that stick all
	// the way (a DS stylus moved with the D-pad).
	Buttons map[string]string
	// Keys maps pad buttons (same names) to keyboard keys, for cores that read
	// a keyboard, by RetroArch's key names ("enter", "escape", "space", "f1",
	// "y", "up" …).
	Keys map[string]string
	// CoreOptions are libretro core options set for the launch only.
	CoreOptions map[string]string
	// Chart is what the controls card and the manual's Controls page show.
	Chart []ctrlLayoutLine
	// Variants are what changes for some cores or devices: a core that hands
	// the arcade buttons out its own way, a DS core's own touch option, a
	// handheld without sticks. Every variant that matches a launch is laid
	// over the layout in order (ctrlLayoutResolve, ctrllayout_apply.go).
	Variants []ctrlLayoutVariant
	// Source is "builtin", "user", or the id of the feature that registered
	// it ("dos-shelf", "scummvm-shelf").
	Source string
}

// ctrlLayoutVariant is the part of a layout that applies only to some cores,
// or only on a handheld without analog sticks (the Brick family). Its Buttons
// and CoreOptions are laid over the layout's key by key; its Chart, when it
// has one, replaces the layout's.
type ctrlLayoutVariant struct {
	// Cores are core names as gameCoreFromLauncher gives them ("fbneo",
	// "mame2003_plus", "melondsds"); empty matches every core.
	Cores []string
	// NoSticks limits the variant to a device without analog sticks.
	NoSticks    bool
	Buttons     map[string]string
	CoreOptions map[string]string
	Chart       []ctrlLayoutLine
}

// ctrlLayoutReg is the registry. Registration happens from init() and the
// user's files load on a goroutine, so it is guarded.
var ctrlLayoutReg struct {
	mu      sync.RWMutex
	layouts []ctrlLayout
}

// ctrlLayoutRegister adds a layout, or replaces the one with the same ID.
func ctrlLayoutRegister(l ctrlLayout) {
	l.ID = strings.TrimSpace(l.ID)
	if l.ID == "" || strings.TrimSpace(l.Name) == "" {
		return
	}
	ctrlLayoutReg.mu.Lock()
	defer ctrlLayoutReg.mu.Unlock()
	for i := range ctrlLayoutReg.layouts {
		if ctrlLayoutReg.layouts[i].ID == l.ID {
			ctrlLayoutReg.layouts[i] = l
			return
		}
	}
	ctrlLayoutReg.layouts = append(ctrlLayoutReg.layouts, l)
}

// ctrlLayoutByID finds a layout by its ID.
func ctrlLayoutByID(id string) (ctrlLayout, bool) {
	ctrlLayoutReg.mu.RLock()
	defer ctrlLayoutReg.mu.RUnlock()
	for _, l := range ctrlLayoutReg.layouts {
		if l.ID == id {
			return l, true
		}
	}
	return ctrlLayout{}, false
}

// ctrlLayoutSystemIn reports whether system is in list (case-insensitive).
func ctrlLayoutSystemIn(system string, list []string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(system)) {
			return true
		}
	}
	return false
}

// ctrlLayoutsFor lists the layouts offered for system's games: the ones made
// for it first, then the ones for every system, each group by name.
func ctrlLayoutsFor(system string) []ctrlLayout {
	ctrlLayoutReg.mu.RLock()
	var own, general []ctrlLayout
	for _, l := range ctrlLayoutReg.layouts {
		switch {
		case len(l.Systems) == 0:
			general = append(general, l)
		case ctrlLayoutSystemIn(system, l.Systems):
			own = append(own, l)
		}
	}
	ctrlLayoutReg.mu.RUnlock()
	byName := func(list []ctrlLayout) {
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	}
	byName(own)
	byName(general)
	return append(own, general...)
}

// ctrlLayoutDefaultFor is the layout system's games start with, if any.
func ctrlLayoutDefaultFor(system string) (ctrlLayout, bool) {
	ctrlLayoutReg.mu.RLock()
	defer ctrlLayoutReg.mu.RUnlock()
	for _, l := range ctrlLayoutReg.layouts {
		if ctrlLayoutSystemIn(system, l.DefaultFor) {
			return l, true
		}
	}
	return ctrlLayout{}, false
}
