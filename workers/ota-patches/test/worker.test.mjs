import { test } from 'node:test';
import assert from 'node:assert/strict';
import worker from '../src/index.mjs';

const base = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const hash = 'a'.repeat(64);
const patchURL = (platform = 'ios', target = '1700000000000', baseID = base, sha = hash) =>
  `https://cdn.example/_ota-patches/test/1/${target}/bundle-patches/${platform}/${baseID}/${sha}.bsdiff`;

function fixture() {
  const entries = new Map();
  let reads = 0;
  let heads = 0;
  const body = new Uint8Array([66, 83, 68, 73, 70, 70]);
  const env = { OTA_BUCKET: {
    async get(key) { assert.ok(!key.startsWith('_ota-patches/')); reads++; return { size: body.length, body, httpEtag: '"etag"' }; },
    async head(key) { assert.ok(!key.startsWith('_ota-patches/')); heads++; return { size: body.length, httpEtag: '"etag"' }; },
  } };
  globalThis.caches = { default: {
    async match(request) { return entries.get(request.url)?.clone(); },
    async put(request, response) { entries.set(request.url, response); },
  } };
  async function request(url = patchURL(), init = {}) {
    const pending = [];
    const response = await worker.fetch(new Request(url, init), env, { waitUntil(p) { pending.push(p); } });
    await Promise.all(pending);
    return response;
  }
  return { env, request, entries, body, counts: () => ({ reads, heads }) };
}

test('iOS and Android: MISS then HIT, correct headers and bytes, HEAD has no body', async () => {
  for (const platform of ['ios', 'android']) {
    const f = fixture();
    const head = await f.request(patchURL(platform), { method: 'HEAD' });
    assert.equal(head.status, 200);
    assert.equal(await head.text(), '');
    assert.equal(head.headers.get('Content-Length'), '6');
    assert.equal(f.entries.size, 0, 'a HEAD response must not seed an empty cached patch');
    for (const status of ['MISS', 'HIT']) {
      const response = await f.request(patchURL(platform));
      assert.equal(response.status, 200);
      assert.equal(response.headers.get('X-OTA-Patch-Cache'), status);
      assert.equal(response.headers.get('IM'), 'bsdiff');
      assert.equal(response.headers.get('Expo-Base-Update-ID'), base);
      assert.equal(response.headers.get('X-OTA-Patch-SHA256'), hash);
      assert.match(response.headers.get('Cache-Control'), /immutable/);
      assert.deepEqual(new Uint8Array(await response.arrayBuffer()), f.body);
    }
    const cachedHead = await f.request(patchURL(platform), { method: 'HEAD' });
    assert.equal(cachedHead.headers.get('X-OTA-Patch-Cache'), 'HIT');
    assert.equal(await cachedHead.text(), '');
    assert.deepEqual(f.counts(), { reads: 1, heads: 1 });
  }
});

test('cache isolates base, target, platform and patch content; request headers cannot poison it', async () => {
  const f = fixture();
  const urls = [patchURL(), patchURL('android'), patchURL('ios', '1700000000001'),
    patchURL('ios', '1700000000000', 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'),
    patchURL('ios', '1700000000000', base, 'b'.repeat(64))];
  for (const url of urls) await f.request(url);
  assert.equal(f.entries.size, 5);
  const response = await f.request(patchURL(), { headers: { 'Expo-Current-Update-ID': 'evil', Range: 'bytes=0-1', 'If-None-Match': '"etag"' } });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('Expo-Base-Update-ID'), base);
  assert.equal((await response.arrayBuffer()).byteLength, 6);
  assert.equal(f.counts().reads, 5);
});

test('missing/unavailable patches signal native full retry and never cache failures', async () => {
  for (const mode of ['missing', 'error', 'empty', 'too-large']) {
    const f = fixture();
    const fail = async () => {
      if (mode === 'error') throw new Error('storage offline');
      if (mode === 'missing') return null;
      return { size: mode === 'empty' ? 0 : 64 * 1024 * 1024 + 1 };
    };
    f.env.OTA_BUCKET.get = fail;
    f.env.OTA_BUCKET.head = fail;
    const response = await f.request();
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('IM'), 'bsdiff');
    assert.equal(response.headers.get('Expo-Base-Update-ID'), null, 'native must reject metadata and retry original asset without A-IM');
    assert.equal(response.headers.get('Cache-Control'), 'no-store');
    assert.equal(await response.text(), '');
    assert.equal((await f.request(patchURL(), { method: 'HEAD' })).status, 503);
    assert.equal(f.entries.size, 0);
  }
});

test('cache unavailable still streams from R2', async () => {
  const f = fixture();
  caches.default.match = async () => { throw new Error('cache unavailable'); };
  caches.default.put = async () => { throw new Error('cache unavailable'); };
  assert.deepEqual(new Uint8Array(await (await f.request()).arrayBuffer()), f.body);
});

test('invalid paths, unrelated objects, query strings and unsupported methods do not access R2', async () => {
  const f = fixture();
  for (const url of ['https://cdn.example/private-key.pem', patchURL() + '?v=1', patchURL().replace('/ios/', '/windows/'), patchURL().replace('/test/', '/test%2F..%2F/'), patchURL().replace('/test/', '/%ZZ/')]) {
    assert.ok((await f.request(url)).status >= 400);
  }
  assert.equal((await f.request(patchURL(), { method: 'POST' })).status, 405);
  assert.deepEqual(f.counts(), { reads: 0, heads: 0 });
});
