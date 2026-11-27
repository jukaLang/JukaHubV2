package main

import (
	"encoding/json"
	"testing"
)

func TestNotificationLiveDownloadRow(t *testing.T) {
	notifEnsureLoaded()
	notifHistoryMu.Lock()
	oldHistory := append([]notification(nil), nc.history...)
	oldAdded := notifAdded
	nc.history = nil
	notifHistoryMu.Unlock()
	defer func() {
		notifHistoryMu.Lock()
		nc.history = oldHistory
		notifAdded = oldAdded
		notifHistoryMu.Unlock()
	}()

	notifUpdateDownload("Installing ffmpeg", 10, 100, 50)
	rows, _ := notifHistoryState()
	if len(rows) != 1 || rows[0].Key != notifDownloadKey || rows[0].Done != 10 || rows[0].Total != 100 {
		t.Fatalf("first row = %+v", rows)
	}
	notifUpdateDownload("Installing ffmpeg", 20, 100, 60)
	rows, _ = notifHistoryState()
	if len(rows) != 1 || rows[0].Done != 20 {
		t.Fatalf("progress added a row or lost its update: %+v", rows)
	}
	if rows[0].Message != "Installing ffmpeg" {
		t.Fatalf("label = %q", rows[0].Message)
	}
	// An ordinary toast with the same words must not be swallowed by the live
	// row: it would disappear when the download finishes.
	notifRecord("Installing ffmpeg", notifInfo)
	if rows, _ := notifHistoryState(); len(rows) != 2 {
		t.Fatalf("a toast was folded into the live row: %+v", rows)
	}

	// A live row must never be written to the card: a percent tick would
	// otherwise fill the notification file with download history.
	var stored notifStoredFile
	if err := json.Unmarshal(notifEncodeStore(rows), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Rows) != 0 {
		t.Fatalf("stored rows = %+v", stored.Rows)
	}

	notifFinishDownload()
	rows, _ = notifHistoryState()
	if len(rows) != 1 || rows[0].Key == notifDownloadKey {
		t.Fatalf("the live row survived the end of the download: %+v", rows)
	}
}
