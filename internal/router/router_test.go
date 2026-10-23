package infrastructure

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"
)

const testUpdatesPath = "../../test/test-updates"

type countingBucket struct {
	bucket.Bucket
	reads atomic.Int32
}

func (b *countingBucket) GetBranches() ([]string, error) {
	b.reads.Add(1)
	return b.Bucket.GetBranches()
}

func (b *countingBucket) GetRuntimeVersions(branch string) ([]bucket.RuntimeVersionWithStats, error) {
	b.reads.Add(1)
	return b.Bucket.GetRuntimeVersions(branch)
}

func (b *countingBucket) GetUpdates(branch, runtimeVersion string) ([]types.Update, error) {
	b.reads.Add(1)
	return b.Bucket.GetUpdates(branch, runtimeVersion)
}

func (b *countingBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	b.reads.Add(1)
	return b.Bucket.GetFile(update, assetPath)
}

// startFreshProcess gives the test what a restarted server has: empty caches,
// the fixture bucket behind a read counter, and an Expo app whose only
// channel, staging, maps to branch-1.
func startFreshProcess(t *testing.T) *countingBucket {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", testUpdatesPath)
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("EXPO_APP_ID", "app-id")
	t.Setenv("EXPO_ACCESS_TOKEN", "token")
	storage := &countingBucket{Bucket: &bucket.LocalBucket{BasePath: testUpdatesPath}}
	bucket.SetBucketInstance(storage)
	t.Cleanup(bucket.ResetBucketInstance)
	require.NoError(t, cache2.GetCache().Clear())
	t.Cleanup(func() { _ = cache2.GetCache().Clear() })

	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)
	httpmock.RegisterResponder(http.MethodPost, "https://api.expo.dev/graphql", func(req *http.Request) (*http.Response, error) {
		switch req.Header.Get("operationName") {
		case "FetchExpoChannels":
			return httpmock.NewJsonResponse(http.StatusOK, map[string]any{
				"data": map[string]any{"app": map[string]any{"byId": map[string]any{
					"updateChannels": []map[string]any{{"id": "staging-id", "name": "staging"}},
				}}},
			})
		case "FetchExpoChannelMapping":
			return httpmock.NewJsonResponse(http.StatusOK, map[string]any{
				"data": map[string]any{"app": map[string]any{"byId": map[string]any{
					"updateBranches": []map[string]any{{"id": "branch-1-id", "name": "branch-1"}},
					"updateChannelByName": map[string]any{
						"id":            "staging-id",
						"branchMapping": `{"version":0,"data":[{"branchId":"branch-1-id","branchMappingLogic":"true"}]}`,
					},
				}}},
			})
		}
		return httpmock.NewStringResponse(http.StatusNotFound, "unknown operation"), nil
	})
	return storage
}

// bootServer serves what main serves and returns once /hc reports healthy,
// which is when Render starts sending it traffic.
func bootServer(t *testing.T) http.Handler {
	t.Helper()
	server := HoldHealthCheck(update.StartBootWarmUp(5*time.Second), NewRouter())
	require.Eventually(t, func() bool { return get(server, "/hc").Code == http.StatusOK }, 5*time.Second, time.Millisecond)
	return server
}

func get(server http.Handler, path string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	server.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
	return res
}

func getManifest(server http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	req.Header.Set("expo-channel-name", "staging")
	req.Header.Set("expo-platform", "android")
	req.Header.Set("expo-runtime-version", "1")
	req.Header.Set("expo-protocol-version", "1")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	return res
}

func TestFirstManifestAfterBootReadsNoStorage(t *testing.T) {
	storage := startFreshProcess(t)
	server := bootServer(t)
	warmUpReads := storage.reads.Load()

	res := getManifest(server)
	manifestReads := storage.reads.Load() - warmUpReads
	t.Logf("bucket reads: boot warm-up=%d first manifest=%d", warmUpReads, manifestReads)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"launchAsset"`)
	require.Zero(t, manifestReads, "the first manifest after boot must come from warm caches")
}

// holdExpo makes Expo calls fail, but only once release is called or the test ends.
func holdExpo(t *testing.T) (release func()) {
	entered, held := make(chan struct{}), make(chan struct{})
	var enter, unhold sync.Once
	httpmock.RegisterResponder(http.MethodPost, "https://api.expo.dev/graphql", func(*http.Request) (*http.Response, error) {
		enter.Do(func() { close(entered) })
		<-held
		return httpmock.NewStringResponse(http.StatusBadGateway, "expo is down"), nil
	})
	release = func() { unhold.Do(func() { close(held) }) }
	// The warm-up must reach Expo before httpmock is deactivated.
	t.Cleanup(func() { <-entered; release() })
	return release
}

func TestHealthCheckWaitsForBootWarmUp(t *testing.T) {
	startFreshProcess(t)
	release := holdExpo(t)
	server := HoldHealthCheck(update.StartBootWarmUp(time.Minute), NewRouter())

	require.Equal(t, http.StatusServiceUnavailable, get(server, "/hc").Code)
	require.Equal(t, http.StatusBadRequest, get(server, "/manifest").Code, "other routes answer during the warm-up")
	release()
	require.Eventually(t, func() bool { return get(server, "/hc").Code == http.StatusOK }, 5*time.Second, time.Millisecond,
		"a failed warm-up ends the wait")
}

func TestHealthCheckPassesAfterDeadlineWhenWarmUpHangs(t *testing.T) {
	startFreshProcess(t)
	holdExpo(t)
	server := HoldHealthCheck(update.StartBootWarmUp(100*time.Millisecond), NewRouter())

	require.Equal(t, http.StatusServiceUnavailable, get(server, "/hc").Code)
	require.Eventually(t, func() bool { return get(server, "/hc").Code == http.StatusOK }, 2*time.Second, time.Millisecond)
}

type panickingBucket struct{ bucket.Bucket }

func (panickingBucket) GetRuntimeVersions(string) ([]bucket.RuntimeVersionWithStats, error) {
	panic("storage exploded")
}

func TestHealthCheckPassesWhenWarmUpPanics(t *testing.T) {
	startFreshProcess(t)
	bucket.SetBucketInstance(panickingBucket{})
	server := HoldHealthCheck(update.StartBootWarmUp(time.Minute), NewRouter())

	require.Eventually(t, func() bool { return get(server, "/hc").Code == http.StatusOK }, 5*time.Second, time.Millisecond)
}
