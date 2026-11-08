package main

// nearby.go — Nearby: who is playing what on this Wi-Fi, with no account and
// nothing leaving the network.
//
// When "Share what I'm playing on this Wi-Fi" is on (off by default, and only
// together with Nearby devices), this device tells the others on the LAN:
//
//   - in its discovery answer (peernet.go): the game it is playing right now;
//   - at GET /peer/v1/nearby: the last few games it played, how many games its
//     library holds, and a Bloom filter of the library — salted hashes of the
//     normalised titles, never the titles themselves.
//
// The filter lets another device test "does it have this game?" for titles it
// already knows (its own library, what the others say they play) without the
// list crossing the network. The salt is random and changes with the network
// (and daily), so a filter seen on one network cannot be linked to one seen on
// another; filters are sized for about one false match in a hundred tests. A
// device that tests a whole catalogue of titles learns most of the library, so
// the filter keeps the list from being read, not from being guessed: sharing
// stays off until switched on (SECURITY.md → Nearby devices).
//
// The Nearby screen (nearbyui.go) lists each device, what it is playing and how
// many of your games it has, plus "Popular nearby": titles two or more devices
// have or play, each opening the Games Library.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// nearbySceneName is the Nearby page.
	nearbySceneName = "Nearby"
	// nearbyShareKey is "Share what I'm playing on this Wi-Fi" (off by default).
	nearbyShareKey = "nearbySharePlaying"
	// nearbyMinBits and nearbyMaxBits bound a filter: 128 bytes to 32 KiB.
	nearbyMinBits = 1024
	nearbyMaxBits = 1 << 18
	// nearbyMaxK bounds the hash count of a filter.
	nearbyMaxK = 12
	// nearbyRecentMax is how many recent games are shared.
	nearbyRecentMax = 5
	// nearbyInfoLimit bounds a fetched info (a full filter is ~44 KB of base64).
	nearbyInfoLimit = 96 << 10
	// nearbyPeerTTL is how long a device stays listed after its last answer.
	nearbyPeerTTL = 30 * time.Second
)

// nearbyShareFlag mirrors the sharing switch for the goroutines.
var nearbyShareFlag atomic.Bool

func nearbySettingShare(c *Config) bool { return peerCustomBool(c, nearbyShareKey, false) }

// nearbySettingSetShare is the Settings row and the Nearby page's switch.
func nearbySettingSetShare(c *Config, on bool) {
	setStoreBool(c, nearbyShareKey, on)
	nearbyShareFlag.Store(on)
	nearbyInvalidate()
	switch {
	case on && !peerSettingEnabled(c):
		ShowToast("Sharing starts once Nearby devices is on", ToastKindInfo)
	case on:
		ShowToast("Others on this Wi-Fi now see what you are playing", ToastKindSuccess)
	default:
		ShowToast("What you play is no longer shared", ToastKindInfo)
	}
}

// ── Titles ────────────────────────────────────────────────────────────────

// nearbyFold maps accented Latin letters to plain ones, so "Pokémon" and
// "Pokemon" are one title.
var nearbyFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'ç': "c", 'ć': "c", 'č': "c", 'ď': "d", 'đ': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ő': "o",
	'ř': "r", 'ś': "s", 'š': "s", 'ş': "s", 'ť': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y", 'ź': "z", 'ż': "z", 'ž': "z",
	'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th",
}

