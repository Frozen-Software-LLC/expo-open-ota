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
local runners. Android protocol branches are tested in Go; Android device E2E,
physical devices, and real-network benchmarks remain pending.

## Before production rollout

- Capture and retain exact embedded bundles from dashboard-started native builds
  for fresh-install patches; the current proof supplied them locally.
- Review the three-base retention policy and provision worker memory headroom.
- Complete Android E2E, CDN/cache behavior, storage latency, and slow-network tests.
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
