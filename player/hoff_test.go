package main

// hoff_test.go — handoff: the activity payload on the wire, what an activity
// from another device may carry, which offer the Home card shows, and the
// activity endpoint between two paired devices.

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// hoffTestState starts a test with no activity, offers or dismissals, and
// puts the previous ones back afterwards.
func hoffTestState(t *testing.T) {
	t.Helper()
	hoffState.mu.Lock()
	prevCur, prevHas, prevLive := hoffState.current, hoffState.hasCur, hoffState.lastLive
	prevSettings, prevSig, prevDue, prevPull := hoffState.settings, hoffState.pushedSig, hoffState.pushDue, hoffState.pullAt
	prevOffers, prevDismissed := hoffState.offers, hoffState.dismissed
	hoffState.current, hoffState.hasCur, hoffState.lastLive = hoffActivity{}, false, time.Time{}
	hoffState.settings, hoffState.pushedSig, hoffState.pushDue, hoffState.pullAt = nil, "", time.Time{}, time.Time{}
	hoffState.offers, hoffState.dismissed = nil, nil
	hoffState.mu.Unlock()
	prevOffersFlag, prevSettingsFlag := hoffOffersFlag.Load(), hoffSettingsFlag.Load()
	t.Cleanup(func() {
		hoffState.mu.Lock()
		hoffState.current, hoffState.hasCur, hoffState.lastLive = prevCur, prevHas, prevLive
		hoffState.settings, hoffState.pushedSig, hoffState.pushDue, hoffState.pullAt = prevSettings, prevSig, prevDue, prevPull
		hoffState.offers, hoffState.dismissed = prevOffers, prevDismissed
		hoffState.mu.Unlock()
		hoffOffersFlag.Store(prevOffersFlag)
		hoffSettingsFlag.Store(prevSettingsFlag)
	})
}

func TestHoffMessageRoundTrip(t *testing.T) {
	act := hoffActivity{
		Kind:     "video",
		Title:    "Big Buck Bunny",
		URL:      "https://example.com/bunny.mp4",
		Position: 754.5,
		Duration: 596,
		At:       1767225600,
		Settings: map[string]string{"theme": "Midnight", "clock12": "true", "textScale": "1.25"},
	}
	in := hoffMessage{V: 1, Now: 1767225660, Activity: &act, Sent: true}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out hoffMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("the round trip changed the message:\n%+v\n%+v", in, out)
	}
	clean, ok := hoffSanitize(*out.Activity)
	if !ok || !reflect.DeepEqual(clean, act) {
		t.Fatalf("checking a clean activity changed it: %+v, %v", clean, ok)
	}
}

