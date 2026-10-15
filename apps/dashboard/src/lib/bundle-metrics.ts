export type BundleMetricRow = {
  platform: string;
  delivery: string;
  reason: string;
  requests: number;
  bytes: number;
};

export type BundleSnapshot = {
  instanceId: string;
  startedAt: string;
  sampledAt: string;
  rows: BundleMetricRow[];
};

export function summarizeBundles(rows: BundleMetricRow[], platform: string) {
  const selected = rows.filter(row => platform === 'all' || row.platform === platform);
  const count = (deliveries: string[]) =>
    selected
      .filter(row => deliveries.includes(row.delivery))
      .reduce((sum, row) => sum + row.requests, 0);
  const patches = count(['patch', 'patch_redirect']);
  const full = count(['full', 'full_redirect']);
  const failures = count(['error', 'write_error']);
  const fullRows = selected.filter(row => ['full', 'full_redirect'].includes(row.delivery));
  const reasons = new Map<string, number>();
  fullRows.forEach(row => reasons.set(row.reason, (reasons.get(row.reason) ?? 0) + row.requests));
  return {
    patches,
    full,
    failures,
    directPatches: count(['patch']),
    cdnPatches: count(['patch_redirect']),
    directFull: count(['full']),
    cdnFull: count(['full_redirect']),
    fallbacks: reasons.get('full_after_patch') ?? 0,
    patchShare: patches + full > 0 ? (patches / (patches + full)) * 100 : null,
    directBytes: selected
      .filter(row => ['patch', 'full'].includes(row.delivery))
      .reduce((sum, row) => sum + row.bytes, 0),
    reasons: [...reasons]
      .map(([reason, requests]) => ({ reason, requests }))
      .sort((a, b) => b.requests - a.requests || a.reason.localeCompare(b.reason)),
  };
}

export const reasonLabels: Record<string, [string, string]> = {
  full_after_patch: [
    'Full after patch',
    'Full request within 10 minutes of a matching patch attempt. Correlated retry, not confirmed rejection.',
  ],
  not_negotiated: [
    'No patch negotiation',
    'The request did not ask for a supported patch. This can include older clients or unmatched retries.',
  ],
  patch_unavailable: [
    'No prepared patch',
    'No patch was available for the installed base and target.',
  ],
  invalid_base: ['Invalid base ID', 'The installed update ID was missing or invalid.'],
  invalid_target: ['Invalid target ID', 'The requested target ID was invalid or matched the base.'],
  unpinned: ['Unpinned asset URL', 'The request did not identify an exact target update.'],
  target_mismatch: ['Target mismatch', 'The target update or platform did not match the request.'],
  invalid_record: [
    'Invalid patch record',
    'The patch metadata failed validation or size eligibility.',
  ],
  invalid_patch: ['Invalid patch data', 'The stored patch failed its size or hash check.'],
  target_unavailable: ['Target unavailable', 'The target could not be resolved.'],
  metadata_unavailable: ['Metadata unavailable', 'The target metadata could not be read.'],
};
