import { expect, test } from "@playwright/test";

// The site ships a strict Content-Security-Policy (see _headers; serve.mjs sends
// the same header here). This drives the parts of the UI that bind handlers,
// build markup and open dialogs, and fails if the browser reports any blocked
// resource, which is how an inline handler or eval would show up in production.

test("the builder runs under the site's Content-Security-Policy without violations", async ({ page }) => {
  await page.addInitScript(() => {
    window.__cspViolations = [];
    document.addEventListener("securitypolicyviolation", (event) => {
      window.__cspViolations.push(`${event.violatedDirective} blocked ${event.blockedURI || "inline"}`);
    });
  });
  const response = await page.goto("/index.html");
  expect(response.headers()["content-security-policy"]).toContain("script-src 'self'");
  await page.waitForFunction(() => typeof buildEditorConfig === "function");

  // Declarative handlers, dialogs and generated markup.
  await page.evaluate(() => {
    openGuide();
    closeGuideFn();
    addScene();
    document.querySelectorAll("[data-click]").forEach((el) => {
      if (!/export|copy|reload|load/i.test(el.getAttribute("data-click") || "")) el.click();
    });
  });
  await page.locator("#darkModeToggle").click();
  await page.keyboard.press("Control+Shift+K");
  await page.waitForTimeout(300);

  expect(await page.evaluate(() => window.__cspViolations)).toEqual([]);
});
