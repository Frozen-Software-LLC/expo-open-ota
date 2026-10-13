package bundlepatch

import (
	"expo-open-ota/config"
	"github.com/google/uuid"
	"net/http"
	"strings"
)

// Builds send this through expo-updates requestHeaders, which Expo attaches to
// both manifest and asset requests. An internal TestFlight build can then
// receive patches from the main server without enrolling each installation.
const CohortHeader = "Expo-OTA-Cohort"

// Installation IDs are supplied by Expo on native manifest and asset requests.
// This is a rollout control, not authentication. Unknown modes fail closed.
func DeviceEligible(r *http.Request) bool {
	switch config.GetEnv("OTA_BUNDLE_DIFFING_ROLLOUT") {
	case "all":
		return true
	case "", "allowlist":
		return cohortAllowed(r) || installationAllowed(r)
	}
	return false
}

func cohortAllowed(r *http.Request) bool {
	cohort := strings.TrimSpace(r.Header.Get(CohortHeader))
	if cohort == "" {
		return false
	}
	for _, entry := range strings.Split(config.GetEnv("OTA_BUNDLE_DIFFING_COHORTS"), ",") {
		if strings.EqualFold(strings.TrimSpace(entry), cohort) {
			return true
		}
	}
	return false
}

func installationAllowed(r *http.Request) bool {
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
	return false
}
