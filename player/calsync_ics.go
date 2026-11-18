package main

// calsync_ics.go — reading iCalendar (.ics, RFC 5545) for the clock's
// calendar: what an .ics / webcal link, a CalDAV server or a loose file hands
// over, turned into the events of a window of days.
//
// Only what a person's calendar actually uses is read: VEVENT with its title,
// place, notes, start and end (DTEND or DURATION), all-day dates, UTC, local
// ("floating") and zoned times, cancelled events, moved or edited instances
// (RECURRENCE-ID), skipped ones (EXDATE), extra ones (RDATE) and the common
// repeat rules (calsync_ics.go's icsRule: DAILY/WEEKLY/MONTHLY/YEARLY with
// INTERVAL, COUNT, UNTIL, BYDAY, BYMONTHDAY, BYMONTH, BYSETPOS). A rule this
// file does not understand still shows the event once, on its first date: a
// calendar that silently dropped an event would be worse than one that shows
// it without its repeats.
//
// Time zones: the Linux build links Go's zone database (tzdata_linux.go), so
// "Europe/Berlin" loads there. Outlook names zones the Windows way ("W. Europe
// Standard Time"), and a PC without Go has no zone database at all, so a zone
// that does not load falls back to the file's own VTIMEZONE block (its
// standard and daylight offsets and their yearly switch rules), and only then
// to the device's local zone.
//
// Everything is bounded: the caller reads at most icsMaxBytes, nesting is
// capped, and one expansion stops at its instance limit. Pure: no I/O.

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// icsMaxBytes is the largest calendar file read (a busy work calendar
	// with years of history is about 1-2 MB).
	icsMaxBytes = 5 << 20
	// icsMaxInstances bounds one source's events in one window.
	icsMaxInstances = 5000
	// icsMaxPeriods bounds how many repeat periods one rule may walk (a daily
	// rule from 2005 to the end of next year is about 8000).
	icsMaxPeriods = 60000
	icsMaxDepth   = 12
)

// icsProp is one content line: NAME;PARAM=value:VALUE.
type icsProp struct {
	Name   string
	Params map[string]string
	Value  string
}

// param is a parameter's value ("" when absent).
func (p icsProp) param(name string) string { return p.Params[name] }

// icsComponent is a BEGIN…END block.
type icsComponent struct {
	Name  string
	Props []icsProp
	Subs  []*icsComponent
}

// prop is the first property with this name.
func (c *icsComponent) prop(name string) (icsProp, bool) {
	for _, p := range c.Props {
		if p.Name == name {
			return p, true
		}
	}
	return icsProp{}, false
}

// icsUnfold splits the text into logical lines: a line that starts with a
// space or a tab continues the one before it (RFC 5545 §3.1).
func icsUnfold(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	raw := strings.Split(data, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line != "" && (line[0] == ' ' || line[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += line[1:]
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// icsParseLine splits one logical line into its name, parameters and value.
// Colons and semicolons inside a quoted parameter value do not count.
func icsParseLine(line string) (icsProp, bool) {
	p := icsProp{}
	inQuote := false
	nameEnd, valueStart := -1, -1
	var params []string
	segStart := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == ';' || c == ':':
			seg := line[segStart:i]
			if nameEnd < 0 {
				nameEnd = i
				p.Name = strings.ToUpper(strings.TrimSpace(seg))
			} else {
				params = append(params, seg)
			}
			segStart = i + 1
			if c == ':' {
				valueStart = i + 1
			}
		}
		if valueStart >= 0 {
			break
		}
	}
	if valueStart < 0 || p.Name == "" {
		return p, false
	}
	p.Value = line[valueStart:]
	for _, kv := range params {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if p.Params == nil {
			p.Params = make(map[string]string, 2)
		}
		p.Params[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return p, true
}

// icsUnescape undoes TEXT escaping: \n, \N, \, \; and \\.
func icsUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		default:
			// \, \; \\ and any stray escape keep the character itself.
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// icsParseTree reads the whole text into its components (VCALENDAR at the
// top). Lines outside any block are ignored.
func icsParseTree(data []byte) ([]*icsComponent, error) {
	if len(data) > icsMaxBytes {
		return nil, errors.New("the calendar file is larger than 5 MB")
	}
	text := string(data)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "?")
	}
	text = strings.TrimPrefix(text, "\ufeff")
	var roots []*icsComponent
	var stack []*icsComponent
	for _, line := range icsUnfold(text) {
		p, ok := icsParseLine(line)
		if !ok {
			continue
		}
		switch p.Name {
		case "BEGIN":
			if len(stack) >= icsMaxDepth {
				return nil, errors.New("the calendar file is nested too deeply")
			}
			c := &icsComponent{Name: strings.ToUpper(strings.TrimSpace(p.Value))}
			if len(stack) == 0 {
				roots = append(roots, c)
			} else {
				top := stack[len(stack)-1]
				top.Subs = append(top.Subs, c)
			}
			stack = append(stack, c)
		case "END":
			name := strings.ToUpper(strings.TrimSpace(p.Value))
			// Close up to the matching BEGIN; an END with no BEGIN is noise.
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].Name == name {
					stack = stack[:i]
					break
				}
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.Props = append(top.Props, p)
			}
		}
	}
	return roots, nil
}

// ── Time zones ────────────────────────────────────────────────────────────