// nearbyNormTitle reduces a game title to what two devices agree on: lower
// case, accents folded, release tags in brackets dropped, "&" as "and", and
// only letters and digits kept.
func nearbyNormTitle(title string) string {
	var b strings.Builder
	depth := 0
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch r {
		case '(', '[', '{':
			depth++
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		if s, ok := nearbyFold[r]; ok {
			b.WriteString(s)
			continue
		}
		switch {
		case r == '&':
			b.WriteString("and")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── Bloom filter ──────────────────────────────────────────────────────────

// nearbyBloom is a set of salted title hashes that answers "maybe present" or
// "certainly absent".
type nearbyBloom struct {
	m    uint32
	k    int
	salt []byte
	bits []byte
}

// nearbyBloomSize picks the bit count and hash count for n titles at a 1%
// false match rate, within the size bounds.
func nearbyBloomSize(n int) (uint32, int) {
	if n < 1 {
		n = 1
	}
	m := math.Ceil(-float64(n) * math.Log(0.01) / (math.Ln2 * math.Ln2))
	if m < nearbyMinBits {
		m = nearbyMinBits
	}
	if m > nearbyMaxBits {
		m = nearbyMaxBits
	}
	size := (uint32(m) + 7) &^ 7
	k := int(math.Round(float64(size) / float64(n) * math.Ln2))
	if k < 1 {
		k = 1
	}
	if k > nearbyMaxK {
		k = nearbyMaxK
	}
	return size, k
}

// newNearbyBloom makes an empty filter for n titles.
func newNearbyBloom(n int, salt []byte) *nearbyBloom {
	m, k := nearbyBloomSize(n)
	return &nearbyBloom{m: m, k: k, salt: append([]byte(nil), salt...), bits: make([]byte, m/8)}
}

// indexes is where a normalised title sets its bits: double hashing over one
// salted SHA-256.
func (b *nearbyBloom) indexes(norm string, out *[nearbyMaxK]uint32) {
	h := sha256.New()
	h.Write(b.salt)
	h.Write([]byte{0})
	h.Write([]byte(norm))
	sum := h.Sum(nil)
	h1 := binary.BigEndian.Uint64(sum[0:8])
	h2 := binary.BigEndian.Uint64(sum[8:16]) | 1
	m := uint64(b.m)
	for i := 0; i < b.k; i++ {
		out[i] = uint32((h1 + uint64(i)*h2) % m)
	}
}

// addNorm adds a normalised title.
func (b *nearbyBloom) addNorm(norm string) {
	if norm == "" || b.m == 0 {
		return
	}
	var idx [nearbyMaxK]uint32
	b.indexes(norm, &idx)
	for i := 0; i < b.k; i++ {
		b.bits[idx[i]/8] |= byte(1) << (idx[i] % 8)
	}
}

// add adds a title.
func (b *nearbyBloom) add(title string) { b.addNorm(nearbyNormTitle(title)) }

// hasNorm reports whether a normalised title may be in the set.
func (b *nearbyBloom) hasNorm(norm string) bool {
	if b == nil || norm == "" || b.m == 0 {
		return false
	}
	var idx [nearbyMaxK]uint32
	b.indexes(norm, &idx)
	for i := 0; i < b.k; i++ {
		if b.bits[idx[i]/8]&(byte(1)<<(idx[i]%8)) == 0 {
			return false
		}
	}
	return true
}

// has reports whether a title may be in the set.
func (b *nearbyBloom) has(title string) bool { return b.hasNorm(nearbyNormTitle(title)) }

// fill is the share of bits set.
func (b *nearbyBloom) fill() float64 {
	if b == nil || b.m == 0 {
		return 0
	}
	set := 0
	for _, x := range b.bits {
		for ; x != 0; x >>= 1 {
			set += int(x & 1)
		}
	}
	return float64(set) / float64(b.m)
}

// nearbyBloomWire is a filter as JSON.
type nearbyBloomWire struct {
	M    uint32 `json:"m"`
	K    int    `json:"k"`
	Salt string `json:"salt"`
	Bits string `json:"bits"`
}

func (b *nearbyBloom) wire() nearbyBloomWire {
	return nearbyBloomWire{
		M:    b.m,
		K:    b.k,
		Salt: base64.StdEncoding.EncodeToString(b.salt),
		Bits: base64.StdEncoding.EncodeToString(b.bits),
	}
}

// nearbyBloomFromWire checks a filter from another device. One that is mostly
// ones would "contain" every title, so it is refused.
func nearbyBloomFromWire(w nearbyBloomWire) (*nearbyBloom, bool) {
	if w.M < 8 || w.M > nearbyMaxBits || w.M%8 != 0 || w.K < 1 || w.K > nearbyMaxK {
		return nil, false
	}
	salt, err := base64.StdEncoding.DecodeString(w.Salt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return nil, false
	}
	set, err := base64.StdEncoding.DecodeString(w.Bits)
	if err != nil || len(set) != int(w.M/8) {
		return nil, false
	}
	b := &nearbyBloom{m: w.M, k: w.K, salt: salt, bits: set}
	if b.fill() > 0.75 {
		return nil, false
	}
	return b, true
}

// ── What this device shares ───────────────────────────────────────────────

// nearbyPlaying is one game, as shared.
type nearbyPlaying struct {
	Title  string `json:"title"`
	System string `json:"system,omitempty"`
}

// nearbyInfo is what GET /peer/v1/nearby answers.
type nearbyInfo struct {
	V       int             `json:"v"`
	Name    string          `json:"name"`
	Model   string          `json:"model,omitempty"`
	Playing *nearbyPlaying  `json:"playing,omitempty"`
	Recent  []nearbyPlaying `json:"recent,omitempty"`
	Games   int             `json:"games"`
	Library nearbyBloomWire `json:"library"`
	Rev     string          `json:"rev"`
}

// nearbyTitle is one game of this device's library.
type nearbyTitle struct {
	Title  string
	System string
	norm   string
}

var nearbySalt struct {
	mu     sync.Mutex
	salt   []byte
	netKey string
	at     time.Time
}

// nearbyNetworkKey names the network this device is on, for rotating the
// salt: the first three parts of its address.
func nearbyNetworkKey() string {
	ip := partyLANAddress()
	if i := strings.LastIndexByte(ip, '.'); i > 0 {
		return ip[:i]
	}
	return ip
}

// nearbyCurrentSalt is the salt this device's filter uses on this network:
// new for every network and every day.
func nearbyCurrentSalt(now time.Time) []byte {
	key := nearbyNetworkKey()
	nearbySalt.mu.Lock()
	defer nearbySalt.mu.Unlock()
	if nearbySalt.salt == nil || nearbySalt.netKey != key || now.Sub(nearbySalt.at) > 24*time.Hour {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil
		}
		nearbySalt.salt, nearbySalt.netKey, nearbySalt.at = salt, key, now
	}
	return append([]byte(nil), nearbySalt.salt...)
}

// nearbyCache is the info this device serves and the library it was built
// from.
var nearbyCache struct {
	mu      sync.Mutex
	info    nearbyInfo
	builtAt time.Time
	library []nearbyTitle
	libAt   time.Time
}

// nearbyInvalidate makes the next request rebuild the shared info.
func nearbyInvalidate() {
	nearbyCache.mu.Lock()
	nearbyCache.builtAt = time.Time{}
	nearbyCache.info.Rev = ""
	nearbyCache.mu.Unlock()
}

// nearbyLibraryTitles is this device's library as titles, scanned at most
// every ten minutes (the scan walks the card). Never call it on the main loop.
func nearbyLibraryTitles(now time.Time) []nearbyTitle {
	nearbyCache.mu.Lock()
	if nearbyCache.library != nil && now.Sub(nearbyCache.libAt) < 10*time.Minute {
		out := nearbyCache.library
		nearbyCache.mu.Unlock()
		return out
	}
	nearbyCache.mu.Unlock()
	lib := scanGameLibrary()
	titles := []nearbyTitle{}
	seen := map[string]bool{}
	for _, s := range lib.Systems {
		for _, g := range s.Games {
			norm := nearbyNormTitle(g.Title)
			if norm == "" || seen[norm] {
				continue
			}
			seen[norm] = true
			titles = append(titles, nearbyTitle{Title: g.Title, System: g.System, norm: norm})
		}
	}
	nearbyCache.mu.Lock()
	nearbyCache.library, nearbyCache.libAt = titles, now
	nearbyCache.mu.Unlock()
	return titles
}

// nearbyNowPlaying is the game running right now, if any. While a game owns
// the screen the main loop is parked, but the listener still answers.
func nearbyNowPlaying() (nearbyPlaying, bool) {
	if !gameScreenSuspended() {
		return nearbyPlaying{}, false
	}
	g, ok := hoffLastGame()
	if !ok {
		return nearbyPlaying{}, false
	}
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = gameDisplayTitle(g.Path)
	}
	p := nearbyPlaying{Title: partyCleanLabel(title, 120), System: partyCleanLabel(g.System, 40)}
	return p, p.Title != ""
}

// nearbyRecentPlays is the last few games played here in the past two weeks.
func nearbyRecentPlays(now time.Time) []nearbyPlaying {
	var out []nearbyPlaying
	seen := map[string]bool{}
	for _, g := range recentGames() {
		if len(out) >= nearbyRecentMax {
			break
		}
		if now.Sub(g.PlayedAt) > 14*24*time.Hour {
			continue
		}
		title := strings.TrimSpace(g.Title)
		if title == "" {
			title = gameDisplayTitle(g.Path)
		}
		norm := nearbyNormTitle(title)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, nearbyPlaying{Title: partyCleanLabel(title, 120), System: partyCleanLabel(g.System, 40)})
	}
	return out
}

