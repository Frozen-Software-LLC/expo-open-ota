package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"

	"github.com/stretchr/testify/require"
)

// The short-circuit scan must pick the same update as the full filter, also
// when concurrent cold-cache misses share one scan.
func TestLatestUpdateScanMatchesFullFilterUnderConcurrency(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	for _, platform := range []string{"ios", "android"} {
		all, err := GetAllUpdatesForRuntimeVersion("branch-2", "1", platform)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		for _, u := range all {
			if IsUpdateValid(u) {
				want = u.UpdateId
				break
			}
		}
		if want == "" {
			t.Fatalf("fixture has no valid %s update", platform)
		}

		cache2.GetCache().Delete(ComputeLastUpdateCacheKey("branch-2", "1", platform))
		// Hold the scan open so that all callers miss the cache while it runs.
		var scans atomic.Int32
		scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
			scans.Add(1)
			time.Sleep(200 * time.Millisecond)
			return scanLatestUpdate(branch, runtimeVersion, platform)
		}
		t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })
		var wg sync.WaitGroup
		got := make([]string, 32)
		for i := range got {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				latest, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", platform)
				if err == nil && latest != nil {
					got[i] = latest.UpdateId
				}
			}(i)
		}
		wg.Wait()
		for i, id := range got {
			if id != want {
				t.Fatalf("%s call %d: got %q, want %q", platform, i, id, want)
			}
		}
		if n := scans.Load(); n != 1 {
			t.Fatalf("%s: %d storage scans, want 1", platform, n)
		}
	}
}

// A scan that started before a publish must not cache its older result.
func TestLatestUpdateScanDuringPublishDoesNotCache(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	cacheKey := ComputeLastUpdateCacheKey("branch-2", "1", "ios")
	cache2.GetCache().Delete(cacheKey)
	entered, release := make(chan struct{}), make(chan struct{})
	scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
		close(entered)
		<-release
		return scanLatestUpdate(branch, runtimeVersion, platform)
	}
	t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })

	done := make(chan *types.Update)
	go func() {
		latest, _ := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", "ios")
		done <- latest
	}()
	<-entered
	// What MarkUpdateAsChecked does after it writes .check.
	invalidateLatestUpdate(cacheKey)
	close(release)
	if <-done == nil {
		t.Fatal("scan found no update")
	}
	if cached := cache2.GetCache().Get(cacheKey); cached != "" {
		t.Fatalf("stale scan cached %s", cached)
	}
}

// A stuck scan must not block callers forever, and the next call must start
// a new scan instead of joining the stuck one.
func TestLatestUpdateScanTimeoutStartsNewScan(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	cache2.GetCache().Delete(ComputeLastUpdateCacheKey("branch-2", "1", "ios"))
	latestUpdateScanTimeout = 50 * time.Millisecond
	t.Cleanup(func() { latestUpdateScanTimeout = 30 * time.Second })
	release, stuckDone := make(chan struct{}), make(chan struct{})
	var scans atomic.Int32
	scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
		if scans.Add(1) == 1 {
			defer close(stuckDone)
			<-release
		}
		return scanLatestUpdate(branch, runtimeVersion, platform)
	}
	t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })
	// Let the stuck scan end before the next test changes the bucket.
	defer func() { close(release); <-stuckDone }()

	if _, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", "ios"); err == nil {
		t.Fatal("stuck scan: want timeout error")
	}
	latest, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", "ios")
	if err != nil || latest == nil {
		t.Fatalf("second call: got %v, %v", latest, err)
	}
	if n := scans.Load(); n != 2 {
		t.Fatalf("%d scans, want 2", n)
	}
}

