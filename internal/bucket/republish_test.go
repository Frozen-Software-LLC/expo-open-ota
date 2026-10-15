package bucket

import (
	"os"
	"path/filepath"
	"testing"

	"expo-open-ota/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRepublishSkipsTargetSpecificArtifacts(t *testing.T) {
	// The same predicate handles local top-level directories and S3/GCS keys.
	for _, name := range []string{".check", "update-metadata.json", "bundle-patches", "bundle-patches/ios/base.json", "bundle-patches/android/base/hash.bsdiff"} {
		require.True(t, skipRepublishObject(name), name)
	}
	for _, name := range []string{"metadata.json", "expoConfig.json", "rollback", "bundles/app.hbc", "assets/image.png", "assets/bundle-patches/icon.png", "bundle-patches-logo.png"} {
		require.False(t, skipRepublishObject(name), name)
	}

	root := t.TempDir()
	storage := &LocalBucket{BasePath: root}
	source := types.Update{Branch: "preview", RuntimeVersion: "1", UpdateId: "1700000000000"}
	files := map[string]string{
		"metadata.json": "metadata", "expoConfig.json": "config",
		"bundles/app.hbc": "bundle", "assets/image.png": "image",
		".check": "checked", "update-metadata.json": "old UUID",
		"bundle-patches/ios/base.json":        "old target record",
		"bundle-patches/ios/base/hash.bsdiff": "old target patch",
		"bundle-patches/android/base.bsdiff":  "legacy patch",
	}
	for name, value := range files {
		path := filepath.Join(root, source.Branch, source.RuntimeVersion, source.UpdateId, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(value), 0644))
	}
	target, err := storage.CreateUpdateFrom(&source, "1700000001000")
	require.NoError(t, err)
	for name, value := range files {
		path := filepath.Join(root, target.Branch, target.RuntimeVersion, target.UpdateId, name)
		if skipRepublishObject(name) {
			require.NoFileExists(t, path)
		} else {
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, value, string(actual))
		}
	}
	require.NoDirExists(t, filepath.Join(root, target.Branch, target.RuntimeVersion, target.UpdateId, "bundle-patches"))
	// The original remains usable; republishing must not delete source patches.
	require.FileExists(t, filepath.Join(root, source.Branch, source.RuntimeVersion, source.UpdateId, "bundle-patches/ios/base/hash.bsdiff"))
}
