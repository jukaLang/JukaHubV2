package main

// calsync_caldav.go — reading a CalDAV server (RFC 4791): iCloud, Fastmail,
// Yahoo, Zoho, Yandex, Nextcloud, Radicale, Baïkal…
//
// The address a person types is rarely the calendar itself, so discovery
// walks the standard chain: PROPFIND the address (then /.well-known/caldav)
// for the signed-in user's principal, the principal for its calendar home,
// and the home one level deep for the calendars in it — those that hold
// events (VEVENT), with their names and colours. Each calendar then answers
// one REPORT calendar-query for the events of the sync window, as iCalendar
// text that calsync_ics.go reads.
//
// Read-only: nothing is ever written back. Events made on the handheld stay
// a follow-up.
//
// The password travels as HTTP Basic auth, so only over HTTPS — a server on
// the home network (a private address, .local, .lan) may use plain HTTP.
// Redirects are followed by hand: Go's client turns a redirected PROPFIND
// into a GET and drops the password for another host, and iCloud answers
// from p##-caldav.icloud.com. The password only follows a redirect (or an
// absolute link in an answer) to the same host or a sibling under the same
// parent domain (caldav.icloud.com → p12-caldav.icloud.com), never to a
// stranger.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// caldavMaxXML bounds one answer: a year of a busy calendar as one
	// REPORT is a few MB.
	caldavMaxXML = 16 << 20
	// caldavMaxCalendars bounds the calendars read from one home.
	caldavMaxCalendars = 24
	caldavMaxRedirects = 5
)

// caldavStatusError is a server answer that was not a success.
type caldavStatusError struct {
	code int
	host string
}

func (e *caldavStatusError) Error() string {
	return fmt.Sprintf("%s answered HTTP %d", e.host, e.code)
}

// caldavConflictError reports a 412 Precondition Failed: the server's copy
// changed since we read it. The caller must re-sync and retry.
type caldavConflictError struct{ host string }

func (e *caldavConflictError) Error() string {
	return fmt.Sprintf("%s: the event changed on the server; re-sync and retry", e.host)
}

// caldavClient is one sign-in to one server.
type caldavClient struct {
	origin *url.URL
	user   string
	pass   string
	http   *http.Client
}

// caldavCalendarInfo is one calendar found in a home.
type caldavCalendarInfo struct {
	URL   string
	Name  string
	Color string // "#rrggbb" or ""
}

// ── Multistatus ───────────────────────────────────────────────────────────

type caldavMultistatus struct {
	XMLName   xml.Name         `xml:"DAV: multistatus"`
	Responses []caldavResponse `xml:"DAV: response"`
}

type caldavResponse struct {
	Href      string           `xml:"DAV: href"`
	Propstats []caldavPropstat `xml:"DAV: propstat"`
}

type caldavPropstat struct {
	Status string     `xml:"DAV: status"`
	Prop   caldavProp `xml:"DAV: prop"`
}

type caldavHrefs struct {
	Hrefs []string `xml:"DAV: href"`
}

