package main

// raoffline_ui.go — the player's side of Offline RetroAchievements
// (raoffline.go): the Settings rows, the wait for Wi-Fi before an
// achievement game, and "Cache recently played".
//
// Settings → RetroAchievements gets two rows after the in-game ones
// (raach_settings.go):
//
//   - Offline achievements (On by default): Left/Right switch
//     it; A opens a sheet with On/Off and the actions — "Send N unlocks now"
//     while some wait, "Cache recently played games", "Clear the offline
//     cache". The line under it says how many games are ready offline and how
//     many unlocks wait. The numbers come from atomics the store publishes,
//     so drawing never touches the disk or a lock the proxy holds.
//   - Wait for Wi-Fi before achievement games (On by default): right after a
//     wake the radio takes a few seconds to reconnect, and a RetroArch game
//     started in those seconds would sign in to RetroAchievements without it.
//     A launch held here (a gate in gamelaunch_hooks.go) waits up to
//     raoWaitMax for the internet, with a toast, then starts on its own. A
//     softcore game ready offline does not wait (a hardcore one does: offline
//     it would play in Casual), nor one started with the
//     radio off or no reconnect under way, nor the resume at power-on;
//     pressing Play again starts it at once. A wait that ran out is not
//     repeated for raoWaitQuiet, so a handheld far from any known network is
//     not held at every start.
//
// Cache recently played. The proxy learns a game when RetroArch plays it
// online once. This fetches, for the last raoCacheRecentMax recent games, the
// same answers RetroArch would ask for — the game ID for its hash, the
// achievement set — with the sign-in the proxy kept, so they work offline the
// first time. The game hash is RetroAchievements' own, which this computes
// for plain cartridge consoles only (raoffline_hash.go); disc games, arcade
// and the rest need one online play. No session is started on the server
// ahead: offline, such a game starts with an empty unlock list, so an
// achievement the account already had may pop up again (the server then
// takes it as already unlocked).

import (
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// raoWaitMax is the longest an achievement game waits for Wi-Fi.
	raoWaitMax = 8 * time.Second
	// raoWaitQuiet: after a wait that ran out, the next starts do not wait
	// for this long.
	raoWaitQuiet = 2 * time.Minute
	// raoCacheRecentMax is how many recent games Cache recently played looks at.
	raoCacheRecentMax = 25

	raoActSend  = "+send"
	raoActCache = "+cache"
	raoActClear = "+clear"
)

// ── Settings ─────────────────────────────────────────────────────────────

// raOfflineSettingsRows are the rows Settings → RetroAchievements adds after
// its own (setmodel.go).
func raOfflineSettingsRows() []setRow {
	return []setRow{
		{
			ID: raoKeyEnabled, Label: "Offline achievements", Kind: setKindChoice,
			Detail: raoSettingDetail,
			Options: func(*Config) []setOption {
				return []setOption{{Value: "on", Label: "On"}, {Value: "off", Label: "Off"}}
			},
			Current: func(c *Config) int {
				if raoEnabled(c) {
					return 0
				}
				return 1
			},
			Sheet:      raoSheetOptions,
			SheetValue: func(c *Config) string { return strings.ToLower(setOnOff(raoEnabled(c))) },
			Choose:     raoSheetChoose,
		},
		{
			ID: raoKeyWaitWifi, Label: "Wait for Wi-Fi before achievement games", Kind: setKindToggle,
			Detail: setStatic("Right after waking, a RetroArch game with achievements waits up to 8 s for Wi-Fi to sign in"),
			Get:    raoWaitEnabled,
			Set: func(c *Config, on bool) {
				setStoreBool(c, raoKeyWaitWifi, on)
				setToast("Wait for Wi-Fi: " + setOnOff(on))
			},
		},
	}
}

// raoUnlocksWord is "1 unlock" or "N unlocks".
func raoUnlocksWord(n int64) string {
	if n == 1 {
		return "1 unlock"
	}
	return strconv.FormatInt(n, 10) + " unlocks"
}

// raoGamesWord is "1 game" or "N games".
func raoGamesWord(n int64) string {
	if n == 1 {
		return "1 game"
	}
	return strconv.FormatInt(n, 10) + " games"
}

// raoSettingDetail is the line under the row.
func raoSettingDetail(c *Config) string {
	pending := raoCounts.pending.Load()
	if !raoEnabled(c) {
		if pending > 0 {
			return "Off · " + raoUnlocksWord(pending) + " earned offline still to send"
		}
		return "Off · RetroArch talks to retroachievements.org directly"
	}
	var parts []string
	if games := raoCounts.games.Load(); games > 0 {
		parts = append(parts, raoGamesWord(games)+" ready offline (Casual)")
	} else {
		parts = append(parts, "Play a game online once and it works offline (Casual)")
	}
	if pending > 0 {
		parts = append(parts, raoUnlocksWord(pending)+" waiting to be sent")
	}
	return strings.Join(parts, " · ")
}

