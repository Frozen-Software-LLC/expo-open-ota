package update

import (
	"os"
	"path/filepath"
	"testing"

	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/types"
)

// GetUpdateType must not cache "normal" for an unchecked update, because a
// rollback that is still being written has no marker yet.
func TestGetUpdateTypeCachesOnlySettledTypes(t *testing.T) {
	base := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", base)
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	update := types.Update{Branch: "type-branch", RuntimeVersion: "1", UpdateId: "1700000000000"}
	dir := filepath.Join(base, update.Branch, update.RuntimeVersion, update.UpdateId)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if GetUpdateType(update) != types.NormalUpdate {
		t.Fatal("unmarked update should be normal")
	}
	touch("rollback")
	touch(".check")
	if GetUpdateType(update) != types.Rollback {
		t.Fatal("unchecked normal result was cached")
	}
	if err := os.Remove(filepath.Join(dir, "rollback")); err != nil {
		t.Fatal(err)
	}
	if GetUpdateType(update) != types.Rollback {
		t.Fatal("rollback result was not cached")
	}
}
