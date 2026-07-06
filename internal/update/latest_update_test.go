package update

import (
	"sync"
	"testing"

	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
)

// The short-circuit scan must pick the same update as the full filter, also
// when concurrent cold-cache misses share one scan.
func TestLatestUpdateScanMatchesFullFilterUnderConcurrency(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", "../../test/test-updates")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	for _, platform := range []string{"ios", "android"} {
		all, err := GetAllUpdatesForRuntimeVersion("branch-2", "1", platform)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		for _, u := range all {
			if IsUpdateValid(u) {
				want = u.UpdateId
				break
			}
		}
		if want == "" {
			t.Fatalf("fixture has no valid %s update", platform)
		}

		cache2.GetCache().Delete(ComputeLastUpdateCacheKey("branch-2", "1", platform))
		var wg sync.WaitGroup
		got := make([]string, 32)
		for i := range got {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				latest, err := GetLatestUpdateBundlePathForRuntimeVersion("branch-2", "1", platform)
				if err == nil && latest != nil {
					got[i] = latest.UpdateId
				}
			}(i)
		}
		wg.Wait()
		for i, id := range got {
			if id != want {
				t.Fatalf("%s call %d: got %q, want %q", platform, i, id, want)
			}
		}
	}
}
