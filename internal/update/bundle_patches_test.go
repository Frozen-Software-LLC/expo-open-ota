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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	// Match the API's worker entry point so this regression exercises an actual
	// child process, without a standalone helper beside the test executable.
	if len(os.Args) > 1 && os.Args[1] == "--prepare-update-patches" {
		if err := RunPatchPreparationCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPatchJobStartsChildWithoutSiblingHelper(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	baseBytes := make([]byte, 32000)
	rand.New(rand.NewSource(42)).Read(baseBytes)
	targetBytes := bytes.Clone(baseBytes)
	copy(targetBytes[100:], []byte("child process regression"))
	baseID, targetID := uuid.NewString(), uuid.NewString()
	for i, data := range [][]byte{baseBytes, targetBytes} {
		id := []string{baseID, targetID}[i]
		dir := filepath.Join(root, "child-test", "1", fmt.Sprint(1700000000000+1000*i))
		require.NoError(t, os.MkdirAll(dir, 0755))
		for name, content := range map[string][]byte{
			".check": []byte("checked"), "bundle.hbc": data,
			"metadata.json":        []byte(`{"fileMetadata":{"ios":{"bundle":"bundle.hbc"}}}`),
			"update-metadata.json": []byte(fmt.Sprintf(`{"platform":"ios","updateUUID":%q}`, id)),
		} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), content, 0644))
		}
	}
	target, err := GetUpdate("child-test", "1", "1700000001000")
	require.NoError(t, err)
	runPatchJob(*target)
	key, err := bundlepatch.Key("ios", baseID)
	require.NoError(t, err)
	dir := filepath.Join(root, target.Branch, target.RuntimeVersion, target.UpdateId)
	raw, err := os.ReadFile(filepath.Join(dir, key+".json"))
	require.NoError(t, err)
	var record bundlepatch.Record
	require.NoError(t, json.Unmarshal(raw, &record))
	require.Equal(t, targetID, record.TargetID)
	objectKey, err := bundlepatch.ObjectKey(record)
	require.NoError(t, err)
	patch, err := os.ReadFile(filepath.Join(dir, objectKey))
	require.NoError(t, err)
	restored, err := bspatch.Bytes(baseBytes, patch)
	require.NoError(t, err)
	require.Equal(t, targetBytes, restored)
}

func TestPublicationPreparesCompatiblePatches(t *testing.T) {
	// Exercise preparation synchronously below without spawning helper processes.
	usePublicationQueue(t, func(types.Update) {})
	for _, platform := range []string{"ios", "android"} {
		t.Run(platform, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("STORAGE_MODE", "local")
			t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
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
				require.Equal(t, 2, record.Version)
				objectKey, err := bundlepatch.ObjectKey(record)
				require.NoError(t, err)
				patch, err := os.ReadFile(filepath.Join(targetDir, objectKey))
				require.NoError(t, err)
				restored, err := bspatch.Bytes(base, patch)
				require.NoError(t, err)
				require.Equal(t, targetBytes, restored)
				// Retry repairs corrupted objects rather than trusting just the record.
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, objectKey), []byte("corrupt"), 0644))
			}
			require.NoError(t, PreparePublishedBundlePatches(target))
			// A republish migrates legacy v1 records even if their old body is valid.
			sm, err := RetrieveUpdateStoredMetadata(second)
			require.NoError(t, err)
			legacyKey, err := bundlepatch.Key(platform, sm.UpdateUUID)
			require.NoError(t, err)
			recordFile := filepath.Join(targetDir, legacyKey+".json")
			encoded, err := os.ReadFile(recordFile)
			require.NoError(t, err)
			var legacy bundlepatch.Record
			require.NoError(t, json.Unmarshal(encoded, &legacy))
			immutableKey, err := bundlepatch.ObjectKey(legacy)
			require.NoError(t, err)
			repaired, err := os.ReadFile(filepath.Join(targetDir, immutableKey))
			require.NoError(t, err)
			require.Equal(t, legacy.PatchHash, bundlepatch.Hash(repaired))
			legacy.Version = 1
			encoded, err = json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(recordFile, encoded, 0644))
			require.NoError(t, os.WriteFile(filepath.Join(targetDir, legacyKey+".bsdiff"), repaired, 0644))
			require.NoError(t, os.Remove(filepath.Join(targetDir, immutableKey)))
			require.NoError(t, PreparePublishedBundlePatches(target))
			encoded, err = os.ReadFile(recordFile)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &legacy))
			require.Equal(t, 2, legacy.Version)
			restoredPatch, err := os.ReadFile(filepath.Join(targetDir, immutableKey))
			require.NoError(t, err)
			require.Equal(t, repaired, restoredPatch)
			require.NoError(t, os.Remove(filepath.Join(targetDir, legacyKey+".bsdiff")))
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

func TestPatchPreparationCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--unknown"},
		{"--branch", "../production", "--runtime", "1", "--update-id", "1700000000000"},
		{"--branch", "preview", "--runtime", "1", "--update-id", "invalid"},
		{"--branch", "preview", "--runtime", "1", "--update-id", "1700000000000", "extra"},
	} {
		require.Error(t, RunPatchPreparationCommand(args))
	}
}

func TestFinalizationQueuesPatchesByDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)
	t.Cleanup(preWarmTasks.Wait)
	require.NoError(t, cache.GetCache().Clear())
	jobs := make(chan types.Update, 2)
	usePublicationQueue(t, func(u types.Update) { jobs <- u })
	target, err := GetUpdate("queue-test", "1", "1700000000000")
	require.NoError(t, err)
	for name, value := range map[string]string{"update-metadata.json": `{"platform":"ios"}`, "metadata.json": `{"fileMetadata":{"ios":{"bundle":"bundle.hbc"}}}`, "bundle.hbc": "bundle", "expoConfig.json": "{}"} {
		require.NoError(t, bucket.GetBucket().UploadFileIntoUpdate(*target, name, bytes.NewBufferString(value)))
	}
	require.NoError(t, MarkUpdateAsChecked(*target))
	select {
	case got := <-jobs:
		require.Equal(t, *target, got)
	case <-time.After(time.Second):
		t.Fatal("finalization did not enqueue")
	}
	require.True(t, IsUpdateValid(*target))
}

// Replace only the background executor; keep the production finalization path.
func usePublicationQueue(t *testing.T, run func(types.Update)) {
	t.Helper()
	q := newPatchQueue(run, 32)
	publicationPatches.queue = q
	publicationPatches.Once = sync.Once{}
	publicationPatches.Do(func() {})
	t.Cleanup(func() {
		close(q.jobs)
		publicationPatches.queue = nil
		publicationPatches.Once = sync.Once{}
	})
}
