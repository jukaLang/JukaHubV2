package main

// artwork_screenscraper_ui.go — the ScreenScraper account rows, its sign-in,
// and Choose cover's LaunchBox search.
//
// Settings → Games & emulators → ScreenScraper account: the user name on the
// shared prompt, then the password on its own masked, submit-only keyboard
// (screenScraperPassVKTarget, like RetroAchievements' sign-in), checked with
// ScreenScraper before it is stored (artwork_screenscraper.go). Sign out of
// ScreenScraper forgets it. A build without the developer key says so and
// asks nothing.
//
// Choose cover (artwork_choose.go) ends with "Search LaunchBox…": A types a
// title (the shared prompt, seeded with the game's), the matches on the
// game's console (or every console when none is) replace the row's previous
// ones above it, the highlighted one's picture is previewed, and A saves the
// game's front box from LaunchBox (artwork_launchbox.go) the way any chosen
// cover is saved.

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// screenScraperPassVKTarget aims the keyboard at the password (masked, submit
// only).
const screenScraperPassVKTarget vkTargetKind = 4127

// screenScraperPassBuffer is the password being typed; cleared when the
// keyboard closes. screenScraperSignInUser is the user name typed first.
// Main thread.
var (
	screenScraperPassBuffer string
	screenScraperSignInUser string
)

// screenScraperAccountRow is Settings → Games & emulators → ScreenScraper
// account, under the box-art rows.
func screenScraperAccountRow() setRow {
	return setRow{
		ID: "screenScraper", Label: "ScreenScraper account", Kind: setKindAction,
		Value: func(*Config) string {
			if !screenScraperAvailable() {
				return "Not in this build"
			}
			if user, ok := screenScraperSignedIn(); ok {
				return user
			}
			return "Sign in"
		},
		Detail: func(*Config) string {
			if !screenScraperAvailable() {
				return "This build has no ScreenScraper developer key, so it cannot sign in"
			}
			if _, ok := screenScraperSignedIn(); ok {
				if note := screenScraperStatusNote(); note != "" {
					return note
				}
				return "Box art, year, genre and synopsis come from ScreenScraper first"
			}
			return "A free screenscraper.fr account: box art, year, genre and synopsis by the game's checksum"
		},
		Open: screenScraperOpenSignIn,
	}
}

