package main

// Clock and date formats for the times the panel shows the user.
//
// The settings are the "clock12" toggle and the "dateFormat" choice in
// Settings → Date & time (datetime.go). They are stored in Variables.Custom
// like the other switches, so they persist in jukauser.json and a config
// written by the web builder can set them too. The default is the 24-hour
// clock, which is what every previous build rendered; 12-hour is opt-in and
// adds an upper-case AM/PM suffix. Dates follow the chosen order, or the
// country the connection is in when the choice is Auto.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// clockHour12 reports whether times are printed with an AM/PM suffix.
func clockHour12(config *Config) bool {
	return config != nil && config.Variables.Clock12
}

// clockDigits formats a wall-clock time: "15:04" by default, "3:04 PM" when the
// 12-hour clock is on. The single space before AM/PM keeps the status cluster's
// width predictable as the hour changes. In screenshot mode the present moment
// reads 9:41 (shotClock).
func clockDigits(config *Config, t time.Time) string {
	t = shotClock(t)
	if clockHour12(config) {
		return t.Format("3:04 PM")
	}
	return t.Format("15:04")
}

// clockDigitsSeconds is the clock-app and alarm variant that includes seconds.
func clockDigitsSeconds(config *Config, t time.Time) string {
	t = shotClock(t)
	if clockHour12(config) {
		return t.Format("3:04:05 PM")
	}
	return t.Format("15:04:05")
}

// clockDigitsHM formats an hour and minute that are not a time.Time value — the
// next alarm on the dock, for example — in the same clock as everywhere else.
func clockDigitsHM(config *Config, hour, minute int) string {
	hour = ((hour % 24) + 24) % 24
	minute = ((minute % 60) + 60) % 60
	if !clockHour12(config) {
		return fmt.Sprintf("%02d:%02d", hour, minute)
	}
	suffix := "AM"
	h := hour
	if hour >= 12 {
		suffix = "PM"
	}
	if h > 12 {
		h -= 12
	}
	if h == 0 {
		h = 12
	}
	return fmt.Sprintf("%d:%02d %s", h, minute, suffix)
}

// ── Dates ─────────────────────────────────────────────────────────────────

// dateFormatKey is the Custom setting behind Settings → Date & time → Date
// format. Its values are the dateOrder constants; anything else is Auto.
const dateFormatKey = "dateFormat"

// The orders a date can be written in. Auto ("") follows the country the
// connection is in (weather_units.go), and is day first until that is known.
const (
	dateOrderAuto = ""
	dateOrderDMY  = "dmy"
	dateOrderMDY  = "mdy"
	dateOrderYMD  = "ymd"
	dateOrderDots = "dots"
)

// dateLayouts are one order's Go layouts: Long carries the weekday and the
// full month, Day is Long without the year (the clock faces), Medium is the
// everyday date with the year, and Short leaves the year out.
type dateLayouts struct {
	Long, Day, Medium, Short string
}

var (
	dateLayoutsDMY  = dateLayouts{Long: "Monday, 2 January 2006", Day: "Monday, 2 January", Medium: "2 Jan 2006", Short: "2 Jan"}
	dateLayoutsMDY  = dateLayouts{Long: "Monday, January 2, 2006", Day: "Monday, January 2", Medium: "Jan 2, 2006", Short: "Jan 2"}
	dateLayoutsYMD  = dateLayouts{Long: "Monday, 2006-01-02", Day: "Monday, 01-02", Medium: "2006-01-02", Short: "01-02"}
	dateLayoutsDots = dateLayouts{Long: "Monday, 02.01.2006", Day: "Monday, 02.01.", Medium: "02.01.2006", Short: "02.01."}
)

// dateLayoutsFor returns an order's layouts (day first for an unknown one).
// It hands out the shared tables, so a frame that formats a date allocates
// nothing but the string itself.
func dateLayoutsFor(order string) *dateLayouts {
	switch order {
	case dateOrderMDY:
		return &dateLayoutsMDY
	case dateOrderYMD:
		return &dateLayoutsYMD
	case dateOrderDots:
		return &dateLayoutsDots
	}
	return &dateLayoutsDMY
}

// dateOrderValid reports whether order is one of the explicit choices.
func dateOrderValid(order string) bool {
	switch order {
	case dateOrderDMY, dateOrderMDY, dateOrderYMD, dateOrderDots:
		return true
	}
	return false
}