// icsTZRule is one yearly switch of a VTIMEZONE: the n-th weekday (n<0
// counts from the end) of a month at a wall-clock time.
type icsTZRule struct {
	month time.Month
	n     int
	wd    time.Weekday
	h, mi int
}

// icsZone turns a wall-clock time into an instant: a loaded location, or the
// rules of a VTIMEZONE block when no location loads.
type icsZone struct {
	loc *time.Location
	// local reads the device's zone at each use: time.Local is replaced (not
	// changed in place) when JukaHub sets the zone (dateTimeSetLocal), so a
	// pointer kept from start-up would stay on the boot zone.
	local bool
	// std and dst are the offsets east of UTC in seconds; with hasDST the
	// zone switches to dst at toDST (read in standard time) and back at
	// toStd (read in daylight time) each year.
	std, dst     int
	hasDST       bool
	toDST, toStd icsTZRule
}

var (
	icsZoneUTC   = &icsZone{loc: time.UTC}
	icsZoneLocal = &icsZone{local: true}
)

// at is the instant of a wall-clock time in the zone.
func (z *icsZone) at(y int, mo time.Month, d, h, mi, s int) time.Time {
	if z == nil || z.local {
		return time.Date(y, mo, d, h, mi, s, 0, time.Local)
	}
	if z.loc != nil {
		return time.Date(y, mo, d, h, mi, s, 0, z.loc)
	}
	wall := time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	off := z.std
	if z.hasDST {
		year := wall.Year()
		on, onOK := z.toDST.wall(year)
		off2, offOK := z.toStd.wall(year)
		if onOK && offOK {
			var in bool
			if on.Before(off2) { // northern hemisphere
				in = !wall.Before(on) && wall.Before(off2)
			} else { // southern: daylight time spans the new year
				in = !wall.Before(on) || wall.Before(off2)
			}
			if in {
				off = z.dst
			}
		}
	}
	return wall.Add(-time.Duration(off) * time.Second)
}

// location is the zone as a Go location, for reading the date of an instant
// (a VTIMEZONE-only zone answers with its standard offset).
func (z *icsZone) location() *time.Location {
	switch {
	case z == nil || z.local:
		return time.Local
	case z.loc != nil:
		return z.loc
	}
	return time.FixedZone("", z.std)
}

// wall is the rule's switch moment in year as a wall-clock reading (in UTC
// fields, compared with other wall readings only).
func (r icsTZRule) wall(year int) (time.Time, bool) {
	d, ok := icsNthWeekday(year, r.month, r.n, r.wd)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(year, r.month, d, r.h, r.mi, 0, 0, time.UTC), true
}

// icsDaysIn is the length of a month.
func icsDaysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 12, 0, 0, 0, time.UTC).Day()
}

// icsNthWeekday is the day of the month of the n-th wd (n<0: from the end).
func icsNthWeekday(y int, m time.Month, n int, wd time.Weekday) (int, bool) {
	dim := icsDaysIn(y, m)
	if n > 0 {
		first := time.Date(y, m, 1, 12, 0, 0, 0, time.UTC).Weekday()
		d := 1 + (int(wd)-int(first)+7)%7 + (n-1)*7
		return d, d <= dim
	}
	if n < 0 {
		last := time.Date(y, m, dim, 12, 0, 0, 0, time.UTC).Weekday()
		d := dim - (int(last)-int(wd)+7)%7 + (n+1)*7
		return d, d >= 1
	}
	return 0, false
}

