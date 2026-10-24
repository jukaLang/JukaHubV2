// Minimal static file server for the Playwright smoke test.
//
// The builder is a static site with no build step, so the test only needs the
// repo root served over HTTP. A dependency-free server keeps `npm ci` in
// tests/e2e down to Playwright itself and works the same on Windows, macOS and
// Linux CI.

import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { extname, join, normalize, resolve, sep } from "node:path";

const root = resolve(process.argv[2] || ".");
const port = Number(process.env.PORT || 8123);
const host = process.env.HOST || "127.0.0.1";

// Response headers come from the site's own _headers file (the "/*" block), so
// the tests run under the same Content-Security-Policy as production.
function siteHeaders() {
  const headers = {};
  let inGlobalBlock = false;
  try {
    for (const raw of readFileSync(join(root, "_headers"), "utf8").split(/\r?\n/)) {
      if (!raw.trim() || raw.trim().startsWith("#")) continue;
      if (!/^\s/.test(raw)) {
        inGlobalBlock = raw.trim() === "/*";
        continue;
      }
      if (!inGlobalBlock) continue;
      const colon = raw.indexOf(":");
      if (colon > 0) headers[raw.slice(0, colon).trim()] = raw.slice(colon + 1).trim();
    }
  } catch {
    // No _headers file: serve without extra headers.
  }
  return headers;
}
const extraHeaders = siteHeaders();

const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".svg": "image/svg+xml",
  ".webmanifest": "application/manifest+json",
  ".txt": "text/plain; charset=utf-8",
  ".xml": "application/xml; charset=utf-8",
};

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url || "/", `http://${host}:${port}`);
    const pathname = decodeURIComponent(url.pathname);
    const relative = normalize(pathname === "/" ? "/index.html" : pathname).replace(/^([/\\])+/, "");
    const target = resolve(join(root, relative));
    // Never serve outside the repo root.
    if (target !== root && !target.startsWith(root + sep)) {
      res.writeHead(403).end("forbidden");
      return;
    }
    const info = await stat(target);
    if (info.isDirectory()) {
      res.writeHead(302, { Location: `${pathname.replace(/\/$/, "")}/index.html` }).end();
      return;
    }
    const body = await readFile(target);
    res.writeHead(200, {
      ...extraHeaders,
      "Content-Type": types[extname(target).toLowerCase()] || "application/octet-stream",
      "Cache-Control": "no-store",
      "Content-Length": body.length,
    });
    res.end(body);
  } catch (error) {
    const status = error && error.code === "ENOENT" ? 404 : 500;
    res.writeHead(status, { "Content-Type": "text/plain; charset=utf-8" }).end(
      status === 404 ? "not found" : "server error"
    );
  }
});

server.listen(port, host, () => {
  process.stdout.write(`serving ${root} on http://${host}:${port}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
