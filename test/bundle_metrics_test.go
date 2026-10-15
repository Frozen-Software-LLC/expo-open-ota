package test

import (
	"encoding/json"
	"expo-open-ota/internal/metrics"
	infrastructure "expo-open-ota/internal/router"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardBundleMetricsRequiresAuth(t *testing.T) {
	defer setup(t)()
	router := infrastructure.NewRouter()
	for _, token := range []string{"", "Bearer invalid"} {
		r := httptest.NewRequest(http.MethodGet, "/api/bundle-metrics", nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	before, err := metrics.SnapshotBundleMetrics()
	require.NoError(t, err)
	find := func(snapshot metrics.BundleSnapshot, delivery, reason string) metrics.BundleMetricRow {
		for _, row := range snapshot.Rows {
			if row.Platform == "ios" && row.Delivery == delivery && row.Reason == reason {
				return row
			}
		}
		return metrics.BundleMetricRow{}
	}
	metrics.TrackBundleResponse("ios", "patch_redirect", "cdn_available", 0)
	metrics.TrackBundleResponse("ios", "full", "full_after_patch", 4096)
	r := httptest.NewRequest(http.MethodGet, "/api/bundle-metrics", nil)
	r.Header.Set("Authorization", "Bearer "+login().Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	var snapshot metrics.BundleSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.NotEmpty(t, snapshot.InstanceID)
	full := find(snapshot, "full", "full_after_patch")
	priorFull := find(before, "full", "full_after_patch")
	require.Equal(t, priorFull.Requests+1, full.Requests)
	require.Equal(t, priorFull.Bytes+4096, full.Bytes)
	redirect := find(snapshot, "patch_redirect", "cdn_available")
	priorRedirect := find(before, "patch_redirect", "cdn_available")
	require.Equal(t, priorRedirect.Requests+1, redirect.Requests)
	require.Equal(t, priorRedirect.Bytes, redirect.Bytes)
	require.NotContains(t, w.Body.String(), "clientId")
}
