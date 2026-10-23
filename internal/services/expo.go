package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/version"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type ExpoUserAccount struct {
	Id       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type ExpoChannelMapping struct {
	Id         string `json:"id"`
	BranchName string `json:"branchName"`
}

type ExpoBranchMapping struct {
	BranchName  string  `json:"branchName"`
	BranchId    string  `json:"branchId"`
	ChannelName *string `json:"channelName"`
}

type ExpoChannel struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	BranchId string `json:"branchId"`
}

type BranchMapping struct {
	Version int `json:"version"`
	Data    []struct {
		BranchId           string          `json:"branchId"`
		BranchMappingLogic json.RawMessage `json:"branchMappingLogic"`
	} `json:"data"`
}

func ValidateExpoAuth(expoAuth types.ExpoAuth) (*ExpoUserAccount, error) {
	if expoAuth.Token == nil && expoAuth.SessionSecret == nil {
		return nil, errors.New("no valid Expo auth provided")
	}
	expoAccount, err := FetchExpoUserAccountInformations(expoAuth)
	if err != nil {
		return nil, err
	}
	if expoAccount == nil {
		return nil, errors.New("no expo account found")
	}
	selfExpoUsername := FetchSelfExpoUsername()
	if selfExpoUsername != expoAccount.Username {
		return nil, errors.New("expo account does not match self expo username")
	}
	return expoAccount, nil
}

func GetExpoAccessToken() string {
	return config.GetEnv("EXPO_ACCESS_TOKEN")
}

func GetExpoAppId() string {
	return config.GetEnv("EXPO_APP_ID")
}

func SetAuthHeaders(expoAuth types.ExpoAuth, req *http.Request) {
	if expoAuth.Token != nil {
		req.Header.Set("Authorization", "Bearer "+*expoAuth.Token)
	}
	if expoAuth.SessionSecret != nil {
		req.Header.Set("expo-session", *expoAuth.SessionSecret)
	}
}

func makeGraphQLRequest(ctx context.Context, query string, variables map[string]interface{}, expoAuth types.ExpoAuth, result interface{}, headers map[string]string) error {
	start := time.Now()
	log.Printf("[TRACE] makeGraphQLRequest - START at %s", start.Format(time.RFC3339Nano))
	defer func() {
		log.Printf("[TRACE] makeGraphQLRequest - END duration=%v", time.Since(start))
	}()

	requestBody := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.expo.dev/graphql", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	SetAuthHeaders(expoAuth, req)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	log.Printf("[TRACE] makeGraphQLRequest - Sending HTTP request at %s", time.Now().Format(time.RFC3339Nano))
	httpStart := time.Now()
	client := &http.Client{}
	resp, err := client.Do(req)
	log.Printf("[TRACE] makeGraphQLRequest - HTTP request completed in %v", time.Since(httpStart))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read error message in response body
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.New("GraphQL request failed with status: " + resp.Status + " and unable to read response body")
		}
		return errors.New("GraphQL request failed with status: " + resp.Status + " message: " + string(responseBody))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

func FetchExpoChannels() ([]ExpoChannel, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateChannels(offset: 0, limit: 10000) {
						id
						name
					}
				}
			}
		}
	`
	appId := GetExpoAppId()
	expoToken := GetExpoAccessToken()
	variables := map[string]interface{}{
		"appId": appId,
	}
	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateChannels []ExpoChannel `json:"updateChannels"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoChannels"
	}
	ctx := context.Background()
	if err := makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}
	return resp.Data.App.ById.UpdateChannels, nil
}

func UpdateChannelBranchMapping(channelName, branchId string) error {
	fmt.Println("Updating channel branch mapping for channel:", channelName, "to branch:", branchId)
	query := `
		mutation UpdateChannelBranchMapping($channelId: ID!, $branchMapping: String!) {
			updateChannel {
				editUpdateChannel(channelId: $channelId, branchMapping: $branchMapping) {
					id
				}
			}
		}
	`
	branchMapping := BranchMapping{
		Version: 0,
		Data: []struct {
			BranchId           string          `json:"branchId"`
			BranchMappingLogic json.RawMessage `json:"branchMappingLogic"`
		}{
			{
				BranchId:           branchId,
				BranchMappingLogic: json.RawMessage(`"true"`),
			},
		},
	}

	branchMappingBytes, err := json.Marshal(branchMapping)
	if err != nil {
		return err
	}

	variables := map[string]interface{}{
		"channelId":     channelName,
		"branchMapping": string(branchMappingBytes),
	}

	token := GetExpoAccessToken()
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "UpdateChannelBranchMapping"
	}
	ctx := context.Background()
	resp := struct{}{}
	return makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{
		Token: &token,
	}, &resp, headers)
}

