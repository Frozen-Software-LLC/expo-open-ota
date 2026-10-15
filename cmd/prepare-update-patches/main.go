// This helper inherits the server's storage environment and runs in a bounded
// child process. It never finalizes updates or changes a channel head.
package main

import (
	"expo-open-ota/internal/update"
	"log"
	"os"
)

func main() {
	if err := update.RunPatchPreparationCommand(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
