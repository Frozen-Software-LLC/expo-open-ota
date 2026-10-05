package handlers

import (
	"bytes"
	"encoding/json"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/bundlepatch"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"
	"fmt"
	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/stretchr/testify/require"
	"math/rand"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

const baseUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const targetUUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func TestBundlePatchAssets(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		t.Run(platform, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("STORAGE_MODE", "local")
			t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
			t.Setenv("BASE_URL", "http://localhost:9090")
			t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "true")
			t.Setenv("CDN_TYPE", "")
			bucket.ResetBucketInstance()
			require.NoError(t, cache.GetCache().Clear())
			t.Cleanup(bucket.ResetBucketInstance)
			base := make([]byte, 32000)
			rand.New(rand.NewSource(1)).Read(base)
			target := bytes.Clone(base)
			copy(target[300:], []byte("new bundle content"))
			dir := filepath.Join(root, "local-diff", "9090.1", "1700000000000")
			require.NoError(t, os.MkdirAll(dir, 0755))
			write := func(name string, data []byte) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0644))
			}
			write("bundle.hbc", target)
			write(".check", []byte("checked"))
			write("expoConfig.json", []byte(`{}`))
			write("metadata.json", []byte(fmt.Sprintf(`{"version":0,"bundler":"metro","fileMetadata":{"%s":{"bundle":"bundle.hbc","assets":[]}}}`, platform)))
			stored, _ := json.Marshal(types.UpdateStoredMetadata{UpdateUUID: targetUUID, Platform: platform})
			write("update-metadata.json", stored)
			record, patch, err := bundlepatch.Prepare(base, target, bundlepatch.Record{BaseID: baseUUID, TargetID: targetUUID, Runtime: "9090.1", Platform: platform, Asset: "bundle.hbc"})
			require.NoError(t, err)
			key, _ := bundlepatch.Key(platform, baseUUID)
			recordJSON, _ := json.Marshal(record)
			write(key+".json", recordJSON)
			write(key+".bsdiff", patch)
			request := func(baseID, requestedID, aim string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest("GET", "/assets?branch=local-diff&runtimeVersion=9090.1&updateId=1700000000000&platform="+platform+"&asset=bundle.hbc", nil)
				r.Header.Set("Expo-Current-Update-ID", baseID)
				r.Header.Set("Expo-Requested-Update-ID", requestedID)
				r.Header.Set("A-IM", aim)
				w := httptest.NewRecorder()
				AssetsHandler(w, r)
				return w
			}
			w := request(baseUUID, targetUUID, "bsdiff")
			require.Equal(t, 226, w.Code, w.Body.String())
			actual, err := bspatch.Bytes(base, w.Body.Bytes())
			require.NoError(t, err)
			require.Equal(t, target, actual)
			for _, tc := range []struct{ name, base, target, aim string }{{"legacy", baseUUID, targetUUID, ""}, {"missing-base", "", targetUUID, "bsdiff"}, {"unknown-base", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", targetUUID, "bsdiff"}, {"wrong-target", baseUUID, baseUUID, "bsdiff"}, {"unsupported", baseUUID, targetUUID, "other"}} {
				t.Run(tc.name, func(t *testing.T) {
					w := request(tc.base, tc.target, tc.aim)
					require.Equal(t, 200, w.Code)
					require.Equal(t, target, w.Body.Bytes())
				})
			}
			t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "false")
			w = request(baseUUID, targetUUID, "bsdiff")
			require.Equal(t, 200, w.Code)
			require.Equal(t, target, w.Body.Bytes())
			t.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "true")
			write(key+".bsdiff", []byte("corrupt"))
			w = request(baseUUID, targetUUID, "bsdiff")
			require.Equal(t, 200, w.Code)
			require.Equal(t, target, w.Body.Bytes())
			// Publishing a newer update cannot change the bytes at a pinned asset URL.
			newer := filepath.Join(root, "local-diff", "9090.1", "1700000001000")
			require.NoError(t, os.CopyFS(newer, os.DirFS(dir)))
			require.NoError(t, os.WriteFile(filepath.Join(newer, "bundle.hbc"), []byte("different update"), 0644))
			w = request(baseUUID, targetUUID, "")
			require.Equal(t, 200, w.Code)
			require.Equal(t, target, w.Body.Bytes())
			selected, err := update.GetUpdate("local-diff", "9090.1", "1700000000000")
			require.NoError(t, err)
			meta, err := update.GetMetadata(*selected)
			require.NoError(t, err)
			manifest, err := update.ComposeUpdateManifest(&meta, *selected, platform)
			require.NoError(t, err)
			assetURL, err := url.Parse(manifest.LaunchAsset.Url)
			require.NoError(t, err)
			require.Equal(t, "1700000000000", assetURL.Query().Get("updateId"))
		})
	}
}