func FetchExpoBranches() ([]string, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
				}
			}
		}
	`
	appId := GetExpoAppId()
	expoToken := GetExpoAccessToken()
	variables := map[string]interface{}{
		"appId": appId,
	}
	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoBranches"
	}
	ctx := context.Background()
	if err := makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}
	var branches []string
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		branches = append(branches, branch.Name)
	}
	return branches, nil
}

func FetchExpoUserAccountInformations(expoAuth types.ExpoAuth) (*ExpoUserAccount, error) {
	query := `
		query GetCurrentUserAccount {
			me {
				id
				username
				email
			}
		}
	`

	var resp struct {
		Data struct {
			Me ExpoUserAccount `json:"me"`
		} `json:"data"`
	}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoUserAccountInformations"
	}

	ctx := context.Background()
	if err := makeGraphQLRequest(ctx, query, nil, expoAuth, &resp, headers); err != nil {
		return nil, err
	}

	return &resp.Data.Me, nil
}

func FetchSelfExpoUsername() string {
	token := GetExpoAccessToken()
	expoAccount, err := FetchExpoUserAccountInformations(types.ExpoAuth{
		Token: &token,
	})
	if err != nil {
		return ""
	}
	return expoAccount.Username
}

func ComputeChannelMappingCacheKey(channelName string) string {
	return fmt.Sprintf("channelMapping-v2:%s:%s", version.Version, channelName)
}

// cachedChannelMapping is the cache entry for one channel. A nil Mapping
// records that Expo had no mapping for the channel.
type cachedChannelMapping struct {
	Mapping   *ExpoChannelMapping `json:"mapping"`
	FetchedAt time.Time           `json:"fetchedAt"`
}

const (
	channelMappingFreshFor = time.Minute
	// A failed refresh keeps the last mapping in use, at most this long.
	channelMappingMaxAge = time.Hour
	// A channel created a moment ago must not get the expired-preview rollback
	// for long, so "no mapping" is never served older than this.
	noChannelMappingMaxAge = 10 * time.Second
)

var (
	channelMappingTimeout = 5 * time.Second
	channelMappingFlight  singleflight.Group
	// Incremented by every remap. ponytail: process-local, so with a shared
	// cache a fetch on another replica can still re-cache the old mapping.
	channelMappingGeneration atomic.Uint64
	// Held for writing by a remap, for reading by a fetch's check and cache write.
	channelMappingCacheMu sync.RWMutex
)

func FetchExpoChannelMapping(channelName string) (*ExpoChannelMapping, error) {
	start := time.Now()
	log.Printf("[TRACE] FetchExpoChannelMapping - START at %s for channel=%s", start.Format(time.RFC3339Nano), channelName)
	defer func() {
		log.Printf("[TRACE] FetchExpoChannelMapping - END duration=%v", time.Since(start))
	}()

	cacheKey := ComputeChannelMappingCacheKey(channelName)
	if cachedValue := cache2.GetCache().Get(cacheKey); cachedValue != "" {
		var cached cachedChannelMapping
		if err := json.Unmarshal([]byte(cachedValue), &cached); err != nil {
			log.Printf("[ChannelMapping] cache unmarshal error for key=%s: %v", cacheKey, err)
		} else if cached.Mapping != nil {
			if time.Since(cached.FetchedAt) >= channelMappingFreshFor {
				// Answer from the cache now; the fetch refreshes it in the background.
				fetchChannelMapping(channelName)
			}
			return cached.Mapping, nil
		} else if time.Since(cached.FetchedAt) < noChannelMappingMaxAge {
			return nil, nil
		}
	}
	result := <-fetchChannelMapping(channelName)
	if result.Err != nil {
		return nil, result.Err
	}
	return result.Val.(*ExpoChannelMapping), nil
}

// fetchChannelMapping asks Expo once per channel, however many callers wait,
// and caches the answer unless a remap happened since the fetch started.
func fetchChannelMapping(channelName string) <-chan singleflight.Result {
	cacheKey := ComputeChannelMappingCacheKey(channelName)
	generation := channelMappingGeneration.Load()
	return channelMappingFlight.DoChan(fmt.Sprintf("%s:%d", cacheKey, generation), func() (interface{}, error) {
		mapping, err := queryExpoChannelMapping(channelName)
		if err != nil {
			log.Printf("[ChannelMapping] fetch failed for channel=%s: %v", channelName, err)
			return nil, err
		}
		maxAge := channelMappingMaxAge
		if mapping == nil {
			maxAge = noChannelMappingMaxAge
		}
		ttl := int(maxAge / time.Second)
		cacheValue, _ := json.Marshal(cachedChannelMapping{Mapping: mapping, FetchedAt: time.Now()})
		channelMappingCacheMu.RLock()
		defer channelMappingCacheMu.RUnlock()
		if channelMappingGeneration.Load() == generation {
			_ = cache2.GetCache().Set(cacheKey, string(cacheValue), &ttl)
		}
		return mapping, nil
	})
}

// InvalidateChannelMapping drops the cached mapping after a remap, and stops
// any fetch that started before it from caching the old mapping.
func InvalidateChannelMapping(channelName string) {
	channelMappingCacheMu.Lock()
	defer channelMappingCacheMu.Unlock()
	channelMappingGeneration.Add(1)
	cache2.GetCache().Delete(ComputeChannelMappingCacheKey(channelName))
}

func queryExpoChannelMapping(channelName string) (*ExpoChannelMapping, error) {
	query := `
		query FetchAppChannel($appId: String!, $channelName: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
					updateChannelByName(name: $channelName) {
						id
						name
						branchMapping
					}
				}
			}
		}
	`

	appId := GetExpoAppId()
	expoToken := GetExpoAccessToken()
	variables := map[string]interface{}{
		"appId":       appId,
		"channelName": channelName,
	}

	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
					UpdateChannelByName struct {
						ID            string `json:"id"`
						BranchMapping string `json:"branchMapping"`
					} `json:"updateChannelByName"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoChannelMapping"
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelMappingTimeout)
	defer cancel()

	log.Printf("[TRACE] FetchExpoChannelMapping - Calling makeGraphQLRequest at %s", time.Now().Format(time.RFC3339Nano))
	gqlStart := time.Now()
	if err := makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{Token: &expoToken}, &resp, headers); err != nil {
		log.Printf("[TRACE] FetchExpoChannelMapping - GraphQL request failed in %v: %v", time.Since(gqlStart), err)
		return nil, err
	}
	log.Printf("[TRACE] FetchExpoChannelMapping - GraphQL request completed in %v", time.Since(gqlStart))

	log.Printf("[TRACE] FetchExpoChannelMapping - Parsing branch mapping at %s", time.Now().Format(time.RFC3339Nano))
	channel := resp.Data.App.ById.UpdateChannelByName
	// Expo returns an empty object when a channel was deleted. Treat that as a
	// missing mapping instead of attempting to unmarshal an empty string and
	// turning an expired preview into a manifest 500.
	if channel.ID == "" || channel.BranchMapping == "" {
		return nil, nil
	}
	var branchMapping BranchMapping
	if err := json.Unmarshal([]byte(channel.BranchMapping), &branchMapping); err != nil {
		return nil, err
	}

	var branchID string
	for _, mapping := range branchMapping.Data {
		var logic string
		if json.Unmarshal(mapping.BranchMappingLogic, &logic) == nil && logic == "true" {
			branchID = mapping.BranchId
			break
		}
	}
	if branchID == "" {
		return nil, nil
	}

	var branchName string
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		if branch.ID == branchID {
			branchName = branch.Name
			break
		}
	}
	if branchName == "" {
		return nil, nil
	}

	log.Printf("[TRACE] FetchExpoChannelMapping - Found branch mapping: branch=%s", branchName)
	return &ExpoChannelMapping{
		Id:         resp.Data.App.ById.UpdateChannelByName.ID,
		BranchName: branchName,
	}, nil
}