// An update whose metadata cannot be read must fail the scan, not fall
// through to an older update.
func TestLatestUpdateScanReturnsReadErrors(t *testing.T) {
	base := t.TempDir()
	older := filepath.Join(base, "b", "1", "100")
	if err := os.MkdirAll(older, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(older, "update-metadata.json"), []byte(`{"platform":"ios"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(older, ".check"), []byte(".check"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory makes the metadata read fail.
	if err := os.MkdirAll(filepath.Join(base, "b", "1", "200", "update-metadata.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", base)
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	if latest, err := scanLatestUpdate("b", "1", "ios"); err == nil {
		t.Fatalf("got %v, want read error", latest)
	}
}

// A waiter that joined a scan late and times out after a replacement scan
// started must not move later requests off the replacement scan.
func TestLatestUpdateLateTimeoutKeepsReplacementScan(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	cacheKey := ComputeLastUpdateCacheKey("branch-2", "1", "ios")
	cache2.GetCache().Delete(cacheKey)
	startEpoch := latestUpdateFlightEpoch(cacheKey)
	latestUpdateScanTimeout = 200 * time.Millisecond
	t.Cleanup(func() { latestUpdateScanTimeout = 30 * time.Second })
	release1, release2, stuckDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var scans atomic.Int32
	scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
		switch scans.Add(1) {
		case 1:
			defer close(stuckDone)
			<-release1
		case 2:
			<-release2
		}
		return scanLatestUpdate(branch, runtimeVersion, platform)
	}
	t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })
	defer func() { close(release1); <-stuckDone }()

	call := func() chan error {
		done := make(chan error, 1)
		go func() {
			_, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", "ios")
			done <- err
		}()
		return done
	}
	a := call()
	time.Sleep(100 * time.Millisecond)
	b := call() // joins the stuck scan late
	if <-a == nil {
		t.Fatal("first waiter: want timeout error")
	}
	c := call() // starts the replacement scan
	if <-b == nil {
		t.Fatal("late waiter: want timeout error")
	}
	if got := latestUpdateFlightEpoch(cacheKey); got != startEpoch+1 {
		t.Fatalf("epoch advanced %d times, want 1", got-startEpoch)
	}
	d := call() // must join the replacement scan
	time.Sleep(20 * time.Millisecond)
	close(release2)
	if err := <-c; err != nil {
		t.Fatal(err)
	}
	if err := <-d; err != nil {
		t.Fatal(err)
	}
	if n := scans.Load(); n != 2 {
		t.Fatalf("%d scans, want 2", n)
	}
}

// Keys that never timed out must not keep timeout state in memory.
func TestLatestUpdateFlightEpochOnlyStoredOnTimeout(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	cacheKey := ComputeLastUpdateCacheKey("branch-2", "no-such-runtime", "ios")
	if _, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "no-such-runtime", "ios"); err != nil {
		t.Fatal(err)
	}
	if _, ok := latestUpdateFlightEpochs.Load(cacheKey); ok {
		t.Fatal("timeout state stored for a key that never timed out")
	}
}

const fakeScanDelay = 150 * time.Millisecond

// ageLatestUpdate makes the cached latest update look older than the
// freshness window and returns the value it left in the cache.
func ageLatestUpdate(t *testing.T, cacheKey string) string {
	t.Helper()
	cached, ok := readLatestUpdate(cacheKey)
	require.True(t, ok)
	cached.FetchedAt = time.Now().Add(-latestUpdateFreshFor)
	value, err := json.Marshal(cached)
	require.NoError(t, err)
	ttl := 1800
	require.NoError(t, cache2.GetCache().Set(cacheKey, string(value), &ttl))
	return string(value)
}

// fakeLatestUpdateScan makes every scan take fakeScanDelay and find the update
// whose ID head holds when the scan starts.
func fakeLatestUpdateScan(t *testing.T, head *atomic.Value) *atomic.Int32 {
	t.Helper()
	var scans atomic.Int32
	scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
		updateId := head.Load().(string)
		scans.Add(1)
		time.Sleep(fakeScanDelay)
		return &types.Update{Branch: branch, RuntimeVersion: runtimeVersion, UpdateId: updateId}, nil
	}
	t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })
	return &scans
}

func latestUpdateId(branch string) string {
	latest, err := GetLatestUpdateBundlePathForRuntimeVersion(branch, "1", "ios")
	if err != nil || latest == nil {
		return ""
	}
	return latest.UpdateId
}

func freshLatestUpdateKey(t *testing.T, branch string) string {
	t.Helper()
	cacheKey := ComputeLastUpdateCacheKey(branch, "1", "ios")
	cache2.GetCache().Delete(cacheKey)
	t.Cleanup(func() { cache2.GetCache().Delete(cacheKey) })
	return cacheKey
}

func waitForCacheChange(t *testing.T, key, old string) {
	t.Helper()
	require.Eventually(t, func() bool { return cache2.GetCache().Get(key) != old }, 5*time.Second, time.Millisecond)
}

func TestStaleLatestUpdateDoesNotWaitForScan(t *testing.T) {
	var head atomic.Value
	head.Store("100")
	scans := fakeLatestUpdateScan(t, &head)

	latencies := make([]time.Duration, 7)
	for i := range latencies {
		// A refresh stays in flight for a moment after it writes the cache, and a
		// read that joins it starts no scan, so each run uses its own key.
		branch := fmt.Sprintf("stale-branch-%d", i)
		cacheKey := freshLatestUpdateKey(t, branch)
		require.Equal(t, "100", latestUpdateId(branch))
		aged := ageLatestUpdate(t, cacheKey)
		before := scans.Load()
		start := time.Now()
		id := latestUpdateId(branch)
		latencies[i] = time.Since(start)
		require.Equal(t, "100", id)
		waitForCacheChange(t, cacheKey, aged)
		require.Equal(t, before+1, scans.Load(), "one scan per stale read")
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	median := latencies[len(latencies)/2]
	t.Logf("stale latest update read: median=%v range=%v..%v runs=%d scan-delay=%v", median, latencies[0], latencies[len(latencies)-1], len(latencies), fakeScanDelay)
	require.Less(t, median, 10*time.Millisecond, "a stale read must not wait for the scan")
}

func TestStaleLatestUpdateRefreshIsVisibleLater(t *testing.T) {
	cacheKey := freshLatestUpdateKey(t, "stale-branch")
	var head atomic.Value
	head.Store("100")
	fakeLatestUpdateScan(t, &head)
	require.Equal(t, "100", latestUpdateId("stale-branch"))

	head.Store("200")
	aged := ageLatestUpdate(t, cacheKey)
	require.NotEmpty(t, latestUpdateId("stale-branch"))
	waitForCacheChange(t, cacheKey, aged)
	require.Equal(t, "200", latestUpdateId("stale-branch"))
}

// A publish during a background refresh wins: the next read scans again, and
// the refresh that started before the publish never caches its older result.
func TestPublishDuringStaleRefreshWins(t *testing.T) {
	cacheKey := freshLatestUpdateKey(t, "stale-branch")
	var head atomic.Value
	head.Store("100")
	entered, release := make(chan struct{}), make(chan struct{})
	var scans atomic.Int32
	scanLatestUpdateFunc = func(branch, runtimeVersion, platform string) (*types.Update, error) {
		updateId := head.Load().(string)
		if scans.Add(1) == 2 {
			close(entered)
			<-release
		}
		return &types.Update{Branch: branch, RuntimeVersion: runtimeVersion, UpdateId: updateId}, nil
	}
	t.Cleanup(func() { scanLatestUpdateFunc = scanLatestUpdate })
	require.Equal(t, "100", latestUpdateId("stale-branch"))

	ageLatestUpdate(t, cacheKey)
	staleRead := make(chan string, 1)
	go func() { staleRead <- latestUpdateId("stale-branch") }()
	<-entered
	head.Store("200")
	// What MarkUpdateAsChecked does after it writes .check.
	invalidateLatestUpdate(cacheKey)
	require.Equal(t, "200", latestUpdateId("stale-branch"), "the read after a publish scans again")

	close(release)
	require.Equal(t, "100", <-staleRead)
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		require.Equal(t, "200", latestUpdateId("stale-branch"), "the refresh that started before the publish cached its result")
	}
	require.Equal(t, int32(3), scans.Load())
}
