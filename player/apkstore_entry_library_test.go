package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// A NativeActivity or GameActivity APK may bundle a large helper library.
// The Games profile must start the library with the activity's entry point.
func TestApkStoreSelectsNativeEntryLibrary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		launcher string
		entry    string
	}{
		{"native activity", "android.app.NativeActivity", "ANativeActivity_onCreate"},
		{"game activity", "com.google.androidgamesdk.GameActivity", "Java_com_google_androidgamesdk_GameActivity_initializeNativeCode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := axEl("manifest", []axAttr{{name: "package", typ: apkxTypeString, str: "org.example.multilib"}},
				axEl("application", []axAttr{axBool("hasCode", axIDHasCode, true)},
					axEl("activity", []axAttr{axString("name", axIDName, tc.launcher)},
						axEl("intent-filter", nil,
							axEl("action", []axAttr{axString("name", axIDName, "android.intent.action.MAIN")}),
							axEl("category", []axAttr{axString("name", axIDName, "android.intent.category.LAUNCHER")}),
						),
					),
				),
			)
			dir := t.TempDir()
			apk := filepath.Join(dir, "multi.apk")
			out, err := os.Create(apk)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(out)
			add := func(name string, content []byte) {
				w, err := zw.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(content); err != nil {
					t.Fatal(err)
				}
			}
			add("AndroidManifest.xml", buildAXML(root, true, false))
			add("lib/arm64-v8a/libhelper.so", make([]byte, 1024))
			add("lib/arm64-v8a/libentry.so", append([]byte("dynstr\x00"), []byte(tc.entry+"\x00")...))
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
			info := apkStoreAnalyze(apk, false)
			if info.Tier != apkTierNative || info.MainLib != "libentry.so" ||
				info.EntryLib != "lib/arm64-v8a/libentry.so" {
				t.Fatalf("selection: tier=%v main=%q entry=%q verdict=%q", info.Tier, info.MainLib, info.EntryLib, info.Verdict)
			}
			profile, err := apkStoreBuildProfile(info, []string{dir})
			if err != nil {
				t.Fatal(err)
			}
			if profile.MainLib != "libentry.so" {
				t.Fatalf("Games profile loads %q, want libentry.so", profile.MainLib)
			}
		})
	}
}
