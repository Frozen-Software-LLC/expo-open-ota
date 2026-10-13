# Bundle diffing (DEV-9212)

This implementation serves precomputed BSDIFF40 patches for Expo launch assets.
It is disabled unless `OTA_BUNDLE_DIFFING_ENABLED=true`. No production service,
channel, OTA, or native store release has been changed.

```text
Exact old bundle + exact new bundle
  -> offline BSDIFF preparation + reconstruction verification
  -> only retain patches at least 10% smaller than gzip(full bundle)
  -> store beneath the target update

Device requests pinned target asset
  -> supports bsdiff + known base + matching runtime/platform/target
       -> valid patch available: HTTP 226 / IM: bsdiff
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
enabling. Multiple replicas may prepare the same deterministic artifact safely;
readers verify the record and patch hash before serving it.

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

Output is `bundle-patches/PLATFORM/BASE_UUID.{json,bsdiff}` underneath that target
update directory. The JSON records the exact identities, byte counts, and SHA-256
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

Patch negotiation occurs before CDN redirection. Patches are private/no-store
and vary by base, target, and supported encoding. Missing artifacts, malformed
records, mismatched identities, unsupported clients, and disabled diffing fall
back to the existing full-asset path. Images and other non-launch assets are not
binary-diffed. The client still verifies the reconstructed target hash.

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
- Verify deployed CDN/cache behavior, storage latency, physical devices, and real-network behavior.
  Local Android E2E, including an explicitly throttled download, has passed.
- Deploy the companion server changes separately from the DealSeek PR; leave the
  server switch off until the concrete staged artifacts are verified.

## Tests

`go test -race ./internal/bundlepatch ./internal/handlers ./internal/update` covers reconstruction,
patch-size eligibility, negotiation, disabled/missing/invalid patch fallback,
iOS/Android identity scoping, corrupted stored artifacts, and pinned target URLs
surviving a later publication. Publication tests cover both platforms, base selection,
queue bounds/deduplication, flag gating, repair, and idempotence. `go vet` passes for the changed packages/commands.
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

## Internal rollout and download observability

Patch generation remains controlled by `OTA_BUNDLE_DIFFING_ENABLED=true`.
Serving now also requires an eligible installation. Default/unset rollout mode
is `allowlist`, with an empty list denying patches to everyone:

```sh
OTA_BUNDLE_DIFFING_ENABLED=true
OTA_BUNDLE_DIFFING_ROLLOUT=allowlist
OTA_BUNDLE_DIFFING_DEVICE_IDS=INSTALLATION_UUID_1,INSTALLATION_UUID_2
```

Use the UUID from Expo's `EAS-Client-ID` asset/manifest request header. It is an
**installation ID**, not a PostHog distinct ID, account ID, hardware ID or push token.
The native Expo client sends it on iOS and Android, including initial downloads
before JavaScript/PostHog initializes. `expo-eas-client` also exports `clientID`
for an internal debug console. The local harness logs it in asset events; obtain
an internal tester's ID from their debug session or a controlled request capture.
Do not log all production installation IDs or put them in metric labels.
Reinstallation/data clearing may require enrolling a new ID. This is a rollout
control, not an authentication boundary: client-supplied IDs can be spoofed.

### Internal builds (no installation IDs)

A build can opt a whole cohort in instead. Builds send
`Expo-OTA-Cohort` through expo-updates `requestHeaders`; Expo attaches those
headers to manifest **and** asset requests on iOS and Android, so the main
server sees the cohort on the launch-bundle download it patches:

```sh
OTA_BUNDLE_DIFFING_COHORTS=internal
```

```ts
// apps/native/app.config.ts, for an internal TestFlight profile only
requestHeaders: {
  "expo-channel-name": updateChannel,
  ...(process.env.EXPO_OTA_COHORT ? { "Expo-OTA-Cohort": process.env.EXPO_OTA_COHORT } : {}),
},
```

Anyone who installs that build gets patches; store builds send no cohort and
keep full bundles. The header is baked into the binary at build time, so it
cannot be added to an existing build by OTA. Matching is case-insensitive and
the header is in `Vary`. Like installation IDs, it is a rollout control and can
be spoofed. Responses and metrics are unchanged: an eligible cohort follows the
same `patch / available` and fallback paths as an allowlisted installation.

Unlisted/missing/invalid IDs (without an allowed cohort) receive ordinary full bundles. Invalid rollout modes
fail closed. Explicit `OTA_BUNDLE_DIFFING_ROLLOUT=all` allows broad rollout later;
setting `OTA_BUNDLE_DIFFING_ENABLED=false` is the global kill switch. These are
server environment settings, requiring a server restart/deployment. They are
independent of the app's `ota-reload-enabled` flag, which controls **when to apply**
an update. No new app build is required for compatible Expo clients.

Prometheus `/metrics` exposes two counters with bounded labels
`platform`, `delivery`, `reason`:

- `ota_bundle_responses_total`: exact launch-asset requests only, excluding images/fonts.
- `ota_bundle_response_bytes_total`: actual body bytes written by this server.

Deliveries: `patch` (HTTP 226), `full` (HTTP 200), `full_redirect` (HTTP 302 to CDN),
`write_error` (interrupted response), and `error` (other HTTP errors). Full reasons
include `disabled`, `device_not_allowed`, `not_negotiated`, `patch_unavailable`,
`invalid_base`, `invalid_target`, `unpinned`, `target_mismatch`, `invalid_record`,
and `invalid_patch`. Patch responses use `available`.

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
count as full download decisions, but their transfer sizes/completion are unknown;
redirect body bytes are not full bundle bytes. Failed metadata resolution cannot
identify a launch asset and is excluded from these bundle-only counters.

Useful PromQL (select your service/job in a shared Prometheus deployment):

```promql
# Patch vs full responses / CDN redirects during the last day
sum by (platform, delivery) (
  increase(ota_bundle_responses_total{delivery=~"patch|full|full_redirect"}[24h])
)

# Why full bundles were selected (separates rollout exclusions from fallbacks)
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

The local harness exposes `/metrics` as well. Unlike its earlier unconditional
patch delivery, it now obeys the same allowlist default; set explicit `all` for
legacy full-vs-patch fixture scripts, or enroll the simulator installation to test
internal rollout. Its optional `bytesPerSecond` state field caps launch-bundle
bandwidth for slow-network testing; this is an explicit artificial network cap.

## Installation allowlist verification (October 13, 2026)

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

Validation: focused Go race tests and vet pass, including iOS/Android handler
coverage, allowlist fail-closed behavior, metric labels, retry identity/expiry,
write failures, and Redis telemetry deadlines. The full Go suite still has the
same four previously documented asset/CDN integration failures.
