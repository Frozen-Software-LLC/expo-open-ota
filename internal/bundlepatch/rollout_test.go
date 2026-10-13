package bundlepatch

import (
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceEligibility(t *testing.T) {
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		name, mode, list, client string
		want                     bool
	}{
		{"default closed", "", "", id, false},
		{"selected install", "", "bad, " + strings.ToUpper(id), id, true},
		{"explicit allowlist", "allowlist", id, id, true},
		{"other install", "allowlist", id, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", false},
		{"missing id", "allowlist", id, "", false},
		{"invalid id", "allowlist", "invalid", "invalid", false},
		{"nil id", "allowlist", "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000", false},
		{"unknown mode", "al", id, id, false},
		{"explicit all", "all", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTA_BUNDLE_DIFFING_ROLLOUT", tc.mode)
			t.Setenv("OTA_BUNDLE_DIFFING_DEVICE_IDS", tc.list)
			r := httptest.NewRequest("GET", "/assets", nil)
			r.Header.Set("EAS-Client-ID", tc.client)
			require.Equal(t, tc.want, DeviceEligible(r))
		})
	}
}

func TestCohortEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, mode, cohorts, cohort string
		want                        bool
	}{
		{"internal build", "", "internal", "internal", true},
		{"case and spacing", "allowlist", "beta, Internal ", "INTERNAL", true},
		{"no cohorts configured", "allowlist", "", "internal", false},
		{"other cohort", "allowlist", "internal", "beta", false},
		{"store build sends none", "allowlist", "internal", "", false},
		{"blank entry does not match blank header", "allowlist", "internal,,", " ", false},
		{"unknown mode", "al", "internal", "internal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTA_BUNDLE_DIFFING_ROLLOUT", tc.mode)
			t.Setenv("OTA_BUNDLE_DIFFING_COHORTS", tc.cohorts)
			t.Setenv("OTA_BUNDLE_DIFFING_DEVICE_IDS", "")
			r := httptest.NewRequest("GET", "/assets", nil)
			r.Header.Set("EAS-Client-ID", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
			r.Header.Set(CohortHeader, tc.cohort)
			require.Equal(t, tc.want, DeviceEligible(r))
		})
	}
}
