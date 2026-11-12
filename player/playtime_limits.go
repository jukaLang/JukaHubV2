package main

// playtime_limits.go — optional daily game limits and break reminders.
//
// The tracker in playtime.go already knows how long JukaHub and each game have
// run. This file adds the two voluntary guardrails on top: a daily cap on game
// time and a reminder after a stretch of use. Both are off by default, and the
// cap is a speed bump rather than a lock: a second launch press within ten
// seconds starts the game anyway, so a shared handheld never becomes unusable
// because a limit was left on.

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	playtimeDailyLimitKey   = "playtime_daily_limit_min"
	playtimeBreakLimitKey   = "playtime_break_reminder_min"
	playtimeLimitConfirmFor = 10 * time.Second
)

var errPlaytimeDailyLimit = errors.New("Daily game limit reached — press Play again to start anyway")

var playtimeDailyLimitSteps = []int{0, 30, 60, 90, 120, 180}
var playtimeBreakLimitSteps = []int{0, 15, 30, 45, 60}

type playtimeLimitState struct {
	// warnedDay is the day the limit toast was last shown, so crossing the
	// line does not repeat it every frame.
	warnedDay string
	// breakBucket is the completed break interval already announced. It falls
	// back to zero when the sitting restarts after a real gap.
	breakBucket int64
	// A launch refused once may be confirmed by pressing Play again for the
	// same game inside playtimeLimitConfirmFor.
	confirmTitle string
	confirmAt    time.Time
}

var playtimeLimits playtimeLimitState

func playtimeLimitMinutes(c *Config, key string, steps []int) int {
	if c == nil {
		return 0
	}
	raw := strings.TrimSpace(customStringValue(c.Variables.Custom, key))
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	for _, step := range steps {
		if step == n {
			return n
		}
	}
	return 0
}

func playtimeDailyLimitMinutes(c *Config) int {
	return playtimeLimitMinutes(c, playtimeDailyLimitKey, playtimeDailyLimitSteps)
}

func playtimeBreakLimitMinutes(c *Config) int {
	return playtimeLimitMinutes(c, playtimeBreakLimitKey, playtimeBreakLimitSteps)
}

func playtimeDurationLabel(minutes int) string {
	switch minutes {
	case 0:
		return "Off"
	case 60:
		return "1 hour"
	case 90:
		return "1½ hours"
	case 120:
		return "2 hours"
	case 180:
		return "3 hours"
	}
	// Longer sittings (the break reminder's later rounds) read as hours.
	if minutes > 60 {
		h, m := strconv.Itoa(minutes/60), minutes%60
		if m == 0 {
			return h + " hours"
		}
		return h + " h " + strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(minutes) + " min"
}

func playtimeChoiceOptions(steps []int) []setOption {
	out := make([]setOption, 0, len(steps))
	for _, n := range steps {
		out = append(out, setOption{Value: strconv.Itoa(n), Label: playtimeDurationLabel(n)})
	}
	return out
}

func playtimeChoiceCurrent(c *Config, key string, steps []int) int {
	minutes := playtimeLimitMinutes(c, key, steps)
	for i, n := range steps {
		if n == minutes {
			return i
		}
	}
	return -1
}

func playtimeLimitChoose(c *Config, key string, steps []int, value string) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return
	}
	valid := false
	for _, step := range steps {
		if step == n {
			valid = true
			break
		}
	}
	if !valid {
		return
	}
	setStoreString(c, key, value)
	playtimeLimits.warnedDay = ""
	playtimeLimits.breakBucket = 0
	playtimeLimits.confirmTitle = ""
	if key == playtimeDailyLimitKey {
		setToast("Daily game limit: " + playtimeDurationLabel(n))
		return
	}
	setToast("Break reminder: " + playtimeDurationLabel(n))
}

// playtimeDailyLimitRow is the Settings row for the daily game cap.
func playtimeDailyLimitRow() setRow {
	return setRow{
		ID:      "playtimeDailyLimit",
		Label:   "Daily game limit",
		Kind:    setKindChoice,
		Detail:  setStatic("Applies to games launched from JukaHub; play again to override"),
		Options: func(*Config) []setOption { return playtimeChoiceOptions(playtimeDailyLimitSteps) },
		Current: func(c *Config) int {
			return playtimeChoiceCurrent(c, playtimeDailyLimitKey, playtimeDailyLimitSteps)
		},
		Choose: func(c *Config, value string) {
			playtimeLimitChoose(c, playtimeDailyLimitKey, playtimeDailyLimitSteps, value)
		},
	}
}

// playtimeBreakReminderRow is the Settings row for the break reminder.
func playtimeBreakReminderRow() setRow {
	return setRow{
		ID:      "playtimeBreakReminder",
		Label:   "Break reminder",
		Kind:    setKindChoice,
		Detail:  setStatic("A toast after this much continuous use"),
		Options: func(*Config) []setOption { return playtimeChoiceOptions(playtimeBreakLimitSteps) },
		Current: func(c *Config) int {
			return playtimeChoiceCurrent(c, playtimeBreakLimitKey, playtimeBreakLimitSteps)
		},
		Choose: func(c *Config, value string) {
			playtimeLimitChoose(c, playtimeBreakLimitKey, playtimeBreakLimitSteps, value)
		},
	}
}

// playtimeAllowGameLaunch answers whether the daily cap has been reached. A
// second press for the same game inside the confirmation window is the user's
// deliberate override.
func playtimeAllowGameLaunch(title string) error {
	limit := playtimeDailyLimitMinutes(appConfig)
	if limit <= 0 {
		return nil
	}
	used := playtime.data.GameDays[playtimeTodayKey()]
	if used < int64(limit*60) {
		return nil
	}
	now := time.Now()
	if playtimeLimits.confirmTitle == title && now.Sub(playtimeLimits.confirmAt) <= playtimeLimitConfirmFor {
		playtimeLimits.confirmTitle = ""
		return nil
	}
	playtimeLimits.confirmTitle = title
	playtimeLimits.confirmAt = now
	return errPlaytimeDailyLimit
}

// playtimeLimitsTick is called from playtimeTick after the day and the sitting
// have been updated. It never touches disk: the playtime snapshot already does.
func playtimeLimitsTick(config *Config) {
	limit := playtimeDailyLimitMinutes(config)
	if limit > 0 {
		day := playtimeTodayKey()
		used := playtime.data.GameDays[day]
		if used >= int64(limit*60) && playtimeLimits.warnedDay != day {
			playtimeLimits.warnedDay = day
			ShowToast("Daily game limit reached — take a break when you can", ToastKindWarning)
		}
	}

	breakMinutes := playtimeBreakLimitMinutes(config)
	if breakMinutes <= 0 || playtime.session <= 0 {
		playtimeLimits.breakBucket = 0
		return
	}
	seconds := int64(breakMinutes) * 60
	bucket := playtime.session / seconds
	if bucket < 1 {
		playtimeLimits.breakBucket = 0
		return
	}
	if bucket == playtimeLimits.breakBucket {
		return
	}
	playtimeLimits.breakBucket = bucket
	// A game leaves the main loop parked, so the reminder arrives when the
	// user comes back to JukaHub, which is the first moment it can be seen.
	// The sitting so far is whole intervals, not one: the second reminder of a
	// 30-minute setting used to say "30 min" an hour in.
	if playtime.session >= seconds {
		ShowToast("Time for a break — you've been at it for "+playtimeDurationLabel(int(bucket)*breakMinutes), ToastKindInfo)
	}
}
