package handlers

import (
	"encoding/json"
	"expo-open-ota/config"
	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"
	update2 "expo-open-ota/internal/update"
	"fmt"
	"github.com/gorilla/mux"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

type BranchMapping struct {
	BranchName     string  `json:"branchName"`
	BranchId       *string `json:"branchId"`
	ReleaseChannel *string `json:"releaseChannel"`
}

type ChannelMapping struct {
	ReleaseChannelName string  `json:"releaseChannelName"`
	ReleaseChannelId   string  `json:"releaseChannelId"`
	BranchName         *string `json:"branchName"`
	BranchId           *string `json:"branchId"`
}

type UpdateItem struct {
	UpdateUUID string `json:"updateUUID"`
	UpdateId   string `json:"updateId"`
	CreatedAt  string `json:"createdAt"`
	CommitHash string `json:"commitHash"`
	Platform   string `json:"platform"`
	Message    string `json:"message,omitempty"`
}

type UpdateDetails struct {
	UpdateUUID string           `json:"updateUUID"`
	UpdateId   string           `json:"updateId"`
	CreatedAt  string           `json:"createdAt"`
	CommitHash string           `json:"commitHash"`
	Platform   string           `json:"platform"`
	Message    string           `json:"message,omitempty"`
	Type       types.UpdateType `json:"type"`
	ExpoConfig string           `json:"expoConfig"`
}

type SettingsEnv struct {
	BASE_URL                               string `json:"BASE_URL"`
	EXPO_APP_ID                            string `json:"EXPO_APP_ID"`
	EXPO_ACCESS_TOKEN                      string `json:"EXPO_ACCESS_TOKEN"`
	CACHE_MODE                             string `json:"CACHE_MODE"`
	REDIS_HOST                             string `json:"REDIS_HOST"`
	REDIS_PORT                             string `json:"REDIS_PORT"`
	STORAGE_MODE                           string `json:"STORAGE_MODE"`
	S3_BUCKET_NAME                         string `json:"S3_BUCKET_NAME"`
	LOCAL_BUCKET_BASE_PATH                 string `json:"LOCAL_BUCKET_BASE_PATH"`
	KEYS_STORAGE_TYPE                      string `json:"KEYS_STORAGE_TYPE"`
	AWSSM_EXPO_PUBLIC_KEY_SECRET_ID        string `json:"AWSSM_EXPO_PUBLIC_KEY_SECRET_ID"`
	AWSSM_EXPO_PRIVATE_KEY_SECRET_ID       string `json:"AWSSM_EXPO_PRIVATE_KEY_SECRET_ID"`
	PUBLIC_EXPO_KEY_B64                    string `json:"PUBLIC_EXPO_KEY_B64"`
	PUBLIC_LOCAL_EXPO_KEY_PATH             string `json:"PUBLIC_LOCAL_EXPO_KEY_PATH"`
	PRIVATE_LOCAL_EXPO_KEY_PATH            string `json:"PRIVATE_LOCAL_EXPO_KEY_PATH"`
	AWS_REGION                             string `json:"AWS_REGION"`
	AWS_BASE_ENDPOINT                      string `json:"AWS_BASE_ENDPOINT"`
	AWS_ACCESS_KEY_ID                      string `json:"AWS_ACCESS_KEY_ID"`
	CLOUDFRONT_DOMAIN                      string `json:"CLOUDFRONT_DOMAIN"`
	CLOUDFRONT_KEY_PAIR_ID                 string `json:"CLOUDFRONT_KEY_PAIR_ID"`
	CLOUDFRONT_PRIVATE_KEY_B64             string `json:"CLOUDFRONT_PRIVATE_KEY_B64"`
	AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID string `json:"AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID"`
	PRIVATE_LOCAL_CLOUDFRONT_KEY_PATH      string `json:"PRIVATE_LOCAL_CLOUDFRONT_KEY_PATH"`
	PROMETHEUS_ENABLED                     string `json:"PROMETHEUS_ENABLED"`
}

func maskSecret(value string) string {
	if len(value) < 5 {
		return "***"
	}
	return "***" + value[:5]
}

func GetSettingsHandler(w http.ResponseWriter, r *http.Request) {

	// Retrieve all in config.GetEnv & return as JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SettingsEnv{
		BASE_URL:                               config.GetEnv("BASE_URL"),
		EXPO_APP_ID:                            config.GetEnv("EXPO_APP_ID"),
		EXPO_ACCESS_TOKEN:                      maskSecret(config.GetEnv("EXPO_ACCESS_TOKEN")),
		CACHE_MODE:                             config.GetEnv("CACHE_MODE"),
		REDIS_HOST:                             config.GetEnv("REDIS_HOST"),
		REDIS_PORT:                             config.GetEnv("REDIS_PORT"),
		STORAGE_MODE:                           config.GetEnv("STORAGE_MODE"),
		S3_BUCKET_NAME:                         config.GetEnv("S3_BUCKET_NAME"),
		LOCAL_BUCKET_BASE_PATH:                 config.GetEnv("LOCAL_BUCKET_BASE_PATH"),
		KEYS_STORAGE_TYPE:                      config.GetEnv("KEYS_STORAGE_TYPE"),
		AWSSM_EXPO_PUBLIC_KEY_SECRET_ID:        config.GetEnv("AWSSM_EXPO_PUBLIC_KEY_SECRET_ID"),
		AWSSM_EXPO_PRIVATE_KEY_SECRET_ID:       config.GetEnv("AWSSM_EXPO_PRIVATE_KEY_SECRET_ID"),
		PUBLIC_EXPO_KEY_B64:                    config.GetEnv("PUBLIC_EXPO_KEY_B64"),
		PUBLIC_LOCAL_EXPO_KEY_PATH:             config.GetEnv("PUBLIC_LOCAL_EXPO_KEY_PATH"),
		PRIVATE_LOCAL_EXPO_KEY_PATH:            config.GetEnv("PRIVATE_LOCAL_EXPO_KEY_PATH"),
		AWS_REGION:                             config.GetEnv("AWS_REGION"),
		AWS_BASE_ENDPOINT:                      config.GetEnv("AWS_BASE_ENDPOINT"),
		AWS_ACCESS_KEY_ID:                      maskSecret(config.GetEnv("AWS_ACCESS_KEY_ID")),
		CLOUDFRONT_DOMAIN:                      config.GetEnv("CLOUDFRONT_DOMAIN"),
		CLOUDFRONT_KEY_PAIR_ID:                 maskSecret(config.GetEnv("CLOUDFRONT_KEY_PAIR_ID")),
		CLOUDFRONT_PRIVATE_KEY_B64:             maskSecret(config.GetEnv("CLOUDFRONT_PRIVATE_KEY_B64")),
		AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID: config.GetEnv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID"),
		PRIVATE_LOCAL_CLOUDFRONT_KEY_PATH:      config.GetEnv("PRIVATE_LOCAL_CLOUDFRONT_KEY_PATH"),
		PROMETHEUS_ENABLED:                     config.GetEnv("PROMETHEUS_ENABLED"),
	})
}

