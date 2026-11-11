package main

import (
	"strings"
	"testing"
)

func TestApkStoreDetectEngine(t *testing.T) {
	cases := []struct {
		name       string
		libs       []string
		activities []string
		launcher   string
		dexNative  bool
		dexGA      bool
		want       string
	}{
		{"unity", []string{"libunity.so", "libil2cpp.so", "libmain.so"}, nil, "com.unity3d.player.UnityPlayerActivity", false, false, apkEngineUnity},
		{"unity beats sdl", []string{"libSDL2.so", "libunity.so"}, nil, "", false, false, apkEngineUnity},
		{"sdl by activity", nil, []string{"org.libsdl.app.SDLActivity"}, "", false, false, apkEngineSDL},
		{"godot", []string{"libgodot_android.so"}, nil, "", false, false, apkEngineGodot},
		{"gamemaker", []string{"libyoyo.so"}, nil, "", false, false, apkEngineGameMaker},
		{"love", []string{"liblove.so"}, nil, "", false, false, apkEngineLove},
		{"minecraft wins", []string{"libminecraftpe.so", "libfmod.so"}, nil, "", false, false, apkEngineMinecraft},
		{"xamarin", []string{"libmonodroid.so"}, nil, "", false, false, apkEngineXamarin},
		{"native activity", []string{"libgame.so"}, nil, "android.app.NativeActivity", false, false, apkEngineNativeActivity},
		{"game activity by dex", []string{"libgame.so"}, nil, "com.example.Main", false, true, apkEngineGameActivity},
		{"native subclass by dex", []string{"libgame.so"}, nil, "com.example.Main", true, false, apkEngineNativeActivity},
		{"unknown", []string{"libhelper.so"}, nil, "com.example.Main", false, false, ""},
	}
	for _, c := range cases {
		if got := apkStoreDetectEngine(c.libs, c.activities, c.launcher, c.dexNative, c.dexGA); got != c.want {
			t.Errorf("%s: engine = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestApkStoreVerdictTiers(t *testing.T) {
	cases := []struct {
		name string
		in   apkStoreVerdictInput
		tier apkStoreTier
		says string
	}{
		{"not a zip", apkStoreVerdictInput{TriageErr: "open APK: zip: not a valid zip file"}, apkTierBroken, "readable"},
		{"java app", apkStoreVerdictInput{TriageErr: "no native libraries — this is a Java/Dalvik app, not a Tier-1 NDK game"}, apkTierJava, "ART"},
		{"x86 only", apkStoreVerdictInput{TriageErr: "no ABI this device can run (A133P wants arm64-v8a or armeabi-v7a)",
			ABIs: []string{"x86", "x86_64"}}, apkTierWrongCPU, "x86, x86_64"},
		{"terraria", apkStoreVerdictInput{TriageErr: "Terraria's mobile edition is a C#/.NET game running on Mono inside a Java app"}, apkTierJava, "Mono"},
		{"32-bit minecraft", apkStoreVerdictInput{TriageErr: "this Minecraft APK carries only armeabi-v7a libraries"}, apkTierWrongCPU, "Minecraft"},
		{"minecraft", apkStoreVerdictInput{Minecraft: true, Engine: apkEngineMinecraft, HasCode: true}, apkTierMinecraft, "license"},
		{"unity", apkStoreVerdictInput{Engine: apkEngineUnity, HasCode: true}, apkTierJava, "Unity"},
		{"gamemaker route", apkStoreVerdictInput{Engine: apkEngineGameMaker, HasCode: true}, apkTierJava, "GMLoader"},
		{"native", apkStoreVerdictInput{Engine: apkEngineNativeActivity, HasCode: true}, apkTierNative, "try it"},
		{"no java code", apkStoreVerdictInput{HasCode: false}, apkTierNative, "no Java code"},
		{"java start", apkStoreVerdictInput{HasCode: true, Launcher: "com.example.Main"}, apkTierMaybe, "com.example.Main"},
		{"java start, entry", apkStoreVerdictInput{HasCode: true, Launcher: "com.example.Main", Entry: apkEntryFound}, apkTierMaybe, "entry point"},
		{"java start, helpers only", apkStoreVerdictInput{HasCode: true, Launcher: "com.example.Main", Entry: apkEntryNone}, apkTierJava, "helpers"},
		{"new android", apkStoreVerdictInput{Engine: apkEngineNativeActivity, HasCode: true, MinSDK: 31}, apkTierNative, "API 31"},
		{"32-bit native", apkStoreVerdictInput{Engine: apkEngineNativeActivity, ChosenABI: "armeabi-v7a"}, apkTierWrongCPU, "64-bit"},
		{"64-bit native", apkStoreVerdictInput{Engine: apkEngineNativeActivity, ChosenABI: "arm64-v8a"}, apkTierNative, "try it"},
		{"32-bit unity is a java app first", apkStoreVerdictInput{Engine: apkEngineUnity, HasCode: true, ChosenABI: "armeabi-v7a"}, apkTierJava, "Unity"},
		{"split piece", apkStoreVerdictInput{Split: "config.arm64_v8a", Engine: apkEngineNativeActivity}, apkTierSplit, "universal"},
		{"split base", apkStoreVerdictInput{SplitsRequired: true, TriageErr: "no native libraries — this is a Java/Dalvik app"}, apkTierSplit, "base piece"},
	}
	for _, c := range cases {
		tier, verdict, why := apkStoreVerdict(c.in)
		if tier != c.tier {
			t.Errorf("%s: tier = %v, want %v (%s)", c.name, tier, c.tier, verdict)
		}
		if !strings.Contains(verdict+" "+why, c.says) {
			t.Errorf("%s: %q / %q does not mention %q", c.name, verdict, why, c.says)
		}
	}
}

// Only the tiers that can run on AndroidRT may become game profiles, and a
// Java app is never worded as something to try.
func TestApkStoreTierWording(t *testing.T) {
	for _, tier := range []apkStoreTier{apkTierBroken, apkTierNative, apkTierMinecraft, apkTierMaybe, apkTierJava, apkTierWrongCPU, apkTierSplit} {
		add := tier == apkTierNative || tier == apkTierMinecraft || tier == apkTierMaybe
		if tier.canAdd() != add {
			t.Errorf("tier %d: canAdd = %v", tier, tier.canAdd())
		}
		if tier.pill() == "" {
			t.Errorf("tier %d has no pill", tier)
		}
	}
	if apkTierJava.pill() != "Can't run" || apkTierNative.pill() != "Try it" {
		t.Errorf("pills: java %q native %q", apkTierJava.pill(), apkTierNative.pill())
	}
	if apkTierNative.rank() >= apkTierJava.rank() || apkTierMaybe.rank() >= apkTierSplit.rank() {
		t.Error("runnable APKs must sort before the rest")
	}
}
