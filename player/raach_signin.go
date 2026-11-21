package main

// raach_signin.go — Sign in for games: RetroArch's own RetroAchievements
// login, typed on the handheld.
//
// JukaHub's RetroAchievements page reads a username and a Web API key, which
// show a profile but cannot sign RetroArch in: the achievements of a running
// game need RetroArch's login (cheevos_username with cheevos_password or
// cheevos_token in retroarch.cfg), and until now that could only be typed in
// RetroArch's own menu. Settings → RetroAchievements → Sign in for games asks
// for the username (the shared prompt) and then the password (its own masked,
// submit-only keyboard target, raachPassVKTarget, like a network drive's), and
// writes them once into the configs of the RetroArch install JukaHub launches
// (raScopeTargets), with an empty cheevos_token so an old account's token is
// not tried, and cheevos_enable on. It is a permanent edit, as RetroArch's
// own menu makes, not a launch-scoped one.
//
// What happens next is RetroArch's: at the next game it logs in with the
// password, and on success clears cheevos_password and keeps the login token
// in cheevos_token instead (RetroArch since 1.7.2, PR #6618), written when it
// saves its config on exit. A RetroArch with config_save_on_exit off keeps
// the password in its file and logs in with it each time, exactly as it
// would after a sign-in in its own menu.
//
// The password is never logged, never stored in JukaHub's files and never
// shown: it lives in the keyboard buffer until submit, then only in the
// goroutine that writes it. A password with a double quote or a line break
// cannot be stored in RetroArch's config format, and is refused with a word
// to sign in from RetroArch's menu instead.

import (
	"errors"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// raachPassVKTarget aims the keyboard at the password (masked, submit only:
// vkeyboard.go treats it like a network drive's password).
const raachPassVKTarget vkTargetKind = 3609

// raachPassMax bounds what can be a password.
const raachPassMax = 256

// raachPassBuffer is the password being typed; cleared when the keyboard
// closes. raachSignInUser is the username typed first, until the password is
// submitted (a dismissed keyboard leaves it for the next sign-in to replace;
// it is no secret). Main thread.
var (
	raachPassBuffer string
	raachSignInUser string
)

// raachOpenSignIn is the row's press: the username first.
func raachOpenSignIn(*Config) {
	// The login is the owner's RetroArch account, used by their games: a
	// profile's games play with achievements off (profiles_saves.go).
	if profilesRefuseUnlessOwner("sign RetroArch in") {
		return
	}
	if retroArchRunning() {
		ShowToast("RetroArch is running: it rewrites its settings when it closes. Quit it first", ToastKindWarning)
		return
	}
	if len(raScopeTargets(retroArchConfigCandidates())) == 0 {
		ShowToast("RetroArch's settings file was not found: start RetroArch once, then sign in", ToastKindWarning)
		return
	}
	seed, _, _ := raachStatusNow()
	if seed == "" {
		seed = strings.TrimSpace(raEffectiveCredentials().Username)
	}
	tbxOpenPrompt("RetroAchievements username", "Your username on retroachievements.org", seed, func(text string) {
		user := strings.TrimSpace(text)
		if user == "" {
			return
		}
		if !ppssppRAValidUser(user) {
			ShowToast("That is not a RetroAchievements username: letters, digits, _ . -", ToastKindWarning)
			return
		}
		raachSignInUser = user
		// The prompt's keyboard is closing; the password's opens next frame.
		tbxPost(raachOpenPassKeyboard)
	})
}

// raachOpenPassKeyboard opens the masked keyboard for the password. It
// starts empty: no password is ever shown.
func raachOpenPassKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = raachPassVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	raachPassBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// raachPassSubmit takes the typed password (vkSubmit, after the keyboard
// closed) and writes the login off the main thread.
func raachPassSubmit(pass string) {
	user := raachSignInUser
	raachSignInUser = ""
	if user == "" || pass == "" {
		return
	}
	if len(pass) > raachPassMax || strings.ContainsAny(pass, "\"\r\n") {
		ShowToast("RetroArch cannot store that password in its settings: sign in from RetroArch's own menu", ToastKindWarning)
		return
	}
	go func() {
		n, err := raachWriteSignIn(user, pass)
		tbxPost(func() {
			raachStatusInvalidate()
			raInvalidateCredentialCache()
			if err != nil && n == 0 {
				logWarn("achievements", "sign-in for games not written: %v", err)
				ShowToast("Could not sign RetroArch in: "+firstLine(err.Error()), ToastKindError)
				return
			}
			logInfo("achievements", "RetroArch's RetroAchievements login set for %s in %d config(s)", user, n)
			ShowToast("Signed in as "+user+": RetroArch logs in when the next game starts", ToastKindSuccess)
		})
	}()
}

// raachSignInText puts the login into one config's text: the account, the
// password, no token (an old one would be tried first) and achievements on.
// Pure.
func raachSignInText(cfg, user, pass string) (string, bool) {
	changed := false
	for _, kv := range [][2]string{
		{"cheevos_username", user},
		{"cheevos_password", pass},
		{"cheevos_token", ""},
		{"cheevos_enable", "true"},
	} {
		var step bool
		cfg, step = raConfigSet(cfg, kv[0], kv[1])
		changed = changed || step
	}
	return cfg, changed
}

// raachWriteSignIn writes the login into every config of the RetroArch
// install JukaHub launches. It reports how many configs hold it. Under the
// launch scope's lock, so a restore after a game never interleaves with it.
func raachWriteSignIn(user, pass string) (int, error) {
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	if retroArchRunning() {
		return 0, errors.New("RetroArch is running")
	}
	targets := raScopeTargets(retroArchConfigCandidates())
	if len(targets) == 0 {
		return 0, errors.New("RetroArch's settings file was not found")
	}
	written := 0
	var first error
	for _, path := range targets {
		if _, err := editRetroArchConfig(path, func(cfg string) (string, bool) {
			return raachSignInText(cfg, user, pass)
		}); err != nil {
			// The error names the file, never the values.
			logWarn("achievements", "cannot write the sign-in into %s: %v", path, err)
			if first == nil {
				first = err
			}
			continue
		}
		written++
	}
	return written, first
}