func FetchExpoBranchesMapping() ([]ExpoBranchMapping, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
					updateChannels(offset: 0, limit: 10000) {
						id
						name
						branchMapping
					}
				}
			}
		}
	`

	appId := GetExpoAppId()
	expoToken := GetExpoAccessToken()
	variables := map[string]interface{}{"appId": appId}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoBranches"
	}

	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
					UpdateChannels []struct {
						ID            string `json:"id"`
						Name          string `json:"name"`
						BranchMapping string `json:"branchMapping"`
					} `json:"updateChannels"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}

	ctx := context.Background()
	if err := makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}

	branchIDToChannels := make(map[string][]string)
	for _, channel := range resp.Data.App.ById.UpdateChannels {
		var mapping BranchMapping
		if err := json.Unmarshal([]byte(channel.BranchMapping), &mapping); err != nil {
			return nil, err
		}

		for _, m := range mapping.Data {
			var logic string
			if json.Unmarshal(m.BranchMappingLogic, &logic) == nil && logic == "true" {
				branchIDToChannels[m.BranchId] = append(branchIDToChannels[m.BranchId], channel.Name)
			}
		}
	}

	var branchMappings []ExpoBranchMapping
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		channelNames, found := branchIDToChannels[branch.ID]
		if !found || len(channelNames) == 0 {
			branchMappings = append(branchMappings, ExpoBranchMapping{
				BranchName:  branch.Name,
				BranchId:    branch.ID,
				ChannelName: nil,
			})
			continue
		}

		for _, channelName := range channelNames {
			cn := channelName
			branchMappings = append(branchMappings, ExpoBranchMapping{
				BranchName:  branch.Name,
				BranchId:    branch.ID,
				ChannelName: &cn,
			})
		}
	}

	return branchMappings, nil
}

func CreateBranch(branch string) error {
	query := `
		mutation CreateUpdateBranchForAppMutation($appId: ID!, $name: String!) {
		  updateBranch {
			createUpdateBranchForApp(appId: $appId, name: $name) {
			  id
			}
		  }
		}
	`
	appId := GetExpoAppId()
	variables := map[string]interface{}{
		"appId": appId,
		"name":  branch,
	}
	token := GetExpoAccessToken()
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "CreateBranch"
	}
	ctx := context.Background()
	resp := struct{}{}
	return makeGraphQLRequest(ctx, query, variables, types.ExpoAuth{
		Token: &token,
	}, &resp, headers)
}

type ExpoApp struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}