func TestHoffSanitize(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 2100)
	cases := []struct {
		name string
		in   hoffActivity
		want hoffActivity
		ok   bool
	}{
		{
			name: "control characters leave the title",
			in:   hoffActivity{Kind: "game", Title: "Zelda\x07", System: "GBA", Path: "/roms/Zelda.gba", At: 100},
			want: hoffActivity{Kind: "game", Title: "Zelda", System: "GBA", Path: "/roms/Zelda.gba", At: 100},
			ok:   true,
		},
		{
			name: "an unknown kind",
			in:   hoffActivity{Kind: "shell", Title: "rm", At: 100},
		},
		{
			name: "a page that is not http",
			in:   hoffActivity{Kind: "web", Title: "x", URL: "javascript:alert(1)", At: 100},
		},
		{
			name: "a page gets its host as its title",
			in:   hoffActivity{Kind: "web", URL: "https://www.example.com/news/", At: 100},
			want: hoffActivity{Kind: "web", Title: "example.com/news", URL: "https://www.example.com/news/", At: 100},
			ok:   true,
		},
		{
			name: "a local file link is dropped and the file names it",
			in:   hoffActivity{Kind: "video", URL: "file:///etc/passwd", Path: "/media/Movie.mkv", At: 100},
			want: hoffActivity{Kind: "video", Title: "Movie", Path: "/media/Movie.mkv", At: 100},
			ok:   true,
		},
		{
			name: "impossible positions are reset",
			in:   hoffActivity{Kind: "music", Title: "Song", Position: math.NaN(), Duration: -5, At: 100},
			want: hoffActivity{Kind: "music", Title: "Song", At: 100},
			ok:   true,
		},
		{
			name: "two days into a video",
			in:   hoffActivity{Kind: "video", Title: "Clip", URL: "rtsp://cam/stream", Position: 49 * 3600, Duration: 60, At: 100},
			want: hoffActivity{Kind: "video", Title: "Clip", URL: "rtsp://cam/stream", Duration: 60, At: 100},
			ok:   true,
		},
		{
			name: "an overlong link",
			in:   hoffActivity{Kind: "video", Title: "Clip", URL: long, At: 100},
			want: hoffActivity{Kind: "video", Title: "Clip", At: 100},
			ok:   true,
		},
		{
			name: "no time",
			in:   hoffActivity{Kind: "game", Title: "Zelda"},
		},
		{
			name: "nothing to call it",
			in:   hoffActivity{Kind: "game", At: 100},
		},
		{
			name: "only known settings travel",
			in:   hoffActivity{Kind: "game", Title: "Zelda", At: 100, Settings: map[string]string{"theme": "Midnight\x01", "clock12": "yes", "textScale": "9", "shell": "rm -rf"}},
			want: hoffActivity{Kind: "game", Title: "Zelda", At: 100, Settings: map[string]string{"theme": "Midnight"}},
			ok:   true,
		},
	}
	for _, tc := range cases {
		got, ok := hoffSanitize(tc.in)
		if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHoffPickOffer(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	offer := func(peer string, ago time.Duration, sent bool) hoffOffer {
		when := now.Add(-ago)
		return hoffOffer{
			PeerID:   peer,
			PeerName: peer,
			Activity: hoffActivity{Kind: "game", Title: "Game on " + peer, At: when.Unix()},
			When:     when,
			Sent:     sent,
		}
	}
	a5 := offer("a", 5*time.Minute, false)
	a5sent := offer("a", 5*time.Minute, true)
	b2 := offer("b", 2*time.Minute, false)
	cases := []struct {
		name      string
		offers    []hoffOffer
		dismissed []hoffOffer
		lastLocal time.Time
		want      string
	}{
		{"the newest wins", []hoffOffer{a5, b2}, nil, time.Time{}, "b"},
		{"sent on purpose wins", []hoffOffer{a5sent, b2}, nil, time.Time{}, "a"},
		{"older than the window", []hoffOffer{offer("a", 40*time.Minute, false)}, nil, time.Time{}, ""},
		{"this device was used since", []hoffOffer{a5}, nil, now.Add(-time.Minute), ""},
		{"sent on purpose after use here", []hoffOffer{a5sent}, nil, now.Add(-time.Minute), "a"},
		{"dismissed", []hoffOffer{b2}, []hoffOffer{b2}, time.Time{}, ""},
		{"the next one after a dismissal", []hoffOffer{a5, b2}, []hoffOffer{b2}, time.Time{}, "a"},
		{"nothing", nil, nil, time.Time{}, ""},
	}
	for _, tc := range cases {
		offers := map[string]hoffOffer{}
		for _, o := range tc.offers {
			offers[o.PeerID] = o
		}
		dismissed := map[string]bool{}
		for _, o := range tc.dismissed {
			dismissed[hoffOfferKey(o)] = true
		}
		got, ok := hoffPickOffer(offers, dismissed, tc.lastLocal, now)
		if ok != (tc.want != "") || (ok && got.PeerID != tc.want) {
			t.Errorf("%s: got %q (%v), want %q", tc.name, got.PeerID, ok, tc.want)
		}
	}
}

func TestHoffStoreOfferAgesBySenderClock(t *testing.T) {
	hoffTestState(t)
	offerOf := func(peer string) hoffOffer {
		hoffState.mu.Lock()
		defer hoffState.mu.Unlock()
		return hoffState.offers[peer]
	}
	// Five minutes old on the sender's clock is five minutes old here,
	// whatever the two clocks say.
	hoffStoreOffer("p1", "Brick", hoffActivity{Kind: "game", Title: "Zelda", At: 999700}, 1000000, true)
	if age := time.Since(offerOf("p1").When); age < 299*time.Second || age > 302*time.Second {
		t.Errorf("an activity five minutes old there is %v old here", age)
	}
	// A sender clock behind its own activity cannot age it.
	hoffStoreOffer("p2", "Desk PC", hoffActivity{Kind: "game", Title: "Zelda", At: 2000}, 1000, false)
	if age := time.Since(offerOf("p2").When); age < 0 || age > 2*time.Second {
		t.Errorf("an activity from an impossible clock is %v old", age)
	}
	// A later pull of the same activity keeps "sent on purpose"; another
	// activity does not.
	hoffStoreOffer("p1", "Brick", hoffActivity{Kind: "game", Title: "Zelda", At: 999800}, 1000000, false)
	if !offerOf("p1").Sent {
		t.Error("a pull undid Send to")
	}
	hoffStoreOffer("p1", "Brick", hoffActivity{Kind: "music", Title: "Song", At: 999900}, 1000000, false)
	if offerOf("p1").Sent {
		t.Error("a new activity inherited Send to")
	}
	hoffForgetPeer("p1")
	hoffState.mu.Lock()
	_, kept := hoffState.offers["p1"]
	hoffState.mu.Unlock()
	if kept {
		t.Error("an unpaired device's offer was kept")
	}
}

// TestHoffActivityEndpoint drives the activity endpoint the way a paired
// device does: an unpaired key and a bad activity are refused, a sent
// activity becomes the Home card's offer, and a pull reads what this device
// is doing.
func TestHoffActivityEndpoint(t *testing.T) {
	peerTestState(t)
	hoffTestState(t)
	self := peerTestIdentity(t, "Brick")
	caller := peerTestIdentity(t, "Smart Pro")
	stranger := peerTestIdentity(t, "Stranger")
	peerTestUse(self)
	peerAddPaired(peerRecord{ID: caller.id, Name: "Smart Pro"})
	peerEnabledFlag.Store(true)
	hoffOffersFlag.Store(true)
	h := peerHandler()
	call := func(who peerIdentity, method string, body interface{}) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, peerTestRequest(t, who, method, "/peer/v1/activity", body))
		return rec
	}
	now := time.Now()
	act := hoffActivity{Kind: "game", Title: "Metroid Fusion", System: "GBA", Path: "/roms/gba/Metroid Fusion.gba", At: now.Add(-time.Minute).Unix()}
	if rec := call(stranger, http.MethodPost, hoffMessage{V: 1, Now: now.Unix(), Activity: &act, Sent: true}); rec.Code != http.StatusForbidden {
		t.Fatalf("an unpaired device: %d", rec.Code)
	}
	bad := hoffActivity{Kind: "shell", Title: "rm", At: now.Unix()}
	if rec := call(caller, http.MethodPost, hoffMessage{V: 1, Now: now.Unix(), Activity: &bad}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an activity of an unknown kind: %d", rec.Code)
	}
	if rec := call(caller, http.MethodPost, hoffMessage{V: 1, Now: now.Unix(), Activity: &act, Sent: true}); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	o, ok := hoffPendingOffer(time.Now())
	if !ok || o.PeerID != caller.id || o.PeerName != "Smart Pro" || !o.Sent || !reflect.DeepEqual(o.Activity, act) {
		t.Fatalf("offer %+v, %v", o, ok)
	}
	hoffDismiss(o)
	if _, ok := hoffPendingOffer(time.Now()); ok {
		t.Fatal("a dismissed offer is still shown")
	}

	mine := hoffActivity{Kind: "web", Title: "example.com", URL: "https://example.com/", At: now.Unix()}
	hoffState.mu.Lock()
	hoffState.current, hoffState.hasCur = mine, true
	hoffState.mu.Unlock()
	rec := call(caller, http.MethodGet, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull: %d", rec.Code)
	}
	var msg hoffMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.V != 1 || msg.Activity == nil || !reflect.DeepEqual(*msg.Activity, mine) {
		t.Fatalf("pulled %+v", msg)
	}
}

