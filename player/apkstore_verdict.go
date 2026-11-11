package main

// apkstore_verdict.go — the APK Store's honest answer for one APK.
//
// AndroidRT (docs/ANDROID.md) runs one kind of Android game: native (NDK) code
// started by NativeActivity or androidx GameActivity, with a thin Java side it
// answers itself. Almost everything else on Android is Java or Kotlin bytecode
// that needs Android's own runtime, and an engine's name usually says which
// side of that line a game is on: Unity, Godot, GameMaker, LÖVE and SDL games
// all start in Java even though most of their code is native.
//
// So the verdict combines three readings: AndroidPak's triage (ABIs, the native
// entry, the titles it knows — androidpak.go, androidpak_titles.go), the
// manifest (the launcher activity, hasCode, android.app.lib_name) and the
// engine the library names give away. The words are the user's: what it is,
// whether it can run here, and — when it cannot — the route that works instead.
// Nothing here pretends: "try it" is as far as a verdict goes, because no game
// has been verified on a handheld yet.

import (
	"strconv"
	"strings"
)

// apkStoreTier is the one-word answer the row's pill shows.
type apkStoreTier int

const (
	apkTierBroken    apkStoreTier = iota // not a readable APK
	apkTierNative                        // native game: try it (experimental)
	apkTierMinecraft                     // Minecraft and its class pack (experimental)
	apkTierMaybe                         // a native entry point, but a Java start: may not get far
	apkTierJava                          // needs Android's Java runtime
	apkTierWrongCPU                      // no 64-bit ARM code (AndroidRT loads arm64-v8a only)
	apkTierSplit                         // one piece of a split app: runs only with its pieces
)

// apkStoreEntryCheck is what the native libraries said about an entry point
// AndroidRT can start (apkStoreFindEntryPoint).
type apkStoreEntryCheck int

const (
	apkEntryUnchecked apkStoreEntryCheck = iota // not looked at
	apkEntryFound                               // a library exports one
	apkEntryNone                                // every library was read; none does
	apkEntryUnknown                             // the bounded read could not cover every library
)

// canAdd reports whether "Add to Games" is offered for the tier.
func (t apkStoreTier) canAdd() bool {
	return t == apkTierNative || t == apkTierMinecraft || t == apkTierMaybe
}

// pill is the row's short status word.
func (t apkStoreTier) pill() string {
	switch t {
	case apkTierNative:
		return "Try it"
	case apkTierMinecraft:
		return "Experimental"
	case apkTierMaybe:
		return "May not run"
	case apkTierJava:
		return "Can't run"
	case apkTierWrongCPU:
		return "Wrong CPU"
	case apkTierSplit:
		return "Split APK"
	}
	return "Unreadable"
}

// tone colours the pill.
func (t apkStoreTier) tone() tbxTone {
	switch t {
	case apkTierNative:
		return tbxToneGood
	case apkTierMinecraft, apkTierMaybe, apkTierSplit:
		return tbxToneWarn
	case apkTierJava, apkTierWrongCPU:
		return tbxToneBad
	}
	return tbxToneNeutral
}

// rank orders the list: what can be tried first.
func (t apkStoreTier) rank() int {
	switch t {
	case apkTierNative:
		return 0
	case apkTierMinecraft:
		return 1
	case apkTierMaybe:
		return 2
	case apkTierSplit:
		return 3
	case apkTierJava:
		return 4
	case apkTierWrongCPU:
		return 5
	}
	return 6
}

// Engines the store recognises.
const (
	apkEngineNativeActivity = "NativeActivity"
	apkEngineGameActivity   = "GameActivity"
	apkEngineMinecraft      = "Minecraft (Bedrock)"
	apkEngineUnity          = "Unity"
	apkEngineUnreal         = "Unreal Engine"
	apkEngineGodot          = "Godot"
	apkEngineCocos          = "Cocos2d-x"
	apkEngineGameMaker      = "GameMaker"
	apkEngineLove           = "LÖVE"
	apkEngineSDL            = "SDL"
	apkEngineFlutter        = "Flutter"
	apkEngineReactNative    = "React Native"
	apkEngineXamarin        = "Xamarin / .NET"
)

