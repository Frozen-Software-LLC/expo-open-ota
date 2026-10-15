package cdn

import (
	"context"
	"expo-open-ota/internal/bundlepatch"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var patchClient = &http.Client{
	Timeout: 500 * time.Millisecond,
	// A storage redirect/login page is not evidence that the patch edge is ready.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type patchProbe struct {
	ready  bool
	expiry time.Time
}

var patchProbes = struct {
	sync.Mutex
	entries map[string]patchProbe
}{entries: make(map[string]patchProbe)}

// PatchReady avoids redirecting clients to an ordinary object CDN that lacks
// Expo's patch headers. Only deployment-configured URLs are probed. Cold probes
// are bounded; concurrent requests use direct serving while a probe is in flight.
func PatchReady(ctx context.Context, location string, record bundlepatch.Record) bool {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	key := location + ":" + record.BaseID + ":" + record.PatchHash + ":" + strconv.Itoa(record.PatchBytes)
	now := time.Now()
	patchProbes.Lock()
	if found, ok := patchProbes.entries[key]; ok && now.Before(found.expiry) {
		patchProbes.Unlock()
		return found.ready
	}
	if len(patchProbes.entries) >= 1024 {
		for key, entry := range patchProbes.entries {
			if !now.Before(entry.expiry) {
				delete(patchProbes.entries, key)
			}
		}
	}
	if len(patchProbes.entries) >= 1024 {
		patchProbes.Unlock()
		return false
	}
	patchProbes.entries[key] = patchProbe{expiry: now.Add(30 * time.Second)}
	patchProbes.Unlock()

	ready := probePatch(ctx, location, record)
	ttl := 30 * time.Second
	if ready {
		ttl = 5 * time.Minute
	}
	patchProbes.Lock()
	patchProbes.entries[key] = patchProbe{ready: ready, expiry: time.Now().Add(ttl)}
	patchProbes.Unlock()
	return ready
}

func probePatch(ctx context.Context, location string, record bundlepatch.Record) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, location, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := patchClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK &&
		strings.EqualFold(resp.Header.Get("IM"), "bsdiff") &&
		resp.Header.Get("Expo-Base-Update-ID") == record.BaseID &&
		resp.Header.Get("X-OTA-Patch-SHA256") == record.PatchHash &&
		resp.ContentLength == int64(record.PatchBytes) &&
		(resp.Header.Get("Content-Encoding") == "" || resp.Header.Get("Content-Encoding") == "identity")
}
