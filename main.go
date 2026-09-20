package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}
	store, err := OpenStore(dataDir)
	if err != nil {
		logger.Error("database initialization failed")
		os.Exit(1)
	}
	defer store.Close()
	_ = os.Chmod(filepath.Join(dataDir, "app.db"), 0o600)
	initialHash, err := hashPassword("Sujan@123")
	if err != nil || store.EnsureUser("sujanshrestha", initialHash) != nil {
		logger.Error("initial user setup failed")
		os.Exit(1)
	}
	security, err := NewSecurity(store)
	if err != nil {
		logger.Error("security initialization failed")
		os.Exit(1)
	}
	if apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); apiKey != "" {
		if _, err := store.Setting(apiKeySetting); err != nil {
			encrypted, encryptErr := security.Encrypt(apiKey)
			if encryptErr != nil || store.SetSetting(apiKeySetting, encrypted) != nil {
				logger.Error("API key bootstrap failed")
				os.Exit(1)
			}
		}
	}

	server := &http.Server{
		Addr:              "0.0.0.0:8080",
		Handler:           NewDashboardHandler(store, security, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go NewProcessor(store, security, logger).Run(workerContext)

	go func() {
		logger.Info("server started", "address", "http://0.0.0.0:8080")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(stop); err != nil {
		logger.Error("server shutdown failed", "error", err)
	}
}