// raoSheetOptions are the sheet A opens: the switch and the actions.
func raoSheetOptions(*Config) []setOption {
	out := []setOption{
		{Value: "on", Label: "On", Note: "Achievements keep unlocking with no internet, in Casual, and are sent later"},
		{Value: "off", Label: "Off", Note: "RetroArch talks to retroachievements.org directly"},
	}
	if n := raoCounts.pending.Load(); n > 0 {
		out = append(out, setOption{Value: raoActSend, Label: "Send " + raoUnlocksWord(n) + " now",
			Note: "Unlocks earned offline go to retroachievements.org"})
	}
	out = append(out, setOption{Value: raoActCache, Label: "Cache recently played games",
		Note: "Achievement data for your recent cartridge games, so they work offline at once"})
	if g := raoCounts.games.Load(); g > 0 {
		out = append(out, setOption{Value: raoActClear, Label: "Clear the offline cache",
			Note: raoGamesWord(g) + " · unlocks waiting to be sent are kept"})
	}
	return out
}

// raoSheetChoose applies a pick from the sheet (or Left/Right's on/off).
func raoSheetChoose(c *Config, value string) {
	switch value {
	case "on", "off":
		on := value == "on"
		setStoreBool(c, raoKeyEnabled, on)
		setToast("Offline achievements: " + setOnOff(on))
	case raoActSend:
		raoSendNow()
	case raoActCache:
		raoCacheRecentStart()
	case raoActClear:
		setToast("Clearing the offline achievements cache…")
		go func() {
			n := raoClearCache()
			tbxPost(func() {
				ShowToast("Offline achievements cache cleared ("+raoGamesWord(int64(n))+")", ToastKindInfo)
			})
		}()
	}
}

// raoSendNow sends the waiting unlocks now, when there is internet.
func raoSendNow() {
	switch {
	case raoCounts.pending.Load() == 0:
		setToast("No unlocks are waiting to be sent")
	case !IsOnline():
		setToast("No internet: the unlocks are sent when you're back online")
	default:
		setToast("Sending the unlocks earned offline…")
		raoReplaySendNow()
	}
}

// ── Wait for Wi-Fi ───────────────────────────────────────────────────────

// raoWait is the launch held for Wi-Fi; main thread. gen tells a wait that
// was called off from the current one; resume lets the launch it starts
// through the gate.
var raoWait struct {
	pending  bool
	gen      uint64
	resume   string
	gaveUpAt time.Time
}

// raoWifiComingUp reports a radio that is on and on its way to a network: a
// wake's reconnect, or the supplicant associating or still looking.
func raoWifiComingUp() bool {
	if netReconnecting() {
		return true
	}
	switch saverWifiNow(time.Now()).kind {
	case saverWifiConnecting, saverWifiDisconnected:
		return true
	}
	return false
}

// raoWifiGate is the launch path's gate (gamelaunch_hooks.go). Main thread.
func raoWifiGate(game libraryGame, launcher string) (held bool, err error) {
	if raoWait.resume != "" && raoWait.resume == game.Path {
		raoWait.resume = ""
		return false, nil
	}
	if raoWait.pending {
		// Play pressed again while waiting: that start goes now.
		raoWait.pending = false
		raoWait.gen++
		return false, nil
	}
	if hakuxUnasked || !raoWaitEnabled(appConfig) || IsOnline() {
		return false, nil
	}
	if !raoWait.gaveUpAt.IsZero() && time.Since(raoWait.gaveUpAt) < raoWaitQuiet {
		return false, nil
	}
	if !raoWifiComingUp() || !retroArchLauncher(launcher) {
		return false, nil
	}
	// A profile's games have achievements off (profiles_saves.go).
	if a := profilesCurrent(); a != nil && a.Dir != "" {
		return false, nil
	}
	// RetroArch's config as this launch will have it: with the choices of
	// Settings → RetroAchievements (raach_settings.go), which are written
	// after the gates.
	cfg, _ := raScopeApplyText(raoLaunchConfig(launcher), raachLaunchValues(appConfig))
	if !raoAchievementsOn(cfg) {
		return false, nil
	}
	// A softcore game ready offline plays from the cache as it is. A hardcore
	// one waits: offline it would drop to Casual (raoPlanFor), and the Wi-Fi
	// coming back within seconds keeps it hardcore.
	if raoEnabled(appConfig) && raoGameReadyOffline(game.Path) && raoCfgString(cfg, raoHostKey) == "" &&
		!raoCfgBool(cfg, "cheevos_hardcore_mode_enable", true) {
		return false, nil
	}
	raoWait.pending = true
	raoWait.gen++
	gen := raoWait.gen
	logInfo("raoffline", "%s waits up to %s for Wi-Fi to sign in to RetroAchievements", game.Title, raoWaitMax)
	tbxPost(func() {
		if raoWait.pending && raoWait.gen == gen {
			ShowToast("Waiting for Wi-Fi for RetroAchievements… Play again starts now", ToastKindInfo)
		}
	})
	go raoWaitForWifi(game, launcher, gen)
	return true, nil
}

