package main

// A staged Vulkan loader is an opt-in way for ARMSX2 to use the Smart Pro's
// PowerVR ICD. The firmware's libvulkan.so.1 lacks vkEnumerateInstanceVersion,
// while the app-local loader and ICD manifest in data/ps2-vulkan-probe pass a
// Vulkan 1.1 instance and display check. Only direct PS2 core launches get it.

import (
	"os"
	"os/exec"
	"path/filepath"
)

const ps2VulkanProbeFolder = "ps2-vulkan-probe"

// ps2VulkanPrepareLaunch runs after screenChildEnv restored the firmware's
// environment, and before raGlibcApply wraps RetroArch in its private runtime.
// The wrapper keeps cmd.Env and appends its LD_LIBRARY_PATH after the runtime.
func ps2VulkanPrepareLaunch(launcher string, cmd *exec.Cmd) {
	if cmd == nil || ps2SoC() == "" || !ps2IsCore(launcher) {
		return
	}
	dataDir, err := P().DataDir()
	if err != nil || !filepath.IsAbs(dataDir) {
		logWarn("library", "PS2: cannot find the private data folder for Vulkan: %v", err)
		return
	}
	dir := filepath.Join(dataDir, ps2VulkanProbeFolder)
	manifest := filepath.Join(dir, "img_icd.json")
	if !regularFile(filepath.Join(dir, "libvulkan.so.1")) ||
		!regularFile(filepath.Join(dir, "libvulkan.so")) || !regularFile(manifest) {
		logInfo("library", "PS2: private Vulkan loader is not staged; using the firmware's library")
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	old, _ := childEnvLookup(env, "LD_LIBRARY_PATH")
	libs := dir
	if old != "" {
		libs += ":" + old
	}
	env = childEnvWithout(env, "LD_LIBRARY_PATH")
	env = append(env, "LD_LIBRARY_PATH="+libs)
	// The modern loader accepts either name. Set both to the same app-local
	// manifest so an inherited VK_DRIVER_FILES cannot override this ICD.
	env = childEnvWithout(childEnvWithout(env, "VK_ICD_FILENAMES"), "VK_DRIVER_FILES")
	env = append(env, "VK_ICD_FILENAMES="+manifest, "VK_DRIVER_FILES="+manifest)
	cmd.Env = env

	// RetroArch must let the core switch to Vulkan and present on the launch
	// thread. Apply these settings after the other launch tuning, through the
	// journal that restores the player's config when RetroArch exits.
	values := []raScopeValue{
		{Key: "driver_switch_enable", Value: "true"},
		{Key: "video_threaded", Value: "false"},
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logWarn("library", "PS2: private Vulkan loader staged, but no RetroArch config found for launch settings")
	}
	for _, path := range targets {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("library", "PS2: cannot set Vulkan launch settings in %s: %v", path, err)
		}
	}
	// An existing core override is read after retroarch.cfg. Change only keys
	// it already defines, so the base config remains authoritative otherwise.
	if len(targets) > 0 {
		if raw, err := os.ReadFile(targets[0]); err == nil {
			override := filepath.Join(raTuneMenuConfigDir(string(raw), targets[0]), ps2CoreConfigName, ps2CoreConfigName+".cfg")
			if opt, err := os.ReadFile(override); err == nil {
				var held []raScopeValue
				for _, value := range values {
					if _, present := raConfigGet(string(opt), value.Key); present {
						held = append(held, value)
					}
				}
				if len(held) > 0 {
					if _, err := raScopeApply(override, held); err != nil {
						logWarn("library", "PS2: cannot set Vulkan launch settings in %s: %v", override, err)
					}
				}
			}
		}
	}
	logInfo("library", "PS2: app-local Vulkan loader, IMG ICD, and RetroArch launch settings prepared: %s", dir)
}
