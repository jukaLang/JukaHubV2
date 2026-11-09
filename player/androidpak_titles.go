package main

// androidpak_titles.go — the titles AndroidPak recognises by what their APK
// carries, before anything is staged.
//
//   - Minecraft: Bedrock Edition (lib/<abi>/libminecraftpe.so). AndroidRT
//     ships a class pack for its Java side (androidrt/src/jnipack_minecraft.c)
//     and needs the 64-bit build. The store, the license check and Xbox Live
//     answer as unavailable — never as granted — so what the game does
//     without a verified Google Play license is the game's own decision
//     (docs/ANDROID-MINECRAFT.md).
//   - Xamarin/Mono apps, Terraria's mobile edition among them
//     (libmonodroid.so, libmonosgen-2.0.so, libxamarin-app.so, assemblies/):
//     their game is .NET code a Mono runtime executes inside the Java app.
//     AndroidRT runs NDK code only, so triage refuses them up front and says
//     which route works instead.

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// androidPakTitle is what triage knows about a recognised title.
type androidPakTitle struct {
	Name    string // "Minecraft"; "" for an unrecognised NDK game
	Package string // the manifest package its AndroidRT class pack answers for
	Note    string // logged at launch
}

const androidPakMinecraftLib = "libminecraftpe.so"

// androidPakXamarinLibs mark a Xamarin.Android / .NET for Android package.
var androidPakXamarinLibs = map[string]bool{
	"libmonodroid.so":       true,
	"libmonosgen-2.0.so":    true,
	"libxamarin-app.so":     true,
	"libmono-native.so":     true,
	"libxa-internal-api.so": true,
}

// androidPakDetectTitle classifies an APK from its entry names (slash form)
// and the ABI triage chose. A non-nil error refuses the APK.
func androidPakDetectTitle(names []string, chosenABI string) (androidPakTitle, error) {
	var minecraftABIs []string
	xamarin, terraria := false, false
	for _, name := range names {
		base := path.Base(name)
		if strings.HasPrefix(name, "lib/") && base == androidPakMinecraftLib {
			minecraftABIs = append(minecraftABIs, path.Base(path.Dir(name)))
		}
		if strings.HasPrefix(name, "lib/") && androidPakXamarinLibs[base] {
			xamarin = true
		}
		if strings.HasPrefix(name, "assemblies/") {
			xamarin = true
		}
		if strings.Contains(strings.ToLower(base), "terraria") {
			terraria = true
		}
	}
	if xamarin {
		what := "This is a Xamarin/Mono (C#/.NET) app"
		if terraria {
			what = "Terraria's mobile edition is a C#/.NET game running on Mono inside a Java app"
		}
		return androidPakTitle{}, errors.New(what + " — AndroidRT runs NDK (native C/C++) games only, so it cannot " +
			"start it. Terraria's PC version runs on Linux handhelds through FNA/Mono with your own game files " +
			"(the PortMaster route); see docs/ANDROID.md")
	}
	if len(minecraftABIs) > 0 {
		if chosenABI != "arm64-v8a" {
			return androidPakTitle{}, fmt.Errorf("this Minecraft APK carries only %s libraries; AndroidRT runs the "+
				"arm64-v8a build — use a 64-bit (arm64-v8a) Minecraft APK", strings.Join(minecraftABIs, ", "))
		}
		return androidPakTitle{
			Name:    "Minecraft",
			Package: "com.mojang.minecraftpe",
			Note: "experimental: the Minecraft class pack answers the game's Java calls; the store, the license " +
				"check and Xbox Live report unavailable, so a Google Play build may refuse to start without its " +
				"license; it needs OpenGL ES 3.0 and most of the 1 GB of RAM (docs/ANDROID-MINECRAFT.md)",
		}, nil
	}
	return androidPakTitle{}, nil
}
