package main

// calsync_graph.go — reading and writing a Microsoft account's default
// calendar through Microsoft Graph (MAIL-2, Microsoft half).
//
// Microsoft calendars arrive in JukaHub as ICS subscriptions (read-only);
// this adds a writable path through Graph so the clock's calendar can
// create, update and delete events on an Outlook / Microsoft 365 account,
// with conflict handling matching the CalDAV path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const graphCalendarBase = "https://graph.microsoft.com/v1.0/me/calendar"

type graphEvent struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Body    struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	Start struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"end"`
	Location struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	IsAllDay  bool   `json:"isAllDay"`
	ODataEtag string `json:"@odata.etag"`
	ChangeKey string `json:"changeKey"`
}

// graphConflictError reports a 412 from Graph: the server's copy changed.
type graphConflictError struct{ msg string }

func (e *graphConflictError) Error() string {
	if e.msg == "" {
		return "the event changed on the server; re-sync and retry"
	}
	return e.msg
}

// calFetchGraph reads the default calendar's events in [from, to).
func calFetchGraph(ctx context.Context, a mailcalAccount, from, to time.Time) (calFetchResult, error) {
	token, err := mailMSGraphAccessToken(ctx, a)
	if err != nil {
		return calFetchResult{}, err
	}
	u := graphCalendarBase + "/events"
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("start/dateTime ge '%s' and end/dateTime le '%s'", from.UTC().Format("2006-01-02T15:04:05Z"), to.UTC().Format("2006-01-02T15:04:05Z")))
	q.Set("$select", "id,subject,body,start,end,location,isAllDay,@odata.etag")
	q.Set("$top", "250")
	u += "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return calFetchResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return calFetchResult{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return calFetchResult{}, fmt.Errorf("Microsoft Graph answered HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Value []graphEvent `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return calFetchResult{}, errors.New("Microsoft Graph returned an unreadable answer")
	}
	var evs []calEvent
	for _, ge := range payload.Value {
		evs = append(evs, graphEventToCalEvent(ge))
	}
	return calFetchResult{events: evs, name: a.label() + " (Outlook)", etag: ""}, nil
}

func graphEventToCalEvent(ge graphEvent) calEvent {
	ev := calEvent{
		UID:        ge.ID,
		Title:      ge.Subject,
		Location:   ge.Location.DisplayName,
		Notes:      ge.Body.Content,
		Source:     "Outlook",
		Color:      "",
		AllDay:     ge.IsAllDay,
		URL:        graphCalendarBase + "/events/" + ge.ID,
		ETag:       ge.ODataEtag,
		SourceKind: "graph",
	}
	if t, err := time.Parse("2006-01-02T15:04:05.0000000", ge.Start.DateTime); err == nil {
		ev.Start = t.UTC()
	} else if t, err := time.Parse(time.RFC3339, ge.Start.DateTime); err == nil {
		ev.Start = t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.0000000", ge.End.DateTime); err == nil {
		ev.End = t.UTC()
	} else if t, err := time.Parse(time.RFC3339, ge.End.DateTime); err == nil {
		ev.End = t.UTC()
	}
	if ev.Start.IsZero() {
		ev.Start = time.Now().UTC()
	}
	if ev.End.IsZero() || !ev.End.After(ev.Start) {
		ev.End = ev.Start.Add(time.Hour)
	}
	return ev
}

func calEventToGraphEvent(ev calEvent) map[string]any {
	startTZ := "UTC"
	endTZ := "UTC"
	startDT := ev.Start.UTC().Format("2006-01-02T15:04:05")
	endDT := ev.End.UTC().Format("2006-01-02T15:04:05")
	if ev.AllDay {
		startDT = ev.Start.UTC().Format("2006-01-02") + "T00:00:00"
		endDT = ev.End.UTC().Format("2006-01-02") + "T00:00:00"
	}
	body := map[string]any{"contentType": "text", "content": ev.Notes}
	loc := map[string]any{"displayName": ev.Location}
	return map[string]any{
		"subject":  ev.Title,
		"body":     body,
		"start":    map[string]any{"dateTime": startDT, "timeZone": startTZ},
		"end":      map[string]any{"dateTime": endDT, "timeZone": endTZ},
		"location": loc,
		"isAllDay": ev.AllDay,
	}
}

// calsyncGraphWritable reports whether a source accepts writes via Graph.
func calsyncGraphWritable(src calSource) bool {
	return src.kind == "graph"
}

// calsyncPushGraph creates or updates one event on a Microsoft account.
func calsyncPushGraph(ctx context.Context, src calSource, ev calEvent) (calEvent, error) {
	token, err := mailMSGraphAccessToken(ctx, src.msAccount)
	if err != nil {
		return ev, err
	}
	body, _ := json.Marshal(calEventToGraphEvent(ev))
	if strings.TrimSpace(ev.URL) == "" {
		// Create.
		req, err := http.NewRequestWithContext(ctx, "POST", graphCalendarBase+"/events", strings.NewReader(string(body)))
		if err != nil {
			return ev, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return ev, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ev, fmt.Errorf("Microsoft Graph answered HTTP %d", resp.StatusCode)
		}
		var ge graphEvent
		if err := json.Unmarshal(data, &ge); err != nil {
			return ev, errors.New("Microsoft Graph returned an unreadable answer")
		}
		ev.UID = ge.ID
		ev.URL = graphCalendarBase + "/events/" + ge.ID
		ev.ETag = ge.ODataEtag
		ev.SourceKind = "graph"
		return ev, nil
	}
	// Update.
	req, err := http.NewRequestWithContext(ctx, "PATCH", ev.URL, strings.NewReader(string(body)))
	if err != nil {
		return ev, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if ev.ETag != "" {
		req.Header.Set("If-Match", ev.ETag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ev, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 412 {
		return ev, &graphConflictError{msg: "the event changed on the server; re-sync and retry"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ev, fmt.Errorf("Microsoft Graph answered HTTP %d", resp.StatusCode)
	}
	var ge graphEvent
	if err := json.Unmarshal(data, &ge); err == nil && ge.ODataEtag != "" {
		ev.ETag = ge.ODataEtag
	}
	return ev, nil
}

// calsyncDeleteGraph removes one event via Graph.
func calsyncDeleteGraph(ctx context.Context, src calSource, ev calEvent) error {
	a := src.msAccount
	token, err := mailMSGraphAccessToken(ctx, a)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ev.URL) == "" {
		return errors.New("this event has no Graph resource")
	}
	req, err := http.NewRequestWithContext(ctx, "DELETE", ev.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ev.ETag != "" {
		req.Header.Set("If-Match", ev.ETag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 412 {
		return &graphConflictError{msg: "the event changed on the server; re-sync and retry"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 && resp.StatusCode != 204 {
		return fmt.Errorf("Microsoft Graph answered HTTP %d", resp.StatusCode)
	}
	return nil
}
