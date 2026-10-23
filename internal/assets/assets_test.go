package assets

import (
	"sync/atomic"
	"testing"
	"time"

	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"

	"github.com/stretchr/testify/require"
)

const testUpdatesPath = "../../test/test-updates"

// heldBucket counts reads and, while held, makes each read wait for release.
type heldBucket struct {
	bucket.Bucket
	reads   atomic.Int32
	held    atomic.Bool
	removed atomic.Bool
	release chan struct{}
}

func (b *heldBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	b.reads.Add(1)
	if b.held.Load() {
		<-b.release
	}
	if b.removed.Load() {
		return nil, nil
	}
	return b.Bucket.GetFile(update, assetPath)
}

func useHeldBucket(t *testing.T) *heldBucket {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", testUpdatesPath)
	storage := &heldBucket{Bucket: &bucket.LocalBucket{BasePath: testUpdatesPath}, release: make(chan struct{})}
	bucket.SetBucketInstance(storage)
	t.Cleanup(bucket.ResetBucketInstance)
	require.NoError(t, cache.GetCache().Clear())
	t.Cleanup(func() { _ = cache.GetCache().Clear() })
	return storage
}

func makeChecksStale(t *testing.T) {
	freshFor := pinnedUpdateFreshFor
	pinnedUpdateFreshFor = 0
	t.Cleanup(func() { pinnedUpdateFreshFor = freshFor })
}

var pinnedRequest = AssetsRequest{Branch: "branch-1", RuntimeVersion: "1", Platform: "android", UpdateID: "1674170951"}

func TestStalePinnedUpdateIsAnsweredWithoutWaitingForStorage(t *testing.T) {
	storage := useHeldBucket(t)
	_, err := ResolveUpdate(pinnedRequest)
	require.NoError(t, err)
	makeChecksStale(t)
	storage.held.Store(true)
	defer func() {
		close(storage.release)
		// Wait for the background check, which reads the global bucket.
		_, _, _ = pinnedUpdateChecks.Do(pinnedUpdateKey(pinnedRequest), func() (interface{}, error) { return nil, nil })
	}()
	readsBefore := storage.reads.Load()

	done := make(chan error, 1)
	go func() { _, err := ResolveUpdate(pinnedRequest); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an asset request waited for storage to re-check its update")
	}
	require.Eventually(t, func() bool { return storage.reads.Load() > readsBefore }, 5*time.Second, time.Millisecond,
		"the check runs in the background")
}

func TestRemovedPinnedUpdateStopsBeingServedAfterBackgroundCheck(t *testing.T) {
	storage := useHeldBucket(t)
	_, err := ResolveUpdate(pinnedRequest)
	require.NoError(t, err)
	makeChecksStale(t)
	storage.removed.Store(true)

	require.Eventually(t, func() bool {
		_, err := ResolveUpdate(pinnedRequest)
		return err != nil
	}, 5*time.Second, time.Millisecond)
}

func TestPinnedUpdateOfOtherPlatformIsRejected(t *testing.T) {
	useHeldBucket(t)
	ios := pinnedRequest
	ios.Platform = "ios"
	_, err := ResolveUpdate(ios)
	require.Error(t, err)
}
