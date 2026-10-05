package update

import (
	"log"
	"sync"
)

// Track asynchronous work so local integration tests can finish storage work
// before tearing down their bucket fixtures.
var preWarmTasks sync.WaitGroup

func queueManifestPreWarm(branch, runtime, platform string) {
	preWarmTasks.Add(1)
	go func() { defer preWarmTasks.Done(); PreWarmManifestCache(branch, runtime, platform) }()
}

// PreWarmManifestCache populates the manifest cache layers for the given
// branch/runtimeVersion/platform combination. It is intended to be called
// as a goroutine after MarkUpdateAsChecked so the first client request
// hits warm caches instead of rebuilding everything from scratch.
func PreWarmManifestCache(branch, runtimeVersion, platform string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PreWarm] panic recovered for branch=%s rv=%s platform=%s: %v", branch, runtimeVersion, platform, r)
		}
	}()

	latestUpdate, err := GetLatestUpdateBundlePathForRuntimeVersion(branch, runtimeVersion, platform)
	if err != nil {
		log.Printf("[PreWarm] error getting latest update for branch=%s rv=%s platform=%s: %v", branch, runtimeVersion, platform, err)
		return
	}
	if latestUpdate == nil {
		return
	}

	metadata, err := GetMetadata(*latestUpdate)
	if err != nil {
		log.Printf("[PreWarm] error getting metadata for update=%s: %v", latestUpdate.UpdateId, err)
		return
	}

	_, err = ComposeUpdateManifest(&metadata, *latestUpdate, platform)
	if err != nil {
		log.Printf("[PreWarm] error composing manifest for update=%s platform=%s: %v", latestUpdate.UpdateId, platform, err)
		return
	}

	log.Printf("[PreWarm] successfully pre-warmed cache for branch=%s rv=%s platform=%s", branch, runtimeVersion, platform)
}
