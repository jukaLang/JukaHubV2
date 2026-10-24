import { expect, test } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";

// A design round trips through the builder's own JSON export and import. The
// test uses the real drop handler and the real file input, not internal
// functions, so a regression in either path fails here.

// Only the parts of the config this test authored. Raw deep equality would be
// brittle: the importer fills defaults and drops values the player derives.
function designShape(config) {
  return {
    width: config.Width,
    height: config.Height,
    title: config.title,
    scenes: (config.scenes || []).map((scene) => ({
      name: scene.name,
      elements: (scene.elements || []).map((el) => ({
        type: el.type,
        x: el.x,
        y: el.y,
        width: el.width,
        height: el.height,
      })),
    })),
  };
}

const canvasElements = () => document.querySelectorAll("#canvas .element").length;

test.beforeEach(async ({ page }) => {
  // The builder loads fonts and icons from CDNs; a blocked font must not turn
  // the page load into a timeout, so fail only on the app's own scripts.
  page.on("pageerror", (error) => {
    throw new Error(`uncaught page error: ${error.message}`);
  });
  await page.goto("/index.html");
  await page.waitForSelector('#canvas');
  await page.waitForFunction(() => typeof buildEditorConfig === "function");
});

test("adds an element by dropping a palette component on the canvas", async ({ page }) => {
  const before = await page.evaluate(canvasElements);

  await page.evaluate(() => {
    const source = document.querySelector('.left-sidebar .element[data-type="button"]');
    const canvas = document.getElementById("canvas");
    const rect = canvas.getBoundingClientRect();
    const data = new DataTransfer();
    source.dispatchEvent(new DragEvent("dragstart", { bubbles: true, cancelable: true, dataTransfer: data }));
    canvas.dispatchEvent(
      new DragEvent("drop", {
        bubbles: true,
        cancelable: true,
        dataTransfer: data,
        clientX: rect.left + 160,
        clientY: rect.top + 120,
      })
    );
  });

  await expect.poll(() => page.evaluate(canvasElements)).toBe(before + 1);
  const config = await page.evaluate(() => buildEditorConfig());
  const element = config.scenes[0].elements.at(-1);
  expect(element.type).toBe("button");
  expect(element.width).toBeGreaterThan(0);
  expect(element.height).toBeGreaterThan(0);
});

test("undo removes the element and redo puts it back", async ({ page }) => {
  const before = await page.evaluate(canvasElements);
  await page.evaluate(() => addElement("label", 200, 200));
  await expect.poll(() => page.evaluate(canvasElements)).toBe(before + 1);

  await page.locator("#undoBtn").click();
  await expect.poll(() => page.evaluate(canvasElements)).toBe(before);

  await page.locator("#redoBtn").click();
  await expect.poll(() => page.evaluate(canvasElements)).toBe(before + 1);
});

test("the fitted canvas keeps pointer coordinates on the device", async ({ page }) => {
  // The canvas is scaled down to fit the pane. A regression here makes every
  // drop and drag land at the wrong spot, which is easy to miss by eye.
  const start = await page.evaluate(() => {
    const canvas = document.getElementById("canvas").getBoundingClientRect();
    const host = document.getElementById("canvasWrapper").getBoundingClientRect();
    return {
      scale: canvasScale,
      readout: document.getElementById("zoomReadout").textContent,
      fits: canvas.left >= host.left - 1 && canvas.right <= host.right + 1 &&
        canvas.top >= host.top - 1 && canvas.bottom <= host.bottom + 1,
    };
  });
  expect(start.scale).toBeLessThan(1);
  expect(start.fits).toBe(true);
  expect(start.readout).toBe(`${Math.round(start.scale * 100)}%`);

  const drop = await page.evaluate(() => {
    const canvas = document.getElementById("canvas");
    const rect = canvas.getBoundingClientRect();
    const data = new DataTransfer();
    data.setData("type", "label");
    canvas.dispatchEvent(
      new DragEvent("drop", {
        bubbles: true,
        cancelable: true,
        dataTransfer: data,
        clientX: rect.left + 200,
        clientY: rect.top + 100,
      })
    );
    const element = [...document.querySelectorAll("#canvas .element")].at(-1);
    return {
      x: Number(element.getAttribute("data-x")),
      y: Number(element.getAttribute("data-y")),
      expectedX: Math.round(200 / canvasScale),
      expectedY: Math.round(100 / canvasScale),
    };
  });
  expect(Math.abs(drop.x - drop.expectedX)).toBeLessThanOrEqual(2);
  expect(Math.abs(drop.y - drop.expectedY)).toBeLessThanOrEqual(2);

  await page.locator("#zoomInBtn").click();
  const zoomed = await page.evaluate(() => canvasScale);
  expect(zoomed).toBeGreaterThan(start.scale);
  await page.locator("#zoomFitBtn").click();
  await expect
    .poll(() => page.evaluate(() => canvasScale))
    .toBeCloseTo(start.scale, 3);
});

