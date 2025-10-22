package metrics

import (
	"expo-open-ota/internal/cache"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	activeUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "active_users_total",
			Help: "Total number of unique active users per clientId, platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update"},
	)

	globalActiveUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "global_active_users_total",
			Help: "Total number of unique active users across all platforms, runtime versions, branches and updates",
		},
		[]string{"platform"},
	)

	updateDownloadsVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_downloads_total",
			Help: "Total number of update downloads per platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update", "updateType"},
	)

	updateErrorUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_error_users_total",
			Help: "Total number of users who encountered an error for a given platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update"},
	)
)

func InitMetrics() {
	prometheus.MustRegister(activeUsersVec)
	prometheus.MustRegister(updateDownloadsVec)
	prometheus.MustRegister(updateErrorUsersVec)
	prometheus.MustRegister(globalActiveUsersVec)
}

func CleanupMetrics() {
	prometheus.Unregister(activeUsersVec)
	prometheus.Unregister(updateDownloadsVec)
	prometheus.Unregister(updateErrorUsersVec)
	prometheus.Unregister(globalActiveUsersVec)
}

func TrackUpdateErrorUsers(clientId, platform, runtime, branch, update string) {
	computedUpdate := update
	if computedUpdate == "" {
		computedUpdate = "unknown"
	}
	if clientId == "" || platform == "" || runtime == "" || branch == ""  {
		return
	}
	resolvedCache := cache.GetCache()
	key := fmt.Sprintf("update_error_users:%s:%s:%s:%s", branch, platform, runtime, computedUpdate)
	ttl := 600

	_ = resolvedCache.Sadd(key, []string{runtime}, &ttl)

	count, err := resolvedCache.Scard(key)
	if err != nil {
		return
	}
	updateErrorUsersVec.WithLabelValues(platform, runtime, branch, computedUpdate).Set(float64(count))
}

func TrackActiveUser(clientId, platform, runtime, branch, update string) {
	start := time.Now()
	log.Printf("[TRACE] TrackActiveUser - START at %s", start.Format(time.RFC3339Nano))
	defer func() {
		log.Printf("[TRACE] TrackActiveUser - END duration=%v", time.Since(start))
	}()

	if clientId == "" || platform == "" || branch == "" || update == "" || runtime == "" {
		return
	}

	resolvedCache := cache.GetCache()
	activeUserKey := fmt.Sprintf("seen_users:%s:%s:%s:%s", branch, platform, runtime, update)
	ttl := 14400

	_ = resolvedCache.Sadd(activeUserKey, []string{clientId}, &ttl)

	count, err := resolvedCache.Scard(activeUserKey)
	if err != nil {
		return
	}
	activeUsersVec.WithLabelValues(platform, runtime, branch, update).Set(float64(count))

	globalActiveUserKey := fmt.Sprintf("global_active_users:%s", platform)
	_ = resolvedCache.Sadd(globalActiveUserKey, []string{clientId}, &ttl)
	count, err = resolvedCache.Scard(globalActiveUserKey)
	if err != nil {
		return
	}
	globalActiveUsersVec.WithLabelValues(platform).Set(float64(count))
}

func TrackUpdateDownload(platform, runtime, branch, update, updateType string) {
	if update == "" || platform == "" || branch == "" {
		return
	}
	updateDownloadsVec.WithLabelValues(platform, runtime, branch, update, updateType).Inc()
}

func PrometheusHandler() http.Handler {
	return promhttp.Handler()
}

func ResetMetricsForTest() {
	activeUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "active_users_total",
			Help: "Total number of unique active users per platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update"},
	)
	updateDownloadsVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_downloads_total",
			Help: "Total number of update downloads per platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update", "updateType"},
	)
	updateErrorUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_error_users_total",
			Help: "Total number of users who encountered an error for a given platform, runtime version, branch and update",
		},
		[]string{"platform", "runtime", "branch", "update"},
	)
	globalActiveUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "global_active_users_total",
			Help: "Total number of unique active users across all platforms, runtime versions, branches and updates",
		},
		[]string{"platform"},
	)
}
