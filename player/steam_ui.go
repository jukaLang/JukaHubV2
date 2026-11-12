package main

// steam_ui.go — Apps → Steam: the library panel, its game sheet and the
// Account page.
//
// The library lists every game of the account with its cover, play time and
// where it can be played, in three tabs (All, Installed, Downloads; L1/R1),
// sorted by last played, name or play time (R2) and narrowed by a search
// (L2 opens the keyboard). A plays the focused game — through the Steam app
// here, or on a Steam PC of the account or a paired JukaHub PC — or installs
// it; Y opens the game's sheet (bigger cover, details, Uninstall, which PC,
// store page). Downloads in progress show a bar and are watched until they
// finish.
//
// X opens the Account page: the Steam app's QR sign-in (steam_auth.go) first,
// then the phone (OpenID, a profile link or a Web API key), this PC's browser,
// the account the Steam app here uses, and Sign out.
//
// Everything slow runs on goroutines (steam.go, steam_remote.go) and comes
// back through tbxPost; the panel draws from its own copy every frame.

import (
	"context"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type steamPanelPage int

const (
	steamPageLibrary steamPanelPage = iota
	steamPageAccount
	steamPageQR
)

type steamTab int

const (
	steamTabAll steamTab = iota
	steamTabInstalled
	steamTabDownloads
	steamTabCount
)

func (t steamTab) label() string {
	switch t {
	case steamTabInstalled:
		return "Installed"
	case steamTabDownloads:
		return "Downloads"
	}
	return "All games"
}

type steamSortMode int

const (
	steamSortRecent steamSortMode = iota
	steamSortName
	steamSortPlaytime
	steamSortCount
)

func (m steamSortMode) label() string {
	switch m {
	case steamSortName:
		return "A–Z"
	case steamSortPlaytime:
		return "Most played"
	}
	return "Recent"
}

type steamAccountAction int

const (
	steamActQR steamAccountAction = iota
	steamActPhone
	steamActBrowser
	steamActAppAccount
	steamActKey
	steamActPCs
	steamActSignOut
)

type steamAccountRow struct {
	title  string
	detail string
	status string
	action steamAccountAction
}

// steamSheetAction is one row of the game sheet.
type steamSheetAction int

const (
	steamSheetPrimary steamSheetAction = iota
	steamSheetUninstall
	steamSheetTarget
	steamSheetStore
	steamSheetClose
)

type steamSheetRow struct {
	label  string
	action steamSheetAction
}

// steamSearchVKTarget aims the on-screen keyboard at the library's search,
// like homeSearchVKTarget: one case per keyboard hook (vkCurrentText,
// vkSetText, vkTitle, vkPlaceholder, vkActionLabel, vkSubmit,
// vkLiveFilterTarget).
const steamSearchVKTarget vkTargetKind = 67

// steamSearchMax bounds the search line.
const steamSearchMax = 48

type steamPanelState struct {
	open     bool
	page     steamPanelPage
	cursor   int
	scroll   int
	acCursor int
	tab      steamTab
	sortMode steamSortMode
	query    string

	sheet       bool
	sheetCursor int

	games   []steamGame
	view    []steamGame // games after tab, search and sort
	dirty   bool        // view must be rebuilt
	account steamAccount
	appUser steamLocalUser
	hasApp  bool
	local   bool // the Steam app is on this computer and can start games
	peers   int  // paired devices
	remote  steamRemoteSnapshot
	target  int // index into remote.pcs for installing a game that is nowhere
	busy    bool
	err     string
	updated time.Time
	gen     int

	// Download watching: polled while anything downloads, or for a while
	// after an install was asked for (the manifest appears once the user
	// confirms Steam's dialog).
	polling    bool
	pollGen    int
	lastPoll   time.Time
	watchUntil time.Time
}

var steamPanel steamPanelState

// steamGenSeq numbers every refresh and poll across panel openings, so an
// answer for a panel that was closed and opened again is never applied.
var steamGenSeq int

type steamRowHit struct {
	index int
	rect  sdl.Rect
}

var (
	steamPanelRect   sdl.Rect
	steamRowHits     []steamRowHit
	steamPillRect    sdl.Rect
	steamTabRects    [steamTabCount]sdl.Rect
	steamQRRect      sdl.Rect
	steamVisibleRows int
)

// openSteamPanel opens the library from Apps (or Search).
func openSteamPanel() {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	portsPanel = portsPanelState{}
	winAppsPanel = windowsAppsPanelState{}
	steamQRStop()
	steamPanel = steamPanelState{open: true, dirty: true}
	steamResetHits()
	steamPanelLoadAccount()
	lib := steamLoadLibrary()
	if lib.SteamID == steamPanel.account.SteamID {
		steamPanel.games = lib.Games
		steamPanel.updated = lib.Updated
		if steamPanel.account.Persona == "" {
			steamPanel.account.Persona = lib.Persona
		}
	}
	if !steamPanelHasSource() {
		// Nothing to list until someone signs in: start where that happens.
		steamPanel.page = steamPageAccount
	}
	PlayActivateSound()
	steamPanelRefresh()
}

func steamResetHits() {
	steamPanelRect, steamPillRect, steamQRRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	steamTabRects = [steamTabCount]sdl.Rect{}
	steamRowHits = nil
}

func closeSteamPanel() {
	if !steamPanel.open {
		return
	}
	steamQRStop()
	if virtualKeyboardActive && vkTarget == steamSearchVKTarget {
		vkCloseKeyboard()
	}
	steamPanel = steamPanelState{}
	steamResetHits()
	PlayBackSound()
}

// steamPanelHasSource reports whether there is anything to list: an
// account, the Steam app here, or a paired PC that may have games.
func steamPanelHasSource() bool {
	return steamPanel.account.SteamID != "" || steamPanel.local || steamPanel.peers > 0
}

// steamPanelLoadAccount reads who is signed in and what this computer has.
// Main thread; small files only.
func steamPanelLoadAccount() {
	acct, _ := steamCurrentAccount()
	steamPanel.account = acct
	steamPanel.appUser, steamPanel.hasApp = steamFindLocalUser()
	steamPanel.local = steamClientPresent()
	steamPanel.peers = len(peerPairedList())
}

// steamPanelAccountChanged reloads the panel after a sign-in, a new key or a
// sign-out, wherever it came from. Main loop.
func steamPanelAccountChanged() {
	if !steamPanel.open {
		return
	}
	// Invalidate a refresh even when there is no source to start a new one.
	// An old answer must never put a signed-out account back on screen.
	steamGenSeq++
	steamPanel.gen = steamGenSeq
	prev := steamPanel.account.SteamID
	steamPanelLoadAccount()
	steamPanel.remote = steamRemoteSnapshot{}
	steamPanel.target = 0
	if steamPanel.account.SteamID != prev {
		steamPanel.games = nil
		steamPanel.dirty = true
		steamPanel.cursor, steamPanel.scroll = 0, 0
	}
	if steamPanel.account.SteamID != "" || steamPanel.local {
		if steamPanel.page == steamPageQR {
			steamQRStop()
		}
		steamPanel.page = steamPageLibrary
	} else if !steamPanelHasSource() {
		steamPanel.page = steamPageAccount
	}
	steamPanel.busy = false
	steamPanel.polling = false
	steamPanelRefresh()
}

// steamPanelRefresh reads the library again in the background.
func steamPanelRefresh() {
	if !steamPanel.open || steamPanel.busy {
		return
	}
	if !steamPanelHasSource() {
		return
	}
	steamPanel.busy = true
	steamPanel.err = ""
	// A poll still out answers for the list this refresh replaces.
	steamPanel.polling = false
	steamGenSeq++
	steamPanel.gen = steamGenSeq
	gen := steamPanel.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res := steamRefreshLibrary(ctx)
		cancel()
		tbxPost(func() { steamPanelApply(gen, res) })
	}()
}

