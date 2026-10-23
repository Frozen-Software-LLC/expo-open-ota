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

// testClock is safe to read from a background refresh.
type testClock struct{ seconds atomic.Int64 }

func (c *testClock) now() time.Time           { return time.Unix(c.seconds.Load(), 0) }
func (c *testClock) advance(by time.Duration) { c.seconds.Add(int64(by / time.Second)) }

// within fails the test if get waits for storage instead of answering.
func within(t *testing.T, get func() ([]byte, error)) ([]byte, error) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() { data, err := get(); done <- result{data, err} }()
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("the lookup waited for storage")
		return nil, nil
	}
}

func TestPatchObjectCacheAnswersStaleEntryWhileOneReadRefreshesIt(t *testing.T) {
	clock := &testClock{}
	c := newPatchObjectCache(clock.now)
	_, err := c.get("record", func() ([]byte, error) { return []byte("old"), nil })
	require.NoError(t, err)
	clock.advance(patchLookupTTL - time.Second)
	cached, err := c.get("record", func() ([]byte, error) { t.Error("fresh entry was read again"); return nil, nil })
	require.NoError(t, err)
	require.Equal(t, "old", string(cached))

	clock.advance(time.Second)
	release := make(chan struct{})
	var reads atomic.Int32
	slowStorage := func() ([]byte, error) { reads.Add(1); <-release; return []byte("new"), nil }
	for i := 0; i < 50; i++ {
		data, err := within(t, func() ([]byte, error) { return c.get("record", slowStorage) })
		require.NoError(t, err)
		require.Equal(t, "old", string(data))
	}
	require.Eventually(t, func() bool { return reads.Load() == 1 }, 5*time.Second, time.Millisecond)
	close(release)
	require.Eventually(t, func() bool {
		data, _ := c.get("record", slowStorage)
		return string(data) == "new"
	}, 5*time.Second, time.Millisecond)
	require.EqualValues(t, 1, reads.Load())
}

func TestPatchObjectCacheFindsNewlyPublishedRecord(t *testing.T) {
	clock := &testClock{}
	c := newPatchObjectCache(clock.now)
	var published atomic.Bool
	load := func() ([]byte, error) {
		if published.Load() {
			return []byte("record"), nil
		}
		return nil, http.ErrMissingFile
	}
	_, err := c.get("missing", load)
	require.ErrorIs(t, err, http.ErrMissingFile)
	published.Store(true)
	_, err = c.get("missing", load)
	require.ErrorIs(t, err, http.ErrMissingFile, "absence is cached while fresh")
	clock.advance(patchLookupTTL)
	require.Eventually(t, func() bool {
		data, err := c.get("missing", load)
		return err == nil && string(data) == "record"
	}, 5*time.Second, time.Millisecond)
}

func TestPatchObjectCacheKeepsLastAnswerWhenStorageFails(t *testing.T) {
	clock := &testClock{}
	c := newPatchObjectCache(clock.now)
	failure := errors.New("temporary storage failure")
	var failing atomic.Bool
	var reads atomic.Int32
	load := func() ([]byte, error) {
		reads.Add(1)
		if failing.Load() {
			return nil, failure
		}
		return []byte("record"), nil
	}

	failing.Store(true)
	_, err := c.get("error", load)
	require.ErrorIs(t, err, failure, "a failed read is never cached as absence")
	failing.Store(false)
	recovered, err := c.get("error", load)
	require.NoError(t, err)
	require.Equal(t, "record", string(recovered))

	failing.Store(true)
	clock.advance(patchLookupTTL)
	before := reads.Load()
	data, err := c.get("error", load)
	require.NoError(t, err)
	require.Equal(t, "record", string(data))
	require.Eventually(t, func() bool { return reads.Load() > before }, 5*time.Second, time.Millisecond)
	data, err = c.get("error", load)
	require.NoError(t, err)
	require.Equal(t, "record", string(data), "a failed refresh keeps the last answer")
	afterFailure := reads.Load()
	for i := 0; i < 10; i++ {
		_, _ = c.get("error", load)
	}
	require.Eventually(t, func() bool { _, running := c.refreshing.Load("error"); return !running }, 5*time.Second, time.Millisecond)
	require.Equal(t, afterFailure, reads.Load(), "a failed refresh waits before the next read")
	clock.advance(patchLookupTTL)
	_, _ = c.get("error", load)
	require.Eventually(t, func() bool { return reads.Load() == afterFailure+1 }, 5*time.Second, time.Millisecond)

	clock.advance(patchLookupMaxAge)
	require.Eventually(t, func() bool {
		_, err = c.get("error", load)
		return err != nil
	}, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, err, failure, "the last answer is kept for at most patchLookupMaxAge")
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
