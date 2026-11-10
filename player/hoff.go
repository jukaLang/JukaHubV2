package main

// hoff.go — handoff: pick up on one paired device what you were doing on
// another, without a server.
//
// Each device keeps "what I am doing" — the game being played, a video or a
// song with its position, the page open in the Browser, the game picked in the
// library — sampled every few seconds on the main loop. Paired devices learn
// it two ways over the paired HTTPS connection (peernet.go):
//
//   - pull: when Home comes up on a device (at start, after a scene change, or
//     when the idle screen ends), it asks the paired devices nearby what they
//     are doing;
//   - push: when the activity changes, or the idle screen starts (the moment a
//     user often reaches for the other device), it tells the paired devices it
//     can reach. "Send to <device>" in the Start menu pushes on purpose.
//
// The other device's Home then shows a glass card, "Continue <title> from
// <device>?" (hoffcard.go): A continues — for a game, its newest saves come
// over first (ssync.go) — and B dismisses. Nothing ever switches on its own.
// An activity is offered for 30 minutes, and only while this device has not
// been used since. The TrimUI Smart Pro has no NFC, so the "tap" is the LAN.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// hoffOffersKey is Settings → Network → "Continue from other devices"
	// (on by default; it only does anything once a device is paired).
	hoffOffersKey = "hoffOffers"
	// hoffSettingsKey is "Bring my settings along": theme, text size and clock
	// travel with a handoff (off by default; both devices must allow it).
	hoffSettingsKey = "hoffShareSettings"
	// hoffWindow is how long an activity can be continued elsewhere.
	hoffWindow = 30 * time.Minute
	// hoffTitleLimit bounds a title from another device.
	hoffTitleLimit = 160
)

var (
	// hoffOffersFlag mirrors "Continue from other devices" for the goroutines.
	hoffOffersFlag atomic.Bool
	// hoffSettingsFlag mirrors "Bring my settings along".
	hoffSettingsFlag atomic.Bool
	// hoffPulling keeps pulls from piling up.
	hoffPulling atomic.Bool
)

// hoffActivity is one thing a device is (or was last) doing. At is when it
// was last current, on the sending device's clock.
type hoffActivity struct {
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	System   string            `json:"system,omitempty"`
	Path     string            `json:"path,omitempty"`
	URL      string            `json:"url,omitempty"`
	Position float64           `json:"pos,omitempty"`
	Duration float64           `json:"dur,omitempty"`
	At       int64             `json:"at"`
	Settings map[string]string `json:"settings,omitempty"`
}

// hoffMessage is the wire form: Now is the sender's clock, so the receiver can
// tell how old the activity is without trusting either clock to agree.
type hoffMessage struct {
	V        int           `json:"v"`
	Now      int64         `json:"now"`
	Activity *hoffActivity `json:"activity,omitempty"`
	Sent     bool          `json:"sent,omitempty"`
}

// hoffOffer is the latest activity a paired device reported.
type hoffOffer struct {
	PeerID   string
	PeerName string
	Activity hoffActivity
	// When is when the activity was last current, on this device's clock.
	When time.Time
	// Sent is set when the other device sent it on purpose ("Send to").
	Sent bool
}

var hoffState struct {
	mu sync.Mutex
	// current is this device's activity; hasCur is false until there is one.
	current hoffActivity
	hasCur  bool
	// lastLive is the last time something was going on here.
	lastLive time.Time
	settings map[string]string
	// pushedSig is the activity last pushed; pushDue is when the next push is
	// due (zero when none is).
	pushedSig string
	pushDue   time.Time
	pullAt    time.Time
	offers    map[string]hoffOffer
	dismissed map[string]bool
}

// ── Settings ──────────────────────────────────────────────────────────────

func hoffSettingOffers(c *Config) bool { return peerCustomBool(c, hoffOffersKey, true) }

func hoffSettingShareSettings(c *Config) bool { return peerCustomBool(c, hoffSettingsKey, false) }

