package main

import "testing"

func TestResolveSafeMode(t *testing.T) {
	noEnv := func(string) string { return "" }
	env := func(v string) func(string) string {
		return func(string) string { return v }
	}

	cases := []struct {
		name    string
		args    []string
		getenv  func(string) string
		marker  bool
		want    bool
		reason  string
		wantRun bool
	}{
		{name: "default", args: nil, getenv: noEnv, want: false, wantRun: true},
		{name: "flag", args: []string{"--safe-mode"}, getenv: noEnv, want: true, reason: "command line"},
		{name: "single dash flag", args: []string{"-safe-mode"}, getenv: noEnv, want: true, reason: "command line"},
		{name: "flag wins over env", args: []string{"--safe-mode"}, getenv: env("1"), marker: true, want: true, reason: "command line"},
		{name: "env 1", getenv: env("1"), want: true, reason: "JUKAHUB_SAFE_MODE"},
		{name: "env TRUE", getenv: env(" TRUE "), want: true, reason: "JUKAHUB_SAFE_MODE"},
		{name: "env on", getenv: env("on"), want: true, reason: "JUKAHUB_SAFE_MODE"},
		{name: "env yes", getenv: env("yes"), want: true, reason: "JUKAHUB_SAFE_MODE"},
		{name: "env zero", getenv: env("0"), want: false, wantRun: true},
		{name: "env off", getenv: env("off"), want: false, wantRun: true},
		{name: "marker file", getenv: noEnv, marker: true, want: true, reason: safeModeFlagPath},
		{name: "unrelated flag", args: []string{"--verbose"}, getenv: noEnv, want: false, wantRun: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSafeMode(tc.args, tc.getenv, tc.marker)
			if got.Active != tc.want {
				t.Fatalf("Active = %v, want %v (%+v)", got.Active, tc.want, got)
			}
			if got.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.reason)
			}
			if got.Active && got.Reason == "" {
				t.Error("active safe mode must report why it was enabled")
			}
		})
	}
}

func TestSafeModeAllows(t *testing.T) {
	prev := safeMode
	t.Cleanup(func() { safeMode = prev })

	all := []SafeFeature{
		SafeFeatureNetwork, SafeFeaturePlugins,
		SafeFeatureUpdates, SafeFeatureToolInstall,
	}

	safeMode = safeModeState{}
	for _, f := range all {
		if !safeModeAllows(f) {
			t.Errorf("feature %d blocked without safe mode", f)
		}
	}

	safeMode = safeModeState{Active: true, Reason: "test"}
	for _, f := range all {
		if safeModeAllows(f) {
			t.Errorf("feature %d allowed in safe mode", f)
		}
	}
	// Unknown future features default to allowed so adding one cannot silently
	// widen what safe mode suppresses.
	if !safeModeAllows(SafeFeature(99)) {
		t.Error("unknown feature should default to allowed")
	}
}

func TestLoadPluginsSkipsInSafeMode(t *testing.T) {
	prevMode, prevPlugins := safeMode, plugins
	t.Cleanup(func() { safeMode, plugins = prevMode, prevPlugins })

	safeMode = safeModeState{Active: true, Reason: "test"}
	plugins = map[string]*PluginInfo{"stale": {Name: "stale", Enabled: true}}

	LoadPlugins(&Config{})

	if len(plugins) != 0 {
		t.Fatalf("safe mode loaded %d plugin(s), want none", len(plugins))
	}
}