// nearbyBuildInfo builds what this device shares.
func nearbyBuildInfo(now time.Time) nearbyInfo {
	titles := nearbyLibraryTitles(now)
	bloom := newNearbyBloom(len(titles), nearbyCurrentSalt(now))
	for _, t := range titles {
		bloom.addNorm(t.norm)
	}
	info := nearbyInfo{
		V:       1,
		Name:    peerDeviceName(),
		Model:   peerModelName(),
		Recent:  nearbyRecentPlays(now),
		Games:   len(titles),
		Library: bloom.wire(),
	}
	if p, ok := nearbyNowPlaying(); ok {
		info.Playing = &p
	}
	if data, err := json.Marshal(info); err == nil {
		sum := sha256.Sum256(data)
		info.Rev = hex.EncodeToString(sum[:6])
	}
	return info
}

// nearbyInfoNow is the shared info, rebuilt when it is a minute old.
func nearbyInfoNow(now time.Time) nearbyInfo {
	nearbyCache.mu.Lock()
	if nearbyCache.info.V == 1 && nearbyCache.info.Rev != "" && now.Sub(nearbyCache.builtAt) < time.Minute {
		info := nearbyCache.info
		nearbyCache.mu.Unlock()
		return info
	}
	nearbyCache.mu.Unlock()
	info := nearbyBuildInfo(now)
	nearbyCache.mu.Lock()
	nearbyCache.info, nearbyCache.builtAt = info, now
	nearbyCache.mu.Unlock()
	return info
}

