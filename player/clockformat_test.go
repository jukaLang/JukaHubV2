package main

import (
	"testing"
	"time"
)

// The clock format must stay 24-hour unless the user turned it on: every
// previous build printed "15:04", and a config that predates the switch has no
// "clock12" key at all.
func TestClockDigitsDefaultsTo24Hour(t *testing.T) {
	when := time.Date(2026, 11, 4, 15, 4, 5, 0, time.Local)
	for _, config := range []*Config{nil, {}, {Variables: Variables{Clock12: false}}} {
		if got := clockDigits(config, when); got != "15:04" {
			t.Fatalf("clockDigits(%v) = %q, want 15:04", config, got)
		}
		if got := clockDigitsSeconds(config, when); got != "15:04:05" {
			t.Fatalf("clockDigitsSeconds(%v) = %q, want 15:04:05", config, got)
		}
		if got := clockDigitsHM(config, 15, 4); got != "15:04" {
			t.Fatalf("clockDigitsHM(%v) = %q, want 15:04", config, got)
		}
	}
}

func TestClockDigitsUsesTwelveHourWithAmPm(t *testing.T) {
	config := &Config{}
	config.Variables.Clock12 = true

	cases := []struct {
		hour, minute int
		want         string
	}{
		{0, 5, "12:05 AM"},
		{9, 5, "9:05 AM"},
		{11, 59, "11:59 AM"},
		{12, 0, "12:00 PM"},
		{15, 4, "3:04 PM"},
		{23, 59, "11:59 PM"},
	}
	for _, tc := range cases {
		when := time.Date(2026, 11, 4, tc.hour, tc.minute, 0, 0, time.Local)
		if got := clockDigits(config, when); got != tc.want {
			t.Errorf("clockDigits(%02d:%02d) = %q, want %q", tc.hour, tc.minute, got, tc.want)
		}
		if got := clockDigitsHM(config, tc.hour, tc.minute); got != tc.want {
			t.Errorf("clockDigitsHM(%02d:%02d) = %q, want %q", tc.hour, tc.minute, got, tc.want)
		}
	}

	// The alarm and clock app add seconds but keep the same meridiem.
	noon := time.Date(2026, 11, 4, 12, 0, 5, 0, time.Local)
	if got := clockDigitsSeconds(config, noon); got != "12:00:05 PM" {
		t.Errorf("clockDigitsSeconds(noon) = %q, want 12:00:05 PM", got)
	}
	// Out-of-range values (an alarm list that somehow holds 26:70) wrap instead
	// of printing "26:70", so a bad stored alarm cannot draw a fake time.
	if got := clockDigitsHM(config, 26, 70); got != "2:10 AM" {
		t.Errorf("clockDigitsHM(26,70) = %q, want 2:10 AM", got)
	}
	if got := clockDigitsHM(nil, 26, 70); got != "02:10" {
		t.Errorf("clockDigitsHM(nil,26,70) = %q, want 02:10", got)
	}
}

// The header picks the clock out of the status cluster so it can act as the
// calendar button; both clock formats have to be recognised, and nothing else
// in the cluster may be.
func TestIsClockStatusPartAcceptsBothClockFormats(t *testing.T) {
	for _, part := range []string{"15:04", "3:04", "3:04 PM", "12:00 AM", "11:59 pm", "9:05"} {
		if !isClockStatusPart(part) {
			t.Errorf("isClockStatusPart(%q) = false, want true", part)
		}
	}
	for _, part := range []string{"", "Offline", "! Offline", "84%", "12° / 4°", "24:00", "3:60", ":04", "3:4", "Now playing", "3:04 PMM"} {
		if isClockStatusPart(part) {
			t.Errorf("isClockStatusPart(%q) = true, want false", part)
		}
	}
}

// The switch has to survive the whole trip: Custom (how the builder and the
// toggle write it) → struct field → jukauser.json → back into a fresh config.
func TestClock12ToggleRoundTripsThroughUserConfig(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"clock12": "true"}
	applyVariableOverrides(config)
	if !config.Variables.Clock12 {
		t.Fatal("applyVariableOverrides did not read clock12 from Custom")
	}
	if got := inputVariableValue(config, "clock12"); got != "true" {
		t.Fatalf("inputVariableValue(clock12) = %q, want true", got)
	}
	element := Element{Variable: "clock12", Text: "12-hour clock", TriggerValue: "false"}
	if !toggleValue(config, element) {
		t.Fatal("the settings toggle should read back as on")
	}

	user := userConfigFromVariables(config)
	if user == nil || !user.Variables.Clock12 {
		t.Fatalf("user config lost clock12: %+v", user)
	}
	fresh := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	mergeUserConfig(fresh, user)
	if !fresh.Variables.Clock12 {
		t.Fatal("mergeUserConfig did not restore clock12")
	}
	// And the default stays 24-hour when the user file predates the switch.
	mergeUserConfig(fresh, &UserConfig{})
	if fresh.Variables.Clock12 {
		t.Fatal("an empty user file must not turn the 12-hour clock on")
	}
}
