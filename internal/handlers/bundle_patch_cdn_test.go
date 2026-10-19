package handlers

import (
	"encoding/json"
	"expo-open-ota/internal/bundlepatch"
	"expo-open-ota/internal/cdn"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func testPatchCDN(t *testing.T, record bundlepatch.Record, patch, base, target []byte, write func(string, []byte), request func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	record.Version = 2
	key, err := bundlepatch.ObjectKey(record)
	require.NoError(t, err)
	write(key, patch)
	metadataKey, _ := bundlepatch.Key(record.Platform, record.BaseID)
	encoded, _ := json.Marshal(record)
	write(metadataKey+".json", encoded)
	var probes atomic.Int32
	edge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local-diff/9090.1/1700000000000/bundle.hbc" {
			_, _ = w.Write(target)
			return
		}
		if r.URL.Path != "/_ota-patches/local-diff/9090.1/1700000000000/"+key {
			t.Errorf("unexpected CDN path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("IM", "bsdiff")
		w.Header().Set("Expo-Base-Update-ID", record.BaseID)
		w.Header().Set("X-OTA-Patch-SHA256", record.PatchHash)
		w.Header().Set("Content-Length", fmt.Sprint(len(patch)))
		if r.Method == http.MethodHead {
			probes.Add(1)
			return
		}
		_, _ = w.Write(patch)
	}))
	defer edge.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = edge.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport; cdn.ResetCDNInstance() })
	// Storage is already initialized to the temporary local bucket. Only the CDN
	// factory reads this setting now; no real S3/CDN account is used in this test.
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("R2_PUBLIC_CDN_DOMAIN", strings.TrimPrefix(edge.URL, "https://"))
	t.Setenv("S3_KEY_PREFIX", "")
	cdn.ResetCDNInstance()
	installation := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	first := request(baseUUID, targetUUID, "bsdiff", installation)
	require.Equal(t, 302, first.Code)
	require.Equal(t, "private, no-store", first.Header().Get("Cache-Control"))
	resp, err := edge.Client().Get(first.Header().Get("Location"))
	require.NoError(t, err)
	defer resp.Body.Close()
	actual, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, patch, actual)
	require.Equal(t, baseUUID, resp.Header.Get("Expo-Base-Update-ID"))
	// The operator bypass still serves a validated patch directly, even after a
	// successful probe; it must not follow either the patch or full CDN path.
	bypassRequest := httptest.NewRequest("GET", "/assets?branch=local-diff&runtimeVersion=9090.1&updateId=1700000000000&platform="+record.Platform+"&asset=bundle.hbc", nil)
	bypassRequest.Header.Set("A-IM", "bsdiff")
	bypassRequest.Header.Set("Expo-Current-Update-ID", baseUUID)
	bypassRequest.Header.Set("Expo-Requested-Update-ID", targetUUID)
	bypassRequest.Header.Set("prevent-cdn-redirection", "true")
	bypass := httptest.NewRecorder()
	AssetsHandler(bypass, bypassRequest)
	require.Equal(t, 226, bypass.Code)
	require.Equal(t, patch, bypass.Body.Bytes())
	// Warm CDN requests must not load or validate the patch body on the API.
	write(key, []byte("not readable as a patch"))
	require.Equal(t, 302, request(baseUUID, targetUUID, "bsdiff", installation).Code)
	require.EqualValues(t, 1, probes.Load())
	require.EqualValues(t, 2, bundleMetric(t, "ota_bundle_responses_total", "patch_redirect", "cdn_available"))
	require.EqualValues(t, 0, bundleMetric(t, "ota_bundle_response_bytes_total", "patch_redirect", "cdn_available"))
	// Expo's native rejection retries the original URL without A-IM. It must
	// select the pinned full bundle, never redirect back to the patch.
	full := request(baseUUID, targetUUID, "", installation)
	require.Equal(t, 302, full.Code)
	require.True(t, strings.HasSuffix(full.Header().Get("Location"), "/bundle.hbc"))
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "full_redirect", "full_after_patch"))
	// An unconfigured edge must not receive native clients. Failed readiness
	// falls back to the same hash-checked direct patch, not an ordinary CDN object.
	unready := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer unready.Close()
	t.Setenv("R2_PUBLIC_CDN_DOMAIN", strings.TrimPrefix(unready.URL, "https://"))
	cdn.ResetCDNInstance()
	write(key, patch)
	direct := request(baseUUID, targetUUID, "bsdiff", "")
	require.Equal(t, 226, direct.Code)
	require.Equal(t, patch, direct.Body.Bytes())
	// Legacy objects remain readable but are never publicly cacheable under
	// their mutable URL, even when the configured edge is ready.
	t.Setenv("R2_PUBLIC_CDN_DOMAIN", strings.TrimPrefix(edge.URL, "https://"))
	cdn.ResetCDNInstance()
	record.Version = 1
	encoded, err = json.Marshal(record)
	require.NoError(t, err)
	write(metadataKey+".json", encoded)
	legacy := request(baseUUID, targetUUID, "bsdiff", "")
	require.Equal(t, 226, legacy.Code)
	require.Equal(t, patch, legacy.Body.Bytes())
	// Exercise real HTTP requests and local TLS CDN redirects after all the
	// single-request counter assertions above have completed.
	record.Version = 2
	encoded, err = json.Marshal(record)
	require.NoError(t, err)
	write(metadataKey+".json", encoded)
	t.Run("concurrent-cdn", func(t *testing.T) {
		testConcurrentBundleDelivery(t, record.Platform, base, target, patch, edge.Client())
	})
}