// steamPanelApply takes a finished refresh on the main loop.
func steamPanelApply(gen int, res steamRefreshResult) {
	if !steamPanel.open || steamPanel.gen != gen {
		return
	}
	steamPanel.busy = false
	if res.account.SteamID != steamPanel.account.SteamID {
		// The account changed while this ran (a sign-in, the Steam app
		// switching user): reload it and read again for the new one.
		steamPanelAccountChanged()
		return
	}
	focused := steamPanelFocusedID()
	steamPanel.games = res.games
	steamPanel.account.Persona = res.account.Persona
	steamPanel.account.HasToken, steamPanel.account.TokenLost = res.account.HasToken, res.account.TokenLost
	steamPanel.local = res.local && steamClientPresent()
	if !steamPanel.local {
		steamPanel.remote = steamMergeSnapshot(steamPanel.remote, res.remote)
		if len(steamPanel.remote.pcs) != len(res.remote.pcs) {
			steamPanel.games = steamApplyRemote(steamPanel.games, steamPanel.remote)
		}
	} else {
		steamPanel.remote = steamRemoteSnapshot{}
	}
	if steamPanel.target >= len(steamPanel.remote.pcs) {
		steamPanel.target = 0
	}
	steamPanel.updated = res.when
	steamPanel.err = steamErrorText(res.webErr)
	if res.account.TokenLost && steamPanel.err == "" {
		steamPanel.err = errSteamSignedOut.Error()
	}
	steamPanel.dirty = true
	steamPanelKeepFocus(focused)
	if steamPanel.err != "" && len(steamPanel.games) == 0 {
		ShowToast(steamPanel.err, ToastKindWarning)
	}
}

// ── Download watching ────────────────────────────────────────────────────

// steamPanelAnyDownloading reports whether a game downloads here or on a PC.
func steamPanelAnyDownloading() bool {
	for _, g := range steamPanel.games {
		if g.steamDownloading() || g.Remote.downloading() {
			return true
		}
	}
	return false
}

// steamPanelTick polls downloads while they run. Main loop, every frame;
// one time compare when nothing downloads.
func steamPanelTick() {
	if !steamPanel.open || steamPanel.polling || steamPanel.busy {
		return
	}
	now := time.Now()
	if now.After(steamPanel.watchUntil) && !steamPanelAnyDownloading() {
		return
	}
	every := 3 * time.Second
	if !steamPanel.local {
		every = 6 * time.Second
	}
	if now.Sub(steamPanel.lastPoll) < every {
		return
	}
	steamPanel.lastPoll = now
	steamPanel.polling = true
	steamGenSeq++
	steamPanel.pollGen = steamGenSeq
	gen := steamPanel.pollGen
	local := steamPanel.local
	id := steamPanel.account.SteamID
	if id == "" {
		id = steamPanel.appUser.SteamID
	}
	hasToken := steamPanel.account.HasToken
	go func() {
		var games []steamGame
		var present map[int]bool
		var snap steamRemoteSnapshot
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if local {
			if root := steamRoot(); root != "" {
				games = steamInstalledGames(root, id, false)
				present = steamManifestIDs(root)
			}
		} else {
			token := ""
			if hasToken {
				token, _, _ = steamAccessToken(ctx)
			}
			snap = steamRemoteSnapshotFetch(ctx, token)
		}
		tbxPost(func() { steamPanelApplyPoll(gen, local, games, present, snap) })
	}()
}

// steamPanelApplyPoll folds a download poll into the list and says when a
// download finished. present is every app with a manifest here (local);
// only a game whose manifest is gone counts as uninstalled. Main loop.
func steamPanelApplyPoll(gen int, local bool, scanned []steamGame, present map[int]bool, snap steamRemoteSnapshot) {
	if !steamPanel.open || steamPanel.pollGen != gen || !steamPanel.polling {
		return
	}
	steamPanel.polling = false
	if local != steamPanel.local || (local && present == nil) {
		return
	}
	was := map[int]bool{}
	for _, g := range steamPanel.games {
		if g.steamDownloading() || g.Remote.downloading() {
			was[g.AppID] = true
		}
	}
	focused := steamPanelFocusedID()
	games := append([]steamGame(nil), steamPanel.games...)
	if local {
		at := make(map[int]int, len(games))
		for i := range games {
			at[games[i].AppID] = i
		}
		seen := map[int]bool{}
		for _, s := range scanned {
			seen[s.AppID] = true
			i, ok := at[s.AppID]
			if !ok {
				games = append(games, s)
				continue
			}
			g := &games[i]
			g.Installed, g.Size = s.Installed, s.Size
			g.DlDone, g.DlTotal, g.DlPaused = s.DlDone, s.DlTotal, s.DlPaused
		}
		// A manifest that is gone is an uninstalled game; one that could
		// not be read right now keeps what the list had.
		for i := range games {
			if !seen[games[i].AppID] && !present[games[i].AppID] {
				games[i].Installed, games[i].Size = false, 0
				games[i].DlDone, games[i].DlTotal = 0, 0
			}
		}
	} else {
		steamPanel.remote = steamMergeSnapshot(steamPanel.remote, snap)
		if steamPanel.target >= len(steamPanel.remote.pcs) {
			steamPanel.target = 0
		}
		games = steamApplyRemote(games, steamPanel.remote)
	}
	steamPanel.games = games
	steamPanel.dirty = true
	steamPanelKeepFocus(focused)
	for _, g := range games {
		if !was[g.AppID] || g.steamDownloading() || g.Remote.downloading() {
			continue
		}
		switch {
		case local && g.Installed:
			PlaySuccessSound()
			ShowToast(g.Name+" finished downloading · press "+hintKeyFor("A", "Play")+" to play", ToastKindSuccess)
		case !local && g.Remote.installed:
			PlaySuccessSound()
			ShowToast(g.Name+" is ready on "+g.Remote.where, ToastKindSuccess)
		}
	}
}

// ── The list ─────────────────────────────────────────────────────────────

