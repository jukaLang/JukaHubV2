package main

// NET-14: every game child is given the zone JukaHub is using now, replacing
// whatever TZ it inherited, so a zone picked in Settings reaches games without
// a restart.

import (
	"strings"
	"testing"
	"time"
)

func childEnvTZValues(env []string) []string {
	var out []string
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok && key == "TZ" {
			out = append(out, value)
		}
	}
	return out
}

func TestChildEnvWithTZReplacesInheritedZone(t *testing.T) {
	base := []string{"PATH=/usr/bin", "TZ=Europe/Paris", "TZ=UTC", "HOME=/home"}
	got := childEnvWithTZ(base, "Asia/Tokyo")
	if zones := childEnvTZValues(got); len(zones) != 1 || zones[0] != "Asia/Tokyo" {
		t.Fatalf("TZ entries = %v, want exactly [Asia/Tokyo]", zones)
	}
	if len(got) != 3 {
		t.Fatalf("environment has %d entries, want 3: %v", len(got), got)
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home"} {
		if !containsString(got, want) {
			t.Fatalf("%q was dropped: %v", want, got)
		}
	}
	for _, zone := range []string{"", "Local"} {
		got := childEnvWithTZ(base, zone)
		if zones := childEnvTZValues(got); len(zones) != 0 {
			t.Fatalf("zone %q left TZ entries %v, want none", zone, zones)
		}
	}
}

func TestGameChildZoneFollowsCurrentLocation(t *testing.T) {
	if zone := gameChildZoneFor(time.FixedZone("Europe/Berlin", 3600)); zone != "Europe/Berlin" {
		t.Fatalf("gameChildZone = %q, want Europe/Berlin", zone)
	}
	if zone := gameChildZoneFor(time.FixedZone("Local", 0)); zone != "" {
		t.Fatalf("gameChildZone = %q, want no TZ for the system zone", zone)
	}
}

func TestScreenChildEnvCarriesCurrentZone(t *testing.T) {
	got := screenChildEnvWithZone([]string{"PATH=/bin", "TZ=Europe/Paris"}, childEnvAppDir(), "America/New_York")
	if zones := childEnvTZValues(got); len(zones) != 1 || zones[0] != "America/New_York" {
		t.Fatalf("screen child TZ = %v, want [America/New_York]", zones)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
