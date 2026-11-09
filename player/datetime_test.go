package main

// Date & time: the zone table, the picker's search and layout, the date
// formats, the SNTP arithmetic, the "should set" rule and the re-anchoring
// after a clock jump. All of it is pure: no SDL, no network, no clock set.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	// The test binary loads every zone of the table on any OS, including a
	// Windows machine without a zoneinfo directory.
	_ "time/tzdata"
)

func TestTZZonesSortedUniqueAndLoadable(t *testing.T) {
	if len(tzZones) < 300 {
		t.Fatalf("only %d zones in the table", len(tzZones))
	}
	names := make([]string, len(tzZones))
	for i, z := range tzZones {
		names[i] = z.Name
		if !tzValidName(z.Name) {
			t.Errorf("%q is not a valid zone name", z.Name)
		}
		if strings.TrimSpace(z.Countries) == "" {
			t.Errorf("%s has no countries", z.Name)
		}
		if _, err := time.LoadLocation(z.Name); err != nil {
			t.Errorf("%s does not load: %v", z.Name, err)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("tzZones is not sorted by name (tzZoneIndex needs it)")
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Errorf("%s is listed twice", names[i])
		}
	}
	for _, want := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Tokyo", "Australia/Sydney"} {
		if tzZoneIndex(want) < 0 {
			t.Errorf("%s is missing", want)
		}
	}
	if tzZoneIndex("Nowhere/Land") != -1 {
		t.Error("an unknown zone was found")
	}
	// The cities zone1970.tab folds into a neighbour are rows of their own.
	for _, want := range []string{"Europe/Amsterdam", "Europe/Oslo", "Europe/Stockholm", "Europe/Copenhagen", "Asia/Kuala_Lumpur"} {
		if tzZoneIndex(want) < 0 {
			t.Errorf("%s is missing", want)
		}
	}
}

func TestTZAliases(t *testing.T) {
	if len(tzAliases) < 50 {
		t.Fatalf("only %d aliases", len(tzAliases))
	}
	for i, a := range tzAliases {
		if i > 0 && tzAliases[i-1].Alias >= a.Alias {
			t.Fatalf("tzAliases not sorted and unique at %s (tzZoneLookup needs it)", a.Alias)
		}
		if tzZoneIndex(a.Alias) >= 0 {
			t.Errorf("%s is both a row and an alias", a.Alias)
		}
		if tzZoneIndex(a.Zone) < 0 {
			t.Errorf("%s stands for %s, which is not listed", a.Alias, a.Zone)
		}
	}
	// The zone in force is marked even when it goes by an old name.
	cases := map[string]string{
		"Asia/Calcutta": "Asia/Kolkata", "Europe/Kiev": "Europe/Kyiv", "US/Eastern": "America/New_York",
		"Etc/UTC": "UTC", "GMT": "UTC", "Europe/Berlin": "Europe/Berlin",
	}
	for name, want := range cases {
		if got := tzZoneLookup(name); got < 0 || tzZones[got].Name != want {
			t.Errorf("tzZoneLookup(%q) = %d, want %s", name, got, want)
		}
	}
	if tzZoneLookup("Etc/GMT+5") != -1 || tzZoneLookup("Nowhere/Land") != -1 {
		t.Error("an unlisted zone was found")
	}
}