test("the AI assistant opens as a visible floating panel", async ({ page }) => {
  await page.locator("#aiToggleBtn").click();
  const panel = page.locator("#aiPanel");
  await expect(panel).toBeVisible();

  // `display:flex` plus the old closed-state transform left the panel off-screen
  // at opacity 0: the button lit up and nothing appeared.
  const state = await panel.evaluate((node) => {
    const style = getComputedStyle(node);
    const rect = node.getBoundingClientRect();
    return {
      opacity: Number(style.opacity),
      transform: style.transform,
      onScreen: rect.top >= 0 && rect.left >= 0 && rect.bottom <= innerHeight + 1 && rect.right <= innerWidth + 1,
    };
  });
  expect(state.opacity).toBe(1);
  expect(state.transform === "none" || state.transform.startsWith("matrix(1, 0, 0, 1")).toBe(true);
  expect(state.onScreen).toBe(true);
});

test("exported JSON re-imports as the same design", async ({ page }, testInfo) => {
  await page.evaluate(() => {
    addElement("button", 100, 100);
    addElement("label", 100, 260);
  });

  const exported = await page.evaluate(() => buildEditorConfig());
  expect(exported.scenes[0].elements.length).toBeGreaterThanOrEqual(2);

  const file = testInfo.outputPath("design.json");
  await writeFile(file, JSON.stringify(exported, null, 2), "utf8");

  // A fresh page has the default empty scene, so a successful import is
  // observable as the element count and scene list changing.
  await page.reload();
  await page.waitForFunction(() => typeof buildEditorConfig === "function");
  await page.locator("#loadFile").setInputFiles(file);

  await expect
    .poll(() => page.evaluate(canvasElements))
    .toBe(exported.scenes[0].elements.length);

  const reimported = await page.evaluate(() => buildEditorConfig());
  expect(designShape(reimported)).toEqual(designShape(exported));

  // The file on disk is what a user would hand to the player: valid JSON with
  // the canvas size the builder shows.
  const saved = JSON.parse(await readFile(file, "utf8"));
  expect(saved.Width).toBe(exported.Width);
  expect(saved.Height).toBe(exported.Height);
});

// Deleting, undoing and Escape must agree on what is selected. Before the fix,
// Delete left the inspector in element mode with nothing selected, and Undo
// brought the element back highlighted (the saved HTML carried its "selected"
// class) while the editor held no selection, so Escape could not clear it.
test("delete, undo and Escape keep the selection state consistent", async ({ page }) => {
  const state = () =>
    page.evaluate(() => ({
      highlighted: document.querySelectorAll("#canvas .element.selected").length,
      current: Boolean(currentElement),
      inspector: document.body.classList.contains("element-selected"),
    }));
  const none = { highlighted: 0, current: false, inspector: false };

  await page.locator("#canvas .element").first().click({ force: true });
  expect(await state()).toEqual({ highlighted: 1, current: true, inspector: true });

  await page.keyboard.press("Delete");
  expect(await state()).toEqual(none);

  await page.keyboard.press("Control+z");
  expect(await state()).toEqual(none);

  await page.locator("#canvas .element").first().click({ force: true });
  await page.keyboard.press("Escape");
  expect(await state()).toEqual(none);
});

// Scene names are what links, menus and exports are keyed on, so add, rename and
// delete have to keep them unique, safe to look up and consistent.
test("adding a scene after a delete never replaces an existing scene", async ({ page }) => {
  const result = await page.evaluate(() => {
    loadJukaApp({
      Width: 1280,
      Height: 720,
      scenes: [
        { name: "Scene 1", elements: [] },
        { name: "Scene 2", elements: [{ type: "label", text: "keep me", x: 5, y: 5, width: 100, height: 20 }] },
        { name: "Scene 3", elements: [{ type: "label", text: "me too", x: 5, y: 5, width: 100, height: 20 }] },
      ],
    });
    window.confirm = () => true;
    sceneSelector.value = "Scene 1";
    changeScene();
    deleteScene(); // two scenes left, so "Scene N+1" would be "Scene 3" again
    addScene();
    return {
      names: Object.keys(scenes),
      options: [...sceneSelector.options].map((o) => o.value),
      scene3: scenes["Scene 3"].map((el) => el.textContent.replace("✕", "").trim()),
    };
  });
  expect(result.names).toEqual(["Scene 2", "Scene 3", "Scene 4"]);
  expect(result.options).toEqual(result.names);
  expect(result.scene3).toEqual(["me too"]);
});

test("renaming a scene carries the links to it, and quoted names are safe", async ({ page }) => {
  const result = await page.evaluate(() => {
    loadJukaApp({
      Width: 1280,
      Height: 720,
      scenes: [
        {
          name: "Main",
          elements: [
            { type: "button", text: "Go", x: 10, y: 10, width: 100, height: 40, trigger: "change_scene:Tube" },
            { type: "button", text: "Also", x: 10, y: 60, width: 100, height: 40, trigger: "", sceneChange: "Tube" },
          ],
        },
        { name: 'The "Tube"', elements: [] },
        { name: "Tube", elements: [] },
      ],
    });
    sceneSelector.value = "Tube";
    changeScene();
    window.prompt = () => "Videos";
    renameScene();
    const links = buildEditorConfig().scenes[0].elements.map((el) => el.trigger || el.sceneChange);
    // A quote in the name used to throw from the option selector.
    sceneSelector.value = 'The "Tube"';
    changeScene();
    window.prompt = () => "Plain";
    renameScene();
    window.confirm = () => true;
    deleteScene();
    return { links, names: Object.keys(scenes) };
  });
  expect(result.links).toEqual(["change_scene:Videos", "Videos"]);
  expect(result.names).toEqual(["Main", "Videos"]);
});
