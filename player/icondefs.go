package main

// icondefs.go — the shell's icon set.
//
// Every glyph is drawn on a 24×24 grid in the icon language that iconlang.go
// parses (the grammar is documented there), so the set has one visual
// language: round-capped outlines in the base stroke, a translucent duotone
// body on objects, and a few solid details. iconatlas.go rasterizes an icon
// the first time it is drawn at a size and keeps the result in one texture.
//
// The table is plain data on purpose: outputs/icons_preview.py parses this
// same file to render contact sheets, so the picture a designer reviews is
// the picture the handheld draws. Keep each entry in the form
// {"name", `source`} and nothing else on its lines.

// iconDef is one icon's source in the icon language.
type iconDef struct {
	name string
	src  string
}

// iconDefs is the whole set, canonical names first by family. Names are
// lower-case; DrawHomeIcon normalizes what callers pass.
var iconDefs = [...]iconDef{
	// ── Home and apps ──────────────────────────────────────────────────
	{"continue", `
		D: R3 3 18 13 3
		F: M10 6.5 L15 9.5 L10 12.5 Z
		so.35: M4 20 H20
		s: M4 20 H12`},
	{"media", `
		s: M5 6 V5 Q5 3 7 3 H17 Q19 3 19 5 V6
		D: R2 6 20 15 3
		F: M10 10.5 L15 13.5 L10 16.5 Z`},
	{"files", `
		D: M2 6 Q2 4 4 4 H9 L11 7 H20 Q22 7 22 9 V18 Q22 20 20 20 H4 Q2 20 2 18 Z
		s: M2 11 H22`},
	{"packages", `
		d: M12 2.5 L21 7 L12 11.5 L3 7 Z
		s: M12 2.5 L21 7 V17 L12 21.5 L3 17 V7 Z
		s: M3 7 L12 11.5 L21 7 M12 11.5 V21.5
		s: M7.5 4.75 L16.5 9.25`},
	{"chat", `
		D: M4 4 H20 Q22 4 22 6 V15 Q22 17 20 17 H10 L5 21 V17 H4 Q2 17 2 15 V6 Q2 4 4 4 Z
		sw1.25: M8 10.5 M12 10.5 M16 10.5`},
	{"favorites", `
		D: T12 12.9 9.5 4.3 5 0`},
	{"apps", `
		D: R3 3 7 7 2 R3 14 7 7 2 R14 14 7 7 2
		D: O17.5 6.5 3.5`},
	{"settings", `
		De: A12 12 10 -100 -80 A12 12 7.4 -76 -59 A12 12 10 -55 -35 A12 12 7.4 -31 -14 A12 12 10 -10 10 A12 12 7.4 14 31 A12 12 10 35 55 A12 12 7.4 59 76 A12 12 10 80 100 A12 12 7.4 104 121 A12 12 10 125 145 A12 12 7.4 149 166 A12 12 10 170 190 A12 12 7.4 194 211 A12 12 10 215 235 A12 12 7.4 239 256 Z O12 12 3`},
	{"tools", `
		D: M4.72 16.81 L10.95 10.57 A15.5 8.5 5 -204.51 -68.58 L14.44 6.73 A15.85 8.15 2 -135 -315 L20.15 6.67 A15.5 8.5 5 -21.42 114.51 L7.19 19.28 A5.95 18.05 1.75 45 225 Z`},
	{"browser", `
		D: O12 12 10
		s: E12 12 4.5 10
		s: M2 12 H22`},
	{"emulator", `
		D: M7 6 H17 C20 6 21.6 8 22.1 11 L22.7 15.6 C23 18 21.6 19.6 19.8 19.6 C18.7 19.6 17.9 18.9 17.3 17.8 L16.4 16 H7.6 L6.7 17.8 C6.1 18.9 5.3 19.6 4.2 19.6 C2.4 19.6 1 18 1.3 15.6 L1.9 11 C2.4 8 4 6 7 6 Z
		s: M7.5 9.5 V13.5 M5.5 11.5 H9.5
		sw1.25: M16 10.5 M18.5 13`},
	{"gamepad", `
		s: M7 6 H17 C20 6 21.6 8 22.1 11 L22.7 15.6 C23 18 21.6 19.6 19.8 19.6 C18.7 19.6 17.9 18.9 17.3 17.8 L16.4 16 H7.6 L6.7 17.8 C6.1 18.9 5.3 19.6 4.2 19.6 C2.4 19.6 1 18 1.3 15.6 L1.9 11 C2.4 8 4 6 7 6 Z
		s: M7.5 9.5 V13.5 M5.5 11.5 H9.5
		sw1.25: M16 10.5 M18.5 13`},
	{"exit", `
		s: M9 21 H5 Q3 21 3 19 V5 Q3 3 5 3 H9
		s: M16 17 L21 12 L16 7 M21 12 H9`},
	{"play", `
		F: M7 4.5 L19 12 L7 19.5 Z`},
	{"pause", `
		F: R6 4.5 3 15 1 R15 4.5 3 15 1`},
	{"back", `
		s: M19 12 H5 M11 6 L5 12 L11 18`},
	{"search", `
		D: O11 11 7
		s: M16.5 16.5 L21 21`},
	{"mic", `
		D: R9 2 6 12 3
		s: A12 11 7 180 0
		s: M12 18 V22 M8.5 22 H15.5`},
	{"clock", `
		D: O12 12 10
		s: M12 6 V12 L16 14`},
	{"alarm", `
		D: O12 13 8
		s: M12 9 V13 L14.5 15
		s: M5 3 L2 6 M19 3 L22 6
		s: M6.5 19.5 L4.5 21.5 M17.5 19.5 L19.5 21.5`},
	{"music", `
		s: M9 18 V5 L21 3 V16
		D: O6 18 3 O18 16 3`},
	{"video", `
		D: R2 6 14 12 2.5
		D: M16 10.5 L22 7 V17 L16 13.5 Z`},
	{"image", `
		D: R3 3 18 18 3
		F: O9 9 1.5
		s: M21 15 L16 10 L5 21`},
	{"pdf", `
		D: M14 2 H6 Q4 2 4 4 V20 Q4 22 6 22 H18 Q20 22 20 20 V8 Z
		s: M14 2 V6 Q14 8 16 8 H20
		F: R2 12 13 7 1.5
		Kw.75: M5 15.5 H7 M9 15.5 H11 M13 15.5 H14`},
	{"text", `
		D: M14 2 H6 Q4 2 4 4 V20 Q4 22 6 22 H18 Q20 22 20 20 V8 Z
		s: M14 2 V6 Q14 8 16 8 H20
		s: M8 13 H16 M8 17 H13`},
	{"folder", `
		s: M2 6 Q2 4 4 4 H9 L11 7 H20 Q22 7 22 9 V18 Q22 20 20 20 H4 Q2 20 2 18 Z`},
	{"folder-up", `
		D: M2 6 Q2 4 4 4 H9 L11 7 H20 Q22 7 22 9 V18 Q22 20 20 20 H4 Q2 20 2 18 Z
		s: M12 17 V11 M9 14 L12 11 L15 14`},
	{"download", `
		s: M12 3 V15 M7 10 L12 15 L17 10
		s: M3 15 V19 Q3 21 5 21 H19 Q21 21 21 19 V15`},
	{"upload", `
		s: M12 15 V3 M7 8 L12 3 L17 8
		s: M3 15 V19 Q3 21 5 21 H19 Q21 21 21 19 V15`},
	{"trash", `
		s: M3 6 H21
		s: M8 6 V4 Q8 3 9 3 H15 Q16 3 16 4 V6
		D: M5 6 L6 20 Q6.1 21 7 21 H17 Q17.9 21 18 20 L19 6
		s: M10 11 V17 M14 11 V17`},
	{"star", `
		s: T12 12.9 9.5 4.3 5 0`},
	{"heart", `
		D: M12 20.5 C7 16.5 2 13.2 2 8.6 C2 5.6 4.3 3.5 7 3.5 C9 3.5 10.8 4.6 12 6.4 C13.2 4.6 15 3.5 17 3.5 C19.7 3.5 22 5.6 22 8.6 C22 13.2 17 16.5 12 20.5 Z`},
	{"share", `
		s: M8.6 13.5 L15.4 17.5 M15.4 6.5 L8.6 10.5
		D: O18 5 3 O6 12 3 O18 19 3`},
	{"copy", `
		D: R8 8 13 13 2.5
		s: M5 16 Q3 16 3 14 V5 Q3 3 5 3 H14 Q16 3 16 5`},
	{"paste", `
		D: M8 4 H6 Q4 4 4 6 V20 Q4 22 6 22 H18 Q20 22 20 20 V6 Q20 4 18 4 H16
		s: R8 2 8 4 1`},
	{"edit", `
		D: M3.5 20.5 L4.9 15.6 L14.1 6.4 A15.9 8.1 2.5 225 405 L8.4 19.1 Z
		s: M11.3 9.2 L14.8 12.7`},
	{"info", `
		D: O12 12 10
		s: M12 11 V16.5
		s: M12 7.5`},
	{"warning", `
		D: M10.3 4 Q12 1 13.7 4 L21.6 17.6 Q23.3 20.5 19.9 20.5 H4.1 Q0.7 20.5 2.4 17.6 Z
		s: M12 9 V13
		s: M12 16.5`},
	{"check", `
		s: M5 12.5 L10 17.5 L19 7`},
	{"close", `
		s: M6 6 L18 18 M18 6 L6 18`},
	{"plus", `
		s: M12 5 V19 M5 12 H19`},
	{"minus", `
		s: M5 12 H19`},
	{"refresh", `
		s: M3 12 A12 12 9 180 315 L21 8 M21 3 V8 H16
		s: M21 12 A12 12 9 0 135 L3 16 M3 21 V16 H8`},
	{"lock", `
		D: R4 11 16 10 2.5
		s: M8 11 V7.5 A12 7.5 4 180 360 V11
		s: M12 15 V17`},
	{"unlock", `
		D: R4 11 16 10 2.5
		s: M8 11 V7.5 A12 7.5 4 180 330
		s: M12 15 V17`},
	{"sun", `
		D: O12 12 4.5
		s: M12 4.5 L12 2 M17.3 6.7 L19.07 4.93 M19.5 12 L22 12 M17.3 17.3 L19.07 19.07 M12 19.5 L12 22 M6.7 17.3 L4.93 19.07 M4.5 12 L2 12 M6.7 6.7 L4.93 4.93`},
	{"moon", `
		D: A12 12 9 -92.08 -357.92 A18 6 7 64.68 205.32 Z`},
	{"battery", `
		D: R2 7 17 10 2.5
		s: M22 10.5 V13.5
		f: R5 10 8 4 1`},
	{"wifi", `
		s: U12 18 4 220 320 U12 18 8 220 320 U12 18 12 220 320
		s: M12 18`},
	{"wifi-off", `
		s: U12 18 4 220 320 U12 18 8 220 320 U12 18 12 220 320
		s: M12 18
		Kw3: M3 3 L21 21
		s: M3 3 L21 21`},
	{"bluetooth", `
		s: M7 7 L17 17 L12 22 V2 L17 7 L7 17`},
	{"globe", `
		s: O12 12 10
		s: E12 12 4.5 10
		s: M2 12 H22`},
	{"link", `
		s: M9 17 H7 A7 12 5 90 270 H9
		s: M15 7 H17 A17 12 5 -90 90 H15
		s: M8 12 H16`},
	{"home", `
		D: M3 10 L12 3 L21 10 V19 Q21 21 19 21 H5 Q3 21 3 19 Z
		s: M10 21 V15 H14 V21`},
	{"user", `
		D: O12 8 4
		D: M5 21 Q5 15 12 15 Q19 15 19 21`},
	{"users", `
		D: O9 8 4
		D: M2 21 Q2 15 9 15 Q16 15 16 21
		s: M16 4 Q19.5 4.5 19.5 8 Q19.5 11.5 16 12
		s: M18.5 15.5 Q22 16.5 22 21`},
	{"bell", `
		D: M5 17.5 Q6.5 16 6.5 13 V10 Q6.5 4.5 12 4.5 Q17.5 4.5 17.5 10 V13 Q17.5 16 19 17.5 Z
		s: M10 20.5 Q12 22 14 20.5
		s: M12 2.5 V4.5`},
	{"calendar", `
		D: R3 5 18 16 2.5
		s: M3 10 H21
		s: M8 3 V7 M16 3 V7
		s: M8 14 M12 14 M16 14 M8 17.5 M12 17.5`},
	{"camera", `
		D: M2 9 Q2 7 4 7 H7.5 L9 4.5 H15 L16.5 7 H20 Q22 7 22 9 V18 Q22 20 20 20 H4 Q2 20 2 18 Z
		s: O12 13 3.5`},
	{"grid", `
		s: R3 3 7 7 1.5 R14 3 7 7 1.5 R3 14 7 7 1.5 R14 14 7 7 1.5`},
	{"list", `
		s: M9 6 H21 M9 12 H21 M9 18 H21
		sw1.25: M4 6 M4 12 M4 18`},
	{"rows", `
		D: R3 4 18 7 2 R3 13 18 7 2`},
	{"filter", `
		D: M3 4 H21 L14 12.5 V19 L10 21 V12.5 Z`},
	{"sort", `
		s: M7 4 V20 M3 8 L7 4 L11 8
		s: M17 4 V20 M13 16 L17 20 L21 16`},
	{"keyboard", `
		D: R2 5 20 14 2.5
		s: M6 9 M10 9 M14 9 M18 9 M6 12.5 M10 12.5 M14 12.5 M18 12.5
		s: M8 16 H16`},
	{"language", `
		s: M2 5 H12 M7 2 V5
		s: M10 5 Q9 11 3 15
		s: M5 8.5 Q7.5 12 11 13.5
		s: M12 22 L16.5 11 L21 22 M13.6 18.5 H19.4`},
	{"sparkles", `
		D: M10 5 Q10 13 18 13 Q10 13 10 21 Q10 13 2 13 Q10 13 10 5 Z
		F: M18.5 2 Q18.5 5.5 22 5.5 Q18.5 5.5 18.5 9 Q18.5 5.5 15 5.5 Q18.5 5.5 18.5 2 Z`},
	{"trophy", `
		D: M7 3 H17 V10 Q17 15 12 15 Q7 15 7 10 Z
		s: M7 5 H5 Q3 5 3 7.5 Q3 10.5 7.2 11
		s: M17 5 H19 Q21 5 21 7.5 Q21 10.5 16.8 11
		s: M12 15 V18
		D: R8 18 8 3 1`},
	{"juka", `
		D: O12 12 10
		sw1.5: M9.5 6.5 H15.5 M14 6.5 V13 Q14 17.5 10.5 17.5 Q8.3 17.5 7.6 15.5`},

	// ── Tools ──────────────────────────────────────────────────────────
	{"terminal", `
		D: R2 4 20 16 2.5
		s: M6 9 L9.5 12 L6 15
		s: M12 15 H17`},
	{"schedule", `
		s: M20 9.5 V6.5 Q20 4.5 18 4.5 H5 Q3 4.5 3 6.5 V19 Q3 21 5 21 H9
		s: M8 2.5 V6.5 M15 2.5 V6.5 M3 10 H9
		D: O16 16 6
		s: M16 13.5 V16 L17.5 17.5`},
	{"startup", `
		D: M12 2 C15.5 4 17 8 17 12 V17 H7 V12 C7 8 8.5 4 12 2 Z
		s: O12 10 2
		s: M7 13 L4.5 15.5 V19 L7 17.5 M17 13 L19.5 15.5 V19 L17 17.5
		s: M10 20 Q12 23 14 20`},
	{"hardware", `
		D: R2 3 20 14 2.5
		s: M8 21 H16 M12 17 V21
		s: M5 10 H8 L10 6.5 L13 13.5 L15 10 H19`},
	{"disk", `
		D: M2 12 L5.5 5.1 Q6 4 7.3 4 H16.7 Q18 4 18.5 5.1 L22 12 V18 Q22 20 20 20 H4 Q2 20 2 18 Z
		s: M2 12 H22
		s: M6 16 M10 16`},
	{"benchmark", `
		s: M3 3 V18 Q3 21 6 21 H21
		D: R7 13 3 5 1 R12 9 3 9 1 R17 5 3 13 1`},
	{"logs", `
		D: R4 2 16 20 2.5
		s: M11 7 H16 M11 12 H16 M11 17 H16
		sw1.25: M8 7 M8 12 M8 17`},
	{"broom", `
		s: M21 3 L13.5 10.5
		D: M10.9 9.9 L14.1 13.1 L10.7 19.6 Q9.6 20.8 8.4 19.6 L4.4 15.6 Q3.2 14.4 4.4 13.3 Z
		s: M9.9 15.9 L8.2 17.6`},
	{"update", `
		D: O12 12 10
		s: M12 16.5 V7.5 M8 11.5 L12 7.5 L16 11.5`},
	{"power", `
		s: U12 13 8.5 -50 230
		s: M12 2.5 V12`},
	{"disc", `
		De: O12 12 10 O12 12 2.5
		s: U12 12 6.5 195 255`},
	{"calculator", `
		D: R4 2 16 20 2.5
		s: R7.5 5 9 4 1
		s: M8.5 13 M12 13 M15.5 13 M8.5 17.5 M12 17.5 M15.5 17.5`},
	{"bug", `
		D: M7 13 Q7 7.5 12 7.5 Q17 7.5 17 13 V15 Q17 21 12 21 Q7 21 7 15 Z
		s: M12 11 V21
		s: M9 7.8 Q9 4.5 12 4.5 Q15 4.5 15 7.8
		s: M7 11.5 L3.5 10 M7 15.5 H3 M7.5 18.5 L4 20.5 M17 11.5 L20.5 10 M17 15.5 H21 M16.5 18.5 L20 20.5
		s: M10 4.8 L8.5 2.5 M14 4.8 L15.5 2.5`},
	{"community", `
		D: M3 4.5 Q3 3 4.5 3 H13.5 Q15 3 15 4.5 V10.5 Q15 12 13.5 12 H7.5 L4 15 V12 Q3 12 3 10.5 Z
		s: M18 8 H19.5 Q21 8 21 9.5 V15.5 Q21 17 20 17 V20 L16.5 17 H10.5 Q9 17 9 15.5 V15`},
	{"network", `
		D: R9 2 6 5 1.5
		D: R2 17 6 5 1.5 R16 17 6 5 1.5
		s: M12 7 V12 M5 17 V14 Q5 12 7 12 H17 Q19 12 19 14 V17`},
	{"party", `
		D: M3 21 L7.5 9.5 L14.5 16.5 Z
		s: M14 3 Q13 5.5 15 6.5 Q17 7.5 16 10
		s: M17.5 13.5 L21.5 12.5 M15.5 8.5 L20 5.5
		s: M10.5 3.5 M20.5 8.5 M19.5 17`},
	{"toolbox", `
		D: R2 9 20 11 2.5
		s: M2 14 H22
		s: M8 9 V6.5 Q8 5 9.5 5 H14.5 Q16 5 16 6.5 V9
		s: M9 13 V15 M15 13 V15`},
	{"shield", `
		D: M12 2.5 L20 5.5 V11 Q20 17.5 12 21.5 Q4 17.5 4 11 V5.5 Z
		s: M8.5 12 L11 14.5 L15.5 9.5`},
	{"audit", `
		s: M10 21 H6 Q4 21 4 19 V5 Q4 3 6 3 H13.5 L18 7.5 V10
		s: M8 8 H12 M8 12 H10
		D: O15.5 15.5 3.5
		s: M18 18 L21 21`},
	{"server", `
		D: R3 3 18 8 2 R3 13 18 8 2
		sw1.25: M7 7 M7 17
		s: M11 7 H17 M11 17 H17`},
	{"key", `
		D: O7.5 15.5 4.5
		s: M10.7 12.3 L20.5 2.5
		s: M15.5 7.5 L18.5 10.5 M18 5 L20.5 7.5`},
	{"phone", `
		D: R6 2 12 20 2.5
		s: M10.5 18 H13.5`},
	{"radar", `
		s: O12 12 10
		s: O12 12 5.5
		d: M12 12 L12 2 A12 12 10 270 315 Z
		s: M12 12 L19.07 4.93
		sw1.25: M8.5 15.5 M15.5 15`},
	{"route", `
		D: O6 19 2.5 O18 5 2.5
		s: M8.5 19 H17 Q20.5 19 20.5 15.5 Q20.5 12 17 12 H7 Q3.5 12 3.5 8.5 Q3.5 5 7 5 H15.5`},
	{"dns", `
		D: M4 6 H17 L20.5 9.5 L17 13 H4 Q3 13 3 12 V7 Q3 6 4 6 Z
		s: M11 13 V21 M11 3 V6 M8 21 H14`},
	{"devices", `
		D: R2 4 15 11 2
		s: M5.5 19 H11.5 M8.5 15 V19
		Kw3: R15 9 7 12 1.5
		D: R15 9 7 12 1.5`},
	{"public-ip", `
		s: O11 13 8
		s: E11 13 3.5 8
		s: M3 13 H19
		Kw3: M14 10 L21 3
		s: M15 3 H21 V9 M21 3 L14 10`},
	{"plug", `
		D: M6.5 9 H17.5 V12 Q17.5 17 12 17 Q6.5 17 6.5 12 Z
		s: M9 3 V9 M15 3 V9
		s: M12 17 V21`},
	{"id-card", `
		D: R2 5 20 14 2.5
		s: O8 10.5 2
		s: M5 15.5 Q5 13.5 8 13.5 Q11 13.5 11 15.5
		s: M14 10 H18.5 M14 14 H17`},
	{"code", `
		s: M16 18 L22 12 L16 6 M8 6 L2 12 L8 18
		s: M14 4 L10 20`},
	{"clock-check", `
		s: U12 12 10 100 360
		s: M12 6 V12 L15.5 14
		s: M14 19 L16.5 21.5 L22 16`},
	{"subnet", `
		s: M11 13 V4 A11 13 9 -90 -360 Z
		D: M13 11 V2 A13 11 9 -90 0 Z`},
	{"speed", `
		D: M3.34 19 A12 14 10 150 390 Z
		s: M12 14 L16.6 9.4
		sw1.5: M12 14`},
	{"qr", `
		s: R3 3 7 7 1.5 R14 3 7 7 1.5 R3 14 7 7 1.5
		sw1.25: M6.5 6.5 M17.5 6.5 M6.5 17.5
		f: R14 14 3 3 0.75 R18 18 3 3 0.75 R18 14 3 3 0.75 R14 18 3 3 0.75`},
	{"cpu", `
		D: R6 6 12 12 2
		s: R9.5 9.5 5 5 1
		s: M9 2 V6 M15 2 V6 M9 18 V22 M15 18 V22 M2 9 H6 M2 15 H6 M18 9 H22 M18 15 H22`},
	{"memory", `
		D: R2 6 20 10 1.5
		f: R5 9 3 4 0.5 R10.5 9 3 4 0.5 R16 9 3 4 0.5
		s: M6 16 V19 M10 16 V19 M14 16 V19 M18 16 V19`},
	{"thermometer", `
		D: M10 13.54 V5 A12 5 2 180 360 V13.54 A12 17 4 -60 240 Z
		f: O12 17 2
		s: M12 15 V9`},
	{"plugin", `
		Dn: M4 7 H9 A11 5.5 2.5 143.13 396.87 H18 V12 A19.5 14 2.5 233.13 486.87 V21 H4 Z`},
	{"theme", `
		s: O12 12 10
		f: M12 2 A12 12 10 270 450 Z`},
	{"palette", `
		D: M12 2 Q22 2 22 11 Q22 15 18 15 H16 Q14 15 14 17 Q14 18.5 15 19.5 Q16 21 13.5 22 Q12.5 22.2 12 22 Q2 22 2 12 Q2 2 12 2 Z
		sw1.25: M7.5 10.5 M10.5 6.5 M15.5 6.5 M18 10.5`},
	{"backup", `
		s: M3 12 A12 12 9 180 -135 L3 8 M3 3 V8 H8
		s: M12 7 V12 L15.5 14`},
	{"sync", `
		s: M3 8 H18 M15 4 L19 8 L15 12
		s: M21 16 H6 M9 12 L5 16 L9 20`},
	{"cloud", `
		D: M7 19 H17.5 Q22 19 22 14.5 Q22 10.2 17.8 10 Q17 5 12 5 Q7.6 5 6.5 9.5 Q2 10 2 14.5 Q2 19 7 19 Z`},
	{"rom", `
		D: M6 3 H18 Q20 3 20 5 V18 H18 V21 H6 V18 H4 V5 Q4 3 6 3 Z
		s: R7.5 6 9 7 1
		s: M9 21 V19 M12 21 V19 M15 21 V19`},
	{"scan", `
		s: M3 8 V5 Q3 3 5 3 H8 M16 3 H19 Q21 3 21 5 V8 M21 16 V19 Q21 21 19 21 H16 M8 21 H5 Q3 21 3 19 V16
		s: M7 12 H17`},
	{"tv", `
		D: R2 7 20 14 2.5
		s: M7 2.5 L12 7 L17 2.5`},

	// ── Emulators and systems ──────────────────────────────────────────
	// Original silhouettes of each system's hardware: what the console,
	// controller or handheld looks like, never a maker's logo.
	{"emu-retroarch", `
		F: M7 6 H17 V8 H19 V11 H21 V16 H19 V13 H17 V16 H7 V13 H5 V16 H3 V11 H5 V8 H7 Z
		k: R8 9.5 2 2 0 R14 9.5 2 2 0
		s: M8.5 6 L7 3.5 M15.5 6 L17 3.5
		s: M9.5 16 L8.5 19.5 M14.5 16 L15.5 19.5`},
	{"emu-nes", `
		D: R1.5 7 21 10 1.5
		s: M6.5 10 V14 M4.5 12 H8.5
		s: M10.5 13 H11.5 M12.5 13 H13.5
		sw1.5: M16.5 13 M19.5 13`},
	{"emu-snes", `
		D: R1 7 22 10 5
		s: M6 10 V14 M4 12 H8
		sw1.25: M18 9.5 M18 14.5 M15.5 12 M20.5 12`},
	{"emu-n64", `
		D: M5 6 H19 Q22.5 6 22.5 10 V16 Q22.5 18.5 20.5 18.5 Q18.5 18.5 18 16.5 L17 13 H15 V18 Q15 20.5 12 20.5 Q9 20.5 9 18 V13 H7 L6 16.5 Q5.5 18.5 3.5 18.5 Q1.5 18.5 1.5 16 V10 Q1.5 6 5 6 Z
		s: M5 8.5 V11.5 M3.5 10 H6.5
		sw1.25: M18 9 M20 10.5
		s: O12 14.5 1.5`},
	{"emu-gb", `
		D: M5 2 H19 Q20 2 20 3 V18 Q20 22 16 22 H5 Q4 22 4 21 V3 Q4 2 5 2 Z
		s: R6.5 4.5 11 8 1
		s: M8 15.5 V18.5 M6.5 17 H9.5
		sw1.25: M14 18 M16.5 16.5`},
	{"emu-gbc", `
		D: M5.5 2 H18.5 Q20 2 20 3.5 V17 Q20 22 12 22 Q4 22 4 17 V3.5 Q4 2 5.5 2 Z
		D: R6.5 4.5 11 8 1.5
		s: M8 15 V18 M6.5 16.5 H9.5
		sw1.25: M14 17 M16.5 15.5`},
	{"emu-gba", `
		D: M5 6 H19 Q23 6 23 12 Q23 18 19 18 H5 Q1 18 1 12 Q1 6 5 6 Z
		s: R7.5 8.5 9 7 1
		s: M4.5 10.5 V13.5 M3 12 H6
		sw1.25: M18.5 13 M20.5 11`},
	{"emu-nds", `
		D: R4 2 16 9.5 2
		D: R4 12.5 16 9.5 2
		s: R7 4.5 10 5 0.5
		s: R7 15 10 5 0.5`},
	{"emu-3ds", `
		D: R3 2 18 9.5 2
		D: R3 12.5 18 9.5 2
		s: R5.5 4.5 13 5 0.5
		s: R9 15 6 5 0.5
		sw1.25: M6 15.5 M18 18`},
	{"emu-gc", `
		s: M6 9.5 V5 Q6 3 8 3 H16 Q18 3 18 5 V9.5
		Kw3: M12 5.5 L20.5 9.75 L12 14 L3.5 9.75 Z
		d: M12 5.5 L20.5 9.75 L12 14 L3.5 9.75 Z
		s: M12 5.5 L20.5 9.75 V18.25 L12 22.5 L3.5 18.25 V9.75 Z
		s: M3.5 9.75 L12 14 L20.5 9.75 M12 14 V22.5
		s: E12 9.75 3 1.5`},
	{"emu-wii", `
		D: R8.5 2 7 20 3.5
		s: M12 4.5 V8.5 M10 6.5 H14
		s: O12 12 1.5
		sw1.25: M12 16.5 M12 19`},
	{"emu-wiiu", `
		D: M4 6 H20 Q23 6 23 9.5 V14.5 Q23 18 20 18 H4 Q1 18 1 14.5 V9.5 Q1 6 4 6 Z
		s: R6.5 8 11 8 1
		sw1.25: M3.8 9.8 M20.2 9.8 M3.8 14 M20.2 14`},
	{"emu-switch", `
		D: M6 4 H7.5 V20 H6 Q2 20 2 16 V8 Q2 4 6 4 Z
		D: M16.5 4 H18 Q22 4 22 8 V16 Q22 20 18 20 H16.5 Z
		s: R9 4 6 16 0.5
		sw1.25: M4.8 8.5 M4.8 14.5 M19.2 9.5 M19.2 15.5`},
	{"emu-ps1", `
		fe: O12 12 10 O12 12 3.5
		s: O12 12 2
		Kw.75: U12 12 7 200 250`},
	{"emu-ps2", `
		D: R8 2 8 17 1.5
		s: M10.5 5 H13.5 M10.5 7.5 H13.5 M10.5 10 H13.5
		s: M6 21.5 H18 M9.5 19 V21.5 M14.5 19 V21.5`},
	{"emu-ps3", `
		D: M2 18 V11.5 Q12 3.5 22 11.5 V18 Q22 19.5 20.5 19.5 H3.5 Q2 19.5 2 18 Z
		s: M5 15.5 H13
		sw1.25: M17 15.5 M19.5 15.5`},
	{"emu-psp", `
		D: M5 6.5 H19 Q23 6.5 23 12 Q23 17.5 19 17.5 H5 Q1 17.5 1 12 Q1 6.5 5 6.5 Z
		s: R7 8.5 10 7 0.5
		s: M4 10.75 V13.25 M2.75 12 H5.25
		sw1.25: M20 10.75 M20 13.25`},
	{"emu-vita", `
		D: E12 12 11 6.5
		s: R6.5 8.5 11 7 1
		sw1.25: M3.6 10.5 M20.4 10.5
		s: O4 14.5 1 O20 14.5 1`},
	{"emu-genesis", `
		D: M6 6 H18 Q23 6 23 11.5 Q23 18 18.5 18 Q16 18 14.5 16 H9.5 Q8 18 5.5 18 Q1 18 1 11.5 Q1 6 6 6 Z
		s: M6 10 V14 M4 12 H8
		sw1.25: M14.5 13 M17 12 M19.5 11`},
	{"emu-sms", `
		D: M2 18 L4.5 10 H19.5 L22 18 Z
		s: M8 10 V7 H16 V10
		s: M6 15 H11 M14 15 H18`},
	{"emu-gg", `
		D: R1 5 22 14 3.5
		s: R7 7.5 10 9 1
		s: M4 10.5 V13.5 M2.5 12 H5.5
		sw1.25: M19 13 M21 11`},
	{"emu-dc", `
		D: R3 3 18 18 3.5
		s: O12 11 5
		s: M6.5 18 V20 M10 18 V20 M14 18 V20 M17.5 18 V20`},
	{"emu-saturn", `
		D: M5 6.5 H19 Q23 6.5 23 12 Q23 17.5 18.5 17.5 Q16.5 17.5 15.5 16 H8.5 Q7.5 17.5 5.5 17.5 Q1 17.5 1 12 Q1 6.5 5 6.5 Z
		s: M6 10 V14 M4 12 H8
		s: M14.5 10.5 M17 10 M19.5 9.5 M14.5 13.5 M17 13 M19.5 12.5`},
	{"emu-xbox", `
		D: O12 12 10
		sw1.25: M7.5 7.5 Q12 10.5 16.5 16.5 M16.5 7.5 Q12 10.5 7.5 16.5`},
	{"emu-arcade", `
		D: M7 2 H17 Q18 2 18 3 V10 L20 13.5 V21 Q20 22 19 22 H5 Q4 22 4 21 V13.5 L6 10 V3 Q6 2 7 2 Z
		s: R8 4.5 8 5.5 0.5
		s: M5 13.5 H19
		s: M9 13.5 V11.5
		sw1.25: M13.5 12 M16 12`},
	{"emu-pce", `
		D: R2 5 20 14 2.5
		s: M6 12 H12.5 M10 9.5 L12.5 12 L10 14.5
		sw.75: M17 8.5 H19.5 M17 11 H19.5 M17 13.5 H19.5 M17 16 H19.5`},
	{"emu-neogeo", `
		D: R1.5 10 21 9 2
		s: M6.5 10 V6.5
		F: O6.5 5 2
		sw1.25: M12.5 15 M15 14 M17.5 13.5 M20 14`},
	{"emu-dos", `
		D: M5 3 H16.5 L21 7.5 V19 Q21 21 19 21 H5 Q3 21 3 19 V5 Q3 3 5 3 Z
		s: M8 3 V8 H15 V3
		s: R6.5 13 11 8 1`},
	{"emu-scummvm", `
		D: M4 3 V18.5 L8 14.5 L10.5 20.5 L13 19.5 L10.5 13.5 H16 Z
		D: M15 3 H20 Q22 3 22 5 V8 Q22 10 20 10 H18 L16 12 V10 Q15 10 15 8 Z`},
	{"emu-atari", `
		D: R4 13 16 8 2
		s: M12 13 V6.5
		F: O12 5 2
		F: R6 11 3 2 0.5`},
	{"emu-generic", `
		D: M6 3 H18 Q20 3 20 5 V18 H18 V21 H6 V18 H4 V5 Q4 3 6 3 Z
		s: R7.5 6 9 8 1
		s: M10 8.5 V11.5 M8.5 10 H11.5
		sw1.25: M14.5 10`},

	// ── Fallback ───────────────────────────────────────────────────────
	{"placeholder", `
		D: R4 4 16 16 4
		s: O12 12 3`},
}