// icsWindowsZones maps the zone names Outlook and Exchange write onto the
// IANA names Go loads.
var icsWindowsZones = map[string]string{
	"Dateline Standard Time":          "Etc/GMT+12",
	"Hawaiian Standard Time":          "Pacific/Honolulu",
	"Alaskan Standard Time":           "America/Anchorage",
	"Pacific Standard Time":           "America/Los_Angeles",
	"Pacific Standard Time (Mexico)":  "America/Tijuana",
	"US Mountain Standard Time":       "America/Phoenix",
	"Mountain Standard Time":          "America/Denver",
	"Central Standard Time":           "America/Chicago",
	"Central Standard Time (Mexico)":  "America/Mexico_City",
	"Canada Central Standard Time":    "America/Regina",
	"Eastern Standard Time":           "America/New_York",
	"US Eastern Standard Time":        "America/Indianapolis",
	"Atlantic Standard Time":          "America/Halifax",
	"Newfoundland Standard Time":      "America/St_Johns",
	"SA Pacific Standard Time":        "America/Bogota",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Argentina Standard Time":         "America/Buenos_Aires",
	"UTC":                             "UTC",
	"Coordinated Universal Time":      "UTC",
	"GMT Standard Time":               "Europe/London",
	"Greenwich Standard Time":         "Atlantic/Reykjavik",
	"W. Europe Standard Time":         "Europe/Berlin",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Central European Standard Time":  "Europe/Warsaw",
	"Romance Standard Time":           "Europe/Paris",
	"E. Europe Standard Time":         "Europe/Chisinau",
	"FLE Standard Time":               "Europe/Kiev",
	"GTB Standard Time":               "Europe/Bucharest",
	"Turkey Standard Time":            "Europe/Istanbul",
	"Israel Standard Time":            "Asia/Jerusalem",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"Egypt Standard Time":             "Africa/Cairo",
	"Kaliningrad Standard Time":       "Europe/Kaliningrad",
	"Russian Standard Time":           "Europe/Moscow",
	"Belarus Standard Time":           "Europe/Minsk",
	"Arabian Standard Time":           "Asia/Dubai",
	"Arab Standard Time":              "Asia/Riyadh",
	"Iran Standard Time":              "Asia/Tehran",
	"Ekaterinburg Standard Time":      "Asia/Yekaterinburg",
	"Pakistan Standard Time":          "Asia/Karachi",
	"India Standard Time":             "Asia/Kolkata",
	"Nepal Standard Time":             "Asia/Kathmandu",
	"Central Asia Standard Time":      "Asia/Almaty",
	"N. Central Asia Standard Time":   "Asia/Novosibirsk",
	"SE Asia Standard Time":           "Asia/Bangkok",
	"North Asia Standard Time":        "Asia/Krasnoyarsk",
	"China Standard Time":             "Asia/Shanghai",
	"North Asia East Standard Time":   "Asia/Irkutsk",
	"Singapore Standard Time":         "Asia/Singapore",
	"Taipei Standard Time":            "Asia/Taipei",
	"W. Australia Standard Time":      "Australia/Perth",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Korea Standard Time":             "Asia/Seoul",
	"Yakutsk Standard Time":           "Asia/Yakutsk",
	"Cen. Australia Standard Time":    "Australia/Adelaide",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"E. Australia Standard Time":      "Australia/Brisbane",
	"Vladivostok Standard Time":       "Asia/Vladivostok",
	"Magadan Standard Time":           "Asia/Magadan",
	"New Zealand Standard Time":       "Pacific/Auckland",
	"Ukraine Standard Time":           "Europe/Kiev",
	"Central Pacific Standard Time":   "Pacific/Guadalcanal",
	"Mid-Atlantic Standard Time":      "Etc/GMT+2",
	"Azores Standard Time":            "Atlantic/Azores",
	"Morocco Standard Time":           "Africa/Casablanca",
	"W. Central Africa Standard Time": "Africa/Lagos",
	"E. Africa Standard Time":         "Africa/Nairobi",
}

// icsLoadLocation loads an IANA zone, a Windows zone name, or a TZID with a
// path prefix ("/mozilla.org/20050126_1/America/New_York").
func icsLoadLocation(tzid string) *time.Location {
	tzid = strings.TrimSpace(tzid)
	if tzid == "" {
		return nil
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if iana, ok := icsWindowsZones[tzid]; ok {
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc
		}
	}
	if parts := strings.Split(strings.Trim(tzid, "/"), "/"); len(parts) > 2 {
		if loc, err := time.LoadLocation(strings.Join(parts[len(parts)-2:], "/")); err == nil {
			return loc
		}
	}
	return nil
}

// icsParseOffset reads a UTC offset ("+0100", "-0530", "+013000") in seconds.
func icsParseOffset(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 5 && len(s) != 7 {
		return 0, false
	}
	sign := 1
	switch s[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return 0, false
	}
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[3:5])
	sec := 0
	var err3 error
	if len(s) == 7 {
		sec, err3 = strconv.Atoi(s[5:7])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return sign * (h*3600 + m*60 + sec), true
}

// icsZoneFromVTimezone builds a zone from a VTIMEZONE block: the newest
// STANDARD and DAYLIGHT parts with their yearly rules. A zone without a
// usable daylight rule keeps its standard offset all year.
func icsZoneFromVTimezone(c *icsComponent) *icsZone {
	type part struct {
		start  string
		offset int
		rule   icsTZRule
		ruleOK bool
	}
	var std, dst *part
	for _, sub := range c.Subs {
		if sub.Name != "STANDARD" && sub.Name != "DAYLIGHT" {
			continue
		}
		to, ok := sub.prop("TZOFFSETTO")
		if !ok {
			continue
		}
		off, ok := icsParseOffset(to.Value)
		if !ok {
			continue
		}
		p := &part{offset: off}
		if ds, ok := sub.prop("DTSTART"); ok {
			p.start = strings.TrimSpace(ds.Value)
			if len(p.start) >= 15 {
				p.rule.h, _ = strconv.Atoi(p.start[9:11])
				p.rule.mi, _ = strconv.Atoi(p.start[11:13])
			}
		}
		if rr, ok := sub.prop("RRULE"); ok {
			p.rule, p.ruleOK = icsTZRuleFrom(rr.Value, p.rule.h, p.rule.mi)
		}
		// The newest part wins: zones that changed their rules list the
		// older ones too.
		if sub.Name == "STANDARD" {
			if std == nil || p.start > std.start {
				std = p
			}
		} else if dst == nil || p.start > dst.start {
			dst = p
		}
	}
	if std == nil && dst == nil {
		return nil
	}
	if std == nil {
		return &icsZone{std: dst.offset}
	}
	z := &icsZone{std: std.offset}
	if dst != nil && dst.ruleOK && std.ruleOK {
		z.dst, z.hasDST, z.toDST, z.toStd = dst.offset, true, dst.rule, std.rule
	}
	return z
}

