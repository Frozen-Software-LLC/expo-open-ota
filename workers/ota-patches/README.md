# OTA patch edge

This Worker adds the Expo patch headers to immutable R2 patch objects and caches
successful responses. Route only the `/_ota-patches/*` paths on the existing
R2 asset hostname through it. Ordinary full bundles keep their existing path.
The Worker strips that public prefix before reading the bucket object, preserving
the server's existing storage layout and optional `S3_KEY_PREFIX`. A fixed prefix
is required because [Workers routes do not support infix wildcards](https://developers.cloudflare.com/workers/configuration/routing/routes/).

```text
Native app -> OTA API: pinned asset + installed base UUID
  -> validate exact target/runtime/platform/base and patch record
  -> HEAD readiness probe (cached, at most 500 ms)
       ready: 302 to immutable patch URL
       not ready: validated patch directly from API (226)

Native app -> patch Worker: GET immutable URL
  -> edge cache HIT, or R2 stream + cache population
  -> 200 + IM: bsdiff + Expo-Base-Update-ID
  -> native reconstructs and verifies target manifest hash
       rejection: retry original asset URL without A-IM -> full bundle
```

## Cache and failure behavior

- The object path contains branch, runtime, target storage ID, platform, base
  update UUID, and patch SHA-256. Different content gets a different URL.
- Cache lifetime is one year with `immutable, no-transform`. Responses ignore
  Range and conditional headers because native reconstruction needs the whole
  patch. Request headers cannot change the representation under a cache key.
- A HEAD probe verifies Expo headers, base, hash, length, and identity encoding.
  Positive readiness is cached for five minutes; failures for 30 seconds, in a
  bounded 1,024-entry process-local map. Concurrent requests during a probe use
  the direct API path. A cold probe can add up to 500 ms before direct fallback.
- Legacy v1 objects remain direct-only. New publication jobs write v2 objects;
  retrying preparation migrates old records. The validation record is written last.
- Cache API failures still serve R2. R2 missing/errors or invalid object sizes
  return a non-cacheable patch-metadata rejection signal to GET requests. Both
  Expo 57 native clients then retry the original asset URL without `A-IM`.
  HEAD failures return 503, preventing new readiness approval.
- This signal requires the Worker to be reachable. DNS/TLS/network failures or
  Worker outages after a successful probe are still ordinary download failures;
  this change does not add a native retry policy for those failures.
- `X-OTA-Patch-SHA256` describes the URL's content identity; it is not a runtime
  body hash calculation in the Worker. Publication verifies the object, and the
  native client independently verifies the reconstructed target hash.
- If an immutable object was accidentally overwritten with bad bytes and those
  bytes reached edge cache, repairing R2 alone is insufficient: purge that exact
  test/production patch URL through the appropriate operator flow. Regeneration
  of different patch bytes naturally gets a new hash URL; restoring the original
  bytes keeps the original URL. Native full fallback protects correctness while
  a stale bad cache entry exists, but costs an extra patch download per attempt.

The server's `patch_redirect` counter counts redirect decisions, not completed
downloads. Redirects contribute zero bundle bytes to the API byte counter.
Worker traffic and cache hits need separate edge telemetry; do not interpret API
byte counters as total CDN traffic or confirmed device installations.

## Isolated Cloudflare validation

An account operator needs R2 bucket/object access and Workers script/route access.
Use an isolated bucket and hostname, never the production OTA route for this test.

1. Copy `wrangler.example.toml` to ignored `wrangler.local.toml`. Set the actual
   test bucket binding and a route on its test R2 custom domain. The bucket key
   prefix must match the OTA server's existing `S3_KEY_PREFIX`.
2. Publish the Worker to that test route with the reviewed configuration.
   `workers.dev` alone is not proof of deployed Cache API behavior. See
   [Cloudflare's R2 Cache API guide](https://developers.cloudflare.com/r2/examples/cache-api/).
3. Configure an isolated OTA server's existing R2 CDN hostname setting to use the
   test hostname. Prepare v2 patches in that test bucket using exact test bundles.
4. Confirm the first GET returns MISS, then a repeat at the same edge location
   returns HIT, with identical body hashes and Expo headers. HEAD must not seed
   an empty cache response. Verify that the full bundle URL remains unchanged.
5. Repeat native successful-patch and full-fallback cases against this test server.
   Use a fresh object URL or remove the test cache entry when checking a missing
   object; a cached good patch may correctly continue serving after an R2 deletion.

The local verification below does not create Cloudflare resources. Separately,
on October 16, 2026 (Asia/Manila), an isolated test deployment was created:

- Worker: `dealseek-ota-patches-test-20261016`
- R2 bucket: `dealseek-ota-patch-test-20261016`
- Route: `ota-patch-test-20261016.dealseek.com/_ota-patches/*`
- Test object prefix: `proof-20261016/`

Production resources and release channels were not modified. Fixture uploads used
a temporary authenticated uploader; its Worker, route, and local token were
removed immediately after upload. The isolated patch Worker and bucket remain
available for further tests.

Real HTTPS patch responses and cache HITs were verified for both platforms.
Requests can reach different Cloudflare data centers, so a repeat request is not
necessarily a HIT at a different location.

## Local verification

```sh
node --test workers/ota-patches/test/*.test.mjs
go test -race ./internal/bundlepatch ./internal/cdn ./internal/handlers ./internal/update ./internal/metrics
```

For native protocol tests, `test/local-edge.mjs` executes this exact Worker module
using filesystem R2 and in-memory cache adapters. Copy isolated OTA fixtures into
a new root; keep the original proof fixtures unchanged. The root needs `bucket/`,
`state.json`, and isolated simulator/APK builds. Create the content-addressed
object copies described in each patch's validation record.

```sh
node workers/ota-patches/test/local-edge.mjs FIXTURE_ROOT > FIXTURE_ROOT/edge.log 2>&1
go run ./cmd/local-bundle-diff --root FIXTURE_ROOT --patch-edge http://127.0.0.1:9091 > FIXTURE_ROOT/server.log 2>&1
```

Run iOS and Android sequentially because they share `state.json`. Use disposable
simulators/emulators and binaries targeting runtime `9090.1` and the loopback
server. For Android, reverse ports 9090 and 9091 with `adb reverse`.

The local harness redirects an already validated direct patch to this adapter.
It tests actual native redirect handling, Worker response behavior, reconstruction,
full retry, stored bundle hashes, and successful cold launches. Production CDN
selection and HTTPS probing are exercised separately in Go integration tests.
This layered proof is not a deployed Cloudflare test or performance benchmark.
The recorded durations start at the app launch command, include emulator/app
initialization, and use reload flag OFF with an explicit cold start to verify the
target. They are not measurements of the app's three-second OTA apply budget.

On October 15, 2026, all four scenarios passed on both native platforms. The
valid iOS patch was 4,854,878 bytes versus 9,542,363 full gzip bytes; Android was
3,636,850 versus 9,640,035. Missing/storage-error responses transferred no patch
body before full retry. Corruption incurred both the patch and full bundle bytes,
as expected. No artificial time delays or bandwidth caps were used; missing,
storage-error, and corrupt data were deliberately injected failure cases.

Validation also passed the focused Go race suite above, `go vet` for the changed
packages/commands, `go build ./...`, all five Worker tests, and an offline CLI
smoke test of v2 object/record output. The full `go test ./...` run still fails
only the four previously confirmed baseline tests in `test/assets_test.go`:
`TestToRetrieveBundleAssetWithGzipCompression`,
`TestToRetrieveBundleAssetWithBrotliCompression`,
`TestToRetrievePNGAssetWithGzipCompression`, and
`TestPreventCDNRedirectionHeader` (expected 200, received 404).