// steamVisibleGames is the list the library page shows, rebuilt only when
// the games, the tab, the sort or the search changed.
func steamVisibleGames() []steamGame {
	if !steamPanel.dirty {
		return steamPanel.view
	}
	steamPanel.dirty = false
	words := strings.Fields(strings.ToLower(steamPanel.query))
	out := make([]steamGame, 0, len(steamPanel.games))
	for _, g := range steamPanel.games {
		switch steamPanel.tab {
		case steamTabInstalled:
			if !g.Installed && !g.Remote.installed {
				continue
			}
		case steamTabDownloads:
			if !g.steamDownloading() && !g.Remote.downloading() {
				continue
			}
		}
		if len(words) > 0 {
			name := strings.ToLower(g.Name)
			match := true
			for _, w := range words {
				if !strings.Contains(name, w) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, g)
	}
	switch steamPanel.sortMode {
	case steamSortName:
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	case steamSortPlaytime:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Playtime != out[j].Playtime {
				return out[i].Playtime > out[j].Playtime
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
	default:
		steamSortGames(out)
	}
	steamPanel.view = out
	return out
}

func steamPanelFocused() (steamGame, bool) {
	games := steamVisibleGames()
	if steamPanel.cursor < 0 || steamPanel.cursor >= len(games) {
		return steamGame{}, false
	}
	return games[steamPanel.cursor], true
}

func steamPanelFocusedID() int {
	if g, ok := steamPanelFocused(); ok {
		return g.AppID
	}
	return 0
}

// steamPanelKeepFocus puts the cursor back on appid after the list changed.
func steamPanelKeepFocus(appid int) {
	if appid == 0 {
		return
	}
	for i, g := range steamVisibleGames() {
		if g.AppID == appid {
			steamPanel.cursor = i
			return
		}
	}
}

// steamPanelUpdateGame changes one game of the list in place.
func steamPanelUpdateGame(appid int, change func(g *steamGame)) {
	for i := range steamPanel.games {
		if steamPanel.games[i].AppID == appid {
			change(&steamPanel.games[i])
			steamPanel.dirty = true
			return
		}
	}
}

func steamPanelMove(delta int) {
	switch {
	case steamPanel.page == steamPageQR:
		return
	case steamPanel.sheet:
		rows := steamSheetRows()
		if len(rows) > 0 {
			steamPanel.sheetCursor = (steamPanel.sheetCursor + delta + len(rows)) % len(rows)
			PlayNavSound()
		}
		return
	case steamPanel.page == steamPageAccount:
		rows := steamAccountRows()
		if len(rows) == 0 {
			return
		}
		steamPanel.acCursor = (steamPanel.acCursor + delta + len(rows)) % len(rows)
		PlayNavSound()
		return
	}
	n := len(steamVisibleGames())
	if n == 0 {
		return
	}
	next := steamPanel.cursor + delta
	switch {
	case delta == 1 || delta == -1:
		next = (next + n) % n
	case next < 0:
		next = 0
	case next >= n:
		next = n - 1
	}
	if next != steamPanel.cursor {
		PlayNavSound()
	}
	steamPanel.cursor = next
}

// steamPanelPageStep is how far Left/Right jump in the library.
func steamPanelPageStep() int {
	if steamVisibleRows > 1 {
		return steamVisibleRows
	}
	return 5
}

// steamPanelSetTab switches the library tab.
func steamPanelSetTab(t steamTab) {
	if steamPanel.page != steamPageLibrary || steamPanel.sheet {
		return
	}
	t = (t%steamTabCount + steamTabCount) % steamTabCount
	if t == steamPanel.tab {
		return
	}
	focused := steamPanelFocusedID()
	steamPanel.tab = t
	steamPanel.dirty = true
	steamPanel.cursor, steamPanel.scroll = 0, 0
	steamPanelKeepFocus(focused)
	PlayToggleSound()
}

// steamPanelCycleSort steps through Recent, A–Z and Most played.
func steamPanelCycleSort() {
	if steamPanel.page != steamPageLibrary || steamPanel.sheet {
		return
	}
	focused := steamPanelFocusedID()
	steamPanel.sortMode = (steamPanel.sortMode + 1) % steamSortCount
	steamPanel.dirty = true
	steamPanel.cursor, steamPanel.scroll = 0, 0
	steamPanelKeepFocus(focused)
	PlayToggleSound()
	ShowToast("Sorted by "+steamPanel.sortMode.label(), ToastKindInfo)
}

// steamPanelOpenSearch opens the keyboard on the library's search line.
func steamPanelOpenSearch() {
	if steamPanel.page != steamPageLibrary || steamPanel.sheet {
		return
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = steamSearchVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// steamPanelSetQuery is the keyboard typing into the search: the list
// narrows on every key.
func steamPanelSetQuery(text string) {
	if r := []rune(text); len(r) > steamSearchMax {
		text = string(r[:steamSearchMax])
	}
	if text == steamPanel.query {
		return
	}
	steamPanel.query = text
	steamPanel.dirty = true
	steamPanel.cursor, steamPanel.scroll = 0, 0
}

// ── Actions ──────────────────────────────────────────────────────────────

// steamTargetPC is the PC a game that is nowhere yet would be installed on.
func steamTargetPC() (steamRemotePC, bool) {
	pcs := steamPanel.remote.pcs
	if len(pcs) == 0 {
		return steamRemotePC{}, false
	}
	i := steamPanel.target
	if i < 0 || i >= len(pcs) {
		i = 0
	}
	return pcs[i], true
}

// steamPrimaryLabel is what A does to g, as the row and the footer say it.
func steamPrimaryLabel(g steamGame) string {
	if steamPanel.local {
		switch {
		case g.steamDownloading() && g.DlPaused:
			return "Paused " + strconv.Itoa(steamPercent(g.DlDone, g.DlTotal)) + "%"
		case g.steamDownloading():
			return "Downloading " + strconv.Itoa(steamPercent(g.DlDone, g.DlTotal)) + "%"
		case g.Installed:
			return "Play"
		}
		return "Install"
	}
	r := g.Remote
	switch {
	case r.downloading():
		return "Downloading " + strconv.Itoa(steamPercent(r.dlDone, r.dlTotal)) + "% · " + r.where
	case r.installed:
		return "Play on " + r.where
	}
	if pc, ok := steamTargetPC(); ok {
		return "Install on " + pc.name
	}
	return "On PC"
}

// steamPlayFocused is A on the library: play, install or say what is going on.
func steamPlayFocused() {
	g, ok := steamPanelFocused()
	if !ok {
		switch {
		case !steamPanelHasSource():
			steamPanelShowAccount()
		case steamPanel.query != "":
			ShowToast("No game matches \""+steamPanel.query+"\" · press "+hintKeyFor("B", "Clear")+" to clear the search", ToastKindInfo)
		case steamPanel.tab != steamTabAll:
			ShowToast("Nothing here · press "+hintKeyFor("R1", "Next tab")+" for the other tabs", ToastKindInfo)
		}
		return
	}
	steamPlay(g)
}

// steamPlay starts, installs or reports g, wherever it can run.
func steamPlay(g steamGame) {
	if steamPanel.local {
		switch {
		case g.steamDownloading():
			ShowToast("Steam is still downloading "+g.Name+" ("+strconv.Itoa(steamPercent(g.DlDone, g.DlTotal))+"%)", ToastKindInfo)
			return
		case g.Installed:
			steamLocalDo(g, steamActRun)
		default:
			steamLocalDo(g, steamActInstall)
		}
		return
	}
	r := g.Remote
	switch {
	case r.downloading():
		ShowToast(g.Name+" is downloading on "+r.where+" ("+strconv.Itoa(steamPercent(r.dlDone, r.dlTotal))+"%)", ToastKindInfo)
	case r.installed:
		PlayActivateSound()
		steamRemoteDo(g, r, steamActRun, nil)
	default:
		pc, ok := steamTargetPC()
		if !ok {
			ShowToast(steamNoPCText(), ToastKindInfo)
			return
		}
		PlayActivateSound()
		st := steamRemoteState{kind: pc.kind, id: pc.id, where: pc.name}
		gen := steamPanel.gen
		steamRemoteDo(g, st, steamActInstall, func(ok bool) {
			if ok && steamPanel.open && steamPanel.gen == gen {
				steamPanel.watchUntil = time.Now().Add(15 * time.Minute)
				steamPanel.lastPoll = time.Time{}
			}
		})
	}
}

// steamLocalDo hands g to the Steam app on this computer.
func steamLocalDo(g steamGame, action steamAction) {
	if err := steamLaunchLocal(g.AppID, action); err != nil {
		ShowToast("Could not reach Steam — "+firstLine(err.Error()), ToastKindError)
		return
	}
	switch action {
	case steamActRun:
		trophyEvent(tkAppLaunch)
		PlaySuccessSound()
		ShowToast("Starting "+g.Name+" in Steam", ToastKindSuccess)
		// Last played now, so the Recent sort and the row say so at once.
		steamPanelUpdateGame(g.AppID, func(x *steamGame) { x.LastPlayed = time.Now().Unix() })
	case steamActInstall:
		PlayActivateSound()
		ShowToast("Confirm the install in Steam · the download shows up here", ToastKindInfo)
		steamPanel.watchUntil = time.Now().Add(15 * time.Minute)
	case steamActUninstall:
		PlayActivateSound()
		ShowToast("Confirm in Steam to uninstall "+g.Name, ToastKindInfo)
		steamPanel.watchUntil = time.Now().Add(5 * time.Minute)
	}
}

func steamPanelActivate() {
	switch {
	case steamPanel.page == steamPageQR:
		steamQRActivate()
	case steamPanel.sheet:
		steamSheetActivate()
	case steamPanel.page == steamPageAccount:
		steamAccountActivate()
	default:
		steamPlayFocused()
	}
}

func steamPanelShowAccount() {
	steamPanel.sheet = false
	steamPanel.page = steamPageAccount
	steamPanel.acCursor = 0
	PlayActivateSound()
}

// steamPanelBack steps back one level: the sheet, the QR code, the Account
// page, the search — and then closes the panel.
func steamPanelBack() {
	switch {
	case steamPanel.sheet:
		steamPanel.sheet = false
		PlayBackSound()
	case steamPanel.page == steamPageQR:
		steamQRStop()
		steamPanel.page = steamPageAccount
		PlayBackSound()
	case steamPanel.page == steamPageAccount && steamPanelHasSource():
		steamPanel.page = steamPageLibrary
		PlayBackSound()
	case steamPanel.page == steamPageLibrary && steamPanel.query != "":
		focused := steamPanelFocusedID()
		steamPanelSetQuery("")
		steamPanelKeepFocus(focused)
		PlayBackSound()
	default:
		closeSteamPanel()
	}
}

func steamPanelManualRefresh() {
	if steamPanel.busy {
		ShowToast("Already reading your library…", ToastKindInfo)
		return
	}
	steamPanelLoadAccount()
	if !steamPanelHasSource() {
		steamPanelShowAccount()
		return
	}
	PlayNavSound()
	steamPanelRefresh()
}

// ── Game sheet ───────────────────────────────────────────────────────────

func steamPanelOpenSheet() {
	if steamPanel.page != steamPageLibrary {
		return
	}
	if _, ok := steamPanelFocused(); !ok {
		return
	}
	steamPanel.sheet = true
	steamPanel.sheetCursor = 0
	PlayActivateSound()
}

func steamSheetRows() []steamSheetRow {
	g, ok := steamPanelFocused()
	if !ok {
		return nil
	}
	rows := []steamSheetRow{{label: steamPrimaryLabel(g), action: steamSheetPrimary}}
	if steamPanel.local && g.Installed && !g.steamDownloading() {
		rows = append(rows, steamSheetRow{label: "Uninstall", action: steamSheetUninstall})
	}
	if !steamPanel.local && !g.Remote.installed && !g.Remote.downloading() && len(steamPanel.remote.pcs) > 1 {
		pc, _ := steamTargetPC()
		rows = append(rows, steamSheetRow{label: "Install on: " + pc.name + " (change)", action: steamSheetTarget})
	}
	store := "Store page"
	if !steamPanel.local {
		store = "Store page on your phone (QR)"
	}
	rows = append(rows, steamSheetRow{label: store, action: steamSheetStore}, steamSheetRow{label: "Close", action: steamSheetClose})
	return rows
}

func steamSheetActivate() {
	g, ok := steamPanelFocused()
	rows := steamSheetRows()
	if !ok || steamPanel.sheetCursor < 0 || steamPanel.sheetCursor >= len(rows) {
		steamPanel.sheet = false
		return
	}
	switch rows[steamPanel.sheetCursor].action {
	case steamSheetPrimary:
		steamPanel.sheet = false
		steamPlay(g)
	case steamSheetUninstall:
		steamPanel.sheet = false
		steamLocalDo(g, steamActUninstall)
	case steamSheetTarget:
		if n := len(steamPanel.remote.pcs); n > 0 {
			steamPanel.target = (steamPanel.target + 1) % n
			PlayToggleSound()
		}
	case steamSheetStore:
		link := "https://store.steampowered.com/app/" + strconv.Itoa(g.AppID) + "/"
		if steamPanel.local {
			if err := openExternalURL(link); err != nil {
				ShowToast("Could not open the web browser — "+firstLine(err.Error()), ToastKindError)
				return
			}
			PlayActivateSound()
			return
		}
		code, err := steamQRModules(link)
		if err != nil {
			ShowToast("Could not draw the code", ToastKindError)
			return
		}
		qrZoomOpenCardDetail(code, g.Name+" · Steam store", "Scan with your phone to open the store page")
	case steamSheetClose:
		steamPanel.sheet = false
		PlayBackSound()
	}
}

// ── Account page ─────────────────────────────────────────────────────────

func steamAccountRows() []steamAccountRow {
	acct := steamPanel.account
	qrStatus := "QR"
	switch {
	case acct.Via == "qr" && acct.HasToken:
		qrStatus = "Signed in"
	case acct.TokenLost:
		qrStatus = "Sign in again"
	}
	rows := []steamAccountRow{{
		title:  "Sign in with the Steam app",
		detail: "Scan a code with the Steam app on your phone and approve it · private libraries, your Steam PCs, remote installs",
		status: qrStatus,
		action: steamActQR,
	}, {
		title:  "Sign in on Steam's website",
		detail: "Identifies your Steam account · add a Web API key there if Steam hides the games page",
		status: "Phone",
		action: steamActPhone,
	}}
	if IsWindows() {
		rows = append(rows, steamAccountRow{
			title:  "Sign in with this PC's browser",
			detail: "Opens Steam's sign-in page in your web browser",
			status: "Open",
			action: steamActBrowser,
		})
	}
	if steamPanel.hasApp {
		name := steamPanel.appUser.Persona
		if name == "" {
			name = steamPanel.appUser.SteamID
		}
		status := "Use"
		if acct.Via == "app" {
			status = "In use"
		}
		rows = append(rows, steamAccountRow{
			title:  "Use the Steam app's account",
			detail: name + " · signed in to Steam on this computer",
			status: status,
			action: steamActAppAccount,
		})
	}
	keyStatus := "Not set"
	if acct.HasKey {
		keyStatus = "Saved"
	}
	rows = append(rows, steamAccountRow{
		title:  "Web API key (optional)",
		detail: "Lists a private profile's games without the Steam app sign-in · send it from your phone",
		status: keyStatus,
		action: steamActKey,
	})
	if !steamPanel.local {
		pcs := steamPanel.remote.pcs
		detail := "None found · sign in with the Steam app (Steam running on the PC) or pair a JukaHub PC"
		if len(pcs) > 0 {
			names := make([]string, 0, len(pcs))
			for _, pc := range pcs {
				names = append(names, pc.name)
			}
			detail = strings.Join(names, ", ")
		}
		rows = append(rows, steamAccountRow{
			title:  "PCs to play on",
			detail: detail,
			status: strconv.Itoa(len(pcs)),
			action: steamActPCs,
		})
	}
	if acct.SteamID != "" && acct.Via != "app" {
		who := acct.Persona
		if who == "" {
			who = acct.SteamID
		}
		rows = append(rows, steamAccountRow{
			title:  "Sign out",
			detail: "Forget " + who + ", the sign-in, the key and the library on this device",
			action: steamActSignOut,
		})
	}
	return rows
}

func steamAccountActivate() {
	rows := steamAccountRows()
	if steamPanel.acCursor < 0 || steamPanel.acCursor >= len(rows) {
		return
	}
	switch rows[steamPanel.acCursor].action {
	case steamActQR:
		steamPanel.page = steamPageQR
		PlayActivateSound()
		steamQRBegin()
	case steamActPhone, steamActKey:
		if phoneCardOpen(phoneCardSteam, "Steam · sign in with your phone",
			"Scan with your phone (same Wi-Fi), tap Sign in through Steam — or paste a profile link or a Web API key") {
			PlayActivateSound()
		}
	case steamActBrowser:
		companionStart()
		if companion.url == "" || !strings.Contains(companion.url, "/?p=") {
			msg := companion.err
			if msg == "" {
				msg = "The sign-in page needs the Phone Companion, which is not running"
			}
			ShowToast(msg, ToastKindWarning)
			return
		}
		link := steamLocalSignInURL(companion.url)
		if err := openExternalURL(link); err != nil {
			ShowToast("Could not open the web browser — "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
		ShowToast("Sign in on Steam's page in your browser; JukaHub picks it up by itself", ToastKindInfo)
	case steamActAppAccount:
		if err := steamUseAppAccount(); err != nil {
			ShowToast("Could not save that: "+err.Error(), ToastKindError)
			return
		}
		PlaySuccessSound()
		steamPanelAccountChanged()
		ShowToast("Using the Steam app's account", ToastKindSuccess)
	case steamActPCs:
		steamPanelManualRefresh()
		ShowToast("Looking for your PCs…", ToastKindInfo)
	case steamActSignOut:
		if err := steamSignOut(); err != nil {
			ShowToast("Could not sign out: "+err.Error(), ToastKindError)
			return
		}
		PlayBackSound()
		steamPanel.games = nil
		steamPanel.dirty = true
		steamPanel.cursor, steamPanel.scroll, steamPanel.acCursor = 0, 0, 0
		// Reload and read again: on a PC the Steam app's installed games stay.
		steamPanelAccountChanged()
		steamPanel.page = steamPageAccount
		ShowToast("Signed out of Steam", ToastKindInfo)
	}
}

// steamQRActivate is A on the QR page: a new code after a failure, else the
// code full screen for the phone's camera.
func steamQRActivate() {
	if steamQR.err != "" || !steamQR.active {
		PlayActivateSound()
		steamQRBegin()
		return
	}
	if len(steamQR.bitmap) == 0 {
		return
	}
	qrZoomOpenCardDetail(steamQR.bitmap, steamQRTitle, "Steam app → Steam Guard (shield) → Scan a QR code, then Approve")
}

// steamQRModules is link as a QR code's modules.
func steamQRModules(link string) ([][]bool, error) {
	code, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	return code.Bitmap(), nil
}

// steamLocalSignInURL turns the companion's pairing link into this PC's own
// sign-in address: 127.0.0.1 and the same port (the LAN name may not resolve
// on the PC itself), /steam instead of /, the same PIN.
func steamLocalSignInURL(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return strings.Replace(link, "/?p=", "/steam?p=", 1)
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort("127.0.0.1", port)
	} else {
		u.Host = "127.0.0.1"
	}
	u.Path = "/steam"
	u.Fragment = ""
	return u.String()
}

// ── Input ────────────────────────────────────────────────────────────────

func ctrlSteamPanel(e *sdl.ControllerButtonEvent) bool {
	lib := steamPanel.page == steamPageLibrary && !steamPanel.sheet
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		steamPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		steamPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if lib {
			steamPanelMove(-steamPanelPageStep())
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if lib {
			steamPanelMove(steamPanelPageStep())
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		steamPanelSetTab(steamPanel.tab - 1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		steamPanelSetTab(steamPanel.tab + 1)
	case sdl.CONTROLLER_BUTTON_A:
		steamPanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		switch {
		case lib:
			steamPanelShowAccount()
		case steamPanel.page != steamPageLibrary:
			steamPanelBack()
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if lib {
			steamPanelOpenSheet()
		} else if steamPanel.sheet {
			steamPanelBack()
		}
	case sdl.CONTROLLER_BUTTON_START:
		steamPanelManualRefresh()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		steamPanelBack()
	}
	return true
}

// steamPanelTrigger is L2 (search) and R2 (sort) on the library.
func steamPanelTrigger(s padShoulder) {
	switch s {
	case padL2:
		steamPanelOpenSearch()
	case padR2:
		steamPanelCycleSort()
	}
}

func handleSteamKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !steamPanel.open {
		return false
	}
	lib := steamPanel.page == steamPageLibrary && !steamPanel.sheet
	switch e.Keysym.Sym {
	case sdl.K_UP:
		steamPanelMove(-1)
	case sdl.K_DOWN:
		steamPanelMove(1)
	case sdl.K_PAGEUP, sdl.K_LEFT:
		if lib {
			steamPanelMove(-steamPanelPageStep())
		}
	case sdl.K_PAGEDOWN, sdl.K_RIGHT:
		if lib {
			steamPanelMove(steamPanelPageStep())
		}
	case sdl.K_TAB:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			steamPanelSetTab(steamPanel.tab - 1)
		} else {
			steamPanelSetTab(steamPanel.tab + 1)
		}
	case sdl.K_1, sdl.K_2, sdl.K_3:
		steamPanelSetTab(steamTab(e.Keysym.Sym - sdl.K_1))
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		steamPanelActivate()
	case sdl.K_x:
		switch {
		case lib:
			steamPanelShowAccount()
		case steamPanel.page != steamPageLibrary:
			steamPanelBack()
		}
	case sdl.K_y, sdl.K_i:
		if lib {
			steamPanelOpenSheet()
		} else if steamPanel.sheet {
			steamPanelBack()
		}
	case sdl.K_s:
		steamPanelCycleSort()
	case sdl.K_f, sdl.K_SLASH, sdl.K_F3:
		steamPanelOpenSearch()
	case sdl.K_r, sdl.K_F5:
		steamPanelManualRefresh()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		steamPanelBack()
	}
	return true
}

func handleSteamMouse(mx, my int32) bool {
	if !steamPanel.open {
		return false
	}
	if steamPillRect.W > 0 && pointInRect(mx, my, steamPillRect) {
		switch {
		case steamPanel.sheet:
			steamPanel.sheet = false
		case steamPanel.page == steamPageLibrary:
			steamPanelShowAccount()
		default:
			steamPanelBack()
		}
		return true
	}
	if steamPanel.page == steamPageQR && steamQRRect.W > 0 && pointInRect(mx, my, steamQRRect) {
		steamQRActivate()
		return true
	}
	if steamPanel.page == steamPageLibrary && !steamPanel.sheet {
		for i, r := range steamTabRects {
			if r.W > 0 && pointInRect(mx, my, r) {
				steamPanelSetTab(steamTab(i))
				return true
			}
		}
	}
	for _, hit := range steamRowHits {
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		switch {
		case steamPanel.sheet:
			steamPanel.sheetCursor = hit.index
		case steamPanel.page == steamPageAccount:
			steamPanel.acCursor = hit.index
		case hit.index != steamPanel.cursor:
			// A game starts or installs on the second tap: the first one
			// only picks it.
			steamPanel.cursor = hit.index
			PlayNavSound()
			return true
		}
		steamPanelActivate()
		return true
	}
	if steamPanelRect.W > 0 && pointInRect(mx, my, steamPanelRect) {
		return true
	}
	if steamPanel.sheet {
		steamPanel.sheet = false
		return true
	}
	closeSteamPanel()
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// steamPlaytimeText is "45 min" or "12.5 h" of play.
func steamPlaytimeText(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes < 60:
		return strconv.Itoa(minutes) + " min played"
	case minutes < 600:
		return strconv.FormatFloat(float64(minutes)/60, 'f', 1, 64) + " h played"
	}
	return strconv.Itoa(minutes/60) + " h played"
}

// steamLastPlayedText is "last played Nov 3" (this year) or "Mar 2023".
func steamLastPlayedText(ts int64) string {
	if ts <= 0 {
		return ""
	}
	t := time.Unix(ts, 0)
	if t.Year() == time.Now().Year() {
		return "last played " + t.Format("Jan 2")
	}
	return "last played " + t.Format("Jan 2006")
}

// steamWhereText says where g is installed or downloading.
func steamWhereText(g steamGame) string {
	if steamPanel.local {
		switch {
		case g.steamDownloading():
			return humanBytes(g.DlDone) + " of " + humanBytes(g.DlTotal)
		case g.Installed && g.Size > 0:
			return "Installed · " + humanBytes(g.Size)
		case g.Installed:
			return "Installed"
		}
		return "Not installed"
	}
	r := g.Remote
	switch {
	case r.downloading() && r.dlTotal > 1:
		return humanBytes(r.dlDone) + " of " + humanBytes(r.dlTotal) + " on " + r.where
	case r.downloading():
		return "Queued on " + r.where
	case r.installed:
		return "Installed on " + r.where
	}
	return ""
}

func steamGameDetail(g steamGame) string {
	var parts []string
	if s := steamWhereText(g); s != "" {
		parts = append(parts, s)
	}
	if s := steamPlaytimeText(g.Playtime); s != "" {
		parts = append(parts, s)
	}
	if s := steamLastPlayedText(g.LastPlayed); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "Never played"
	}
	return strings.Join(parts, " · ")
}

// steamGameProgress is g's download as done, total and paused, or total 0.
func steamGameProgress(g steamGame) (int64, int64, bool) {
	if g.steamDownloading() {
		return g.DlDone, g.DlTotal, g.DlPaused
	}
	if g.Remote.downloading() {
		return g.Remote.dlDone, g.Remote.dlTotal, g.Remote.paused
	}
	return 0, 0, false
}

func steamPanelSubtitle() string {
	acct := steamPanel.account
	var parts []string
	switch {
	case acct.Persona != "":
		parts = append(parts, acct.Persona)
	case acct.SteamID != "":
		parts = append(parts, "Steam ID "+acct.SteamID)
	case steamPanel.local:
		parts = append(parts, "Steam app on this PC")
	default:
		parts = append(parts, "Not signed in")
	}
	if steamPanel.page != steamPageLibrary {
		if acct.SteamID != "" {
			via := map[string]string{"qr": "signed in with the Steam app", "openid": "signed in through Steam",
				"profile": "public profile", "app": "the Steam app's account"}[acct.Via]
			if via != "" {
				parts = append(parts, via)
			}
		}
		return strings.Join(parts, " · ")
	}
	if len(steamPanel.games) > 0 {
		parts = append(parts, pluralizeCount(len(steamPanel.games), "game", "games"))
	}
	if !steamPanel.local {
		if n := len(steamPanel.remote.pcs); n > 0 {
			parts = append(parts, pluralizeCount(n, "PC", "PCs"))
		}
	}
	parts = append(parts, steamPanel.sortMode.label())
	if steamPanel.query != "" {
		parts = append(parts, "\""+steamPanel.query+"\"")
	}
	switch {
	case steamPanel.busy:
		parts = append(parts, "updating…")
	case steamPanel.err != "":
		parts = append(parts, steamPanel.err)
	}
	return strings.Join(parts, " · ")
}

// renderSteamPanel draws the library, the game sheet, the Account page or
// the QR sign-in. Row hits are rebuilt every frame so a tap always lands on
// what is drawn.
func renderSteamPanel(renderer *sdl.Renderer, config *Config) {
	if !steamPanel.open || renderer == nil {
		return
	}
	steamPanelTick()
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const padX = OverlayPadX
	rowH := 8 + medH + 4 + smallH + 8

	title := "Steam"
	switch steamPanel.page {
	case steamPageAccount:
		title = "Steam · Account"
	case steamPageQR:
		title = "Steam · Sign in with the Steam app"
	}
	header := UIPanelHeader(title, steamPanelSubtitle(), "gamepad")
	if steamPanel.err != "" && steamPanel.page == steamPageLibrary && !steamPanel.busy {
		header.SubtitleColor = WarningInk()
	}
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := 20 + smallH
	tabsH := int32(0)
	if steamPanel.page == steamPageLibrary {
		tabsH = smallH + 18
	}

	panelW := int32(960)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	games := steamVisibleGames()
	accountRows := steamAccountRows()
	if steamPanel.acCursor >= len(accountRows) {
		steamPanel.acCursor = len(accountRows) - 1
	}
	if steamPanel.acCursor < 0 {
		steamPanel.acCursor = 0
	}
	if steamPanel.cursor >= len(games) {
		steamPanel.cursor = len(games) - 1
	}
	if steamPanel.cursor < 0 {
		steamPanel.cursor = 0
	}
	if steamPanel.sheet && len(games) == 0 {
		steamPanel.sheet = false
	}

	var bodyH int32
	visible := int32(0)
	switch {
	case steamPanel.page == steamPageQR:
		bodyH = min32(screenHeight-80-headerH-footerH, 340)
	case steamPanel.page == steamPageAccount:
		bodyH = rowH*int32(len(accountRows)) + 12 + 2*(smallH+4)
	case len(games) == 0:
		bodyH = tabsH + 18 + medH + 4 + 2*(smallH+4) + 18
	default:
		maxRows := (screenHeight - 40 - headerH - footerH - tabsH) / rowH
		if maxRows < 3 {
			maxRows = 3
		}
		visible = int32(len(games))
		if visible > maxRows {
			visible = maxRows
		}
		bodyH = tabsH + visible*rowH
	}
	steamVisibleRows = int(visible)
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	steamPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if visible > 0 {
		if steamPanel.cursor < steamPanel.scroll {
			steamPanel.scroll = steamPanel.cursor
		}
		if steamPanel.cursor >= steamPanel.scroll+int(visible) {
			steamPanel.scroll = steamPanel.cursor - int(visible) + 1
		}
		if steamPanel.scroll > len(games)-int(visible) {
			steamPanel.scroll = len(games) - int(visible)
		}
		if steamPanel.scroll < 0 {
			steamPanel.scroll = 0
		}
	} else {
		steamPanel.scroll = 0
	}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, steamPanelRect)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, steamPanelRect)
	UIPageHeaderDraw(renderer, config, header)
	steamDrawPill(renderer, config, panelX, panelY, panelW)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	steamRowHits = steamRowHits[:0]
	steamTabRects = [steamTabCount]sdl.Rect{}
	steamQRRect = sdl.Rect{}
	drawRow := func(r sdl.Rect, focused bool) {
		bg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, bg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
	}

	switch {
	case steamPanel.page == steamPageQR:
		steamDrawQRPage(renderer, config, font, medium, sdl.Rect{X: panelX + padX, Y: panelY + headerH, W: rowW, H: bodyH})
	case steamPanel.page == steamPageAccount:
		y := panelY + headerH
		for i, row := range accountRows {
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			steamRowHits = append(steamRowHits, steamRowHit{index: i, rect: r})
			drawRow(r, i == steamPanel.acCursor)
			statusW := int32(0)
			if row.status != "" {
				color := ColorTextTertiary()
				switch row.status {
				case "In use", "Saved", "Signed in":
					color = ToastSuccessInk()
				case "Sign in again":
					color = WarningInk()
				}
				sw, _, _ := font.SizeUTF8(row.status)
				statusW = int32(sw) + 12
				renderText(renderer, config, font, row.status, color, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)
			}
			renderText(renderer, config, medium, ellipsize(medium, row.title, r.W-32-statusW), ColorTextPrimary(), r.X+16, r.Y+8)
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
			y += rowH
		}
		y += 12
		for _, line := range []string{
			"Signing in happens in the Steam app or on Steam's own page: JukaHub never sees your password.",
			"Without the Steam app sign-in, set Game details to Public in Steam's privacy settings.",
		} {
			if y+smallH > panelY+panelH-footerH {
				break
			}
			renderText(renderer, config, font, ellipsize(font, line, rowW), ColorTextSecondary(), panelX+padX, y)
			y += smallH + 4
		}
	default:
		steamDrawTabs(renderer, config, font, panelX+padX, panelY+headerH, rowW)
		if len(games) == 0 {
			steamDrawEmpty(renderer, config, font, medium, panelX+padX, panelY+headerH+tabsH+6, rowW)
			break
		}
		thumbH := rowH - 8 - 12
		thumbW := thumbH * 231 / 87
		for i := int32(0); i < visible; i++ {
			idx := steamPanel.scroll + int(i)
			g := games[idx]
			y := panelY + headerH + tabsH + i*rowH
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			steamRowHits = append(steamRowHits, steamRowHit{index: idx, rect: r})
			drawRow(r, idx == steamPanel.cursor && !steamPanel.sheet)

			thumb := sdl.Rect{X: r.X + 10, Y: r.Y + 6, W: thumbW, H: thumbH}
			steamDrawCover(renderer, config, thumb, g, medium)
			textX := thumb.X + thumb.W + 14

			status := steamPrimaryLabel(g)
			statusColor := ColorTextTertiary()
			switch {
			case strings.HasPrefix(status, "Play"):
				statusColor = ToastSuccessInk()
			case strings.HasPrefix(status, "Install"):
				statusColor = accentColor
			case strings.HasPrefix(status, "Downloading"), strings.HasPrefix(status, "Paused"):
				statusColor = WarningInk()
			}
			status = ellipsize(font, status, r.W/3)
			sw, _, _ := font.SizeUTF8(status)
			renderText(renderer, config, font, status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)
			nameW := r.X + r.W - 16 - int32(sw) - 12 - textX
			renderText(renderer, config, medium, ellipsize(medium, g.Name, nameW), ColorTextPrimary(), textX, r.Y+8)
			renderText(renderer, config, font, ellipsize(font, steamGameDetail(g), r.X+r.W-16-textX),
				ColorTextTertiary(), textX, r.Y+8+medH+4)
			if done, total, paused := steamGameProgress(g); total > 0 {
				bar := sdl.Rect{X: textX, Y: r.Y + r.H - 6, W: r.X + r.W - 16 - textX, H: 3}
				steamDrawBar(renderer, bar, done, total, paused)
			}
		}
		if steamPanel.sheet {
			steamDrawSheet(renderer, config, font, medium, steamPanelRect)
		}
	}

	var hint string
	switch {
	case steamPanel.page == steamPageQR:
		label := "Enlarge"
		if steamQR.err != "" || !steamQR.active {
			label = "New code"
		}
		hint = hintLine(FooterHint{"A", label}, FooterHint{"B", "Back"})
	case steamPanel.page == steamPageAccount && steamPanelHasSource():
		hint = hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Library"})
	case steamPanel.page == steamPageAccount:
		hint = hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
	case steamPanel.sheet:
		hint = hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Close"})
	case len(games) == 0:
		hint = hintLine(FooterHint{"L1 / R1", "Tabs"}, FooterHint{"X", "Account"}, FooterHint{"Start / F5", "Refresh"}, FooterHint{"B", "Back"})
	default:
		action := "Play"
		if g, ok := steamPanelFocused(); ok {
			action = steamPrimaryLabel(g)
			if i := strings.Index(action, " · "); i > 0 {
				action = action[:i]
			}
		}
		back := "Back"
		if steamPanel.query != "" {
			back = "Clear search"
		}
		hint = hintLine(FooterHint{"A", action}, FooterHint{"Y", "Options"}, FooterHint{"L1 / R1", "Tabs"},
			FooterHint{"L2 / F", "Search"}, FooterHint{"R2 / S", "Sort"}, FooterHint{"X", "Account"}, FooterHint{"B", back})
	}
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*padX), ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}

// steamDrawTabs draws All / Installed / Downloads with their counts.
func steamDrawTabs(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32) {
	counts := [steamTabCount]int{}
	for _, g := range steamPanel.games {
		counts[steamTabAll]++
		if g.Installed || g.Remote.installed {
			counts[steamTabInstalled]++
		}
		if g.steamDownloading() || g.Remote.downloading() {
			counts[steamTabDownloads]++
		}
	}
	h := int32(font.Height()) + 10
	cx := x
	for t := steamTab(0); t < steamTabCount; t++ {
		label := t.label() + " " + strconv.Itoa(counts[t])
		tw, _, _ := font.SizeUTF8(label)
		r := sdl.Rect{X: cx, Y: y + 2, W: int32(tw) + 24, H: h}
		if r.X+r.W > x+w {
			break
		}
		steamTabRects[t] = r
		color := ColorTextSecondary()
		if t == steamPanel.tab {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(installHighlight(), 60))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, installHighlight())
			color = ColorTextPrimary()
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(ColorSurfaceRow, 160))
		}
		renderText(renderer, config, font, label, color, r.X+12, r.Y+5)
		cx += r.W + 8
	}
}

