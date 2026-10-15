// Package bundlepatch prepares and serves immutable Expo launch-asset patches.
// Generation belongs in an offline publication job, never the download request.
package bundlepatch

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gabstv/go-bsdiff/pkg/bsdiff"
	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/google/uuid"
)

const MaxBundleBytes = 64 << 20
const MaxPatchBytes = 64 << 20

var ErrNotSmaller = errors.New("patch does not save at least 10% versus the gzip bundle")

type Record struct {
	Version    int    `json:"version"`
	BaseID     string `json:"baseId"`
	TargetID   string `json:"targetId"`
	Runtime    string `json:"runtimeVersion"`
	Platform   string `json:"platform"`
	Asset      string `json:"asset"`
	BaseHash   string `json:"baseSha256"`
	TargetHash string `json:"targetSha256"`
	PatchHash  string `json:"patchSha256"`
	FullBytes  int    `json:"fullBytes"`
	GzipBytes  int    `json:"gzipBytes"`
	PatchBytes int    `json:"patchBytes"`
}

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func Key(platform, baseID string) (string, error) {
	if platform != "ios" && platform != "android" {
		return "", errors.New("invalid platform")
	}
	id, err := uuid.Parse(baseID)
	if err != nil {
		return "", errors.New("invalid base update UUID")
	}
	return "bundle-patches/" + platform + "/" + id.String(), nil
}

// Prepare operates on exact uncompressed bundle bytes. Verify reconstruction
// before storing anything; compare against compressed full-download cost.
func Prepare(base, target []byte, record Record) (Record, []byte, error) {
	if len(base) == 0 || len(target) == 0 || len(base) > MaxBundleBytes || len(target) > MaxBundleBytes {
		return record, nil, errors.New("bundle size out of bounds")
	}
	if _, err := Key(record.Platform, record.BaseID); err != nil {
		return record, nil, err
	}
	baseID, _ := uuid.Parse(record.BaseID)
	targetID, err := uuid.Parse(record.TargetID)
	if err != nil || baseID == targetID || record.Runtime == "" || record.Asset == "" {
		return record, nil, errors.New("invalid bundle identity")
	}
	record.BaseID = baseID.String()
	record.TargetID = targetID.String()
	patch, err := bsdiff.Bytes(base, target)
	if err != nil {
		return record, nil, err
	}
	reconstructed, err := bspatch.Bytes(base, patch)
	if err != nil || !bytes.Equal(reconstructed, target) {
		return record, nil, errors.New("patch reconstruction failed")
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err = zw.Write(target); err != nil {
		return record, nil, err
	}
	if err = zw.Close(); err != nil {
		return record, nil, err
	}
	if len(patch)*10 >= compressed.Len()*9 {
		return record, nil, ErrNotSmaller
	}
	record.Version = 1
	record.BaseHash = Hash(base)
	record.TargetHash = Hash(target)
	record.PatchHash = Hash(patch)
	record.FullBytes = len(target)
	record.GzipBytes = compressed.Len()
	record.PatchBytes = len(patch)
	return record, patch, nil
}

func ReadBounded(reader io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return data, nil
}

func AcceptsPatch(r *http.Request) bool {
	for _, value := range strings.Split(r.Header.Get("A-IM"), ",") {
		// A-IM is a weighted negotiation header; only accept the exact supported token.
		parts := strings.Split(strings.TrimSpace(value), ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "bsdiff") {
			continue
		}
		if len(parts) == 1 {
			return true
		}
	}
	return false
}

// Serve keeps responses private: the same URL has a different representation
// for each installed base, and must never poison the full-bundle CDN cache.
func Serve(w http.ResponseWriter, record Record, patch []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(patch)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "A-IM, Expo-Current-Update-ID, Expo-Requested-Update-ID, Accept-Encoding")
	w.Header().Set("IM", "bsdiff")
	w.Header().Set("Expo-Base-Update-ID", record.BaseID)
	w.WriteHeader(http.StatusIMUsed)
	_, _ = w.Write(patch)
}
