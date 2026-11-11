package main

// companion_psremote.go — the phone's PlayStation card.
//
// Pairing a PS5, or a PS4 on firmware 7 or newer, needs the PSN Account-ID
// once, and the sign-in page that fetches it (/psn, psn_signin.go) could only
// be reached through the code on the PS Remote Play panel's Account-ID card.
// The phone usually has the companion page open already, so the page has a
// card of its own:
//
//   - what the handheld has saved, shortened the way the panel shows it;
//   - a field that takes the Account-ID, the long account number other tools
//     print or the address of Sony's "redirect" page, sent to /api/psn as the
//     sign-in page sends it (the handheld trades a sign-in code with Sony and
//     keeps only the Account-ID; psPanelAccountArrived saves it on the main
//     loop, and starts Remote Play's download when it is missing);
//   - a button to the sign-in page;
//   - how Remote Play's own download is going on the handheld
//     (psHelperStatusNow), refreshed by the page while it runs, with Download
//     now or Try again when a press can start one.
//
// GET /api/remote-play answers with all of that; POST {"action":"install"}
// starts the download the way the panel's Install row does. Neither touches
// UI state: psremote.json is read from the card (every writer replaces it
// whole), the install has its own lock and the setting an atomic copy.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// companionPSAccount is what the page may know about the saved Account-ID:
// whether there is one, and its first and last characters.
type companionPSAccount struct {
	Set   bool   `json:"set"`
	Shown string `json:"shown,omitempty"`
}

// companionPSRemote is the helpers' install as the page shows it.
type companionPSRemote struct {
	Supported  bool   `json:"supported"`
	State      string `json:"state"`
	Text       string `json:"text"`
	Percent    int    `json:"percent"`
	Busy       bool   `json:"busy"`
	CanStart   bool   `json:"canStart"`
	StartLabel string `json:"startLabel,omitempty"`
}

// companionPSState is GET /api/remote-play's answer, and POST's with a
// message.
type companionPSState struct {
	Account    companionPSAccount `json:"account"`
	RemotePlay companionPSRemote  `json:"remotePlay"`
	Message    string             `json:"message,omitempty"`
}

// companionPSStateNow gathers the card's state. Any goroutine.
func companionPSStateNow() companionPSState {
	account := strings.TrimSpace(loadPSHosts().Settings.Account)
	st := psHelperStatusNow()
	state := companionPSState{
		RemotePlay: companionPSRemote(st),
	}
	if account != "" {
		state.Account = companionPSAccount{Set: true, Shown: accountStatus(account)}
	}
	return state
}

// companionStartRemotePlay is the card's Download now (or Try again): an
// install the way the panel's Install row starts one, a refusal by the
// firmware forgotten. It answers with the sentence for the phone and the HTTP
// status to send it with.
func companionStartRemotePlay() (string, int) {
	if !psHelperSupportedFn() {
		return "Remote Play streams on the TrimUI handhelds: there is nothing to download here", http.StatusConflict
	}
	psHelpersForgetPresence()
	switch {
	case !psHelpersNeeded():
		return "Remote Play is already installed on the handheld", http.StatusOK
	case psHelpersRunning():
		return "Remote Play is already downloading on the handheld", http.StatusOK
	case !IsOnline():
		return "Not started: " + psHelperNoInternetText(), http.StatusConflict
	}
	if !psHelpersStart(psStartRetry) {
		return "Remote Play could not start downloading: open PS Remote Play on the handheld", http.StatusConflict
	}
	// Like the panel's Retry, it wakes the background download too, so a
	// network failure of this try is tried again by itself.
	psBgKick()
	logInfo("psremote", "the phone started the Remote Play download")
	// The handheld says why its activity chip just appeared.
	tbxPost(func() { ShowToast("Downloading Remote Play — started from your phone", ToastKindInfo) })
	return "Downloading on the handheld", http.StatusOK
}

// handleRemotePlay answers the card: GET is its state, POST {"action":"install"}
// starts the download.
func (s *companionServer) handleRemotePlay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, companionPSStateNow())
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(body.Action), "install") {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		msg, code := companionStartRemotePlay()
		if code != http.StatusOK {
			http.Error(w, msg, code)
			return
		}
		state := companionPSStateNow()
		state.Message = msg
		writeCompanionJSON(w, state)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}
