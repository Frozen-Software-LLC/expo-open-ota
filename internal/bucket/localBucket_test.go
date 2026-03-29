package bucket

import (
	"expo-open-ota/internal/types"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetFile_ValidAssetPath(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	projectRoot, err := os.Getwd()
	assert.Nil(t, err)
	basePath := filepath.Join(projectRoot, "..", "..", "test", "test-updates")

	b := &LocalBucket{BasePath: basePath}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "1674170951",
		CreatedAt:      time.Duration(1674170951) * time.Millisecond,
	}

	file, err := b.GetFile(update, "metadata.json")
	assert.Nil(t, err)
	assert.NotNil(t, file)
	file.Reader.Close()
}

func TestGetFile_PathTraversalBlocked(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	file, err := b.GetFile(update, "../../../etc/passwd")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
	assert.Nil(t, file)
}

func TestGetFile_PathTraversalMultipleLevels(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	file, err := b.GetFile(update, "../../../../etc/shadow")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
	assert.Nil(t, file)
}

func TestGetFile_NormalSubdirectoryAllowed(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	// This path is valid (stays within the update dir), just won't exist on disk
	file, err := b.GetFile(update, "assets/image.png")
	assert.Nil(t, err)
	assert.Nil(t, file) // file doesn't exist, but no path traversal error
}
