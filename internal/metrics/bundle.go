package metrics

import "github.com/prometheus/client_golang/prometheus"

func newBundleMetrics() (*prometheus.CounterVec, *prometheus.CounterVec) {
	labels := []string{"platform", "delivery", "reason"}
	return prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ota_bundle_responses_total",
			Help: "Launch-bundle HTTP responses, not device installations. Full-after-patch is a correlated retry, not confirmed rejection.",
		}, labels), prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ota_bundle_response_bytes_total",
			Help: "Launch-bundle body bytes written by this server; excludes CDN transfers and does not prove device receipt.",
		}, labels)
}

var bundleResponses, bundleResponseBytes = newBundleMetrics()

func TrackBundleResponse(platform, delivery, reason string, bytes int) {
	// No client, branch, runtime, URL or update IDs in metric labels.
	if platform != "ios" && platform != "android" {
		platform = "unknown"
	}
	bundleResponses.WithLabelValues(platform, delivery, reason).Inc()
	if bytes > 0 {
		bundleResponseBytes.WithLabelValues(platform, delivery, reason).Add(float64(bytes))
	}
}
