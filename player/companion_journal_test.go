package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompanionJournalRoundTrip(t *testing.T) {
	srv, _ := newTestCompanion(t)
	const system, title = "Companion Journal Console", "Phone Game"
	game := libraryGame{System: system, Title: title, Path: "/roms/phone.gba", DisplayTitle: title}
	id := companionGameID(game.Path)
	cg := companionGame{ID: id, Title: game.Label(), Console: system, game: game}

	// The phone's library copy is normally filled on the main loop. A test
	// hands the handler a fixture and puts the global back when it is done.
	companionLib.mu.Lock()
	oldGames, oldByID, oldReady, oldAt := companionLib.games, companionLib.byID, companionLib.ready, companionLib.at
	companionLib.games, companionLib.byID = []companionGame{cg}, map[string]companionGame{id: cg}
	companionLib.ready, companionLib.at = true, time.Now()
	companionLib.mu.Unlock()
	defer func() {
		companionLib.mu.Lock()
		companionLib.games, companionLib.byID, companionLib.ready, companionLib.at = oldGames, oldByID, oldReady, oldAt
		companionLib.mu.Unlock()
	}()

	// No PIN: the journal is as private as the rest of the Games card.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/games/journal?id="+id, "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without PIN = %d, want 401", rec.Code)
	}

	// A POST names a game the phone's library knows and stores its three
	// fields in the same record the handheld's game page reads.
	body := `{"id":"` + id + `","status":"` + gmetaTagBacklog + `","rating":4,"note":"Take this on a trip"}`
	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/games/journal", "123456", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	var answer struct {
		Game companionJournalItem `json:"game"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Game.Status != gmetaTagBacklog || answer.Game.Rating != 4 || answer.Game.Note != "Take this on a trip" {
		t.Fatalf("answer = %+v", answer.Game)
	}
	stored, ok := gmetaGet(system, title)
	if !ok || !gmetaHasTag(stored, gmetaTagBacklog) || stored.Rating != 4 || stored.Notes != "Take this on a trip" {
		t.Fatalf("stored = %+v", stored)
	}

	// A GET reads it back, and lists the statuses the phone may offer.
	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/games/journal?id="+id, "123456", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	var read struct {
		Game     companionJournalItem `json:"game"`
		Statuses []string             `json:"statuses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Game.Status != gmetaTagBacklog || read.Game.Note != "Take this on a trip" {
		t.Fatalf("read = %+v", read.Game)
	}
	if len(read.Statuses) != len(gmetaStatusTags) {
		t.Fatalf("statuses = %v", read.Statuses)
	}

	// Bad values and a made-up game get fixed errors, never a write.
	for _, tc := range []struct {
		body, want string
		code       int
	}{
		{`{"id":"` + id + `","status":"Nonsense"}`, errJournalStatus.Error(), http.StatusBadRequest},
		{`{"id":"` + id + `","rating":9}`, errJournalRating.Error(), http.StatusBadRequest},
		{`{"id":"missing","status":"Playing"}`, errJournalID.Error(), http.StatusNotFound},
	} {
		rec = httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/games/journal", "123456", strings.NewReader(tc.body)))
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s => %d %q, want %d %q", tc.body, rec.Code, rec.Body.String(), tc.code, tc.want)
		}
	}
}
