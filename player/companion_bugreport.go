package main

// A paired phone can write a report with its keyboard and ask the handheld to
// send it. The browser never sees the service's GitHub token. The same report
// format, scrubber and outbox as the handheld form are used here.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type companionBugSubmission struct {
	ID          string `json:"id"`
	Created     string `json:"created"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func companionBugCategoryIndex(name string) int {
	for i, category := range bugrepCategories {
		if strings.EqualFold(strings.TrimSpace(name), category) {
			return i
		}
	}
	return -1
}

func (s *companionServer) handleBugReport(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch req.Method {
	case http.MethodGet:
		pending := make([]map[string]string, 0)
		for _, report := range bugrepQueued() {
			if len(pending) == 10 {
				break
			}
			pending = append(pending, map[string]string{
				"id": report.ID, "title": report.Title,
				"github_url": bugrepIssueURL(report, 900),
			})
		}
		writeCompanionJSON(w, map[string]interface{}{
			"id": bugrepNewRef(), "created": time.Now().UTC().Format(time.RFC3339), "queued": pending,
		})
	case http.MethodPost:
		s.submitBugReport(w, req)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (s *companionServer) submitBugReport(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, 16<<10)
	var input companionBugSubmission
	dec := json.NewDecoder(req.Body)
	if err := dec.Decode(&input); err != nil {
		http.Error(w, "report is missing or too large", http.StatusBadRequest)
		return
	}
	var extra interface{}
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "send one report at a time", http.StatusBadRequest)
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	category := companionBugCategoryIndex(input.Category)
	created, dateErr := time.Parse(time.RFC3339, strings.TrimSpace(input.Created))
	if !bugrepRefValid(input.ID) || dateErr != nil || category < 0 ||
		(input.Title == "" && input.Description == "") {
		http.Error(w, "add a title or description and try again", http.StatusBadRequest)
		return
	}
	s.bugMu.Lock()
	defer s.bugMu.Unlock()
	// A lost phone response must not create a second report. The page retries
	// with the same reference; a copy already in the outbox is still waiting.
	for _, queued := range bugrepQueued() {
		if queued.ID == input.ID {
			writeCompanionJSON(w, map[string]interface{}{
				"status": "queued", "reference": queued.ID,
				"message":    "Saved on the handheld and waiting to send.",
				"github_url": bugrepIssueURL(queued, 7000),
			})
			return
		}
	}

	form := bugrepForm{Category: category, Title: input.Title, Description: input.Description}
	report := bugrepBuild(nil, form, created)
	report.ID = input.ID
	report.Created = strings.TrimSpace(input.Created)
	if s.reportVersion != "" {
		report.Version = s.reportVersion
	}
	phoneURL := bugrepIssueURL(report, 7000)
	endpoint := s.reportEndpoint
	if endpoint == "" {
		companionBugPhoneFallback(w, report, phoneURL,
			"The report service is unavailable. You can submit the prefilled issue on GitHub.")
		return
	}
	sent, kind, err := bugrepPostFunc(endpoint, report)
	if err == nil {
		tbxPost(func() {
			trophyEvent(tkBugReport)
			notifPush(bugrepSentToast(sent), notifSuccess)
			bugrepCollectorAnswered(endpoint)
		})
		writeCompanionJSON(w, map[string]interface{}{
			"status": "sent", "reference": sent.Ref, "issue": sent.Issue,
			"issue_url": sent.URL, "message": "Bug report sent from the handheld.",
		})
		return
	}
	if kind == bugrepErrRejected {
		companionBugPhoneFallback(w, report, phoneURL,
			"The report service could not accept this report. You can submit it on GitHub.")
		return
	}
	now := time.Now()
	switch kind {
	case bugrepErrSetup:
		bugrepNoteServiceSetupPending(now)
	case bugrepErrUnavailable, bugrepErrTrust:
		bugrepCollectorUnavailable()
	default:
		bugrepNoteSendFailed(now)
	}
	if saveErr := bugrepQueue(report); saveErr != nil {
		companionBugPhoneFallback(w, report, phoneURL,
			"The report could not be saved on the handheld. You can submit it on GitHub.")
		return
	}
	why := bugrepHoldBusy
	copied := false
	switch kind {
	case bugrepErrOffline:
		why = bugrepHoldOffline
	case bugrepErrClock:
		why = bugrepHoldClock
	case bugrepErrSetup:
		why = bugrepHoldSetup
	case bugrepErrUnavailable:
		why = bugrepHoldService
	case bugrepErrTrust:
		why = bugrepHoldTrust
		_, copyErr := bugrepSaveCopy(report)
		copied = copyErr == nil
	}
	message, priority := bugrepQueuedNotice(why, copied)
	tbxPost(func() {
		trophyEvent(tkBugReport)
		notifPush(message, priority)
	})
	writeCompanionJSON(w, map[string]interface{}{
		"status": "queued", "reference": report.ID,
		"message":    "Saved on the handheld. JukaHub will retry automatically.",
		"github_url": phoneURL,
	})
}

func companionBugPhoneFallback(w http.ResponseWriter, report bugrepReport, phoneURL, message string) {
	_, _ = bugrepSaveCopy(report)
	tbxPost(func() {
		notifPush("Bug report "+report.ID+" needs a GitHub submission from your phone", notifWarning)
	})
	writeCompanionJSON(w, map[string]interface{}{
		"status": "phone", "reference": report.ID,
		"message": message, "github_url": phoneURL,
	})
}
