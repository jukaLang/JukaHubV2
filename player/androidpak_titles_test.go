package main

import (
	"strings"
	"testing"
)

func TestAndroidPakDetectTitle(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		abi     string
		title   string
		pkg     string
		errHas  string
	}{
		{"plain NDK game", []string{"AndroidManifest.xml", "lib/arm64-v8a/libgame.so"}, "arm64-v8a", "", "", ""},
		{"Minecraft arm64", []string{"lib/arm64-v8a/libminecraftpe.so", "lib/arm64-v8a/libfmod.so"}, "arm64-v8a",
			"Minecraft", "com.mojang.minecraftpe", ""},
		{"Minecraft 32-bit only", []string{"lib/armeabi-v7a/libminecraftpe.so"}, "armeabi-v7a", "", "",
			"arm64-v8a build"},
		{"Xamarin app", []string{"lib/arm64-v8a/libmonodroid.so", "lib/arm64-v8a/libmonosgen-2.0.so"}, "arm64-v8a",
			"", "", "NDK (native C/C++) games only"},
		{"Terraria", []string{"lib/arm64-v8a/libmonodroid.so", "assemblies/Terraria.dll"}, "arm64-v8a", "", "",
			"Terraria's mobile edition"},
	}
	for _, c := range cases {
		got, err := androidPakDetectTitle(c.entries, c.abi)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%s: error = %v, want one containing %q", c.name, err, c.errHas)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got.Name != c.title || got.Package != c.pkg {
			t.Errorf("%s: title %q / package %q, want %q / %q", c.name, got.Name, got.Package, c.title, c.pkg)
		}
	}
}
