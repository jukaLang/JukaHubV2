package main

import "testing"

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