// steamDrawEmpty says why the tab is empty and what to press.
func steamDrawEmpty(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, x, y, w int32) {
	headline, line1, line2 := "No games yet", "", ""
	switch {
	case steamPanel.busy && len(steamPanel.games) == 0:
		headline, line1 = "Reading your Steam library…", "This takes a moment the first time."
	case steamPanel.query != "":
		headline, line1 = "No game matches \""+steamPanel.query+"\"", "Press "+hintKeyFor("B", "Clear")+" to clear the search, or "+hintKeyFor("L2", "Search")+" to change it."
	case steamPanel.tab == steamTabInstalled && len(steamPanel.games) > 0:
		headline = "Nothing installed"
		if steamPanel.local {
			line1 = "Pick a game under All games and press " + hintKeyFor("A", "Install") + "."
		} else {
			line1 = "Games installed on your PCs show here · " + hintKeyFor("A", "Install") + " on a game puts it on a PC."
		}
	case steamPanel.tab == steamTabDownloads:
		headline, line1 = "Nothing downloading", "Installs started here show their progress in this tab."
	case steamPanel.err != "":
		headline, line1, line2 = "Could not read the library", steamPanel.err, "Press "+hintKeyFor("Start", "Refresh")+" to try again, or "+hintKeyFor("X", "Account")+" for sign-in options."
	case steamPanel.account.SteamID != "":
		line1 = "This account has no games to show yet · press " + hintKeyFor("Start", "Refresh") + " to check again."
	default:
		line1 = "Press " + hintKeyFor("X", "Account") + " to sign in, then " + hintKeyFor("Start", "Refresh") + "."
	}
	renderText(renderer, config, medium, ellipsize(medium, headline, w), ColorTextPrimary(), x, y)
	y += int32(medium.Height()) + 4
	for _, line := range []string{line1, line2} {
		if line == "" {
			continue
		}
		renderText(renderer, config, font, ellipsize(font, line, w), ColorTextTertiary(), x, y)
		y += int32(font.Height()) + 4
	}
}

