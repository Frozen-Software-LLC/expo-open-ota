package cdn

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestR2PatchRoutePreservesBucketPrefixAndFullAssetURL(t *testing.T) {
	t.Setenv("R2_PUBLIC_CDN_DOMAIN", "cdn.example")
	provider := &R2PublicCDN{}
	patch := "bundle-patches/ios/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/" + strings.Repeat("a", 64) + ".bsdiff"
	for _, prefix := range []string{"", "test", "test/"} {
		t.Setenv("S3_KEY_PREFIX", prefix)
		bucketPrefix := strings.TrimSuffix(prefix, "/")
		if bucketPrefix != "" {
			bucketPrefix += "/"
		}
		location, err := provider.ComputeRedirectionURLForAsset("preview", "1", "123", patch)
		require.NoError(t, err)
		require.Equal(t, "https://cdn.example/_ota-patches/"+bucketPrefix+"preview/1/123/"+patch, location)
		full, err := provider.ComputeRedirectionURLForAsset("preview", "1", "123", "bundle.hbc")
		require.NoError(t, err)
		require.Equal(t, "https://cdn.example/"+bucketPrefix+"preview/1/123/bundle.hbc", full)
	}
}