func GetChannelsHandler(w http.ResponseWriter, r *http.Request) {
	cacheKey := dashboard.ComputeGetChannelsCacheKey()
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		var channels []ChannelMapping
		json.Unmarshal([]byte(cacheValue), &channels)
		json.NewEncoder(w).Encode(channels)
		return
	}
	allChannels, err := services.FetchExpoChannels()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	branchesMapping, err := services.FetchExpoBranchesMapping()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var channels []ChannelMapping
	for _, channel := range allChannels {
		var branchName *string
		var branchId *string
		for _, mapping := range branchesMapping {
			if mapping.ChannelName != nil && *mapping.ChannelName == channel.Name {
				branchName = &mapping.BranchName
				branchId = &mapping.BranchId
				break
			}
		}
		channels = append(channels, ChannelMapping{
			ReleaseChannelId:   channel.Id,
			ReleaseChannelName: channel.Name,
			BranchName:         branchName,
			BranchId:           branchId,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(channels)
	ttl := 10 * time.Second
	ttlMs := int(ttl.Milliseconds())
	marshaledResponse, _ := json.Marshal(channels)
	cache.Set(cacheKey, string(marshaledResponse), &ttlMs)
}

func GetBranchesHandler(w http.ResponseWriter, r *http.Request) {
	resolvedBucket := bucket.GetBucket()
	branches, err := resolvedBucket.GetBranches()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	branchesMapping, err := services.FetchExpoBranchesMapping()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var response []BranchMapping
	for _, branch := range branches {
		var releaseChannel *string
		var branchId *string
		for _, mapping := range branchesMapping {
			if mapping.BranchName == branch {
				releaseChannel = mapping.ChannelName
				branchId = &mapping.BranchId
				break
			}
		}
		response = append(response, BranchMapping{
			BranchName:     branch,
			BranchId:       branchId,
			ReleaseChannel: releaseChannel,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func GetRuntimeVersionsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	branchName := vars["BRANCH"]
	cacheKey := dashboard.ComputeGetRuntimeVersionsCacheKey(branchName)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		var runtimeVersions []bucket.RuntimeVersionWithStats
		json.Unmarshal([]byte(cacheValue), &runtimeVersions)
		json.NewEncoder(w).Encode(runtimeVersions)
		return
	}
	resolvedBucket := bucket.GetBucket()
	runtimeVersions, err := resolvedBucket.GetRuntimeVersions(branchName)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	sort.Slice(runtimeVersions, func(i, j int) bool {
		timeI, _ := time.Parse(time.RFC3339, runtimeVersions[i].CreatedAt)
		timeJ, _ := time.Parse(time.RFC3339, runtimeVersions[j].CreatedAt)
		return timeI.After(timeJ)
	})
	json.NewEncoder(w).Encode(runtimeVersions)
	marshaledResponse, _ := json.Marshal(runtimeVersions)
	ttl := 10 * time.Second
	ttlMs := int(ttl.Milliseconds())
	cache.Set(cacheKey, string(marshaledResponse), &ttlMs)
}

func GetUpdateDetails(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	updateId := vars["UPDATE_ID"]
	cacheKey := dashboard.ComputeGetUpdateDetailsCacheKey(branchName, runtimeVersion, updateId)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		var updateDetailsResponse UpdateDetails
		json.Unmarshal([]byte(cacheValue), &updateDetailsResponse)
		json.NewEncoder(w).Encode(updateDetailsResponse)
		return
	}
	update, err := update2.GetUpdate(branchName, runtimeVersion, updateId)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	metadata, err := update2.GetMetadata(*update)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	numberUpdate, _ := strconv.ParseInt(update.UpdateId, 10, 64)
	storedMetadata, _ := update2.RetrieveUpdateStoredMetadata(*update)
	expoConfig, err := update2.GetExpoConfig(*update)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	updateUUID := storedMetadata.UpdateUUID
	if updateUUID == "" {
		updateUUID = crypto.ConvertSHA256HashToUUID(metadata.ID)
	}
	updatesResponse := UpdateDetails{
		UpdateUUID: updateUUID,
		UpdateId:   update.UpdateId,
		CreatedAt:  time.UnixMilli(numberUpdate).UTC().Format(time.RFC3339),
		CommitHash: storedMetadata.CommitHash,
		Platform:   storedMetadata.Platform,
		Message:    storedMetadata.Message,
		Type:       update2.GetUpdateType(*update),
		ExpoConfig: string(expoConfig),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(updatesResponse)
	marshaledResponse, _ := json.Marshal(updatesResponse)
	ttl := 120 * time.Second
	ttlMs := int(ttl.Milliseconds())
	cache.Set(cacheKey, string(marshaledResponse), &ttlMs)
}

// Building a row costs up to four storage reads (.check, update-metadata.json,
// rollback, metadata.json). Runtimes with hundreds of updates took 30-90s
// serially, so rows are built concurrently and cached individually; the full
// list is cached too (publish invalidates it via MarkUpdateAsChecked).
const (
	dashboardUpdateItemWorkers         = 16
	dashboardUpdateItemTTLSeconds      = 7 * 24 * 60 * 60
	dashboardUpdatesResponseTTLSeconds = 60 * 60
	dashboardUpdatesMaxLimit           = 1000
)

func buildUpdateItem(update types.Update, cache cache2.Cache) (UpdateItem, bool) {
	itemKey := dashboard.ComputeUpdateItemCacheKey(update.Branch, update.RuntimeVersion, update.UpdateId)
	if cached := cache.Get(itemKey); cached != "" {
		var item UpdateItem
		if err := json.Unmarshal([]byte(cached), &item); err == nil {
			return item, true
		}
	}
	if !update2.IsUpdateValid(update) {
		return UpdateItem{}, false
	}
	numberUpdate, _ := strconv.ParseInt(update.UpdateId, 10, 64)
	storedMetadata, _ := update2.RetrieveUpdateStoredMetadata(update)
	if storedMetadata == nil {
		storedMetadata = &types.UpdateStoredMetadata{}
	}
	item := UpdateItem{
		UpdateId:   update.UpdateId,
		CreatedAt:  time.UnixMilli(numberUpdate).UTC().Format(time.RFC3339),
		CommitHash: storedMetadata.CommitHash,
		Platform:   storedMetadata.Platform,
		Message:    storedMetadata.Message,
	}
	if update2.GetUpdateType(update) == types.Rollback {
		item.UpdateUUID = "Rollback to embedded"
	} else {
		metadata, err := update2.GetMetadata(update)
		if err != nil {
			return UpdateItem{}, false
		}
		item.UpdateUUID = storedMetadata.UpdateUUID
		if item.UpdateUUID == "" {
			item.UpdateUUID = crypto.ConvertSHA256HashToUUID(metadata.ID)
		}
	}
	if encoded, err := json.Marshal(item); err == nil {
		ttl := dashboardUpdateItemTTLSeconds
		_ = cache.Set(itemKey, string(encoded), &ttl)
	}
	return item, true
}

func buildUpdateItems(updates []types.Update, cache cache2.Cache) []UpdateItem {
	built := make([]*UpdateItem, len(updates))
	sem := make(chan struct{}, dashboardUpdateItemWorkers)
	var wg sync.WaitGroup
	for i, update := range updates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, update types.Update) {
			defer wg.Done()
			defer func() { <-sem }()
			if item, ok := buildUpdateItem(update, cache); ok {
				built[i] = &item
			}
		}(i, update)
	}
	wg.Wait()
	items := make([]UpdateItem, 0, len(updates))
	for _, item := range built {
		if item != nil {
			items = append(items, *item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		timeI, _ := time.Parse(time.RFC3339, items[i].CreatedAt)
		timeJ, _ := time.Parse(time.RFC3339, items[j].CreatedAt)
		return timeI.After(timeJ)
	})
	return items
}

// Optional ?limit=&offset= (newest first). Absent or invalid → the full list,
// which keeps existing clients unchanged. X-Total-Count always carries the
// unpaginated size so a client can page without a second request.
func parseUpdatesPage(r *http.Request) (limit int, offset int) {
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
			if limit > dashboardUpdatesMaxLimit {
				limit = dashboardUpdatesMaxLimit
			}
		}
	}
	if raw := query.Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return limit, offset
}

func pageUpdates(items []UpdateItem, limit int, offset int) []UpdateItem {
	if offset >= len(items) {
		return []UpdateItem{}
	}
	end := len(items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return items[offset:end]
}

func GetUpdatesHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	limit, offset := parseUpdatesPage(r)
	cacheKey := dashboard.ComputeGetUpdatesCacheKey(branchName, runtimeVersion)
	cache := cache2.GetCache()

	var updatesResponse []UpdateItem
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		json.Unmarshal([]byte(cacheValue), &updatesResponse)
	} else {
		resolvedBucket := bucket.GetBucket()
		updates, err := resolvedBucket.GetUpdates(branchName, runtimeVersion)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		updatesResponse = buildUpdateItems(updates, cache)
		if marshaledResponse, err := json.Marshal(updatesResponse); err == nil {
			ttl := dashboardUpdatesResponseTTLSeconds
			cache.Set(cacheKey, string(marshaledResponse), &ttl)
		}
	}
	if updatesResponse == nil {
		updatesResponse = []UpdateItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(updatesResponse)))
	w.WriteHeader(http.StatusOK)
	if limit > 0 || offset > 0 {
		json.NewEncoder(w).Encode(pageUpdates(updatesResponse, limit, offset))
		return
	}
	json.NewEncoder(w).Encode(updatesResponse)
}

func UpdateChannelBranchMappingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	branchId := vars["BRANCH"]
	var requestBody struct {
		ReleaseChannel string `json:"releaseChannel"`
	}
	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		fmt.Println("Error decoding request body:", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Error decoding request body"))
		return
	}
	releaseChannel := requestBody.ReleaseChannel
	if releaseChannel == "" {
		fmt.Println("Release channel is empty")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Release channel is empty"))
		return
	}
	err = services.UpdateChannelBranchMapping(releaseChannel, branchId)
	if err != nil {
		fmt.Println("Error updating channel branch mapping:", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error updating channel branch mapping"))
		return
	}
	w.WriteHeader(http.StatusOK)
	marshaledResponse, _ := json.Marshal("ok")
	w.Header().Set("Content-Type", "application/json")
	w.Write(marshaledResponse)

	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey()
	channelsCacheKey := dashboard.ComputeGetChannelsCacheKey()
	cache := cache2.GetCache()
	cache.Delete(branchesCacheKey)
	cache.Delete(channelsCacheKey)
	channelMappingCacheKey := services.ComputeChannelMappingCacheKey(releaseChannel)
	cache.Delete(channelMappingCacheKey)
}
