package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"expo-open-ota/config"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/bundlepatch"
	"expo-open-ota/internal/types"
	"github.com/google/uuid"
)

const patchBaseLimit = 3
const patchJobTimeout = 2 * time.Minute

// Patches are an optional optimization. Bound admission and run CPU-heavy work
// in a killable child, never in a manifest/download or finalization request.
// The queue is intentionally best-effort: a restart/full queue falls back to
// full downloads; retrying finalization or the helper command rebuilds patches.
type patchQueue struct {
	mu      sync.Mutex
	pending map[types.Update]bool
	jobs    chan types.Update
	run     func(types.Update)
}

func newPatchQueue(run func(types.Update), capacity int) *patchQueue {
	q := &patchQueue{pending: make(map[types.Update]bool), jobs: make(chan types.Update, capacity), run: run}
	go func() {
		for target := range q.jobs {
			q.run(target)
			q.mu.Lock()
			delete(q.pending, target)
			q.mu.Unlock()
		}
	}()
	return q
}

func (q *patchQueue) enqueue(target types.Update) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending[target] {
		return true
	}
	select {
	case q.jobs <- target:
		q.pending[target] = true
		return true
	default:
		return false
	}
}

var publicationPatches struct {
	sync.Once
	queue *patchQueue
}

func QueueBundlePatches(target types.Update) {
	if config.GetEnv("OTA_BUNDLE_DIFFING_ENABLED") != "true" {
		return
	}
	publicationPatches.Do(func() { publicationPatches.queue = newPatchQueue(runPatchJob, 16) })
	if !publicationPatches.queue.enqueue(target) {
		log.Printf("[bundle-diff] queue full; full bundle remains available for %s/%s/%s", target.Branch, target.RuntimeVersion, target.UpdateId)
	}
}

func runPatchJob(target types.Update) {
	executable, err := os.Executable()
	if err != nil {
		log.Printf("[bundle-diff] executable: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), patchJobTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(filepath.Dir(executable), "prepare-update-patches"), "--branch", target.Branch, "--runtime", target.RuntimeVersion, "--update-id", target.UpdateId)
	// A soft GC target, not an OS memory limit. Container memory limits must leave
	// headroom for the server and one worker. Inputs are bounded independently.
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=768MiB", "GOMAXPROCS=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("[bundle-diff] preparation failed for %s/%s/%s: %v; full download remains available", target.Branch, target.RuntimeVersion, target.UpdateId, err)
	}
}

func readPatchInput(storage bucket.Bucket, target types.Update, name string, max int64) ([]byte, error) {
	f, err := storage.GetFile(target, name)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, os.ErrNotExist
	}
	defer f.Reader.Close()
	return bundlepatch.ReadBounded(f.Reader, max)
}

func patchIdentity(storage bucket.Bucket, target types.Update) (types.UpdateStoredMetadata, string, error) {
	var stored types.UpdateStoredMetadata
	if _, err := readPatchInput(storage, target, ".check", 1024); err != nil {
		return stored, "", err
	}
	rollback, err := storage.GetFile(target, "rollback")
	if err != nil {
		return stored, "", err
	}
	if rollback != nil {
		rollback.Reader.Close()
		return stored, "", errors.New("rollback has no launch bundle")
	}
	raw, err := readPatchInput(storage, target, "update-metadata.json", 64<<10)
	if err != nil {
		return stored, "", err
	}
	if err = json.Unmarshal(raw, &stored); err != nil {
		return stored, "", err
	}
	id, err := uuid.Parse(stored.UpdateUUID)
	if err != nil || (stored.Platform != "ios" && stored.Platform != "android") {
		return stored, "", errors.New("invalid stored identity")
	}
	stored.UpdateUUID = id.String()
	raw, err = readPatchInput(storage, target, "metadata.json", 4<<20)
	if err != nil {
		return stored, "", err
	}
	var metadata types.MetadataObject
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return stored, "", err
	}
	asset := metadata.FileMetadata.IOS.Bundle
	if stored.Platform == "android" {
		asset = metadata.FileMetadata.Android.Bundle
	}
	if asset == "" || filepath.IsAbs(asset) || strings.Contains(asset, "\\") || filepath.Clean(asset) != asset || asset == ".." || strings.HasPrefix(asset, "../") {
		return stored, "", errors.New("invalid bundle path")
	}
	return stored, asset, nil
}

