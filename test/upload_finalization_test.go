package test

import (
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"expo-open-ota/internal/update"
	"github.com/stretchr/testify/require"
)

func TestUploadFinalizationRetriesPreservePublishedUpdate(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		t.Run(platform, func(t *testing.T) {
			defer setup(t)()
			mockExpoForRequestUploadUrlTest("staging")
			root, err := findProjectRoot()
			require.NoError(t, err)
			fixture := filepath.Join(root, "test", "test-updates", "branch-4", "1", "1674170952")
			id := performUpload(t, root, "DO_NOT_USE", "1", fixture, platform)

			// Model overlapping retries when the first HTTP response is lost.
			codes := make(chan int, 4)
			var requests sync.WaitGroup
			for i := 0; i < 4; i++ {
				requests.Add(1)
				go func() {
					defer requests.Done()
					codes <- markUpdateAsUploaded(t, "DO_NOT_USE", "1", id, platform).Code
				}()
			}
			requests.Wait()
			close(codes)
			for code := range codes {
				require.Equal(t, http.StatusOK, code)
			}
			current, err := update.GetUpdate("DO_NOT_USE", "1", id)
			require.NoError(t, err)
			require.True(t, update.IsUpdateValid(*current))
			require.NoError(t, update.VerifyUploadedUpdate(*current))
			// Retry after completion, when latest now points at this same update.
			require.Equal(t, http.StatusOK, markUpdateAsUploaded(t, "DO_NOT_USE", "1", id, platform).Code)
			require.True(t, update.IsUpdateValid(*current))

			// A late retry must preserve the historical update after a new release.
			nextFixture := filepath.Join(root, "test", "test-updates", "branch-4", "1", "1674170951")
			nextID := performUpload(t, root, "DO_NOT_USE", "1", nextFixture, platform)
			require.Equal(t, http.StatusOK, markUpdateAsUploaded(t, "DO_NOT_USE", "1", nextID, platform).Code)
			require.Equal(t, http.StatusOK, markUpdateAsUploaded(t, "DO_NOT_USE", "1", id, platform).Code)
			require.True(t, update.IsUpdateValid(*current))
			latest, err := update.GetLatestUpdateBundlePathForRuntimeVersion("DO_NOT_USE", "1", platform)
			require.NoError(t, err)
			require.Equal(t, nextID, latest.UpdateId)
		})
	}
}
