package main

// The "Get set up" checklist judges live device state and names the screen that
// fixes each unfinished row. These checks pin the rows, their order and their
// wording for every state a first run can be in.

import (
	"strings"
	"testing"
)

func setupTestActions(calls *[]string) setupActions {
	return setupActions{
		WiFi:              func() { *calls = append(*calls, "wifi") },
		RetroAchievements: func() { *calls = append(*calls, "retroachievements") },
		MediaTools:        func() { *calls = append(*calls, "mediatools") },
		RemotePlay:        func() { *calls = append(*calls, "remoteplay") },
		Companion:         func() { *calls = append(*calls, "companion") },
	}
}

func TestSetupChecklistFreshDevice(t *testing.T) {
	var calls []string
	// A fresh card: offline, no accounts, neither media tool installed.
	fresh := setupEnv{MissingTools: []string{"yt-dlp", "ffmpeg"}}
	rows := setupChecklist(fresh, setupTestActions(&calls))

	wantOrder := []setupRowID{
		setupRowWiFi, setupRowRetroAchievements, setupRowMediaTools,
		setupRowRemotePlay, setupRowCompanion,
	}
	if len(rows) != len(wantOrder) {
		t.Fatalf("checklist has %d rows, want %d", len(rows), len(wantOrder))
	}
	for i, id := range wantOrder {
		if rows[i].ID != id {
			t.Fatalf("row %d is %v, want %v", i, rows[i].ID, id)
		}
		if rows[i].Action == nil {
			t.Fatalf("row %q has no action", rows[i].Title)
		}
		if rows[i].Done {
			t.Fatalf("row %q is done on a fresh device", rows[i].Title)
		}
		if strings.TrimSpace(rows[i].Title) == "" || strings.TrimSpace(rows[i].Detail) == "" {
			t.Fatalf("row %d is missing text: %+v", i, rows[i])
		}
	}
	if got := setupSummary(rows); got != "0 of 5 set up" {
		t.Fatalf("summary = %q, want %q", got, "0 of 5 set up")
	}
}

func TestSetupChecklistReadyDevice(t *testing.T) {
	env := setupEnv{
		Online:         true,
		Network:        "Home 5G",
		RetroUsername:  "Stepan",
		RetroComplete:  true,
		PairedConsoles: 2,
		PSNAccount:     true,
		CompanionPIN:   "482013",
	}
	rows := setupChecklist(env, setupActions{})
	for _, row := range rows {
		if !row.Done {
			t.Fatalf("row %q is not done on a fully configured device: %q", row.Title, row.Detail)
		}
	}
	if got := setupSummary(rows); got != "5 of 5 set up" {
		t.Fatalf("summary = %q, want %q", got, "5 of 5 set up")
	}
	if want := "Home 5G"; !strings.Contains(rows[0].Detail, want) {
		t.Errorf("Wi-Fi detail = %q, want the network name %q", rows[0].Detail, want)
	}
	if want := "Stepan"; !strings.Contains(rows[1].Detail, want) {
		t.Errorf("RetroAchievements detail = %q, want the linked username %q", rows[1].Detail, want)
	}
	if want := "2 consoles"; !strings.Contains(rows[3].Detail, want) {
		t.Errorf("Remote Play detail = %q, want %q", rows[3].Detail, want)
	}
	if want := "482013"; !strings.Contains(rows[4].Detail, want) {
		t.Errorf("companion detail = %q, want the pairing PIN %q", rows[4].Detail, want)
	}
}

func TestSetupChecklistPartialStates(t *testing.T) {
	// Only the tools that are really missing are named, so the row can be
	// acted on without opening a panel that lists everything.
	rows := setupChecklist(setupEnv{MissingTools: []string{"ffmpeg"}}, setupActions{})
	if !strings.Contains(rows[2].Detail, "ffmpeg") {
		t.Errorf("missing tool not named: %q", rows[2].Detail)
	}
	if strings.Contains(rows[2].Detail, "yt-dlp") {
		t.Errorf("installed tool still reported missing: %q", rows[2].Detail)
	}

	// A username without the key is half-linked: the row asks for the key.
	rows = setupChecklist(setupEnv{RetroUsername: "x"}, setupActions{})
	if !strings.Contains(strings.ToLower(rows[1].Detail), "api key") {
		t.Errorf("half-linked RA detail = %q, want it to ask for the API key", rows[1].Detail)
	}

	// Remote Play needs the PSN account before a PIN can pair anything.
	rows = setupChecklist(setupEnv{}, setupActions{})
	if !strings.Contains(rows[3].Detail, "PSN") {
		t.Errorf("unlinked Remote Play detail = %q, want the PSN account hint", rows[3].Detail)
	}

	// A companion that cannot start explains itself instead of repeating the
	// generic hint.
	rows = setupChecklist(setupEnv{CompanionErr: "No Wi-Fi address"}, setupActions{})
	if got := rows[4].Detail; got != "No Wi-Fi address" {
		t.Errorf("companion detail = %q, want the live error", got)
	}
}

func TestSetupChecklistRowsRunTheirAction(t *testing.T) {
	var calls []string
	rows := setupChecklist(setupEnv{}, setupTestActions(&calls))
	for _, row := range rows {
		row.Action()
	}
	want := []string{"wifi", "retroachievements", "mediatools", "remoteplay", "companion"}
	if len(calls) != len(want) {
		t.Fatalf("fired %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("row %d fired %q, want %q", i, calls[i], want[i])
		}
	}
}