// icsTZRuleFrom reads a VTIMEZONE part's "FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU".
func icsTZRuleFrom(rule string, h, mi int) (icsTZRule, bool) {
	r := icsTZRule{h: h, mi: mi}
	okMonth, okDay := false, false
	for _, kv := range strings.Split(rule, ";") {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "BYMONTH":
			if m, err := strconv.Atoi(v); err == nil && m >= 1 && m <= 12 {
				r.month, okMonth = time.Month(m), true
			}
		case "BYDAY":
			if bd, ok := icsParseByDay(strings.Split(v, ",")[0]); ok {
				r.n, r.wd, okDay = bd.n, bd.wd, true
			}
		}
	}
	if okDay && r.n == 0 {
		// "BYDAY=SU;BYMONTHDAY=8,9,…" (the second Sunday) is how some
		// writers say "2SU"; without the month days it is ambiguous.
		r.n = 1
		for _, kv := range strings.Split(rule, ";") {
			if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "BYMONTHDAY") {
				if d, err := strconv.Atoi(strings.Split(v, ",")[0]); err == nil && d > 0 {
					r.n = (d-1)/7 + 1
				}
			}
		}
	}
	return r, okMonth && okDay
}

// ── Values ────────────────────────────────────────────────────────────────

// icsTimeValue is a parsed DATE or DATE-TIME.
type icsTimeValue struct {
	t      time.Time
	allDay bool
	zone   *icsZone // nil for a date
	y      int
	mo     time.Month
	d      int
	h      int
	mi, s  int
}

// icsParseTime reads one DATE (20261117) or DATE-TIME (20261117T093000,
// …Z for UTC) value; TZID names the zone of a local reading.
func (c *icsCalendar) icsParseTime(value string, params map[string]string) (icsTimeValue, bool) {
	v := strings.TrimSpace(value)
	var tv icsTimeValue
	if len(v) < 8 {
		return tv, false
	}
	y, err1 := strconv.Atoi(v[0:4])
	mo, err2 := strconv.Atoi(v[4:6])
	d, err3 := strconv.Atoi(v[6:8])
	if err1 != nil || err2 != nil || err3 != nil || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return tv, false
	}
	tv.y, tv.mo, tv.d = y, time.Month(mo), d
	if len(v) == 8 || strings.EqualFold(params["VALUE"], "DATE") {
		tv.allDay = true
		tv.t = time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local)
		return tv, true
	}
	if len(v) < 15 || (v[8] != 'T' && v[8] != 't') {
		return tv, false
	}
	h, e1 := strconv.Atoi(v[9:11])
	mi, e2 := strconv.Atoi(v[11:13])
	s, e3 := strconv.Atoi(v[13:15])
	if e1 != nil || e2 != nil || e3 != nil || h > 24 || mi > 59 || s > 60 {
		return tv, false
	}
	tv.h, tv.mi, tv.s = h, mi, s
	switch {
	case strings.HasSuffix(v, "Z") || strings.HasSuffix(v, "z"):
		tv.zone = icsZoneUTC
	case params["TZID"] != "":
		tv.zone = c.zone(params["TZID"])
	default:
		tv.zone = icsZoneLocal
	}
	tv.t = tv.zone.at(y, time.Month(mo), d, h, mi, s)
	return tv, true
}

// icsParseDuration reads "P1D", "PT1H30M", "-PT15M", "P1W".
func icsParseDuration(s string) (time.Duration, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	if !strings.HasPrefix(s, "P") || len(s) < 3 {
		return 0, false
	}
	s = s[1:]
	var total time.Duration
	inTime := false
	num := 0
	digits := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + int(c-'0')
			digits = true
			if num > 1<<20 {
				return 0, false
			}
			continue
		case c == 'T':
			inTime = true
			continue
		}
		if !digits {
			return 0, false
		}
		n := time.Duration(num)
		switch {
		case c == 'W' && !inTime:
			total += n * 7 * 24 * time.Hour
		case c == 'D' && !inTime:
			total += n * 24 * time.Hour
		case c == 'H' && inTime:
			total += n * time.Hour
		case c == 'M' && inTime:
			total += n * time.Minute
		case c == 'S' && inTime:
			total += n * time.Second
		default:
			return 0, false
		}
		num, digits = 0, false
	}
	if neg {
		total = -total
	}
	return total, true
}

// ── Calendar ──────────────────────────────────────────────────────────────

// icsEvent is one VEVENT as read.
type icsEvent struct {
	uid, summary, location, notes string
	start                         icsTimeValue
	dur                           time.Duration // timed events
	days                          int           // all-day events: length in days
	rrule                         string
	rdates                        []icsTimeValue
	exdates                       map[string]bool
	recurKey                      string // RECURRENCE-ID's key, "" for a master
	cancelled                     bool
}

// icsCalendar is a parsed calendar file.
type icsCalendar struct {
	Name   string
	events []icsEvent
	tzs    map[string]*icsComponent
	zones  map[string]*icsZone
}

// zone resolves a TZID: Go's database, a Windows name, the file's own
// VTIMEZONE, then the local zone.
func (c *icsCalendar) zone(tzid string) *icsZone {
	if z, ok := c.zones[tzid]; ok {
		return z
	}
	var z *icsZone
	if loc := icsLoadLocation(tzid); loc != nil {
		z = &icsZone{loc: loc}
	} else if vt, ok := c.tzs[tzid]; ok {
		z = icsZoneFromVTimezone(vt)
	}
	if z == nil {
		icsLogOnce("zone:"+tzid, "time zone %q is unknown; using the device's zone", tzid)
		z = icsZoneLocal
	}
	if c.zones == nil {
		c.zones = make(map[string]*icsZone)
	}
	c.zones[tzid] = z
	return z
}

