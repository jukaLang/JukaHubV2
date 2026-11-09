package main

// portalsignin.go — the one action a captive portal needs.
//
// netmon.go detects the portal and keeps its sign-in URL. This is what the
// Wi-Fi page's "Sign in" control calls: it opens that page in the in-app
// Browser, so the user finishes the hotel/airport login without leaving
// JukaHub or guessing which page the network wanted.

// openPortalSignIn opens the detected portal page. It prefers the in-app
// Browser (the page stays over JukaHub, and a phone-style redirect is
// followed), and falls back to the desktop handler if the browser cannot open.
func openPortalSignIn(config *Config) bool {
	link := PortalSignInURL()
	if link == "" {
		ShowToast("No Wi-Fi sign-in page was detected", ToastKindInfo)
		return false
	}
	if config != nil && browserOpenURL(config, link) {
		ShowToast("Finish signing in, then come back to Wi-Fi", ToastKindInfo)
		return true
	}
	if err := openExternalURL(link); err == nil {
		ShowToast("Sign-in page opened", ToastKindInfo)
		return true
	}
	ShowToast("Could not open the sign-in page", ToastKindError)
	return false
}
