// Local-only E2E harness: real manifest construction and production asset
// handler, with local feature flags and optional transport fault injection.
package main

import (
	"encoding/json"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/update"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type state struct {
	Target string `json:"target"`
	Reload bool   `json:"reloadEnabled"`
	Mode   string `json:"mode"`
}

func main() {
	root := flag.String("root", "", "Local fixture directory containing bucket/ and state.json")
	finalize := flag.String("finalize-update", "", "Finalize a local fixture through the publication hook before serving")
	flag.Parse()
	if *root == "" {
		log.Fatal("--root required")
	}
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("CACHE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(*root, "bucket"))
	os.Setenv("BASE_URL", "http://127.0.0.1:9090")
	os.Setenv("OTA_BUNDLE_DIFFING_ENABLED", "true")
	if *finalize != "" {
		if err := update.ValidatePatchJobPath("local-diff", "9090.1", *finalize); err != nil {
			log.Fatal(err)
		}
		target, err := update.GetUpdate("local-diff", "9090.1", *finalize)
		if err != nil {
			log.Fatal(err)
		}
		if err = update.MarkUpdateAsChecked(*target); err != nil {
			log.Fatal(err)
		}
		log.Printf("Local fixture finalized: %s", *finalize)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(filepath.Join(*root, "state.json"))
		var s state
		if err != nil || json.Unmarshal(raw, &s) != nil {
			http.Error(w, "invalid local state", 500)
			return
		}
		if r.URL.Path == "/assets" {
			if r.URL.Query().Get("branch") != "local-diff" {
				http.Error(w, "local branch only", 400)
				return
			}
			start := time.Now()
			rec := httptest.NewRecorder()
			if s.Mode == "full" {
				r.Header.Del("A-IM")
			}
			handlers.AssetsHandler(rec, r)
			if s.Mode == "corrupt" && rec.Code == 226 && rec.Body.Len() > 40 {
				rec.Body.Bytes()[40] ^= 255
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			n, _ := w.Write(rec.Body.Bytes())
			event := map[string]any{"event": "asset", "status": rec.Code, "bytes": n, "ms": float64(time.Since(start).Microseconds()) / 1000, "asset": r.URL.Query().Get("asset"), "base": r.Header.Get("Expo-Current-Update-ID"), "target": r.Header.Get("Expo-Requested-Update-ID"), "patchRequested": r.Header.Get("A-IM"), "encoding": rec.Header().Get("Content-Encoding")}
			data, _ := json.Marshal(event)
			fmt.Println(string(data))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/manifest" {
			w.Header().Set("expo-protocol-version", "1")
			w.Header().Set("expo-sfv-version", "0")
			if s.Target == "" {
				w.WriteHeader(204)
				return
			}
			platform := r.Header.Get("expo-platform")
			if platform != "ios" && platform != "android" {
				http.Error(w, "invalid platform", 400)
				return
			}
			runtime := r.Header.Get("expo-runtime-version")
			if runtime != "9090.1" {
				http.Error(w, "isolated test runtime required", 400)
				return
			}
			target, err := update.GetUpdate("local-diff", runtime, s.Target)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			metadata, err := update.GetMetadata(*target)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			manifest, err := update.ComposeUpdateManifest(&metadata, *target, platform)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if manifest.Id == r.Header.Get("expo-current-update-id") {
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/flags") || strings.HasPrefix(r.URL.Path, "/decide") {
			log.Printf("local flags path=%s reload=%t", r.URL.Path, s.Reload)
			json.NewEncoder(w).Encode(map[string]any{"featureFlags": map[string]any{"ota-updates-enabled": true, "ota-reload-enabled": s.Reload, "onboarding-refresh": false}, "featureFlagPayloads": map[string]string{"ota-updates-enabled": "{\"forceReload\":false,\"checkOnForeground\":true}"}, "errorsWhileComputingFlags": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": 1, "hasFeatureFlags": true, "surveys": []any{}})
	})
	log.Println("Local bundle diff harness at http://127.0.0.1:9090")
	log.Fatal(http.ListenAndServe("127.0.0.1:9090", mux))
}
