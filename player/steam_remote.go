package main

// steam_remote.go — Steam games on the user's other computers.
//
// A handheld has no Steam, so what it can play is what a PC has. Two ways to
// reach one:
//
//   - The account's own Steam PCs (needs the Steam app sign-in, steam_auth.go):
//     Steam's IClientCommService lists every computer signed in to Steam right
//     now, what each has installed and is downloading, and installs or starts
//     a game there — the same remote download the Steam app on a phone does.
//     Nothing has to run on the PC but Steam.
//   - A paired JukaHub PC (Nearby devices, peernet.go): /peer/v1/steam/library
//     reports its installed and downloading games, and /peer/v1/steam/run
//     starts one or opens Steam's install dialog there.
//
// steamRemoteSnapshotFetch asks both in parallel; steamApplyRemote folds the
// answer into the library so each row says where the game can be played.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	steamClientCommAPI = "https://api.steampowered.com/IClientCommService/"
	// steamPeerLibraryPath is the paired-device call listing a PC's games.
	steamPeerLibraryPath = "/peer/v1/steam/library"
	// steamPeerLibraryMax bounds the games one PC reports.
	steamPeerLibraryMax = 4000
)

// steamRemoteKind is how a remote PC is reached.
type steamRemoteKind uint8

const (
	steamRemoteNone steamRemoteKind = iota
	// steamRemoteSteam is a Steam PC of the account (ClientComm).
	steamRemoteSteam
	// steamRemotePeer is a paired JukaHub PC.
	steamRemotePeer
)

// steamRemoteState is where a game is outside this computer.
type steamRemoteState struct {
	kind      steamRemoteKind
	id        string // client_instanceid or peer ID
	where     string // the PC's name
	installed bool
	dlDone    int64
	dlTotal   int64
	paused    bool
}

func (r steamRemoteState) downloading() bool { return r.dlTotal > 0 && r.dlDone < r.dlTotal }

// steamRemoteApp is one game a remote PC reported.
type steamRemoteApp struct {
	appid     int
	name      string
	installed bool
	dlDone    int64
	dlTotal   int64
	paused    bool
}

// steamRemotePC is one computer that can install or start games.
type steamRemotePC struct {
	kind steamRemoteKind
	id   string
	name string
	apps []steamRemoteApp
	// misses counts looks in a row that did not reach it (steamMergeSnapshot).
	misses int
}

// steamRemoteSnapshot is one look at every remote PC.
type steamRemoteSnapshot struct {
	pcs []steamRemotePC
	// steamErr is why the account's Steam PCs could not be listed, if a
	// sign-in was there to list them with.
	steamErr error
	when     time.Time
}

// ── Steam's ClientComm service ───────────────────────────────────────────

// steamClientComm calls one IClientCommService method with the access token
// and decodes the "response" object into out.
func steamClientComm(ctx context.Context, method, token string, params url.Values, post bool, out interface{}) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("access_token", token)
	params.Set("format", "json")
	var req *http.Request
	var err error
	if post {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, steamClientCommAPI+method+"/v1/", strings.NewReader(params.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, steamClientCommAPI+method+"/v1/?"+params.Encode(), nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "okhttp/4.9.2")
	resp, err := steamHTTPClient().Do(req)
	if err != nil {
		return steamRequestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &steamHTTPError{code: resp.StatusCode}
	}
	if s := strings.TrimSpace(resp.Header.Get("X-eresult")); s != "" {
		if r, err := strconv.Atoi(s); err == nil && steamEResult(r) != steamEROK {
			return &steamAuthError{result: steamEResult(r)}
		}
	}
	var wrap struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, steamPageLimit)).Decode(&wrap); err != nil {
		return err
	}
	if out == nil || len(wrap.Response) == 0 {
		return nil
	}
	return json.Unmarshal(wrap.Response, out)
}