// nearbyHandleInfo serves the shared info to any device on the LAN, while
// sharing is on.
func nearbyHandleInfo(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !nearbyShareFlag.Load() {
		http.Error(w, "not sharing", http.StatusNotFound)
		return
	}
	peerWriteJSONResponse(w, http.StatusOK, nearbyInfoNow(time.Now()))
}

// nearbyBeaconFields adds the sharing fields to a discovery answer: whether
// this device shares, the revision of its info and what it is playing.
func nearbyBeaconFields(reply *peerBeaconReply) {
	if !nearbyShareFlag.Load() {
		return
	}
	reply.Share = true
	nearbyCache.mu.Lock()
	reply.NRev = nearbyCache.info.Rev
	nearbyCache.mu.Unlock()
	if p, ok := nearbyNowPlaying(); ok {
		reply.Play, reply.Sys = p.Title, p.System
	}
}

// ── What the others share ─────────────────────────────────────────────────

// nearbyPeer is one device heard on this Wi-Fi.
type nearbyPeer struct {
	ID      string
	Name    string
	Model   string
	Paired  bool
	Share   bool
	Playing *nearbyPlaying
	Recent  []nearbyPlaying
	Games   int
	// Common is how many of this device's games it has (-1 until its
	// library was read).
	Common  int
	Seen    time.Time
	bloom   *nearbyBloom
	rev     string
	fetched time.Time
}

// nearbyPopular is one title several nearby devices have or play.
type nearbyPopular struct {
	Title   string
	System  string
	Devices int
	Have    int
	Playing int
	Recent  int
	Mine    bool
}

// nearbyPeers is the live Nearby list, filled by nearbyScanLoop.
var nearbyPeers struct {
	mu      sync.Mutex
	peers   map[string]*nearbyPeer
	mine    []nearbyTitle
	popular []nearbyPopular
	scans   int
}

// nearbyView is the list as the screen draws it.
type nearbyView struct {
	peers   []nearbyPeer
	popular []nearbyPopular
	scans   int
	mine    int
}

// nearbySnapshot copies the list for the screen.
func nearbySnapshot() nearbyView {
	nearbyPeers.mu.Lock()
	defer nearbyPeers.mu.Unlock()
	v := nearbyView{scans: nearbyPeers.scans, mine: len(nearbyPeers.mine)}
	for _, p := range nearbyPeers.peers {
		v.peers = append(v.peers, *p)
	}
	v.popular = append(v.popular, nearbyPeers.popular...)
	sort.SliceStable(v.peers, func(i, j int) bool {
		a, b := strings.ToLower(v.peers[i].Name), strings.ToLower(v.peers[j].Name)
		if a != b {
			return a < b
		}
		return v.peers[i].ID < v.peers[j].ID
	})
	return v
}

