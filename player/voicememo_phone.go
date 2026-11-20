package main

// voicememo_phone.go — "Send to phone" for a memo.
//
// The phone downloads the memo straight from the handheld: the Phone
// Companion's Files card already serves every shared folder behind its pairing
// PIN (companion_files.go, /api/files/download), and the memos' folder is one
// of those shares on the card (ftpShareNames). So the QR code shown full
// screen is the companion's own address and PIN with the download path and
// the memo's share-relative path — scanning it saves the WAV on the phone, no
// app and no pairing step. The share is looked up through companionFileShares
// and matched by folder identity (os.SameFile), so the link names the share
// exactly as the server will find it, whatever the card folder's case or a
// symlinked card root.
//
// On a PC the companion shares only the "shared" folder next to the app, and
// the memos live in Documents: a toast then says where the file is instead of
// showing a link that cannot work.

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/skip2/go-qrcode"
)

// vmSendToPhone shows the QR code that downloads m on a phone.
func vmSendToPhone(m vmMemo) {
	full := vmMemoPath(m)
	virtual, ok := vmShareVirtualPath(full)
	if !ok {
		ShowToast("The phone sees only the Phone Companion's shared folders — this memo is in "+vmDisplayDir(), ToastKindInfo)
		return
	}
	companionStart()
	if companion.url == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone fetches it over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return
	}
	link, ok := vmDownloadLink(companion.url, virtual)
	if !ok {
		ShowToast("The Phone Companion has no pairing link yet", ToastKindWarning)
		return
	}
	code, err := qrcode.New(link, qrcode.Low)
	if err != nil {
		ShowToast("Cannot draw the QR code: "+err.Error(), ToastKindWarning)
		return
	}
	detail := "Scan it with the phone's camera on the same Wi-Fi: the phone saves “" + m.file + "”"
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption
	}
	if qrZoomOpenCardDetail(code.Bitmap(), "Send “"+m.name+"” to your phone", detail) {
		logInfo("voicememo", "send to phone: %s", virtual)
	}
}

// vmDownloadLink turns the companion's pairing link ("http://ip:port/?p=PIN")
// into the Files card's download link for virtual, a share-relative path
// ("/VoiceMemos/Memo 1.wav"). The PIN rides in "p", the way the Files card's
// own download links carry it. false when the link has no PIN. Pure.
func vmDownloadLink(pairing, virtual string) (string, bool) {
	u, err := url.Parse(pairing)
	if err != nil || u.Host == "" {
		return "", false
	}
	pin := u.Query().Get("p")
	if pin == "" || virtual == "" {
		return "", false
	}
	q := url.Values{}
	q.Set("path", virtual)
	q.Set("p", pin)
	u.Path = "/api/files/download"
	u.RawPath = ""
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), true
}

// vmShareVirtualPath maps a file onto the Phone Companion's shares: the
// share whose folder is the file's folder or one above it, as the
// share-relative path the Files card takes. false when no share holds it.
func vmShareVirtualPath(full string) (string, bool) {
	shares := companionFileShares()
	if len(shares) == 0 {
		return "", false
	}
	type shareStat struct {
		name string
		info os.FileInfo
	}
	stats := make([]shareStat, 0, len(shares))
	for _, s := range shares {
		if info, err := os.Stat(s.dir); err == nil && info.IsDir() {
			stats = append(stats, shareStat{name: s.name, info: info})
		}
	}
	rel := filepath.Base(full)
	dir := filepath.Dir(full)
	for depth := 0; depth < 8; depth++ {
		if info, err := os.Stat(dir); err == nil {
			for _, s := range stats {
				if os.SameFile(info, s.info) {
					return path.Join("/", s.name, rel), true
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir || strings.TrimSpace(parent) == "" {
			break
		}
		rel = filepath.Base(dir) + "/" + rel
		dir = parent
	}
	return "", false
}