// steamDrawBar is a thin download bar; amber while paused.
func steamDrawBar(renderer *sdl.Renderer, r sdl.Rect, done, total int64, paused bool) {
	if r.W <= 4 {
		return
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 1, WithAlpha(ColorTextTertiary(), 60))
	fill := int32(int64(r.W) * int64(steamPercent(done, total)) / 100)
	if fill < 2 {
		fill = 2
	}
	color := accentColor
	if paused {
		color = WarningInk()
	}
	fillRoundedRect(renderer, r.X, r.Y, fill, r.H, 1, color)
}

// steamDrawSheet draws the focused game's sheet over the list: the cover,
// what is known about it, and its actions.
func steamDrawSheet(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, card sdl.Rect) {
	g, ok := steamPanelFocused()
	if !ok {
		return
	}
	rows := steamSheetRows()
	if steamPanel.sheetCursor >= len(rows) {
		steamPanel.sheetCursor = len(rows) - 1
	}
	if steamPanel.sheetCursor < 0 {
		steamPanel.sheetCursor = 0
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	rowH := medH + 14
	w := min32(card.W-48, 720)
	coverW := min32(w/2-24, 360)
	coverH := coverW * 215 / 460
	infoH := max32(coverH, medH+6+3*(smallH+6))
	h := 20 + infoH + 16 + int32(len(rows))*(rowH+6) + 14
	if h > card.H-16 {
		h = card.H - 16
	}
	box := sdl.Rect{X: card.X + (card.W-w)/2, Y: card.Y + (card.H-h)/2, W: w, H: h}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 18, sdl.Color{R: 0, G: 0, B: 0, A: 110})
	drawOverlayCard(renderer, box)

	cover := sdl.Rect{X: box.X + 20, Y: box.Y + 20, W: coverW, H: coverH}
	steamDrawCover(renderer, config, cover, g, medium)
	tx := cover.X + cover.W + 18
	tw := box.X + box.W - 20 - tx
	y := cover.Y
	renderText(renderer, config, medium, ellipsize(medium, g.Name, tw), ColorTextPrimary(), tx, y)
	y += medH + 6
	lines := []string{steamWhereText(g), steamPlaytimeText(g.Playtime), steamLastPlayedText(g.LastPlayed)}
	if lines[1] == "" && lines[2] == "" {
		lines[1] = "Never played"
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		renderText(renderer, config, font, ellipsize(font, capitalizeFirst(line), tw), ColorTextSecondary(), tx, y)
		y += smallH + 6
	}
	if done, total, paused := steamGameProgress(g); total > 0 {
		steamDrawBar(renderer, sdl.Rect{X: tx, Y: y + 2, W: tw, H: 5}, done, total, paused)
	}

	y = box.Y + 20 + infoH + 16
	steamRowHits = steamRowHits[:0]
	focus := installHighlight()
	for i, row := range rows {
		if y+rowH > box.Y+box.H-8 {
			break
		}
		r := sdl.Rect{X: box.X + 20, Y: y, W: box.W - 40, H: rowH}
		steamRowHits = append(steamRowHits, steamRowHit{index: i, rect: r})
		bg := WithAlpha(ColorSurfaceRow, 190)
		if i == steamPanel.sheetCursor {
			bg = WithAlpha(focus, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, bg)
		if i == steamPanel.sheetCursor {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, focus)
		}
		renderText(renderer, config, medium, ellipsize(medium, row.label, r.W-28), ColorTextPrimary(), r.X+14, r.Y+7)
		y += rowH + 6
	}
}