// steamListSteamPCs lists the account's computers signed in to Steam now,
// with what each has installed or is downloading.
func steamListSteamPCs(ctx context.Context, token string) ([]steamRemotePC, error) {
	var logon struct {
		Sessions []struct {
			ID      steamFlexUint `json:"client_instanceid"`
			Machine string        `json:"machine_name"`
			OS      string        `json:"os_name"`
		} `json:"sessions"`
	}
	if err := steamClientComm(ctx, "GetAllClientLogonInfo", token, nil, false, &logon); err != nil {
		return nil, err
	}
	var pcs []steamRemotePC
	for _, s := range logon.Sessions {
		if s.ID == 0 || len(pcs) >= 8 {
			continue
		}
		name := steamCleanText(s.Machine, 40)
		if name == "" {
			name = steamCleanText(s.OS, 40)
		}
		if name == "" {
			name = "your Steam PC"
		}
		pc := steamRemotePC{kind: steamRemoteSteam, id: strconv.FormatUint(uint64(s.ID), 10), name: name}
		pc.apps = steamListSteamPCApps(ctx, token, pc.id)
		pcs = append(pcs, pc)
	}
	return pcs, nil
}

// steamClientApp is one app of GetClientAppList.
type steamClientApp struct {
	AppID     int           `json:"appid"`
	Name      string        `json:"app"`
	Installed bool          `json:"installed"`
	Changing  bool          `json:"changing"`
	Paused    bool          `json:"download_paused"`
	Done      steamFlexUint `json:"bytes_downloaded"`
	Total     steamFlexUint `json:"bytes_to_download"`
	AppType   string        `json:"app_type"`
}

// steamListSteamPCApps is what one Steam PC has installed or is changing
// (downloading, updating). A PC that does not answer lists nothing.
func steamListSteamPCApps(ctx context.Context, token, instance string) []steamRemoteApp {
	seen := map[int]int{}
	var apps []steamRemoteApp
	for _, filter := range []string{"installed", "changing"} {
		var body struct {
			Apps []steamClientApp `json:"apps"`
		}
		params := url.Values{}
		params.Set("client_instanceid", instance)
		params.Set("fields", "games")
		params.Set("filters", filter)
		params.Set("language", "english")
		if err := steamClientComm(ctx, "GetClientAppList", token, params, false, &body); err != nil {
			logDebug("steam", "app list of PC %s (%s): %v", instance, filter, err)
			continue
		}
		for _, a := range body.Apps {
			if a.AppID <= 0 || len(apps) >= steamPeerLibraryMax {
				continue
			}
			if a.AppType != "" && !strings.EqualFold(a.AppType, "game") && !strings.EqualFold(a.AppType, "demo") {
				continue
			}
			ra := steamRemoteApp{appid: a.AppID, name: steamCleanText(a.Name, 120), installed: a.Installed, paused: a.Paused}
			if total := int64(a.Total); total > 0 && int64(a.Done) < total {
				ra.dlDone, ra.dlTotal = int64(a.Done), total
			} else if a.Changing && !a.Installed {
				// Queued, no byte count yet: a bar at 0%.
				ra.dlTotal = 1
			}
			if i, ok := seen[a.AppID]; ok {
				if ra.dlTotal > 0 {
					apps[i].dlDone, apps[i].dlTotal, apps[i].paused = ra.dlDone, ra.dlTotal, ra.paused
				}
				apps[i].installed = apps[i].installed || ra.installed
				continue
			}
			seen[a.AppID] = len(apps)
			apps = append(apps, ra)
		}
	}
	return apps
}

// steamSteamPCAction installs or starts appid on a Steam PC.
func steamSteamPCAction(ctx context.Context, instance string, appid int, action steamAction) error {
	token, _, err := steamAccessToken(ctx)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("client_instanceid", instance)
	params.Set("appid", strconv.Itoa(appid))
	method := "LaunchClientApp"
	switch action {
	case steamActInstall:
		method = "InstallClientApp"
	case steamActUninstall:
		method = "UninstallClientApp"
	default:
		params.Set("query_params", "")
	}
	return steamClientComm(ctx, method, token, params, true, nil)
}

// ── Paired JukaHub PCs ───────────────────────────────────────────────────

