package handlers

import (
	"container/list"
	"encoding/json"
	"errors"
	"expo-open-ota/internal/types"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const patchLookupTTL = 60 * time.Second
const patchLookupMaxEntries = 1024
const patchLookupMaxBytes = 4 << 20

// Keep these small lookups in-process: cache hits add neither bucket nor
// Redis reads. Entries (including confirmed absence) expire after 60 seconds.
// The LRU and byte budget bound memory even with many distinct client base IDs.
var bundlePatchObjects = newPatchObjectCache(time.Now)

type patchObjectEntry struct {
	key     string
	data    []byte
	missing bool
	expires time.Time
}
type patchObjectCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	bytes   int
	now     func() time.Time
	flights singleflight.Group
}

func newPatchObjectCache(now func() time.Time) *patchObjectCache {
	return &patchObjectCache{entries: make(map[string]*list.Element), lru: list.New(), now: now}
}
func (c *patchObjectCache) remove(e *list.Element) {
	entry := e.Value.(patchObjectEntry)
	delete(c.entries, entry.key)
	c.bytes -= len(entry.data)
	c.lru.Remove(e)
}
func (c *patchObjectCache) lookup(key string) (patchObjectEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return patchObjectEntry{}, false
	}
	entry := e.Value.(patchObjectEntry)
	if !c.now().Before(entry.expires) {
		c.remove(e)
		return patchObjectEntry{}, false
	}
	c.lru.MoveToFront(e)
	return entry, true
}
func (c *patchObjectCache) get(key string, load func() ([]byte, error)) ([]byte, error) {
	resolve := func(e patchObjectEntry) ([]byte, error) {
		if e.missing {
			return nil, http.ErrMissingFile
		}
		return e.data, nil
	}
	if e, ok := c.lookup(key); ok {
		return resolve(e)
	}
	value, err, _ := c.flights.Do(key, func() (any, error) {
		if e, ok := c.lookup(key); ok {
			return e, nil
		}
		data, err := load()
		// Never turn timeouts, permission errors or failed reads into cached misses.
		if err != nil && !errors.Is(err, http.ErrMissingFile) {
			return nil, err
		}
		entry := patchObjectEntry{key: key, data: data, missing: errors.Is(err, http.ErrMissingFile), expires: c.now().Add(patchLookupTTL)}
		if len(data) <= patchLookupMaxBytes {
			c.mu.Lock()
			if old, ok := c.entries[key]; ok {
				c.remove(old)
			}
			for c.lru.Len() >= patchLookupMaxEntries || c.bytes+len(data) > patchLookupMaxBytes {
				c.remove(c.lru.Back())
			}
			c.entries[key] = c.lru.PushFront(entry)
			c.bytes += len(data)
			c.mu.Unlock()
		}
		return entry, nil
	})
	if err != nil {
		return nil, err
	}
	return resolve(value.(patchObjectEntry))
}
func readCachedPatchObject(target types.Update, key string, max int64) ([]byte, error) {
	// A JSON tuple keeps branch/runtime/object boundaries unambiguous.
	identity, _ := json.Marshal([4]string{target.Branch, target.RuntimeVersion, target.UpdateId, key})
	return bundlePatchObjects.get(string(identity), func() ([]byte, error) { return readPatchObject(target, key, max) })
}
