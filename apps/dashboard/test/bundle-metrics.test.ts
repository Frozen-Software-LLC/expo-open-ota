import { test } from 'node:test';
import assert from 'node:assert/strict';
import { summarizeBundles, type BundleMetricRow } from '../src/lib/bundle-metrics.js';

const rows: BundleMetricRow[] = [
  { platform: 'ios', delivery: 'patch', reason: 'available', requests: 2, bytes: 200 },
  { platform: 'ios', delivery: 'patch_redirect', reason: 'cdn_available', requests: 6, bytes: 999 },
  { platform: 'ios', delivery: 'full', reason: 'full_after_patch', requests: 1, bytes: 500 },
  {
    platform: 'ios',
    delivery: 'full_redirect',
    reason: 'patch_unavailable',
    requests: 1,
    bytes: 999,
  },
  { platform: 'ios', delivery: 'error', reason: 'invalid_patch', requests: 2, bytes: 123 },
  { platform: 'ios', delivery: 'write_error', reason: 'available', requests: 1, bytes: 456 },
  { platform: 'android', delivery: 'full', reason: 'full_after_patch', requests: 2, bytes: 2000 },
];

test('patch share includes CDN decisions and excludes failed responses', () => {
  const result = summarizeBundles(rows, 'ios');
  assert.equal(result.patchShare, 80);
  assert.equal(result.patches, 8);
  assert.equal(result.full, 2);
  assert.equal(result.failures, 3);
  assert.equal(
    result.directBytes,
    700,
    'CDN and interrupted bodies must not count as direct bundle bytes'
  );
});

test('platform filters and correlated full requests do not double count deliveries', () => {
  const result = summarizeBundles(rows, 'all');
  assert.equal(result.full, 4);
  assert.equal(result.fallbacks, 3);
  assert.equal(result.reasons[0].reason, 'full_after_patch');
  assert.equal(result.reasons[0].requests, 3);
  assert.equal(summarizeBundles(rows, 'android').patchShare, 0);
  assert.equal(summarizeBundles(rows, 'android').fallbacks, 2);
});

test('empty and error-only snapshots have no misleading zero-percent success metric', () => {
  assert.equal(summarizeBundles([], 'all').patchShare, null);
  assert.equal(
    summarizeBundles(
      rows.filter(row => row.delivery === 'error'),
      'all'
    ).patchShare,
    null
  );
  assert.deepEqual(summarizeBundles(rows, 'unknown').reasons, []);
});

test('unknown reasons and platforms remain visible in all-platform totals', () => {
  const result = summarizeBundles(
    [{ platform: 'unknown', delivery: 'full', reason: 'new_reason', requests: 2, bytes: 50 }],
    'all'
  );
  assert.deepEqual(result.reasons, [{ reason: 'new_reason', requests: 2 }]);
  assert.equal(result.full, 2);
});