func TestTZValidName(t *testing.T) {
	for _, ok := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+5", "America/Port-au-Prince"} {
		if !tzValidName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "/etc/localtime", "../../etc/passwd", "Europe/../Berlin", "Europe/Berlin;reboot", "a b", "Europe/", "$(id)", strings.Repeat("A", 65)} {
		if tzValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TZ spellings that name the same zone compare equal with the picker's name
// (the Time zone row's "games get it after JukaHub restarts" note).
func TestDateTimeTZName(t *testing.T) {
	cases := [][2]string{
		{"Europe/Berlin", "Europe/Berlin"},
		{":Europe/Berlin", "Europe/Berlin"},
		{" /usr/share/zoneinfo/Europe/Berlin ", "Europe/Berlin"},
		{":/usr/share/zoneinfo/America/Argentina/Buenos_Aires", "America/Argentina/Buenos_Aires"},
		{"/mnt/SDCARD/System/zoneinfo/Asia/Tokyo", "Asia/Tokyo"},
		{":/etc/localtime", "Local"},
		{"/etc/localtime", "Local"},
		{"Local", "Local"},
		{"UTC", "UTC"},
		{"", ""},
	}
	for _, c := range cases {
		if got := dateTimeTZName(c[0]); got != c[1] {
			t.Errorf("dateTimeTZName(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestTZCity(t *testing.T) {
	cases := [][2]string{
		{"Europe/Berlin", "Berlin"},
		{"America/Argentina/Buenos_Aires", "Buenos Aires · Argentina"},
		{"America/North_Dakota/New_Salem", "New Salem · North Dakota"},
		{"UTC", "UTC"},
	}
	for _, c := range cases {
		if got := tzCity(c[0]); got != c[1] {
			t.Errorf("tzCity(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	if tzRegion("America/Argentina/Salta") != "America" || tzRegion("UTC") != "UTC" {
		t.Error("tzRegion")
	}
}

func tzMatchNames(query string) []string {
	var out []string
	for _, i := range tzMatches(query) {
		out = append(out, tzZones[i].Name)
	}
	return out
}

func tzContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestTZMatches(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"berlin", "Europe/Berlin"},
		{"Germany", "Europe/Berlin"},
		{"new york", "America/New_York"},
		{"new_york", "America/New_York"},
		{"buenos aires", "America/Argentina/Buenos_Aires"},
		{"america argentina", "America/Argentina/Buenos_Aires"},
		{"tokyo", "Asia/Tokyo"},
		{"japan", "Asia/Tokyo"},
		{"utc", "UTC"},
		// zone.tab's own rows and notes.
		{"amsterdam", "Europe/Amsterdam"},
		{"netherlands", "Europe/Amsterdam"},
		{"kuala lumpur", "Asia/Kuala_Lumpur"},
		{"reykjavik", "Atlantic/Reykjavik"},
		{"iceland", "Atlantic/Reykjavik"},
		{"eastern", "America/New_York"},
		// Old names, through the alias table.
		{"calcutta", "Asia/Kolkata"},
		{"kiev", "Europe/Kyiv"},
		{"saigon", "Asia/Ho_Chi_Minh"},
		// Accents folded away.
		{"cote", "Africa/Abidjan"},
		{"curacao", "America/Curacao"},
	}
	for _, c := range cases {
		if got := tzMatchNames(c.query); !tzContains(got, c.want) {
			t.Errorf("%q does not find %s (got %d zones)", c.query, c.want, len(got))
		}
	}
	if n := len(tzMatches("")); n != len(tzZones) {
		t.Errorf("empty query matched %d of %d zones", n, len(tzZones))
	}
	if got := tzMatchNames("zzzzqq"); len(got) != 0 {
		t.Errorf("nonsense matched %v", got)
	}
	// Every word must match: "berlin tokyo" is nothing.
	if got := tzMatchNames("berlin tokyo"); len(got) != 0 {
		t.Errorf("two places matched %v", got)
	}
}

func TestTZRowsAndNavigation(t *testing.T) {
	rows := tzBuildRows("", true)
	if len(rows) < len(tzZones)+5 || rows[0].kind != tzRowAuto || rows[1].kind != tzRowHeader {
		t.Fatalf("rows start %v %v (%d rows)", rows[0].kind, rows[1].kind, len(rows))
	}
	// Every zone sits under its own region's header.
	region := ""
	for _, r := range rows {
		switch r.kind {
		case tzRowHeader:
			region = r.text
		case tzRowZone:
			if got := strings.ToUpper(tzRegion(tzZones[r.zone].Name)); got != region {
				t.Fatalf("%s under %s", tzZones[r.zone].Name, region)
			}
		}
	}
	// A search hides the automatic row.
	if got := tzBuildRows("berlin", true); len(got) == 0 || got[0].kind != tzRowHeader {
		t.Fatalf("search rows start with %v", got)
	}
	// Steps skip headers and stop at the ends.
	if tzStep(rows, 0, 1) != 2 {
		t.Errorf("down from the auto row lands on %d, want 2 (past the header)", tzStep(rows, 0, 1))
	}
	if tzStep(rows, 0, -1) != 0 {
		t.Error("up from the first row moved")
	}
	// Region jumps: forward to the next region's first zone, back to this
	// region's first zone, then to the previous region's.
	small := []tzPickerRow{
		{kind: tzRowAuto},
		{kind: tzRowHeader, text: "AFRICA"}, {kind: tzRowZone, zone: 0}, {kind: tzRowZone, zone: 1},
		{kind: tzRowHeader, text: "AMERICA"}, {kind: tzRowZone, zone: 2}, {kind: tzRowZone, zone: 3},
	}
	jumps := []struct{ from, dir, want int }{
		{0, 1, 2}, {2, 1, 5}, {5, 1, 5}, {6, -1, 5}, {5, -1, 2}, {2, -1, 0}, {3, -1, 2},
	}
	for _, j := range jumps {
		if got := tzRegionJump(small, j.from, j.dir); got != j.want {
			t.Errorf("jump from %d dir %d = %d, want %d", j.from, j.dir, got, j.want)
		}
	}
}

func TestUTCOffsetLabel(t *testing.T) {
	at := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	cases := map[int]string{0: "UTC", 3600: "UTC+1", -3 * 3600: "UTC-3", 5*3600 + 1800: "UTC+5:30", -(9*3600 + 1800): "UTC-9:30", 5*3600 + 2700: "UTC+5:45"}
	for off, want := range cases {
		if got := utcOffsetLabel(at.In(time.FixedZone("", off))); got != want {
			t.Errorf("offset %d = %q, want %q", off, got, want)
		}
	}
}

func dtTestConfig(order string, twelve bool) *Config {
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{dateFormatKey: order}
	c.Variables.Clock12 = twelve
	return c
}

func TestDateFormats(t *testing.T) {
	at := time.Date(2026, time.November, 9, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		order, medium, long, day, short string
	}{
		{dateOrderDMY, "9 Nov 2026", "Monday, 9 November 2026", "Monday, 9 November", "9 Nov"},
		{dateOrderMDY, "Nov 9, 2026", "Monday, November 9, 2026", "Monday, November 9", "Nov 9"},
		{dateOrderYMD, "2026-11-09", "Monday, 2026-11-09", "Monday, 11-09", "11-09"},
		{dateOrderDots, "09.11.2026", "Monday, 09.11.2026", "Monday, 09.11.", "09.11."},
	}
	for _, c := range cases {
		cfg := dtTestConfig(c.order, false)
		if got := formatDate(cfg, at); got != c.medium {
			t.Errorf("%s formatDate = %q, want %q", c.order, got, c.medium)
		}
		if got := formatDateLong(cfg, at); got != c.long {
			t.Errorf("%s formatDateLong = %q, want %q", c.order, got, c.long)
		}
		if got := formatDayLong(cfg, at); got != c.day {
			t.Errorf("%s formatDayLong = %q, want %q", c.order, got, c.day)
		}
		if got := formatDateShort(cfg, at); got != c.short {
			t.Errorf("%s formatDateShort = %q, want %q", c.order, got, c.short)
		}
	}
	if got := formatDateTime(dtTestConfig(dateOrderMDY, true), at); got != "Nov 9, 2026, 3:04 PM" {
		t.Errorf("formatDateTime 12 h = %q", got)
	}
	if got := formatDateTime(dtTestConfig(dateOrderDMY, false), at); got != "9 Nov 2026, 15:04" {
		t.Errorf("formatDateTime 24 h = %q", got)
	}
	// An unknown stored value is Auto, which is day first while the country
	// is unknown (no weather lookup has run in the test binary).
	if weatherCountryCode() == "" {
		if got := formatDate(dtTestConfig("bogus", false), at); got != "9 Nov 2026" {
			t.Errorf("auto formatDate = %q", got)
		}
	}
}

func TestFormatDateTimeNear(t *testing.T) {
	cfg := dtTestConfig(dateOrderDMY, false)
	now := time.Date(2026, time.November, 9, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, time.November, 9, 15, 4, 0, 0, time.UTC), "Today, 15:04"},
		{time.Date(2026, time.November, 8, 23, 59, 0, 0, time.UTC), "Yesterday, 23:59"},
		{time.Date(2026, time.February, 1, 9, 30, 0, 0, time.UTC), "1 Feb, 09:30"},
		{time.Date(2025, time.November, 9, 15, 4, 0, 0, time.UTC), "9 Nov 2025, 15:04"},
	}
	for _, c := range cases {
		if got := formatDateTimeNear(cfg, c.at, now); got != c.want {
			t.Errorf("formatDateTimeNear(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestDateOrderForCountry(t *testing.T) {
	cases := map[string]string{"us": dateOrderMDY, "PH": dateOrderMDY, "JP": dateOrderYMD, "hu": dateOrderYMD, "DE": dateOrderDMY, "GB": dateOrderDMY, "": dateOrderDMY}
	for code, want := range cases {
		if got := dateOrderForCountry(code); got != want {
			t.Errorf("%q = %q, want %q", code, got, want)
		}
	}
}

// sntpTestReply builds a server reply to req with receive time t2 and
// transmit time t3.
func sntpTestReply(req []byte, t2, t3 time.Time) []byte {
	reply := make([]byte, 48)
	reply[0] = 0x24 // leap 0, version 4, mode 4 (server)
	reply[1] = 2    // stratum
	copy(reply[24:32], req[40:48])
	ntpPutTime(reply[32:40], t2)
	ntpPutTime(reply[40:48], t3)
	return reply
}

func sntpNear(a, b time.Duration) bool {
	d := a - b
	return d < time.Microsecond && d > -time.Microsecond
}

func TestSNTPOffsetAndDelay(t *testing.T) {
	// The device is 10 s behind; 50 ms each way; the server takes 1 ms.
	t1 := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	req := sntpRequest(t1)
	if req[0] != 0x23 || len(req) != 48 {
		t.Fatalf("request header %#x len %d", req[0], len(req))
	}
	t2 := t1.Add(10*time.Second + 50*time.Millisecond)
	t3 := t2.Add(time.Millisecond)
	t4 := t1.Add(101 * time.Millisecond)
	r, err := sntpParse(sntpTestReply(req, t2, t3), req, t1, t4)
	if err != nil {
		t.Fatal(err)
	}
	if !sntpNear(r.Offset, 10*time.Second) {
		t.Errorf("offset %v, want 10s", r.Offset)
	}
	if !sntpNear(r.Delay, 100*time.Millisecond) {
		t.Errorf("delay %v, want 100ms", r.Delay)
	}
	if r.Stratum != 2 {
		t.Errorf("stratum %d", r.Stratum)
	}

	// A clock far in the past (a handheld that booted at 1970) still yields
	// the full offset.
	old := time.Date(1970, time.January, 1, 0, 0, 10, 0, time.UTC)
	oreq := sntpRequest(old)
	real := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	r, err = sntpParse(sntpTestReply(oreq, real, real), oreq, old, old)
	if err != nil {
		t.Fatal(err)
	}
	if !sntpNear(r.Offset, real.Sub(old)) {
		t.Errorf("offset from 1970 = %v, want %v", r.Offset, real.Sub(old))
	}
}

func TestSNTPRejects(t *testing.T) {
	t1 := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	req := sntpRequest(t1)
	good := func() []byte { return sntpTestReply(req, t1, t1) }
	cases := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"short", func(b []byte) []byte { return b[:40] }},
		{"client mode", func(b []byte) []byte { b[0] = 0x23; return b }},
		{"unsynced", func(b []byte) []byte { b[0] = 0xE4; return b }},
		{"kiss of death", func(b []byte) []byte { b[1] = 0; return b }},
		{"stratum 16", func(b []byte) []byte { b[1] = 16; return b }},
		{"not ours", func(b []byte) []byte { b[24]++; return b }},
		{"no time", func(b []byte) []byte { copy(b[40:48], make([]byte, 8)); return b }},
		// Sent before it was received.
		{"out of order", func(b []byte) []byte { ntpPutTime(b[40:48], t1.Add(-time.Second)); return b }},
	}
	for _, c := range cases {
		if _, err := sntpParse(c.mutate(good()), req, t1, t1); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// The server claims it spent longer on the request than the round trip.
	slow := sntpTestReply(req, t1, t1.Add(500*time.Millisecond))
	if _, err := sntpParse(slow, req, t1, t1.Add(100*time.Millisecond)); err == nil {
		t.Error("a server time longer than the round trip was accepted")
	}
	// A round trip too long to set a clock by.
	if _, err := sntpParse(good(), req, t1, t1.Add(sntpMaxDelay+time.Second)); err == nil {
		t.Error("a 3 s round trip was accepted")
	}
	// The device time is the middle of the exchange.
	r, err := sntpParse(good(), req, t1, t1.Add(200*time.Millisecond))
	if err != nil || !r.Local.Equal(t1.Add(100*time.Millisecond)) {
		t.Errorf("Local = %v (%v), want t1+100ms", r.Local, err)
	}
}

func TestClockNeedsConfirm(t *testing.T) {
	cases := []struct {
		offset, unconfirmed time.Duration
		want                bool
	}{
		{3 * time.Second, 0, false},
		{-9 * time.Minute, 0, false},
		{11 * time.Minute, 0, true},
		{-3 * time.Hour, 0, true},
		{56 * 365 * 24 * time.Hour, 0, true}, // a cold boot at 1970
		// Steps just under the limit add up.
		{6 * time.Minute, 5 * time.Minute, true},
		{-6 * time.Minute, 3 * time.Minute, false},
	}
	for _, c := range cases {
		if got := clockNeedsConfirm(c.offset, c.unconfirmed); got != c.want {
			t.Errorf("clockNeedsConfirm(%v, %v) = %v, want %v", c.offset, c.unconfirmed, got, c.want)
		}
	}
}

// The SNTP-only sum gives back ten minutes per day of uptime, never more than
// it holds, and never grows by itself.
func TestClockUnconfirmedAfter(t *testing.T) {
	cases := []struct {
		sum, elapsed, want time.Duration
	}{
		{0, time.Hour, 0},
		{9 * time.Minute, 0, 9 * time.Minute},
		{9 * time.Minute, -time.Hour, 9 * time.Minute},
		{9 * time.Minute, time.Hour, 9*time.Minute - 25*time.Second},
		{10 * time.Minute, 12 * time.Hour, 5 * time.Minute},
		{10 * time.Minute, 24 * time.Hour, 0},
		{3 * time.Minute, 30 * 24 * time.Hour, 0},
	}
	for _, c := range cases {
		if got := clockUnconfirmedAfter(c.sum, c.elapsed); got != c.want {
			t.Errorf("clockUnconfirmedAfter(%v, %v) = %v, want %v", c.sum, c.elapsed, got, c.want)
		}
	}
	// A burst still adds up: three 4-minute steps an hour apart need HTTPS
	// by the third.
	sum := time.Duration(0)
	for step := 1; step <= 3; step++ {
		sum = clockUnconfirmedAfter(sum, time.Hour)
		if need := clockNeedsConfirm(4*time.Minute, sum); need != (step == 3) {
			t.Errorf("step %d (sum %v): needs confirmation = %v", step, sum, need)
		}
		sum += 4 * time.Minute
	}
}

// netTimeNoteSet keeps the sum in [0, clockConfirmAbove]: a confirmed set
// clears it, and a cold boot's unconfirmed jump of decades fills it without
// outlasting a day's decay.
func TestNetTimeNoteSet(t *testing.T) {
	prevSum, prevAt := netTime.unconfirmed, netTime.unconfirmedAt
	t.Cleanup(func() { netTime.unconfirmed, netTime.unconfirmedAt = prevSum, prevAt })
	netTime.unconfirmed, netTime.unconfirmedAt = 0, time.Time{}

	netTimeNoteSet(-3*time.Minute, false)
	netTimeNoteSet(4*time.Minute, false)
	if got := netTime.unconfirmed; got > 7*time.Minute || got < 7*time.Minute-time.Second {
		t.Fatalf("after -3 and +4 minutes: %v, want 7 minutes", got)
	}
	netTimeNoteSet(56*365*24*time.Hour, false)
	if got := netTime.unconfirmed; got != clockConfirmAbove {
		t.Fatalf("after a cold boot's jump: %v, want %v", got, clockConfirmAbove)
	}
	// A day of uptime later (the monotonic anchor moved back a day).
	netTime.unconfirmedAt = netTime.unconfirmedAt.Add(-clockConfirmDecay)
	if got := netTimeUnconfirmed(); got != 0 {
		t.Fatalf("a day later: %v, want 0", got)
	}
	netTimeNoteSet(8*time.Minute, false)
	netTimeNoteSet(time.Hour, true)
	if got := netTime.unconfirmed; got != 0 {
		t.Fatalf("after a confirmed set: %v, want 0", got)
	}
}

// An SNTP claim that HTTPS could not confirm sets the clock only on a device
// with no CA certificate at all, only when the check failed on exactly that,
// and only while the clock is before this build.
func TestClockUnconfirmedSetOK(t *testing.T) {
	floor := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	coldBoot := time.Date(1970, time.January, 1, 0, 0, 42, 0, time.UTC)
	plausible := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	// As http.Client.Do returns them: *url.Error around the TLS error.
	wrap := func(err error) error {
		return &url.Error{Op: "Head", URL: clockHTTPSURL, Err: &tls.CertificateVerificationError{Err: err}}
	}
	unknown := wrap(x509.UnknownAuthorityError{})
	noSystem := wrap(x509.SystemRootsError{})
	expired := wrap(x509.CertificateInvalidError{Reason: x509.Expired})
	unreachable := &url.Error{Op: "Head", URL: clockHTTPSURL, Err: errors.New("dial tcp: i/o timeout")}
	disagree := errors.New("the HTTPS date puts the clock 3 days behind, not 2 h behind")
	cases := []struct {
		name    string
		err     error
		noRoots bool
		now     time.Time
		want    bool
	}{
		{"no CA certificate, cold boot", unknown, true, coldBoot, true},
		{"no system roots, cold boot", noSystem, true, coldBoot, true},
		{"no CA certificate, plausible clock", unknown, true, plausible, false},
		{"no CA certificate, clock at the floor", unknown, true, floor, false},
		{"CA certificates present: a forged certificate", unknown, false, coldBoot, false},
		{"no CA certificate, certificate expired at the claim", expired, true, coldBoot, false},
		{"no CA certificate, HTTPS unreachable", unreachable, true, coldBoot, false},
		{"CA certificates present, dates disagree", disagree, false, coldBoot, false},
		{"confirmed", nil, true, coldBoot, false},
	}
	for _, c := range cases {
		if got := clockUnconfirmedSetOK(c.err, c.noRoots, c.now, floor); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNetTimeRetryAfter(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		if got := netTimeRetryAfter(i+1, false); got != w {
			t.Errorf("after %d failures: %v, want %v", i+1, got, w)
		}
	}
	if got := netTimeRetryAfter(0, true); got != netTimeInterval {
		t.Errorf("after a success: %v", got)
	}
	// Once a sync has found the time this run, a failure waits the hour.
	if got := netTimeRetryAfter(1, true); got != netTimeInterval {
		t.Errorf("a failure after a success: %v", got)
	}
}

func TestNTPTimeRoundTripAndEra(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, time.November, 9, 12, 0, 0, 500000000, time.UTC),
		time.Date(1999, time.December, 31, 23, 59, 59, 0, time.UTC),
		// After the 32-bit seconds field wraps (2036-02-07 06:28:16 UTC).
		time.Date(2036, time.February, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2050, time.June, 1, 0, 0, 0, 0, time.UTC),
	} {
		b := make([]byte, 8)
		ntpPutTime(b, at)
		got, ok := ntpReadTime(b)
		if !ok {
			t.Fatalf("%v: unreadable", at)
		}
		if d := got.Sub(at); d > time.Microsecond || d < -time.Microsecond {
			t.Errorf("%v read back as %v", at, got.UTC())
		}
	}
	if _, ok := ntpReadTime(make([]byte, 8)); ok {
		t.Error("an all-zero timestamp read as a time")
	}
}

func TestClockDecide(t *testing.T) {
	floor := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		offset  time.Duration
		network time.Time
		maySet  bool
		want    clockAction
	}{
		{"in sync", time.Second, now, true, clockKeep},
		{"in sync behind", -1900 * time.Millisecond, now, true, clockKeep},
		{"two seconds", 2 * time.Second, now, true, clockSet},
		{"far behind", 3 * time.Hour, now, true, clockSet},
		{"ahead", -40 * time.Second, now, true, clockSet},
		{"system owns it", 5 * time.Second, now, false, clockReport},
		{"before the build", 5 * time.Second, time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC), true, clockReject},
		{"next century", 5 * time.Second, time.Date(2101, time.January, 1, 0, 0, 0, 0, time.UTC), true, clockReject},
	}
	for _, c := range cases {
		if got := clockDecide(c.offset, c.network, floor, c.maySet); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClockJumpSeen(t *testing.T) {
	var view clockJumpView
	clockJumpSeen(&view) // catch up with whatever ran before
	if _, ok := clockJumpSeen(&view); ok {
		t.Fatal("a jump was seen before any happened")
	}
	// Network time moves the clock 5 s; then a new zone moves the wall clock
	// an hour on. Both arrive at once, summed.
	clockJumpNotify(5*time.Second, 5*time.Second)
	clockJumpNotify(0, time.Hour)
	shift, ok := clockJumpSeen(&view)
	if !ok {
		t.Fatal("the jumps were missed")
	}
	if shift.clock != 5*time.Second || shift.wall != time.Hour+5*time.Second {
		t.Errorf("shift = %+v, want clock 5s, wall 1h0m5s", shift)
	}
	if _, ok := clockJumpSeen(&view); ok {
		t.Fatal("one jump was seen twice")
	}
	clockJumpNotify(-2*time.Second, -2*time.Second)
	if shift, ok := clockJumpSeen(&view); !ok || shift.clock != -2*time.Second || shift.wall != -2*time.Second {
		t.Errorf("second jump = %+v %v, want -2s", shift, ok)
	}
}

func TestAlarmReanchor(t *testing.T) {
	now := time.Date(2026, time.November, 9, 10, 0, 0, 0, time.UTC)
	// 1 and 2 were marked done for a day still to come (the clock ran
	// ahead), 3 is fine as it is, 4 has a real snooze, 5 an impossible one.
	alarms := []alarmEntry{
		{ID: 1, Hour: 7, Enabled: true, FiredOn: "2026-11-10"},
		{ID: 2, Hour: 12, Enabled: true, FiredOn: "2026-11-10", RangOn: "2026-11-12"},
		{ID: 3, Hour: 7, Enabled: true, FiredOn: "2026-11-08", RangOn: "2026-11-08"},
		{ID: 4, Hour: 9, Enabled: true, SnoozeUntil: now.Add(5 * time.Minute).Format(time.RFC3339)},
		{ID: 5, Hour: 9, Enabled: true, FiredOn: "2026-11-09", SnoozeUntil: now.Add(3 * time.Hour).Format(time.RFC3339)},
	}
	realSnooze := alarms[3].SnoozeUntil
	// A zone change: the instants did not move.
	if !alarmReanchor(alarms, now, 0) {
		t.Fatal("nothing re-anchored")
	}
	if alarms[0].FiredOn != "2026-11-09" {
		t.Errorf("07:00 alarm FiredOn %q, want today (its time has passed)", alarms[0].FiredOn)
	}
	if alarms[1].FiredOn != "" || alarms[1].RangOn != "" {
		t.Errorf("12:00 alarm FiredOn %q RangOn %q, want both empty", alarms[1].FiredOn, alarms[1].RangOn)
	}
	if alarms[2].FiredOn != "2026-11-08" || alarms[2].RangOn != "2026-11-08" {
		t.Error("a past alarm was touched")
	}
	if alarms[3].SnoozeUntil != realSnooze {
		t.Errorf("a real snooze moved: %q, want %q", alarms[3].SnoozeUntil, realSnooze)
	}
	// Never dropped: brought back to a snooze's length.
	if want := now.Add(alarmSnoozeMinutes * time.Minute).Format(time.RFC3339); alarms[4].SnoozeUntil != want {
		t.Errorf("a snooze hours ahead = %q, want %q", alarms[4].SnoozeUntil, want)
	}
	if alarmReanchor(alarms, now, 0) {
		t.Error("a second pass changed something")
	}
	// The 12:00 alarm rings at noon today again.
	if !alarmDue(alarms[1], time.Date(2026, time.November, 9, 12, 1, 0, 0, time.UTC)) {
		t.Error("the re-armed alarm does not ring today")
	}
}

func TestAlarmReanchorSnoozeAcrossJumps(t *testing.T) {
	now := time.Date(2026, time.November, 9, 10, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) string { return at.Format(time.RFC3339) }

	// The clock was two hours behind. Snoozed at what it called 07:56 (09:56
	// really): due at its 08:05, which is 10:05 now. Without the shift the
	// snooze would sit two hours in the past, out of the catch-up window.
	forward := 2 * time.Hour
	alarms := []alarmEntry{
		{ID: 1, Hour: 7, Minute: 50, Enabled: true, FiredOn: "2026-11-09", SnoozeUntil: stamp(now.Add(-forward + 5*time.Minute))},
		// Lapsed before the jump (the device slept through it): stays lapsed.
		{ID: 2, Hour: 6, Enabled: true, FiredOn: "2026-11-09", SnoozeUntil: stamp(now.Add(-forward - time.Hour))},
	}
	alarmReanchor(alarms, now, forward)
	if want := stamp(now.Add(5 * time.Minute)); alarms[0].SnoozeUntil != want {
		t.Errorf("forward jump: snooze %q, want %q", alarms[0].SnoozeUntil, want)
	}
	if alarmDue(alarms[0], now) || !alarmDue(alarms[0], now.Add(6*time.Minute)) {
		t.Error("forward jump: the snooze does not ring five minutes on")
	}
	if want := stamp(now.Add(-forward - time.Hour)); alarms[1].SnoozeUntil != want {
		t.Errorf("a lapsed snooze moved: %q", alarms[1].SnoozeUntil)
	}
	if alarmDue(alarms[1], now) {
		t.Error("a lapsed snooze rings")
	}

	// The clock was a day ahead: the 09:55 alarm rang on its "tomorrow" and
	// was snoozed. The day is re-armed and the snooze moves back with the
	// clock.
	back := -24 * time.Hour
	alarms = []alarmEntry{
		{ID: 3, Hour: 9, Minute: 55, Enabled: true, FiredOn: "2026-11-10", RangOn: "2026-11-10", SnoozeUntil: stamp(now.Add(-back + 4*time.Minute))},
	}
	if !alarmReanchor(alarms, now, back) {
		t.Fatal("backward jump: nothing re-anchored")
	}
	if want := stamp(now.Add(4 * time.Minute)); alarms[0].SnoozeUntil != want {
		t.Errorf("backward jump: snooze %q, want %q", alarms[0].SnoozeUntil, want)
	}
	if alarms[0].FiredOn != "2026-11-09" || alarms[0].RangOn != "" {
		t.Errorf("backward jump: FiredOn %q RangOn %q", alarms[0].FiredOn, alarms[0].RangOn)
	}
	if !alarmDue(alarms[0], now.Add(5*time.Minute)) {
		t.Error("backward jump: the snooze does not ring")
	}
}

func TestPlaytimeReanchorDays(t *testing.T) {
	// Booted with a stale clock: ten minutes recorded under January, then
	// network time moved the clock to 9 November. A day after today came from
	// a clock that ran ahead in an earlier run.
	stale := 312 * 24 * time.Hour
	days := map[string]int64{"2026-01-01": 600, "2026-11-08": 100, "2026-12-25": 50}
	if !playtimeReanchorDays(days, "2026-01-01", 600, stale, "2026-11-09") {
		t.Fatal("nothing moved")
	}
	want := map[string]int64{"2026-11-08": 100, "2026-11-09": 650}
	if len(days) != len(want) {
		t.Fatalf("days = %v, want %v", days, want)
	}
	for k, v := range want {
		if days[k] != v {
			t.Errorf("days[%s] = %d, want %d (all: %v)", k, days[k], v, days)
		}
	}
	// The run claims more than the day holds (pruned meanwhile): move what is
	// there, never go negative.
	days = map[string]int64{"2026-01-01": 300}
	playtimeReanchorDays(days, "2026-01-01", 600, stale, "2026-11-09")
	if days["2026-11-09"] != 300 || len(days) != 1 {
		t.Errorf("days = %v", days)
	}
	// Nothing wrong: nothing moves.
	days = map[string]int64{"2026-11-08": 100, "2026-11-09": 5}
	if playtimeReanchorDays(days, "2026-11-09", 5, 0, "2026-11-09") {
		t.Errorf("a correct ledger changed: %v", days)
	}
	// An evening played yesterday, an idle night, then a small correction
	// (or a zone change) in the morning: the date did not move, so the
	// evening stays where it is.
	days = map[string]int64{"2026-11-08": 3 * 3600}
	if playtimeReanchorDays(days, "2026-11-09", 0, 3*time.Second, "2026-11-09") || days["2026-11-08"] != 3*3600 {
		t.Errorf("an idle night's evening moved: %v", days)
	}
	if playtimeReanchorDays(days, "2026-11-09", 0, 9*time.Hour, "2026-11-09") || days["2026-11-08"] != 3*3600 {
		t.Errorf("a morning zone change moved the evening: %v", days)
	}
	// A correction of three seconds just after midnight re-dates three
	// seconds, not the evening recorded before it.
	if !playtimeReanchorDays(days, "2026-11-08", 3*3600, 3*time.Second, "2026-11-09") {
		t.Fatal("a jump across midnight moved nothing")
	}
	if days["2026-11-08"] != 3*3600-3 || days["2026-11-09"] != 3 {
		t.Errorf("a 3 s jump across midnight: %v", days)
	}
	// A backward jump across midnight: the day the clock left lies after the
	// new today, so it is folded in, and only once.
	days = map[string]int64{"2026-11-08": 600, "2026-11-09": 120}
	if !playtimeReanchorDays(days, "2026-11-09", 120, -time.Hour, "2026-11-08") || days["2026-11-08"] != 720 || len(days) != 1 {
		t.Errorf("a backward jump: %v", days)
	}
}

func TestDurationWords(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{400 * time.Millisecond, "0.4 s"},
		{-12 * time.Second, "12 s"},
		{3 * time.Minute, "3 min"},
		{2*time.Hour + 13*time.Minute, "2 h 13 min"},
		{5 * time.Hour, "5 h"},
		{41 * 24 * time.Hour, "41 days"},
	}
	for _, c := range cases {
		if got := durationWords(c.d); got != c.want {
			t.Errorf("durationWords(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if got := clockOffsetWords(3 * time.Second); got != "3.0 s behind" {
		t.Errorf("behind: %q", got)
	}
	if got := clockOffsetWords(-3 * time.Minute); got != "3 min ahead" {
		t.Errorf("ahead: %q", got)
	}
}
