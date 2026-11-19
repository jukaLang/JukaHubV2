package main

// homebrew_store_key.go — the player's itch.io API key for Get Homebrew
// (homebrew_store.go), and the on-screen keyboard's two targets there.
//
// Browsing needs nothing; downloading needs the player's own key: itch.io
// serves a game's files only to an account, free games included (paid ones
// only to an account that bought them). The key is made at
// https://itch.io/user/settings/api-keys ("Generate new API key").
//
// It reaches the handheld two ways:
//
//   - From the phone: the Phone Companion page has an "itch.io" card beside
//     the RetroAchievements and RomM ones (companion.go's page, the
//     /api/itch-key handler below). Get Homebrew's key sheet shows a code that
//     opens the page at that card ("#itch", companion_phonecard.go).
//   - On the handheld: the on-screen keyboard with a submit-only, masked
//     target (homebrewKeyVKTarget, the Groq key's rules in vkeyboard.go and
//     vkedit.go): nothing is saved while it is typed, the strip offers no
//     words, the buffer is cleared when the keyboard closes.
//
// Either way the key is checked with itch.io (/profile, which also names the
// account) before it is saved; a handheld that cannot reach itch.io saves it
// and says it could not check. It is stored in data/homebrew-store/itch-key.json
// (mode 0600) encrypted with JukaHub's API-token encryption when that works
// (the "ENC:" form the Groq key uses), and never logged or sent back: the page
// learns only whether a key is saved, its last four characters and the
// account name.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// homebrewKeyVKTarget aims the keyboard at the itch.io API key.
	homebrewKeyVKTarget vkTargetKind = 1907
	// homebrewSearchVKTarget aims the keyboard at the store's search.
	homebrewSearchVKTarget vkTargetKind = 1908
	// phoneCardItch is the companion page's itch.io card ("itchCard").
	phoneCardItch phoneCardKind = "itch"
	// homebrewKeysURL is where itch.io makes API keys.
	homebrewKeysURL = "https://itch.io/user/settings/api-keys"
	// hbKeyMin and hbKeyMax bound what can be a key.
	hbKeyMin = 20
	hbKeyMax = 200
	// hbKeySheetToken names the key sheet, so a check can relabel it.
	hbKeySheetToken = "homebrew-key"
)

// homebrewKeyBuffer holds the key while it is typed; cleared on close.
var homebrewKeyBuffer string

// hbKeyFile is itch-key.json.
type hbKeyFile struct {
	Key     string `json:"key"`
	Account string `json:"account,omitempty"`
}

var hbKeyStore struct {
	mu      sync.Mutex
	loaded  bool
	key     string
	account string
	// write keeps two saves apart. It is held across the disk write; mu is
	// not, so the main thread's hbKeyGet never waits on the card.
	write sync.Mutex
}

func hbKeyPath() string { return filepath.Join(hbDataDir(), "itch-key.json") }

// hbKeyLoadLocked reads the key once.
func hbKeyLoadLocked() {
	if hbKeyStore.loaded {
		return
	}
	hbKeyStore.loaded = true
	raw, err := os.ReadFile(hbKeyPath())
	if err != nil {
		return
	}
	var f hbKeyFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	key := strings.TrimSpace(f.Key)
	if strings.HasPrefix(key, "ENC:") {
		dec, derr := DecryptAPIToken(key, "itch_api_key")
		if derr != nil {
			logWarn("homebrew", "the saved itch.io key could not be decrypted; add it again")
			return
		}
		key = strings.TrimSpace(dec)
	}
	if k, err := hbCleanKey(key); err == nil {
		hbKeyStore.key, hbKeyStore.account = k, hbOneLine(f.Account, 60)
	}
}

// hbKeyGet is the saved key, "" when there is none. Any goroutine.
func hbKeyGet() string {
	hbKeyStore.mu.Lock()
	defer hbKeyStore.mu.Unlock()
	hbKeyLoadLocked()
	return hbKeyStore.key
}

// hbKeyAccount is the account the saved key belongs to, when it was checked.
func hbKeyAccount() string {
	hbKeyStore.mu.Lock()
	defer hbKeyStore.mu.Unlock()
	hbKeyLoadLocked()
	return hbKeyStore.account
}