// dateOrderForCountry is the everyday order in a country: month first in the
// United States and the places that follow its usage, year first in East Asia,
// Hungary, Lithuania and Iran, day first everywhere else and while the country
// is not known.
func dateOrderForCountry(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "US", "PR", "GU", "VI", "AS", "MP", "UM", "FM", "MH", "PW", "PH":
		return dateOrderMDY
	case "CN", "JP", "KR", "KP", "TW", "MN", "HU", "LT", "IR":
		return dateOrderYMD
	}
	return dateOrderDMY
}

// dateOrder is the order config writes dates in: the stored choice, or for
// Auto the connection's country. Main thread (it reads the config's map).
func dateOrder(config *Config) string {
	if config != nil && config.Variables.Custom != nil {
		if v, ok := config.Variables.Custom[dateFormatKey].(string); ok && dateOrderValid(v) {
			return v
		}
	}
	return dateOrderForCountry(weatherCountryCode())
}

// formatDate writes a date with its year: "9 Nov 2026", "Nov 9, 2026",
// "2026-11-09" or "09.11.2026".
func formatDate(config *Config, t time.Time) string {
	return t.Format(dateLayoutsFor(dateOrder(config)).Medium)
}

// formatDateShort writes a date without its year ("9 Nov", "Nov 9"), for a
// date the reader knows is this year's.
func formatDateShort(config *Config, t time.Time) string {
	return t.Format(dateLayoutsFor(dateOrder(config)).Short)
}

// formatDateLong writes the weekday and the full date: "Monday, 9 November
// 2026" or "Monday, November 9, 2026".
func formatDateLong(config *Config, t time.Time) string {
	return t.Format(dateLayoutsFor(dateOrder(config)).Long)
}

// formatDayLong is formatDateLong without the year, for the clock faces that
// show today under the time: "Monday, 9 November". In screenshot mode today
// reads Saturday 14 November (shotClock).
func formatDayLong(config *Config, t time.Time) string {
	return shotClock(t).Format(dateLayoutsFor(dateOrder(config)).Day)
}

// formatDateTime writes a date and a time in the user's clock: "9 Nov 2026,
// 15:04" or "Nov 9, 2026, 3:04 PM". The old hard-coded "Jan 2, 15:04" layouts
// ignored the 12-hour setting; this one cannot.
func formatDateTime(config *Config, t time.Time) string {
	return formatDate(config, t) + ", " + clockDigits(config, t)
}

// formatDateTimeNear is formatDateTime for a moment the reader relates to now:
// "Today, 15:04", "Yesterday, 15:04", the date without the year this year, the
// full date otherwise. now and t are compared in t's zone.
func formatDateTimeNear(config *Config, t, now time.Time) string {
	now = now.In(t.Location())
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	switch {
	case ty == ny && tm == nm && td == nd:
		return "Today, " + clockDigits(config, t)
	case t.Before(now):
		y, m, d := now.AddDate(0, 0, -1).Date()
		if ty == y && tm == m && td == d {
			return "Yesterday, " + clockDigits(config, t)
		}
	}
	if ty == ny {
		return formatDateShort(config, t) + ", " + clockDigits(config, t)
	}
	return formatDateTime(config, t)
}

// isClockStatusPart reports whether a header status part is the clock text. The
// home screen treats the clock as its calendar button, so the header has to pick
// it out of the cluster in either clock format: "15:04" and "3:04 PM" both
// qualify, and nothing else in the cluster does.
func isClockStatusPart(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) >= 3 {
		upper := strings.ToUpper(s)
		if strings.HasSuffix(upper, " AM") || strings.HasSuffix(upper, " PM") {
			s = strings.TrimSpace(s[:len(s)-3])
		}
	}
	// Accept "H:MM" and "HH:MM"; the hour may be one digit only in 12-hour mode.
	if len(s) < 4 || len(s) > 5 {
		return false
	}
	colon := strings.IndexByte(s, ':')
	if colon < 1 || colon > 2 || colon != len(s)-3 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == colon {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	hour, err := strconv.Atoi(s[:colon])
	if err != nil || hour > 23 {
		return false
	}
	minute, err := strconv.Atoi(s[colon+1:])
	if err != nil || minute > 59 {
		return false
	}
	return true
}
