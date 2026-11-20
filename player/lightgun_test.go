package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A hand-edited light-gun.json can make the gun odd, never broken.
func TestLightgunSettingsSane(t *testing.T) {
	got := lightgunSettings{Aim: "sideways", Speed: 99, Area: "moon", AreaW: 5, AreaH: 400, AreaX: -90, AreaY: 12, Trigger: "a", Reload: "l2"}.sane()
	want := lightgunSettings{Aim: "move", Speed: lightgunSpeedDefault, Area: "picture", AreaW: lightgunAreaMin, AreaH: lightgunAreaMax,
		AreaX: -lightgunShiftMax, AreaY: 12, Trigger: "r1", Reload: "l2"}
	if got != want {
		t.Fatalf("sane() = %+v, want %+v", got, want)
	}
	if def := lightgunDefaults(); def.sane() != def {
		t.Fatalf("the defaults are not sane: %+v", def)
	}
}

// The settings survive a save and a fresh read; a newer file is left alone.
func TestLightgunStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	oldPath := lightgunStorePath
	lightgunStorePath = func() string { return filepath.Join(dir, lightgunRecordName) }
	reset := func() {
		lightgunStore.mu.Lock()
		lightgunStore.loaded, lightgunStore.readOnly, lightgunStore.auto, lightgunStore.games = false, false, nil, nil
		lightgunStore.mu.Unlock()
	}
	t.Cleanup(func() { lightgunStorePath = oldPath; reset() })
	reset()
	rom := filepath.Join(dir, "Duck Hunt (World).nes")
	s := lightgunDefaults()
	s.Aim, s.Area, s.AreaW, s.Trigger, s.Recoil = "point", "custom", 80, "r2", true
	if !lightgunSaveSettings(rom, s) || !lightgunSetAutoAssign(false) {
		t.Fatal("saving failed")
	}
	lightgunMarkDecided(rom)
	reset()
	got := lightgunSettingsFor(rom)
	s.Auto = true
	if got != s.sane() {
		t.Fatalf("read back %+v, want %+v", got, s.sane())
	}
	if lightgunAutoAssignOn() || !lightgunDecided(rom) {
		t.Fatal("the switch or the decided mark was lost")
	}
}

// The launch's keys come from the config's own player 1 bindings; "nul" is
// no binding, port 2 reads player 1's pad and raises the user count.
func TestLightgunCfgValues(t *testing.T) {
	cfg := strings.Join([]string{
		`input_player1_a_btn = "1"`,
		`input_player1_b_btn = "0"`,
		`input_player1_x_btn = "nul"`,
		`input_player1_start_btn = "7"`,
		`input_player1_select_btn = "6"`,
		`input_player1_up_btn = "h0up"`,
		`input_max_users = "1"`,
	}, "\n")
	binds := lightgunPadBinds(cfg)
	if _, ok := binds["x"]; ok {
		t.Fatal("a nul binding was copied")
	}
	got := map[string]string{}
	for _, v := range lightgunCfgValues(cfg, lightgunCfgPlan{Port: 2, Device: 260, Sticks: true}, binds) {
		got[v.Key] = v.Value
	}
	want := map[string]string{
		"input_libretro_device_p2":              "260",
		"input_player2_mouse_index":             "0",
		"input_player2_gun_trigger_mbtn":        "1",
		"input_player2_gun_offscreen_shot_mbtn": "2",
		"input_player2_gun_aux_a_btn":           "1",
		"input_player2_gun_start_btn":           "7",
		"input_player2_gun_dpad_up_btn":         "h0up",
		"input_player2_joypad_index":            "0",
		"input_max_users":                       "2",
		"menu_mouse_enable":                     "false",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["input_player2_gun_aux_c_btn"]; ok {
		t.Error("a binding the config does not have was set")
	}
	// Without sticks the D-pad aims: the gun's D-pad is left alone.
	for _, v := range lightgunCfgValues(cfg, lightgunCfgPlan{Port: 1, Device: 4}, binds) {
		if strings.Contains(v.Key, "dpad") || v.Key == "input_max_users" || strings.HasSuffix(v.Key, "joypad_index") {
			t.Errorf("port 1 without sticks set %s", v.Key)
		}
	}
}
