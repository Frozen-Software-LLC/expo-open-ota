import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Activity, ArrowDownToLine, RefreshCw, AlertTriangle } from 'lucide-react';
import { api } from '@/lib/api';
import { reasonLabels, summarizeBundles } from '@/lib/bundle-metrics';

const number = new Intl.NumberFormat();
const date = (value: string) => new Date(value).toLocaleString();
const bytes = (value: number) => {
  if (value < 1_000_000) return `${number.format(value)} B`;
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
  return `${(value / 1_000_000_000).toFixed(2)} GB`;
};

function Stat({
  label,
  value,
  description,
  accent = false,
}: {
  label: string;
  value: string;
  description: string;
  accent?: boolean;
}) {
  return (
    <section
      className={`rounded-xl border p-5 ${accent ? 'border-orange-200 bg-orange-50' : 'border-slate-200 bg-white'}`}>
      <h2 className="text-sm font-medium text-slate-600">{label}</h2>
      <p
        className={`mt-3 text-3xl font-semibold tracking-tight tabular-nums ${accent ? 'text-orange-700' : 'text-slate-900'}`}>
        {value}
      </p>
      <p className="mt-2 text-xs leading-5 text-slate-500">{description}</p>
    </section>
  );
}

export const BundleMetrics = () => {
  const [platform, setPlatform] = useState('all');
  const previousInstance = useRef<string | undefined>(undefined);
  const [instanceChanged, setInstanceChanged] = useState(false);
  const { data, error, isPending, isFetching, refetch } = useQuery({
    queryKey: ['bundle-metrics'],
    queryFn: ({ signal }) => api.getBundleMetrics(signal),
    staleTime: 10_000,
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
    retry: 1,
  });
  useEffect(() => {
    if (!data) return;
    if (previousInstance.current && previousInstance.current !== data.instanceId) {
      setInstanceChanged(true);
    }
    previousInstance.current = data.instanceId;
  }, [data]);

  const stats = data ? summarizeBundles(data.rows, platform) : null;
  const total = stats ? stats.patches + stats.full : 0;
  const deliveries = stats
    ? [
        { label: 'Patch · direct', count: stats.directPatches, color: 'bg-orange-500' },
        { label: 'Patch · CDN redirect', count: stats.cdnPatches, color: 'bg-orange-300' },
        { label: 'Full bundle · direct', count: stats.directFull, color: 'bg-slate-700' },
        { label: 'Full bundle · CDN redirect', count: stats.cdnFull, color: 'bg-slate-400' },
      ]
    : [];

  return (
    <div className="mx-auto max-w-6xl space-y-6 pb-10 text-slate-900">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-widest text-orange-700">
            <Activity className="h-4 w-4" aria-hidden="true" /> OTA delivery
          </div>
          <h1 className="text-3xl font-semibold tracking-tight">Bundle metrics</h1>
          <p className="mt-2 text-sm text-slate-500">
            See when patches are offered and why full bundles are requested.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void refetch()}
          disabled={isFetching}
          className="flex items-center gap-2 rounded-lg border border-slate-200 bg-white px-4 py-2 text-sm font-medium hover:bg-slate-50 disabled:opacity-50">
          <RefreshCw className={`h-4 w-4 ${isFetching ? 'animate-spin' : ''}`} aria-hidden="true" />
          {isFetching ? 'Refreshing…' : 'Refresh'}
        </button>
      </header>

      <div className="rounded-xl border border-slate-200 bg-slate-50 px-5 py-4 text-sm">
        <p className="font-medium">Current server only · since process start</p>
        <p className="mt-1 leading-6 text-slate-500">
          Counters reset on restart. Other replicas are not included. Counts represent HTTP
          responses and redirect decisions, not unique users or successful installs.
        </p>
        {data && (
          <p className="mt-2 text-xs text-slate-500">
            Started {date(data.startedAt)} · Instance {data.instanceId.slice(0, 8)} · Updated{' '}
            {date(data.sampledAt)}
          </p>
        )}
      </div>

      {instanceChanged && (
        <p
          role="status"
          className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
          The responding server instance changed. These totals replace the previous snapshot; they
          are not combined across instances.
        </p>
      )}
      {error && (
        <div
          role="alert"
          className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-800">
          {data
            ? 'Refresh failed. Showing the last available snapshot.'
            : 'Bundle metrics could not be loaded.'}{' '}
          Use Refresh to try again.
        </div>
      )}
      {isPending && (
        <div
          role="status"
          className="rounded-xl border border-slate-200 p-12 text-center text-sm text-slate-500">
          Loading bundle metrics…
        </div>
      )}

      {data && stats && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              <label htmlFor="bundle-platform" className="text-sm font-medium">
                Platform
              </label>
              <select
                id="bundle-platform"
                value={platform}
                onChange={event => setPlatform(event.target.value)}
                className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm">
                <option value="all">All platforms</option>
                <option value="ios">iOS</option>
                <option value="android">Android</option>
                {data.rows.some(row => row.platform === 'unknown') && (
                  <option value="unknown">Unknown</option>
                )}
              </select>
            </div>
            <span className="text-xs text-slate-500">Refreshes every 15 seconds while visible</span>
          </div>

          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <Stat
              label="Patch share"
              value={stats.patchShare === null ? '—' : `${stats.patchShare.toFixed(1)}%`}
              description="Patch ÷ (patch + full) responses. Excludes HTTP and write errors."
              accent
            />
            <Stat
              label="Patch responses"
              value={number.format(stats.patches)}
              description="Direct patches + CDN redirect decisions."
            />
            <Stat
              label="Full bundle responses"
              value={number.format(stats.full)}
              description="Direct full bundles + CDN redirect decisions."
            />
            <Stat
              label="Full after patch"
              value={number.format(stats.fallbacks)}
              description="Correlated full requests within 10 minutes of a matching patch attempt."
            />
          </div>

          {total + stats.failures === 0 ? (
            <section className="rounded-xl border border-dashed border-slate-300 bg-white px-6 py-14 text-center">
              <ArrowDownToLine className="mx-auto mb-4 h-7 w-7 text-slate-400" aria-hidden="true" />
              <h2 className="font-semibold">No bundle requests yet</h2>
              <p className="mx-auto mt-2 max-w-md text-sm leading-6 text-slate-500">
                No launch-bundle responses were recorded for this platform on this server. Images
                and fonts are excluded.
              </p>
            </section>
          ) : (
            <>
              <div className="grid gap-6 lg:grid-cols-2">
                <section className="rounded-xl border border-slate-200 bg-white p-6">
                  <div className="flex items-center justify-between gap-3">
                    <h2 className="font-semibold">Delivery mix</h2>
                    <span className="text-xs text-slate-500">{number.format(total)} responses</span>
                  </div>
                  <div
                    role="img"
                    aria-label={deliveries.map(item => `${item.label}: ${item.count}`).join(', ')}
                    className="my-6 flex h-4 overflow-hidden rounded-full bg-slate-100">
                    {deliveries.map(item => (
                      <div
                        key={item.label}
                        aria-hidden="true"
                        className={item.color}
                        style={{ width: `${total ? (item.count / total) * 100 : 0}%` }}
                      />
                    ))}
                  </div>
                  <div className="space-y-4">
                    {deliveries.map(item => (
                      <div
                        key={item.label}
                        className="flex items-center justify-between gap-3 text-sm">
                        <span className="flex items-center gap-2 text-slate-600">
                          <span
                            className={`h-2.5 w-2.5 rounded-full ${item.color}`}
                            aria-hidden="true"
                          />
                          {item.label}
                        </span>
                        <span className="font-medium tabular-nums">
                          {number.format(item.count)}
                        </span>
                      </div>
                    ))}
                  </div>
                </section>

                <section className="rounded-xl border border-slate-200 bg-white p-6">
                  <h2 className="font-semibold">What the server can observe</h2>
                  <div className="mt-5 flex items-baseline justify-between gap-4 border-b border-slate-100 pb-4">
                    <span className="text-sm text-slate-600">Direct bundle bytes written</span>
                    <span className="text-xl font-semibold tabular-nums">
                      {bytes(stats.directBytes)}
                    </span>
                  </div>
                  <p className="mt-3 text-xs leading-5 text-slate-500">
                    Excludes CDN transfer bytes, redirect bodies, and interrupted responses. This is
                    not a bandwidth-savings estimate.
                  </p>
                  <div className="mt-5 flex items-center justify-between gap-3 text-sm">
                    <span className="flex items-center gap-2 text-slate-600">
                      <AlertTriangle className="h-4 w-4" aria-hidden="true" />
                      HTTP / write errors
                    </span>
                    <span className="font-semibold tabular-nums">
                      {number.format(stats.failures)}
                    </span>
                  </div>
                  <p className="mt-5 rounded-lg bg-slate-50 p-3 text-xs leading-5 text-slate-500">
                    A full request after a patch is a correlation, not a confirmed patch failure.
                    Repeated requests can count more than once. CDN cache hits and completed
                    installations are not measured here.
                  </p>
                </section>
              </div>

              <section className="overflow-hidden rounded-xl border border-slate-200 bg-white">
                <div className="border-b border-slate-100 px-6 py-5">
                  <h2 className="font-semibold">Why full bundles were selected</h2>
                  <p className="mt-1 text-xs text-slate-500">
                    Includes normal full downloads and correlated retries.
                  </p>
                </div>
                {stats.reasons.length === 0 ? (
                  <p className="p-6 text-sm text-slate-500">
                    No full-bundle responses in this snapshot.
                  </p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className="w-full text-left text-sm">
                      <thead className="bg-slate-50 text-xs text-slate-500">
                        <tr>
                          <th scope="col" className="px-6 py-3 font-medium">
                            Reason
                          </th>
                          <th scope="col" className="px-4 py-3 text-right font-medium">
                            Requests
                          </th>
                          <th
                            scope="col"
                            className="whitespace-nowrap px-6 py-3 text-right font-medium">
                            % of full
                          </th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100">
                        {stats.reasons.map(item => (
                          <tr key={item.reason}>
                            <td className="min-w-56 px-6 py-4">
                              <p className="font-medium">
                                {reasonLabels[item.reason]?.[0] ?? item.reason.replace(/_/g, ' ')}
                              </p>
                              <p className="mt-1 max-w-xl text-xs leading-5 text-slate-500">
                                {reasonLabels[item.reason]?.[1] ??
                                  'Other full-bundle response recorded by the server.'}
                              </p>
                            </td>
                            <td className="px-4 py-4 text-right font-medium tabular-nums">
                              {number.format(item.requests)}
                            </td>
                            <td className="px-6 py-4 text-right tabular-nums text-slate-500">
                              {stats.full ? ((item.requests / stats.full) * 100).toFixed(1) : '0.0'}
                              %
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            </>
          )}
        </>
      )}
    </div>
  );
};
