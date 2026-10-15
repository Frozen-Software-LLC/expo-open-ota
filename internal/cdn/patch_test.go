package cdn

import (
	"context"
	"expo-open-ota/internal/bundlepatch"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPatchReadiness(t *testing.T) {
	record := bundlepatch.Record{BaseID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", PatchHash: strings.Repeat("a", 64), PatchBytes: 123}
	for _, mode := range []string{"ready", "ordinary-cdn", "wrong-base", "wrong-hash", "wrong-size", "redirect", "missing", "encoded", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			edge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodHead {
					t.Error("probe downloaded a body")
				}
				w.Header().Set("Content-Length", fmt.Sprint(record.PatchBytes))
				w.Header().Set("IM", "bsdiff")
				w.Header().Set("Expo-Base-Update-ID", record.BaseID)
				w.Header().Set("X-OTA-Patch-SHA256", record.PatchHash)
				switch mode {
				case "ordinary-cdn":
					w.Header().Del("IM")
				case "wrong-base":
					w.Header().Set("Expo-Base-Update-ID", "wrong")
				case "wrong-hash":
					w.Header().Set("X-OTA-Patch-SHA256", "wrong")
				case "wrong-size":
					w.Header().Set("Content-Length", "122")
				case "redirect":
					w.Header().Set("Location", "/login")
					w.WriteHeader(302)
				case "missing":
					w.WriteHeader(404)
				case "encoded":
					w.Header().Set("Content-Encoding", "gzip")
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer edge.Close()
			oldClient := patchClient
			patchClient = &http.Client{Transport: edge.Client().Transport, Timeout: 100 * time.Millisecond, CheckRedirect: oldClient.CheckRedirect}
			t.Cleanup(func() { patchClient = oldClient })
			require.Equal(t, mode == "ready", PatchReady(context.Background(), edge.URL+"/patch", record))
			require.Equal(t, mode == "ready", PatchReady(context.Background(), edge.URL+"/patch", record))
			require.EqualValues(t, 1, calls.Load(), "cache must suppress repeated positive and negative probes")
			if mode == "ready" {
				changed := record
				changed.PatchBytes++
				require.False(t, PatchReady(context.Background(), edge.URL+"/patch", changed))
			}
		})
	}
	for _, location := range []string{"http://cdn.example/patch", "https://user:password@cdn.example/patch", ":bad"} {
		require.False(t, PatchReady(context.Background(), location, record))
	}
}
