package main

import (
	"strings"
	"testing"
	"time"
)

// icsTestCal joins lines with CRLF, as calendar servers send them.
func icsTestCal(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func TestICSParseFoldingEscapesAndTimes(t *testing.T) {
	cal, err := icsParse(icsTestCal(
		"BEGIN:VCALENDAR",
		"X-WR-CALNAME:Work",
		"BEGIN:VEVENT",
		"UID:a1",
		`SUMMARY:Lunch\, then a walk\nwith Ann`,
		" ie",
		"DTSTART:20261118T120000Z",
		"DTEND:20261118T130000Z",
		`LOCATION:Cafe\; Main St`,
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:b2",
		"SUMMARY:Holiday",
		"DTSTART;VALUE=DATE:20261224",
		"DTEND;VALUE=DATE:20261226",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:c3",
		"SUMMARY:Call",
		"DTSTART;TZID=Europe/Berlin:20260701T090000",
		"DURATION:PT30M",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:d4",
		"SUMMARY:Dropped",
		"STATUS:CANCELLED",
		"DTSTART:20261119T120000Z",
		"END:VEVENT",
		"END:VCALENDAR",
	))
	if err != nil {
		t.Fatal(err)
	}
	if cal.Name != "Work" {
		t.Errorf("name = %q", cal.Name)
	}
	evs := cal.expand(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC), 0)
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(evs), evs)
	}
	call, lunch, holiday := evs[0], evs[1], evs[2]
	if call.UID != "c3" || !call.Start.Equal(time.Date(2026, 7, 1, 7, 0, 0, 0, time.UTC)) ||
		!call.End.Equal(time.Date(2026, 7, 1, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("call = %v … %v (%s)", call.Start, call.End, call.UID)
	}
	if lunch.Title != "Lunch, then a walk\nwith Annie" || lunch.Location != "Cafe; Main St" {
		t.Errorf("lunch text = %q / %q", lunch.Title, lunch.Location)
	}
	if !lunch.Start.Equal(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)) || !lunch.End.Equal(time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("lunch = %v … %v", lunch.Start, lunch.End)
	}
	if !holiday.AllDay || !holiday.Start.Equal(time.Date(2026, 12, 24, 0, 0, 0, 0, time.Local)) ||
		!holiday.End.Equal(time.Date(2026, 12, 26, 0, 0, 0, 0, time.Local)) {
		t.Errorf("holiday = %v … %v all-day=%v", holiday.Start, holiday.End, holiday.AllDay)
	}
}

func TestICSWeeklyRuleWithExdateAndCount(t *testing.T) {
	// Mondays and Wednesdays, five in all from Mon 2 Nov 2026: 2, 4, 9, 11,
	// 16 Nov. Wed 4 Nov is skipped but still counts, so 18 Nov is not added.
	cal, err := icsParse(icsTestCal(
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT",
		"UID:w1",
		"SUMMARY:Standup",
		"DTSTART:20261102T100000Z",
		"DTEND:20261102T101500Z",
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE;COUNT=5",
		"EXDATE:20261104T100000Z",
		"END:VEVENT",
		"END:VCALENDAR",
	))
	if err != nil {
		t.Fatal(err)
	}
	evs := cal.expand(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), 0)
	want := []int{2, 9, 11, 16}
	if len(evs) != len(want) {
		t.Fatalf("got %d instances, want %d: %+v", len(evs), len(want), evs)
	}
	for i, d := range want {
		at := time.Date(2026, 11, d, 10, 0, 0, 0, time.UTC)
		if !evs[i].Start.Equal(at) || !evs[i].End.Equal(at.Add(15*time.Minute)) {
			t.Errorf("instance %d = %v, want %v", i, evs[i].Start, at)
		}
	}
}

func TestCalDAVMultistatus(t *testing.T) {
	ics := strings.Join([]string{"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:e1", "SUMMARY:Dentist",
		"DTSTART:20261120T150000Z", "DTEND:20261120T160000Z", "END:VEVENT", "END:VCALENDAR"}, "\n")
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/">
<d:response><d:href>/cal/work/</d:href><d:propstat><d:prop>
<d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
<d:displayname>Work</d:displayname><a:calendar-color>#FF8800FF</a:calendar-color>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
<d:response><d:href>/cal/work/e1.ics</d:href><d:propstat><d:prop>
<c:calendar-data>` + ics + `</c:calendar-data>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`
	ms, err := caldavParseMultistatus([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("got %d responses", len(ms.Responses))
	}
	p := ms.Responses[0].props()
	if p.ResourceType.Calendar == nil || p.DisplayName != "Work" || caldavColor(p.Color) != "#ff8800" {
		t.Errorf("calendar props = %+v", p)
	}
	evs := caldavEventsFrom(ms, nil, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if len(evs) != 1 || evs[0].Title != "Dentist" || !evs[0].Start.Equal(time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("events = %+v", evs)
	}
}
