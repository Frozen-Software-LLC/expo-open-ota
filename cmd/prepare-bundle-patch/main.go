// Prepare a patch offline and write it beneath a target update directory.
// This command neither publishes an update nor changes a channel head.
package main

import (
	"encoding/json"
	"expo-open-ota/internal/bundlepatch"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func read(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	b, err := bundlepatch.ReadBounded(f, bundlepatch.MaxBundleBytes)
	if err != nil {
		log.Fatal(err)
	}
	return b
}
func main() {
	var record bundlepatch.Record
	base := flag.String("base-bundle", "", "Exact base bundle file (including an embedded build bundle)")
	target := flag.String("target-bundle", "", "Exact target bundle file")
	out := flag.String("output-dir", "", "Target update directory; must already exist")
	flag.StringVar(&record.BaseID, "base-id", "", "Base manifest UUID")
	flag.StringVar(&record.TargetID, "target-id", "", "Target manifest UUID")
	flag.StringVar(&record.Runtime, "runtime", "", "Shared runtime version")
	flag.StringVar(&record.Platform, "platform", "", "ios or android")
	flag.StringVar(&record.Asset, "asset", "", "Target launch asset path relative to its update")
	flag.Parse()
	if *base == "" || *target == "" || *out == "" {
		log.Fatal("base-bundle, target-bundle and output-dir are required")
	}
	if info, err := os.Stat(*out); err != nil || !info.IsDir() {
		log.Fatal("output-dir must be an existing target update directory")
	}
	record, patch, err := bundlepatch.Prepare(read(*base), read(*target), record)
	if err != nil {
		log.Fatal(err)
	}
	key, _ := bundlepatch.Key(record.Platform, record.BaseID)
	prefix := filepath.Join(*out, key)
	if err = os.MkdirAll(filepath.Dir(prefix), 0755); err != nil {
		log.Fatal(err)
	}
	// Write the record last: an incomplete preparation cannot advertise a patch.
	if err = os.WriteFile(prefix+".bsdiff", patch, 0644); err != nil {
		log.Fatal(err)
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile(prefix+".json", encoded, 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