// screenScraperSignOutRow forgets the account; shown only while signed in.
func screenScraperSignOutRow() setRow {
	return setRow{
		ID: "screenScraperSignOut", Label: "Sign out of ScreenScraper", Kind: setKindAction,
		Detail: setStatic("Forgets the user name and password on this device"),
		Visible: func(*Config) bool {
			_, ok := screenScraperSignedIn()
			return ok
		},
		Open: func(*Config) {
			if err := screenScraperSaveAccount(screenScraperAccount{}); err != nil {
				ShowToast("Could not sign out: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			logInfo("art", "ScreenScraper: signed out")
			ShowToast("Signed out of ScreenScraper", ToastKindInfo)
		},
	}
}

// screenScraperOpenSignIn is the account row's press: the user name first.
func screenScraperOpenSignIn(*Config) {
	if !screenScraperAvailable() {
		ShowToast("This build has no ScreenScraper developer key, so it cannot sign in", ToastKindInfo)
		return
	}
	seed := screenScraperLoadAccount().User
	tbxOpenPrompt("ScreenScraper user name", "Your user name on screenscraper.fr", seed, func(text string) {
		user := strings.TrimSpace(text)
		if user == "" {
			return
		}
		if len(user) > screenScraperFieldMax || strings.ContainsAny(user, "\r\n\t") {
			ShowToast("That is not a ScreenScraper user name", ToastKindWarning)
			return
		}
		screenScraperSignInUser = user
		// The prompt's keyboard is closing; the password's opens next frame.
		tbxPost(screenScraperOpenPassKeyboard)
	})
}

// screenScraperOpenPassKeyboard opens the masked keyboard for the password.
// It starts empty: no password is ever shown.
func screenScraperOpenPassKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = screenScraperPassVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	screenScraperPassBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// screenScraperPassSubmit takes the typed password (vkSubmit, after the
// keyboard closed), checks the account off the main thread and stores it.
func screenScraperPassSubmit(pass string) {
	user := screenScraperSignInUser
	screenScraperSignInUser = ""
	if user == "" || pass == "" {
		return
	}
	if len(pass) > screenScraperFieldMax {
		ShowToast("That password is too long", ToastKindWarning)
		return
	}
	ShowToast("Checking the ScreenScraper account…", ToastKindInfo)
	go func() {
		acct := screenScraperAccount{User: user, Password: pass}
		// The lock first: a cover lookup holding it must not use up the
		// check's own time.
		screenScraperOne.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), screenScraperTimeout+5*time.Second)
		err := screenScraperVerify(ctx, acct)
		cancel()
		screenScraperOne.Unlock()
		if err == nil {
			err = screenScraperSaveAccount(acct)
		}
		tbxPost(func() {
			if err != nil {
				logWarn("art", "ScreenScraper sign-in not stored: %v", err)
				ShowToast("ScreenScraper sign-in failed: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			logInfo("art", "ScreenScraper: signed in")
			ShowToast("Signed in to ScreenScraper as "+user+" · new covers ask it first", ToastKindSuccess)
		})
	}()
}

// ── Choose cover: Search LaunchBox ──────────────────────────────────────────

// artChooseLaunchBoxRow is the "Search LaunchBox…" row the chooser's list ends
// with.
func artChooseLaunchBoxRow() artChooseCandidate {
	return artChooseCandidate{name: "Search LaunchBox…", search: true}
}

// artChooseSourceLabel is a row's second line.
func artChooseSourceLabel(cand artChooseCandidate) string {
	switch {
	case cand.search:
		return "Type a title · LaunchBox Games Database"
	case cand.lb != nil && cand.lb.Platform != "":
		return "LaunchBox · " + cand.lb.Platform
	case cand.lb != nil:
		return "LaunchBox Games Database"
	case cand.local:
		return "Imgs folder on the card"
	}
	return "Thumbnail server"
}

// artChooseLaunchBoxPreview is a LaunchBox row's preview picture: the
// search's picture of it, else its best front box.
func artChooseLaunchBoxPreview(ctx context.Context, r launchBoxResult, romTags []string) ([]byte, error) {
	if r.Thumb != "" {
		if data, err := launchBoxPicture(ctx, r.Thumb); err == nil || !errors.Is(err, os.ErrNotExist) {
			return data, err
		}
	}
	r.Thumb = ""
	return launchBoxCoverFor(ctx, r, romTags)
}

// artChooseOpenLaunchBoxSearch asks for the title to look up (A on the
// search row).
func artChooseOpenLaunchBoxSearch() {
	if !artChoose.open {
		return
	}
	gen := artChoose.gen
	rom := artChoose.rom
	seed := stripArtworkTagGroups(artworkLookupName(romGameStem(rom.Path)))
	tbxOpenPrompt("Search LaunchBox", "Title of the game", seed, func(text string) {
		title := strings.TrimSpace(text)
		if title == "" || !artChoose.open || artChoose.gen != gen {
			return
		}
		artChoose.note = "Searching LaunchBox for " + title + "…"
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
			defer cancel()
			results, err := launchBoxSearch(ctx, title)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			// The game's console first; every console when it has none there.
			platform := launchBoxPlatforms[rom.Collection]
			var picked []launchBoxResult
			for _, r := range results {
				if platform == "" || strings.EqualFold(r.Platform, platform) {
					picked = append(picked, r)
				}
			}
			if len(picked) == 0 {
				picked = results
			}
			if len(picked) > launchBoxMaxResults {
				picked = picked[:launchBoxMaxResults]
			}
			tbxPost(func() {
				if !artChoose.open || artChoose.gen != gen {
					return
				}
				switch {
				case err != nil:
					logWarn("art", "LaunchBox search %q: %v", title, err)
					artChoose.note = "LaunchBox could not be reached."
					return
				case len(picked) == 0:
					artChoose.note = "LaunchBox has nothing titled like " + title + "."
					return
				}
				// The rows of an earlier search make way for these, above
				// the search row.
				kept := make([]artChooseCandidate, 0, len(artChoose.cands)+len(picked))
				for _, c := range artChoose.cands {
					if c.lb == nil && !c.search {
						kept = append(kept, c)
					}
				}
				first := len(kept)
				tags := artworkTags(romGameStem(rom.Path))
				for i := range picked {
					r := picked[i]
					kept = append(kept, artChooseCandidate{name: r.Title, lb: &r, tags: tags})
				}
				kept = append(kept, artChooseLaunchBoxRow())
				// The list's indexes change: a preview still on its way for
				// the old list is dropped (gen), not shown on a new row.
				if artChoose.previewCancel != nil {
					artChoose.previewCancel()
					artChoose.previewCancel = nil
				}
				artChoose.gen++
				artChoose.cands = kept
				artChoose.cursor = first
				artChoose.previewFor, artChoose.previewBusy = -1, -1
				artChoose.previewFailed = false
				artChoose.note = "LaunchBox matches for " + title + "."
			})
		}()
	})
}
