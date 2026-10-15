# Bundle diffing (DEV-9212)

This implementation serves precomputed BSDIFF40 patches for Expo launch assets.
Patch preparation and serving are enabled by default in code for all compatible
clients. No environment switch or device allowlist is required. Deploying this
server version enables that behavior; it does not change which updates each
channel/runtime/platform receives.

```text
Exact old bundle + exact new bundle
  -> offline BSDIFF preparation + reconstruction verification
  -> only retain patches at least 10% smaller than gzip(full bundle)
  -> store beneath the target update

Device requests pinned target asset
  -> supports bsdiff + known base + matching runtime/platform/target
       -> v2 patch + ready CDN: 302 -> edge 200 / IM: bsdiff
       -> otherwise valid patch: API HTTP 226 / IM: bsdiff
       -> otherwise: existing full-asset/CDN response
  -> native client reconstructs + verifies the target manifest hash
       -> corrupt patch: retry the full bundle
```

## Automatic publication integration

After a verified upload or republish writes its `.check` marker, the existing
`MarkUpdateAsChecked` path queues optional patch preparation. Dashboard promotion
already calls this finalization endpoint, so there is no extra publication API
or new release bypass. A retry of an already-finalized upload also requeues it.

```text
Governed upload / promotion / republish
  -> verify files + store UUID + commit .check
  -> return success (full bundle immediately available)
  -> bounded background queue
       -> isolated helper: up to 3 prior committed bases
       -> same branch + runtime + platform, older than target
       -> reconstruct + verify + compare gzip size
       -> upload patch, then validation record
  -> subsequent compatible downloads receive the patch
```

The Docker image includes the `prepare-update-patches` helper next to the API
binary. One helper runs at a time per server process, with a queue of 16 waiting
updates and a two-minute process deadline covering storage and generation. Inputs
are capped at 64 MiB each; `GOMEMLIMIT=768MiB` is a **soft GC target**, not an OS
memory ceiling. Allocate container memory for both API and one worker before
deploying. Multiple replicas may prepare the same deterministic artifact safely;
direct readers verify the record and patch hash before serving it. CDN delivery
uses immutable object URLs and a readiness check; native clients still verify
the reconstructed target hash.

The queue is best-effort, not durable. A full queue, timeout, missing helper,
restart, unsuitable patch, or storage error leaves full downloads available.
The normal finalization retry or this storage-only maintenance command recovers
missed jobs (it never finalizes an update or changes a channel):

```sh
# In the server environment, with its existing storage settings:
./prepare-update-patches --branch BRANCH --runtime RUNTIME --update-id STORAGE_ID
```

Existing patch records are reused only when identities and hashes match. Copied
records from a republish are regenerated for the new UUID. Preparation skips
rollback records, uncommitted uploads, other platforms and newer updates.

## Prepare an artifact locally

Run from this repository, using exact manifest UUIDs and uncompressed bundle files:

```sh
go run ./cmd/prepare-bundle-patch \
  --base-bundle /path/to/base.hbc \
  --target-bundle /path/to/target.hbc \
  --base-id BASE_MANIFEST_UUID \
  --target-id TARGET_MANIFEST_UUID \
  --runtime SHARED_RUNTIME \
  --platform ios \
  --asset _expo/static/js/ios/entry-HASH.hbc \
  --output-dir /path/to/target-update-directory
```

Output is `bundle-patches/PLATFORM/BASE_UUID.json` and the immutable object
`bundle-patches/PLATFORM/BASE_UUID/PATCH_SHA256.bsdiff` underneath that target
update directory. The v2 JSON records the exact identities, byte counts, and SHA-256
hashes. Preparation rejects empty/oversized inputs and non-beneficial patches.
It verifies reconstructed output byte-for-byte before writing a patch. Generation
runs offline rather than during the user's download; input bundles are limited to
64 MiB. The automatic publication helper adds the process deadline described above.
The command does not publish an OTA, advance a channel, or access production.

A fresh-install base is the actual embedded bundle and manifest UUID extracted
from the native build. A previous OTA base is that exact published bundle. These
are different baselines; a patch is never assumed valid for an unknown base.

## Serving

New manifests pin each asset URL to its storage update ID. This avoids a newer
promotion changing the full-bundle fallback while the earlier update downloads.
Legacy unpinned URLs retain their old behavior. Validation is cached for 60 seconds
to avoid repeated object-storage reads for every asset. Manifest/asset cache keys
are versioned so old cached unpinned URLs are not reused after deployment.

