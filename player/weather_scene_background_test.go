package main

import "testing"

func TestWeatherSceneCondition(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{0, "sun"}, {2, "sun"}, {3, "cloud"}, {45, "cloud"},
		{51, "rain"}, {61, "rain"}, {80, "rain"}, {95, "rain"},
		{71, "snow"}, {85, "snow"},
	} {
		if got := weatherSceneCondition(tc.code); got != tc.want {
			t.Errorf("code %d: got %q, want %q", tc.code, got, tc.want)
		}
	}
}
