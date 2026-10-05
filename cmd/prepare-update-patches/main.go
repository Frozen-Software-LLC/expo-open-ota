// This helper inherits the server's storage environment and runs in a bounded
// child process. It never finalizes updates or changes a channel head.
package main

import (
	"expo-open-ota/internal/update"
	"flag"
	"log"
)

func main() {
	branch := flag.String("branch", "", "Published target branch")
	runtime := flag.String("runtime", "", "Published runtime")
	id := flag.String("update-id", "", "Published storage update ID")
	flag.Parse()
	if err := update.ValidatePatchJobPath(*branch, *runtime, *id); err != nil {
		log.Fatal(err)
	}
	target, err := update.GetUpdate(*branch, *runtime, *id)
	if err != nil {
		log.Fatal(err)
	}
	if err = update.PreparePublishedBundlePatches(*target); err != nil {
		log.Fatal(err)
	}
}
