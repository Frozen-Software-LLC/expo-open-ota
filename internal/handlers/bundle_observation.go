package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"expo-open-ota/internal/assets"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/metrics"
	"expo-open-ota/internal/update"
	"github.com/google/uuid"
	"net/http"
	"sync"
	"time"
)

func isLaunchAsset(req assets.AssetsRequest) bool {
	if req.Platform != "ios" && req.Platform != "android" {
		return false
	}
	target, err := assets.ResolveUpdate(req)
	if err != nil || target == nil {
		return false
	}
	metadata, err := update.GetMetadata(*target)
	if err != nil {
		return false
	}
	if req.Platform == "android" {
		return req.AssetName == metadata.MetadataJSON.FileMetadata.Android.Bundle
	}
	return req.AssetName == metadata.MetadataJSON.FileMetadata.IOS.Bundle
}

// Hash identifiers together so short-lived correlation state contains no raw
// installation ID. Shared Redis supports retries routed to another replica.
func patchRequestKey(r *http.Request, req assets.AssetsRequest) string {
	ids := make([]string, 0, 3)
	for _, header := range []string{"EAS-Client-ID", "Expo-Current-Update-ID", "Expo-Requested-Update-ID"} {
		id, err := uuid.Parse(r.Header.Get(header))
		if err != nil || id == uuid.Nil {
			return ""
		}
		ids = append(ids, id.String())
	}
	data, _ := json.Marshal(append(ids, req.Branch, req.RuntimeVersion, req.Platform, req.UpdateID, req.AssetName))
	sum := sha256.Sum256(data)
	return "bundle_patch_attempt:" + hex.EncodeToString(sum[:])
}

// The shared cache's local implementation only expires keys on read. Use a
// bounded local tracker here so one-off installation IDs cannot grow it forever.
var localPatchAttempts = struct {
	sync.Mutex
	entries map[string]time.Time
}{entries: make(map[string]time.Time)}

func rememberPatchRequest(r *http.Request, req assets.AssetsRequest) {
	key := patchRequestKey(r, req)
	if key == "" {
		return
	}
	if cache.ResolveCacheType() == cache.RedisCacheType {
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
		defer cancel()
		if shared, ok := cache.GetCache().(*cache.RedisCache); ok {
			_ = shared.SetWithContext(ctx, key, "1", 10*time.Minute)
		}
		return
	}
	localPatchAttempts.Lock()
	defer localPatchAttempts.Unlock()
	now := time.Now()
	if len(localPatchAttempts.entries) >= 10000 {
		for k, expiry := range localPatchAttempts.entries {
			if !expiry.After(now) {
				delete(localPatchAttempts.entries, k)
			}
		}
	}
	if len(localPatchAttempts.entries) < 10000 {
		localPatchAttempts.entries[key] = now.Add(10 * time.Minute)
	}
}

func recentlySentPatch(key string) bool {
	if key == "" {
		return false
	}
	if cache.ResolveCacheType() == cache.RedisCacheType {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if shared, ok := cache.GetCache().(*cache.RedisCache); ok {
			return shared.GetWithContext(ctx, key) == "1"
		}
		return false
	}
	localPatchAttempts.Lock()
	defer localPatchAttempts.Unlock()
	expiry, exists := localPatchAttempts.entries[key]
	if exists && !expiry.After(time.Now()) {
		delete(localPatchAttempts.entries, key)
		return false
	}
	return exists
}

type bundleResponse struct {
	http.ResponseWriter
	status, bytes int
	failed        bool
	reason        string
}

func (w *bundleResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *bundleResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *bundleResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	w.failed = w.failed || err != nil || n != len(body)
	return n, err
}
func (w *bundleResponse) observe(r *http.Request, req assets.AssetsRequest) {
	delivery := "error"
	switch {
	case w.failed:
		delivery = "write_error"
	case w.status == http.StatusIMUsed:
		delivery = "patch"
	case w.status == http.StatusOK:
		delivery = "full"
	case w.status == http.StatusFound:
		delivery = "full_redirect"
	}
	// Expo's rejection retry removes A-IM but keeps installation/base/target IDs.
	// A later matching download can look identical: deliberately name this a
	// correlation and count requests, never claim an exact patch failure rate.
	if (delivery == "full" || delivery == "full_redirect") && r.Header.Get("A-IM") == "" {
		if key := patchRequestKey(r, req); recentlySentPatch(key) {
			w.reason = "full_after_patch"
		}
	}
	metrics.TrackBundleResponse(req.Platform, delivery, w.reason, w.bytes)
}
