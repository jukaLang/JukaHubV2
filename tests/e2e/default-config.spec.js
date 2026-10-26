import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

// The website must show the same design the player ships. This loads the real
// player/jukaconfig.json and checks every scene: same element count and types,
// same position and size, nothing outside the 1280x720 device, and every
// scene-change trigger pointing at a scene that exists.

test("the shipped jukaconfig.json renders every element where the config says", async ({ page }) => {
  page.on("pageerror", (error) => {
    throw new Error(`uncaught page error: ${error.message}`);
  });
  const config = JSON.parse(await readFile(new URL("../../player/jukaconfig.json", import.meta.url), "utf8"));

  await page.goto("/index.html");
  await page.waitForFunction(() => typeof loadJukaApp === "function");
  await page.evaluate((c) => loadJukaApp(c), config);

  const canvas = await page.evaluate(() => {
    const el = document.getElementById("canvas");
    return { w: parseFloat(el.style.width), h: parseFloat(el.style.height) };
  });
  expect(canvas).toEqual({ w: config.Width, h: config.Height });

  const sceneNames = new Set(config.scenes.map((s) => s.name));
  const problems = [];
  for (const scene of config.scenes) {
    await page.evaluate((name) => {
      const select = document.getElementById("sceneSelector");
      select.value = name;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    }, scene.name);
    const dom = await page.evaluate(() =>
      [...document.querySelectorAll("#canvas .element")].map((e) => ({
        type: e.dataset.type,
        left: parseFloat(e.style.left),
        top: parseFloat(e.style.top),
        width: parseFloat(e.style.width),
        height: parseFloat(e.style.height),
      })),
    );
    const elements = scene.elements || [];
    if (dom.length !== elements.length) {
      problems.push(`${scene.name}: config has ${elements.length} elements, canvas shows ${dom.length}`);
      continue;
    }
    elements.forEach((el, i) => {
      const got = dom[i];
      const label = `${scene.name}[${i}] ${el.type} "${el.text || ""}"`;
      if (got.type !== el.type) problems.push(`${label}: rendered as ${got.type}`);
      if (Math.abs(got.left - +el.x) > 1 || Math.abs(got.top - +el.y) > 1) {
        problems.push(`${label}: at (${got.left},${got.top}), config says (${el.x},${el.y})`);
      }
      const w = +el.width;
      const h = +el.height;
      if ((w > 0 && Math.abs(got.width - w) > 1) || (h > 0 && Math.abs(got.height - h) > 1)) {
        problems.push(`${label}: ${got.width}x${got.height}, config says ${w}x${h}`);
      }
      if (+el.x + (w || 0) > config.Width + 0.5 || +el.y + (h || 0) > config.Height + 0.5) {
        problems.push(`${label}: runs off the ${config.Width}x${config.Height} device`);
      }
      const target = /^change_scene:(.+)$/.exec(el.trigger || "");
      if (target && !sceneNames.has(target[1])) problems.push(`${label}: goes to missing scene "${target[1]}"`);
    });
  }
  expect(problems).toEqual([]);
});

// Loading the shipped config and exporting it again must not drop anything the
// player reads. Empty defaults are omitted on export and numbers may be written
// as numbers instead of numeric strings; any other difference is data loss.
test("exporting the shipped jukaconfig.json keeps every meaningful value", async ({ page }) => {
  page.on("pageerror", (error) => {
    throw new Error(`uncaught page error: ${error.message}`);
  });
  const config = JSON.parse(await readFile(new URL("../../player/jukaconfig.json", import.meta.url), "utf8"));

  await page.goto("/index.html");
  await page.waitForFunction(() => typeof loadJukaApp === "function");
  await page.evaluate((c) => loadJukaApp(c), config);
  const exported = await page.evaluate(() => JSON.parse(JSON.stringify(buildEditorConfig())));

  const empty = (v) => v === "" || v === null || v === false || (Array.isArray(v) && v.length === 0);
  const problems = [];
  for (const key of Object.keys(config)) {
    if (key !== "scenes" && !(key in exported)) problems.push(`top-level ${key} missing`);
  }
  const byName = Object.fromEntries(exported.scenes.map((s) => [s.name, s]));
  for (const scene of config.scenes) {
    const out = byName[scene.name];
    if (!out) { problems.push(`scene ${scene.name} missing`); continue; }
    if ((out.elements || []).length !== (scene.elements || []).length) {
      problems.push(`scene ${scene.name}: ${(scene.elements || []).length} elements became ${(out.elements || []).length}`);
      continue;
    }
    (scene.elements || []).forEach((el, i) => {
      for (const [key, value] of Object.entries(el)) {
        const got = out.elements[i][key];
        if (got === undefined) {
          if (!empty(value)) problems.push(`${scene.name}#${i} ${el.type}.${key}=${JSON.stringify(value)} was dropped`);
        } else if (String(value) !== String(got) && JSON.stringify(value) !== JSON.stringify(got)) {
          problems.push(`${scene.name}#${i} ${el.type}.${key}: ${JSON.stringify(value)} became ${JSON.stringify(got)}`);
        }
      }
    });
  }
  expect(problems).toEqual([]);
});
