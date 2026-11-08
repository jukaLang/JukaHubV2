package main

// nearby_test.go — Nearby: title normalisation, the Bloom filter's promises
// (every member matches, few strangers do, a saturated or malformed filter is
// refused, the salt moves where a title lands), what another device's info
// may carry, "Popular nearby", and that nothing is served while sharing is
// off.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// nearbyTestState starts a test with an empty Nearby list and puts the
// previous list, shared info and switch back afterwards.
func nearbyTestState(t *testing.T) {
	t.Helper()
	nearbyPeers.mu.Lock()
	prevPeers, prevMine, prevPopular, prevScans := nearbyPeers.peers, nearbyPeers.mine, nearbyPeers.popular, nearbyPeers.scans
	nearbyPeers.peers, nearbyPeers.mine, nearbyPeers.popular, nearbyPeers.scans = nil, nil, nil, 0
	nearbyPeers.mu.Unlock()
	nearbyCache.mu.Lock()
	prevInfo, prevBuilt, prevLib, prevLibAt := nearbyCache.info, nearbyCache.builtAt, nearbyCache.library, nearbyCache.libAt
	nearbyCache.mu.Unlock()
	prevShare := nearbyShareFlag.Load()
	t.Cleanup(func() {
		nearbyPeers.mu.Lock()
		nearbyPeers.peers, nearbyPeers.mine, nearbyPeers.popular, nearbyPeers.scans = prevPeers, prevMine, prevPopular, prevScans
		nearbyPeers.mu.Unlock()
		nearbyCache.mu.Lock()
		nearbyCache.info, nearbyCache.builtAt, nearbyCache.library, nearbyCache.libAt = prevInfo, prevBuilt, prevLib, prevLibAt
		nearbyCache.mu.Unlock()
		nearbyShareFlag.Store(prevShare)
	})
}

