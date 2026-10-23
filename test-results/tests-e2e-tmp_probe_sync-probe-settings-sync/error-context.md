# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: tests\e2e\tmp_probe_sync.spec.js >> probe settings sync
- Location: tests\e2e\tmp_probe_sync.spec.js:7:0

# Error details

```
Error: goto: Protocol error (Page.navigate): Cannot navigate to invalid URL
Call log:
  - navigating to "/index.html", waiting until "load"

```

# Test source

```ts
  1  | import { test } from "@playwright/test";
  2  | import { readFileSync } from "node:fs";
  3  | 
  4  | // Probe: jukaconfig.json ↔ builder settings round-trip. Confirms the import
  5  | // fills the settings controls with the device-resolved values, that the theme
  6  | // preset palette paints $refs in edit mode, and that edits land in the export.
  7  | test("probe settings sync", async ({ page }) => {
  8  |   page.on("pageerror", e => console.log("PAGEERROR:", e.message));
> 9  |   await page.goto("/index.html");
     |             ^ Error: goto: Protocol error (Page.navigate): Cannot navigate to invalid URL
  10 |   await page.waitForFunction(() => typeof buildEditorConfig === "function");
  11 |   const parsed = JSON.parse(readFileSync("../../player/jukaconfig.json", "utf8"));
  12 |   await page.evaluate(cfg => loadJukaApp(cfg), parsed);
  13 | 
  14 |   const before = await page.evaluate(() => ({
  15 |     fullscreen: document.getElementById("fullscreenToggle").checked,
  16 |     screenW: document.getElementById("screenWidthInput").value,
  17 |     screenH: document.getElementById("screenHeightInput").value,
  18 |     toolsPath: document.getElementById("toolsPath").value,
  19 |     fileExplorerRoot: document.getElementById("fileExplorerRoot").value,
  20 |     weatherUnitValue: document.getElementById("weatherUnit").value,
  21 |     buttonColor: document.getElementById("buttonColorPicker").value,
  22 |     labelColor: document.getElementById("labelColorPicker").value,
  23 |     titleFontFile: document.getElementById("titleFontFile").value,
  24 |     mediumFontFile: document.getElementById("mediumFontFile").value,
  25 |     effectiveButton: effectiveThemeColour("buttoncolor"),
  26 |     effectiveFull: effectiveConfigValue("fullscreen"),
  27 |     effectiveW: effectiveConfigValue("screenWidth"),
  28 |     panelKind: variables.Custom ? Object.keys(variables.Custom).length : -1,
  29 |   }));
  30 |   console.log("BEFORE:", JSON.stringify(before));
  31 | 
  32 |   // A $buttonColor element on the canvas must be painted with the resolved value.
  33 |   const painted = await page.evaluate(() => {
  34 |     const hit = [...document.querySelectorAll("#canvas .element")].find(el =>
  35 |       (el.getAttribute("data-bg-color") || "").trim().startsWith("$") ||
  36 |       (el.getAttribute("data-color") || "").trim().startsWith("$"));
  37 |     return hit ? { attr: hit.getAttribute("data-bg-color") || hit.getAttribute("data-color"),
  38 |       bg: hit.style.backgroundColor, color: hit.style.color } : null;
  39 |   });
  40 |   console.log("PAINTED:", JSON.stringify(painted));
  41 | 
  42 |   // Change a control; the write must land in the panel variables, not the
  43 |   // import snapshot, and mask nothing on export.
  44 |   await page.evaluate(() => {
  45 |     document.getElementById("weatherUnit").value = "F";
  46 |     document.getElementById("weatherUnit").dispatchEvent(new Event("change"));
  47 |     document.getElementById("toolsPath").value = "./custom-tools";
  48 |     document.getElementById("toolsPath").dispatchEvent(new Event("input"));
  49 |     const t = document.getElementById("fullscreenToggle");
  50 |     t.checked = true;
  51 |     t.dispatchEvent(new Event("change"));
  52 |   });
  53 |   const after = await page.evaluate(() => {
  54 |     const exported = buildEditorConfig();
  55 |     return {
  56 |       panelVars: {
  57 |         weatherUnit: variables.weatherUnit, tools_path: variables.tools_path,
  58 |         fullscreen: variables.fullscreen,
  59 |       },
  60 |       customCleanup: {
  61 |         WeatherUnit: variables.Custom && variables.Custom.WeatherUnit,
  62 |         weatherUnit: variables.Custom && variables.Custom.weatherUnit,
  63 |         Tools_path: variables.Custom && variables.Custom.Tools_path,
  64 |         Fullscreen: variables.Custom && variables.Custom.Fullscreen,
  65 |       },
  66 |       exported: {
  67 |         weatherUnit: exported.variables.weatherUnit,
  68 |         tools_path: exported.variables.tools_path,
  69 |         fullscreen: exported.variables.fullscreen,
  70 |         customWeather: exported.variables.Custom && exported.variables.Custom.WeatherUnit,
  71 |       },
  72 |     };
  73 |   });
  74 |   console.log("AFTER:", JSON.stringify(after));
  75 | });
  76 | 
```