// hoffSettingSetOffers is the Settings row "Continue from other devices".
func hoffSettingSetOffers(c *Config, on bool) {
	setStoreBool(c, hoffOffersKey, on)
	hoffOffersFlag.Store(on)
	if on {
		ShowToast("Home offers to continue what you were doing on a paired device", ToastKindInfo)
		return
	}
	ShowToast("Home no longer offers to continue from other devices", ToastKindInfo)
}

// hoffSettingSetShareSettings is the Settings row "Bring my settings along".
func hoffSettingSetShareSettings(c *Config, on bool) {
	setStoreBool(c, hoffSettingsKey, on)
	hoffSettingsFlag.Store(on)
	if on {
		ShowToast("Theme, text size and clock travel with a handoff (when both devices allow it)", ToastKindInfo)
		return
	}
	ShowToast("Settings stay on each device", ToastKindInfo)
}

// ── Activity ──────────────────────────────────────────────────────────────

// hoffKindLabel names a kind of activity for the card.
func hoffKindLabel(kind string) string {
	switch kind {
	case "game":
		return "Game"
	case "video":
		return "Video"
	case "music":
		return "Music"
	case "web":
		return "Web page"
	case "library":
		return "Game library"
	}
	return "Activity"
}

// hoffKindIcon is the shared glyph for a kind.
func hoffKindIcon(kind string) string {
	switch kind {
	case "game", "library":
		return "gamepad"
	case "video":
		return "video"
	case "music":
		return "music"
	case "web":
		return "globe"
	}
	return "continue"
}

// hoffBaseName is a file or link's last part without its extension: a title
// when nothing better is known. Both separators are understood.
func hoffBaseName(p string) string {
	p = strings.TrimSpace(p)
	if isHTTPMediaURL(p) {
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
	}
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if ext := filepath.Ext(p); ext != "" && len(ext) <= 6 {
		p = strings.TrimSuffix(p, ext)
	}
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return strings.TrimSpace(p)
}

// hoffHost is a web page's title for the card: its host and the start of its
// path.
func hoffHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	out := strings.TrimPrefix(u.Host, "www.")
	if p := strings.Trim(u.Path, "/"); p != "" {
		out += "/" + p
	}
	if runes := []rune(out); len(runes) > 60 {
		out = string(runes[:59]) + "…"
	}
	return out
}

// hoffLastGame is the game launched most recently on this device.
func hoffLastGame() (recentGame, bool) {
	games := recentGames()
	if len(games) == 0 || strings.TrimSpace(games[0].Path) == "" {
		return recentGame{}, false
	}
	return games[0], true
}

// hoffGameActivity is a game as an activity.
func hoffGameActivity(g recentGame, at time.Time) hoffActivity {
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = gameDisplayTitle(g.Path)
	}
	return hoffActivity{Kind: "game", Title: title, System: g.System, Path: g.Path, At: at.Unix()}
}

// hoffVideoTitle is the title the recently-played list keeps for a video.
func hoffVideoTitle(src string) string {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, it := range favoritesStore.Recent {
		if it.Type != "video" {
			continue
		}
		if v, ok := it.Data.(VideoInfo); ok && v.GetURL() == src && strings.TrimSpace(v.Title) != "" {
			return strings.TrimSpace(v.Title)
		}
	}
	return ""
}

// hoffVideoActivity is the video playing (or paused) now.
func hoffVideoActivity(now time.Time) (hoffActivity, bool) {
	st := snapshotVideoState()
	if st.phase != "playing" && st.phase != "paused" {
		return hoffActivity{}, false
	}
	src := strings.TrimSpace(currentPlaybackURL)
	if src == "" {
		return hoffActivity{}, false
	}
	act := hoffActivity{Kind: "video", At: now.Unix(), Duration: st.duration}
	if st.duration > 0 {
		act.Position = st.progress * st.duration
	}
	if looksLikeStreamURL(src) {
		act.URL = src
	} else {
		act.Path = src
	}
	act.Title = hoffVideoTitle(src)
	if act.Title == "" {
		act.Title = hoffBaseName(src)
	}
	return act, act.Title != ""
}

