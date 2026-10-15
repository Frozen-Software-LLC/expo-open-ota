package handlers

import (
	"encoding/json"
	"expo-open-ota/internal/metrics"
	"net/http"
)

// GetBundleMetricsHandler is mounted under the authenticated dashboard API.
func GetBundleMetricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	snapshot, err := metrics.SnapshotBundleMetrics()
	if err != nil {
		http.Error(w, "Bundle metrics are unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}
