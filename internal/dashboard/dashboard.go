package dashboard

import (
	"expo-open-ota/config"
	"expo-open-ota/internal/version"
	"fmt"
)

func IsDashboardEnabled() bool {
	return config.GetEnv("USE_DASHBOARD") == "true"
}

func ComputeGetRuntimeVersionsCacheKey(branch string) string {
	return fmt.Sprintf("dashboard:%s:request:getRuntimeVersions:%s", version.Version, branch)
}

func ComputeGetBranchesCacheKey() string {
	return fmt.Sprintf("dashboard:%s:request:getBranches", version.Version)
}

func ComputeGetChannelsCacheKey() string {
	return fmt.Sprintf("dashboard:%s:request:getChannels", version.Version)
}

func ComputeGetUpdatesCacheKey(branch string, runtimeVersion string) string {
	return fmt.Sprintf("dashboard:%s:request:getUpdates:%s:%s", version.Version, branch, runtimeVersion)
}

func ComputeGetUpdateDetailsCacheKey(branch string, runtimeVersion string, updateID string) string {
	return fmt.Sprintf("dashboard:%s:request:getUpdateDetails:%s:%s:%s", version.Version, branch, runtimeVersion, updateID)
}

// One dashboard row per update. Immutable once the update is valid (its .check
// marker is written after the stored metadata), so it can live for a long time
// and turn the per-update storage reads into a single cache hit.
func ComputeUpdateItemCacheKey(branch string, runtimeVersion string, updateID string) string {
	return fmt.Sprintf("dashboard:%s:item:%s:%s:%s", version.Version, branch, runtimeVersion, updateID)
}