// hoffMusicActivity is the track playing (or paused) now.
func hoffMusicActivity(now time.Time) (hoffActivity, bool) {
	music.mu.Lock()
	defer music.mu.Unlock()
	if !music.playing || music.current < 0 || music.current >= len(music.tracks) {
		return hoffActivity{}, false
	}
	t := music.tracks[music.current]
	act := hoffActivity{Kind: "music", Position: music.position, Duration: music.duration, At: now.Unix()}
	if isHTTPMediaURL(t.Path) {
		act.URL = t.Path
	} else {
		act.Path = t.Path
	}
	act.Title = hoffBaseName(t.Name)
	if act.Title == "" {
		act.Title = hoffBaseName(t.Path)
	}
	return act, act.Title != ""
}

// hoffWebActivity is the page open in the Browser, while it is on screen.
func hoffWebActivity(config *Config, now time.Time) (hoffActivity, bool) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
		config.Scenes[currentSceneIndex].Name != browserSceneName {
		return hoffActivity{}, false
	}
	raw, _ := config.Variables.Custom[textBrowserURLVariable].(string)
	raw = strings.TrimSpace(raw)
	// A search engine's start page is the Browser at rest (browser_engines.go).
	if !isHTTPMediaURL(raw) || brwIsStartPage(config, raw) {
		return hoffActivity{}, false
	}
	return hoffActivity{Kind: "web", Title: hoffHost(raw), URL: raw, At: now.Unix()}, true
}

// hoffCapture is what this device is doing right now, read on the main loop.
// It reports false when nothing is going on (menus do not count).
func hoffCapture(config *Config, now time.Time) (hoffActivity, bool) {
	if gameScreenSuspended() {
		if g, ok := hoffLastGame(); ok {
			return hoffGameActivity(g, now), true
		}
	}
	if act, ok := hoffVideoActivity(now); ok {
		return act, true
	}
	if act, ok := hoffMusicActivity(now); ok {
		return act, true
	}
	if act, ok := hoffWebActivity(config, now); ok {
		return act, true
	}
	if gameLib.open {
		if g, ok := selectedLibraryGame(); ok && strings.TrimSpace(g.Title) != "" {
			return hoffActivity{Kind: "library", Title: g.Title, System: g.System, Path: g.Path, At: now.Unix()}, true
		}
	}
	return hoffActivity{}, false
}

// hoffCaptureSettings is the small settings set that travels with a handoff,
// when "Bring my settings along" is on.
func hoffCaptureSettings(config *Config) map[string]string {
	if config == nil || !hoffSettingsFlag.Load() {
		return nil
	}
	out := map[string]string{
		"clock12":   boolString(config.Variables.Clock12),
		"textScale": strconv.FormatFloat(a11y.fontScale, 'f', 2, 64),
	}
	if name, ok := config.Variables.Custom["theme_preset"].(string); ok && strings.TrimSpace(name) != "" {
		out["theme"] = strings.TrimSpace(name)
	}
	return out
}

