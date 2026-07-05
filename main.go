package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-health" {
		doHealthCheck()
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Curator")

	dataDir := os.Getenv("CURATOR_DATA_DIR")
	if dataDir == "" {
		dataDir = "/app/data"
	}

	downloadDir := os.Getenv("CURATOR_DOWNLOAD_DIR")
	if downloadDir == "" {
		downloadDir = "/app/downloads"
	}

	envPort := 0
	if portStr := os.Getenv("CURATOR_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			envPort = p
		}
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory %s: %v", dataDir, err)
	}
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		log.Fatalf("Failed to create download directory %s: %v", downloadDir, err)
	}

	configPath := filepath.Join(dataDir, "config.json")
	dataPath := filepath.Join(dataDir, "data.json")

	config, err := LoadConfig(configPath)
	if err != nil {
		log.Printf("Failed to load config (%v), using defaults", err)
		config = DefaultConfig()
		config.DownloadDir = downloadDir
		if envPort > 0 {
			config.APIPort = envPort
		}
		if saveErr := config.Save(configPath); saveErr != nil {
			log.Printf("Failed to save default config: %v", saveErr)
		}
	} else {
		// Apply env var overrides for paths not yet set in config
		if config.DownloadDir == "" {
			config.DownloadDir = downloadDir
		}
		if envPort > 0 && config.APIPort == 0 {
			config.APIPort = envPort
		}
	}

	applyConfigDefaults(config)
	log.Printf("Config loaded: port=%d download_dir=%s", config.APIPort, config.DownloadDir)

	storage, err := NewStorage(dataPath)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Println("Storage initialized")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	refreshCh := make(chan struct{}, 1)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		RunScheduler(ctx, config, storage, refreshCh)
	}()
	log.Println("Scheduler started")

	wg.Add(1)
	go func() {
		defer wg.Done()
		StartAPIServer(ctx, config, storage, configPath, refreshCh)
	}()
	log.Printf("API server starting on port %d", config.APIPort)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutdown signal received, stopping gracefully...")

	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("All goroutines stopped")
	case <-time.After(30 * time.Second):
		log.Println("Shutdown timeout, forcing exit")
	}

	log.Println("Curator stopped")
}

// doHealthCheck makes a single GET /api/status request and exits 0/1.
// Used by the Docker HEALTHCHECK so no shell utilities are needed in scratch.
func doHealthCheck() {
	port := 8080
	if p, err := strconv.Atoi(os.Getenv("CURATOR_PORT")); err == nil && p > 0 {
		port = p
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/status", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "health: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
