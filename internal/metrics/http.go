package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newHTTPMetrics() (*prometheus.HistogramVec, prometheus.Gauge) {
	// _count doubles as the request counter; error rate = code=~"5.." / all.
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ota_http_request_duration_seconds",
			Help:    "HTTP request latency by route template, method, status code and whether a preview-pr channel or branch asked.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"route", "method", "code", "preview"}), prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ota_http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		})
}

var httpRequestDuration, httpRequestsInFlight = newHTTPMetrics()

func HTTPRequestStarted() { httpRequestsInFlight.Inc() }

// route must be a path template (e.g. /api/branch/{BRANCH}), never a raw path.
// preview marks requests from preview-pr channels, which serve PR builds, not users.
func TrackHTTPRequest(route, method string, code int, preview bool, duration time.Duration) {
	httpRequestsInFlight.Dec()
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		method = "OTHER" // client-controlled; keep label cardinality bounded
	}
	httpRequestDuration.WithLabelValues(route, method, strconv.Itoa(code), strconv.FormatBool(preview)).Observe(duration.Seconds())
}
