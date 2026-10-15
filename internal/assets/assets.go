package assets

import (
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/cdn"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strings"
)

type AssetsRequest struct {
	UpdateID       string
	Branch         string
	AssetName      string
	RuntimeVersion string
	Platform       string
	RequestID      string
}

type AssetsResponse struct {
	StatusCode  int
	Headers     map[string]string
	Body        []byte
	ContentType string
	URL         string
}

// ResolveUpdate pins new manifest asset URLs to the selected release, so a
// promotion during download cannot substitute a newer full bundle or patch.
func ResolveUpdate(req AssetsRequest) (*types.Update, error) {
	if req.UpdateID == "" {
		return update.GetLatestUpdateBundlePathForRuntimeVersion(req.Branch, req.RuntimeVersion, req.Platform)
	}
	if req.Branch == "" || strings.ContainsAny(req.Branch, "/\\") || req.RuntimeVersion == "" || strings.ContainsAny(req.RuntimeVersion, "/\\") || req.Branch == "." || req.RuntimeVersion == "." || req.Branch == ".." || req.RuntimeVersion == ".." {
		return nil, fmt.Errorf("invalid update path")
	}
	target, err := update.GetUpdate(req.Branch, req.RuntimeVersion, req.UpdateID)
	if err != nil {
		return nil, fmt.Errorf("invalid target update")
	}
	// A manifest may reference many assets. Avoid re-reading validation objects
	// from storage for every file while keeping removal checks short-lived.
	key := fmt.Sprintf("pinnedAssetUpdate:v1:%s:%s:%s:%s", req.Branch, req.RuntimeVersion, req.Platform, req.UpdateID)
	resolvedCache := cache.GetCache()
	if resolvedCache.Get(key) == "valid" {
		return target, nil
	}
	if !update.IsUpdateValid(*target) || update.GetUpdateType(*target) != types.NormalUpdate {
		return nil, fmt.Errorf("invalid target update")
	}
	metadata, err := update.RetrieveUpdateStoredMetadata(*target)
	if err != nil || metadata == nil || metadata.Platform != req.Platform {
		return nil, fmt.Errorf("target platform mismatch")
	}
	ttl := 60
	_ = resolvedCache.Set(key, "valid", &ttl)
	return target, nil
}

func getAssetMetadata(req AssetsRequest, returnAsset bool) (AssetsResponse, *types.BucketFile, string, error) {
	requestID := req.RequestID

	if req.AssetName == "" {
		log.Printf("[RequestID: %s] No asset name provided", requestID)
		return AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("No asset name provided")}, nil, "", nil
	}

	if req.Platform == "" || (req.Platform != "ios" && req.Platform != "android") {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, req.Platform)
		return AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("Invalid platform")}, nil, "", nil
	}

	if req.RuntimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		return AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("No runtime version provided")}, nil, "", nil
	}

	lastUpdate, err := ResolveUpdate(req)
	if err != nil || lastUpdate == nil {
		log.Printf("[RequestID: %s] No update found for runtimeVersion: %s", requestID, req.RuntimeVersion)
		return AssetsResponse{StatusCode: http.StatusNotFound, Body: []byte("No update found")}, nil, "", nil
	}

	if !returnAsset {
		headers := map[string]string{
			"expo-protocol-version": "1",
			"expo-sfv-version":      "0",
			"Cache-Control":         "public, max-age=31536000",
			"Vary":                  "Accept-Encoding, A-IM, Expo-Current-Update-ID, Expo-Requested-Update-ID",
		}
		return AssetsResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
		}, nil, lastUpdate.UpdateId, nil
	}

	metadata, err := update.GetMetadata(*lastUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error getting metadata: %v", requestID, err)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error getting metadata")}, nil, "", nil
	}

	var platformMetadata types.PlatformMetadata
	switch req.Platform {
	case "android":
		platformMetadata = metadata.MetadataJSON.FileMetadata.Android
	case "ios":
		platformMetadata = metadata.MetadataJSON.FileMetadata.IOS
	default:
		return AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("Platform not supported")}, nil, "", nil
	}

	bundle := platformMetadata.Bundle
	isLaunchAsset := bundle == req.AssetName

	var assetMetadata types.Asset
	for _, asset := range platformMetadata.Assets {
		if asset.Path == req.AssetName {
			assetMetadata = asset
		}
	}

	resolvedBucket := bucket.GetBucket()
	asset, err := resolvedBucket.GetFile(*lastUpdate, req.AssetName)
	if err != nil {
		log.Printf("[RequestID: %s] Error getting asset: %v", requestID, err)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error getting asset")}, nil, "", nil
	}

	var contentType string
	if isLaunchAsset {
		contentType = "application/javascript"
	} else {
		contentType = mime.TypeByExtension("." + string(assetMetadata.Ext))
	}

	headers := map[string]string{
		"expo-protocol-version": "1",
		"expo-sfv-version":      "0",
		"Cache-Control":         "public, max-age=31536000",
		"Vary":                  "Accept-Encoding, A-IM, Expo-Current-Update-ID, Expo-Requested-Update-ID",
		"Content-Type":          contentType,
	}

	return AssetsResponse{
		StatusCode:  http.StatusOK,
		Headers:     headers,
		ContentType: contentType,
	}, asset, lastUpdate.UpdateId, nil
}

func HandleAssetsWithFile(req AssetsRequest) (AssetsResponse, error) {
	resp, asset, _, err := getAssetMetadata(req, true)
	if err != nil {
		return resp, err
	}
	if resp.StatusCode != 200 {
		return AssetsResponse{
			StatusCode: resp.StatusCode,
			Body:       resp.Body,
		}, nil
	}

	if asset == nil {
		log.Printf("[RequestID: %s] Resolved file is nil", req.RequestID)
		return AssetsResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       []byte("Resolved file is nil"),
		}, nil
	}

	buffer, err := bucket.ConvertReadCloserToBytes(asset.Reader)
	defer asset.Reader.Close()
	if err != nil {
		log.Printf("[RequestID: %s] Error converting asset to buffer: %v", req.RequestID, err)
		return AssetsResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       []byte("Error converting asset to buffer"),
		}, err
	}

	resp.Body = buffer
	return resp, nil
}

func HandleAssetsWithURL(req AssetsRequest, resolvedCDN cdn.CDN) (AssetsResponse, error) {
	resp, _, updateId, err := getAssetMetadata(req, false)
	if err != nil {
		return resp, err
	}
	if resp.StatusCode != 200 {
		return AssetsResponse{
			StatusCode: resp.StatusCode,
			Body:       resp.Body,
		}, nil
	}
	resp.URL, err = resolvedCDN.ComputeRedirectionURLForAsset(req.Branch, req.RuntimeVersion, updateId, req.AssetName)
	if err != nil {
		log.Printf("[RequestID: %s] Error computing redirection URL: %v", req.RequestID, err)
		return AssetsResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       []byte("Error computing redirection URL"),
		}, err
	}
	return resp, nil
}