// raoWaitForWifi waits for the internet, then starts the game from the main
// loop unless the wait was called off or another game took the screen.
func raoWaitForWifi(game libraryGame, launcher string, gen uint64) {
	deadline := time.Now().Add(raoWaitMax)
	netNudge()
	for !IsOnline() {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		if left > 500*time.Millisecond {
			left = 500 * time.Millisecond
		}
		t := time.NewTimer(left)
		select {
		case <-netMon.probeSignal():
		case <-t.C:
		}
		t.Stop()
	}
	online := IsOnline()
	tbxPost(func() {
		if !raoWait.pending || raoWait.gen != gen {
			return
		}
		raoWait.pending = false
		if !online {
			raoWait.gaveUpAt = time.Now()
			logInfo("raoffline", "no Wi-Fi after %s; %s starts without it", raoWaitMax, game.Title)
		}
		if gameScreenSuspended() {
			return
		}
		raoWait.resume = game.Path
		err := launchLibraryGame(game, launcher)
		raoWait.resume = ""
		if err != nil {
			gameLaunchErrorToast(game.Title, err)
		}
	})
}

// ── Cache recently played ────────────────────────────────────────────────

// raoCaching is up while Cache recently played runs.
var raoCaching atomic.Bool

// raoPrefetchResult is what Cache recently played made of one game.
type raoPrefetchResult int

const (
	raoPrefetchReady   raoPrefetchResult = iota // its achievements are cached
	raoPrefetchNone                             // RetroAchievements has none for it
	raoPrefetchSkipped                          // not hashable here, or no answer
)

// raoCacheRecentStart is the sheet's "Cache recently played games". Main
// thread; the fetching runs on its own.
func raoCacheRecentStart() {
	if raoCaching.Load() {
		setToast("Already caching your recent games…")
		return
	}
	if !IsOnline() {
		setToast("Caching games for offline needs the internet")
		return
	}
	login, ok := raoLastLogin()
	if !ok || login.Token == "" {
		setToast("Play one achievement game with Wi-Fi first, so JukaHub has RetroArch's sign-in")
		return
	}
	recent := recentGames()
	if len(recent) > raoCacheRecentMax {
		recent = recent[:raoCacheRecentMax]
	}
	if len(recent) == 0 {
		setToast("No recently played games yet")
		return
	}
	raoCaching.Store(true)
	setToast("Caching your recent games for offline achievements…")
	go func() {
		defer raoCaching.Store(false)
		ready, none, skipped := 0, 0, 0
		for _, g := range recent {
			switch raoPrefetchGame(g.Path, login) {
			case raoPrefetchReady:
				ready++
			case raoPrefetchNone:
				none++
			default:
				skipped++
			}
		}
		msg := raoGamesWord(int64(ready)) + " ready for offline achievements"
		if none > 0 {
			msg += " · " + raoGamesWord(int64(none)) + " without achievements"
		}
		if skipped > 0 {
			msg += " · " + raoGamesWord(int64(skipped)) + " need one online play first"
		}
		logInfo("raoffline", "cache recently played: %d ready, %d without achievements, %d skipped", ready, none, skipped)
		kind := ToastKindSuccess
		if ready == 0 {
			kind = ToastKindInfo
		}
		tbxPost(func() { ShowToast(msg, kind) })
	}()
}

// raoFetch sends a request JukaHub builds and returns a successful answer.
func raoFetch(form url.Values, agent string) ([]byte, bool) {
	status, body, err := raoPostForm(form, agent)
	if err != nil || status != 200 || !raoSuccess(body) {
		return nil, false
	}
	return body, true
}

// raoPrefetchGame caches one recent game's achievements. Off the main thread.
func raoPrefetchGame(path string, login raoLogin) raoPrefetchResult {
	if !regularFile(path) {
		return raoPrefetchSkipped
	}
	hash := raoPathHash(path)
	if hash == "" {
		hash = raoRomHash(path)
	}
	if hash == "" {
		return raoPrefetchSkipped
	}
	game := raoHashGame(hash)
	if game <= 0 {
		body, ok := raoFetch(url.Values{"r": {"gameid"}, "m": {hash}}, login.Agent)
		if !ok {
			return raoPrefetchSkipped
		}
		game = raoJSONInt(body, "GameID")
		raoCachePut("gameid:"+hash, body, 0)
		if game <= 0 {
			raoNotePathHash(path, hash, 0)
			return raoPrefetchNone
		}
	}
	raoNotePathHash(path, hash, game)
	got := false
	id := strconv.Itoa(game)
	if body, ok := raoFetch(url.Values{"r": {"patch"}, "u": {login.User}, "t": {login.Token}, "g": {id}}, login.Agent); ok {
		raoCachePut("patch:"+id, body, game)
		got = true
	}
	// Newer RetroArch asks for the set by hash in one request.
	if body, ok := raoFetch(url.Values{"r": {"achievementsets"}, "u": {login.User}, "t": {login.Token}, "m": {hash}}, login.Agent); ok {
		raoCachePut("sets:"+hash, body, game)
		got = true
	}
	if !got {
		return raoPrefetchSkipped
	}
	return raoPrefetchReady
}
