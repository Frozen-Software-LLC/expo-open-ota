package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPatchObjectCacheExpiryAndRecovery(t *testing.T) {
	now := time.Unix(0, 0)
	c := newPatchObjectCache(func() time.Time { return now })
	calls := 0
	body := []byte("first")
	var failure error
	load := func() ([]byte, error) { calls++; return body, failure }
	first, err := c.get("record", load)
	require.NoError(t, err)
	require.Equal(t, "first", string(first))
	body = []byte("second")
	now = now.Add(59 * time.Second)
	cached, err := c.get("record", load)
	require.NoError(t, err)
	require.Equal(t, "first", string(cached))
	require.Equal(t, 1, calls)
	now = now.Add(time.Second)
	refreshed, err := c.get("record", load)
	require.NoError(t, err)
	require.Equal(t, "second", string(refreshed))
	require.Equal(t, 2, calls)

	failure = http.ErrMissingFile
	body = nil
	_, err = c.get("missing", load)
	require.ErrorIs(t, err, http.ErrMissingFile)
	failure = nil
	body = []byte("newly published")
	_, err = c.get("missing", load)
	require.ErrorIs(t, err, http.ErrMissingFile)
	require.Equal(t, 3, calls)
	now = now.Add(patchLookupTTL)
	published, err := c.get("missing", load)
	require.NoError(t, err)
	require.Equal(t, "newly published", string(published))
	require.Equal(t, 4, calls)

	failure = errors.New("temporary storage failure")
	_, err = c.get("error", load)
	require.ErrorIs(t, err, failure)
	failure = nil
	recovered, err := c.get("error", load)
	require.NoError(t, err)
	require.Equal(t, body, recovered)
	require.Equal(t, 6, calls)
}

func TestPatchObjectCacheCoalescesConcurrentRequests(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			c := newPatchObjectCache(time.Now)
			var calls atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			start := make(chan struct{})
			load := func() ([]byte, error) {
				if calls.Add(1) == 1 {
					close(entered)
				}
				<-release
				if missing {
					return nil, http.ErrMissingFile
				}
				return []byte("record"), nil
			}
			var wg sync.WaitGroup
			for i := 0; i < 100; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					data, err := c.get("same", load)
					if missing {
						if !errors.Is(err, http.ErrMissingFile) {
							t.Errorf("missing result: %v", err)
						}
					} else if err != nil || string(data) != "record" {
						t.Errorf("result: %s %v", data, err)
					}
				}()
			}
			close(start)
			<-entered
			close(release)
			wg.Wait()
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestPatchObjectCacheBoundsMemoryAndSeparatesKeys(t *testing.T) {
	c := newPatchObjectCache(time.Now)
	for i := 0; i < patchLookupMaxEntries+20; i++ {
		_, err := c.get(fmt.Sprint(i), func() ([]byte, error) { return nil, http.ErrMissingFile })
		require.ErrorIs(t, err, http.ErrMissingFile)
	}
	require.Equal(t, patchLookupMaxEntries, len(c.entries))
	_, ok := c.lookup("0")
	require.False(t, ok)
	for i := 0; i < 100; i++ {
		_, err := c.get("large"+fmt.Sprint(i), func() ([]byte, error) { return make([]byte, 64<<10), nil })
		require.NoError(t, err)
	}
	require.LessOrEqual(t, c.bytes, patchLookupMaxBytes)
	require.LessOrEqual(t, len(c.entries), patchLookupMaxEntries)
	for _, key := range []string{"ios/base1", "ios/base2", "android/base1", "other-runtime/base1"} {
		_, err := c.get(key, func() ([]byte, error) { return []byte(key), nil })
		require.NoError(t, err)
	}
	for _, key := range []string{"ios/base1", "ios/base2", "android/base1", "other-runtime/base1"} {
		data, err := c.get(key, func() ([]byte, error) { t.Error("unexpected reload"); return nil, nil })
		require.NoError(t, err)
		require.Equal(t, key, string(data))
	}
}