// steamPeerGame is one game in /peer/v1/steam/library.
type steamPeerGame struct {
	AppID     int    `json:"appid"`
	Name      string `json:"name"`
	Installed bool   `json:"installed,omitempty"`
	Size      int64  `json:"size,omitempty"`
	DlDone    int64  `json:"dlDone,omitempty"`
	DlTotal   int64  `json:"dlTotal,omitempty"`
	Paused    bool   `json:"paused,omitempty"`
}

// steamPeerLibrary is the answer of /peer/v1/steam/library.
type steamPeerLibrary struct {
	Steam bool            `json:"steam"`
	Games []steamPeerGame `json:"games"`
}

// steamHandlePeerLibrary lists the games the Steam app on this computer has
// installed or is downloading, for a paired handheld. Only paired devices
// reach it (peerGuard).
func steamHandlePeerLibrary(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	out := steamPeerLibrary{Games: []steamPeerGame{}}
	if root := steamRoot(); root != "" && steamClientPresent() {
		out.Steam = true
		user, _ := steamFindLocalUser()
		for _, g := range steamInstalledGames(root, user.SteamID, false) {
			if !g.Installed && !g.steamDownloading() {
				continue
			}
			if len(out.Games) >= steamPeerLibraryMax {
				break
			}
			out.Games = append(out.Games, steamPeerGame{AppID: g.AppID, Name: g.Name, Installed: g.Installed,
				Size: g.Size, DlDone: g.DlDone, DlTotal: g.DlTotal, Paused: g.DlPaused})
		}
	}
	peerWriteJSONResponse(w, http.StatusOK, out)
}

// steamListPeerPCs asks every paired device for its Steam games; the ones
// with Steam come back, the most recently seen first.
func steamListPeerPCs(ctx context.Context) []steamRemotePC {
	peers := peerPairedList()
	if len(peers) == 0 {
		return nil
	}
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].LastSeen.After(peers[j].LastSeen) })
	if len(peers) > 6 {
		peers = peers[:6]
	}
	found := make([]*steamRemotePC, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p peerRecord) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			var lib steamPeerLibrary
			if err := peerDoJSON(pctx, p.ID, http.MethodGet, steamPeerLibraryPath, nil, &lib, 2<<20); err != nil || !lib.Steam {
				return
			}
			name := steamCleanText(p.Name, 40)
			if name == "" {
				name = "your PC"
			}
			pc := &steamRemotePC{kind: steamRemotePeer, id: p.ID, name: name}
			for _, g := range lib.Games {
				if g.AppID <= 0 || len(pc.apps) >= steamPeerLibraryMax {
					continue
				}
				a := steamRemoteApp{appid: g.AppID, name: steamCleanText(g.Name, 120), installed: g.Installed, paused: g.Paused}
				if g.DlTotal > 0 && g.DlDone >= 0 && g.DlDone < g.DlTotal {
					a.dlDone, a.dlTotal = g.DlDone, g.DlTotal
				}
				pc.apps = append(pc.apps, a)
			}
			found[i] = pc
		}(i, p)
	}
	wg.Wait()
	var out []steamRemotePC
	for _, pc := range found {
		if pc != nil {
			out = append(out, *pc)
		}
	}
	return out
}

// ── Both together ────────────────────────────────────────────────────────

// steamRemoteSnapshotFetch asks the account's Steam PCs (when token is set)
// and the paired JukaHub PCs, in parallel. Any goroutine.
func steamRemoteSnapshotFetch(ctx context.Context, token string) steamRemoteSnapshot {
	snap := steamRemoteSnapshot{when: time.Now()}
	var steamPCs, peerPCs []steamRemotePC
	var wg sync.WaitGroup
	if token != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			steamPCs, snap.steamErr = steamListSteamPCs(sctx, token)
			if snap.steamErr != nil {
				logWarn("steam", "listing the account's Steam PCs failed: %v", snap.steamErr)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		peerPCs = steamListPeerPCs(ctx)
	}()
	wg.Wait()
	snap.pcs = append(steamPCs, peerPCs...)
	return snap
}

// steamRemoteMaxMisses is how many looks in a row a PC may miss (a dropped
// request, a slow peer) before it leaves the list.
const steamRemoteMaxMisses = 3

