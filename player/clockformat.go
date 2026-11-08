package main

// Clock format for the times the panel shows the user.
//
// The setting is the "clock12" toggle in Settings → Appearance. It is stored in
// Variables.Custom like the other switches, so it persists in jukauser.json and
// a config written by the web builder can set it too. The default is the
// 24-hour clock, which is what every previous build rendered; 12-hour is opt-in
// and adds an upper-case AM/PM suffix.

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
// width predictable as the hour changes.
func clockDigits(config *Config, t time.Time) string {
	if clockHour12(config) {
		return t.Format("3:04 PM")
	}
	return t.Format("15:04")
}

// clockDigitsSeconds is the clock-app and alarm variant that includes seconds.
func clockDigitsSeconds(config *Config, t time.Time) string {
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
