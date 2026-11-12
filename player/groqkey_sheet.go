package main

// groqkey_sheet.go — Settings → General → Groq API key, the row right under
// "Your name".
//
// A on it opens a sheet of ways to give JukaHub the key, as the VPN
// configuration row does (vpn.go):
//
//   - Paste it from your phone: a full-screen QR code that opens the Phone
//     Companion at its Groq key card (companion_phonecard.go). The phone pastes
//     the key there, the handheld checks it with Groq and saves it
//     (companion_groq.go), and the code closes itself when the key arrives.
//   - Type or paste it here: the keyboard's own key field (vksettings.go). It
//     starts empty and saves on submit only; its Paste chip, L1+Y and Ctrl+V
//     paste what this device copied.
//   - Get a free key (Windows): Groq's console in the browser.
//   - Remove the key, once one is saved, behind a confirmation.
//
// The key itself is never shown: the row and the sheet name its last four
// characters (vkGroqKeyHint).

import (
	"runtime"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// groqConsoleKeysURL is the page of Groq's console that hands out keys.
	groqConsoleKeysURL = "https://console.groq.com/keys"
	// groqKeyRowID is the General row's id: search results and the Chat
	// banner's "Add key" land on it.
	groqKeyRowID = "aiKey"
	// groqKeyPendingMS bounds how long a request to open the sheet waits for
	// Settings to come up (groqKeyHubEnter).
	groqKeyPendingMS = 5000
)

// groqKeyPendingAt is when a screen outside Settings asked for the key's
// sheet (0 = nobody). Main thread only.
var groqKeyPendingAt uint64

// groqKeyRow is the General row.
func groqKeyRow() setRow {
	return setRow{
		ID: groqKeyRowID, Label: "Groq API key", Kind: setKindAction,
		Detail: groqKeyRowDetail,
		Value:  groqKeyRowValue,
		Open:   groqKeyOpenSheet,
	}
}

// groqKeyRowValue is the row's read-out: the key's last four characters,
// never the key.
func groqKeyRowValue(*Config) string {
	if hint := vkGroqKeyHint(); hint != "" {
		return "Saved · " + hint
	}
	return "Not set"
}

// groqKeyRowDetail is the line under the label. Search reads it too, so it
// names what the key is for ("Juka AI", "voice typing").
func groqKeyRowDetail(*Config) string {
	if vkGroqKeyHint() != "" {
		return "Juka AI in Chat, voice typing and voice commands use it"
	}
	return "For Juka AI and voice typing: paste it from your phone or type it"
}

// groqKeyOpenSheet lists the ways to give JukaHub the key. The handheld puts
// the phone first (typing a 56-character key with a pad is the slow way); a
// PC puts the keyboard first, where Ctrl+V pastes a copied key at once.
func groqKeyOpenSheet(c *Config) {
	phone := setOption{Value: "phone", Label: "Paste it from your phone",
		Note: "Scan a code: your phone opens the key field, you paste and tap Save"}
	typed := setOption{Value: "type", Label: "Type or paste it here", Note: groqKeyTypeNote()}
	opts := []setOption{phone, typed}
	if !setHandheld() {
		opts = []setOption{typed, phone}
	}
	if runtime.GOOS == "windows" {
		opts = append(opts, setOption{Value: "get", Label: "Get a free key", Note: "Opens console.groq.com/keys in your browser"})
	}
	note := "Juka AI in Chat, voice typing, voice commands and Juka's voice use it · free at console.groq.com"
	if hint := vkGroqKeyHint(); hint != "" {
		opts = append(opts, setOption{Value: "remove", Label: "Remove the key",
			Note: "Juka AI and voice typing stop until a new key is saved"})
		note = "Saved · " + hint + " · a new key replaces it"
	}
	setSheetOpen(&setHub.sheet, "Groq API key", note, opts, "", func(value string) {
		groqKeyAction(c, value)
	})
}

// groqKeyTypeNote says how pasting works on this device.
func groqKeyTypeNote() string {
	if setHandheld() {
		return "The on-screen keyboard; its Paste chip pastes what you copied here"
	}
	return "Ctrl+V pastes a key you copied; Enter saves it"
}

// groqKeyAction runs the sheet's choice.
func groqKeyAction(c *Config, value string) {
	switch value {
	case "phone":
		groqKeyFromPhone()
	case "type":
		vkOpenGroqKeyKeyboard()
	case "get":
		if err := openExternalURL(groqConsoleKeysURL); err != nil {
			setToast("Open console.groq.com/keys in a browser")
			return
		}
		setToast("Create a key on Groq's page, copy it, then paste it here")
	case "remove":
		setSheetOpen(&setHub.sheet, "Remove the Groq key?", "Juka AI, voice typing and voice commands stop until a new key is saved",
			[]setOption{{Value: "keep", Label: "Keep it"}, {Value: "remove", Label: "Remove"}}, "keep",
			func(choice string) {
				if choice == "remove" {
					groqKeyRemove(c)
				}
			})
	}
}

// groqKeyFromPhone shows the code that opens the companion's key card.
func groqKeyFromPhone() {
	phoneCardOpen(phoneCardGroq, "Paste your Groq key from your phone",
		"Scan it, paste the key and tap Save on handheld")
}

// groqKeyRemove clears the key, and the old setting name an early build
// stored it under.
func groqKeyRemove(c *Config) {
	if c == nil {
		return
	}
	for _, key := range []string{"groq_api_key", "GroqApiKey"} {
		if v, _ := c.Variables.Custom[key].(string); strings.TrimSpace(v) != "" {
			vkSetCustom(c, key, "")
		}
	}
	setToast("Groq key removed")
}

// groqKeyOpenFromAnywhere takes the user to the key's row with its sheet open:
// the Chat banner's "Add key" (chatinput.go). Inside Settings the sheet opens
// at once; elsewhere it opens when Settings comes up (groqKeyHubEnter). A
// setup without the Settings screen gets the key field itself.
func groqKeyOpenFromAnywhere(c *Config) {
	if c == nil {
		return
	}
	if setIsHubScene(setHubSceneName(c)) {
		setOpenSettingRow(c, "general", groqKeyRowID, "")
		groqKeyOpenSheet(c)
		return
	}
	if sceneIndexExact(c, "Settings") < 0 {
		PlayActivateSound()
		vkOpenGroqKeyKeyboard()
		return
	}
	groqKeyPendingAt = sdl.GetTicks64()
	setOpenSettingRow(c, "general", groqKeyRowID, "")
}

// groqKeyHubEnter opens the sheet on the Settings visit groqKeyOpenFromAnywhere
// asked for, once its row has the focus (setui.go setHubEnter calls it). A
// request older than groqKeyPendingMS is dropped: it must not open the sheet
// on some later visit.
func groqKeyHubEnter(c *Config) {
	at := groqKeyPendingAt
	if at == 0 {
		return
	}
	groqKeyPendingAt = 0
	if sdl.GetTicks64()-at > groqKeyPendingMS || !setHub.inPane {
		return
	}
	if setHub.row < 0 || setHub.row >= len(setHub.rows) || setHub.rows[setHub.row].ID != groqKeyRowID {
		return
	}
	groqKeyOpenSheet(c)
}
