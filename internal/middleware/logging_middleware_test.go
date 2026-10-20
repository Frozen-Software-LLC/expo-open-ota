package middleware

import (
	"expo-open-ota/internal/metrics"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestLoggingMiddlewareRecordsHTTPMetrics(t *testing.T) {
	oldRegisterer, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer, prometheus.DefaultGatherer = reg, reg
	t.Cleanup(func() {
		metrics.CleanupMetrics()
		prometheus.DefaultRegisterer, prometheus.DefaultGatherer = oldRegisterer, oldGatherer
	})
	metrics.ResetMetricsForTest()
	metrics.InitMetrics()

	r := mux.NewRouter()
	r.Use(LoggingMiddleware)
	r.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {})
	r.HandleFunc("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	r.HandleFunc("/partial", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("started"))
		panic("boom after write")
	})
	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/branch/{BRANCH}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/branch/a", nil),
		httptest.NewRequest(http.MethodGet, "/api/branch/b", nil),
		httptest.NewRequest(http.MethodGet, "/boom", nil),
		httptest.NewRequest(http.MethodGet, "/partial", nil),
		httptest.NewRequest("WEIRD", "/ok", nil),
	} {
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	out := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := out.Body.String()
	for _, want := range []string{
		`ota_http_request_duration_seconds_count{code="404",method="GET",route="/api/branch/{BRANCH}"} 2`,
		`ota_http_request_duration_seconds_count{code="500",method="GET",route="/boom"} 1`,
		`ota_http_request_duration_seconds_count{code="500",method="GET",route="/partial"} 1`,
		`ota_http_request_duration_seconds_count{code="200",method="OTHER",route="/ok"} 1`,
		`ota_http_requests_in_flight 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}