// hoffSanitizeSettings keeps only the settings a handoff may carry.
func hoffSanitizeSettings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	if v := partyCleanLabel(in["theme"], 40); v != "" {
		out["theme"] = v
	}
	if v := in["clock12"]; v == "true" || v == "false" {
		out["clock12"] = v
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(in["textScale"]), 64); err == nil && f >= 0.5 && f <= 3 {
		out["textScale"] = strconv.FormatFloat(f, 'f', 2, 64)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hoffSanitize bounds and checks an activity from another device. Only links
// a player or the Browser can open are kept: http(s) for pages, stream schemes
// for media.
func hoffSanitize(a hoffActivity) (hoffActivity, bool) {
	switch a.Kind {
	case "game", "video", "music", "web", "library":
	default:
		return hoffActivity{}, false
	}
	a.Title = partyCleanLabel(a.Title, hoffTitleLimit)
	a.System = partyCleanLabel(a.System, 60)
	a.Path = partyCleanLabel(a.Path, 1024)
	a.URL = strings.TrimSpace(a.URL)
	switch {
	case len(a.URL) > 2048:
		a.URL = ""
	case a.Kind == "web" && !isHTTPMediaURL(a.URL):
		a.URL = ""
	case a.Kind != "web" && a.URL != "" && !looksLikeStreamURL(a.URL):
		a.URL = ""
	}
	if a.Kind == "web" && a.URL == "" {
		return hoffActivity{}, false
	}
	if math.IsNaN(a.Position) || math.IsInf(a.Position, 0) || a.Position < 0 || a.Position > 48*3600 {
		a.Position = 0
	}
	if math.IsNaN(a.Duration) || math.IsInf(a.Duration, 0) || a.Duration < 0 || a.Duration > 48*3600 {
		a.Duration = 0
	}
	if a.Title == "" {
		if a.Kind == "web" {
			a.Title = hoffHost(a.URL)
		} else {
			a.Title = partyCleanLabel(hoffBaseName(a.Path+a.URL), hoffTitleLimit)
		}
	}
	if a.Title == "" || a.At <= 0 {
		return hoffActivity{}, false
	}
	a.Settings = hoffSanitizeSettings(a.Settings)
	return a, true
}

// hoffSignature changes when an activity is worth telling the other devices
// about: a new thing, or a playhead that moved half a minute.
func hoffSignature(a hoffActivity) string {
	return a.Kind + "|" + a.Title + "|" + a.URL + "|" + a.Path + "|" + strconv.Itoa(int(a.Position/30))
}

// hoffSameActivity reports whether two activities are the same thing.
func hoffSameActivity(a, b hoffActivity) bool {
	return a.Kind == b.Kind && a.Title == b.Title && a.URL == b.URL && a.Path == b.Path
}

// ── Offers ────────────────────────────────────────────────────────────────

// hoffOfferKey identifies one offer, for dismissing it.
func hoffOfferKey(o hoffOffer) string {
	return o.PeerID + "|" + o.Activity.Kind + "|" + o.Activity.Title + "|" + strconv.FormatInt(o.Activity.At, 10)
}

// hoffStoreOffer records what a paired device reported. senderNow is the
// sender's clock when it reported, so the age needs neither clock to be right.
func hoffStoreOffer(peerID, peerName string, act hoffActivity, senderNow int64, sent bool) {
	now := time.Now()
	when := now
	if senderNow > 0 && act.At > 0 && act.At <= senderNow {
		when = now.Add(-time.Duration(senderNow-act.At) * time.Second)
	}
	o := hoffOffer{PeerID: peerID, PeerName: peerName, Activity: act, When: when, Sent: sent}
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	if hoffState.offers == nil {
		hoffState.offers = make(map[string]hoffOffer)
	}
	// A later pull of the same activity must not undo "Send to".
	if prev, ok := hoffState.offers[peerID]; ok && prev.Sent && hoffSameActivity(prev.Activity, act) {
		o.Sent = true
	}
	hoffState.offers[peerID] = o
}

// hoffPickOffer chooses the offer the Home card shows: one a device sent on
// purpose first, else the newest one inside the handoff window that is newer
// than the last thing done on this device, and not dismissed.
func hoffPickOffer(offers map[string]hoffOffer, dismissed map[string]bool, lastLocal, now time.Time) (hoffOffer, bool) {
	var best hoffOffer
	found := false
	for _, o := range offers {
		if dismissed[hoffOfferKey(o)] || now.Sub(o.When) > hoffWindow {
			continue
		}
		if !o.Sent && !lastLocal.IsZero() && !o.When.After(lastLocal) {
			// This device was used since: the user has moved on.
			continue
		}
		better := !found ||
			(o.Sent && !best.Sent) ||
			(o.Sent == best.Sent && o.When.After(best.When))
		if better {
			best, found = o, true
		}
	}
	return best, found
}

// hoffPendingOffer is the offer to show now, if any. None in screenshot mode:
// the card names the other device, often after its owner (screenshotmode.go).
func hoffPendingOffer(now time.Time) (hoffOffer, bool) {
	if !hoffOffersFlag.Load() || !peerEnabledFlag.Load() || shotModeOn() {
		return hoffOffer{}, false
	}
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	return hoffPickOffer(hoffState.offers, hoffState.dismissed, hoffState.lastLive, now)
}

// hoffDismiss puts an offer away for good.
func hoffDismiss(o hoffOffer) {
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	if hoffState.dismissed == nil {
		hoffState.dismissed = make(map[string]bool)
	}
	if len(hoffState.dismissed) > 200 {
		hoffState.dismissed = make(map[string]bool)
	}
	hoffState.dismissed[hoffOfferKey(o)] = true
}

// hoffForgetPeer drops a device's offer (it was unpaired).
func hoffForgetPeer(peerID string) {
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	delete(hoffState.offers, peerID)
}

// ── Main loop ─────────────────────────────────────────────────────────────

var hoffTickState struct {
	sampleAt time.Time
	wasHome  bool
	wasSaver bool
}

// hoffTick samples the activity, pulls when Home comes up and pushes when it
// is due. It runs every frame from peerTick, only while the switch is on.
func hoffTick(config *Config, now time.Time) {
	if now.Sub(hoffTickState.sampleAt) >= 3*time.Second {
		prev := hoffTickState.sampleAt
		hoffTickState.sampleAt = now
		hoffSample(config, now, prev)
	}
	onHome := homeLayoutActive && transitionPhase == "none"
	saver := screensaverActiveNow()
	// Home came up, or the idle screen ended over it: the user may have just
	// picked this device up after using another one.
	if onHome && !saver && (!hoffTickState.wasHome || hoffTickState.wasSaver) {
		hoffPullSoon(now)
	}
	// The idle screen starting is when a user often reaches for the other
	// device: tell the paired ones now rather than after the usual delay.
	if saver && !hoffTickState.wasSaver {
		hoffPushSoon(now)
	}
	hoffTickState.wasHome, hoffTickState.wasSaver = onHome, saver
	hoffMaybePush(now)
}

// hoffSample records the activity. prev is the previous sample's time: the
// main loop is parked while a game owns the screen, so a game launched since
// then was being played until just now.
func hoffSample(config *Config, now, prev time.Time) {
	act, live := hoffCapture(config, now)
	settings := hoffCaptureSettings(config)
	game, haveGame := hoffLastGame()
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	hoffState.settings = settings
	switch {
	case live:
		act.Settings = settings
		hoffState.current, hoffState.hasCur = act, true
		hoffState.lastLive = now
	case haveGame && !prev.IsZero() && game.PlayedAt.After(prev):
		act = hoffGameActivity(game, now)
		act.Settings = settings
		hoffState.current, hoffState.hasCur = act, true
		hoffState.lastLive = now
	case haveGame && prev.IsZero() && !hoffState.hasCur && now.Sub(game.PlayedAt) < hoffWindow:
		// The first sample after a start: the last game played here, as of
		// when it was launched.
		act = hoffGameActivity(game, game.PlayedAt)
		act.Settings = settings
		hoffState.current, hoffState.hasCur = act, true
		hoffState.lastLive = game.PlayedAt
	}
	if !hoffState.hasCur {
		return
	}
	if sig := hoffSignature(hoffState.current); sig != hoffState.pushedSig && hoffState.pushDue.IsZero() {
		hoffState.pushDue = now.Add(8 * time.Second)
	}
}

// hoffPushSoon makes the next push due now.
func hoffPushSoon(now time.Time) {
	hoffState.mu.Lock()
	if hoffState.hasCur {
		hoffState.pushDue = now
	}
	hoffState.mu.Unlock()
}

// hoffMaybePush sends the activity to the paired devices when a push is due.
func hoffMaybePush(now time.Time) {
	hoffState.mu.Lock()
	if hoffState.pushDue.IsZero() || now.Before(hoffState.pushDue) || !hoffState.hasCur {
		hoffState.mu.Unlock()
		return
	}
	hoffState.pushDue = time.Time{}
	act := hoffState.current
	hoffState.pushedSig = hoffSignature(act)
	hoffState.mu.Unlock()
	if peerPairedCount() > 0 && !backgroundPaused() {
		go hoffPushAll(act)
	}
}

// hoffPushAll tells every paired device that answered lately. One that is
// away pulls on its own when its Home comes up.
func hoffPushAll(act hoffActivity) {
	if !peerEnabledFlag.Load() {
		return
	}
	msg := hoffMessage{V: 1, Now: time.Now().Unix(), Activity: &act}
	for _, p := range peerPairedList() {
		if peerAddrFor(p.ID) == "" || peerIsUnpairedThere(p.ID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = peerDoJSON(ctx, p.ID, http.MethodPost, "/peer/v1/activity", msg, nil, 0)
		cancel()
	}
}

// hoffPullSoon asks the paired devices what they are doing, at most every 15
// seconds.
func hoffPullSoon(now time.Time) {
	if !hoffOffersFlag.Load() {
		return
	}
	hoffState.mu.Lock()
	recent := now.Sub(hoffState.pullAt) < 15*time.Second
	if !recent {
		hoffState.pullAt = now
	}
	hoffState.mu.Unlock()
	if recent || peerPairedCount() == 0 {
		return
	}
	go hoffPullAll()
}

// hoffPullAll scans for the paired devices and asks each one that answered.
func hoffPullAll() {
	if !peerEnabledFlag.Load() || !hoffOffersFlag.Load() || partyLANAddress() == "" {
		return
	}
	if !hoffPulling.CompareAndSwap(false, true) {
		return
	}
	defer hoffPulling.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	peerScanAndMerge(ctx)
	for _, p := range peerPairedList() {
		if !peerOnline(p.ID) || peerIsUnpairedThere(p.ID) {
			continue
		}
		var msg hoffMessage
		rctx, rcancel := context.WithTimeout(ctx, 4*time.Second)
		err := peerDoJSON(rctx, p.ID, http.MethodGet, "/peer/v1/activity", nil, &msg, 0)
		rcancel()
		if err != nil || msg.V != 1 || msg.Activity == nil {
			continue
		}
		act, ok := hoffSanitize(*msg.Activity)
		if !ok {
			continue
		}
		hoffStoreOffer(p.ID, p.Name, act, msg.Now, false)
	}
}

// hoffServeActivity is what this device answers a pull with. While a game owns
// the screen the main loop is parked, so the running game is read directly.
func hoffServeActivity(now time.Time) (hoffActivity, bool) {
	if gameScreenSuspended() {
		if g, ok := hoffLastGame(); ok {
			act := hoffGameActivity(g, now)
			hoffState.mu.Lock()
			act.Settings = hoffState.settings
			hoffState.mu.Unlock()
			return act, true
		}
	}
	hoffState.mu.Lock()
	defer hoffState.mu.Unlock()
	if !hoffState.hasCur || now.Sub(time.Unix(hoffState.current.At, 0)) > hoffWindow {
		return hoffActivity{}, false
	}
	return hoffState.current, true
}

// hoffHandleActivity: GET answers with what this device is doing; POST is a
// paired device telling this one (a push, or "Send to").
func hoffHandleActivity(w http.ResponseWriter, r *http.Request, call peerCall) {
	switch r.Method {
	case http.MethodGet:
		now := time.Now()
		msg := hoffMessage{V: 1, Now: now.Unix()}
		if act, ok := hoffServeActivity(now); ok {
			msg.Activity = &act
		}
		peerWriteJSONResponse(w, http.StatusOK, msg)
	case http.MethodPost:
		var msg hoffMessage
		if !peerDecodeBody(w, r, peerMaxJSONBody, &msg) {
			return
		}
		if msg.V != 1 || msg.Activity == nil {
			http.Error(w, "bad activity", http.StatusBadRequest)
			return
		}
		act, ok := hoffSanitize(*msg.Activity)
		if !ok {
			http.Error(w, "bad activity", http.StatusBadRequest)
			return
		}
		hoffStoreOffer(call.id, call.rec.Name, act, msg.Now, msg.Sent)
		if msg.Sent {
			name, title := call.rec.Name, act.Title
			tbxPost(func() { hoffNotifySent(name, title) })
		}
		peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// hoffNotifySent says a device sent something. On Home the card shows it by
// itself; elsewhere a toast says where to find it.
func hoffNotifySent(name, title string) {
	if homeLayoutActive {
		PlayActivateSound()
		return
	}
	ShowToast(name+" sent you "+title+" — go Home to continue it", ToastKindInfo)
}

// ── Send to ───────────────────────────────────────────────────────────────

// hoffSendTo sends what this device is doing to one paired device, which
// offers it on its Home (Start menu → Send to <device>).
func hoffSendTo(config *Config, peerID string) {
	rec, ok := peerPairedRecord(peerID)
	if !ok {
		return
	}
	act, live := hoffCapture(config, time.Now())
	if !live {
		hoffState.mu.Lock()
		act, live = hoffState.current, hoffState.hasCur
		hoffState.mu.Unlock()
	}
	if !live {
		ShowToast("Nothing to send yet — start a game, a video, music or a web page", ToastKindInfo)
		return
	}
	act.At = time.Now().Unix()
	act.Settings = hoffCaptureSettings(config)
	ShowToast("Sending "+act.Title+" to "+rec.Name+"…", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		msg := hoffMessage{V: 1, Now: time.Now().Unix(), Activity: &act, Sent: true}
		err := peerDoJSON(ctx, peerID, http.MethodPost, "/peer/v1/activity", msg, nil, 0)
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not send: "+peerErrorText(err, rec.Name), ToastKindWarning)
				return
			}
			ShowToast("Sent to "+rec.Name+" — continue it from Home there", ToastKindSuccess)
		})
	}()
}

// hoffSendTargets is the paired devices the Start menu offers to send to.
func hoffSendTargets() []peerRecord {
	if !peerEnabledFlag.Load() {
		return nil
	}
	list := peerPairedList()
	if len(list) > 4 {
		list = list[:4]
	}
	// Two devices with one name get their short id, so the menu never lists
	// the same row twice.
	count := map[string]int{}
	for _, p := range list {
		count[p.Name]++
	}
	for i := range list {
		if count[list[i].Name] > 1 {
			list[i].Name += " (" + peerShortID(list[i].ID) + ")"
		}
	}
	return list
}

// ── Continuing here ───────────────────────────────────────────────────────

// hoffApplySettings applies the settings an offer carried, when this device
// also allows it. It returns what changed.
func hoffApplySettings(config *Config, s map[string]string) []string {
	if config == nil || len(s) == 0 || !hoffSettingsFlag.Load() {
		return nil
	}
	var applied []string
	if name := s["theme"]; name != "" {
		cur, _ := config.Variables.Custom["theme_preset"].(string)
		if _, ok := themePresets[name]; ok && name != cur {
			applyThemePreset(config, name, false)
			applied = append(applied, "theme")
		}
	}
	if v, ok := s["clock12"]; ok {
		on := v == "true"
		if on != config.Variables.Clock12 {
			setStoreBool(config, "clock12", on)
			applied = append(applied, "clock")
		}
	}
	if v, ok := s["textScale"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			f = a11yClampScale(f)
			if math.Abs(f-a11y.fontScale) > 0.01 {
				a11y.fontScale = f
				a11yRefreshFonts()
				a11ySave(config)
				applied = append(applied, "text size")
			}
		}
	}
	return applied
}

