package bundlepatch

import (
	"expo-open-ota/config"
	"github.com/google/uuid"
	"net/http"
	"strings"
)

// Installation IDs are supplied by Expo on native manifest and asset requests.
// This is a rollout control, not authentication. Unknown modes fail closed.
func DeviceEligible(r *http.Request) bool {
	switch config.GetEnv("OTA_BUNDLE_DIFFING_ROLLOUT") {
	case "all":
		return true
	case "", "allowlist":
		id, err := uuid.Parse(r.Header.Get("EAS-Client-ID"))
		if err != nil || id == uuid.Nil {
			return false
		}
		for _, entry := range strings.Split(config.GetEnv("OTA_BUNDLE_DIFFING_DEVICE_IDS"), ",") {
			allowed, err := uuid.Parse(strings.TrimSpace(entry))
			if err == nil && allowed == id {
				return true
			}
		}
	}
	return false
}
