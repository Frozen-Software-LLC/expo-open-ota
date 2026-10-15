package handlers

import (
	"errors"
	"expo-open-ota/internal/assets"
	"expo-open-ota/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func setupBundleMetrics(t *testing.T) {
	oldRegisterer, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry
	metrics.ResetMetricsForTest()
	metrics.InitMetrics()
	t.Cleanup(func() {
		metrics.CleanupMetrics()
		prometheus.DefaultRegisterer = oldRegisterer
		prometheus.DefaultGatherer = oldGatherer
	})
}
func bundleMetric(t *testing.T, name, delivery, reason string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			require.Len(t, labels, 3)
			if labels["delivery"] == delivery && labels["reason"] == reason {
				return metric.Counter.GetValue()
			}
		}
	}
	return 0
}
func TestBundleResponseCorrelation(t *testing.T) {
	setupBundleMetrics(t)
	t.Setenv("CACHE_MODE", "local")
	req := assets.AssetsRequest{Platform: "android", Branch: "test", RuntimeVersion: "1", UpdateID: "1", AssetName: "bundle.hbc"}
	r := httptest.NewRequest("GET", "/assets", nil)
	r.Header.Set("EAS-Client-ID", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	r.Header.Set("Expo-Current-Update-ID", baseUUID)
	r.Header.Set("Expo-Requested-Update-ID", targetUUID)
	key := patchRequestKey(r, req)
	require.NotEmpty(t, key)
	other := req
	other.Platform = "ios"
	require.NotEqual(t, key, patchRequestKey(r, other))
	other = req
	other.RuntimeVersion = "different"
	require.NotEqual(t, key, patchRequestKey(r, other))
	rememberPatchRequest(r, req)
	require.True(t, recentlySentPatch(key))
	observe := func(status int, reason string) {
		w := &bundleResponse{ResponseWriter: httptest.NewRecorder(), reason: reason}
		w.WriteHeader(status)
		_, err := w.Write([]byte("body"))
		require.NoError(t, err)
		w.observe(r, req)
	}
	observe(226, "available")
	observe(302, "cdn_available")
	observe(200, "not_negotiated")
	observe(302, "not_negotiated")
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "patch", "available"))
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "patch_redirect", "cdn_available"))
	require.EqualValues(t, 0, bundleMetric(t, "ota_bundle_response_bytes_total", "patch_redirect", "cdn_available"))
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "full", "full_after_patch"))
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "full_redirect", "full_after_patch"))
	require.EqualValues(t, 4, bundleMetric(t, "ota_bundle_response_bytes_total", "patch", "available"))
	// A different installation must not be attributed to this patch.
	r.Header.Set("EAS-Client-ID", "dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	observe(200, "not_negotiated")
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "full", "not_negotiated"))
	// An expired attempt cannot label future full requests as a retry.
	localPatchAttempts.Lock()
	localPatchAttempts.entries[key] = time.Now().Add(-time.Second)
	localPatchAttempts.Unlock()
	require.False(t, recentlySentPatch(key))
	r.Header.Del("EAS-Client-ID")
	require.Empty(t, patchRequestKey(r, req))
}

type brokenWriter struct{ http.ResponseWriter }

func (brokenWriter) Write(p []byte) (int, error) { return 2, errors.New("connection lost") }
func TestBundleWriteFailure(t *testing.T) {
	setupBundleMetrics(t)
	w := &bundleResponse{ResponseWriter: brokenWriter{httptest.NewRecorder()}, reason: "available"}
	w.WriteHeader(226)
	_, err := w.Write([]byte("patch"))
	require.Error(t, err)
	w.observe(httptest.NewRequest("GET", "/assets", nil), assets.AssetsRequest{Platform: "ios"})
	require.EqualValues(t, 1, bundleMetric(t, "ota_bundle_responses_total", "write_error", "available"))
	require.EqualValues(t, 0, bundleMetric(t, "ota_bundle_responses_total", "patch", "available"))
}
