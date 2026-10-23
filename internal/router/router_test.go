package infrastructure

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

type fakeProcess struct {
	storage   *countingBucket
	expoCalls atomic.Int32
}

// startFreshProcess gives the test what a restarted server has: empty caches,
// the fixture bucket behind a read counter, and an Expo app whose channel
// staging maps to branch-1, next to a channel with an unreadable mapping.
func startFreshProcess(t *testing.T) *fakeProcess {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", testUpdatesPath)
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("EXPO_APP_ID", "app-id")
	t.Setenv("EXPO_ACCESS_TOKEN", "token")
	process := &fakeProcess{storage: &countingBucket{Bucket: &bucket.LocalBucket{BasePath: testUpdatesPath}}}
	bucket.SetBucketInstance(process.storage)
	t.Cleanup(bucket.ResetBucketInstance)
	require.NoError(t, cache2.GetCache().Clear())
	t.Cleanup(func() { _ = cache2.GetCache().Clear() })

	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)
	serveExpoChannels(process, nil)
	return process
}

// serveExpoChannels answers the warm-up's Expo query with the staging and
// broken channels, plus extra channels that map to branches with their names.
func serveExpoChannels(process *fakeProcess, extra []string) {
	branches := []map[string]any{{"id": "branch-1-id", "name": "branch-1"}}
	channels := []map[string]any{
		{"id": "broken-id", "name": "broken", "branchMapping": "not json"},
		{
			"id":            "staging-id",
			"name":          "staging",
			"branchMapping": `{"version":0,"data":[{"branchId":"branch-1-id","branchMappingLogic":"true"}]}`,
		},
	}
	for _, name := range extra {
		branches = append(branches, map[string]any{"id": name + "-id", "name": name})
		channels = append(channels, map[string]any{
			"id":            name + "-channel-id",
			"name":          name,
			"branchMapping": `{"version":0,"data":[{"branchId":"` + name + `-id","branchMappingLogic":"true"}]}`,
		})
	}
	httpmock.RegisterResponder(http.MethodPost, "https://api.expo.dev/graphql", func(req *http.Request) (*http.Response, error) {
		process.expoCalls.Add(1)
		if req.Header.Get("operationName") != "FetchExpoBranches" {
			return httpmock.NewStringResponse(http.StatusNotFound, "unknown operation"), nil
		}
		return httpmock.NewJsonResponse(http.StatusOK, map[string]any{
			"data": map[string]any{"app": map[string]any{"byId": map[string]any{
				"updateBranches": branches,
				"updateChannels": channels,
			}}},
		})
	})
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

func TestFirstManifestAfterBootReadsNoStorageAndCallsNoExpo(t *testing.T) {
	process := startFreshProcess(t)
	server := bootServer(t)
	warmUpReads, warmUpExpoCalls := process.storage.reads.Load(), process.expoCalls.Load()

	res := getManifest(server)
	manifestReads := process.storage.reads.Load() - warmUpReads
	t.Logf("bucket reads: boot warm-up=%d first manifest=%d", warmUpReads, manifestReads)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"launchAsset"`)
	require.Zero(t, manifestReads, "the first manifest after boot must come from warm caches")
	require.Equal(t, int32(1), warmUpExpoCalls, "the warm-up resolves every channel with one Expo call")
	require.Equal(t, warmUpExpoCalls, process.expoCalls.Load(), "the first manifest after boot must not call Expo")
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

// slowPreviewBucket holds the listing of preview branches until release closes,
// then fails it, so the warm-up reads no more storage after release.
type slowPreviewBucket struct {
	*countingBucket
	listed  chan struct{}
	release chan struct{}
}

func (b *slowPreviewBucket) GetRuntimeVersions(branch string) ([]bucket.RuntimeVersionWithStats, error) {
	if !strings.HasPrefix(branch, "preview-pr-") {
		return b.countingBucket.GetRuntimeVersions(branch)
	}
	close(b.listed)
	<-b.release
	return nil, errors.New("storage is slow")
}

func TestHealthCheckPassesOnceUserBranchesAreWarm(t *testing.T) {
	process := startFreshProcess(t)
	serveExpoChannels(process, []string{"preview-pr-1"})
	storage := &slowPreviewBucket{countingBucket: process.storage, listed: make(chan struct{}), release: make(chan struct{})}
	bucket.SetBucketInstance(storage)
	t.Cleanup(func() { close(storage.release) })
	server := HoldHealthCheck(update.StartBootWarmUp(time.Minute), NewRouter())

	require.Eventually(t, func() bool { return get(server, "/hc").Code == http.StatusOK }, 5*time.Second, time.Millisecond,
		"a slow preview branch does not hold /hc")
	<-storage.listed
	readsBefore := process.storage.reads.Load()
	res := getManifest(server)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, readsBefore, process.storage.reads.Load(), "the user branch was warm before /hc passed")
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