Patch negotiation occurs before full-bundle CDN redirection. Direct patches and
redirect decisions are private/no-store. New v2 patches can redirect to a ready
CDN under a content-addressed URL; the edge response is publicly cacheable because
base, target, runtime, platform, and content identity are in the URL. Missing artifacts, malformed
records, mismatched identities, and unsupported clients fall
back to the existing full-asset path. Images and other non-launch assets are not
binary-diffed. The client still verifies the reconstructed target hash.

See [patch CDN behavior and validation](../workers/ota-patches/README.md) for the
Worker, readiness probe, fallback limits, and local iOS/Android protocol evidence.

## Local E2E harness

`go run ./cmd/local-bundle-diff --root /path/to/fixture-root` binds only
`127.0.0.1:9090`. It requires isolated runtime `9090.1` and branch `local-diff`.
The fixture root contains `bucket/` and `state.json`. It uses real manifest
construction, this repository's production asset handler, and real native Expo
patch application. Local feature flags and deliberately corrupted response bytes
are test fixtures; they never alter production flags or stored artifacts.

With a copied isolated test binary (`DealSeek.app`) and `fixture.json` describing
its prepared target in that root, run:

```sh
python3 scripts/local-bundle-diff-proof.py --root /path/to/fixture-root --device SIMULATOR_UUID
```

The runner only accepts a simulator named `DealSeek DEV-9212…` and a binary pinned
to the loopback server and isolated runtime. It reinstalls that test app, tests full,
patch, and corrupted-patch paths, verifies stored hashes and successful cold launches,
and saves per-case evidence. It waits for native initialization before querying SQLite.

`state.json` selects a local storage update ID using `target`, a local flag using
`reloadEnabled`, and `mode` (`normal`, `full`, or `corrupt`). An empty target returns
no update. Full mode suppresses patch negotiation for the comparison; corrupt mode
changes a byte after the production handler has verified the stored patch.

## Measured iOS proof (October 5, 2026)

Release-mode iPhone 17 Pro simulator / iOS 26.3, Expo Updates 57.0.23, loopback
server, no injected download/check delays. The old bundle was the existing local
OTA test binary's 21,675,612-byte embedded Hermes bundle. Target A was a fresh
export of DealSeek commit `7ee9f8318` (26,542,646 bytes). Target B adds one local-only
console marker to the root layout; that temporary source change was restored.

| Scenario | Full gzip bundle | Patch | Verified result |
| --- | ---: | ---: | --- |
| Fresh install → A | 9,542,363 B | 4,854,878 B | Patch applied; exact hash; successful cold launch |
| Existing OTA A → B | 9,541,383 B | 665,529 B | Patch applied; exact hash; successful cold launch |
| Corrupted fresh-install patch | 9,542,363 B fallback | 4,854,878 B rejected | Native rejection, full retry, exact hash, successful launch |

This is 49.1% and 93.0% less **bundle** transfer respectively. Other assets are
excluded from those savings. The unthrottled fresh full download took 1.320s from
launch command to download-ready database state; patch runs took 1.312s and 1.670s.
Corruption and fallback took 3.609s. These are individual loopback observations,
not production speed claims or visible-loader measurements, and do not establish
a speed improvement. Generation took 9.179s for the first
pair and ran before any device request.

Native logs contain `Applied diff for asset …`, the server records HTTP 226,
and the stored reconstructed SHA-256 matches the exported target. Successful
launch counts increased with zero failed launches. Publishing B did not interrupt
the still-open A session. Automatic launch reload was not confirmed: the simulator
tracking prompt was open and local flags remained unresolved, so the launch gate
deferred. Cold-start application was verified after declining tracking.

Evidence is saved in the developer's `Downloads/DEV-9212-evidence/`, including
requests, native logs, comparison JSON, screenshots, fixture preparation, and
local runners. Android device E2E subsequently passed with the merged app PR, including the
three-second budget and native fallback. Physical devices and production network
benchmarks remain pending.

## Before production rollout

- Capture and retain exact embedded bundles from dashboard-started native builds
  for fresh-install patches; the current proof supplied them locally.
- Review the three-base retention policy and provision worker memory headroom.
- Verify production CDN/cache configuration and physical-device/network behavior.
  Isolated live Cloudflare verification passed on both simulators/emulators (see below);
  this does not verify the production deployment.
- Deploy the companion server changes separately from the DealSeek PR, only
  after the concrete staged artifacts are verified. Diffing is enabled in code;
  there is no server environment switch or installation allowlist.

## Tests

`go test -race ./internal/bundlepatch ./internal/handlers ./internal/update` covers reconstruction,
patch-size eligibility, negotiation, missing/invalid patch fallback,
iOS/Android identity scoping, corrupted stored artifacts, and pinned target URLs
surviving a later publication. Publication tests cover both platforms, base selection,
queue bounds/deduplication, repair, and idempotence. `go vet` passes for the changed packages/commands.
The full suite retains four asset/CDN tests that also fail on unmodified server
main `5f4f6d7`; no additional failures remain after updating URL expectations.

