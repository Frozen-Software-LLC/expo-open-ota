// Local native protocol fixture. Executes the production Worker module with a
// filesystem R2 adapter and an in-memory Cache API; this is NOT Cloudflare proof.
import http from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { resolve, sep } from 'node:path';
import worker from '../src/index.mjs';

const root = resolve(process.argv[2] ?? '');
if (!process.argv[2]) throw new Error('Usage: node test/local-edge.mjs FIXTURE_ROOT');
const bucket = resolve(root, 'bucket');
let mode;
const entries = new Map();
globalThis.caches = { default: {
  async match(request) { return entries.get(request.url)?.clone(); },
  async put(request, response) { entries.set(request.url, response); },
} };

http.createServer(async (incoming, outgoing) => {
  try {
    const state = JSON.parse(await readFile(resolve(root, 'state.json'), 'utf8'));
    if (state.mode !== mode) { entries.clear(); mode = state.mode; }
    const requestMode = mode;
    const object = async (key, head) => {
      if (requestMode === 'cdn-missing') return null;
      if (requestMode === 'cdn-unavailable') throw new Error('injected local storage failure');
      const file = resolve(bucket, key);
      if (!file.startsWith(bucket + sep)) throw new Error('outside fixture bucket');
      let info;
      try { info = await stat(file); } catch (error) {
        if (error.code === 'ENOENT') return null;
        throw error;
      }
      let body;
      if (!head) {
        body = await readFile(file);
        if (requestMode === 'cdn-corrupt') body[40] ^= 255;
      }
      return { size: info.size, body, httpEtag: '"local-fixture"' };
    };
    const pending = [];
    const request = new Request('http://127.0.0.1:9091' + incoming.url, { method: incoming.method, headers: incoming.headers });
    const response = await worker.fetch(request, { OTA_BUCKET: {
      get: key => object(key, false), head: key => object(key, true),
    } }, { waitUntil(promise) { pending.push(promise); } });
    const bytes = Buffer.from(await response.arrayBuffer());
    outgoing.writeHead(response.status, Object.fromEntries(response.headers));
    outgoing.end(bytes);
    await Promise.all(pending);
    console.log(JSON.stringify({ event: 'edge', mode: requestMode, method: incoming.method,
      path: incoming.url, status: response.status, bytes: bytes.length,
      cache: response.headers.get('X-OTA-Patch-Cache'),
      fallback: response.headers.get('X-OTA-Patch-Fallback'),
      base: response.headers.get('Expo-Base-Update-ID') }));
  } catch (error) {
    console.error(error);
    outgoing.writeHead(500); outgoing.end();
  }
}).listen(9091, '127.0.0.1', () => console.log('Local Worker adapter on 127.0.0.1:9091'));
