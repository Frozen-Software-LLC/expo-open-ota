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