// steamDrawQRPage draws the Steam app sign-in: the code on the left, the
// steps and the status on the right.
func steamDrawQRPage(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, band sdl.Rect) {
	side := min32(band.H-16, band.W/2)
	tile := sdl.Rect{X: band.X, Y: band.Y + 8, W: side, H: side}
	core := steamQR.core
	if n := int32(len(core)); n > 0 {
		module := headerQRModule(side, n)
		if module >= 1 {
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, max32(6, side/16), sdl.Color{R: 255, G: 255, B: 255, A: 255})
			code := module * n
			rects := qrModuleRects(core, tile.X+(side-code)/2, tile.Y+(side-code)/2, module)
			if len(rects) > 0 {
				_ = renderer.SetDrawColor(0, 0, 0, 255)
				_ = renderer.FillRects(rects)
			}
			steamQRRect = tile
		}
	} else {
		fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 16, WithAlpha(ColorSurfaceRow, 190))
		msg := "Getting a code…"
		if steamQR.err != "" {
			msg = "No code"
		}
		mw, mh, _ := font.SizeUTF8(msg)
		renderText(renderer, config, font, msg, ColorTextTertiary(), tile.X+(side-int32(mw))/2, tile.Y+(side-int32(mh))/2)
	}

	x := tile.X + side + 24
	maxW := band.X + band.W - x
	if maxW <= 40 {
		return
	}
	y := tile.Y + 4
	renderText(renderer, config, medium, ellipsize(medium, "Scan with the Steam app", maxW), ColorTextPrimary(), x, y)
	y += int32(medium.Height()) + 10
	lineH := int32(font.Height()) + 8
	for _, step := range []string{
		"1  Open the Steam app on your phone",
		"2  Tap the Steam Guard shield",
		"3  Tap Scan a QR code, point it here",
		"4  Approve the sign-in",
	} {
		renderText(renderer, config, font, ellipsize(font, step, maxW), ColorTextSecondary(), x, y)
		y += lineH
	}
	y += 8
	status, color := steamQR.status, ColorTextTertiary()
	if steamQR.scanned {
		color = ToastSuccessInk()
	}
	if steamQR.err != "" {
		status, color = steamQR.err, WarningInk()
	}
	if status != "" {
		renderText(renderer, config, font, ellipsize(font, status, maxW), color, x, y)
		y += lineH
	}
	renderText(renderer, config, font, ellipsize(font, "Your password stays in the Steam app.", maxW), ColorTextTertiary(), x, y)
}

