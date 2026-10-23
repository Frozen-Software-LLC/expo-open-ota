package update

import (
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/services"
	"log"
	"sort"
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
// closes when the warm-up ends or the deadline passes, whichever is first.
func StartBootWarmUp(deadline time.Duration) <-chan struct{} {
	ready := make(chan struct{})
	var once sync.Once
	markReady := func() { once.Do(func() { close(ready) }) }
	time.AfterFunc(deadline, markReady)
	go func() {
		defer markReady()
		defer recoverWarmUpPanic()
		start := time.Now()
		keys := activeManifestKeys()
		var workers errgroup.Group
		workers.SetLimit(bootWarmUpWorkers)
		for key := range keys {
			workers.Go(func() error {
				PreWarmManifestCache(key.branch, key.runtimeVersion, key.platform)
				return nil
			})
		}
		_ = workers.Wait()
		log.Printf("[BootWarmUp] warmed %d manifest keys in %v", len(keys), time.Since(start))
	}()
	return ready
}

func activeManifestKeys() map[manifestKey]struct{} {
	channels, err := services.FetchExpoChannels()
	if err != nil {
		log.Printf("[BootWarmUp] error listing channels: %v", err)
		return nil
	}
	var (
		mu      sync.Mutex
		keys    = map[manifestKey]struct{}{}
		workers errgroup.Group
	)
	workers.SetLimit(bootWarmUpWorkers)
	for _, channel := range channels {
		workers.Go(func() error {
			defer recoverWarmUpPanic()
			// Resolves the channel the way /manifest does, and caches the mapping.
			mapping, err := services.FetchExpoChannelMapping(channel.Name)
			if err != nil || mapping == nil {
				return nil
			}
			runtimeVersions, err := newestRuntimeVersions(mapping.BranchName)
			if err != nil {
				log.Printf("[BootWarmUp] error listing runtime versions for branch=%s: %v", mapping.BranchName, err)
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			for _, runtimeVersion := range runtimeVersions {
				keys[manifestKey{mapping.BranchName, runtimeVersion, "ios"}] = struct{}{}
				keys[manifestKey{mapping.BranchName, runtimeVersion, "android"}] = struct{}{}
			}
			return nil
		})
	}
	_ = workers.Wait()
	return keys
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

// errgroup does not recover panics, and a panic in any goroutine ends the process.
func recoverWarmUpPanic() {
	if r := recover(); r != nil {
		log.Printf("[BootWarmUp] panic recovered: %v", r)
	}
}