func TestHoffSendTargetsNameTwins(t *testing.T) {
	peerTestState(t)
	a := peerTestIdentity(t, "A")
	b := peerTestIdentity(t, "B")
	c := peerTestIdentity(t, "C")
	peerAddPaired(peerRecord{ID: a.id, Name: "Brick"})
	peerAddPaired(peerRecord{ID: b.id, Name: "Brick"})
	peerAddPaired(peerRecord{ID: c.id, Name: "Desk PC"})
	peerEnabledFlag.Store(false)
	if len(hoffSendTargets()) != 0 {
		t.Fatal("Send to is offered while Nearby devices is off")
	}
	peerEnabledFlag.Store(true)
	names := map[string]bool{}
	for _, p := range hoffSendTargets() {
		names[p.Name] = true
	}
	for _, want := range []string{"Brick (" + peerShortID(a.id) + ")", "Brick (" + peerShortID(b.id) + ")", "Desk PC"} {
		if !names[want] {
			t.Errorf("%q is missing from %v", want, names)
		}
	}
	if len(names) != 3 {
		t.Errorf("targets %v, want three distinct rows", names)
	}
}

func TestHoffSignatureMovesEveryHalfMinute(t *testing.T) {
	a := hoffActivity{Kind: "video", Title: "Clip", Position: 10}
	b, c := a, a
	b.Position, c.Position = 25, 31
	if hoffSignature(a) != hoffSignature(b) {
		t.Error("15 seconds of playback asked for a push")
	}
	if hoffSignature(a) == hoffSignature(c) {
		t.Error("crossing a half minute did not ask for a push")
	}
	if !hoffSameActivity(a, c) {
		t.Error("the same video at another position is another activity")
	}
}

func TestHoffBaseNameAndHost(t *testing.T) {
	for _, tc := range [][2]string{
		{"https://cdn.example.com/media/My%20Song.mp3?token=1", "My Song"},
		{`C:\Music\Track 01.flac`, "Track 01"},
		{"/roms/gba/Zelda", "Zelda"},
		{"", ""},
	} {
		if got := hoffBaseName(tc[0]); got != tc[1] {
			t.Errorf("hoffBaseName(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
	if got := hoffHost("https://www.example.com/a/b/"); got != "example.com/a/b" {
		t.Errorf("hoffHost = %q", got)
	}
}