// apkEngineLibs maps a library base name to the engine it gives away. The
// order of the checks in apkStoreDetectEngine decides ties.
var apkEngineLibs = map[string]string{
	"libminecraftpe.so":     apkEngineMinecraft,
	"libunity.so":           apkEngineUnity,
	"libil2cpp.so":          apkEngineUnity,
	"libUE4.so":             apkEngineUnreal,
	"libUnreal.so":          apkEngineUnreal,
	"libgodot_android.so":   apkEngineGodot,
	"libcocos2dcpp.so":      apkEngineCocos,
	"libcocos2djs.so":       apkEngineCocos,
	"libcocos2dlua.so":      apkEngineCocos,
	"libcocos.so":           apkEngineCocos,
	"libyoyo.so":            apkEngineGameMaker,
	"liblove.so":            apkEngineLove,
	"libSDL2.so":            apkEngineSDL,
	"libSDL3.so":            apkEngineSDL,
	"libflutter.so":         apkEngineFlutter,
	"libreactnativejni.so":  apkEngineReactNative,
	"libhermes.so":          apkEngineReactNative,
	"libmonodroid.so":       apkEngineXamarin,
	"libmonosgen-2.0.so":    apkEngineXamarin,
	"libxamarin-app.so":     apkEngineXamarin,
	"libmono-native.so":     apkEngineXamarin,
	"libxa-internal-api.so": apkEngineXamarin,
}

// apkEngineOrder is the priority when an APK carries several engines' names:
// a Unity game bundling SDL for its controller support is a Unity game.
var apkEngineOrder = []string{
	apkEngineMinecraft, apkEngineXamarin, apkEngineUnity, apkEngineUnreal, apkEngineGodot,
	apkEngineGameMaker, apkEngineLove, apkEngineCocos, apkEngineFlutter, apkEngineReactNative, apkEngineSDL,
}

// apkStoreDetectEngine names the engine from the library base names (every
// ABI), the declared activities and the launcher class. dexNative reports
// that the DEX code mentions NativeActivity or GameActivity as a class, the
// sign of a thin Java subclass in front of a native game.
func apkStoreDetectEngine(libs, activities []string, launcher string, dexNative, dexGameActivity bool) string {
	found := map[string]bool{}
	for _, lib := range libs {
		if e, ok := apkEngineLibs[lib]; ok {
			found[e] = true
		}
	}
	for _, a := range activities {
		switch {
		case strings.HasPrefix(a, "org.libsdl.app."):
			found[apkEngineSDL] = true
		case strings.HasPrefix(a, "com.unity3d.player."):
			found[apkEngineUnity] = true
		case strings.HasPrefix(a, "org.godotengine."):
			found[apkEngineGodot] = true
		case strings.HasPrefix(a, "com.yoyogames.runner."):
			found[apkEngineGameMaker] = true
		case strings.HasPrefix(a, "org.love2d."):
			found[apkEngineLove] = true
		case strings.HasPrefix(a, "org.cocos2dx."):
			found[apkEngineCocos] = true
		case strings.HasPrefix(a, "com.epicgames."):
			found[apkEngineUnreal] = true
		}
	}
	for _, e := range apkEngineOrder {
		if found[e] {
			return e
		}
	}
	switch {
	case launcher == "android.app.NativeActivity":
		return apkEngineNativeActivity
	case launcher == "com.google.androidgamesdk.GameActivity", dexGameActivity:
		return apkEngineGameActivity
	case dexNative:
		return apkEngineNativeActivity
	}
	return ""
}