// icsInstanceKey names one instance for EXDATE and RECURRENCE-ID matching:
// the date for all-day events, the instant otherwise.
func icsInstanceKey(t time.Time, allDay bool) string {
	if allDay {
		return t.Format("20060102")
	}
	return strconv.FormatInt(t.Unix(), 10)
}

var icsLogged sync.Map

// icsLogOnce logs a limitation once per run, without any event text.
func icsLogOnce(key, format string, args ...any) {
	if _, dup := icsLogged.LoadOrStore(key, true); !dup {
		logInfo("calendar", format, args...)
	}
}

// icsParse reads a calendar file's events. An empty calendar is fine; a file
// with no VCALENDAR is an error ("not a calendar").
func icsParse(data []byte) (*icsCalendar, error) {
	roots, err := icsParseTree(data)
	if err != nil {
		return nil, err
	}
	cal := &icsCalendar{tzs: map[string]*icsComponent{}}
	found := false
	for _, root := range roots {
		if root.Name != "VCALENDAR" {
			continue
		}
		found = true
		if p, ok := root.prop("X-WR-CALNAME"); ok && cal.Name == "" {
			cal.Name = strings.TrimSpace(icsUnescape(p.Value))
		}
		for _, sub := range root.Subs {
			if sub.Name == "VTIMEZONE" {
				if id, ok := sub.prop("TZID"); ok {
					cal.tzs[strings.TrimSpace(id.Value)] = sub
				}
			}
		}
		for _, sub := range root.Subs {
			if sub.Name == "VEVENT" {
				if ev, ok := cal.readEvent(sub); ok {
					cal.events = append(cal.events, ev)
				}
			}
		}
	}
	if !found {
		return nil, errors.New("this is not a calendar (no VCALENDAR)")
	}
	return cal, nil
}

// readEvent reads one VEVENT; one without a usable start is skipped.
func (c *icsCalendar) readEvent(v *icsComponent) (icsEvent, bool) {
	ev := icsEvent{}
	var hasEnd, hasDur bool
	var end icsTimeValue
	var dur time.Duration
	okStart := false
	for _, p := range v.Props {
		switch p.Name {
		case "UID":
			ev.uid = strings.TrimSpace(p.Value)
		case "SUMMARY":
			ev.summary = icsUnescape(p.Value)
		case "LOCATION":
			ev.location = icsUnescape(p.Value)
		case "DESCRIPTION":
			ev.notes = icsUnescape(p.Value)
		case "STATUS":
			ev.cancelled = strings.EqualFold(strings.TrimSpace(p.Value), "CANCELLED")
		case "DTSTART":
			ev.start, okStart = c.icsParseTime(p.Value, p.Params)
		case "DTEND":
			end, hasEnd = c.icsParseTime(p.Value, p.Params)
		case "DURATION":
			dur, hasDur = icsParseDuration(p.Value)
		case "RRULE":
			ev.rrule = strings.TrimSpace(p.Value)
		case "RDATE":
			if strings.EqualFold(p.param("VALUE"), "PERIOD") {
				continue
			}
			for _, one := range strings.Split(p.Value, ",") {
				if tv, ok := c.icsParseTime(one, p.Params); ok && len(ev.rdates) < 500 {
					ev.rdates = append(ev.rdates, tv)
				}
			}
		case "EXDATE":
			for _, one := range strings.Split(p.Value, ",") {
				if tv, ok := c.icsParseTime(one, p.Params); ok {
					if ev.exdates == nil {
						ev.exdates = make(map[string]bool)
					}
					// A date drops that day's instance; a date-time drops the
					// instance at that instant, and its wall date ("w…") drops
					// an all-day event's day (writers mix the two forms).
					ev.exdates[icsInstanceKey(tv.t, tv.allDay)] = true
					if !tv.allDay {
						ev.exdates["w"+time.Date(tv.y, tv.mo, tv.d, 12, 0, 0, 0, time.UTC).Format("20060102")] = true
					}
				}
			}
		case "RECURRENCE-ID":
			if tv, ok := c.icsParseTime(p.Value, p.Params); ok {
				ev.recurKey = icsInstanceKey(tv.t, tv.allDay)
			}
		}
	}
	if !okStart {
		return ev, false
	}
	if ev.start.allDay {
		ev.days = 1
		switch {
		case hasEnd && end.allDay:
			if n := icsDayNumber(end.y, end.mo, end.d) - icsDayNumber(ev.start.y, ev.start.mo, ev.start.d); n > 0 {
				ev.days = n
			}
		case hasDur && dur >= 24*time.Hour:
			ev.days = int(dur / (24 * time.Hour))
		}
		if ev.days > 366 {
			ev.days = 366
		}
	} else {
		switch {
		case hasEnd && end.t.After(ev.start.t):
			ev.dur = end.t.Sub(ev.start.t)
		case hasDur && dur > 0:
			ev.dur = dur
		}
		if ev.dur > 366*24*time.Hour {
			ev.dur = 366 * 24 * time.Hour
		}
	}
	return ev, true
}

// icsDayNumber counts days since a fixed epoch, so date differences ignore
// daylight saving.
func icsDayNumber(y int, m time.Month, d int) int {
	return int(time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Unix() / 86400)
}