// steamDrawPill is the header's Account/Library switch, for touch.
func steamDrawPill(renderer *sdl.Renderer, config *Config, panelX, panelY, panelW int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		steamPillRect = sdl.Rect{}
		return
	}
	label := "Account"
	switch {
	case steamPanel.sheet:
		label = "Close"
	case steamPanel.page == steamPageQR:
		label = "Back"
	case steamPanel.page == steamPageAccount:
		if !steamPanelHasSource() {
			steamPillRect = sdl.Rect{}
			return
		}
		label = "Library"
	}
	w, _, _ := font.SizeUTF8(label)
	pad := int32(12)
	r := sdl.Rect{X: panelX + panelW - 24 - int32(w) - pad*2, Y: panelY + 14, W: int32(w) + pad*2, H: int32(font.Height()) + 10}
	steamPillRect = r
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(ColorSurfaceRow, 200))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, ColorBorderDefault)
	renderText(renderer, config, font, label, ColorTextSecondary(), r.X+pad, r.Y+5)
}

// steamDrawCover draws a game's capsule art, or a glass badge with its
// initial while the picture is on its way (or when Steam has none).
func steamDrawCover(renderer *sdl.Renderer, config *Config, box sdl.Rect, g steamGame, medium *ttf.Font) {
	if tex := appIconTexture(renderer, steamArtFor(g.AppID)); tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			// Cover the box: crop the longer side instead of letterboxing.
			src := sdl.Rect{W: tw, H: th}
			if tw*box.H > th*box.W {
				src.W = th * box.W / box.H
				src.X = (tw - src.W) / 2
			} else {
				src.H = tw * box.H / box.W
				src.Y = (th - src.H) / 2
			}
			_ = renderer.Copy(tex, &src, &box)
			return
		}
	}
	drawGlassBadge(renderer, box)
	if medium == nil {
		return
	}
	glyph := appGridGlyph(g.Name)
	gw, gh, _ := medium.SizeUTF8(glyph)
	renderText(renderer, config, medium, glyph, ColorTextPrimary(), box.X+(box.W-int32(gw))/2, box.Y+(box.H-int32(gh))/2)
}
