package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Run starts the video automation service and handles its command-line modes.
func Run() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mode := ""
	if len(os.Args) > 1 {
		if len(os.Args) != 2 || (os.Args[1] != "recovery-code" && os.Args[1] != "storage-path") {
			fmt.Fprintln(os.Stderr, "usage: video-automation [recovery-code|storage-path]")
			os.Exit(2)
		}
		mode = os.Args[1]
	}
	storage, err := resolveRuntimeStorage(os.Getenv, os.ReadFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "storage initialization refused:", err)
		os.Exit(1)
	}
	dataDir := storage.dataDir
	if mode == "storage-path" {
		absolute, err := filepath.Abs(dataDir)
		if err == nil {
			absolute, err = resolveExistingAncestor(absolute)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "unable to resolve storage path: check DATA_DIR directory access")
			os.Exit(1)
		}
		fmt.Println(absolute)
		return
	}
	logger.Info("storage directory validated", "data_directory", dataDir, "railway_persistent_volume_checked", storage.railway)
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
	if mode == "recovery-code" {
		code, err := generateRecoveryCode()
		if err != nil || store.CreateRecoveryCode("sujanshrestha", recoveryCodeHash(code), time.Now().Add(recoveryCodeLifetime).Unix()) != nil {
			fmt.Fprintln(os.Stderr, "unable to generate recovery code")
			os.Exit(1)
		}
		fmt.Printf("One-time recovery code: %s\nExpires in 15 minutes. Generating another code invalidates this one.\n", code)
		return
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
