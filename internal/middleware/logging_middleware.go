package middleware

import (
	"expo-open-ota/internal/metrics"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

func redactHeaders(headers http.Header) http.Header {
	redactedHeaders := make(http.Header)
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "expo-session") {
			redactedHeaders[key] = []string{"REDACTED"}
		} else {
			redactedHeaders[key] = values
		}
	}
	return redactedHeaders
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hc" || r.URL.Path == "/metrics" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()

		safeHeaders := redactHeaders(r.Header)

		log.Printf("Started %s %s with query: %s and headers: %v", r.Method, r.RequestURI, r.URL.RawQuery, safeHeaders)

		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		metrics.HTTPRequestStarted()
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Panic recovered during %s %s\nQuery: %s\nHeaders: %v\nError: %v\nStack Trace:\n%s",
					r.Method, r.RequestURI, r.URL.RawQuery, safeHeaders, err, debug.Stack())
				http.Error(recorder, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
			metrics.TrackHTTPRequest(routeTemplate(r), r.Method, recorder.statusCode, isPreviewRequest(r), time.Since(start))
		}()

		next.ServeHTTP(recorder, r)

		if recorder.statusCode >= 500 {
			log.Printf("Error detected: %s %s returned status %d", r.Method, r.RequestURI, recorder.statusCode)
		}
		log.Printf("Completed %s %d in %v", r.RequestURI, recorder.statusCode, time.Since(start))
	})
}

// Asset requests can name the branch instead of the channel.
func isPreviewRequest(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("expo-channel-name"), "preview-pr-") ||
		strings.HasPrefix(r.URL.Query().Get("branch"), "preview-pr-")
}

func routeTemplate(r *http.Request) string {
	if route := mux.CurrentRoute(r); route != nil {
		if tpl, err := route.GetPathTemplate(); err == nil {
			return tpl
		}
	}
	return "unmatched"
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
