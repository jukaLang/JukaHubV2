package main

// onboarding_name.go — the tour's "What should we call you?" page.
//
// The first-run tour asks for the user's name right after the welcome. It is
// the same value as Settings → General → Your name and the chat's name chip
// (userName / setChatDisplayName, chat.go), so Home's greeting, the chat and
// the boot and shutdown greetings (bootgreet.go) all use it from then on.
//
// The page types on the on-screen keyboard's existing name field
// (vkTargetName, vkOpenName): it opens seeded with the current name, saves
// only on submit, and B leaves the name as it was. While the keyboard is up
// the tour steps aside (the keyboard is a modal: onboardingCoveredByOverlay)
// and comes back on this page, now greeting the user by the new name.
//
// A on this page opens the keyboard the first time when no name is set — the
// page's question is the obvious thing to answer — and is Next after that, so
// a user who does not want to give a name is never sent round in a loop.
// Right, Start and the Next button always move on: the name is optional.

// onbName is the page's state for one run of the tour.
var onbName struct {
	// asked is set once the keyboard has been opened from this page.
	asked bool
}

// onboardingNameReset forgets the page's state (onboardingStart).
func onboardingNameReset() { onbName.asked = false }

// onboardingNameStepIs reports whether the tour is showing the name page.
func onboardingNameStepIs() bool {
	if onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return false
	}
	return onboardingSteps[onboarding.step].kind == "name"
}

// onboardingNameAOpens reports whether A on the name page opens the keyboard
// rather than going on: only before it has been opened, with no name set.
func onboardingNameAOpens(config *Config) bool {
	return onboardingNameStepIs() && !onbName.asked && userName(config) == ""
}

// onboardingNameOpen opens the keyboard on the name field.
func onboardingNameOpen() {
	onbName.asked = true
	onboarding.info = ""
	vkOpenName()
}

// onboardingNameActionLabel is the page's button.
func onboardingNameActionLabel(config *Config) string {
	if userName(config) == "" {
		return "Type your name"
	}
	return "Change name"
}

// onboardingNameStatus is the line under the hint: the name as it will be
// used, or that none is needed.
func onboardingNameStatus(config *Config) string {
	if n := userName(config); n != "" {
		return "Hi, " + n + "! JukaHub will greet you by name."
	}
	return "No name yet. It is optional."
}

// onboardingNameAHint is what A does on the page, for the footer.
func onboardingNameAHint(config *Config) string {
	if onboardingNameAOpens(config) {
		return "Type name"
	}
	return "Next"
}
