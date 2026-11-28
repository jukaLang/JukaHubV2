package main

// INP-2 — sentences that name buttons.
//
// The hint layer (inputhints.go) decides whether the UI names the pad's
// buttons or the keyboard's keys. A sentence written straight into a Go string
// ("Press A again to delete …") bypasses it: on a PC the badge above it reads
// "Enter" while the sentence still says "A". This checker walks every
// non-test source file and fails on a sentence that names A, B, Start or
// Select, except:
//
//   - a chord ("Hold Select + R1"), which is read from the physical buttons;
//   - the documented physical cases below: hakuX's exit chord, the hotseat
//     join prompt, the one-handed help, the pad-only remap and controller
//     test words, and boot-time presses;
//   - badge text that goes through hintKeyFor when it is drawn.
//
// The X and Y buttons are left out on purpose: they are letter keys on a
// keyboard, so a sentence that names them is true in both vocabularies.

import (
	"go/ast"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// hintSentencePadButtonRE matches a sentence that names a pad button. A and B
// are case-sensitive (a lowercase "press a" is prose, not the button); Start
// and Select are not.
var hintSentencePadButtonRE = regexp.MustCompile(
	`\b(?:[Pp]ress|[Hh]old)\s+(?:A|B)\b|\b(?:[Pp]ress|[Hh]old)\s+(?:[Ss]tart|[Ss]elect)\b`)

// hintSentenceAllowedExact are literals that name a physical control whole:
// a badge the footer translates when it draws, or a name that only looks like
// a button.
var hintSentenceAllowedExact = map[string]string{
	"Hold A":         "a footer badge: hintKeyFor translates it when it is drawn",
	"Hold B":         "a footer badge: hintKeyFor translates it when it is drawn",
	"Press Start":    "a trophy's name",
	"Press Start 2P": "a font's name, not a button",
	"Press A on a":   "hotseat's join prompt is pad-only; the next line finishes the sentence",
	"Hold A for accents  ·  L1+X copy  ·  L1+Y paste": "the keyboard tip has its own line (vkui.go)",
	"Hold Select to close the controller test.":       "the controller test reads the physical buttons",
}

// hintSentenceAllowedPart are literals that contain a physical sentence inside
// a larger template: the phone's page, the boot-time presses, the pad-only
// remap words and the profile help.
var hintSentenceAllowedPart = map[string]string{
	"press A, B, X or Y with one thumb":                 "the one-handed help names the pad's buttons",
	"you press B there":                                 "the phone's page describes the handheld's pad",
	"Press B on the handheld":                           "the phone's page describes the handheld's pad",
	"hold Select and press R1 switches profiles":        "the remap diagnostics name the pad chord",
	"hold Select and press L1 is mouse mode":            "the remap diagnostics name the pad chord",
	"hold Select and press R1 to switch":                "the remap screen names its pad chord",
	"Hold Select and press R1 to switch":                "the remap screen names its pad chord",
	"Hold Select and press R1 to step through profiles": "the remap settings row names its pad chord",
	"Hold Start for Quick Settings, hold B for Home":    "a pad profile's description",
	"hold L1 and press A.":                              "a pad profile's button notation",
	"Hold Select or B through the boot animation":       "a press read before JukaHub starts",
	"hold B at power-on":                                "a press read before JukaHub starts",
	"Copy <version> from this PC":                       "the PC-side note describes the handheld's pad",
	"hold Start for Quick Settings":                     "the plugin help describes the pad's buttons",
}

func TestNoUserSentenceNamesAPadButton(t *testing.T) {
	var bad []string
	for name, file := range packageSources(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			loc := hintSentencePadButtonRE.FindStringIndex(s)
			if loc == nil {
				return true
			}
			// A chord is read from the physical buttons, so its words stay.
			if strings.HasPrefix(strings.TrimLeft(s[loc[1]:], " "), "+") {
				return true
			}
			if _, ok := hintSentenceAllowedExact[s]; ok {
				return true
			}
			for part := range hintSentenceAllowedPart {
				if strings.Contains(s, part) {
					return true
				}
			}
			bad = append(bad, name+": "+s)
			return true
		})
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Fatalf("%d sentence(s) name a pad button outside the hint layer; "+
			"use hintSentence or rewrite the sentence:\n%s", len(bad), strings.Join(bad, "\n"))
	}
}