## Recorded publication-hook verification

The final comparison video contains two 24-second chapters, each showing full
versus patch downloads at 1× speed. The same real simulator state is restored
before each pair; OS permission prompts are handled beforehand. The fresh pair
starts with only the embedded update cached. The existing-user pair starts with
OTA A installed and onboarding completed. Both use flag OFF and an explicitly
labeled manual cold start after a 10-second viewing hold (not a network delay).

Before recording the existing-user pair, the manually generated B patch was
removed. `--finalize-update 1791200001000` in the local harness called the real
`MarkUpdateAsChecked` hook; its queued helper generated the 665,529-byte patch.
Both the full and patch captures then launched the exact same B hash successfully.

Final captured launch-command-to-download-ready observations were 2.781s / 1.549s
for the fresh full/patch pair and 1.545s / 1.118s for the existing pair. A final
corrupt-patch retry took 2.405s. These individual loopback runs are evidence of
actual execution, **not production performance benchmarks**. Earlier observations
above illustrate run-to-run variability. Byte counts are stable across runs.

Sanitized measurements and HTTP evidence are in
[`evidence/bundle-diffing-ios.json`](evidence/bundle-diffing-ios.json).
The recording runner is `scripts/record-bundle-diff.py`; its `--save-fresh` and
`--save-existing` modes save baselines from the disposable simulator, then
`--scenario fresh|existing --mode full|normal` records and validates each run.

## Default patch delivery and download observability

Every finalized update queues patch preparation automatically. Every client that
negotiates BSDIFF and has a matching base/runtime/platform/target is eligible for
a verified patch. There is no device UUID allowlist or server environment switch.
Missing, invalid or insufficiently smaller patches use the full bundle; legacy
clients that do not negotiate BSDIFF keep receiving full bundles.

Channel selection remains unchanged: preview apps receive the selected preview
channel's compatible updates, and production apps receive production updates.
Patches can be served on both. The app's `ota-reload-enabled` flag still controls
**when to apply** an update, independently of patch delivery.

Expo's `EAS-Client-ID` is used only to correlate patch attempts with later full
requests. It does not authorize patch downloads: missing or malformed installation
IDs can still receive patches, but cannot contribute to correlated retry counts.
The correlation key hashes the identifiers together; do not put raw production
installation IDs in logs or metric labels.

Prometheus `/metrics` exposes two counters with bounded labels
`platform`, `delivery`, `reason`:

- `ota_bundle_responses_total`: exact launch-asset requests only, excluding images/fonts.
- `ota_bundle_response_bytes_total`: actual body bytes written by this server.

Deliveries: `patch` (HTTP 226), `patch_redirect` (HTTP 302 to a ready patch CDN),
`full` (HTTP 200), `full_redirect` (HTTP 302 to CDN),
`write_error` (interrupted response), and `error` (other HTTP errors). Full reasons
include `not_negotiated`, `patch_unavailable`,
`invalid_base`, `invalid_target`, `unpinned`, `target_mismatch`, `invalid_record`,
and `invalid_patch`. Direct patches use `available`; patch redirects use `cdn_available`.

`full_after_patch` means the same installation/base/target/branch/runtime/platform/
asset requested a full bundle without `A-IM` within ten minutes of a patch attempt.
Expo 57's native rejection retry does exactly this. It is **correlated fallback
traffic, not confirmed device-side rejection**: repeated matching requests can
also qualify. Counts are requests, not unique users or successful installations.
HTTP 226 alone does not prove that reconstruction or installation succeeded.

Correlation uses hashed identifiers and ten-minute TTLs in shared Redis, with
optional telemetry reads/writes capped at 50 ms. Cache failure loses correlation
but leaves download handling intact. Local cache mode uses a bounded 10,000-entry
process-local tracker; restarts, capacity limits, and cross-replica retries can
undercount. Shared Redis is recommended for multiple API replicas. CDN redirects
count as patch or full download decisions, but their transfer sizes/completion are unknown;
all redirect body bytes are excluded from bundle-byte counters. Failed metadata resolution cannot
identify a launch asset and is excluded from these bundle-only counters.

Useful PromQL (select your service/job in a shared Prometheus deployment):

```promql
# Patch vs full responses / CDN redirects during the last day
sum by (platform, delivery) (
  increase(ota_bundle_responses_total{delivery=~"patch|patch_redirect|full|full_redirect"}[24h])
)

# Why full bundles were selected
sum by (platform, reason) (
  increase(ota_bundle_responses_total{delivery=~"full|full_redirect"}[24h])
)

# Full requests correlated with a recent patch attempt
sum by (platform) (
  increase(ota_bundle_responses_total{reason="full_after_patch"}[24h])
)

# Directly served bundle bytes, excluding redirect bodies
sum by (platform, delivery) (
  increase(ota_bundle_response_bytes_total{delivery=~"patch|full"}[24h])
)
```

