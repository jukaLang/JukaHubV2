package main

import (
	"testing"
)

func TestPlaytimeLimitMinutesAcceptsOnlyKnownSteps(t *testing.T) {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{
		playtimeDailyLimitKey: "60",
		playtimeBreakLimitKey: "50",
	}}}
	if got := playtimeDailyLimitMinutes(c); got != 60 {
		t.Errorf("daily limit = %d, want 60", got)
	}
	if got := playtimeBreakLimitMinutes(c); got != 0 {
		t.Errorf("break limit = %d, want 0 for an unknown step", got)
	}
	c.Variables.Custom[playtimeBreakLimitKey] = "30"
	if got := playtimeBreakLimitMinutes(c); got != 30 {
		t.Errorf("break limit = %d, want 30", got)
	}
}

func TestPlaytimeDailyLimitNeedsASecondPress(t *testing.T) {
	oldApp, oldPlaytime, oldLimits := appConfig, playtime, playtimeLimits
	defer func() {
		appConfig, playtime, playtimeLimits = oldApp, oldPlaytime, oldLimits
	}()
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		playtimeDailyLimitKey: "60",
	}}}
	playtime = playtimeState{data: newPlaytimeData()}
	playtime.data.GameDays[playtimeTodayKey()] = 60 * 60
	playtimeLimits = playtimeLimitState{}

	if err := playtimeAllowGameLaunch("A game"); err == nil {
		t.Fatal("a game over the daily limit must ask first")
	}
	if err := playtimeAllowGameLaunch("A game"); err != nil {
		t.Fatalf("the second press must confirm the launch: %v", err)
	}
	if err := playtimeAllowGameLaunch("A game"); err == nil {
		t.Fatal("the confirmation must be single-use")
	}
}

func TestPlaytimeLimitLabel(t *testing.T) {
	if got := playtimeDurationLabel(90); got != "1½ hours" {
		t.Errorf("90 minutes reads %q", got)
	}
}