func TestNearbyNormTitle(t *testing.T) {
	for _, tc := range [][2]string{
		{"Pokémon Emerald (USA)", "pokemonemerald"},
		{"Pokemon: Emerald [!]", "pokemonemerald"},
		{"Mario & Luigi", "marioandluigi"},
		{"  ZELDA  ", "zelda"},
		{"Final Fantasy VI (v1.1) [T+Eng]", "finalfantasyvi"},
		{"Straße", "strasse"},
		{"(Beta)", ""},
		{"", ""},
	} {
		if got := nearbyNormTitle(tc[0]); got != tc[1] {
			t.Errorf("nearbyNormTitle(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

func TestNearbyBloomSizeBounds(t *testing.T) {
	cases := []struct {
		n    int
		bits uint32
		k    int
	}{
		{0, nearbyMinBits, nearbyMaxK},
		{5, nearbyMinBits, nearbyMaxK},
		{500, 4800, 7},
		{1000000, nearbyMaxBits, 1},
	}
	for _, tc := range cases {
		m, k := nearbyBloomSize(tc.n)
		if m != tc.bits || k != tc.k || m%8 != 0 {
			t.Errorf("nearbyBloomSize(%d) = %d bits, %d hashes; want %d, %d", tc.n, m, k, tc.bits, tc.k)
		}
	}
}

// TestNearbyBloomMembersAndStrangers checks the two promises the "N of your
// games" count rests on: every title in the filter matches (however it is
// written), and a title that is not matches about one time in a hundred.
func TestNearbyBloomMembersAndStrangers(t *testing.T) {
	const n = 500
	b := newNearbyBloom(n, []byte("0123456789abcdef"))
	for i := 0; i < n; i++ {
		b.add(fmt.Sprintf("Game number %d", i))
	}
	for i := 0; i < n; i++ {
		if !b.has(fmt.Sprintf("game NUMBER %d (USA)", i)) {
			t.Fatalf("member %d does not match", i)
		}
	}
	const trials = 10000
	matches := 0
	for i := 0; i < trials; i++ {
		if b.has(fmt.Sprintf("Other title %d", i)) {
			matches++
		}
	}
	if rate := float64(matches) / trials; rate > 0.03 {
		t.Fatalf("%.3f of the titles not in the filter match it, want about 0.01", rate)
	}
	if f := b.fill(); f < 0.3 || f > 0.7 {
		t.Errorf("a filter sized for its titles is %.2f full, want about half", f)
	}
}

func TestNearbyBloomWireAndSalt(t *testing.T) {
	a := newNearbyBloom(50, []byte("salt-for-network-a"))
	b := newNearbyBloom(50, []byte("salt-for-network-b"))
	a.add("Metroid Fusion")
	b.add("Metroid Fusion")
	if bytes.Equal(a.bits, b.bits) {
		t.Error("two salts put a title on the same bits, so filters could be linked across networks")
	}
	data, err := json.Marshal(a.wire())
	if err != nil {
		t.Fatal(err)
	}
	var w nearbyBloomWire
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	back, ok := nearbyBloomFromWire(w)
	if !ok || !back.has("metroid fusion") || back.has("Zelda") || !bytes.Equal(back.bits, a.bits) {
		t.Fatalf("the filter did not survive the wire (accepted: %v)", ok)
	}
}

func TestNearbyBloomFromWireRefusesBadFilters(t *testing.T) {
	salt := []byte("0123456789abcdef")
	good := newNearbyBloom(10, salt).wire()
	if _, ok := nearbyBloomFromWire(good); !ok {
		t.Fatal("a good filter was refused")
	}
	full := newNearbyBloom(10, salt)
	for i := range full.bits {
		full.bits[i] = 0xff
	}
	mutate := func(fn func(w *nearbyBloomWire)) nearbyBloomWire {
		w := good
		fn(&w)
		return w
	}
	cases := []struct {
		name string
		w    nearbyBloomWire
	}{
		{"saturated: it would match every title", full.wire()},
		{"size not whole bytes", mutate(func(w *nearbyBloomWire) { w.M = 1023 })},
		{"too large", mutate(func(w *nearbyBloomWire) { w.M = nearbyMaxBits * 2 })},
		{"no hashes", mutate(func(w *nearbyBloomWire) { w.K = 0 })},
		{"too many hashes", mutate(func(w *nearbyBloomWire) { w.K = nearbyMaxK + 1 })},
		{"short salt", mutate(func(w *nearbyBloomWire) { w.Salt = "YWJj" })},
		{"bits of another size", mutate(func(w *nearbyBloomWire) { w.Bits = "AAAA" })},
		{"not base64", mutate(func(w *nearbyBloomWire) { w.Bits = "!!" })},
	}
	for _, tc := range cases {
		if _, ok := nearbyBloomFromWire(tc.w); ok {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestNearbyPopularList(t *testing.T) {
	salt := []byte("0123456789abcdef")
	bloomOf := func(titles ...string) *nearbyBloom {
		b := newNearbyBloom(len(titles), salt)
		for _, title := range titles {
			b.add(title)
		}
		return b
	}
	mine := []nearbyTitle{
		{Title: "Metroid Fusion", System: "GBA", norm: nearbyNormTitle("Metroid Fusion")},
		{Title: "Tetris", System: "GB", norm: nearbyNormTitle("Tetris")},
	}
	peers := []nearbyPeer{
		{ID: "p1", Name: "Brick", Playing: &nearbyPlaying{Title: "Zelda", System: "GBA"}, bloom: bloomOf("Metroid Fusion (USA)")},
		{ID: "p2", Name: "Smart Pro", Recent: []nearbyPlaying{{Title: "Zelda (Europe)"}}, bloom: bloomOf("Metroid Fusion", "Zelda")},
		{ID: "p3", Name: "Desk PC", bloom: bloomOf()},
	}
	got := nearbyPopularList(peers, mine)
	if len(got) != 2 {
		t.Fatalf("popular %+v, want Zelda and Metroid Fusion", got)
	}
	if z := got[0]; z.Title != "Zelda" || z.Devices != 2 || z.Playing != 1 || z.Recent != 1 || z.Have != 1 || z.Mine {
		t.Errorf("first %+v", z)
	}
	if m := got[1]; m.Title != "Metroid Fusion" || m.Devices != 2 || m.Have != 2 || !m.Mine {
		t.Errorf("second %+v", m)
	}
	if n := nearbyCommon(peers[1].bloom, mine); n != 1 {
		t.Errorf("Smart Pro has %d of my games, want 1", n)
	}
	if nearbyCommon(nil, mine) != -1 {
		t.Error("a device that shares no library got a count")
	}
}

func TestNearbyApplyInfoBoundsWhatOthersSend(t *testing.T) {
	nearbyTestState(t)
	salt := []byte("0123456789abcdef")
	nearbyPeers.mu.Lock()
	nearbyPeers.peers = map[string]*nearbyPeer{"p1": {ID: "p1", Name: "Brick", Common: -1}}
	nearbyPeers.mine = []nearbyTitle{{Title: "Tetris", norm: "tetris"}, {Title: "Zelda", norm: "zelda"}}
	nearbyPeers.mu.Unlock()

	lib := newNearbyBloom(10, salt)
	lib.add("Zelda")
	info := nearbyInfo{V: 1, Name: "Brick\x07 2", Games: -4, Library: lib.wire(), Rev: "r1", Playing: &nearbyPlaying{Title: "\x01"}}
	for i := 0; i < 8; i++ {
		info.Recent = append(info.Recent, nearbyPlaying{Title: fmt.Sprintf("Game %d", i)})
	}
	now := time.Now()
	nearbyApplyInfo("p1", info, now)
	// A device that left while its info was on the way is not added back.
	nearbyApplyInfo("gone", info, now)
	v := nearbySnapshot()
	if len(v.peers) != 1 {
		t.Fatalf("%d devices listed, want 1", len(v.peers))
	}
	if p := v.peers[0]; p.Name != "Brick 2" || p.Games != 0 || len(p.Recent) != nearbyRecentMax || p.Playing != nil || p.Common != 1 {
		t.Errorf("stored %+v", p)
	}

	full := newNearbyBloom(10, salt)
	for i := range full.bits {
		full.bits[i] = 0xff
	}
	info.Library = full.wire()
	nearbyApplyInfo("p1", info, now)
	if p := nearbySnapshot().peers[0]; p.Common != -1 {
		t.Errorf("a saturated filter claims %d of my games", p.Common)
	}
}

// TestNearbyInfoIsServedOnlyWhileSharing asks for the shared info the way
// another device on the LAN does (any key may) and reads the discovery
// fields, with sharing off and on.
func TestNearbyInfoIsServedOnlyWhileSharing(t *testing.T) {
	peerTestState(t)
	nearbyTestState(t)
	stranger := peerTestIdentity(t, "Stranger")
	peerTestUse(peerTestIdentity(t, "Brick"))
	peerEnabledFlag.Store(true)
	lib := newNearbyBloom(1, []byte("0123456789abcdef"))
	lib.add("Zelda")
	shared := nearbyInfo{V: 1, Name: "Brick", Games: 1, Library: lib.wire(), Rev: "abc123", Recent: []nearbyPlaying{{Title: "Zelda", System: "GBA"}}}
	nearbyCache.mu.Lock()
	nearbyCache.info, nearbyCache.builtAt = shared, time.Now()
	nearbyCache.mu.Unlock()
	h := peerHandler()
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, peerTestRequest(t, stranger, http.MethodGet, "/peer/v1/nearby", nil))
		return rec
	}

	nearbyShareFlag.Store(false)
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Fatalf("not sharing, the info answered %d", rec.Code)
	}
	var reply peerBeaconReply
	nearbyBeaconFields(&reply)
	if reply.Share || reply.NRev != "" || reply.Play != "" {
		t.Errorf("a device that does not share announced %+v", reply)
	}

	nearbyShareFlag.Store(true)
	rec := get()
	if rec.Code != http.StatusOK {
		t.Fatalf("sharing, an unpaired device's request answered %d", rec.Code)
	}
	var got nearbyInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, shared) {
		t.Errorf("served %+v, want %+v", got, shared)
	}
	nearbyBeaconFields(&reply)
	if !reply.Share || reply.NRev != "abc123" || reply.Play != "" {
		t.Errorf("announced %+v", reply)
	}
}