// apkStoreJavaEngine reports whether an engine starts in Java (and so cannot
// run on AndroidRT today), with the sentence that says why and what works.
func apkStoreJavaEngine(engine string) (why string, javaOnly bool) {
	switch engine {
	case apkEngineUnity:
		return "A Unity game: Unity's player starts in Java (UnityPlayerActivity) and calls back into it every frame. " +
			"AndroidRT has no Java runtime, so it cannot run Unity games today. If the game has a Linux build, " +
			"PortMaster may carry it.", true
	case apkEngineUnreal:
		return "An Unreal Engine game: Unreal's Android build runs from its own Java activity with heavy Java glue, " +
			"which AndroidRT does not have.", true
	case apkEngineGodot:
		return "A Godot game: Godot's Android export starts in Java. Godot games usually have a PC build, and the " +
			"Linux arm64 export runs on the handheld through PortMaster.", true
	case apkEngineGameMaker:
		return "A GameMaker game: GameMaker's Android runner starts in Java. PortMaster's GMLoader runs a number of " +
			"GameMaker games from their Android APK on Linux handhelds — check PortMaster for this one.", true
	case apkEngineLove:
		return "A LÖVE game: LÖVE for Android is a Java app around the game's .love file. LÖVE for Linux runs .love " +
			"games directly (PortMaster carries it).", true
	case apkEngineCocos:
		return "A Cocos2d-x game: it starts in Java (Cocos2dxActivity), which AndroidRT does not provide.", true
	case apkEngineSDL:
		return "An SDL game: SDL's Android port starts in Java (SDLActivity). The same game's Linux build is the " +
			"better route — many SDL games are in PortMaster.", true
	case apkEngineFlutter:
		return "A Flutter app: its UI runs on Android's Java framework. It is not something AndroidRT can run.", true
	case apkEngineReactNative:
		return "A React Native app: it runs JavaScript on Android's Java framework. AndroidRT cannot run it.", true
	case apkEngineXamarin:
		return "A Xamarin/.NET app: its code is C# run by Mono inside a Java app. AndroidRT runs native code only.", true
	}
	return "", false
}

// apkStoreVerdictInput is everything the verdict reads.
type apkStoreVerdictInput struct {
	TriageErr   string   // inspectAndroidPak's refusal, "" when it accepted the APK
	ManifestErr string   // the manifest decoder's error
	ABIs        []string // every lib/<abi> the APK ships
	ChosenABI   string   // the ABI triage would run ("" when it refused)
	Minecraft   bool     // triage recognised Minecraft
	Engine      string
	HasCode     bool
	Launcher    string
	MinSDK      int
	// Split names the piece when the file is one part of a split app;
	// SplitsRequired marks the base part of one.
	Split          string
	SplitsRequired bool
	// Entry is what the libraries said about an entry point (checked for an
	// APK with native code and a Java start).
	Entry apkStoreEntryCheck
}

// apkStoreSplitWhy explains a split piece: it cannot run, and what to get
// instead.
func apkStoreSplitWhy(in apkStoreVerdictInput) string {
	what := "This is the base piece of an app that was installed as a bundle: its native libraries and other " +
		"resources live in separate split_config.… APKs beside it, so on its own it cannot start."
	if in.Split != "" {
		what = "This is one piece (\"" + in.Split + "\") of an app that was installed as a bundle: it holds only part " +
			"of the app — some libraries or resources — and the app's code is in its base.apk."
	}
	return what + " AndroidRT needs the whole game in one APK. Get a single (universal) APK instead: some games publish " +
		"one, and a tool that merges split APKs (such as APKEditor's merge command) can make one from the pieces you own."
}