// hbKeySave stores key (and the account it was checked as); an empty key
// removes it. Any goroutine.
func hbKeySave(key, account string) error {
	f := hbKeyFile{Account: hbOneLine(account, 60)}
	if key != "" {
		f.Key = key
		if enc, err := EncryptAPIToken(key, "itch_api_key"); err == nil && strings.HasPrefix(enc, "ENC:") {
			f.Key = enc
		}
	}
	hbKeyStore.write.Lock()
	defer hbKeyStore.write.Unlock()
	path := hbKeyPath()
	if key == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(path + ".bak")
	} else {
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if err := AtomicWrite(path, raw, 0o600); err != nil {
			return err
		}
		// AtomicWrite keeps the previous file as a backup: not for a secret.
		_ = os.Remove(path + ".bak")
	}
	hbKeyStore.mu.Lock()
	hbKeyStore.loaded, hbKeyStore.key, hbKeyStore.account = true, key, f.Account
	hbKeyStore.mu.Unlock()
	return nil
}

// hbCleanKey trims what a paste carries around a key and refuses what
// cannot be one. Pure.
func hbCleanKey(raw string) (string, error) {
	key := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "\"'`"))
	if len(key) < hbKeyMin || len(key) > hbKeyMax {
		return "", errors.New("that does not look like an itch.io API key — copy it again from itch.io/user/settings/api-keys")
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return "", errors.New("that does not look like an itch.io API key — copy it again from itch.io/user/settings/api-keys")
		}
	}
	return key, nil
}

// hbCheckKey asks itch.io whose key it is: the account's name, and a
// verdict (rejected for a key itch.io refuses, unchecked when it could not be
// asked). A variable, so tests never touch the network.
var hbCheckKey = func(ctx context.Context, key string) (string, groqKeyVerdict) {
	name, err := hbFetchProfile(ctx, key)
	switch {
	case err == nil:
		return name, groqKeyAccepted
	case errors.Is(err, errHbKeyRefused):
		return "", groqKeyRejected
	}
	return "", groqKeyUnchecked
}

// ── The phone ────────────────────────────────────────────────────────────

// hbKeyStatus is what the page may know: whether a key is saved, its last
// four characters and the account. Never the key.
func hbKeyStatus() map[string]interface{} {
	key := hbKeyGet()
	return map[string]interface{}{"set": key != "", "tail": companionAccountTail(key), "account": hbKeyAccount()}
}

// handleItchKey answers the page's itch.io card: GET tells whether a key is
// saved, POST checks a new one with itch.io and saves it.
func (s *companionServer) handleItchKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, hbKeyStatus())
	case http.MethodPost:
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		key, err := hbCleanKey(body.Key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), companionAccountCheckWait)
		account, verdict := hbCheckKey(ctx, key)
		cancel()
		if verdict == groqKeyRejected {
			http.Error(w, "itch.io refused this key - copy it again from itch.io/user/settings/api-keys", http.StatusUnprocessableEntity)
			return
		}
		if err := hbKeySave(key, account); err != nil {
			http.Error(w, "The handheld could not save it: "+firstLine(err.Error()), http.StatusInternalServerError)
			return
		}
		tbxPost(hbKeyArrived)
		logInfo("companion", "itch.io key received from the phone (checked: %v)", verdict == groqKeyAccepted)
		msg := "Saved on the handheld. itch.io knows it as " + account + "."
		if verdict != groqKeyAccepted {
			msg = "Saved on the handheld. JukaHub could not reach itch.io to check it just now."
		}
		out := hbKeyStatus()
		out["ok"], out["verified"], out["message"] = true, verdict == groqKeyAccepted, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// hbKeyFromPhone shows the code that opens the companion's itch.io card.
func hbKeyFromPhone() {
	phoneCardOpen(phoneCardItch, "itch.io key · paste it on your phone",
		"Scan it, paste your API key from itch.io/user/settings/api-keys, then tap Save")
}

// hbKeyArrived applies a key saved from the phone or the keyboard, on the
// main loop.
func hbKeyArrived() {
	phoneCardArrived(phoneCardItch)
	hb.ownedAt = time.Time{}
	hbOptionsRefresh()
	if !homebrewOnScreen(appConfig) {
		return
	}
	hbLoadOwned()
	hbCheckUpdatesSoon(30 * time.Minute)
	name := hbKeyAccount()
	if name == "" {
		ShowToast("itch.io key saved — downloads are ready", ToastKindSuccess)
		return
	}
	ShowToast("itch.io key saved — signed in as "+name, ToastKindSuccess)
}

