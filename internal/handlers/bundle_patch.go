package handlers

import (
	"encoding/json"
	"expo-open-ota/internal/assets"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/bundlepatch"
	"expo-open-ota/internal/cdn"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"
	"github.com/google/uuid"
	"net/http"
	"strings"
)

// Only precomputed, validated launch-asset patches are served. Missing or
// invalid artifacts fall through to the ordinary full asset/CDN response.
func tryServeBundlePatch(w http.ResponseWriter, r *http.Request, req assets.AssetsRequest) (bool, string) {
	if !bundlepatch.AcceptsPatch(r) {
		return false, "not_negotiated"
	}
	if req.UpdateID == "" {
		return false, "unpinned"
	}
	baseID, err := uuid.Parse(r.Header.Get("Expo-Current-Update-ID"))
	if err != nil {
		return false, "invalid_base"
	}
	targetID, err := uuid.Parse(r.Header.Get("Expo-Requested-Update-ID"))
	if err != nil || baseID == targetID {
		return false, "invalid_target"
	}
	key, err := bundlepatch.Key(req.Platform, baseID.String())
	if err != nil {
		return false, "invalid_platform"
	}
	target, err := assets.ResolveUpdate(req)
	if err != nil || target == nil {
		return false, "target_unavailable"
	}
	storedData, err := readCachedPatchObject(*target, "update-metadata.json", 64<<10)
	var stored types.UpdateStoredMetadata
	if err != nil || json.Unmarshal(storedData, &stored) != nil || !strings.EqualFold(stored.UpdateUUID, targetID.String()) || stored.Platform != req.Platform {
		return false, "target_mismatch"
	}
	metadata, err := update.GetMetadata(*target)
	if err != nil {
		return false, "metadata_unavailable"
	}
	platform := metadata.MetadataJSON.FileMetadata.IOS
	if req.Platform == "android" {
		platform = metadata.MetadataJSON.FileMetadata.Android
	}
	if req.AssetName != platform.Bundle {
		return false, "not_launch_asset"
	}
	data, err := readCachedPatchObject(*target, key+".json", 4096)
	if err != nil {
		return false, "patch_unavailable"
	}
	var record bundlepatch.Record
	if json.Unmarshal(data, &record) != nil || record.BaseID != baseID.String() || record.TargetID != targetID.String() || record.Runtime != req.RuntimeVersion || record.Platform != req.Platform || record.Asset != req.AssetName || record.PatchBytes <= 0 || record.PatchBytes > bundlepatch.MaxPatchBytes || record.GzipBytes <= 0 || int64(record.PatchBytes)*10 >= int64(record.GzipBytes)*9 {
		return false, "invalid_record"
	}
	objectKey, err := bundlepatch.ObjectKey(record)
	if err != nil {
		return false, "invalid_record"
	}
	if record.Version == 2 && r.Header.Get("prevent-cdn-redirection") != "true" {
		if provider := cdn.GetCDN(); provider != nil {
			location, err := provider.ComputeRedirectionURLForAsset(req.Branch, req.RuntimeVersion, req.UpdateID, objectKey)
			if err == nil && cdn.PatchReady(r.Context(), location, record) {
				rememberPatchRequest(r, req)
				w.Header().Set("Cache-Control", "private, no-store")
				w.Header().Set("Vary", "A-IM, Expo-Current-Update-ID, Expo-Requested-Update-ID, Accept-Encoding, prevent-cdn-redirection")
				http.Redirect(w, r, location, http.StatusFound)
				return true, "cdn_available"
			}
		}
	}
	patch, err := readPatchObject(*target, objectKey, int64(record.PatchBytes))
	if err != nil || len(patch) != record.PatchBytes || bundlepatch.Hash(patch) != record.PatchHash {
		return false, "invalid_patch"
	}
	rememberPatchRequest(r, req)
	bundlepatch.Serve(w, record, patch)
	return true, "available"
}

func readPatchObject(target types.Update, key string, max int64) ([]byte, error) {
	file, err := bucket.GetBucket().GetFile(target, key)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, http.ErrMissingFile
	}
	defer file.Reader.Close()
	return bundlepatch.ReadBounded(file.Reader, max)
}
