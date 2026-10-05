package update

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/bundlepatch"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"
	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/stretchr/testify/require"
)

func TestPublicationPreparesCompatiblePatches(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		t.Run(platform, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("STORAGE_MODE", "local")
			t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
			t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "false")
			bucket.ResetBucketInstance()
			t.Cleanup(bucket.ResetBucketInstance)
			t.Cleanup(preWarmTasks.Wait)
			require.NoError(t, cache.GetCache().Clear())
			base := make([]byte, 32000)
			rand.New(rand.NewSource(42)).Read(base)
			targetBytes := bytes.Clone(base)
			copy(targetBytes[100:], []byte("the actual change"))
			writeUpdate := func(id, plat string, data []byte, checked bool) types.Update {
				t.Helper()
				u, err := GetUpdate("patch-test", "1", id)
				require.NoError(t, err)
				storage := bucket.GetBucket()
				files := map[string][]byte{"bundle.hbc": data, "expoConfig.json": []byte(`{}`), "metadata.json": []byte(fmt.Sprintf(`{"version":0,"bundler":"metro","fileMetadata":{"%s":{"bundle":"bundle.hbc","assets":[]}}}`, plat)), "update-metadata.json": []byte(fmt.Sprintf(`{"platform":%q}`, plat))}
				for name, content := range files {
					require.NoError(t, storage.UploadFileIntoUpdate(*u, name, bytes.NewReader(content)))
				}
				if checked {
					require.NoError(t, MarkUpdateAsChecked(*u))
				}
				return *u
			}
			first := writeUpdate("1700000000000", platform, base, true)
			otherPlatform := "android"
			if platform == "android" {
				otherPlatform = "ios"
			}
			writeUpdate("1700000000100", otherPlatform, base, true)
			writeUpdate("1700000000200", platform, base, false) // uncommitted base
			// Several compatible bases exercise the bounded most-recent selection.
			second := writeUpdate("1700000000300", platform, base, true)
			third := writeUpdate("1700000000400", platform, base, true)
			fourth := writeUpdate("1700000000500", platform, base, true)
			target := writeUpdate("1700000001000", platform, targetBytes, true)
			writeUpdate("1700000002000", platform, base, true) // newer must not become a base
			require.NoError(t, PreparePublishedBundlePatches(target))
			tm, err := RetrieveUpdateStoredMetadata(target)
			require.NoError(t, err)
			targetDir := filepath.Join(root, target.Branch, target.RuntimeVersion, target.UpdateId)
			for _, u := range []types.Update{second, third, fourth} {
				sm, err := RetrieveUpdateStoredMetadata(u)
				require.NoError(t, err)
				key, err := bundlepatch.Key(platform, sm.UpdateUUID)
				require.NoError(t, err)
				encoded, err := os.ReadFile(filepath.Join(targetDir, key+".json"))
				require.NoError(t, err)
				var record bundlepatch.Record
				require.NoError(t, json.Unmarshal(encoded, &record))
				require.Equal(t, tm.UpdateUUID, record.TargetID)
				patch, err := os.ReadFile(filepath.Join(targetDir, key+".bsdiff"))
				require.NoError(t, err)
				restored, err := bspatch.Bytes(base, patch)
				require.NoError(t, err)
				require.Equal(t, targetBytes, restored)
				// Retry repairs corrupted objects rather than trusting just the record.
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, key+".bsdiff"), []byte("corrupt"), 0644))
			}
			require.NoError(t, PreparePublishedBundlePatches(target))
			fm, err := RetrieveUpdateStoredMetadata(first)
			require.NoError(t, err)
			key, _ := bundlepatch.Key(platform, fm.UpdateUUID)
			require.NoFileExists(t, filepath.Join(targetDir, key+".json"))
			entries, err := os.ReadDir(filepath.Join(targetDir, "bundle-patches", platform))
			require.NoError(t, err)
			require.Len(t, entries, 6)
			require.NoError(t, PreparePublishedBundlePatches(target)) // idempotent retry
		})
	}
}

func TestPatchQueueBoundsAndDeduplicates(t *testing.T) {
	started := make(chan types.Update, 4)
	release := make(chan struct{})
	q := newPatchQueue(func(target types.Update) { started <- target; <-release }, 1)
	t.Cleanup(func() { close(release); close(q.jobs) })
	first := types.Update{UpdateId: "1"}
	second := types.Update{UpdateId: "2"}
	require.True(t, q.enqueue(first))
	require.Equal(t, first, <-started)
	require.True(t, q.enqueue(first)) // coalesces running job
	require.True(t, q.enqueue(second))
	require.False(t, q.enqueue(types.Update{UpdateId: "3"}))
	release <- struct{}{}
	require.Equal(t, second, <-started)
}

func TestFinalizationQueuesOnlyWhenEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)
	t.Cleanup(preWarmTasks.Wait)
	require.NoError(t, cache.GetCache().Clear())
	jobs := make(chan types.Update, 2)
	q := newPatchQueue(func(u types.Update) { jobs <- u }, 2)
	publicationPatches.queue = q
	publicationPatches.Once = sync.Once{}
	publicationPatches.Do(func() {})
	t.Cleanup(func() { close(q.jobs); publicationPatches.queue = nil; publicationPatches.Once = sync.Once{} })
	target, err := GetUpdate("queue-test", "1", "1700000000000")
	require.NoError(t, err)
	for name, value := range map[string]string{"update-metadata.json": `{"platform":"ios"}`, "metadata.json": `{"fileMetadata":{"ios":{"bundle":"bundle.hbc"}}}`, "bundle.hbc": "bundle", "expoConfig.json": "{}"} {
		require.NoError(t, bucket.GetBucket().UploadFileIntoUpdate(*target, name, bytes.NewBufferString(value)))
	}
	t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "false")
	require.NoError(t, MarkUpdateAsChecked(*target))
	select {
	case <-jobs:
		t.Fatal("disabled flag queued a job")
	default:
	}
	t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "true")
	require.NoError(t, MarkUpdateAsChecked(*target))
	select {
	case got := <-jobs:
		require.Equal(t, *target, got)
	case <-time.After(time.Second):
		t.Fatal("finalization did not enqueue")
	}
	require.True(t, IsUpdateValid(*target))
}
