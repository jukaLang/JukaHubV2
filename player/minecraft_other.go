//go:build !windows

package main

// minecraft_other.go — Minecraft launchers on a Linux (or macOS) desktop. A
// TrimUI handheld has none of them: its ways in are the PortMaster ports and
// AndroidRT (minecraft.go), so the search is skipped there.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// mcJavaHomeCandidates lists where the Java launcher keeps .minecraft.
func mcJavaHomeCandidates() []string {
	if IsTrimuiDevice() {
		return nil
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".minecraft"),
		filepath.Join(home, ".var", "app", "com.mojang.Minecraft", ".minecraft"),
		filepath.Join(home, "Library", "Application Support", "minecraft"),
	}
}

// mcStartCmd starts a program and lets it run on.
func mcStartCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// mcDesktopApps lists the Minecraft launchers on this computer.
func mcDesktopApps() []mcDesktopApp {
	if IsTrimuiDevice() {
		return nil
	}
	var apps []mcDesktopApp
	home := strings.TrimSpace(os.Getenv("HOME"))
	flatpak, _ := exec.LookPath("flatpak")
	hasFlatpak := func(id string) bool {
		return flatpak != "" && home != "" && dirExists(filepath.Join(home, ".var", "app", id))
	}
	if bin, err := exec.LookPath("minecraft-launcher"); err == nil {
		apps = append(apps, mcDesktopApp{id: "launcher", title: "Minecraft Launcher",
			subtitle: "Java Edition, with your Microsoft account",
			launch:   func() error { return mcStartCmd(bin) }})
	} else if hasFlatpak("com.mojang.Minecraft") {
		apps = append(apps, mcDesktopApp{id: "launcher", title: "Minecraft Launcher",
			subtitle: "Java Edition (Flatpak), with your Microsoft account",
			launch:   func() error { return mcStartCmd(flatpak, "run", "com.mojang.Minecraft") }})
	}
	if bin, err := exec.LookPath("prismlauncher"); err == nil {
		apps = append(apps, mcDesktopApp{id: "prism", title: "Prism Launcher",
			subtitle: "Java Edition instances and mod packs",
			launch:   func() error { return mcStartCmd(bin) }})
	} else if hasFlatpak("org.prismlauncher.PrismLauncher") {
		apps = append(apps, mcDesktopApp{id: "prism", title: "Prism Launcher",
			subtitle: "Java Edition instances and mod packs (Flatpak)",
			launch:   func() error { return mcStartCmd(flatpak, "run", "org.prismlauncher.PrismLauncher") }})
	}
	return apps
}