// iconAliasPairs maps legacy, plural and descriptive names onto canonical
// icons, so an older config or a caller's own vocabulary still resolves. The
// first group is the launcher's old drawTileIcon kinds. No alias is a single
// letter: a one-letter icon field is a monogram.
var iconAliasPairs = [...][2]string{
	{"box", "packages"},
	{"chart", "benchmark"},
	{"gear", "settings"},
	{"chip", "cpu"},
	{"game", "gamepad"},
	{"resume", "continue"},
	{"cache", "broom"},

	{"emulators", "gamepad"},
	{"controller", "gamepad"},
	{"joystick", "gamepad"},
	{"games", "gamepad"},
	{"tube", "media"},
	{"youtube", "media"},
	{"videos", "media"},
	{"live", "tv"},
	{"livetv", "tv"},
	{"stream", "tv"},
	{"iptv", "tv"},
	{"cog", "settings"},
	{"preferences", "settings"},
	{"wrench", "tools"},
	{"misc", "tools"},
	{"store", "packages"},
	{"package", "packages"},
	{"messages", "chat"},
	{"message", "chat"},
	{"discord", "chat"},
	{"web", "browser"},
	{"internet", "browser"},
	{"explorer", "files"},
	{"file", "text"},
	{"document", "text"},
	{"doc", "text"},
	{"txt", "text"},
	{"folder-open", "folder"},
	{"directory", "folder"},
	{"parent", "folder-up"},
	{"up", "folder-up"},
	{"photo", "image"},
	{"picture", "image"},
	{"film", "video"},
	{"movie", "video"},
	{"song", "music"},
	{"audio", "music"},
	{"headphones", "music"},
	{"podcast", "mic"},
	{"podcasts", "mic"},
	{"microphone", "mic"},
	{"voice", "mic"},
	{"favourites", "favorites"},
	{"favorite", "favorites"},
	{"saved", "favorites"},
	{"history", "backup"},
	{"recent", "continue"},
	{"quit", "exit"},
	{"logout", "exit"},
	{"log-out", "exit"},
	{"shutdown", "power"},
	{"power-off", "power"},
	{"reboot", "refresh"},
	{"reload", "refresh"},
	{"retry", "refresh"},
	{"add", "plus"},
	{"new", "plus"},
	{"remove", "minus"},
	{"delete", "trash"},
	{"bin", "trash"},
	{"rename", "edit"},
	{"pencil", "edit"},
	{"find", "search"},
	{"magnifier", "search"},
	{"cancel", "close"},
	{"done", "check"},
	{"ok", "check"},
	{"alert", "warning"},
	{"error", "warning"},
	{"about", "info"},
	{"help", "info"},
	{"notifications", "bell"},
	{"notification", "bell"},
	{"date", "calendar"},
	{"timer", "clock"},
	{"time", "clock"},
	{"person", "user"},
	{"profile", "user"},
	{"account", "user"},
	{"people", "users"},
	{"friends", "users"},
	{"social", "users"},
	{"night", "moon"},
	{"dark", "moon"},
	{"brightness", "sun"},
	{"light", "sun"},
	{"weather", "cloud"},
	{"translate", "language"},
	{"languages", "language"},
	{"ai", "sparkles"},
	{"magic", "sparkles"},
	{"achievements", "trophy"},
	{"achievement", "trophy"},
	{"trophies", "trophy"},
	{"playtime", "trophy"},
	{"jukahub", "juka"},
	{"jukaland", "juka"},
	{"view-grid", "grid"},
	{"view-list", "list"},
	{"console", "terminal"},
	{"shell", "terminal"},
	{"ssh", "terminal"},
	{"cron", "schedule"},
	{"tasks", "schedule"},
	{"scheduler", "schedule"},
	{"autostart", "startup"},
	{"launch", "startup"},
	{"rocket", "startup"},
	{"monitor", "hardware"},
	{"system", "hardware"},
	{"sysinfo", "hardware"},
	{"storage", "disk"},
	{"drive", "disk"},
	{"hdd", "disk"},
	{"diskspace", "disk"},
	{"bench", "benchmark"},
	{"score", "benchmark"},
	{"log", "logs"},
	{"clean", "broom"},
	{"cleanup", "broom"},
	{"patch", "update"},
	{"upgrade", "update"},
	{"updates", "update"},
	{"cd", "disc"},
	{"dvd", "disc"},
	{"iso", "disc"},
	{"calc", "calculator"},
	{"report", "bug"},
	{"debug", "bug"},
	{"support", "community"},
	{"forum", "community"},
	{"net", "network"},
	{"lan", "network"},
	{"nettools", "network"},
	{"watch-party", "party"},
	{"watchparty", "party"},
	{"security", "shield"},
	{"trust", "shield"},
	{"privacy", "shield"},
	{"ftp", "server"},
	{"sftp", "server"},
	{"password", "key"},
	{"companion", "phone"},
	{"mobile", "phone"},
	{"ping", "radar"},
	{"traceroute", "route"},
	{"ip", "public-ip"},
	{"whois", "id-card"},
	{"html", "code"},
	{"codec", "code"},
	{"ntp", "clock-check"},
	{"bandwidth", "speed"},
	{"netspeed", "speed"},
	{"latency", "speed"},
	{"qrcode", "qr"},
	{"processor", "cpu"},
	{"ram", "memory"},
	{"temperature", "thermometer"},
	{"temp", "thermometer"},
	{"plugins", "plugin"},
	{"extension", "plugin"},
	{"addon", "plugin"},
	{"themes", "theme"},
	{"appearance", "palette"},
	{"colors", "palette"},
	{"colours", "palette"},
	{"restore", "backup"},
	{"savestate", "backup"},
	{"saves", "backup"},
	{"transfer", "sync"},
	{"handoff", "sync"},
	{"roms", "rom"},
	{"cartridge", "rom"},

	// Systems by their short names, so a config or a ROM folder name can ask
	// for a console glyph directly.
	{"retroarch", "emu-retroarch"},
	{"libretro", "emu-retroarch"},
	{"nes", "emu-nes"},
	{"famicom", "emu-nes"},
	{"fc", "emu-nes"},
	{"snes", "emu-snes"},
	{"sfc", "emu-snes"},
	{"n64", "emu-n64"},
	{"gb", "emu-gb"},
	{"gameboy", "emu-gb"},
	{"gbc", "emu-gbc"},
	{"gba", "emu-gba"},
	{"nds", "emu-nds"},
	{"ds", "emu-nds"},
	{"3ds", "emu-3ds"},
	{"gc", "emu-gc"},
	{"ngc", "emu-gc"},
	{"gamecube", "emu-gc"},
	{"wii", "emu-wii"},
	{"wiiu", "emu-wiiu"},
	{"switch", "emu-switch"},
	{"ps", "emu-ps1"},
	{"ps1", "emu-ps1"},
	{"psx", "emu-ps1"},
	{"playstation", "emu-ps1"},
	{"ps2", "emu-ps2"},
	{"ps3", "emu-ps3"},
	{"psp", "emu-psp"},
	{"vita", "emu-vita"},
	{"psvita", "emu-vita"},
	{"genesis", "emu-genesis"},
	{"megadrive", "emu-genesis"},
	{"md", "emu-genesis"},
	{"sms", "emu-sms"},
	{"mastersystem", "emu-sms"},
	{"gg", "emu-gg"},
	{"gamegear", "emu-gg"},
	{"dc", "emu-dc"},
	{"dreamcast", "emu-dc"},
	{"saturn", "emu-saturn"},
	{"xbox", "emu-xbox"},
	{"arcade", "emu-arcade"},
	{"mame", "emu-arcade"},
	{"fbneo", "emu-arcade"},
	{"pce", "emu-pce"},
	{"pcengine", "emu-pce"},
	{"tg16", "emu-pce"},
	{"neogeo", "emu-neogeo"},
	{"dos", "emu-dos"},
	{"dosbox", "emu-dos"},
	{"scummvm", "emu-scummvm"},
	{"atari", "emu-atari"},
	{"atari2600", "emu-atari"},
}
