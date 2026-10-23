package main

import (
	"expo-open-ota/config"
	"expo-open-ota/internal/metrics"
	"expo-open-ota/internal/migration"
	infrastructure "expo-open-ota/internal/router"
	"expo-open-ota/internal/update"
	"github.com/gorilla/handlers"
	"log"
	"net/http"
	"os"
	"time"
)

import (
	_ "expo-open-ota/internal/migrations"
)

func init() {
	config.LoadConfig()
	metrics.InitMetrics()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--prepare-update-patches" {
		if err := update.RunPatchPreparationCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	migration.RunMigrationsWithLock()
	router := infrastructure.HoldHealthCheck(update.StartBootWarmUp(20*time.Second), infrastructure.NewRouter())
	log.Println("Server is running on port " + config.GetPort())
	corsOptions := handlers.CORS(
		handlers.AllowedHeaders([]string{"Authorization", "Content-Type"}),
		handlers.AllowedMethods([]string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}),
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowCredentials(),
	)
	err := http.ListenAndServe("0.0.0.0:"+config.GetPort(), corsOptions(router))
	if err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
