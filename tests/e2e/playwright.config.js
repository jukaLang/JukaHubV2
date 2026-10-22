import { defineConfig, devices } from "@playwright/test";

// The builder is static, so the test serves the repo root with the small
// dependency-free server next to this file and drives Chromium headless.
// `reuseExistingServer` keeps a locally running server (for debugging) usable.
const PORT = Number(process.env.E2E_PORT || 8123);

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.js$/,
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "node serve.mjs ../..",
    url: `http://127.0.0.1:${PORT}/index.html`,
    env: { PORT: String(PORT) },
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