// ── The keyboard ─────────────────────────────────────────────────────────

// hbOpenKeyKeyboard opens the masked keyboard for the key. It starts empty:
// a saved key is never shown.
func hbOpenKeyKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = homebrewKeyVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	homebrewKeyBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// homebrewKeySubmit takes a typed key (vkSubmit, after the keyboard closed):
// checked with itch.io on a goroutine, then saved. An empty submit keeps the
// saved key.
func homebrewKeySubmit(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	key, err := hbCleanKey(text)
	if err != nil {
		PlayErrorSound()
		ShowToast(firstLine(err.Error()), ToastKindWarning)
		return
	}
	ShowToast("Checking the key with itch.io…", ToastKindInfo)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "checking the key failed: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		account, verdict := hbCheckKey(ctx, key)
		cancel()
		if verdict == groqKeyRejected {
			tbxPost(func() {
				PlayErrorSound()
				ShowToast("itch.io refused this key — copy it again from itch.io/user/settings/api-keys", ToastKindError)
			})
			return
		}
		serr := hbKeySave(key, account)
		tbxPost(func() {
			if serr != nil {
				ShowToast("Could not save the key: "+firstLine(serr.Error()), ToastKindError)
				return
			}
			if verdict != groqKeyAccepted {
				ShowToast("Key saved — itch.io could not be reached to check it", ToastKindWarning)
			}
			hbKeyArrived()
		})
	}()
}

// hbOpenSearch opens the keyboard on the store's search; the grid narrows on
// every key (homebrewSetQuery).
func hbOpenSearch() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = homebrewSearchVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
	PlayActivateSound()
}

// ── The key sheet ────────────────────────────────────────────────────────

// hbOpenKeySheet explains the key and offers the ways to add it. why, when
// set, is the line that says what needed it.
func hbOpenKeySheet(config *Config, why string) {
	body, buttons := hbKeySheetSpec(why)
	tbxOpenSheet(config, tbxSheetSpec{
		token: hbKeySheetToken, title: "itch.io key", subtitle: "Downloads use your own itch.io account",
		icon: "key", body: body, qr: tbxQRModules(homebrewKeysURL), qrLabel: "itch.io → Settings → API keys",
		buttons: buttons, focus: len(buttons) - 1,
	})
}

// hbKeySheetSpec is the key sheet's text and buttons.
func hbKeySheetSpec(why string) (string, []tbxSheetButton) {
	var b strings.Builder
	if why != "" {
		b.WriteString(why + "\n\n")
	}
	key := hbKeyGet()
	switch {
	case key != "" && hbKeyAccount() != "":
		b.WriteString("## Signed in\nThe key of " + hbKeyAccount() + " is saved (it ends in " + companionAccountTail(key) + ").\n\n")
	case key != "":
		b.WriteString("## A key is saved\nIt ends in " + companionAccountTail(key) + ".\n\n")
	default:
		b.WriteString("## No key yet\nBrowsing works without one. To download, itch.io wants your own account — free games too.\n\n")
	}
	b.WriteString("## Get a key\n• Sign in on itch.io (the code opens the page)\n• Settings → API keys → Generate new API key\n" +
		"• Copy it, then send it from your phone or type it here\n\n")
	b.WriteString("## What it is used for\nDownloading free games and the ones you bought, and checking them for updates. " +
		"Paid games you do not own are never downloaded: buy them on itch.io first. The key stays on this handheld.")
	buttons := []tbxSheetButton{{label: "Close"}}
	if key != "" {
		buttons = append(buttons, tbxSheetButton{label: "Remove key", run: func() {
			if err := hbKeySave("", ""); err != nil {
				ShowToast("Could not remove the key: "+firstLine(err.Error()), ToastKindError)
				return
			}
			hbMergeOwned(nil)
			ShowToast("itch.io key removed", ToastKindInfo)
		}})
	}
	buttons = append(buttons, tbxSheetButton{label: "Type it", run: hbOpenKeyKeyboard},
		tbxSheetButton{label: "From your phone", primary: true, run: hbKeyFromPhone})
	return b.String(), buttons
}
