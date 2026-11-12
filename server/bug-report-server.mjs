// Run the same collector used on Netlify behind your own HTTPS reverse proxy.
// Bind to loopback by default; the reverse proxy supplies the public TLS URL.
import { createServer } from 'node:http';
import { isIP } from 'node:net';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import collectBugReport from '../netlify/functions/bug-report.mjs';

const MAX_BODY_BYTES = 256 * 1024;

function answer(res, status, text) {
  res.writeHead(status, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' });
  res.end(text);
}

function clientIP(req) {
  const peer = req.socket.remoteAddress || 'unknown';
  // A local reverse proxy overwrites X-Real-IP with the actual client. Trust
  // that value only for a loopback connection, never from an internet peer.
  if (peer !== '127.0.0.1' && peer !== '::1' && peer !== '::ffff:127.0.0.1') return peer;
  const proxied = req.headers['x-real-ip'];
  return typeof proxied === 'string' && isIP(proxied.trim()) ? proxied.trim() : peer;
}

export function createBugReportServer(collector = collectBugReport) {
  const server = createServer(async (req, res) => {
    // Keep this listener dedicated to reports. Host is never used to build
    // the request URL, and only a loopback proxy may supply the rate-limit IP.
    if (req.url !== '/api/bug-report') {
      answer(res, 404, 'Not found');
      return;
    }
    try {
      const chunks = [];
      let length = 0;
      for await (const chunk of req) {
        length += chunk.length;
        if (length > MAX_BODY_BYTES) {
          answer(res, 413, 'Report is too large');
          return;
        }
        chunks.push(chunk);
      }
      const request = new Request('http://localhost/api/bug-report', {
        method: req.method,
        headers: req.headers,
        ...(chunks.length ? { body: Buffer.concat(chunks) } : {}),
      });
      const result = await collector(request, { ip: clientIP(req) });
      const headers = Object.fromEntries(result.headers);
      headers['cache-control'] = 'no-store';
      res.writeHead(result.status, headers);
      res.end(Buffer.from(await result.arrayBuffer()));
    } catch (err) {
      console.error('bug-report server: request failed', err);
      if (!res.headersSent) answer(res, 500, 'Report service error');
      else res.end();
    }
  });
  server.requestTimeout = 15_000;
  server.headersTimeout = 10_000;
  return server;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const host = process.env.JUKAHUB_REPORT_HOST || '127.0.0.1';
  const port = Number(process.env.JUKAHUB_REPORT_PORT || '8787');
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    console.error('JUKAHUB_REPORT_PORT must be between 1 and 65535');
    process.exit(1);
  }
  const server = createBugReportServer();
  server.listen(port, host, () => {
    console.log(`JukaHub report service listening on ${host}:${port}`);
  });
}