// hoffResume continues an offer here. Only the Home card's Continue calls it:
// nothing switches on its own.
func hoffResume(config *Config, o hoffOffer) {
	hoffDismiss(o)
	if applied := hoffApplySettings(config, o.Activity.Settings); len(applied) > 0 {
		ShowToast("Brought your "+strings.Join(applied, ", ")+" from "+o.PeerName, ToastKindInfo)
	}
	a := o.Activity
	switch a.Kind {
	case "game":
		hoffResumeGame(o, true)
	case "library":
		hoffResumeGame(o, false)
	case "video":
		src := a.URL
		if src == "" && a.Path != "" && regularFile(a.Path) {
			src = a.Path
		}
		if src == "" {
			ShowToast(a.Title+" is a file on "+o.PeerName+" only — copy it here to watch it", ToastKindInfo)
			return
		}
		playVideoInfo(config, VideoInfo{URL: src, Title: a.Title, Position: a.Position})
	case "music":
		src := a.URL
		if src == "" && a.Path != "" && regularFile(a.Path) {
			src = a.Path
		}
		if src == "" {
			ShowToast(a.Title+" is a file on "+o.PeerName+" only — copy it here to play it", ToastKindInfo)
			return
		}
		openAudioPlayer(config, src)
		if a.Position > 3 {
			musicSeek(a.Position)
		}
	case "web":
		openBrowserAtURL(config, a.URL)
	}
}

