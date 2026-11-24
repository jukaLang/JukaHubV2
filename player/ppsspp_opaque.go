package main

// ppsspp_opaque.go — a PSP game's picture made opaque for the A133P's panel.
//
// Juka, 2026-11-23: "I tried psp, but the screen was black and only played game
// sound." It was the third black PSP screen on his Smart Pro (2026-11-14 and
// 11-15 before it), and RetroArch's games beside JukaHub had always shown.
//
// On the A133P handhelds (Smart Pro, Brick, Hammer, Brick Pro) every program
// draws into the one framebuffer the display engine shows — the PowerVR's fbdev
// EGL, which SDL calls "mali" — and that framebuffer is ARGB. Two ports on this
// chip found that a picture whose alpha is not 1 can show as nothing at all, a
// black panel while the game plays and its sound runs: vf1-recomp's port (2026-
// 11-22) needed an opaque display layer, and NextOS-Ports' Horizon Chase needed
// SDL's own contexts. JukaHub and RetroArch write alpha 255 and show. PPSSPP's
// last pass copies the PSP's framebuffer to the screen as it is, alpha and all,
// and on the PSP that alpha is the stencil buffer: 0 in most games.
//
// So JukaHub's PPSSPP gets one more post-processing shader, "JukaHub opaque
// picture": the picture as PPSSPP made it, with alpha 1. It is written into the
// shared memstick's shaders folder (PSP/shaders, where PPSSPP looks for shaders
// of its own) and kept last in the chain ([PostShaderList] in ppsspp.ini, and in
// a game's own <ID>_ppsspp.ini, which PPSSPP reads instead). At output
// resolution the last shader is PPSSPP's final draw, so it adds no pass; a
// player's own shaders stay and run before it. On the tg5040 builds only — the
// Smart Pro S shows its picture through DRM/KMS — and never in the firmware's
// PPSSPP, whose files are the firmware's.
//
// Whether the picture then reached the panel is measured after each start
// (ppsspp_picture.go), which also takes over when it did not.

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// ppssppOpaqueShader is the shader's section in its ini: the name PPSSPP's
	// post-shader chain stores.
	ppssppOpaqueShader = "JukaHubOpaque"
	// ppssppOpaqueBase names the shader's three files in PSP/shaders.
	ppssppOpaqueBase = "jukahub-opaque"
	// ppssppPostShaderSection and ppssppPostShaderKey are PPSSPP's chain:
	// PostShader1, PostShader2… in key order; "Off" is an empty slot.
	ppssppPostShaderSection = "PostShaderList"
	ppssppPostShaderKey     = "PostShader"
)

// ppssppOpaqueINI describes the shader to PPSSPP (its comments start with #,
// as in PPSSPP's own defaultshaders.ini). OutputResolution: drawn at the
// screen's size, as the final pass.
const ppssppOpaqueINI = "# Written by JukaHub (ppsspp_opaque.go), again before every PSP game: it keeps\n" +
	"# the picture opaque on the TrimUI's panel. JukaHub keeps it last in the shader\n" +
	"# chain, after your own shaders.\n" +
	"[" + ppssppOpaqueShader + "]\n" +
	"Name=JukaHub opaque picture\n" +
	"Author=JukaHub\n" +
	"Fragment=" + ppssppOpaqueBase + ".fsh\n" +
	"Vertex=" + ppssppOpaqueBase + ".vsh\n" +
	"OutputResolution=True\n"

// ppssppOpaqueFragment is the picture with alpha 1, in the GLSL 1.x form of
// PPSSPP's own post shaders (it translates them for the backend in use).
const ppssppOpaqueFragment = "// JukaHub opaque picture: PPSSPP's picture with alpha 1 (ppsspp_opaque.go).\n" +
	"#ifdef GL_ES\n" +
	"precision mediump float;\n" +
	"precision mediump int;\n" +
	"#endif\n" +
	"\n" +
	"uniform sampler2D sampler0;\n" +
	"varying vec2 v_texcoord0;\n" +
	"\n" +
	"void main() {\n" +
	"\tgl_FragColor = vec4(texture2D(sampler0, v_texcoord0.xy).rgb, 1.0);\n" +
	"}\n"

// ppssppOpaqueVertex is PPSSPP's plain post-shader vertex stage.
const ppssppOpaqueVertex = "// JukaHub opaque picture (ppsspp_opaque.go).\n" +
	"#ifdef GL_ES\n" +
	"precision mediump float;\n" +
	"precision mediump int;\n" +
	"#endif\n" +
	"\n" +
	"attribute vec4 a_position;\n" +
	"attribute vec2 a_texcoord0;\n" +
	"varying vec2 v_texcoord0;\n" +
	"\n" +
	"void main() {\n" +
	"\tv_texcoord0 = a_texcoord0;\n" +
	"\tgl_Position = a_position;\n" +
	"}\n"

// ppssppOpaqueWanted reports whether a build for platform gets the shader: the
// A133P's builds, whose panel blends by alpha.
func ppssppOpaqueWanted(platform string) bool {
	return platform == ppssppOwnPlatform
}

