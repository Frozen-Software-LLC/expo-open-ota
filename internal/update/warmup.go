package update

import (
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/services"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// Devices on older store builds keep asking for older runtime versions;
	// the most recently updated ones are the builds still in use.
	bootWarmUpRuntimesPerBranch = 3
	// Each manifest build downloads every asset of its update.
	bootWarmUpWorkers = 4
)

type manifestKey struct {
	branch, runtimeVersion, platform string
}

// StartBootWarmUp fills the caches that the first device requests after a
// restart need: each channel's mapping, and the manifests of the newest
// runtime versions of every branch a channel maps to. The returned channel
// closes when the user branches are warm or the deadline passes, whichever is
// first. Preview branches are warmed after that, in the background.
func StartBootWarmUp(deadline time.Duration) <-chan struct{} {
	ready := make(chan struct{})
	var once sync.Once
	markReady := func() { once.Do(func() { close(ready) }) }
	time.AfterFunc(deadline, markReady)
	go func() {
		defer markReady()
		defer recoverWarmUpPanic()
		start := time.Now()
		branches, err := services.WarmChannelMappings()
		if err != nil {
			log.Printf("[BootWarmUp] error fetching channel mappings: %v", err)
			return
		}
		users, previews := splitPreviewBranches(branches)
		userKeys := manifestKeys(users)
		warmManifests(userKeys)
		log.Printf("[BootWarmUp] warmed %d user manifest keys in %v", len(userKeys), time.Since(start))
		markReady()
		previewKeys := manifestKeys(previews)
		warmManifests(previewKeys)
		log.Printf("[BootWarmUp] warmed %d preview manifest keys in %v", len(previewKeys), time.Since(start))
	}()
	return ready
}

// Preview branches serve PR builds to the team, not to users.
const previewBranchPrefix = "preview-pr-"

// splitPreviewBranches lists each branch once; several channels can serve one branch.
func splitPreviewBranches(branches []string) (users, previews []string) {
	slices.Sort(branches)
	for _, branch := range slices.Compact(branches) {
		if strings.HasPrefix(branch, previewBranchPrefix) {
			previews = append(previews, branch)
		} else {
			users = append(users, branch)
		}
	}
	return users, previews
}

func manifestKeys(branches []string) []manifestKey {
	var keys []manifestKey
	for _, branch := range branches {
		runtimeVersions, err := newestRuntimeVersions(branch)
		if err != nil {
			log.Printf("[BootWarmUp] error listing runtime versions for branch=%s: %v", branch, err)
			continue
		}
		for _, runtimeVersion := range runtimeVersions {
			keys = append(keys, manifestKey{branch, runtimeVersion, "ios"}, manifestKey{branch, runtimeVersion, "android"})
		}
	}
	return keys
}

func warmManifests(keys []manifestKey) {
	var workers errgroup.Group
	workers.SetLimit(bootWarmUpWorkers)
	for _, key := range keys {
		workers.Go(func() error {
			PreWarmManifestCache(key.branch, key.runtimeVersion, key.platform)
			return nil
		})
	}
	_ = workers.Wait()
}

func newestRuntimeVersions(branch string) ([]string, error) {
	runtimeVersions, err := bucket.GetBucket().GetRuntimeVersions(branch)
	if err != nil {
		return nil, err
	}
	// Every bucket writes LastUpdatedAt as UTC RFC 3339, which sorts as text.
	sort.Slice(runtimeVersions, func(i, j int) bool {
		return runtimeVersions[i].LastUpdatedAt > runtimeVersions[j].LastUpdatedAt
	})
	newest := make([]string, 0, bootWarmUpRuntimesPerBranch)
	for _, runtimeVersion := range runtimeVersions[:min(len(runtimeVersions), bootWarmUpRuntimesPerBranch)] {
		newest = append(newest, runtimeVersion.RuntimeVersion)
	}
	return newest, nil
}

// A panic in a goroutine ends the process.
func recoverWarmUpPanic() {
	if r := recover(); r != nil {
		log.Printf("[BootWarmUp] panic recovered: %v", r)
	}
}
