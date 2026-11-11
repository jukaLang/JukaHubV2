package main

import (
	"strings"
	"testing"
)

// childEnvCheck asserts that env holds exactly the wanted values for the named
// keys ("" with present=false means the key must be absent), and that no key
// appears twice.
func childEnvCheck(t *testing.T, env []string, want map[string]*string) {
	t.Helper()
	seen := map[string]int{}
	values := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		seen[key]++
		values[key] = value
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times in %q", key, n, env)
		}
	}
	for key, value := range want {
		got, ok := values[key]
		switch {
		case value == nil && ok:
			t.Errorf("%s = %q, want it unset", key, got)
		case value != nil && !ok:
			t.Errorf("%s is unset, want %q", key, *value)
		case value != nil && got != *value:
			t.Errorf("%s = %q, want %q", key, got, *value)
		}
	}
}

func childEnvStr(s string) *string { return &s }

func TestScreenChildEnvRestoresRecordedOriginals(t *testing.T) {
	cases := []struct {
		name    string
		environ []string
		want    map[string]*string
	}{
		{
			name: "all three recorded",
			environ: []string{
				"JUKAHUB_ORIG_ENV=HOME PATH LD_LIBRARY_PATH",
				"JUKAHUB_ORIG_HOME=/root",
				"JUKAHUB_ORIG_PATH=/usr/trimui/bin:/usr/bin:/bin",
				"JUKAHUB_ORIG_LD_LIBRARY_PATH=/mnt/SDCARD/System/lib:/usr/trimui/lib",
				"HOME=/mnt/SDCARD/Apps/JukaHub",
				"PATH=/mnt/SDCARD/Apps/JukaHub:/mnt/SDCARD/Apps/JukaHub/required:/usr/trimui/bin:/usr/bin:/bin",
				"LD_LIBRARY_PATH=/mnt/SDCARD/Apps/JukaHub:/mnt/SDCARD/Apps/JukaHub/libs:/usr/trimui/lib:/usr/lib:/lib:/mnt/SDCARD/System/lib:/usr/trimui/lib",
				"JUKAHUB_LAUNCHED_BY=launch.sh",
				"JUKAHUB_SCREEN_HANDOFF=1",
				"TZ=Europe/Berlin",
				"CLR_OPENSSL_VERSION_OVERRIDE=1.1",
			},
			want: map[string]*string{
				"HOME":                         childEnvStr("/root"),
				"PATH":                         childEnvStr("/usr/trimui/bin:/usr/bin:/bin"),
				"LD_LIBRARY_PATH":              childEnvStr("/mnt/SDCARD/System/lib:/usr/trimui/lib"),
				"TZ":                           childEnvStr("Europe/Berlin"),
				"CLR_OPENSSL_VERSION_OVERRIDE": childEnvStr("1.1"),
				"JUKAHUB_ORIG_ENV":             nil,
				"JUKAHUB_ORIG_HOME":            nil,
				"JUKAHUB_ORIG_PATH":            nil,
				"JUKAHUB_ORIG_LD_LIBRARY_PATH": nil,
				"JUKAHUB_LAUNCHED_BY":          nil,
				"JUKAHUB_SCREEN_HANDOFF":       nil,
			},
		},
		{
			// No JUKAHUB_ORIG_LD_LIBRARY_PATH: the firmware had none, so the
			// child gets none, not JukaHub's.
			name: "unset before launch.sh stays unset",
			environ: []string{
				"JUKAHUB_ORIG_ENV=HOME PATH LD_LIBRARY_PATH",
				"JUKAHUB_ORIG_PATH=/usr/bin:/bin",
				"HOME=/mnt/SDCARD/Apps/JukaHub",
				"PATH=/app:/usr/bin:/bin",
				"LD_LIBRARY_PATH=/app:/app/libs:/usr/trimui/lib:/usr/lib:/lib:",
			},
			want: map[string]*string{
				"HOME":            nil,
				"PATH":            childEnvStr("/usr/bin:/bin"),
				"LD_LIBRARY_PATH": nil,
			},
		},
		{
			// Set but empty is a value, and it is what the child gets.
			name: "empty is not unset",
			environ: []string{
				"JUKAHUB_ORIG_ENV=HOME",
				"JUKAHUB_ORIG_HOME=",
				"HOME=/app",
			},
			want: map[string]*string{"HOME": childEnvStr("")},
		},
		{
			// Duplicated keys collapse to the one restored value, and the
			// record itself may be duplicated: the last one wins, as in exec.
			name: "duplicates collapse",
			environ: []string{
				"PATH=/first",
				"JUKAHUB_ORIG_ENV=PATH",
				"JUKAHUB_ORIG_PATH=/stale",
				"PATH=/second",
				"JUKAHUB_ORIG_PATH=/usr/bin",
			},
			want: map[string]*string{"PATH": childEnvStr("/usr/bin")},
		},
		{
			// Junk in the list never touches anything: TZ is JukaHub's choice
			// for every child, ENV's record would be the list itself, and
			// names a shell could not hold are ignored.
			name: "junk names ignored",
			environ: []string{
				"JUKAHUB_ORIG_ENV=TZ ENV 1BAD JUKAHUB_X HO-ME PATH PATH",
				"JUKAHUB_ORIG_TZ=UTC",
				"JUKAHUB_ORIG_PATH=/usr/bin",
				"TZ=Asia/Tokyo",
				"ENV=/etc/profile",
				"PATH=/app:/usr/bin",
			},
			want: map[string]*string{
				"TZ":   childEnvStr("Asia/Tokyo"),
				"ENV":  childEnvStr("/etc/profile"),
				"PATH": childEnvStr("/usr/bin"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := screenChildEnvFor(tc.environ, "/app")
			childEnvCheck(t, got, tc.want)
		})
	}
}

// Unrelated variables keep their place: only the managed ones move, to the end.
func TestScreenChildEnvKeepsOrderOfOthers(t *testing.T) {
	env := screenChildEnvFor([]string{
		"A=1", "JUKAHUB_ORIG_ENV=HOME", "HOME=/app", "B=2", "JUKAHUB_ORIG_HOME=/root", "C=3",
	}, "/app")
	if got := strings.Join(env, " "); got != "A=1 B=2 C=3 HOME=/root" {
		t.Fatalf("env = %q", got)
	}
}

func TestScreenChildEnvFallbackStripsAppFolder(t *testing.T) {
	cases := []struct {
		name    string
		environ []string
		appDir  string
		want    map[string]*string
	}{
		{
			name: "older launch.sh",
			environ: []string{
				"HOME=/mnt/SDCARD/Apps/JukaHub",
				"PATH=/mnt/SDCARD/Apps/JukaHub:/mnt/SDCARD/Apps/JukaHub/required:/usr/bin:/bin",
				"LD_LIBRARY_PATH=/mnt/SDCARD/Apps/JukaHub:/mnt/SDCARD/Apps/JukaHub/libs:/usr/trimui/lib:/usr/lib:/lib:",
				"JUKAHUB_LAUNCHED_BY=launch.sh",
				"JUKAHUB_SCREEN_HANDOFF=1",
			},
			appDir: "/mnt/SDCARD/Apps/JukaHub",
			want: map[string]*string{
				// HOME is left alone without a record: the launchers that need
				// one set their own.
				"HOME":                   childEnvStr("/mnt/SDCARD/Apps/JukaHub"),
				"PATH":                   childEnvStr("/usr/bin:/bin"),
				"LD_LIBRARY_PATH":        childEnvStr("/usr/trimui/lib:/usr/lib:/lib"),
				"JUKAHUB_LAUNCHED_BY":    nil,
				"JUKAHUB_SCREEN_HANDOFF": nil,
			},
		},
		{
			// Every occurrence goes, trailing slashes included; a list left
			// with nothing is unset rather than empty.
			name: "every occurrence, nothing left",
			environ: []string{
				"PATH=/app/:/usr/bin:/app",
				"LD_LIBRARY_PATH=/app/libs::/app/",
			},
			appDir: "/app",
			want: map[string]*string{
				"PATH":            childEnvStr("/usr/bin"),
				"LD_LIBRARY_PATH": nil,
			},
		},
		{
			// No app folder known: only the empty entries go.
			name:    "no app folder",
			environ: []string{"PATH=/usr/bin::/bin:"},
			appDir:  "",
			want:    map[string]*string{"PATH": childEnvStr("/usr/bin:/bin")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			childEnvCheck(t, screenChildEnvFor(tc.environ, tc.appDir), tc.want)
		})
	}
}

// screenChildEnv reads the app folder through childEnvAppDir.
func TestScreenChildEnvUsesAppDir(t *testing.T) {
	prev := childEnvAppDir
	childEnvAppDir = func() string { return "/opt/juka" }
	t.Cleanup(func() { childEnvAppDir = prev })
	env := screenChildEnv([]string{"PATH=/opt/juka/required:/usr/bin"})
	childEnvCheck(t, env, map[string]*string{"PATH": childEnvStr("/usr/bin")})
}
