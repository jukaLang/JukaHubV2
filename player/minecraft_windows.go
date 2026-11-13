//go:build windows

package main

// minecraft_windows.go — the Minecraft launchers a Windows PC may have, and
// how each one is started. Nothing is downloaded or signed in here: JukaHub
// only finds what the player installed and starts it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	// mcStoreLauncherPackage is the Minecraft Launcher from the Microsoft
	// Store / Xbox app.
	mcStoreLauncherPackage = "Microsoft.4297127D64EC6_8wekyb3d8bbwe"
	// mcBedrockPackage is Minecraft for Windows (Bedrock).
	mcBedrockPackage = "Microsoft.MinecraftUWP_8wekyb3d8bbwe"
)

// mcJavaHomeCandidates lists where the Java launcher keeps .minecraft.
func mcJavaHomeCandidates() []string {
	if v := strings.TrimSpace(os.Getenv("APPDATA")); v != "" {
		return []string{filepath.Join(v, ".minecraft")}
	}
	return nil
}

// mcFirstFile returns the first path that is a regular file, "" for none.
func mcFirstFile(paths ...string) string {
	for _, p := range paths {
		if p != "" && regularFile(p) {
			return p
		}
	}
	return ""
}

// mcEnvJoin joins elem onto an environment folder, "" when it is unset.
func mcEnvJoin(env string, elem ...string) string {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return ""
	}
	return filepath.Join(append([]string{v}, elem...)...)
}

// mcStartExe starts a program in its own folder and lets it run on.
func mcStartExe(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// mcStartAppsFolder starts a Store app by its application id through the
// shell's AppsFolder, the way the Start menu does.
func mcStartAppsFolder(appID string) error {
	cmd := exec.Command("explorer.exe", `shell:AppsFolder\`+appID)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// mcProtocolRegistered reports whether a URL scheme has a handler.
func mcProtocolRegistered(scheme string) bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, scheme, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("URL Protocol")
	return err == nil
}

// mcDesktopApps lists the Minecraft launchers on this PC.
func mcDesktopApps() []mcDesktopApp {
	var apps []mcDesktopApp
	java := mcFirstFile(
		mcEnvJoin("ProgramFiles(x86)", "Minecraft Launcher", "MinecraftLauncher.exe"),
		mcEnvJoin("ProgramFiles", "Minecraft Launcher", "MinecraftLauncher.exe"),
		`C:\XboxGames\Minecraft Launcher\Content\Minecraft.exe`,
	)
	switch {
	case java != "":
		apps = append(apps, mcDesktopApp{id: "launcher", title: "Minecraft Launcher",
			subtitle: "Java Edition and Bedrock, with your Microsoft account",
			launch:   func() error { return mcStartExe(java) }})
	case dirExists(mcEnvJoin("LOCALAPPDATA", "Packages", mcStoreLauncherPackage)):
		apps = append(apps, mcDesktopApp{id: "launcher", title: "Minecraft Launcher",
			subtitle: "From the Microsoft Store — Java Edition and Bedrock",
			launch:   func() error { return mcStartAppsFolder(mcStoreLauncherPackage + "!Minecraft") }})
	}
	if dirExists(mcEnvJoin("LOCALAPPDATA", "Packages", mcBedrockPackage)) || mcProtocolRegistered("minecraft") {
		apps = append(apps, mcDesktopApp{id: "bedrock", title: "Minecraft for Windows",
			subtitle: "Bedrock Edition — controllers, Realms and crossplay",
			launch: func() error {
				// The constant link goes to its registered handler; rundll32
				// keeps any shell out of it, as steamOpenURI does.
				return mcStartExe("rundll32", "url.dll,FileProtocolHandler", "minecraft:")
			}})
	}
	if prism := mcFirstFile(
		mcEnvJoin("LOCALAPPDATA", "Programs", "PrismLauncher", "prismlauncher.exe"),
		mcEnvJoin("ProgramFiles", "PrismLauncher", "prismlauncher.exe"),
		mcEnvJoin("USERPROFILE", "scoop", "apps", "prismlauncher", "current", "prismlauncher.exe"),
	); prism != "" {
		apps = append(apps, mcDesktopApp{id: "prism", title: "Prism Launcher",
			subtitle: "Java Edition instances and mod packs",
			launch:   func() error { return mcStartExe(prism) }})
	}
	return apps
}