// hoffFindGame finds the game an offer names in this device's library: the
// same path (two handhelds with the same card layout), else the same file
// name, else the same title on the same console.
func hoffFindGame(a hoffActivity) (libraryGame, bool) {
	if a.Path != "" && regularFile(a.Path) {
		return libraryGame{Path: a.Path, System: a.System, Title: a.Title}, true
	}
	stem := ssyncGameStem(a.Path)
	want := nearbyNormTitle(a.Title)
	sys := foldName(a.System)
	var best libraryGame
	bestScore := 0
	lib := scanGameLibrary()
	for _, s := range lib.Systems {
		for _, g := range s.Games {
			score := 0
			if stem != "" && ssyncGameStem(g.Path) == stem {
				score += 4
			}
			if want != "" && nearbyNormTitle(g.Title) == want {
				score += 2
			}
			if score == 0 {
				continue
			}
			if sys != "" && foldName(g.System) == sys {
				score++
			}
			if score > bestScore {
				best, bestScore = g, score
			}
		}
	}
	return best, bestScore >= 2
}

// hoffResumeGame finds the game here, brings its newest saves from the other
// device, then starts it (or, for a library pick, shows it in the library).
func hoffResumeGame(o hoffOffer, launch bool) {
	a := o.Activity
	ShowToast("Getting "+a.Title+" ready…", ToastKindInfo)
	go func() {
		game, found := hoffFindGame(a)
		var synced ssyncResult
		var syncErr error
		sameNames := found && ssyncGameStem(a.Path) != "" && ssyncGameStem(a.Path) == ssyncGameStem(game.Path)
		if found && launch && sameNames && peerOnline(o.PeerID) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			synced, syncErr = ssyncPullForGame(ctx, o.PeerID, o.PeerName, a.Path)
			cancel()
		}
		tbxPost(func() {
			if !found {
				ShowToast(a.Title+" is not on this device — here is your library", ToastKindInfo)
				openGameLibraryPanel()
				setGameLibraryQuery(a.Title)
				return
			}
			switch {
			case syncErr != nil:
				ShowToast("Saves not brought over ("+peerErrorText(syncErr, o.PeerName)+") — the saves here are used", ToastKindWarning)
			case synced.Pulled+synced.Conflicts > 0:
				ShowToast(fmt.Sprintf("Brought %d save file(s) from %s", synced.Pulled+synced.Conflicts, o.PeerName), ToastKindSuccess)
			case launch && found && !sameNames:
				ShowToast("The ROM has another name on "+o.PeerName+" — sync its saves from Save sync", ToastKindInfo)
			}
			if !launch {
				openGameLibraryPanel()
				setGameLibraryQuery(game.Title)
				return
			}
			playRecentGame(recentGame{Path: game.Path, System: game.System, Title: game.Title})
		})
	}()
}
