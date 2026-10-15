package metrics

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleSnapshotEmptyAndDeliveryIdentity(t *testing.T) {
	bundleResponses, bundleResponseBytes = newBundleMetrics()
	empty, err := SnapshotBundleMetrics()
	require.NoError(t, err)
	require.NotNil(t, empty.Rows)
	require.Empty(t, empty.Rows)
	require.NotEmpty(t, empty.InstanceID)
	require.False(t, empty.StartedAt.After(empty.SampledAt))
	TrackBundleResponse("ios", "patch", "available", 123)
	TrackBundleResponse("ios", "patch", "available", 456)
	TrackBundleResponse("android", "patch_redirect", "cdn_available", 0)
	TrackBundleResponse("android", "full", "full_after_patch", 1000)
	TrackBundleResponse("android", "full_redirect", "patch_unavailable", 0)
	snapshot, err := SnapshotBundleMetrics()
	require.NoError(t, err)
	require.Equal(t, empty.InstanceID, snapshot.InstanceID)
	require.Equal(t, empty.StartedAt, snapshot.StartedAt)
	require.Equal(t, []BundleMetricRow{
		{Platform: "android", Delivery: "full", Reason: "full_after_patch", Requests: 1, Bytes: 1000},
		{Platform: "android", Delivery: "full_redirect", Reason: "patch_unavailable", Requests: 1},
		{Platform: "android", Delivery: "patch_redirect", Reason: "cdn_available", Requests: 1},
		{Platform: "ios", Delivery: "patch", Reason: "available", Requests: 2, Bytes: 579},
	}, snapshot.Rows)
}

func TestBundleSnapshotConcurrentTraffic(t *testing.T) {
	bundleResponses, bundleResponseBytes = newBundleMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				TrackBundleResponse("ios", "full", "not_negotiated", 200)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		_, err := SnapshotBundleMetrics()
		require.NoError(t, err)
	}
	wg.Wait()
	snapshot, err := SnapshotBundleMetrics()
	require.NoError(t, err)
	require.Equal(t, []BundleMetricRow{{Platform: "ios", Delivery: "full", Reason: "not_negotiated", Requests: 400, Bytes: 80000}}, snapshot.Rows)
}