type caldavProp struct {
	CurrentUserPrincipal caldavHrefs `xml:"DAV: current-user-principal"`
	CalendarHomeSet      caldavHrefs `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	ResourceType         struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"DAV: resourcetype"`
	DisplayName    string `xml:"DAV: displayname"`
	Color          string `xml:"http://apple.com/ns/ical/ calendar-color"`
	SupportedComps struct {
		Comps []struct {
			Name string `xml:"name,attr"`
		} `xml:"urn:ietf:params:xml:ns:caldav comp"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	GetETag      string `xml:"DAV: getetag"`
}

// ok reports whether a propstat's status line is a 2xx (a missing status
// counts as fine: some servers leave it out).
func (p caldavPropstat) ok() bool {
	f := strings.Fields(p.Status)
	return len(f) < 2 || strings.HasPrefix(f[1], "2")
}

// caldavParseMultistatus reads a 207 answer.
func caldavParseMultistatus(data []byte) (*caldavMultistatus, error) {
	var ms caldavMultistatus
	dec := xml.NewDecoder(bytes.NewReader(data))
	// Servers declare odd charsets for what is UTF-8 in practice.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&ms); err != nil {
		return nil, errors.New("the server's answer is not a CalDAV multistatus")
	}
	return &ms, nil
}

// props merges the successful propstats of a response.
func (r caldavResponse) props() caldavProp {
	var out caldavProp
	for _, ps := range r.Propstats {
		if !ps.ok() {
			continue
		}
		p := ps.Prop
		if len(p.CurrentUserPrincipal.Hrefs) > 0 {
			out.CurrentUserPrincipal = p.CurrentUserPrincipal
		}
		if len(p.CalendarHomeSet.Hrefs) > 0 {
			out.CalendarHomeSet = p.CalendarHomeSet
		}
		if p.ResourceType.Calendar != nil {
			out.ResourceType = p.ResourceType
		}
		if p.DisplayName != "" {
			out.DisplayName = p.DisplayName
		}
		if p.Color != "" {
			out.Color = p.Color
		}
		if len(p.SupportedComps.Comps) > 0 {
			out.SupportedComps = p.SupportedComps
		}
		if p.CalendarData != "" {
			out.CalendarData = p.CalendarData
		}
	}
	return out
}

// caldavColor turns "#RRGGBBAA" (Apple's form) or "#RRGGBB" into "#rrggbb".
func caldavColor(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 9 && s[0] == '#' {
		s = s[:7]
	}
	if _, ok := parseHexColor(s); !ok || len(s) != 7 {
		return ""
	}
	return strings.ToLower(s)
}

// ── Requests ──────────────────────────────────────────────────────────────

// caldavCheckURL refuses a password over plain HTTP to a server on the
// internet.
func caldavCheckURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if chatVoiceLocalHost(u.Hostname()) {
			return nil
		}
		return errors.New("a CalDAV password is only sent over https:// (http:// is allowed for servers on your home network)")
	}
	return errors.New("enter a CalDAV address that starts with https://")
}

// caldavSameSite reports whether the password may go to host: the server the
// person typed, or a sibling under its parent domain.
func (c *caldavClient) caldavSameSite(u *url.URL) bool {
	want := strings.ToLower(c.origin.Hostname())
	got := strings.ToLower(u.Hostname())
	if got == want {
		return true
	}
	_, parent, ok := strings.Cut(want, ".")
	if !ok || !strings.Contains(parent, ".") {
		return false
	}
	// "example.co.uk" has the parent "co.uk", which is everyone's: a
	// registry's second level ("co", "com", "org"… under a two-letter
	// country) is no family of hosts.
	if label, tld, _ := strings.Cut(parent, "."); !strings.Contains(tld, ".") && len(tld) == 2 {
		switch label {
		case "co", "com", "net", "org", "gov", "edu", "ac", "or", "ne", "go", "gob", "nic", "ltd", "plc":
			return false
		}
	}
	return strings.HasSuffix(got, "."+parent)
}

// do sends one WebDAV request with the password, following redirects by
// hand, and returns the body of a 2xx answer (bounded).
// doRaw sends one WebDAV request like do, but returns the HTTP status code and
// response headers instead of turning a non-2xx answer into an error. Used by
// the write-back path, which needs to tell 412 Precondition Failed from
// success. contentType is the request Content-Type (empty => application/xml);
// extra holds additional request headers (If-Match, If-None-Match, ...).
func (c *caldavClient) doRaw(ctx context.Context, method, rawURL, depth, body, contentType string, extra map[string]string) ([]byte, *url.URL, int, http.Header, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, 0, nil, errors.New("the CalDAV address is not valid")
	}
	if contentType == "" {
		contentType = `application/xml; charset="utf-8"`
	}
	for hop := 0; ; hop++ {
		if err := caldavCheckURL(u); err != nil {
			return nil, u, 0, nil, err
		}
		if !c.caldavSameSite(u) {
			return nil, u, 0, nil, fmt.Errorf("the server sent the calendar to another site (%s); not following it with your password", u.Hostname())
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
		if err != nil {
			return nil, u, 0, nil, err
		}
		req.SetBasicAuth(c.user, c.pass)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("User-Agent", "JukaHub/"+versionString(nil))
		if depth != "" {
			req.Header.Set("Depth", depth)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, u, 0, nil, err
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, caldavMaxXML+1))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc := resp.Header.Get("Location")
			if loc == "" || hop >= caldavMaxRedirects {
				return data, u, resp.StatusCode, resp.Header, &caldavStatusError{code: resp.StatusCode, host: u.Hostname()}
			}
			next, perr := u.Parse(loc)
			if perr != nil {
				return data, u, resp.StatusCode, resp.Header, errors.New("the server sent a broken redirect")
			}
			u = next
			continue
		}
		if rerr != nil {
			return data, u, resp.StatusCode, resp.Header, rerr
		}
		if len(data) > caldavMaxXML {
			return data, u, resp.StatusCode, resp.Header, errors.New("the server's answer is too large")
		}
		return data, u, resp.StatusCode, resp.Header, nil
	}
}

// do keeps its old contract: a 2xx body, or an error. It is now a thin wrapper
// over doRaw.
func (c *caldavClient) do(ctx context.Context, method, rawURL, depth, body string) ([]byte, *url.URL, error) {
	data, base, status, _, err := c.doRaw(ctx, method, rawURL, depth, body, "", nil)
	if err != nil {
		return data, base, err
	}
	if status < 200 || status >= 300 {
		host := ""
		if base != nil {
			host = base.Hostname()
		}
		return data, base, &caldavStatusError{code: status, host: host}
	}
	return data, base, nil
}

// putEvent creates or updates one event resource. etag is the resource's
// current ETag for an update (sent as If-Match); pass "" to create (sent as
// If-None-Match: *). Returns the new ETag from the response (or the supplied
// one when the server omits it).
func (c *caldavClient) putEvent(ctx context.Context, href, ics, etag string) (string, error) {
	extra := map[string]string{}
	if etag == "" {
		extra["If-None-Match"] = "*"
	} else {
		extra["If-Match"] = etag
	}
	extra["Content-Type"] = `text/calendar; charset="utf-8"`
	data, base, status, hdr, err := c.doRaw(ctx, "PUT", href, "", ics, `text/calendar; charset="utf-8"`, extra)
	_ = data
	_ = base
	if err != nil {
		return etag, err
	}
	if status == 412 {
		return etag, &caldavConflictError{host: hostOf(base, href)}
	}
	if status < 200 || status >= 300 {
		return etag, &caldavStatusError{code: status, host: hostOf(base, href)}
	}
	if ne := strings.TrimSpace(hdr.Get("ETag")); ne != "" {
		return ne, nil
	}
	return etag, nil
}

// deleteEvent removes one event resource. etag, when non-empty, is sent as
// If-Match; an empty etag deletes unconditionally.
func (c *caldavClient) deleteEvent(ctx context.Context, href, etag string) error {
	extra := map[string]string{}
	if etag != "" {
		extra["If-Match"] = etag
	}
	_, base, status, _, err := c.doRaw(ctx, "DELETE", href, "", "", "", extra)
	if err != nil {
		return err
	}
	if status == 412 {
		return &caldavConflictError{host: hostOf(base, href)}
	}
	if status < 200 || status >= 300 {
		return &caldavStatusError{code: status, host: hostOf(base, href)}
	}
	return nil
}

func hostOf(base *url.URL, href string) string {
	if base != nil {
		return base.Hostname()
	}
	if u, err := url.Parse(href); err == nil {
		return u.Hostname()
	}
	return ""
}

// propfind runs a PROPFIND and parses its multistatus. base is the URL that
// answered (after redirects), for resolving relative hrefs.
func (c *caldavClient) propfind(ctx context.Context, rawURL, depth, props string) (*caldavMultistatus, *url.URL, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/">` +
		`<d:prop>` + props + `</d:prop></d:propfind>`
	data, base, err := c.do(ctx, "PROPFIND", rawURL, depth, body)
	if err != nil {
		return nil, base, err
	}
	ms, err := caldavParseMultistatus(data)
	return ms, base, err
}

// resolve turns an href from an answer into an absolute URL.
func caldavResolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || base == nil {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// caldavSameResource compares two URLs by host and path, ignoring a trailing
// slash (an href names itself either way).
func caldavSameResource(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && strings.TrimSuffix(ua.Path, "/") == strings.TrimSuffix(ub.Path, "/")
}

// caldavTryWellKnown reports whether a failed first PROPFIND is worth a try
// at /.well-known/caldav: a server answer other than a refused login.
func caldavTryWellKnown(err error) bool {
	var se *caldavStatusError
	return errors.As(err, &se) && se.code != 401 && se.code != 403
}

// findHome asks rawURL for the signed-in user's calendar home (through the
// principal when the answer only names that). cal is set instead when the
// address is a calendar itself.
func (c *caldavClient) findHome(ctx context.Context, rawURL string) (cal *caldavCalendarInfo, home string, err error) {
	const startProps = `<d:current-user-principal/><c:calendar-home-set/><d:resourcetype/><d:displayname/><a:calendar-color/>`
	ms, base, err := c.propfind(ctx, rawURL, "0", startProps)
	if err != nil {
		return nil, "", err
	}
	for _, r := range ms.Responses {
		p := r.props()
		if p.ResourceType.Calendar != nil {
			return &caldavCalendarInfo{URL: caldavResolve(base, r.Href), Name: strings.TrimSpace(p.DisplayName), Color: caldavColor(p.Color)}, "", nil
		}
		if len(p.CalendarHomeSet.Hrefs) > 0 {
			home = caldavResolve(base, p.CalendarHomeSet.Hrefs[0])
		}
		if home == "" && len(p.CurrentUserPrincipal.Hrefs) > 0 {
			principal := caldavResolve(base, p.CurrentUserPrincipal.Hrefs[0])
			pms, pbase, perr := c.propfind(ctx, principal, "0", `<c:calendar-home-set/>`)
			if perr != nil {
				return nil, "", perr
			}
			for _, pr := range pms.Responses {
				if pp := pr.props(); len(pp.CalendarHomeSet.Hrefs) > 0 {
					home = caldavResolve(pbase, pp.CalendarHomeSet.Hrefs[0])
				}
			}
		}
	}
	return nil, home, nil
}

// discover finds the calendars that hold events, starting at rawURL.
func (c *caldavClient) discover(ctx context.Context, rawURL string) ([]caldavCalendarInfo, error) {
	cal, home, err := c.findHome(ctx, rawURL)
	if (err == nil && cal == nil && home == "") || (err != nil && caldavTryWellKnown(err)) {
		// The address may be the bare server: try its well-known entry. A
		// first answer that only lacked the home keeps its own complaint.
		wk := *c.origin
		wk.Path, wk.RawQuery = "/.well-known/caldav", ""
		if !caldavSameResource(wk.String(), rawURL) {
			wcal, whome, werr := c.findHome(ctx, wk.String())
			if werr == nil || err != nil {
				cal, home, err = wcal, whome, werr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if cal != nil {
		// The address is a calendar itself.
		return []caldavCalendarInfo{*cal}, nil
	}
	if home == "" {
		return nil, errors.New("the server did not say where your calendars are — check the CalDAV address")
	}
	hms, hbase, err := c.propfind(ctx, home, "1",
		`<d:resourcetype/><d:displayname/><a:calendar-color/><c:supported-calendar-component-set/>`)
	if err != nil {
		return nil, err
	}
	var out []caldavCalendarInfo
	for _, r := range hms.Responses {
		p := r.props()
		if p.ResourceType.Calendar == nil {
			continue
		}
		if comps := p.SupportedComps.Comps; len(comps) > 0 {
			events := false
			for _, comp := range comps {
				if strings.EqualFold(comp.Name, "VEVENT") {
					events = true
				}
			}
			if !events {
				continue // a task list or a journal
			}
		}
		u := caldavResolve(hbase, r.Href)
		if u == "" || caldavSameResource(u, home) {
			continue
		}
		out = append(out, caldavCalendarInfo{URL: u, Name: strings.TrimSpace(p.DisplayName), Color: caldavColor(p.Color)})
		if len(out) >= caldavMaxCalendars {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the account has no calendars with events")
	}
	return out, nil
}

// caldavUTC formats a time-range bound.
func caldavUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// events reads one calendar's events that touch [from, to), expanded.
func (c *caldavClient) events(ctx context.Context, cal caldavCalendarInfo, from, to time.Time) ([]calEvent, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` +
		`<d:prop><d:getetag/><c:calendar-data/></d:prop>` +
		`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
		`<c:time-range start="` + caldavUTC(from) + `" end="` + caldavUTC(to) + `"/>` +
		`</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`
	data, base, err := c.do(ctx, "REPORT", cal.URL, "1", body)
	if err != nil {
		return nil, err
	}
	ms, err := caldavParseMultistatus(data)
	if err != nil {
		return nil, err
	}
	return caldavEventsFrom(ms, base, from, to), nil
}

// caldavEventsFrom expands every calendar-data of a REPORT answer. Pure.
func caldavEventsFrom(ms *caldavMultistatus, base *url.URL, from, to time.Time) []calEvent {
	var out []calEvent
	for _, r := range ms.Responses {
		p := r.props()
		if strings.TrimSpace(p.CalendarData) == "" {
			continue
		}
		cal, err := icsParse([]byte(p.CalendarData))
		if err != nil {
			continue
		}
		resURL := caldavResolve(base, r.Href)
		etag := strings.TrimSpace(p.GetETag)
		expanded := cal.expand(from, to, icsMaxInstances-len(out))
		for i := range expanded {
			expanded[i].URL = resURL
			expanded[i].ETag = etag
			expanded[i].SourceKind = "caldav"
		}
		out = append(out, expanded...)
		if len(out) >= icsMaxInstances {
			break
		}
	}
	return out
}
