package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSteamAchievementsFetchAndOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "0123456789ABCDEF0123456789ABCDEF" || r.URL.Query().Get("appid") != "123" {
			t.Errorf("bad query on %s: %s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetSchemaForGame/v2/"):
			_, _ = w.Write([]byte(`{"game":{"availableGameStats":{"achievements":[
				{"name":"LOSE","displayName":"First Loss","description":"Lose a match"},
				{"name":"WIN","displayName":"First Win","description":"Win a match"}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/GetUserStatsForGame/v2/"):
			if r.URL.Query().Get("steamid") != "76561198000000000" {
				t.Errorf("no steam id in %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"playerstats":{"achievements":[
				{"name":"LOSE","achieved":0,"unlocktime":0},
				{"name":"WIN","achieved":1,"unlocktime":1700000000}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := steamAchBase
	steamAchBase = srv.URL
	defer func() { steamAchBase = old }()

	list, err := steamAchFetch(context.Background(), "0123456789ABCDEF0123456789ABCDEF", "76561198000000000", 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("achievements = %d, want 2", len(list))
	}
	if list[0].Name != "First Win" || !list[0].Unlocked || list[0].When.Unix() != 1700000000 {
		t.Fatalf("newest unlocked = %+v", list[0])
	}
	if list[1].Name != "First Loss" || list[1].Unlocked {
		t.Fatalf("locked = %+v", list[1])
	}
	lines := strings.Join(steamAchLines("A Game", list, false, ""), "\n")
	for _, want := range []string{"A Game", "Unlocked 1 of 2", "First Win", "○ First Loss"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("card has no %q:\n%s", want, lines)
		}
	}
}

func TestSteamAchievementsNeedsAnAccount(t *testing.T) {
	if _, err := steamAchFetch(context.Background(), "", "76561198000000000", 123); err == nil {
		t.Fatal("a fetch without an API key should fail")
	}
	lines := strings.Join(steamAchLines("A Game", nil, true, ""), "\n")
	if !strings.Contains(lines, "Reading achievements") {
		t.Fatalf("loading card = %s", lines)
	}
}