// nearbyReset starts a visit with an empty list.
func nearbyReset() {
	nearbyPeers.mu.Lock()
	nearbyPeers.peers = nil
	nearbyPeers.popular = nil
	nearbyPeers.scans = 0
	nearbyPeers.mu.Unlock()
}

// nearbyScanLoop keeps the list fresh while the Nearby screen is up: the
// screen starts it as a scene task, so leaving the screen stops it.
func nearbyScanLoop(ctx context.Context) {
	mine := nearbyLibraryTitles(time.Now())
	nearbyPeers.mu.Lock()
	nearbyPeers.mine = mine
	nearbyPeers.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return
		}
		if peerEnabledFlag.Load() && partyLANAddress() != "" {
			devs, _ := peerDiscoverOnce(ctx, peerScanWait)
			if ctx.Err() != nil {
				return
			}
			now := time.Now()
			peerMergeSeen(devs, now)
			nearbyMergeScan(ctx, devs, now)
		} else {
			nearbyPeers.mu.Lock()
			nearbyPeers.scans++
			nearbyPeers.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(4 * time.Second):
		}
	}
}

// nearbyMergeScan folds one scan into the list: devices that share get their
// info fetched when it changed, devices gone quiet drop out, and "Popular
// nearby" is recomputed.
func nearbyMergeScan(ctx context.Context, devs []peerSeenDevice, now time.Time) {
	type fetchJob struct {
		dev peerSeenDevice
	}
	var jobs []fetchJob
	nearbyPeers.mu.Lock()
	if nearbyPeers.peers == nil {
		nearbyPeers.peers = make(map[string]*nearbyPeer)
	}
	for _, d := range devs {
		p := nearbyPeers.peers[d.ID]
		if p == nil {
			if len(nearbyPeers.peers) >= 32 {
				continue
			}
			p = &nearbyPeer{ID: d.ID, Common: -1}
			nearbyPeers.peers[d.ID] = p
		}
		_, p.Paired = peerPairedRecord(d.ID)
		p.Name, p.Model, p.Share, p.Seen = d.Name, d.Model, d.Share, now
		if !d.Share {
			p.Playing, p.Recent, p.bloom, p.Common, p.Games, p.rev = nil, nil, nil, -1, 0, ""
			continue
		}
		if d.Play != "" {
			p.Playing = &nearbyPlaying{Title: d.Play, System: d.Sys}
		} else {
			p.Playing = nil
		}
		if d.NRev == "" || d.NRev != p.rev || now.Sub(p.fetched) > 2*time.Minute {
			jobs = append(jobs, fetchJob{dev: d})
		}
	}
	for id, p := range nearbyPeers.peers {
		if now.Sub(p.Seen) > nearbyPeerTTL {
			delete(nearbyPeers.peers, id)
		}
	}
	nearbyPeers.mu.Unlock()

	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		info, err := nearbyFetchInfo(ctx, job.dev)
		if err != nil {
			continue
		}
		nearbyApplyInfo(job.dev.ID, info, now)
	}
	nearbyRecompute()
}

// nearbyFetchInfo reads a device's shared info. The key is pinned to the id
// its discovery answer announced, paired or not.
func nearbyFetchInfo(ctx context.Context, dev peerSeenDevice) (nearbyInfo, error) {
	self, ok := peerSelf()
	if !ok {
		return nearbyInfo{}, errors.New("this device has no identity yet")
	}
	client := peerNewClient(self, dev.ID, nil)
	defer client.CloseIdleConnections()
	fctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var info nearbyInfo
	if err := peerGetJSON(fctx, client, dev.Addr, "/peer/v1/nearby", &info, nearbyInfoLimit); err != nil {
		return nearbyInfo{}, err
	}
	if info.V != 1 {
		return nearbyInfo{}, errors.New("unreadable nearby info")
	}
	return info, nil
}

// nearbyCleanPlaying bounds one shared game.
func nearbyCleanPlaying(p nearbyPlaying) (nearbyPlaying, bool) {
	p.Title = partyCleanLabel(p.Title, 120)
	p.System = partyCleanLabel(p.System, 40)
	return p, p.Title != ""
}