// ppssppPostShaderChain is the chain [PostShaderList] holds, in PPSSPP's key
// order (PostShader1, 2, …; "Off" is no shader), and whether the section is
// there at all. Pure.
func ppssppPostShaderChain(lines []string) (chain []string, found bool) {
	type slot struct {
		n    int
		name string
	}
	var slots []slot
	section := ""
	for _, line := range lines {
		name, key, value, header := iniSectionKey(line)
		if header {
			section = name
			if strings.EqualFold(name, ppssppPostShaderSection) {
				found = true
			}
			continue
		}
		if !strings.EqualFold(section, ppssppPostShaderSection) || key == "" {
			continue
		}
		n, ok := ppssppPostShaderSlot(key)
		if !ok || value == "" || strings.EqualFold(value, "Off") {
			continue
		}
		slots = append(slots, slot{n, value})
	}
	sort.SliceStable(slots, func(i, j int) bool { return slots[i].n < slots[j].n })
	for _, s := range slots {
		chain = append(chain, s.name)
	}
	return chain, found
}

// ppssppPostShaderSlot reads the number of a "PostShader<n>" key.
func ppssppPostShaderSlot(key string) (int, bool) {
	if len(key) <= len(ppssppPostShaderKey) || !strings.EqualFold(key[:len(ppssppPostShaderKey)], ppssppPostShaderKey) {
		return 0, false
	}
	n, err := strconv.Atoi(key[len(ppssppPostShaderKey):])
	return n, err == nil && n > 0
}

// ppssppChainWithOpaque is chain with JukaHub's shader last: once, after every
// shader of the player's. Pure.
func ppssppChainWithOpaque(chain []string) []string {
	out := make([]string, 0, len(chain)+1)
	for _, name := range chain {
		if !strings.EqualFold(name, ppssppOpaqueShader) {
			out = append(out, name)
		}
	}
	return append(out, ppssppOpaqueShader)
}

// ppssppSetPostShaderChain makes [PostShaderList] hold exactly chain, as
// PostShader1…N right after its header, and leaves every other line alone. A
// missing section goes at the end of the file. Pure.
func ppssppSetPostShaderChain(lines []string, chain []string) []string {
	entries := make([]string, 0, len(chain))
	for i, name := range chain {
		entries = append(entries, ppssppPostShaderKey+strconv.Itoa(i+1)+" = "+name)
	}
	out := make([]string, 0, len(lines)+len(entries)+2)
	section, placed := "", false
	for _, line := range lines {
		name, key, _, header := iniSectionKey(line)
		if header {
			section = name
			out = append(out, line)
			if strings.EqualFold(name, ppssppPostShaderSection) && !placed {
				out = append(out, entries...)
				placed = true
			}
			continue
		}
		if strings.EqualFold(section, ppssppPostShaderSection) && key != "" {
			if _, ok := ppssppPostShaderSlot(key); ok {
				continue // replaced by entries
			}
		}
		out = append(out, line)
	}
	if placed {
		return out
	}
	if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
		out = out[:n-1]
	}
	return append(append(out, "["+ppssppPostShaderSection+"]"), entries...)
}

// ppssppIniWithOpaque is lines with the shader last in the chain, and whether
// that changed anything. Pure.
func ppssppIniWithOpaque(lines []string) ([]string, bool) {
	chain, found := ppssppPostShaderChain(lines)
	want := ppssppChainWithOpaque(chain)
	if found && strings.Join(chain, "\x00") == strings.Join(want, "\x00") {
		return lines, false
	}
	return ppssppSetPostShaderChain(lines, want), true
}

// ppssppWriteIfChanged writes data to path unless the file already holds it.
func ppssppWriteIfChanged(path string, data []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

// ppssppEnsureOpaquePicture writes the shader into memstick's shaders folder and
// puts it last in the chain of the memstick's ppsspp.ini and of every game's own
// ini beside it. memstick is the PSP folder; platform the build's. Only a file
// that needs it is rewritten (writePPSSPPControls keeps the line endings and a
// first-write backup). Never fails a launch: a problem is logged.
func ppssppEnsureOpaquePicture(memstick, platform string) {
	if memstick == "" || !ppssppOpaqueWanted(platform) {
		return
	}
	dir := filepath.Join(memstick, "shaders")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("ppsspp", "opaque picture: cannot create %s: %v", dir, err)
		return
	}
	for name, text := range map[string]string{
		ppssppOpaqueBase + ".ini": ppssppOpaqueINI,
		ppssppOpaqueBase + ".fsh": ppssppOpaqueFragment,
		ppssppOpaqueBase + ".vsh": ppssppOpaqueVertex,
	} {
		if err := ppssppWriteIfChanged(filepath.Join(dir, name), []byte(text)); err != nil {
			logWarn("ppsspp", "opaque picture: cannot write %s: %v", name, err)
			return
		}
	}
	sys := filepath.Join(memstick, "SYSTEM")
	inis := []string{filepath.Join(sys, "ppsspp.ini")}
	if entries, err := os.ReadDir(sys); err == nil {
		for _, e := range entries {
			if lower := strings.ToLower(e.Name()); !e.IsDir() && strings.HasSuffix(lower, "_ppsspp.ini") {
				inis = append(inis, filepath.Join(sys, e.Name()))
			}
		}
	}
	for _, ini := range inis {
		if !regularFile(ini) {
			continue
		}
		raw, err := os.ReadFile(ini)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
		if _, changed := ppssppIniWithOpaque(lines); !changed {
			continue
		}
		if err := writePPSSPPControls(ini, func(lines []string) []string {
			out, _ := ppssppIniWithOpaque(lines)
			return out
		}); err != nil {
			logWarn("ppsspp", "opaque picture: cannot set the shader chain in %s: %v", filepath.Base(ini), err)
			continue
		}
		logInfo("ppsspp", "opaque picture: %s ends its shader chain with %s", filepath.Base(ini), ppssppOpaqueShader)
	}
}
