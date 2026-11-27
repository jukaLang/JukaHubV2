package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// notifPhoneTestHistory replaces the centre's history for one test and puts
// the previous rows back afterwards.
func notifPhoneTestHistory(t *testing.T, rows ...notification) {
	t.Helper()
	notifHistoryMu.Lock()
	old, oldSeq := nc.history, notifSeq
	nc.history = append([]notification(nil), rows...)
	notifSeq = uint64(len(rows))
	notifHistoryMu.Unlock()
	t.Cleanup(func() {
		notifHistoryMu.Lock()
		nc.history, notifSeq = old, oldSeq
		notifHistoryMu.Unlock()
	})
}

func notifPhoneTestPost(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := companionRequest(http.MethodPost, "/api/notifications", "123456", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCompanionNotificationsListAndSafeActions(t *testing.T) {
	notifPhoneTestHistory(t,
		notification{ID: 3, Message: "An update is ready", Priority: notifWarning, Source: notifSrcUpdates,
			Action: notifAction{Label: "Install update", Kind: notifActUpdate}, Repeat: 1, Timestamp: time.Now()},
		notification{ID: 2, Message: "Download finished", Priority: notifSuccess, Source: notifSrcDownloads,
			Action: notifAction{Label: "Open", Kind: notifActDownloads}, Read: true, Timestamp: time.Now()},
		notification{ID: 1, Message: "Plain note", Priority: notifInfo, Timestamp: time.Now()},
	)
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/notifications", "123456", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rec.Code, rec.Body)
	}
	var st companionNotifState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Rows) != 3 || st.Unread != 2 {
		t.Fatalf("state = %+v, want three rows and two unread", st)
	}
	if st.Rows[0].Priority != "warning" || st.Rows[0].Source != "Updates" || st.Rows[0].Action != nil {
		t.Errorf("update row = %+v, want a warning from Updates with no phone action", st.Rows[0])
	}
	if st.Rows[1].Action == nil || st.Rows[1].Action.Kind != notifActDownloads || st.Rows[1].Action.Label != "Open" {
		t.Errorf("download row action = %+v, want the safe Open action", st.Rows[1].Action)
	}
	if st.Rows[2].Action != nil {
		t.Errorf("plain row has an action: %+v", st.Rows[2].Action)
	}
	if !notifPhoneSafeAction(notifActScene) || !notifPhoneSafeAction(notifActOpenFile) ||
		!notifPhoneSafeAction(notifActDownloadRetry) || notifPhoneSafeAction(notifActUpdate) {
		t.Fatal("the phone action allowlist does not keep installs on the handheld")
	}
}

func TestCompanionNotificationsRejectsUnsafeAndStaleActions(t *testing.T) {
	notifPhoneTestHistory(t,
		notification{ID: 7, Message: "An update is ready", Priority: notifWarning,
			Action: notifAction{Label: "Install update", Kind: notifActUpdate}, Timestamp: time.Now()},
		notification{ID: 8, Message: "Download finished", Priority: notifSuccess,
			Action: notifAction{Label: "Open", Kind: notifActDownloads}, Timestamp: time.Now()},
	)
	srv, _ := newTestCompanion(t)
	handler := srv.handler()

	if rec := notifPhoneTestPost(t, handler, `{"id":7,"action":"update.offer"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("install from phone status = %d, want 403 (body %s)", rec.Code, rec.Body)
	}
	notifHistoryMu.Lock()
	stillThere := len(nc.history) == 2 && nc.history[0].ID == 7
	notifHistoryMu.Unlock()
	if !stillThere {
		t.Fatal("a refused action removed its row")
	}
	if rec := notifPhoneTestPost(t, handler, `{"id":8,"action":"update.offer"}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale action status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if rec := notifPhoneTestPost(t, handler, `{"id":999,"action":"downloads.open"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing row status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

func TestCompanionNotificationsRunsSafeAction(t *testing.T) {
	notifPhoneTestHistory(t,
		notification{ID: 9, Message: "Download finished", Priority: notifSuccess,
			Action: notifAction{Label: "Open", Kind: notifActDownloads}, Timestamp: time.Now()},
	)
	srv, _ := newTestCompanion(t)
	handler := srv.handler()

	oldRun := notifPhoneRunAction
	var gotKind, gotArg string
	notifPhoneRunAction = func(kind, arg string) { gotKind, gotArg = kind, arg }
	t.Cleanup(func() { notifPhoneRunAction = oldRun })
	if rec := notifPhoneTestPost(t, handler, `{"id":9,"action":"downloads.open"}`); rec.Code != http.StatusOK {
		t.Fatalf("safe action status = %d, body %s", rec.Code, rec.Body)
	}
	// Run only this handler's queued closure; the main loop is not running in
	// a test.
	tbxUIQueue.mu.Lock()
	queued := tbxUIQueue.fns
	tbxUIQueue.fns = nil
	tbxUIQueue.mu.Unlock()
	if len(queued) == 0 {
		t.Fatal("the safe action was not handed to the main thread")
	}
	for _, fn := range queued {
		fn()
	}
	if gotKind != notifActDownloads || gotArg != "" {
		t.Fatalf("dispatched %q %q, want %q", gotKind, gotArg, notifActDownloads)
	}
	notifHistoryMu.Lock()
	stillThere := false
	for _, n := range nc.history {
		if n.ID == 9 {
			stillThere = true
			break
		}
	}
	notifHistoryMu.Unlock()
	if stillThere {
		t.Fatal("the acted row stayed in the history")
	}
}

func TestCompanionNotificationsPage(t *testing.T) {
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/notifications?p=123456", "", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Notifications") ||
		!strings.Contains(rec.Body.String(), "/api/notifications") {
		t.Fatalf("page status = %d, body %.240s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "safe from a phone") {
		t.Fatal("the page does not explain which actions are safe")
	}
}

func TestCompanionNotificationsRouteRequiresPin(t *testing.T) {
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/notifications", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired list status = %d, want 401", rec.Code)
	}
	notifPhoneTestHistory(t, notification{ID: 1, Message: "private", Timestamp: time.Now()})
	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/notifications", "", bytes.NewBufferString(`{"id":1}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired action status = %d, want 401", rec.Code)
	}
}
