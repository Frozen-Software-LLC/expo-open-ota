// Route only /_ota-patches/* through this Worker. Ordinary OTA assets retain
// their existing CDN path. Patch identity is in the URL, never request headers.
const patchPath = /^(.*\/bundle-patches\/(?:ios|android)\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}))\/([0-9a-f]{64})\.bsdiff$/;
const maxPatchBytes = 64 * 1024 * 1024;
const routePrefix = '/_ota-patches';

function noStore(status) {
  return new Response(null, { status, headers: { 'Cache-Control': 'no-store' } });
}

// Expo retries its original asset URL without A-IM when patch metadata is
// incomplete. A plain 404/503 does NOT trigger that path on both native clients.
// Never cache this explicit fallback signal, and never send it for HEAD probes.
function fallback(request, reason) {
  if (request.method === 'HEAD') return noStore(503);
  return new Response(null, {
    status: 200,
    headers: {
      'IM': 'bsdiff',
      'Cache-Control': 'no-store',
      'Content-Length': '0',
      'X-OTA-Patch-Fallback': reason,
    },
  });
}

export default {
  async fetch(request, env, ctx) {
    if (request.method !== 'GET' && request.method !== 'HEAD') return noStore(405);
    const url = new URL(request.url);
    let path;
    try { path = decodeURIComponent(url.pathname); } catch { return noStore(400); }
    if (!path.startsWith(routePrefix + '/')) return noStore(404);
    path = path.slice(routePrefix.length);
    // S3_KEY_PREFIX is already part of the CDN path. No arbitrary object access.
    if (url.search || path.includes('\\') || path.slice(1).split('/').some(p => !p || p === '.' || p === '..')) return noStore(400);
    const match = patchPath.exec(path);
    if (!match) return noStore(404);
    const [, , baseID, hash] = match;
    const key = path.slice(1);
    const cacheKey = new Request(url.origin + url.pathname, { method: 'GET' });
    const cache = caches.default;
    try {
      const cached = await cache.match(cacheKey);
      if (cached) {
        const headers = new Headers(cached.headers);
        headers.set('X-OTA-Patch-Cache', 'HIT');
        return new Response(request.method === 'HEAD' ? null : cached.body, { status: 200, headers });
      }
    } catch { /* Cache failure must not block the R2 download. */ }

    let object;
    try {
      object = await env.OTA_BUCKET[request.method === 'HEAD' ? 'head' : 'get'](key);
    } catch { return fallback(request, 'storage_unavailable'); }
    if (!object) return fallback(request, 'patch_missing');
    if (object.size <= 0 || object.size > maxPatchBytes) return fallback(request, 'invalid_size');

    const headers = new Headers({
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(object.size),
      'Cache-Control': 'public, max-age=31536000, immutable, no-transform',
      'IM': 'bsdiff',
      'Expo-Base-Update-ID': baseID,
      'X-OTA-Patch-SHA256': hash,
      'X-OTA-Patch-Cache': 'MISS',
      'ETag': object.httpEtag,
    });
    // A cacheable 200 + IM: bsdiff is understood by Expo on iOS and Android.
    // Ignore Range/conditional headers: a patch must be reconstructed in full.
    const response = new Response(request.method === 'HEAD' ? null : object.body, { status: 200, headers });
    if (request.method === 'GET') {
      ctx.waitUntil(cache.put(cacheKey, response.clone()).catch(() => {}));
    }
    return response;
  },
};