The local harness exposes `/metrics` and uses the same default patch delivery as
the server. Its optional `bytesPerSecond` state field caps launch-bundle bandwidth
for slow-network testing; this is an explicit artificial network cap.

## Built-in bundle metrics dashboard

Open **Dashboard → Bundle metrics** (`/dashboard/bundle-metrics`). The page reads
the authenticated `GET /api/bundle-metrics` endpoint and refreshes every 15 seconds
while visible. It uses the existing in-process Prometheus counters directly;
Grafana and a Prometheus server are not required.

- Filter all platforms, iOS, or Android; unknown-platform traffic remains visible
  in the all-platform totals and gets its own filter when present.
- Patch share = `(direct patches + patch CDN redirects) / (patch + full responses)`.
  HTTP and interrupted-write errors are shown separately, outside that denominator.
- Full-after-patch requests are a subset of full responses, not an additional
  delivery category or a confirmed native failure rate. The reasons table shows
  each reason's percentage of full responses.
- Direct bundle bytes exclude CDN transfers, redirect bodies, and interrupted
  responses. The dashboard does not estimate bandwidth savings or CDN cache hits.
- No traffic displays an empty state and an undefined patch share (`—`), not an
  invented success rate. Failed refreshes retain the previous snapshot with a
  warning; initial failures do not display zero counters.

**Scope:** totals cover only the responding server process since startup. They
reset on restart and do not aggregate across replicas. The endpoint includes an
opaque process ID, start time, and sample time; the UI warns if a refresh comes
from a different instance and replaces rather than adds its totals. There are no
historical time windows, per-release breakdowns, or unique-device/install counts.
Do not interpret load-balanced snapshots as a fleet-wide trend.

Validation: authenticated/unauthenticated router tests, counter snapshot and
concurrent-update race tests, and `npm run test:metrics` in `apps/dashboard` cover
aggregation, filters, CDN byte exclusion, empty/error-only data, and unknown labels.
The dashboard production build and focused ESLint checks pass. Browser checks
cover loading, populated and empty states, platform filtering, initial errors,
stale data after refresh errors, recovery, and server-instance changes. Browser
preview counters are explicitly seeded local test data, not production telemetry.

## Historical installation allowlist verification (October 13, 2026)

These measurements cover the earlier allowlist implementation, which has since
been removed. They remain historical evidence, not current setup instructions.

The Android release-mode app was tested against the local server with the same
real Expo installation UUID preserved while resetting its OTA cache between cases.
No synthetic check/download delays were added:

| Server configuration / scenario | Native bundle responses | Verified metric |
| --- | --- | --- |
| Empty allowlist | Full HTTP 200: 9,640,035 B | `full / device_not_allowed = 1` |
| Installation allowlisted | Patch HTTP 226: 3,636,850 B | `patch / available = 1` |
| Allowlisted, deliberately corrupt patch response | HTTP 226 then HTTP 200: 3,636,850 + 9,640,035 B | `patch / available = 1`, `full / full_after_patch = 1` |

Every case matched the target bundle SHA-256 and completed a successful cold
launch with zero failed launches. The native app and its reload flag were unchanged;
all configuration and fault injection were isolated to the loopback test server.
Sanitized HTTP/metric evidence: [bundle-rollout-android.json](evidence/bundle-rollout-android.json).

Validation at the time: focused Go race tests and vet passed, including iOS/Android
handler coverage, the former allowlist fail-closed behavior, metric labels, retry identity/expiry,
write failures, and Redis telemetry deadlines. The full Go suite still has the
same four previously documented asset/CDN integration failures.

## Live isolated Cloudflare verification

On October 16, 2026 (Asia/Manila), the real patch Worker/R2 path passed six
native cases: successful patch, missing-after-readiness fallback, and corrupt
patch fallback on both an iOS simulator and Android emulator. Reconstructed
bundle hashes and successful cold launches were checked. HTTP response body
hashes, Expo patch headers, and Cloudflare cache HITs were also verified.

The test used a separate bucket and hostname, a local OTA server, and existing
isolated native builds. Full-bundle fallbacks stayed local; this was not a
production rollout, physical-device test, or measurement of the three-second
launch budget. The first Android corrupt-case attempt safely served a direct
patch when readiness was not approved; a primed retry exercised the CDN failure.

See [live Cloudflare evidence](evidence/bundle-cdn-cloudflare-validation.json) and
[deployment setup](evidence/bundle-cdn-cloudflare-setup.json).
