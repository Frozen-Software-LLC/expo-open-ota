package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// _count doubles as the request counter; error rate = code=~"5.." / all.
	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ota_http_request_duration_seconds",
		Help:    "HTTP request latency by route template, method and status code.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"route", "method", "code"})

	httpRequestsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ota_http_requests_in_flight",
		Help: "HTTP requests currently being served.",
	})
)

func HTTPRequestStarted() { httpRequestsInFlight.Inc() }

// route must be a path template (e.g. /api/branch/{BRANCH}), never a raw path.
func TrackHTTPRequest(route, method string, code int, duration time.Duration) {
	httpRequestsInFlight.Dec()
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		method = "OTHER" // client-controlled; keep label cardinality bounded
	}
	httpRequestDuration.WithLabelValues(route, method, strconv.Itoa(code)).Observe(duration.Seconds())
}
