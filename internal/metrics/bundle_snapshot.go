package metrics

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

var bundleProcessStartedAt = time.Now().UTC()
var bundleProcessID = uuid.NewString()

type BundleMetricRow struct {
	Platform string  `json:"platform"`
	Delivery string  `json:"delivery"`
	Reason   string  `json:"reason"`
	Requests float64 `json:"requests"`
	Bytes    float64 `json:"bytes"`
}

type BundleSnapshot struct {
	InstanceID string            `json:"instanceId"`
	StartedAt  time.Time         `json:"startedAt"`
	SampledAt  time.Time         `json:"sampledAt"`
	Rows       []BundleMetricRow `json:"rows"`
}

// SnapshotBundleMetrics reads the same counters exposed to Prometheus, without
// requiring a Prometheus server or keeping a second set of counters. It returns
// only this process's lifetime totals, never fleet-wide or historical statistics.
func SnapshotBundleMetrics() (BundleSnapshot, error) {
	snapshot := BundleSnapshot{InstanceID: bundleProcessID, StartedAt: bundleProcessStartedAt,
		SampledAt: time.Now().UTC(), Rows: make([]BundleMetricRow, 0)}
	registry := prometheus.NewRegistry()
	registry.MustRegister(bundleResponses, bundleResponseBytes)
	families, err := registry.Gather()
	if err != nil {
		return snapshot, err
	}
	type identity struct{ platform, delivery, reason string }
	rows := make(map[identity]*BundleMetricRow)
	for _, family := range families {
		for _, metric := range family.Metric {
			var key identity
			for _, label := range metric.Label {
				switch label.GetName() {
				case "platform":
					key.platform = label.GetValue()
				case "delivery":
					key.delivery = label.GetValue()
				case "reason":
					key.reason = label.GetValue()
				}
			}
			row, ok := rows[key]
			if !ok {
				row = &BundleMetricRow{Platform: key.platform, Delivery: key.delivery, Reason: key.reason}
				rows[key] = row
			}
			if family.GetName() == "ota_bundle_responses_total" {
				row.Requests = metric.GetCounter().GetValue()
			} else {
				row.Bytes = metric.GetCounter().GetValue()
			}
		}
	}
	for _, row := range rows {
		snapshot.Rows = append(snapshot.Rows, *row)
	}
	sort.Slice(snapshot.Rows, func(i, j int) bool {
		a, b := snapshot.Rows[i], snapshot.Rows[j]
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		if a.Delivery != b.Delivery {
			return a.Delivery < b.Delivery
		}
		return a.Reason < b.Reason
	})
	return snapshot, nil
}