// instance makes the calEvent of ev starting at t.
func (ev *icsEvent) instance(t time.Time) calEvent {
	out := calEvent{
		UID:      ev.uid,
		Title:    ev.summary,
		Location: ev.location,
		Notes:    ev.notes,
		Start:    t,
		AllDay:   ev.start.allDay,
	}
	if ev.start.allDay {
		out.End = t.AddDate(0, 0, ev.days)
	} else {
		out.End = t.Add(ev.dur)
	}
	return out
}

// calEventOverlaps reports whether e touches [from, to). An event without a
// length counts at its start.
func calEventOverlaps(e calEvent, from, to time.Time) bool {
	if !e.Start.Before(to) {
		return false
	}
	if e.End.After(e.Start) {
		return e.End.After(from)
	}
	return !e.Start.Before(from)
}

// expand lists the calendar's event instances that touch [from, to), sorted
// by start, at most max.
func (c *icsCalendar) expand(from, to time.Time, max int) []calEvent {
	if max <= 0 {
		max = icsMaxInstances
	}
	// Edited single instances replace their master's instance.
	overrides := map[string]bool{}
	for i := range c.events {
		if ev := &c.events[i]; ev.recurKey != "" {
			overrides[ev.uid+"|"+ev.recurKey] = true
		}
	}
	var out []calEvent
	add := func(e calEvent) bool {
		if calEventOverlaps(e, from, to) {
			out = append(out, e)
		}
		return len(out) < max
	}
	for i := range c.events {
		ev := &c.events[i]
		if ev.recurKey != "" {
			// An edited instance stands alone; a cancelled one only hides its
			// master's instance.
			if !ev.cancelled && !add(ev.instance(ev.start.t)) {
				break
			}
			continue
		}
		if ev.cancelled {
			continue
		}
		skip := func(t time.Time) bool {
			key := icsInstanceKey(t, ev.start.allDay)
			if overrides[ev.uid+"|"+key] || ev.exdates[key] {
				return true
			}
			if len(ev.exdates) == 0 {
				return false
			}
			if ev.start.allDay {
				return ev.exdates["w"+key]
			}
			// An EXDATE written as a date drops the instance on that day.
			return ev.exdates[icsInstanceKey(t.In(ev.start.zone.location()), true)]
		}
		var starts []time.Time
		if ev.rrule != "" {
			starts = c.occurrences(ev, from, to, max)
		} else {
			starts = []time.Time{ev.start.t}
		}
		for _, rd := range ev.rdates {
			starts = append(starts, rd.t)
		}
		full := false
		for _, t := range starts {
			if skip(t) {
				continue
			}
			if !add(ev.instance(t)) {
				full = true
				break
			}
		}
		if full {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// ── Repeat rules ──────────────────────────────────────────────────────────

// icsByDay is one BYDAY entry: "MO", "1MO", "-1FR".
type icsByDay struct {
	n  int
	wd time.Weekday
}

var icsWeekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

func icsParseByDay(s string) (icsByDay, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 2 {
		return icsByDay{}, false
	}
	wd, ok := icsWeekdays[s[len(s)-2:]]
	if !ok {
		return icsByDay{}, false
	}
	bd := icsByDay{wd: wd}
	if num := s[:len(s)-2]; num != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(num, "+"))
		if err != nil || n == 0 || n > 53 || n < -53 {
			return icsByDay{}, false
		}
		bd.n = n
	}
	return bd, true
}

// icsRule is a parsed RRULE.
type icsRule struct {
	freq       string
	interval   int
	count      int
	until      time.Time
	hasUntil   bool
	untilTV    icsTimeValue // UNTIL as written, for a floating one
	byDay      []icsByDay
	byMonthDay []int
	byMonth    []int
	bySetPos   []int
	wkst       time.Weekday
}

// icsParseRule reads an RRULE. unsupported names the first part this file
// cannot follow ("" when it can follow all of it).
func (c *icsCalendar) icsParseRule(s string) (r icsRule, unsupported string) {
	r = icsRule{interval: 1, wkst: time.Monday}
	ints := func(v string, lo, hi int) ([]int, bool) {
		var out []int
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || n == 0 || n < lo || n > hi {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	for _, kv := range strings.Split(s, ";") {
		k, v, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		var ok bool
		switch k {
		case "":
			continue
		case "FREQ":
			r.freq = strings.ToUpper(v)
			ok = r.freq == "DAILY" || r.freq == "WEEKLY" || r.freq == "MONTHLY" || r.freq == "YEARLY"
		case "INTERVAL":
			r.interval, _ = strconv.Atoi(v)
			ok = r.interval >= 1 && r.interval <= 1000
		case "COUNT":
			r.count, _ = strconv.Atoi(v)
			ok = r.count >= 1
		case "UNTIL":
			var tv icsTimeValue
			tv, ok = c.icsParseTime(v, nil)
			if ok {
				r.hasUntil = true
				r.until, r.untilTV = tv.t, tv
				if tv.allDay {
					// A date UNTIL includes that whole day.
					r.until = tv.t.AddDate(0, 0, 1).Add(-time.Second)
				}
			}
		case "BYDAY":
			ok = true
			for _, f := range strings.Split(v, ",") {
				bd, good := icsParseByDay(f)
				if !good {
					ok = false
					break
				}
				r.byDay = append(r.byDay, bd)
			}
		case "BYMONTHDAY":
			r.byMonthDay, ok = ints(v, -31, 31)
		case "BYMONTH":
			r.byMonth, ok = ints(v, 1, 12)
		case "BYSETPOS":
			r.bySetPos, ok = ints(v, -366, 366)
		case "WKST":
			r.wkst, ok = icsWeekdays[strings.ToUpper(v)]
		case "BYHOUR", "BYMINUTE", "BYSECOND":
			// One value that repeats DTSTART's own time changes nothing.
			ok = !strings.Contains(v, ",")
		}
		if !ok {
			return r, k
		}
	}
	if r.freq == "" {
		return r, "FREQ"
	}
	for _, bd := range r.byDay {
		if bd.n != 0 && (r.freq == "DAILY" || r.freq == "WEEKLY" || (r.freq == "YEARLY" && len(r.byMonth) == 0)) {
			return r, "BYDAY with a number"
		}
	}
	if len(r.bySetPos) > 0 && (r.freq == "DAILY" || r.freq == "WEEKLY") {
		return r, "BYSETPOS"
	}
	if r.freq == "YEARLY" && len(r.byMonth) == 0 && (len(r.byDay) > 0 || len(r.byMonthDay) > 0) {
		// Days of the whole year rather than of DTSTART's month.
		return r, "YEARLY BYDAY/BYMONTHDAY without BYMONTH"
	}
	return r, ""
}

// icsHasMonth reports whether m is allowed by BYMONTH (all when empty).
func icsHasMonth(r *icsRule, m time.Month) bool {
	if len(r.byMonth) == 0 {
		return true
	}
	for _, x := range r.byMonth {
		if time.Month(x) == m {
			return true
		}
	}
	return false
}

// icsMonthDays lists the days of month y/m a MONTHLY or YEARLY rule picks,
// sorted. startDay is DTSTART's day, used when the rule names no day.
func icsMonthDays(r *icsRule, y int, m time.Month, startDay int) []int {
	dim := icsDaysIn(y, m)
	var fromMD, fromBD []int
	for _, md := range r.byMonthDay {
		d := md
		if d < 0 {
			d = dim + md + 1
		}
		if d >= 1 && d <= dim {
			fromMD = append(fromMD, d)
		}
	}
	for _, bd := range r.byDay {
		if bd.n != 0 {
			if d, ok := icsNthWeekday(y, m, bd.n, bd.wd); ok {
				fromBD = append(fromBD, d)
			}
			continue
		}
		first := time.Date(y, m, 1, 12, 0, 0, 0, time.UTC).Weekday()
		for d := 1 + (int(bd.wd)-int(first)+7)%7; d <= dim; d += 7 {
			fromBD = append(fromBD, d)
		}
	}
	var days []int
	switch {
	case len(r.byMonthDay) > 0 && len(r.byDay) > 0:
		in := map[int]bool{}
		for _, d := range fromBD {
			in[d] = true
		}
		for _, d := range fromMD {
			if in[d] {
				days = append(days, d)
			}
		}
	case len(r.byMonthDay) > 0:
		days = fromMD
	case len(r.byDay) > 0:
		days = fromBD
	default:
		if startDay <= dim {
			days = []int{startDay}
		}
	}
	sort.Ints(days)
	// Unique: "1MO" and "MO" can name the same day.
	out := days[:0]
	for i, d := range days {
		if i == 0 || d != days[i-1] {
			out = append(out, d)
		}
	}
	return out
}

// icsDate is one candidate day.
type icsDate struct {
	y int
	m time.Month
	d int
}

// icsSetPos keeps the BYSETPOS picks of one period's sorted candidates.
func icsSetPos(r *icsRule, c []icsDate) []icsDate {
	if len(r.bySetPos) == 0 {
		return c
	}
	var out []icsDate
	for _, p := range r.bySetPos {
		i := p - 1
		if p < 0 {
			i = len(c) + p
		}
		if i >= 0 && i < len(c) {
			out = append(out, c[i])
		}
	}
	sort.Slice(out, func(a, b int) bool {
		return icsDayNumber(out[a].y, out[a].m, out[a].d) < icsDayNumber(out[b].y, out[b].m, out[b].d)
	})
	return out
}

// occurrences lists the starts of a repeating event that may touch
// [from, to): every instance from DTSTART on (DTSTART itself is always the
// first, as RFC 5545 says), stopping at COUNT, UNTIL, to, or the limits. An
// event that ends before from is not returned, but still counts for COUNT.
func (c *icsCalendar) occurrences(ev *icsEvent, from, to time.Time, max int) []time.Time {
	start := ev.start
	r, bad := c.icsParseRule(ev.rrule)
	if bad != "" {
		icsLogOnce("rrule:"+bad, "repeat rule part %s is not supported; showing the first date only", bad)
		return []time.Time{start.t}
	}
	length := ev.dur
	if start.allDay {
		length = time.Duration(ev.days) * 24 * time.Hour
	}
	// Instances that end before from are skipped without being built, but
	// with COUNT they must still be counted, so a counted walk starts at
	// DTSTART; icsMaxPeriods bounds it.
	earliest := from.Add(-length - 25*time.Hour)
	out := []time.Time{start.t}
	count := 1
	at := func(d icsDate) time.Time {
		if start.allDay {
			return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.Local)
		}
		return start.zone.at(d.y, d.m, d.d, start.h, start.mi, start.s)
	}
	// A floating UNTIL beside a zoned DTSTART (against the RFC, but common)
	// is a wall-clock reading in DTSTART's zone, not the device's.
	if r.hasUntil && !r.untilTV.allDay && r.untilTV.zone == icsZoneLocal && !start.allDay && start.zone != nil {
		u := r.untilTV
		r.until = start.zone.at(u.y, u.mo, u.d, u.h, u.mi, u.s)
	}
	base := time.Date(start.y, start.mo, start.d, 12, 0, 0, 0, time.UTC)
	// The week of DTSTART, from its WKST day.
	weekStart := base.AddDate(0, 0, -((int(base.Weekday()) - int(r.wkst) + 7) % 7))
	startDay := icsDayNumber(start.y, start.mo, start.d)
	// Without COUNT nothing before the window matters, so the walk may begin
	// a couple of periods before it rather than at an old DTSTART.
	p0 := 0
	if r.count == 0 && earliest.After(start.t) {
		ey, em, ed := earliest.Date()
		switch r.freq {
		case "DAILY":
			p0 = (icsDayNumber(ey, em, ed) - startDay) / r.interval
		case "WEEKLY":
			p0 = (icsDayNumber(ey, em, ed) - startDay) / (7 * r.interval)
		case "MONTHLY":
			p0 = ((ey-start.y)*12 + int(em) - int(start.mo)) / r.interval
		case "YEARLY":
			p0 = (ey - start.y) / r.interval
		}
		// max is the instance limit here, not the builtin.
		if p0 -= 2; p0 < 0 {
			p0 = 0
		}
	}
	// A period that begins two days after the window's end cannot reach back
	// into it: the walk stops there even when a rule never matches.
	ty, tm, td := to.Date()
	lastDay := icsDayNumber(ty, tm, td) + 2
	for p := p0; p < p0+icsMaxPeriods; p++ {
		var cands []icsDate
		var periodStart time.Time
		switch r.freq {
		case "DAILY":
			periodStart = base.AddDate(0, 0, p*r.interval)
		case "WEEKLY":
			periodStart = weekStart.AddDate(0, 0, p*r.interval*7)
		case "MONTHLY":
			periodStart = time.Date(start.y, start.mo+time.Month(p*r.interval), 1, 12, 0, 0, 0, time.UTC)
		case "YEARLY":
			periodStart = time.Date(start.y+p*r.interval, time.January, 1, 12, 0, 0, 0, time.UTC)
		}
		if icsDayNumber(periodStart.Date()) > lastDay {
			return out
		}
		switch r.freq {
		case "DAILY":
			d := periodStart
			ok := icsHasMonth(&r, d.Month())
			if ok && len(r.byMonthDay) > 0 {
				ok = false
				dim := icsDaysIn(d.Year(), d.Month())
				for _, md := range r.byMonthDay {
					if md == d.Day() || (md < 0 && dim+md+1 == d.Day()) {
						ok = true
					}
				}
			}
			if ok && len(r.byDay) > 0 {
				ok = false
				for _, bd := range r.byDay {
					if bd.wd == d.Weekday() {
						ok = true
					}
				}
			}
			if ok {
				cands = append(cands, icsDate{d.Year(), d.Month(), d.Day()})
			}
		case "WEEKLY":
			ws := periodStart
			wds := []time.Weekday{base.Weekday()}
			if len(r.byDay) > 0 {
				wds = wds[:0]
				for _, bd := range r.byDay {
					wds = append(wds, bd.wd)
				}
			}
			for _, wd := range wds {
				d := ws.AddDate(0, 0, (int(wd)-int(r.wkst)+7)%7)
				if icsHasMonth(&r, d.Month()) {
					cands = append(cands, icsDate{d.Year(), d.Month(), d.Day()})
				}
			}
			sort.Slice(cands, func(a, b int) bool {
				return icsDayNumber(cands[a].y, cands[a].m, cands[a].d) < icsDayNumber(cands[b].y, cands[b].m, cands[b].d)
			})
		case "MONTHLY":
			first := periodStart
			if icsHasMonth(&r, first.Month()) {
				for _, d := range icsMonthDays(&r, first.Year(), first.Month(), start.d) {
					cands = append(cands, icsDate{first.Year(), first.Month(), d})
				}
			}
			cands = icsSetPos(&r, cands)
		case "YEARLY":
			y := start.y + p*r.interval
			months := []int{int(start.mo)}
			if len(r.byMonth) > 0 {
				months = append([]int(nil), r.byMonth...)
				sort.Ints(months)
			}
			for _, m := range months {
				for _, d := range icsMonthDays(&r, y, time.Month(m), start.d) {
					cands = append(cands, icsDate{y, time.Month(m), d})
				}
			}
			cands = icsSetPos(&r, cands)
		}
		for _, d := range cands {
			t := at(d)
			if !t.After(start.t) {
				continue // DTSTART is already the first instance
			}
			if r.hasUntil && t.After(r.until) {
				return out
			}
			count++
			if r.count > 0 && count > r.count {
				return out
			}
			if !t.Before(to) {
				return out
			}
			if t.After(earliest) {
				out = append(out, t)
				if len(out) >= max {
					return out
				}
			}
		}
	}
	return out
}
