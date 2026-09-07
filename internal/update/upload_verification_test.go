package update

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/types"
)

type verificationBucket struct {
	bucket.Bucket
	getFile func(string) (*types.BucketFile, error)
}

func (b verificationBucket) GetFile(_ types.Update, path string) (*types.BucketFile, error) {
	return b.getFile(path)
}

func TestVerifyUploadedFilesRejectsMissingAndStorageErrors(t *testing.T) {
	storageError := errors.New("storage unavailable")
	for _, failure := range []error{nil, storageError} {
		storage := verificationBucket{getFile: func(string) (*types.BucketFile, error) {
			return nil, failure
		}}
		err := verifyUploadedFiles(storage, types.Update{}, []string{"bundle.js"})
		if err == nil || !strings.Contains(err.Error(), "bundle.js") {
			t.Fatalf("expected a file-specific verification failure, got %v", err)
		}
		if failure != nil && !errors.Is(err, storageError) {
			t.Fatalf("storage error was lost: %v", err)
		}
	}
}

func TestVerifyUploadedFilesDeduplicatesAndBoundsConcurrency(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	active, maximum := 0, 0
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	storage := verificationBucket{getFile: func(path string) (*types.BucketFile, error) {
		mu.Lock()
		calls[path]++
		active++
		maximum = max(maximum, active)
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return &types.BucketFile{Reader: io.NopCloser(strings.NewReader("asset"))}, nil
	}}
	files := []string{}
	for i := 0; i < 16; i++ {
		files = append(files, fmt.Sprint(i), fmt.Sprint(i))
	}
	done := make(chan error, 1)
	go func() { done <- verifyUploadedFiles(storage, types.Update{}, files) }()
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("verification did not make concurrent progress")
		}
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum != 8 || len(calls) != 16 {
		t.Fatalf("unexpected concurrency=%d or unique files=%d", maximum, len(calls))
	}
	for path, count := range calls {
		if count != 1 {
			t.Errorf("file %s checked %d times", path, count)
		}
	}
}
