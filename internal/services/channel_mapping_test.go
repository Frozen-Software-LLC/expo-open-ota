package services

import (
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cache2 "expo-open-ota/internal/cache"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"
)

const (
	testChannel   = "staging"
	fakeExpoDelay = 150 * time.Millisecond
)

// fakeExpo answers the channel mapping query after fakeExpoDelay with the
// branch it held when the request arrived.
type fakeExpo struct {
	calls  atomic.Int32
	branch atomic.Value
	// When set, the next call waits for this channel to close.
	hold atomic.Pointer[chan struct{}]
}

func newFakeExpo(t *testing.T, branch string) *fakeExpo {
	t.Setenv("EXPO_APP_ID", "app-id")
	t.Setenv("EXPO_ACCESS_TOKEN", "token")
	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)
	expo := &fakeExpo{}
	expo.branch.Store(branch)
	httpmock.RegisterResponder(http.MethodPost, "https://api.expo.dev/graphql", func(req *http.Request) (*http.Response, error) {
		branch := expo.branch.Load().(string)
		expo.calls.Add(1)
		if hold := expo.hold.Swap(nil); hold != nil {
			<-*hold
		}
		time.Sleep(fakeExpoDelay)
		return httpmock.NewJsonResponse(http.StatusOK, map[string]any{
			"data": map[string]any{"app": map[string]any{"byId": map[string]any{
				"updateBranches": []map[string]any{
					{"id": "branch-1-id", "name": "branch-1"},
					{"id": "branch-2-id", "name": "branch-2"},
				},
				"updateChannelByName": map[string]any{
					"id":            "staging-id",
					"branchMapping": `{"version":0,"data":[{"branchId":"` + branch + `-id","branchMappingLogic":"true"}]}`,
				},
			}}},
		})
	})
	cache2.GetCache().Delete(ComputeChannelMappingCacheKey(testChannel))
	t.Cleanup(func() { cache2.GetCache().Delete(ComputeChannelMappingCacheKey(testChannel)) })
	return expo
}

// ageChannelMapping makes the cached mapping look older than the freshness
// window and returns the value it left in the cache. A 60 s TTL entry that
// old has expired.
func ageChannelMapping(t *testing.T, channel string) string {
	t.Helper()
	cache2.GetCache().Delete(ComputeChannelMappingCacheKey(channel))
	return ""
}

// remapChannel does to the cache what the dashboard remap handler does.
func remapChannel(channel string) {
	cache2.GetCache().Delete(ComputeChannelMappingCacheKey(channel))
}

func waitForCacheChange(t *testing.T, key, old string) {
	t.Helper()
	require.Eventually(t, func() bool { return cache2.GetCache().Get(key) != old }, 5*time.Second, time.Millisecond)
}

func summarize(latencies []time.Duration) (median, low, high time.Duration) {
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[0], sorted[len(sorted)-1]
}

func TestStaleChannelMappingDoesNotWaitForExpo(t *testing.T) {
	expo := newFakeExpo(t, "branch-1")
	_, err := FetchExpoChannelMapping(testChannel)
	require.NoError(t, err)
	key := ComputeChannelMappingCacheKey(testChannel)

	latencies := make([]time.Duration, 7)
	for i := range latencies {
		aged := ageChannelMapping(t, testChannel)
		calls := expo.calls.Load()
		start := time.Now()
		mapping, err := FetchExpoChannelMapping(testChannel)
		latencies[i] = time.Since(start)
		require.NoError(t, err)
		require.Equal(t, "branch-1", mapping.BranchName)
		waitForCacheChange(t, key, aged)
		require.Equal(t, calls+1, expo.calls.Load(), "one Expo call per stale read")
	}
	median, low, high := summarize(latencies)
	t.Logf("stale channel mapping read: median=%v range=%v..%v runs=%d expo-delay=%v", median, low, high, len(latencies), fakeExpoDelay)
	require.Less(t, median, 10*time.Millisecond, "a stale read must not wait for Expo")
}

func TestStaleChannelMappingRefreshesOnce(t *testing.T) {
	expo := newFakeExpo(t, "branch-1")
	_, err := FetchExpoChannelMapping(testChannel)
	require.NoError(t, err)
	expo.branch.Store("branch-2")
	aged := ageChannelMapping(t, testChannel)
	calls := expo.calls.Load()

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := FetchExpoChannelMapping(testChannel); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	require.NoError(t, <-errs)
	waitForCacheChange(t, ComputeChannelMappingCacheKey(testChannel), aged)
	require.Equal(t, calls+1, expo.calls.Load(), "concurrent stale reads share one Expo call")

	mapping, err := FetchExpoChannelMapping(testChannel)
	require.NoError(t, err)
	require.Equal(t, "branch-2", mapping.BranchName, "a later read sees the refreshed mapping")
}

// A remap during a refresh must win: the next read fetches the new mapping,
// and the refresh that started before the remap must not write over it.
func TestRemapDuringChannelMappingRefresh(t *testing.T) {
	expo := newFakeExpo(t, "branch-1")
	_, err := FetchExpoChannelMapping(testChannel)
	require.NoError(t, err)
	release := make(chan struct{})
	expo.hold.Store(&release)
	ageChannelMapping(t, testChannel)
	staleRead := make(chan error, 1)
	go func() {
		_, err := FetchExpoChannelMapping(testChannel)
		staleRead <- err
	}()
	require.Eventually(t, func() bool { return expo.calls.Load() == 2 }, 5*time.Second, time.Millisecond)

	expo.branch.Store("branch-2")
	remapChannel(testChannel)
	start := time.Now()
	mapping, err := FetchExpoChannelMapping(testChannel)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, "branch-2", mapping.BranchName)
	require.GreaterOrEqual(t, elapsed, fakeExpoDelay, "the read after a remap waits for Expo")

	close(release)
	require.NoError(t, <-staleRead)
	require.Never(t, func() bool {
		mapping, err := FetchExpoChannelMapping(testChannel)
		return err != nil || mapping.BranchName != "branch-2"
	}, 3*fakeExpoDelay, 5*time.Millisecond, "the pre-remap refresh wrote the old mapping")
}