// PreparePublishedBundlePatches is run by the isolated helper. Select at most
// three prior, committed deliveries from the target branch/runtime/platform.
// Unknown/embedded bases keep using full bundles unless explicitly prepared.
func PreparePublishedBundlePatches(target types.Update) error {
	storage := bucket.GetBucket()
	targetMeta, targetAsset, err := patchIdentity(storage, target)
	if err != nil {
		return err
	}
	candidates, err := storage.GetUpdates(target.Branch, target.RuntimeVersion)
	if err != nil {
		return err
	}
	targetBytes, err := readPatchInput(storage, target, targetAsset, bundlepatch.MaxBundleBytes)
	if err != nil {
		return err
	}
	attempted := 0
	for _, base := range sortUpdates(candidates) {
		if base.UpdateId == target.UpdateId || base.CreatedAt >= target.CreatedAt {
			continue
		}
		baseMeta, baseAsset, err := patchIdentity(storage, base)
		if err != nil || baseMeta.Platform != targetMeta.Platform || baseMeta.UpdateUUID == targetMeta.UpdateUUID {
			continue
		}
		attempted++
		if attempted > patchBaseLimit {
			break
		}
		key, _ := bundlepatch.Key(targetMeta.Platform, baseMeta.UpdateUUID)
		// Also validates copied patch records on republish: a different target UUID
		// must be regenerated, even if its bundle bytes happen to be identical.
		raw, err := readPatchInput(storage, target, key+".json", 4096)
		var prior bundlepatch.Record
		if err == nil && json.Unmarshal(raw, &prior) == nil && prior.Version == 1 && prior.TargetID == targetMeta.UpdateUUID && prior.BaseID == baseMeta.UpdateUUID && prior.Runtime == target.RuntimeVersion && prior.Platform == targetMeta.Platform && prior.Asset == targetAsset && prior.TargetHash == bundlepatch.Hash(targetBytes) && prior.PatchBytes > 0 && prior.PatchBytes <= bundlepatch.MaxPatchBytes {
			patch, err := readPatchInput(storage, target, key+".bsdiff", int64(prior.PatchBytes))
			if err == nil && len(patch) == prior.PatchBytes && bundlepatch.Hash(patch) == prior.PatchHash {
				continue
			}
		}
		baseBytes, err := readPatchInput(storage, base, baseAsset, bundlepatch.MaxBundleBytes)
		if err != nil {
			log.Printf("[bundle-diff] skipping base %s: %v", base.UpdateId, err)
			continue
		}
		record, patch, err := bundlepatch.Prepare(baseBytes, targetBytes, bundlepatch.Record{BaseID: baseMeta.UpdateUUID, TargetID: targetMeta.UpdateUUID, Runtime: target.RuntimeVersion, Platform: targetMeta.Platform, Asset: targetAsset})
		if err != nil {
			log.Printf("[bundle-diff] skipping base %s: %v", base.UpdateId, err)
			continue
		}
		// Publish the record last. Incomplete writes cannot advertise a valid patch.
		if err := storage.UploadFileIntoUpdate(target, key+".bsdiff", bytes.NewReader(patch)); err != nil {
			return err
		}
		raw, err = json.Marshal(record)
		if err != nil {
			return err
		}
		if err := storage.UploadFileIntoUpdate(target, key+".json", bytes.NewReader(raw)); err != nil {
			return err
		}
		log.Printf("[bundle-diff] ready branch=%s runtime=%s platform=%s base=%s target=%s gzip=%d patch=%d saved=%.1f%%", target.Branch, target.RuntimeVersion, targetMeta.Platform, record.BaseID, record.TargetID, record.GzipBytes, record.PatchBytes, 100*(1-float64(record.PatchBytes)/float64(record.GzipBytes)))
	}
	return nil
}

func ValidatePatchJobPath(branch, runtime, id string) error {
	for _, part := range []string{branch, runtime} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\") {
			return fmt.Errorf("invalid branch/runtime path")
		}
	}
	_, err := GetUpdate(branch, runtime, id)
	return err
}
