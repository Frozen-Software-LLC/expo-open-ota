package cache

import (
	"fmt"
	"sync"
	"testing"
)

// Expiration is a write, even when reached through Get. Concurrent requests
// must be able to expire different entries in the same map safely.
func TestLocalCacheConcurrentExpiration(t *testing.T) {
	c := NewLocalCache()
	expiredTTL := -1
	const workers, entries = 32, 128
	for worker := 0; worker < workers; worker++ {
		for entry := 0; entry < entries; entry++ {
			if err := c.Set(fmt.Sprintf("expired:%d:%d", worker, entry), "old", &expiredTTL); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := c.Set("live", "keep", nil); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for entry := 0; entry < entries; entry++ {
				if got := c.Get(fmt.Sprintf("expired:%d:%d", worker, entry)); got != "" {
					t.Errorf("expired entry returned %q", got)
				}
				if got := c.Get("live"); got != "keep" {
					t.Errorf("live entry returned %q", got)
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	if len(c.items) != 1 {
		t.Fatalf("expired entries retained: got %d entries, want only live entry", len(c.items))
	}
}

func TestLocalCacheConcurrentRefresh(t *testing.T) {
	c := NewLocalCache()
	expiredTTL, freshTTL := -1, 3600
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			key := fmt.Sprintf("refresh:%d", worker)
			for cycle := 0; cycle < 128; cycle++ {
				_ = c.Set(key, "expired", &expiredTTL)
				var readers sync.WaitGroup
				for reader := 0; reader < 4; reader++ {
					readers.Add(1)
					go func() { defer readers.Done(); c.Get(key) }()
				}
				_ = c.Set(key, "fresh", &freshTTL)
				readers.Wait()
				if got := c.Get(key); got != "fresh" {
					t.Errorf("refresh lost: got %q", got)
				}
				c.Delete(key)
			}
		}(worker)
	}
	wg.Wait()
}

func BenchmarkLocalCacheGet(b *testing.B) {
	c := NewLocalCache()
	_ = c.Set("live", "value", nil)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get("live")
		}
	})
}
