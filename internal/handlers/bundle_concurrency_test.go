package handlers

import (
	"bytes"
	"expo-open-ota/internal/cache"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/stretchr/testify/require"
)

// Use real local HTTP and (for CDN mode) TLS requests, without production
// credentials. Expire caches between waves instead of waiting 60 seconds.
// This is a concurrency regression test, not a production bandwidth benchmark.
func testConcurrentBundleDelivery(t *testing.T, platform string, base, target, patch []byte, edgeClient *http.Client) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(AssetsHandler))
	defer api.Close()
	client := &http.Client{
		Transport:     api.Client().Transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	oldObjects := bundlePatchObjects
	now := time.Now()
	bundlePatchObjects = newPatchObjectCache(func() time.Time { return now })
	t.Cleanup(func() { bundlePatchObjects = oldObjects })
	const workers, perWorker, waves = 16, 30, 3
	latencies := make([]time.Duration, workers*perWorker*waves)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for wave := 0; wave < waves; wave++ {
		// No requests are running while the test clock advances. This expires
		// both successful patch records and cached "no patch" results.
		now = now.Add(61 * time.Second)
		ttl := -1
		key := fmt.Sprintf("pinnedAssetUpdate:v1:local-diff:9090.1:%s:1700000000000", platform)
		require.NoError(t, cache.GetCache().Set(key, "valid", &ttl))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				<-start
				for request := 0; request < perWorker; request++ {
					index := wave*workers*perWorker + worker*perWorker + request
					began := time.Now()
					err := checkConcurrentBundleResponse(client, edgeClient, api.URL, platform, request%3, base, target, patch)
					latencies[index] = time.Since(began)
					if err != nil {
						t.Errorf("wave=%d worker=%d scenario=%d: %v", wave, worker, request%3, err)
					}
				}
			}(worker)
		}
		close(start)
		wg.Wait()
	}
	runtime.ReadMemStats(&after)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("%s cdn=%t requests=%d concurrency=%d expiry-waves=%d elapsed=%s p50=%s p95=%s heap-before=%d heap-after=%d allocated=%d",
		platform, edgeClient != nil, len(latencies), workers, waves, time.Since(started),
		latencies[len(latencies)/2], latencies[len(latencies)*95/100], before.HeapAlloc, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc)
}

func checkConcurrentBundleResponse(client, edgeClient *http.Client, apiURL, platform string, scenario int, base, target, patch []byte) error {
	req, err := http.NewRequest("GET", apiURL+"/assets?branch=local-diff&runtimeVersion=9090.1&updateId=1700000000000&platform="+platform+"&asset=bundle.hbc", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Expo-Current-Update-ID", baseUUID)
	req.Header.Set("Expo-Requested-Update-ID", targetUUID)
	if scenario != 1 { // 0: patch, 1: full, 2: missing patch -> full
		req.Header.Set("A-IM", "bsdiff")
	}
	if scenario == 2 {
		req.Header.Set("Expo-Current-Update-ID", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if edgeClient != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			return fmt.Errorf("expected CDN redirect, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if strings.Contains(location, "/_ota-patches/") != (scenario == 0) {
			return fmt.Errorf("incorrect CDN target: %s", location)
		}
		resp, err = edgeClient.Get(location)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	wantStatus := http.StatusOK
	if scenario == 0 && edgeClient == nil {
		wantStatus = http.StatusIMUsed
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("status %d, expected %d", resp.StatusCode, wantStatus)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if scenario == 0 {
		if !bytes.Equal(body, patch) {
			return fmt.Errorf("patch bytes differ")
		}
		body, err = bspatch.Bytes(base, body)
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(body, target) {
		return fmt.Errorf("downloaded/reconstructed bundle differs from target")
	}
	return nil
}