// steamMergeSnapshot keeps the PCs of old that next did not reach, for a few
// looks, so one failed request does not empty the Installed tab.
func steamMergeSnapshot(old, next steamRemoteSnapshot) steamRemoteSnapshot {
	for _, pc := range old.pcs {
		found := false
		for _, n := range next.pcs {
			if n.kind == pc.kind && n.id == pc.id {
				found = true
				break
			}
		}
		if found || pc.misses+1 >= steamRemoteMaxMisses {
			continue
		}
		pc.misses++
		next.pcs = append(next.pcs, pc)
	}
	return next
}

// steamApplyRemote marks each game with the best remote PC for it — one that
// has it installed, else one downloading it — and adds the games a PC has
// that the account list lacks (no sign-in on the handheld). Steam PCs come
// before paired ones.
func steamApplyRemote(games []steamGame, snap steamRemoteSnapshot) []steamGame {
	kept := games[:0]
	for _, g := range games {
		if g.RemoteOnly {
			continue
		}
		g.Remote = steamRemoteState{}
		kept = append(kept, g)
	}
	games = kept
	at := make(map[int]int, len(games))
	for i := range games {
		at[games[i].AppID] = i
	}
	added := false
	for _, pc := range snap.pcs {
		for _, a := range pc.apps {
			if !a.installed && a.dlTotal == 0 {
				continue
			}
			st := steamRemoteState{kind: pc.kind, id: pc.id, where: pc.name, installed: a.installed,
				dlDone: a.dlDone, dlTotal: a.dlTotal, paused: a.paused}
			i, ok := at[a.appid]
			if !ok {
				if len(games) >= steamMaxGames {
					continue
				}
				name := a.name
				if name == "" {
					name = "App " + strconv.Itoa(a.appid)
				}
				at[a.appid] = len(games)
				games = append(games, steamGame{AppID: a.appid, Name: name, Remote: st, RemoteOnly: true})
				added = true
				continue
			}
			cur := games[i].Remote
			better := cur.kind == steamRemoteNone ||
				(st.installed && !st.downloading() && (!cur.installed || cur.downloading()))
			if better {
				games[i].Remote = st
			}
		}
	}
	if added {
		steamSortGames(games)
	}
	return games
}

// steamRemoteDo runs action for g on the PC st names, on a goroutine, and
// reports with a toast. Main thread.
func steamRemoteDo(g steamGame, st steamRemoteState, action steamAction, done func(ok bool)) {
	if st.kind == steamRemoteNone {
		ShowToast(steamNoPCText(), ToastKindInfo)
		return
	}
	verb := "Starting "
	if action == steamActInstall {
		verb = "Installing "
	}
	ShowToast(verb+g.Name+" on "+st.where+"…", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		switch st.kind {
		case steamRemoteSteam:
			err = steamSteamPCAction(ctx, st.id, g.AppID, action)
		case steamRemotePeer:
			err = peerDoJSON(ctx, st.id, http.MethodPost, steamPeerRunPath, steamPeerRun{AppID: g.AppID, Name: g.Name, Action: string(action)}, nil, 0)
		}
		tbxPost(func() {
			if err != nil {
				msg := steamErrorText(err)
				if st.kind == steamRemotePeer {
					msg = peerErrorText(err, st.where)
				}
				logWarn("steam", "%s %d on %s failed: %v", action, g.AppID, st.where, err)
				ShowToast("Could not reach "+st.where+" — "+msg, ToastKindError)
				if done != nil {
					done(false)
				}
				return
			}
			switch {
			case action == steamActInstall && st.kind == steamRemotePeer:
				ShowToast("Steam opened the install dialog on "+st.where+" · confirm it there", ToastKindInfo)
			case action == steamActInstall:
				ShowToast(g.Name+" is downloading on "+st.where, ToastKindSuccess)
			default:
				trophyEvent(tkAppLaunch)
				ShowToast("Starting "+g.Name+" on "+st.where, ToastKindSuccess)
			}
			if done != nil {
				done(true)
			}
		})
	}()
}

// steamNoPCText says how a handheld gets a PC to play on.
func steamNoPCText() string {
	return "Steam games run on a PC · sign in with the Steam app (X → Account) to use your Steam PCs, or pair a PC under Devices"
}