// apkStoreVerdict returns the tier, the one-line verdict and the explanation.
func apkStoreVerdict(in apkStoreVerdictInput) (apkStoreTier, string, string) {
	// A split piece first: the base piece has no libraries, which triage would
	// otherwise call a Java app, and a library piece has no app at all.
	if in.Split != "" || in.SplitsRequired {
		return apkTierSplit, "One piece of a split APK — can't run on its own", apkStoreSplitWhy(in)
	}
	if in.TriageErr != "" {
		lower := strings.ToLower(in.TriageErr)
		switch {
		case strings.Contains(lower, "open apk"), strings.Contains(lower, "not an apk"):
			return apkTierBroken, "Not a readable APK", "The file could not be read as an Android package: " + in.TriageErr + "."
		case strings.Contains(lower, "xamarin"), strings.Contains(lower, "mono"):
			return apkTierJava, "Needs Android's Java runtime — can't run on AndroidRT", in.TriageErr + "."
		case strings.Contains(lower, "no native libraries"):
			return apkTierJava, "Needs Android's Java runtime — can't run on AndroidRT",
				"It has no native code: the whole app is Java/Kotlin bytecode, which only Android's own runtime (ART) " +
					"can run. AndroidRT runs native (NDK) games only; a full Android firmware is the way to run apps like this."
		case strings.Contains(lower, "minecraft"):
			return apkTierWrongCPU, "Wrong build for the handheld", in.TriageErr + "."
		case strings.Contains(lower, "no abi"):
			abis := strings.Join(in.ABIs, ", ")
			if abis == "" {
				abis = "another CPU"
			}
			return apkTierWrongCPU, "Wrong CPU (" + abis + " only)",
				"Its native code is built for " + abis + " only — usually a PC-emulator build. AndroidRT runs 64-bit ARM " +
					"(arm64-v8a) code; look for the arm64-v8a version of this APK."
		}
		return apkTierBroken, "Cannot be prepared", in.TriageErr + "."
	}
	if in.Minecraft {
		return apkTierMinecraft, "Minecraft class pack (experimental)", "AndroidRT answers Minecraft's Java side with its own " +
			"class pack. The store, the license check and Xbox Live answer as unavailable (never as granted), so a Google " +
			"Play build may refuse to start without its license. It needs OpenGL ES 3.0 and most of the 1 GB of RAM " +
			"(docs/ANDROID-MINECRAFT.md)."
	}
	if why, javaOnly := apkStoreJavaEngine(in.Engine); javaOnly {
		return apkTierJava, "Needs Android's Java runtime — can't run on AndroidRT", why
	}
	// Triage falls back to 32-bit libraries when there are no 64-bit ones, but
	// AndroidRT's loader refuses 32-bit ELF outright (androidrt/src/elfdyn.c).
	if in.ChosenABI != "" && in.ChosenABI != "arm64-v8a" {
		return apkTierWrongCPU, "32-bit build only (" + in.ChosenABI + ")", "Its native code is 32-bit ARM (" +
			in.ChosenABI + ") only. AndroidRT loads 64-bit (arm64-v8a) libraries only, so it cannot start this build — " +
			"look for the 64-bit (arm64-v8a) APK of the same game."
	}
	note := ""
	if in.MinSDK > 28 {
		note = " It asks for a newer Android (API " + strconv.Itoa(in.MinSDK) + ") than the Android 9 (API 28) AndroidRT " +
			"reports, so it may refuse to start."
	}
	switch {
	case in.Engine == apkEngineNativeActivity || in.Engine == apkEngineGameActivity || !in.HasCode:
		engine := in.Engine
		if engine == "" {
			engine = "no Java code"
		}
		return apkTierNative, "Native game — try it (experimental)", "It starts in native code (" + engine + "), the kind " +
			"of game AndroidRT is built for. No game has been verified on a handheld yet: try it, and if it stops, the " +
			"launch log says what is missing." + note
	}
	launcher := in.Launcher
	if launcher == "" {
		launcher = "a Java activity"
	}
	switch in.Entry {
	case apkEntryNone:
		// AndroidRT never runs the Java start: it calls a native entry point,
		// and a library without one is a helper for Java code (runner.c).
		return apkTierJava, "Needs Android's Java runtime — can't run on AndroidRT", "It starts in " + launcher +
			", and its native libraries are helpers for that Java code: none of them has the NativeActivity or " +
			"GameActivity entry point AndroidRT starts a game from. Only Android's own runtime (ART) can run it."
	case apkEntryFound:
		return apkTierMaybe, "Native entry, Java start — may not run", "One of its libraries has a NativeActivity or " +
			"GameActivity entry point, but the app starts in " + launcher + ". AndroidRT skips that Java start and calls the " +
			"native entry directly, so the game may work or may stop early; trying it is safe, and the launch log " +
			"names what it asked for." + note
	}
	return apkTierMaybe, "Native code, Java start — may not run", "It ships native libraries but starts in " + launcher +
		", and the store could not read every library to look for the NativeActivity or GameActivity entry point " +
		"AndroidRT starts a game from. If there is none, the launch stops at once and its log says so." + note
}
