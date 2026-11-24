package main

import (
	"strings"
	"testing"
)

func TestApkStoreNativeLauncherBeatsBundledEngine(t *testing.T) {
	for _, launcher := range []string{"android.app.NativeActivity", "com.google.androidgamesdk.GameActivity"} {
		in := apkStoreVerdictInput{
			ChosenABI: "arm64-v8a", Engine: apkEngineSDL, HasCode: true, Launcher: launcher,
		}
		tier, _, _ := apkStoreVerdict(in)
		if tier != apkTierNative || !tier.canAdd() {
			t.Errorf("%s with SDL library = %v, want an addable native APK", launcher, tier)
		}
		in.ChosenABI = "armeabi-v7a"
		if tier, _, _ := apkStoreVerdict(in); tier != apkTierWrongCPU {
			t.Errorf("%s with 32-bit library = %v, want wrong CPU", launcher, tier)
		}
	}
}

func TestApkStoreJavaEngineStillNeedsAndroid(t *testing.T) {
	for _, engine := range []string{apkEngineSDL, apkEngineGodot, apkEngineUnity} {
		in := apkStoreVerdictInput{
			ChosenABI: "arm64-v8a", Engine: engine, HasCode: true, Launcher: "org.libsdl.app.SDLActivity",
		}
		if tier, _, _ := apkStoreVerdict(in); tier != apkTierJava || tier.canAdd() {
			t.Errorf("Java-started %s = %v, want a non-addable Java APK", engine, tier)
		}
	}
}

func TestApkStoreNativeEntryBehindJavaLauncherIsExperimental(t *testing.T) {
	in := apkStoreVerdictInput{
		ChosenABI: "arm64-v8a", Engine: apkEngineNativeActivity,
		HasCode: true, Launcher: "org.example.NativeLauncher", Entry: apkEntryFound,
	}
	if tier, _, _ := apkStoreVerdict(in); tier != apkTierMaybe || !tier.canAdd() {
		t.Errorf("Java subclass with native entry = %v, want addable but uncertain", tier)
	}
	in.Entry = apkEntryNone
	if tier, _, _ := apkStoreVerdict(in); tier != apkTierJava || tier.canAdd() {
		t.Errorf("Java subclass without native entry = %v, want Java-only", tier)
	}
}

// A Qt streaming app (Colosseum's APK: Qt 6 libraries named per ABI, libmpv)
// is a Java app here, and its verdict says where films play instead.
func TestApkStoreQtStreamingAppPointsToMedia(t *testing.T) {
	libs := []string{"libQt6Core_arm64-v8a.so", "libQt6Quick_arm64-v8a.so", "libmpv.so"}
	launcher := "org.qtproject.qt.android.bindings.QtActivity"
	engine := apkStoreDetectEngine(libs, []string{launcher}, launcher, false, false)
	if engine != apkEngineQt || !apkStoreMediaApp(libs) {
		t.Fatalf("engine = %q, media = %v", engine, apkStoreMediaApp(libs))
	}
	in := apkStoreVerdictInput{ChosenABI: "arm64-v8a", Engine: engine, HasCode: true, Launcher: launcher, Media: true}
	tier, _, why := apkStoreVerdict(in)
	if tier != apkTierJava || tier.canAdd() || !strings.HasSuffix(why, apkStoreMediaRoute) {
		t.Errorf("Qt streaming app = %v %q", tier, why)
	}
}
