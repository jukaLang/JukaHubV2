package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func companionBugDraftForTest(t *testing.T, srv *companionServer) companionBugSubmission {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/bug-report", "123456", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("draft answered %d: %s", rec.Code, rec.Body.String())
	}
	var draft companionBugSubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &draft); err != nil || !bugrepRefValid(draft.ID) {
		t.Fatalf("draft = %+v, %v", draft, err)
	}
	if _, err := time.Parse(time.RFC3339, draft.Created); err != nil {
		t.Fatalf("draft creation time %q: %v", draft.Created, err)
	}
	return draft
}

func companionBugPostForTest(t *testing.T, srv *companionServer, draft companionBugSubmission) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/bug-report", "123456", strings.NewReader(string(data))))
	return rec
}

func TestCompanionBugReportSendsFromDevice(t *testing.T) {
	bugrepTestOutbox(t)
	var got bugrepReport
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Errorf("collector got %s", req.Method)
		}
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"issue":41,"url":"https://github.com/jukaLang/JukaHubV2/issues/41"}`)
	}))
	defer collector.Close()
	srv, _ := newTestCompanion(t)
	srv.reportEndpoint, srv.reportVersion = collector.URL, "v1.2.3"
	draft := companionBugDraftForTest(t, srv)
	draft.Category = "crash"
	draft.Title = "Crashes when opening a game"
	draft.Description = "Please contact me at alice@example.com"
	withoutPIN := httptest.NewRecorder()
	srv.handler().ServeHTTP(withoutPIN, companionRequest(http.MethodPost, "/api/bug-report", "", strings.NewReader(`{}`)))
	if withoutPIN.Code != http.StatusUnauthorized {
		t.Fatalf("the report API accepted no PIN: %d", withoutPIN.Code)
	}
	rec := companionBugPostForTest(t, srv, draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit answered %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Status    string `json:"status"`
		Reference string `json:"reference"`
		Issue     int    `json:"issue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "sent" || result.Issue != 41 || result.Reference != draft.ID {
		t.Fatalf("submit result = %+v", result)
	}
	if got.ID != draft.ID || got.Created != draft.Created || got.Version != "v1.2.3" ||
		got.Category != "crash" || strings.Contains(got.Description, "alice@example.com") {
		t.Fatalf("collector got the wrong report: %+v", got)
	}
}

func TestCompanionBugReportQueuesAndOffersGitHub(t *testing.T) {
	bugrepTestOutbox(t)
	srv, _ := newTestCompanion(t)
	srv.reportEndpoint = "https://service.example/api/bug-report"
	calls := 0
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		calls++
		return bugrepSent{}, bugrepErrSetup, errors.New("token missing")
	}
	draft := companionBugDraftForTest(t, srv)
	draft.Category, draft.Title, draft.Description = "ui", "Menu is blank", "After opening Settings"
	for i := 0; i < 2; i++ {
		rec := companionBugPostForTest(t, srv, draft)
		if rec.Code != http.StatusOK {
			t.Fatalf("submit %d answered %d: %s", i, rec.Code, rec.Body.String())
		}
		var result struct {
			Status    string `json:"status"`
			Reference string `json:"reference"`
			GitHubURL string `json:"github_url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "queued" || result.Reference != draft.ID || result.GitHubURL == "" {
			t.Fatalf("submit %d = %+v", i, result)
		}
		parsed, err := url.Parse(result.GitHubURL)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(parsed.Query().Get("title"), "("+draft.ID+")") ||
			!strings.Contains(parsed.Query().Get("body"), "| Created | "+draft.Created+" |") {
			t.Fatalf("phone link cannot be recognized by the retry: %s", result.GitHubURL)
		}
	}
	if calls != 1 || bugrepOutboxCount() != 1 {
		t.Fatalf("same draft was posted %d times with %d saved", calls, bugrepOutboxCount())
	}
	queued := httptest.NewRecorder()
	srv.handler().ServeHTTP(queued, companionRequest(http.MethodGet, "/api/bug-report", "123456", nil))
	var listing struct {
		Queued []struct {
			ID string `json:"id"`
		} `json:"queued"`
	}
	if err := json.Unmarshal(queued.Body.Bytes(), &listing); err != nil || len(listing.Queued) != 1 || listing.Queued[0].ID != draft.ID {
		t.Fatalf("paired phone cannot find its saved report: %+v, %v", listing, err)
	}
	page := httptest.NewRecorder()
	srv.handler().ServeHTTP(page, companionRequest(http.MethodGet, "/?p=123456", "", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "id=submitBug") {
		t.Fatal("paired companion page has no report form")
	}
}

func TestCompanionBugReportOffersGitHubWithoutCollector(t *testing.T) {
	bugrepTestOutbox(t)
	srv, _ := newTestCompanion(t)
	draft := companionBugDraftForTest(t, srv)
	draft.Category, draft.Title = "other", "Controls do nothing"
	rec := companionBugPostForTest(t, srv, draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit answered %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Status    string `json:"status"`
		GitHubURL string `json:"github_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Status != "phone" || result.GitHubURL == "" {
		t.Fatalf("without a service, the phone got %+v, %v", result, err)
	}
}