// nearbyApplyInfo stores a fetched info, bounded and checked.
func nearbyApplyInfo(id string, info nearbyInfo, now time.Time) {
	var recent []nearbyPlaying
	for _, r := range info.Recent {
		if len(recent) >= nearbyRecentMax {
			break
		}
		if c, ok := nearbyCleanPlaying(r); ok {
			recent = append(recent, c)
		}
	}
	var playing *nearbyPlaying
	if info.Playing != nil {
		if c, ok := nearbyCleanPlaying(*info.Playing); ok {
			playing = &c
		}
	}
	bloom, okBloom := nearbyBloomFromWire(info.Library)
	games := info.Games
	if games < 0 || games > 100000 {
		games = 0
	}
	nearbyPeers.mu.Lock()
	defer nearbyPeers.mu.Unlock()
	p := nearbyPeers.peers[id]
	if p == nil {
		return
	}
	if name := peerCleanName(info.Name); name != "" {
		p.Name = name
	}
	p.Recent, p.Games, p.rev, p.fetched = recent, games, partyCleanLabel(info.Rev, 64), now
	if playing != nil {
		p.Playing = playing
	}
	p.bloom = nil
	p.Common = -1
	if okBloom {
		p.bloom = bloom
		p.Common = nearbyCommon(bloom, nearbyPeers.mine)
	}
}

// nearbyCommon is how many of mine a filter may hold.
func nearbyCommon(b *nearbyBloom, mine []nearbyTitle) int {
	if b == nil {
		return -1
	}
	n := 0
	for _, t := range mine {
		if b.hasNorm(t.norm) {
			n++
		}
	}
	return n
}

// nearbyRecompute rebuilds "Popular nearby" off the main loop: each title is
// hashed with each device's salt, which is too slow for a frame on a big card.
func nearbyRecompute() {
	nearbyPeers.mu.Lock()
	peers := make([]nearbyPeer, 0, len(nearbyPeers.peers))
	for _, p := range nearbyPeers.peers {
		peers = append(peers, *p)
	}
	mine := nearbyPeers.mine
	nearbyPeers.mu.Unlock()
	popular := nearbyPopularList(peers, mine)
	nearbyPeers.mu.Lock()
	nearbyPeers.popular = popular
	nearbyPeers.scans++
	nearbyPeers.mu.Unlock()
}

// nearbyPopularList ranks the titles two or more nearby devices have or play:
// this device's own games and the titles the others say they play, each
// tested against every device's filter.
func nearbyPopularList(peers []nearbyPeer, mine []nearbyTitle) []nearbyPopular {
	type tally struct {
		p       nearbyPopular
		devices map[string]bool
	}
	byNorm := map[string]*tally{}
	get := func(title, system string) (*tally, string) {
		norm := nearbyNormTitle(title)
		if norm == "" {
			return nil, ""
		}
		t := byNorm[norm]
		if t == nil {
			t = &tally{p: nearbyPopular{Title: title, System: system}, devices: map[string]bool{}}
			byNorm[norm] = t
		}
		return t, norm
	}
	for _, m := range mine {
		if t, _ := get(m.Title, m.System); t != nil {
			t.p.Mine = true
		}
	}
	for _, pr := range peers {
		if pr.Playing != nil {
			if t, _ := get(pr.Playing.Title, pr.Playing.System); t != nil {
				t.p.Playing++
				t.devices[pr.ID] = true
			}
		}
		for _, r := range pr.Recent {
			if t, _ := get(r.Title, r.System); t != nil {
				t.p.Recent++
				t.devices[pr.ID] = true
			}
		}
	}
	for _, pr := range peers {
		if pr.bloom == nil {
			continue
		}
		for norm, t := range byNorm {
			if pr.bloom.hasNorm(norm) {
				t.p.Have++
				t.devices[pr.ID] = true
			}
		}
	}
	var out []nearbyPopular
	for _, t := range byNorm {
		t.p.Devices = len(t.devices)
		if t.p.Devices >= 2 {
			out = append(out, t.p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Devices != b.Devices {
			return a.Devices > b.Devices
		}
		if a.Playing != b.Playing {
			return a.Playing > b.Playing
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
